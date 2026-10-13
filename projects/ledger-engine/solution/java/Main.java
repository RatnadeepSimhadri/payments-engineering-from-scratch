// Project: projects/ledger-engine — reference solution demo: open accounts, post, reverse, reconcile.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: python3 scripts/project_test.py ledger-engine --lang java --all --solution
// Java 21, standard library only. Deterministic: no clock, no randomness, no I/O beyond stdout.

import java.util.List;

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Ledger Engine: a double-entry ledger that reconciles itself ==");

        // Stage 1: a chart of accounts
        AccountRegistry chart = new AccountRegistry();
        chart.open("bank", AccountType.ASSET, Currency.USD);
        chart.open("receivable", AccountType.ASSET, Currency.USD);
        chart.open("payable:sellers", AccountType.LIABILITY, Currency.USD);
        chart.open("revenue:fees", AccountType.REVENUE, Currency.USD);
        for (Account account : chart.all()) {
            System.out.printf("  %-16s %-9s %s  grows on %s%n", account.id(), account.type(), account.currency().code(), account.normalSide());
        }

        // Stage 2: post balanced entries; the ledger refuses anything else
        Ledger ledger = new Ledger(chart, 2);
        Money sale = Money.parse("100.00", Currency.USD);
        Money fee = sale.times(29, 1000, Rounding.HALF_EVEN).plus(Money.ofMinor(30, Currency.USD));
        ledger.post(new JournalEntry("sale-1", "2026-10-01",
                new Posting("bank", Side.DEBIT, sale),
                new Posting("payable:sellers", Side.CREDIT, sale.minus(fee)),
                new Posting("revenue:fees", Side.CREDIT, fee)));
        ledger.post(new JournalEntry("payout-1", "2026-10-03",
                new Posting("payable:sellers", Side.DEBIT, Money.parse("50.00", Currency.USD)),
                new Posting("bank", Side.CREDIT, Money.parse("50.00", Currency.USD))));
        ledger.post(new JournalEntry("sale-2", "2026-10-04",
                new Posting("bank", Side.DEBIT, Money.parse("20.00", Currency.USD)),
                new Posting("revenue:fees", Side.CREDIT, Money.parse("20.00", Currency.USD))));
        try {
            ledger.post(new JournalEntry("bad", "2026-10-04",
                    new Posting("bank", Side.DEBIT, Money.parse("1.00", Currency.USD)),
                    new Posting("revenue:fees", Side.CREDIT, Money.parse("0.99", Currency.USD))));
        } catch (LedgerException e) {
            System.out.println("\nrejected: " + e.getMessage());
        }
        System.out.println("journal has " + ledger.size() + " entries; fee on 100.00 = " + fee);

        // Stage 3: balances are derived, the trial balance ties, reversals mirror
        System.out.println("\nbalances: bank " + ledger.balance("bank") + ", sellers " + ledger.balance("payable:sellers")
                + ", fees " + ledger.balance("revenue:fees"));
        TrialBalance tb = ledger.trialBalance().get(0);
        System.out.println("trial balance " + tb.currency().code() + ": debits " + tb.totalDebits() + " credits " + tb.totalCredits()
                + " difference " + tb.difference());
        ledger.reverse("sale-2");
        System.out.println("after reversing sale-2: bank " + ledger.balance("bank") + ", fees " + ledger.balance("revenue:fees")
                + ", snapshots at " + ledger.snapshots().stream().map(s -> Long.toString(s.sequence())).toList());

        // Stage 4: the processor's statement does not quite agree
        List<StatementLine> statement = List.of(
                new StatementLine("sale-1", Money.parse("100.00", Currency.USD), "2026-10-01"),
                new StatementLine("PAYOUT-0001", Money.parse("-50.00", Currency.USD), "2026-10-04"),
                new StatementLine("sale-2", Money.parse("20.00", Currency.USD), "2026-10-04"),
                new StatementLine("sale-2", Money.parse("20.00", Currency.USD), "2026-10-04"),
                new StatementLine("refund-9", Money.parse("-5.00", Currency.USD), "2026-10-05"));
        ReconciliationReport report = ledger.reconcile("bank", statement, 3);
        System.out.println("\nreconcile bank: " + report.matches().size() + " matched, " + report.breaks().size() + " breaks");
        for (ReconciliationMatch m : report.matches()) System.out.println("  match  " + m.ledgerRef() + " ~ " + m.statementRef() + " " + m.amount() + " by " + m.by());
        for (ReconciliationBreak b : report.breaks()) System.out.println("  break  " + b.type() + " " + b.ref() + " ledger " + b.ledgerAmount() + " statement " + b.statementAmount());
        System.out.println("ledger total " + report.ledgerTotal() + " = matched " + report.matchedTotal() + " + breaks " + report.ledgerBreakTotal()
                + "; statement total " + report.statementTotal() + " = matched + breaks " + report.statementBreakTotal()
                + "; ties out: " + report.tiesOut());
    }
}
