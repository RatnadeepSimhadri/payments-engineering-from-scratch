// Stage 4 tests for projects/ledger-engine: an account's ledger lines net each entry's effect, reconcile
// matches by reference then by amount within a date window, every break lands in exactly one category,
// and the report's totals tie out. Exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage04Test {
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static Money usd(String text) { return Money.parse(text, Currency.USD); }
    static Money eur(String text) { return Money.parse(text, Currency.EUR); }
    static StatementLine st(String ref, String amount, String date) { return new StatementLine(ref, usd(amount), date); }

    static AccountRegistry s4chart() {
        AccountRegistry chart = new AccountRegistry();
        chart.open("bank", AccountType.ASSET, Currency.USD);
        chart.open("sellers", AccountType.LIABILITY, Currency.USD);
        chart.open("fees", AccountType.REVENUE, Currency.USD);
        chart.open("eur-bank", AccountType.ASSET, Currency.EUR);
        return chart;
    }

    /** The seven-entry scenario from the stage doc. Bank lines: +100, -50, +20, -7.50, +15, +6 = 83.50. */
    static Ledger s4ledger() {
        Ledger ledger = new Ledger(s4chart());
        ledger.post(new JournalEntry("sale-1", "2026-10-01",
                new Posting("bank", Side.DEBIT, usd("100.00")), new Posting("sellers", Side.CREDIT, usd("96.80")), new Posting("fees", Side.CREDIT, usd("3.20"))));
        ledger.post(new JournalEntry("payout-1", "2026-10-03",
                new Posting("sellers", Side.DEBIT, usd("50.00")), new Posting("bank", Side.CREDIT, usd("50.00"))));
        ledger.post(new JournalEntry("sale-2", "2026-10-04",
                new Posting("bank", Side.DEBIT, usd("20.00")), new Posting("fees", Side.CREDIT, usd("20.00"))));
        ledger.post(new JournalEntry("fee-adj", "2026-10-04",
                new Posting("fees", Side.DEBIT, usd("1.00")), new Posting("sellers", Side.CREDIT, usd("1.00"))));
        ledger.post(new JournalEntry("refund-1", "2026-10-06",
                new Posting("sellers", Side.DEBIT, usd("7.50")), new Posting("bank", Side.CREDIT, usd("7.50"))));
        ledger.post(new JournalEntry("sale-3", "2026-10-08",
                new Posting("bank", Side.DEBIT, usd("15.00")), new Posting("fees", Side.CREDIT, usd("15.00"))));
        ledger.post(new JournalEntry("multi", "2026-10-09",
                new Posting("bank", Side.DEBIT, usd("10.00")), new Posting("bank", Side.CREDIT, usd("4.00")), new Posting("fees", Side.CREDIT, usd("6.00"))));
        return ledger;
    }

    static List<StatementLine> s4statement() {
        return List.of(
                st("sale-1", "100.00", "2026-10-01"),       // match by ref
                st("PO-77", "-50.00", "2026-10-05"),        // match payout-1 by amount within 3 days
                st("sale-2", "25.00", "2026-10-04"),        // amount mismatch (ledger has 20.00)
                st("sale-1", "100.00", "2026-10-01"),       // duplicate reference
                st("chargeback-3", "-12.00", "2026-10-07"), // missing in ledger
                st("sale-3", "15.00", "2026-10-08"),        // match by ref
                st("RF-9", "-7.50", "2026-10-20"));         // 14 days from refund-1: outside a 3-day window
    }

    static ReconciliationBreak breakFor(ReconciliationReport report, String ref) {
        for (ReconciliationBreak b : report.breaks()) if (b.ref().equals(ref)) return b;
        throw new AssertionError("no break for " + ref);
    }

    public static void main(String[] args) {
        // statement lines are values with a reference, a signed amount, and a date
        StatementLine line = st("sale-1", "100.00", "2026-10-01");
        check("statement line keeps fields", line.ref().equals("sale-1") && line.amount().equals(usd("100.00")) && line.date().equals("2026-10-01"));
        checkThrows("blank statement ref rejected", IllegalArgumentException.class, () -> st(" ", "1.00", "2026-10-01"));
        checkThrows("blank statement date rejected", IllegalArgumentException.class, () -> st("x", "1.00", ""));

        // ledger lines: one per entry that touches the account, netted, in journal order
        Ledger ledger = s4ledger();
        List<LedgerLine> lines = ledger.lines("bank");
        check("six entries touch bank", lines.size() == 6);
        check("lines are in journal order", lines.get(0).ref().equals("sale-1") && lines.get(1).ref().equals("payout-1") && lines.get(5).ref().equals("multi"));
        check("asset debit is positive", lines.get(0).amount().equals(usd("100.00")) && lines.get(0).date().equals("2026-10-01"));
        check("asset credit is negative", lines.get(1).amount().equals(usd("-50.00")));
        check("an entry touching the account twice nets to one line", lines.get(5).amount().equals(usd("6.00")));
        check("entries that skip the account produce no line", lines.stream().noneMatch(l -> l.ref().equals("fee-adj")));
        check("liability lines are signed on the credit side", ledger.lines("sellers").get(0).amount().equals(usd("96.80")) && ledger.lines("sellers").get(1).amount().equals(usd("-50.00")));
        check("untouched account has no lines", ledger.lines("eur-bank").isEmpty());
        checkThrows("lines of unknown account", UnknownAccountException.class, () -> ledger.lines("nope"));

        // the full reconciliation: three matches, six breaks, totals tie out
        ReconciliationReport report = ledger.reconcile("bank", s4statement(), 3);
        check("report names the account and currency", report.accountId().equals("bank") && report.currency().equals(Currency.USD));
        check("ledger total is the sum of the lines", report.ledgerTotal().equals(usd("83.50")));
        check("statement total is the sum of every statement line, duplicates included", report.statementTotal().equals(usd("170.50")));
        check("three matches", report.matches().size() == 3);
        check("pass 1 matches come first", report.matches().get(0).ledgerRef().equals("sale-1") && report.matches().get(0).by() == MatchedBy.REF
                && report.matches().get(1).ledgerRef().equals("sale-3") && report.matches().get(1).by() == MatchedBy.REF);
        ReconciliationMatch byAmount = report.matches().get(2);
        check("pass 2 matches by amount within the window", byAmount.ledgerRef().equals("payout-1") && byAmount.statementRef().equals("PO-77")
                && byAmount.amount().equals(usd("-50.00")) && byAmount.by() == MatchedBy.AMOUNT_AND_DATE);
        check("matched total", report.matchedTotal().equals(usd("65.00")));
        check("six breaks", report.breaks().size() == 6);
        ReconciliationBreak mismatch = breakFor(report, "sale-2");
        check("amount mismatch carries both sides", mismatch.type() == BreakType.AMOUNT_MISMATCH && mismatch.ledgerAmount().equals(usd("20.00")) && mismatch.statementAmount().equals(usd("25.00")));
        ReconciliationBreak dup = breakFor(report, "sale-1");
        check("duplicate statement reference", dup.type() == BreakType.DUPLICATE && dup.ledgerAmount().isZero() && dup.statementAmount().equals(usd("100.00")));
        ReconciliationBreak chargeback = breakFor(report, "chargeback-3");
        check("missing in ledger", chargeback.type() == BreakType.MISSING_IN_LEDGER && chargeback.ledgerAmount().isZero() && chargeback.statementAmount().equals(usd("-12.00")));
        check("outside the window is missing in ledger", breakFor(report, "RF-9").type() == BreakType.MISSING_IN_LEDGER);
        ReconciliationBreak refund = breakFor(report, "refund-1");
        check("missing in statement", refund.type() == BreakType.MISSING_IN_STATEMENT && refund.ledgerAmount().equals(usd("-7.50")) && refund.statementAmount().isZero());
        check("netted multi-leg entry is missing in statement", breakFor(report, "multi").type() == BreakType.MISSING_IN_STATEMENT && breakFor(report, "multi").ledgerAmount().equals(usd("6.00")));
        check("breaksOf filters by category", report.breaksOf(BreakType.MISSING_IN_LEDGER).size() == 2 && report.breaksOf(BreakType.MISSING_IN_STATEMENT).size() == 2
                && report.breaksOf(BreakType.AMOUNT_MISMATCH).size() == 1 && report.breaksOf(BreakType.DUPLICATE).size() == 1);
        check("ledger break total", report.ledgerBreakTotal().equals(usd("18.50")));
        check("statement break total", report.statementBreakTotal().equals(usd("105.50")));
        check("ledger total = matched + ledger breaks", report.ledgerTotal().equals(report.matchedTotal().plus(report.ledgerBreakTotal())));
        check("statement total = matched + statement breaks", report.statementTotal().equals(report.matchedTotal().plus(report.statementBreakTotal())));
        check("report ties out but is not clean", report.tiesOut() && !report.isClean());
        checkThrows("report breaks are read-only", UnsupportedOperationException.class, () -> report.breaks().clear());
        check("reconcile does not change the journal", ledger.size() == 7 && ledger.lines("bank").size() == 6);

        // the date window is inclusive and symmetric
        ReconciliationReport wide = ledger.reconcile("bank", s4statement(), 14);
        check("14-day window matches RF-9 to refund-1", wide.matches().size() == 4 && wide.breaks().size() == 4
                && wide.matches().get(3).ledgerRef().equals("refund-1") && wide.matches().get(3).statementRef().equals("RF-9"));
        check("wide report still ties out", wide.tiesOut() && wide.matchedTotal().equals(usd("57.50")));
        ReconciliationReport narrow = ledger.reconcile("bank", s4statement(), 13);
        check("13-day window does not", narrow.matches().size() == 3);
        ReconciliationReport zero = ledger.reconcile("bank", List.of(st("PO-77", "-50.00", "2026-10-03")), 0);
        check("zero window matches the same day", zero.matches().size() == 1 && zero.matches().get(0).by() == MatchedBy.AMOUNT_AND_DATE);
        ReconciliationReport dayOff = ledger.reconcile("bank", List.of(st("PO-77", "-50.00", "2026-10-02")), 0);
        check("zero window rejects the day before", dayOff.matches().isEmpty() && breakFor(dayOff, "PO-77").type() == BreakType.MISSING_IN_LEDGER);
        ReconciliationReport sign = ledger.reconcile("bank", List.of(st("PO-77", "50.00", "2026-10-03")), 3);
        check("pass 2 compares signed amounts", sign.matches().isEmpty());

        // a clean statement: every line matched, nothing to report
        List<StatementLine> clean = new ArrayList<>();
        for (LedgerLine l : lines) clean.add(new StatementLine(l.ref(), l.amount(), l.date()));
        ReconciliationReport cleanReport = ledger.reconcile("bank", clean, 0);
        check("clean statement matches everything", cleanReport.matches().size() == 6 && cleanReport.isClean() && cleanReport.tiesOut());
        check("clean statement totals agree", cleanReport.ledgerTotal().equals(cleanReport.statementTotal()) && cleanReport.matchedTotal().equals(usd("83.50")));

        // an empty statement: everything is missing in statement
        ReconciliationReport empty = ledger.reconcile("bank", List.of(), 3);
        check("empty statement", empty.matches().isEmpty() && empty.breaksOf(BreakType.MISSING_IN_STATEMENT).size() == 6
                && empty.statementTotal().isZero() && empty.ledgerBreakTotal().equals(usd("83.50")) && empty.tiesOut());

        // an account with no lines: everything on the statement is missing in ledger
        ReconciliationReport noLines = ledger.reconcile("eur-bank", List.of(new StatementLine("w-1", eur("9.99"), "2026-10-01")), 3);
        check("statement against an empty account", noLines.breaks().size() == 1 && noLines.breaks().get(0).type() == BreakType.MISSING_IN_LEDGER
                && noLines.ledgerTotal().equals(eur("0.00")) && noLines.tiesOut());

        // pass 2 ties go to the first unmatched ledger line in journal order
        Ledger twins = new Ledger(s4chart());
        twins.post(new JournalEntry("a1", "2026-10-01", new Posting("bank", Side.DEBIT, usd("10.00")), new Posting("fees", Side.CREDIT, usd("10.00"))));
        twins.post(new JournalEntry("a2", "2026-10-02", new Posting("bank", Side.DEBIT, usd("10.00")), new Posting("fees", Side.CREDIT, usd("10.00"))));
        ReconciliationReport twinReport = twins.reconcile("bank", List.of(st("X", "10.00", "2026-10-02")), 3);
        check("first unmatched ledger line wins", twinReport.matches().get(0).ledgerRef().equals("a1") && breakFor(twinReport, "a2").type() == BreakType.MISSING_IN_STATEMENT);
        ReconciliationReport twinReport2 = twins.reconcile("bank", List.of(st("a2", "10.00", "2026-10-02"), st("X", "10.00", "2026-10-02")), 3);
        check("pass 1 consumes before pass 2 runs", twinReport2.matches().size() == 2 && twinReport2.matches().get(0).ledgerRef().equals("a2") && twinReport2.matches().get(1).ledgerRef().equals("a1") && twinReport2.isClean());

        // rejections: unknown account, negative window, wrong currency, bad date
        checkThrows("reconcile unknown account", UnknownAccountException.class, () -> ledger.reconcile("nope", List.of(), 3));
        checkThrows("negative window", IllegalArgumentException.class, () -> ledger.reconcile("bank", List.of(), -1));
        checkThrows("statement currency must match the account", CurrencyMismatchException.class,
                () -> ledger.reconcile("bank", List.of(new StatementLine("e", eur("1.00"), "2026-10-01")), 3));
        checkThrows("statement date must be ISO-8601", LedgerException.class, () -> ledger.reconcile("bank", List.of(st("d", "1.00", "10/01/2026")), 3));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
