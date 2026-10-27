"""Stage 1 tests for projects/ledger-engine (Python): Money is present, account types know their normal side,
the registry rejects duplicates and unknown ids. Run by scripts/project_test.py via python3 -m unittest -v.
Port of Stage01Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    GBP,
    JPY,
    KWD,
    USD,
    Account,
    AccountRegistry,
    AccountType,
    CurrencyMismatchError,
    DuplicateAccountError,
    LedgerError,
    Money,
    Rounding,
    Side,
    UnknownAccountError,
)

LONG_MAX = 2**63 - 1


class Stage01MoneyTest(unittest.TestCase):
    def test_parse_exactly_never_from_a_float(self):
        self.assertEqual(Money.parse("12.34", USD).minor_units, 1234, "money parse")
        self.assertEqual(Money.parse("12", USD).minor_units, 1200, "money parse pads")
        self.assertEqual(Money.parse(".99", USD).minor_units, 99, "money parse .99")
        self.assertEqual(Money.parse("-0.07", USD).minor_units, -7, "money parse negative")
        self.assertEqual(Money.parse("1200", JPY).minor_units, 1200, "money parse JPY")
        self.assertEqual(Money.parse("1.250", KWD).minor_units, 1250, "money parse KWD")
        with self.assertRaises(ArithmeticError, msg="money parse excess precision"):
            Money.parse("1.005", USD)
        with self.assertRaises(ValueError, msg="money parse garbage"):
            Money.parse("12,34", USD)
        self.assertEqual(str(Money.parse("12.05", USD)), "12.05 USD", "money str round-trips")

    def test_arithmetic_stays_in_the_currency_and_in_integers(self):
        self.assertEqual(Money.parse("1.10", USD).plus(Money.parse("2.20", USD)), Money.parse("3.30", USD), "money plus")
        self.assertEqual(Money.parse("1.00", USD).minus(Money.parse("2.50", USD)), Money.parse("-1.50", USD), "money minus goes negative")
        with self.assertRaises(CurrencyMismatchError, msg="money plus currency mismatch"):
            Money.parse("1.00", USD).plus(Money.parse("1.00", EUR))
        with self.assertRaises(CurrencyMismatchError, msg="money minus currency mismatch"):
            Money.parse("1.00", USD).minus(Money.parse("1.00", GBP))
        with self.assertRaises(ArithmeticError, msg="money plus overflow (64-bit bound kept from the Java design)"):
            Money.of_minor(LONG_MAX, USD).plus(Money.of_minor(1, USD))
        self.assertTrue(Money.sum(USD).is_zero(), "money sum of nothing is zero")

    def test_times_rounds_exactly_once_the_way_you_asked(self):
        hundred = Money.parse("100.00", USD)
        self.assertEqual(hundred.times(29, 1000, Rounding.HALF_EVEN).minor_units, 290, "2.9% fee of 100.00")
        ten_point_oh_five = Money.parse("10.05", USD)
        self.assertEqual(ten_point_oh_five.times(1, 2, Rounding.HALF_EVEN).minor_units, 502, "half-even tie goes to even (10.05 * 50%)")
        self.assertEqual(ten_point_oh_five.times(1, 2, Rounding.HALF_UP).minor_units, 503, "half-up tie goes up (10.05 * 50%)")
        self.assertEqual(ten_point_oh_five.times(1, 2, Rounding.DOWN).minor_units, 502, "down truncates")
        self.assertEqual(Money.parse("10.01", USD).times(1, 2, Rounding.UP).minor_units, 501, "up ceils")
        self.assertEqual(Money.parse("-10.05", USD).times(1, 2, Rounding.HALF_UP).minor_units, -503, "negative half-up rounds away from zero")
        self.assertEqual(Money.parse("-10.05", USD).times(1, 2, Rounding.DOWN).minor_units, -502, "negative down truncates toward zero")
        with self.assertRaises(ArithmeticError, msg="times rejects zero denominator"):
            hundred.times(1, 0, Rounding.HALF_EVEN)
        self.assertEqual(Money.divide_rounded(7, 2, Rounding.HALF_EVEN), 4, "divide_rounded 7/2 half-even is 4")
        self.assertEqual(Money.divide_rounded(5, 2, Rounding.HALF_EVEN), 2, "divide_rounded 5/2 half-even is 2")
        self.assertEqual(Money.divide_rounded(-7, 2, Rounding.HALF_UP), -4, "divide_rounded -7/2 half-up is -4")

    def test_allocate_never_loses_or_invents_a_minor_unit(self):
        hundred = Money.parse("100.00", USD)
        thirds = hundred.allocate(1, 1, 1)
        self.assertEqual(thirds[0].plus(thirds[1]).plus(thirds[2]), hundred, "allocate thirds sums to whole")
        self.assertEqual([t.minor_units for t in thirds], [3334, 3333, 3333], "allocate thirds gives 33.34/33.33/33.33")
        split = Money.parse("0.05", USD).allocate(70, 30)
        self.assertEqual([s.minor_units for s in split], [4, 1], "allocate 5 cents 70/30 is 4/1")
        negative = Money.parse("-1.00", USD).allocate(1, 2)
        self.assertEqual([n.minor_units for n in negative], [-33, -67], "allocate negative keeps sign and sums")
        with self.assertRaises(ValueError, msg="allocate rejects no ratios"):
            hundred.allocate()
        with self.assertRaises(ValueError, msg="allocate rejects all-zero ratios"):
            hundred.allocate(0, 0)
        with self.assertRaises(ValueError, msg="allocate rejects negative ratio"):
            hundred.allocate(1, -1)


class Stage01AccountsTest(unittest.TestCase):
    def test_normal_balance_side_per_account_class(self):
        self.assertIs(AccountType.ASSET.normal_side(), Side.DEBIT, "ASSET grows on DEBIT")
        self.assertIs(AccountType.EXPENSE.normal_side(), Side.DEBIT, "EXPENSE grows on DEBIT")
        self.assertIs(AccountType.LIABILITY.normal_side(), Side.CREDIT, "LIABILITY grows on CREDIT")
        self.assertIs(AccountType.EQUITY.normal_side(), Side.CREDIT, "EQUITY grows on CREDIT")
        self.assertIs(AccountType.REVENUE.normal_side(), Side.CREDIT, "REVENUE grows on CREDIT")
        self.assertIs(Side.DEBIT.opposite(), Side.CREDIT, "DEBIT opposite is CREDIT")
        self.assertIs(Side.CREDIT.opposite(), Side.DEBIT, "CREDIT opposite is DEBIT")

    def test_the_account_value(self):
        bank = Account("bank", AccountType.ASSET, USD)
        self.assertEqual(bank.id, "bank", "account keeps id")
        self.assertIs(bank.type, AccountType.ASSET, "account keeps type")
        self.assertEqual(bank.currency, USD, "account keeps currency")
        self.assertIs(bank.normal_side(), Side.DEBIT, "account normal side delegates to type")
        self.assertIs(Account("sellers", AccountType.LIABILITY, USD).normal_side(), Side.CREDIT, "liability account normal side")
        self.assertEqual(bank, Account("bank", AccountType.ASSET, USD), "accounts are values")
        with self.assertRaises(ValueError, msg="blank account id rejected"):
            Account("  ", AccountType.ASSET, USD)
        with self.assertRaises((ValueError, TypeError), msg="None account id rejected"):
            Account(None, AccountType.ASSET, USD)

    def test_the_registry(self):
        bank = Account("bank", AccountType.ASSET, USD)
        chart = AccountRegistry()
        self.assertEqual(chart.size(), 0, "empty registry has size 0")
        self.assertFalse(chart.contains("bank"), "empty registry contains nothing")
        opened = chart.open("bank", AccountType.ASSET, USD)
        self.assertEqual(opened, bank, "open returns the account")
        self.assertTrue(chart.contains("bank"), "open makes it visible")
        self.assertEqual(chart.get("bank"), bank, "get returns the same account")
        chart.open("sellers", AccountType.LIABILITY, USD)
        chart.open("eur-bank", AccountType.ASSET, EUR)
        self.assertEqual(chart.size(), 3, "size counts opened accounts")
        with self.assertRaises(DuplicateAccountError, msg="duplicate id rejected"):
            chart.open("bank", AccountType.ASSET, USD)
        with self.assertRaises(DuplicateAccountError, msg="duplicate id rejected even with different type"):
            chart.open("bank", AccountType.LIABILITY, EUR)
        self.assertEqual(chart.size(), 3, "failed open does not change size")
        self.assertIs(chart.get("bank").type, AccountType.ASSET, "failed open does not change the original")
        with self.assertRaises(UnknownAccountError, msg="unknown id rejected"):
            chart.get("nope")
        self.assertTrue(issubclass(DuplicateAccountError, LedgerError), "duplicate is a LedgerError")
        self.assertTrue(issubclass(UnknownAccountError, LedgerError), "unknown is a LedgerError")
        all_accounts = chart.all()
        self.assertEqual([a.id for a in all_accounts], ["bank", "sellers", "eur-bank"], "all() keeps opening order")
        with self.assertRaises((AttributeError, TypeError), msg="all() is a read-only view"):
            chart.all().append(Account("x", AccountType.ASSET, USD))  # type: ignore[attr-defined]
        self.assertTrue(chart.size() == 3 and len(chart.all()) == 3, "all() is a copy")


if __name__ == "__main__":
    unittest.main()
