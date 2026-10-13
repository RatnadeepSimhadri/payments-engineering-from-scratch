// Stage 2 tests for projects/ledger-engine: postings and entries are values, the journal only accepts balanced
// single-currency entries, sequence numbers are dense, posting is idempotent on id. Exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage02Test {
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

    static AccountRegistry chart() {
        AccountRegistry chart = new AccountRegistry();
        chart.open("bank", AccountType.ASSET, Currency.USD);
        chart.open("sellers", AccountType.LIABILITY, Currency.USD);
        chart.open("fees", AccountType.REVENUE, Currency.USD);
        chart.open("eur-bank", AccountType.ASSET, Currency.EUR);
        chart.open("eur-fees", AccountType.REVENUE, Currency.EUR);
        return chart;
    }

    public static void main(String[] args) {
        // postings are positive amounts on one side of one account
        Posting debitBank = new Posting("bank", Side.DEBIT, usd("100.00"));
        check("posting keeps fields", debitBank.accountId().equals("bank") && debitBank.side() == Side.DEBIT && debitBank.amount().equals(usd("100.00")));
        check("mirrored flips the side only", debitBank.mirrored().equals(new Posting("bank", Side.CREDIT, usd("100.00"))));
        checkThrows("zero posting rejected", IllegalArgumentException.class, () -> new Posting("bank", Side.DEBIT, usd("0.00")));
        checkThrows("negative posting rejected", IllegalArgumentException.class, () -> new Posting("bank", Side.DEBIT, usd("-1.00")));
        checkThrows("blank posting account rejected", IllegalArgumentException.class, () -> new Posting(" ", Side.DEBIT, usd("1.00")));

        // entries are immutable values that do not check balance themselves
        List<Posting> legs = new ArrayList<>(List.of(debitBank, new Posting("fees", Side.CREDIT, usd("100.00"))));
        JournalEntry sale = new JournalEntry("sale-1", "2026-10-01", legs);
        legs.clear();
        check("entry copies its postings", sale.postings().size() == 2);
        checkThrows("entry postings are read-only", UnsupportedOperationException.class, () -> sale.postings().add(debitBank));
        check("entry currency is the first posting's", sale.currency().equals(Currency.USD));
        checkThrows("blank entry id rejected", IllegalArgumentException.class, () -> new JournalEntry(" ", "2026-10-01", debitBank));
        checkThrows("blank entry date rejected", IllegalArgumentException.class, () -> new JournalEntry("x", "", debitBank));
        JournalEntry unbalanced = new JournalEntry("bad", "2026-10-01", debitBank, new Posting("fees", Side.CREDIT, usd("99.99")));
        check("constructing an unbalanced entry is allowed; posting it is not", unbalanced.postings().size() == 2);

        // the journal accepts balanced entries and numbers them 1, 2, 3, ...
        Ledger ledger = new Ledger(chart());
        check("new ledger is empty", ledger.size() == 0 && ledger.journal().isEmpty());
        PostedEntry first = ledger.post(sale);
        check("first entry gets sequence 1", first.sequence() == 1);
        check("posted entry carries the entry", first.entry().equals(sale) && first.id().equals("sale-1"));
        PostedEntry second = ledger.post(new JournalEntry("sale-2", "2026-10-02",
                new Posting("bank", Side.DEBIT, usd("100.00")),
                new Posting("sellers", Side.CREDIT, usd("96.80")),
                new Posting("fees", Side.CREDIT, usd("3.20"))));
        check("three-leg entry balances 100.00 = 96.80 + 3.20", second.sequence() == 2);
        check("size counts posted entries", ledger.size() == 2);
        check("journal is in sequence order", ledger.journal().get(0).id().equals("sale-1") && ledger.journal().get(1).id().equals("sale-2"));
        check("entry(id) finds a posted entry", ledger.entry("sale-2").sequence() == 2);
        check("hasEntry", ledger.hasEntry("sale-1") && !ledger.hasEntry("nope"));
        checkThrows("unknown entry id", UnknownEntryException.class, () -> ledger.entry("nope"));
        checkThrows("journal() is read-only", UnsupportedOperationException.class, () -> ledger.journal().clear());

        // posting the same id twice is a no-op that returns the original
        PostedEntry again = ledger.post(new JournalEntry("sale-1", "2026-12-31",
                new Posting("bank", Side.DEBIT, usd("1.00")), new Posting("fees", Side.CREDIT, usd("1.00"))));
        check("duplicate id returns the existing record", again.equals(first));
        check("duplicate id does not grow the journal", ledger.size() == 2);
        check("duplicate id does not replace the content", ledger.entry("sale-1").entry().date().equals("2026-10-01"));

        // rejections leave the journal untouched and consume no sequence number
        checkThrows("unbalanced entry rejected", UnbalancedEntryException.class, () -> ledger.post(unbalanced));
        check("rejected entry is not in the journal", !ledger.hasEntry("bad") && ledger.size() == 2);
        checkThrows("single posting rejected", InvalidEntryException.class,
                () -> ledger.post(new JournalEntry("one", "2026-10-01", debitBank)));
        checkThrows("no postings rejected", InvalidEntryException.class,
                () -> ledger.post(new JournalEntry("none", "2026-10-01", List.of())));
        checkThrows("unknown account rejected", UnknownAccountException.class,
                () -> ledger.post(new JournalEntry("ghost", "2026-10-01",
                        new Posting("ghost", Side.DEBIT, usd("1.00")), new Posting("fees", Side.CREDIT, usd("1.00")))));
        checkThrows("mixed currencies rejected", CurrencyMismatchException.class,
                () -> ledger.post(new JournalEntry("mixed", "2026-10-01",
                        new Posting("bank", Side.DEBIT, usd("1.00")), new Posting("eur-fees", Side.CREDIT, Money.parse("1.00", Currency.EUR)))));
        checkThrows("USD posting to EUR account rejected", CurrencyMismatchException.class,
                () -> ledger.post(new JournalEntry("wrong-acct", "2026-10-01",
                        new Posting("eur-bank", Side.DEBIT, usd("1.00")), new Posting("fees", Side.CREDIT, usd("1.00")))));
        check("rejections consume no sequence numbers", ledger.size() == 2);
        PostedEntry third = ledger.post(new JournalEntry("eur-sale", "2026-10-03",
                new Posting("eur-bank", Side.DEBIT, Money.parse("10.00", Currency.EUR)),
                new Posting("eur-fees", Side.CREDIT, Money.parse("10.00", Currency.EUR))));
        check("next accepted entry is sequence 3", third.sequence() == 3);
        check("a second currency lives in the same journal", ledger.size() == 3 && ledger.entry("eur-sale").entry().currency().equals(Currency.EUR));

        // every rejection is a LedgerException so callers can catch "the ledger said no" once
        check("Unbalanced is a LedgerException", LedgerException.class.isAssignableFrom(UnbalancedEntryException.class));
        check("InvalidEntry is a LedgerException", LedgerException.class.isAssignableFrom(InvalidEntryException.class));
        check("UnknownEntry is a LedgerException", LedgerException.class.isAssignableFrom(UnknownEntryException.class));
        checkThrows("ledger needs a registry", IllegalArgumentException.class, () -> new Ledger(null));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
