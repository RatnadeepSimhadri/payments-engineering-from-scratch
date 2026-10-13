// Tests for phases/10-money-and-ledgers/10-fees-and-splits. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Random;

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

    static final Currency EUR = Currency.EUR;

    static Money eur(String s) { return Money.parse(s, EUR); }

    static SplitPolicy policy() {
        return new SplitPolicy(
                FeeSchedule.percentPlusFixed("c", 1_200, eur("0.30")).withBounds(eur("0.50"), eur("25.00")),
                1_900, 2_000, Map.of("seller-B", "partner-P"),
                FeeSchedule.percentPlusFixed("p", 140, eur("0.25")));
    }

    public static void main(String[] args) {
        // fee schedules
        FeeSchedule c = policy().commission();
        check("percent + fixed (50.00 -> 6.30)", c.feeFor(eur("50.00")).equals(eur("6.30")));
        check("minimum applies (1.00 -> 0.50)", c.feeFor(eur("1.00")).equals(eur("0.50")));
        check("maximum applies (300.00 -> 25.00)", c.feeFor(eur("300.00")).equals(eur("25.00")));
        check("fee never exceeds the amount (0.40 -> 0.40)", c.feeFor(eur("0.40")).equals(eur("0.40")));
        check("fee on zero is zero", c.feeFor(eur("0.00")).isZero());
        check("percent rounds half even (0.125 -> 0.12 + 0.30)", c.feeFor(eur("1.04")).equals(eur("0.50"))
                && FeeSchedule.percentPlusFixed("x", 1_250, eur("0.00")).feeFor(eur("1.00")).equals(eur("0.12")));
        checkThrows("min above max rejected", IllegalArgumentException.class,
                () -> FeeSchedule.percentPlusFixed("x", 100, eur("0.00")).withBounds(eur("2.00"), eur("1.00")));
        checkThrows("bps above 100% rejected", IllegalArgumentException.class, () -> FeeSchedule.percentPlusFixed("x", 10_001, eur("0.00")));
        checkThrows("negative fee base rejected", IllegalArgumentException.class, () -> c.feeFor(eur("-1.00")));

        // the worked order
        Order order = new Order("o-1", List.of(
                new LineItem("lamp", "seller-A", eur("59.99")),
                new LineItem("bulb", "seller-A", eur("4.99")),
                new LineItem("rug", "seller-B", eur("119.00"))), eur("10.00"));
        OrderSplit s = new Splitter(policy()).split(order);
        check("discount shares sum to discount", s.total(LineSplit::discountShare).equals(eur("10.00")));
        check("discount allocated by price (3.26, 0.27, 6.47)", s.lines().get(0).discountShare().equals(eur("3.26"))
                && s.lines().get(1).discountShare().equals(eur("0.27")) && s.lines().get(2).discountShare().equals(eur("6.47")));
        check("every line's parts sum to its charge", s.lines().stream().allMatch(l -> l.sumOfParts().equals(l.charged())));
        check("lamp VAT 9.06, platform 6.02, seller 41.65", s.lines().get(0).tax().equals(eur("9.06"))
                && s.lines().get(0).platformFee().equals(eur("6.02")) && s.lines().get(0).sellerNet().equals(eur("41.65")));
        check("rug commission split 80/20 (9.32 + 2.33)", s.lines().get(2).platformFee().equals(eur("9.32")) && s.lines().get(2).referralFee().equals(eur("2.33")));
        check("no referral for seller without a referrer", s.lines().get(0).referralFee().isZero() && s.lines().get(0).referrer() == null);
        check("seller totals aggregate by seller", s.sellerNets().equals(Map.of("seller-A", eur("44.84"), "seller-B", eur("82.91"))));
        Money parts = s.total(LineSplit::tax).plus(s.total(LineSplit::platformFee)).plus(s.total(LineSplit::referralFee)).plus(s.total(LineSplit::sellerNet));
        check("order parts sum to order charged", parts.equals(order.charged()) && order.charged().equals(eur("173.98")));
        check("processor fee 1.4% + 0.25 on 173.98 = 2.69", s.processorFee().equals(eur("2.69")));

        // ledger
        Ledger ledger = new Ledger();
        ledger.post(Ledger.captureEntry(s));
        check("capture entry balances", ledger.trialBalanceNet() == 0);
        check("receivable net of processor fee", ledger.netDebit("1200 Receivable") == 17_398 - 269);
        check("VAT payable is the sum of line VAT", ledger.netDebit("2300 VAT Payable") == -2_778);

        // payouts
        PayoutPolicy pp = new PayoutPolicy(1_000, FeeSchedule.percentPlusFixed("payout", 0, eur("0.25")), eur("20.00"));
        Payout a = PayoutCalculator.compute("seller-A", eur("44.84"), pp);
        check("reserve rounds up (4.484 -> 4.49)", a.reserve().equals(eur("4.49")));
        check("payout parts sum to available", a.reserve().plus(a.fee()).plus(a.net()).plus(a.carriedOver()).equals(a.available()));
        check("payout net 40.10", a.net().equals(eur("40.10")) && a.paid());
        Payout small = PayoutCalculator.compute("seller-C", eur("12.40"), pp);
        check("below minimum carries over everything", !small.paid() && small.carriedOver().equals(eur("12.40")) && small.reserve().isZero());
        Payout negative = PayoutCalculator.compute("seller-D", eur("-5.00"), pp);
        check("negative balance is never paid out", !negative.paid() && negative.carriedOver().equals(eur("-5.00")));
        ledger.post(Ledger.payoutEntry(a));
        check("payout entry balances and empties payable", ledger.trialBalanceNet() == 0 && ledger.netDebit("2100 Seller Payable seller-A") == 0);
        check("reserve moved to reserve account", ledger.netDebit("2150 Seller Reserve seller-A") == -449);

        // boundaries and errors
        checkThrows("discount above subtotal rejected", IllegalArgumentException.class,
                () -> new Splitter(policy()).split(new Order("o", List.of(new LineItem("x", "seller-A", eur("1.00"))), eur("2.00"))));
        OrderSplit free = new Splitter(policy()).split(new Order("o-free", List.of(new LineItem("x", "seller-A", eur("5.00"))), eur("5.00")));
        check("fully discounted line: every part zero", free.lines().get(0).sumOfParts().isZero() && free.lines().get(0).sellerNet().isZero());
        OrderSplit tiny = new Splitter(policy()).split(new Order("o-tiny", List.of(new LineItem("x", "seller-B", eur("0.01"))), eur("0.00")));
        check("one-cent line still sums", tiny.lines().get(0).sumOfParts().equals(eur("0.01")) && !tiny.lines().get(0).sellerNet().isNegative());
        checkThrows("zero-price line rejected", IllegalArgumentException.class,
                () -> new Splitter(policy()).split(new Order("o", List.of(new LineItem("x", "seller-A", eur("0.00"))), eur("0.00"))));

        // property: random orders always conserve, never produce negative parts, fee within bounds
        Random r = new Random(23);
        boolean conserve = true, nonNegative = true, bounded = true, balanced = true;
        for (int i = 0; i < 2_000; i++) {
            int n = 1 + r.nextInt(5);
            List<LineItem> lines = new ArrayList<>();
            long subtotal = 0;
            for (int k = 0; k < n; k++) {
                long price = 1 + r.nextInt(r.nextBoolean() ? 500 : 50_000);
                subtotal += price;
                lines.add(new LineItem("s" + k, r.nextBoolean() ? "seller-A" : "seller-B", Money.ofMinor(price, EUR)));
            }
            Money discount = Money.ofMinor(r.nextInt(4) == 0 ? r.nextLong(subtotal + 1) : 0, EUR);
            OrderSplit os = new Splitter(policy()).split(new Order("o" + i, lines, discount));
            Money total = os.total(LineSplit::tax).plus(os.total(LineSplit::platformFee)).plus(os.total(LineSplit::referralFee)).plus(os.total(LineSplit::sellerNet));
            conserve &= total.equals(os.order().charged()) && os.total(LineSplit::discountShare).equals(discount);
            for (LineSplit l : os.lines()) {
                nonNegative &= !l.tax().isNegative() && !l.platformFee().isNegative() && !l.referralFee().isNegative() && !l.sellerNet().isNegative();
                Money commission = l.platformFee().plus(l.referralFee());
                bounded &= commission.compareTo(eur("25.00")) <= 0;
            }
            if (os.order().charged().isPositive()) {
                Ledger lg = new Ledger();
                lg.post(Ledger.captureEntry(os));
                balanced &= lg.trialBalanceNet() == 0;
            }
        }
        check("property: 2,000 random orders conserve every cent", conserve);
        check("property: no part is ever negative", nonNegative);
        check("property: commission never exceeds the maximum", bounded);
        check("property: every capture entry balances", balanced);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
