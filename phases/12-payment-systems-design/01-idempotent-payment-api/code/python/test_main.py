"""Tests for phases/12-payment-systems-design/01-idempotent-payment-api. Run: python3 -m unittest -v"""

import threading
import unittest
from collections import Counter

from main import (
    EUR,
    KEY_TTL_MILLIS,
    LEASE_MILLIS,
    RETRY_AFTER_SECONDS,
    USD,
    CurrencyMismatchError,
    FakeClock,
    FakeProcessor,
    IdempotencyStore,
    KeyState,
    Money,
    PaymentRequest,
    PaymentResponse,
    PaymentsHandler,
    Reservation,
    SimulatedCrash,
    reference_id_for,
    scoped_key,
)

M = "merchant_acme"
ORDER = PaymentRequest(Money.parse("19.99", USD), "tok_visa_4242", "Order 1001")


class Fixture:
    def __init__(self) -> None:
        self.clock = FakeClock(1_700_000_000_000)
        self.processor = FakeProcessor()
        self.store = IdempotencyStore(self.clock, KEY_TTL_MILLIS, LEASE_MILLIS)
        self.api = PaymentsHandler(self.store, self.processor)


class FingerprintTest(unittest.TestCase):
    def test_canonical_body_sorts_fields(self):
        self.assertEqual(ORDER.canonical(), "amount=1999\ncurrency=USD\ndescription=Order 1001\nsource=tok_visa_4242\n")

    def test_fingerprint_matches_cross_language_vector(self):
        self.assertEqual(ORDER.fingerprint(), "a3e98e16f24b06155d4539aa040c1d1362dcaafddc3debe88ee16fb23f9a644f")

    def test_fingerprint_changes_with_amount_and_currency(self):
        self.assertNotEqual(ORDER.fingerprint(), PaymentRequest(Money.parse("20.00", USD), "tok_visa_4242", "Order 1001").fingerprint())
        self.assertNotEqual(ORDER.fingerprint(), PaymentRequest(Money.of_minor(1999, EUR), "tok_visa_4242", "Order 1001").fingerprint())

    def test_scoped_key_uses_control_separator(self):
        self.assertEqual(scoped_key(M, "key-1001"), "merchant_acme\u001fkey-1001")

    def test_reference_id_is_pure_function_of_scoped_key(self):
        self.assertEqual(reference_id_for(scoped_key(M, "key-1001")), "ref_349281adde6ae74d")

    def test_request_rejects_non_positive_amount(self):
        with self.assertRaises(ValueError):
            PaymentRequest(Money.of_minor(0, USD), "tok_visa_4242", "x")


class HandlerTest(unittest.TestCase):
    def test_first_call_charges_and_duplicate_replays(self):
        f = Fixture()
        first = f.api.post(M, "key-1", ORDER)
        second = f.api.post(M, "key-1", ORDER)
        self.assertEqual(first.status, 201)
        self.assertFalse(first.replayed())
        self.assertEqual(second.status, 201)
        self.assertEqual(second.body, first.body)
        self.assertTrue(second.replayed())
        self.assertEqual(f.processor.charge_calls, 1)
        self.assertEqual(f.store.size(), 1)
        self.assertIs(f.store.get(scoped_key(M, "key-1")).state, KeyState.COMPLETED)

    def test_missing_key_is_400_and_never_charges(self):
        f = Fixture()
        self.assertEqual(f.api.post(M, None, ORDER).status, 400)
        self.assertEqual(f.api.post(M, "   ", ORDER).status, 400)
        self.assertEqual(f.api.post(M, "key\u001fx", ORDER).status, 400)
        self.assertEqual(f.api.post(None, "key-1", ORDER).status, 401)
        self.assertEqual(f.processor.charge_calls, 0)

    def test_keys_are_scoped_per_merchant(self):
        f = Fixture()
        acme = f.api.post("merchant_acme", "order-1001", ORDER)
        globex = f.api.post("merchant_globex", "order-1001", ORDER)
        self.assertEqual(f.processor.charge_calls, 2)
        self.assertEqual((acme.status, globex.status), (201, 201))
        self.assertFalse(globex.replayed())
        self.assertNotEqual(acme.body, globex.body)
        self.assertEqual(f.api.post("merchant_globex", "order-1001", ORDER).body, globex.body)
        self.assertEqual(f.store.size(), 2)

    def test_reads_are_not_idempotent_operations(self):
        f = Fixture()
        created = f.api.post(M, "key-r", ORDER)
        pid = reference_id_for(scoped_key(M, "key-r"))
        read = f.api.get(pid)
        self.assertEqual((read.status, read.body), (200, created.body))
        self.assertFalse(read.replayed())
        self.assertFalse(f.api.get(pid).replayed())
        self.assertEqual(f.api.get("ref_missing").status, 404)
        self.assertEqual(f.store.size(), 1)
        self.assertEqual(f.processor.charge_calls, 1)

    def test_same_key_different_fingerprint_is_422(self):
        f = Fixture()
        f.api.post(M, "key-2", ORDER)
        changed = PaymentRequest(Money.parse("29.99", USD), "tok_visa_4242", "Order 1001")
        r = f.api.post(M, "key-2", changed)
        self.assertEqual(r.status, 422)
        self.assertFalse(r.replayed())
        self.assertEqual(f.processor.charge_calls, 1)
        reordered = PaymentRequest(Money.of_minor(1999, USD), "tok_visa_4242", "Order 1001")
        self.assertTrue(f.api.post(M, "key-2", reordered).replayed())

    def test_declines_are_stored_and_replayed(self):
        f = Fixture()
        declined = PaymentRequest(Money.parse("10.00", USD), "tok_decline_0002", "Order 7")
        d1 = f.api.post(M, "key-3", declined)
        d2 = f.api.post(M, "key-3", declined)
        self.assertEqual(d1.status, 402)
        self.assertEqual(d2.status, 402)
        self.assertTrue(d2.replayed())
        self.assertEqual(f.processor.charge_calls, 1)

    def test_concurrent_duplicate_while_in_progress_is_409(self):
        f = Fixture()
        in_flight = threading.Event()
        release = threading.Event()

        def hold() -> None:
            in_flight.set()
            release.wait()

        f.processor.before_commit = hold
        results: list[PaymentResponse] = []
        owner = threading.Thread(target=lambda: results.append(f.api.post(M, "key-4", ORDER)))
        owner.start()
        self.assertTrue(in_flight.wait(timeout=5))
        concurrent = f.api.post(M, "key-4", ORDER)
        self.assertEqual(concurrent.status, 409)
        self.assertEqual(concurrent.retry_after_seconds(), RETRY_AFTER_SECONDS)
        self.assertEqual(f.processor.charge_calls, 1)
        release.set()
        owner.join()
        f.processor.before_commit = lambda: None
        self.assertEqual(results[0].status, 201)
        self.assertTrue(f.api.post(M, "key-4", ORDER).replayed())

    def test_many_threads_one_key_exactly_one_charge(self):
        f = Fixture()
        start = threading.Barrier(32)
        statuses: Counter[int] = Counter()
        lock = threading.Lock()

        def worker() -> None:
            start.wait()
            r = f.api.post(M, "key-5", ORDER)
            with lock:
                statuses[r.status] += 1

        threads = [threading.Thread(target=worker) for _ in range(32)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertEqual(f.processor.charge_calls, 1)
        self.assertTrue(set(statuses) <= {201, 409})
        self.assertEqual(sum(statuses.values()), 32)

    def test_crash_after_commit_recovers_by_lookup(self):
        f = Fixture()
        f.processor.crash_after_next_charge()
        with self.assertRaises(SimulatedCrash):
            f.api.post(M, "key-6", ORDER)
        self.assertIs(f.store.get(scoped_key(M, "key-6")).state, KeyState.IN_PROGRESS)
        self.assertEqual(f.processor.charges_stored(), 1)
        self.assertEqual(f.api.post(M, "key-6", ORDER).status, 409)
        f.clock.advance(LEASE_MILLIS)
        recovered = f.api.post(M, "key-6", ORDER)
        self.assertEqual(recovered.status, 201)
        self.assertEqual(f.processor.charge_calls, 1)
        self.assertIn(reference_id_for(scoped_key(M, "key-6")), recovered.body)
        self.assertIs(f.store.get(scoped_key(M, "key-6")).state, KeyState.COMPLETED)
        self.assertTrue(f.api.post(M, "key-6", ORDER).replayed())

    def test_crash_before_commit_recovers_by_charging(self):
        f = Fixture()
        scoped = scoped_key(M, "key-7")
        f.store.reserve(scoped, ORDER.fingerprint(), reference_id_for(scoped))
        f.clock.advance(LEASE_MILLIS)
        r = f.api.post(M, "key-7", ORDER)
        self.assertEqual(r.status, 201)
        self.assertEqual(f.processor.charge_calls, 1)

    def test_key_ttl_24h(self):
        f = Fixture()
        f.api.post(M, "key-8", ORDER)
        f.clock.advance(KEY_TTL_MILLIS - 1)
        self.assertTrue(f.api.post(M, "key-8", ORDER).replayed())
        f.clock.advance(1)
        fresh = f.api.post(M, "key-8", ORDER)
        self.assertFalse(fresh.replayed())
        self.assertEqual(f.processor.charge_calls, 2)
        f.clock.advance(KEY_TTL_MILLIS)
        other = PaymentRequest(Money.parse("1.00", USD), "tok_visa_4242", "new order")
        self.assertEqual(f.api.post(M, "key-8", other).status, 201)


class StoreTest(unittest.TestCase):
    def test_complete_without_reserve_is_a_bug(self):
        f = Fixture()
        with self.assertRaises(RuntimeError):
            f.store.complete("never-reserved", PaymentResponse(201, "{}"))

    def test_reserve_decisions(self):
        f = Fixture()
        self.assertIs(f.store.reserve("k", "fp", "ref").decision, Reservation.RESERVED)
        self.assertIs(f.store.reserve("k", "fp", "ref").decision, Reservation.IN_PROGRESS)
        self.assertIs(f.store.reserve("k", "other", "ref").decision, Reservation.FINGERPRINT_MISMATCH)


class MoneyTest(unittest.TestCase):
    def test_money_discipline(self):
        self.assertEqual(str(Money.parse("19.99", USD)), "19.99 USD")
        with self.assertRaises(CurrencyMismatchError):
            Money.parse("1.00", USD).plus(Money.parse("1.00", EUR))
        with self.assertRaises(TypeError):
            Money(19.99, USD)  # type: ignore[arg-type]


if __name__ == "__main__":
    unittest.main()
