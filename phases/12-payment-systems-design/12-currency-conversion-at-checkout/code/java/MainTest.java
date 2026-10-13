// Tests for phases/12-payment-systems-design/12-currency-conversion-at-checkout. Run: javac -d .out *.java && java -cp .out MainTest
import java.math.RoundingMode;
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
    static Money eur(String s) { return Money.parse(s, Currency.EUR); }
    static Money gbp(String s) { return Money.parse(s, Currency.GBP); }

    record Rig(FakeClock clock, FakeRateSource market, FxDesk desk) {
        static Rig create() {
            FakeClock clock = new FakeClock(Main.T0);
            FakeRateSource market = new FakeRateSource();
            market.set(Currency.EUR, Currency.USD, "1.0850");
            return new Rig(clock, market, new FxDesk(clock, market, 200, Main.FIFTEEN_MINUTES));
        }
    }

    public static void main(String[] args) {
        // Rationals are exact and normalized.
        check("parse 1.0850 = 217/200", Rational.parse("1.0850").equals(Rational.of(217, 200)));
        check("sign lives in the numerator", Rational.of(2, -4).equals(Rational.of(-1, 2)));
        check("inverse is exact", Rational.parse("1.0850").inverse().equals(Rational.of(200, 217)));
        check("compareTo is exact", Rational.of(1, 3).compareTo(Rational.parse("0.3333")) > 0);
        check("bps", Rational.bps(350).equals(Rational.of(7, 200)));
        checkThrows("zero denominator rejected", ArithmeticException.class, () -> Rational.of(1, 0));

        // Conversion: one rounding, on the amount, with an explicit mode.
        FxRate usdEur = new FxRate(Currency.USD, Currency.EUR, Rational.parse("1.0850").inverse()).adjustedBy(200);
        check("customer rate is mid * 1.02 = 204/217", usdEur.rate().equals(Rational.of(204, 217)));
        check("120.00 USD -> 112.82 EUR rounding up", FxConverter.convert(usd("120.00"), usdEur, RoundingMode.CEILING).equals(eur("112.82")));
        check("120.00 USD -> 112.81 EUR half-even", FxConverter.convert(usd("120.00"), usdEur, RoundingMode.HALF_EVEN).equals(eur("112.81")));
        FxRate usdJpy = new FxRate(Currency.USD, Currency.JPY, Rational.parse("152.37"));
        check("USD -> JPY shifts exponents 2 -> 0", FxConverter.convert(usd("120.00"), usdJpy, RoundingMode.HALF_EVEN).equals(Money.ofMinor(18284, Currency.JPY)));
        check("JPY -> USD round trip", FxConverter.convert(Money.ofMinor(18284, Currency.JPY), usdJpy.inverse(), RoundingMode.HALF_EVEN).equals(usd("120.00")));
        Money exact = FxConverter.convert(usd("50000.00"), usdEur, RoundingMode.HALF_EVEN);
        Money roundedRate = FxConverter.convertWithRoundedRate(usd("50000.00"), usdEur, 4, RoundingMode.HALF_EVEN);
        check("exact conversion 47004.61", exact.equals(eur("47004.61")));
        check("rounding the rate first costs 0.39 EUR", roundedRate.minus(exact).equals(eur("0.39")));
        checkThrows("convert rejects the wrong source currency", CurrencyMismatchException.class,
                () -> FxConverter.convert(eur("1.00"), usdEur, RoundingMode.HALF_EVEN));
        checkThrows("non-positive rate rejected", IllegalArgumentException.class,
                () -> new FxRate(Currency.USD, Currency.EUR, Rational.of(0, 1)));
        checkThrows("same-currency rate must be 1", IllegalArgumentException.class,
                () -> new FxRate(Currency.USD, Currency.USD, Rational.parse("1.01")));
        check("negative bps scales the rate down", new FxRate(Currency.EUR, Currency.USD, Rational.ONE).adjustedBy(-200).rate().equals(Rational.of(49, 50)));

        // The lock window is half-open: [issuedAt, expiresAt).
        {
            Rig r = Rig.create();
            RateQuote q = r.desk().quote(Currency.USD, Currency.EUR);
            check("quote locks mid + spread", q.customerRate().rate().equals(Rational.of(204, 217)) && q.spreadBps() == 200);
            r.clock().advanceMillis(Main.FIFTEEN_MINUTES - 1);
            LockedCharge c = r.desk().confirm(q.id(), usd("120.00"));
            check("confirm 1 ms before expiry succeeds", c.presentmentAmount().equals(eur("112.82")));
            r.market().set(Currency.EUR, Currency.USD, "1.0000");
            check("a market move inside the window does not change a locked quote",
                    r.desk().confirm(q.id(), usd("120.00")).presentmentAmount().equals(eur("112.82")));
            r.clock().advanceMillis(1);
            checkThrows("confirm at exactly expiresAt is expired", QuoteExpiredException.class, () -> r.desk().confirm(q.id(), usd("120.00")));
            checkThrows("unknown quote rejected", IllegalArgumentException.class, () -> r.desk().confirm("fxq_9999", usd("120.00")));
            checkThrows("quote is bound to its settlement currency", CurrencyMismatchException.class,
                    () -> r.desk().confirm(r.desk().quote(Currency.USD, Currency.EUR).id(), gbp("120.00")));
        }

        // Settlement: the merchant always gets the promised amount; the PSP owns the movement.
        {
            Rig r = Rig.create();
            LockedCharge c = r.desk().confirm(r.desk().quote(Currency.USD, Currency.EUR).id(), usd("120.00"));
            SettlementResult flat = FxDesk.settle(c, Rational.parse("1.0850"));
            SettlementResult down = FxDesk.settle(c, Rational.parse("1.0600"));
            check("unchanged market: PSP earns 2.41", flat.pspFxPnl().equals(usd("2.41")));
            check("EUR weakens to 1.06: PSP loses 0.41", down.pspFxPnl().equals(usd("-0.41")));
            check("merchant receives 120.00 either way", flat.merchantReceives().equals(usd("120.00")) && down.merchantReceives().equals(usd("120.00")));
            check("proceeds = merchant + P&L", down.pspProceeds().equals(down.merchantReceives().plus(down.pspFxPnl())));
            Rational be = FxDesk.breakEvenRate(c);
            check("break-even rate is 12000/11282", be.equals(Rational.of(12000, 11282)));
            check("settling at break-even gives zero P&L", FxDesk.settle(c, be).pspFxPnl().minorUnits() == 0);
            r.market().set(Currency.EUR, Currency.USD, "1.0600");
            check("re-quote after the move shows a new price", FxDesk.presentmentPrice(usd("120.00"), r.desk().quote(Currency.USD, Currency.EUR)).equals(eur("115.48")));
        }
        {
            FakeClock clock = new FakeClock(0);
            FakeRateSource market = new FakeRateSource();
            market.set(Currency.EUR, Currency.USD, "1.2500");
            FxDesk zero = new FxDesk(clock, market, 0, 1000);
            check("zero spread presents at mid", FxDesk.presentmentPrice(usd("100.00"), zero.quote(Currency.USD, Currency.EUR)).equals(eur("80.00")));
            checkThrows("lock window must be positive", IllegalArgumentException.class, () -> new FxDesk(clock, market, 0, 0));
        }

        // DCC disclosure math.
        {
            DccOffer o = Dcc.offer(eur("100.00"), Currency.GBP, Rational.parse("0.8550"), 350);
            check("DCC rate is 0.8550 * 1.035 = 0.884925", o.dccRate().rate().equals(Rational.parse("0.884925")));
            check("cardholder pays 88.49 GBP", o.cardAmount().equals(gbp("88.49")));
            check("disclosed markup is exactly 3.5%", o.markupPercent().equals(Rational.of(7, 2)));
            check("effective markup after rounding is below the disclosed", o.effectiveMarkupPercent().compareTo(o.markupPercent()) < 0);
            check("gross margin 2.99 GBP", o.grossMargin().equals(gbp("2.99")));
            Money[] split = o.grossMargin().allocate(3000, 7000);
            check("margin split 0.90 / 2.09 sums exactly", split[0].equals(gbp("0.90")) && split[1].equals(gbp("2.09")));
            String d = o.disclosure();
            check("disclosure names both amounts, the rate and the markup",
                    d.contains("88.49 GBP") && d.contains("100.00 EUR") && d.contains("0.884925") && d.contains("3.50%") && d.contains("0.8550"));
            check("declining DCC with no issuer fee costs 85.60", Dcc.issuerConversion(eur("100.00"), Currency.GBP, Rational.parse("0.8560"), 0).equals(gbp("85.60")));
            check("declining DCC with a 2.99% issuer fee costs 88.16", Dcc.issuerConversion(eur("100.00"), Currency.GBP, Rational.parse("0.8560"), 299).equals(gbp("88.16")));
            check("zero markup DCC equals the reference conversion", Dcc.offer(eur("100.00"), Currency.GBP, Rational.parse("0.8550"), 0).cardAmount().equals(gbp("85.50")));
            checkThrows("DCC requires a different card currency", IllegalArgumentException.class,
                    () -> Dcc.offer(eur("100.00"), Currency.EUR, Rational.ONE, 350));
        }

        checkThrows("Money rejects currency mix", CurrencyMismatchException.class, () -> usd("1.00").plus(eur("1.00")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
