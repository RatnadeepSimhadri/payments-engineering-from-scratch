// Tests for phases/11-payment-rails/13-open-banking-and-account-aggregation. Run: go test ./...
package main

import (
	"errors"
	"regexp"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// oauthCode is the Java test's oauthError: the RFC 6749 error code, or "none" when the call succeeded.
func oauthCode(err error) string {
	if err == nil {
		return "none"
	}
	var oe *OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return "other: " + err.Error()
}

// World is a fresh world: clock, auth server, aggregator. Each test group builds its own so they cannot interfere.
type World struct {
	clock *ManualClock
	auth  *AuthorizationServer
	agg   *Aggregator
	bankA *FdxStyleBank
	bankB *UkObStyleBank
}

func newWorld() *World {
	clock := NewManualClock(1_772_000_000)
	random := NewJavaRandom(7)
	auth := NewAuthorizationServer(clock, random)
	auth.RegisterClient(RegisteredClient{ClientId, RedirectUri})
	return &World{clock, auth, NewAggregator(random), BankA(auth), BankB(auth)}
}

func (w *World) connectA(t *testing.T, scopes ScopeSet) *Connection {
	t.Helper()
	c, err := Connect(w.agg, w.auth, w.bankA, FdxAdapter{}, "u1", scopes, false)
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	return c
}

func (w *World) connectB(t *testing.T, scopes ScopeSet) *Connection {
	t.Helper()
	c, err := Connect(w.agg, w.auth, w.bankB, UkObAdapter{}, "u1", scopes, false)
	if err != nil {
		t.Fatalf("connect B: %v", err)
	}
	return c
}

var read = Scopes(AccountsRead, TransactionsRead)

func authorize(t *testing.T, w *World, p PendingAuthorization) string {
	t.Helper()
	code, err := w.auth.Authorize(ClientId, RedirectUri, read, p.CodeChallenge, "S256", "u1")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return code
}

func TestPkce(t *testing.T) {
	rnd := NewJavaRandom(1)
	verifier := Pkce.GenerateVerifier(rnd)
	check(t, "pkce verifier is 64 unreserved chars", len(verifier) == 64 && regexp.MustCompile(`^[A-Za-z0-9._~-]+$`).MatchString(verifier))
	ch := Pkce.ChallengeS256(verifier)
	check(t, "pkce challenge is 43 base64url chars without padding", len(ch) == 43 && !regexp.MustCompile(`=`).MatchString(ch))
	check(t, "pkce RFC 7636 appendix B vector", Pkce.ChallengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
	check(t, "pkce verify accepts the right verifier", Pkce.Verify(verifier, ch))
	check(t, "pkce verify rejects another verifier", !Pkce.Verify(Pkce.GenerateVerifier(rnd), ch))
	check(t, "pkce verify rejects a too-short verifier", !Pkce.Verify("short", Pkce.ChallengeS256("short")))
}

func TestJavaRandomMatchesTheJdk(t *testing.T) {
	// Values taken from java.util.Random(2026): nextInt(), nextInt(66), nextInt(64), nextLong().
	r := NewJavaRandom(2026)
	check(t, "nextInt()", r.NextInt() == -1637959697)
	check(t, "nextInt(66)", r.NextIntBound(66) == 52)
	check(t, "nextInt(64)", r.NextIntBound(64) == 54)
	check(t, "nextLong()", r.NextLong() == 3301047695993903762)
}

func TestAuthorizationCodeGrantHappyPath(t *testing.T) {
	w := newWorld()
	p := w.agg.BeginAuthorization(read)
	url := w.agg.AuthorizationUrl("https://bank.example/authorize", p)
	check(t, "authorize url carries code_challenge, state, S256, and scopes",
		contains(url, "code_challenge="+p.CodeChallenge) && contains(url, "state="+p.State) &&
			contains(url, "code_challenge_method=S256") && contains(url, "accounts:read%20transactions:read"))
	check(t, "authorize url never carries the verifier", !contains(url, p.CodeVerifier))
	code := authorize(t, w, p)
	c, err := w.agg.CompleteAuthorization("u1", p.State, code, w.auth, w.bankA, FdxAdapter{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	grant := w.auth.Introspect(c.AccessToken)
	check(t, "tokens issued with the consented scopes", grant != nil && grant.Scopes == read)
	check(t, "access token expires_in is the server TTL", AccessTTLSeconds == 300)
}

func TestAuthorizationCodeGrantFailures(t *testing.T) {
	rnd := NewJavaRandom(1)
	w2 := newWorld()
	p2 := w2.agg.BeginAuthorization(read)
	code2 := authorize(t, w2, p2)
	_, err := w2.auth.ExchangeCode(ClientId, code2, RedirectUri, Pkce.GenerateVerifier(rnd))
	check(t, "wrong verifier -> invalid_grant", oauthCode(err) == "invalid_grant")
	_, err = w2.auth.ExchangeCode(ClientId, code2, RedirectUri, p2.CodeVerifier)
	check(t, "code is single use even after a failed exchange", oauthCode(err) == "invalid_grant")
	p3 := w2.agg.BeginAuthorization(read)
	code3 := authorize(t, w2, p3)
	_, err = w2.auth.ExchangeCode(ClientId, code3, "https://other.example/cb", p3.CodeVerifier)
	check(t, "redirect_uri mismatch at token endpoint -> invalid_grant", oauthCode(err) == "invalid_grant")
	p4 := w2.agg.BeginAuthorization(read)
	code4 := authorize(t, w2, p4)
	w2.clock.AdvanceSeconds(CodeTTLSeconds)
	_, err = w2.auth.ExchangeCode(ClientId, code4, RedirectUri, p4.CodeVerifier)
	check(t, "authorization code expires after 10 minutes", oauthCode(err) == "invalid_grant")

	cases := []struct {
		name, want                    string
		clientId, redirectUri, method string
		scopes                        ScopeSet
	}{
		{"unregistered redirect_uri at authorize -> invalid_request", "invalid_request", ClientId, "https://evil.example/cb", "S256", read},
		{"plain challenge method is refused", "invalid_request", ClientId, RedirectUri, "plain", read},
		{"unknown client -> invalid_client", "invalid_client", "someone-else", RedirectUri, "S256", read},
		{"payments:write not registered -> invalid_scope", "invalid_scope", ClientId, RedirectUri, "S256", Scopes(PaymentsWrite)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := w2.auth.Authorize(c.clientId, c.redirectUri, c.scopes, p4.CodeChallenge, c.method, "u1")
			check(t, c.name, oauthCode(err) == c.want)
		})
	}
	p5 := w2.agg.BeginAuthorization(read)
	code5 := authorize(t, w2, p5)
	_, err = w2.agg.CompleteAuthorization("u1", "forged-state", code5, w2.auth, w2.bankA, FdxAdapter{})
	check(t, "state mismatch on the redirect is rejected (CSRF)", oauthCode(err) == "invalid_request")
}

func TestPaginationAndNormalization(t *testing.T) {
	w3 := newWorld()
	a := w3.connectA(t, read)
	accountsA, err := w3.agg.FetchAccounts(a.ConnectionId)
	check(t, "bank A: 3 accounts across 2 pages", err == nil && len(accountsA) == 3)
	check(t, "bank A: account types mapped (CD -> OTHER)", accountsA[0].Type == Checking && accountsA[2].Type == Other)
	txA, err := w3.agg.FetchTransactions(a.ConnectionId, "acc-chk-1")
	check(t, "bank A: 5 transactions across 3 pages, in order", err == nil && len(txA) == 5 && txA[0].TransactionId == "t-1" && txA[4].TransactionId == "t-5")
	check(t, "bank A: signed decimal parsed to minor units", txA[0].Amount == Money{-4217, USD})
	check(t, "bank A: pending status mapped", txA[3].Pending && !txA[0].Pending)
	check(t, "bank A: no refresh was needed", a.RefreshCount == 0)
	rawA := w3.bankA.Accounts(a.AccessToken, "")
	check(t, "bank A raw page has nextOffset=2", str(obj(rawA.Body, "page"), "nextOffset") == "2")

	b := w3.connectB(t, read)
	accountsB, err := w3.agg.FetchAccounts(b.ConnectionId)
	check(t, "bank B: 2 accounts, CurrentAccount -> CHECKING", err == nil && len(accountsB) == 2 && accountsB[0].Type == Checking)
	check(t, "bank B: identifier masked to last four", accountsB[0].MaskedIdentifier == "****3345")
	txB, err := w3.agg.FetchTransactions(b.ConnectionId, "22289")
	check(t, "bank B: 4 transactions across 2 pages", err == nil && len(txB) == 4)
	check(t, "bank B: Debit indicator becomes a negative amount", txB[1].Amount == Money{-3650, GBP})
	check(t, "bank B: Credit indicator stays positive", txB[0].Amount == Money{1000, GBP})
	check(t, "bank B: Pending status mapped", txB[3].Pending)
	check(t, "both banks normalize to one Transaction type with the same sign convention",
		txA[0].Amount.MinorUnits < 0 && txB[1].Amount.MinorUnits < 0 && txA[1].Amount.MinorUnits > 0 && txB[2].Amount.MinorUnits > 0)
}

func TestExpiryRefreshRotationReplay(t *testing.T) {
	w4 := newWorld()
	a4 := w4.connectA(t, read)
	firstAccess, firstRefresh := a4.AccessToken, a4.RefreshToken
	w4.clock.AdvanceSeconds(AccessTTLSeconds - 1)
	if _, err := w4.agg.FetchAccounts(a4.ConnectionId); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	check(t, "one second before expiry no refresh happens", a4.RefreshCount == 0 && a4.AccessToken == firstAccess)
	w4.clock.AdvanceSeconds(2)
	r := w4.bankA.Accounts(firstAccess, "")
	check(t, "expired token gets 401 invalid_token from the bank", r.Status == 401 && r.AuthError == "invalid_token")
	after, err := w4.agg.FetchAccounts(a4.ConnectionId)
	check(t, "aggregator refreshed exactly once and retried", err == nil && a4.RefreshCount == 1 && len(after) == 3)
	check(t, "refresh rotated both tokens", a4.AccessToken != firstAccess && a4.RefreshToken != firstRefresh)
	_, err = w4.auth.Refresh(ClientId, firstRefresh)
	check(t, "replaying the retired refresh token -> invalid_grant", oauthCode(err) == "invalid_grant")
	check(t, "replay revokes the whole family: current access token is dead", w4.auth.Introspect(a4.AccessToken) == nil)
	_, err = w4.agg.FetchAccounts(a4.ConnectionId)
	check(t, "after revocation the aggregator cannot recover without the user", errors.Is(err, ErrReauthorizationRequired))
}

func TestConsentLifetime(t *testing.T) {
	w5 := newWorld()
	b5 := w5.connectB(t, read)
	w5.clock.AdvanceDays(89)
	accounts, err := w5.agg.FetchAccounts(b5.ConnectionId)
	check(t, "day 89: refresh still works", err == nil && len(accounts) == 2 && b5.RefreshCount == 1)
	w5.clock.AdvanceDays(2)
	_, err = w5.agg.FetchAccounts(b5.ConnectionId)
	check(t, "day 91: consent expired, reauthorization required", errors.Is(err, ErrReauthorizationRequired))
}

func TestScopes(t *testing.T) {
	w6 := newWorld()
	narrow := w6.connectA(t, Scopes(AccountsRead))
	accounts, err := w6.agg.FetchAccounts(narrow.ConnectionId)
	check(t, "accounts:read alone can list accounts", err == nil && len(accounts) == 3)
	_, err = w6.agg.FetchTransactions(narrow.ConnectionId, "acc-chk-1")
	check(t, "accounts:read alone gets 403 insufficient_scope on transactions", oauthCode(err) == "insufficient_scope")
	check(t, "403 is not retried with a refresh", narrow.RefreshCount == 0)
	_, err = ScopeFromWire("admin:all")
	check(t, "unknown wire scope -> invalid_scope", oauthCode(err) == "invalid_scope")
	s, err := ScopeFromWire("transactions:read")
	check(t, "known wire scope round-trips", err == nil && s == TransactionsRead && s.Wire() == "transactions:read")
}

func TestDeterminism(t *testing.T) {
	d1, d2 := newWorld(), newWorld()
	check(t, "seeded worlds are deterministic", d1.connectA(t, read).AccessToken == d2.connectA(t, read).AccessToken)
}

func contains(s, sub string) bool { return regexp.MustCompile(regexp.QuoteMeta(sub)).MatchString(s) }
