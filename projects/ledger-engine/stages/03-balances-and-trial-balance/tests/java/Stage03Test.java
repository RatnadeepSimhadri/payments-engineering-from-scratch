// Stage 3 tests for projects/ledger-engine: balances are derived from the journal (signed in the account's
// normal direction), snapshots every K entries agree with full replay, balanceAt reads any point in time,
// the trial balance ties per currency, and reversals mirror without editing history. Exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage03Test {
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

    static AccountRegistry s3chart() {
        AccountRegistry chart = new AccountRegistry();
        chart.open("bank", AccountType.ASSET, Currency.USD);
        chart.open("receivable", AccountType.ASSET, Currency.USD);
        chart.open("sellers", AccountType.LIABILITY, Currency.USD);
        chart.open("fees", AccountType.REVENUE, Currency.USD);
        chart.open("ops", AccountType.EXPENSE, Currency.USD);
        chart.open("eur-bank", AccountType.ASSET, Currency.EUR);
        chart.open("eur-capital", AccountType.EQUITY, Currency.EUR);
        return chart;
    }

    /** The four-entry scenario from the stage doc, with a snapshot after every second entry. */
    static Ledger s3ledger() {
        Ledger ledger = new Ledger(s3chart(), 2);
        ledger.post(new JournalEntry("sale-1", "2026-10-01",
                new Posting("bank", Side.DEBIT, usd("100.00")),
                new Posting("sellers", Side.CREDIT, usd("96.80")),
                new Posting("fees", Side.CREDIT, usd("3.20"))));
        ledger.post(new JournalEntry("payout-1", "2026-10-03",
                new Posting("sellers", Side.DEBIT, usd("50.00")),
                new Posting("bank", Side.CREDIT, usd("50.00"))));
        ledger.post(new JournalEntry("rent", "2026-10-04",
                new Posting("ops", Side.DEBIT, usd("30.00")),
                new Posting("bank", Side.CREDIT, usd("30.00"))));
        ledger.post(new JournalEntry("sale-2", "2026-10-05",
                new Posting("bank", Side.DEBIT, usd("20.00")),
                new Posting("fees", Side.CREDIT, usd("20.00"))));
        return ledger;
    }

    static TrialBalanceLine line(TrialBalance tb, String accountId) {
        for (TrialBalanceLine l : tb.lines()) if (l.account().id().equals(accountId)) return l;
        throw new AssertionError("no trial balance line for " + accountId);
    }

    public static void main(String[] args) {
        // balances are signed in the account's normal direction
        Ledger ledger = s3ledger();
        check("asset balance grows on debit", ledger.balance("bank").equals(usd("40.00")));
        check("liability balance grows on credit", ledger.balance("sellers").equals(usd("46.80")));
        check("revenue balance grows on credit", ledger.balance("fees").equals(usd("23.20")));
        check("expense balance grows on debit", ledger.balance("ops").equals(usd("30.00")));
        check("untouched account is the currency's zero", ledger.balance("receivable").equals(usd("0.00")));
        check("untouched EUR account is EUR zero", ledger.balance("eur-bank").equals(eur("0.00")));
        checkThrows("balance of unknown account", UnknownAccountException.class, () -> ledger.balance("nope"));

        // balanceAt reads the journal as of a sequence number
        check("balanceAt 0 is zero", ledger.balanceAt("bank", 0).equals(usd("0.00")));
        check("balanceAt 1", ledger.balanceAt("bank", 1).equals(usd("100.00")));
        check("balanceAt 2", ledger.balanceAt("bank", 2).equals(usd("50.00")));
        check("balanceAt 3", ledger.balanceAt("bank", 3).equals(usd("20.00")));
        check("balanceAt 4 equals balance", ledger.balanceAt("bank", 4).equals(ledger.balance("bank")));
        check("balanceAt past the end is the current balance", ledger.balanceAt("bank", 999).equals(usd("40.00")));
        check("balanceAt for a liability", ledger.balanceAt("sellers", 1).equals(usd("96.80")));
        checkThrows("balanceAt unknown account", UnknownAccountException.class, () -> ledger.balanceAt("nope", 1));

        // snapshots are taken after every K-th entry and hold every account
        check("snapshotEvery is kept", ledger.snapshotEvery() == 2);
        List<BalanceSnapshot> snaps = ledger.snapshots();
        check("snapshots at 2 and 4", snaps.size() == 2 && snaps.get(0).sequence() == 2 && snaps.get(1).sequence() == 4);
        check("snapshot 2 holds bank 50.00", snaps.get(0).balances().get("bank").equals(usd("50.00")));
        check("snapshot 2 holds sellers 46.80", snaps.get(0).balances().get("sellers").equals(usd("46.80")));
        check("snapshot covers every account", snaps.get(0).balances().size() == 7 && snaps.get(0).balances().get("eur-capital").equals(eur("0.00")));
        check("snapshot 4 agrees with balanceAt 4", snaps.get(1).balances().get("bank").equals(ledger.balanceAt("bank", 4)));
        checkThrows("snapshot balances are read-only", UnsupportedOperationException.class, () -> snaps.get(0).balances().clear());
        Ledger plain = new Ledger(s3chart());
        check("default snapshot interval is 100", plain.snapshotEvery() == Ledger.DEFAULT_SNAPSHOT_EVERY && Ledger.DEFAULT_SNAPSHOT_EVERY == 100);
        check("no snapshot before the interval", plain.snapshots().isEmpty());
        checkThrows("snapshotEvery must be >= 1", IllegalArgumentException.class, () -> new Ledger(s3chart(), 0));

        // balance() after a snapshot replays only the tail and agrees with full recomputation
        ledger.post(new JournalEntry("sale-3", "2026-10-06",
                new Posting("bank", Side.DEBIT, usd("5.00")), new Posting("fees", Side.CREDIT, usd("5.00"))));
        check("balance after snapshot + tail", ledger.balance("bank").equals(usd("45.00")));
        check("balance agrees with balanceAt(size)", ledger.balance("bank").equals(ledger.balanceAt("bank", ledger.size())));
        check("no snapshot at an odd sequence", ledger.snapshots().size() == 2);

        // the trial balance ties out per currency
        Ledger fresh = s3ledger();
        List<TrialBalance> tbs = fresh.trialBalance();
        check("one trial balance per currency, in code order", tbs.size() == 2 && tbs.get(0).currency().equals(Currency.EUR) && tbs.get(1).currency().equals(Currency.USD));
        TrialBalance usdTb = tbs.get(1);
        check("USD debits total 70.00", usdTb.totalDebits().equals(usd("70.00")));
        check("USD credits total 70.00", usdTb.totalCredits().equals(usd("70.00")));
        check("USD trial balance is balanced", usdTb.isBalanced() && usdTb.difference().equals(usd("0.00")));
        check("USD trial balance has five lines", usdTb.lines().size() == 5);
        check("bank sits in the debit column", line(usdTb, "bank").debit().equals(usd("40.00")) && line(usdTb, "bank").credit().equals(usd("0.00")));
        check("sellers sits in the credit column", line(usdTb, "sellers").credit().equals(usd("46.80")) && line(usdTb, "sellers").debit().equals(usd("0.00")));
        check("fees sits in the credit column", line(usdTb, "fees").credit().equals(usd("23.20")));
        check("ops sits in the debit column", line(usdTb, "ops").debit().equals(usd("30.00")));
        check("zero account shows zero in both columns", line(usdTb, "receivable").debit().isZero() && line(usdTb, "receivable").credit().isZero());
        TrialBalance eurTb = tbs.get(0);
        check("empty EUR trial balance is balanced at zero", eurTb.isBalanced() && eurTb.totalDebits().equals(eur("0.00")) && eurTb.lines().size() == 2);
        checkThrows("trial balance lines are read-only", UnsupportedOperationException.class, () -> usdTb.lines().clear());

        // a liability driven negative moves to the debit column; the totals still tie
        fresh.post(new JournalEntry("over-payout", "2026-10-06",
                new Posting("sellers", Side.DEBIT, usd("60.00")), new Posting("bank", Side.CREDIT, usd("60.00"))));
        check("overdrawn liability has a negative signed balance", fresh.balance("sellers").equals(usd("-13.20")));
        TrialBalance after = fresh.trialBalance().get(1);
        check("negative liability shows in the debit column", line(after, "sellers").debit().equals(usd("13.20")) && line(after, "sellers").credit().isZero());
        check("negative asset shows in the credit column", line(after, "bank").credit().equals(usd("20.00")) && line(after, "bank").debit().isZero());
        check("trial balance still ties after the overdraw", after.isBalanced() && after.totalDebits().equals(usd("43.20")));

        // EUR activity lands only in the EUR trial balance
        fresh.post(new JournalEntry("capital", "2026-10-06",
                new Posting("eur-bank", Side.DEBIT, eur("10.00")), new Posting("eur-capital", Side.CREDIT, eur("10.00"))));
        List<TrialBalance> both = fresh.trialBalance();
        check("EUR trial balance ties at 10.00", both.get(0).totalDebits().equals(eur("10.00")) && both.get(0).isBalanced());
        check("USD totals unchanged by EUR entry", both.get(1).totalDebits().equals(usd("43.20")));

        // reversals: post the mirror, never edit the original
        Ledger rev = s3ledger();
        PostedEntry reversal = rev.reverse("rent");
        check("reversal id is rev:<id>", reversal.id().equals("rev:rent") && Ledger.reversalId("rent").equals("rev:rent"));
        check("reversal gets the next sequence", reversal.sequence() == 5 && rev.size() == 5);
        check("reversal keeps the original date", reversal.entry().date().equals("2026-10-04"));
        check("reversal mirrors every posting", reversal.entry().postings().equals(List.of(
                new Posting("ops", Side.CREDIT, usd("30.00")), new Posting("bank", Side.DEBIT, usd("30.00")))));
        check("original is untouched", rev.entry("rent").sequence() == 3 && rev.entry("rent").entry().postings().get(0).side() == Side.DEBIT);
        check("reversal undoes the balance effect", rev.balance("ops").equals(usd("0.00")) && rev.balance("bank").equals(usd("70.00")));
        check("history still shows the original at sequence 3", rev.balanceAt("ops", 3).equals(usd("30.00")) && rev.balanceAt("ops", 5).isZero());
        check("isReversed", rev.isReversed("rent") && !rev.isReversed("sale-1"));
        PostedEntry again = rev.reverse("rent");
        check("reversing twice returns the first reversal", again.equals(reversal) && rev.size() == 5);
        checkThrows("reversing an unknown entry", UnknownEntryException.class, () -> rev.reverse("nope"));
        check("trial balance ties after a reversal", rev.trialBalance().get(1).isBalanced() && rev.trialBalance().get(1).totalDebits().equals(usd("70.00")));
        check("reversal is a balanced entry in the journal", rev.journal().get(4).id().equals("rev:rent"));
        PostedEntry rr = rev.reverse("rev:rent");
        check("a reversal can itself be reversed", rr.id().equals("rev:rev:rent") && rev.balance("ops").equals(usd("30.00")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
