"""Tests for phases/10-money-and-ledgers/10-fees-and-splits. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, FeeSchedule, Ledger, LineItem, Money, Order, PayoutCalculator, PayoutPolicy, SplitPolicy,
                  Splitter)


class JavaRandom:
    """java.util.Random (48-bit LCG) including nextLong(bound), so the property test replays MainTest.java's 2,000 orders."""

    MASK64 = (1 << 64) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r

    def next_boolean(self) -> bool:
        return self.next(1) != 0

    def next_long(self) -> int:
        v = ((self.next(32) << 32) + self.next(32)) & self.MASK64
        return v - (1 << 64) if v >= (1 << 63) else v

    def next_long_bounded(self, bound: int) -> int:
        """RandomSupport.boundedNextLong: reject over-represented candidates of (nextLong >>> 1) % bound."""
        m = bound - 1
        r = self.next_long()
        if (bound & m) == 0:
            return r & m
        u = (r & self.MASK64) >> 1
        while True:
            r = u % bound
            if ((u + m - r) & self.MASK64) < (1 << 63):  # Java: u + m - r did not overflow to negative
                return r
            u = (self.next_long() & self.MASK64) >> 1


def eur(s: str) -> Money:
    return Money.parse(s, EUR)


def policy() -> SplitPolicy:
    return SplitPolicy(FeeSchedule.percent_plus_fixed("c", 1_200, eur("0.30")).with_bounds(eur("0.50"), eur("25.00")),
                       1_900, 2_000, {"seller-B": "partner-P"}, FeeSchedule.percent_plus_fixed("p", 140, eur("0.25")))


def worked_order() -> Order:
    return Order("o-1", (LineItem("lamp", "seller-A", eur("59.99")), LineItem("bulb", "seller-A", eur("4.99")),
                         LineItem("rug", "seller-B", eur("119.00"))), eur("10.00"))


class FeeScheduleTest(unittest.TestCase):
    def test_percent_plus_fixed_with_bounds(self):
        c = policy().commission
        self.assertEqual(c.fee_for(eur("50.00")), eur("6.30"))
        self.assertEqual(c.fee_for(eur("1.00")), eur("0.50"))
        self.assertEqual(c.fee_for(eur("300.00")), eur("25.00"))
        self.assertEqual(c.fee_for(eur("0.40")), eur("0.40"))
        self.assertTrue(c.fee_for(eur("0.00")).is_zero())
        self.assertEqual(c.fee_for(eur("1.04")), eur("0.50"))
        self.assertEqual(FeeSchedule.percent_plus_fixed("x", 1_250, eur("0.00")).fee_for(eur("1.00")), eur("0.12"))

    def test_rejections(self):
        with self.assertRaises(ValueError):
            FeeSchedule.percent_plus_fixed("x", 100, eur("0.00")).with_bounds(eur("2.00"), eur("1.00"))
        with self.assertRaises(ValueError):
            FeeSchedule.percent_plus_fixed("x", 10_001, eur("0.00"))
        with self.assertRaises(ValueError):
            policy().commission.fee_for(eur("-1.00"))


class WorkedOrderTest(unittest.TestCase):
    def setUp(self):
        self.order = worked_order()
        self.s = Splitter(policy()).split(self.order)

    def test_discount_allocation(self):
        s = self.s
        self.assertEqual(s.total(lambda l: l.discount_share), eur("10.00"))
        self.assertEqual([l.discount_share for l in s.lines], [eur("3.26"), eur("0.27"), eur("6.47")])

    def test_parts_sum_to_charge(self):
        s = self.s
        self.assertTrue(all(l.sum_of_parts() == l.charged for l in s.lines))
        self.assertEqual((s.lines[0].tax, s.lines[0].platform_fee, s.lines[0].seller_net), (eur("9.06"), eur("6.02"), eur("41.65")))
        self.assertEqual((s.lines[2].platform_fee, s.lines[2].referral_fee), (eur("9.32"), eur("2.33")))
        self.assertTrue(s.lines[0].referral_fee.is_zero())
        self.assertIsNone(s.lines[0].referrer)
        self.assertEqual(s.seller_nets(), {"seller-A": eur("44.84"), "seller-B": eur("82.91")})
        parts = (s.total(lambda l: l.tax).plus(s.total(lambda l: l.platform_fee))
                 .plus(s.total(lambda l: l.referral_fee)).plus(s.total(lambda l: l.seller_net)))
        self.assertEqual(parts, self.order.charged())
        self.assertEqual(self.order.charged(), eur("173.98"))
        self.assertEqual(s.processor_fee, eur("2.69"))

    def test_ledger_capture(self):
        ledger = Ledger()
        ledger.post(Ledger.capture_entry(self.s))
        self.assertEqual(ledger.trial_balance_net(), 0)
        self.assertEqual(ledger.net_debit("1200 Receivable"), 17_398 - 269)
        self.assertEqual(ledger.net_debit("2300 VAT Payable"), -2_778)


class PayoutTest(unittest.TestCase):
    def test_payouts(self):
        pp = PayoutPolicy(1_000, FeeSchedule.percent_plus_fixed("payout", 0, eur("0.25")), eur("20.00"))
        a = PayoutCalculator.compute("seller-A", eur("44.84"), pp)
        self.assertEqual(a.reserve, eur("4.49"))
        self.assertEqual(a.reserve.plus(a.fee).plus(a.net).plus(a.carried_over), a.available)
        self.assertEqual(a.net, eur("40.10"))
        self.assertTrue(a.paid())
        small = PayoutCalculator.compute("seller-C", eur("12.40"), pp)
        self.assertFalse(small.paid())
        self.assertEqual(small.carried_over, eur("12.40"))
        self.assertTrue(small.reserve.is_zero())
        negative = PayoutCalculator.compute("seller-D", eur("-5.00"), pp)
        self.assertFalse(negative.paid())
        self.assertEqual(negative.carried_over, eur("-5.00"))
        ledger = Ledger()
        ledger.post(Ledger.capture_entry(Splitter(policy()).split(worked_order())))
        ledger.post(Ledger.payout_entry(a))
        self.assertEqual(ledger.trial_balance_net(), 0)
        self.assertEqual(ledger.net_debit("2100 Seller Payable seller-A"), 0)
        self.assertEqual(ledger.net_debit("2150 Seller Reserve seller-A"), -449)


class BoundaryTest(unittest.TestCase):
    def test_boundaries_and_errors(self):
        with self.assertRaises(ValueError):
            Splitter(policy()).split(Order("o", (LineItem("x", "seller-A", eur("1.00")),), eur("2.00")))
        free = Splitter(policy()).split(Order("o-free", (LineItem("x", "seller-A", eur("5.00")),), eur("5.00")))
        self.assertTrue(free.lines[0].sum_of_parts().is_zero())
        self.assertTrue(free.lines[0].seller_net.is_zero())
        tiny = Splitter(policy()).split(Order("o-tiny", (LineItem("x", "seller-B", eur("0.01")),), eur("0.00")))
        self.assertEqual(tiny.lines[0].sum_of_parts(), eur("0.01"))
        self.assertFalse(tiny.lines[0].seller_net.is_negative())
        with self.assertRaises(ValueError):
            Splitter(policy()).split(Order("o", (LineItem("x", "seller-A", eur("0.00")),), eur("0.00")))


class PropertyTest(unittest.TestCase):
    def test_random_orders(self):
        r = JavaRandom(23)
        conserve = non_negative = bounded = balanced = True
        for i in range(2_000):
            n = 1 + r.next_int(5)
            lines = []
            subtotal = 0
            for k in range(n):
                price = 1 + r.next_int(500 if r.next_boolean() else 50_000)
                subtotal += price
                lines.append(LineItem(f"s{k}", "seller-A" if r.next_boolean() else "seller-B", Money.of_minor(price, EUR)))
            discount = Money.of_minor(r.next_long_bounded(subtotal + 1) if r.next_int(4) == 0 else 0, EUR)
            os_ = Splitter(policy()).split(Order(f"o{i}", lines, discount))
            total = (os_.total(lambda l: l.tax).plus(os_.total(lambda l: l.platform_fee))
                     .plus(os_.total(lambda l: l.referral_fee)).plus(os_.total(lambda l: l.seller_net)))
            conserve = conserve and total == os_.order.charged() and os_.total(lambda l: l.discount_share) == discount
            for l in os_.lines:
                non_negative = non_negative and not (l.tax.is_negative() or l.platform_fee.is_negative()
                                                     or l.referral_fee.is_negative() or l.seller_net.is_negative())
                bounded = bounded and l.platform_fee.plus(l.referral_fee) <= eur("25.00")
            if os_.order.charged().is_positive():
                lg = Ledger()
                lg.post(Ledger.capture_entry(os_))
                balanced = balanced and lg.trial_balance_net() == 0
        self.assertTrue(conserve)
        self.assertTrue(non_negative)
        self.assertTrue(bounded)
        self.assertTrue(balanced)


if __name__ == "__main__":
    unittest.main()
