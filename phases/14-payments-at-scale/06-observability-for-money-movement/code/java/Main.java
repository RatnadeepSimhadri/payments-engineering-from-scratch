// Lesson: phases/14-payments-at-scale/06-observability-for-money-movement/docs/en.md
// Observability for Money Movement
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Google SRE Workbook ch. 5 "Alerting on SLOs" (multiwindow, multi-burn-rate alerts);
// Phase 9 Lessons 01 and 06 (SLOs, metrics). Java 21, standard library only. Money is integer minor units.

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.TreeMap;
import java.util.TreeSet;

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

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
// Time is injected. Nothing in this file reads the wall clock.
// ---------------------------------------------------------------------------------------------

interface Clock { Instant now(); }

final class ManualClock implements Clock {
    private Instant now;

    ManualClock(Instant start) { this.now = start; }

    @Override public Instant now() { return now; }

    void advance(Duration d) {
        if (d.isNegative()) throw new IllegalArgumentException("time does not go backwards");
        now = now.plus(d);
    }
}

enum PaymentState {
    CREATED, AUTHORIZED, CAPTURE_PENDING, CAPTURED, SETTLED, FAILED;

    boolean terminal() { return this == SETTLED || this == FAILED; }
}

/** The three outcomes an availability SLI must tell apart: only PROCESSOR_ERROR is our failure. */
enum AuthOutcome { APPROVED, ISSUER_DECLINE, PROCESSOR_ERROR }

enum Severity { PAGE, TICKET }

record Alert(Severity severity, String name, String detail) {}

record StuckPayment(String paymentId, String processor, PaymentState state, Duration age, Money amount) {}

/**
 * Counts per minute bucket, keyed by epoch minute. A window query sums the buckets whose minute is in
 * (now - window, now]. Old buckets are dropped once they fall outside the longest window we query.
 */
final class MinuteCounter {
    private final TreeMap<Long, long[]> buckets = new TreeMap<>();
    private final int width;

    MinuteCounter(int width) { this.width = width; }

    void add(Instant at, int slot, long n) {
        long minute = Math.floorDiv(at.getEpochSecond(), 60);
        buckets.computeIfAbsent(minute, k -> new long[width])[slot] += n;
    }

    long[] sum(Instant now, Duration window) {
        long nowMinute = Math.floorDiv(now.getEpochSecond(), 60);
        long from = nowMinute - window.toMinutes() + 1;
        long[] total = new long[width];
        for (long[] counts : buckets.subMap(from, true, nowMinute, true).values()) {
            for (int i = 0; i < width; i++) total[i] += counts[i];
        }
        return total;
    }

    /** True when the counter has data reaching back at least the whole window. */
    boolean covers(Instant now, Duration window) {
        if (buckets.isEmpty()) return false;
        long nowMinute = Math.floorDiv(now.getEpochSecond(), 60);
        return buckets.firstKey() <= nowMinute - window.toMinutes() + 1;
    }

    void prune(Instant now, Duration keep) {
        long cutoff = Math.floorDiv(now.getEpochSecond(), 60) - keep.toMinutes();
        buckets.headMap(cutoff).clear();
    }
}

/** Integer basis-point ratio: 10,000 * num / den, rounded down. -1 when there is no data. */
final class Ratio {
    private Ratio() {}

    static long bps(long numerator, long denominator) {
        return denominator == 0 ? -1 : Math.multiplyExact(numerator, 10_000L) / denominator;
    }

    static String pct(long bps) {
        if (bps < 0) return "n/a";
        return String.format("%d.%02d%%", bps / 100, bps % 100);
    }
}

/**
 * Burn rate for an SLO with error budget given in parts per million (99.9% SLO = 1,000 ppm budget).
 * Returned x100 so that 14.4 is 1440 and every comparison is an exact integer comparison.
 */
record Slo(String name, long budgetPpm) {
    long burnRateX100(long bad, long total) {
        if (total == 0) return 0;
        // (bad / total) / (budgetPpm / 1e6) * 100 = bad * 1e8 / (total * budgetPpm)
        return Math.multiplyExact(bad, 100_000_000L) / Math.multiplyExact(total, budgetPpm);
    }
}

/** One burn-rate rule from the SRE Workbook table: both windows must exceed the threshold. */
record BurnRule(Severity severity, Duration longWindow, Duration shortWindow, long thresholdX100) {}

final class MoneyMovementMonitor {
    private record Tracked(String processor, Money amount, PaymentState state, Instant enteredAt) {}

    static final List<BurnRule> DEFAULT_BURN_RULES = List.of(
            new BurnRule(Severity.PAGE, Duration.ofHours(1), Duration.ofMinutes(5), 1440),     // 2% of 30-day budget in 1h
            new BurnRule(Severity.PAGE, Duration.ofHours(6), Duration.ofMinutes(30), 600),     // 5% in 6h
            new BurnRule(Severity.TICKET, Duration.ofHours(24), Duration.ofHours(2), 300),     // 10% in 1d
            new BurnRule(Severity.TICKET, Duration.ofHours(72), Duration.ofHours(6), 100));    // 10% in 3d

    static Map<PaymentState, Duration> defaultStuckAfter() {
        Map<PaymentState, Duration> m = new EnumMap<>(PaymentState.class);
        m.put(PaymentState.CREATED, Duration.ofMinutes(30));          // abandoned checkout or lost auth response
        m.put(PaymentState.AUTHORIZED, Duration.ofDays(6));            // capture before the auth expires (~7 days)
        m.put(PaymentState.CAPTURE_PENDING, Duration.ofMinutes(15));   // capture sent, no answer
        m.put(PaymentState.CAPTURED, Duration.ofDays(3));              // should be in a settlement file by now
        return m;
    }

    private final Clock clock;
    private final Currency currency;
    private final Map<PaymentState, Duration> stuckAfter;
    private final Slo slo;
    private final List<BurnRule> burnRules;
    private final Money stuckPageThreshold;
    private final long authDropBps;
    private final long minAttemptsForRate;
    private final long reconBreakTicketBps;

    private final Map<String, Tracked> payments = new HashMap<>();
    private final Map<String, MinuteCounter> authByProcessor = new TreeMap<>();
    private final MinuteCounter authAll = new MinuteCounter(AuthOutcome.values().length);
    private final List<long[]> reconRuns = new ArrayList<>();          // {matched, breaks}

    MoneyMovementMonitor(Clock clock, Currency currency) {
        this(clock, currency, defaultStuckAfter(), new Slo("auth availability 99.9%", 1_000), DEFAULT_BURN_RULES,
                Money.ofMinor(100_000, currency), 500, 100, 50);
    }

    MoneyMovementMonitor(Clock clock, Currency currency, Map<PaymentState, Duration> stuckAfter, Slo slo, List<BurnRule> burnRules,
                         Money stuckPageThreshold, long authDropBps, long minAttemptsForRate, long reconBreakTicketBps) {
        this.clock = clock;
        this.currency = currency;
        this.stuckAfter = stuckAfter;
        this.slo = slo;
        this.burnRules = burnRules;
        this.stuckPageThreshold = stuckPageThreshold;
        this.authDropBps = authDropBps;
        this.minAttemptsForRate = minAttemptsForRate;
        this.reconBreakTicketBps = reconBreakTicketBps;
    }

    // ---- inputs -------------------------------------------------------------------------------

    /** Record that a payment entered a state now. Terminal states stop tracking the payment. */
    void transition(String paymentId, String processor, Money amount, PaymentState to) {
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
        if (to.terminal()) payments.remove(paymentId);
        else payments.put(paymentId, new Tracked(processor, amount, to, clock.now()));
    }

    void authResult(String processor, AuthOutcome outcome) {
        authByProcessor.computeIfAbsent(processor, k -> new MinuteCounter(AuthOutcome.values().length))
                .add(clock.now(), outcome.ordinal(), 1);
        authAll.add(clock.now(), outcome.ordinal(), 1);
    }

    void reconRun(long matched, long breaks) { reconRuns.add(new long[] {matched, breaks}); }

    // ---- metrics ------------------------------------------------------------------------------

    Duration ageOf(String paymentId) {
        Tracked t = payments.get(paymentId);
        return t == null ? null : Duration.between(t.enteredAt(), clock.now());
    }

    /** Payments whose time in their current state exceeds that state's limit, oldest first. */
    List<StuckPayment> stuck() {
        List<StuckPayment> out = new ArrayList<>();
        for (Map.Entry<String, Tracked> e : payments.entrySet()) {
            Tracked t = e.getValue();
            Duration age = Duration.between(t.enteredAt(), clock.now());
            Duration limit = stuckAfter.get(t.state());
            if (limit != null && age.compareTo(limit) > 0) {
                out.add(new StuckPayment(e.getKey(), t.processor(), t.state(), age, t.amount()));
            }
        }
        out.sort(Comparator.comparing(StuckPayment::age).reversed().thenComparing(StuckPayment::paymentId));
        return out;
    }

    Money stuckExposure() {
        Money total = Money.zero(currency);
        for (StuckPayment s : stuck()) total = total.plus(s.amount());
        return total;
    }

    /** Money in every non-terminal state: what is committed but not yet final. */
    Map<PaymentState, Money> inFlightExposure() {
        Map<PaymentState, Money> out = new EnumMap<>(PaymentState.class);
        for (PaymentState s : PaymentState.values()) if (!s.terminal()) out.put(s, Money.zero(currency));
        for (Tracked t : payments.values()) out.merge(t.state(), t.amount(), Money::plus);
        return out;
    }

    Money totalInFlight() {
        Money total = Money.zero(currency);
        for (Money m : inFlightExposure().values()) total = total.plus(m);
        return total;
    }

    /** Histogram of payments by state and age band: the shape of the queue, not just its length. */
    static final long[] AGE_BANDS_MINUTES = {1, 5, 15, 60, 24 * 60};

    Map<PaymentState, long[]> stateAgeHistogram() {
        Map<PaymentState, long[]> out = new EnumMap<>(PaymentState.class);
        for (PaymentState s : PaymentState.values()) if (!s.terminal()) out.put(s, new long[AGE_BANDS_MINUTES.length + 1]);
        for (Tracked t : payments.values()) {
            long minutes = Duration.between(t.enteredAt(), clock.now()).toMinutes();
            int band = 0;
            while (band < AGE_BANDS_MINUTES.length && minutes >= AGE_BANDS_MINUTES[band]) band++;
            out.get(t.state())[band]++;
        }
        return out;
    }

    /** Approvals over all attempts, in basis points; -1 if fewer than minAttemptsForRate attempts. */
    long authRateBps(String processor, Duration window) {
        long[] c = counts(processor, window);
        long total = c[0] + c[1] + c[2];
        return total < minAttemptsForRate ? -1 : Ratio.bps(c[AuthOutcome.APPROVED.ordinal()], total);
    }

    long attempts(String processor, Duration window) {
        long[] c = counts(processor, window);
        return c[0] + c[1] + c[2];
    }

    private long[] counts(String processor, Duration window) {
        if (processor == null) return authAll.sum(clock.now(), window);
        MinuteCounter mc = authByProcessor.get(processor);
        return mc == null ? new long[3] : mc.sum(clock.now(), window);
    }

    /** Availability SLI burn rate: processor errors are bad, issuer declines are good events. */
    long burnRateX100(Duration window) {
        long[] c = authAll.sum(clock.now(), window);
        return slo.burnRateX100(c[AuthOutcome.PROCESSOR_ERROR.ordinal()], c[0] + c[1] + c[2]);
    }

    /** Breaks over records across the last n reconciliation runs, in basis points. */
    long reconBreakRateBps(int lastRuns) {
        long matched = 0, breaks = 0;
        for (int i = Math.max(0, reconRuns.size() - lastRuns); i < reconRuns.size(); i++) {
            matched += reconRuns.get(i)[0];
            breaks += reconRuns.get(i)[1];
        }
        return Ratio.bps(breaks, matched + breaks);
    }

    boolean burnRuleArmed(BurnRule r) { return authAll.covers(clock.now(), r.longWindow()); }

    Iterable<String> processors() { return authByProcessor.keySet(); }

    // ---- alerts -------------------------------------------------------------------------------

    List<Alert> evaluateAlerts() {
        List<Alert> out = new ArrayList<>();
        for (BurnRule r : burnRules) {
            if (!authAll.covers(clock.now(), r.longWindow())) continue; // a half-empty long window is a short window
            long lng = burnRateX100(r.longWindow()), shrt = burnRateX100(r.shortWindow());
            if (lng >= r.thresholdX100() && shrt >= r.thresholdX100()) {
                out.add(new Alert(r.severity(), "slo-burn-" + r.longWindow().toMinutes() + "m",
                        String.format("%s burning %s over %dm and %s over %dm (threshold %s)", slo.name(), x100(lng),
                                r.longWindow().toMinutes(), x100(shrt), r.shortWindow().toMinutes(), x100(r.thresholdX100()))));
            }
        }
        List<StuckPayment> stuck = stuck();
        if (!stuck.isEmpty()) {
            Money exposure = stuckExposure();
            Severity sev = exposure.compareTo(stuckPageThreshold) >= 0 ? Severity.PAGE : Severity.TICKET;
            StuckPayment oldest = stuck.get(0);
            out.add(new Alert(sev, "stuck-payments", stuck.size() + " stuck, " + exposure + "; oldest " + oldest.paymentId()
                    + " in " + oldest.state() + " for " + oldest.age().toMinutes() + "m"));
        }
        long fleet = authRateBps(null, Duration.ofMinutes(15));
        for (String p : processors()) {
            long rate = authRateBps(p, Duration.ofMinutes(15));
            if (rate >= 0 && fleet >= 0 && fleet - rate >= authDropBps) {
                out.add(new Alert(Severity.PAGE, "auth-rate-drop:" + p,
                        p + " approves " + Ratio.pct(rate) + " vs fleet " + Ratio.pct(fleet) + " over 15m"));
            }
        }
        long recon = reconBreakRateBps(4);
        if (recon >= reconBreakTicketBps) {
            out.add(new Alert(Severity.TICKET, "recon-break-rate", "break rate " + Ratio.pct(recon) + " over last 4 runs"));
        }
        return out;
    }

    static String x100(long v) { return String.format("%d.%02d", v / 100, v % 100); }

    // ---- dashboard ----------------------------------------------------------------------------

    String dashboard() {
        StringBuilder sb = new StringBuilder();
        sb.append("MONEY MOVEMENT  ").append(clock.now()).append('\n');
        sb.append(String.format("in flight %s%n", totalInFlight()));
        Map<PaymentState, long[]> hist = stateAgeHistogram();
        sb.append(String.format("  %-16s %-14s %6s %6s %6s %6s %6s %6s%n", "state", "amount", "<1m", "1-5m", "5-15m", "15-60m", "1-24h", ">24h"));
        for (Map.Entry<PaymentState, Money> e : inFlightExposure().entrySet()) {
            long[] h = hist.get(e.getKey());
            sb.append(String.format("  %-16s %-14s %6d %6d %6d %6d %6d %6d%n", e.getKey(), e.getValue(), h[0], h[1], h[2], h[3], h[4], h[5]));
        }
        sb.append(String.format("auth rate        %-10s %-10s %-10s%n", "5m", "1h", "attempts 1h"));
        for (String p : processors()) {
            sb.append(String.format("  %-14s %-10s %-10s %d%n", p, Ratio.pct(authRateBps(p, Duration.ofMinutes(5))),
                    Ratio.pct(authRateBps(p, Duration.ofHours(1))), attempts(p, Duration.ofHours(1))));
        }
        sb.append(String.format("slo burn         5m %s  1h %s  6h %s%n", x100(burnRateX100(Duration.ofMinutes(5))),
                x100(burnRateX100(Duration.ofHours(1))), x100(burnRateX100(Duration.ofHours(6)))));
        sb.append("recon breaks     ").append(Ratio.pct(reconBreakRateBps(4))).append(" (last 4 runs)\n");
        List<Alert> alerts = evaluateAlerts();
        if (alerts.isEmpty()) sb.append("alerts           none\n");
        for (Alert a : alerts) sb.append("ALERT ").append(a.severity()).append(' ').append(a.name()).append(": ").append(a.detail()).append('\n');
        return sb.toString();
    }
}

/**
 * A seeded eight-hour day from 06:00: two processors, 40 attempts a minute, an hourly settlement run,
 * three lost capture webhooks at 12:00, and a partial outage on proc_b from 13:00. Records the first
 * minute each alert fires. Six quiet hours come first so the 6h burn window has full coverage.
 */
final class Simulation {
    final ManualClock clock;
    final MoneyMovementMonitor monitor;
    final Map<String, Integer> firstFired = new LinkedHashMap<>();
    final Map<Integer, String> snapshots = new TreeMap<>();
    private final Random rnd;
    private final Map<Integer, List<Runnable>> scheduled = new HashMap<>();
    private long reconMatched = 0, reconBreaks = 0;
    private int seq = 0;

    static final Instant START = Instant.parse("2026-03-31T06:00:00Z");
    static final int MINUTES = 480;
    static final int LOST_WEBHOOK_MINUTE = 360;
    static final int OUTAGE_MINUTE = 420;
    private final Map<String, Money> captured = new LinkedHashMap<>();

    Simulation(long seed) {
        this.rnd = new Random(seed);
        this.clock = new ManualClock(START);
        this.monitor = new MoneyMovementMonitor(clock, Currency.USD);
    }

    private void at(int minute, Runnable r) { scheduled.computeIfAbsent(minute, k -> new ArrayList<>()).add(r); }

    void run(int minutes, TreeSet<Integer> snapshotMinutes) {
        for (int minute = 0; minute < minutes; minute++) {
            for (String processor : List.of("proc_a", "proc_b")) {
                for (int i = 0; i < 20; i++) attempt(minute, processor);
            }
            if (minute == LOST_WEBHOOK_MINUTE) {
                for (long amount : new long[] {40_000, 35_000, 30_000}) {
                    String id = String.format("pay_lost_%d", amount);
                    monitor.transition(id, "proc_a", Money.ofMinor(amount, Currency.USD), PaymentState.CAPTURE_PENDING);
                }
            }
            List<Runnable> due = scheduled.remove(minute);
            if (due != null) due.forEach(Runnable::run);
            if (minute % 60 == 59) {
                // hourly settlement file in this simulation: everything captured so far settles
                captured.forEach((id, amt) -> monitor.transition(id, "settle", amt, PaymentState.SETTLED));
                captured.clear();
            }
            if (minute % 30 == 29) {
                monitor.reconRun(reconMatched, reconBreaks);
                reconMatched = 0;
                reconBreaks = 0;
            }
            for (Alert a : monitor.evaluateAlerts()) firstFired.putIfAbsent(a.name(), minute);
            if (snapshotMinutes.contains(minute)) snapshots.put(minute, monitor.dashboard());
            clock.advance(Duration.ofMinutes(1));
        }
    }

    private void attempt(int minute, String processor) {
        String id = String.format("pay_%06d", seq++);
        Money amount = Money.ofMinor(500 + rnd.nextInt(49_500), Currency.USD);
        double u = rnd.nextDouble();
        boolean degraded = processor.equals("proc_b") && minute >= OUTAGE_MINUTE;
        double errorP = degraded ? 0.25 : 0.0005;
        AuthOutcome outcome = u < errorP ? AuthOutcome.PROCESSOR_ERROR : u < errorP + 0.08 ? AuthOutcome.ISSUER_DECLINE : AuthOutcome.APPROVED;
        monitor.transition(id, processor, amount, PaymentState.CREATED);
        monitor.authResult(processor, outcome);
        switch (outcome) {
            case APPROVED -> {
                monitor.transition(id, processor, amount, PaymentState.AUTHORIZED);
                at(minute + 1, () -> monitor.transition(id, processor, amount, PaymentState.CAPTURE_PENDING));
                at(minute + 2, () -> {
                    monitor.transition(id, processor, amount, PaymentState.CAPTURED);
                    captured.put(id, amount);
                    reconMatched++;
                });
            }
            case ISSUER_DECLINE -> monitor.transition(id, processor, amount, PaymentState.FAILED);
            case PROCESSOR_ERROR -> {
                monitor.transition(id, processor, amount, PaymentState.FAILED);
                // A timeout is not a decline: one in five was actually approved at the processor,
                // which reconciliation later reports as a capture we do not have.
                if (rnd.nextInt(5) == 0) reconBreaks++;
            }
        }
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Burn rate by hand: 99.9% SLO, budget 1,000 ppm ==");
        Slo slo = new Slo("auth availability 99.9%", 1_000);
        System.out.println("5m: 6 errors / 200 attempts   -> burn " + MoneyMovementMonitor.x100(slo.burnRateX100(6, 200)));
        System.out.println("1h: 40 errors / 2,400 attempts -> burn " + MoneyMovementMonitor.x100(slo.burnRateX100(40, 2_400)));
        System.out.println("page rule needs both >= 14.40: fires");

        System.out.println("\n== Eight hours of traffic, seed 42 ==");
        Simulation sim = new Simulation(42);
        sim.run(Simulation.MINUTES, new TreeSet<>(List.of(359, 380, 430)));
        System.out.println("lost capture webhooks injected at minute " + Simulation.LOST_WEBHOOK_MINUTE
                + "; proc_b outage starts at minute " + Simulation.OUTAGE_MINUTE);
        sim.firstFired.forEach((name, minute) -> System.out.printf("  first fired %-22s minute %3d (%s)%n", name, minute,
                Simulation.START.plusSeconds(60L * minute).toString().substring(11, 16)));
        for (Map.Entry<Integer, String> e : sim.snapshots.entrySet()) {
            System.out.println("\n-- dashboard at minute " + e.getKey() + " --");
            System.out.print(e.getValue());
        }
    }
}
