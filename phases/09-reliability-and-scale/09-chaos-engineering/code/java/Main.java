// Lesson: phases/09-reliability-and-scale/09-chaos-engineering/docs/en.md
// Chaos Engineering and Fault Injection
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Basiri et al., "Chaos Engineering", IEEE Software 2016; principlesofchaos.org; ISO 4217 for Money.
// Java 21, standard library only. Deterministic: simulated clock, seeded Random, in-memory network.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;

/** ISO 4217 currency, copied from Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units + currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public String toString() {
        if (currency.exponent() == 0) return minorUnits + " " + currency.code();
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = String.valueOf(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/** Manual clock. Every "wait" in the simulation is an explicit advance, so demos and tests are deterministic. */
final class SimClock {
    private long nowMillis;

    SimClock(long startMillis) { this.nowMillis = startMillis; }

    long nowMillis() { return nowMillis; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("clock never moves backwards");
        nowMillis += millis;
    }
}

/** The four faults this lesson injects. Each is a value: it can be logged, diffed, and replayed. */
sealed interface Fault permits Fault.Latency, Fault.Error, Fault.Partition, Fault.ClockSkew {
    /** Added one-way delay on a network link. */
    record Latency(long addedMillis) implements Fault {
        public Latency { if (addedMillis < 0) throw new IllegalArgumentException("latency must be >= 0"); }
    }
    /** The callee fails the request with a 5xx-style error. */
    record Error(String code) implements Fault {}
    /** The link is cut: requests never arrive and never return. */
    record Partition() implements Fault {}
    /** The callee's clock runs ahead of (or behind) true time. */
    record ClockSkew(long skewMillis) implements Fault {}
}

/** Who is affected. The whole point of chaos in production is that this is small and explicit. */
sealed interface BlastRadius permits BlastRadius.PercentOfRequests, BlastRadius.Merchants, BlastRadius.Everyone {
    boolean includes(AuthRequest request, Random rng);

    record PercentOfRequests(int percent) implements BlastRadius {
        public PercentOfRequests { if (percent < 0 || percent > 100) throw new IllegalArgumentException("percent 0..100"); }
        public boolean includes(AuthRequest request, Random rng) { return rng.nextInt(100) < percent; }
    }
    record Merchants(Set<String> merchantIds) implements BlastRadius {
        public boolean includes(AuthRequest request, Random rng) { return merchantIds.contains(request.merchantId()); }
    }
    record Everyone() implements BlastRadius {
        public boolean includes(AuthRequest request, Random rng) { return true; }
    }
}

/** A fault bound to a target and a blast radius. */
record Injection(String target, Fault fault, BlastRadius blastRadius) {}

/**
 * The fault-injection layer. Lives between caller and callee, decides per request whether an injection
 * applies, and is the only component with a "clear everything" switch (the abort path depends on it).
 */
final class FaultInjector {
    private final List<Injection> injections = new ArrayList<>();
    private final Random rng;
    private int applied = 0;

    FaultInjector(long seed) { this.rng = new Random(seed); }

    void inject(String target, Fault fault, BlastRadius radius) { injections.add(new Injection(target, fault, radius)); }

    void clear() { injections.clear(); }

    boolean isArmed() { return !injections.isEmpty(); }

    int appliedCount() { return applied; }

    /** Faults whose target and blast radius match this request, in injection order. */
    List<Fault> faultsFor(String target, AuthRequest request) {
        List<Fault> out = new ArrayList<>();
        for (Injection inj : injections) {
            if (inj.target().equals(target) && inj.blastRadius().includes(request, rng)) {
                out.add(inj.fault());
                applied++;
            }
        }
        return out;
    }
}

enum Outcome {
    AUTHORIZED,       // good
    DECLINED,         // good: a legitimate business answer
    TIMEOUT,          // bad: caller gave up
    PROCESSOR_ERROR,  // bad: callee failed
    STALE_REQUEST;    // bad: callee rejected the request timestamp

    boolean isGood() { return this == AUTHORIZED || this == DECLINED; }
}

record AuthRequest(String paymentId, String merchantId, Money amount, String idempotencyKey, long sentAtMillis) {}

record AuthResult(Outcome outcome, long latencyMillis, int attempts) {}

final class ProcessorErrorException extends RuntimeException {
    ProcessorErrorException(String code) { super(code); }
}

final class StaleRequestException extends RuntimeException {
    StaleRequestException(String msg) { super(msg); }
}

/**
 * The downstream card processor. Verifies the request timestamp against ITS clock (a signing window, as
 * real processors do), deduplicates by idempotency key, and declines a seeded 2% of traffic.
 */
final class Processor {
    static final long TIMESTAMP_TOLERANCE_MILLIS = 5 * 60 * 1000L;

    private final SimClock clock;
    private final Random rng;
    private long clockSkewMillis = 0;
    private final Map<String, Outcome> byIdempotencyKey = new HashMap<>();
    private final Map<String, Integer> authorizationsByPayment = new HashMap<>();

    Processor(SimClock clock, long seed) { this.clock = clock; this.rng = new Random(seed); }

    void setClockSkew(long millis) { this.clockSkewMillis = millis; }

    long localNowMillis() { return clock.nowMillis() + clockSkewMillis; }

    Outcome handle(AuthRequest request) {
        long age = Math.abs(localNowMillis() - request.sentAtMillis());
        if (age > TIMESTAMP_TOLERANCE_MILLIS) {
            throw new StaleRequestException("request timestamp outside signing window by " + age + " ms");
        }
        Outcome seen = byIdempotencyKey.get(request.idempotencyKey());
        if (seen != null) return seen;                       // replay: same answer, no second authorization
        Outcome outcome = rng.nextInt(100) < 2 ? Outcome.DECLINED : Outcome.AUTHORIZED;
        byIdempotencyKey.put(request.idempotencyKey(), outcome);
        if (outcome == Outcome.AUTHORIZED) authorizationsByPayment.merge(request.paymentId(), 1, Integer::sum);
        return outcome;
    }

    int authorizationsFor(String paymentId) { return authorizationsByPayment.getOrDefault(paymentId, 0); }

    boolean noDoubleAuthorizations() {
        return authorizationsByPayment.values().stream().allMatch(n -> n <= 1);
    }
}

/**
 * In-memory network between gateway and processor. Computes the round-trip latency from a seeded base
 * plus any injected latency, applies partitions, errors, and clock skew, and advances the shared clock.
 */
final class SimNetwork {
    static final String LINK_GATEWAY_TO_PROCESSOR = "gateway->processor";
    static final String TARGET_PROCESSOR = "processor";

    private final SimClock clock;
    private final FaultInjector injector;
    private final Random rng;

    SimNetwork(SimClock clock, FaultInjector injector, long seed) {
        this.clock = clock; this.injector = injector; this.rng = new Random(seed);
    }

    /** Returns the outcome after advancing the clock by the observed latency, or TIMEOUT after timeoutMillis. */
    Outcome call(AuthRequest request, Processor processor, long timeoutMillis) {
        long latency = 80 + rng.nextInt(41);                 // 80..120 ms baseline
        boolean partitioned = false;
        for (Fault f : injector.faultsFor(LINK_GATEWAY_TO_PROCESSOR, request)) {
            if (f instanceof Fault.Latency l) latency += l.addedMillis();
            if (f instanceof Fault.Partition) partitioned = true;
        }
        if (partitioned || latency > timeoutMillis) {
            clock.advance(timeoutMillis);
            return Outcome.TIMEOUT;
        }
        clock.advance(latency);
        long skew = 0;
        String errorCode = null;
        for (Fault f : injector.faultsFor(TARGET_PROCESSOR, request)) {
            if (f instanceof Fault.ClockSkew s) skew = s.skewMillis();
            if (f instanceof Fault.Error e) errorCode = e.code();
        }
        processor.setClockSkew(skew);
        try {
            if (errorCode != null) throw new ProcessorErrorException(errorCode);
            return processor.handle(request);
        } catch (ProcessorErrorException e) {
            return Outcome.PROCESSOR_ERROR;
        } catch (StaleRequestException e) {
            return Outcome.STALE_REQUEST;
        } finally {
            processor.setClockSkew(0);
        }
    }
}

/** The system under test: times out, retries once with the same idempotency key, never retries a decline. */
final class Gateway {
    static final long TIMEOUT_MILLIS = 2_000;
    static final int MAX_ATTEMPTS = 2;

    private final SimClock clock;
    private final SimNetwork network;
    private final Processor processor;

    Gateway(SimClock clock, SimNetwork network, Processor processor) {
        this.clock = clock; this.network = network; this.processor = processor;
    }

    AuthResult authorize(String paymentId, String merchantId, Money amount) {
        long started = clock.nowMillis();
        String idempotencyKey = "idem-" + paymentId;
        Outcome outcome = Outcome.TIMEOUT;
        int attempts = 0;
        while (attempts < MAX_ATTEMPTS) {
            attempts++;
            AuthRequest request = new AuthRequest(paymentId, merchantId, amount, idempotencyKey, clock.nowMillis());
            outcome = network.call(request, processor, TIMEOUT_MILLIS);
            if (outcome != Outcome.TIMEOUT && outcome != Outcome.PROCESSOR_ERROR) break;
        }
        return new AuthResult(outcome, clock.nowMillis() - started, attempts);
    }
}

/** Seeded traffic: payment ids, ten merchants, amounts between 1.00 and 500.00 USD. */
final class TrafficGenerator {
    private final Random rng;
    private int sequence = 0;
    private final String prefix;

    TrafficGenerator(long seed, String prefix) { this.rng = new Random(seed); this.prefix = prefix; }

    String nextPaymentId() { return prefix + "-" + (++sequence); }

    String nextMerchantId() { return "m-" + rng.nextInt(10); }

    Money nextAmount() { return Money.ofMinor(100 + rng.nextInt(49_901), Currency.USD); }
}

/** Counts outcomes and keeps latency samples; p99 is computed from the sorted samples, never from an average. */
final class Metrics {
    private final Map<Outcome, Integer> counts = new LinkedHashMap<>();
    private final List<Long> latencies = new ArrayList<>();

    void record(AuthResult result) {
        counts.merge(result.outcome(), 1, Integer::sum);
        latencies.add(result.latencyMillis());
    }

    int total() { return latencies.size(); }

    int count(Outcome o) { return counts.getOrDefault(o, 0); }

    /** Good events / all events, in percent, 100.0 when empty. */
    double goodRatePercent() {
        if (latencies.isEmpty()) return 100.0;
        int good = count(Outcome.AUTHORIZED) + count(Outcome.DECLINED);
        return 100.0 * good / latencies.size();
    }

    long p99Millis() {
        if (latencies.isEmpty()) return 0;
        long[] sorted = latencies.stream().mapToLong(Long::longValue).toArray();
        Arrays.sort(sorted);
        int index = (int) Math.ceil(0.99 * sorted.length) - 1;
        return sorted[Math.max(0, index)];
    }

    @Override public String toString() {
        return String.format("n=%d good=%.1f%% p99=%dms auth=%d decl=%d timeout=%d error=%d stale=%d",
                total(), goodRatePercent(), p99Millis(), count(Outcome.AUTHORIZED), count(Outcome.DECLINED),
                count(Outcome.TIMEOUT), count(Outcome.PROCESSOR_ERROR), count(Outcome.STALE_REQUEST));
    }
}

/** What "normal" looks like, as thresholds on measurable SLIs. If you cannot write this, you are not ready to inject. */
record SteadyState(double minGoodRatePercent, long maxP99Millis) {
    boolean holds(Metrics m) { return m.goodRatePercent() >= minGoodRatePercent && m.p99Millis() <= maxP99Millis; }
}

/** The tripwire: evaluated every checkEvery requests on the cumulative experimental metrics. */
record AbortCondition(double minGoodRatePercent, int checkEvery) {
    boolean triggered(Metrics m) { return m.goodRatePercent() < minGoodRatePercent; }
}

record Experiment(String name, String hypothesis, String target, Fault fault, BlastRadius blastRadius,
                  SteadyState steadyState, AbortCondition abort, int requests) {}

/** Everything the GameDay needs to decide what to fix. */
record ExperimentReport(Experiment experiment, Metrics baseline, Metrics experimental, Metrics recovery,
                        boolean baselineHeld, boolean aborted, int requestsBeforeAbort,
                        boolean hypothesisHeld, boolean recovered, boolean noDoubleAuthorizations) {

    String verdict() {
        if (!baselineHeld) return "NOT RUN (steady state not established)";
        if (aborted) return "ABORTED after " + requestsBeforeAbort + " requests";
        return hypothesisHeld ? "HYPOTHESIS HELD" : "HYPOTHESIS VIOLATED";
    }

    String render() {
        StringBuilder sb = new StringBuilder();
        sb.append("Experiment: ").append(experiment.name()).append('\n');
        sb.append("  Hypothesis: ").append(experiment.hypothesis()).append('\n');
        sb.append("  Fault: ").append(experiment.fault()).append(" on ").append(experiment.target())
          .append(", blast radius ").append(experiment.blastRadius()).append('\n');
        sb.append("  Baseline:     ").append(baseline).append('\n');
        sb.append("  Experimental: ").append(experimental).append('\n');
        sb.append("  Recovery:     ").append(recovery).append('\n');
        sb.append("  Invariant (no payment authorized twice): ").append(noDoubleAuthorizations ? "HELD" : "VIOLATED").append('\n');
        sb.append("  Recovered to steady state after clearing faults: ").append(recovered ? "yes" : "NO").append('\n');
        sb.append("  Verdict: ").append(verdict()).append('\n');
        return sb.toString();
    }
}

/** Runs one experiment: baseline, inject, observe with abort checks, clear, verify recovery. */
final class ExperimentRunner {
    private final SimClock clock;
    private final FaultInjector injector;
    private final Gateway gateway;
    private final Processor processor;
    private final TrafficGenerator traffic;

    ExperimentRunner(SimClock clock, FaultInjector injector, Gateway gateway, Processor processor, TrafficGenerator traffic) {
        this.clock = clock; this.injector = injector; this.gateway = gateway; this.processor = processor; this.traffic = traffic;
    }

    private Metrics drive(int requests, AbortCondition abort, int[] stoppedAt) {
        Metrics m = new Metrics();
        for (int i = 1; i <= requests; i++) {
            m.record(gateway.authorize(traffic.nextPaymentId(), traffic.nextMerchantId(), traffic.nextAmount()));
            clock.advance(10);                                // inter-arrival gap
            if (abort != null && i % abort.checkEvery() == 0 && abort.triggered(m)) {
                stoppedAt[0] = i;
                return m;
            }
        }
        stoppedAt[0] = -1;
        return m;
    }

    ExperimentReport run(Experiment e) {
        injector.clear();
        int[] stop = new int[1];
        Metrics baseline = drive(e.requests(), null, stop);
        boolean baselineHeld = e.steadyState().holds(baseline);
        if (!baselineHeld) {
            Metrics empty = new Metrics();
            return new ExperimentReport(e, baseline, empty, empty, false, false, 0, false, false, processor.noDoubleAuthorizations());
        }
        injector.inject(e.target(), e.fault(), e.blastRadius());
        Metrics experimental = drive(e.requests(), e.abort(), stop);
        boolean aborted = stop[0] > 0;
        int requestsBeforeAbort = aborted ? stop[0] : experimental.total();
        injector.clear();                                      // the abort path and the normal path both end here
        Metrics recovery = drive(e.requests(), null, stop);
        boolean hypothesisHeld = !aborted && e.steadyState().holds(experimental);
        return new ExperimentReport(e, baseline, experimental, recovery, true, aborted, requestsBeforeAbort,
                hypothesisHeld, e.steadyState().holds(recovery), processor.noDoubleAuthorizations());
    }
}

/** A GameDay is an ordered list of experiments with a summary at the end. */
final class GameDay {
    private final ExperimentRunner runner;
    private final List<ExperimentReport> reports = new ArrayList<>();

    GameDay(ExperimentRunner runner) { this.runner = runner; }

    ExperimentReport run(Experiment e) {
        ExperimentReport r = runner.run(e);
        reports.add(r);
        return r;
    }

    List<ExperimentReport> reports() { return reports; }

    String summary() {
        StringBuilder sb = new StringBuilder("GameDay summary\n");
        for (ExperimentReport r : reports) {
            sb.append(String.format("  %-44s %s%n", r.experiment().name(), r.verdict()));
        }
        return sb.toString();
    }
}

public final class Main {
    static final SteadyState STEADY = new SteadyState(99.0, 1_500);
    static final AbortCondition ABORT = new AbortCondition(80.0, 50);

    static Experiment experiment(String name, String hypothesis, String target, Fault fault, BlastRadius radius) {
        return new Experiment(name, hypothesis, target, fault, radius, STEADY, ABORT, 500);
    }

    public static void main(String[] args) {
        SimClock clock = new SimClock(1_700_000_000_000L);
        FaultInjector injector = new FaultInjector(7);
        Processor processor = new Processor(clock, 11);
        SimNetwork network = new SimNetwork(clock, injector, 13);
        Gateway gateway = new Gateway(clock, network, processor);
        TrafficGenerator traffic = new TrafficGenerator(17, "pay");
        GameDay day = new GameDay(new ExperimentRunner(clock, injector, gateway, processor, traffic));

        System.out.println("Steady state: good rate >= " + STEADY.minGoodRatePercent() + "%, p99 <= " + STEADY.maxP99Millis() + " ms");
        System.out.println("Abort if good rate < " + ABORT.minGoodRatePercent() + "% at any " + ABORT.checkEvery() + "-request checkpoint\n");

        List<Experiment> plan = List.of(
                experiment("latency +300ms, 10% of requests",
                        "A 300 ms slower processor stays inside the 2 s timeout; p99 rises but the SLO holds",
                        SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(300), new BlastRadius.PercentOfRequests(10)),
                experiment("latency +2500ms, 10% of requests",
                        "Requests beyond the timeout are retried once with the same idempotency key; no double authorization",
                        SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(2_500), new BlastRadius.PercentOfRequests(10)),
                experiment("processor 500s, 70% of requests",
                        "One retry absorbs 5xx bursts; the abort tripwire fires before the whole fleet is affected",
                        SimNetwork.TARGET_PROCESSOR, new Fault.Error("HTTP_500"), new BlastRadius.PercentOfRequests(70)),
                experiment("partition, canary merchant m-3",
                        "A cut link fails fast for the canary merchant and leaves every other merchant inside the SLO",
                        SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Partition(), new BlastRadius.Merchants(Set.of("m-3"))),
                experiment("processor clock +6 minutes, everyone",
                        "Six minutes of skew is inside what NTP drift could produce; the processor should still accept requests",
                        SimNetwork.TARGET_PROCESSOR, new Fault.ClockSkew(6 * 60 * 1000L), new BlastRadius.Everyone()));

        for (Experiment e : plan) System.out.println(day.run(e).render());
        System.out.print(day.summary());
        System.out.println("\nFaults applied in total: " + injector.appliedCount() + "; injector armed now: " + injector.isArmed());
    }
}
