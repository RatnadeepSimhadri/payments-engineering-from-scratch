// Lesson: phases/10-money-and-ledgers/02-currency-and-fx/docs/en.md
// Currencies, Exchange Rates, and FX Conversion
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 4217:2015 (alphabetic code, numeric code, minor-unit exponent); ECB reference-rate conventions
// (base currency quoting). Money type copied from Phase 10, Lesson 01. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
    static final Currency JPY = new Currency("JPY", 0);
    static final Currency KWD = new Currency("KWD", 3);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    Money abs() { return minorUnits < 0 ? negate() : this; }
    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isPositive() { return minorUnits > 0; }

    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twiceRemainder = Math.abs(remainder) * 2;
        long absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twiceRemainder >= absDivisor ? quotient + sign : quotient;
            case HALF_EVEN -> {
                if (twiceRemainder > absDivisor) yield quotient + sign;
                if (twiceRemainder < absDivisor) yield quotient;
                yield (quotient % 2 == 0) ? quotient : quotient + sign;
            }
        };
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = result + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// ISO 4217 registry.
// ---------------------------------------------------------------------------------------------

final class UnknownCurrencyException extends IllegalArgumentException {
    UnknownCurrencyException(String code) { super("unknown currency code: " + code); }
}

/** One row of ISO 4217: alphabetic code, numeric code, minor-unit exponent, and a display name. */
record CurrencyInfo(String code, int numericCode, int exponent, String name) {
    Currency currency() { return new Currency(code, exponent); }
}

/**
 * The list of currencies this system is allowed to handle. The exponent lives here, in data, so a
 * currency that redenominates (ISO 4217 amendments do this) is a data change, not a code change.
 */
final class CurrencyRegistry {
    private final Map<String, CurrencyInfo> byCode = new LinkedHashMap<>();
    private final Map<Integer, CurrencyInfo> byNumeric = new LinkedHashMap<>();

    /** A registry pre-loaded with a representative slice of ISO 4217, including the 0- and 3-decimal cases. */
    static CurrencyRegistry iso4217() {
        CurrencyRegistry r = new CurrencyRegistry();
        r.register("USD", 840, 2, "US Dollar");
        r.register("EUR", 978, 2, "Euro");
        r.register("GBP", 826, 2, "Pound Sterling");
        r.register("INR", 356, 2, "Indian Rupee");
        r.register("CHF", 756, 2, "Swiss Franc");
        r.register("CAD", 124, 2, "Canadian Dollar");
        r.register("AUD", 36, 2, "Australian Dollar");
        r.register("BRL", 986, 2, "Brazilian Real");
        r.register("JPY", 392, 0, "Yen");
        r.register("KRW", 410, 0, "Won");
        r.register("VND", 704, 0, "Dong");
        r.register("KWD", 414, 3, "Kuwaiti Dinar");
        r.register("BHD", 48, 3, "Bahraini Dinar");
        r.register("JOD", 400, 3, "Jordanian Dinar");
        r.register("CLF", 990, 4, "Unidad de Fomento");
        return r;
    }

    CurrencyInfo register(String code, int numericCode, int exponent, String name) {
        CurrencyInfo info = new CurrencyInfo(code, numericCode, exponent, name);
        info.currency(); // validates code and exponent
        if (byCode.putIfAbsent(code, info) != null) throw new IllegalArgumentException("already registered: " + code);
        if (byNumeric.putIfAbsent(numericCode, info) != null) {
            byCode.remove(code);
            throw new IllegalArgumentException("numeric code already registered: " + numericCode);
        }
        return info;
    }

    Currency currency(String code) {
        CurrencyInfo info = byCode.get(code);
        if (info == null) throw new UnknownCurrencyException(code);
        return info.currency();
    }

    CurrencyInfo info(String code) {
        CurrencyInfo info = byCode.get(code);
        if (info == null) throw new UnknownCurrencyException(code);
        return info;
    }

    Currency byNumeric(int numericCode) {
        CurrencyInfo info = byNumeric.get(numericCode);
        if (info == null) throw new UnknownCurrencyException(Integer.toString(numericCode));
        return info.currency();
    }

    boolean contains(String code) { return byCode.containsKey(code); }

    /** Parse a decimal string in a currency known to the registry. Unknown code: rejected before the amount is read. */
    Money parse(String amount, String code) { return Money.parse(amount, currency(code)); }

    int size() { return byCode.size(); }
}

// ---------------------------------------------------------------------------------------------
// Exchange rates as exact rationals.
// ---------------------------------------------------------------------------------------------

final class StaleRateException extends IllegalStateException {
    StaleRateException(String detail) { super(detail); }
}

/**
 * One unit of {@code base} buys {@code numerator/denominator} units of {@code quote}: an exact rational,
 * so "0.9217" is 9217/10000 and never a double. Carries provenance (source, observation time) and the
 * spread in basis points the provider charges around this mid rate.
 */
record FxRate(Currency base, Currency quote, long numerator, long denominator,
              String source, long observedAtEpochSeconds, int spreadBps) {

    FxRate {
        Objects.requireNonNull(base, "base");
        Objects.requireNonNull(quote, "quote");
        Objects.requireNonNull(source, "source");
        if (base.equals(quote)) throw new IllegalArgumentException("base and quote must differ: " + base.code());
        if (numerator <= 0 || denominator <= 0) throw new IllegalArgumentException("rate must be positive");
        if (spreadBps < 0 || spreadBps >= 10_000) throw new IllegalArgumentException("spread must be 0..9999 bps");
        long g = gcd(numerator, denominator);
        numerator /= g;
        denominator /= g;
    }

    /** Build a mid rate from a decimal string such as "0.9217" or "149.37" without going through a double. */
    static FxRate of(Currency base, Currency quote, String decimalRate, String source, long observedAtEpochSeconds, int spreadBps) {
        String s = decimalRate.strip();
        int dot = s.indexOf('.');
        String whole = dot < 0 ? s : s.substring(0, dot);
        String frac = dot < 0 ? "" : s.substring(dot + 1);
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit) || (whole + frac).isEmpty()) {
            throw new NumberFormatException("not a decimal rate: " + decimalRate);
        }
        long den = 1;
        for (int i = 0; i < frac.length(); i++) den = Math.multiplyExact(den, 10);
        long num = Long.parseLong(whole + frac);
        return new FxRate(base, quote, num, den, source, observedAtEpochSeconds, spreadBps);
    }

    static long gcd(long a, long b) {
        a = Math.abs(a); b = Math.abs(b);
        while (b != 0) { long t = a % b; a = b; b = t; }
        return a == 0 ? 1 : a;
    }

    /** The same rate quoted the other way round: quote per base becomes base per quote. Exact, no rounding. */
    FxRate invert() {
        return new FxRate(quote, base, denominator, numerator, source, observedAtEpochSeconds, spreadBps);
    }

    /** The rate the customer gets when they sell base and buy quote: mid less half the spread. */
    FxRate bid() { return withSpreadApplied(-spreadBps); }

    /** The rate the customer pays when they buy base with quote: mid plus half the spread. */
    FxRate ask() { return withSpreadApplied(+spreadBps); }

    private FxRate withSpreadApplied(int signedBps) {
        // mid * (1 +- spread/2) = num/den * (20000 +- bps)/20000, all integer
        long num = Math.multiplyExact(numerator, 20_000L + signedBps);
        long den = Math.multiplyExact(denominator, 20_000L);
        return new FxRate(base, quote, num, den, source, observedAtEpochSeconds, 0);
    }

    /**
     * Cross rate through a shared base. If this is USD/EUR (1 USD = x EUR) and other is USD/GBP (1 USD = y GBP),
     * the result is EUR/GBP: 1 EUR = y/x GBP. Exact rational division; no intermediate rounding.
     */
    FxRate cross(FxRate other) {
        if (!base.equals(other.base)) {
            throw new IllegalArgumentException("cross rate needs a shared base: " + base.code() + " vs " + other.base.code());
        }
        if (quote.equals(other.quote)) throw new IllegalArgumentException("cross rate between identical quotes");
        long num = Math.multiplyExact(other.numerator, denominator);
        long den = Math.multiplyExact(other.denominator, numerator);
        return new FxRate(quote, other.quote, num, den, source + "+" + other.source,
                Math.min(observedAtEpochSeconds, other.observedAtEpochSeconds), Math.max(spreadBps, other.spreadBps));
    }

    /** Reject a rate older than maxAgeSeconds at the given clock reading. Rates have a shelf life. */
    FxRate requireFresh(long nowEpochSeconds, long maxAgeSeconds) {
        long age = nowEpochSeconds - observedAtEpochSeconds;
        if (age > maxAgeSeconds) {
            throw new StaleRateException(pair() + " rate from " + source + " is " + age + "s old; max " + maxAgeSeconds + "s");
        }
        return this;
    }

    String pair() { return base.code() + "/" + quote.code(); }

    /** Decimal rendering with the requested number of places, HALF_EVEN. For display and logs only. */
    String toDecimal(int places) {
        long scale = 1;
        for (int i = 0; i < places; i++) scale *= 10;
        long scaled = Money.divideRounded(Math.multiplyExact(numerator, scale), denominator, Rounding.HALF_EVEN);
        String whole = Long.toString(scaled / scale);
        if (places == 0) return whole;
        String frac = Long.toString(scaled % scale);
        StringBuilder pad = new StringBuilder();
        for (int i = frac.length(); i < places; i++) pad.append('0');
        return whole + "." + pad + frac;
    }

    @Override public String toString() {
        return pair() + " " + toDecimal(6) + " (" + numerator + "/" + denominator + ") from " + source;
    }
}

// ---------------------------------------------------------------------------------------------
// Conversion: the one place a fraction is created, and the record of what was done with it.
// ---------------------------------------------------------------------------------------------

/**
 * The result of one conversion: the amount that went in, the amount that came out, the rate and rounding used,
 * and the exact fraction of a quote minor unit that the rounding discarded (remainderNumerator/remainderDenominator,
 * in the open interval (-1, 1)). The fraction is what lets an audit recompute the conversion and account for every
 * sub-unit of value.
 */
record Conversion(Money from, Money to, FxRate rate, Rounding rounding, long remainderNumerator, long remainderDenominator) {
    /** True when the rounding moved the result away from the exact value in the quote currency. */
    boolean rounded() { return remainderNumerator != 0; }
}

final class FxConverter {

    /**
     * amount (base minor units) * rate * 10^quoteExp / 10^baseExp, as one exact rational, rounded once.
     * The remainder of that single division is reported, not discarded.
     */
    static Conversion convert(Money amount, FxRate rate, Rounding rounding) {
        if (!amount.currency().equals(rate.base())) {
            throw new CurrencyMismatchException(amount.currency(), rate.base());
        }
        long num = Math.multiplyExact(amount.minorUnits(), rate.numerator());
        num = Math.multiplyExact(num, rate.quote().minorUnitsPerMajor());
        long den = Math.multiplyExact(rate.denominator(), rate.base().minorUnitsPerMajor());
        long rounded = Money.divideRounded(num, den, rounding);
        // exact - rounded, as a fraction of one quote minor unit: (num - rounded*den) / den
        long remainderNum = Math.subtractExact(num, Math.multiplyExact(rounded, den));
        long g = FxRate.gcd(remainderNum, den);
        return new Conversion(amount, Money.ofMinor(rounded, rate.quote()), rate, rounding, remainderNum / g, den / g);
    }

    /**
     * Convert out and back with the inverse rate. The residue is what the customer does not get back:
     * positive means the round trip lost money (it belongs in an FX rounding gain account), negative means it
     * manufactured money (an FX rounding loss). It is never silently zero.
     */
    static RoundTrip roundTrip(Money amount, FxRate rate, Rounding rounding) {
        Conversion out = convert(amount, rate, rounding);
        Conversion back = convert(out.to(), rate.invert(), rounding);
        Money residue = amount.minus(back.to());
        return new RoundTrip(out, back, residue);
    }
}

record RoundTrip(Conversion out, Conversion back, Money residue) {}

/**
 * A rate table keyed by pair, with a base currency through which any cross rate can be derived.
 * Direct rates win; otherwise base/X and base/Y are combined. Every lookup is checked for staleness.
 */
final class RateTable {
    private final Currency baseCurrency;
    private final Map<String, FxRate> rates = new LinkedHashMap<>();
    private final long maxAgeSeconds;

    RateTable(Currency baseCurrency, long maxAgeSeconds) {
        this.baseCurrency = baseCurrency;
        this.maxAgeSeconds = maxAgeSeconds;
    }

    void put(FxRate rate) {
        if (!rate.base().equals(baseCurrency)) {
            throw new IllegalArgumentException("table quotes against " + baseCurrency.code() + "; got " + rate.pair());
        }
        rates.put(rate.quote().code(), rate);
    }

    FxRate rate(Currency from, Currency to, long nowEpochSeconds) {
        if (from.equals(to)) throw new IllegalArgumentException("no rate needed for " + from.code());
        if (from.equals(baseCurrency)) return direct(to).requireFresh(nowEpochSeconds, maxAgeSeconds);
        if (to.equals(baseCurrency)) return direct(from).requireFresh(nowEpochSeconds, maxAgeSeconds).invert();
        FxRate a = direct(from).requireFresh(nowEpochSeconds, maxAgeSeconds);
        FxRate b = direct(to).requireFresh(nowEpochSeconds, maxAgeSeconds);
        return a.cross(b);
    }

    private FxRate direct(Currency quote) {
        FxRate r = rates.get(quote.code());
        if (r == null) throw new IllegalArgumentException("no " + baseCurrency.code() + "/" + quote.code() + " rate loaded");
        return r;
    }

    List<FxRate> all() { return Collections.unmodifiableList(new ArrayList<>(rates.values())); }
}

public final class Main {
    public static void main(String[] args) {
        long now = 1_700_000_000L;   // injected clock: 2023-11-14T22:13:20Z
        CurrencyRegistry registry = CurrencyRegistry.iso4217();
        Currency USD = registry.currency("USD"), EUR = registry.currency("EUR"), GBP = registry.currency("GBP");
        Currency JPY = registry.currency("JPY"), KWD = registry.currency("KWD");

        System.out.println("== ISO 4217 registry: the exponent is data ==");
        for (String code : List.of("USD", "JPY", "KWD", "CLF")) {
            CurrencyInfo info = registry.info(code);
            System.out.printf("%s numeric=%03d exponent=%d  1 major = %d minor  (%s)%n",
                    code, info.numericCode(), info.exponent(), info.currency().minorUnitsPerMajor(), info.name());
        }
        System.out.println("numeric 978 -> " + registry.byNumeric(978).code());
        try { registry.currency("USX"); } catch (UnknownCurrencyException e) { System.out.println("rejected: " + e.getMessage()); }

        System.out.println("\n== A rate is an exact rational with provenance ==");
        FxRate usdEur = FxRate.of(USD, EUR, "0.9217", "ecb-ref", now - 60, 50);
        FxRate usdJpy = FxRate.of(USD, JPY, "149.37", "ecb-ref", now - 60, 50);
        FxRate usdGbp = FxRate.of(USD, GBP, "0.7893", "ecb-ref", now - 60, 50);
        FxRate usdKwd = FxRate.of(USD, KWD, "0.30825", "ecb-ref", now - 60, 50);
        System.out.println(usdEur);
        System.out.println("inverted: " + usdEur.invert() + "   (exact: 10000/9217, not 1.0849...)");
        System.out.println("bid " + usdEur.bid().toDecimal(6) + "  mid " + usdEur.toDecimal(6) + "  ask " + usdEur.ask().toDecimal(6) + "  (50 bps spread)");

        System.out.println("\n== Conversion rounds exactly once, and reports what it dropped ==");
        Conversion c1 = FxConverter.convert(Money.parse("100.00", USD), usdEur, Rounding.HALF_EVEN);
        System.out.println(c1.from() + " -> " + c1.to() + "  dropped " + c1.remainderNumerator() + "/" + c1.remainderDenominator() + " of a cent");
        Conversion c2 = FxConverter.convert(Money.parse("19.99", USD), usdEur, Rounding.HALF_EVEN);
        System.out.println(c2.from() + " -> " + c2.to() + "  exact 18.424783, dropped " + c2.remainderNumerator() + "/" + c2.remainderDenominator() + " of a cent");
        Conversion c3 = FxConverter.convert(Money.parse("19.99", USD), usdJpy, Rounding.HALF_EVEN);
        System.out.println(c3.from() + " -> " + c3.to() + "  (JPY has no fraction: exact 2985.9063 yen)");
        Conversion c4 = FxConverter.convert(Money.parse("19.99", USD), usdKwd, Rounding.HALF_EVEN);
        System.out.println(c4.from() + " -> " + c4.to() + "  (KWD has three decimals)");
        Conversion c5 = FxConverter.convert(Money.parse("0.01", USD), usdEur, Rounding.DOWN);
        System.out.println(c5.from() + " -> " + c5.to() + "  with DOWN: one US cent is less than one euro cent");

        System.out.println("\n== Round trip: the residue is real money and needs a home ==");
        RoundTrip rt = FxConverter.roundTrip(Money.parse("19.99", USD), usdEur, Rounding.HALF_EVEN);
        System.out.println(rt.out().from() + " -> " + rt.out().to() + " -> " + rt.back().to() + "   residue " + rt.residue());
        RoundTrip rt2 = FxConverter.roundTrip(Money.parse("0.06", USD), usdEur, Rounding.HALF_EVEN);
        System.out.println(rt2.out().from() + " -> " + rt2.out().to() + " -> " + rt2.back().to() + "   residue " + rt2.residue());
        System.out.println("Positive residue: the platform gained a cent (FX rounding gain). Negative: it manufactured one (FX rounding loss).");
        System.out.println("Either way the residue is posted to an FX rounding account, never swallowed.");

        System.out.println("\n== Cross rates through a base currency ==");
        RateTable table = new RateTable(USD, 300);
        table.put(usdEur); table.put(usdGbp); table.put(usdJpy);
        FxRate eurGbp = table.rate(EUR, GBP, now);
        System.out.println("EUR/GBP via USD = " + eurGbp);
        Conversion c6 = FxConverter.convert(Money.parse("50.00", EUR), eurGbp, Rounding.HALF_EVEN);
        System.out.println(c6.from() + " -> " + c6.to() + "  (50 * 7893/9217 = 42.8176...)");
        FxRate eurUsd = table.rate(EUR, USD, now);
        System.out.println("EUR/USD (inverted) = " + eurUsd.toDecimal(6));

        System.out.println("\n== Rates go stale ==");
        try {
            table.rate(USD, EUR, now + 3600);
        } catch (StaleRateException e) { System.out.println("rejected: " + e.getMessage()); }
        try {
            FxConverter.convert(Money.parse("1.00", EUR), usdEur, Rounding.HALF_EVEN);
        } catch (CurrencyMismatchException e) { System.out.println("rejected: " + e.getMessage()); }
    }
}
