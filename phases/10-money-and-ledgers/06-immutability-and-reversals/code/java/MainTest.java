// Tests for phases/10-money-and-ledgers/06-immutability-and-reversals. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static final Currency USD = Currency.USD;

    static Ledger ledger() {
        Ledger l = new Ledger();
        l.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        l.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, USD);
        l.openAccount("4100", "Fee Revenue", AccountType.REVENUE, USD);
        return l;
    }

    static JournalEntry sale(String id, long gross, long fee) {
        return JournalEntry.original(id, "sale", List.of(Posting.debit("1200", Money.ofMinor(gross, USD)),
                Posting.credit("2100", Money.ofMinor(gross - fee, USD)), Posting.credit("4100", Money.ofMinor(fee, USD))));
    }

    static long sumBalances(Ledger l) {
        return l.balance("1200").minorUnits() - l.balance("2100").minorUnits() - l.balance("4100").minorUnits();
    }

    public static void main(String[] args) {
        // immutability of values
        JournalEntry s = sale("pay-1", 12_000, 348);
        checkThrows("postings list is unmodifiable", UnsupportedOperationException.class, () -> s.postings().clear());
        Ledger l = ledger();
        l.post(s);
        checkThrows("journal view is unmodifiable", UnsupportedOperationException.class, () -> l.records().remove(0));
        check("idempotent post of identical entry", !l.post(sale("pay-1", 12_000, 348)) && l.size() == 1);
        checkThrows("same id different content conflicts", IdempotencyConflictException.class, () -> l.post(sale("pay-1", 12_000, 349)));
        checkThrows("post refuses a hand-built reversal", LedgerException.class, () -> l.post(new JournalEntry("x", "d",
                List.of(Posting.debit("2100", Money.ofMinor(1, USD)), Posting.credit("1200", Money.ofMinor(1, USD))), EntryKind.REVERSAL, "pay-1", "r")));
        checkThrows("reversal without a reason cannot be built", LedgerException.class, () -> new JournalEntry("x", "d",
                List.of(Posting.debit("2100", Money.ofMinor(1, USD)), Posting.credit("1200", Money.ofMinor(1, USD))), EntryKind.REVERSAL, "pay-1", " "));
        checkThrows("original with a link cannot be built", LedgerException.class, () -> new JournalEntry("x", "d",
                List.of(Posting.debit("2100", Money.ofMinor(1, USD)), Posting.credit("1200", Money.ofMinor(1, USD))), EntryKind.ORIGINAL, "pay-1", null));

        // partial reversal
        JournalEntry r1 = l.reversePartially("pay-1", "ref-1", Money.ofMinor(5_000, USD), "return");
        check("partial reversal is balanced and sized", r1.debitTotal() == 5_000 && r1.postings().size() == 3);
        check("partial: payable leg proportional (48.55)", r1.postings().stream().anyMatch(p -> p.accountId().equals("2100") && p.amount().minorUnits() == 4_855));
        check("partial: fee leg proportional (1.45)", r1.postings().stream().anyMatch(p -> p.accountId().equals("4100") && p.amount().minorUnits() == 145));
        check("partial: sides flipped", r1.postings().stream().allMatch(p -> p.side() == (p.accountId().equals("1200") ? Side.CREDIT : Side.DEBIT)));
        check("remaining after partial", l.remaining("pay-1").minorUnits() == 7_000);
        check("idempotent partial reversal", l.reversePartially("pay-1", "ref-1", Money.ofMinor(5_000, USD), "retry") == r1 && l.size() == 2);
        checkThrows("same refund id different amount conflicts", IdempotencyConflictException.class,
                () -> l.reversePartially("pay-1", "ref-1", Money.ofMinor(4_000, USD), "retry"));
        checkThrows("refund larger than remaining", ExcessiveReversalException.class,
                () -> l.reversePartially("pay-1", "ref-2", Money.ofMinor(7_001, USD), "too much"));
        check("rejected refund left no trace", l.size() == 2 && l.remaining("pay-1").minorUnits() == 7_000);
        checkThrows("refund currency mismatch", CurrencyMismatchException.class,
                () -> l.reversePartially("pay-1", "ref-3", Money.ofMinor(1, Currency.EUR), "eur"));
        checkThrows("cannot reverse a reversal", LedgerException.class, () -> l.reverse("ref-1", "x", "undo the refund"));

        // full reversal of the remainder returns every balance to zero
        JournalEntry r2 = l.reverse("pay-1", "cb-1", "chargeback");
        check("full reversal covers exactly the remainder", r2.debitTotal() == 7_000);
        check("fully reversed", l.isFullyReversed("pay-1"));
        check("every balance back to zero", l.balance("1200").isZero() && l.balance("2100").isZero() && l.balance("4100").isZero());
        check("idempotent full reversal", l.reverse("pay-1", "cb-1", "retry") == r2 && l.size() == 3);
        checkThrows("second reversal with new id rejected", AlreadyReversedException.class, () -> l.reverse("pay-1", "cb-2", "dup"));
        checkThrows("partial after full rejected", AlreadyReversedException.class, () -> l.reversePartially("pay-1", "ref-9", Money.ofMinor(1, USD), "late"));
        checkThrows("child id reused for different original", IdempotencyConflictException.class, () -> {
            l.post(sale("pay-2", 1_000, 29));
            l.reverse("pay-2", "cb-1", "wrong id reuse");
        });
        check("original still in journal, untouched", l.entry("pay-1").equals(s) && l.records().get(0).entry() == s);

        // correction
        Ledger c = ledger();
        c.post(sale("pay-200", 8_000, 312));
        JournalEntry fixed = c.correct("pay-200", "rev-200", "pay-200c", List.of(Posting.debit("1200", Money.ofMinor(8_000, USD)),
                Posting.credit("2100", Money.ofMinor(7_768, USD)), Posting.credit("4100", Money.ofMinor(232, USD))), "fee config");
        check("correction appends reversal then replacement", c.size() == 3 && c.records().get(1).entry().kind() == EntryKind.REVERSAL
                && c.records().get(2).entry().kind() == EntryKind.CORRECTION);
        check("correction links to original", fixed.relatesTo().equals("pay-200") && c.entry("rev-200").relatesTo().equals("pay-200"));
        check("balances reflect only the corrected entry", c.balance("4100").minorUnits() == 232 && c.balance("2100").minorUnits() == 7_768);
        check("original fully reversed by correction", c.isFullyReversed("pay-200"));
        check("correction is idempotent", c.correct("pay-200", "rev-200", "pay-200c", fixed.postings(), "fee config").equals(fixed) && c.size() == 3);
        checkThrows("unbalanced replacement rejected before anything is posted", LedgerException.class, () -> c.correct("pay-200c", "rev-x", "pay-x",
                List.of(Posting.debit("1200", Money.ofMinor(8_000, USD)), Posting.credit("2100", Money.ofMinor(7_000, USD))), "bad"));
        check("failed correction left no trace", c.size() == 3);
        JournalEntry fixed2 = c.correct("pay-200c", "rev-200c", "pay-200cc", List.of(Posting.debit("1200", Money.ofMinor(8_000, USD)),
                Posting.credit("2100", Money.ofMinor(7_800, USD)), Posting.credit("4100", Money.ofMinor(200, USD))), "fee waiver");
        check("a correction can itself be corrected", fixed2.relatesTo().equals("pay-200c") && c.balance("4100").minorUnits() == 200);
        check("history follows the chain transitively", c.history("pay-200").stream().map(r -> r.entry().id()).toList()
                .equals(List.of("pay-200", "rev-200", "pay-200c", "rev-200c", "pay-200cc")));

        // hash chain
        check("chain intact", Ledger.verifyChain(c.records()) == -1);
        List<JournalRecord> copy = new ArrayList<>(c.records());
        JournalRecord v = copy.get(1);
        copy.set(1, new JournalRecord(v.seq(), new JournalEntry(v.entry().id(), "edited", v.entry().postings(), v.entry().kind(),
                v.entry().relatesTo(), v.entry().reason()), v.prevHash(), v.hash()));
        check("edited record detected at its seq", Ledger.verifyChain(copy) == 2);
        List<JournalRecord> dropped = new ArrayList<>(c.records());
        dropped.remove(2);
        check("deleted record detected", Ledger.verifyChain(dropped) == 3);
        check("audit view has one line per record", c.auditView().size() == c.size());

        // property: random partial refunds never over-reverse and always conserve
        Random r = new Random(9);
        boolean ok = true;
        for (int trial = 0; trial < 200; trial++) {
            Ledger p = ledger();
            long gross = 1 + r.nextInt(100_000), fee = r.nextInt((int) Math.min(gross, 5_000));
            if (fee == gross) fee = 0;
            JournalEntry e = fee == 0
                    ? JournalEntry.original("t", "s", List.of(Posting.debit("1200", Money.ofMinor(gross, USD)), Posting.credit("2100", Money.ofMinor(gross, USD))))
                    : sale("t", gross, fee);
            p.post(e);
            int k = 0;
            while (p.remaining("t").minorUnits() > 0 && k < 6) {
                long rem = p.remaining("t").minorUnits();
                p.reversePartially("t", "r" + k++, Money.ofMinor(1 + r.nextInt((int) Math.min(rem, Integer.MAX_VALUE)), USD), "refund");
                ok &= sumBalances(p) == 0;
            }
            if (!p.isFullyReversed("t")) p.reverse("t", "final", "close out");
            ok &= p.balance("1200").isZero() && p.balance("2100").isZero() && p.balance("4100").isZero();
        }
        check("property: 200 random refund sequences conserve and end at zero", ok);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
