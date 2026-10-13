// Tests for phases/10-money-and-ledgers/01-representing-money. Run: javac -d .out *.java && java -cp .out MainTest
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
        Currency USD = Currency.USD;

        // parsing and formatting round-trip
        check("parse 12.34 USD", Money.parse("12.34", USD).minorUnits() == 1234);
        check("parse 12 USD pads", Money.parse("12", USD).minorUnits() == 1200);
        check("parse 0.5 USD pads", Money.parse("0.5", USD).minorUnits() == 50);
        check("parse -0.07 USD", Money.parse("-0.07", USD).minorUnits() == -7);
        check("parse .99 USD", Money.parse(".99", USD).minorUnits() == 99);
        check("parse JPY no decimals", Money.parse("1200", Currency.JPY).minorUnits() == 1200);
        check("parse KWD three decimals", Money.parse("1.250", Currency.KWD).minorUnits() == 1250);
        check("toString USD", Money.ofMinor(1234, USD).toString().equals("12.34 USD"));
        check("toString pads fraction", Money.ofMinor(1205, USD).toString().equals("12.05 USD"));
        check("toString negative small", Money.ofMinor(-7, USD).toString().equals("-0.07 USD"));
        check("toString JPY", Money.ofMinor(1200, Currency.JPY).toString().equals("1200 JPY"));
        check("toString KWD", Money.ofMinor(1250, Currency.KWD).toString().equals("1.250 KWD"));
        checkThrows("reject excess precision", ArithmeticException.class, () -> Money.parse("1.5", Currency.JPY));
        checkThrows("reject excess precision USD", ArithmeticException.class, () -> Money.parse("1.005", USD));
        checkThrows("reject garbage", NumberFormatException.class, () -> Money.parse("12,34", USD));

        // arithmetic and currency safety
        check("plus", Money.parse("1.10", USD).plus(Money.parse("2.20", USD)).equals(Money.parse("3.30", USD)));
        check("minus goes negative", Money.parse("1.00", USD).minus(Money.parse("1.50", USD)).minorUnits() == -50);
        check("negate", Money.parse("5.00", USD).negate().minorUnits() == -500);
        check("abs", Money.ofMinor(-5, USD).abs().minorUnits() == 5);
        checkThrows("currency mismatch plus", CurrencyMismatchException.class,
                () -> Money.parse("1.00", USD).plus(Money.parse("1.00", Currency.EUR)));
        checkThrows("currency mismatch compare", CurrencyMismatchException.class,
                () -> Money.parse("1.00", USD).compareTo(Money.parse("1.00", Currency.EUR)));
        checkThrows("overflow is an error, not a wrap", ArithmeticException.class,
                () -> Money.ofMinor(Long.MAX_VALUE, USD).plus(Money.ofMinor(1, USD)));
        check("compareTo", Money.parse("1.00", USD).compareTo(Money.parse("2.00", USD)) < 0);
        check("sum of nothing is zero", Money.sum(USD).isZero());

        // rounding modes on exact ties and non-ties
        check("divideRounded HALF_EVEN tie to even (12.5 -> 12)", Money.divideRounded(125, 10, Rounding.HALF_EVEN) == 12);
        check("divideRounded HALF_EVEN tie to even (13.5 -> 14)", Money.divideRounded(135, 10, Rounding.HALF_EVEN) == 14);
        check("divideRounded HALF_UP tie (12.5 -> 13)", Money.divideRounded(125, 10, Rounding.HALF_UP) == 13);
        check("divideRounded HALF_UP negative tie (-12.5 -> -13)", Money.divideRounded(-125, 10, Rounding.HALF_UP) == -13);
        check("divideRounded HALF_EVEN negative tie (-12.5 -> -12)", Money.divideRounded(-125, 10, Rounding.HALF_EVEN) == -12);
        check("divideRounded DOWN truncates", Money.divideRounded(129, 10, Rounding.DOWN) == 12);
        check("divideRounded DOWN negative truncates toward zero", Money.divideRounded(-129, 10, Rounding.DOWN) == -12);
        check("divideRounded UP", Money.divideRounded(121, 10, Rounding.UP) == 13);
        check("divideRounded UP negative", Money.divideRounded(-121, 10, Rounding.UP) == -13);
        check("divideRounded exact", Money.divideRounded(120, 10, Rounding.HALF_EVEN) == 12);
        check("fee 2.9% of 19.99 = 0.58", Money.parse("19.99", USD).times(29, 1000, Rounding.HALF_EVEN).minorUnits() == 58);
        check("times 3", Money.parse("19.99", USD).times(3, 1, Rounding.HALF_EVEN).minorUnits() == 5997);
        checkThrows("times rejects zero denominator", ArithmeticException.class, () -> Money.ofMinor(1, USD).times(1, 0, Rounding.DOWN));

        // allocation conserves every minor unit
        Money[] thirds = Money.parse("100.00", USD).allocate(1, 1, 1);
        check("allocate thirds sums to whole", Money.sum(USD, thirds).equals(Money.parse("100.00", USD)));
        check("allocate thirds: first gets the extra cent", thirds[0].minorUnits() == 3334 && thirds[1].minorUnits() == 3333 && thirds[2].minorUnits() == 3333);
        Money[] split = Money.parse("0.05", USD).allocate(70, 30);
        check("allocate 5c 70/30 -> 4c + 1c", split[0].minorUnits() == 4 && split[1].minorUnits() == 1);
        Money[] neg = Money.parse("-1.00", USD).allocate(1, 1, 1);
        check("allocate negative sums to whole", Money.sum(USD, neg).minorUnits() == -100);
        Money[] skew = Money.ofMinor(1, USD).allocate(1, 1, 1);
        check("allocate one cent three ways gives it to one share", skew[0].minorUnits() == 1 && skew[1].isZero() && skew[2].isZero());
        checkThrows("allocate rejects all-zero ratios", IllegalArgumentException.class, () -> Money.ofMinor(1, USD).allocate(0, 0));
        checkThrows("allocate rejects negative ratios", IllegalArgumentException.class, () -> Money.ofMinor(1, USD).allocate(1, -1));

        // currency validation
        checkThrows("currency code must be uppercase", IllegalArgumentException.class, () -> new Currency("usd", 2));
        checkThrows("currency exponent bounded", IllegalArgumentException.class, () -> new Currency("ABC", 7));
        check("minorUnitsPerMajor KWD", Currency.KWD.minorUnitsPerMajor() == 1000);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
