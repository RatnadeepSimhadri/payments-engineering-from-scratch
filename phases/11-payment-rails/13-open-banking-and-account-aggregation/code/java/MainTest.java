// Tests for phases/11-payment-rails/13-open-banking-and-account-aggregation. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.Random;
import java.util.Set;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static String oauthError(Runnable action) {
        try { action.run(); return "none"; } catch (OAuthException e) { return e.error; }
    }

    /** A fresh world: clock, auth server, aggregator. Each test group builds its own so they cannot interfere. */
    record World(ManualClock clock, AuthorizationServer auth, Aggregator agg, FdxStyleBank bankA, UkObStyleBank bankB) {
        static World create() {
            ManualClock clock = new ManualClock(1_772_000_000L);
            Random random = new Random(7);
            AuthorizationServer auth = new AuthorizationServer(clock, random);
            auth.registerClient(new RegisteredClient(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI));
            return new World(clock, auth, new Aggregator(random), Main.bankA(auth), Main.bankB(auth));
        }

        Connection connectA(Set<Scope> scopes) { return Main.connect(agg, auth, bankA, new FdxAdapter(), "u1", scopes, false); }
        Connection connectB(Set<Scope> scopes) { return Main.connect(agg, auth, bankB, new UkObAdapter(), "u1", scopes, false); }
    }

    static final Set<Scope> READ = EnumSet.of(Scope.ACCOUNTS_READ, Scope.TRANSACTIONS_READ);

    public static void main(String[] args) {
        // PKCE (RFC 7636)
        Random rnd = new Random(1);
        String verifier = Pkce.generateVerifier(rnd);
        check("pkce verifier is 64 unreserved chars", verifier.length() == 64 && verifier.matches("[A-Za-z0-9._~-]+"));
        check("pkce challenge is 43 base64url chars without padding", Pkce.challengeS256(verifier).length() == 43 && !Pkce.challengeS256(verifier).contains("="));
        check("pkce RFC 7636 appendix B vector", Pkce.challengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk").equals("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"));
        check("pkce verify accepts the right verifier", Pkce.verify(verifier, Pkce.challengeS256(verifier)));
        check("pkce verify rejects another verifier", !Pkce.verify(Pkce.generateVerifier(rnd), Pkce.challengeS256(verifier)));
        check("pkce verify rejects a too-short verifier", !Pkce.verify("short", Pkce.challengeS256("short")));

        // Authorization code grant: happy path
        World w = World.create();
        PendingAuthorization p = w.agg.beginAuthorization(READ);
        String url = w.agg.authorizationUrl("https://bank.example/authorize", p);
        check("authorize url carries code_challenge, state, S256, and scopes", url.contains("code_challenge=" + p.codeChallenge())
                && url.contains("state=" + p.state()) && url.contains("code_challenge_method=S256") && url.contains("accounts:read%20transactions:read"));
        check("authorize url never carries the verifier", !url.contains(p.codeVerifier()));
        String code = w.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p.codeChallenge(), "S256", "u1");
        Connection c = w.agg.completeAuthorization("u1", p.state(), code, w.auth, w.bankA, new FdxAdapter());
        check("tokens issued with the consented scopes", w.auth.introspect(c.accessToken) != null && w.auth.introspect(c.accessToken).scopes().equals(READ));
        check("access token expires_in is the server TTL", AuthorizationServer.ACCESS_TTL_SECONDS == 300);

        // Authorization code grant: failures
        World w2 = World.create();
        PendingAuthorization p2 = w2.agg.beginAuthorization(READ);
        String code2 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p2.codeChallenge(), "S256", "u1");
        check("wrong verifier -> invalid_grant", oauthError(() -> w2.auth.exchangeCode(Aggregator.CLIENT_ID, code2, Aggregator.REDIRECT_URI, Pkce.generateVerifier(rnd))).equals("invalid_grant"));
        check("code is single use even after a failed exchange", oauthError(() -> w2.auth.exchangeCode(Aggregator.CLIENT_ID, code2, Aggregator.REDIRECT_URI, p2.codeVerifier())).equals("invalid_grant"));
        PendingAuthorization p3 = w2.agg.beginAuthorization(READ);
        String code3 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p3.codeChallenge(), "S256", "u1");
        check("redirect_uri mismatch at token endpoint -> invalid_grant", oauthError(() -> w2.auth.exchangeCode(Aggregator.CLIENT_ID, code3, "https://other.example/cb", p3.codeVerifier())).equals("invalid_grant"));
        PendingAuthorization p4 = w2.agg.beginAuthorization(READ);
        String code4 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p4.codeChallenge(), "S256", "u1");
        w2.clock.advanceSeconds(AuthorizationServer.CODE_TTL_SECONDS);
        check("authorization code expires after 10 minutes", oauthError(() -> w2.auth.exchangeCode(Aggregator.CLIENT_ID, code4, Aggregator.REDIRECT_URI, p4.codeVerifier())).equals("invalid_grant"));
        check("unregistered redirect_uri at authorize -> invalid_request", oauthError(() -> w2.auth.authorize(Aggregator.CLIENT_ID, "https://evil.example/cb", READ, p4.codeChallenge(), "S256", "u1")).equals("invalid_request"));
        check("plain challenge method is refused", oauthError(() -> w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p4.codeChallenge(), "plain", "u1")).equals("invalid_request"));
        check("unknown client -> invalid_client", oauthError(() -> w2.auth.authorize("someone-else", Aggregator.REDIRECT_URI, READ, p4.codeChallenge(), "S256", "u1")).equals("invalid_client"));
        check("payments:write not registered -> invalid_scope", oauthError(() -> w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, EnumSet.of(Scope.PAYMENTS_WRITE), p4.codeChallenge(), "S256", "u1")).equals("invalid_scope"));
        PendingAuthorization p5 = w2.agg.beginAuthorization(READ);
        String code5 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p5.codeChallenge(), "S256", "u1");
        check("state mismatch on the redirect is rejected (CSRF)", oauthError(() -> w2.agg.completeAuthorization("u1", "forged-state", code5, w2.auth, w2.bankA, new FdxAdapter())).equals("invalid_request"));

        // Pagination and normalization: Bank A (FDX-style, offset cursor, page size 2)
        World w3 = World.create();
        Connection a = w3.connectA(READ);
        List<Account> accountsA = w3.agg.fetchAccounts(a.connectionId);
        check("bank A: 3 accounts across 2 pages", accountsA.size() == 3);
        check("bank A: account types mapped (CD -> OTHER)", accountsA.get(0).type() == AccountType.CHECKING && accountsA.get(2).type() == AccountType.OTHER);
        List<Transaction> txA = w3.agg.fetchTransactions(a.connectionId, "acc-chk-1");
        check("bank A: 5 transactions across 3 pages, in order", txA.size() == 5 && txA.get(0).transactionId().equals("t-1") && txA.get(4).transactionId().equals("t-5"));
        check("bank A: signed decimal parsed to minor units", txA.get(0).amount().equals(new Money(-4217, Currency.USD)));
        check("bank A: pending status mapped", txA.get(3).pending() && !txA.get(0).pending());
        check("bank A: no refresh was needed", a.refreshCount == 0);
        ApiResponse rawA = w3.bankA.accounts(a.accessToken, null);
        check("bank A raw page has nextOffset=2", "2".equals(((java.util.Map<?, ?>) rawA.body().get("page")).get("nextOffset")));

        // Pagination and normalization: Bank B (UK OB-style, Links.Next, unsigned amount + indicator)
        Connection b = w3.connectB(READ);
        List<Account> accountsB = w3.agg.fetchAccounts(b.connectionId);
        check("bank B: 2 accounts, CurrentAccount -> CHECKING", accountsB.size() == 2 && accountsB.get(0).type() == AccountType.CHECKING);
        check("bank B: identifier masked to last four", accountsB.get(0).maskedIdentifier().equals("****3345"));
        List<Transaction> txB = w3.agg.fetchTransactions(b.connectionId, "22289");
        check("bank B: 4 transactions across 2 pages", txB.size() == 4);
        check("bank B: Debit indicator becomes a negative amount", txB.get(1).amount().equals(new Money(-3650, Currency.GBP)));
        check("bank B: Credit indicator stays positive", txB.get(0).amount().equals(new Money(1000, Currency.GBP)));
        check("bank B: Pending status mapped", txB.get(3).pending());
        check("both banks normalize to one Transaction type with the same sign convention",
                txA.get(0).amount().minorUnits() < 0 && txB.get(1).amount().minorUnits() < 0 && txA.get(1).amount().minorUnits() > 0 && txB.get(2).amount().minorUnits() > 0);

        // Expiry, refresh on 401, rotation, replay
        World w4 = World.create();
        Connection a4 = w4.connectA(READ);
        String firstAccess = a4.accessToken, firstRefresh = a4.refreshToken;
        w4.clock.advanceSeconds(AuthorizationServer.ACCESS_TTL_SECONDS - 1);
        w4.agg.fetchAccounts(a4.connectionId);
        check("one second before expiry no refresh happens", a4.refreshCount == 0 && a4.accessToken.equals(firstAccess));
        w4.clock.advanceSeconds(2);
        check("expired token gets 401 invalid_token from the bank", w4.bankA.accounts(firstAccess, null).status() == 401
                && "invalid_token".equals(w4.bankA.accounts(firstAccess, null).authError()));
        List<Account> after = w4.agg.fetchAccounts(a4.connectionId);
        check("aggregator refreshed exactly once and retried", a4.refreshCount == 1 && after.size() == 3);
        check("refresh rotated both tokens", !a4.accessToken.equals(firstAccess) && !a4.refreshToken.equals(firstRefresh));
        check("replaying the retired refresh token -> invalid_grant", oauthError(() -> w4.auth.refresh(Aggregator.CLIENT_ID, firstRefresh)).equals("invalid_grant"));
        check("replay revokes the whole family: current access token is dead", w4.auth.introspect(a4.accessToken) == null);
        checkThrows("after revocation the aggregator cannot recover without the user", ReauthorizationRequiredException.class, () -> w4.agg.fetchAccounts(a4.connectionId));

        // Consent lifetime
        World w5 = World.create();
        Connection b5 = w5.connectB(READ);
        w5.clock.advanceDays(89);
        check("day 89: refresh still works", w5.agg.fetchAccounts(b5.connectionId).size() == 2 && b5.refreshCount == 1);
        w5.clock.advanceDays(2);
        checkThrows("day 91: consent expired, reauthorization required", ReauthorizationRequiredException.class, () -> w5.agg.fetchAccounts(b5.connectionId));

        // Scopes
        World w6 = World.create();
        Connection narrow = w6.connectA(EnumSet.of(Scope.ACCOUNTS_READ));
        check("accounts:read alone can list accounts", w6.agg.fetchAccounts(narrow.connectionId).size() == 3);
        check("accounts:read alone gets 403 insufficient_scope on transactions", oauthError(() -> w6.agg.fetchTransactions(narrow.connectionId, "acc-chk-1")).equals("insufficient_scope"));
        check("403 is not retried with a refresh", narrow.refreshCount == 0);
        check("unknown wire scope -> invalid_scope", oauthError(() -> Scope.fromWire("admin:all")).equals("invalid_scope"));

        // Determinism: two worlds built from the same seed produce the same tokens
        World d1 = World.create(), d2 = World.create();
        check("seeded worlds are deterministic", d1.connectA(READ).accessToken.equals(d2.connectA(READ).accessToken));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
