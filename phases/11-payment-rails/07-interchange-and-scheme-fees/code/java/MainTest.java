// Tests for phases/11-payment-rails/07-interchange-and-scheme-fees. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Money usd(String s) { return Money.parse(s, Currency.USD); }

    public static void main(String[] args) {
        FeeEngine engine = new FeeEngine(InterchangeTable.demo(), SchemeFees.demo());
        Pricing icpp = new Pricing.IcPlusPlus(Rate.of("0.20", 5));
        Pricing blended = new Pricing.Blended(Rate.of("2.90", 30));
        Region us = Region.US_DOMESTIC;
        Channel cnp = Channel.CARD_NOT_PRESENT, cp = Channel.CARD_PRESENT;

        // Rate parsing is exact
        check("1.80% is 18000 ppm", Rate.of("1.80", 10).ppm() == 18_000);
        check("0.05% is 500 ppm", Rate.of("0.05", 0).ppm() == 500);
        check("2% is 20000 ppm", Rate.of("2", 0).ppm() == 20_000);
        check("0.0013% is 13 ppm", Rate.of("0.0013", 0).ppm() == 13);
        checkThrows("five decimals of a percent rejected", IllegalArgumentException.class, () -> Rate.of("0.00001", 0));
        check("rate toString", Rate.of("1.80", 10).toString().equals("1.80% + 10"));

        // The worked example: 100.00 USD consumer credit CNP, IC++
        FeeBreakdown f = engine.quote(new Transaction("t1", usd("100.00"), CardType.CONSUMER_CREDIT, us, cnp), icpp);
        check("interchange 1.90", f.interchange().equals(usd("1.90")));
        check("scheme 0.16", f.schemeFees().equals(usd("0.16")));
        check("acquirer markup 0.25", f.acquirerMargin().equals(usd("0.25")));
        check("merchant net 97.69", f.merchantNet().equals(usd("97.69")));
        check("merchant discount 2.31", f.merchantDiscount().equals(usd("2.31")));
        check("effective rate 231 bps", f.effectiveRateBps() == 231);

        // Blended: merchant price fixed, acquirer margin absorbs the card mix
        FeeBreakdown b1 = engine.quote(new Transaction("t", usd("100.00"), CardType.CONSUMER_CREDIT, us, cnp), blended);
        FeeBreakdown b2 = engine.quote(new Transaction("t", usd("100.00"), CardType.COMMERCIAL_CREDIT, us, cnp), blended);
        check("blended merchant net is card-independent", b1.merchantNet().equals(b2.merchantNet()) && b1.merchantNet().equals(usd("96.80")));
        check("blended margin on consumer credit 1.14", b1.acquirerMargin().equals(usd("1.14")));
        check("blended margin on commercial 0.24", b2.acquirerMargin().equals(usd("0.24")));
        FeeBreakdown big = engine.quote(new Transaction("t", usd("10000.00"), CardType.COMMERCIAL_CREDIT, us, cnp), new Pricing.Blended(Rate.of("2.70", 0)));
        check("blended can lose money: margin negative on commercial at 2.70% flat", big.acquirerMargin().isNegative());

        // Regulation II: a fixed fee dominates small debit tickets
        FeeBreakdown d = engine.quote(new Transaction("t", usd("2.00"), CardType.CONSUMER_DEBIT_REGULATED, us, cp), icpp);
        check("regulated debit 2.00: 0.05% rounds to 0, + 0.22", d.interchange().equals(usd("0.22")));
        checkThrows("Reg II cap enforced at load", IllegalArgumentException.class,
                () -> new InterchangeTable().put(CardType.CONSUMER_DEBIT_REGULATED, us, cp, Rate.of("0.05", 25)));
        check("exempt debit is not capped", new InterchangeTable().put(CardType.CONSUMER_DEBIT_EXEMPT, us, cp, Rate.of("0.80", 15)).size() == 1);

        // IFR caps and missing rows
        checkThrows("IFR credit cap enforced", IllegalArgumentException.class,
                () -> new InterchangeTable().put(CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, cp, Rate.of("0.31", 0)));
        checkThrows("IFR forbids a fixed part on consumer cards", IllegalArgumentException.class,
                () -> new InterchangeTable().put(CardType.CONSUMER_DEBIT_REGULATED, Region.EEA_DOMESTIC, cp, Rate.of("0.10", 1)));
        Money eur = Money.parse("100.00", Currency.EUR);
        FeeBreakdown e = engine.quote(new Transaction("t", eur, CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, cp), icpp);
        check("EEA premium credit capped at 0.30", e.interchange().equals(Money.parse("0.30", Currency.EUR)));
        FeeBreakdown x = engine.quote(new Transaction("t", eur, CardType.CONSUMER_CREDIT, Region.INTER_REGIONAL, cnp), icpp);
        check("inter-regional scheme fee adds 0.60% cross-border", x.schemeFees().equals(Money.parse("0.76", Currency.EUR)));
        checkThrows("no EEA exempt-debit row", NoRateException.class,
                () -> engine.quote(new Transaction("t", eur, CardType.CONSUMER_DEBIT_EXEMPT, Region.EEA_DOMESTIC, cp), icpp));

        // Rounding: one HALF_EVEN per component
        FeeBreakdown odd = engine.quote(new Transaction("t", usd("37.77"), CardType.CONSUMER_CREDIT, us, cnp), icpp);
        check("37.77 interchange 0.78", odd.interchange().equals(usd("0.78")));
        check("37.77 scheme 0.05 + 0.02 = 0.07", odd.schemeFees().equals(usd("0.07")));

        // The invariant over many amounts and every card type
        boolean allSum = true;
        for (long cents = 1; cents <= 50_000; cents += 97) {
            for (CardType t : CardType.values()) {
                for (Pricing p : new Pricing[] {icpp, blended}) {
                    FeeBreakdown q = engine.quote(new Transaction("t", Money.ofMinor(cents, Currency.USD), t, us, cnp), p);
                    allSum &= Money.sum(Currency.USD, q.interchange(), q.schemeFees(), q.acquirerMargin(), q.merchantNet()).equals(q.gross());
                }
            }
        }
        check("split sums to gross for 515 amounts x 5 card types x 2 pricings", allSum);
        checkThrows("a non-summing breakdown cannot be constructed", IllegalStateException.class,
                () -> new FeeBreakdown(usd("1.00"), Rate.of("1", 0), usd("0.01"), usd("0.01"), usd("0.01"), usd("0.98")));
        checkThrows("zero amount is not a purchase", IllegalArgumentException.class,
                () -> new Transaction("t", usd("0.00"), CardType.CONSUMER_CREDIT, us, cp));

        // Attribution of an aggregate fee
        List<Transaction> month = List.of(
                new Transaction("a", usd("100.00"), CardType.CONSUMER_CREDIT, us, cp),
                new Transaction("b", usd("37.77"), CardType.PREMIUM_CREDIT, us, cnp),
                new Transaction("c", usd("2.00"), CardType.CONSUMER_DEBIT_REGULATED, us, cp));
        Money[] parts = FeeEngine.attribute(usd("25.00"), month);
        check("attribution sums exactly", Money.sum(Currency.USD, parts).equals(usd("25.00")));
        check("largest remainder: a gets 17.89 (floor 17.88 + leftover cent)", parts[0].equals(usd("17.89")));
        checkThrows("attribution rejects mixed currency", CurrencyMismatchException.class,
                () -> FeeEngine.attribute(Money.parse("25.00", Currency.EUR), month));
        List<FeeBreakdown> quotes = new ArrayList<>();
        for (Transaction tx : month) quotes.add(engine.quote(tx, icpp));
        FeeBreakdown total = FeeEngine.total(quotes);
        check("month total gross 139.77", total.gross().equals(usd("139.77")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(fl -> System.out.println("FAIL: " + fl));
        if (!failures.isEmpty()) System.exit(1);
    }
}
