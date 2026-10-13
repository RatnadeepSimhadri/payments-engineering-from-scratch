// Tests for phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic. Run: javac -d .out *.java && java -cp .out MainTest
import java.math.BigDecimal;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkEquals(String name, Object expected, Object actual) {
        if (Objects.equals(expected, actual)) passed++;
        else failures.add(name + " (expected " + expected + ", got " + actual + ")");
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++;
            else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) {
        // --- floats: the failures the lesson warns about, pinned ---
        check("0.1 + 0.2 is not 0.3", Floats.add(0.1, 0.2) != 0.3);
        checkEquals("0.1 + 0.2 prints as 0.30000000000000004", "0.30000000000000004", Double.toString(Floats.add(0.1, 0.2)));
        checkEquals("(long)(19.99 * 100) loses a cent", 1998L, Floats.naiveCents(19.99));
        check("0.1 added ten times is not 1.0", Floats.sumRepeated(0.1, 10) != 1.0);
        check("0.01 added a million times is off by more than 1e-7", Math.abs(Floats.sumRepeated(0.01, 1_000_000) - 10_000) > 1e-7);
        check("0.25 is exact: 0.25 added four times is 1.0", Floats.sumRepeated(0.25, 4) == 1.0);

        // --- checked arithmetic: wrap vs error ---
        checkEquals("plain + wraps MAX to MIN", Long.MIN_VALUE, Checked.wrappingAdd(Long.MAX_VALUE, 1));
        checkEquals("checked add ordinary", 3L, Checked.add(1, 2));
        checkEquals("checked add negatives", -3L, Checked.add(-1, -2));
        checkThrows("checked add overflow", ArithmeticException.class, () -> Checked.add(Long.MAX_VALUE, 1));
        checkThrows("checked add underflow", ArithmeticException.class, () -> Checked.add(Long.MIN_VALUE, -1));
        checkThrows("checked sub underflow", ArithmeticException.class, () -> Checked.sub(Long.MIN_VALUE, 1));
        checkEquals("checked sub ordinary", -1L, Checked.sub(1, 2));
        checkEquals("checked mul ordinary", 6L, Checked.mul(2, 3));
        checkEquals("checked mul by zero", 0L, Checked.mul(Long.MAX_VALUE, 0));
        checkThrows("checked mul overflow", ArithmeticException.class, () -> Checked.mul(1L << 62, 4));
        checkThrows("checked mul MIN * -1", ArithmeticException.class, () -> Checked.mul(Long.MIN_VALUE, -1));
        checkThrows("checked mul -1 * MIN", ArithmeticException.class, () -> Checked.mul(-1, Long.MIN_VALUE));
        checkEquals("checked negate", -5L, Checked.negate(5));
        checkThrows("checked negate MIN", ArithmeticException.class, () -> Checked.negate(Long.MIN_VALUE));
        check("checked results agree with Math.*Exact", Checked.add(123456789L, 987654321L) == Math.addExact(123456789L, 987654321L)
                && Checked.mul(123456789L, 987L) == Math.multiplyExact(123456789L, 987L));

        // --- rounded division and bias ---
        checkEquals("HALF_EVEN tie 12.5 -> 12", 12L, Checked.divideRounded(125, 10, Rounding.HALF_EVEN));
        checkEquals("HALF_EVEN tie 13.5 -> 14", 14L, Checked.divideRounded(135, 10, Rounding.HALF_EVEN));
        checkEquals("HALF_UP tie 12.5 -> 13", 13L, Checked.divideRounded(125, 10, Rounding.HALF_UP));
        checkEquals("HALF_UP negative tie -12.5 -> -13", -13L, Checked.divideRounded(-125, 10, Rounding.HALF_UP));
        checkEquals("HALF_EVEN negative tie -12.5 -> -12", -12L, Checked.divideRounded(-125, 10, Rounding.HALF_EVEN));
        checkEquals("DOWN truncates toward zero", -12L, Checked.divideRounded(-129, 10, Rounding.DOWN));
        checkEquals("UP away from zero", 13L, Checked.divideRounded(121, 10, Rounding.UP));
        checkThrows("division by zero", ArithmeticException.class, () -> Checked.divideRounded(1, 0, Rounding.DOWN));
        checkEquals("HALF_UP drifts +50 over 100 ties", 50L, Checked.roundingBias(Rounding.HALF_UP));
        checkEquals("HALF_EVEN drifts 0 over 100 ties", 0L, Checked.roundingBias(Rounding.HALF_EVEN));
        checkEquals("DOWN drifts -50", -50L, Checked.roundingBias(Rounding.DOWN));
        checkEquals("UP drifts +50", 50L, Checked.roundingBias(Rounding.UP));

        // --- Fixed2 ---
        checkEquals("parse 19.99", 1999L, Fixed2.parse("19.99").hundredths());
        checkEquals("parse 7 pads", 700L, Fixed2.parse("7").hundredths());
        checkEquals("parse -0.5 pads", -50L, Fixed2.parse("-0.5").hundredths());
        checkEquals("parse .05", 5L, Fixed2.parse(".05").hundredths());
        checkThrows("parse rejects three decimals", ArithmeticException.class, () -> Fixed2.parse("19.995"));
        checkThrows("parse rejects garbage", NumberFormatException.class, () -> Fixed2.parse("19,99"));
        checkEquals("fromDouble 19.99 via shortest string", 1999L, Fixed2.fromDouble(19.99).hundredths());
        checkThrows("fromDouble(0.1 + 0.2) is rejected: the binary error is surfaced, not hidden", ArithmeticException.class, () -> Fixed2.fromDouble(0.1 + 0.2));
        checkThrows("fromDouble rejects 19.995", ArithmeticException.class, () -> Fixed2.fromDouble(19.995));
        checkThrows("fromDouble rejects NaN", ArithmeticException.class, () -> Fixed2.fromDouble(Double.NaN));
        checkEquals("toString 19.99", "19.99", Fixed2.ofHundredths(1999).toString());
        checkEquals("toString -0.05", "-0.05", Fixed2.ofHundredths(-5).toString());
        checkEquals("toString 0.00", "0.00", Fixed2.ZERO.toString());
        checkEquals("plus", Fixed2.parse("3.30"), Fixed2.parse("1.10").plus(Fixed2.parse("2.20")));
        checkEquals("minus goes negative", -50L, Fixed2.parse("1.00").minus(Fixed2.parse("1.50")).hundredths());
        checkThrows("plus overflow is an error", ArithmeticException.class, () -> Fixed2.ofHundredths(Long.MAX_VALUE).plus(Fixed2.ofHundredths(1)));
        checkEquals("times 19.99 x 3", Fixed2.parse("59.97"), Fixed2.parse("19.99").times(Fixed2.parse("3"), Rounding.HALF_EVEN));
        checkEquals("times 19.99 x 1.08 rounds 2158.92 -> 21.59", Fixed2.parse("21.59"), Fixed2.parse("19.99").times(Fixed2.parse("1.08"), Rounding.HALF_EVEN));
        checkEquals("times 0.05 x 0.05 = 0.0025 -> 0.00 HALF_EVEN", Fixed2.ZERO, Fixed2.parse("0.05").times(Fixed2.parse("0.05"), Rounding.HALF_EVEN));
        checkEquals("times 0.05 x 0.05 -> 0.01 UP", Fixed2.parse("0.01"), Fixed2.parse("0.05").times(Fixed2.parse("0.05"), Rounding.UP));
        checkEquals("timesRate 2.9% of 19.99 = 0.58", Fixed2.parse("0.58"), Fixed2.parse("19.99").timesRate(29, 1000, Rounding.HALF_EVEN));
        checkThrows("timesRate rejects zero denominator", ArithmeticException.class, () -> Fixed2.parse("1").timesRate(1, 0, Rounding.DOWN));
        Fixed2 third = Fixed2.parse("100.00").dividedBy(3, Rounding.HALF_EVEN);
        checkEquals("100.00 / 3 = 33.33", Fixed2.parse("33.33"), third);
        check("33.33 x 3 is 99.99, not 100.00: division alone does not conserve", !third.times(Fixed2.parse("3"), Rounding.HALF_EVEN).equals(Fixed2.parse("100.00")));
        check("compareTo", Fixed2.parse("1.00").compareTo(Fixed2.parse("2.00")) < 0);
        long millionCents = 0;
        Fixed2 acc = Fixed2.ZERO, cent = Fixed2.parse("0.01");
        for (int i = 0; i < 1_000_000; i++) { acc = acc.plus(cent); millionCents++; }
        checkEquals("a million cents is exactly 10000.00", Fixed2.parse("10000.00"), acc);

        // --- the decimal type the JDK ships ---
        checkEquals("BigDecimal from strings is exact", new BigDecimal("0.3"), new BigDecimal("0.1").add(new BigDecimal("0.2")));
        check("BigDecimal from a double carries the binary error", new BigDecimal(0.1).compareTo(new BigDecimal("0.1")) != 0);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
