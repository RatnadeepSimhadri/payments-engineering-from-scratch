// Lesson: phases/11-payment-rails/05-tokenization-and-pci-scope/docs/en.md
// Tokenization and PCI DSS Scope Reduction
// Run:  go run .
// Test: go test ./...
// Sources: PCI DSS v4.0.1 (Req. 3, 7, 10); PCI SSC Tokenization Product Security Guidelines (2015);
// NIST SP 800-38D (AES-GCM); ISO/IEC 7812 (PAN structure); RFC 2104 (HMAC). Go 1.22, stdlib only.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Clock is the injected time source so expiry is testable without sleeping. Seconds since the Unix epoch.
type Clock interface {
	NowEpochSeconds() int64
}

type FixedClock struct {
	now int64
}

func NewFixedClock(start int64) *FixedClock        { return &FixedClock{now: start} }
func (c *FixedClock) NowEpochSeconds() int64       { return c.now }
func (c *FixedClock) AdvanceSeconds(seconds int64) { c.now += seconds }

// Sentinel errors. Java's exception classes become values a caller tests with errors.Is.
var (
	ErrInvalidArgument = errors.New("invalid argument")                                               // IllegalArgumentException
	ErrUnauthorized    = errors.New("unauthorized")                                                   // AuthorizationException
	ErrTokenState      = errors.New("token state")                                                    // TokenStateException
	ErrAuthentication  = errors.New("ciphertext failed authentication: tampered record or wrong key") // SecurityException
)

// Pan is a primary account number that has been validated (12-19 digits, Luhn-correct) and that can never be
// printed by accident: String, and therefore %v, %s and %+v, produce the mask. The digits live in an unexported
// field reachable only through Digits(), which is the single call site reviewers grep for.
type Pan struct {
	digits string
}

func PanOf(raw string) (Pan, error) {
	s := strings.ReplaceAll(strings.ReplaceAll(raw, " ", ""), "-", "")
	if len(s) < 12 || len(s) > 19 || !allDigits(s) {
		return Pan{}, fmt.Errorf("%w: PAN must be 12-19 digits", ErrInvalidArgument)
	}
	if !LuhnValid(s) {
		return Pan{}, fmt.Errorf("%w: PAN fails the Luhn check", ErrInvalidArgument)
	}
	return Pan{s}, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Digits is the full PAN. Only the vault's encrypt path and an authorized detokenize caller should call this.
func (p Pan) Digits() string   { return p.digits }
func (p Pan) LastFour() string { return p.digits[len(p.digits)-4:] }
func (p Pan) Bin() string      { return p.digits[:6] }
func (p Pan) Length() int      { return len(p.digits) }

// Masked shows the first six and last four, as PCI DSS Requirement 3.4 allows for display; the rest is masked.
func (p Pan) Masked() string {
	return p.Bin() + strings.Repeat("*", len(p.digits)-10) + p.LastFour()
}

func (p Pan) String() string { return p.Masked() }

// GoString covers %#v, which bypasses Stringer and would otherwise print the unexported field in Go syntax.
func (p Pan) GoString() string { return p.Masked() }

// Equal is constant-time, like Java's MessageDigest.isEqual.
func (p Pan) Equal(o Pan) bool {
	return subtle.ConstantTimeCompare([]byte(p.digits), []byte(o.digits)) == 1
}

// LuhnValid is the ISO/IEC 7812 Annex B check. Also used to make tokens deliberately Luhn-invalid.
func LuhnValid(digits string) bool {
	sum, dbl := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		dbl = !dbl
	}
	return sum%10 == 0
}

type TokenStatus int

const (
	Active TokenStatus = iota
	Suspended
	Revoked
)

func (s TokenStatus) String() string { return [...]string{"ACTIVE", "SUSPENDED", "REVOKED"}[s] }

// Scope is what a caller is allowed to do. Tokenize is cheap to grant; detokenize is the privilege that matters.
type Scope int

const (
	Tokenize Scope = iota
	Detokenize
	Manage
)

type Principal struct {
	ID     string
	scopes map[Scope]bool
}

func PrincipalOf(id string, scopes ...Scope) Principal {
	p := Principal{ID: id, scopes: map[Scope]bool{}}
	for _, s := range scopes {
		p.scopes[s] = true
	}
	return p
}

func (p Principal) Has(s Scope) bool { return p.scopes[s] }

// TokenRecord is everything the vault stores per token. Note what is absent: the PAN in clear, in any field.
type TokenRecord struct {
	Token      string
	Nonce      []byte // 12 bytes, unique per key: 4 random + 8 counter
	Ciphertext []byte // AES-256-GCM output: PAN bytes || 16-byte tag; AAD = token
	LastFour   string
	PanLength  int
	CreatedAt  int64
	ExpiresAt  int64
	Status     TokenStatus
}

// AuditEntry is one line of the access log. Tokens and principals are safe to log; PANs never appear here.
type AuditEntry struct {
	At        int64
	Principal string
	Action    string
	Token     string
	Allowed   bool
	Detail    string
}

func (e AuditEntry) String() string {
	verdict := "DENY"
	if e.Allowed {
		verdict = "ALLOW"
	}
	s := fmt.Sprintf("%d %s %s %s %s", e.At, e.Principal, e.Action, e.Token, verdict)
	if e.Detail != "" {
		s += " " + e.Detail
	}
	return s
}

const (
	GCMTagBytes = 16
	NonceBytes  = 12
)

// TokenVault is the one system in the estate that holds PANs, and it holds them only under AES-GCM.
//
// Invariants:
//  1. Tokens preserve length and last four, and fail Luhn, so a token can never be mistaken for a PAN.
//  2. The same PAN tokenizes to the same token (lookup through an HMAC index, never a plaintext PAN).
//  3. Ciphertext is bound to its token through GCM additional authenticated data; moving a record breaks it.
//  4. Detokenize requires Detokenize scope and an ACTIVE, unexpired token; every attempt is audited.
//  5. Revoke destroys the ciphertext (crypto-shred); the record remains as a tombstone for audit.
type TokenVault struct {
	aead           cipher.AEAD // AES-256-GCM over the data-encrypting key (DEK)
	indexKey       []byte      // HMAC-SHA256 key for the PAN -> token index
	random         *JavaRandom // injected: seeded in tests, crypto/rand-backed in production
	clock          Clock
	ttlSeconds     int64
	byToken        map[string]*TokenRecord
	tokenByPanHmac map[string]string
	audit          []AuditEntry
	nonceCounter   int64
}

func NewTokenVault(dataKey, indexKey []byte, random *JavaRandom, clock Clock, ttlSeconds int64) (*TokenVault, error) {
	if len(dataKey) != 32 {
		return nil, fmt.Errorf("%w: data key must be 32 bytes (AES-256)", ErrInvalidArgument)
	}
	if len(indexKey) < 32 {
		return nil, fmt.Errorf("%w: index key must be at least 32 bytes", ErrInvalidArgument)
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TokenVault{aead: aead, indexKey: append([]byte(nil), indexKey...), random: random, clock: clock, ttlSeconds: ttlSeconds,
		byToken: map[string]*TokenRecord{}, tokenByPanHmac: map[string]string{}}, nil
}

// ---- tokenize -------------------------------------------------------------------------------

// Tokenize returns the existing token for this PAN or mints a new one. Idempotent per PAN.
func (v *TokenVault) Tokenize(pan Pan, who Principal) (string, error) {
	if !who.Has(Tokenize) {
		v.log(who, "TOKENIZE", "-", false, "missing scope TOKENIZE")
		return "", fmt.Errorf("%w: %s may not tokenize", ErrUnauthorized, who.ID)
	}
	index := v.HmacHex(pan.Digits())
	if existing, ok := v.tokenByPanHmac[index]; ok && v.byToken[existing].Status != Revoked {
		v.log(who, "TOKENIZE", existing, true, "existing")
		return existing, nil
	}
	token, err := v.MintToken(pan)
	if err != nil {
		return "", err
	}
	now := v.clock.NowEpochSeconds()
	nonce := v.NextNonce()
	ciphertext := v.Encrypt([]byte(pan.Digits()), nonce, token)
	v.byToken[token] = &TokenRecord{Token: token, Nonce: nonce, Ciphertext: ciphertext, LastFour: pan.LastFour(),
		PanLength: pan.Length(), CreatedAt: now, ExpiresAt: now + v.ttlSeconds, Status: Active}
	v.tokenByPanHmac[index] = token
	v.log(who, "TOKENIZE", token, true, "new")
	return token, nil
}

// MintToken is format-preserving: same length, same last four, random middle, and Luhn-INVALID by construction.
func (v *TokenVault) MintToken(pan Pan) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		t := make([]byte, pan.Length())
		for i := 0; i < len(t)-4; i++ {
			t[i] = byte('0' + v.random.NextInt(10))
		}
		copy(t[len(t)-4:], pan.LastFour())
		if LuhnValid(string(t)) {
			// bump one non-preserved digit: a single-digit change always flips the Luhn sum mod 10
			pos := len(t) - 5
			t[pos] = byte('0' + (t[pos]-'0'+1)%10)
		}
		candidate := string(t)
		if _, taken := v.byToken[candidate]; !taken {
			return candidate, nil
		}
	}
	return "", errors.New("token space exhausted for this last-four")
}

// ---- detokenize -----------------------------------------------------------------------------

func (v *TokenVault) Detokenize(token string, who Principal, purpose string) (Pan, error) {
	if !who.Has(Detokenize) {
		v.log(who, "DETOKENIZE", token, false, "missing scope DETOKENIZE")
		return Pan{}, fmt.Errorf("%w: %s may not detokenize", ErrUnauthorized, who.ID)
	}
	rec, ok := v.byToken[token]
	if !ok {
		v.log(who, "DETOKENIZE", token, false, "unknown token")
		return Pan{}, fmt.Errorf("%w: unknown token", ErrTokenState)
	}
	if rec.Status != Active {
		v.log(who, "DETOKENIZE", token, false, "status "+rec.Status.String())
		return Pan{}, fmt.Errorf("%w: token is %v", ErrTokenState, rec.Status)
	}
	if v.clock.NowEpochSeconds() >= rec.ExpiresAt {
		v.log(who, "DETOKENIZE", token, false, "expired")
		return Pan{}, fmt.Errorf("%w: token expired", ErrTokenState)
	}
	plain, err := v.Decrypt(rec.Ciphertext, rec.Nonce, token)
	if err != nil {
		return Pan{}, err
	}
	v.log(who, "DETOKENIZE", token, true, "purpose="+purpose)
	return PanOf(string(plain))
}

// ---- lifecycle ------------------------------------------------------------------------------

func (v *TokenVault) Suspend(token string, who Principal) error {
	return v.transition(token, who, Active, Suspended, "SUSPEND")
}

func (v *TokenVault) Reactivate(token string, who Principal) error {
	return v.transition(token, who, Suspended, Active, "REACTIVATE")
}

// Revoke is a crypto-shred: zero the ciphertext so no future key compromise can recover the PAN.
func (v *TokenVault) Revoke(token string, who Principal) error {
	rec, err := v.require(token, who, "REVOKE")
	if err != nil {
		return err
	}
	if rec.Status == Revoked {
		return fmt.Errorf("%w: already revoked", ErrTokenState)
	}
	for i := range rec.Ciphertext {
		rec.Ciphertext[i] = 0
	}
	rec.Status = Revoked
	for k, t := range v.tokenByPanHmac {
		if t == token {
			delete(v.tokenByPanHmac, k)
		}
	}
	v.log(who, "REVOKE", token, true, "ciphertext shredded")
	return nil
}

func (v *TokenVault) transition(token string, who Principal, from, to TokenStatus, action string) error {
	rec, err := v.require(token, who, action)
	if err != nil {
		return err
	}
	if rec.Status != from {
		return fmt.Errorf("%w: cannot %s a token that is %v", ErrTokenState, action, rec.Status)
	}
	rec.Status = to
	v.log(who, action, token, true, "")
	return nil
}

func (v *TokenVault) require(token string, who Principal, action string) (*TokenRecord, error) {
	if !who.Has(Manage) {
		v.log(who, action, token, false, "missing scope MANAGE")
		return nil, fmt.Errorf("%w: %s may not %s", ErrUnauthorized, who.ID, strings.ToLower(action))
	}
	rec, ok := v.byToken[token]
	if !ok {
		return nil, fmt.Errorf("%w: unknown token", ErrTokenState)
	}
	return rec, nil
}

func (v *TokenVault) Status(token string) (TokenStatus, error) {
	rec, ok := v.byToken[token]
	if !ok {
		return 0, fmt.Errorf("%w: unknown token", ErrTokenState)
	}
	return rec.Status, nil
}

// LastFour is what a token-only system may know: the last four, for "card ending 1111" on a receipt.
func (v *TokenVault) LastFour(token string) string        { return v.byToken[token].LastFour }
func (v *TokenVault) Size() int                           { return len(v.byToken) }
func (v *TokenVault) AuditLog() []AuditEntry              { return append([]AuditEntry(nil), v.audit...) }
func (v *TokenVault) RecordFor(token string) *TokenRecord { return v.byToken[token] } // exposed for tests of the tamper invariant

// ---- crypto ---------------------------------------------------------------------------------

// NextNonce is a 96-bit nonce: 4 random bytes || 64-bit counter. Never repeats under one key within this process.
func (v *TokenVault) NextNonce() []byte {
	buf := make([]byte, NonceBytes)
	binary.BigEndian.PutUint32(buf[:4], uint32(v.random.NextInt32()))
	binary.BigEndian.PutUint64(buf[4:], uint64(v.nonceCounter))
	v.nonceCounter++
	return buf
}

func (v *TokenVault) Encrypt(plaintext, nonce []byte, aad string) []byte {
	return v.aead.Seal(nil, nonce, plaintext, []byte(aad))
}

func (v *TokenVault) Decrypt(ciphertext, nonce []byte, aad string) ([]byte, error) {
	plain, err := v.aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrAuthentication
	}
	return plain, nil
}

// HmacHex is the keyed hash of the PAN for the dedup index. Unkeyed SHA-256 would be brute-forceable: 10^15 PANs per BIN.
func (v *TokenVault) HmacHex(pan string) string {
	mac := hmac.New(sha256.New, v.indexKey)
	mac.Write([]byte(pan))
	return hex.EncodeToString(mac.Sum(nil))
}

func (v *TokenVault) log(who Principal, action, token string, allowed bool, detail string) {
	v.audit = append(v.audit, AuditEntry{v.clock.NowEpochSeconds(), who.ID, action, token, allowed, detail})
}

// SystemInventory records which systems hold PAN, and therefore which are inside the PCI DSS cardholder data environment.
type SystemInventory struct {
	SeesPan map[string]bool
}

func (s SystemInventory) InScope() int { return len(s.InScopeSystems()) }

func (s SystemInventory) InScopeSystems() []string {
	out := []string{}
	for name, sees := range s.SeesPan {
		if sees {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// DemoKey is demo only; production keys come from an HSM or KMS.
func DemoKey(seed int64) []byte {
	k := make([]byte, 32)
	NewJavaRandom(seed).NextBytes(k)
	return k
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	clock := NewFixedClock(1_700_000_000)
	vault := must(NewTokenVault(DemoKey(1), DemoKey(2), NewJavaRandom(42), clock, 365*24*3600))
	checkout := PrincipalOf("checkout-api", Tokenize)
	gateway := PrincipalOf("auth-gateway", Detokenize)
	ops := PrincipalOf("vault-ops", Manage)

	fmt.Println("== Tokenize test PANs ==")
	visa := must(PanOf("4111 1111 1111 1111"))
	mc := must(PanOf("5555555555554444"))
	amex := must(PanOf("378282246310005"))
	tVisa := must(vault.Tokenize(visa, checkout))
	tMc := must(vault.Tokenize(mc, checkout))
	tAmex := must(vault.Tokenize(amex, checkout))
	fmt.Printf("PAN %v -> token %s (Luhn valid? %v)\n", visa, tVisa, LuhnValid(tVisa))
	fmt.Printf("PAN %v -> token %s (Luhn valid? %v)\n", mc, tMc, LuhnValid(tMc))
	fmt.Printf("PAN %v -> token %s (length %d)\n", amex, tAmex, len(tAmex))
	fmt.Printf("same PAN again -> %s (idempotent)\n", must(vault.Tokenize(visa, checkout)))

	fmt.Println("\n== What is stored ==")
	rec := vault.RecordFor(tVisa)
	fmt.Printf("nonce      = %s\n", hex.EncodeToString(rec.Nonce))
	fmt.Printf("ciphertext = %s (%d bytes = 16 PAN + 16 tag)\n", hex.EncodeToString(rec.Ciphertext), len(rec.Ciphertext))
	fmt.Printf("last four  = %s, status = %v\n", rec.LastFour, rec.Status)

	fmt.Println("\n== Detokenize: authorization decides ==")
	back := must(vault.Detokenize(tVisa, gateway, "authorization"))
	fmt.Printf("auth-gateway detokenized %s -> %v (equals original: %v)\n", tVisa, back, back.Equal(visa))
	if _, err := vault.Detokenize(tVisa, checkout, "curiosity"); errors.Is(err, ErrUnauthorized) {
		fmt.Println("checkout-api denied:", strings.TrimPrefix(err.Error(), ErrUnauthorized.Error()+": "))
	}

	fmt.Println("\n== Lifecycle ==")
	must(0, vault.Suspend(tMc, ops))
	if _, err := vault.Detokenize(tMc, gateway, "authorization"); errors.Is(err, ErrTokenState) {
		fmt.Println("suspended:", strings.TrimPrefix(err.Error(), ErrTokenState.Error()+": "))
	}
	must(0, vault.Reactivate(tMc, ops))
	clock.AdvanceSeconds(366 * 24 * 3600)
	if _, err := vault.Detokenize(tMc, gateway, "authorization"); errors.Is(err, ErrTokenState) {
		fmt.Println("after 366 days:", strings.TrimPrefix(err.Error(), ErrTokenState.Error()+": "))
	}
	must(0, vault.Revoke(tAmex, ops))
	fmt.Printf("revoked %s: ciphertext now %s...\n", tAmex, hex.EncodeToString(vault.RecordFor(tAmex).Ciphertext)[:16])

	fmt.Println("\n== Audit log (tokens, never PANs) ==")
	for _, e := range vault.AuditLog() {
		fmt.Println("  " + e.String())
	}

	fmt.Println("\n== PCI scope: before and after ==")
	before := map[string]bool{}
	for _, s := range []string{"web-checkout", "order-db", "auth-gateway", "analytics", "crm", "support-tool", "backup", "token-vault"} {
		before[s] = true
	}
	after := map[string]bool{}
	for k, v := range before {
		after[k] = v
	}
	for _, s := range []string{"order-db", "analytics", "crm", "support-tool", "backup"} {
		after[s] = false
	}
	b, a := SystemInventory{before}, SystemInventory{after}
	fmt.Printf("systems storing PAN before tokenization: %d %s\n", b.InScope(), javaList(b.InScopeSystems()))
	fmt.Printf("systems storing PAN after tokenization:  %d %s\n", a.InScope(), javaList(a.InScopeSystems()))
}

// javaList renders a slice the way Java's List.toString does, so the demo output lines up.
func javaList(items []string) string { return "[" + strings.Join(items, ", ") + "]" }
