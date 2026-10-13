// Lesson: phases/12-payment-systems-design/06-refunds-and-partial-refunds/docs/en.md
// Refunds, Partial Refunds, and Refund Windows
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Visa Core Rules (credit transaction processing); Stripe API reference (Refund object, amount_refunded);
//          Pacioli, "Summa de arithmetica" (1494) for the double-entry rule. Java 21, stdlib only.

import java.time.LocalDate;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }
    boolean isZero() { return minorUnits == 0; }

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
// Time is injected as a calendar date: refund windows are counted in days, and tests move the
// date rather than waiting for it.
// ---------------------------------------------------------------------------------------------

interface Clock { LocalDate today(); }

final class FakeClock implements Clock {
    private LocalDate today;
    FakeClock(LocalDate start) { today = start; }
    @Override public LocalDate today() { return today; }
    void advanceDays(long days) { today = today.plusDays(days); }
}

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger (Phase 10, Lesson 03): balanced entries, append-only, reversals
// instead of edits. Every refund movement below is one of these entries.
// ---------------------------------------------------------------------------------------------

enum Side { DEBIT, CREDIT; Side opposite() { return this == DEBIT ? CREDIT : DEBIT; } }

enum AccountType {
    ASSET(Side.DEBIT), LIABILITY(Side.CREDIT), REVENUE(Side.CREDIT), EXPENSE(Side.DEBIT);
    final Side normal;
    AccountType(Side normal) { this.normal = normal; }
}

record Posting(String account, Side side, Money amount) {
    Posting {
        if (!amount.isPositive()) throw new IllegalArgumentException("posting amount must be positive: " + amount);
    }
    static Posting debit(String account, Money amount) { return new Posting(account, Side.DEBIT, amount); }
    static Posting credit(String account, Money amount) { return new Posting(account, Side.CREDIT, amount); }
    @Override public String toString() { return (side == Side.DEBIT ? "  Dr " : "      Cr ") + account + "  " + amount; }
}

record JournalEntry(String id, String description, List<Posting> postings) {
    JournalEntry {
        postings = List.copyOf(postings);
        if (postings.size() < 2) throw new IllegalArgumentException("entry " + id + " needs at least two postings");
        long net = 0;
        for (Posting p : postings) net += p.side() == Side.DEBIT ? p.amount().minorUnits() : -p.amount().minorUnits();
        if (net != 0) throw new IllegalArgumentException("entry " + id + " is unbalanced by " + net + " minor units");
    }

    JournalEntry reversal(String newId, String why) {
        List<Posting> flipped = new ArrayList<>();
        for (Posting p : postings) flipped.add(new Posting(p.account(), p.side().opposite(), p.amount()));
        return new JournalEntry(newId, why + " (reverses " + id + ")", flipped);
    }
}

final class Ledger {
    private final Map<String, AccountType> accounts = new LinkedHashMap<>();
    private final List<JournalEntry> journal = new ArrayList<>();
    private final Map<String, Long> balances = new HashMap<>();
    private final Currency currency;

    Ledger(Currency currency) { this.currency = currency; }

    void open(String account, AccountType type) { accounts.put(account, type); balances.put(account, 0L); }

    void post(JournalEntry e) {
        for (Posting p : e.postings()) {
            if (!accounts.containsKey(p.account())) throw new IllegalArgumentException("unknown account " + p.account());
            if (!p.amount().currency().equals(currency)) throw new CurrencyMismatchException(currency, p.amount().currency());
        }
        journal.add(e);
        for (Posting p : e.postings()) {
            long signed = p.side() == accounts.get(p.account()).normal ? p.amount().minorUnits() : -p.amount().minorUnits();
            balances.merge(p.account(), signed, Math::addExact);
        }
    }

    /** Balance on the account's normal side: a positive liability is money we owe. */
    Money balance(String account) { return Money.ofMinor(balances.get(account), currency); }

    /** Sum of debit-normal balances equals sum of credit-normal balances, or the ledger is corrupt. */
    boolean balances() {
        long debitSide = 0, creditSide = 0;
        for (Map.Entry<String, AccountType> a : accounts.entrySet()) {
            if (a.getValue().normal == Side.DEBIT) debitSide += balances.get(a.getKey()); else creditSide += balances.get(a.getKey());
        }
        return debitSide == creditSide;
    }

    List<JournalEntry> journal() { return Collections.unmodifiableList(journal); }
    Map<String, AccountType> accounts() { return Collections.unmodifiableMap(accounts); }
}

// ---------------------------------------------------------------------------------------------
// Captures and refunds.
// ---------------------------------------------------------------------------------------------

/** Settled money a refund can be issued against. The fee is what the processor kept on capture. */
record Capture(String id, String paymentId, Money amount, Money fee, LocalDate capturedOn) {}

enum RefundState { PENDING, SUCCEEDED, FAILED }

/** Whether the processor gives back its fee when a payment is refunded. Contracts differ; the ledger must not care. */
enum FeeRefundPolicy { NONE, PROPORTIONAL }

record RefundPolicy(int windowDays, FeeRefundPolicy feePolicy) {
    RefundPolicy {
        if (windowDays < 0) throw new IllegalArgumentException("windowDays must be >= 0");
        Objects.requireNonNull(feePolicy, "feePolicy");
    }
}

final class Refund {
    final String id;
    final String captureId;
    final Money amount;
    final String reason;
    final LocalDate requestedOn;
    RefundState state = RefundState.PENDING;
    Money feeReturned;
    String failureReason;

    Refund(String id, String captureId, Money amount, String reason, LocalDate requestedOn) {
        this.id = id;
        this.captureId = captureId;
        this.amount = amount;
        this.reason = reason;
        this.requestedOn = requestedOn;
        this.feeReturned = Money.zero(amount.currency());
    }

    RefundState state() { return state; }
    Money amount() { return amount; }
    Money feeReturned() { return feeReturned; }

    @Override public String toString() {
        return String.format("%-8s %-10s %-12s fee back %-10s %s", id, state, amount, feeReturned, reason);
    }
}

class RefundException extends IllegalStateException { RefundException(String m) { super(m); } }

final class OverRefundException extends RefundException { OverRefundException(String m) { super(m); } }

final class RefundWindowClosedException extends RefundException { RefundWindowClosedException(String m) { super(m); } }

final class RefundIdempotencyConflictException extends RefundException { RefundIdempotencyConflictException(String m) { super(m); } }

record RefundResult(Refund refund, boolean replayed) {}

/**
 * The refund service. Every public method is synchronized: the check "does the remaining balance cover
 * this refund" and the reservation that consumes it must be one atomic step, or two concurrent partial
 * refunds can both pass the check. In a database this is one conditional UPDATE (see the docs).
 */
final class RefundService {
    static final String SCHEME = "scheme_settlement";       // asset: what the card scheme owes / has paid us
    static final String MERCHANT = "merchant_payable";      // liability: what we owe the merchant
    static final String REFUNDS = "refunds_payable";        // liability: refunds promised, not yet sent
    static final String FEES = "fee_revenue";               // revenue: processing fees we kept

    private final Ledger ledger;
    private final Clock clock;
    private final RefundPolicy policy;
    private final Map<String, Capture> captures = new LinkedHashMap<>();
    private final Map<String, Refund> refunds = new LinkedHashMap<>();
    private final Map<String, List<Refund>> refundsByCapture = new HashMap<>();
    private int entrySeq = 0;

    RefundService(Ledger ledger, Clock clock, RefundPolicy policy) {
        this.ledger = ledger;
        this.clock = clock;
        this.policy = policy;
        ledger.open(SCHEME, AccountType.ASSET);
        ledger.open(MERCHANT, AccountType.LIABILITY);
        ledger.open(REFUNDS, AccountType.LIABILITY);
        ledger.open(FEES, AccountType.REVENUE);
    }

    private String nextEntryId() { return String.format("je_%03d", ++entrySeq); }

    /** Record a capture: the scheme owes us the gross, we owe the merchant the net, and we keep the fee. */
    synchronized Capture capture(String captureId, String paymentId, Money amount, long feeBps, long fixedFeeMinor) {
        if (captures.containsKey(captureId)) throw new IllegalStateException("capture exists: " + captureId);
        if (!amount.isPositive()) throw new IllegalArgumentException("capture amount must be positive");
        long variable = Math.floorDiv(amount.minorUnits() * feeBps + 5_000, 10_000);
        Money fee = Money.ofMinor(variable + fixedFeeMinor, amount.currency());
        Capture c = new Capture(captureId, paymentId, amount, fee, clock.today());
        ledger.post(new JournalEntry(nextEntryId(), "capture " + captureId, List.of(
                Posting.debit(SCHEME, amount),
                Posting.credit(MERCHANT, amount.minus(fee)),
                Posting.credit(FEES, fee))));
        captures.put(captureId, c);
        refundsByCapture.put(captureId, new ArrayList<>());
        return c;
    }

    synchronized Money refunded(String captureId) { return sum(captureId, RefundState.SUCCEEDED); }
    synchronized Money pending(String captureId) { return sum(captureId, RefundState.PENDING); }

    /** captured - succeeded - pending. A pending refund already consumes balance: that is what stops the race. */
    synchronized Money remainingRefundable(String captureId) {
        Capture c = capture(captureId);
        return c.amount().minus(refunded(captureId)).minus(pending(captureId));
    }

    synchronized Money feeReturned(String captureId) {
        Money total = Money.zero(capture(captureId).amount().currency());
        for (Refund r : refundsByCapture.get(captureId)) total = total.plus(r.feeReturned);
        return total;
    }

    synchronized Refund refund(String refundId) {
        Refund r = refunds.get(refundId);
        if (r == null) throw new IllegalArgumentException("unknown refund " + refundId);
        return r;
    }

    synchronized List<Refund> refundsFor(String captureId) { return List.copyOf(refundsByCapture.get(capture(captureId).id())); }

    private Capture capture(String captureId) {
        Capture c = captures.get(captureId);
        if (c == null) throw new IllegalArgumentException("unknown capture " + captureId);
        return c;
    }

    private Money sum(String captureId, RefundState state) {
        Money total = Money.zero(capture(captureId).amount().currency());
        for (Refund r : refundsByCapture.get(captureId)) if (r.state == state) total = total.plus(r.amount);
        return total;
    }

    /**
     * Request a refund. Checks, in order: idempotency (same id), amount positive and same currency, the
     * refund window, and the remaining refundable balance. Only after every check passes does anything
     * change: the refund is recorded PENDING and the merchant's balance is moved into refunds_payable.
     */
    synchronized RefundResult requestRefund(String refundId, String captureId, Money amount, String reason) {
        Refund existing = refunds.get(refundId);
        if (existing != null) {
            if (existing.captureId.equals(captureId) && existing.amount.equals(amount)) return new RefundResult(existing, true);
            throw new RefundIdempotencyConflictException("refund id " + refundId + " was already used for "
                    + existing.amount + " on " + existing.captureId);
        }
        Capture c = capture(captureId);
        if (!amount.isPositive()) throw new IllegalArgumentException("refund amount must be positive: " + amount);
        if (!amount.currency().equals(c.amount().currency())) throw new CurrencyMismatchException(c.amount().currency(), amount.currency());
        LocalDate deadline = c.capturedOn().plusDays(policy.windowDays());
        if (clock.today().isAfter(deadline)) {
            throw new RefundWindowClosedException("capture " + captureId + " on " + c.capturedOn() + ": refund window of "
                    + policy.windowDays() + " days closed on " + deadline + " (today " + clock.today() + ", "
                    + ChronoUnit.DAYS.between(c.capturedOn(), clock.today()) + " days)");
        }
        Money remaining = remainingRefundable(captureId);
        if (amount.compareTo(remaining) > 0) {
            throw new OverRefundException("refund " + amount + " exceeds remaining refundable " + remaining + " on " + captureId
                    + " (captured " + c.amount() + ", refunded " + refunded(captureId) + ", pending " + pending(captureId) + ")");
        }
        Refund r = new Refund(refundId, captureId, amount, reason, clock.today());
        ledger.post(new JournalEntry(reserveEntryId(refundId), "refund " + refundId + " requested on " + captureId, List.of(
                Posting.debit(MERCHANT, amount),
                Posting.credit(REFUNDS, amount))));
        refunds.put(refundId, r);
        refundsByCapture.get(captureId).add(r);
        return new RefundResult(r, false);
    }

    private static String reserveEntryId(String refundId) { return "je_req_" + refundId; }

    /**
     * The processor confirmed the refund. Money leaves: refunds_payable is cleared against the scheme
     * account, and under PROPORTIONAL the fee share is returned from fee_revenue to the merchant.
     * Returns false for a redelivered confirmation (already SUCCEEDED).
     */
    synchronized boolean succeed(String refundId) {
        Refund r = refund(refundId);
        if (r.state == RefundState.SUCCEEDED) return false;
        if (r.state != RefundState.PENDING) throw new RefundException("refund " + refundId + " is " + r.state + "; cannot succeed");
        Capture c = capture(r.captureId);
        Money feeBack = policy.feePolicy() == FeeRefundPolicy.PROPORTIONAL ? feeShare(c, r.amount) : Money.zero(r.amount.currency());
        List<Posting> postings = new ArrayList<>(List.of(Posting.debit(REFUNDS, r.amount), Posting.credit(SCHEME, r.amount)));
        if (feeBack.isPositive()) {
            postings.add(Posting.debit(FEES, feeBack));
            postings.add(Posting.credit(MERCHANT, feeBack));
        }
        ledger.post(new JournalEntry(nextEntryId(), "refund " + refundId + " settled", postings));
        r.state = RefundState.SUCCEEDED;
        r.feeReturned = feeBack;
        return true;
    }

    /**
     * Fee to return for a refund that is about to succeed, computed cumulatively so the rounding never
     * drifts: target(after) - returned(so far), where target(x) = floor(fee * x / captured). When the
     * capture is fully refunded, target is exactly the fee, so the sum of the shares is exactly the fee.
     */
    private Money feeShare(Capture c, Money refundAmount) {
        long after = refunded(c.id()).plus(refundAmount).minorUnits();
        long target = Math.floorDiv(Math.multiplyExact(c.fee().minorUnits(), after), c.amount().minorUnits());
        return Money.ofMinor(target, c.fee().currency()).minus(feeReturned(c.id()));
    }

    /** The processor refused the refund: the reservation is reversed and the balance becomes refundable again. */
    synchronized void fail(String refundId, String why) {
        Refund r = refund(refundId);
        if (r.state != RefundState.PENDING) throw new RefundException("refund " + refundId + " is " + r.state + "; cannot fail");
        JournalEntry reserve = ledger.journal().stream().filter(e -> e.id().equals(reserveEntryId(refundId))).findFirst().orElseThrow();
        ledger.post(reserve.reversal(nextEntryId(), "refund " + refundId + " failed: " + why));
        r.state = RefundState.FAILED;
        r.failureReason = why;
    }

    /** The invariants the tests assert after every operation. Returns an empty list when all hold. */
    synchronized List<String> checkInvariants() {
        List<String> broken = new ArrayList<>();
        if (!ledger.balances()) broken.add("ledger does not balance");
        Money pendingTotal = Money.zero(ledger.balance(REFUNDS).currency());
        for (Capture c : captures.values()) {
            Money refundedAndPending = refunded(c.id()).plus(pending(c.id()));
            if (refundedAndPending.compareTo(c.amount()) > 0) broken.add(c.id() + ": refunded + pending exceeds captured");
            if (feeReturned(c.id()).compareTo(c.fee()) > 0) broken.add(c.id() + ": fee returned exceeds fee charged");
            if (refunded(c.id()).equals(c.amount()) && policy.feePolicy() == FeeRefundPolicy.PROPORTIONAL && !feeReturned(c.id()).equals(c.fee())) {
                broken.add(c.id() + ": fully refunded but fee not fully returned");
            }
            pendingTotal = pendingTotal.plus(pending(c.id()));
        }
        if (!ledger.balance(REFUNDS).equals(pendingTotal)) broken.add("refunds_payable " + ledger.balance(REFUNDS) + " != pending " + pendingTotal);
        return broken;
    }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    static void balances(Ledger l) {
        for (String a : l.accounts().keySet()) System.out.printf("   %-18s %12s%n", a, l.balance(a));
        System.out.println("   ledger balances: " + l.balances());
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;
        FakeClock clock = new FakeClock(LocalDate.of(2026, 1, 10));
        Ledger ledger = new Ledger(USD);
        RefundService svc = new RefundService(ledger, clock, new RefundPolicy(180, FeeRefundPolicy.PROPORTIONAL));

        System.out.println("== Capture 100.00 USD at 2.90% + 0.30 ==");
        Capture cap = svc.capture("cap_1", "pay_1", Money.ofMinor(10_000, USD), 290, 30);
        System.out.println("fee " + cap.fee() + ", merchant owed " + cap.amount().minus(cap.fee()));
        balances(ledger);

        System.out.println();
        System.out.println("== Partial refund 30.00, settled ==");
        clock.advanceDays(5);
        svc.requestRefund("re_1", "cap_1", Money.ofMinor(3_000, USD), "one item returned");
        System.out.println("pending: remaining refundable " + svc.remainingRefundable("cap_1"));
        svc.succeed("re_1");
        System.out.println("settled: " + svc.refund("re_1") + "; remaining " + svc.remainingRefundable("cap_1"));

        System.out.println();
        System.out.println("== A second partial is pending; an over-refund is refused; a retry replays ==");
        svc.requestRefund("re_2", "cap_1", Money.ofMinor(3_000, USD), "second item returned");
        try {
            svc.requestRefund("re_3", "cap_1", Money.ofMinor(5_000, USD), "customer asked for the rest");
        } catch (OverRefundException e) {
            System.out.println("refused: " + e.getMessage());
        }
        RefundResult again = svc.requestRefund("re_2", "cap_1", Money.ofMinor(3_000, USD), "client retry");
        System.out.println("re_2 again -> replayed=" + again.replayed() + ", state " + again.refund().state());
        try {
            svc.requestRefund("re_2", "cap_1", Money.ofMinor(3_500, USD), "client bug");
        } catch (RefundIdempotencyConflictException e) {
            System.out.println("refused: " + e.getMessage());
        }

        System.out.println();
        System.out.println("== The processor fails re_2: the reservation is reversed ==");
        svc.fail("re_2", "card account closed");
        System.out.println("re_2 " + svc.refund("re_2").state() + "; remaining refundable " + svc.remainingRefundable("cap_1"));

        System.out.println();
        System.out.println("== Refund the remaining 70.00: the fee shares sum to exactly the fee ==");
        svc.requestRefund("re_4", "cap_1", Money.ofMinor(7_000, USD), "order canceled");
        svc.succeed("re_4");
        System.out.println("redelivered confirmation applied again? " + svc.succeed("re_4"));
        for (Refund r : svc.refundsFor("cap_1")) System.out.println("   " + r);
        System.out.println("refunded " + svc.refunded("cap_1") + ", fee returned " + svc.feeReturned("cap_1") + " of " + cap.fee()
                + ", remaining " + svc.remainingRefundable("cap_1"));
        balances(ledger);
        System.out.println("journal for cap_1:");
        for (JournalEntry e : ledger.journal()) {
            System.out.println("   " + e.id() + "  " + e.description());
            for (Posting p : e.postings()) System.out.println("   " + p);
        }

        System.out.println();
        System.out.println("== The same full refund under FeeRefundPolicy.NONE: the merchant ends up owing the fee ==");
        Ledger noneLedger = new Ledger(USD);
        RefundService none = new RefundService(noneLedger, clock, new RefundPolicy(180, FeeRefundPolicy.NONE));
        none.capture("cap_n", "pay_n", Money.ofMinor(10_000, USD), 290, 30);
        none.requestRefund("re_n", "cap_n", Money.ofMinor(10_000, USD), "order canceled");
        none.succeed("re_n");
        System.out.println("merchant_payable " + noneLedger.balance(RefundService.MERCHANT) + ", fee_revenue "
                + noneLedger.balance(RefundService.FEES) + ", ledger balances: " + noneLedger.balances());

        System.out.println();
        System.out.println("== Rounding: 10.00 USD capture, fee 0.59, refunded as 3.33 + 3.33 + 3.34 ==");
        Capture small = svc.capture("cap_2", "pay_2", Money.ofMinor(1_000, USD), 290, 30);
        for (long amt : new long[] {333, 333, 334}) {
            String id = "re_s" + amt + "_" + svc.refundsFor("cap_2").size();
            svc.requestRefund(id, "cap_2", Money.ofMinor(amt, USD), "split");
            svc.succeed(id);
        }
        System.out.println("shares: " + svc.refundsFor("cap_2").stream().map(r -> r.feeReturned().toString()).toList()
                + " sum " + svc.feeReturned("cap_2") + " (fee " + small.fee() + "; per-refund floor would give 0.57)");

        System.out.println();
        System.out.println("== The refund window: 180 days ==");
        svc.capture("cap_3", "pay_3", Money.ofMinor(2_500, USD), 290, 30);
        clock.advanceDays(180);
        svc.requestRefund("re_w1", "cap_3", Money.ofMinor(500, USD), "day 180");
        System.out.println("day 180: accepted, remaining " + svc.remainingRefundable("cap_3"));
        clock.advanceDays(1);
        try {
            svc.requestRefund("re_w2", "cap_3", Money.ofMinor(500, USD), "day 181");
        } catch (RefundWindowClosedException e) {
            System.out.println("refused: " + e.getMessage());
        }

        System.out.println();
        System.out.println("invariants broken: " + svc.checkInvariants());
    }
}
