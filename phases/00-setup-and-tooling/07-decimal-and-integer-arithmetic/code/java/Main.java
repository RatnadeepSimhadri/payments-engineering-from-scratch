// Lesson: phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic/docs/en.md
// Decimal and Integer Arithmetic in Three Runtimes: floats, wrapping ints, checked ints, and a Fixed2 type
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: IEEE 754-2019 (binary64); Goldberg, "What Every Computer Scientist Should Know About Floating-Point
//          Arithmetic" (1991); JLS 15.18.2 (integer overflow wraps); Java 21 java.math.BigDecimal.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.math.BigDecimal;

/** How to resolve a fraction when integer division is inexact. Same four modes as Phase 10, Lesson 01. */
enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

/** Where floats go wrong, isolated so the tests can pin each failure. */
final class Floats {
    static double add(double a, double b) {
        return a + b;
    }

    /** The penny-skimming bug: (long)(19.99 * 100) is 1998 because 19.99 * 100 is really 1998.9999999999998. */
    static long naiveCents(double amount) {
        return (long) (amount * 100);
    }

    /** Add `term` to 0.0 `times` times, as a double. Each addition rounds; the errors do not cancel. */
    static double sumRepeated(double term, int times) {
        double acc = 0.0;
        for (int i = 0; i < times; i++) acc += term;
        return acc;
    }
}

/** 64-bit integer arithmetic that refuses to wrap. Hand-written so the Go version is the same code. */
final class Checked {
    static long add(long a, long b) {
        long r = a + b;
        if (((a ^ r) & (b ^ r)) < 0) throw new ArithmeticException("long overflow: " + a + " + " + b);
        return r;                                  // overflow iff both operands disagree in sign with the result
    }

    static long sub(long a, long b) {
        long r = a - b;
        if (((a ^ b) & (a ^ r)) < 0) throw new ArithmeticException("long overflow: " + a + " - " + b);
        return r;
    }

    static long mul(long a, long b) {
        long r = a * b;
        // if it did not wrap, dividing back recovers b; the one case division cannot catch is -1 * MIN_VALUE
        if (a != 0 && (r / a != b || (a == -1 && b == Long.MIN_VALUE))) {
            throw new ArithmeticException("long overflow: " + a + " * " + b);
        }
        return r;
    }

    static long negate(long a) {
        if (a == Long.MIN_VALUE) throw new ArithmeticException("long overflow: -(" + a + ")");
        return -a;
    }

    /** What the hardware does: two's-complement wrap. Here only to show it next to the checked version. */
    static long wrappingAdd(long a, long b) {
        return a + b;
    }

    /** Integer division with an explicit rounding rule; identical to Phase 10, Lesson 01. */
    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        if (divisor == 0) throw new ArithmeticException("division by zero");
        long quotient = dividend / divisor;        // truncates toward zero
        long remainder = dividend % divisor;       // same sign as dividend
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

    /**
     * Round each of 0.5, 1.5, ..., 99.5 (stored as tenths: 5, 15, ..., 995) to an integer with the given mode
     * and return the sum minus the exact sum (5000). HALF_UP drifts by +50 over 100 ties; HALF_EVEN by 0.
     */
    static long roundingBias(Rounding rounding) {
        long sum = 0;
        for (long tenths = 5; tenths < 1000; tenths += 10) sum += divideRounded(tenths, 10, rounding);
        return sum - 5000;
    }
}

/**
 * A fixed-point decimal with exactly two places, stored as a long count of hundredths.
 * No currency: that, plus allocation, is what Phase 10, Lesson 01's Money adds on top of this type.
 * Every operation that could produce a fraction takes a Rounding. Overflow is an error, never a wrap.
 */
record Fixed2(long hundredths) implements Comparable<Fixed2> {
    static final Fixed2 ZERO = new Fixed2(0);

    static Fixed2 ofHundredths(long hundredths) {
        return new Fixed2(hundredths);
    }

    /** Parse "19.99", "-0.5", "7" exactly. More than two decimals is an error, not a rounding. */
    static Fixed2 parse(String text) {
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
            throw new NumberFormatException("not a decimal: " + text);
        }
        if (frac.length() > 2) throw new ArithmeticException(text + " has more than two decimals");
        while (frac.length() < 2) frac += "0";
        long value = Checked.add(Checked.mul(Long.parseLong(whole), 100), Long.parseLong(frac));
        return new Fixed2(negative ? -value : value);
    }

    /**
     * The only correct way in from a double: through its shortest round-trip decimal string, never
     * through (long)(v * 100). 19.99 -> "19.99" -> 1999; 19.995 is rejected like any 3-decimal input.
     */
    static Fixed2 fromDouble(double value) {
        if (Double.isNaN(value) || Double.isInfinite(value)) throw new ArithmeticException("not finite: " + value);
        return parse(BigDecimal.valueOf(value).toPlainString());   // valueOf uses Double.toString, the shortest repr
    }

    Fixed2 plus(Fixed2 other) {
        return new Fixed2(Checked.add(hundredths, other.hundredths));
    }

    Fixed2 minus(Fixed2 other) {
        return new Fixed2(Checked.sub(hundredths, other.hundredths));
    }

    Fixed2 negate() {
        return new Fixed2(Checked.negate(hundredths));
    }

    /** Multiply two Fixed2 values: the product has four decimals, so two must be rounded away, your way. */
    Fixed2 times(Fixed2 other, Rounding rounding) {
        long product = Checked.mul(hundredths, other.hundredths);  // scaled by 10^4
        return new Fixed2(Checked.divideRounded(product, 100, rounding));
    }

    /** Multiply by an exact rational: 2.9% is 29/1000, an FX rate of 0.9217 is 9217/10000. */
    Fixed2 timesRate(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Fixed2(Checked.divideRounded(Checked.mul(hundredths, numerator), denominator, rounding));
    }

    Fixed2 dividedBy(long divisor, Rounding rounding) {
        return new Fixed2(Checked.divideRounded(hundredths, divisor, rounding));
    }

    @Override
    public int compareTo(Fixed2 other) {
        return Long.compare(hundredths, other.hundredths);
    }

    /** "19.99", "-0.05", "0.00": always two decimals, never scientific notation. */
    @Override
    public String toString() {
        long magnitude = Math.abs(hundredths);
        String frac = Long.toString(magnitude % 100);
        if (frac.length() < 2) frac = "0" + frac;
        return (hundredths < 0 ? "-" : "") + (magnitude / 100) + "." + frac;
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== 0.1 + 0.2 ==");
        double a = 0.1, b = 0.2;
        System.out.println("0.1 + 0.2 = " + Floats.add(a, b) + "  (equals 0.3? " + (Floats.add(a, b) == 0.3) + ")");
        System.out.println("(long)(19.99 * 100) = " + Floats.naiveCents(19.99) + "  <- the penny-skimming bug (19.99 * 100 = 1998.9999999999998)");
        System.out.println("Fixed2.fromDouble(19.99) = " + Fixed2.fromDouble(19.99) + "  (via the shortest decimal string \"19.99\")");

        System.out.println("\n== Accumulation ==");
        System.out.println("0.1 added 10 times as double = " + Floats.sumRepeated(0.1, 10));
        System.out.printf("0.01 added 1,000,000 times as double = %.10f (error %.3e)%n",
                Floats.sumRepeated(0.01, 1_000_000), Floats.sumRepeated(0.01, 1_000_000) - 10_000);
        Fixed2 cent = Fixed2.parse("0.01"), acc = Fixed2.ZERO;
        for (int i = 0; i < 1_000_000; i++) acc = acc.plus(cent);
        System.out.println("0.01 added 1,000,000 times as Fixed2 = " + acc + " exactly");

        System.out.println("\n== Rounding bias on 100 exact half-ties (0.5, 1.5, ..., 99.5) ==");
        for (Rounding r : Rounding.values()) {
            System.out.printf("%-9s drift = %+d%n", r, Checked.roundingBias(r));
        }

        System.out.println("\n== Overflow ==");
        System.out.println("Long.MAX_VALUE + 1 with plain + = " + Checked.wrappingAdd(Long.MAX_VALUE, 1) + "  (wrapped to Long.MIN_VALUE)");
        try {
            Checked.add(Long.MAX_VALUE, 1);
        } catch (ArithmeticException e) {
            System.out.println("Checked.add(Long.MAX_VALUE, 1) -> ArithmeticException: " + e.getMessage());
        }

        System.out.println("\n== Fixed2: two decimals, explicit rounding ==");
        Fixed2 price = Fixed2.parse("19.99");
        System.out.println(price + " x 3        = " + price.times(Fixed2.parse("3"), Rounding.HALF_EVEN));
        System.out.println(price + " x 2.9%     = " + price.timesRate(29, 1000, Rounding.HALF_EVEN) + "  (57.971 hundredths -> 58)");
        System.out.println(price + " x 1.08     = " + price.times(Fixed2.parse("1.08"), Rounding.HALF_EVEN) + "  (2158.92 hundredths -> 2159)");
        Fixed2 third = Fixed2.parse("100.00").dividedBy(3, Rounding.HALF_EVEN);
        System.out.println("100.00 / 3       = " + third + ", and " + third + " x 3 = " + third.times(Fixed2.parse("3"), Rounding.HALF_EVEN)
                + "  <- why Phase 10 adds allocate()");
        try {
            Fixed2.parse("19.995");
        } catch (ArithmeticException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Decimal types in the standard library ==");
        System.out.println("new BigDecimal(\"0.1\").add(new BigDecimal(\"0.2\")) = " + new BigDecimal("0.1").add(new BigDecimal("0.2")));
        System.out.println("new BigDecimal(0.1)  (from a double)             = " + new BigDecimal(0.1));
    }
}
