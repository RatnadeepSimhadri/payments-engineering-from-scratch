// Lesson: phases/12-payment-systems-design/04-smart-routing-and-retries/docs/en.md
// Smart Routing, Cascading, and Retry Policies
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 8583 response codes (field 39); Mastercard Merchant Advice Codes; Visa excessive reattempt rules;
//          Google SRE book ch. 22 (retry budgets); Nygard, "Release It!" (circuit breaker). Java 21, stdlib only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.Deque;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.function.BiFunction;

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
// Time is injected so the circuit breaker's cool-down is driven by the test, never by sleep().
// ---------------------------------------------------------------------------------------------

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long startMillis) { now = startMillis; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) { now += millis; }
}

// ---------------------------------------------------------------------------------------------
// The request and the processors we can send it to.
// ---------------------------------------------------------------------------------------------

enum CardNetwork { VISA, MASTERCARD, AMEX }

record PaymentRequest(String id, Money amount, CardNetwork network, String cardFingerprint) {
    PaymentRequest {
        Objects.requireNonNull(id, "id");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
        Objects.requireNonNull(network, "network");
    }
}

/** A processor's static terms: what it accepts and what it charges (basis points plus a fixed fee). */
record ProcessorConfig(String id, Set<CardNetwork> networks, Set<Currency> currencies, long feeBps, long fixedFeeMinor) {
    ProcessorConfig {
        networks = Set.copyOf(networks);
        currencies = Set.copyOf(currencies);
        if (feeBps < 0 || fixedFeeMinor < 0) throw new IllegalArgumentException("fees cannot be negative");
    }

    boolean accepts(PaymentRequest r) { return networks.contains(r.network()) && currencies.contains(r.amount().currency()); }

    /** Fee in minor units: amount * bps / 10000 rounded half-up, plus the fixed component. Integer arithmetic only. */
    Money fee(Money amount) {
        long variable = Math.floorDiv(Math.addExact(Math.multiplyExact(amount.minorUnits(), feeBps), 5_000L), 10_000L);
        return Money.ofMinor(variable + fixedFeeMinor, amount.currency());
    }
}

// ---------------------------------------------------------------------------------------------
// Decline classification. The response code decides whether another processor could change the
// answer. Only SOFT_CASCADE may be cascaded; everything else stops the attempt chain.
// ---------------------------------------------------------------------------------------------

enum DeclineClass {
    APPROVED,          // 00
    SOFT_CASCADE,      // the path failed, not the card: another acquirer may succeed now
    SOFT_RETRY_LATER,  // the issuer said no for now (funds, limits): same answer anywhere today
    AUTH_REQUIRED,     // the issuer wants strong customer authentication: step up, do not cascade
    HARD,              // the card or the cardholder is the problem: never retry
    AMBIGUOUS          // we do not know whether it was authorized: reconcile before anything else
}

record DeclineRule(String code, String meaning, DeclineClass decline) {}

record ProcessorResponse(String code, String merchantAdviceCode) {
    static final String CONNECT_ERROR = "CONNECT_ERROR";
    static final String TIMEOUT = "TIMEOUT";

    static ProcessorResponse approved() { return new ProcessorResponse("00", null); }
    static ProcessorResponse of(String code) { return new ProcessorResponse(code, null); }
    static ProcessorResponse of(String code, String mac) { return new ProcessorResponse(code, mac); }
    static ProcessorResponse connectError() { return new ProcessorResponse(CONNECT_ERROR, null); }
    static ProcessorResponse timeout() { return new ProcessorResponse(TIMEOUT, null); }

    /** Technical outcomes say something about the processor's health, not about the issuer's decision. */
    boolean technical() { return code.equals(CONNECT_ERROR) || code.equals(TIMEOUT); }
}

final class DeclineCodes {
    static final Map<String, DeclineRule> TABLE = new LinkedHashMap<>();

    private static void rule(String code, String meaning, DeclineClass c) { TABLE.put(code, new DeclineRule(code, meaning, c)); }

    static {
        rule("00", "Approved", DeclineClass.APPROVED);
        rule("05", "Do not honor (generic)", DeclineClass.SOFT_CASCADE);
        rule("06", "Error", DeclineClass.SOFT_CASCADE);
        rule("91", "Issuer or switch inoperative", DeclineClass.SOFT_CASCADE);
        rule("96", "System malfunction", DeclineClass.SOFT_CASCADE);
        rule(ProcessorResponse.CONNECT_ERROR, "Request never reached the processor", DeclineClass.SOFT_CASCADE);
        rule("51", "Insufficient funds", DeclineClass.SOFT_RETRY_LATER);
        rule("61", "Exceeds withdrawal amount limit", DeclineClass.SOFT_RETRY_LATER);
        rule("1A", "Additional customer authentication required", DeclineClass.AUTH_REQUIRED);
        rule("65", "Exceeds frequency limit / SCA required (EEA)", DeclineClass.AUTH_REQUIRED);
        rule("14", "Invalid card number", DeclineClass.HARD);
        rule("41", "Lost card, pick up", DeclineClass.HARD);
        rule("43", "Stolen card, pick up", DeclineClass.HARD);
        rule("54", "Expired card", DeclineClass.HARD);
        rule("57", "Transaction not permitted to cardholder", DeclineClass.HARD);
        rule("59", "Suspected fraud", DeclineClass.HARD);
        rule("62", "Restricted card", DeclineClass.HARD);
        rule("N7", "CVV2 mismatch", DeclineClass.HARD);
        rule(ProcessorResponse.TIMEOUT, "No response; authorization may exist", DeclineClass.AMBIGUOUS);
    }

    /**
     * Classify a response. Two overrides come before the table: a Mastercard Merchant Advice Code of
     * 03 ("do not try again") or 21 ("payment cancellation") makes any decline HARD, and an unknown
     * code is HARD because retrying what you do not understand is how a merchant gets fined.
     */
    static DeclineClass classify(ProcessorResponse r) {
        DeclineRule rule = TABLE.get(r.code());
        if (rule != null && rule.decline() == DeclineClass.APPROVED) return DeclineClass.APPROVED;
        if ("03".equals(r.merchantAdviceCode()) || "21".equals(r.merchantAdviceCode())) return DeclineClass.HARD;
        return rule == null ? DeclineClass.HARD : rule.decline();
    }

    static String meaning(String code) {
        DeclineRule rule = TABLE.get(code);
        return rule == null ? "Unknown code" : rule.meaning();
    }
}

// ---------------------------------------------------------------------------------------------
// Authorization-rate estimates: a Beta prior plus exponentially decayed counts per (processor,
// network). Recent outcomes weigh more, so a processor that degrades is noticed within hours.
// The estimate is a probability used to rank; it is never an amount of money.
// ---------------------------------------------------------------------------------------------

final class AuthRateEstimator {
    private record Counts(double approvals, double attempts) {}

    private final double priorRate;
    private final double priorWeight;
    private final double decay;
    private final Map<String, Counts> counts = new HashMap<>();

    AuthRateEstimator(double priorRate, double priorWeight, double decay) {
        if (priorRate <= 0 || priorRate >= 1) throw new IllegalArgumentException("priorRate must be in (0,1)");
        if (decay <= 0 || decay > 1) throw new IllegalArgumentException("decay must be in (0,1]");
        this.priorRate = priorRate;
        this.priorWeight = priorWeight;
        this.decay = decay;
    }

    private static String key(String processorId, CardNetwork network) { return processorId + "/" + network; }

    /** (decayed approvals + prior pseudo-approvals) / (decayed attempts + prior weight). */
    double estimate(String processorId, CardNetwork network) {
        Counts c = counts.getOrDefault(key(processorId, network), new Counts(0, 0));
        return (c.approvals() + priorRate * priorWeight) / (c.attempts() + priorWeight);
    }

    /** Record one issuer decision. Technical failures are health signals and must not be recorded here. */
    void record(String processorId, CardNetwork network, boolean approved) {
        Counts c = counts.getOrDefault(key(processorId, network), new Counts(0, 0));
        counts.put(key(processorId, network),
                new Counts(c.approvals() * decay + (approved ? 1 : 0), c.attempts() * decay + 1));
    }

    double observations(String processorId, CardNetwork network) {
        return counts.getOrDefault(key(processorId, network), new Counts(0, 0)).attempts();
    }
}

// ---------------------------------------------------------------------------------------------
// Processor health: a circuit breaker over the last N technical outcomes. OPEN removes the
// processor from routing until the cool-down passes; HALF_OPEN lets one request probe it.
// ---------------------------------------------------------------------------------------------

enum CircuitState { CLOSED, OPEN, HALF_OPEN }

final class ProcessorHealth {
    private final Clock clock;
    private final int windowSize;
    private final int failureThreshold;
    private final long coolDownMillis;
    private final Deque<Boolean> window = new ArrayDeque<>();
    private CircuitState state = CircuitState.CLOSED;
    private long openedAtMillis;

    ProcessorHealth(Clock clock, int windowSize, int failureThreshold, long coolDownMillis) {
        this.clock = clock;
        this.windowSize = windowSize;
        this.failureThreshold = failureThreshold;
        this.coolDownMillis = coolDownMillis;
    }

    CircuitState state() {
        if (state == CircuitState.OPEN && clock.nowMillis() - openedAtMillis >= coolDownMillis) state = CircuitState.HALF_OPEN;
        return state;
    }

    boolean allows() { return state() != CircuitState.OPEN; }

    void recordSuccess() {
        if (state() == CircuitState.HALF_OPEN) { state = CircuitState.CLOSED; window.clear(); }
        push(false);
    }

    void recordFailure() {
        if (state() == CircuitState.HALF_OPEN) { trip(); return; }
        push(true);
        long failures = window.stream().filter(b -> b).count();
        if (failures >= failureThreshold) trip();
    }

    private void trip() { state = CircuitState.OPEN; openedAtMillis = clock.nowMillis(); window.clear(); }

    private void push(boolean failure) {
        window.addLast(failure);
        while (window.size() > windowSize) window.removeFirst();
    }
}

// ---------------------------------------------------------------------------------------------
// Retry budget: cascades are paid for by first attempts. Each first attempt deposits a fraction
// of a token; each cascade spends a whole one. When an outage turns every attempt into a soft
// decline, the budget runs dry and the router stops multiplying load on a struggling network.
// ---------------------------------------------------------------------------------------------

final class RetryBudget {
    static final long TOKEN = 1_000;          // milli-tokens per retry
    private final long depositPerAttempt;      // milli-tokens earned per first attempt (100 = 10% retry ratio)
    private final long capacity;
    private long balance;

    RetryBudget(long depositPerAttempt, long capacity, long initial) {
        this.depositPerAttempt = depositPerAttempt;
        this.capacity = capacity;
        this.balance = Math.min(initial, capacity);
    }

    void recordFirstAttempt() { balance = Math.min(capacity, balance + depositPerAttempt); }

    boolean tryAcquireRetry() {
        if (balance < TOKEN) return false;
        balance -= TOKEN;
        return true;
    }

    long balanceMilli() { return balance; }
}

// ---------------------------------------------------------------------------------------------
// The gateway is the network boundary. Scripted for tests, simulated with a seeded Random for the
// learning demo. Nothing here sleeps or opens a socket.
// ---------------------------------------------------------------------------------------------

interface ProcessorGateway { ProcessorResponse authorize(String processorId, PaymentRequest request); }

final class ScriptedGateway implements ProcessorGateway {
    private final Map<String, Deque<ProcessorResponse>> scripts = new HashMap<>();
    private final List<String> calls = new ArrayList<>();

    ScriptedGateway script(String processorId, ProcessorResponse... responses) {
        scripts.computeIfAbsent(processorId, k -> new ArrayDeque<>()).addAll(List.of(responses));
        return this;
    }

    List<String> calls() { return Collections.unmodifiableList(calls); }

    @Override public ProcessorResponse authorize(String processorId, PaymentRequest request) {
        calls.add(processorId + ":" + request.id());
        Deque<ProcessorResponse> q = scripts.get(processorId);
        return q == null || q.isEmpty() ? ProcessorResponse.approved() : q.poll();
    }
}

/** True approval probability per (processor, network), decided by a seeded Random. */
final class SimulatedGateway implements ProcessorGateway {
    private final Random random;
    private final Map<String, Double> trueRates = new HashMap<>();

    SimulatedGateway(long seed) { this.random = new Random(seed); }

    SimulatedGateway rate(String processorId, CardNetwork network, double approvalProbability) {
        trueRates.put(processorId + "/" + network, approvalProbability);
        return this;
    }

    @Override public ProcessorResponse authorize(String processorId, PaymentRequest request) {
        double p = trueRates.getOrDefault(processorId + "/" + request.network(), 0.9);
        double roll = random.nextDouble();
        if (roll < p) return ProcessorResponse.approved();
        // Of the declines, a third are path problems (05/91) another acquirer might clear; the rest are issuer decisions.
        double declineRoll = random.nextDouble();
        if (declineRoll < 0.33) return ProcessorResponse.of(declineRoll < 0.2 ? "05" : "91");
        return ProcessorResponse.of(declineRoll < 0.8 ? "51" : "54");
    }
}

// ---------------------------------------------------------------------------------------------
// The router.
// ---------------------------------------------------------------------------------------------

/** One ranked option: the inputs to the score are kept so the decision can be logged and explained. */
record Candidate(ProcessorConfig processor, double authRate, Money fee, double expectedMarginMinor) {
    @Override public String toString() {
        return String.format("%-6s auth %.3f  fee %-10s expected margin %8.2f minor", processor.id(), authRate, fee, expectedMarginMinor);
    }
}

record Attempt(int number, String processorId, String code, DeclineClass decline, long atMillis) {
    @Override public String toString() {
        return String.format("#%d %-6s %-13s %-16s %s", number, processorId, code, decline, DeclineCodes.meaning(code));
    }
}

enum RouteOutcome { APPROVED, DECLINED, PENDING_UNKNOWN, NO_ROUTE }

record RouteResult(String paymentId, RouteOutcome outcome, List<Attempt> attempts, String stopReason) {
    String approvedBy() {
        return outcome == RouteOutcome.APPROVED ? attempts.get(attempts.size() - 1).processorId() : null;
    }
}

record RoutingPolicy(int maxAttempts, double explorationRate) {
    RoutingPolicy {
        if (maxAttempts < 1) throw new IllegalArgumentException("maxAttempts must be >= 1");
        if (explorationRate < 0 || explorationRate >= 1) throw new IllegalArgumentException("explorationRate must be in [0,1)");
    }
}

final class Router {
    private final Map<String, ProcessorConfig> processors = new LinkedHashMap<>();
    private final Map<String, ProcessorHealth> health = new LinkedHashMap<>();
    private final AuthRateEstimator estimator;
    private final RetryBudget budget;
    private final ProcessorGateway gateway;
    private final Clock clock;
    private final RoutingPolicy policy;
    private final Random random;

    Router(List<ProcessorConfig> configs, BiFunction<String, Clock, ProcessorHealth> healthFactory, AuthRateEstimator estimator,
           RetryBudget budget, ProcessorGateway gateway, Clock clock, RoutingPolicy policy, Random random) {
        for (ProcessorConfig c : configs) {
            processors.put(c.id(), c);
            health.put(c.id(), healthFactory.apply(c.id(), clock));
        }
        this.estimator = estimator;
        this.budget = budget;
        this.gateway = gateway;
        this.clock = clock;
        this.policy = policy;
        this.random = random;
    }

    ProcessorHealth health(String processorId) { return health.get(processorId); }
    AuthRateEstimator estimator() { return estimator; }
    RetryBudget budget() { return budget; }

    /**
     * Eligible processors, best first. Eligible: accepts the network and currency, and its circuit is
     * not OPEN. Score: P(approve) * (amount - fee), the expected margin of sending the attempt there.
     * Ties break on processor id so the order is deterministic.
     */
    List<Candidate> rank(PaymentRequest request) {
        List<Candidate> out = new ArrayList<>();
        for (ProcessorConfig p : processors.values()) {
            if (!p.accepts(request) || !health.get(p.id()).allows()) continue;
            double rate = estimator.estimate(p.id(), request.network());
            Money fee = p.fee(request.amount());
            double margin = rate * request.amount().minus(fee).minorUnits();
            out.add(new Candidate(p, rate, fee, margin));
        }
        out.sort(Comparator.comparingDouble(Candidate::expectedMarginMinor).reversed()
                .thenComparing(c -> c.processor().id()));
        return out;
    }

    /**
     * Try the ranked processors in order. Stop on approval, on any decline that is not SOFT_CASCADE,
     * on the per-payment attempt cap, or when the shared retry budget cannot pay for a cascade.
     */
    RouteResult route(PaymentRequest request) {
        List<Candidate> ranked = new ArrayList<>(rank(request));
        if (ranked.isEmpty()) return new RouteResult(request.id(), RouteOutcome.NO_ROUTE, List.of(), "no eligible processor");
        if (ranked.size() > 1 && policy.explorationRate() > 0 && random.nextDouble() < policy.explorationRate()) {
            Collections.swap(ranked, 0, 1 + random.nextInt(ranked.size() - 1));   // explore: lead with a non-best candidate
        }
        List<Attempt> attempts = new ArrayList<>();
        budget.recordFirstAttempt();
        for (Candidate c : ranked) {
            if (attempts.size() >= policy.maxAttempts()) {
                return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "attempt cap " + policy.maxAttempts() + " reached");
            }
            if (!attempts.isEmpty() && !budget.tryAcquireRetry()) {
                return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "retry budget exhausted");
            }
            String pid = c.processor().id();
            if (!health.get(pid).allows()) continue;   // tripped by an earlier attempt of this same payment
            ProcessorResponse response = gateway.authorize(pid, request);
            DeclineClass decline = DeclineCodes.classify(response);
            attempts.add(new Attempt(attempts.size() + 1, pid, response.code(), decline, clock.nowMillis()));
            learn(pid, request, response, decline);
            switch (decline) {
                case APPROVED -> { return new RouteResult(request.id(), RouteOutcome.APPROVED, attempts, "approved"); }
                case SOFT_CASCADE -> { }   // the only class that may try the next processor
                case SOFT_RETRY_LATER -> { return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "issuer decline: retry later, not elsewhere"); }
                case AUTH_REQUIRED -> { return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "step up with 3-D Secure, then retry"); }
                case HARD -> { return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "hard decline: do not retry this card"); }
                case AMBIGUOUS -> { return new RouteResult(request.id(), RouteOutcome.PENDING_UNKNOWN, attempts, "timeout: query or reverse at " + pid + " before any cascade"); }
            }
        }
        return new RouteResult(request.id(), RouteOutcome.DECLINED, attempts, "all eligible processors tried");
    }

    /** Health learns from technical outcomes; the auth-rate estimate learns only from issuer decisions. */
    private void learn(String pid, PaymentRequest request, ProcessorResponse response, DeclineClass decline) {
        ProcessorHealth h = health.get(pid);
        if (response.technical()) {
            h.recordFailure();
            return;
        }
        h.recordSuccess();
        estimator.record(pid, request.network(), decline == DeclineClass.APPROVED);
    }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    static final Set<Currency> USD_EUR = Set.of(Currency.USD, Currency.EUR);

    static List<ProcessorConfig> processors() {
        return List.of(
                new ProcessorConfig("acq_a", EnumSet.of(CardNetwork.VISA, CardNetwork.MASTERCARD), USD_EUR, 220, 20),
                new ProcessorConfig("acq_b", EnumSet.of(CardNetwork.VISA, CardNetwork.MASTERCARD), USD_EUR, 290, 30),
                new ProcessorConfig("acq_c", EnumSet.of(CardNetwork.AMEX), Set.of(Currency.USD), 350, 0));
    }

    static Router router(ProcessorGateway gateway, FakeClock clock, AuthRateEstimator est, RetryBudget budget, double explore) {
        return new Router(processors(), (id, c) -> new ProcessorHealth(c, 10, 5, 30_000), est, budget, gateway, clock,
                new RoutingPolicy(3, explore), new Random(7));
    }

    static void print(RouteResult r) {
        System.out.println(r.paymentId() + " -> " + r.outcome() + " (" + r.stopReason() + ")");
        for (Attempt a : r.attempts()) System.out.println("   " + a);
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        Currency USD = Currency.USD;

        System.out.println("== Decline classification table ==");
        for (DeclineRule r : DeclineCodes.TABLE.values()) {
            System.out.printf("%-13s %-17s %s%n", r.code(), r.decline(), r.meaning());
        }

        System.out.println();
        System.out.println("== Ranking a 100.00 USD Visa payment (expected margin = P(approve) x (amount - fee)) ==");
        AuthRateEstimator est = new AuthRateEstimator(0.85, 20, 0.995);
        for (int i = 0; i < 100; i++) est.record("acq_a", CardNetwork.VISA, i % 100 < 86);
        for (int i = 0; i < 100; i++) est.record("acq_b", CardNetwork.VISA, i % 100 < 93);
        ScriptedGateway gw = new ScriptedGateway();
        Router r = router(gw, clock, est, new RetryBudget(100, 10_000, 3_000), 0);
        PaymentRequest p100 = new PaymentRequest("pay_100", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_1");
        for (Candidate c : r.rank(p100)) System.out.println(c);

        System.out.println();
        System.out.println("== Cascading: only SOFT_CASCADE moves to the next processor ==");
        gw.script("acq_b", ProcessorResponse.of("91")).script("acq_a", ProcessorResponse.approved());
        print(r.route(p100));
        gw.script("acq_b", ProcessorResponse.of("43"));
        print(r.route(new PaymentRequest("pay_stolen", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_2")));
        gw.script("acq_b", ProcessorResponse.of("51"));
        print(r.route(new PaymentRequest("pay_nsf", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_3")));
        gw.script("acq_b", ProcessorResponse.of("05", "03"));
        print(r.route(new PaymentRequest("pay_mac03", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_4")));
        gw.script("acq_b", ProcessorResponse.timeout());
        print(r.route(new PaymentRequest("pay_timeout", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_5")));
        gw.script("acq_b", ProcessorResponse.of("96")).script("acq_a", ProcessorResponse.of("05"));
        print(r.route(new PaymentRequest("pay_exhaust", Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_6")));
        print(r.route(new PaymentRequest("pay_amex", Money.ofMinor(10_000, Currency.EUR), CardNetwork.AMEX, "fp_7")));

        System.out.println();
        System.out.println("== Circuit breaker: 5 technical failures in the last 10 calls take acq_b out of the ranking ==");
        ScriptedGateway flaky = new ScriptedGateway();
        Router cb = router(flaky, clock, est, new RetryBudget(100, 10_000, 10_000), 0);
        int sent = 0;
        while (cb.health("acq_b").allows()) {
            flaky.script("acq_b", ProcessorResponse.connectError());
            RouteResult res = cb.route(new PaymentRequest("pay_ce" + sent, Money.ofMinor(5_000, USD), CardNetwork.VISA, "fp_ce"));
            System.out.println("pay_ce" + sent + " -> " + res.outcome() + " via " + res.approvedBy() + " after " + res.attempts().size() + " attempts");
            sent++;
        }
        System.out.println("acq_b circuit: " + cb.health("acq_b").state() + "; ranking now " + cb.rank(p100).stream().map(c -> c.processor().id()).toList());
        clock.advance(30_000);
        System.out.println("after 30s cool-down: " + cb.health("acq_b").state() + "; the next payment probes it");
        RouteResult probe = cb.route(new PaymentRequest("pay_probe", Money.ofMinor(5_000, USD), CardNetwork.VISA, "fp_p"));
        System.out.println("probe approved by " + probe.approvedBy() + "; acq_b circuit: " + cb.health("acq_b").state());

        System.out.println();
        System.out.println("== Retry budget: an issuer outage makes every first attempt a 91 ==");
        ScriptedGateway outage = new ScriptedGateway();
        Router storm = router(outage, clock, new AuthRateEstimator(0.85, 20, 0.995), new RetryBudget(100, 10_000, 3_000), 0);
        int cascades = 0, attempts = 0;
        for (int i = 0; i < 20; i++) {
            outage.script("acq_a", ProcessorResponse.of("91")).script("acq_b", ProcessorResponse.of("91"));
            RouteResult res = storm.route(new PaymentRequest("pay_s" + i, Money.ofMinor(2_000, USD), CardNetwork.VISA, "fp_s" + i));
            attempts += res.attempts().size();
            cascades += res.attempts().size() - 1;
        }
        System.out.println("20 payments -> " + attempts + " processor calls, " + cascades + " cascades; budget left "
                + storm.budget().balanceMilli() + " milli-tokens (without a budget: 40 calls)");

        System.out.println();
        System.out.println("== Learning: acq_a's true Visa approval rate is 0.80, acq_b's is 0.93; both start at the 0.85 prior ==");
        AuthRateEstimator learn = new AuthRateEstimator(0.85, 20, 0.995);
        SimulatedGateway sim = new SimulatedGateway(42).rate("acq_a", CardNetwork.VISA, 0.80).rate("acq_b", CardNetwork.VISA, 0.93);
        Router lr = router(sim, clock, learn, new RetryBudget(100, 10_000, 3_000), 0.05);
        Map<String, Integer> firstChoice = new LinkedHashMap<>();
        int approved = 0;
        for (int i = 0; i < 2_000; i++) {
            RouteResult res = lr.route(new PaymentRequest("pay_l" + i, Money.ofMinor(10_000, USD), CardNetwork.VISA, "fp_l" + i));
            firstChoice.merge(res.attempts().get(0).processorId(), 1, Integer::sum);
            if (res.outcome() == RouteOutcome.APPROVED) approved++;
        }
        System.out.printf("estimates after 2000 payments: acq_a %.3f, acq_b %.3f%n",
                learn.estimate("acq_a", CardNetwork.VISA), learn.estimate("acq_b", CardNetwork.VISA));
        System.out.println("first attempts: " + firstChoice + "; approved " + approved + "/2000");
    }
}
