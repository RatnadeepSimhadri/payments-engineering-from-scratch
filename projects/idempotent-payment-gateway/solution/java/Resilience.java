// Project: projects/idempotent-payment-gateway — Stage 03: retries, backoff with full jitter, retry budget,
// circuit breaker, and the Gateway that ties the idempotency store, the state machine, and the processor together.
// Backoff follows the "full jitter" recipe: sleep = uniform(0, min(cap, base * 2^(retry-1))), drawn from the
// seeded Rng so every schedule is replayable. Time comes from the injected Clock; nothing here ever blocks.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;

enum ProcessorOutcome {
    APPROVED,     // funds captured; the processor returns a reference
    DECLINED,     // a definitive answer: never retry a decline
    UNAVAILABLE   // timeout, 5xx, connection reset: the only outcome worth retrying
}

record ProcessorResult(ProcessorOutcome outcome, String reference) {}

interface Processor {
    ProcessorResult charge(ChargeRequest request);
}

/**
 * Fake processor driven by a script: 'A' approve, 'D' decline, 'U' unavailable. "UUA" fails twice then approves.
 * When the script runs out the last character repeats, so "A" is an always-healthy processor and "U" is a dead one.
 */
final class ScriptedProcessor implements Processor {
    private final String script;
    private int calls;

    ScriptedProcessor(String script) {
        if (script == null || script.isEmpty() || !script.chars().allMatch(c -> c == 'A' || c == 'D' || c == 'U')) {
            throw new IllegalArgumentException("script must be a non-empty string of A, D, U: " + script);
        }
        this.script = script;
    }

    @Override
    public ProcessorResult charge(ChargeRequest request) {
        char step = script.charAt(Math.min(calls, script.length() - 1));
        calls++;
        return switch (step) {
            case 'A' -> new ProcessorResult(ProcessorOutcome.APPROVED, "auth-" + calls);
            case 'D' -> new ProcessorResult(ProcessorOutcome.DECLINED, "decl-" + calls);
            default -> new ProcessorResult(ProcessorOutcome.UNAVAILABLE, "");
        };
    }

    int calls() {
        return calls;
    }
}

/** Exponential backoff with full jitter and a hard cap, plus the maximum number of attempts (first try included). */
final class BackoffPolicy {
    private final long baseMillis;
    private final long capMillis;
    private final int maxAttempts;

    BackoffPolicy(long baseMillis, long capMillis, int maxAttempts) {
        if (baseMillis <= 0 || capMillis < baseMillis) throw new IllegalArgumentException("need 0 < base <= cap");
        if (maxAttempts < 1) throw new IllegalArgumentException("maxAttempts must be at least 1");
        this.baseMillis = baseMillis;
        this.capMillis = capMillis;
        this.maxAttempts = maxAttempts;
    }

    int maxAttempts() { return maxAttempts; }

    /** min(cap, base * 2^(retry-1)) for retry >= 1: the largest delay full jitter may draw. */
    long ceilingFor(int retry) {
        if (retry < 1) throw new IllegalArgumentException("retry numbers start at 1");
        long ceiling = baseMillis;
        for (int i = 1; i < retry && ceiling < capMillis; i++) ceiling = Math.min(capMillis, ceiling * 2);
        return Math.min(capMillis, ceiling);
    }

    /** uniform(0, ceiling) inclusive, from the injected Rng. */
    long delayBefore(int retry, Rng rng) {
        return rng.nextBelow(ceilingFor(retry) + 1);
    }
}

/**
 * A token budget for retries. Every retry withdraws one token; every definitive answer from the processor
 * deposits one (up to the cap). When the processor is down, the budget drains and retries stop: the
 * retry storm that would otherwise finish the processor off never starts.
 */
final class RetryBudget {
    private final int maxTokens;
    private int tokens;

    RetryBudget(int initialTokens, int maxTokens) {
        if (maxTokens < 1 || initialTokens < 0 || initialTokens > maxTokens) throw new IllegalArgumentException("bad budget");
        this.tokens = initialTokens;
        this.maxTokens = maxTokens;
    }

    int tokens() { return tokens; }

    boolean tryWithdraw() {
        if (tokens == 0) return false;
        tokens--;
        return true;
    }

    void deposit(int n) {
        if (n < 0) throw new IllegalArgumentException("deposit must be non-negative");
        tokens = Math.min(maxTokens, tokens + n);
    }
}

enum CircuitState { CLOSED, OPEN, HALF_OPEN }

/**
 * CLOSED: everything flows; failureThreshold consecutive failures open the circuit.
 * OPEN: every request is refused until openMillis have passed on the injected clock, then HALF_OPEN.
 * HALF_OPEN: requests flow as probes; one failure reopens, successesToClose consecutive successes close.
 */
final class CircuitBreaker {
    private final Clock clock;
    private final int failureThreshold;
    private final long openMillis;
    private final int successesToClose;
    private CircuitState state = CircuitState.CLOSED;
    private int consecutiveFailures;
    private int halfOpenSuccesses;
    private long openedAt;

    CircuitBreaker(Clock clock, int failureThreshold, long openMillis, int successesToClose) {
        if (failureThreshold < 1 || openMillis <= 0 || successesToClose < 1) throw new IllegalArgumentException("bad breaker settings");
        this.clock = Objects.requireNonNull(clock, "clock");
        this.failureThreshold = failureThreshold;
        this.openMillis = openMillis;
        this.successesToClose = successesToClose;
    }

    /** Current state after applying the OPEN -> HALF_OPEN timer. */
    CircuitState state() {
        if (state == CircuitState.OPEN && clock.nowMillis() - openedAt >= openMillis) {
            state = CircuitState.HALF_OPEN;
            halfOpenSuccesses = 0;
        }
        return state;
    }

    boolean allowRequest() {
        return state() != CircuitState.OPEN;
    }

    void recordSuccess() {
        CircuitState current = state();
        consecutiveFailures = 0;
        if (current == CircuitState.HALF_OPEN && ++halfOpenSuccesses >= successesToClose) {
            state = CircuitState.CLOSED;
            halfOpenSuccesses = 0;
        }
    }

    void recordFailure() {
        CircuitState current = state();
        consecutiveFailures++;
        if (current == CircuitState.HALF_OPEN || consecutiveFailures >= failureThreshold) trip();
    }

    private void trip() {
        state = CircuitState.OPEN;
        openedAt = clock.nowMillis();
        consecutiveFailures = 0;
        halfOpenSuccesses = 0;
    }

    /** Milliseconds until an OPEN circuit becomes HALF_OPEN; zero when requests are allowed. */
    long retryAfterMillis() {
        return state() == CircuitState.OPEN ? openedAt + openMillis - clock.nowMillis() : 0;
    }
}

/** The result of a charge through the retrying client: the final outcome plus how it was reached. */
record ChargeOutcome(ProcessorOutcome outcome, String reference, int attempts, long waitedMillis, String reason) {}

/** Wraps a Processor with backoff, a retry budget, and a circuit breaker. Only UNAVAILABLE is retried. */
final class RetryingClient {
    private final Processor processor;
    private final BackoffPolicy policy;
    private final RetryBudget budget;
    private final CircuitBreaker breaker;
    private final Rng rng;
    private final Clock clock;

    RetryingClient(Processor processor, BackoffPolicy policy, RetryBudget budget, CircuitBreaker breaker, Rng rng, Clock clock) {
        this.processor = Objects.requireNonNull(processor);
        this.policy = Objects.requireNonNull(policy);
        this.budget = Objects.requireNonNull(budget);
        this.breaker = Objects.requireNonNull(breaker);
        this.rng = Objects.requireNonNull(rng);
        this.clock = Objects.requireNonNull(clock);
    }

    ChargeOutcome charge(ChargeRequest request) {
        if (!breaker.allowRequest()) return new ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", 0, 0, "circuit open");
        int attempts = 0;
        long waited = 0;
        while (true) {
            attempts++;
            ProcessorResult result = processor.charge(request);
            if (result.outcome() != ProcessorOutcome.UNAVAILABLE) {
                breaker.recordSuccess();
                budget.deposit(1);
                String reason = result.outcome() == ProcessorOutcome.APPROVED ? "approved" : "declined";
                return new ChargeOutcome(result.outcome(), result.reference(), attempts, waited, reason);
            }
            breaker.recordFailure();
            if (attempts >= policy.maxAttempts()) return new ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "retries exhausted");
            if (!breaker.allowRequest()) return new ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "circuit open");
            if (!budget.tryWithdraw()) return new ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "retry budget exhausted");
            long delay = policy.delayBefore(attempts, rng);
            clock.sleep(delay);
            waited += delay;
        }
    }
}

/**
 * The payment gateway a merchant talks to. One charge() call: claim the idempotency key, create the payment,
 * drive it through the state machine with the retrying client, store the response under the key.
 * Responses that must not be replayed (processor unavailable, circuit open) release the key instead.
 */
final class Gateway {
    private final IdempotencyStore store;
    private final RetryingClient client;
    private final Clock clock;
    private final Map<String, Payment> payments = new LinkedHashMap<>();
    private WebhookDispatcher dispatcher;   // Stage 04; null until attached
    private int paymentSeq;
    private int eventSeq;

    Gateway(IdempotencyStore store, RetryingClient client, Clock clock) {
        this.store = Objects.requireNonNull(store);
        this.client = Objects.requireNonNull(client);
        this.clock = Objects.requireNonNull(clock);
    }

    ApiResponse charge(ChargeRequest request) {
        BeginResult begin = store.begin(request.merchantId(), request.idempotencyKey(), request.fingerprint());
        switch (begin.decision()) {
            case REPLAY -> { return begin.response().asReplay(); }
            case IN_PROGRESS -> { return new ApiResponse(409, "{\"error\":\"request in progress\"}"); }
            case CONFLICT -> { return new ApiResponse(422, "{\"error\":\"idempotency key reused with a different request\"}"); }
            default -> { }
        }
        Payment payment = new Payment("pay_" + (++paymentSeq), request.amount(), clock);
        payments.put(payment.id(), payment);
        payment.startProcessing();
        ChargeOutcome outcome = client.charge(request);
        ApiResponse response;
        String eventType;
        switch (outcome.outcome()) {
            case APPROVED -> {
                payment.succeed(outcome.reference());
                response = new ApiResponse(201, body(payment));
                eventType = "payment.succeeded";
            }
            case DECLINED -> {
                payment.fail("declined " + outcome.reference());
                response = new ApiResponse(402, body(payment));
                eventType = "payment.failed";
            }
            default -> {
                payment.fail(outcome.reason());
                store.release(request.merchantId(), request.idempotencyKey());
                return new ApiResponse(503, "{\"error\":\"" + outcome.reason() + "\",\"payment_id\":\"" + payment.id() + "\"}");
            }
        }
        // Complete the key first, then emit: a crash in between loses an event (detectable, repairable) rather
        // than leaving the key open so a retry charges again. The transactional outbox closes even that gap.
        store.complete(request.merchantId(), request.idempotencyKey(), response);
        emit(eventType, payment);
        return response;
    }

    private static String body(Payment p) {
        return "{\"payment_id\":\"" + p.id() + "\",\"state\":\"" + p.state().wire() + "\",\"amount\":\"" + p.amount() + "\"}";
    }

    /** Stage 04: route lifecycle events to a dispatcher. */
    void attachWebhooks(WebhookDispatcher dispatcher) {
        this.dispatcher = Objects.requireNonNull(dispatcher);
    }

    private void emit(String type, Payment payment) {
        if (dispatcher == null) return;
        WebhookEvent event = new WebhookEvent("evt_" + (++eventSeq), type, payment.id(), "amount=" + payment.amount(), clock.nowMillis());
        dispatcher.enqueue(event);
    }

    Optional<Payment> payment(String id) {
        return Optional.ofNullable(payments.get(id));
    }

    List<Payment> payments() {
        return Collections.unmodifiableList(new ArrayList<>(payments.values()));
    }
}
