// Tests for phases/10-money-and-ledgers/08-reconciliation. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Collections;
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

    static LedgerPosting ours(String ref, String date, String amount) {
        return new LedgerPosting(ref, LocalDate.parse(date), Money.parse(amount, Currency.USD), "1010");
    }

    static StatementLine theirs(String ref, String date, String amount) {
        return new StatementLine(ref, LocalDate.parse(date), Money.parse(amount, Currency.USD), "stmt");
    }

    public static void main(String[] args) {
        Reconciler r = new Reconciler(Currency.USD, 1);

        // happy path: identical books produce no breaks and tie out
        ReconciliationReport clean = r.reconcile(
                List.of(ours("a", "2026-03-02", "10.00"), ours("b", "2026-03-02", "20.00")),
                List.of(theirs("b", "2026-03-02", "20.00"), theirs("a", "2026-03-02", "10.00")));
        check("clean: two matches", clean.matches().size() == 2);
        check("clean: no breaks", clean.isClean());
        check("clean: ties out", clean.tiesOut());
        check("clean: matched total is the ledger total", clean.matchedTotal().equals(Money.parse("30.00", Currency.USD)));
        check("clean: matches ordered by date then reference", clean.matches().get(0).ours().reference().equals("a"));
        check("clean: rule is EXACT_REFERENCE", clean.matches().stream().allMatch(m -> m.rule() == MatchRule.EXACT_REFERENCE));

        // missing on each side
        ReconciliationReport missing = r.reconcile(
                List.of(ours("a", "2026-03-02", "10.00"), ours("only-ours", "2026-03-02", "88.88")),
                List.of(theirs("a", "2026-03-02", "10.00"), theirs("only-theirs", "2026-03-02", "5.00")));
        check("missing: one MISSING_THEIRS", missing.countBreaks(BreakKind.MISSING_THEIRS) == 1);
        check("missing: one MISSING_OURS", missing.countBreaks(BreakKind.MISSING_OURS) == 1);
        check("missing: MISSING_OURS reported before MISSING_THEIRS", missing.breaks().get(0).kind() == BreakKind.MISSING_OURS);
        check("missing: ours unmatched 88.88", missing.oursUnmatched().equals(Money.parse("88.88", Currency.USD)));
        check("missing: theirs unmatched 5.00", missing.theirsUnmatched().equals(Money.parse("5.00", Currency.USD)));
        check("missing: net difference 83.88", missing.netDifference().equals(Money.parse("83.88", Currency.USD)));
        check("missing: ties out", missing.tiesOut());

        // amount mismatch on a shared reference
        ReconciliationReport mismatch = r.reconcile(
                List.of(ours("x", "2026-03-05", "12.00")),
                List.of(theirs("x", "2026-03-05", "21.00")));
        check("mismatch: one AMOUNT_MISMATCH break", mismatch.countBreaks(BreakKind.AMOUNT_MISMATCH) == 1);
        check("mismatch: no matches", mismatch.matches().isEmpty());
        check("mismatch: difference is -9.00", mismatch.breaks().get(0).difference(Currency.USD).equals(Money.parse("-9.00", Currency.USD)));
        check("mismatch: both sides counted in unmatched totals",
                mismatch.oursUnmatched().minorUnits() == 1200 && mismatch.theirsUnmatched().minorUnits() == 2100);
        check("mismatch: ties out", mismatch.tiesOut());

        // amount+date tolerance matching when references differ
        ReconciliationReport fuzzy = r.reconcile(
                List.of(ours("int_1", "2026-03-04", "64.00")),
                List.of(theirs("txn_9", "2026-03-05", "64.00")));
        check("fuzzy: matched within tolerance", fuzzy.matches().size() == 1 && fuzzy.isClean());
        check("fuzzy: rule is AMOUNT_DATE", fuzzy.matches().get(0).rule() == MatchRule.AMOUNT_DATE);
        ReconciliationReport tooFar = r.reconcile(
                List.of(ours("int_1", "2026-03-04", "64.00")),
                List.of(theirs("txn_9", "2026-03-06", "64.00")));
        check("fuzzy: two days is outside a one-day tolerance", tooFar.matches().isEmpty() && tooFar.breaks().size() == 2);
        ReconciliationReport zeroTol = new Reconciler(Currency.USD, 0).reconcile(
                List.of(ours("int_1", "2026-03-04", "64.00")),
                List.of(theirs("txn_9", "2026-03-05", "64.00")));
        check("fuzzy: zero tolerance needs the same date", zeroTol.matches().isEmpty());
        ReconciliationReport nearest = r.reconcile(
                List.of(ours("int_1", "2026-03-04", "64.00")),
                List.of(theirs("far", "2026-03-05", "64.00"), theirs("near", "2026-03-04", "64.00")));
        check("fuzzy: nearest date wins over an earlier-sorted candidate", nearest.matches().get(0).theirs().reference().equals("near"));
        check("fuzzy: the loser is MISSING_OURS", nearest.breaks().get(0).kind() == BreakKind.MISSING_OURS
                && nearest.breaks().get(0).theirs().reference().equals("far"));
        ReconciliationReport wrongAmount = r.reconcile(
                List.of(ours("int_1", "2026-03-04", "64.00")),
                List.of(theirs("txn_9", "2026-03-04", "64.01")));
        check("fuzzy: a one-cent difference is never a match", wrongAmount.matches().isEmpty());

        // duplicates
        ReconciliationReport dupTheirs = r.reconcile(
                List.of(ours("d", "2026-03-04", "33.10")),
                List.of(theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")));
        check("dup theirs: one match plus one DUPLICATE_THEIRS", dupTheirs.matches().size() == 1 && dupTheirs.countBreaks(BreakKind.DUPLICATE_THEIRS) == 1);
        check("dup theirs: statement total exceeds ledger by one copy", dupTheirs.netDifference().minorUnits() == -3310);
        check("dup theirs: ties out", dupTheirs.tiesOut());
        ReconciliationReport dupOurs = r.reconcile(
                List.of(ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")),
                List.of(theirs("d", "2026-03-04", "33.10")));
        check("dup ours: one DUPLICATE_OURS", dupOurs.countBreaks(BreakKind.DUPLICATE_OURS) == 1);
        ReconciliationReport bothTwice = r.reconcile(
                List.of(ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")),
                List.of(theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")));
        check("same multiplicity on both sides is two matches, not a duplicate", bothTwice.matches().size() == 2 && bothTwice.isClean());

        // the partition invariant on the demo data
        ReconciliationReport demo = r.reconcile(demoLedger(), demoStatement());
        check("demo: 8 matches", demo.matches().size() == 8);
        check("demo: 4 breaks", demo.breaks().size() == 4);
        check("demo: every input is in exactly one match or break",
                demo.matches().size() + demo.breaks().stream().filter(b -> b.ours() != null).count() == demoLedger().size()
                && demo.matches().size() + demo.breaks().stream().filter(b -> b.theirs() != null).count() == demoStatement().size());
        check("demo: ties out", demo.tiesOut());
        check("demo: net difference 88.88 - 9.00 - 33.10 - 5.00 = 41.78", demo.netDifference().equals(Money.parse("41.78", Currency.USD)));

        // determinism: shuffled input gives the identical report
        List<LedgerPosting> shuffledLedger = new ArrayList<>(demoLedger());
        List<StatementLine> shuffledStatement = new ArrayList<>(demoStatement());
        Collections.shuffle(shuffledLedger, new Random(7));
        Collections.shuffle(shuffledStatement, new Random(11));
        ReconciliationReport again = r.reconcile(shuffledLedger, shuffledStatement);
        check("determinism: shuffled input renders byte-for-byte the same report", again.render().equals(demo.render()));
        check("determinism: breaks sorted by kind order", isSortedByKind(demo.breaks()));

        // failures the lesson warns about
        checkThrows("foreign currency is rejected, not summed", CurrencyMismatchException.class, () -> r.reconcile(
                List.of(new LedgerPosting("e", LocalDate.parse("2026-03-04"), Money.parse("1.00", Currency.EUR), "1010")),
                List.of()));
        checkThrows("negative tolerance is rejected", IllegalArgumentException.class, () -> new Reconciler(Currency.USD, -1));
        checkThrows("a Match with unequal amounts cannot be constructed", IllegalArgumentException.class,
                () -> new Match(ours("a", "2026-03-04", "1.00"), theirs("a", "2026-03-04", "2.00"), MatchRule.EXACT_REFERENCE));
        checkThrows("a MISSING_OURS break needs their side", IllegalArgumentException.class,
                () -> new Break(BreakKind.MISSING_OURS, ours("a", "2026-03-04", "1.00"), null));
        ReconciliationReport empty = r.reconcile(List.of(), List.of());
        check("empty books tie out at zero", empty.tiesOut() && empty.ledgerTotal().isZero() && empty.isClean());

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean isSortedByKind(List<Break> breaks) {
        for (int i = 1; i < breaks.size(); i++) {
            if (breaks.get(i - 1).kind().compareTo(breaks.get(i).kind()) > 0) return false;
        }
        return true;
    }

    static List<LedgerPosting> demoLedger() {
        return List.of(
                ours("ch_1001", "2026-03-02", "19.99"), ours("ch_1002", "2026-03-02", "250.00"),
                ours("ch_1003", "2026-03-02", "7.50"), ours("ch_1004", "2026-03-03", "120.00"),
                ours("ch_1005", "2026-03-03", "49.95"), ours("ch_1006", "2026-03-03", "1500.00"),
                ours("ch_1007", "2026-03-04", "33.10"), ours("int_8841", "2026-03-04", "64.00"),
                ours("ch_1009", "2026-03-04", "88.88"), ours("ch_1010", "2026-03-05", "12.00"));
    }

    static List<StatementLine> demoStatement() {
        return List.of(
                theirs("ch_1001", "2026-03-02", "19.99"), theirs("ch_1002", "2026-03-02", "250.00"),
                theirs("ch_1003", "2026-03-02", "7.50"), theirs("ch_1004", "2026-03-03", "120.00"),
                theirs("ch_1005", "2026-03-03", "49.95"), theirs("ch_1006", "2026-03-03", "1500.00"),
                theirs("ch_1007", "2026-03-04", "33.10"), theirs("ch_1007", "2026-03-04", "33.10"),
                theirs("txn_77f2", "2026-03-05", "64.00"), theirs("ch_1010", "2026-03-05", "21.00"),
                theirs("ch_1011", "2026-03-05", "5.00"));
    }
}
