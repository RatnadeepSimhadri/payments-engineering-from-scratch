// Tests for phases/10-money-and-ledgers/09-multi-currency-ledgers. Run: javac -d .out *.java && java -cp .out MainTest
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

    static final Currency USD = Currency.USD, EUR = Currency.EUR, GBP = Currency.GBP, JPY = Currency.JPY;

    static FxRate eurUsd(long num) { return new FxRate(EUR, USD, num, 10_000, "test", 0); }

    static MultiCurrencyLedger ledger() {
        MultiCurrencyLedger l = new MultiCurrencyLedger(USD);
        l.openAccount("1200-USD", "Receivable", AccountType.ASSET, USD);
        l.openAccount("1010-USD", "Bank", AccountType.ASSET, USD);
        l.openAccount("1010-EUR", "Bank", AccountType.ASSET, EUR);
        l.openAccount("2100-EUR", "Merchant Payable", AccountType.LIABILITY, EUR);
        l.openAccount("1010-GBP", "Bank", AccountType.ASSET, GBP);
        return l;
    }

    public static void main(String[] args) {
        // FxRate conversion
        check("EUR->USD at 1.1000", eurUsd(11_000).convert(Money.parse("100.00", EUR), Rounding.HALF_EVEN).equals(Money.parse("110.00", USD)));
        check("USD->EUR at 1.1000", eurUsd(11_000).convert(Money.parse("110.00", USD), Rounding.HALF_EVEN).equals(Money.parse("100.00", EUR)));
        check("USD->EUR rounds half even (1.00 / 1.1 = 0.909 -> 0.91)", eurUsd(11_000).convert(Money.parse("1.00", USD), Rounding.HALF_EVEN).minorUnits() == 91);
        FxRate jpy = new FxRate(USD, JPY, 15_725, 100, "t", 0);
        check("USD->JPY scales exponent, tie to even (10.00 USD = 1572.5 -> 1572 JPY)", jpy.convert(Money.parse("10.00", USD), Rounding.HALF_EVEN).equals(Money.parse("1572", JPY)));
        check("JPY->USD scales exponent (5000 JPY -> 31.80 USD)", jpy.convert(Money.parse("5000", JPY), Rounding.HALF_EVEN).equals(Money.parse("31.80", USD)));
        checkThrows("rate rejects unrelated currency", CurrencyMismatchException.class, () -> eurUsd(11_000).convert(Money.parse("1.00", GBP), Rounding.HALF_EVEN));
        checkThrows("rate rejects same currency", IllegalArgumentException.class, () -> new FxRate(USD, USD, 1, 1, "t", 0));
        checkThrows("rate rejects non-positive", IllegalArgumentException.class, () -> new FxRate(EUR, USD, 0, 1, "t", 0));

        // no mixed-currency entries
        checkThrows("mixed-currency entry rejected", MixedCurrencyException.class, () -> new JournalEntry("x", USD, List.of(
                Posting.debit("1200-USD", Money.parse("1.00", USD)), Posting.credit("2100-EUR", Money.parse("1.00", EUR)))));
        MultiCurrencyLedger l = ledger();
        checkThrows("entry posted to an account of another currency rejected", LedgerException.class, () -> l.post(new Transaction("t", "d",
                List.of(JournalEntry.of("t/EUR", Posting.debit("1010-EUR", Money.parse("1.00", EUR)), Posting.credit("1200-USD", Money.parse("1.00", EUR)))), null)));
        check("rejected transaction left no trace", l.subLedger(EUR).size() == 0 && l.subLedger(USD).size() == 0);

        // exchange through clearing
        Transaction tx = l.exchange("fx-1", "pay", "1200-USD", Money.parse("110.00", USD), "2100-EUR", eurUsd(11_000));
        check("exchange produces one entry per currency", tx.entries().size() == 2 && tx.entries().get(0).currency().equals(USD) && tx.entries().get(1).currency().equals(EUR));
        check("clearing balances mirror the legs", l.netDebit("1900-USD").minorUnits() == -11_000 && l.netDebit("1900-EUR").minorUnits() == 10_000);
        check("each sub-ledger balances", l.allSubLedgersBalance());
        check("EUR position is -100.00", l.position(EUR).minorUnits() == -10_000);
        check("EUR basis is -110.00 USD", l.positionReport(EUR).basis().minorUnits() == -11_000);
        check("clearing matches basis after exchange", l.clearingMatchesBasis());
        check("exchange replay is idempotent", l.exchange("fx-1", "pay", "1200-USD", Money.parse("110.00", USD), "2100-EUR", eurUsd(11_000)).equals(tx)
                && l.subLedger(USD).size() == 1 && l.positionReport(EUR).basis().minorUnits() == -11_000);
        checkThrows("exchange id reused with different amount", LedgerException.class,
                () -> l.exchange("fx-1", "pay", "1200-USD", Money.parse("120.00", USD), "2100-EUR", eurUsd(11_000)));
        checkThrows("cross pair must route through functional", LedgerException.class,
                () -> l.exchange("fx-x", "eur->gbp", "1010-EUR", Money.parse("10.00", EUR), "1010-GBP", new FxRate(EUR, GBP, 85, 100, "t", 0)));
        checkThrows("same-currency exchange rejected", LedgerException.class,
                () -> l.exchange("fx-y", "usd->usd", "1200-USD", Money.parse("1.00", USD), "1010-USD", eurUsd(11_000)));

        // revaluation
        check("revalue at the booked rate books nothing", l.revalue(EUR, eurUsd(11_000)).isZero());
        Money loss = l.revalue(EUR, eurUsd(11_200));
        check("EUR up to 1.12 on a short EUR position is a 2.00 loss", loss.minorUnits() == -200);
        check("loss lands in 4900 (revenue, negative)", l.balance(MultiCurrencyLedger.UNREALIZED).minorUnits() == -200);
        check("adjustment account carries the revaluation", l.netDebit("1950-EUR").minorUnits() == -200);
        check("revaluation posts only in functional currency", l.subLedger(EUR).size() == 1);
        check("revalue twice at same rate is a no-op", l.revalue(EUR, eurUsd(11_200)).isZero() && l.subLedger(USD).size() == 2);
        Money back = l.revalue(EUR, eurUsd(10_900));
        check("EUR falls to 1.09: gain of 3.00 relative to carrying 112.00", back.minorUnits() == 300);
        check("cumulative unrealized = fair - basis", l.positionReport(EUR).carryingValue().minorUnits() == -10_900);
        checkThrows("revalue functional currency rejected", LedgerException.class, () -> l.revalue(USD, eurUsd(11_000)));
        checkThrows("realize an open position rejected", LedgerException.class, () -> l.realize(EUR));

        // realization
        l.post(new Transaction("stl", "settle", List.of(JournalEntry.of("stl/USD",
                Posting.debit("1010-USD", Money.parse("110.00", USD)), Posting.credit("1200-USD", Money.parse("110.00", USD)))), null));
        l.exchange("fx-2", "buy EUR", "1010-EUR", Money.parse("100.00", EUR), "1010-USD", eurUsd(11_150));
        l.post(new Transaction("out", "payout", List.of(JournalEntry.of("out/EUR",
                Posting.debit("2100-EUR", Money.parse("100.00", EUR)), Posting.credit("1010-EUR", Money.parse("100.00", EUR)))), null));
        check("position flat after buying and paying out", l.position(EUR).isZero());
        check("functional clearing holds the round-trip cost", l.netDebit("1900-USD").minorUnits() == 150 && l.clearingMatchesBasis());
        Money realized = l.realize(EUR);
        check("realized loss is 1.50", realized.minorUnits() == -150);
        check("4910 shows the realized loss", l.balance(MultiCurrencyLedger.REALIZED).minorUnits() == -150);
        check("unrealized fully unwound", l.balance(MultiCurrencyLedger.UNREALIZED).isZero() && l.netDebit("1950-EUR").isZero());
        check("functional clearing empty after realize", l.netDebit("1900-USD").isZero());
        check("total FX P&L equals the bank's loss", l.balance("1010-USD").minorUnits() == -150);
        check("all sub-ledgers balance at the end", l.allSubLedgersBalance());

        // long position: holding EUR gains when EUR strengthens
        MultiCurrencyLedger longL = ledger();
        longL.exchange("pre", "prefund", "1010-EUR", Money.parse("500.00", EUR), "1010-USD", eurUsd(10_900));
        check("long position basis +545.00", longL.positionReport(EUR).basis().minorUnits() == 54_500 && longL.position(EUR).minorUnits() == 50_000);
        check("long EUR position gains 15.00 at 1.12", longL.revalue(EUR, eurUsd(11_200)).minorUnits() == 1_500
                && longL.balance(MultiCurrencyLedger.UNREALIZED).minorUnits() == 1_500);

        // property: random exchanges and revaluations keep every sub-ledger balanced and clearing == basis
        MultiCurrencyLedger r = ledger();
        Random rnd = new Random(17);
        boolean ok = true;
        for (int i = 0; i < 500; i++) {
            long rate = 10_000 + rnd.nextInt(3_000);
            Money amt = Money.ofMinor(1 + rnd.nextInt(1_000_000), rnd.nextBoolean() ? USD : EUR);
            if (amt.currency().equals(USD)) r.exchange("e" + i, "x", "1200-USD", amt, "2100-EUR", eurUsd(rate));
            else r.exchange("e" + i, "x", "1010-EUR", amt, "1010-USD", eurUsd(rate));
            if (i % 50 == 0) r.revalue(EUR, eurUsd(10_000 + rnd.nextInt(3_000)));
            ok &= r.allSubLedgersBalance() && r.clearingMatchesBasis();
        }
        check("property: 500 random exchanges keep sub-ledgers balanced and clearing == basis", ok);
        Position pr = r.positionReport(EUR);
        Money fair = eurUsd(11_000).convert(pr.position(), Rounding.HALF_EVEN);
        r.revalue(EUR, eurUsd(11_000));
        check("property: after revalue, carrying value == fair value", r.positionReport(EUR).carryingValue().equals(fair));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
