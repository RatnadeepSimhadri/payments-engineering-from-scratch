// Lesson: phases/12-payment-systems-design/12-currency-conversion-at-checkout/docs/en.md
// Currency Conversion at Checkout and DCC
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 4217 (currency exponents); Regulation (EU) 2019/518 (currency conversion disclosure as a
//          markup over the ECB reference rate); ECB euro foreign exchange reference rates. Java 21, stdlib only.

import java.math.BigDecimal;
import java.math.BigInteger;
import java.math.RoundingMode;
import java.util.Arrays;
import java.util.HashMap;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
    static final Currency JPY = new Currency("JPY", 0);

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        }
        String padded = frac + "0".repeat(currency.exponent() - frac.length());
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.isEmpty() ? 0 : Long.parseLong(padded));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }

    /** Largest-remainder split (Phase 10, Lesson 01): shares sum to exactly this amount. */
    Money[] allocate(long... ratios) {
        long total = 0;
        for (long r : ratios) {
            if (r < 0) throw new IllegalArgumentException("ratios must be non-negative");
            total = Math.addExact(total, r);
        }
        if (ratios.length == 0 || total == 0) throw new IllegalArgumentException("need a positive ratio");
        long[] shares = new long[ratios.length];
        long[] remainders = new long[ratios.length];
        long distributed = 0;
        long magnitude = Math.abs(minorUnits);
        for (int i = 0; i < ratios.length; i++) {
            long scaled = Math.multiplyExact(magnitude, ratios[i]);
            shares[i] = scaled / total;
            remainders[i] = scaled % total;
            distributed += shares[i];
        }
        Integer[] order = new Integer[ratios.length];
        for (int i = 0; i < order.length; i++) order[i] = i;
        Arrays.sort(order, (a, b) -> Long.compare(remainders[b], remainders[a]));
        for (int k = 0; k < magnitude - distributed; k++) shares[order[k]]++;
        Money[] result = new Money[ratios.length];
        for (int i = 0; i < ratios.length; i++) result[i] = new Money(minorUnits < 0 ? -shares[i] : shares[i], currency);
        return result;
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Exact rationals. A rate is a ratio; rounding happens once, on the final amount, never on the rate.
// ---------------------------------------------------------------------------------------------

record Rational(BigInteger num, BigInteger den) implements Comparable<Rational> {
    static final Rational ONE = new Rational(BigInteger.ONE, BigInteger.ONE);

    Rational {
        Objects.requireNonNull(num);
        Objects.requireNonNull(den);
        if (den.signum() == 0) throw new ArithmeticException("zero denominator");
        if (den.signum() < 0) { num = num.negate(); den = den.negate(); }
        BigInteger g = num.gcd(den);
        if (!g.equals(BigInteger.ONE)) { num = num.divide(g); den = den.divide(g); }
    }

    static Rational of(long num, long den) { return new Rational(BigInteger.valueOf(num), BigInteger.valueOf(den)); }

    /** Exact parse of a decimal string: "1.0850" is 10850/10000 = 217/200. */
    static Rational parse(String decimal) {
        BigDecimal d = new BigDecimal(decimal.strip());
        return d.scale() >= 0
                ? new Rational(d.unscaledValue(), BigInteger.TEN.pow(d.scale()))
                : new Rational(d.unscaledValue().multiply(BigInteger.TEN.pow(-d.scale())), BigInteger.ONE);
    }

    /** basis points as a fraction: 350 bps = 350/10000. */
    static Rational bps(long basisPoints) { return of(basisPoints, 10_000); }

    static Rational pow10(int exponent) {
        return exponent >= 0 ? new Rational(BigInteger.TEN.pow(exponent), BigInteger.ONE)
                : new Rational(BigInteger.ONE, BigInteger.TEN.pow(-exponent));
    }

    Rational times(Rational o) { return new Rational(num.multiply(o.num), den.multiply(o.den)); }
    Rational dividedBy(Rational o) { return new Rational(num.multiply(o.den), den.multiply(o.num)); }
    Rational plus(Rational o) { return new Rational(num.multiply(o.den).add(o.num.multiply(den)), den.multiply(o.den)); }
    Rational minus(Rational o) { return plus(new Rational(o.num.negate(), o.den)); }
    Rational inverse() { return new Rational(den, num); }
    int signum() { return num.signum(); }

    long roundToLong(RoundingMode mode) {
        return new BigDecimal(num).divide(new BigDecimal(den), 0, mode).longValueExact();
    }

    String toDecimal(int scale) { return new BigDecimal(num).divide(new BigDecimal(den), scale, RoundingMode.HALF_EVEN).toPlainString(); }

    @Override public int compareTo(Rational o) { return num.multiply(o.den).compareTo(o.num.multiply(den)); }
    @Override public String toString() { return num + "/" + den; }
}

/** 1 unit of {@code from} = {@code rate} units of {@code to}, in major units. */
record FxRate(Currency from, Currency to, Rational rate) {
    FxRate {
        if (rate.signum() <= 0) throw new IllegalArgumentException("rate must be positive");
        if (from.equals(to) && rate.compareTo(Rational.ONE) != 0) throw new IllegalArgumentException("same-currency rate must be 1");
    }

    FxRate inverse() { return new FxRate(to, from, rate.inverse()); }

    /** Multiply the rate by (1 + bps/10000); a negative bps scales it down. */
    FxRate adjustedBy(long basisPoints) { return new FxRate(from, to, rate.times(Rational.ONE.plus(Rational.bps(basisPoints)))); }
}

final class FxConverter {
    private FxConverter() {}

    /** Exact conversion in minor units, one rounding at the end with an explicit mode. */
    static Money convert(Money amount, FxRate rate, RoundingMode mode) {
        if (!amount.currency().equals(rate.from())) throw new CurrencyMismatchException(amount.currency(), rate.from());
        Rational minor = Rational.of(amount.minorUnits(), 1)
                .times(rate.rate())
                .times(Rational.pow10(rate.to().exponent() - rate.from().exponent()));
        return Money.ofMinor(minor.roundToLong(mode), rate.to());
    }

    /** The anti-pattern: round the rate to a few decimals first, then convert. */
    static Money convertWithRoundedRate(Money amount, FxRate rate, int rateDecimals, RoundingMode mode) {
        Rational rounded = Rational.parse(rate.rate().toDecimal(rateDecimals));
        return convert(amount, new FxRate(rate.from(), rate.to(), rounded), mode);
    }
}

// ---------------------------------------------------------------------------------------------
// Time and market rates are injected.
// ---------------------------------------------------------------------------------------------

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long start) { this.now = start; }
    public long nowMillis() { return now; }
    void advanceMillis(long ms) { now += ms; }
}

interface RateSource { Rational mid(Currency from, Currency to); }

/** Mid-market rates set by hand; the inverse pair is derived exactly. */
final class FakeRateSource implements RateSource {
    private final Map<String, Rational> rates = new HashMap<>();

    void set(Currency from, Currency to, String decimal) {
        Rational r = Rational.parse(decimal);
        rates.put(from.code() + to.code(), r);
        rates.put(to.code() + from.code(), r.inverse());
    }

    public Rational mid(Currency from, Currency to) {
        if (from.equals(to)) return Rational.ONE;
        Rational r = rates.get(from.code() + to.code());
        if (r == null) throw new IllegalArgumentException("no rate " + from.code() + "/" + to.code());
        return r;
    }
}

// ---------------------------------------------------------------------------------------------
// Presentment pricing with a rate lock: the merchant is promised a settlement amount; the shopper
// pays a presentment amount; whoever locked the rate owns the movement until settlement.
// ---------------------------------------------------------------------------------------------

final class QuoteExpiredException extends RuntimeException {
    QuoteExpiredException(String id, long now, long expiresAt) { super("quote " + id + " expired at " + expiresAt + ", now " + now); }
}

/** A locked customer rate settlement -> presentment, valid on [issuedAt, expiresAt). */
record RateQuote(String id, FxRate customerRate, Rational mid, long spreadBps, long issuedAtMillis, long expiresAtMillis) {
    boolean validAt(long now) { return now >= issuedAtMillis && now < expiresAtMillis; }
    Currency settlement() { return customerRate.from(); }
    Currency presentment() { return customerRate.to(); }
}

record LockedCharge(RateQuote quote, Money settlementAmount, Money presentmentAmount) {}

record SettlementResult(Money merchantReceives, Money pspProceeds, Money pspFxPnl) {}

final class FxDesk {
    private final Clock clock;
    private final RateSource rates;
    private final long spreadBps;
    private final long lockMillis;
    private final Map<String, RateQuote> quotes = new HashMap<>();
    private int seq;

    FxDesk(Clock clock, RateSource rates, long spreadBps, long lockMillis) {
        if (spreadBps < 0 || lockMillis <= 0) throw new IllegalArgumentException("spread >= 0 and lock > 0 required");
        this.clock = clock;
        this.rates = rates;
        this.spreadBps = spreadBps;
        this.lockMillis = lockMillis;
    }

    /** Lock a rate now. The shopper's rate is mid plus the spread, so the shopper pays more presentment currency. */
    RateQuote quote(Currency settlement, Currency presentment) {
        long now = clock.nowMillis();
        Rational mid = rates.mid(settlement, presentment);
        FxRate customer = new FxRate(settlement, presentment, mid).adjustedBy(spreadBps);
        RateQuote q = new RateQuote(String.format("fxq_%04d", ++seq), customer, mid, spreadBps, now, now + lockMillis);
        quotes.put(q.id(), q);
        return q;
    }

    /** The price to display. Rounded up so the shopper never pays less than the locked rate implies. */
    static Money presentmentPrice(Money settlementPrice, RateQuote q) {
        return FxConverter.convert(settlementPrice, q.customerRate(), RoundingMode.CEILING);
    }

    /** Checkout confirmation: only inside the lock window. After it, the shopper must see a new price. */
    LockedCharge confirm(String quoteId, Money settlementPrice) {
        RateQuote q = quotes.get(quoteId);
        if (q == null) throw new IllegalArgumentException("unknown quote " + quoteId);
        if (!settlementPrice.currency().equals(q.settlement())) throw new CurrencyMismatchException(settlementPrice.currency(), q.settlement());
        long now = clock.nowMillis();
        if (!q.validAt(now)) throw new QuoteExpiredException(q.id(), now, q.expiresAtMillis());
        return new LockedCharge(q, settlementPrice, presentmentPrice(settlementPrice, q));
    }

    /**
     * Days later the PSP sells the presentment currency at the then-market mid. The merchant gets the
     * promised amount regardless; the difference is the PSP's FX P&L (spread minus market movement).
     */
    static SettlementResult settle(LockedCharge charge, Rational marketMidPresentmentToSettlement) {
        FxRate market = new FxRate(charge.quote().presentment(), charge.quote().settlement(), marketMidPresentmentToSettlement);
        Money proceeds = FxConverter.convert(charge.presentmentAmount(), market, RoundingMode.HALF_EVEN);
        return new SettlementResult(charge.settlementAmount(), proceeds, proceeds.minus(charge.settlementAmount()));
    }

    /** The settlement-time rate at which the PSP's P&L is exactly zero. */
    static Rational breakEvenRate(LockedCharge charge) {
        return Rational.of(charge.settlementAmount().minorUnits(), 1)
                .dividedBy(Rational.of(charge.presentmentAmount().minorUnits(), 1))
                .times(Rational.pow10(charge.quote().presentment().exponent() - charge.quote().settlement().exponent()));
    }
}

// ---------------------------------------------------------------------------------------------
// Dynamic currency conversion: the cardholder is offered their card's currency at a disclosed markup.
// ---------------------------------------------------------------------------------------------

record DccOffer(Money merchantAmount, Money cardAmount, FxRate dccRate, Rational referenceRate, long markupBps) {

    /** Markup over the reference rate, as an exact percentage: (dcc / reference - 1) * 100. */
    Rational markupPercent() { return dccRate.rate().dividedBy(referenceRate).minus(Rational.ONE).times(Rational.of(100, 1)); }

    /** What the cardholder actually pays over the reference conversion, after rounding the amount. */
    Rational effectiveMarkupPercent() {
        Money atReference = FxConverter.convert(merchantAmount, new FxRate(merchantAmount.currency(), cardAmount.currency(), referenceRate), RoundingMode.HALF_EVEN);
        return Rational.of(cardAmount.minorUnits(), atReference.minorUnits()).minus(Rational.ONE).times(Rational.of(100, 1));
    }

    /** DCC gross margin in card currency: cardholder amount minus the reference conversion. */
    Money grossMargin() {
        Money atReference = FxConverter.convert(merchantAmount, new FxRate(merchantAmount.currency(), cardAmount.currency(), referenceRate), RoundingMode.HALF_EVEN);
        return cardAmount.minus(atReference);
    }

    String disclosure() {
        return "Pay " + cardAmount + " (rate 1 " + merchantAmount.currency().code() + " = " + dccRate.rate().toDecimal(6) + " "
                + cardAmount.currency().code() + ", including a " + markupPercent().toDecimal(2)
                + "% markup over the ECB reference rate " + referenceRate.toDecimal(4) + ") or pay " + merchantAmount
                + " and let your card issuer convert.";
    }
}

final class Dcc {
    private Dcc() {}

    static DccOffer offer(Money merchantAmount, Currency cardCurrency, Rational referenceRate, long markupBps) {
        if (merchantAmount.currency().equals(cardCurrency)) throw new IllegalArgumentException("DCC needs a different card currency");
        if (markupBps < 0) throw new IllegalArgumentException("markup must be >= 0");
        FxRate rate = new FxRate(merchantAmount.currency(), cardCurrency, referenceRate).adjustedBy(markupBps);
        Money cardAmount = FxConverter.convert(merchantAmount, rate, RoundingMode.HALF_EVEN);
        return new DccOffer(merchantAmount, cardAmount, rate, referenceRate, markupBps);
    }

    /** What declining DCC costs: the network converts at its rate and the issuer may add a foreign transaction fee. */
    static Money issuerConversion(Money merchantAmount, Currency cardCurrency, Rational networkRate, long issuerFeeBps) {
        Money converted = FxConverter.convert(merchantAmount, new FxRate(merchantAmount.currency(), cardCurrency, networkRate), RoundingMode.HALF_EVEN);
        Money fee = Money.ofMinor(Rational.of(converted.minorUnits(), 1).times(Rational.bps(issuerFeeBps)).roundToLong(RoundingMode.HALF_EVEN), cardCurrency);
        return converted.plus(fee);
    }
}

public final class Main {
    static final long T0 = 1_760_000_000_000L;
    static final long FIFTEEN_MINUTES = 15 * 60 * 1000L;

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(T0);
        FakeRateSource market = new FakeRateSource();
        market.set(Currency.EUR, Currency.USD, "1.0850");
        FxDesk desk = new FxDesk(clock, market, 200, FIFTEEN_MINUTES);
        Money price = Money.parse("120.00", Currency.USD);

        System.out.println("== Presentment vs settlement: a US merchant shows EUR to a German shopper ==");
        RateQuote q = desk.quote(Currency.USD, Currency.EUR);
        System.out.println("mid USD->EUR      = " + q.mid() + " = " + q.mid().toDecimal(10));
        System.out.println("customer USD->EUR = " + q.customerRate().rate() + " = " + q.customerRate().rate().toDecimal(10) + " (mid + 2.00%)");
        System.out.println("lock window       = [t0, t0 + 15 min)");
        clock.advanceMillis(9 * 60 * 1000L);
        LockedCharge charge = desk.confirm(q.id(), price);
        System.out.println("confirmed at +9m: shopper pays " + charge.presentmentAmount() + ", merchant is promised " + charge.settlementAmount());

        System.out.println("\n== Who owns the FX risk: the PSP, from lock to settlement ==");
        for (String eurUsd : new String[] {"1.0850", "1.0700", "1.0600", "1.1000"}) {
            SettlementResult r = FxDesk.settle(charge, Rational.parse(eurUsd));
            System.out.printf("EUR/USD at settlement %s: PSP sells %s for %s, merchant gets %s, PSP FX P&L %s%n",
                    eurUsd, charge.presentmentAmount(), r.pspProceeds(), r.merchantReceives(), r.pspFxPnl());
        }
        System.out.println("break-even EUR/USD = " + FxDesk.breakEvenRate(charge).toDecimal(6));

        System.out.println("\n== The lock expires: a late confirmation must re-quote ==");
        clock.advanceMillis(6 * 60 * 1000L);
        try {
            desk.confirm(q.id(), price);
        } catch (QuoteExpiredException e) {
            System.out.println("at +15m: " + e.getMessage());
        }
        market.set(Currency.EUR, Currency.USD, "1.0600");
        RateQuote fresh = desk.quote(Currency.USD, Currency.EUR);
        System.out.println("re-quoted at EUR/USD 1.0600: shopper now pays " + FxDesk.presentmentPrice(price, fresh));

        System.out.println("\n== Round the amount once, never the rate ==");
        Money big = Money.parse("50000.00", Currency.USD);
        System.out.println("exact rate:          " + FxConverter.convert(big, q.customerRate(), RoundingMode.HALF_EVEN));
        System.out.println("rate rounded to 4dp: " + FxConverter.convertWithRoundedRate(big, q.customerRate(), 4, RoundingMode.HALF_EVEN)
                + "  (rate " + q.customerRate().rate().toDecimal(4) + ")");
        System.out.println("USD 120.00 -> JPY at 152.37: " + FxConverter.convert(price, new FxRate(Currency.USD, Currency.JPY, Rational.parse("152.37")), RoundingMode.HALF_EVEN));

        System.out.println("\n== DCC: a UK card at a German merchant, EUR 100.00 ==");
        Money eur100 = Money.parse("100.00", Currency.EUR);
        DccOffer offer = Dcc.offer(eur100, Currency.GBP, Rational.parse("0.8550"), 350);
        System.out.println(offer.disclosure());
        System.out.println("disclosed markup  = " + offer.markupPercent().toDecimal(4) + "% (exact: " + offer.markupPercent() + ")");
        System.out.println("effective markup  = " + offer.effectiveMarkupPercent().toDecimal(4) + "% after rounding the amount");
        Money[] split = offer.grossMargin().allocate(3000, 7000);
        System.out.println("DCC gross margin  = " + offer.grossMargin() + ": merchant rebate " + split[0] + ", DCC provider " + split[1]);
        System.out.println("decline DCC, network 0.8560, no issuer fee:   " + Dcc.issuerConversion(eur100, Currency.GBP, Rational.parse("0.8560"), 0));
        System.out.println("decline DCC, network 0.8560, 2.99% issuer fee: " + Dcc.issuerConversion(eur100, Currency.GBP, Rational.parse("0.8560"), 299));
    }
}
