# Retries, Backoff and a Circuit Breaker

**Stage 3 of 4.** Plan about 3 hours.

The card processor your gateway calls is a remote system. It times out, returns 503, drops connections, and sometimes goes away for ten minutes. Stage 1 protected you from the *merchant* retrying; this stage decides how *you* retry the processor, and when you must stop. Three mechanisms, each from a Phase 5 lesson, now become one client: exponential backoff with full jitter so a thousand gateways do not retry in lockstep, a retry budget so a dead processor does not receive three times its normal traffic in retries, and a circuit breaker so that once the processor is known to be down, calls fail fast instead of each one burning the full retry schedule.

The first design decision is classification. A processor answer is one of three things: `APPROVED` (money moved), `DECLINED` (the issuer said no, a definitive answer), or `UNAVAILABLE` (timeout, 5xx, reset: you do not know what happened). Only `UNAVAILABLE` is retried. Retrying a decline is pointless and, with some issuers, counts against the merchant. Retrying an `APPROVED` is a double charge; the processor's own idempotency key (not built here) is what makes a retry after a lost `APPROVED` response safe, and your `ChargeRequest` already carries one.

The second decision is what the merchant sees. The stage ends with the `Gateway`: one `charge(request)` that claims the idempotency key, creates the `Payment`, drives it through the state machine using the retrying client, and either *completes* the key with the response (201 approved, 402 declined, both safe to replay forever) or *releases* it (503, the processor could not be reached, the merchant should try again and get a fresh attempt). The invariant from Stage 1 holds across the whole flow: **for one live key, the processor is called for at most one attempt sequence, and a replay never calls the processor at all.**

Everything is deterministic. Waits go through `Clock.sleep`, which on a `FakeClock` just advances time; jitter comes from `Rng`, a seeded xorshift64\* generator already in the workspace. The same seed produces the same schedule every run, which is how the tests can assert `46 + 26 = 72 ms`.

## Worked example

**Backoff ceilings** for `BackoffPolicy(base = 100 ms, cap = 2,000 ms, maxAttempts = 4)`. Retry `r` (the wait *before* attempt `r + 1`) may wait anywhere in `[0, ceiling(r)]`:

| Retry r | `base * 2^(r-1)` | Ceiling after cap |
|---|---|---|
| 1 | 100 | 100 |
| 2 | 200 | 200 |
| 3 | 400 | 400 |
| 4 | 800 | 800 |
| 5 | 1,600 | 1,600 |
| 6 | 3,200 | **2,000** |
| 40 | overflow | **2,000** (compute by doubling and stop at the cap; never `1L << 39`) |

**Full jitter** draws `rng.nextBelow(ceiling + 1)`, uniform over `[0, ceiling]`. With `Rng(42)` the first four draws, in order, are:

| Retry | Ceiling | `nextBelow(ceiling + 1)` |
|---|---|---|
| 1 | 100 | 46 |
| 2 | 200 | 26 |
| 3 | 400 | 131 |
| 4 | 800 | 226 |

So a processor scripted as `"UUA"` (unavailable, unavailable, approved) behind `Rng(42)` on a `FakeClock(0)`:

| Attempt | Processor says | Breaker | Budget (10/10) | Action | Clock after |
|---|---|---|---|---|---|
| 1 | `UNAVAILABLE` | failure #1 (CLOSED) | withdraw → 9 | sleep 46 | 46 |
| 2 | `UNAVAILABLE` | failure #2 (CLOSED) | withdraw → 8 | sleep 26 | 72 |
| 3 | `APPROVED auth-3` | success, count reset | deposit → 9 | return | 72 |

`ChargeOutcome(APPROVED, "auth-3", attempts = 3, waitedMillis = 72, reason = "approved")`. The same script with a processor that only ever says `U` runs four attempts, waits 46 + 26 + 131 = 203 ms, and returns `(UNAVAILABLE, "", 4, 203, "retries exhausted")`.

**Circuit breaker** `CircuitBreaker(clock, failureThreshold = 3, openMillis = 30,000, successesToClose = 2)` starting at t = 100,000:

| t | Event | Consecutive failures | State after | `allowRequest` | `retryAfterMillis` |
|---|---|---|---|---|---|
| 100,000 | failure, failure | 2 | CLOSED | true | 0 |
| 100,000 | success | 0 | CLOSED | true | 0 |
| 100,000 | failure, failure, failure | 3 → trip | OPEN | false | 30,000 |
| 129,999 | `state()` | — | OPEN | false | 1 |
| 130,000 | `state()` | — | HALF_OPEN | true | 0 |
| 130,000 | failure | — | OPEN (re-tripped) | false | 30,000 |
| 160,000 | success | — | HALF_OPEN (1 of 2) | true | 0 |
| 160,000 | success | — | CLOSED | true | 0 |

The OPEN → HALF_OPEN move happens inside `state()` by comparing the clock to `openedAt + openMillis`; nothing runs in the background.

**Gateway end to end** with processor script `"ADUUUA"`, `BackoffPolicy(100, 2000, maxAttempts = 2)`, breaker `(3, 30 s, 1)`, store TTL 60 s:

| Request | Store says | Processor calls | Payment | Response | Key afterwards |
|---|---|---|---|---|---|
| `key-1` 19.99 | NEW | 1: A | `pay_1` SUCCEEDED | **201** `{"payment_id":"pay_1","state":"succeeded","amount":"19.99 USD"}` | COMPLETED |
| `key-1` 19.99 | REPLAY | none | — | 201, same body, `replayed = true` | unchanged |
| `key-1` 29.99 | CONFLICT | none | — | **422** | unchanged |
| `key-2` 5.00 | NEW | 2: D | `pay_2` FAILED | **402** | COMPLETED (declines replay too) |
| `key-3` 7.00 | NEW | 3: U, 4: U | `pay_3` FAILED "retries exhausted" | **503** | **released** |
| `key-3` 7.00 | NEW again | 5: U → breaker trips | `pay_4` FAILED "circuit open" | 503 | released |
| `key-4` 7.00 | NEW | none (breaker OPEN) | `pay_5` FAILED | 503 "circuit open" | released |
| +30 s, `key-3` 7.00 | NEW | 6: A (HALF_OPEN probe) | `pay_6` SUCCEEDED | 201 | COMPLETED |

## Your task

Add `Resilience.java`. Stages 1 and 2 are graded again first. The Python port has the same surface in snake_case in one `main.py`, and the Go port the same surface in exported CamelCase (signatures below).

```java
enum ProcessorOutcome { APPROVED, DECLINED, UNAVAILABLE }
record ProcessorResult(ProcessorOutcome outcome, String reference) {}
interface Processor { ProcessorResult charge(ChargeRequest request); }

/** 'A' approve ("auth-<n>"), 'D' decline ("decl-<n>"), 'U' unavailable; the last character repeats. */
final class ScriptedProcessor implements Processor {
    ScriptedProcessor(String script);      // empty or any char outside A/D/U -> IllegalArgumentException
    int calls();
}

final class BackoffPolicy {
    BackoffPolicy(long baseMillis, long capMillis, int maxAttempts);  // need 0 < base <= cap, maxAttempts >= 1
    int maxAttempts();
    long ceilingFor(int retry);            // min(cap, base * 2^(retry-1)); retry < 1 -> IllegalArgumentException
    long delayBefore(int retry, Rng rng);  // rng.nextBelow(ceilingFor(retry) + 1)
}

final class RetryBudget {
    RetryBudget(int initialTokens, int maxTokens);   // 0 <= initial <= max, max >= 1
    int tokens();
    boolean tryWithdraw();                 // false when empty
    void deposit(int n);                   // capped at max; negative -> IllegalArgumentException
}

enum CircuitState { CLOSED, OPEN, HALF_OPEN }

final class CircuitBreaker {
    CircuitBreaker(Clock clock, int failureThreshold, long openMillis, int successesToClose);
    CircuitState state();                  // applies the OPEN -> HALF_OPEN timer
    boolean allowRequest();                // state() != OPEN
    void recordSuccess();
    void recordFailure();
    long retryAfterMillis();               // ms until HALF_OPEN while OPEN; 0 otherwise
}

record ChargeOutcome(ProcessorOutcome outcome, String reference, int attempts, long waitedMillis, String reason) {}

final class RetryingClient {
    RetryingClient(Processor processor, BackoffPolicy policy, RetryBudget budget, CircuitBreaker breaker, Rng rng, Clock clock);
    ChargeOutcome charge(ChargeRequest request);
}

final class Gateway {
    Gateway(IdempotencyStore store, RetryingClient client, Clock clock);
    ApiResponse charge(ChargeRequest request);
    Optional<Payment> payment(String id);
    List<Payment> payments();
}
```

The same surface in Python (one `main.py` module; methods and fields are snake_case, exceptions end in `Error`, read-only views are tuples):

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

The same surface in Go (`resilience.go`; configuration errors come back from the constructors, and the three argument checks Java makes on live objects, `CeilingFor(retry < 1)`, `NextBelow(bound <= 0)` and `Deposit(n < 0)`, `panic` because they are caller bugs that return plain values otherwise):

```go
type ProcessorOutcome int; const ( Approved ProcessorOutcome = iota; Declined; Unavailable )
type ProcessorResult struct{ Outcome ProcessorOutcome; Reference string }
type Processor interface{ Charge(request ChargeRequest) ProcessorResult }
func NewScriptedProcessor(script string) (*ScriptedProcessor, error)   // 'A' 'D' 'U'; Charge, Calls()

func NewBackoffPolicy(baseMillis, capMillis int64, maxAttempts int) (*BackoffPolicy, error)
func (b *BackoffPolicy) MaxAttempts() int
func (b *BackoffPolicy) CeilingFor(retry int) int64              // min(cap, base * 2^(retry-1)); retry < 1 panics
func (b *BackoffPolicy) DelayBefore(retry int, rng *Rng) int64   // rng.NextBelow(ceiling + 1)

func NewRetryBudget(initialTokens, maxTokens int) (*RetryBudget, error)
func (b *RetryBudget) Tokens() int; TryWithdraw() bool; Deposit(n int)   // n < 0 panics

type CircuitState int; const ( CircuitClosed CircuitState = iota; CircuitOpen; CircuitHalfOpen )
func NewCircuitBreaker(clock Clock, failureThreshold int, openMillis int64, successesToClose int) (*CircuitBreaker, error)
func (c *CircuitBreaker) State() CircuitState; AllowRequest() bool; RecordSuccess(); RecordFailure(); RetryAfterMillis() int64

type ChargeOutcome struct{ Outcome ProcessorOutcome; Reference string; Attempts int; WaitedMillis int64; Reason string }
func NewRetryingClient(processor Processor, policy *BackoffPolicy, budget *RetryBudget, breaker *CircuitBreaker, rng *Rng, clock Clock) *RetryingClient
func (c *RetryingClient) Charge(request ChargeRequest) ChargeOutcome

func NewGateway(store *IdempotencyStore, client *RetryingClient, clock Clock) *Gateway
func (g *Gateway) Charge(request ChargeRequest) ApiResponse
func (g *Gateway) Payment(id string) (*Payment, bool)           // Optional -> (value, ok)
func (g *Gateway) Payments() []*Payment                         // creation order, a fresh slice
```

Java's `LinkedHashMap` of payments becomes a map plus a slice of ids in creation order. The reason strings (`"approved"`, `"declined"`, `"retries exhausted"`, `"circuit open"`, `"retry budget exhausted"`) and the JSON bodies are byte-identical across the three languages; the tests compare them.

**`RetryingClient.charge` algorithm.** The order of the checks matters and the tests pin the reason strings exactly:

```text
if !breaker.allowRequest(): return (UNAVAILABLE, "", 0, 0, "circuit open")
attempts = 0, waited = 0
loop:
    attempts++
    result = processor.charge(request)
    if result is not UNAVAILABLE:
        breaker.recordSuccess(); budget.deposit(1)
        return (result.outcome, result.reference, attempts, waited, "approved" | "declined")
    breaker.recordFailure()
    if attempts >= policy.maxAttempts():  return (UNAVAILABLE, "", attempts, waited, "retries exhausted")
    if !breaker.allowRequest():           return (UNAVAILABLE, "", attempts, waited, "circuit open")
    if !budget.tryWithdraw():             return (UNAVAILABLE, "", attempts, waited, "retry budget exhausted")
    delay = policy.delayBefore(attempts, rng)     // retry number == attempts so far
    clock.sleep(delay); waited += delay
```

A decline counts as a *success* for the breaker and the budget: the processor answered, so it is healthy. Only `UNAVAILABLE` is a failure.

**`Gateway.charge` algorithm.**

```text
begin = store.begin(merchant, key, request.fingerprint())
REPLAY      -> return begin.response().asReplay()
IN_PROGRESS -> 409 {"error":"request in progress"}
CONFLICT    -> 422 {"error":"idempotency key reused with a different request"}
NEW:
    payment = new Payment("pay_<n>", amount, clock); payment.startProcessing()
    outcome = client.charge(request)
    APPROVED    -> payment.succeed(reference); response = 201 body(payment)
    DECLINED    -> payment.fail("declined " + reference); response = 402 body(payment)
    UNAVAILABLE -> payment.fail(reason); store.release(merchant, key)
                   return 503 {"error":"<reason>","payment_id":"pay_<n>"}   (not stored)
    store.complete(merchant, key, response); return response
```

`body(payment)` is `{"payment_id":"pay_1","state":"succeeded","amount":"19.99 USD"}`: the payment id, `state().wire()`, and `Money.toString()`. Payment ids are `pay_1`, `pay_2`, … in creation order; a released 503 still consumed an id and left a `FAILED` payment behind, which is correct: the attempt happened and belongs in the audit trail.

What must fail and how:

| Situation | Expected |
|---|---|
| `new BackoffPolicy(500, 100, 3)`, `maxAttempts 0`, `ceilingFor(0)` | `IllegalArgumentException` |
| `new RetryBudget(5, 3)`, `deposit(-1)` | `IllegalArgumentException` |
| `new CircuitBreaker(clock, 1, 0, 1)` (zero open window) | `IllegalArgumentException` |
| `new ScriptedProcessor("AXU")` or `""` | `IllegalArgumentException` |
| `Rng.nextBelow(0)` | `IllegalArgumentException` |
| Processor declines | exactly one call, no sleep, `reason = "declined"` |
| Breaker trips between attempts | stop immediately with `"circuit open"`; the next `charge` makes zero processor calls |
| Budget empty before a retry | stop with `"retry budget exhausted"` |
| Gateway gets `UNAVAILABLE` | 503, key released, the next identical request is `NEW` (not a replay) |

## Design notes

**Why full jitter rather than equal jitter or none.** With no jitter, every client that failed at the same instant retries at the same instant; the processor sees a wall of traffic at t + 100, t + 300, t + 700. Full jitter spreads attempt `k` over `[0, ceiling]`, so the retry load is roughly uniform. The AWS Architecture Blog analysis ("Exponential Backoff and Jitter", 2015) measured full jitter as both the lowest total work and the lowest completion time among the common variants.

**Why a retry budget on top of backoff.** Backoff slows down each client; it does not bound the *aggregate*. If 10,000 charges arrive during a two-minute outage and each is allowed four attempts, the processor receives 40,000 calls while it is trying to come back. A token budget shared by all calls on the gateway caps retries as a fraction of successes: every definitive answer deposits one token, every retry withdraws one, so retries can never exceed roughly the success rate plus the initial balance. Google's SRE book describes the same mechanism as a per-client "retry budget" of about 10% of requests.

**Why the breaker trips on consecutive failures and probes with one call.** Consecutive-failure counting is the simplest signal that is not fooled by a single blip; production breakers usually use a sliding-window error rate instead. HALF_OPEN lets one request through as a probe; if it fails the circuit reopens for another full window, if `successesToClose` probes pass the circuit closes. The point is that while OPEN, `charge()` returns in microseconds without touching the budget or the schedule.

**Why 503 releases the key and 402 does not.** A decline is a definitive answer about *this* request: the same card, amount and merchant will be declined again, and replaying the 402 is correct. An unavailable processor says nothing about the request; the merchant should be able to retry with the same key and have the gateway actually try again. If the 503 were stored, the key would be poisoned for the whole TTL. `Retry-After` on the 503, derived from `breaker.retryAfterMillis()`, is the natural extension.

## Run the grader

```bash
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 3 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 3 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 3 --path my-gateway
```

Stages 1 and 2 run again (89 assertions), then Stage 3's 59.

## Check yourself

1. Attempt 1 fails at t = 0, the breaker is still CLOSED, and the budget has tokens. Which of the four checks after `recordFailure()` runs first, and why does `retries exhausted` take precedence over `circuit open`?
2. A processor approves the charge but the response is lost and your client sees a timeout, classified `UNAVAILABLE`. What does your client do next, what prevents a double charge, and whose idempotency key does the work?
3. Why does a `DECLINED` answer deposit a token into the retry budget and reset the breaker's failure count?
4. The `Gateway` creates the `Payment` and calls `startProcessing()` *before* calling the client. What would be wrong with creating it only after an `APPROVED` outcome?
