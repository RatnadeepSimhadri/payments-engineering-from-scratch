"""Tests for phases/11-payment-rails/05-tokenization-and-pci-scope. Run: python3 -m unittest -v"""
import unittest

from main import (AuthorizationError, FixedClock, JavaRandom, Luhn, Pan, Principal, Scope, SecurityError,
                  SystemInventory, TokenStateError, TokenStatus, TokenVault, demo_key)

CHECKOUT = Principal.of("checkout", Scope.TOKENIZE)
GATEWAY = Principal.of("gateway", Scope.DETOKENIZE)
OPS = Principal.of("ops", Scope.MANAGE)
NOBODY = Principal.of("nobody")
VISA = Pan.of("4111 1111 1111 1111")


def fresh_vault(clock: FixedClock) -> TokenVault:
    return TokenVault(demo_key(1), demo_key(2), JavaRandom(7), clock, 3600)


class PanTest(unittest.TestCase):
    def test_validation_and_masking(self):
        self.assertEqual(VISA.digits(), "4111111111111111")
        self.assertEqual(VISA.last_four(), "1111")
        self.assertEqual(VISA.bin(), "411111")
        self.assertEqual(str(VISA), "411111******1111")
        self.assertNotIn("4111111111111111", f"card={VISA}")
        self.assertNotIn("4111111111111111", repr([VISA]))
        self.assertEqual(str(Pan.of("378282246310005")), "378282*****0005")
        with self.assertRaises(ValueError):
            Pan.of("4111111111111112")
        with self.assertRaises(ValueError):
            Pan.of("41111111111")
        with self.assertRaises(ValueError):
            Pan.of("4111 1111 1111 111a")
        self.assertTrue(Luhn.is_valid("4242424242424242"))
        self.assertFalse(Luhn.is_valid("4242424242424241"))


class TokenFormatTest(unittest.TestCase):
    def test_format_preserving_luhn_invalid_idempotent(self):
        vault = fresh_vault(FixedClock(1_000_000))
        t1 = vault.tokenize(VISA, CHECKOUT)
        self.assertEqual(len(t1), 16)
        self.assertTrue(t1.endswith("1111"))
        self.assertTrue(t1.isdigit())
        self.assertNotEqual(t1, VISA.digits())
        self.assertFalse(Luhn.is_valid(t1))
        self.assertEqual(vault.tokenize(VISA, CHECKOUT), t1)
        self.assertEqual(vault.size(), 1)
        t2 = vault.tokenize(Pan.of("4242424242424242"), CHECKOUT)
        self.assertNotEqual(t2, t1)
        self.assertTrue(t2.endswith("4242"))
        self.assertEqual(vault.size(), 2)

    def test_1000_minted_tokens_are_luhn_invalid_and_distinct(self):
        many = fresh_vault(FixedClock(0))
        sample = Pan.of("4111111111111111")
        seen = set()
        for _ in range(1000):
            t = many.mint_token(sample)
            self.assertFalse(Luhn.is_valid(t))
            seen.add(t)
        self.assertEqual(len(seen), 1000)

    def test_demo_tokens_match_the_java_sequence(self):
        vault = TokenVault(demo_key(1), demo_key(2), JavaRandom(42), FixedClock(1_700_000_000), 365 * 24 * 3600)
        self.assertEqual(vault.tokenize(VISA, Principal.of("c", Scope.TOKENIZE)), "0384055893221111")
        self.assertEqual(vault.record_for("0384055893221111").nonce.hex(), "5e688e990000000000000000")


class AuthorizationAndLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.clock = FixedClock(1_000_000)
        self.vault = fresh_vault(self.clock)
        self.t1 = self.vault.tokenize(VISA, CHECKOUT)
        self.t2 = self.vault.tokenize(Pan.of("4242424242424242"), CHECKOUT)

    def test_authorization(self):
        vault, t1 = self.vault, self.t1
        self.assertEqual(vault.detokenize(t1, GATEWAY, "auth"), VISA)
        with self.assertRaises(AuthorizationError):
            vault.detokenize(t1, CHECKOUT, "auth")
        with self.assertRaises(AuthorizationError):
            vault.tokenize(VISA, NOBODY)
        with self.assertRaises(AuthorizationError):
            vault.suspend(t1, GATEWAY)
        with self.assertRaises(TokenStateError):
            vault.detokenize("0000000000001111", GATEWAY, "auth")

    def test_audit_log_never_contains_a_pan(self):
        vault, t1 = self.vault, self.t1
        with self.assertRaises(AuthorizationError):
            vault.detokenize(t1, CHECKOUT, "auth")
        audit_text = str(vault.audit_log())
        self.assertTrue(any(not e.allowed for e in vault.audit_log()))
        self.assertNotIn("4111111111111111", audit_text)
        self.assertNotIn("4242424242424242", audit_text)
        self.assertIn(t1, audit_text)

    def test_lifecycle(self):
        vault, t1, t2, clock = self.vault, self.t1, self.t2, self.clock
        vault.suspend(t1, OPS)
        self.assertIs(vault.status(t1), TokenStatus.SUSPENDED)
        with self.assertRaises(TokenStateError):
            vault.detokenize(t1, GATEWAY, "auth")
        with self.assertRaises(TokenStateError):
            vault.suspend(t1, OPS)
        vault.reactivate(t1, OPS)
        self.assertEqual(vault.detokenize(t1, GATEWAY, "auth"), VISA)
        clock.advance_seconds(3600)
        with self.assertRaises(TokenStateError):
            vault.detokenize(t1, GATEWAY, "auth")
        vault.revoke(t2, OPS)
        self.assertIs(vault.status(t2), TokenStatus.REVOKED)
        self.assertTrue(all(b == 0 for b in vault.record_for(t2).ciphertext))
        with self.assertRaises(TokenStateError):
            vault.detokenize(t2, GATEWAY, "auth")
        with self.assertRaises(TokenStateError):
            vault.revoke(t2, OPS)
        t3 = vault.tokenize(Pan.of("4242424242424242"), CHECKOUT)
        self.assertNotEqual(t3, t2)
        self.assertEqual(vault.last_four(t3), "4242")


class CryptoInvariantTest(unittest.TestCase):
    def setUp(self):
        self.v2 = fresh_vault(FixedClock(0))
        self.tok = self.v2.tokenize(VISA, CHECKOUT)
        self.rec = self.v2.record_for(self.tok)

    def test_ciphertext_shape(self):
        self.assertEqual(len(self.rec.ciphertext), 16 + 16)
        self.assertNotIn(b"4111111111111111", bytes(self.rec.ciphertext))
        self.assertEqual(self.v2.decrypt(self.rec.ciphertext, self.rec.nonce, self.tok), b"4111111111111111")

    def test_tamper_aad_and_key_binding(self):
        v2, rec, tok = self.v2, self.rec, self.tok
        tampered = bytearray(rec.ciphertext)
        tampered[3] ^= 0x01
        with self.assertRaises(SecurityError):
            v2.decrypt(tampered, rec.nonce, tok)
        tag_tampered = bytearray(rec.ciphertext)
        tag_tampered[-1] ^= 0x80
        with self.assertRaises(SecurityError):
            v2.decrypt(tag_tampered, rec.nonce, tok)
        with self.assertRaises(SecurityError):
            v2.decrypt(rec.ciphertext, rec.nonce, "9999999999991111")
        other_key = TokenVault(demo_key(99), demo_key(2), JavaRandom(7), FixedClock(0), 3600)
        with self.assertRaises(SecurityError):
            other_key.decrypt(rec.ciphertext, rec.nonce, tok)

    def test_nonces_and_index(self):
        v2 = self.v2
        n1, n2 = v2.next_nonce(), v2.next_nonce()
        self.assertNotEqual(n1, n2)
        self.assertEqual(len(n1), 12)
        self.assertEqual(v2.hmac_hex("4111111111111111"), v2.hmac_hex("4111111111111111"))
        other_index = TokenVault(demo_key(1), demo_key(3), JavaRandom(7), FixedClock(0), 3600)
        self.assertNotEqual(v2.hmac_hex("4111111111111111"), other_index.hmac_hex("4111111111111111"))
        with self.assertRaises(ValueError):
            TokenVault(bytes(16), demo_key(2), JavaRandom(1), FixedClock(0), 10)

    def test_same_plaintext_different_nonce_different_ciphertext(self):
        v2 = self.v2
        c1 = v2.encrypt(b"4111111111111111", v2.next_nonce(), "t")
        c2 = v2.encrypt(b"4111111111111111", v2.next_nonce(), "t")
        self.assertNotEqual(bytes(c1), bytes(c2))


class ScopeInventoryTest(unittest.TestCase):
    def test_only_pan_holding_systems_are_in_scope(self):
        inv = SystemInventory({"vault": True, "orders": False, "crm": False})
        self.assertEqual(inv.in_scope(), 1)
        self.assertEqual(inv.in_scope_systems(), ["vault"])


if __name__ == "__main__":
    unittest.main()
