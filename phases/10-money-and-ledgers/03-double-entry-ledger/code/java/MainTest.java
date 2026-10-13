// Tests for phases/10-money-and-ledgers/03-double-entry-ledger. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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

    static Ledger paymentsLedger() {
        Ledger ledger = new Ledger();
        ledger.openAccount("1010", "Settlement Bank", AccountType.ASSET, Currency.USD);
        ledger.openAccount("1200", "Processor Receivable", AccountType.ASSET, Currency.USD);
        ledger.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, Currency.USD);
        ledger.openAccount("4100", "Fee Revenue", AccountType.REVENUE, Currency.USD);
        ledger.openAccount("2200", "EUR Merchant Payable", AccountType.LIABILITY, Currency.EUR);
        return ledger;
    }

    static Money usd(String s) { return Money.parse(s, Currency.USD); }

    static JournalEntry sale(String id) {
        return new JournalEntry(id, "card payment", List.of(
                Posting.debit("1200", usd("100.00")),
                Posting.credit("2100", usd("97.10")),
                Posting.credit("4100", usd("2.90"))));
    }

    public static void main(String[] args) {
        // normal balances
        check("asset normal balance is debit", AccountType.ASSET.normalBalance() == Side.DEBIT);
        check("expense normal balance is debit", AccountType.EXPENSE.normalBalance() == Side.DEBIT);
        check("liability normal balance is credit", AccountType.LIABILITY.normalBalance() == Side.CREDIT);
        check("equity normal balance is credit", AccountType.EQUITY.normalBalance() == Side.CREDIT);
        check("revenue normal balance is credit", AccountType.REVENUE.normalBalance() == Side.CREDIT);

        // entry construction enforces the balance rule
        checkThrows("unbalanced entry rejected", UnbalancedEntryException.class, () -> new JournalEntry("u1", "x", List.of(
                Posting.debit("1200", usd("100.00")), Posting.credit("2100", usd("97.10")))));
        checkThrows("mixed-currency entry rejected", UnbalancedEntryException.class, () -> new JournalEntry("m1", "x", List.of(
                Posting.debit("1200", usd("100.00")), Posting.credit("2200", Money.parse("100.00", Currency.EUR)))));
        checkThrows("single posting rejected", UnbalancedEntryException.class, () -> new JournalEntry("s1", "x", List.of(
                Posting.debit("1200", usd("1.00")))));
        checkThrows("zero posting rejected", InvalidPostingException.class, () -> Posting.debit("1200", usd("0.00")));
        checkThrows("negative posting rejected", InvalidPostingException.class, () -> Posting.credit("1200", usd("-1.00")));
        check("three-leg entry balances", sale("ok").totalDebits(Currency.USD).equals(usd("100.00")));

        // posting the worked example
        Ledger ledger = paymentsLedger();
        check("first post appends", ledger.post(sale("pay-001")));
        check("receivable is 100.00", ledger.balance("1200").equals(usd("100.00")));
        check("merchant payable is 97.10", ledger.balance("2100").equals(usd("97.10")));
        check("fee revenue is 2.90", ledger.balance("4100").equals(usd("2.90")));
        check("bank untouched", ledger.balance("1010").isZero());
        check("journal size 1", ledger.size() == 1);

        // idempotency
        check("duplicate id is a no-op", !ledger.post(sale("pay-001")));
        check("duplicate did not change balance", ledger.balance("1200").equals(usd("100.00")));
        check("duplicate did not grow journal", ledger.size() == 1);
        checkThrows("same id different content is a conflict", IdempotencyConflictException.class, () -> ledger.post(
                new JournalEntry("pay-001", "card payment", List.of(
                        Posting.debit("1200", usd("50.00")), Posting.credit("2100", usd("50.00"))))));
        check("conflict left no trace", ledger.size() == 1 && ledger.balance("1200").equals(usd("100.00")));

        // ledger-level validation: unknown account, account/posting currency mismatch; no partial writes
        checkThrows("unknown account rejected", UnknownAccountException.class, () -> ledger.post(new JournalEntry("x1", "x", List.of(
                Posting.debit("9999", usd("1.00")), Posting.credit("2100", usd("1.00"))))));
        checkThrows("posting currency must match account currency", InvalidPostingException.class, () -> ledger.post(
                new JournalEntry("x2", "x", List.of(
                        Posting.debit("2200", usd("1.00")), Posting.credit("2100", usd("1.00"))))));
        check("rejected entries left the journal untouched", ledger.size() == 1 && ledger.balance("2100").equals(usd("97.10")));

        // trial balance always sums to zero
        ledger.post(new JournalEntry("stl-001", "settlement", List.of(Posting.debit("1010", usd("100.00")), Posting.credit("1200", usd("100.00")))));
        ledger.post(new JournalEntry("out-001", "payout", List.of(Posting.debit("2100", usd("97.10")), Posting.credit("1010", usd("97.10")))));
        TrialBalance tb = ledger.trialBalance(Currency.USD);
        check("trial balance debits equal credits", tb.balances());
        check("trial balance totals are 2.90", tb.totalDebits().equals(usd("2.90")) && tb.totalCredits().equals(usd("2.90")));
        check("bank holds the fee", ledger.balance("1010").equals(usd("2.90")));
        check("payable settled to zero", ledger.balance("2100").isZero());

        // reversal returns balances to the prior state, and the original is still there
        Money bankBefore = ledger.balance("1010"), payableBefore = ledger.balance("2100"), feeBefore = ledger.balance("4100");
        JournalEntry refund = ledger.reverse("pay-001", "rev-001", "refund");
        check("reversal records what it reverses", "pay-001".equals(refund.reversalOf()));
        check("reversal flips every side", refund.postings().get(0).side() == Side.CREDIT && refund.postings().get(1).side() == Side.DEBIT);
        check("reversal moved fee revenue back by 2.90", ledger.balance("4100").equals(feeBefore.minus(usd("2.90"))));
        check("reversal moved payable back by 97.10", ledger.balance("2100").equals(payableBefore.minus(usd("97.10"))));
        check("reversal did not touch the bank", ledger.balance("1010").equals(bankBefore));
        check("original entry still in journal", ledger.entry("pay-001").equals(sale("pay-001")));
        check("journal grew by one", ledger.size() == 4);
        check("trial balance still balances after reversal", ledger.trialBalance(Currency.USD).balances());
        checkThrows("cannot reverse twice", AlreadyReversedException.class, () -> ledger.reverse("pay-001", "rev-002", "again"));
        checkThrows("cannot reverse unknown entry", UnknownEntryException.class, () -> ledger.reverse("nope", "rev-003", "x"));

        // a fresh ledger: posting then reversing is exactly the identity on every balance
        Ledger fresh = paymentsLedger();
        fresh.post(sale("p"));
        fresh.reverse("p", "r", "undo");
        check("post+reverse is identity on receivable", fresh.balance("1200").isZero());
        check("post+reverse is identity on payable", fresh.balance("2100").isZero());
        check("post+reverse is identity on revenue", fresh.balance("4100").isZero());

        // many seeded random balanced entries: trial balance zero, cache equals recomputation
        Ledger stress = paymentsLedger();
        RandomEntries gen = new RandomEntries(7, List.of("1010", "1200", "2100", "4100"), Currency.USD);
        for (int i = 0; i < 5_000; i++) stress.post(gen.next());
        TrialBalance stressTb = stress.trialBalance(Currency.USD);
        check("random entries: trial balance balances", stressTb.balances());
        check("random entries: totals are non-trivial", stressTb.totalDebits().isPositive());
        long netSum = 0;
        boolean cacheAgrees = true;
        for (String id : List.of("1010", "1200", "2100", "4100")) {
            netSum += stress.netDebit(id).minorUnits();
            cacheAgrees &= stress.netDebit(id).equals(stress.netDebitFromPostings(id));
        }
        check("random entries: net debits sum to zero", netSum == 0);
        check("random entries: cached balances equal recomputation from postings", cacheAgrees);
        check("random entries: journal holds every entry", stress.size() == 5_000);
        check("random generator is deterministic", new RandomEntries(7, List.of("a", "b"), Currency.USD).next()
                .equals(new RandomEntries(7, List.of("a", "b"), Currency.USD).next()));

        // the journal is append-only from the outside
        checkThrows("entries() is read-only", UnsupportedOperationException.class, () -> stress.entries().clear());
        checkThrows("entry postings are immutable", UnsupportedOperationException.class, () -> sale("i").postings().clear());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
