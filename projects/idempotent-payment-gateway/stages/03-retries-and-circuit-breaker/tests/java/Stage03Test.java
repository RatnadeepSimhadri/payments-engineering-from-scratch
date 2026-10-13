// Stage 3 tests for projects/idempotent-payment-gateway: backoff ceilings and full jitter from a seeded Rng,
// retryable-error classification, the retry budget, the CLOSED/OPEN/HALF_OPEN breaker on an injected clock,
// and the Gateway that ties the store, the state machine and the client together.
// Run by scripts/project_test.py; exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage03Test {
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

    static ChargeRequest s3Request(String key, String amount) {
        return new ChargeRequest("m_1", key, Money.parse(amount, Currency.USD), "tok_visa", "order-42");
    }

    static RetryingClient s3Client(String script, FakeClock clock, CircuitBreaker breaker, RetryBudget budget, long seed) {
        return new RetryingClient(new ScriptedProcessor(script), new BackoffPolicy(100, 2_000, 4), budget, breaker, new Rng(seed), clock);
    }

    public static void main(String[] args) {
        ChargeRequest req = s3Request("key-1", "19.99");

        // Scripted processor: A approve, D decline, U unavailable; last character repeats
        ScriptedProcessor scripted = new ScriptedProcessor("UDA");
        check("script step 1 unavailable", scripted.charge(req).outcome() == ProcessorOutcome.UNAVAILABLE);
        check("script step 2 declined", scripted.charge(req).outcome() == ProcessorOutcome.DECLINED);
        check("script step 3 approved with reference", scripted.charge(req).outcome() == ProcessorOutcome.APPROVED && scripted.charge(req).reference().equals("auth-4"));
        check("last character repeats", scripted.charge(req).outcome() == ProcessorOutcome.APPROVED && scripted.calls() == 5);
        checkThrows("script must be A/D/U", IllegalArgumentException.class, () -> new ScriptedProcessor("AXU"));
        checkThrows("script must be non-empty", IllegalArgumentException.class, () -> new ScriptedProcessor(""));

        // Backoff: ceiling = min(cap, base * 2^(retry-1)); delay = uniform(0, ceiling) from the seeded Rng
        BackoffPolicy policy = new BackoffPolicy(100, 2_000, 4);
        check("ceiling retry 1", policy.ceilingFor(1) == 100);
        check("ceiling retry 2", policy.ceilingFor(2) == 200);
        check("ceiling retry 4", policy.ceilingFor(4) == 800);
        check("ceiling is capped", policy.ceilingFor(6) == 2_000 && policy.ceilingFor(40) == 2_000);
        check("maxAttempts", policy.maxAttempts() == 4);
        checkThrows("retry numbers start at 1", IllegalArgumentException.class, () -> policy.ceilingFor(0));
        checkThrows("cap below base rejected", IllegalArgumentException.class, () -> new BackoffPolicy(500, 100, 3));
        checkThrows("maxAttempts at least 1", IllegalArgumentException.class, () -> new BackoffPolicy(100, 200, 0));
        Rng rng = new Rng(42);
        check("seed 42 draws 46, 26, 131, 226", policy.delayBefore(1, rng) == 46 && policy.delayBefore(2, rng) == 26
                && policy.delayBefore(3, rng) == 131 && policy.delayBefore(4, rng) == 226);
        check("same seed, same schedule", policy.delayBefore(1, new Rng(42)) == 46 && policy.delayBefore(1, new Rng(42)) == 46);
        boolean withinCeiling = true;
        Rng spread = new Rng(7);
        for (int i = 0; i < 500; i++) {
            long d = policy.delayBefore(3, spread);
            if (d < 0 || d > 400) withinCeiling = false;
        }
        check("full jitter never exceeds the ceiling", withinCeiling);
        check("Rng.nextBelow is uniform over [0, bound)", new Rng(42).nextBelow(101) == 46 && new Rng(7).nextBelow(1000) == 822);
        checkThrows("nextBelow needs a positive bound", IllegalArgumentException.class, () -> new Rng(1).nextBelow(0));

        // Retry budget
        RetryBudget budget = new RetryBudget(2, 3);
        check("budget starts with initial tokens", budget.tokens() == 2);
        check("withdraw twice", budget.tryWithdraw() && budget.tryWithdraw() && budget.tokens() == 0);
        check("empty budget refuses", !budget.tryWithdraw());
        budget.deposit(5);
        check("deposit is capped at max", budget.tokens() == 3);
        checkThrows("negative deposit", IllegalArgumentException.class, () -> budget.deposit(-1));
        checkThrows("initial above max", IllegalArgumentException.class, () -> new RetryBudget(5, 3));

        // Circuit breaker on the injected clock
        FakeClock clock = new FakeClock(100_000);
        CircuitBreaker breaker = new CircuitBreaker(clock, 3, 30_000, 2);
        check("starts CLOSED", breaker.state() == CircuitState.CLOSED && breaker.allowRequest() && breaker.retryAfterMillis() == 0);
        breaker.recordFailure();
        breaker.recordFailure();
        check("two failures still CLOSED", breaker.state() == CircuitState.CLOSED);
        breaker.recordSuccess();
        breaker.recordFailure();
        breaker.recordFailure();
        check("success resets the consecutive count", breaker.state() == CircuitState.CLOSED);
        breaker.recordFailure();
        check("third consecutive failure OPENs", breaker.state() == CircuitState.OPEN && !breaker.allowRequest());
        check("retryAfter is the full open window", breaker.retryAfterMillis() == 30_000);
        clock.advance(29_999);
        check("still OPEN one ms before the window ends", breaker.state() == CircuitState.OPEN && breaker.retryAfterMillis() == 1);
        clock.advance(1);
        check("HALF_OPEN after the window", breaker.state() == CircuitState.HALF_OPEN && breaker.allowRequest());
        breaker.recordFailure();
        check("one HALF_OPEN failure reopens", breaker.state() == CircuitState.OPEN && breaker.retryAfterMillis() == 30_000);
        clock.advance(30_000);
        breaker.recordSuccess();
        check("one of two successes keeps HALF_OPEN", breaker.state() == CircuitState.HALF_OPEN);
        breaker.recordSuccess();
        check("second success CLOSEs", breaker.state() == CircuitState.CLOSED);
        checkThrows("open window must be positive", IllegalArgumentException.class, () -> new CircuitBreaker(clock, 1, 0, 1));

        // Retrying client: only UNAVAILABLE is retried, with full-jitter waits on the clock
        FakeClock c1 = new FakeClock(0);
        ChargeOutcome ok = s3Client("UUA", c1, new CircuitBreaker(c1, 5, 30_000, 1), new RetryBudget(10, 10), 42).charge(req);
        check("UUA approves on the third attempt", ok.outcome() == ProcessorOutcome.APPROVED && ok.attempts() == 3 && ok.reference().equals("auth-3") && ok.reason().equals("approved"));
        check("waited 46 + 26 ms from seed 42", ok.waitedMillis() == 72 && c1.nowMillis() == 72);

        FakeClock c2 = new FakeClock(0);
        ScriptedProcessor declining = new ScriptedProcessor("D");
        ChargeOutcome declined = new RetryingClient(declining, new BackoffPolicy(100, 2_000, 4), new RetryBudget(10, 10), new CircuitBreaker(c2, 5, 30_000, 1), new Rng(42), c2).charge(req);
        check("a decline is never retried", declined.outcome() == ProcessorOutcome.DECLINED && declined.attempts() == 1 && declining.calls() == 1 && declined.reason().equals("declined"));
        check("no waiting on a decline", declined.waitedMillis() == 0 && c2.nowMillis() == 0);

        FakeClock c3 = new FakeClock(0);
        ChargeOutcome exhausted = s3Client("U", c3, new CircuitBreaker(c3, 10, 30_000, 1), new RetryBudget(10, 10), 42).charge(req);
        check("dead processor exhausts maxAttempts", exhausted.outcome() == ProcessorOutcome.UNAVAILABLE && exhausted.attempts() == 4 && exhausted.reason().equals("retries exhausted"));
        check("three waits: 46 + 26 + 131", exhausted.waitedMillis() == 203 && c3.nowMillis() == 203);

        FakeClock c4 = new FakeClock(0);
        RetryBudget tiny = new RetryBudget(1, 10);
        ChargeOutcome budgeted = s3Client("U", c4, new CircuitBreaker(c4, 10, 30_000, 1), tiny, 42).charge(req);
        check("drained budget stops retries", budgeted.outcome() == ProcessorOutcome.UNAVAILABLE && budgeted.attempts() == 2 && budgeted.reason().equals("retry budget exhausted") && tiny.tokens() == 0);

        FakeClock c5 = new FakeClock(0);
        CircuitBreaker trip = new CircuitBreaker(c5, 2, 30_000, 1);
        ScriptedProcessor dead = new ScriptedProcessor("U");
        RetryingClient tripping = new RetryingClient(dead, new BackoffPolicy(100, 2_000, 4), new RetryBudget(10, 10), trip, new Rng(42), c5);
        ChargeOutcome tripped = tripping.charge(req);
        check("breaker opens mid-retry", tripped.outcome() == ProcessorOutcome.UNAVAILABLE && tripped.attempts() == 2 && tripped.reason().equals("circuit open") && trip.state() == CircuitState.OPEN);
        ChargeOutcome refused = tripping.charge(req);
        check("open breaker refuses without calling the processor", refused.attempts() == 0 && refused.reason().equals("circuit open") && dead.calls() == 2);

        // Gateway: store + state machine + client
        FakeClock gc = new FakeClock(1_000_000);
        IdempotencyStore store = new IdempotencyStore(gc, 60_000);
        ScriptedProcessor script = new ScriptedProcessor("ADUUUA");
        RetryingClient client = new RetryingClient(script, new BackoffPolicy(100, 2_000, 2), new RetryBudget(10, 10), new CircuitBreaker(gc, 3, 30_000, 1), new Rng(42), gc);
        Gateway gateway = new Gateway(store, client, gc);

        ApiResponse first = gateway.charge(s3Request("key-1", "19.99"));
        check("approved charge is 201", first.status() == 201 && !first.replayed());
        check("body names the payment and state", first.body().contains("\"payment_id\":\"pay_1\"") && first.body().contains("\"state\":\"succeeded\""));
        check("payment is SUCCEEDED and captured", gateway.payment("pay_1").map(p -> p.state() == PaymentState.SUCCEEDED && p.captured().equals(Money.parse("19.99", Currency.USD))).orElse(false));
        ApiResponse replay = gateway.charge(s3Request("key-1", "19.99"));
        check("same request replays the same body, flagged", replay.status() == 201 && replay.body().equals(first.body()) && replay.replayed());
        check("replay does not call the processor again", script.calls() == 1 && gateway.payments().size() == 1);
        ApiResponse conflict = gateway.charge(s3Request("key-1", "29.99"));
        check("same key, other amount is 422", conflict.status() == 422 && gateway.payments().size() == 1);

        ApiResponse declinedResp = gateway.charge(s3Request("key-2", "5.00"));
        check("declined charge is 402 and FAILED", declinedResp.status() == 402 && gateway.payment("pay_2").map(p -> p.state() == PaymentState.FAILED).orElse(false));
        check("declines are stored and replayed", gateway.charge(s3Request("key-2", "5.00")).replayed() && script.calls() == 2);

        ApiResponse unavailable = gateway.charge(s3Request("key-3", "7.00"));
        check("unavailable is 503 after maxAttempts", unavailable.status() == 503 && unavailable.body().contains("retries exhausted") && script.calls() == 4);
        check("failed payment is recorded", gateway.payment("pay_3").map(p -> p.state() == PaymentState.FAILED).orElse(false));
        check("503 releases the key", store.get("m_1", "key-3").isEmpty());
        ApiResponse again = gateway.charge(s3Request("key-3", "7.00"));
        check("retrying a 503 is a fresh attempt, not a replay", again.status() == 503 && !again.replayed() && gateway.payments().size() == 4);
        ApiResponse open = gateway.charge(s3Request("key-4", "7.00"));
        check("open breaker short-circuits with 503", open.status() == 503 && open.body().contains("circuit open") && script.calls() == 5);
        gc.advance(30_000);
        ApiResponse recovered = gateway.charge(s3Request("key-3", "7.00"));
        check("after the window the same key succeeds", recovered.status() == 201 && recovered.body().contains("\"state\":\"succeeded\"") && script.calls() == 6);

        System.out.println("Stage03Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
