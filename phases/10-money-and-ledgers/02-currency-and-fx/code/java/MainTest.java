// Tests for phases/10-money-and-ledgers/02-currency-and-fx. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        long now = 1_700_000_000L;
        CurrencyRegistry registry = CurrencyRegistry.iso4217();
        Currency USD = registry.currency("USD"), EUR = registry.currency("EUR"), GBP = registry.currency("GBP");
        Currency JPY = registry.currency("JPY"), KWD = registry.currency("KWD");

        // registry: exponents are data, unknown codes are rejected
        check("registry USD exponent 2", USD.exponent() == 2);
        check("registry JPY exponent 0", JPY.exponent() == 0);
        check("registry KWD exponent 3", KWD.exponent() == 3);
        check("registry CLF exponent 4", registry.currency("CLF").exponent() == 4);
        check("registry numeric lookup", registry.byNumeric(978).equals(EUR));
        check("registry info numeric code", registry.info("USD").numericCode() == 840);
        checkThrows("registry rejects unknown code", UnknownCurrencyException.class, () -> registry.currency("USX"));
        checkThrows("registry rejects duplicate code", IllegalArgumentException.class, () -> registry.register("USD", 999, 2, "dup"));
        checkThrows("registry rejects duplicate numeric", IllegalArgumentException.class, () -> registry.register("ZZZ", 840, 2, "dup"));
        check("registry parse uses registered exponent", registry.parse("1.250", "KWD").minorUnits() == 1250);
        checkThrows("registry parse rejects excess precision", ArithmeticException.class, () -> registry.parse("1.5", "JPY"));

        // rates: exact rationals, reduced, with provenance
        FxRate usdEur = FxRate.of(USD, EUR, "0.9217", "ecb-ref", now - 60, 50);
        check("rate parsed as 9217/10000", usdEur.numerator() == 9217 && usdEur.denominator() == 10000);
        FxRate half = FxRate.of(USD, EUR, "0.50", "x", now, 0);
        check("rate is reduced by gcd", half.numerator() == 1 && half.denominator() == 2);
        check("rate keeps source and timestamp", usdEur.source().equals("ecb-ref") && usdEur.observedAtEpochSeconds() == now - 60);
        FxRate inv = usdEur.invert();
        check("invert swaps numerator and denominator", inv.numerator() == 10000 && inv.denominator() == 9217 && inv.base().equals(EUR) && inv.quote().equals(USD));
        check("invert twice is identity", inv.invert().equals(usdEur));
        check("toDecimal 6 places", usdEur.toDecimal(6).equals("0.921700"));
        check("toDecimal rounds HALF_EVEN", FxRate.of(USD, EUR, "0.12345", "x", now, 0).toDecimal(4).equals("0.1234"));
        checkThrows("rate rejects same base and quote", IllegalArgumentException.class, () -> FxRate.of(USD, USD, "1", "x", now, 0));
        checkThrows("rate rejects zero", IllegalArgumentException.class, () -> FxRate.of(USD, EUR, "0", "x", now, 0));
        checkThrows("rate rejects garbage", NumberFormatException.class, () -> FxRate.of(USD, EUR, "0,92", "x", now, 0));

        // spread: bid below mid below ask, symmetric
        FxRate bid = usdEur.bid(), ask = usdEur.ask();
        check("bid < mid", bid.numerator() * usdEur.denominator() < usdEur.numerator() * bid.denominator());
        check("ask > mid", ask.numerator() * usdEur.denominator() > usdEur.numerator() * ask.denominator());
        check("bid at 50bps of 0.9217 is 0.919396 (6 places)", bid.toDecimal(6).equals("0.919396"));
        check("ask at 50bps of 0.9217 is 0.924004 (6 places)", ask.toDecimal(6).equals("0.924004"));
        check("spread-applied rate has zero residual spread", bid.spreadBps() == 0);

        // conversion: one exact rational, one rounding, remainder reported
        Conversion c = FxConverter.convert(Money.parse("100.00", USD), usdEur, Rounding.HALF_EVEN);
        check("100.00 USD -> 92.17 EUR", c.to().equals(Money.parse("92.17", EUR)));
        check("exact conversion has zero remainder", !c.rounded() && c.remainderNumerator() == 0);
        Conversion c2 = FxConverter.convert(Money.parse("19.99", USD), usdEur, Rounding.HALF_EVEN);
        check("19.99 USD -> 18.42 EUR", c2.to().equals(Money.parse("18.42", EUR)));
        check("remainder is 4783/10000 of a cent", c2.rounded() && c2.remainderNumerator() == 4783 && c2.remainderDenominator() == 10000);
        Conversion c3 = FxConverter.convert(Money.parse("19.99", USD), FxRate.of(USD, JPY, "149.37", "x", now, 0), Rounding.HALF_EVEN);
        check("USD -> JPY lands on a whole yen", c3.to().equals(Money.parse("2986", JPY)));
        Conversion c4 = FxConverter.convert(Money.parse("19.99", USD), FxRate.of(USD, KWD, "0.30825", "x", now, 0), Rounding.HALF_EVEN);
        check("USD -> KWD uses three decimals", c4.to().equals(Money.parse("6.162", KWD)));
        Conversion c5 = FxConverter.convert(Money.parse("0.01", USD), usdEur, Rounding.DOWN);
        check("sub-unit conversion with DOWN is zero", c5.to().isZero() && c5.rounded());
        Conversion c6 = FxConverter.convert(Money.parse("0.01", USD), usdEur, Rounding.UP);
        check("sub-unit conversion with UP is one minor unit", c6.to().minorUnits() == 1);
        Conversion neg = FxConverter.convert(Money.parse("-19.99", USD), usdEur, Rounding.HALF_EVEN);
        check("negative amounts convert symmetrically", neg.to().equals(Money.parse("-18.42", EUR)));
        checkThrows("convert rejects amount in wrong currency", CurrencyMismatchException.class,
                () -> FxConverter.convert(Money.parse("1.00", EUR), usdEur, Rounding.HALF_EVEN));

        // round trip residue
        RoundTrip rt = FxConverter.roundTrip(Money.parse("19.99", USD), usdEur, Rounding.HALF_EVEN);
        check("round trip comes back 19.98", rt.back().to().equals(Money.parse("19.98", USD)));
        check("round trip residue is 0.01 USD", rt.residue().equals(Money.parse("0.01", USD)));
        check("residue is in the original currency", rt.residue().currency().equals(USD));
        RoundTrip manufactured = FxConverter.roundTrip(Money.parse("0.06", USD), usdEur, Rounding.HALF_EVEN);
        check("round trip can manufacture a cent: residue -0.01", manufactured.residue().equals(Money.parse("-0.01", USD)));
        RoundTrip exact = FxConverter.roundTrip(Money.parse("100.00", USD), half, Rounding.HALF_EVEN);
        check("exact round trip has zero residue", exact.residue().isZero());

        // cross rates through a base
        FxRate usdGbp = FxRate.of(USD, GBP, "0.7893", "ecb-ref", now - 60, 50);
        FxRate eurGbp = usdEur.cross(usdGbp);
        check("cross rate EUR/GBP = 7893/9217", eurGbp.base().equals(EUR) && eurGbp.quote().equals(GBP)
                && eurGbp.numerator() == 7893 && eurGbp.denominator() == 9217);
        check("cross rate keeps older timestamp", eurGbp.observedAtEpochSeconds() == now - 60);
        Conversion c7 = FxConverter.convert(Money.parse("50.00", EUR), eurGbp, Rounding.HALF_EVEN);
        check("50.00 EUR -> 42.82 GBP via USD", c7.to().equals(Money.parse("42.82", GBP)));
        checkThrows("cross rate needs shared base", IllegalArgumentException.class, () -> usdEur.cross(eurGbp));

        // rate table: direct, inverted, crossed, stale
        RateTable table = new RateTable(USD, 300);
        table.put(usdEur); table.put(usdGbp);
        check("table direct", table.rate(USD, EUR, now).equals(usdEur));
        check("table inverted", table.rate(EUR, USD, now).equals(usdEur.invert()));
        check("table crossed", table.rate(EUR, GBP, now).numerator() == 7893);
        checkThrows("table rejects stale rate", StaleRateException.class, () -> table.rate(USD, EUR, now + 3600));
        checkThrows("table rejects foreign base", IllegalArgumentException.class, () -> table.put(eurGbp));
        checkThrows("table rejects missing pair", IllegalArgumentException.class, () -> table.rate(USD, JPY, now));
        checkThrows("table rejects same currency", IllegalArgumentException.class, () -> table.rate(USD, USD, now));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
