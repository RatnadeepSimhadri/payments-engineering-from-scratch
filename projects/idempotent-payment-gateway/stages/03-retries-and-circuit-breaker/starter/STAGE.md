# Stage 03 starter

Continue editing the same workspace: add a new file `Resilience.java`. `Rng.java` (xorshift64*, seeded) and
`Clock.java` are already in your workspace from Stage 01. The Python port has the same surface in snake_case (signatures below); Go port follows later.

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

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you; the names below are the Java ones in snake_case.

```python
class ProcessorOutcome(Enum): APPROVED, DECLINED, UNAVAILABLE
@dataclass(frozen=True)
class ProcessorResult:        # outcome: ProcessorOutcome, reference: str
class Processor(Protocol):    def charge(self, request: ChargeRequest) -> ProcessorResult

class ScriptedProcessor:      # 'A' approve, 'D' decline, 'U' unavailable; last character repeats; bad script -> ValueError
    def __init__(self, script: str) -> None; charge(request) -> ProcessorResult; calls() -> int

class BackoffPolicy:
    def __init__(self, base_millis: int, cap_millis: int, max_attempts: int) -> None: ...   # bad settings -> ValueError
    def max_attempts(self) -> int: ...
    def ceiling_for(self, retry: int) -> int: ...                 # min(cap, base * 2**(retry-1)); retry < 1 -> ValueError
    def delay_before(self, retry: int, rng: Rng) -> int: ...      # rng.next_below(ceiling + 1)

class RetryBudget:
    def __init__(self, initial_tokens: int, max_tokens: int) -> None: ...
    def tokens(self) -> int; try_withdraw() -> bool; deposit(n: int) -> None

class CircuitState(Enum): CLOSED, OPEN, HALF_OPEN
class CircuitBreaker:
    def __init__(self, clock: Clock, failure_threshold: int, open_millis: int, successes_to_close: int) -> None: ...
    def state(self) -> CircuitState; allow_request() -> bool; record_success() -> None; record_failure() -> None
    def retry_after_millis(self) -> int: ...

@dataclass(frozen=True)
class ChargeOutcome:          # outcome, reference, attempts: int, waited_millis: int, reason: str

class RetryingClient:
    def __init__(self, processor: Processor, policy: BackoffPolicy, budget: RetryBudget, breaker: CircuitBreaker, rng: Rng, clock: Clock) -> None: ...
    def charge(self, request: ChargeRequest) -> ChargeOutcome: ...

class Gateway:
    def __init__(self, store: IdempotencyStore, client: RetryingClient, clock: Clock) -> None: ...
    def charge(self, request: ChargeRequest) -> ApiResponse: ...   # 201 / 402 / 409 / 422 / 503
    def payment(self, id: str) -> Payment | None: ...
    def payments(self) -> tuple[Payment, ...]: ...

# Rng (given): Rng(seed).next_long() -> int in [0, 2**64); next_below(bound) -> int; bound <= 0 -> ValueError
```
