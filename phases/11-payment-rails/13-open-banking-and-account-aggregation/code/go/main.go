// Lesson: phases/11-payment-rails/13-open-banking-and-account-aggregation/docs/en.md
// Open Banking: OAuth Consent, Account Aggregation, and Data Access
// Run:  go run .
// Test: go test ./...
// Sources: RFC 6749 (OAuth 2.0), RFC 7636 (PKCE), RFC 6750 (Bearer tokens), FDX API v6 and UK Open Banking Read/Write
//
//	API v3.1 (payload shapes). Go 1.22+, standard library only. No network: the bank, its auth server, and the
//	aggregator are in-memory objects sharing an injected clock. Money is integer minor units (Phase 10, Lesson 01).
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ---------- Money (subset of Phase 10, Lesson 01) ----------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	GBP = Currency{"GBP", 2}
	EUR = Currency{"EUR", 2}
)

func CurrencyOf(code string) Currency {
	switch code {
	case "USD":
		return USD
	case "GBP":
		return GBP
	case "EUR":
		return EUR
	default:
		return Currency{code, 2}
	}
}

func (c Currency) MinorUnitsPerMajor() int64 {
	s := int64(1)
	for i := 0; i < c.Exponent; i++ {
		s *= 10
	}
	return s
}

type Money struct {
	MinorUnits int64
	Currency   Currency
}

// ErrOverflow is returned where Java's Math.addExact would throw: an int64 never wraps silently.
var ErrOverflow = errors.New("int64 overflow")

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// ParseMoney reads "2500.00" or "-42.17" exactly into minor units; no float64 is ever involved.
func ParseMoney(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows", text, currency.Code)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	var minor int64
	for _, ch := range whole + frac {
		next := minor*10 + int64(ch-'0')
		if next < minor {
			return Money{}, ErrOverflow
		}
		minor = next
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (m Money) Negate() Money { return Money{-m.MinorUnits, m.Currency} }

func (m Money) Plus(o Money) (Money, error) {
	if m.Currency != o.Currency {
		return Money{}, errors.New("currency mismatch")
	}
	sum, err := addChecked(m.MinorUnits, o.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	mag := m.MinorUnits
	if mag < 0 {
		mag = -mag
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, mag/scale, m.Currency.Exponent, mag%scale, m.Currency.Code)
}

// ---------- Clock ----------

type Clock interface{ NowEpochSeconds() int64 }

type ManualClock struct{ now int64 }

func NewManualClock(epochSeconds int64) *ManualClock { return &ManualClock{epochSeconds} }
func (c *ManualClock) NowEpochSeconds() int64        { return c.now }
func (c *ManualClock) AdvanceSeconds(seconds int64)  { c.now += seconds }
func (c *ManualClock) AdvanceDays(days int64)        { c.now += days * 86_400 }

// ---------- OAuth 2.0 primitives ----------

// Scope is a scope as the bank publishes it. The wire form is what goes in the request (RFC 6749 section 3.3).
type Scope int

const (
	AccountsRead Scope = iota
	BalancesRead
	TransactionsRead
	PaymentsWrite
)

var scopeWire = [...]string{"accounts:read", "balances:read", "transactions:read", "payments:write"}

func (s Scope) Wire() string { return scopeWire[s] }

func ScopeFromWire(s string) (Scope, error) {
	for i, w := range scopeWire {
		if w == s {
			return Scope(i), nil
		}
	}
	return 0, &OAuthError{"invalid_scope", "unknown scope " + s}
}

// ScopeSet is the EnumSet<Scope>: a bitmask that iterates in declaration order and compares with ==.
type ScopeSet uint8

func Scopes(scopes ...Scope) ScopeSet {
	var set ScopeSet
	for _, s := range scopes {
		set |= 1 << s
	}
	return set
}

func (set ScopeSet) Contains(s Scope) bool       { return set&(1<<s) != 0 }
func (set ScopeSet) ContainsAll(o ScopeSet) bool { return set&o == o }
func (set ScopeSet) IsEmpty() bool               { return set == 0 }
func (set ScopeSet) Each(fn func(s Scope)) {
	for s := AccountsRead; s <= PaymentsWrite; s++ {
		if set.Contains(s) {
			fn(s)
		}
	}
}

// OAuthError is an OAuth error as RFC 6749 section 5.2 defines it: a short code plus a human description.
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Description }

// Pkce is RFC 7636: Proof Key for Code Exchange.
var Pkce pkce

type pkce struct{}

const pkceUnreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// GenerateVerifier draws a 64-character verifier from the unreserved set (RFC 7636 section 4.1 requires 43..128).
func (pkce) GenerateVerifier(random *JavaRandom) string {
	var sb strings.Builder
	for i := 0; i < 64; i++ {
		sb.WriteByte(pkceUnreserved[random.NextIntBound(int32(len(pkceUnreserved)))])
	}
	return sb.String()
}

// ChallengeS256 is BASE64URL-ENCODE(SHA256(ASCII(code_verifier))), no padding (section 4.2).
func (pkce) ChallengeS256(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (p pkce) Verify(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(p.ChallengeS256(verifier)), []byte(challenge)) == 1
}

type RegisteredClient struct {
	ClientId    string
	RedirectUri string
}

// Consent is what the user agreed to, with the bank's consent lifetime. UK Open Banking: 90 days before re-authentication.
type Consent struct {
	ConsentId string
	UserId    string
	ClientId  string
	Scopes    ScopeSet
	GrantedAt int64
	ExpiresAt int64
}

func (c Consent) IsExpired(now int64) bool { return now >= c.ExpiresAt }

// AuthorizationCode: single use, short lived, bound to the client, the redirect URI, and the PKCE challenge.
type AuthorizationCode struct {
	Code          string
	ConsentId     string
	ClientId      string
	RedirectUri   string
	CodeChallenge string
	ExpiresAt     int64
}

type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Scopes       ScopeSet
}

// AccessGrant is the server-side record of an access token: who, what scopes, until when.
type AccessGrant struct {
	ConsentId string
	Scopes    ScopeSet
	ExpiresAt int64
}

// AuthorizationServer is the bank's authorization server. It implements the authorization-code grant with PKCE,
// refresh with rotation, and replay detection.
type AuthorizationServer struct {
	clock                Clock
	random               *JavaRandom
	clients              map[string]RegisteredClient
	consents             map[string]Consent
	codes                map[string]AuthorizationCode
	accessTokens         map[string]AccessGrant
	refreshTokens        map[string]string // refresh token -> consentId
	retiredRefreshTokens map[string]string // rotated-out token -> consentId (replay detection)
	sequence             int
}

const (
	CodeTTLSeconds    int64 = 600 // RFC 6749 section 4.1.2: a maximum of 10 minutes is recommended
	AccessTTLSeconds  int64 = 300
	ConsentTTLSeconds int64 = 90 * 86_400
)

func NewAuthorizationServer(clock Clock, random *JavaRandom) *AuthorizationServer {
	return &AuthorizationServer{clock: clock, random: random, clients: map[string]RegisteredClient{},
		consents: map[string]Consent{}, codes: map[string]AuthorizationCode{}, accessTokens: map[string]AccessGrant{},
		refreshTokens: map[string]string{}, retiredRefreshTokens: map[string]string{}}
}

func (a *AuthorizationServer) RegisterClient(client RegisteredClient) {
	a.clients[client.ClientId] = client
}

func (a *AuthorizationServer) newToken(prefix string) string {
	bytes := make([]byte, 18)
	a.random.NextBytes(bytes)
	a.sequence++
	return prefix + "_" + strconv.Itoa(a.sequence) + "_" + base64.RawURLEncoding.EncodeToString(bytes)
}

// Authorize is the /authorize endpoint after the user has authenticated and clicked "allow" on the consent screen.
// It returns the authorization code the bank would send back on the redirect URI together with `state`.
func (a *AuthorizationServer) Authorize(clientId, redirectUri string, requestedScopes ScopeSet, codeChallenge, codeChallengeMethod, userId string) (string, error) {
	client, ok := a.clients[clientId]
	if !ok {
		return "", &OAuthError{"invalid_client", "unknown client " + clientId}
	}
	if client.RedirectUri != redirectUri {
		return "", &OAuthError{"invalid_request", "redirect_uri does not match registration"}
	}
	if codeChallengeMethod != "S256" {
		return "", &OAuthError{"invalid_request", "code_challenge_method must be S256"}
	}
	if len(codeChallenge) != 43 {
		return "", &OAuthError{"invalid_request", "code_challenge must be a 43-char base64url SHA-256"}
	}
	if requestedScopes.IsEmpty() {
		return "", &OAuthError{"invalid_scope", "at least one scope is required"}
	}
	if requestedScopes.Contains(PaymentsWrite) {
		return "", &OAuthError{"invalid_scope", "this client is not registered for payments:write"}
	}

	now := a.clock.NowEpochSeconds()
	a.sequence++
	consentId := "consent_" + strconv.Itoa(a.sequence)
	a.consents[consentId] = Consent{consentId, userId, clientId, requestedScopes, now, now + ConsentTTLSeconds}
	code := a.newToken("code")
	a.codes[code] = AuthorizationCode{code, consentId, clientId, redirectUri, codeChallenge, now + CodeTTLSeconds}
	return code, nil
}

// ExchangeCode is the /token endpoint, grant_type=authorization_code.
func (a *AuthorizationServer) ExchangeCode(clientId, code, redirectUri, codeVerifier string) (TokenResponse, error) {
	stored, ok := a.codes[code]
	delete(a.codes, code) // single use, whether or not the rest succeeds
	if !ok {
		return TokenResponse{}, &OAuthError{"invalid_grant", "unknown or already used authorization code"}
	}
	if stored.ClientId != clientId {
		return TokenResponse{}, &OAuthError{"invalid_grant", "code was issued to a different client"}
	}
	if stored.RedirectUri != redirectUri {
		return TokenResponse{}, &OAuthError{"invalid_grant", "redirect_uri does not match the authorization request"}
	}
	if a.clock.NowEpochSeconds() >= stored.ExpiresAt {
		return TokenResponse{}, &OAuthError{"invalid_grant", "authorization code expired"}
	}
	if !Pkce.Verify(codeVerifier, stored.CodeChallenge) {
		return TokenResponse{}, &OAuthError{"invalid_grant", "PKCE verification failed"}
	}
	return a.issueTokens(a.consents[stored.ConsentId]), nil
}

// Refresh is the /token endpoint, grant_type=refresh_token, with rotation and replay detection.
func (a *AuthorizationServer) Refresh(clientId, refreshToken string) (TokenResponse, error) {
	consentId, ok := a.refreshTokens[refreshToken]
	delete(a.refreshTokens, refreshToken)
	if !ok {
		if replayedFamily, replayed := a.retiredRefreshTokens[refreshToken]; replayed {
			a.RevokeConsent(replayedFamily) // a replayed token means it leaked: kill the family
			return TokenResponse{}, &OAuthError{"invalid_grant", "refresh token replay detected; consent revoked"}
		}
		return TokenResponse{}, &OAuthError{"invalid_grant", "unknown refresh token"}
	}
	consent, ok := a.consents[consentId]
	if !ok {
		return TokenResponse{}, &OAuthError{"invalid_grant", "consent revoked"}
	}
	if consent.ClientId != clientId {
		return TokenResponse{}, &OAuthError{"invalid_grant", "refresh token belongs to another client"}
	}
	if consent.IsExpired(a.clock.NowEpochSeconds()) {
		return TokenResponse{}, &OAuthError{"invalid_grant", "consent expired; user must re-authorize"}
	}
	a.retiredRefreshTokens[refreshToken] = consentId
	return a.issueTokens(consent), nil
}

func (a *AuthorizationServer) issueTokens(consent Consent) TokenResponse {
	now := a.clock.NowEpochSeconds()
	access := a.newToken("at")
	refresh := a.newToken("rt")
	a.accessTokens[access] = AccessGrant{consent.ConsentId, consent.Scopes, now + AccessTTLSeconds}
	a.refreshTokens[refresh] = consent.ConsentId
	return TokenResponse{access, refresh, AccessTTLSeconds, consent.Scopes}
}

// Introspect is the token introspection the resource server uses (RFC 7662 in spirit): nil if unknown, expired, or revoked.
func (a *AuthorizationServer) Introspect(accessToken string) *AccessGrant {
	grant, ok := a.accessTokens[accessToken]
	if !ok {
		return nil
	}
	if a.clock.NowEpochSeconds() >= grant.ExpiresAt {
		return nil
	}
	if _, live := a.consents[grant.ConsentId]; !live {
		return nil
	}
	return &grant
}

func (a *AuthorizationServer) RevokeConsent(consentId string) {
	delete(a.consents, consentId)
	for token, g := range a.accessTokens {
		if g.ConsentId == consentId {
			delete(a.accessTokens, token)
		}
	}
	for token, c := range a.refreshTokens {
		if c == consentId {
			delete(a.refreshTokens, token)
		}
	}
}

func (a *AuthorizationServer) Consent(consentId string) (Consent, bool) {
	c, ok := a.consents[consentId]
	return c, ok
}

// ---------- Resource servers: two banks with different payload shapes ----------

// ApiResponse is an HTTP-like response without HTTP: status, an optional WWW-Authenticate error, and a JSON-like body.
type ApiResponse struct {
	Status    int
	AuthError string
	Body      map[string]any
}

func ApiOk(body map[string]any) ApiResponse { return ApiResponse{200, "", body} }
func ApiUnauthorized() ApiResponse          { return ApiResponse{401, "invalid_token", map[string]any{}} }
func ApiForbidden() ApiResponse             { return ApiResponse{403, "insufficient_scope", map[string]any{}} }

// BankApi is a bank's data API. `cursor` is opaque to the caller ("" for the first page); each bank encodes paging its own way.
type BankApi interface {
	BankId() string
	Accounts(accessToken, cursor string) ApiResponse
	Transactions(accessToken, accountId, cursor string) ApiResponse
}

// bankBase holds the common token checks every resource server performs (RFC 6750 section 3.1 error codes).
type bankBase struct {
	auth     *AuthorizationServer
	pageSize int
}

func (b bankBase) requireScope(accessToken string, scope Scope) (ApiResponse, bool) {
	grant := b.auth.Introspect(accessToken)
	if grant == nil {
		return ApiUnauthorized(), false
	}
	if !grant.Scopes.Contains(scope) {
		return ApiForbidden(), false
	}
	return ApiResponse{}, true
}

func page(all []any, offset, size int) []any {
	return all[min(offset, len(all)):min(offset+size, len(all))]
}

// FdxStyleBank is Bank A: an FDX-style dialect, camelCase, accounts wrapped by type, signed decimal amounts, offset paging.
type FdxStyleBank struct {
	bankBase
	accounts     []any
	transactions map[string][]any
}

func NewFdxStyleBank(auth *AuthorizationServer, pageSize int) *FdxStyleBank {
	return &FdxStyleBank{bankBase{auth, pageSize}, []any{}, map[string][]any{}}
}

func (b *FdxStyleBank) BankId() string { return "bank-a-fdx" }

func (b *FdxStyleBank) AddAccount(accountId, nickname, currency, accountType, maskedNumber string) {
	b.accounts = append(b.accounts, map[string]any{"depositAccount": map[string]any{"accountId": accountId, "nickname": nickname,
		"currency": currency, "accountType": accountType, "accountNumberDisplay": maskedNumber}})
	b.transactions[accountId] = []any{}
}

// AddTransaction: FDX carries a signed amount as a JSON number and a debitCreditMemo; we keep the digits as a string so no float exists.
func (b *FdxStyleBank) AddTransaction(accountId, txId, postedDate, signedAmount, description, status string) {
	memo := "CREDIT"
	if strings.HasPrefix(signedAmount, "-") {
		memo = "DEBIT"
	}
	b.transactions[accountId] = append(b.transactions[accountId], map[string]any{"depositTransaction": map[string]any{
		"transactionId": txId, "postedTimestamp": postedDate + "T00:00:00Z", "amount": signedAmount, "debitCreditMemo": memo,
		"description": description, "status": status}})
}

func (b *FdxStyleBank) Accounts(token, cursor string) ApiResponse {
	if err, ok := b.requireScope(token, AccountsRead); !ok {
		return err
	}
	return b.pageOf("accounts", b.accounts, cursor)
}

func (b *FdxStyleBank) Transactions(token, accountId, cursor string) ApiResponse {
	if err, ok := b.requireScope(token, TransactionsRead); !ok {
		return err
	}
	return b.pageOf("transactions", b.transactions[accountId], cursor)
}

func (b *FdxStyleBank) pageOf(key string, all []any, cursor string) ApiResponse {
	offset := 0
	if cursor != "" {
		offset, _ = strconv.Atoi(cursor)
	}
	pageInfo := map[string]any{}
	if offset+b.pageSize < len(all) {
		pageInfo["nextOffset"] = strconv.Itoa(offset + b.pageSize)
	}
	return ApiOk(map[string]any{key: page(all, offset, b.pageSize), "page": pageInfo})
}

// UkObStyleBank is Bank B: a UK Open Banking-style dialect, PascalCase under Data, amount object with Currency, Links.Next paging.
type UkObStyleBank struct {
	bankBase
	accounts     []any
	transactions map[string][]any
}

func NewUkObStyleBank(auth *AuthorizationServer, pageSize int) *UkObStyleBank {
	return &UkObStyleBank{bankBase{auth, pageSize}, []any{}, map[string][]any{}}
}

func (b *UkObStyleBank) BankId() string { return "bank-b-ukob" }

func (b *UkObStyleBank) AddAccount(accountId, nickname, currency, subType, sortCodeAccountNumber string) {
	b.accounts = append(b.accounts, map[string]any{"AccountId": accountId, "Nickname": nickname, "Currency": currency, "AccountSubType": subType,
		"Account": []any{map[string]any{"SchemeName": "UK.OBIE.SortCodeAccountNumber", "Identification": sortCodeAccountNumber}}})
	b.transactions[accountId] = []any{}
}

func (b *UkObStyleBank) AddTransaction(accountId, txId, bookingDate, unsignedAmount, creditDebit, info, status string) {
	b.transactions[accountId] = append(b.transactions[accountId], map[string]any{"TransactionId": txId, "BookingDateTime": bookingDate + "T09:30:00+00:00",
		"Amount": map[string]any{"Amount": unsignedAmount, "Currency": "GBP"}, "CreditDebitIndicator": creditDebit,
		"TransactionInformation": info, "Status": status})
}

func (b *UkObStyleBank) Accounts(token, cursor string) ApiResponse {
	if err, ok := b.requireScope(token, AccountsRead); !ok {
		return err
	}
	return b.pageOf("Account", "/accounts", b.accounts, cursor)
}

func (b *UkObStyleBank) Transactions(token, accountId, cursor string) ApiResponse {
	if err, ok := b.requireScope(token, TransactionsRead); !ok {
		return err
	}
	return b.pageOf("Transaction", "/accounts/"+accountId+"/transactions", b.transactions[accountId], cursor)
}

func (b *UkObStyleBank) pageOf(key, path string, all []any, cursor string) ApiResponse {
	pageNo := 1
	if cursor != "" {
		pageNo, _ = strconv.Atoi(cursor[strings.Index(cursor, "page=")+5:])
	}
	offset := (pageNo - 1) * b.pageSize
	links := map[string]any{"Self": path + "?page=" + strconv.Itoa(pageNo)}
	if offset+b.pageSize < len(all) {
		links["Next"] = path + "?page=" + strconv.Itoa(pageNo+1)
	}
	return ApiOk(map[string]any{"Data": map[string]any{key: page(all, offset, b.pageSize)}, "Links": links})
}

// ---------- The aggregator's unified schema and per-bank adapters ----------

type AccountType int

const (
	Checking AccountType = iota
	Savings
	CreditCard
	Other
)

func (t AccountType) String() string {
	return [...]string{"CHECKING", "SAVINGS", "CREDIT_CARD", "OTHER"}[t]
}

type Account struct {
	BankId           string
	AccountId        string
	DisplayName      string
	Currency         Currency
	Type             AccountType
	MaskedIdentifier string
}

func (a Account) String() string {
	return fmt.Sprintf("%-12s %-10s %-18s %s %-9s %s", a.BankId, a.AccountId, a.DisplayName, a.Currency.Code, a.Type, a.MaskedIdentifier)
}

// Transaction carries a signed amount: negative is money leaving the account. One sign convention for every bank.
type Transaction struct {
	BankId        string
	AccountId     string
	TransactionId string
	BookedDate    string
	Amount        Money
	Description   string
	Pending       bool
}

func (t Transaction) String() string {
	suffix := ""
	if t.Pending {
		suffix = "  (pending)"
	}
	return fmt.Sprintf("%-12s %-10s %-5s %s %13s  %-18s%s", t.BankId, t.AccountId, t.TransactionId, t.BookedDate, t.Amount, t.Description, suffix)
}

// Page is one page of normalized items; NextCursor "" means this was the last page.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// BankAdapter translates one bank's dialect into the unified schema and knows how that bank pages.
type BankAdapter interface {
	Accounts(response ApiResponse, bankId string) (Page[Account], error)
	Transactions(response ApiResponse, bankId, accountId string) (Page[Transaction], error)
}

// str reads a string field from a JSON-like object ("" when absent): the Go spelling of the Java (String) cast.
func str(m any, key string) string {
	if obj, ok := m.(map[string]any); ok {
		if v, ok := obj[key].(string); ok {
			return v
		}
	}
	return ""
}

func obj(m any, key string) map[string]any {
	if o, ok := m.(map[string]any); ok {
		if v, ok := o[key].(map[string]any); ok {
			return v
		}
	}
	return nil
}

func list(m any, key string) []any {
	if o, ok := m.(map[string]any); ok {
		if v, ok := o[key].([]any); ok {
			return v
		}
	}
	return nil
}

type FdxAdapter struct{}

func (FdxAdapter) Accounts(r ApiResponse, bankId string) (Page[Account], error) {
	out := []Account{}
	for _, wrapper := range list(r.Body, "accounts") {
		a := obj(wrapper, "depositAccount")
		var accountType AccountType
		switch str(a, "accountType") {
		case "CHECKING":
			accountType = Checking
		case "SAVINGS":
			accountType = Savings
		default:
			accountType = Other
		}
		out = append(out, Account{bankId, str(a, "accountId"), str(a, "nickname"), CurrencyOf(str(a, "currency")), accountType, str(a, "accountNumberDisplay")})
	}
	return Page[Account]{out, str(obj(r.Body, "page"), "nextOffset")}, nil
}

func (FdxAdapter) Transactions(r ApiResponse, bankId, accountId string) (Page[Transaction], error) {
	out := []Transaction{}
	for _, wrapper := range list(r.Body, "transactions") {
		t := obj(wrapper, "depositTransaction")
		amount, err := ParseMoney(str(t, "amount"), USD) // FDX amounts are already signed
		if err != nil {
			return Page[Transaction]{}, err
		}
		debit := str(t, "debitCreditMemo") == "DEBIT"
		if debit != (amount.MinorUnits < 0) {
			return Page[Transaction]{}, fmt.Errorf("FDX sign and debitCreditMemo disagree on %s", str(t, "transactionId"))
		}
		out = append(out, Transaction{bankId, accountId, str(t, "transactionId"), str(t, "postedTimestamp")[:10],
			amount, str(t, "description"), str(t, "status") == "PENDING"})
	}
	return Page[Transaction]{out, str(obj(r.Body, "page"), "nextOffset")}, nil
}

type UkObAdapter struct{}

func (UkObAdapter) Accounts(r ApiResponse, bankId string) (Page[Account], error) {
	out := []Account{}
	for _, a := range list(obj(r.Body, "Data"), "Account") {
		var accountType AccountType
		switch str(a, "AccountSubType") {
		case "CurrentAccount":
			accountType = Checking
		case "Savings":
			accountType = Savings
		case "CreditCard":
			accountType = CreditCard
		default:
			accountType = Other
		}
		ids := list(a, "Account")
		ident := str(ids[0], "Identification")
		masked := "****" + ident[len(ident)-4:]
		out = append(out, Account{bankId, str(a, "AccountId"), str(a, "Nickname"), CurrencyOf(str(a, "Currency")), accountType, masked})
	}
	return Page[Account]{out, str(obj(r.Body, "Links"), "Next")}, nil
}

func (UkObAdapter) Transactions(r ApiResponse, bankId, accountId string) (Page[Transaction], error) {
	out := []Transaction{}
	for _, t := range list(obj(r.Body, "Data"), "Transaction") {
		amt := obj(t, "Amount")
		unsigned, err := ParseMoney(str(amt, "Amount"), CurrencyOf(str(amt, "Currency")))
		if err != nil {
			return Page[Transaction]{}, err
		}
		signed := unsigned // UK OB amounts are unsigned
		if str(t, "CreditDebitIndicator") == "Debit" {
			signed = unsigned.Negate()
		}
		out = append(out, Transaction{bankId, accountId, str(t, "TransactionId"), str(t, "BookingDateTime")[:10],
			signed, str(t, "TransactionInformation"), str(t, "Status") == "Pending"})
	}
	return Page[Transaction]{out, str(obj(r.Body, "Links"), "Next")}, nil
}

// ---------- The aggregator ----------

// ErrReauthorizationRequired is returned when a refresh fails: the only fix is to send the user back through consent.
var ErrReauthorizationRequired = errors.New("reauthorization required")

// Connection is one user's link to one bank: the tokens and the consent they came from. Mutable because tokens rotate.
type Connection struct {
	ConnectionId string
	UserId       string
	Bank         BankApi
	Adapter      BankAdapter
	Auth         *AuthorizationServer
	AccessToken  string
	RefreshToken string
	RefreshCount int
}

// PendingAuthorization is the authorization request the aggregator builds, and the state it must remember until the redirect comes back.
type PendingAuthorization struct {
	State         string
	CodeVerifier  string
	CodeChallenge string
	Scopes        ScopeSet
}

type Aggregator struct {
	random      *JavaRandom
	pending     map[string]PendingAuthorization
	connections map[string]*Connection
	sequence    int
}

const (
	ClientId    = "aggregator-app"
	RedirectUri = "https://aggregator.example/callback"
)

func NewAggregator(random *JavaRandom) *Aggregator {
	return &Aggregator{random: random, pending: map[string]PendingAuthorization{}, connections: map[string]*Connection{}}
}

// BeginAuthorization is step 1: build the /authorize request. The verifier never leaves this process; only its hash does.
func (g *Aggregator) BeginAuthorization(scopes ScopeSet) PendingAuthorization {
	stateBytes := make([]byte, 12)
	g.random.NextBytes(stateBytes)
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := Pkce.GenerateVerifier(g.random)
	p := PendingAuthorization{state, verifier, Pkce.ChallengeS256(verifier), scopes}
	g.pending[state] = p
	return p
}

func (g *Aggregator) AuthorizationUrl(bankAuthorizeEndpoint string, p PendingAuthorization) string {
	var scope []string
	p.Scopes.Each(func(s Scope) { scope = append(scope, s.Wire()) })
	return bankAuthorizeEndpoint + "?response_type=code&client_id=" + ClientId + "&redirect_uri=" + RedirectUri +
		"&scope=" + strings.Join(scope, "%20") + "&state=" + p.State +
		"&code_challenge=" + p.CodeChallenge + "&code_challenge_method=S256"
}

// CompleteAuthorization is step 3: the redirect came back with code and state. Verify state (CSRF), exchange the code with the verifier.
func (g *Aggregator) CompleteAuthorization(userId, returnedState, code string, auth *AuthorizationServer, bank BankApi, adapter BankAdapter) (*Connection, error) {
	p, ok := g.pending[returnedState]
	delete(g.pending, returnedState)
	if !ok {
		return nil, &OAuthError{"invalid_request", "state does not match any pending authorization (possible CSRF)"}
	}
	tokens, err := auth.ExchangeCode(ClientId, code, RedirectUri, p.CodeVerifier)
	if err != nil {
		return nil, err
	}
	if !tokens.Scopes.ContainsAll(p.Scopes) {
		return nil, &OAuthError{"invalid_scope", "bank granted fewer scopes than requested"}
	}
	g.sequence++
	id := "conn_" + strconv.Itoa(g.sequence)
	c := &Connection{ConnectionId: id, UserId: userId, Bank: bank, Adapter: adapter, Auth: auth,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken}
	g.connections[id] = c
	return c, nil
}

func (g *Aggregator) Connection(id string) *Connection { return g.connections[id] }

// FetchAccounts fetches every page of accounts, refreshing the token once on 401.
func (g *Aggregator) FetchAccounts(connectionId string) ([]Account, error) {
	c := g.connections[connectionId]
	all := []Account{}
	cursor := ""
	for {
		cur := cursor
		r, err := g.callWithRefresh(c, func(token string) ApiResponse { return c.Bank.Accounts(token, cur) })
		if err != nil {
			return nil, err
		}
		page, err := c.Adapter.Accounts(r, c.Bank.BankId())
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		cursor = page.NextCursor
		if cursor == "" {
			return all, nil
		}
	}
}

func (g *Aggregator) FetchTransactions(connectionId, accountId string) ([]Transaction, error) {
	c := g.connections[connectionId]
	all := []Transaction{}
	cursor := ""
	for {
		cur := cursor
		r, err := g.callWithRefresh(c, func(token string) ApiResponse { return c.Bank.Transactions(token, accountId, cur) })
		if err != nil {
			return nil, err
		}
		page, err := c.Adapter.Transactions(r, c.Bank.BankId(), accountId)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		cursor = page.NextCursor
		if cursor == "" {
			return all, nil
		}
	}
}

// Call is one authenticated request; the aggregator retries it with a fresh token.
type Call func(accessToken string) ApiResponse

// callWithRefresh: 401 invalid_token -> refresh once and retry. 403 insufficient_scope -> never retry; the consent lacks the scope.
func (g *Aggregator) callWithRefresh(c *Connection, call Call) (ApiResponse, error) {
	r := call(c.AccessToken)
	if r.Status == 401 {
		fresh, err := c.Auth.Refresh(ClientId, c.RefreshToken)
		if err != nil {
			return ApiResponse{}, fmt.Errorf("%w: %s: %v", ErrReauthorizationRequired, c.ConnectionId, err)
		}
		c.AccessToken = fresh.AccessToken
		c.RefreshToken = fresh.RefreshToken // rotation: store the new one, the old one is now dead
		c.RefreshCount++
		r = call(c.AccessToken)
	}
	if r.Status == 403 {
		return ApiResponse{}, &OAuthError{"insufficient_scope", "consent does not include the scope this call needs"}
	}
	if r.Status != 200 {
		return ApiResponse{}, fmt.Errorf("unexpected status %d", r.Status)
	}
	return r, nil
}

// ---------- Demo ----------

func BankA(auth *AuthorizationServer) *FdxStyleBank {
	a := NewFdxStyleBank(auth, 2)
	a.AddAccount("acc-chk-1", "Everyday Checking", "USD", "CHECKING", "****4321")
	a.AddAccount("acc-sav-1", "Rainy Day", "USD", "SAVINGS", "****8765")
	a.AddAccount("acc-cd-1", "12mo CD", "USD", "CD", "****1111")
	a.AddTransaction("acc-chk-1", "t-1", "2026-03-02", "-42.17", "GROCERY MART", "POSTED")
	a.AddTransaction("acc-chk-1", "t-2", "2026-03-03", "2500.00", "PAYROLL ACME", "POSTED")
	a.AddTransaction("acc-chk-1", "t-3", "2026-03-04", "-1200.00", "RENT", "POSTED")
	a.AddTransaction("acc-chk-1", "t-4", "2026-03-05", "-9.99", "STREAMING", "PENDING")
	a.AddTransaction("acc-chk-1", "t-5", "2026-03-05", "-61.40", "FUEL", "POSTED")
	return a
}

func BankB(auth *AuthorizationServer) *UkObStyleBank {
	b := NewUkObStyleBank(auth, 2)
	b.AddAccount("22289", "Main Current", "GBP", "CurrentAccount", "80200110203345")
	b.AddAccount("31820", "Holiday Savings", "GBP", "Savings", "80200110203346")
	b.AddTransaction("22289", "123", "2026-03-02", "10.00", "Credit", "Cash from Aubrey", "Booked")
	b.AddTransaction("22289", "124", "2026-03-03", "36.50", "Debit", "TFL TRAVEL", "Booked")
	b.AddTransaction("22289", "125", "2026-03-04", "1800.00", "Credit", "SALARY", "Booked")
	b.AddTransaction("22289", "126", "2026-03-05", "4.20", "Debit", "COFFEE", "Pending")
	return b
}

// Connect runs the three-legged flow end to end: build the request, let the bank authorize, exchange the code.
func Connect(agg *Aggregator, auth *AuthorizationServer, bank BankApi, adapter BankAdapter, user string, scopes ScopeSet, verbose bool) (*Connection, error) {
	p := agg.BeginAuthorization(scopes)
	url := agg.AuthorizationUrl("https://"+bank.BankId()+".example/authorize", p)
	if verbose {
		fmt.Println("1. aggregator redirects user to:\n   " + url)
		fmt.Printf("   verifier stays server-side: %s... (%d chars)\n", p.CodeVerifier[:12], len(p.CodeVerifier))
	}
	code, err := auth.Authorize(ClientId, RedirectUri, scopes, p.CodeChallenge, "S256", user)
	if err != nil {
		return nil, err
	}
	if verbose {
		fmt.Printf("2. user authenticates at the bank, approves scopes; bank redirects back with code=%s... state=%s\n", code[:12], p.State)
	}
	c, err := agg.CompleteAuthorization(user, p.State, code, auth, bank, adapter)
	if err != nil {
		return nil, err
	}
	if verbose {
		fmt.Printf("3. aggregator exchanges code + verifier for tokens: access=%s... refresh=%s...\n", c.AccessToken[:10], c.RefreshToken[:10])
	}
	return c, nil
}

func mustConnect(c *Connection, err error) *Connection {
	if err != nil {
		panic(err)
	}
	return c
}

func main() {
	clock := NewManualClock(1_772_000_000) // a fixed instant; advanced by hand below
	random := NewJavaRandom(2026)
	auth := NewAuthorizationServer(clock, random)
	auth.RegisterClient(RegisteredClient{ClientId, RedirectUri})
	agg := NewAggregator(random)
	scopes := Scopes(AccountsRead, TransactionsRead)

	fmt.Println("== Consent flow: authorization code + PKCE (RFC 6749 section 4.1, RFC 7636) ==")
	a := mustConnect(Connect(agg, auth, BankA(auth), FdxAdapter{}, "user-42", scopes, true))
	fmt.Println()

	fmt.Println("== Bank A (FDX-style, page size 2): paginated fetch, normalized ==")
	accountsA, _ := agg.FetchAccounts(a.ConnectionId)
	for _, acc := range accountsA {
		fmt.Println("   " + acc.String())
	}
	txA, _ := agg.FetchTransactions(a.ConnectionId, "acc-chk-1")
	for _, t := range txA {
		fmt.Println("   " + t.String())
	}
	fmt.Println()

	fmt.Println("== Bank B (UK Open Banking-style, Links.Next paging): same schema out ==")
	b := mustConnect(Connect(agg, auth, BankB(auth), UkObAdapter{}, "user-42", scopes, false))
	accountsB, _ := agg.FetchAccounts(b.ConnectionId)
	for _, acc := range accountsB {
		fmt.Println("   " + acc.String())
	}
	txB, _ := agg.FetchTransactions(b.ConnectionId, "22289")
	for _, t := range txB {
		fmt.Println("   " + t.String())
	}
	fmt.Println()

	fmt.Println("== Token expiry and refresh ==")
	clock.AdvanceSeconds(AccessTTLSeconds + 1)
	oldRefresh := a.RefreshToken
	before := a.RefreshCount
	refreshed, _ := agg.FetchAccounts(a.ConnectionId)
	fmt.Printf("   after %ds the access token is dead: first call got 401, refreshed %d time, retried, got %d accounts\n",
		AccessTTLSeconds+1, a.RefreshCount-before, len(refreshed))
	fmt.Println("   refresh token rotated:", oldRefresh != a.RefreshToken)
	if _, err := auth.Refresh(ClientId, oldRefresh); err != nil {
		fmt.Println("   replaying the old refresh token:", err)
	}
	fmt.Println("   the replay revoked the whole consent, so A's current access token is now dead too:", auth.Introspect(a.AccessToken) == nil)
	fmt.Println()

	fmt.Println("== Consent expiry (90 days) ==")
	clock.AdvanceDays(91)
	if _, err := agg.FetchTransactions(b.ConnectionId, "22289"); errors.Is(err, ErrReauthorizationRequired) {
		fmt.Println("   " + strings.TrimPrefix(err.Error(), ErrReauthorizationRequired.Error()+": "))
	}
	fmt.Println()

	fmt.Println("== Things the bank refuses ==")
	p := agg.BeginAuthorization(scopes)
	code, _ := auth.Authorize(ClientId, RedirectUri, scopes, p.CodeChallenge, "S256", "user-42")
	if _, err := auth.ExchangeCode(ClientId, code, RedirectUri, Pkce.GenerateVerifier(random)); err != nil {
		fmt.Println("   wrong verifier:", err)
	}
	if _, err := auth.ExchangeCode(ClientId, code, RedirectUri, p.CodeVerifier); err != nil {
		fmt.Println("   same code again (it was consumed by the failed attempt):", err)
	}
	if _, err := auth.Authorize(ClientId, "https://evil.example/callback", scopes, p.CodeChallenge, "S256", "user-42"); err != nil {
		fmt.Println("   unregistered redirect_uri:", err)
	}
	if _, err := auth.Authorize(ClientId, RedirectUri, Scopes(PaymentsWrite), p.CodeChallenge, "S256", "user-42"); err != nil {
		fmt.Println("   scope the client is not registered for:", err)
	}
}
