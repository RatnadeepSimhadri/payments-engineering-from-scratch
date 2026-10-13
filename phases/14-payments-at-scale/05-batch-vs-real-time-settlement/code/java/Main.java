// Lesson: phases/14-payments-at-scale/05-batch-vs-real-time-settlement/docs/en.md
// Batch vs Real-Time Settlement Pipelines
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Nacha Operating Rules (batch/file control records, entry hash); ISO 20022 pain.001 (payment initiation).
// Java 21, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).

import java.time.Instant;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

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

/** Exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (frac.length() > currency.exponent()) throw new ArithmeticException("too many decimals: " + text);
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole.isEmpty() ? "0" : whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }

    boolean isPositive() { return minorUnits > 0; }

    boolean isNegative() { return minorUnits < 0; }

    Money times(long numerator, long denominator, Rounding rounding) {
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twice = Math.abs(remainder) * 2, abs = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twice >= abs ? quotient + sign : quotient;
            case HALF_EVEN -> twice > abs ? quotient + sign : twice < abs ? quotient : (quotient % 2 == 0 ? quotient : quotient + sign);
        };
    }

    @Override
    public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override
    public String toString() {
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
// Domain: merchants, settlement events, fees
// ---------------------------------------------------------------------------------------------

/** A merchant and the bank account its payouts go to. routing is a 9-digit ABA routing number. */
record Merchant(String id, String routing, String account, boolean onHold) {
    Merchant {
        if (routing.length() != 9 || !routing.chars().allMatch(Character::isDigit)) {
            throw new IllegalArgumentException("routing number must be 9 digits: " + routing);
        }
    }
}

enum EventType { CAPTURE, REFUND, CHARGEBACK }

/** One money-moving event that the acquirer will settle with the platform: amount is always positive. */
record SettlementEvent(String id, String merchantId, EventType type, Money amount, Instant at) {
    SettlementEvent {
        if (!amount.isPositive()) throw new IllegalArgumentException("event amounts are positive; the type gives the sign: " + id);
    }
}

/** Percentage fee (numerator/denominator) plus a fixed fee per capture, and a flat chargeback fee. */
record FeeSchedule(long percentNumerator, long percentDenominator, Money fixed, Money chargebackFee) {
    static FeeSchedule standard(Currency c) {
        return new FeeSchedule(29, 1000, Money.ofMinor(30, c), Money.ofMinor(1500, c)); // 2.9% + 0.30, 15.00
    }

    /** Fee the platform keeps on this event. Refunds return no fee (the common card-acquiring policy). */
    Money feeFor(SettlementEvent e) {
        return switch (e.type()) {
            case CAPTURE -> e.amount().times(percentNumerator, percentDenominator, Rounding.HALF_EVEN).plus(fixed);
            case REFUND -> Money.zero(e.amount().currency());
            case CHARGEBACK -> chargebackFee;
        };
    }

    /** Signed effect of the event on what the platform owes the merchant. */
    Money merchantEffect(SettlementEvent e) {
        return switch (e.type()) {
            case CAPTURE -> e.amount().minus(feeFor(e));
            case REFUND -> e.amount().negate();
            case CHARGEBACK -> e.amount().plus(chargebackFee).negate();
        };
    }

    /** Signed effect of the event on what the acquirer owes the platform. */
    static Money processorEffect(SettlementEvent e) {
        return e.type() == EventType.CAPTURE ? e.amount() : e.amount().negate();
    }
}

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger: debit positive, credit negative, every journal sums to zero.
// ---------------------------------------------------------------------------------------------

record Posting(String account, long minor) {}

final class UnbalancedJournalException extends IllegalStateException {
    UnbalancedJournalException(String id, long sum) { super("journal " + id + " does not balance: sum " + sum); }
}

final class Ledger {
    static final String PROCESSOR_RECEIVABLE = "processor_receivable";
    static final String FEE_REVENUE = "fee_revenue";
    static final String PAYOUTS_IN_TRANSIT = "payouts_in_transit";
    static final String CASH = "cash:settlement_bank";

    static String merchantPayable(String merchantId) { return "merchant_payable:" + merchantId; }

    final Currency currency;
    private final Map<String, Long> balances = new TreeMap<>();
    private final Map<String, List<Posting>> journals = new LinkedHashMap<>();
    private long minCash = 0;

    Ledger(Currency currency) { this.currency = currency; }

    /** Post a balanced journal once. Returns false (and changes nothing) if the id was already posted. */
    boolean post(String journalId, List<Posting> postings) {
        if (journals.containsKey(journalId)) return false;
        long sum = 0;
        for (Posting p : postings) sum = Math.addExact(sum, p.minor());
        if (sum != 0) throw new UnbalancedJournalException(journalId, sum);
        for (Posting p : postings) balances.merge(p.account(), p.minor(), Math::addExact);
        journals.put(journalId, List.copyOf(postings));
        minCash = Math.min(minCash, balances.getOrDefault(CASH, 0L));
        return true;
    }

    Money balance(String account) { return Money.ofMinor(balances.getOrDefault(account, 0L), currency); }

    /** What the platform owes the merchant: the credit balance of its payable account, as a positive number. */
    Money owedTo(String merchantId) { return balance(merchantPayable(merchantId)).negate(); }

    long trialBalance() {
        long sum = 0;
        for (long v : balances.values()) sum = Math.addExact(sum, v);
        return sum;
    }

    int journalCount() { return journals.size(); }

    /** The most negative the settlement bank account has been: how much the platform had to prefund. */
    Money peakPrefunding() { return Money.ofMinor(-minCash, currency); }

    Map<String, Long> balancesView() { return Collections.unmodifiableMap(balances); }
}

/** Books events into the ledger as they happen. Shared by both settlement styles. */
final class Platform {
    final Ledger ledger;
    final FeeSchedule fees;
    final Map<String, Merchant> merchants = new TreeMap<>();
    final List<SettlementEvent> events = new ArrayList<>();

    Platform(Currency currency, FeeSchedule fees) {
        this.ledger = new Ledger(currency);
        this.fees = fees;
    }

    void addMerchant(Merchant m) { merchants.put(m.id(), m); }

    /** Post the event's journal at the moment it happens. Idempotent on event id. */
    boolean book(SettlementEvent e) {
        if (!merchants.containsKey(e.merchantId())) throw new IllegalArgumentException("unknown merchant " + e.merchantId());
        if (!e.amount().currency().equals(ledger.currency)) throw new CurrencyMismatchException(ledger.currency, e.amount().currency());
        String payable = Ledger.merchantPayable(e.merchantId());
        long amount = e.amount().minorUnits();
        long fee = fees.feeFor(e).minorUnits();
        List<Posting> postings = switch (e.type()) {
            case CAPTURE -> List.of(
                    new Posting(Ledger.PROCESSOR_RECEIVABLE, amount),
                    new Posting(payable, -(amount - fee)),
                    new Posting(Ledger.FEE_REVENUE, -fee));
            case REFUND -> List.of(
                    new Posting(payable, amount),
                    new Posting(Ledger.PROCESSOR_RECEIVABLE, -amount));
            case CHARGEBACK -> List.of(
                    new Posting(payable, amount + fee),
                    new Posting(Ledger.PROCESSOR_RECEIVABLE, -amount),
                    new Posting(Ledger.FEE_REVENUE, -fee));
        };
        boolean fresh = ledger.post("evt:" + e.id(), postings);
        if (fresh) events.add(e);
        return fresh;
    }

    /** The acquirer pays what it owes for a set of events: cash in, receivable down. */
    void receiveFunding(String fundingId, Money amount) {
        ledger.post("fund:" + fundingId, List.of(
                new Posting(Ledger.CASH, amount.minorUnits()),
                new Posting(Ledger.PROCESSOR_RECEIVABLE, -amount.minorUnits())));
    }
}

// ---------------------------------------------------------------------------------------------
// Batch settlement: cutoff, per-merchant netting, payout instructions, settlement file.
// ---------------------------------------------------------------------------------------------

record PayoutInstruction(int sequence, String merchantId, String routing, String account, Money amount) {}

record SettlementBatch(String batchId, LocalDate businessDate, Instant cutoff, List<String> eventIds,
                       List<PayoutInstruction> instructions, Map<String, Money> carriedForward, Money processorFunding) {
    Money total() {
        Money sum = Money.zero(processorFunding.currency());
        for (PayoutInstruction p : instructions) sum = sum.plus(p.amount());
        return sum;
    }
}

final class BatchSettler {
    private final Platform platform;
    private final Money minimumPayout;
    private final Map<String, String> settledBy = new HashMap<>();      // event id -> batch id
    private final Map<String, Money> carry = new TreeMap<>();            // merchant -> carried net
    private final Map<String, SettlementBatch> batches = new LinkedHashMap<>();

    BatchSettler(Platform platform, Money minimumPayout) {
        this.platform = platform;
        this.minimumPayout = minimumPayout;
    }

    Money carried(String merchantId) { return carry.getOrDefault(merchantId, Money.zero(platform.ledger.currency)); }

    boolean isSettled(String eventId) { return settledBy.containsKey(eventId); }

    /**
     * Close the books for one business date. Every booked, unsettled event before the cutoff for a
     * merchant not on hold is netted into one amount per merchant. Positive nets at or above the
     * minimum become payout instructions; the rest are carried to the next batch. Running the same
     * business date twice returns the first batch unchanged: the batch id is the idempotency key.
     */
    SettlementBatch run(LocalDate businessDate, Instant cutoff) {
        String batchId = "BATCH-" + businessDate;
        SettlementBatch existing = batches.get(batchId);
        if (existing != null) return existing;

        Currency c = platform.ledger.currency;
        Map<String, Money> net = new TreeMap<>();
        Map<String, List<String>> included = new TreeMap<>();
        Money funding = Money.zero(c);
        for (SettlementEvent e : platform.events) {
            if (settledBy.containsKey(e.id()) || !e.at().isBefore(cutoff)) continue;
            Merchant m = platform.merchants.get(e.merchantId());
            if (m.onHold()) continue;
            net.merge(e.merchantId(), platform.fees.merchantEffect(e), Money::plus);
            included.computeIfAbsent(e.merchantId(), k -> new ArrayList<>()).add(e.id());
            funding = funding.plus(FeeSchedule.processorEffect(e));
        }
        for (Map.Entry<String, Money> entry : carry.entrySet()) {
            if (!platform.merchants.get(entry.getKey()).onHold()) net.merge(entry.getKey(), entry.getValue(), Money::plus);
        }

        List<PayoutInstruction> instructions = new ArrayList<>();
        Map<String, Money> carriedOut = new TreeMap<>();
        for (Map.Entry<String, Money> entry : net.entrySet()) {
            String merchantId = entry.getKey();
            Money amount = entry.getValue();
            Merchant m = platform.merchants.get(merchantId);
            if (amount.isPositive() && amount.compareTo(minimumPayout) >= 0) {
                PayoutInstruction p = new PayoutInstruction(instructions.size() + 1, merchantId, m.routing(), m.account(), amount);
                instructions.add(p);
                platform.ledger.post("payout:" + batchId + ":" + merchantId, List.of(
                        new Posting(Ledger.merchantPayable(merchantId), amount.minorUnits()),
                        new Posting(Ledger.PAYOUTS_IN_TRANSIT, -amount.minorUnits())));
                carry.remove(merchantId);
            } else if (amount.minorUnits() != 0) {
                carry.put(merchantId, amount);
                carriedOut.put(merchantId, amount);
            } else {
                carry.remove(merchantId);
            }
        }
        List<String> eventIds = new ArrayList<>();
        for (List<String> ids : included.values()) {
            for (String id : ids) { settledBy.put(id, batchId); eventIds.add(id); }
        }
        SettlementBatch batch = new SettlementBatch(batchId, businessDate, cutoff, List.copyOf(eventIds),
                List.copyOf(instructions), Collections.unmodifiableMap(carriedOut), funding);
        batches.put(batchId, batch);
        return batch;
    }

    /** The bank confirms the payout file was executed: in-transit clears against cash. */
    void confirmPayouts(SettlementBatch batch) {
        long total = batch.total().minorUnits();
        if (total == 0) return;
        platform.ledger.post("confirm:" + batch.batchId(), List.of(
                new Posting(Ledger.PAYOUTS_IN_TRANSIT, total),
                new Posting(Ledger.CASH, -total)));
    }

    /**
     * The tie-out: for every merchant, the ledger's payable balance must equal what is carried plus
     * the effect of events not yet settled. If this is false, a payout was lost or paid twice.
     */
    boolean tiesOut() {
        Map<String, Money> expected = new TreeMap<>();
        for (String id : platform.merchants.keySet()) expected.put(id, carried(id));
        for (SettlementEvent e : platform.events) {
            if (!settledBy.containsKey(e.id())) expected.merge(e.merchantId(), platform.fees.merchantEffect(e), Money::plus);
        }
        for (Map.Entry<String, Money> entry : expected.entrySet()) {
            if (!platform.ledger.owedTo(entry.getKey()).equals(entry.getValue())) return false;
        }
        return platform.ledger.trialBalance() == 0;
    }
}

/**
 * Renders a batch as a fixed-layout text file in the spirit of a Nacha file: a header, one detail
 * line per payout, and a control record with entry count, total, and entry hash (the sum of the first
 * eight digits of every routing number, modulo 10^10). The receiving bank recomputes all three.
 */
final class SettlementFile {
    static String render(SettlementBatch batch) {
        StringBuilder out = new StringBuilder();
        String currency = batch.processorFunding().currency().code();
        out.append(String.format("H|%s|%s|%s%n", batch.batchId(), batch.businessDate(), currency));
        long total = 0;
        for (PayoutInstruction p : batch.instructions()) {
            out.append(String.format("D|%06d|%s|%s|%s|%d%n", p.sequence(), p.merchantId(), p.routing(), p.account(), p.amount().minorUnits()));
            total = Math.addExact(total, p.amount().minorUnits());
        }
        out.append(String.format("T|%d|%d|%010d%n", batch.instructions().size(), total, entryHash(batch.instructions())));
        return out.toString();
    }

    static long entryHash(List<PayoutInstruction> instructions) {
        long hash = 0;
        for (PayoutInstruction p : instructions) hash += Long.parseLong(p.routing().substring(0, 8));
        return hash % 10_000_000_000L;
    }

    /** Receiver-side validation: recompute count, total and hash from the detail lines. */
    static boolean validate(String file) {
        long count = 0, total = 0, hash = 0;
        String[] trailer = null;
        for (String line : file.split("\n")) {
            String[] f = line.split("\\|");
            if (f[0].equals("D")) {
                count++;
                total = Math.addExact(total, Long.parseLong(f[5]));
                hash += Long.parseLong(f[3].substring(0, 8));
            } else if (f[0].equals("T")) {
                trailer = f;
            }
        }
        return trailer != null && Long.parseLong(trailer[1]) == count && Long.parseLong(trailer[2]) == total
                && Long.parseLong(trailer[3]) == hash % 10_000_000_000L;
    }
}

// ---------------------------------------------------------------------------------------------
// Real-time settlement: pay each merchant's positive balance the moment it appears.
// ---------------------------------------------------------------------------------------------

final class RealTimeSettler {
    private final Platform platform;
    private int transfers = 0;
    private Money grossPaid;

    RealTimeSettler(Platform platform) {
        this.platform = platform;
        this.grossPaid = Money.zero(platform.ledger.currency);
    }

    /** Book the event, then sweep the merchant's positive balance over an instant rail. */
    void onEvent(SettlementEvent e) {
        if (!platform.book(e)) return;
        Money owed = platform.ledger.owedTo(e.merchantId());
        if (!owed.isPositive()) return;
        transfers++;
        grossPaid = grossPaid.plus(owed);
        String id = "rt:" + e.id();
        platform.ledger.post(id, List.of(
                new Posting(Ledger.merchantPayable(e.merchantId()), owed.minorUnits()),
                new Posting(Ledger.CASH, -owed.minorUnits())));
    }

    int transfers() { return transfers; }

    Money grossPaid() { return grossPaid; }

    /** Sum of negative merchant balances: money already paid out that must be clawed back. */
    Money clawbackExposure() {
        Money exposure = Money.zero(platform.ledger.currency);
        for (String id : platform.merchants.keySet()) {
            Money owed = platform.ledger.owedTo(id);
            if (owed.isNegative()) exposure = exposure.minus(owed);
        }
        return exposure;
    }
}

/** Side-by-side numbers for the two pipelines over the same day of events. */
record LiquidityComparison(int batchTransfers, Money batchPaid, Money batchPeakPrefunding, Money batchCashAfter,
                           int realTimeTransfers, Money realTimePaid, Money realTimePeakPrefunding,
                           Money realTimeExposure, Money realTimeCashAfter) {

    static LiquidityComparison run(List<Merchant> merchants, List<SettlementEvent> day, LocalDate date, Instant cutoff, Currency c) {
        Platform batchPlatform = new Platform(c, FeeSchedule.standard(c));
        merchants.forEach(batchPlatform::addMerchant);
        BatchSettler settler = new BatchSettler(batchPlatform, Money.zero(c));
        for (SettlementEvent e : day) batchPlatform.book(e);
        SettlementBatch batch = settler.run(date, cutoff);
        batchPlatform.receiveFunding(batch.batchId(), batch.processorFunding()); // T+1 morning: acquirer pays first
        settler.confirmPayouts(batch);                                         // T+1 afternoon: payout file executes

        Platform rtPlatform = new Platform(c, FeeSchedule.standard(c));
        merchants.forEach(rtPlatform::addMerchant);
        RealTimeSettler rt = new RealTimeSettler(rtPlatform);
        Money rtFunding = Money.zero(c);
        for (SettlementEvent e : day) {
            if (!e.at().isBefore(cutoff)) continue; // same scope as the batch, so the comparison is fair
            rt.onEvent(e);
            rtFunding = rtFunding.plus(FeeSchedule.processorEffect(e));
        }
        Money exposure = rt.clawbackExposure();
        Money rtPeak = rtPlatform.ledger.peakPrefunding();
        rtPlatform.receiveFunding("T+1", rtFunding);

        return new LiquidityComparison(batch.instructions().size(), batch.total(), batchPlatform.ledger.peakPrefunding(),
                batchPlatform.ledger.balance(Ledger.CASH), rt.transfers(), rt.grossPaid(), rtPeak, exposure,
                rtPlatform.ledger.balance(Ledger.CASH));
    }
}

public final class Main {
    static final Currency USD = Currency.USD;
    static final LocalDate DAY = LocalDate.parse("2026-03-31");
    static final Instant CUTOFF = Instant.parse("2026-03-31T23:00:00Z");

    static Instant at(String hhmm) { return Instant.parse("2026-03-31T" + hhmm + ":00Z"); }

    static List<Merchant> demoMerchants() {
        return List.of(
                new Merchant("m_acme", "021000021", "100200300", false),
                new Merchant("m_bolt", "026009593", "555000111", false),
                new Merchant("m_cora", "011000138", "987654321", false));
    }

    static List<SettlementEvent> demoDay() {
        return List.of(
                new SettlementEvent("c1", "m_acme", EventType.CAPTURE, Money.parse("120.00", USD), at("09:15")),
                new SettlementEvent("c3", "m_bolt", EventType.CAPTURE, Money.parse("45.50", USD), at("10:05")),
                new SettlementEvent("c2", "m_acme", EventType.CAPTURE, Money.parse("80.00", USD), at("11:40")),
                new SettlementEvent("c4", "m_cora", EventType.CAPTURE, Money.parse("1000.00", USD), at("12:30")),
                new SettlementEvent("c6", "m_cora", EventType.CAPTURE, Money.parse("250.00", USD), at("13:45")),
                new SettlementEvent("r1", "m_acme", EventType.REFUND, Money.parse("20.00", USD), at("15:05")),
                new SettlementEvent("cb1", "m_bolt", EventType.CHARGEBACK, Money.parse("60.00", USD), at("16:20")),
                new SettlementEvent("c5", "m_cora", EventType.CAPTURE, Money.parse("10.00", USD), at("23:30")));
    }

    /** A seeded day: many merchants, many events, a realistic mix of refunds and chargebacks. */
    static List<SettlementEvent> generatedDay(List<Merchant> merchants, int count, long seed) {
        Random rnd = new Random(seed);
        List<SettlementEvent> out = new ArrayList<>();
        long dayStart = Instant.parse("2026-03-31T00:00:00Z").getEpochSecond();
        for (int i = 0; i < count; i++) {
            Merchant m = merchants.get(rnd.nextInt(merchants.size()));
            int roll = rnd.nextInt(1000);
            EventType type = roll < 930 ? EventType.CAPTURE : roll < 995 ? EventType.REFUND : EventType.CHARGEBACK;
            long minor = 500 + rnd.nextInt(49_500);               // 5.00 .. 499.99
            Instant when = Instant.ofEpochSecond(dayStart + (long) i * 86_400 / count);
            out.add(new SettlementEvent("g" + i, m.id(), type, Money.ofMinor(minor, USD), when));
        }
        return out;
    }

    static List<Merchant> generatedMerchants(int n) {
        List<Merchant> out = new ArrayList<>();
        for (int i = 0; i < n; i++) out.add(new Merchant(String.format("m_%04d", i), String.format("0%08d", 21000021 + i), "ACCT" + i, false));
        return out;
    }

    public static void main(String[] args) {
        System.out.println("== One business day, three merchants, cutoff 23:00 ==");
        Platform platform = new Platform(USD, FeeSchedule.standard(USD));
        demoMerchants().forEach(platform::addMerchant);
        for (SettlementEvent e : demoDay()) {
            platform.book(e);
            System.out.printf("%s %-10s %-6s %-12s fee %-9s merchant %s%n", e.at().toString().substring(11, 16), e.type(), e.merchantId(),
                    e.amount(), platform.fees.feeFor(e), platform.fees.merchantEffect(e));
        }
        BatchSettler settler = new BatchSettler(platform, Money.zero(USD));
        SettlementBatch batch = settler.run(DAY, CUTOFF);
        System.out.println("\nbatch " + batch.batchId() + ": " + batch.eventIds().size() + " events, "
                + batch.instructions().size() + " payouts totalling " + batch.total());
        for (PayoutInstruction p : batch.instructions()) System.out.println("  pay " + p.merchantId() + " " + p.amount());
        batch.carriedForward().forEach((m, a) -> System.out.println("  carry " + m + " " + a + " (merchant owes the platform)"));
        System.out.println("  c5 at 23:30 settled? " + settler.isSettled("c5") + " (after cutoff: next batch)");
        System.out.println("acquirer owes for this batch: " + batch.processorFunding());

        System.out.println("\n== Settlement file ==");
        String file = SettlementFile.render(batch);
        System.out.print(file);
        System.out.println("receiver validation: " + SettlementFile.validate(file));

        platform.receiveFunding(batch.batchId(), batch.processorFunding());
        settler.confirmPayouts(batch);
        System.out.println("\n== Ledger after funding and payout ==");
        platform.ledger.balancesView().forEach((acct, v) -> System.out.printf("  %-28s %s%n", acct, Money.ofMinor(v, USD)));
        System.out.println("trial balance: " + platform.ledger.trialBalance() + "   ties out: " + settler.tiesOut());
        SettlementBatch again = settler.run(DAY, CUTOFF);
        System.out.println("rerun of " + again.batchId() + " returns the same batch: " + (again == batch)
                + ", journals still " + platform.ledger.journalCount());

        System.out.println("\n== Batch vs real time over the same events ==");
        LiquidityComparison cmp = LiquidityComparison.run(demoMerchants(), demoDay(), DAY, CUTOFF, USD);
        printComparison(cmp);

        System.out.println("\n== 200 merchants, 50,000 events, seed 7 ==");
        List<Merchant> many = generatedMerchants(200);
        List<SettlementEvent> big = generatedDay(many, 50_000, 7);
        Platform bigPlatform = new Platform(USD, FeeSchedule.standard(USD));
        many.forEach(bigPlatform::addMerchant);
        big.forEach(bigPlatform::book);
        BatchSettler bigSettler = new BatchSettler(bigPlatform, Money.parse("1.00", USD));
        SettlementBatch bigBatch = bigSettler.run(DAY, Instant.parse("2026-04-01T00:00:00Z"));
        System.out.println("payouts " + bigBatch.instructions().size() + ", total " + bigBatch.total()
                + ", carried " + bigBatch.carriedForward().size() + ", ties out " + bigSettler.tiesOut()
                + ", file valid " + SettlementFile.validate(SettlementFile.render(bigBatch)));
        printComparison(LiquidityComparison.run(many, big, DAY, Instant.parse("2026-04-01T00:00:00Z"), USD));
    }

    static void printComparison(LiquidityComparison cmp) {
        System.out.printf("  %-26s %-18s %-18s%n", "", "batch (T+1)", "real time");
        System.out.printf("  %-26s %-18d %-18d%n", "transfers sent", cmp.batchTransfers(), cmp.realTimeTransfers());
        System.out.printf("  %-26s %-18s %-18s%n", "value paid to merchants", cmp.batchPaid(), cmp.realTimePaid());
        System.out.printf("  %-26s %-18s %-18s%n", "peak prefunding", cmp.batchPeakPrefunding(), cmp.realTimePeakPrefunding());
        System.out.printf("  %-26s %-18s %-18s%n", "clawback exposure", "0.00 USD", cmp.realTimeExposure());
        System.out.printf("  %-26s %-18s %-18s%n", "cash after T+1 funding", cmp.batchCashAfter(), cmp.realTimeCashAfter());
    }
}
