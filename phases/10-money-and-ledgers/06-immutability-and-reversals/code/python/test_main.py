"""Tests for phases/10-money-and-ledgers/06-immutability-and-reversals. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, USD, AccountType, AlreadyReversedError, CurrencyMismatchError, EntryKind, ExcessiveReversalError,
                  IdempotencyConflictError, JournalEntry, JournalRecord, Ledger, LedgerError, Money, Posting, Side)


class JavaRandom:
    """java.util.Random's LCG, so the property test draws the same 200 refund sequences as MainTest.java."""

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


def ledger() -> Ledger:
    l = Ledger()
    l.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    l.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    l.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)
    return l


def sale(id: str, gross: int, fee: int) -> JournalEntry:
    return JournalEntry.original(id, "sale", [Posting.debit("1200", Money.of_minor(gross, USD)),
                                              Posting.credit("2100", Money.of_minor(gross - fee, USD)),
                                              Posting.credit("4100", Money.of_minor(fee, USD))])


def sum_balances(l: Ledger) -> int:
    return l.balance("1200").minor_units - l.balance("2100").minor_units - l.balance("4100").minor_units


def two_legs(kind: EntryKind, relates_to, reason) -> JournalEntry:
    return JournalEntry("x", "d", [Posting.debit("2100", Money.of_minor(1, USD)), Posting.credit("1200", Money.of_minor(1, USD))],
                        kind, relates_to, reason)


class ImmutabilityTest(unittest.TestCase):
    def test_values_cannot_be_modified(self):
        s = sale("pay-1", 12_000, 348)
        with self.assertRaises(AttributeError):
            s.postings.clear()  # a tuple has no clear()
        l = ledger()
        l.post(s)
        with self.assertRaises(TypeError):
            del l.records()[0]  # tuple view: item deletion unsupported

    def test_post_is_idempotent_on_content(self):
        l = ledger()
        l.post(sale("pay-1", 12_000, 348))
        self.assertFalse(l.post(sale("pay-1", 12_000, 348)))
        self.assertEqual(l.size(), 1)
        with self.assertRaises(IdempotencyConflictError):
            l.post(sale("pay-1", 12_000, 349))

    def test_entry_construction_rules(self):
        l = ledger()
        l.post(sale("pay-1", 12_000, 348))
        with self.assertRaises(LedgerError):
            l.post(two_legs(EntryKind.REVERSAL, "pay-1", "r"))  # post refuses a hand-built reversal
        with self.assertRaises(LedgerError):
            two_legs(EntryKind.REVERSAL, "pay-1", " ")  # reversal without a reason
        with self.assertRaises(LedgerError):
            two_legs(EntryKind.ORIGINAL, "pay-1", None)  # original with a link


class ReversalTest(unittest.TestCase):
    def setUp(self):
        self.s = sale("pay-1", 12_000, 348)
        self.l = ledger()
        self.l.post(self.s)

    def test_partial_reversal_allocates_in_proportion(self):
        l = self.l
        r1 = l.reverse_partially("pay-1", "ref-1", Money.of_minor(5_000, USD), "return")
        self.assertEqual(r1.debit_total(), 5_000)
        self.assertEqual(len(r1.postings), 3)
        self.assertTrue(any(p.account_id == "2100" and p.amount.minor_units == 4_855 for p in r1.postings))
        self.assertTrue(any(p.account_id == "4100" and p.amount.minor_units == 145 for p in r1.postings))
        self.assertTrue(all(p.side is (Side.CREDIT if p.account_id == "1200" else Side.DEBIT) for p in r1.postings))
        self.assertEqual(l.remaining("pay-1").minor_units, 7_000)
        self.assertIs(l.reverse_partially("pay-1", "ref-1", Money.of_minor(5_000, USD), "retry"), r1)
        self.assertEqual(l.size(), 2)
        with self.assertRaises(IdempotencyConflictError):
            l.reverse_partially("pay-1", "ref-1", Money.of_minor(4_000, USD), "retry")
        with self.assertRaises(ExcessiveReversalError):
            l.reverse_partially("pay-1", "ref-2", Money.of_minor(7_001, USD), "too much")
        self.assertEqual(l.size(), 2)
        self.assertEqual(l.remaining("pay-1").minor_units, 7_000)
        with self.assertRaises(CurrencyMismatchError):
            l.reverse_partially("pay-1", "ref-3", Money.of_minor(1, EUR), "eur")
        with self.assertRaises(LedgerError):
            l.reverse("ref-1", "x", "undo the refund")

    def test_full_reversal_of_the_remainder(self):
        l = self.l
        l.reverse_partially("pay-1", "ref-1", Money.of_minor(5_000, USD), "return")
        r2 = l.reverse("pay-1", "cb-1", "chargeback")
        self.assertEqual(r2.debit_total(), 7_000)
        self.assertTrue(l.is_fully_reversed("pay-1"))
        self.assertTrue(l.balance("1200").is_zero() and l.balance("2100").is_zero() and l.balance("4100").is_zero())
        self.assertIs(l.reverse("pay-1", "cb-1", "retry"), r2)
        self.assertEqual(l.size(), 3)
        with self.assertRaises(AlreadyReversedError):
            l.reverse("pay-1", "cb-2", "dup")
        with self.assertRaises(AlreadyReversedError):
            l.reverse_partially("pay-1", "ref-9", Money.of_minor(1, USD), "late")
        with self.assertRaises(IdempotencyConflictError):
            l.post(sale("pay-2", 1_000, 29))
            l.reverse("pay-2", "cb-1", "wrong id reuse")
        self.assertEqual(l.entry("pay-1"), self.s)
        self.assertIs(l.records()[0].entry, self.s)


class CorrectionTest(unittest.TestCase):
    def setUp(self):
        self.c = ledger()
        self.c.post(sale("pay-200", 8_000, 312))
        self.fixed = self.c.correct("pay-200", "rev-200", "pay-200c", [
            Posting.debit("1200", Money.of_minor(8_000, USD)), Posting.credit("2100", Money.of_minor(7_768, USD)),
            Posting.credit("4100", Money.of_minor(232, USD))], "fee config")

    def test_correction_is_reverse_plus_replace(self):
        c, fixed = self.c, self.fixed
        self.assertEqual(c.size(), 3)
        self.assertIs(c.records()[1].entry.kind, EntryKind.REVERSAL)
        self.assertIs(c.records()[2].entry.kind, EntryKind.CORRECTION)
        self.assertEqual(fixed.relates_to, "pay-200")
        self.assertEqual(c.entry("rev-200").relates_to, "pay-200")
        self.assertEqual(c.balance("4100").minor_units, 232)
        self.assertEqual(c.balance("2100").minor_units, 7_768)
        self.assertTrue(c.is_fully_reversed("pay-200"))
        self.assertEqual(c.correct("pay-200", "rev-200", "pay-200c", fixed.postings, "fee config"), fixed)
        self.assertEqual(c.size(), 3)

    def test_failed_correction_leaves_no_trace_and_chains(self):
        c = self.c
        with self.assertRaises(LedgerError):
            c.correct("pay-200c", "rev-x", "pay-x", [Posting.debit("1200", Money.of_minor(8_000, USD)),
                                                     Posting.credit("2100", Money.of_minor(7_000, USD))], "bad")
        self.assertEqual(c.size(), 3)
        fixed2 = c.correct("pay-200c", "rev-200c", "pay-200cc", [
            Posting.debit("1200", Money.of_minor(8_000, USD)), Posting.credit("2100", Money.of_minor(7_800, USD)),
            Posting.credit("4100", Money.of_minor(200, USD))], "fee waiver")
        self.assertEqual(fixed2.relates_to, "pay-200c")
        self.assertEqual(c.balance("4100").minor_units, 200)
        self.assertEqual([r.entry.id for r in c.history("pay-200")],
                         ["pay-200", "rev-200", "pay-200c", "rev-200c", "pay-200cc"])

    def test_hash_chain_detects_edits_and_deletions(self):
        c = self.c
        c.correct("pay-200c", "rev-200c", "pay-200cc", [
            Posting.debit("1200", Money.of_minor(8_000, USD)), Posting.credit("2100", Money.of_minor(7_800, USD)),
            Posting.credit("4100", Money.of_minor(200, USD))], "fee waiver")
        self.assertEqual(Ledger.verify_chain(c.records()), -1)
        copy = list(c.records())
        v = copy[1]
        copy[1] = JournalRecord(v.seq, JournalEntry(v.entry.id, "edited", v.entry.postings, v.entry.kind,
                                                    v.entry.relates_to, v.entry.reason), v.prev_hash, v.hash)
        self.assertEqual(Ledger.verify_chain(copy), 2)
        dropped = list(c.records())
        del dropped[2]
        self.assertEqual(Ledger.verify_chain(dropped), 3)
        self.assertEqual(len(c.audit_view()), c.size())

    def test_hash_matches_the_java_chain(self):
        # SHA-256 over the same canonical bytes as Java: "<prev>|<seq>|<id>|<KIND>|null|null|<desc>|<legs>"
        genesis_entry = JournalEntry.original("pay-1", "sale", [Posting.debit("1200", Money.of_minor(100, USD)),
                                                                Posting.credit("2100", Money.of_minor(100, USD))])
        self.assertEqual(genesis_entry.canonical(), "pay-1|ORIGINAL|null|null|sale|1200:DEBIT:100:USD|2100:CREDIT:100:USD")
        self.assertEqual(len(JournalRecord.hash_of(1, genesis_entry, JournalRecord.GENESIS)), 64)


class PropertyTest(unittest.TestCase):
    def test_random_partial_refunds_conserve_and_end_at_zero(self):
        r = JavaRandom(9)
        ok = True
        for _ in range(200):
            p = ledger()
            gross = 1 + r.next_int(100_000)
            fee = r.next_int(min(gross, 5_000))
            if fee == gross:
                fee = 0
            if fee == 0:
                e = JournalEntry.original("t", "s", [Posting.debit("1200", Money.of_minor(gross, USD)),
                                                     Posting.credit("2100", Money.of_minor(gross, USD))])
            else:
                e = sale("t", gross, fee)
            p.post(e)
            k = 0
            while p.remaining("t").minor_units > 0 and k < 6:
                rem = p.remaining("t").minor_units
                p.reverse_partially("t", f"r{k}", Money.of_minor(1 + r.next_int(min(rem, (1 << 31) - 1)), USD), "refund")
                k += 1
                ok = ok and sum_balances(p) == 0
            if not p.is_fully_reversed("t"):
                p.reverse("t", "final", "close out")
            ok = ok and p.balance("1200").is_zero() and p.balance("2100").is_zero() and p.balance("4100").is_zero()
        self.assertTrue(ok)


if __name__ == "__main__":
    unittest.main()
