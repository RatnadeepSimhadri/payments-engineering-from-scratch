// Project: projects/ledger-engine — stage 1 starter: the Money value type.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py ledger-engine --lang java --stage 1 --path <workspace>
// Same design as phases/10-money-and-ledgers/01-representing-money. Currency, Rounding, and the Money
// record are given; parse, plus, minus, times, divideRounded, and allocate are yours to implement.
// Java 21, standard library only.

import java.util.Objects;

/** ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
    static final Currency INR = new Currency("INR", 2);
    static final Currency JPY = new Currency("JPY", 0);
    static final Currency KWD = new Currency("KWD", 3);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) {
            throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
        }
    }

    /** 10^exponent: how many minor units make one major unit (100 for USD, 1 for JPY, 1000 for KWD). */
    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** How to resolve a fractional minor unit. */
enum Rounding {
    HALF_EVEN, // banker's rounding: ties go to the even neighbour; statistically unbiased
    HALF_UP,   // ties go away from zero; what most people mean by "round"
    DOWN,      // truncate toward zero; what you owe never rounds up against the payer
    UP,        // away from zero; fees that must cover a cost
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

/**
 * An exact amount of one currency, stored as a signed count of minor units.
 * Immutable. Arithmetic never produces a fraction: every operation that could is given a Rounding.
 */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {

    Money {
        Objects.requireNonNull(currency, "currency");
    }

    static Money ofMinor(long minorUnits, Currency currency) {
        return new Money(minorUnits, currency);
    }

    /**
     * Parse a decimal string such as "12.34", "-0.5", ".99" or "1200" exactly.
     * More decimals than the currency allows -> ArithmeticException. Non-digits -> NumberFormatException.
     */
    static Money parse(String text, Currency currency) {
        throw new UnsupportedOperationException("stage 1: implement Money.parse");
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    /** Same-currency addition with overflow detection (Math.addExact). */
    Money plus(Money other) {
        throw new UnsupportedOperationException("stage 1: implement Money.plus");
    }

    /** Same-currency subtraction with overflow detection (Math.subtractExact). */
    Money minus(Money other) {
        throw new UnsupportedOperationException("stage 1: implement Money.minus");
    }

    Money negate() {
        return new Money(Math.negateExact(minorUnits), currency);
    }

    Money abs() {
        return minorUnits < 0 ? negate() : this;
    }

    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isPositive() { return minorUnits > 0; }

    /**
     * Multiply by an exact rational numerator/denominator (a fee of 2.9% is 29/1000) and resolve the
     * remainder with the given rounding. A denominator <= 0 is an ArithmeticException.
     */
    Money times(long numerator, long denominator, Rounding rounding) {
        throw new UnsupportedOperationException("stage 1: implement Money.times");
    }

    /** Integer division with an explicit rounding rule. Public so the ledger can reuse it. */
    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        throw new UnsupportedOperationException("stage 1: implement Money.divideRounded");
    }

    /**
     * Split this amount in the given ratios so the parts sum exactly to the whole (largest-remainder
     * method, ties broken by index order). No ratios, a negative ratio, or all-zero ratios -> IllegalArgumentException.
     */
    Money[] allocate(long... ratios) {
        throw new UnsupportedOperationException("stage 1: implement Money.allocate");
    }

    /** Sum any number of same-currency amounts; an empty sum of a currency is that currency's zero. */
    static Money sum(Currency currency, Money... amounts) {
        Money acc = new Money(0, currency);
        for (Money m : amounts) acc = acc.plus(m);
        return acc;
    }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
    }

    /** "12.34 USD", "-0.50 EUR", "1200 JPY", "1.250 KWD": the decimal point is placed by the exponent. */
    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String whole = Long.toString(magnitude / scale);
        String result = whole;
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = whole + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}
