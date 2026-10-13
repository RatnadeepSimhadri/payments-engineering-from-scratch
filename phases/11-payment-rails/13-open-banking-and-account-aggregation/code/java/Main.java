// Lesson: phases/11-payment-rails/13-open-banking-and-account-aggregation/docs/en.md
// Open Banking: OAuth Consent, Account Aggregation, and Data Access
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 6749 (OAuth 2.0), RFC 7636 (PKCE), RFC 6750 (Bearer tokens), FDX API v6 and UK Open Banking Read/Write
//          API v3.1 (payload shapes). Java 21, standard library only. No network: the bank, its auth server, and the
//          aggregator are in-memory objects sharing an injected clock. Money is integer minor units (Phase 10, Lesson 01).

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.TreeMap;

// ---------- Money (subset of Phase 10, Lesson 01) ----------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency GBP = new Currency("GBP", 2);
    static final Currency EUR = new Currency("EUR", 2);

    static Currency of(String code) {
        return switch (code) { case "USD" -> USD; case "GBP" -> GBP; case "EUR" -> EUR; default -> new Currency(code, 2); };
    }

    long minorUnitsPerMajor() { long s = 1; for (int i = 0; i < exponent; i++) s *= 10; return s; }
}

record Money(long minorUnits, Currency currency) {
    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) throw new NumberFormatException(text);
        if (frac.length() > currency.exponent()) throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }

    Money plus(Money o) {
        if (!currency.equals(o.currency)) throw new IllegalArgumentException("currency mismatch");
        return new Money(Math.addExact(minorUnits, o.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor(), mag = Math.abs(minorUnits);
        String frac = Long.toString(mag % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (mag / scale) + "." + frac + " " + currency.code();
    }
}

// ---------- Clock ----------

interface Clock { long nowEpochSeconds(); }

final class ManualClock implements Clock {
    private long now;
    ManualClock(long epochSeconds) { this.now = epochSeconds; }
    @Override public long nowEpochSeconds() { return now; }
    void advanceSeconds(long seconds) { now += seconds; }
    void advanceDays(long days) { now += days * 86_400L; }
}

// ---------- OAuth 2.0 primitives ----------

/** Scopes as the bank publishes them. The string form is what goes on the wire (RFC 6749 section 3.3). */
enum Scope {
    ACCOUNTS_READ("accounts:read"), BALANCES_READ("balances:read"), TRANSACTIONS_READ("transactions:read"), PAYMENTS_WRITE("payments:write");
    final String wire;
    Scope(String wire) { this.wire = wire; }
    static Scope fromWire(String s) {
        for (Scope v : values()) if (v.wire.equals(s)) return v;
        throw new OAuthException("invalid_scope", "unknown scope " + s);
    }
}

/** An OAuth error as RFC 6749 section 5.2 defines it: a short code plus a human description. */
final class OAuthException extends RuntimeException {
    final String error;
    OAuthException(String error, String description) { super(error + ": " + description); this.error = error; }
}

/** RFC 7636: Proof Key for Code Exchange. */
final class Pkce {
    private static final String UNRESERVED = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~";

    /** A 64-character verifier from the unreserved set (RFC 7636 section 4.1 requires 43..128). */
    static String generateVerifier(Random random) {
        StringBuilder sb = new StringBuilder(64);
        for (int i = 0; i < 64; i++) sb.append(UNRESERVED.charAt(random.nextInt(UNRESERVED.length())));
        return sb.toString();
    }

    /** code_challenge = BASE64URL-ENCODE(SHA256(ASCII(code_verifier))), no padding (section 4.2). */
    static String challengeS256(String verifier) {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(StandardCharsets.US_ASCII));
            return Base64.getUrlEncoder().withoutPadding().encodeToString(digest);
        } catch (NoSuchAlgorithmException e) { throw new IllegalStateException(e); }
    }

    static boolean verify(String verifier, String challenge) {
        if (verifier == null || verifier.length() < 43 || verifier.length() > 128) return false;
        return MessageDigest.isEqual(challengeS256(verifier).getBytes(StandardCharsets.US_ASCII), challenge.getBytes(StandardCharsets.US_ASCII));
    }
}

record RegisteredClient(String clientId, String redirectUri) {}

/** What the user agreed to, with the bank's consent lifetime. UK Open Banking: 90 days before re-authentication. */
record Consent(String consentId, String userId, String clientId, Set<Scope> scopes, long grantedAt, long expiresAt) {
    boolean isExpired(long now) { return now >= expiresAt; }
}

/** The authorization code: single use, short lived, bound to the client, the redirect URI, and the PKCE challenge. */
record AuthorizationCode(String code, String consentId, String clientId, String redirectUri, String codeChallenge, long expiresAt) {}

record TokenResponse(String accessToken, String refreshToken, long expiresIn, Set<Scope> scopes) {}

/** Server-side record of an access token: who, what scopes, until when. */
record AccessGrant(String consentId, Set<Scope> scopes, long expiresAt) {}

/**
 * The bank's authorization server. Implements the authorization-code grant with PKCE, refresh with rotation,
 * and replay detection. Everything is keyed in TreeMaps so iteration is deterministic in tests.
 */
final class AuthorizationServer {
    static final long CODE_TTL_SECONDS = 600;        // RFC 6749 section 4.1.2: a maximum of 10 minutes is recommended
    static final long ACCESS_TTL_SECONDS = 300;
    static final long CONSENT_TTL_SECONDS = 90L * 86_400L;

    private final Clock clock;
    private final Random random;
    private final Map<String, RegisteredClient> clients = new TreeMap<>();
    private final Map<String, Consent> consents = new TreeMap<>();
    private final Map<String, AuthorizationCode> codes = new TreeMap<>();
    private final Map<String, AccessGrant> accessTokens = new TreeMap<>();
    private final Map<String, String> refreshTokens = new TreeMap<>();      // refresh token -> consentId
    private final Map<String, String> retiredRefreshTokens = new TreeMap<>(); // rotated-out token -> consentId (replay detection)
    private int sequence = 0;

    AuthorizationServer(Clock clock, Random random) { this.clock = clock; this.random = random; }

    void registerClient(RegisteredClient client) { clients.put(client.clientId(), client); }

    private String newToken(String prefix) {
        byte[] bytes = new byte[18];
        random.nextBytes(bytes);
        return prefix + "_" + (++sequence) + "_" + Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
    }

    /**
     * The /authorize endpoint after the user has authenticated and clicked "allow" on the consent screen.
     * Returns the authorization code the bank would send back on the redirect URI together with `state`.
     */
    String authorize(String clientId, String redirectUri, Set<Scope> requestedScopes, String codeChallenge, String codeChallengeMethod, String userId) {
        RegisteredClient client = clients.get(clientId);
        if (client == null) throw new OAuthException("invalid_client", "unknown client " + clientId);
        if (!client.redirectUri().equals(redirectUri)) throw new OAuthException("invalid_request", "redirect_uri does not match registration");
        if (!"S256".equals(codeChallengeMethod)) throw new OAuthException("invalid_request", "code_challenge_method must be S256");
        if (codeChallenge == null || codeChallenge.length() != 43) throw new OAuthException("invalid_request", "code_challenge must be a 43-char base64url SHA-256");
        if (requestedScopes.isEmpty()) throw new OAuthException("invalid_scope", "at least one scope is required");
        if (requestedScopes.contains(Scope.PAYMENTS_WRITE)) throw new OAuthException("invalid_scope", "this client is not registered for payments:write");

        long now = clock.nowEpochSeconds();
        String consentId = "consent_" + (++sequence);
        consents.put(consentId, new Consent(consentId, userId, clientId, EnumSet.copyOf(requestedScopes), now, now + CONSENT_TTL_SECONDS));
        String code = newToken("code");
        codes.put(code, new AuthorizationCode(code, consentId, clientId, redirectUri, codeChallenge, now + CODE_TTL_SECONDS));
        return code;
    }

    /** The /token endpoint, grant_type=authorization_code. */
    TokenResponse exchangeCode(String clientId, String code, String redirectUri, String codeVerifier) {
        AuthorizationCode stored = codes.remove(code);                       // single use, whether or not the rest succeeds
        if (stored == null) throw new OAuthException("invalid_grant", "unknown or already used authorization code");
        if (!stored.clientId().equals(clientId)) throw new OAuthException("invalid_grant", "code was issued to a different client");
        if (!stored.redirectUri().equals(redirectUri)) throw new OAuthException("invalid_grant", "redirect_uri does not match the authorization request");
        if (clock.nowEpochSeconds() >= stored.expiresAt()) throw new OAuthException("invalid_grant", "authorization code expired");
        if (!Pkce.verify(codeVerifier, stored.codeChallenge())) throw new OAuthException("invalid_grant", "PKCE verification failed");
        return issueTokens(consents.get(stored.consentId()));
    }

    /** The /token endpoint, grant_type=refresh_token, with rotation and replay detection. */
    TokenResponse refresh(String clientId, String refreshToken) {
        String consentId = refreshTokens.remove(refreshToken);
        if (consentId == null) {
            String replayedFamily = retiredRefreshTokens.get(refreshToken);
            if (replayedFamily != null) {
                revokeConsent(replayedFamily);                                  // a replayed token means it leaked: kill the family
                throw new OAuthException("invalid_grant", "refresh token replay detected; consent revoked");
            }
            throw new OAuthException("invalid_grant", "unknown refresh token");
        }
        Consent consent = consents.get(consentId);
        if (consent == null) throw new OAuthException("invalid_grant", "consent revoked");
        if (!consent.clientId().equals(clientId)) throw new OAuthException("invalid_grant", "refresh token belongs to another client");
        if (consent.isExpired(clock.nowEpochSeconds())) throw new OAuthException("invalid_grant", "consent expired; user must re-authorize");
        retiredRefreshTokens.put(refreshToken, consentId);
        return issueTokens(consent);
    }

    private TokenResponse issueTokens(Consent consent) {
        long now = clock.nowEpochSeconds();
        String access = newToken("at");
        String refresh = newToken("rt");
        accessTokens.put(access, new AccessGrant(consent.consentId(), consent.scopes(), now + ACCESS_TTL_SECONDS));
        refreshTokens.put(refresh, consent.consentId());
        return new TokenResponse(access, refresh, ACCESS_TTL_SECONDS, consent.scopes());
    }

    /** Token introspection used by the resource server (RFC 7662 in spirit): null if unknown, expired, or revoked. */
    AccessGrant introspect(String accessToken) {
        AccessGrant grant = accessTokens.get(accessToken);
        if (grant == null) return null;
        if (clock.nowEpochSeconds() >= grant.expiresAt()) return null;
        if (!consents.containsKey(grant.consentId())) return null;
        return grant;
    }

    void revokeConsent(String consentId) {
        consents.remove(consentId);
        accessTokens.values().removeIf(g -> g.consentId().equals(consentId));
        refreshTokens.values().removeIf(c -> c.equals(consentId));
    }

    Consent consent(String consentId) { return consents.get(consentId); }
}

// ---------- Resource servers: two banks with different payload shapes ----------

/** An HTTP-like response without HTTP: status, an optional WWW-Authenticate error, and a JSON-like body. */
record ApiResponse(int status, String authError, Map<String, Object> body) {
    static ApiResponse ok(Map<String, Object> body) { return new ApiResponse(200, null, body); }
    static ApiResponse unauthorized() { return new ApiResponse(401, "invalid_token", Map.of()); }
    static ApiResponse forbidden() { return new ApiResponse(403, "insufficient_scope", Map.of()); }
}

/** A bank's data API. `cursor` is opaque to the caller; each bank encodes paging its own way. */
interface BankApi {
    String bankId();
    ApiResponse accounts(String accessToken, String cursor);
    ApiResponse transactions(String accessToken, String accountId, String cursor);
}

/** Common token checks every resource server performs (RFC 6750 section 3.1 error codes). */
abstract class AbstractBank implements BankApi {
    protected final AuthorizationServer auth;
    protected final int pageSize;
    AbstractBank(AuthorizationServer auth, int pageSize) { this.auth = auth; this.pageSize = pageSize; }

    protected ApiResponse requireScope(String accessToken, Scope scope) {
        AccessGrant grant = auth.introspect(accessToken);
        if (grant == null) return ApiResponse.unauthorized();
        if (!grant.scopes().contains(scope)) return ApiResponse.forbidden();
        return null;
    }

    protected static <T> List<T> page(List<T> all, int offset, int size) {
        return all.subList(Math.min(offset, all.size()), Math.min(offset + size, all.size()));
    }
}

/** Bank A speaks an FDX-style dialect: camelCase, accounts wrapped by type, signed decimal amounts, offset paging. */
final class FdxStyleBank extends AbstractBank {
    private final List<Map<String, Object>> accounts = new ArrayList<>();
    private final Map<String, List<Map<String, Object>>> transactions = new TreeMap<>();

    FdxStyleBank(AuthorizationServer auth, int pageSize) { super(auth, pageSize); }

    @Override public String bankId() { return "bank-a-fdx"; }

    void addAccount(String accountId, String nickname, String currency, String accountType, String maskedNumber) {
        accounts.add(Map.of("depositAccount", Map.of("accountId", accountId, "nickname", nickname, "currency", currency,
                "accountType", accountType, "accountNumberDisplay", maskedNumber)));
        transactions.put(accountId, new ArrayList<>());
    }

    /** FDX carries a signed amount as a JSON number and a debitCreditMemo; we keep the digits as a string so no float exists. */
    void addTransaction(String accountId, String txId, String postedDate, String signedAmount, String description, String status) {
        transactions.get(accountId).add(Map.of("depositTransaction", Map.of("transactionId", txId, "postedTimestamp", postedDate + "T00:00:00Z",
                "amount", signedAmount, "debitCreditMemo", signedAmount.startsWith("-") ? "DEBIT" : "CREDIT",
                "description", description, "status", status)));
    }

    @Override public ApiResponse accounts(String token, String cursor) {
        ApiResponse err = requireScope(token, Scope.ACCOUNTS_READ);
        if (err != null) return err;
        return pageOf("accounts", accounts, cursor);
    }

    @Override public ApiResponse transactions(String token, String accountId, String cursor) {
        ApiResponse err = requireScope(token, Scope.TRANSACTIONS_READ);
        if (err != null) return err;
        return pageOf("transactions", transactions.getOrDefault(accountId, List.of()), cursor);
    }

    private ApiResponse pageOf(String key, List<Map<String, Object>> all, String cursor) {
        int offset = cursor == null ? 0 : Integer.parseInt(cursor);
        List<Map<String, Object>> items = page(all, offset, pageSize);
        Map<String, Object> body = new LinkedHashMap<>();
        body.put(key, items);
        Map<String, Object> pageInfo = new LinkedHashMap<>();
        if (offset + pageSize < all.size()) pageInfo.put("nextOffset", Integer.toString(offset + pageSize));
        body.put("page", pageInfo);
        return ApiResponse.ok(body);
    }
}

/** Bank B speaks a UK Open Banking-style dialect: PascalCase under Data, amount object with Currency, Links.Next paging. */
final class UkObStyleBank extends AbstractBank {
    private final List<Map<String, Object>> accounts = new ArrayList<>();
    private final Map<String, List<Map<String, Object>>> transactions = new TreeMap<>();

    UkObStyleBank(AuthorizationServer auth, int pageSize) { super(auth, pageSize); }

    @Override public String bankId() { return "bank-b-ukob"; }

    void addAccount(String accountId, String nickname, String currency, String subType, String sortCodeAccountNumber) {
        accounts.add(Map.of("AccountId", accountId, "Nickname", nickname, "Currency", currency, "AccountSubType", subType,
                "Account", List.of(Map.of("SchemeName", "UK.OBIE.SortCodeAccountNumber", "Identification", sortCodeAccountNumber))));
        transactions.put(accountId, new ArrayList<>());
    }

    void addTransaction(String accountId, String txId, String bookingDate, String unsignedAmount, String creditDebit, String info, String status) {
        transactions.get(accountId).add(Map.of("TransactionId", txId, "BookingDateTime", bookingDate + "T09:30:00+00:00",
                "Amount", Map.of("Amount", unsignedAmount, "Currency", "GBP"), "CreditDebitIndicator", creditDebit,
                "TransactionInformation", info, "Status", status));
    }

    @Override public ApiResponse accounts(String token, String cursor) {
        ApiResponse err = requireScope(token, Scope.ACCOUNTS_READ);
        if (err != null) return err;
        return pageOf("Account", "/accounts", accounts, cursor);
    }

    @Override public ApiResponse transactions(String token, String accountId, String cursor) {
        ApiResponse err = requireScope(token, Scope.TRANSACTIONS_READ);
        if (err != null) return err;
        return pageOf("Transaction", "/accounts/" + accountId + "/transactions", transactions.getOrDefault(accountId, List.of()), cursor);
    }

    private ApiResponse pageOf(String key, String path, List<Map<String, Object>> all, String cursor) {
        int pageNo = cursor == null ? 1 : Integer.parseInt(cursor.substring(cursor.indexOf("page=") + 5));
        int offset = (pageNo - 1) * pageSize;
        Map<String, Object> links = new LinkedHashMap<>();
        links.put("Self", path + "?page=" + pageNo);
        if (offset + pageSize < all.size()) links.put("Next", path + "?page=" + (pageNo + 1));
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("Data", Map.of(key, page(all, offset, pageSize)));
        body.put("Links", links);
        return ApiResponse.ok(body);
    }
}

// ---------- The aggregator's unified schema and per-bank adapters ----------

enum AccountType { CHECKING, SAVINGS, CREDIT_CARD, OTHER }

record Account(String bankId, String accountId, String displayName, Currency currency, AccountType type, String maskedIdentifier) {
    @Override public String toString() {
        return String.format("%-12s %-10s %-18s %s %-9s %s", bankId, accountId, displayName, currency.code(), type, maskedIdentifier);
    }
}

/** Signed amount: negative is money leaving the account. One sign convention for every bank. */
record Transaction(String bankId, String accountId, String transactionId, String bookedDate, Money amount, String description, boolean pending) {
    @Override public String toString() {
        return String.format("%-12s %-10s %-5s %s %13s  %-18s%s", bankId, accountId, transactionId, bookedDate, amount, description, pending ? "  (pending)" : "");
    }
}

record Page<T>(List<T> items, String nextCursor) {}

/** Translates one bank's dialect into the unified schema and knows how that bank pages. */
interface BankAdapter {
    Page<Account> accounts(ApiResponse response, String bankId);
    Page<Transaction> transactions(ApiResponse response, String bankId, String accountId);
}

final class FdxAdapter implements BankAdapter {
    @SuppressWarnings("unchecked")
    @Override public Page<Account> accounts(ApiResponse r, String bankId) {
        List<Account> out = new ArrayList<>();
        for (Map<String, Object> wrapper : (List<Map<String, Object>>) r.body().get("accounts")) {
            Map<String, Object> a = (Map<String, Object>) wrapper.get("depositAccount");
            AccountType type = switch ((String) a.get("accountType")) { case "CHECKING" -> AccountType.CHECKING; case "SAVINGS" -> AccountType.SAVINGS; default -> AccountType.OTHER; };
            out.add(new Account(bankId, (String) a.get("accountId"), (String) a.get("nickname"), Currency.of((String) a.get("currency")), type, (String) a.get("accountNumberDisplay")));
        }
        return new Page<>(out, (String) ((Map<String, Object>) r.body().get("page")).get("nextOffset"));
    }

    @SuppressWarnings("unchecked")
    @Override public Page<Transaction> transactions(ApiResponse r, String bankId, String accountId) {
        List<Transaction> out = new ArrayList<>();
        for (Map<String, Object> wrapper : (List<Map<String, Object>>) r.body().get("transactions")) {
            Map<String, Object> t = (Map<String, Object>) wrapper.get("depositTransaction");
            Money amount = Money.parse((String) t.get("amount"), Currency.USD);     // FDX amounts are already signed
            boolean debit = "DEBIT".equals(t.get("debitCreditMemo"));
            if (debit != amount.minorUnits() < 0) throw new IllegalStateException("FDX sign and debitCreditMemo disagree on " + t.get("transactionId"));
            out.add(new Transaction(bankId, accountId, (String) t.get("transactionId"), ((String) t.get("postedTimestamp")).substring(0, 10),
                    amount, (String) t.get("description"), "PENDING".equals(t.get("status"))));
        }
        return new Page<>(out, (String) ((Map<String, Object>) r.body().get("page")).get("nextOffset"));
    }
}

final class UkObAdapter implements BankAdapter {
    @SuppressWarnings("unchecked")
    @Override public Page<Account> accounts(ApiResponse r, String bankId) {
        List<Account> out = new ArrayList<>();
        Map<String, Object> data = (Map<String, Object>) r.body().get("Data");
        for (Map<String, Object> a : (List<Map<String, Object>>) data.get("Account")) {
            AccountType type = switch ((String) a.get("AccountSubType")) { case "CurrentAccount" -> AccountType.CHECKING; case "Savings" -> AccountType.SAVINGS; case "CreditCard" -> AccountType.CREDIT_CARD; default -> AccountType.OTHER; };
            List<Map<String, Object>> ids = (List<Map<String, Object>>) a.get("Account");
            String ident = (String) ids.get(0).get("Identification");
            String masked = "****" + ident.substring(ident.length() - 4);
            out.add(new Account(bankId, (String) a.get("AccountId"), (String) a.get("Nickname"), Currency.of((String) a.get("Currency")), type, masked));
        }
        return new Page<>(out, (String) ((Map<String, Object>) r.body().get("Links")).get("Next"));
    }

    @SuppressWarnings("unchecked")
    @Override public Page<Transaction> transactions(ApiResponse r, String bankId, String accountId) {
        List<Transaction> out = new ArrayList<>();
        Map<String, Object> data = (Map<String, Object>) r.body().get("Data");
        for (Map<String, Object> t : (List<Map<String, Object>>) data.get("Transaction")) {
            Map<String, Object> amt = (Map<String, Object>) t.get("Amount");
            Money unsigned = Money.parse((String) amt.get("Amount"), Currency.of((String) amt.get("Currency")));
            Money signed = "Debit".equals(t.get("CreditDebitIndicator")) ? unsigned.negate() : unsigned;   // UK OB amounts are unsigned
            out.add(new Transaction(bankId, accountId, (String) t.get("TransactionId"), ((String) t.get("BookingDateTime")).substring(0, 10),
                    signed, (String) t.get("TransactionInformation"), "Pending".equals(t.get("Status"))));
        }
        return new Page<>(out, (String) ((Map<String, Object>) r.body().get("Links")).get("Next"));
    }
}

// ---------- The aggregator ----------

/** Thrown when a refresh fails: the only fix is to send the user back through consent. */
final class ReauthorizationRequiredException extends RuntimeException {
    ReauthorizationRequiredException(String message) { super(message); }
}

/** One user's link to one bank: the tokens and the consent they came from. Mutable because tokens rotate. */
final class Connection {
    final String connectionId;
    final String userId;
    final BankApi bank;
    final BankAdapter adapter;
    final AuthorizationServer auth;
    String accessToken;
    String refreshToken;
    int refreshCount = 0;

    Connection(String connectionId, String userId, BankApi bank, BankAdapter adapter, AuthorizationServer auth, TokenResponse tokens) {
        this.connectionId = connectionId; this.userId = userId; this.bank = bank; this.adapter = adapter; this.auth = auth;
        this.accessToken = tokens.accessToken(); this.refreshToken = tokens.refreshToken();
    }
}

/** The authorization request the aggregator builds, and the state it must remember until the redirect comes back. */
record PendingAuthorization(String state, String codeVerifier, String codeChallenge, Set<Scope> scopes) {}

final class Aggregator {
    static final String CLIENT_ID = "aggregator-app";
    static final String REDIRECT_URI = "https://aggregator.example/callback";

    private final Random random;
    private final Map<String, PendingAuthorization> pending = new HashMap<>();
    private final Map<String, Connection> connections = new TreeMap<>();
    private int sequence = 0;

    Aggregator(Random random) { this.random = random; }

    /** Step 1: build the /authorize request. The verifier never leaves this process; only its hash does. */
    PendingAuthorization beginAuthorization(Set<Scope> scopes) {
        byte[] stateBytes = new byte[12];
        random.nextBytes(stateBytes);
        String state = Base64.getUrlEncoder().withoutPadding().encodeToString(stateBytes);
        String verifier = Pkce.generateVerifier(random);
        PendingAuthorization p = new PendingAuthorization(state, verifier, Pkce.challengeS256(verifier), EnumSet.copyOf(scopes));
        pending.put(state, p);
        return p;
    }

    String authorizationUrl(String bankAuthorizeEndpoint, PendingAuthorization p) {
        StringBuilder scope = new StringBuilder();
        for (Scope s : p.scopes()) scope.append(scope.length() == 0 ? "" : " ").append(s.wire);
        return bankAuthorizeEndpoint + "?response_type=code&client_id=" + CLIENT_ID + "&redirect_uri=" + REDIRECT_URI
                + "&scope=" + scope.toString().replace(" ", "%20") + "&state=" + p.state()
                + "&code_challenge=" + p.codeChallenge() + "&code_challenge_method=S256";
    }

    /** Step 3: the redirect came back with code and state. Verify state (CSRF), exchange the code with the verifier. */
    Connection completeAuthorization(String userId, String returnedState, String code, AuthorizationServer auth, BankApi bank, BankAdapter adapter) {
        PendingAuthorization p = pending.remove(returnedState);
        if (p == null) throw new OAuthException("invalid_request", "state does not match any pending authorization (possible CSRF)");
        TokenResponse tokens = auth.exchangeCode(CLIENT_ID, code, REDIRECT_URI, p.codeVerifier());
        if (!tokens.scopes().containsAll(p.scopes())) throw new OAuthException("invalid_scope", "bank granted fewer scopes than requested");
        String id = "conn_" + (++sequence);
        Connection c = new Connection(id, userId, bank, adapter, auth, tokens);
        connections.put(id, c);
        return c;
    }

    Connection connection(String id) { return connections.get(id); }

    /** Fetch every page of accounts, refreshing the token once on 401. */
    List<Account> fetchAccounts(String connectionId) {
        Connection c = connections.get(connectionId);
        List<Account> all = new ArrayList<>();
        String cursor = null;
        do {
            final String cur = cursor;
            ApiResponse r = callWithRefresh(c, token -> c.bank.accounts(token, cur));
            Page<Account> page = c.adapter.accounts(r, c.bank.bankId());
            all.addAll(page.items());
            cursor = page.nextCursor();
        } while (cursor != null);
        return all;
    }

    List<Transaction> fetchTransactions(String connectionId, String accountId) {
        Connection c = connections.get(connectionId);
        List<Transaction> all = new ArrayList<>();
        String cursor = null;
        do {
            final String cur = cursor;
            ApiResponse r = callWithRefresh(c, token -> c.bank.transactions(token, accountId, cur));
            Page<Transaction> page = c.adapter.transactions(r, c.bank.bankId(), accountId);
            all.addAll(page.items());
            cursor = page.nextCursor();
        } while (cursor != null);
        return all;
    }

    interface Call { ApiResponse run(String accessToken); }

    /** 401 invalid_token -> refresh once and retry. 403 insufficient_scope -> never retry; the consent lacks the scope. */
    private ApiResponse callWithRefresh(Connection c, Call call) {
        ApiResponse r = call.run(c.accessToken);
        if (r.status() == 401) {
            try {
                TokenResponse fresh = c.auth.refresh(CLIENT_ID, c.refreshToken);
                c.accessToken = fresh.accessToken();
                c.refreshToken = fresh.refreshToken();          // rotation: store the new one, the old one is now dead
                c.refreshCount++;
            } catch (OAuthException e) {
                throw new ReauthorizationRequiredException(c.connectionId + ": " + e.getMessage());
            }
            r = call.run(c.accessToken);
        }
        if (r.status() == 403) throw new OAuthException("insufficient_scope", "consent does not include the scope this call needs");
        if (r.status() != 200) throw new IllegalStateException("unexpected status " + r.status());
        return r;
    }
}

public final class Main {
    static FdxStyleBank bankA(AuthorizationServer auth) {
        FdxStyleBank a = new FdxStyleBank(auth, 2);
        a.addAccount("acc-chk-1", "Everyday Checking", "USD", "CHECKING", "****4321");
        a.addAccount("acc-sav-1", "Rainy Day", "USD", "SAVINGS", "****8765");
        a.addAccount("acc-cd-1", "12mo CD", "USD", "CD", "****1111");
        a.addTransaction("acc-chk-1", "t-1", "2026-03-02", "-42.17", "GROCERY MART", "POSTED");
        a.addTransaction("acc-chk-1", "t-2", "2026-03-03", "2500.00", "PAYROLL ACME", "POSTED");
        a.addTransaction("acc-chk-1", "t-3", "2026-03-04", "-1200.00", "RENT", "POSTED");
        a.addTransaction("acc-chk-1", "t-4", "2026-03-05", "-9.99", "STREAMING", "PENDING");
        a.addTransaction("acc-chk-1", "t-5", "2026-03-05", "-61.40", "FUEL", "POSTED");
        return a;
    }

    static UkObStyleBank bankB(AuthorizationServer auth) {
        UkObStyleBank b = new UkObStyleBank(auth, 2);
        b.addAccount("22289", "Main Current", "GBP", "CurrentAccount", "80200110203345");
        b.addAccount("31820", "Holiday Savings", "GBP", "Savings", "80200110203346");
        b.addTransaction("22289", "123", "2026-03-02", "10.00", "Credit", "Cash from Aubrey", "Booked");
        b.addTransaction("22289", "124", "2026-03-03", "36.50", "Debit", "TFL TRAVEL", "Booked");
        b.addTransaction("22289", "125", "2026-03-04", "1800.00", "Credit", "SALARY", "Booked");
        b.addTransaction("22289", "126", "2026-03-05", "4.20", "Debit", "COFFEE", "Pending");
        return b;
    }

    static Connection connect(Aggregator agg, AuthorizationServer auth, BankApi bank, BankAdapter adapter, String user, Set<Scope> scopes, boolean verbose) {
        PendingAuthorization p = agg.beginAuthorization(scopes);
        String url = agg.authorizationUrl("https://" + bank.bankId() + ".example/authorize", p);
        if (verbose) {
            System.out.println("1. aggregator redirects user to:\n   " + url);
            System.out.println("   verifier stays server-side: " + p.codeVerifier().substring(0, 12) + "... (" + p.codeVerifier().length() + " chars)");
        }
        String code = auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, scopes, p.codeChallenge(), "S256", user);
        if (verbose) System.out.println("2. user authenticates at the bank, approves scopes; bank redirects back with code=" + code.substring(0, 12) + "... state=" + p.state());
        Connection c = agg.completeAuthorization(user, p.state(), code, auth, bank, adapter);
        if (verbose) System.out.println("3. aggregator exchanges code + verifier for tokens: access=" + c.accessToken.substring(0, 10) + "... refresh=" + c.refreshToken.substring(0, 10) + "...");
        return c;
    }

    public static void main(String[] args) {
        ManualClock clock = new ManualClock(1_772_000_000L);     // a fixed instant; advanced by hand below
        Random random = new Random(2026);
        AuthorizationServer auth = new AuthorizationServer(clock, random);
        auth.registerClient(new RegisteredClient(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI));
        Aggregator agg = new Aggregator(random);
        Set<Scope> scopes = EnumSet.of(Scope.ACCOUNTS_READ, Scope.TRANSACTIONS_READ);

        System.out.println("== Consent flow: authorization code + PKCE (RFC 6749 section 4.1, RFC 7636) ==");
        Connection a = connect(agg, auth, bankA(auth), new FdxAdapter(), "user-42", scopes, true);
        System.out.println();

        System.out.println("== Bank A (FDX-style, page size 2): paginated fetch, normalized ==");
        for (Account acc : agg.fetchAccounts(a.connectionId)) System.out.println("   " + acc);
        for (Transaction t : agg.fetchTransactions(a.connectionId, "acc-chk-1")) System.out.println("   " + t);
        System.out.println();

        System.out.println("== Bank B (UK Open Banking-style, Links.Next paging): same schema out ==");
        Connection b = connect(agg, auth, bankB(auth), new UkObAdapter(), "user-42", scopes, false);
        for (Account acc : agg.fetchAccounts(b.connectionId)) System.out.println("   " + acc);
        for (Transaction t : agg.fetchTransactions(b.connectionId, "22289")) System.out.println("   " + t);
        System.out.println();

        System.out.println("== Token expiry and refresh ==");
        clock.advanceSeconds(AuthorizationServer.ACCESS_TTL_SECONDS + 1);
        String oldRefresh = a.refreshToken;
        int before = a.refreshCount;
        int n = agg.fetchAccounts(a.connectionId).size();
        System.out.println("   after " + (AuthorizationServer.ACCESS_TTL_SECONDS + 1) + "s the access token is dead: first call got 401, refreshed "
                + (a.refreshCount - before) + " time, retried, got " + n + " accounts");
        System.out.println("   refresh token rotated: " + !oldRefresh.equals(a.refreshToken));
        try { auth.refresh(Aggregator.CLIENT_ID, oldRefresh); }
        catch (OAuthException e) { System.out.println("   replaying the old refresh token: " + e.getMessage()); }
        System.out.println("   the replay revoked the whole consent, so A's current access token is now dead too: " + (auth.introspect(a.accessToken) == null));
        System.out.println();

        System.out.println("== Consent expiry (90 days) ==");
        clock.advanceDays(91);
        try { agg.fetchTransactions(b.connectionId, "22289"); }
        catch (ReauthorizationRequiredException e) { System.out.println("   " + e.getMessage()); }
        System.out.println();

        System.out.println("== Things the bank refuses ==");
        PendingAuthorization p = agg.beginAuthorization(scopes);
        String code = auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, scopes, p.codeChallenge(), "S256", "user-42");
        try { auth.exchangeCode(Aggregator.CLIENT_ID, code, Aggregator.REDIRECT_URI, Pkce.generateVerifier(random)); }
        catch (OAuthException e) { System.out.println("   wrong verifier: " + e.getMessage()); }
        try { auth.exchangeCode(Aggregator.CLIENT_ID, code, Aggregator.REDIRECT_URI, p.codeVerifier()); }
        catch (OAuthException e) { System.out.println("   same code again (it was consumed by the failed attempt): " + e.getMessage()); }
        try { auth.authorize(Aggregator.CLIENT_ID, "https://evil.example/callback", scopes, p.codeChallenge(), "S256", "user-42"); }
        catch (OAuthException e) { System.out.println("   unregistered redirect_uri: " + e.getMessage()); }
        try { auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, EnumSet.of(Scope.PAYMENTS_WRITE), p.codeChallenge(), "S256", "user-42"); }
        catch (OAuthException e) { System.out.println("   scope the client is not registered for: " + e.getMessage()); }
    }
}
