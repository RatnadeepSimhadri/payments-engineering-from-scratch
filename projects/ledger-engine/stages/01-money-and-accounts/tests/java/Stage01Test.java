// Stage 1 tests for projects/ledger-engine: Money is present, account types know their normal side,
// the registry rejects duplicates and unknown ids. Run by scripts/project_test.py; exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage01Test {
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    public static void main(String[] args) {
        // Money: parse exactly, never from a float
        check("money parse", Money.parse("12.34", Currency.USD).minorUnits() == 1234);
        check("money parse pads", Money.parse("12", Currency.USD).minorUnits() == 1200);
        check("money parse .99", Money.parse(".99", Currency.USD).minorUnits() == 99);
        check("money parse negative", Money.parse("-0.07", Currency.USD).minorUnits() == -7);
        check("money parse JPY", Money.parse("1200", Currency.JPY).minorUnits() == 1200);
        check("money parse KWD", Money.parse("1.250", Currency.KWD).minorUnits() == 1250);
        checkThrows("money parse excess precision", ArithmeticException.class, () -> Money.parse("1.005", Currency.USD));
        checkThrows("money parse garbage", NumberFormatException.class, () -> Money.parse("12,34", Currency.USD));
        check("money toString round-trips", Money.parse("12.05", Currency.USD).toString().equals("12.05 USD"));

        // Money: arithmetic stays in the currency and in integers
        check("money plus", Money.parse("1.10", Currency.USD).plus(Money.parse("2.20", Currency.USD)).equals(Money.parse("3.30", Currency.USD)));
        check("money minus goes negative", Money.parse("1.00", Currency.USD).minus(Money.parse("2.50", Currency.USD)).equals(Money.parse("-1.50", Currency.USD)));
        checkThrows("money plus currency mismatch", CurrencyMismatchException.class,
                () -> Money.parse("1.00", Currency.USD).plus(Money.parse("1.00", Currency.EUR)));
        checkThrows("money minus currency mismatch", CurrencyMismatchException.class,
                () -> Money.parse("1.00", Currency.USD).minus(Money.parse("1.00", Currency.GBP)));
        checkThrows("money plus overflow", ArithmeticException.class,
                () -> Money.ofMinor(Long.MAX_VALUE, Currency.USD).plus(Money.ofMinor(1, Currency.USD)));
        check("money sum of nothing is zero", Money.sum(Currency.USD).isZero());

        // Money: times rounds exactly once, the way you asked
        Money hundred = Money.parse("100.00", Currency.USD);
        check("2.9% fee of 100.00", hundred.times(29, 1000, Rounding.HALF_EVEN).minorUnits() == 290);
        Money tenPointOhFive = Money.parse("10.05", Currency.USD);
        check("half-even tie goes to even (10.05 * 50%)", tenPointOhFive.times(1, 2, Rounding.HALF_EVEN).minorUnits() == 502);
        check("half-up tie goes up (10.05 * 50%)", tenPointOhFive.times(1, 2, Rounding.HALF_UP).minorUnits() == 503);
        check("down truncates", tenPointOhFive.times(1, 2, Rounding.DOWN).minorUnits() == 502);
        check("up ceils", Money.parse("10.01", Currency.USD).times(1, 2, Rounding.UP).minorUnits() == 501);
        check("negative half-up rounds away from zero", Money.parse("-10.05", Currency.USD).times(1, 2, Rounding.HALF_UP).minorUnits() == -503);
        check("negative down truncates toward zero", Money.parse("-10.05", Currency.USD).times(1, 2, Rounding.DOWN).minorUnits() == -502);
        checkThrows("times rejects zero denominator", ArithmeticException.class, () -> hundred.times(1, 0, Rounding.HALF_EVEN));
        check("divideRounded 7/2 half-even is 4", Money.divideRounded(7, 2, Rounding.HALF_EVEN) == 4);
        check("divideRounded 5/2 half-even is 2", Money.divideRounded(5, 2, Rounding.HALF_EVEN) == 2);
        check("divideRounded -7/2 half-up is -4", Money.divideRounded(-7, 2, Rounding.HALF_UP) == -4);

        // Money: allocate never loses or invents a minor unit
        Money[] thirds = Money.parse("100.00", Currency.USD).allocate(1, 1, 1);
        check("allocate thirds sums to whole", thirds[0].plus(thirds[1]).plus(thirds[2]).equals(hundred));
        check("allocate thirds gives 33.34/33.33/33.33", thirds[0].minorUnits() == 3334 && thirds[1].minorUnits() == 3333 && thirds[2].minorUnits() == 3333);
        Money[] split = Money.parse("0.05", Currency.USD).allocate(70, 30);
        check("allocate 5 cents 70/30 is 4/1", split[0].minorUnits() == 4 && split[1].minorUnits() == 1);
        Money[] negative = Money.parse("-1.00", Currency.USD).allocate(1, 2);
        check("allocate negative keeps sign and sums", negative[0].minorUnits() == -33 && negative[1].minorUnits() == -67);
        checkThrows("allocate rejects no ratios", IllegalArgumentException.class, () -> hundred.allocate());
        checkThrows("allocate rejects all-zero ratios", IllegalArgumentException.class, () -> hundred.allocate(0, 0));
        checkThrows("allocate rejects negative ratio", IllegalArgumentException.class, () -> hundred.allocate(1, -1));

        // normal balance side per account class
        check("ASSET grows on DEBIT", AccountType.ASSET.normalSide() == Side.DEBIT);
        check("EXPENSE grows on DEBIT", AccountType.EXPENSE.normalSide() == Side.DEBIT);
        check("LIABILITY grows on CREDIT", AccountType.LIABILITY.normalSide() == Side.CREDIT);
        check("EQUITY grows on CREDIT", AccountType.EQUITY.normalSide() == Side.CREDIT);
        check("REVENUE grows on CREDIT", AccountType.REVENUE.normalSide() == Side.CREDIT);
        check("DEBIT opposite is CREDIT", Side.DEBIT.opposite() == Side.CREDIT);
        check("CREDIT opposite is DEBIT", Side.CREDIT.opposite() == Side.DEBIT);

        // the account value
        Account bank = new Account("bank", AccountType.ASSET, Currency.USD);
        check("account keeps id", bank.id().equals("bank"));
        check("account keeps type", bank.type() == AccountType.ASSET);
        check("account keeps currency", bank.currency().equals(Currency.USD));
        check("account normal side delegates to type", bank.normalSide() == Side.DEBIT);
        check("liability account normal side", new Account("sellers", AccountType.LIABILITY, Currency.USD).normalSide() == Side.CREDIT);
        check("accounts are values", bank.equals(new Account("bank", AccountType.ASSET, Currency.USD)));
        checkThrows("blank account id rejected", IllegalArgumentException.class, () -> new Account("  ", AccountType.ASSET, Currency.USD));
        checkThrows("null account id rejected", IllegalArgumentException.class, () -> new Account(null, AccountType.ASSET, Currency.USD));

        // the registry
        AccountRegistry chart = new AccountRegistry();
        check("empty registry has size 0", chart.size() == 0);
        check("empty registry contains nothing", !chart.contains("bank"));
        Account opened = chart.open("bank", AccountType.ASSET, Currency.USD);
        check("open returns the account", opened.equals(bank));
        check("open makes it visible", chart.contains("bank"));
        check("get returns the same account", chart.get("bank").equals(bank));
        chart.open("sellers", AccountType.LIABILITY, Currency.USD);
        chart.open("eur-bank", AccountType.ASSET, Currency.EUR);
        check("size counts opened accounts", chart.size() == 3);
        checkThrows("duplicate id rejected", DuplicateAccountException.class, () -> chart.open("bank", AccountType.ASSET, Currency.USD));
        checkThrows("duplicate id rejected even with different type", DuplicateAccountException.class,
                () -> chart.open("bank", AccountType.LIABILITY, Currency.EUR));
        check("failed open does not change size", chart.size() == 3);
        check("failed open does not change the original", chart.get("bank").type() == AccountType.ASSET);
        checkThrows("unknown id rejected", UnknownAccountException.class, () -> chart.get("nope"));
        check("duplicate is a LedgerException", LedgerException.class.isAssignableFrom(DuplicateAccountException.class));
        check("unknown is a LedgerException", LedgerException.class.isAssignableFrom(UnknownAccountException.class));
        List<Account> all = chart.all();
        check("all() keeps opening order", all.get(0).id().equals("bank") && all.get(1).id().equals("sellers") && all.get(2).id().equals("eur-bank"));
        checkThrows("all() is a read-only view", UnsupportedOperationException.class,
                () -> chart.all().add(new Account("x", AccountType.ASSET, Currency.USD)));
        check("all() is a copy", chart.size() == 3 && chart.all().size() == 3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
