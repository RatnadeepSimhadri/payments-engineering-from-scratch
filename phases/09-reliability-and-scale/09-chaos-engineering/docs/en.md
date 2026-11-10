# Chaos Engineering and Fault Injection

> Kill the processor connection on purpose, in test.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 5 Lesson 02 (Failure Models), Phase 9 Lesson 01 (SLIs, SLOs, and Error Budgets), Phase 9 Lesson 06 (Metrics and Histograms)
**Time:** ~75 minutes

## Learning Objectives

- Explain why a system that has only ever been tested against the failures its authors imagined will fail in production against the ones they did not
- Build a fault-injection layer that sits between a payment gateway and a card processor and can add latency, return errors, cut the link, and skew the processor's clock, each with an explicit blast radius
- Structure an experiment as steady state, hypothesis, injection, abort condition, and report, and run it deterministically against a seeded simulation
- Prove with a test that the gateway's retry never authorizes a payment twice under any of the four faults
- Read an experiment report and turn a violated hypothesis into a concrete engineering change

## The Problem

A payment gateway sits between merchants and a card processor. Its authors tested it well: unit tests for every state transition, integration tests against a processor sandbox, a load test at twice peak volume. It has an SLO of 99.9% good authorizations with a p99 under 1.5 seconds, and it has met that SLO for eighteen months.

On a Tuesday the processor rolls out a new signing-key rotation service in one of its data centres. The rotation service has a bug: it syncs time from a decommissioned NTP pool, and over four hours the clock on that fleet drifts six minutes ahead. The processor validates every request's timestamp against its own clock with a five-minute window, as the API documentation says it does. Every authorization the gateway sends to that fleet is rejected as a stale request. The gateway treats the rejection as a permanent decline, does not retry, and tells 40% of merchants that their customers' cards were refused. The processor's own dashboards are green: it is rejecting invalid requests exactly as designed. The gateway's dashboards show a decline rate climbing from 2% to 40% and an on-call engineer who has never seen a `STALE_REQUEST` response code and has no runbook for it.

Nothing in the gateway's test suite could have found this, because nobody on the team imagined the processor's clock would be wrong. The test suite covers the failures its authors thought of. Production produces the ones they did not. The same gateway would also have surprised its authors on the day a network partition cut one availability zone off from the processor: the gateway's timeout was 2 seconds and its retry policy was "retry once on timeout", which means every affected request burned 4 seconds before failing, which means a 10% partition pushed the p99 from 120 milliseconds to 4,000 and blew the latency SLO while the success-rate SLO still looked acceptable.

Chaos engineering is the discipline of finding these before a Tuesday does. It is not "break things randomly". It is a controlled experiment: write down what normal looks like as a measurable steady state, state a hypothesis about what the system will do under a specific fault, inject that fault into a bounded blast radius with a tripwire that stops the experiment if it goes wrong, and compare the measurement with the hypothesis. When the hypothesis holds, you have evidence. When it is violated, you have a bug report with a reproduction. This lesson builds the injection layer, the experiment structure, and the report, over a simulated payment flow on a simulated clock, so that every result is replayable.

## The Concept

### Five parts of an experiment

Chaos engineering as a named practice comes from Netflix (Basiri et al., 2016, and the Principles of Chaos Engineering). Stripped of tooling, an experiment has five parts, and skipping any one of them turns the experiment into an outage.

| Part | Question it answers | In this lesson |
|------|---------------------|----------------|
| Steady state | What does normal look like, as numbers? | good rate >= 99%, p99 <= 1,500 ms over 500 requests |
| Hypothesis | What do we believe happens under the fault? | "a 300 ms slower processor stays inside the SLO" |
| Fault and blast radius | What do we break, and for whom? | `Latency(300)` on the gateway-to-processor link, 10% of requests |
| Abort condition | When do we stop regardless of the hypothesis? | good rate < 80% at any 50-request checkpoint |
| Report | What did we learn and what will we change? | baseline, experimental, recovery metrics; verdict; invariant |

The steady state must be written down before the fault is armed. If you cannot express normal as thresholds on SLIs, you cannot tell whether the experiment changed anything, and you have no abort condition because the abort condition is a steady-state threshold with a shorter window. In this lesson the runner refuses to inject anything when the baseline run does not meet the steady state: the report says `NOT RUN` and the injector is never armed.

The hypothesis is a sentence about behaviour, not a wish. "The system will be fine" is not a hypothesis. "Requests beyond the timeout are retried once with the same idempotency key, so the good rate stays above 99% and no payment is authorized twice" is a hypothesis, because it names the mechanism (retry with idempotency key) and the measurable consequences (good rate, authorization count), and the experiment can show it is wrong.

### The four faults

Real infrastructure fails in a small number of ways, and the fault model from Phase 5 Lesson 01 enumerates them. This lesson injects four, chosen because together they cover almost every production payments incident that is not a code bug.

| Fault | What it models | How a caller sees it | What it tends to find |
|-------|----------------|----------------------|-----------------------|
| `Latency(ms)` | slow network, GC pause, saturated downstream | responses still arrive, later | timeouts set too tight or too loose, retry storms, p99 breaches |
| `Error(code)` | downstream 5xx, deploy gone wrong, dependency down | fast, explicit failure | missing retry classification, wrong fallback, alert gaps |
| `Partition()` | cut link, firewall change, AZ isolation | requests vanish; the caller sees only its own timeout | timeout plus retry doubling latency, no circuit breaker, no fail-fast |
| `ClockSkew(ms)` | bad NTP, VM pause, leap-second handling | the callee rejects requests as stale or future-dated | signing windows, token expiry, idempotency TTLs, certificate validity |

Notice the third column for `Partition`. From the caller's seat a partition is indistinguishable from a very slow dependency: the request leaves and nothing comes back. The gateway's timeout is what converts "nothing" into a result, which is why the timeout value is the single most important number in a partition experiment and why the `Partition` fault in this lesson is implemented as "latency exceeds any timeout".

Clock skew is the fault teams forget, and it is the one that produced the incident in The Problem. Payment APIs sign requests with a timestamp and reject anything outside a window (typically five minutes); OAuth tokens expire by wall clock; idempotency keys have a time-to-live. Every one of those is a dependency on two clocks agreeing. Phase 5 Lesson 02 showed that they do not have to; this lesson shows what breaks when they do not.

### Blast radius

Production chaos is safe only because the fault reaches a small, known population. Three shapes of blast radius appear in practice, and the lesson implements all three as a predicate over the request:

- **A percentage of requests**, chosen with a seeded random source. Good for latency and error faults where you want statistical coverage without picking on anyone.
- **A set of merchants (or tenants, or users)**: canaries. Good for partitions and anything where the affected party needs to know in advance. In production the set is usually internal test merchants or a merchant who has agreed to be a canary.
- **Everyone.** Only in a test environment, or when the fault is believed to be harmless and the abort condition is tight.

The blast radius is a property of the injection, not of the fault. The same `Latency(300)` can hit 1% of requests in production on Monday and everyone in staging on Tuesday. Keeping them separate is what lets the report say "fault X at radius Y produced Z", which is the sentence the postmortem needs.

### The abort condition is not optional

An experiment is a bet that the hypothesis is right. The abort condition is what you pay when it is wrong. It is evaluated on the cumulative experimental metrics at fixed checkpoints, and when it fires three things happen in order: the injector clears every fault, the run stops, and the report records how many requests were affected. The abort threshold is looser than the steady state (80% versus 99% here) because the steady state is measured over a full window and a short checkpoint is noisier; an abort set at the SLO threshold would fire on noise and teach you nothing.

The abort path must be the simplest code in the system. In the lesson it is one method call, `injector.clear()`, with no dependency on the thing being broken. A chaos tool whose off switch goes through the service mesh it is partitioning is a well-documented way to turn an experiment into an incident.

### Worked example: the latency experiment by hand

Steady state: good rate >= 99%, p99 <= 1,500 ms. Fault: `Latency(2500)` on 10% of requests. Gateway timeout 2,000 ms; retry once on timeout with the same idempotency key. Baseline latency 80 to 120 ms.

Take one affected request. The first attempt leaves at t=0, the network adds 2,500 ms to a 100 ms base, so a reply would arrive at t=2,600, but the gateway stops waiting at t=2,000 and records a timeout. The retry leaves at t=2,000 with the same idempotency key. Whether the retry is also affected is a fresh 10% draw. If it is not, the reply arrives around t=2,100 and the request succeeds in 2,100 ms with two attempts. If it is, the second attempt also times out at t=4,000 and the outcome is `TIMEOUT`.

Over 500 requests, about 50 are affected on the first attempt and about 5 of those on the retry too. The expected good rate is roughly 99%, right at the steady-state edge, and the p99 is 4,000 ms because more than 1% of requests took the full two timeouts. The run in `Main` records `good=98.4% p99=4000ms timeout=8`, and the verdict is `HYPOTHESIS VIOLATED`. The hypothesis said "retried once, no double authorization, SLO holds". Two of the three were right: the retry happened and no payment was authorized twice (the processor deduplicates by idempotency key and the report's invariant line says `HELD`). The third was wrong, and the report tells you exactly why: the p99 went to twice the timeout. The engineering change that follows is a decision, not a guess: lower the timeout, make the retry budget-aware, or accept a looser latency SLO.

```mermaid
flowchart LR
  G[Gateway<br/>timeout 2000 ms<br/>retry once, same idempotency key] -->|AuthRequest| N[SimNetwork<br/>base 80-120 ms]
  N -->|faultsFor link| I[FaultInjector<br/>Latency / Partition]
  N -->|faultsFor processor| I2[FaultInjector<br/>Error / ClockSkew]
  N -->|deliver| P[Processor<br/>timestamp window 5 min<br/>dedup by idempotency key]
  P -->|Outcome| N --> G
  R[ExperimentRunner] -->|baseline, inject, observe, clear, recover| G
  R -->|checkpoints| A{Abort?}
  A -->|yes| C[injector.clear]
  R --> Rep[ExperimentReport<br/>verdict + invariant]
```

### What the experiment proves and what it does not

An experiment that holds proves the hypothesis for that fault, that blast radius, that traffic mix, and that build. It does not prove the system is resilient. The latency experiment at 300 ms held; the one at 2,500 ms did not; nothing in the first result predicted the second. Chaos engineering produces a growing set of specific, replayable facts, not a certificate.

It also does not find bugs in the code path that was not exercised. The processor in this lesson deduplicates by idempotency key, so the "no double authorization" invariant holds under every fault. If the processor forgot to persist the dedup table, this lesson's experiments would find it on the first retry. If the bug were in a refund path, they would not, because no experiment sends refunds. The experiments you run are bounded by the hypotheses you write, which is why GameDays are a team exercise: the person who wrote the retry logic is the worst person to guess how it fails.

### GameDays

A GameDay is a scheduled block of time, a named incident commander, a list of experiments with hypotheses written in advance, and a rule that the on-call engineer treats whatever happens as a real incident. The structure forces the experiments to be prepared (you cannot write a hypothesis on the day), bounds the risk (the commander owns the abort), and tests the humans and runbooks along with the software. The `GameDay` class in this lesson is the minimum: an ordered list of experiments and a summary table. Lesson 12 covers the incident-response side.

## Build It

The whole implementation is in `code/java/Main.java`. It has three layers: the simulated payment flow (clock, network, processor, gateway), the fault-injection layer that the network consults, and the experiment machinery (steady state, abort, runner, report, GameDay). Money appears as the Phase 10 Lesson 01 record because every request carries an amount and the invariant is about money.

### Step 1: Faults as values

```java
sealed interface Fault permits Fault.Latency, Fault.Error, Fault.Partition, Fault.ClockSkew {
    record Latency(long addedMillis) implements Fault {
        public Latency { if (addedMillis < 0) throw new IllegalArgumentException("latency must be >= 0"); }
    }
    record Error(String code) implements Fault {}
    record Partition() implements Fault {}
    record ClockSkew(long skewMillis) implements Fault {}
}
```

A fault is data, not a callback. That is what makes it loggable (`Latency[addedMillis=300]` appears verbatim in the report), comparable in a test, and replayable from a saved experiment file. The sealed interface means `SimNetwork` can pattern-match over the four cases and the compiler will complain when a fifth is added.

**Predict:** why is `ClockSkew` a fault on the processor rather than on the link?

### Step 2: Blast radius as a predicate

```java
sealed interface BlastRadius permits BlastRadius.PercentOfRequests, BlastRadius.Merchants, BlastRadius.Everyone {
    boolean includes(AuthRequest request, Random rng);

    record PercentOfRequests(int percent) implements BlastRadius {
        public boolean includes(AuthRequest request, Random rng) { return rng.nextInt(100) < percent; }
    }
    record Merchants(Set<String> merchantIds) implements BlastRadius {
        public boolean includes(AuthRequest request, Random rng) { return merchantIds.contains(request.merchantId()); }
    }
    record Everyone() implements BlastRadius {
        public boolean includes(AuthRequest request, Random rng) { return true; }
    }
}
```

The random source is passed in rather than owned, so the injector controls the seed and two runs with the same seed affect the same requests. `Merchants` ignores the random source entirely; a canary is a canary on every request.

### Step 3: The injector

```java
final class FaultInjector {
    private final List<Injection> injections = new ArrayList<>();
    private final Random rng;
    private int applied = 0;

    void inject(String target, Fault fault, BlastRadius radius) { injections.add(new Injection(target, fault, radius)); }
    void clear() { injections.clear(); }
    boolean isArmed() { return !injections.isEmpty(); }

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
```

Targets are strings (`"gateway->processor"` for the link, `"processor"` for the callee) because in production they are service names and route labels. `clear()` is the abort path and is deliberately trivial: no I/O, no dependency, cannot fail. `applied` is the count the report uses to say how many requests were actually touched, which is a different number from the blast radius you asked for.

**Predict:** an injection targets `"processor"` with `PercentOfRequests(10)`. Over 500 requests with one retry each on some of them, is `appliedCount()` closer to 50 or to 55, and why?

### Step 4: The processor, with its own clock

```java
final class Processor {
    static final long TIMESTAMP_TOLERANCE_MILLIS = 5 * 60 * 1000L;
    private long clockSkewMillis = 0;
    private final Map<String, Outcome> byIdempotencyKey = new HashMap<>();
    private final Map<String, Integer> authorizationsByPayment = new HashMap<>();

    long localNowMillis() { return clock.nowMillis() + clockSkewMillis; }

    Outcome handle(AuthRequest request) {
        long age = Math.abs(localNowMillis() - request.sentAtMillis());
        if (age > TIMESTAMP_TOLERANCE_MILLIS) {
            throw new StaleRequestException("request timestamp outside signing window by " + age + " ms");
        }
        Outcome seen = byIdempotencyKey.get(request.idempotencyKey());
        if (seen != null) return seen;
        Outcome outcome = rng.nextInt(100) < 2 ? Outcome.DECLINED : Outcome.AUTHORIZED;
        byIdempotencyKey.put(request.idempotencyKey(), outcome);
        if (outcome == Outcome.AUTHORIZED) authorizationsByPayment.merge(request.paymentId(), 1, Integer::sum);
        return outcome;
    }
}
```

Two things to notice. The processor validates the request timestamp against its own view of time, `localNowMillis()`, which the `ClockSkew` fault offsets; that is how six minutes of skew produces `STALE_REQUEST` on every request even though the gateway's clock is correct. And the processor is the system that has to hold the invariant: the idempotency map returns the first answer on a replay, so a retry with the same key can never produce a second authorization. `authorizationsByPayment` exists only so the test and the report can check that.

### Step 5: The network applies faults in the order they happen

```java
Outcome call(AuthRequest request, Processor processor, long timeoutMillis) {
    long latency = 80 + rng.nextInt(41);
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
    } catch (ProcessorErrorException e) { return Outcome.PROCESSOR_ERROR; }
    catch (StaleRequestException e) { return Outcome.STALE_REQUEST; }
    finally { processor.setClockSkew(0); }
}
```

Link faults are resolved first because they decide whether the request arrives at all. A partition or a latency beyond the timeout advances the clock by exactly the timeout and returns `TIMEOUT` without ever calling the processor, which is what happens in reality: the processor never saw the request, so there is nothing to deduplicate and nothing was authorized. Only requests that arrive are subject to processor faults. The clock advances by the observed latency, never by sleeping, so 500 requests with 4-second timeouts take milliseconds of real time.

**Predict:** a request is affected by both `Latency(2500)` on the link and `Error("HTTP_500")` on the processor. Which outcome does the gateway see, and does the processor's error counter move?

### Step 6: The gateway under test

```java
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
```

The gateway is deliberately plain: a 2-second timeout, one retry, retry only on timeout and 5xx. The idempotency key is derived from the payment id and reused across attempts, which is the mechanism from Phase 12 that the invariant depends on. `STALE_REQUEST` and `DECLINED` are not retried: a decline is an answer, and a stale request will be just as stale a hundred milliseconds later. Whether that classification is right is exactly the kind of thing an experiment tests; the clock-skew experiment shows that treating `STALE_REQUEST` as terminal turns a clock problem into a 100% failure rate.

### Step 7: Steady state, abort, and the runner

```java
record SteadyState(double minGoodRatePercent, long maxP99Millis) {
    boolean holds(Metrics m) { return m.goodRatePercent() >= minGoodRatePercent && m.p99Millis() <= maxP99Millis; }
}

record AbortCondition(double minGoodRatePercent, int checkEvery) {
    boolean triggered(Metrics m) { return m.goodRatePercent() < minGoodRatePercent; }
}
```

Both are thresholds on SLIs from Lesson 01. `Metrics.p99Millis()` sorts the samples and takes the 99th percentile; it is never an average, for the reasons Lesson 06 gives. The runner is the experiment protocol in code:

```java
ExperimentReport run(Experiment e) {
    injector.clear();
    int[] stop = new int[1];
    Metrics baseline = drive(e.requests(), null, stop);
    boolean baselineHeld = e.steadyState().holds(baseline);
    if (!baselineHeld) { /* report NOT RUN; injector never armed */ }
    injector.inject(e.target(), e.fault(), e.blastRadius());
    Metrics experimental = drive(e.requests(), e.abort(), stop);
    boolean aborted = stop[0] > 0;
    int requestsBeforeAbort = aborted ? stop[0] : experimental.total();
    injector.clear();
    Metrics recovery = drive(e.requests(), null, stop);
    boolean hypothesisHeld = !aborted && e.steadyState().holds(experimental);
    return new ExperimentReport(e, baseline, experimental, recovery, true, aborted, requestsBeforeAbort,
            hypothesisHeld, e.steadyState().holds(recovery), processor.noDoubleAuthorizations());
}
```

Five phases: establish the baseline, arm, observe with abort checkpoints, clear, verify recovery. The recovery run matters as much as the experimental one: a system that stays broken after the fault is removed (a retry storm that keeps feeding itself, a circuit breaker stuck open, a cache full of errors) is a worse finding than one that degrades under the fault. `drive` checks the abort condition every `checkEvery` requests and returns early, and the `finally`-style `injector.clear()` runs whether or not it aborted.

**Predict:** the abort fires after 50 requests. How many requests of the 500 planned were actually affected, at most, and what does the report print for `requestsBeforeAbort`?

### Step 8: Run it

```bash
cd phases/09-reliability-and-scale/09-chaos-engineering/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Abbreviated output:

```console
Steady state: good rate >= 99.0%, p99 <= 1500 ms
Abort if good rate < 80.0% at any 50-request checkpoint

Experiment: latency +300ms, 10% of requests
  Baseline:     n=500 good=100.0% p99=120ms auth=488 decl=12 timeout=0 error=0 stale=0
  Experimental: n=500 good=100.0% p99=417ms auth=490 decl=10 timeout=0 error=0 stale=0
  Verdict: HYPOTHESIS HELD

Experiment: latency +2500ms, 10% of requests
  Experimental: n=500 good=98.4% p99=4000ms auth=486 decl=6 timeout=8 error=0 stale=0
  Invariant (no payment authorized twice): HELD
  Verdict: HYPOTHESIS VIOLATED

Experiment: processor 500s, 70% of requests
  Experimental: n=50 good=56.0% p99=231ms auth=27 decl=1 timeout=0 error=22 stale=0
  Verdict: ABORTED after 50 requests

Experiment: partition, canary merchant m-3
  Experimental: n=500 good=92.0% p99=4000ms auth=446 decl=14 timeout=40 error=0 stale=0
  Verdict: HYPOTHESIS VIOLATED

Experiment: processor clock +6 minutes, everyone
  Experimental: n=50 good=0.0% p99=120ms auth=0 decl=0 timeout=0 error=0 stale=50
  Verdict: ABORTED after 50 requests

GameDay summary
  latency +300ms, 10% of requests              HYPOTHESIS HELD
  latency +2500ms, 10% of requests             HYPOTHESIS VIOLATED
  processor 500s, 70% of requests              ABORTED after 50 requests
  partition, canary merchant m-3               HYPOTHESIS VIOLATED
  processor clock +6 minutes, everyone         ABORTED after 50 requests
```

Read the five verdicts as five findings. The first is evidence that 300 ms of extra latency is absorbed. The second says the retry works but the timeout is too long for the latency SLO. The third says the abort tripwire works. The fourth says a partition for one merchant in ten doubles that merchant's latency to 4 seconds, because retry-on-timeout against a dead link is a second timeout, not a second chance. The fifth is the incident from The Problem reproduced in a test: six minutes of processor clock skew is total failure, which argues for monitoring the processor's `Date` header against your own clock and for treating `STALE_REQUEST` as a page, not a decline.

### In Python

The Python port (`code/python/main.py`) is the same design with four differences. **Sealed interfaces become a base class plus frozen dataclasses:** `Fault` and `BlastRadius` are plain classes; `Latency`, `Error`, `Partition`, `ClockSkew` and `PercentOfRequests`, `Merchants`, `Everyone` are `@dataclass(frozen=True)` subclasses attached as `Fault.Latency` and so on, so call sites read like the Java. The network uses `match` with class patterns where Java uses `instanceof` pattern matching. Nothing stops a third party adding a fifth `Fault` in Python; the "sealed" part is a convention here, not a compiler guarantee. **Every RNG is `java.util.Random`, not `random.Random`:** `JavaRandom.next_int` ports the 48-bit LCG and its rejection loop, so seeds 7, 11, 13, 17 produce the same blast-radius draws, declines, latencies, and merchant ids as the Java demo, and the GameDay numbers (488/12 baseline, p99 417 ms, 298 faults applied) match exactly rather than approximately. **Exceptions and the `finally`:** `ProcessorErrorException` and `StaleRequestException` subclass `RuntimeError`; `SimNetwork.call` keeps the same `try`/`except`/`finally` so the processor's clock skew is always reset, and every `IllegalArgumentException` is a `ValueError`. **The abort index is a return value:** Java smuggles `stoppedAt` out of `drive` through a one-element `int[]`; Python returns `(metrics, stopped_at)` as a tuple. `Money.__post_init__` additionally rejects floats, since nothing else in Python would.

Run it from `code/python/` with `python3 main.py`; test with `python3 -m unittest -v`.

#### Step 1: Faults as values

Four frozen dataclasses under an empty `Fault` base; each `__str__` renders the Java record form so the report text is identical. The last line attaches them as `Fault.Latency` and friends, which is the Python spelling of the nested sealed types.

```python
class Fault:
    """The four faults this lesson injects. Each is a value: it can be logged, diffed, and replayed."""


@dataclass(frozen=True)
class Latency(Fault):
    added_millis: int

    def __post_init__(self) -> None:
        if self.added_millis < 0:
            raise ValueError("latency must be >= 0")

    def __str__(self) -> str:
        return f"Latency[addedMillis={self.added_millis}]"


@dataclass(frozen=True)
class Error(Fault):
    code: str


@dataclass(frozen=True)
class Partition(Fault):
    def __str__(self) -> str:
        return "Partition[]"


@dataclass(frozen=True)
class ClockSkew(Fault):
    skew_millis: int


Fault.Latency, Fault.Error, Fault.Partition, Fault.ClockSkew = Latency, Error, Partition, ClockSkew
```

#### Step 2: Blast radius as a predicate

Same shape: a base class with `includes`, three frozen dataclasses. `Merchants` holds a `frozenset` (hashable, so the dataclass stays frozen) and prints it sorted; `PercentOfRequests` draws from the injector's `JavaRandom`, never its own.

```python
class BlastRadius:
    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        raise NotImplementedError


@dataclass(frozen=True)
class PercentOfRequests(BlastRadius):
    percent: int

    def __post_init__(self) -> None:
        if self.percent < 0 or self.percent > 100:
            raise ValueError("percent 0..100")

    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return rng.next_int(100) < self.percent


@dataclass(frozen=True)
class Merchants(BlastRadius):
    merchant_ids: frozenset[str]

    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return request.merchant_id in self.merchant_ids


@dataclass(frozen=True)
class Everyone(BlastRadius):
    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return True
```

#### Step 3: The injector

A list of `Injection`s and one seeded `JavaRandom`. `clear()` is the abort path and is a list clear; `faults_for` counts every applied fault so the report can say how many requests were actually touched.

```python
class FaultInjector:
    def __init__(self, seed: int) -> None:
        self._injections: list[Injection] = []
        self._rng = JavaRandom(seed)
        self._applied = 0

    def inject(self, target: str, fault: Fault, radius: BlastRadius) -> None:
        self._injections.append(Injection(target, fault, radius))

    def clear(self) -> None:
        self._injections.clear()

    def is_armed(self) -> bool:
        return bool(self._injections)

    def applied_count(self) -> int:
        return self._applied

    def faults_for(self, target: str, request: AuthRequest) -> list[Fault]:
        out: list[Fault] = []
        for inj in self._injections:
            if inj.target == target and inj.blast_radius.includes(request, self._rng):
                out.append(inj.fault)
                self._applied += 1
        return out
```

#### Step 4: The processor, with its own clock

The processor checks the request timestamp against `local_now_millis()`, which the `ClockSkew` fault offsets, and raises `StaleRequestException` outside the window. The idempotency dict returns the first answer on a replay; the 2% decline comes from the processor's own `JavaRandom`.

```python
    def local_now_millis(self) -> int:
        return self._clock.now_millis() + self._clock_skew_millis

    def handle(self, request: AuthRequest) -> Outcome:
        age = abs(self.local_now_millis() - request.sent_at_millis)
        if age > self.TIMESTAMP_TOLERANCE_MILLIS:
            raise StaleRequestException(f"request timestamp outside signing window by {age} ms")
        seen = self._by_idempotency_key.get(request.idempotency_key)
        if seen is not None:
            return seen  # replay: same answer, no second authorization
        outcome = Outcome.DECLINED if self._rng.next_int(100) < 2 else Outcome.AUTHORIZED
        self._by_idempotency_key[request.idempotency_key] = outcome
        if outcome == Outcome.AUTHORIZED:
            self._authorizations_by_payment[request.payment_id] = self._authorizations_by_payment.get(request.payment_id, 0) + 1
        return outcome

    def no_double_authorizations(self) -> bool:
        return all(n <= 1 for n in self._authorizations_by_payment.values())
```

#### Step 5: The network applies faults in the order they happen

Link faults first, matched with `match`/`case` class patterns; a partition or over-timeout latency advances the clock by the timeout and returns `TIMEOUT` without touching the processor. Processor faults are matched the same way, and the `finally` resets the skew whatever happens.

```python
    def call(self, request: AuthRequest, processor: Processor, timeout_millis: int) -> Outcome:
        latency = 80 + self._rng.next_int(41)  # 80..120 ms baseline
        partitioned = False
        for f in self._injector.faults_for(self.LINK_GATEWAY_TO_PROCESSOR, request):
            match f:
                case Fault.Latency(added_millis=added):
                    latency += added
                case Fault.Partition():
                    partitioned = True
        if partitioned or latency > timeout_millis:
            self._clock.advance(timeout_millis)
            return Outcome.TIMEOUT
        self._clock.advance(latency)
        skew = 0
        error_code: Optional[str] = None
        for f in self._injector.faults_for(self.TARGET_PROCESSOR, request):
            match f:
                case Fault.ClockSkew(skew_millis=s):
                    skew = s
                case Fault.Error(code=c):
                    error_code = c
        processor.set_clock_skew(skew)
        try:
            if error_code is not None:
                raise ProcessorErrorException(error_code)
            return processor.handle(request)
        except ProcessorErrorException:
            return Outcome.PROCESSOR_ERROR
        except StaleRequestException:
            return Outcome.STALE_REQUEST
        finally:
            processor.set_clock_skew(0)
```

#### Step 6: The gateway under test

A 2-second timeout, at most two attempts, the same idempotency key across attempts, and a retry only on `TIMEOUT` or `PROCESSOR_ERROR` (the `not in` tuple test).

```python
    def authorize(self, payment_id: str, merchant_id: str, amount: Money) -> AuthResult:
        started = self._clock.now_millis()
        idempotency_key = "idem-" + payment_id
        outcome = Outcome.TIMEOUT
        attempts = 0
        while attempts < self.MAX_ATTEMPTS:
            attempts += 1
            request = AuthRequest(payment_id, merchant_id, amount, idempotency_key, self._clock.now_millis())
            outcome = self._network.call(request, self._processor, self.TIMEOUT_MILLIS)
            if outcome not in (Outcome.TIMEOUT, Outcome.PROCESSOR_ERROR):
                break
        return AuthResult(outcome, self._clock.now_millis() - started, attempts)
```

#### Step 7: Steady state, abort, and the runner

`SteadyState` and `AbortCondition` are frozen dataclasses with one predicate each. `_drive` returns `(metrics, stopped_at)` as a tuple, so the runner's five phases read without the `int[]` trick; `injector.clear()` runs on both the abort and the normal path before the recovery run.

```python
    def holds(self, m: Metrics) -> bool:
        return m.good_rate_percent() >= self.min_good_rate_percent and m.p99_millis() <= self.max_p99_millis
```

```python
    def run(self, e: Experiment) -> ExperimentReport:
        self._injector.clear()
        baseline, _ = self._drive(e.requests, None)
        baseline_held = e.steady_state.holds(baseline)
        if not baseline_held:
            empty = Metrics()
            return ExperimentReport(e, baseline, empty, empty, False, False, 0, False, False,
                                    self._processor.no_double_authorizations())
        self._injector.inject(e.target, e.fault, e.blast_radius)
        experimental, stop = self._drive(e.requests, e.abort)
        aborted = stop > 0
        requests_before_abort = stop if aborted else experimental.total()
        self._injector.clear()  # the abort path and the normal path both end here
        recovery, _ = self._drive(e.requests, None)
        hypothesis_held = not aborted and e.steady_state.holds(experimental)
        return ExperimentReport(e, baseline, experimental, recovery, True, aborted, requests_before_abort,
                                hypothesis_held, e.steady_state.holds(recovery), self._processor.no_double_authorizations())
```

#### Step 8: Run it

```bash
cd phases/09-reliability-and-scale/09-chaos-engineering/code/python
python3 main.py
python3 -m unittest -v
```

Every number in the five experiments matches the Java run exactly:

```console
Experiment: latency +300ms, 10% of requests
  Hypothesis: A 300 ms slower processor stays inside the 2 s timeout; p99 rises but the SLO holds
  Fault: Latency[addedMillis=300] on gateway->processor, blast radius PercentOfRequests[percent=10]
  Baseline:     n=500 good=100.0% p99=120ms auth=488 decl=12 timeout=0 error=0 stale=0
  Experimental: n=500 good=100.0% p99=417ms auth=490 decl=10 timeout=0 error=0 stale=0
  Recovery:     n=500 good=100.0% p99=120ms auth=496 decl=4 timeout=0 error=0 stale=0
  Invariant (no payment authorized twice): HELD
  Recovered to steady state after clearing faults: yes
```

### In Go

The Go port (`code/go/main.go` and `javarandom.go`) keeps the design with five differences. **Sealed interfaces become interfaces with an unexported method:** `Fault` declares `fault()` and `String()`; `Latency`, `Error`, `Partition`, and `ClockSkew` are the only types in the package that can implement it, which is as close to `sealed ... permits` as Go gets. `BlastRadius` works the same way with `Includes`. The network's `instanceof` chain is a type switch. Each type has a `String()` that renders the Java record form (`Latency[addedMillis=300]`, `Merchants[merchantIds=[m-3]]`), so the report text is identical. **Exceptions become `(Outcome, error)`:** `Processor.Handle` returns `ErrStaleRequest` (wrapped with the age) instead of throwing; `SimNetwork.Call` maps it to `STALE_REQUEST` with `errors.Is`, and the Java `finally` that resets the clock skew is a `defer processor.SetClockSkew(0)`. Constructor checks (`NewLatency`, `NewPercentOfRequests`) return `error` where Java throws `IllegalArgumentException`; `SimClock.Advance` panics on a negative step because that is a bug in the simulation, not a condition. **The abort index is a return value:** Java smuggles `stoppedAt` out of `drive` through a one-element `int[]`; Go returns `(*Metrics, int)` with `-1` for "ran to completion". **Every RNG is `java.util.Random`:** `JavaRandom.NextInt` ports the 48-bit LCG with its rejection loop, so seeds 7, 11, 13, 17 produce the same blast-radius draws, declines, latencies, and merchant ids as the Java demo, and the GameDay numbers (488/12 baseline, p99 417 ms, 298 faults applied) match exactly; `TestDemoParity` pins them. The processor's 2% decline and the 80..120 ms baseline both come from `NextInt`, which is why `math/rand` would have changed every row. **Sets and ordered maps:** `Set<String>` is `map[string]bool` built by `NewMerchants(ids...)`, printed sorted; the `LinkedHashMap<Outcome, Integer>` of counts is a plain `map[Outcome]int` because nothing iterates it in order.

Run it from `code/go/` with `go run .`; test with `go test ./...`.

#### Step 1: Faults as values

The unexported `fault()` method closes the set to this package. `NewLatency` is the validating constructor (a struct literal cannot reject a negative value), and the `String()` methods reproduce the Java record `toString`.

```go
type Fault interface {
	fault()
	String() string
}

type Latency struct{ AddedMillis int64 }

type Error struct{ Code string }

type Partition struct{}

type ClockSkew struct{ SkewMillis int64 }

func NewLatency(addedMillis int64) (Latency, error) {
	if addedMillis < 0 {
		return Latency{}, errors.New("latency must be >= 0")
	}
	return Latency{addedMillis}, nil
}

func (Latency) fault()   {}
func (Error) fault()     {}
func (Partition) fault() {}
func (ClockSkew) fault() {}

func (l Latency) String() string   { return fmt.Sprintf("Latency[addedMillis=%d]", l.AddedMillis) }
func (e Error) String() string     { return fmt.Sprintf("Error[code=%s]", e.Code) }
```

#### Step 2: Blast radius as a predicate

The interface takes the injector's `*JavaRandom` so the seed is controlled centrally. `Merchants` is a `map[string]bool` filled by the variadic `NewMerchants`; `Everyone` ignores both arguments.

```go
type BlastRadius interface {
	Includes(request AuthRequest, rng *JavaRandom) bool
	String() string
}

type PercentOfRequests struct{ Percent int }

type Merchants struct{ MerchantIds map[string]bool }

type Everyone struct{}

func NewPercentOfRequests(percent int) (PercentOfRequests, error) {
	if percent < 0 || percent > 100 {
		return PercentOfRequests{}, errors.New("percent 0..100")
	}
	return PercentOfRequests{percent}, nil
}

func NewMerchants(ids ...string) Merchants {
	m := Merchants{map[string]bool{}}
	for _, id := range ids {
		m.MerchantIds[id] = true
	}
	return m
}

func (p PercentOfRequests) Includes(_ AuthRequest, rng *JavaRandom) bool {
	return rng.NextInt(100) < p.Percent
}
func (m Merchants) Includes(r AuthRequest, _ *JavaRandom) bool { return m.MerchantIds[r.MerchantId] }
func (Everyone) Includes(AuthRequest, *JavaRandom) bool        { return true }
```

#### Step 3: The injector

`Clear` sets the slice to `nil`; `FaultsFor` ranges over the injections in order and counts each applied fault.

```go
type FaultInjector struct {
	injections []Injection
	rng        *JavaRandom
	applied    int
}

func NewFaultInjector(seed int64) *FaultInjector { return &FaultInjector{rng: NewJavaRandom(seed)} }

func (fi *FaultInjector) Inject(target string, fault Fault, radius BlastRadius) {
	fi.injections = append(fi.injections, Injection{target, fault, radius})
}

func (fi *FaultInjector) Clear()            { fi.injections = nil }
func (fi *FaultInjector) IsArmed() bool     { return len(fi.injections) > 0 }
func (fi *FaultInjector) AppliedCount() int { return fi.applied }

func (fi *FaultInjector) FaultsFor(target string, request AuthRequest) []Fault {
	var out []Fault
	for _, inj := range fi.injections {
		if inj.Target == target && inj.BlastRadius.Includes(request, fi.rng) {
			out = append(out, inj.Fault)
			fi.applied++
		}
	}
	return out
}
```

#### Step 4: The processor, with its own clock

`Handle` returns `(Outcome, error)`; a stale timestamp is `ErrStaleRequest` wrapped with the age. The comma-ok lookup is the idempotency replay, and `authorizationsByPayment[...]++` works on a missing key.

```go
func (p *Processor) Handle(request AuthRequest) (Outcome, error) {
	age := p.LocalNowMillis() - request.SentAtMillis
	if age < 0 {
		age = -age
	}
	if age > TimestampToleranceMillis {
		return 0, fmt.Errorf("%w: request timestamp outside signing window by %d ms", ErrStaleRequest, age)
	}
	if seen, ok := p.byIdempotencyKey[request.IdempotencyKey]; ok {
		return seen, nil // replay: same answer, no second authorization
	}
	outcome := AUTHORIZED
	if p.rng.NextInt(100) < 2 {
		outcome = DECLINED
	}
	p.byIdempotencyKey[request.IdempotencyKey] = outcome
	if outcome == AUTHORIZED {
		p.authorizationsByPayment[request.PaymentId]++
	}
	return outcome, nil
}
```

#### Step 5: The network applies faults in the order they happen

Two type switches replace the `instanceof` chains. `defer processor.SetClockSkew(0)` is the `finally`; an injected error short-circuits to `PROCESSOR_ERROR` before the processor is called, and the processor's own error is classified with `errors.Is`. Any other error is a bug and panics.

```go
func (n *SimNetwork) Call(request AuthRequest, processor *Processor, timeoutMillis int64) Outcome {
	latency := int64(80 + n.rng.NextInt(41)) // 80..120 ms baseline
	partitioned := false
	for _, f := range n.injector.FaultsFor(LinkGatewayToProcessor, request) {
		switch f := f.(type) {
		case Latency:
			latency += f.AddedMillis
		case Partition:
			partitioned = true
		}
	}
	if partitioned || latency > timeoutMillis {
		n.clock.Advance(timeoutMillis)
		return TIMEOUT
	}
	n.clock.Advance(latency)
	var skew int64
	var errorCode *string
	for _, f := range n.injector.FaultsFor(TargetProcessor, request) {
		switch f := f.(type) {
		case ClockSkew:
			skew = f.SkewMillis
		case Error:
			code := f.Code
			errorCode = &code
		}
	}
	processor.SetClockSkew(skew)
	defer processor.SetClockSkew(0) // the Java finally: skew never leaks into the next request
	if errorCode != nil {
		return PROCESSOR_ERROR
	}
	outcome, err := processor.Handle(request)
	switch {
	case errors.Is(err, ErrStaleRequest):
		return STALE_REQUEST
	case errors.Is(err, ErrProcessorError):
		return PROCESSOR_ERROR
	case err != nil:
		panic(err)
	}
	return outcome
}
```

#### Step 6: The gateway under test

Package constants for the timeout and attempt count; the loop is the Java one.

```go
func (g *Gateway) Authorize(paymentId, merchantId string, amount Money) AuthResult {
	started := g.clock.NowMillis()
	idempotencyKey := "idem-" + paymentId
	outcome := TIMEOUT
	attempts := 0
	for attempts < MaxAttempts {
		attempts++
		request := AuthRequest{paymentId, merchantId, amount, idempotencyKey, g.clock.NowMillis()}
		outcome = g.network.Call(request, g.processor, TimeoutMillis)
		if outcome != TIMEOUT && outcome != PROCESSOR_ERROR {
			break
		}
	}
	return AuthResult{outcome, g.clock.NowMillis() - started, attempts}
}
```

#### Step 7: Steady state, abort, and the runner

Value structs with one predicate method each. `drive` returns `(*Metrics, int)`; `Run` passes `&e.Abort` so a nil pointer means "no abort checks" on the baseline and recovery runs.

```go
type SteadyState struct {
	MinGoodRatePercent float64
	MaxP99Millis       int64
}

func (s SteadyState) Holds(m *Metrics) bool {
	return m.GoodRatePercent() >= s.MinGoodRatePercent && m.P99Millis() <= s.MaxP99Millis
}

type AbortCondition struct {
	MinGoodRatePercent float64
	CheckEvery         int
}

func (a AbortCondition) Triggered(m *Metrics) bool { return m.GoodRatePercent() < a.MinGoodRatePercent }
```

```go
func (r *ExperimentRunner) Run(e Experiment) ExperimentReport {
	r.injector.Clear()
	baseline, _ := r.drive(e.Requests, nil)
	baselineHeld := e.SteadyState.Holds(baseline)
	if !baselineHeld {
		empty := NewMetrics()
		return ExperimentReport{e, baseline, empty, empty, false, false, 0, false, false, r.processor.NoDoubleAuthorizations()}
	}
	r.injector.Inject(e.Target, e.Fault, e.BlastRadius)
	experimental, stop := r.drive(e.Requests, &e.Abort)
	aborted := stop > 0
	requestsBeforeAbort := experimental.Total()
	if aborted {
		requestsBeforeAbort = stop
	}
	r.injector.Clear() // the abort path and the normal path both end here
	recovery, _ := r.drive(e.Requests, nil)
	hypothesisHeld := !aborted && e.SteadyState.Holds(experimental)
	return ExperimentReport{e, baseline, experimental, recovery, true, aborted, requestsBeforeAbort,
		hypothesisHeld, e.SteadyState.Holds(recovery), r.processor.NoDoubleAuthorizations()}
}
```

#### Step 8: Run it

```bash
cd phases/09-reliability-and-scale/09-chaos-engineering/code/go
go run .
go test ./...
```

The five verdicts and the fault count match the Java run:

```console
GameDay summary
  latency +300ms, 10% of requests              HYPOTHESIS HELD
  latency +2500ms, 10% of requests             HYPOTHESIS VIOLATED
  processor 500s, 70% of requests              ABORTED after 50 requests
  partition, canary merchant m-3               HYPOTHESIS VIOLATED
  processor clock +6 minutes, everyone         ABORTED after 50 requests

Faults applied in total: 298; injector armed now: false
```

## Use It

### Chaos tooling

The structure in this lesson is what production tools implement with real infrastructure instead of a simulated network. [Chaos Monkey](https://netflix.github.io/chaosmonkey/) terminates instances; [Chaos Mesh](https://chaos-mesh.org/docs/) injects pod kills, network latency, partitions, DNS failures, and clock skew (`TimeChaos`) into Kubernetes workloads, with a CRD per experiment that has a `selector` (the blast radius) and a `duration`; [Gremlin](https://www.gremlin.com/docs/) and [AWS Fault Injection Service](https://docs.aws.amazon.com/fis/latest/userguide/what-is.html) add the abort condition as a first-class "stop condition" tied to a CloudWatch alarm, which is the production form of `AbortCondition`. What these do that the from-scratch version makes explicit: the steady-state measurement comes from your real metrics pipeline (Lesson 06), the blast radius is enforced by labels and IAM rather than a predicate, and the abort is a monitored alarm rather than an in-process checkpoint. What they cannot do for you: write the hypothesis.

### Fault injection inside the code

Toxiproxy and Envoy's fault-injection filter sit on the network path exactly where `SimNetwork` does, adding latency or aborting a percentage of requests by header or route. Java's `java.net.http.HttpClient` and Go's `net/http` both allow a wrapping transport, which is where teams put a `FaultInjector` for integration tests. The idea of a deterministic simulated network you can replay is the basis of FoundationDB's simulation testing and of Jepsen's nemesis, both of which found consensus bugs that years of production had not; Phase 6 used the same approach for Raft.

### Clock skew specifically

Google's [Spanner paper](https://research.google/pubs/spanner-googles-globally-distributed-database/) exposes clock uncertainty as an interval rather than a point for exactly the reason the fifth experiment fails: a timestamp compared with another machine's clock is a comparison with an error bar. Processors publish their signing-window rules; Stripe's webhook signatures, for example, carry a timestamp and the docs recommend a five-minute tolerance. Monitoring the difference between a dependency's `Date` response header and your own clock is a one-line SLI that would have caught the incident in The Problem four hours early.

## Ship It

`outputs/runbook-gameday.md` is a GameDay plan template: the experiment table with hypothesis, fault, blast radius, abort condition, and owner; the pre-flight checklist; the roles; and the report skeleton. Fill it in a week before the GameDay, run it as a real incident, and attach the filled copy to the postmortem (Lesson 12).

## Exercises

1. **Reinforce.** Add a fifth fault, `Fault.Corruption(fieldName)`, that makes the processor receive a request with a different amount than the gateway sent (for example off by one minor unit). Decide what the processor should do and write the experiment whose hypothesis is "the processor's amount check rejects the request and the invariant holds".
2. **Apply.** The partition experiment showed that retry-on-timeout doubles latency against a dead link. Add a circuit breaker to `Gateway` (Phase 12 covers the design; a count of consecutive timeouts per merchant that opens after three is enough) and re-run the GameDay. Which verdicts change, and does the recovery run still reach steady state after the fault clears?
3. **Extend.** Write a `BudgetedAbort` that uses the error-budget arithmetic from Phase 9, Lesson 01: the experiment aborts when it has consumed more than 5% of the monthly error budget at the current burn rate, rather than at a fixed good-rate threshold. Run the 70% error experiment under both abort rules and explain, in the test's comments, which one you would use in production and why.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Steady state | "normal" | A set of SLI thresholds, measured before any fault is armed, that defines what unchanged behaviour looks like; without it there is no experiment |
| Hypothesis | "we think it'll be fine" | A falsifiable statement naming the mechanism and the measurable result, written before the fault is injected |
| Blast radius | "how much we break" | The population the injection can reach: a percentage of requests, a set of tenants, or everyone; a property of the injection, not of the fault |
| Abort condition | "the kill switch" | A threshold evaluated at checkpoints during the experiment that clears every fault and stops the run; looser than the steady state, independent of the thing being broken |
| Fault injection | "chaos" | Deliberately introducing a specific failure (latency, error, partition, skew) into a system under observation |
| GameDay | "chaos day" | A scheduled, staffed session that runs prepared experiments as real incidents to test software, runbooks, and people together |
| Clock skew | "the clocks are a bit off" | A fixed offset between two machines' clocks; breaks every mechanism that compares a remote timestamp with a local one, including signing windows and token expiry |

## Further Reading

- [Principles of Chaos Engineering](https://principlesofchaos.org/) — the one-page statement of the discipline: steady state, hypothesis, real-world events, production, automation, minimal blast radius.
- [Basiri et al., "Chaos Engineering", IEEE Software, 2016](https://ieeexplore.ieee.org/document/7436642) — the Netflix paper that names the five principles and explains why injecting failures into production is safer than not doing so.
- [Chaos Mesh documentation: TimeChaos](https://chaos-mesh.org/docs/simulate-time-chaos-on-kubernetes/) — a production implementation of the clock-skew fault, with the selector fields that are its blast radius.
- [AWS Fault Injection Service: stop conditions](https://docs.aws.amazon.com/fis/latest/userguide/stop-conditions.html) — the abort condition as an alarm-backed first-class object.
- [Corbett et al., "Spanner: Google's Globally-Distributed Database", OSDI 2012](https://research.google/pubs/spanner-googles-globally-distributed-database/) — TrueTime, and the argument that a remote timestamp is an interval, not a point.
- [Kingsbury, Jepsen analyses](https://jepsen.io/analyses) — fault injection (the "nemesis") against real databases, with the partitions and clock skews this lesson simulates.
