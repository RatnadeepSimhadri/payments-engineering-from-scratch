// Project: projects/idempotent-payment-gateway — shared money types.
// Copied from phases/10-money-and-ledgers/01-representing-money (Money is integer minor units + ISO 4217 currency).
// Java 21, standard library only.

import java.util.Arrays;
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
    HALF_UP,   // ties go away from zero
    DOWN,      // truncate toward zero
    UP,        // away from zero
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

/**
 * An exact amount of one currency, stored as a signed count of minor units. Immutable.
 * Arithmetic never produces a fraction: every operation that could is given a Rounding.
 */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {

    Money {
        Objects.requireNonNull(currency, "currency");
    }

    static Money ofMinor(long minorUnits, Currency currency) {
        return new Money(minorUnits, currency);
    }

    /** Parse a decimal string such as "12.34" or "-0.5" or "1200" exactly; reject excess precision. */
    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) {
            whole = s.substring(0, dot);
            frac = s.substring(dot + 1);
        }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(
                Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
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

    /** Multiply by an exact rational numerator/denominator (2.9% is 29/1000) and round once, as asked. */
    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        long product = Math.multiplyExact(minorUnits, numerator);
        return new Money(divideRounded(product, denominator, rounding), currency);
    }

    /** Integer division with an explicit rounding rule. */
    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;            // truncates toward zero
        long remainder = dividend % divisor;           // same sign as dividend
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
                yield (quotient % 2 == 0) ? quotient : quotient + sign; // exact tie: go to even
            }
        };
    }

    /** Split this amount in the given ratios so the parts sum exactly to the whole (largest-remainder method). */
    Money[] allocate(long... ratios) {
        if (ratios.length == 0) throw new IllegalArgumentException("need at least one ratio");
        long total = 0;
        for (long r : ratios) {
            if (r < 0) throw new IllegalArgumentException("ratios must be non-negative");
            total = Math.addExact(total, r);
        }
        if (total == 0) throw new IllegalArgumentException("ratios must not all be zero");
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
        long leftover = magnitude - distributed;        // always 0 <= leftover < ratios.length
        Integer[] order = new Integer[ratios.length];
        for (int i = 0; i < order.length; i++) order[i] = i;
        Arrays.sort(order, (a, b) -> Long.compare(remainders[b], remainders[a])); // stable: ties keep index order
        for (int k = 0; k < leftover; k++) shares[order[k]]++;
        Money[] result = new Money[ratios.length];
        for (int i = 0; i < ratios.length; i++) {
            result[i] = new Money(minorUnits < 0 ? -shares[i] : shares[i], currency);
        }
        return result;
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
