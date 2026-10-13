# Stage 03 starter

Continue editing the same workspace: add a new file `Resilience.java`. `Rng.java` (xorshift64*, seeded) and
`Clock.java` are already in your workspace from Stage 01. Python and Go ports follow later; this pass is Java only.

Stages 01 and 02 are graded again first.

## New signatures (Java)

| Concept | Signature |
|---|---|
| Processor answer | `enum ProcessorOutcome { APPROVED, DECLINED, UNAVAILABLE }`, `record ProcessorResult(ProcessorOutcome outcome, String reference)` |
| Processor | `interface Processor { ProcessorResult charge(ChargeRequest request); }` |
| Fake | `new ScriptedProcessor("UUA")` ('A' approve, 'D' decline, 'U' unavailable, last char repeats), `int calls()` |
| Backoff | `new BackoffPolicy(long baseMillis, long capMillis, int maxAttempts)`; `int maxAttempts()`, `long ceilingFor(int retry)`, `long delayBefore(int retry, Rng rng)` |
| Budget | `new RetryBudget(int initialTokens, int maxTokens)`; `int tokens()`, `boolean tryWithdraw()`, `void deposit(int n)` |
| Breaker | `enum CircuitState { CLOSED, OPEN, HALF_OPEN }`; `new CircuitBreaker(Clock clock, int failureThreshold, long openMillis, int successesToClose)`; `state()`, `allowRequest()`, `recordSuccess()`, `recordFailure()`, `long retryAfterMillis()` |
| Client | `record ChargeOutcome(ProcessorOutcome outcome, String reference, int attempts, long waitedMillis, String reason)`; `new RetryingClient(Processor, BackoffPolicy, RetryBudget, CircuitBreaker, Rng, Clock)`; `ChargeOutcome charge(ChargeRequest)` |
| Gateway | `new Gateway(IdempotencyStore store, RetryingClient client, Clock clock)`; `ApiResponse charge(ChargeRequest)`; `Optional<Payment> payment(String id)`; `List<Payment> payments()` |

Rules the tests pin down: `ceilingFor(retry) = min(cap, base * 2^(retry-1))`; `delayBefore` draws
`rng.nextBelow(ceiling + 1)`; only `UNAVAILABLE` is retried; the reason strings are exactly `approved`,
`declined`, `retries exhausted`, `retry budget exhausted`, `circuit open`. Gateway statuses: 201 approved,
402 declined, 409 in progress, 422 conflict, 503 unavailable (and the key is released). Payment ids are
`pay_1`, `pay_2`, … and the success/decline body is `{"payment_id":"pay_1","state":"succeeded","amount":"19.99 USD"}`.
Read `docs/en.md`.
