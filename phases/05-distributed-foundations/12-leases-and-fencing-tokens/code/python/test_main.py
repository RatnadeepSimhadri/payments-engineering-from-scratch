"""Tests for phases/05-distributed-foundations/12-leases-and-fencing-tokens. Run: python3 -m unittest -v"""
import unittest

from main import (
    FakeClock, FencedStorage, LeaseExpiredException, LeaseLock, PayoutWorker, RaceStudy, StaleTokenException,
    UnfencedStorage, or_else_throw,
)

APPLIED, FENCED_OFF = PayoutWorker.Result.APPLIED, PayoutWorker.Result.FENCED_OFF


class LeaseMechanicsTest(unittest.TestCase):
    def test_lifecycle(self):
        clock = FakeClock(0)
        lock = LeaseLock(clock, "r")
        a = or_else_throw(lock.try_acquire("A", 1_000))
        self.assertEqual(a.token, 1, "first lease gets token 1")
        self.assertEqual((a.expires_at_ms, a.ttl_ms()), (1_000, 1_000), "lease expires at grant + ttl")
        self.assertEqual(lock.holder().owner, "A", "holder is A while live")
        self.assertIsNone(lock.try_acquire("B", 1_000), "second acquirer is denied while lease is live")
        clock.advance(999)
        self.assertFalse(a.is_expired(clock), "not expired one ms before expiry")
        clock.advance(1)
        self.assertTrue(a.is_expired(clock), "expired exactly at expiry (>=)")
        self.assertIsNone(lock.holder(), "holder is empty once expired")
        b = or_else_throw(lock.try_acquire("B", 1_000))
        self.assertEqual(b.token, 2)
        self.assertGreater(b.token, a.token, "next lease gets a strictly larger token")
        with self.assertRaises(LeaseExpiredException):
            lock.renew(a, 1_000)
        with self.assertRaises(LeaseExpiredException):
            lock.release(a)
        b_renewed = lock.renew(b, 500)
        self.assertEqual(b_renewed.token, 2, "renew keeps the token")
        self.assertEqual(b_renewed.expires_at_ms, clock.now_ms() + 500, "renew moves expiry")
        lock.release(b_renewed)
        self.assertEqual(lock.try_acquire("C", 10).token, 3, "released lock is free immediately")
        with self.assertRaises(ValueError):
            lock.try_acquire("D", 0)
        self.assertEqual(lock.highest_token_issued(), 3, "tokens are monotonic across release and expiry")

    def test_safety_margin(self):
        c2 = FakeClock(0)
        m = or_else_throw(LeaseLock(c2, "m").try_acquire("A", 1_000))
        c2.advance(850)
        self.assertTrue(m.is_safe_to_use(c2, 0), "safe to use with no margin at 850/1000")
        self.assertFalse(m.is_safe_to_use(c2, 200), "not safe with a 200 ms margin at 850/1000")


class FencedStorageTest(unittest.TestCase):
    def test_rejects_stale_accepts_equal_and_newer(self):
        fs = FencedStorage()
        fs.write("k", 1, 5)
        fs.write("k", 2, 5)
        self.assertEqual(fs.read("k"), 2, "same token may write again (holder's own retries)")
        fs.write("k", 3, 7)
        self.assertEqual(fs.read("k"), 3)
        self.assertEqual(fs.highest_token_seen("k"), 7, "newer token accepted and recorded")
        with self.assertRaises(StaleTokenException) as ctx:
            fs.write("k", 99, 6)
        self.assertEqual((ctx.exception.presented, ctx.exception.highest_seen), (6, 7))
        self.assertEqual(fs.read("k"), 3, "rejected write did not change the value")
        fs.write("other", 1, 1)
        self.assertEqual(fs.highest_token_seen("other"), 1, "tokens are tracked per key")
        self.assertEqual(fs.highest_token_seen("k"), 7)


class PausedHolderTest(unittest.TestCase):
    def test_the_bug_unfenced(self):
        c3 = FakeClock(0)
        l3 = LeaseLock(c3, "payout")
        us = UnfencedStorage()
        us.write("acct", 100_000, 0)
        la = or_else_throw(l3.try_acquire("A", 1_000))
        seen_by_a = us.read("acct")
        c3.advance(1_500)
        rb = PayoutWorker("B", c3, l3, us).payout("acct", 30_000, 1_000, 0, 0)
        us.write("acct", seen_by_a - 20_000, la.token)
        self.assertIs(rb, APPLIED, "B applied its payout after A's lease expired")
        self.assertEqual(us.read("acct"), 80_000, "unfenced storage accepts A's stale write and loses B's update")

    def test_the_fix_fenced(self):
        c4 = FakeClock(0)
        l4 = LeaseLock(c4, "payout")
        fs4 = FencedStorage()
        fs4.write("acct", 100_000, 0)
        la4 = or_else_throw(l4.try_acquire("A", 1_000))
        seen_by_a4 = fs4.read("acct")
        c4.advance(1_500)
        rb4 = PayoutWorker("B", c4, l4, fs4).payout("acct", 30_000, 1_000, 0, 0)
        with self.assertRaises(StaleTokenException):
            fs4.write("acct", seen_by_a4 - 20_000, la4.token)
        self.assertIs(rb4, APPLIED)
        self.assertEqual(fs4.read("acct"), 70_000, "balance reflects only B's payout")

    def test_client_side_check_is_not_sufficient(self):
        c5 = FakeClock(0)
        us5 = UnfencedStorage()
        us5.write("acct", 100_000, 0)
        r5 = PayoutWorker("A", c5, LeaseLock(c5, "payout"), us5).payout("acct", 20_000, 1_000, 1_500, 0)
        self.assertIs(r5, FENCED_OFF, "pause before the check: client-side check catches it")
        self.assertEqual(us5.read("acct"), 100_000)
        c6 = FakeClock(0)
        us6 = UnfencedStorage()
        us6.write("acct", 100_000, 0)
        r6 = PayoutWorker("A", c6, LeaseLock(c6, "payout"), us6).payout("acct", 20_000, 1_000, 0, 1_500)
        self.assertIs(r6, APPLIED, "pause after the check: the stale write lands anyway on unfenced storage")
        self.assertEqual(c6.now_ms(), 1_500)
        c7 = FakeClock(0)
        l7 = LeaseLock(c7, "payout")
        fs7 = FencedStorage()
        fs7.write("acct", 100_000, 0)
        blocker = or_else_throw(l7.try_acquire("A", 1_000))
        seen7 = fs7.read("acct")
        c7.advance(1_500)
        PayoutWorker("B", c7, l7, fs7).payout("acct", 30_000, 1_000, 0, 0)
        with self.assertRaises(StaleTokenException):
            fs7.write("acct", seen7 - 20_000, blocker.token)


class RaceStudyTest(unittest.TestCase):
    def test_randomized_races(self):
        plain = RaceStudy.run(7, 2_000, False)
        fenced = RaceStudy.run(7, 2_000, True)
        self.assertGreater(plain.lost_updates, 0)
        self.assertEqual(plain.lost_updates, plain.both_applied,
                         "unfenced storage loses updates whenever the pause outlives the lease")
        self.assertEqual(fenced.lost_updates, 0, "fenced storage never loses an update")
        self.assertEqual(fenced.fenced_off, plain.lost_updates, "fenced rejected exactly the runs that would have lost")
        self.assertEqual(RaceStudy.run(7, 2_000, True), fenced, "study is deterministic under the seed")
        self.assertEqual(plain.lost_updates, 995, "same count as java.util.Random(7) in the Java version")


if __name__ == "__main__":
    unittest.main()
