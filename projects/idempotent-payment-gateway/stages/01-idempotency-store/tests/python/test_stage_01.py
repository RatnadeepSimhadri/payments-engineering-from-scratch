"""Stage 1 tests for projects/idempotent-payment-gateway (Python): SHA-256 fingerprints, canonical form, and the
NEW / IN_PROGRESS / REPLAY / CONFLICT decisions of the idempotency store with TTL on an injected clock.
Port of Stage01Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    USD,
    ApiResponse,
    ChargeRequest,
    Decision,
    FakeClock,
    Fingerprint,
    IdempotencyStore,
    KeyState,
    Money,
)


def s1_request(key: str, amount: str) -> ChargeRequest:
    return ChargeRequest("m_1", key, Money.parse(amount, USD), "tok_visa", "order-42")


class Stage01FingerprintTest(unittest.TestCase):
    def test_fingerprints_sha256_over_a_canonical_string_that_excludes_the_key(self):
        self.assertEqual(Fingerprint.sha256_hex("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "sha256 of abc")
        self.assertEqual(Fingerprint.sha256_hex(""), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "sha256 of empty string")
        req = s1_request("key-1", "19.99")
        self.assertEqual(req.canonical(), "1999\nUSD\ntok_visa\norder-42", "canonical form")
        self.assertEqual(req.fingerprint(), Fingerprint.sha256_hex(req.canonical()), "fingerprint is sha256 of canonical")
        self.assertEqual(req.fingerprint(), "485e792d29b86326c3335f0b5e84d3a948d47121f7e4c4b57f889d7287e90b91", "fingerprint known value")
        self.assertEqual(s1_request("key-2", "19.99").fingerprint(), req.fingerprint(), "fingerprint ignores the key")
        self.assertNotEqual(s1_request("key-1", "29.99").fingerprint(), req.fingerprint(), "fingerprint changes with amount")
        self.assertNotEqual(ChargeRequest("m_1", "key-1", Money.parse("19.99", EUR), "tok_visa", "order-42").fingerprint(), req.fingerprint(), "fingerprint changes with currency")
        self.assertNotEqual(ChargeRequest("m_1", "key-1", Money.parse("19.99", USD), "tok_visa", "order-43").fingerprint(), req.fingerprint(), "fingerprint changes with description")

    def test_request_validation(self):
        with self.assertRaises(ValueError, msg="zero amount rejected"):
            s1_request("key-1", "0.00")
        with self.assertRaises(ValueError, msg="negative amount rejected"):
            s1_request("key-1", "-1.00")
        with self.assertRaises(ValueError, msg="blank key rejected"):
            ChargeRequest("m_1", " ", Money.parse("1.00", USD), "tok_visa", "x")
        with self.assertRaises(ValueError, msg="newline in description rejected"):
            ChargeRequest("m_1", "k", Money.parse("1.00", USD), "tok_visa", "a\nb")


class Stage01StoreTest(unittest.TestCase):
    def test_store_decisions_misuse_and_ttl(self):
        req = s1_request("key-1", "19.99")
        clock = FakeClock(1_000_000)
        store = IdempotencyStore(clock, 60_000)
        with self.assertRaises(ValueError, msg="ttl must be positive"):
            IdempotencyStore(clock, 0)
        self.assertIs(store.begin("m_1", "key-1", req.fingerprint()).decision, Decision.NEW, "first begin is NEW")
        self.assertIsNone(store.begin("m_1", "key-9", req.fingerprint()).response, "NEW carries no response")
        self.assertIs(store.begin("m_1", "key-1", req.fingerprint()).decision, Decision.IN_PROGRESS, "second begin while open is IN_PROGRESS")
        rec = store.get("m_1", "key-1")
        self.assertTrue(rec is not None and rec.state is KeyState.IN_PROGRESS, "record is IN_PROGRESS")
        self.assertTrue(rec is not None and rec.expires_at == 1_060_000 and rec.created_at == 1_000_000, "record expiry is created + ttl")
        self.assertIs(store.begin("m_2", "key-1", req.fingerprint()).decision, Decision.NEW, "different merchant, same key is NEW")
        self.assertIs(store.begin("m_1", "key-1", s1_request("key-1", "29.99").fingerprint()).decision, Decision.CONFLICT, "different fingerprint while open is CONFLICT")

        stored = ApiResponse(201, '{"payment_id":"pay_1"}')
        store.complete("m_1", "key-1", stored)
        replay = store.begin("m_1", "key-1", req.fingerprint())
        self.assertIs(replay.decision, Decision.REPLAY, "completed key replays")
        self.assertTrue(replay.response is not None and replay.response.status == 201 and replay.response.body == stored.body, "replay returns the stored status and body")
        self.assertTrue(replay.response is not None and not replay.response.replayed, "stored response is not marked replayed")
        self.assertTrue(replay.response.as_replay().replayed and replay.response.as_replay().body == stored.body, "as_replay flags the copy")
        self.assertIs(store.begin("m_1", "key-1", req.fingerprint()).decision, Decision.REPLAY, "replay is stable")
        self.assertIs(store.begin("m_1", "key-1", s1_request("key-1", "29.99").fingerprint()).decision, Decision.CONFLICT, "different fingerprint after completion is CONFLICT")
        rec = store.get("m_1", "key-1")
        self.assertTrue(rec is not None and rec.state is KeyState.COMPLETED, "record is COMPLETED")

        # complete / release misuse
        with self.assertRaises(RuntimeError, msg="complete twice"):
            store.complete("m_1", "key-1", stored)
        with self.assertRaises(RuntimeError, msg="complete unknown key"):
            store.complete("m_1", "nope", stored)
        with self.assertRaises(RuntimeError, msg="release completed key"):
            store.release("m_1", "key-1")
        self.assertFalse(store.release("m_1", "nope"), "release unknown key is False")
        self.assertTrue(store.release("m_1", "key-9"), "release in-progress key is True")
        self.assertIsNone(store.get("m_1", "key-9"), "released key is absent")
        self.assertIs(store.begin("m_1", "key-9", req.fingerprint()).decision, Decision.NEW, "released key can be claimed again")

        # TTL on the injected clock
        clock.advance(59_999)
        self.assertIs(store.begin("m_1", "key-1", req.fingerprint()).decision, Decision.REPLAY, "one ms before expiry still replays")
        clock.advance(1)
        self.assertIsNone(store.get("m_1", "key-1"), "expired record reads as absent")
        self.assertIs(store.begin("m_1", "key-1", s1_request("key-1", "29.99").fingerprint()).decision, Decision.NEW, "expired key begins fresh as NEW")
        rec = store.get("m_1", "key-1")
        self.assertTrue(rec is not None and rec.fingerprint == s1_request("key-1", "29.99").fingerprint(), "fresh record has the new fingerprint")
        self.assertEqual(store.size(), 3, "size counts stale records")
        purged = store.purge_expired()
        self.assertEqual(purged, 2, "purge removes exactly the expired records")
        self.assertEqual(store.size(), 1, "size after purge")
        clock.advance(60_000)
        self.assertTrue(store.purge_expired() == 1 and store.size() == 0, "purge removes everything once all expire")


if __name__ == "__main__":
    unittest.main()
