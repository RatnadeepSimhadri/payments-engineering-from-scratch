// Lesson: phases/12-payment-systems-design/11-recurring-and-subscriptions/docs/en.md
// Recurring Payments, Subscriptions, and Dunning
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Visa/Mastercard stored-credential (credential-on-file) framework; Visa Account Updater;
//          Stripe Billing docs (subscription statuses, proration). Java 21, standard library only.

import java.time.LocalDate;
import java.time.temporal.ChronoUnit;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collection;
import java.util.Deque;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
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
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        }
        String padded = frac + "0".repeat(currency.exponent() - frac.length());
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.isEmpty() ? 0 : Long.parseLong(padded));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isZero() { return minorUnits == 0; }

    /**
     * Split by ratios with the largest-remainder method: shares sum to exactly this amount.
     * Ties in remainder go to the lower index (stable sort), so results are deterministic.
     */
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
// Time is injected. Billing runs at day granularity on the merchant's calendar.
// ---------------------------------------------------------------------------------------------

interface Clock { LocalDate today(); }

final class FakeClock implements Clock {
    private LocalDate today;
    FakeClock(LocalDate start) { this.today = start; }
    public LocalDate today() { return today; }
    void advanceDays(long days) { today = today.plusDays(days); }
    void set(LocalDate date) { today = date; }
}

// ---------------------------------------------------------------------------------------------
// Billing cycles: every period boundary is computed from the anchor, never from the previous boundary.
// ---------------------------------------------------------------------------------------------

enum Interval {
    MONTHLY(1), QUARTERLY(3), YEARLY(12);

    final int months;
    Interval(int months) { this.months = months; }
}

record Plan(String id, Money price, Interval interval) {
    Plan {
        if (price.isNegative()) throw new IllegalArgumentException("price must be non-negative");
    }
}

final class BillingCycle {
    private BillingCycle() {}

    /** Start of period n (n = 0 is the anchor). plusMonths clamps to month end, e.g. Jan 31 + 1 month = Feb 28. */
    static LocalDate periodStart(LocalDate anchor, Interval interval, long n) {
        if (n < 0) throw new IllegalArgumentException("period index must be >= 0");
        return anchor.plusMonths(Math.multiplyExact(n, (long) interval.months));
    }

    static LocalDate periodEnd(LocalDate anchor, Interval interval, long n) { return periodStart(anchor, interval, n + 1); }

    /** The bug this lesson warns about: each boundary computed from the previous one drifts after a short month. */
    static List<LocalDate> chainedStarts(LocalDate anchor, Interval interval, int count) {
        List<LocalDate> out = new ArrayList<>();
        LocalDate d = anchor;
        for (int i = 0; i < count; i++) { out.add(d); d = d.plusMonths(interval.months); }
        return out;
    }

    static List<LocalDate> anchoredStarts(LocalDate anchor, Interval interval, int count) {
        List<LocalDate> out = new ArrayList<>();
        for (int i = 0; i < count; i++) out.add(periodStart(anchor, interval, i));
        return out;
    }
}

// ---------------------------------------------------------------------------------------------
// Proration: unused time on the old plan is credited, remaining time on the new plan is charged.
// ---------------------------------------------------------------------------------------------

record ProrationResult(long usedDays, long remainingDays, long periodDays, Money credit, Money charge) {
    Money net() { return charge.minus(credit); }
}

final class Proration {
    private Proration() {}

    /**
     * Day-based proration with exact allocation. The old price is split used:remaining with
     * largest-remainder allocation, so usedPortion + credit == old price to the minor unit.
     */
    static ProrationResult compute(Plan oldPlan, Plan newPlan, LocalDate periodStart, LocalDate periodEnd, LocalDate changeDate) {
        if (changeDate.isBefore(periodStart) || !changeDate.isBefore(periodEnd)) {
            throw new IllegalArgumentException("change date " + changeDate + " outside period [" + periodStart + ", " + periodEnd + ")");
        }
        if (!oldPlan.price().currency().equals(newPlan.price().currency())) {
            throw new CurrencyMismatchException(oldPlan.price().currency(), newPlan.price().currency());
        }
        long period = ChronoUnit.DAYS.between(periodStart, periodEnd);
        long used = ChronoUnit.DAYS.between(periodStart, changeDate);
        long remaining = period - used;
        Money credit = oldPlan.price().allocate(used, remaining)[1];
        Money charge = newPlan.price().allocate(used, remaining)[1];
        return new ProrationResult(used, remaining, period, credit, charge);
    }
}

// ---------------------------------------------------------------------------------------------
// The subscription state machine.
// ---------------------------------------------------------------------------------------------

enum SubscriptionStatus { TRIALING, ACTIVE, PAST_DUE, UNPAID, CANCELED }

final class IllegalTransitionException extends IllegalStateException {
    IllegalTransitionException(SubscriptionStatus from, SubscriptionStatus to) { super("illegal transition " + from + " -> " + to); }
}

final class SubscriptionTransitions {
    private SubscriptionTransitions() {}

    static final Map<SubscriptionStatus, Set<SubscriptionStatus>> ALLOWED = new EnumMap<>(Map.of(
            SubscriptionStatus.TRIALING, EnumSet.of(SubscriptionStatus.ACTIVE, SubscriptionStatus.PAST_DUE, SubscriptionStatus.CANCELED),
            SubscriptionStatus.ACTIVE, EnumSet.of(SubscriptionStatus.PAST_DUE, SubscriptionStatus.CANCELED),
            SubscriptionStatus.PAST_DUE, EnumSet.of(SubscriptionStatus.ACTIVE, SubscriptionStatus.UNPAID, SubscriptionStatus.CANCELED),
            SubscriptionStatus.UNPAID, EnumSet.of(SubscriptionStatus.ACTIVE, SubscriptionStatus.CANCELED),
            SubscriptionStatus.CANCELED, EnumSet.noneOf(SubscriptionStatus.class)));

    static boolean allowed(SubscriptionStatus from, SubscriptionStatus to) { return ALLOWED.get(from).contains(to); }
}

final class Subscription {
    final String id;
    final String customerId;
    final LocalDate anchor;
    Plan plan;
    String cardToken;
    SubscriptionStatus status;
    long periodIndex = -1;          // -1 while trialing: period 0 starts at the anchor
    LocalDate currentPeriodStart;
    LocalDate currentPeriodEnd;     // the next billing date
    boolean cancelAtPeriodEnd;
    Money creditBalance;

    Subscription(String id, String customerId, Plan plan, String cardToken, LocalDate today, LocalDate anchor) {
        this.id = id;
        this.customerId = customerId;
        this.plan = plan;
        this.cardToken = cardToken;
        this.anchor = anchor;
        this.status = anchor.isAfter(today) ? SubscriptionStatus.TRIALING : SubscriptionStatus.ACTIVE;
        this.currentPeriodStart = today;
        this.currentPeriodEnd = anchor;
        this.creditBalance = Money.zero(plan.price().currency());
    }

    void transition(SubscriptionStatus to) {
        if (status == to) return;
        if (!SubscriptionTransitions.allowed(status, to)) throw new IllegalTransitionException(status, to);
        status = to;
    }

    boolean bills() { return status != SubscriptionStatus.CANCELED && status != SubscriptionStatus.UNPAID; }
}

// ---------------------------------------------------------------------------------------------
// Invoices, the gateway, and the card-updater hook.
// ---------------------------------------------------------------------------------------------

enum InvoiceStatus { OPEN, PAID, UNCOLLECTIBLE }

final class Invoice {
    final String id;
    final String subscriptionId;
    final LocalDate periodStart;
    final LocalDate periodEnd;
    final Money amount;
    final String description;
    InvoiceStatus status = InvoiceStatus.OPEN;
    int attempts;
    LocalDate firstFailure;
    LocalDate nextAttempt;
    ChargeOutcome lastOutcome;

    Invoice(String id, String subscriptionId, LocalDate periodStart, LocalDate periodEnd, Money amount, String description, LocalDate due) {
        this.id = id;
        this.subscriptionId = subscriptionId;
        this.periodStart = periodStart;
        this.periodEnd = periodEnd;
        this.amount = amount;
        this.description = description;
        this.nextAttempt = due;
    }
}

enum ChargeOutcome {
    APPROVED, INSUFFICIENT_FUNDS, DO_NOT_HONOR, EXPIRED_CARD, DO_NOT_RETRY;

    boolean retryable() { return this == INSUFFICIENT_FUNDS || this == DO_NOT_HONOR; }
}

/** Merchant-initiated charge on a stored credential; the reference is the gateway's idempotency key. */
interface ChargeGateway { ChargeOutcome charge(String reference, String cardToken, Money amount); }

/** Visa Account Updater / Mastercard ABU style: given an old credential, maybe a replacement. */
interface CardUpdater { Optional<String> refresh(String customerId, String oldCardToken); }

record DunningPolicy(List<Integer> retryOffsetsDays, SubscriptionStatus whenExhausted) {
    DunningPolicy {
        retryOffsetsDays = List.copyOf(retryOffsetsDays);
        for (int i = 0; i < retryOffsetsDays.size(); i++) {
            if (retryOffsetsDays.get(i) <= 0 || (i > 0 && retryOffsetsDays.get(i) <= retryOffsetsDays.get(i - 1))) {
                throw new IllegalArgumentException("retry offsets must be positive and strictly increasing: " + retryOffsetsDays);
            }
        }
        if (whenExhausted != SubscriptionStatus.CANCELED && whenExhausted != SubscriptionStatus.UNPAID) {
            throw new IllegalArgumentException("exhausted dunning ends CANCELED or UNPAID");
        }
    }

    static DunningPolicy standard() { return new DunningPolicy(List.of(1, 3, 7, 14), SubscriptionStatus.CANCELED); }

    int maxAttempts() { return 1 + retryOffsetsDays.size(); }
}

// ---------------------------------------------------------------------------------------------
// The engine: one runDay() per calendar day, safe to call more than once on the same day.
// ---------------------------------------------------------------------------------------------

final class BillingEngine {
    private final Clock clock;
    private final ChargeGateway gateway;
    private final CardUpdater updater;
    private final DunningPolicy policy;
    private final Map<String, Subscription> subscriptions = new LinkedHashMap<>();
    private final Map<String, Invoice> invoices = new LinkedHashMap<>();
    private final List<String> log = new ArrayList<>();

    BillingEngine(Clock clock, ChargeGateway gateway, CardUpdater updater, DunningPolicy policy) {
        this.clock = clock;
        this.gateway = gateway;
        this.updater = updater;
        this.policy = policy;
    }

    List<String> log() { return log; }
    Subscription subscription(String id) { return subscriptions.get(id); }
    Invoice invoice(String id) { return invoices.get(id); }
    Collection<Invoice> invoices() { return invoices.values(); }

    Subscription subscribe(String id, String customerId, Plan plan, String cardToken, int trialDays) {
        if (trialDays < 0) throw new IllegalArgumentException("trialDays must be >= 0");
        if (subscriptions.containsKey(id)) throw new IllegalStateException("duplicate subscription " + id);
        LocalDate today = clock.today();
        Subscription s = new Subscription(id, customerId, plan, cardToken, today, today.plusDays(trialDays));
        subscriptions.put(id, s);
        note(s.id + " created " + s.status + ", anchor " + s.anchor);
        return s;
    }

    void cancel(String subscriptionId, boolean atPeriodEnd) {
        Subscription s = subscriptions.get(subscriptionId);
        if (atPeriodEnd) { s.cancelAtPeriodEnd = true; note(s.id + " will cancel at " + s.currentPeriodEnd); }
        else { s.transition(SubscriptionStatus.CANCELED); note(s.id + " canceled now"); }
    }

    /** Immediate plan change with proration. A positive net is invoiced now; a negative net becomes credit. */
    ProrationResult changePlan(String subscriptionId, Plan newPlan) {
        Subscription s = subscriptions.get(subscriptionId);
        if (s.status != SubscriptionStatus.ACTIVE) throw new IllegalStateException("plan changes require ACTIVE, was " + s.status);
        if (newPlan.interval() != s.plan.interval()) throw new IllegalArgumentException("interval changes reset the anchor; not modeled");
        LocalDate today = clock.today();
        ProrationResult p = Proration.compute(s.plan, newPlan, s.currentPeriodStart, s.currentPeriodEnd, today);
        Plan old = s.plan;
        s.plan = newPlan;
        note(s.id + " " + old.id() + " -> " + newPlan.id() + ": credit " + p.credit() + ", charge " + p.charge() + ", net " + p.net());
        if (p.net().isPositive()) {
            Invoice inv = openInvoice(s.id + ":proration:" + today, s, s.currentPeriodStart, s.currentPeriodEnd, p.net(), "proration", today);
            attempt(inv, s, today);
        } else if (p.net().isNegative()) {
            s.creditBalance = s.creditBalance.minus(p.net());
        }
        return p;
    }

    /** Run everything due today. Idempotent: a second call on the same day changes nothing. */
    void runDay() {
        LocalDate today = clock.today();
        for (Subscription s : subscriptions.values()) {
            while (s.bills() && !today.isBefore(s.currentPeriodEnd)) {
                if (s.cancelAtPeriodEnd) { s.transition(SubscriptionStatus.CANCELED); note(s.id + " canceled at period end"); break; }
                rollPeriod(s);
            }
        }
        for (Invoice inv : new ArrayList<>(invoices.values())) {
            if (inv.status == InvoiceStatus.OPEN && inv.nextAttempt != null && !today.isBefore(inv.nextAttempt)) {
                Subscription s = subscriptions.get(inv.subscriptionId);
                if (s.status == SubscriptionStatus.CANCELED) continue;
                attempt(inv, s, today);
            }
        }
    }

    private void rollPeriod(Subscription s) {
        s.periodIndex++;
        s.currentPeriodStart = BillingCycle.periodStart(s.anchor, s.plan.interval(), s.periodIndex);
        s.currentPeriodEnd = BillingCycle.periodEnd(s.anchor, s.plan.interval(), s.periodIndex);
        Money due = s.plan.price();
        Money applied = due.compareTo(s.creditBalance) < 0 ? due : s.creditBalance;
        s.creditBalance = s.creditBalance.minus(applied);
        Money amount = due.minus(applied);
        String id = s.id + ":" + s.currentPeriodStart;
        Invoice inv = openInvoice(id, s, s.currentPeriodStart, s.currentPeriodEnd, amount, s.plan.id(), s.currentPeriodStart);
        if (amount.isZero()) { inv.status = InvoiceStatus.PAID; inv.nextAttempt = null; note(id + " covered by credit"); }
    }

    private Invoice openInvoice(String id, Subscription s, LocalDate start, LocalDate end, Money amount, String description, LocalDate due) {
        Invoice existing = invoices.get(id);
        if (existing != null) return existing;
        Invoice inv = new Invoice(id, s.id, start, end, amount, description, due);
        invoices.put(id, inv);
        note("invoice " + id + " " + amount + " for [" + start + ", " + end + ")");
        return inv;
    }

    private void attempt(Invoice inv, Subscription s, LocalDate today) {
        inv.attempts++;
        String reference = inv.id + "#" + inv.attempts;
        ChargeOutcome outcome = gateway.charge(reference, s.cardToken, inv.amount);
        if (outcome == ChargeOutcome.EXPIRED_CARD) {
            Optional<String> fresh = updater.refresh(s.customerId, s.cardToken);
            if (fresh.isPresent() && !fresh.get().equals(s.cardToken)) {
                note(reference + " EXPIRED_CARD; card updater " + s.cardToken + " -> " + fresh.get());
                s.cardToken = fresh.get();
                inv.attempts++;
                reference = inv.id + "#" + inv.attempts;
                outcome = gateway.charge(reference, s.cardToken, inv.amount);
            }
        }
        inv.lastOutcome = outcome;
        if (outcome == ChargeOutcome.APPROVED) {
            inv.status = InvoiceStatus.PAID;
            inv.nextAttempt = null;
            if (s.status != SubscriptionStatus.ACTIVE) s.transition(SubscriptionStatus.ACTIVE);
            note(reference + " APPROVED " + inv.amount + " -> " + s.id + " " + s.status);
            return;
        }
        if (inv.firstFailure == null) inv.firstFailure = today;
        if (s.status == SubscriptionStatus.ACTIVE || s.status == SubscriptionStatus.TRIALING) s.transition(SubscriptionStatus.PAST_DUE);
        int retriesUsed = retriesUsed(inv);
        boolean exhausted = !outcome.retryable() || retriesUsed >= policy.retryOffsetsDays().size();
        if (exhausted) {
            inv.status = InvoiceStatus.UNCOLLECTIBLE;
            inv.nextAttempt = null;
            s.transition(policy.whenExhausted());
            note(reference + " " + outcome + "; dunning exhausted -> invoice UNCOLLECTIBLE, " + s.id + " " + s.status);
            return;
        }
        inv.nextAttempt = inv.firstFailure.plusDays(policy.retryOffsetsDays().get(retriesUsed));
        note(reference + " " + outcome + " -> " + s.id + " " + s.status + ", next attempt " + inv.nextAttempt);
    }

    /** Scheduled retries consumed so far: every dunning-day attempt after the first failure day. */
    private int retriesUsed(Invoice inv) {
        int used = 0;
        for (int offset : policy.retryOffsetsDays()) if (!clock.today().isBefore(inv.firstFailure.plusDays(offset))) used++;
        return used;
    }

    private void note(String line) { log.add(clock.today() + "  " + line); }
}

// ---------------------------------------------------------------------------------------------
// Fakes: a scripted gateway that dedupes by reference, and a card updater backed by a map.
// ---------------------------------------------------------------------------------------------

final class ScriptedGateway implements ChargeGateway {
    private final Map<String, Deque<ChargeOutcome>> scripts = new HashMap<>();
    private final Map<String, ChargeOutcome> byReference = new HashMap<>();
    private final List<String> charges = new ArrayList<>();

    void script(String cardToken, ChargeOutcome... outcomes) { scripts.put(cardToken, new ArrayDeque<>(List.of(outcomes))); }

    public ChargeOutcome charge(String reference, String cardToken, Money amount) {
        ChargeOutcome prior = byReference.get(reference);
        if (prior != null) return prior;
        Deque<ChargeOutcome> q = scripts.get(cardToken);
        ChargeOutcome outcome = (q == null || q.isEmpty()) ? ChargeOutcome.APPROVED : q.poll();
        byReference.put(reference, outcome);
        charges.add(reference + " " + cardToken + " " + amount + " " + outcome);
        return outcome;
    }

    List<String> charges() { return charges; }
    long approvedCount() { return charges.stream().filter(c -> c.endsWith("APPROVED")).count(); }
}

final class MapCardUpdater implements CardUpdater {
    private final Map<String, String> replacements = new HashMap<>();
    private int calls;
    void replace(String oldToken, String newToken) { replacements.put(oldToken, newToken); }
    int calls() { return calls; }
    public Optional<String> refresh(String customerId, String oldCardToken) {
        calls++;
        return Optional.ofNullable(replacements.get(oldCardToken));
    }
}

public final class Main {
    static final Plan BASIC = new Plan("basic", Money.parse("30.00", Currency.USD), Interval.MONTHLY);
    static final Plan PRO = new Plan("pro", Money.parse("50.00", Currency.USD), Interval.MONTHLY);

    public static void main(String[] args) {
        System.out.println("== Anchored billing dates vs chained dates (anchor 2026-01-31) ==");
        LocalDate jan31 = LocalDate.of(2026, 1, 31);
        System.out.println("anchored: " + BillingCycle.anchoredStarts(jan31, Interval.MONTHLY, 6));
        System.out.println("chained:  " + BillingCycle.chainedStarts(jan31, Interval.MONTHLY, 6) + "  <- drifted to the 28th forever");

        System.out.println("\n== Proration: basic 30.00 -> pro 50.00 on 2026-03-11, period [2026-03-01, 2026-04-01) ==");
        ProrationResult p = Proration.compute(BASIC, PRO, LocalDate.of(2026, 3, 1), LocalDate.of(2026, 4, 1), LocalDate.of(2026, 3, 11));
        System.out.printf("used %d / remaining %d / period %d days%n", p.usedDays(), p.remainingDays(), p.periodDays());
        Money[] basicSplit = BASIC.price().allocate(p.usedDays(), p.remainingDays());
        System.out.println("basic split used:unused = " + basicSplit[0] + " + " + basicSplit[1] + " = " + basicSplit[0].plus(basicSplit[1]));
        System.out.println("credit " + p.credit() + ", charge " + p.charge() + ", net due now " + p.net());
        Plan odd = new Plan("odd", Money.parse("9.99", Currency.USD), Interval.MONTHLY);
        Money[] halves = odd.price().allocate(15, 15);
        System.out.println("9.99 split 15:15 days = " + halves[0] + " + " + halves[1] + " (independent half-up rounding would give 5.00 + 5.00 = 10.00)");

        System.out.println("\n== Billing engine: trial, renewals, dunning, card updater ==");
        FakeClock clock = new FakeClock(LocalDate.of(2026, 1, 31));
        ScriptedGateway gateway = new ScriptedGateway();
        MapCardUpdater updater = new MapCardUpdater();
        BillingEngine engine = new BillingEngine(clock, gateway, updater, DunningPolicy.standard());

        gateway.script("tok_flaky", ChargeOutcome.APPROVED, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.APPROVED);
        gateway.script("tok_old", ChargeOutcome.APPROVED, ChargeOutcome.EXPIRED_CARD);
        updater.replace("tok_old", "tok_reissued");
        gateway.script("tok_broke", ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS,
                ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS);

        engine.subscribe("sub_flaky", "cus_a", BASIC, "tok_flaky", 0);
        engine.subscribe("sub_updater", "cus_b", BASIC, "tok_old", 0);
        engine.subscribe("sub_trial", "cus_c", PRO, "tok_broke", 14);
        for (int day = 0; day < 75; day++) {
            engine.runDay();
            engine.runDay();                      // a second run on the same day must be a no-op
            clock.advanceDays(1);
        }
        engine.log().forEach(System.out::println);

        System.out.println("\n== Final state ==");
        for (String id : List.of("sub_flaky", "sub_updater", "sub_trial")) {
            Subscription s = engine.subscription(id);
            System.out.printf("%-12s %-9s card=%-13s next bill %s%n", id, s.status, s.cardToken, s.currentPeriodEnd);
        }
        System.out.println("gateway calls = " + gateway.charges().size() + ", approved = " + gateway.approvedCount());
    }
}
