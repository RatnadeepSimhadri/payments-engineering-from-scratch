"""Tests for phases/14-payments-at-scale/08-cost-and-authorization-optimization. Run: python3 -m unittest -v"""
import unittest

from main import (
    ALL_LEVERS, NO_LEVERS, USD, AuthExperiment, CardType, DeclineClass, JavaRandom, Lever, Money, PriceList, ResponseCode,
    Rounding, Txn, Uplift,
)

EXP = AuthExperiment(PriceList.standard(USD), USD)


def txn(tid: str, minor: int, card_type: CardType, token: bool, base: ResponseCode, d_token: int, d_retry: int, d_updater: int) -> Txn:
    return Txn(tid, Money.of_minor(minor, USD), card_type, token, base, d_token, d_retry, d_updater)


def only(*levers: Lever) -> frozenset:
    return frozenset(levers)


class FeeTest(unittest.TestCase):
    def test_interchange_and_network_fees(self):
        self.assertEqual(EXP.interchange(Money.of_minor(100_000, USD), CardType.COMMERCIAL, False).minor_units, 2_660)
        self.assertEqual(EXP.interchange(Money.of_minor(100_000, USD), CardType.COMMERCIAL, True).minor_units, 1_860)
        self.assertEqual(EXP.interchange(Money.of_minor(5_000, USD), CardType.CONSUMER_CREDIT, True),
                         EXP.interchange(Money.of_minor(5_000, USD), CardType.CONSUMER_CREDIT, False), "L3 data does not change consumer credit")
        self.assertEqual(EXP.interchange(Money.of_minor(5_000, USD), CardType.REGULATED_DEBIT, False).minor_units, 24,
                         "0.05% + 0.22 = 0.245 -> 0.24 (half-even)")
        self.assertEqual(EXP.network_fee(Money.of_minor(10_000, USD), CardType.REGULATED_DEBIT, True).minor_units, 6)
        self.assertEqual(EXP.network_fee(Money.of_minor(10_000, USD), CardType.REGULATED_DEBIT, False).minor_units, 13)


class PathTest(unittest.TestCase):
    def test_expired_card_paths(self):
        expired = txn("e", 4_999, CardType.CONSUMER_CREDIT, True, ResponseCode.EXPIRED_CARD, 9_000, 9_000, 1_000)
        self.assertEqual(EXP.simulate(expired, only(Lever.SOFT_DECLINE_RETRY)).attempts, 1, "expired card is not retried unchanged")
        upd = EXP.simulate(expired, only(Lever.ACCOUNT_UPDATER))
        self.assertTrue(upd.final_outcome.approved())
        self.assertTrue(upd.updater_hit)
        self.assertEqual(upd.fees.minor_units, 20)
        expired_miss = txn("m", 4_999, CardType.CONSUMER_CREDIT, False, ResponseCode.EXPIRED_CARD, 0, 0, 8_000)
        self.assertFalse(EXP.simulate(expired_miss, only(Lever.ACCOUNT_UPDATER)).updater_hit, "draw 8000 >= 8000 misses")
        tok = EXP.simulate(expired, only(Lever.NETWORK_TOKEN, Lever.ACCOUNT_UPDATER))
        self.assertTrue(tok.final_outcome.approved())
        self.assertFalse(tok.updater_hit, "token fixes the expired card first, so no updater fee")
        self.assertEqual(tok.fees.minor_units, 6)
        not_eligible = txn("n", 4_999, CardType.CONSUMER_CREDIT, False, ResponseCode.EXPIRED_CARD, 0, 0, 9_999)
        self.assertFalse(EXP.simulate(not_eligible, only(Lever.NETWORK_TOKEN)).token_used)

    def test_do_not_honor_paths(self):
        dnh = txn("d", 2_000, CardType.CONSUMER_CREDIT, True, ResponseCode.DO_NOT_HONOR, 2_499, 9_999, 0)
        self.assertTrue(EXP.simulate(dnh, only(Lever.NETWORK_TOKEN)).final_outcome.approved(), "token recovers 05 when draw < 2500")
        dnh2 = txn("d2", 2_000, CardType.CONSUMER_CREDIT, True, ResponseCode.DO_NOT_HONOR, 2_500, 999, 0)
        r05 = EXP.simulate(dnh2, only(Lever.SOFT_DECLINE_RETRY))
        self.assertEqual(r05.attempts, 2)
        self.assertTrue(r05.retried)
        self.assertTrue(r05.final_outcome.approved(), "05 gets exactly one retry, approved when draw < 1000")

    def test_hard_declines_never_retried(self):
        for hard in (ResponseCode.LOST_OR_STOLEN, ResponseCode.NOT_PERMITTED):
            with self.subTest(code=hard.code):
                h = EXP.simulate(txn("h", 1_000, CardType.CONSUMER_CREDIT, True, hard, 0, 0, 0), ALL_LEVERS)
                self.assertEqual(h.attempts, 1)
                self.assertFalse(h.final_outcome.approved())

    def test_timeouts_and_determinism(self):
        timeout = txn("t", 3_000, CardType.CONSUMER_CREDIT, False, ResponseCode.PROCESSOR_TIMEOUT, 0, 8_999, 0)
        fo = EXP.simulate(timeout, only(Lever.ROUTING, Lever.SOFT_DECLINE_RETRY))
        self.assertTrue(fo.failed_over)
        self.assertFalse(fo.retried, "timeout fails over once and is not also retried")
        self.assertEqual(fo.attempts, 2)
        self.assertTrue(fo.final_outcome.approved())
        timeout2 = txn("t2", 3_000, CardType.CONSUMER_CREDIT, False, ResponseCode.PROCESSOR_TIMEOUT, 0, 9_000, 0)
        self.assertIs(EXP.simulate(timeout2, only(Lever.ROUTING)).final_outcome, ResponseCode.ISSUER_UNAVAILABLE)
        self.assertEqual(EXP.simulate(timeout, ALL_LEVERS), EXP.simulate(timeout, ALL_LEVERS), "same txn, same levers, same trace")
        with self.assertRaises(ValueError):
            txn("x", 1, CardType.CONSUMER_CREDIT, False, ResponseCode.APPROVED, 10_000, 0, 0)


class ExperimentTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.pop = AuthExperiment.population(20_000, 7, USD)
        cls.control = EXP.run(cls.pop, NO_LEVERS)
        cls.singles = {l: EXP.run(cls.pop, only(l)).approvals - cls.control.approvals for l in Lever}
        cls.all = EXP.run(cls.pop, ALL_LEVERS)

    def test_control(self):
        self.assertEqual(self.control, EXP.run(AuthExperiment.population(20_000, 7, USD), NO_LEVERS), "control is reproducible")
        self.assertEqual(self.control.attempts, len(self.pop), "one attempt per transaction")
        self.assertEqual(self.control.approvals, sum(1 for t in self.pop if t.base_outcome.approved()))

    def test_levers_never_lower_approvals(self):
        for l in Lever:
            with self.subTest(lever=l.name):
                self.assertGreaterEqual(self.singles[l], 0)

    def test_overlap_and_exact_uplift(self):
        all_uplift = self.all.approvals - self.control.approvals
        self.assertGreater(all_uplift, max(self.singles.values()), "all levers beat the best single lever")
        self.assertLess(all_uplift, sum(self.singles.values()), "uplifts are not additive (overlap)")
        self.assertEqual(self.singles[Lever.LEVEL3_DATA], 0, "LEVEL3_DATA changes cost, not approvals")
        self.assertLess(EXP.run(self.pop, only(Lever.LEVEL3_DATA)).interchange, self.control.interchange)
        expired_eligible = sum(1 for t in self.pop if t.base_outcome is ResponseCode.EXPIRED_CARD and t.token_eligible)
        dnh_recovered = sum(1 for t in self.pop if t.base_outcome is ResponseCode.DO_NOT_HONOR and t.token_eligible and t.draw_token < 2_500)
        self.assertEqual(self.singles[Lever.NETWORK_TOKEN], expired_eligible + dnh_recovered, "token uplift is exactly eligible expired + recovered 05")
        hard_resent = sum(1 for t in self.pop if t.base_outcome.decline_class is DeclineClass.NEVER_RETRY
                          and EXP.simulate(t, ALL_LEVERS).attempts > 1)
        self.assertEqual(hard_resent, 0, "no hard decline is ever re-sent")

    def test_uplift_arithmetic(self):
        u = Uplift.of("ALL", self.control, self.all, 3_000)
        self.assertEqual(u.net_value, u.extra_volume.times(3_000, 10_000, Rounding.HALF_EVEN).minus(u.extra_cost))
        self.assertEqual(u.rate_delta_bps, self.all.approvals * 10_000 // len(self.pop) - self.control.approvals * 10_000 // len(self.pop))

    def test_java_random_sequence(self):
        """Values taken from `new java.util.Random(2026)` on the JDK; they pin the LCG that generates the population."""
        r = JavaRandom(2026)
        self.assertEqual([r.next_int(66), r.next_int(100)], [29, 50])


if __name__ == "__main__":
    unittest.main()
