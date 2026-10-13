# Open Banking: OAuth Consent, Account Aggregation, and Data Access

> Consent flows, token refresh, and the aggregator SDK pattern.

**Type:** Build
**Languages:** Java
**Prerequisites:** Phase 5, Lesson 09 (Idempotency Keys), Phase 5, Lesson 10 (Retries, Exponential Backoff, and Jitter), Phase 10, Lesson 01 (Representing Money), Phase 11, Lesson 11 (IBAN, BIC, and Account Number Validation)
**Time:** ~120 minutes

## Learning Objectives

- Trace the OAuth 2.0 authorization-code grant with PKCE end to end (RFC 6749 section 4.1, RFC 7636) and name what each of the five parameters on the authorize request defends against
- Build a bank authorization server that issues single-use codes, short-lived access tokens, and rotating refresh tokens, and detects refresh-token replay
- Build an aggregator client that pages through accounts and transactions with opaque cursors, refreshes on `401 invalid_token`, and refuses to retry on `403 insufficient_scope`
- Normalize two differently shaped bank payloads (an FDX-style dialect and a UK Open Banking-style dialect) into one account and transaction schema with a single sign convention and integer minor units
- Explain consent lifetime, scope minimization, and why a leaked refresh token must kill the entire token family

## The Problem

A personal-finance app wants to show a user every account they hold across four banks on one screen, with transactions, categorized, updated daily. Ten years ago the only way to do that was screen scraping: the user typed their online-banking username and password into the app, the app logged in as the user, and parsed the HTML. It worked until the bank changed a `<div>`, and it meant a third party held the user's banking credentials, with full write access, forever. A breach at any aggregator was a breach of every connected bank. Banks responded by blocking aggregator IP ranges; aggregators responded by rotating IPs; users were caught in the middle with broken connections and no recourse.

Open banking replaced that arrangement with a delegated-authorization protocol. The user never gives the app a password. Instead the app sends the user to the bank, the user authenticates there and approves a specific list of permissions (read accounts, read transactions, for 90 days), and the bank hands the app a token that works for exactly those permissions and nothing else. The protocol is OAuth 2.0; the payload formats are defined by regional standards (the UK Open Banking Read/Write API under PSD2, the Financial Data Exchange API in North America, the Berlin Group NextGenPSD2 in continental Europe); and the aggregator SDKs that a fintech integrates (Plaid, MX, Mastercard Open Banking, TrueLayer, Tink) are the layer that speaks every bank's dialect and presents one schema.

The failures in that stack are specific and this lesson builds against each one. An aggregator that stores the authorization code and replays it gets a second token pair, and so does an attacker who intercepted the redirect, which is why codes are single-use and bound to a PKCE verifier that never left the client. An aggregator that treats the access token as permanent gets `401` after five minutes and shows the user "connection broken"; one that refreshes on every `401` including the ones caused by a revoked consent will hammer the token endpoint forever. An aggregator that refreshes on `403` will never succeed, because `403 insufficient_scope` means the consent never included the permission. An aggregator that assumes a leaked refresh token is harmless because it rotates on use discovers that the attacker refreshed first, and the legitimate client is the one holding the dead token. And an aggregator that normalizes a UK bank's `{"Amount": "36.50", "CreditDebitIndicator": "Debit"}` and an FDX bank's `"amount": -36.50` into the same column without a sign convention produces a budget screen where one bank's spending is income.

This lesson builds the three parties as in-memory objects sharing an injected clock: the bank's authorization server, two resource servers with deliberately different payload shapes, and the aggregator that connects to both. Every rule above is a test.

## The Concept

### Three parties and four tokens

OAuth 2.0 (RFC 6749) names four roles. In open banking they map as follows:

| OAuth role | Open banking party | In this lesson |
|------------|--------------------|----------------|
| Resource owner | The bank customer | `userId` string passed to `authorize` |
| Client | The aggregator or fintech app (TPP, "third-party provider", in PSD2 language) | `Aggregator` |
| Authorization server | The bank's identity and consent system | `AuthorizationServer` |
| Resource server | The bank's data API (ASPSP, "account servicing payment service provider") | `FdxStyleBank`, `UkObStyleBank` |

And four credentials flow between them, each with a different lifetime and a different threat it is designed to survive:

| Credential | Issued by | Lifetime | Single use | Bound to |
|------------|-----------|----------|------------|----------|
| Authorization code | Auth server, via the user's browser redirect | 10 minutes maximum (RFC 6749 section 4.1.2) | Yes | Client id, redirect URI, PKCE challenge |
| Access token | Token endpoint | Minutes (300 s here) | No | Consent and its scopes |
| Refresh token | Token endpoint | Until consent expires or it is used | Yes, with rotation | Consent |
| Consent | Auth server, at the moment the user clicks "allow" | 90 days (UK Open Banking) | n/a | User, client, scopes |

The authorization code is the only credential that passes through the user's browser, so it is the most exposed and the most tightly bound. The access token is the one the resource server sees on every call, so it is short-lived to limit the damage of a leak. The refresh token is the long-lived secret and never goes anywhere except the token endpoint over a back channel.

### The authorization-code grant with PKCE

```mermaid
sequenceDiagram
  participant U as User's browser
  participant A as Aggregator (client)
  participant S as Bank auth server
  participant R as Bank data API
  A->>A: verifier = random(64); challenge = B64URL(SHA256(verifier)); state = random
  A->>U: redirect to /authorize?client_id&redirect_uri&scope&state&code_challenge&code_challenge_method=S256
  U->>S: authenticate, review scopes, click allow
  S->>S: store consent (90 d); store code bound to client, redirect_uri, challenge (10 min, single use)
  S->>U: redirect to redirect_uri?code&state
  U->>A: code, state
  A->>A: state matches a pending authorization? else reject (CSRF)
  A->>S: POST /token grant_type=authorization_code, code, redirect_uri, code_verifier
  S->>S: consume code; SHA256(verifier) == challenge? redirect_uri equal? not expired?
  S->>A: access_token (300 s), refresh_token, scope
  A->>R: GET /accounts  Authorization: Bearer access_token
  R->>S: introspect(access_token)
  R->>A: 200 page 1 + cursor
```

Each parameter on the authorize request exists because of a documented attack:

- **`redirect_uri`** must match the one registered for the client exactly. Without this, an attacker registers nothing and simply asks the bank to send the code to `https://evil.example/cb`. RFC 6749 section 3.1.2 and the OAuth 2.0 Security Best Current Practice require exact string matching.
- **`state`** is a random value the client stores and checks when the redirect returns. Without it, an attacker who obtained a code for *their own* account can trick a victim's browser into completing the victim's session with the attacker's code, silently linking the attacker's bank account into the victim's app (login CSRF).
- **`code_challenge`** is the SHA-256 of a secret the client keeps. Without it, an attacker who intercepts the code (a malicious browser extension, a leaked referrer header, a shared device) can exchange it for tokens. With it, the token endpoint demands the original secret, which never travelled through the browser. RFC 7636 was written for public clients on mobile devices, but the Security BCP now recommends it for every client, and the open banking profiles require it.
- **`code_challenge_method=S256`** rather than `plain`. `plain` sends the verifier itself as the challenge, which defends only against an attacker who sees the code but not the authorize request. S256 costs nothing and defends against both. The server here refuses `plain`.
- **`scope`** lists what is being asked for, space separated (section 3.3). The user sees it on the consent screen; the token carries what was granted; the resource server enforces it per endpoint.

### Why codes are single-use and why the failed exchange still consumes them

Section 4.1.2 says a code used more than once must be denied, and the server should revoke every token already issued on it. The implementation goes one step further: `exchangeCode` removes the code from the store *before* checking the verifier, so a code whose exchange failed (wrong verifier, wrong redirect URI) is also gone. The reasoning is that a failed exchange is evidence of an attack in progress, and the legitimate client, if it is the one that failed, can simply start a new authorization. The demo shows this: a wrong verifier fails with `invalid_grant`, and the subsequent correct attempt fails with `unknown or already used authorization code`.

### Refresh, rotation, and family revocation

An access token lasts five minutes here (production values range from five minutes to an hour). When the resource server returns `401` with `WWW-Authenticate: Bearer error="invalid_token"` (RFC 6750 section 3.1), the client presents its refresh token to the token endpoint and gets a new pair. Two rules make this safe:

1. **Rotation.** Every refresh returns a new refresh token and retires the old one. A refresh token is therefore used at most once by an honest client.
2. **Replay detection with family revocation.** If a retired refresh token is presented again, somebody has a copy. The server cannot know whether the second presenter is the attacker or the honest client (the attacker may have refreshed first), so it revokes the entire consent: every access token and the current refresh token. The honest client gets `ReauthorizationRequired` and sends the user back through consent. This is the OAuth 2.0 Security BCP's recommendation and it is what the demo shows when the old refresh token is replayed: the connection's *current* access token stops working too.

The client side has its own rule: refresh at most once per call, and only on `401`. A `403 insufficient_scope` means the consent never had the permission and no amount of refreshing will change that; retrying it is a bug that shows up as a token-endpoint flood. The test asserts `refreshCount == 0` after a `403`.

### Consent lifetime

A consent is what the user agreed to: which client, which scopes, until when. UK Open Banking set the lifetime at 90 days, after which the user had to re-authenticate at the bank (the FCA relaxed this in 2022 so that the TPP can re-confirm consent without a full re-authentication, but the 90-day clock remains the model). FDX leaves it to the data provider; most US banks use one year. The implementation stores `expiresAt` on the `Consent`, and `refresh` checks it: on day 89 the refresh succeeds, on day 91 it fails with `invalid_grant` and the aggregator surfaces `ReauthorizationRequired`. The access token's own expiry is irrelevant at that point; the chain of refreshes is what the consent lifetime bounds.

### Scopes are the permission model

Open banking standards define scopes (UK OB calls them permissions and nests them inside the consent object; FDX uses OAuth scopes directly). The important properties are the same: the client asks for the minimum it needs, the user sees exactly that list, and the resource server checks per endpoint. Here `accounts:read` unlocks `/accounts`, `transactions:read` unlocks `/accounts/{id}/transactions`, and the server refuses to grant `payments:write` to a client that is not registered for payment initiation at all, which models the regulatory split between account-information and payment-initiation licences under PSD2.

### Two dialects, one schema

The second half of the aggregator's job is translation. Banks implement different standards, and even within one standard they differ in details. The two banks in this lesson are modelled on the two most widespread shapes:

| Aspect | Bank A (FDX-style) | Bank B (UK Open Banking-style) |
|--------|--------------------|-------------------------------|
| Casing | camelCase | PascalCase under a `Data` envelope |
| Account wrapper | `{"depositAccount": {...}}` keyed by account category | `Data.Account[]` with `AccountSubType` |
| Account identifier | `accountNumberDisplay`, already masked | `Account[].Identification` in the clear with a `SchemeName` |
| Transaction amount | Signed decimal, `-42.17`, plus `debitCreditMemo` | Unsigned `Amount.Amount` string plus `CreditDebitIndicator` |
| Currency on a transaction | Implied by the account | Explicit `Amount.Currency` |
| Timestamp | `postedTimestamp` ISO 8601 with `Z` | `BookingDateTime` ISO 8601 with offset |
| Status | `POSTED` / `PENDING` | `Booked` / `Pending` |
| Paging | `page.nextOffset` numeric | `Links.Next` URL with `?page=N` |

The unified schema the aggregator produces:

```text
Account(bankId, accountId, displayName, currency, type: CHECKING|SAVINGS|CREDIT_CARD|OTHER, maskedIdentifier)
Transaction(bankId, accountId, transactionId, bookedDate, amount: Money (signed, negative = outflow), description, pending)
```

Four normalization decisions that matter:

1. **Sign convention.** Negative is money leaving the account, for every bank. Bank A's amounts are already signed; the adapter additionally checks that the sign agrees with `debitCreditMemo` and throws if not, because a disagreement means the bank changed its format. Bank B's amounts are unsigned and the adapter applies the sign from `CreditDebitIndicator`.
2. **Money as integer minor units.** Both banks send decimal strings (FDX's wire format is a JSON number, which the adapter must read as a string before any float is created; the in-memory payload here stores the digits). `Money.parse` from Phase 10, Lesson 01 rejects excess precision rather than rounding.
3. **Masked identifiers.** Bank B sends the full sort code and account number; the adapter keeps only the last four. The aggregator stores what it needs to display, not what it was given.
4. **Enumerations, not strings.** `AccountSubType: "CurrentAccount"` and `accountType: "CHECKING"` both become `AccountType.CHECKING`; an unknown value becomes `OTHER` rather than an exception, because banks add product types without notice.

### Cursors are opaque

Bank A pages with a numeric offset; Bank B with a `Links.Next` URL. The aggregator's fetch loop knows neither. The adapter returns `Page<T>(items, nextCursor)` where `nextCursor` is whatever string that bank needs back, and the loop passes it to the bank unchanged until it is `null`. This is the contract that lets a new bank be added by writing one adapter and nothing else. It also means the aggregator never constructs a cursor itself, so a bank that switches from offsets to keyset tokens does not break the client.

### Complexity and cost

Each page is one call. For `n` transactions and page size `p`, a full sync is `ceil(n / p)` calls plus at most one refresh. Banks rate-limit TPPs per consent (UK OB: four unattended calls per day per consent is the regulatory floor for "without the customer present"; more when the user is in the app), so the aggregator's scheduler, not covered here, is what decides when to spend those calls. Normalization is linear in the number of records.

### What this implementation does not do

- It does not do HTTP, TLS, or mutual TLS. Open banking profiles require MTLS or private-key JWT client authentication at the token endpoint (FAPI profile); here the client is identified by `clientId` and the back channel is a method call. Phase 5, Lesson 07 covers the transport.
- It does not sign requests or validate JWS-signed responses. UK OB requires detached JWS on payment requests; account information is TLS-only.
- It does not implement payment initiation (PIS). The `payments:write` scope exists only to show the server refusing it.
- It does not persist tokens. In production, refresh tokens live in an encrypted store with access logged, and rotation must be atomic with the store update or a crash between "received new token" and "saved it" strands the connection.
- It does not schedule syncs, deduplicate transactions across runs, or handle a bank that re-issues a transaction id with changed data. Those are the aggregator's data pipeline, and Phase 8 covers the stream-processing half of it.

## Build It

### Step 1: PKCE

```java
final class Pkce {
    static String generateVerifier(Random random) {
        StringBuilder sb = new StringBuilder(64);
        for (int i = 0; i < 64; i++) sb.append(UNRESERVED.charAt(random.nextInt(UNRESERVED.length())));
        return sb.toString();
    }

    static String challengeS256(String verifier) {
        byte[] digest = MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(StandardCharsets.US_ASCII));
        return Base64.getUrlEncoder().withoutPadding().encodeToString(digest);
    }

    static boolean verify(String verifier, String challenge) {
        if (verifier == null || verifier.length() < 43 || verifier.length() > 128) return false;
        return MessageDigest.isEqual(challengeS256(verifier).getBytes(StandardCharsets.US_ASCII), challenge.getBytes(StandardCharsets.US_ASCII));
    }
}
```

RFC 7636 section 4.1 requires a verifier of 43 to 128 characters from the unreserved set; 64 random characters from a 66-symbol alphabet is about 387 bits of entropy. The challenge is the base64url encoding, without padding, of the SHA-256 digest: always 43 characters, which the authorization server checks as a cheap format test. `MessageDigest.isEqual` is constant-time, which does not matter much for a hash comparison but is the habit to have. The test includes the RFC's Appendix B vector: verifier `dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk` must produce challenge `E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM`.

**Predict:** the `Random` is injected and seeded in tests. What would be wrong with seeding it in production, and which JDK class should be used there instead?

### Step 2: The authorization endpoint

```java
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
```

This method models the moment after the user has authenticated at the bank and clicked "allow"; the browser interaction itself is outside the lesson. Two things are created: the `Consent`, which is the durable record of what was agreed and outlives every token, and the `AuthorizationCode`, which is a ten-minute pointer to it bound to everything the token endpoint will re-check. The error codes are the ones RFC 6749 section 4.1.2.1 defines, so a client can switch on them.

### Step 3: The token endpoint, authorization-code grant

```java
TokenResponse exchangeCode(String clientId, String code, String redirectUri, String codeVerifier) {
    AuthorizationCode stored = codes.remove(code);                       // single use, whether or not the rest succeeds
    if (stored == null) throw new OAuthException("invalid_grant", "unknown or already used authorization code");
    if (!stored.clientId().equals(clientId)) throw new OAuthException("invalid_grant", "code was issued to a different client");
    if (!stored.redirectUri().equals(redirectUri)) throw new OAuthException("invalid_grant", "redirect_uri does not match the authorization request");
    if (clock.nowEpochSeconds() >= stored.expiresAt()) throw new OAuthException("invalid_grant", "authorization code expired");
    if (!Pkce.verify(codeVerifier, stored.codeChallenge())) throw new OAuthException("invalid_grant", "PKCE verification failed");
    return issueTokens(consents.get(stored.consentId()));
}
```

`remove` first, validate second. Every failure after the first line is `invalid_grant` (section 5.2), and the code is gone regardless. The `redirect_uri` is checked a second time here because section 4.1.3 requires the token request to repeat it and the server to compare; it closes an attack where the authorize request used a legitimate URI but the attacker controls the token request.

**Predict:** the clock is advanced by exactly `CODE_TTL_SECONDS` in the expiry test. Is the code expired at that instant or one second later, and which comparison operator decides?

### Step 4: Refresh with rotation and replay detection

```java
TokenResponse refresh(String clientId, String refreshToken) {
    String consentId = refreshTokens.remove(refreshToken);
    if (consentId == null) {
        String replayedFamily = retiredRefreshTokens.get(refreshToken);
        if (replayedFamily != null) {
            revokeConsent(replayedFamily);
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
```

Two maps: live refresh tokens and retired ones. A token moves from the first to the second on use. A token found in neither is simply unknown; a token found in the second is a replay, and the whole consent dies. Note the order: the consent-expiry check happens *before* the token is retired, so an expired consent does not burn the token (it is useless anyway, but the error message should say "expired", not "replay").

### Step 5: Two resource servers, one bearer-token check

```java
abstract class AbstractBank implements BankApi {
    protected ApiResponse requireScope(String accessToken, Scope scope) {
        AccessGrant grant = auth.introspect(accessToken);
        if (grant == null) return ApiResponse.unauthorized();      // 401 invalid_token
        if (!grant.scopes().contains(scope)) return ApiResponse.forbidden();   // 403 insufficient_scope
        return null;
    }
}
```

RFC 6750 section 3.1 defines exactly these two outcomes: `invalid_token` (expired, revoked, malformed) with status 401, and `insufficient_scope` with status 403. The distinction is what the client's retry logic keys on. `introspect` returns null for an unknown token, an expired one, and one whose consent was revoked, so revocation is immediate even though the access token's own expiry is in the future.

The two banks then differ only in payload construction. Bank A wraps each account in a type-keyed object and pages by offset:

```java
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
```

Bank B nests everything under `Data` and returns `Links.Next` as a URL. Both are in `Main.java`; read them side by side.

### Step 6: Adapters

```java
final class UkObAdapter implements BankAdapter {
    @Override public Page<Transaction> transactions(ApiResponse r, String bankId, String accountId) {
        List<Transaction> out = new ArrayList<>();
        Map<String, Object> data = (Map<String, Object>) r.body().get("Data");
        for (Map<String, Object> t : (List<Map<String, Object>>) data.get("Transaction")) {
            Map<String, Object> amt = (Map<String, Object>) t.get("Amount");
            Money unsigned = Money.parse((String) amt.get("Amount"), Currency.of((String) amt.get("Currency")));
            Money signed = "Debit".equals(t.get("CreditDebitIndicator")) ? unsigned.negate() : unsigned;
            out.add(new Transaction(bankId, accountId, (String) t.get("TransactionId"), ((String) t.get("BookingDateTime")).substring(0, 10),
                    signed, (String) t.get("TransactionInformation"), "Pending".equals(t.get("Status"))));
        }
        return new Page<>(out, (String) ((Map<String, Object>) r.body().get("Links")).get("Next"));
    }
}
```

The adapter is the only code that knows Bank B's field names. The FDX adapter does the mirror image: amount already signed, cross-checked against `debitCreditMemo`, cursor from `page.nextOffset`. Both return the same `Page<Transaction>`.

**Predict:** Bank B sends `"Amount": "36.50"` with `"CreditDebitIndicator": "Debit"`. What `Money` comes out, and what would come out if the adapter forgot the indicator?

### Step 7: The aggregator's fetch loop

```java
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

private ApiResponse callWithRefresh(Connection c, Call call) {
    ApiResponse r = call.run(c.accessToken);
    if (r.status() == 401) {
        try {
            TokenResponse fresh = c.auth.refresh(CLIENT_ID, c.refreshToken);
            c.accessToken = fresh.accessToken();
            c.refreshToken = fresh.refreshToken();
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
```

`callWithRefresh` is the whole retry policy: one refresh, on 401 only, and the refresh token is swapped in the same step so a second 401 inside the same page loop uses the new one. If the refresh itself fails, the connection is unusable and the exception type says what to do (send the user back to the bank). A `403` propagates as an `OAuthException` with the RFC 6750 error code, and `refreshCount` stays at zero, which the test checks.

### Step 8: Run it

```bash
cd phases/11-payment-rails/13-open-banking-and-account-aggregation/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Consent flow: authorization code + PKCE (RFC 6749 section 4.1, RFC 7636) ==
1. aggregator redirects user to:
   https://bank-a-fdx.example/authorize?response_type=code&client_id=aggregator-app&redirect_uri=https://aggregator.example/callback&scope=accounts:read%20transactions:read&state=77densXCpTK7yGXZ&code_challenge=KGHLRyiNm1c6z9nXaBZ66znBLJAAYeAWn15O3gdcqSU&code_challenge_method=S256
   verifier stays server-side: G.Kpo_knpPGe... (64 chars)
2. user authenticates at the bank, approves scopes; bank redirects back with code=code_2_zSY_v... state=77densXCpTK7yGXZ
3. aggregator exchanges code + verifier for tokens: access=at_3_3RC4m... refresh=rt_4_EZdxj...

== Bank A (FDX-style, page size 2): paginated fetch, normalized ==
   bank-a-fdx   acc-chk-1  Everyday Checking  USD CHECKING  ****4321
   ...
   bank-a-fdx   acc-chk-1  t-1   2026-03-02    -42.17 USD  GROCERY MART

== Bank B (UK Open Banking-style, Links.Next paging): same schema out ==
   bank-b-ukob  22289      124   2026-03-03    -36.50 GBP  TFL TRAVEL

== Token expiry and refresh ==
   after 301s the access token is dead: first call got 401, refreshed 1 time, retried, got 3 accounts
   refresh token rotated: true
   replaying the old refresh token: invalid_grant: refresh token replay detected; consent revoked
   the replay revoked the whole consent, so A's current access token is now dead too: true

== Consent expiry (90 days) ==
   conn_2: invalid_grant: consent expired; user must re-authorize

== Things the bank refuses ==
   wrong verifier: invalid_grant: PKCE verification failed
   same code again (it was consumed by the failed attempt): invalid_grant: unknown or already used authorization code
   unregistered redirect_uri: invalid_request: redirect_uri does not match registration
   scope the client is not registered for: invalid_scope: this client is not registered for payments:write
```

The test harness prints `47 passed, 0 failed`.

### Porting notes

A Python port uses `hashlib.sha256` and `base64.urlsafe_b64encode(...).rstrip(b"=")` for PKCE, `secrets.token_urlsafe` in production and a seeded `random.Random` in tests, `hmac.compare_digest` for the constant-time comparison, and plain `dict` payloads, which is what a JSON parser gives you anyway; the adapters become functions over dicts and the `@SuppressWarnings("unchecked")` noise disappears. A Go port returns `(TokenResponse, error)` with a typed `*OAuthError{Code, Description}` that callers test with `errors.As`, uses `crypto/sha256`, `base64.RawURLEncoding`, and `crypto/subtle.ConstantTimeCompare`, and decodes payloads into `map[string]any` with type assertions in the adapters. In both, the `Call` functional interface is a closure, and the mutable `Connection` would need a mutex the moment two goroutines or threads sync the same connection, which the Java version also lacks and which Exercise 3 adds.

## Use It

### The aggregator SDKs

Plaid, MX, Mastercard Open Banking (formerly Finicity), TrueLayer, Tink, and Yodlee are this lesson's `Aggregator` with a thousand adapters. Their product is the normalization layer plus the consent UI ("Link", "Connect", "Widget") that handles each bank's authorize flow, including banks that still require credential-based access where no OAuth API exists. Their APIs expose the unified schema: Plaid's `/accounts/get` and `/transactions/sync` (cursor-based, with `added`, `modified`, and `removed` arrays), MX's `/users/{guid}/transactions`, Mastercard Open Banking's `/aggregation/v2/customers/{id}/accounts`. When one of them returns `ITEM_LOGIN_REQUIRED` (Plaid's name for it), that is `ReauthorizationRequiredException`.

What they do that this lesson does not: webhook-driven sync scheduling, transaction deduplication across days (a pending transaction that posts gets a new id at some banks), merchant cleansing and categorization, balance and identity endpoints, and the regulatory registration (an AISP licence under PSD2, FDX certification) that lets them call banks at all.

### The standards

- **UK Open Banking Read/Write API** (OBIE, now Open Banking Limited): `Data`/`Links`/`Meta` envelopes, `CreditDebitIndicator`, consent objects with a `Permissions` array and `ExpirationDateTime`, FAPI security profile with MTLS or private-key JWT. Bank B follows its shape.
- **FDX API** (Financial Data Exchange, North America): camelCase, account objects keyed by type (`depositAccount`, `loanAccount`, `investmentAccount`), signed amounts, OAuth scopes, consent via FDX's own consent API. Bank A follows its shape. The US CFPB's Personal Financial Data Rights rule (Section 1033 of Dodd-Frank, finalized 2024) is pushing US banks toward FDX-conformant APIs on a schedule.
- **Berlin Group NextGenPSD2** (continental Europe): a third shape, with `transactionAmount: {amount, currency}` and a consent resource created before the authorize redirect. An exercise adapter.

### The JDK and OAuth

The JDK has no OAuth client. `java.net.http.HttpClient` does the transport; libraries like Nimbus OAuth 2.0 SDK or Spring Security's OAuth client do the protocol, and both implement exactly the state, PKCE, and rotation rules built here. What a library adds is the parts this lesson skipped: JWT access tokens with signature validation, discovery documents (`/.well-known/openid-configuration`), DPoP or MTLS token binding, and `token_endpoint_auth_method` negotiation.

## Ship It

`outputs/checklist-oauth-aggregator-integration.md` is a review checklist for any integration that connects to a bank or an aggregator: the authorize request, token storage, the 401/403 retry matrix, consent lifetime handling, and the normalization decisions (sign, currency, minor units, masking) that must be written down before the first adapter is merged.

## Exercises

1. **Reinforce.** Add a third bank in the Berlin Group shape: transactions under `transactions.booked[]` and `transactions.pending[]` (two separate arrays rather than a status field), amounts as `transactionAmount: {"amount": "-36.50", "currency": "EUR"}` (signed), paging via `_links.next.href`. Write the adapter, add it to the demo, and make the existing tests pass unchanged.
2. **Apply.** Implement token persistence with crash safety: a `TokenStore` interface with `save(connectionId, access, refresh)` and an in-memory implementation that can be told to fail after the refresh succeeded but before the save. Show with a test that the naive sequence loses the connection (the old refresh token is retired, the new one was never saved), then fix it by persisting the new pair before using the new access token, and by treating a replay error immediately after a crash as "check the store first".
3. **Extend.** Combine with Phase 5, Lesson 10 and Phase 4: two threads sync the same connection concurrently and both receive `401`. Without coordination, both refresh; the second refresh presents a token that was just retired by the first, the server detects a replay, and the consent is revoked by your own client. Add a per-connection lock (or a single-flight refresh) so that concurrent 401s produce exactly one refresh, and write a test that runs 8 threads against a `ManualClock` and asserts `refreshCount == 1` and no `ReauthorizationRequiredException`.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Authorization code | "the code in the redirect" | A single-use, ten-minute credential bound to client, redirect URI, and PKCE challenge, exchanged once over a back channel for tokens |
| PKCE | "pixie" | RFC 7636: the client keeps a random verifier, sends its SHA-256 on the authorize request, and proves possession at the token endpoint; defeats code interception |
| Access token | "the bearer token" | A short-lived credential the resource server checks on every call; expiry is the primary limit on the damage of a leak |
| Refresh token | "the long-lived token" | A credential presented only to the token endpoint to obtain a new access token; rotated on every use, and a replay revokes its whole family |
| Consent | "the connection" | The durable record of user, client, scopes, and expiry that every token points back to; the thing the user revokes in their banking app |
| Scope | "permissions" | The space-separated list on the authorize request, shown on the consent screen and enforced per endpoint by the resource server |
| AISP / PISP | "a TPP" | PSD2 licence categories: account-information service providers read data, payment-initiation service providers move money; separate registrations, separate scopes |
| Aggregator | "Plaid" | A licensed client that holds consents to many banks, speaks every dialect, and exposes one schema; the pattern this lesson builds in miniature |

## Further Reading

- [RFC 6749: The OAuth 2.0 Authorization Framework](https://www.rfc-editor.org/rfc/rfc6749) — sections 4.1 (authorization-code grant), 5.2 (error codes), and 6 (refresh) are the normative source for everything in `AuthorizationServer`.
- [RFC 7636: Proof Key for Code Exchange by OAuth Public Clients](https://www.rfc-editor.org/rfc/rfc7636) — short; Appendix B contains the test vector used in `MainTest`.
- [RFC 6750: The OAuth 2.0 Authorization Framework: Bearer Token Usage](https://www.rfc-editor.org/rfc/rfc6750) — section 3.1 defines `invalid_token` versus `insufficient_scope`, the distinction the retry logic depends on.
- [OAuth 2.0 Security Best Current Practice (RFC 9700)](https://www.rfc-editor.org/rfc/rfc9700) — exact redirect-URI matching, PKCE for all clients, refresh-token rotation with replay detection; the reasoning behind each rule built here.
- [FDX API specification](https://financialdataexchange.org/FDX/FDX/About/API.aspx) — the North American data standard; the `depositAccount` and `depositTransaction` shapes Bank A imitates.
- [UK Open Banking Read/Write API Specifications](https://standards.openbanking.org.uk/api-specifications/) — the Account and Transaction API; the `Data`/`Links` envelope, `CreditDebitIndicator`, and consent `Permissions` Bank B imitates.
- [CFPB Personal Financial Data Rights final rule (12 CFR Part 1033)](https://www.consumerfinance.gov/rules-policy/final-rules/required-rulemaking-on-personal-financial-data-rights/) — the US regulation that makes bank data APIs mandatory and references FDX as a recognized standard.
