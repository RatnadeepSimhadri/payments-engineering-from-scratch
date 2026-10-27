"""Tests for phases/12-payment-systems-design/06-refunds-and-partial-refunds. Run: python3 -m unittest -v"""

import threading
import unittest
from datetime import date

from main import (EUR, USD, CurrencyMismatchError, FakeClock, FeeRefundPolicy, JournalEntry, Ledger, Money,
                  OverRefundError, Posting, RefundError, RefundIdempotencyConflictError, RefundPolicy, RefundService,
                  RefundState, RefundWindowClosedError)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


class Fixture:
    def __init__(self, fees: FeeRefundPolicy) -> None:
        self.clock = FakeClock(date(2026, 3, 1))
        self.ledger = Ledger(USD)
        self.svc = RefundService(self.ledger, self.clock, RefundPolicy(180, fees))


class RefundLifecycleTest(unittest.TestCase):
    def test_capture_partials_failure_and_full_refund(self) -> None:
        f = Fixture(FeeRefundPolicy.PROPORTIONAL)
        svc = f.svc
        c = svc.capture("cap", "pay", usd(10_000), 290, 30)
        self.assertEqual(c.fee, usd(320), "fee 2.90% + 0.30 on 100.00 = 3.20")
        self.assertEqual(f.ledger.balance(RefundService.SCHEME), usd(10_000))
        self.assertEqual(f.ledger.balance(RefundService.MERCHANT), usd(9_680))
        self.assertEqual(f.ledger.balance(RefundService.FEES), usd(320))
        self.assertEqual(svc.remaining_refundable("cap"), usd(10_000))

        # partial refunds
        r1 = svc.request_refund("r1", "cap", usd(3_000), "partial")
        self.assertIs(r1.refund.state, RefundState.PENDING)
        self.assertFalse(r1.replayed)
        self.assertEqual(svc.remaining_refundable("cap"), usd(7_000))
        self.assertEqual(svc.pending("cap"), usd(3_000))
        self.assertEqual(f.ledger.balance(RefundService.REFUNDS), usd(3_000))
        self.assertEqual(f.ledger.balance(RefundService.MERCHANT), usd(6_680))
        self.assertTrue(svc.succeed("r1"))
        self.assertEqual(svc.refunded("cap"), usd(3_000))
        self.assertTrue(svc.pending("cap").is_zero())
        self.assertEqual(svc.refund("r1").fee_returned, usd(96), "floor(320 x 3000 / 10000) = 96")
        self.assertFalse(svc.succeed("r1"), "redelivered success is a no-op")
        self.assertEqual(svc.fee_returned("cap"), usd(96))
        svc.request_refund("r2", "cap", usd(2_000), "second partial")
        svc.succeed("r2")
        self.assertEqual(svc.refunded("cap"), usd(5_000))
        self.assertEqual(svc.remaining_refundable("cap"), usd(5_000))
        self.assertEqual(svc.check_invariants(), [])

        # over-refund
        with self.assertRaises(OverRefundError):
            svc.request_refund("r3", "cap", usd(5_001), "too much")
        svc.request_refund("r4", "cap", usd(4_000), "pending big one")
        with self.assertRaises(OverRefundError):
            svc.request_refund("r5", "cap", usd(1_001), "race")
        self.assertEqual(len(svc.refunds_for("cap")), 3, "refused requests left no refund record")
        exact = svc.request_refund("r6", "cap", usd(1_000), "exact remainder")
        self.assertIs(exact.refund.state, RefundState.PENDING)
        self.assertTrue(svc.remaining_refundable("cap").is_zero())
        with self.assertRaises(OverRefundError):
            svc.request_refund("r7", "cap", usd(1), "x")

        # failure releases the reservation
        svc.fail("r4", "issuer rejected credit")
        self.assertIs(svc.refund("r4").state, RefundState.FAILED)
        self.assertEqual(svc.remaining_refundable("cap"), usd(4_000))
        self.assertEqual(f.ledger.balance(RefundService.REFUNDS), usd(1_000))
        with self.assertRaises(RefundError):
            svc.succeed("r4")
        with self.assertRaises(RefundError):
            svc.fail("r1", "late")

        # full refund: fee shares sum to the fee exactly
        svc.succeed("r6")
        svc.request_refund("r8", "cap", usd(4_000), "rest")
        svc.succeed("r8")
        self.assertEqual(svc.refunded("cap"), usd(10_000))
        self.assertTrue(svc.remaining_refundable("cap").is_zero())
        self.assertEqual(svc.fee_returned("cap"), usd(320))
        self.assertTrue(f.ledger.balance(RefundService.MERCHANT).is_zero())
        self.assertTrue(f.ledger.balance(RefundService.FEES).is_zero())
        self.assertTrue(f.ledger.balance(RefundService.SCHEME).is_zero())
        self.assertEqual(svc.check_invariants(), [])


class RoundingTest(unittest.TestCase):
    def test_cumulative_shares_do_not_drift(self) -> None:
        g = Fixture(FeeRefundPolicy.PROPORTIONAL)
        small = g.svc.capture("c2", "p2", usd(1_000), 290, 30)
        self.assertEqual(small.fee, usd(59))
        for i, part in enumerate((333, 333, 334)):
            g.svc.request_refund(f"s{i}", "c2", usd(part), "split")
            g.svc.succeed(f"s{i}")
        self.assertEqual([g.svc.refund(f"s{i}").fee_returned for i in range(3)], [usd(19), usd(20), usd(20)])
        self.assertEqual(g.svc.fee_returned("c2"), usd(59), "no lost cents")

    def test_thousand_one_cent_refunds(self) -> None:
        h = Fixture(FeeRefundPolicy.PROPORTIONAL)
        h.svc.capture("c3", "p3", usd(1_000), 290, 30)
        for i in range(1_000):
            h.svc.request_refund(f"u{i}", "c3", usd(1), "cent")
            h.svc.succeed(f"u{i}")
        self.assertEqual(h.svc.fee_returned("c3"), usd(59))
        self.assertEqual(h.svc.check_invariants(), [])


class FeePolicyNoneTest(unittest.TestCase):
    def test_merchant_owes_the_fee(self) -> None:
        n = Fixture(FeeRefundPolicy.NONE)
        n.svc.capture("cn", "pn", usd(10_000), 290, 30)
        n.svc.request_refund("rn", "cn", usd(10_000), "all")
        n.svc.succeed("rn")
        self.assertTrue(n.svc.fee_returned("cn").is_zero())
        self.assertEqual(n.ledger.balance(RefundService.MERCHANT), usd(-320))
        self.assertTrue(n.ledger.balances())


class IdempotencyAndValidationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.i = Fixture(FeeRefundPolicy.PROPORTIONAL)
        self.i.svc.capture("ci", "pi", usd(5_000), 290, 30)

    def test_idempotency(self) -> None:
        first = self.i.svc.request_refund("ri", "ci", usd(1_000), "a")
        replay = self.i.svc.request_refund("ri", "ci", usd(1_000), "a retry")
        self.assertTrue(replay.replayed)
        self.assertIs(replay.refund, first.refund)
        self.assertEqual(self.i.svc.pending("ci"), usd(1_000), "replay did not reserve twice")
        self.assertEqual(self.i.ledger.balance(RefundService.REFUNDS), usd(1_000))
        with self.assertRaises(RefundIdempotencyConflictError):
            self.i.svc.request_refund("ri", "ci", usd(2_000), "bug")

    def test_validation(self) -> None:
        with self.assertRaises(ValueError):
            self.i.svc.request_refund("z", "ci", usd(0), "x")
        with self.assertRaises(CurrencyMismatchError):
            self.i.svc.request_refund("e", "ci", Money.of_minor(100, EUR), "x")
        with self.assertRaises(ValueError):
            self.i.svc.request_refund("u", "nope", usd(1), "x")
        with self.assertRaises(ValueError):
            JournalEntry("bad", "x", (Posting.debit("a", usd(2)), Posting.credit("b", usd(1))))


class RefundWindowTest(unittest.TestCase):
    def test_window_edges(self) -> None:
        w = Fixture(FeeRefundPolicy.PROPORTIONAL)
        w.svc.capture("cw", "pw", usd(5_000), 290, 30)
        w.clock.advance_days(180)
        self.assertIs(w.svc.request_refund("w1", "cw", usd(100), "edge").refund.state, RefundState.PENDING)
        w.clock.advance_days(1)
        with self.assertRaises(RefundWindowClosedError):
            w.svc.request_refund("w2", "cw", usd(100), "late")
        self.assertTrue(w.svc.succeed("w1"), "a pending refund requested in-window can still settle after it closes")


class ConcurrencyTest(unittest.TestCase):
    def test_ten_threads_race_for_fifteen_each(self) -> None:
        cc = Fixture(FeeRefundPolicy.PROPORTIONAL)
        cc.svc.capture("cr", "pr", usd(10_000), 290, 30)
        accepted: list[int] = []
        refused: list[int] = []
        tally = threading.Lock()
        start = threading.Event()

        def worker(n: int) -> None:
            start.wait()
            try:
                cc.svc.request_refund(f"race{n}", "cr", usd(1_500), "race")
                with tally:
                    accepted.append(n)
            except OverRefundError:
                with tally:
                    refused.append(n)

        threads = [threading.Thread(target=worker, args=(t,)) for t in range(10)]
        for th in threads:
            th.start()
        start.set()
        for th in threads:
            th.join()
        self.assertEqual((len(accepted), len(refused)), (6, 4), "exactly 6 of 10 concurrent 15.00 refunds fit in 100.00")
        self.assertEqual(cc.svc.remaining_refundable("cr"), usd(1_000))
        self.assertEqual(cc.svc.check_invariants(), [])


if __name__ == "__main__":
    unittest.main()
