// Tests for phases/11-payment-rails/05-tokenization-and-pci-scope. Run: go test ./...
package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func check(t *testing.T, name string, cond bool) {
	t.Helper()
	if !cond {
		t.Error(name)
	}
}

func checkErr(t *testing.T, name string, want, got error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: want %v, got %v", name, want, got)
	}
}

func freshVault(t *testing.T, clock Clock) *TokenVault {
	t.Helper()
	return ok[*TokenVault](t)(NewTokenVault(DemoKey(1), DemoKey(2), NewJavaRandom(7), clock, 3600))
}

func pan(t *testing.T, raw string) Pan {
	t.Helper()
	return ok[Pan](t)(PanOf(raw))
}

var (
	checkout = PrincipalOf("checkout", Tokenize)
	gateway  = PrincipalOf("gateway", Detokenize)
	ops      = PrincipalOf("ops", Manage)
	nobody   = PrincipalOf("nobody")
)

func TestPanValidationAndMasking(t *testing.T) {
	visa := pan(t, "4111 1111 1111 1111")
	check(t, "PAN strips spaces", visa.Digits() == "4111111111111111")
	check(t, "PAN last four", visa.LastFour() == "1111")
	check(t, "PAN bin", visa.Bin() == "411111")
	check(t, "PAN String is masked", visa.String() == "411111******1111")
	check(t, "PAN concatenation never leaks digits", !strings.Contains("card="+visa.String(), "4111111111111111"))
	check(t, "PAN fmt verbs never leak digits", !strings.Contains(fmt.Sprintf("%v %s %+v %#v", visa, visa, visa, []Pan{visa}), "4111111111111111"))
	check(t, "Amex 15-digit masked", pan(t, "378282246310005").String() == "378282*****0005")
	for _, bad := range []struct{ name, raw string }{
		{"PAN rejects Luhn failure", "4111111111111112"},
		{"PAN rejects too short", "41111111111"},
		{"PAN rejects letters", "4111 1111 1111 111a"},
	} {
		_, err := PanOf(bad.raw)
		checkErr(t, bad.name, ErrInvalidArgument, err)
	}
	check(t, "Luhn accepts 4242424242424242", LuhnValid("4242424242424242"))
	check(t, "Luhn rejects 4242424242424241", !LuhnValid("4242424242424241"))
}

func TestTokensFormatPreservingLuhnInvalidIdempotent(t *testing.T) {
	vault := freshVault(t, NewFixedClock(1_000_000))
	visa := pan(t, "4111 1111 1111 1111")
	t1 := ok[string](t)(vault.Tokenize(visa, checkout))
	check(t, "token preserves length", len(t1) == 16)
	check(t, "token preserves last four", strings.HasSuffix(t1, "1111"))
	check(t, "token is all digits", allDigits(t1))
	check(t, "token is not the PAN", t1 != visa.Digits())
	check(t, "token fails Luhn by construction", !LuhnValid(t1))
	check(t, "same PAN -> same token", ok[string](t)(vault.Tokenize(visa, checkout)) == t1)
	check(t, "idempotent tokenize does not grow the vault", vault.Size() == 1)
	t2 := ok[string](t)(vault.Tokenize(pan(t, "4242424242424242"), checkout))
	check(t, "different PAN -> different token", t2 != t1)
	check(t, "different PAN with same last four still distinct", strings.HasSuffix(t2, "4242") && vault.Size() == 2)

	// 1000 minted tokens for the same last-four: every one fails Luhn and none collide
	many := freshVault(t, NewFixedClock(0))
	seen := map[string]bool{}
	allInvalid := true
	sample := pan(t, "4111111111111111")
	for range 1000 {
		tok := ok[string](t)(many.MintToken(sample))
		allInvalid = allInvalid && !LuhnValid(tok)
		seen[tok] = true
	}
	check(t, "1000 minted tokens all Luhn-invalid", allInvalid)
	check(t, "1000 minted tokens all distinct", len(seen) == 1000)
}

func TestAuthorizationAndAudit(t *testing.T) {
	vault := freshVault(t, NewFixedClock(1_000_000))
	visa := pan(t, "4111 1111 1111 1111")
	t1 := ok[string](t)(vault.Tokenize(visa, checkout))
	ok[string](t)(vault.Tokenize(pan(t, "4242424242424242"), checkout))
	check(t, "detokenize with scope returns the PAN", ok[Pan](t)(vault.Detokenize(t1, gateway, "auth")).Equal(visa))
	_, err := vault.Detokenize(t1, checkout, "auth")
	checkErr(t, "detokenize without scope is denied", ErrUnauthorized, err)
	_, err = vault.Tokenize(visa, nobody)
	checkErr(t, "tokenize without scope is denied", ErrUnauthorized, err)
	checkErr(t, "suspend without MANAGE is denied", ErrUnauthorized, vault.Suspend(t1, gateway))
	_, err = vault.Detokenize("0000000000001111", gateway, "auth")
	checkErr(t, "unknown token", ErrTokenState, err)

	// audit log never contains a PAN
	var sb strings.Builder
	denied := false
	for _, e := range vault.AuditLog() {
		sb.WriteString(e.String() + "\n")
		denied = denied || !e.Allowed
	}
	auditText := sb.String()
	check(t, "audit log records denials", denied)
	check(t, "audit log never contains a PAN", !strings.Contains(auditText, "4111111111111111") && !strings.Contains(auditText, "4242424242424242"))
	check(t, "audit log contains the token", strings.Contains(auditText, t1))
}

func TestLifecycle(t *testing.T) {
	clock := NewFixedClock(1_000_000)
	vault := freshVault(t, clock)
	visa := pan(t, "4111 1111 1111 1111")
	t1 := ok[string](t)(vault.Tokenize(visa, checkout))
	t2 := ok[string](t)(vault.Tokenize(pan(t, "4242424242424242"), checkout))

	ok[int](t)(0, vault.Suspend(t1, ops))
	check(t, "status after suspend", ok[TokenStatus](t)(vault.Status(t1)) == Suspended)
	_, err := vault.Detokenize(t1, gateway, "auth")
	checkErr(t, "suspended token cannot be detokenized", ErrTokenState, err)
	checkErr(t, "cannot suspend twice", ErrTokenState, vault.Suspend(t1, ops))
	ok[int](t)(0, vault.Reactivate(t1, ops))
	check(t, "reactivated token works", ok[Pan](t)(vault.Detokenize(t1, gateway, "auth")).Equal(visa))
	clock.AdvanceSeconds(3600)
	_, err = vault.Detokenize(t1, gateway, "auth")
	checkErr(t, "expired token cannot be detokenized", ErrTokenState, err)

	ok[int](t)(0, vault.Revoke(t2, ops))
	check(t, "status after revoke", ok[TokenStatus](t)(vault.Status(t2)) == Revoked)
	shredded := vault.RecordFor(t2).Ciphertext
	check(t, "revoke shreds the ciphertext", len(shredded) == 32 && bytes.Equal(shredded, make([]byte, 32)))
	_, err = vault.Detokenize(t2, gateway, "auth")
	checkErr(t, "revoked token cannot be detokenized", ErrTokenState, err)
	checkErr(t, "cannot revoke twice", ErrTokenState, vault.Revoke(t2, ops))
	t3 := ok[string](t)(vault.Tokenize(pan(t, "4242424242424242"), checkout))
	check(t, "re-tokenizing a revoked PAN mints a new token", t3 != t2)
	check(t, "last four visible without detokenizing", vault.LastFour(t3) == "4242")
	_, err = vault.Status("0000000000000000")
	checkErr(t, "status of unknown token", ErrTokenState, err)
}

func TestCryptoInvariants(t *testing.T) {
	visa := pan(t, "4111 1111 1111 1111")
	v2 := freshVault(t, NewFixedClock(0))
	tok := ok[string](t)(v2.Tokenize(visa, checkout))
	rec := v2.RecordFor(tok)
	check(t, "ciphertext is PAN length + 16-byte tag", len(rec.Ciphertext) == 16+16)
	check(t, "ciphertext does not contain the PAN bytes", !bytes.Contains(rec.Ciphertext, []byte("4111111111111111")))
	tampered := append([]byte(nil), rec.Ciphertext...)
	tampered[3] ^= 0x01
	_, err := v2.Decrypt(tampered, rec.Nonce, tok)
	checkErr(t, "flipping one ciphertext bit fails authentication", ErrAuthentication, err)
	_, err = v2.Decrypt(rec.Ciphertext, rec.Nonce, "9999999999991111")
	checkErr(t, "ciphertext bound to its token via AAD", ErrAuthentication, err)
	otherKey := ok[*TokenVault](t)(NewTokenVault(DemoKey(99), DemoKey(2), NewJavaRandom(7), NewFixedClock(0), 3600))
	_, err = otherKey.Decrypt(rec.Ciphertext, rec.Nonce, tok)
	checkErr(t, "wrong key fails authentication", ErrAuthentication, err)
	check(t, "round trip under the right key and AAD", string(ok[[]byte](t)(v2.Decrypt(rec.Ciphertext, rec.Nonce, tok))) == "4111111111111111")
	n1, n2 := v2.NextNonce(), v2.NextNonce()
	check(t, "nonces never repeat", !bytes.Equal(n1, n2))
	check(t, "nonce is 96 bits", len(n1) == 12)
	check(t, "same PAN hashes to same index", v2.HmacHex("4111111111111111") == v2.HmacHex("4111111111111111"))
	otherIndex := ok[*TokenVault](t)(NewTokenVault(DemoKey(1), DemoKey(3), NewJavaRandom(7), NewFixedClock(0), 3600))
	check(t, "different index key -> different index", v2.HmacHex("4111111111111111") != otherIndex.HmacHex("4111111111111111"))
	_, err = NewTokenVault(make([]byte, 16), DemoKey(2), NewJavaRandom(1), NewFixedClock(0), 10)
	checkErr(t, "data key must be 256-bit", ErrInvalidArgument, err)
	_, err = NewTokenVault(DemoKey(1), make([]byte, 16), NewJavaRandom(1), NewFixedClock(0), 10)
	checkErr(t, "index key must be at least 256-bit", ErrInvalidArgument, err)
}

func TestDemoDeterminism(t *testing.T) {
	// The JDK's Random(42) stream drives the demo: the Java and Python ports print these exact values.
	vault := ok[*TokenVault](t)(NewTokenVault(DemoKey(1), DemoKey(2), NewJavaRandom(42), NewFixedClock(1_700_000_000), 365*24*3600))
	tok := ok[string](t)(vault.Tokenize(pan(t, "4111111111111111"), PrincipalOf("checkout-api", Tokenize)))
	check(t, "demo token matches the Java demo", tok == "0384055893221111")
	check(t, "demo nonce matches the Java demo", fmt.Sprintf("%x", vault.RecordFor(tok).Nonce)[:8] == "5e688e99")
	check(t, "demo key matches java.util.Random(1).nextBytes", fmt.Sprintf("%x", DemoKey(1)[:4]) == "73d51abb")
}

func TestScopeInventory(t *testing.T) {
	inv := SystemInventory{map[string]bool{"vault": true, "orders": false, "crm": false}}
	systems := inv.InScopeSystems()
	check(t, "only PAN-holding systems are in scope", inv.InScope() == 1 && len(systems) == 1 && systems[0] == "vault")
}
