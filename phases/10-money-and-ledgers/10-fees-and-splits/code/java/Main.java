// Lesson: phases/10-money-and-ledgers/10-fees-and-splits/docs/en.md
// Fees, Splits, and Platform Payments
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: largest-remainder apportionment (Hamilton); EU VAT Directive 2006/112/EC (tax-inclusive prices);
// Money and allocate from Lesson 01, ledger shape from Lesson 03. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
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

    /** Largest-remainder split (Lesson 01): the parts always sum exactly to the whole; ties go to the lower index. */
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
        long leftover = magnitude - distributed;
        Integer[] order = new Integer[ratios.length];
        for (int i = 0; i < order.length; i++) order[i] = i;
        Arrays.sort(order, (a, b) -> Long.compare(remainders[b], remainders[a]));
        for (int k = 0; k < leftover; k++) shares[order[k]]++;
        Money[] result = new Money[ratios.length];
        for (int i = 0; i < ratios.length; i++) result[i] = new Money(minorUnits < 0 ? -shares[i] : shares[i], currency);
        return result;
    }

    static Money sum(Currency currency, Money... amounts) {
        Money acc = zero(currency);
        for (Money m : amounts) acc = acc.plus(m);
        return acc;
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
// Fee schedules, the split of every line into parts that sum to it, and payouts.
// ---------------------------------------------------------------------------------------------

/**
 * percent + fixed, clamped to [minimum, maximum], and never more than the amount it is charged on.
 * Percent is in basis points (1 bp = 0.01%), an exact rational bps/10000.
 */
record FeeSchedule(String name, long bps, Money fixed, Money minimum, Money maximum, Rounding rounding) {
    FeeSchedule {
        Objects.requireNonNull(fixed, "fixed");
        if (bps < 0 || bps > 10_000) throw new IllegalArgumentException("bps must be 0..10000: " + bps);
        if (fixed.isNegative()) throw new IllegalArgumentException("fixed fee must not be negative");
        if (minimum != null && maximum != null && minimum.compareTo(maximum) > 0) {
            throw new IllegalArgumentException("minimum " + minimum + " exceeds maximum " + maximum);
        }
    }

    static FeeSchedule percentPlusFixed(String name, long bps, Money fixed) {
        return new FeeSchedule(name, bps, fixed, null, null, Rounding.HALF_EVEN);
    }

    FeeSchedule withBounds(Money min, Money max) { return new FeeSchedule(name, bps, fixed, min, max, rounding); }

    Money feeFor(Money amount) {
        if (amount.isNegative()) throw new IllegalArgumentException("fee base must not be negative: " + amount);
        Money fee = amount.times(bps, 10_000, rounding).plus(fixed);
        if (minimum != null && fee.compareTo(minimum) < 0) fee = minimum;
        if (maximum != null && fee.compareTo(maximum) > 0) fee = maximum;
        if (fee.compareTo(amount) > 0) fee = amount;     // a fee never exceeds what it is charged on
        return fee;
    }
}

record LineItem(String sku, String sellerId, Money price) {}

record Order(String id, List<LineItem> lines, Money discount) {
    Order {
        lines = List.copyOf(lines);
        if (lines.isEmpty()) throw new IllegalArgumentException("order needs at least one line");
        if (discount.isNegative()) throw new IllegalArgumentException("discount must not be negative");
    }

    Money subtotal() {
        Money acc = Money.zero(discount.currency());
        for (LineItem l : lines) acc = acc.plus(l.price());
        return acc;
    }

    Money charged() { return subtotal().minus(discount); }
}

/**
 * Prices are tax-inclusive (VAT-style). The platform commission is charged on the amount net of tax.
 * A referral partner, if a seller has one, receives referralShareBps of the commission.
 */
record SplitPolicy(FeeSchedule commission, long taxBps, long referralShareBps, Map<String, String> referrerBySeller, FeeSchedule processorFee) {
    SplitPolicy {
        referrerBySeller = Map.copyOf(referrerBySeller);
        if (taxBps < 0 || referralShareBps < 0 || referralShareBps > 10_000) throw new IllegalArgumentException("bad bps");
    }
}

/** One line's money, in parts. Invariant: tax + platformFee + referralFee + sellerNet == charged. */
record LineSplit(LineItem line, Money discountShare, Money charged, Money tax, Money platformFee, Money referralFee,
                 String referrer, Money sellerNet) {
    Money sumOfParts() { return tax.plus(platformFee).plus(referralFee).plus(sellerNet); }
}

record OrderSplit(Order order, List<LineSplit> lines, Money processorFee) {
    Money total(java.util.function.Function<LineSplit, Money> part) {
        Money acc = Money.zero(order.discount().currency());
        for (LineSplit l : lines) acc = acc.plus(part.apply(l));
        return acc;
    }

    Map<String, Money> sellerNets() {
        Map<String, Money> out = new LinkedHashMap<>();
        for (LineSplit l : lines) out.merge(l.line().sellerId(), l.sellerNet(), Money::plus);
        return out;
    }

    Map<String, Money> referralFees() {
        Map<String, Money> out = new LinkedHashMap<>();
        for (LineSplit l : lines) if (l.referrer() != null && !l.referralFee().isZero()) out.merge(l.referrer(), l.referralFee(), Money::plus);
        return out;
    }
}

final class Splitter {
    private final SplitPolicy policy;

    Splitter(SplitPolicy policy) { this.policy = policy; }

    /**
     * 1. Allocate the order discount across lines in proportion to price.
     * 2. Per line: allocate charged into [net of tax, tax] by ratio 10000 : taxBps.
     * 3. Commission = schedule(net of tax). Allocate it into [platform, referral] if the seller has a referrer.
     * 4. Seller net = net of tax - commission: the remainder, so the parts sum to the line exactly.
     */
    OrderSplit split(Order order) {
        Currency cur = order.discount().currency();
        if (order.discount().compareTo(order.subtotal()) > 0) throw new IllegalArgumentException("discount exceeds subtotal");
        long[] weights = new long[order.lines().size()];
        for (int i = 0; i < weights.length; i++) {
            Money price = order.lines().get(i).price();
            if (!price.isPositive()) throw new IllegalArgumentException("line price must be positive: " + price);
            weights[i] = price.minorUnits();
        }
        Money[] discountShares = order.discount().isZero() ? zeros(cur, weights.length) : order.discount().allocate(weights);
        List<LineSplit> out = new ArrayList<>();
        for (int i = 0; i < weights.length; i++) {
            LineItem line = order.lines().get(i);
            Money charged = line.price().minus(discountShares[i]);
            Money[] taxParts = charged.isZero() ? zeros(cur, 2) : charged.allocate(10_000, policy.taxBps());
            Money netOfTax = taxParts[0], tax = taxParts[1];
            Money commission = policy.commission().feeFor(netOfTax);
            String referrer = policy.referrerBySeller().get(line.sellerId());
            Money platform = commission, referral = Money.zero(cur);
            if (referrer != null && !commission.isZero()) {
                Money[] parts = commission.allocate(10_000 - policy.referralShareBps(), policy.referralShareBps());
                platform = parts[0];
                referral = parts[1];
            }
            Money sellerNet = netOfTax.minus(commission);
            out.add(new LineSplit(line, discountShares[i], charged, tax, platform, referral, referrer, sellerNet));
        }
        Money processorFee = policy.processorFee().feeFor(order.charged());
        return new OrderSplit(order, out, processorFee);
    }

    private static Money[] zeros(Currency c, int n) {
        Money[] z = new Money[n];
        Arrays.fill(z, Money.zero(c));
        return z;
    }
}

/** Payout policy: hold a rolling reserve, charge a payout fee, and skip payouts below a minimum (carry them over). */
record PayoutPolicy(long reserveBps, FeeSchedule payoutFee, Money minimumPayout) {}

/** available == reserve + fee + net + carriedOver, always. */
record Payout(String sellerId, Money available, Money reserve, Money fee, Money net, Money carriedOver) {
    boolean paid() { return net.isPositive(); }
}

final class PayoutCalculator {
    static Payout compute(String sellerId, Money available, PayoutPolicy policy) {
        Currency cur = available.currency();
        Money zero = Money.zero(cur);
        if (!available.isPositive()) return new Payout(sellerId, available, zero, zero, zero, available);
        Money reserve = available.times(policy.reserveBps(), 10_000, Rounding.UP);   // round the hold in the platform's favour
        Money afterReserve = available.minus(reserve);
        Money fee = policy.payoutFee().feeFor(afterReserve);
        Money net = afterReserve.minus(fee);
        if (net.compareTo(policy.minimumPayout()) < 0) {
            return new Payout(sellerId, available, zero, zero, zero, available);    // nothing moves; try again next cycle
        }
        return new Payout(sellerId, available, reserve, fee, net, zero);
    }
}

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger (Lesson 03) to show that the split posts as one balanced entry.
// ---------------------------------------------------------------------------------------------

enum Side { DEBIT, CREDIT }

record Posting(String accountId, Side side, Money amount) {
    Posting {
        if (!amount.isPositive()) throw new IllegalArgumentException("posting amount must be positive, got " + amount);
    }

    static Posting debit(String accountId, Money amount) { return new Posting(accountId, Side.DEBIT, amount); }
    static Posting credit(String accountId, Money amount) { return new Posting(accountId, Side.CREDIT, amount); }

    long signedMinor() { return side == Side.DEBIT ? amount.minorUnits() : -amount.minorUnits(); }
}

record JournalEntry(String id, List<Posting> postings) {
    JournalEntry {
        postings = List.copyOf(postings);
        long net = 0;
        for (Posting p : postings) net = Math.addExact(net, p.signedMinor());
        if (postings.size() < 2 || net != 0) throw new IllegalArgumentException("entry " + id + " unbalanced by " + net);
    }
}

final class Ledger {
    private final Map<String, Long> netDebits = new LinkedHashMap<>();
    private final List<JournalEntry> journal = new ArrayList<>();

    void post(JournalEntry e) {
        journal.add(e);
        for (Posting p : e.postings()) netDebits.merge(p.accountId(), p.signedMinor(), Math::addExact);
    }

    long netDebit(String accountId) { return netDebits.getOrDefault(accountId, 0L); }

    long trialBalanceNet() { return netDebits.values().stream().mapToLong(Long::longValue).sum(); }

    Map<String, Long> netDebits() { return netDebits; }

    /** Build the capture entry for a split: one debit to the receivable, one credit per party, nothing dropped. */
    static JournalEntry captureEntry(OrderSplit s) {
        List<Posting> ps = new ArrayList<>();
        ps.add(Posting.debit("1200 Receivable", s.order().charged()));
        addCredit(ps, "2300 VAT Payable", s.total(LineSplit::tax));
        addCredit(ps, "4100 Commission Revenue", s.total(LineSplit::platformFee));
        s.referralFees().forEach((partner, fee) -> addCredit(ps, "2400 Referral Payable " + partner, fee));
        s.sellerNets().forEach((seller, net) -> addCredit(ps, "2100 Seller Payable " + seller, net));
        if (s.processorFee().isPositive()) {
            ps.add(Posting.debit("5100 Processing Expense", s.processorFee()));
            ps.add(Posting.credit("1200 Receivable", s.processorFee()));
        }
        return new JournalEntry("cap-" + s.order().id(), ps);
    }

    static JournalEntry payoutEntry(Payout p) {
        List<Posting> ps = new ArrayList<>();
        String payable = "2100 Seller Payable " + p.sellerId();
        ps.add(Posting.debit(payable, p.reserve().plus(p.fee()).plus(p.net())));
        addCredit(ps, "2150 Seller Reserve " + p.sellerId(), p.reserve());
        addCredit(ps, "4200 Payout Fee Revenue", p.fee());
        addCredit(ps, "1010 Bank", p.net());
        return new JournalEntry("out-" + p.sellerId(), ps);
    }

    private static void addCredit(List<Posting> ps, String account, Money amount) {
        if (amount.isPositive()) ps.add(Posting.credit(account, amount));
    }
}

public final class Main {
    public static void main(String[] args) {
        Currency EUR = Currency.EUR;
        Money minFee = Money.parse("0.50", EUR), maxFee = Money.parse("25.00", EUR);
        SplitPolicy policy = new SplitPolicy(
                FeeSchedule.percentPlusFixed("commission 12% + 0.30", 1_200, Money.parse("0.30", EUR)).withBounds(minFee, maxFee),
                1_900,                                                  // German VAT 19%, included in prices
                2_000,                                                  // referral partner gets 20% of commission
                Map.of("seller-B", "partner-P"),
                FeeSchedule.percentPlusFixed("processor 1.4% + 0.25", 140, Money.parse("0.25", EUR)));

        System.out.println("== Fee schedule: 12% + 0.30, min 0.50, max 25.00 ==");
        for (String amount : List.of("1.00", "2.00", "50.00", "300.00", "0.40")) {
            Money a = Money.parse(amount, EUR);
            System.out.printf("  fee on %-11s = %s%n", a, policy.commission().feeFor(a));
        }

        System.out.println("\n== Order o-1: three lines, two sellers, 10.00 coupon, prices include 19% VAT ==");
        Order order = new Order("o-1", List.of(
                new LineItem("lamp", "seller-A", Money.parse("59.99", EUR)),
                new LineItem("bulb", "seller-A", Money.parse("4.99", EUR)),
                new LineItem("rug", "seller-B", Money.parse("119.00", EUR))), Money.parse("10.00", EUR));
        OrderSplit split = new Splitter(policy).split(order);
        System.out.printf("  %-5s %-9s %8s %9s %9s %8s %9s %9s %10s %6s%n", "sku", "seller", "price", "discount", "charged", "VAT", "platform", "referral", "seller net", "sums");
        for (LineSplit l : split.lines()) {
            System.out.printf("  %-5s %-9s %8s %9s %9s %8s %9s %9s %10s %6s%n", l.line().sku(), l.line().sellerId(),
                    amt(l.line().price()), amt(l.discountShare()), amt(l.charged()), amt(l.tax()), amt(l.platformFee()),
                    amt(l.referralFee()), amt(l.sellerNet()), l.sumOfParts().equals(l.charged()));
        }
        System.out.println("  order charged " + order.charged() + "; VAT " + split.total(LineSplit::tax) + "; platform " + split.total(LineSplit::platformFee)
                + "; referral " + split.referralFees() + "; sellers " + split.sellerNets());
        System.out.println("  processor fee on the charge: " + split.processorFee());

        System.out.println("\n== Posted as one balanced entry ==");
        Ledger ledger = new Ledger();
        JournalEntry cap = Ledger.captureEntry(split);
        ledger.post(cap);
        for (Posting p : cap.postings()) System.out.println((p.side() == Side.DEBIT ? "  Dr " : "      Cr ") + p.accountId() + "  " + p.amount());
        System.out.println("  trial balance nets to " + ledger.trialBalanceNet());

        System.out.println("\n== Payouts: 10% rolling reserve (rounded up), payout fee 0.25, minimum payout 20.00 ==");
        PayoutPolicy payoutPolicy = new PayoutPolicy(1_000, FeeSchedule.percentPlusFixed("payout", 0, Money.parse("0.25", EUR)), Money.parse("20.00", EUR));
        for (String seller : split.sellerNets().keySet()) {
            Money available = Money.ofMinor(-ledger.netDebit("2100 Seller Payable " + seller), EUR);
            Payout p = PayoutCalculator.compute(seller, available, payoutPolicy);
            System.out.println("  " + seller + ": available " + p.available() + " -> reserve " + p.reserve() + ", fee " + p.fee()
                    + ", to bank " + p.net() + ", carried " + p.carriedOver());
            if (p.paid()) ledger.post(Ledger.payoutEntry(p));
        }
        Payout small = PayoutCalculator.compute("seller-C", Money.parse("12.40", EUR), payoutPolicy);
        System.out.println("  seller-C: available " + small.available() + " -> below minimum, carried " + small.carriedOver());
        System.out.println("  trial balance after payouts nets to " + ledger.trialBalanceNet());

        System.out.println("\n== Why allocate, not round each part: 0.10 split three ways ==");
        Money dime = Money.parse("0.10", EUR);
        Money naive = dime.times(1, 3, Rounding.HALF_EVEN);
        System.out.println("  rounding each third: " + naive + " x 3 = " + naive.times(3, 1, Rounding.HALF_EVEN) + " (a cent lost)");
        System.out.println("  allocate(1,1,1): " + Arrays.toString(dime.allocate(1, 1, 1)) + " sum " + Money.sum(EUR, dime.allocate(1, 1, 1)));
    }

    static String amt(Money m) {
        String s = m.toString();
        return s.substring(0, s.length() - 4);
    }
}
