"""Tests for phases/11-payment-rails/13-open-banking-and-account-aggregation. Run: python3 -m unittest -v"""
import re
import unittest

from main import (
    GBP, USD, AccountType, Aggregator, AuthorizationServer, FdxAdapter, JavaRandom, ManualClock, Money, OAuthException,
    Pkce, ReauthorizationRequiredException, RegisteredClient, Scope, UkObAdapter, bank_a, bank_b, connect,
)

READ = frozenset({Scope.ACCOUNTS_READ, Scope.TRANSACTIONS_READ})


def oauth_error(action) -> str:
    try:
        action()
        return "none"
    except OAuthException as e:
        return e.error


class World:
    """A fresh world: clock, auth server, aggregator. Each test group builds its own so they cannot interfere."""

    def __init__(self) -> None:
        self.clock = ManualClock(1_772_000_000)
        random = JavaRandom(7)
        self.auth = AuthorizationServer(self.clock, random)
        self.auth.register_client(RegisteredClient(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI))
        self.agg = Aggregator(random)
        self.bank_a = bank_a(self.auth)
        self.bank_b = bank_b(self.auth)

    def connect_a(self, scopes):
        return connect(self.agg, self.auth, self.bank_a, FdxAdapter(), "u1", scopes, False)

    def connect_b(self, scopes):
        return connect(self.agg, self.auth, self.bank_b, UkObAdapter(), "u1", scopes, False)


class PkceTest(unittest.TestCase):
    def test_pkce(self):
        rnd = JavaRandom(1)
        verifier = Pkce.generate_verifier(rnd)
        self.assertEqual(len(verifier), 64)
        self.assertIsNotNone(re.fullmatch(r"[A-Za-z0-9._~-]+", verifier))
        challenge = Pkce.challenge_s256(verifier)
        self.assertEqual(len(challenge), 43)
        self.assertNotIn("=", challenge)
        self.assertEqual(Pkce.challenge_s256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
                         "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", "RFC 7636 appendix B vector")
        self.assertTrue(Pkce.verify(verifier, challenge))
        self.assertFalse(Pkce.verify(Pkce.generate_verifier(rnd), challenge))
        self.assertFalse(Pkce.verify("short", Pkce.challenge_s256("short")))


class AuthorizationCodeGrantTest(unittest.TestCase):
    def test_happy_path(self):
        w = World()
        p = w.agg.begin_authorization(READ)
        url = w.agg.authorization_url("https://bank.example/authorize", p)
        self.assertIn("code_challenge=" + p.code_challenge, url)
        self.assertIn("state=" + p.state, url)
        self.assertIn("code_challenge_method=S256", url)
        self.assertIn("accounts:read%20transactions:read", url)
        self.assertNotIn(p.code_verifier, url, "authorize url never carries the verifier")
        code = w.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p.code_challenge, "S256", "u1")
        c = w.agg.complete_authorization("u1", p.state, code, w.auth, w.bank_a, FdxAdapter())
        grant = w.auth.introspect(c.access_token)
        self.assertIsNotNone(grant)
        self.assertEqual(grant.scopes, READ)
        self.assertEqual(AuthorizationServer.ACCESS_TTL_SECONDS, 300)

    def test_failures(self):
        w2 = World()
        rnd = JavaRandom(1)
        p2 = w2.agg.begin_authorization(READ)
        code2 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p2.code_challenge, "S256", "u1")
        self.assertEqual(oauth_error(lambda: w2.auth.exchange_code(Aggregator.CLIENT_ID, code2, Aggregator.REDIRECT_URI,
                                                                   Pkce.generate_verifier(rnd))), "invalid_grant")
        self.assertEqual(oauth_error(lambda: w2.auth.exchange_code(Aggregator.CLIENT_ID, code2, Aggregator.REDIRECT_URI,
                                                                   p2.code_verifier)), "invalid_grant",
                         "code is single use even after a failed exchange")
        p3 = w2.agg.begin_authorization(READ)
        code3 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p3.code_challenge, "S256", "u1")
        self.assertEqual(oauth_error(lambda: w2.auth.exchange_code(Aggregator.CLIENT_ID, code3, "https://other.example/cb",
                                                                   p3.code_verifier)), "invalid_grant")
        p4 = w2.agg.begin_authorization(READ)
        code4 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p4.code_challenge, "S256", "u1")
        w2.clock.advance_seconds(AuthorizationServer.CODE_TTL_SECONDS)
        self.assertEqual(oauth_error(lambda: w2.auth.exchange_code(Aggregator.CLIENT_ID, code4, Aggregator.REDIRECT_URI,
                                                                   p4.code_verifier)), "invalid_grant", "code expires after 10 minutes")
        self.assertEqual(oauth_error(lambda: w2.auth.authorize(Aggregator.CLIENT_ID, "https://evil.example/cb", READ,
                                                               p4.code_challenge, "S256", "u1")), "invalid_request")
        self.assertEqual(oauth_error(lambda: w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ,
                                                               p4.code_challenge, "plain", "u1")), "invalid_request")
        self.assertEqual(oauth_error(lambda: w2.auth.authorize("someone-else", Aggregator.REDIRECT_URI, READ,
                                                               p4.code_challenge, "S256", "u1")), "invalid_client")
        self.assertEqual(oauth_error(lambda: w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI,
                                                               frozenset({Scope.PAYMENTS_WRITE}), p4.code_challenge, "S256", "u1")),
                         "invalid_scope")
        p5 = w2.agg.begin_authorization(READ)
        code5 = w2.auth.authorize(Aggregator.CLIENT_ID, Aggregator.REDIRECT_URI, READ, p5.code_challenge, "S256", "u1")
        self.assertEqual(oauth_error(lambda: w2.agg.complete_authorization("u1", "forged-state", code5, w2.auth, w2.bank_a,
                                                                           FdxAdapter())), "invalid_request", "CSRF")


class PaginationAndNormalizationTest(unittest.TestCase):
    def setUp(self):
        self.w = World()

    def test_bank_a_fdx(self):
        w = self.w
        a = w.connect_a(READ)
        accounts = w.agg.fetch_accounts(a.connection_id)
        self.assertEqual(len(accounts), 3, "3 accounts across 2 pages")
        self.assertEqual(accounts[0].type, AccountType.CHECKING)
        self.assertEqual(accounts[2].type, AccountType.OTHER, "CD -> OTHER")
        tx = w.agg.fetch_transactions(a.connection_id, "acc-chk-1")
        self.assertEqual(len(tx), 5)
        self.assertEqual(tx[0].transaction_id, "t-1")
        self.assertEqual(tx[4].transaction_id, "t-5")
        self.assertEqual(tx[0].amount, Money(-4217, USD))
        self.assertTrue(tx[3].pending)
        self.assertFalse(tx[0].pending)
        self.assertEqual(a.refresh_count, 0)
        raw = w.bank_a.accounts(a.access_token, None)
        self.assertEqual(raw.body["page"].get("nextOffset"), "2")

    def test_bank_b_ukob_and_common_sign(self):
        w = self.w
        a = w.connect_a(READ)
        tx_a = w.agg.fetch_transactions(a.connection_id, "acc-chk-1")
        b = w.connect_b(READ)
        accounts = w.agg.fetch_accounts(b.connection_id)
        self.assertEqual(len(accounts), 2)
        self.assertEqual(accounts[0].type, AccountType.CHECKING)
        self.assertEqual(accounts[0].masked_identifier, "****3345")
        tx_b = w.agg.fetch_transactions(b.connection_id, "22289")
        self.assertEqual(len(tx_b), 4, "4 transactions across 2 pages")
        self.assertEqual(tx_b[1].amount, Money(-3650, GBP), "Debit indicator becomes a negative amount")
        self.assertEqual(tx_b[0].amount, Money(1000, GBP), "Credit indicator stays positive")
        self.assertTrue(tx_b[3].pending)
        self.assertTrue(tx_a[0].amount.minor_units < 0 and tx_b[1].amount.minor_units < 0
                        and tx_a[1].amount.minor_units > 0 and tx_b[2].amount.minor_units > 0,
                        "both banks normalize to one sign convention")


class RefreshAndRotationTest(unittest.TestCase):
    def test_expiry_refresh_rotation_replay(self):
        w4 = World()
        a4 = w4.connect_a(READ)
        first_access, first_refresh = a4.access_token, a4.refresh_token
        w4.clock.advance_seconds(AuthorizationServer.ACCESS_TTL_SECONDS - 1)
        w4.agg.fetch_accounts(a4.connection_id)
        self.assertEqual(a4.refresh_count, 0)
        self.assertEqual(a4.access_token, first_access, "one second before expiry no refresh happens")
        w4.clock.advance_seconds(2)
        expired = w4.bank_a.accounts(first_access, None)
        self.assertEqual(expired.status, 401)
        self.assertEqual(expired.auth_error, "invalid_token")
        after = w4.agg.fetch_accounts(a4.connection_id)
        self.assertEqual(a4.refresh_count, 1, "aggregator refreshed exactly once and retried")
        self.assertEqual(len(after), 3)
        self.assertNotEqual(a4.access_token, first_access)
        self.assertNotEqual(a4.refresh_token, first_refresh, "refresh rotated both tokens")
        self.assertEqual(oauth_error(lambda: w4.auth.refresh(Aggregator.CLIENT_ID, first_refresh)), "invalid_grant")
        self.assertIsNone(w4.auth.introspect(a4.access_token), "replay revokes the whole family")
        with self.assertRaises(ReauthorizationRequiredException):
            w4.agg.fetch_accounts(a4.connection_id)

    def test_consent_lifetime(self):
        w5 = World()
        b5 = w5.connect_b(READ)
        w5.clock.advance_days(89)
        self.assertEqual(len(w5.agg.fetch_accounts(b5.connection_id)), 2)
        self.assertEqual(b5.refresh_count, 1, "day 89: refresh still works")
        w5.clock.advance_days(2)
        with self.assertRaises(ReauthorizationRequiredException):
            w5.agg.fetch_accounts(b5.connection_id)


class ScopeTest(unittest.TestCase):
    def test_scopes(self):
        w6 = World()
        narrow = w6.connect_a(frozenset({Scope.ACCOUNTS_READ}))
        self.assertEqual(len(w6.agg.fetch_accounts(narrow.connection_id)), 3)
        self.assertEqual(oauth_error(lambda: w6.agg.fetch_transactions(narrow.connection_id, "acc-chk-1")), "insufficient_scope")
        self.assertEqual(narrow.refresh_count, 0, "403 is not retried with a refresh")
        self.assertEqual(oauth_error(lambda: Scope.from_wire("admin:all")), "invalid_scope")


class DeterminismTest(unittest.TestCase):
    def test_seeded_worlds_agree(self):
        d1, d2 = World(), World()
        self.assertEqual(d1.connect_a(READ).access_token, d2.connect_a(READ).access_token)

    def test_java_random_sequence(self):
        """The 48-bit LCG reproduces java.util.Random: these values come from `new Random(2026)` on the JDK."""
        r = JavaRandom(2026)
        self.assertEqual([r.next_int(66), r.next_int(100), r.next_int()], [29, 50, -647640901])


if __name__ == "__main__":
    unittest.main()
