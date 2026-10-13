"""Tests for phases/05-distributed-foundations/09-idempotency-keys. Run: python3 -m unittest -v
Same cases as MainTest.java and main_test.go."""
import threading
import unittest

from main import (DAY_MS, EUR, USD, Begin, ChargeRequest, FakeClock, FakeProcessor, IdempotencyStore, Money,
                  Outcome, PaymentsApi, Response)

EXPECTED_FINGERPRINT = "795fe63bfb540da3cf414455d7494e879034c285ddc8d254e3b0ec2c396cb96f"


def request(key: str | None, cents: int, account: str = "acct_1") -> ChargeRequest:
    return ChargeRequest(account, key, Money.of_minor(cents, USD), "tok_visa_4242", "Order 8812")


class BlockingProcessor:
    """Blocks inside charge() until released, so concurrent duplicates really overlap."""

    def __init__(self) -> None:
        self.release = threading.Event()
        self.executions = 0
        self._lock = threading.Lock()

    def charge(self, req: ChargeRequest) -> Response:
        with self._lock:
            self.executions += 1
        self.release.wait()
        return Response(200, '{"id":"ch_blocking"}')


class FingerprintTest(unittest.TestCase):
    def test_matches_java_and_go(self):
        self.assertEqual(request("k", 1999).fingerprint(), EXPECTED_FINGERPRINT)

    def test_ignores_key_and_account(self):
        self.assertEqual(request("other-key", 1999).fingerprint(), EXPECTED_FINGERPRINT)
        self.assertEqual(request("k", 1999, account="acct_2").fingerprint(), EXPECTED_FINGERPRINT)

    def test_changes_with_amount_and_currency(self):
        self.assertNotEqual(request("k", 2999).fingerprint(), EXPECTED_FINGERPRINT)
        eur = ChargeRequest("acct_1", "k", Money.of_minor(1999, EUR), "tok_visa_4242", "Order 8812")
        self.assertNotEqual(eur.fingerprint(), EXPECTED_FINGERPRINT)

    def test_request_validation(self):
        with self.assertRaises(ValueError):
            request(" ", 1999)
        with self.assertRaises(ValueError):
            request("x" * 256, 1999)
        with self.assertRaises(ValueError):
            request("k", 0)
        with self.assertRaises(TypeError):
            Money(19.99, USD)  # type: ignore[arg-type]


class PaymentsApiTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(1_000)
        self.processor = FakeProcessor()
        self.store = IdempotencyStore(self.clock, DAY_MS, 30_000)
        self.api = PaymentsApi(self.store, self.processor)

    def test_first_executes_and_retry_replays(self):
        first = self.api.charge(request("order-1", 1999))
        retry = self.api.charge(request("order-1", 1999))
        self.assertEqual((first.status, first.replayed), (200, False))
        self.assertEqual(retry.body, first.body)
        self.assertTrue(retry.replayed)
        self.assertEqual(self.processor.executions(), 1)
        self.assertEqual(self.store.size(), 1)

    def test_without_a_key_every_send_executes(self):
        no_key = request(None, 1999)
        self.api.charge(no_key)
        self.api.charge(no_key)
        self.assertEqual(self.processor.executions(), 2)

    def test_same_key_different_body_is_422_and_does_not_execute(self):
        first = self.api.charge(request("order-1", 1999))
        conflict = self.api.charge(request("order-1", 2999))
        self.assertEqual((conflict.status, conflict.replayed), (422, False))
        self.assertEqual(self.processor.executions(), 1)
        self.assertEqual(self.api.charge(request("order-1", 1999)).body, first.body)

    def test_keys_are_scoped_per_account(self):
        self.api.charge(request("order-1", 1999))
        other = self.api.charge(request("order-1", 1999, account="acct_2"))
        self.assertEqual((other.status, other.replayed), (200, False))
        self.assertEqual(self.processor.executions(), 2)

    def test_in_progress_duplicate_gets_409_without_executing(self):
        self.store.begin("acct_1", "order-3", request("order-3", 1999).fingerprint())
        busy = self.api.charge(request("order-3", 1999))
        self.assertEqual(busy.status, 409)
        self.assertEqual(self.processor.executions(), 0)

    def test_declines_and_5xx_replay(self):
        self.processor.decline_next()
        d1 = self.api.charge(request("order-4", 5000))
        d2 = self.api.charge(request("order-4", 5000))
        self.assertEqual((d1.status, d2.status, d2.replayed), (402, 402, True))
        self.processor.server_error_next()
        e1 = self.api.charge(request("order-5", 5000))
        e2 = self.api.charge(request("order-5", 5000))
        self.assertEqual((e1.status, e2.status, e2.replayed), (502, 502, True))
        self.assertEqual(self.processor.executions(), 2)

    def test_crash_releases_reservation_so_retry_executes(self):
        self.processor.crash_next()
        with self.assertRaises(ConnectionError):
            self.api.charge(request("order-6", 1200))
        after = self.api.charge(request("order-6", 1200))
        self.assertEqual((after.status, after.replayed), (200, False))
        self.assertEqual(self.processor.executions(), 2)


class IdempotencyStoreTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(0)
        self.store = IdempotencyStore(self.clock, DAY_MS, 30_000)
        self.fp = request("k", 1999).fingerprint()

    def test_concurrent_duplicates_one_new_one_in_progress_then_replay(self):
        self.assertEqual(self.store.begin("acct_1", "order-2", self.fp), Begin(Outcome.NEW))
        self.assertEqual(self.store.begin("acct_1", "order-2", self.fp), Begin(Outcome.IN_PROGRESS))
        self.assertEqual(self.store.begin("acct_1", "order-2", "other-fp").outcome, Outcome.CONFLICT)
        self.store.complete("acct_1", "order-2", Response(200, '{"id":"ch_x"}'))
        c = self.store.begin("acct_1", "order-2", self.fp)
        self.assertEqual(c, Begin(Outcome.REPLAY, Response(200, '{"id":"ch_x"}', True)))

    def test_complete_without_reservation_or_twice_is_a_bug(self):
        with self.assertRaises(RuntimeError):
            self.store.complete("acct_1", "never-begun", Response(200, "{}"))
        self.store.begin("acct_1", "order-2", self.fp)
        self.store.complete("acct_1", "order-2", Response(200, "{}"))
        with self.assertRaises(RuntimeError):
            self.store.complete("acct_1", "order-2", Response(200, "{}"))

    def test_in_progress_lease_is_taken_over_after_it_expires(self):
        self.store.begin("acct_1", "order-7", self.fp)
        self.clock.advance(29_999)
        self.assertEqual(self.store.begin("acct_1", "order-7", self.fp).outcome, Outcome.IN_PROGRESS)
        self.clock.advance(1)
        self.assertEqual(self.store.begin("acct_1", "order-7", self.fp).outcome, Outcome.NEW)

    def test_ttl_replays_until_expiry_then_forgets(self):
        processor = FakeProcessor()
        api = PaymentsApi(self.store, processor)
        t1 = api.charge(request("order-8", 1999))
        self.clock.advance(DAY_MS - 1)
        self.assertTrue(api.charge(request("order-8", 1999)).replayed)
        self.assertEqual(processor.executions(), 1)
        self.clock.advance(1)
        self.assertEqual(self.store.purge_expired(), 1)
        self.assertEqual(self.store.size(), 0)
        t2 = api.charge(request("order-8", 1999))
        self.assertFalse(t2.replayed)
        self.assertEqual(processor.executions(), 2)
        self.assertNotEqual(t1.body, t2.body)

    def test_config_validation(self):
        with self.assertRaises(ValueError):
            IdempotencyStore(self.clock, 0, 1)
        with self.assertRaises(ValueError):
            IdempotencyStore(self.clock, 1, 0)


class ConcurrencyTest(unittest.TestCase):
    def test_eight_concurrent_duplicates_execute_once(self):
        blocking = BlockingProcessor()
        api = PaymentsApi(IdempotencyStore(FakeClock(0), DAY_MS, 30_000), blocking)
        results: list[Response] = []
        results_lock = threading.Lock()
        start = threading.Event()

        def worker() -> None:
            start.wait()
            r = api.charge(request("order-9", 1999))
            with results_lock:
                results.append(r)

        threads = [threading.Thread(target=worker) for _ in range(8)]
        for t in threads:
            t.start()
        start.set()
        while True:                      # the seven losers finish with 409 while the winner is blocked
            with results_lock:
                if len(results) >= 7:
                    break
        blocking.release.set()
        for t in threads:
            t.join()
        statuses = sorted(r.status for r in results)
        self.assertEqual(statuses, [200] + [409] * 7)
        self.assertEqual(blocking.executions, 1)


if __name__ == "__main__":
    unittest.main()
