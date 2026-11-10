# Observability for Money Movement

> Metrics that detect a stuck payment before a customer does.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 9 Lesson 01 (SLIs, SLOs, and Error Budgets), Phase 9 Lesson 06 (Metrics and Histograms), Phase 12 Lesson 02 (The Payment State Machine), Phase 14 Lesson 04 (Reconciliation at Scale), Phase 14 Lesson 05 (Batch vs Real-Time Settlement Pipelines)
**Time:** ~90 minutes

## Learning Objectives

- Explain why request-level metrics (latency, 5xx rate) miss the most expensive payment failures, and name the four money-movement signals that catch them
- Build a stuck-payment detector that measures each payment's age in its current state against a per-state limit, using an injected clock
- Compute in-flight exposure by state, authorization rate by processor over sliding windows, and reconciliation break rate, all in exact integer arithmetic
- Implement multiwindow, multi-burn-rate SLO alerts and explain why each rule needs both a long and a short window and full coverage of the long one
- Separate issuer declines from processor errors in an availability SLI, and show how mixing them hides a processor outage or pages on a bank holiday

## The Problem

At 12:00 a payment processor's capture webhook stops reaching a marketplace's platform for three payments: a network change on the processor side drops the callbacks, the captures succeed at the processor, and the platform's state machine sits at `CAPTURE_PENDING`. Every dashboard is green. The API's p99 latency is 180 ms, the 5xx rate is 0.02%, the Kafka consumer lag is zero, and the database has headroom. The three payments, worth 1,050.00 USD, do not appear in the next settlement batch because the batch only settles `CAPTURED` payments. The merchant notices two days later when its payout is short and opens a support ticket. Engineering finds the cause on day three.

At 13:00 the same day, one of the platform's two processors starts timing out a quarter of its authorization requests. The platform's "payment success rate" dashboard drops from 91% to 81%, which the on-call engineer reads as normal afternoon variation: the success rate has always been noisy because it counts issuer declines, and declines move with the time of day, the merchant mix, and whatever the issuers' fraud models are doing. A quarter of the timeouts were actually approved at the processor; the platform marked them failed and the customers retried, so some of them were charged twice. Reconciliation finds those the next morning.

Nothing in either story is a request failing. Requests succeed, quickly, and return wrong or incomplete answers about money. The signals that catch them are about **payments**, not requests: how long each payment has been in its current state, how much money is sitting in non-final states, what fraction of attempts each processor approves compared with the fleet, how fast the availability error budget is burning, and how many records reconciliation could not match. This lesson builds a monitor that computes all of them from the event stream, with time injected so that every alert can be replayed in a test, and runs it over a seeded eight-hour day in which both failures happen. The stuck captures page 16 minutes after the webhook was lost; the processor outage pages in 3.

## The Concept

### Four signals that are about money

| Signal | Question it answers | Why request metrics miss it |
|--------|--------------------|-----------------------------|
| Stuck payments by state age | Is any payment older than it should be in its current state? | A stuck payment generates no requests at all; silence looks healthy |
| In-flight exposure | How much money is committed but not final, and where? | Exposure is a sum of amounts, not a count of calls |
| Auth rate by processor | Is one processor approving less than the fleet? | Declines are 200 OK responses; a processor that wrongly declines is "up" |
| Reconciliation break rate | What fraction of records could not be matched with the other side? | Breaks are discovered hours later by a batch job, not on the request path |

They share a design rule: **the unit of observation is the payment, and the dimension that matters is time in state.** A payment state machine (Phase 12, Lesson 02) moves through `CREATED`, `AUTHORIZED`, `CAPTURE_PENDING`, `CAPTURED`, and ends in `SETTLED` or `FAILED`. Each non-terminal state has an expected dwell time, and a payment that exceeds it is either stuck or about to cost money.

### Stuck by state age

Each state gets a limit, chosen from the business process rather than from the latency distribution:

| State | Limit | What exceeding it means |
|-------|-------|-------------------------|
| `CREATED` | 30 min | Authorization response lost, or customer abandoned; either way, clean it up |
| `AUTHORIZED` | 6 days | Card authorizations expire around 7 days (shorter for some MCCs); capture or lose the sale |
| `CAPTURE_PENDING` | 15 min | Capture sent, no answer; the processor may have captured and the platform does not know |
| `CAPTURED` | 3 days | Should be in a settlement file by now; if not, the merchant's payout is short |

The detector needs exactly one timestamp per payment: when it **entered its current state**, not when it was created. A payment created yesterday and captured a minute ago is healthy; a payment created a minute ago that has been `CAPTURE_PENDING` for 16 minutes is not. Tracking entered-at per state makes the age a subtraction against the injected clock.

The comparison is strict: age `> limit`. A payment at exactly 15:00 in `CAPTURE_PENDING` is not stuck; at 16:00 it is. Writing the boundary down matters because the test suite checks it, and because two services that disagree on `>` versus `>=` produce alerts that flicker.

Stuck payments are counted, and their amounts are summed. Severity comes from the sum: one 4.00 payment stuck is a ticket; 1,050.00 stuck pages. Count-based paging trains on-call engineers to ignore pages for tiny amounts; amount-based paging ranks by what is at risk.

### In-flight exposure and the age histogram

In-flight exposure is the sum of amounts in each non-terminal state. It answers two questions a count cannot: how much the platform would have to explain if every in-flight payment failed now, and where the money is piling up. A healthy system's exposure is dominated by `CAPTURED` (waiting for the settlement run) and resets after each run; a growing `CAPTURE_PENDING` balance means captures are not completing.

Exposure alone hides shape. The state-age histogram splits each state by age band (`<1m`, `1-5m`, `5-15m`, `15-60m`, `1-24h`, `>24h`). In the simulation at 12:20:

```text
  state            amount            <1m   1-5m  5-15m 15-60m  1-24h   >24h
  CAPTURE_PENDING  12198.94 USD       38      0      0      3      0      0
```

Thirty-eight payments under a minute old are normal flow. Three in the 15-60 minute band are the lost webhooks. A gauge of "CAPTURE_PENDING count = 41" would not show the difference; the histogram makes the tail visible.

### Authorization rate by processor

Authorization rate is approvals divided by attempts over a window. Two choices determine whether it is useful:

1. **Compare processors with the fleet, not with a fixed target.** Approval rates vary by time of day, merchant mix, card mix, and issuer behaviour, so a static threshold either pages constantly or never. The question is whether one processor approves materially less than the others for the same traffic. This lesson pages when a processor's 15-minute rate is at least 500 basis points below the fleet's.
2. **Require a minimum sample.** With 50 attempts, a 5-point drop is two or three declines, which is noise. Rates computed from fewer than 100 attempts are reported as `n/a` and never alert.

Rates are computed in **basis points** with integer division: `10,000 * approvals / attempts`. 9,000 bps is 90.00%. No floating point appears in any comparison, so an alert threshold either fires or does not, identically on every machine and in every replay.

### Availability SLO: declines are good events

An SLO needs a definition of a bad event. For authorization, the tempting definition is "not approved," which counts issuer declines as failures. That is wrong for two reasons. Declines are the issuer doing its job (insufficient funds, suspected fraud) and are outside the platform's control. And their rate moves by several points with no fault anywhere, so a decline-polluted SLI has so much variance that real faults disappear into it, which is what happened at 13:00 in The Problem.

The availability SLI for authorization is: **of attempts that reached a processor, what fraction got a definitive answer (approved or declined)?** Bad events are timeouts, connection failures, and processor 5xx responses. A 99.9% SLO allows 1,000 bad events per million attempts (`budgetPpm = 1000`).

### Burn rate and the multiwindow rule

The **burn rate** is how fast the error budget is being spent relative to the rate that would exactly exhaust it over the SLO period:

```text
burn = (bad / total) / budget
```

A burn rate of 1 spends the 30-day budget in 30 days. A burn rate of 14.4 spends 2% of it in one hour (`14.4 * 1h / 720h = 0.02`). The Google SRE Workbook recommends alerting on burn rate with pairs of windows:

| Severity | Long window | Short window | Burn threshold | Budget consumed when it fires |
|----------|-------------|--------------|----------------|-------------------------------|
| Page | 1 h | 5 min | 14.4 | 2% |
| Page | 6 h | 30 min | 6 | 5% |
| Ticket | 1 d | 2 h | 3 | 10% |
| Ticket | 3 d | 6 h | 1 | 10% |

Both windows must exceed the threshold. The long window gives significance: one bad minute does not page. The short window gives a fast reset: when the incident ends, the 5-minute burn drops to zero within five minutes and the page clears, even though the 1-hour window is still elevated. The test suite proves this with a ten-minute spike that has ended: 1-hour burn 16.66, 5-minute burn 0, no page.

Worked example. In the five minutes to 13:05 the platform made 200 attempts with 6 processor errors; in the hour, 2,400 attempts with 40 errors:

```text
5m burn = (6 / 200) / 0.001     = 0.03 / 0.001  = 30.00
1h burn = (40 / 2400) / 0.001   = 0.01666 / 0.001 = 16.66
both >= 14.40 -> page
```

The code computes `bad * 10^8 / (total * budgetPpm)`, which is the burn rate times 100 as an exact integer: 3000 and 1666. Thresholds are stored the same way (1440 for 14.4).

### Coverage: a half-empty long window is a short window

Burn rate is a ratio of events in the window. If the monitor started 90 minutes ago, the "6-hour" window contains 90 minutes of data, and its burn rate is the 90-minute burn rate. The first version of this lesson's simulation started at noon; the 3-day ticket rule fired one minute into the outage, before the 1-hour page, because its "3-day" window held ninety minutes. The fix is to arm a rule only when the counter has data reaching back the whole long window. After a deploy or a counter reset, the slow rules stay quiet until coverage is complete and the fast rules carry the load. Production systems get the same effect from long metric retention, but a freshly created metric (a new processor, a new region) has the same cold-start problem.

### Reconciliation break rate

Lesson 04 produces breaks; this lesson counts them. Break rate is `breaks / (matched + breaks)` over the last four runs, in basis points, with a ticket at 50 bps (0.5%). It is a lagging signal, hours behind the event, but it is the only one that sees money moving without the platform's knowledge: in the simulation, one in five processor timeouts was actually approved at the processor, and those surface only as breaks. It is the backstop for everything the real-time signals miss.

### The simulation, end to end

```mermaid
sequenceDiagram
  participant Sim as Simulation (seed 42)
  participant Mon as MoneyMovementMonitor
  participant Clk as ManualClock
  loop every simulated minute, 06:00 to 14:00
    Sim->>Mon: authResult(processor, outcome) x40
    Sim->>Mon: transition(payment, state)
    Sim->>Mon: reconRun(matched, breaks) every 30 min
    Sim->>Mon: evaluateAlerts()
    Mon-->>Sim: alerts (first firing recorded)
    Sim->>Clk: advance(1 min)
  end
```

Forty attempts a minute across two processors, 92% approved, 8% issuer declines, 0.05% processor errors. Captures complete two minutes after authorization, an hourly settlement run moves `CAPTURED` to `SETTLED`. At 12:00 three capture webhooks are lost. At 13:00 `proc_b` starts failing 25% of its attempts.

| Alert | First fired | Delay | Why that delay |
|-------|-------------|-------|----------------|
| `stuck-payments` (page) | 12:16 | 16 min | 15-minute `CAPTURE_PENDING` limit, strict comparison |
| `auth-rate-drop:proc_b` (page) | 13:03 | 3 min | 15-minute window needs a few bad minutes to open a 5-point gap |
| `slo-burn-60m` (page) | 13:05 | 5 min | 1-hour burn crosses 14.4 once ~1.44% of the hour's attempts failed |
| `slo-burn-360m` (page) | 13:16 | 16 min | 6-hour burn crosses 6 later because six quiet hours dilute it |
| `recon-break-rate` (ticket) | 13:29 | 29 min | Reconciliation runs every 30 minutes |

Without this monitor the first signal would have been the merchant's support ticket two days later. That is the lesson's hook made concrete.

### Failure modes of money metrics

Money metrics fail in ways request metrics do not. Each of these has shipped to production somewhere:

1. **Retries counted as attempts.** A customer who retries a declined card three times produces three attempts and three declines. If the platform also retries timeouts automatically, one checkout can produce six attempts. Auth rate per attempt and auth rate per checkout are different numbers and both are useful; label which one a dashboard shows. The SLI in this lesson counts attempts, because each attempt is a call the processor either answered or did not.
2. **Unknown outcomes recorded as failures.** A timeout is not a decline. Recording it as `FAILED` makes the auth-rate drop visible, which is good, but it also lets the customer retry and be charged twice, which is the reconciliation break in the simulation. The state machine should record `UNKNOWN` and resolve it with a status query (Lesson 07); the monitor should count `UNKNOWN` age like any other state.
3. **Label cardinality.** A label per merchant, per card BIN, or per payment id turns one counter into millions of series and takes down the metrics backend during the incident you need it for. Use bounded labels (processor, brand, region, outcome) on counters and put per-payment detail in the stuck query and the logs.
4. **Summing currencies.** An exposure gauge that adds JPY and USD minor units is off by a factor of 100 for one of them. Export one series per currency, as `Money` forces in code.
5. **Averages of rates.** The fleet auth rate is total approvals over total attempts, not the mean of each processor's rate. With 1,000 attempts at 90% on one processor and 10 at 50% on another, the mean of rates is 70% and the fleet rate is 89.6%. `authRateBps(null, ...)` sums counts first for this reason.
6. **Silence as success.** A reconciliation job that did not run reports zero breaks. A missing run must be its own alert, and a counter that has stopped increasing must be distinguishable from one at zero.

### Choosing limits and thresholds

| Knob | Starting value | How to tune it |
|------|----------------|----------------|
| `CAPTURE_PENDING` limit | 15 min | p99.9 of capture completion time over 30 days, times 3, rounded up |
| Stuck page amount | 1,000.00 | Below the amount that would justify waking someone if it were lost |
| Auth-drop gap | 500 bps over 15 min | Largest gap seen between processors on a healthy week, plus a margin |
| Minimum sample | 100 attempts | Large enough that one decline moves the rate by at most 1 point |
| Break-rate ticket | 50 bps over 4 runs | The steady-state break rate from Lesson 04, times 5 |

Every value in the table is a business decision written down in code, which is the point: when an alert fires or fails to, the review asks whether the number was right, not whether someone remembered to look.

Revisit the table after every incident review (Lesson 07). An alert that fired late is a threshold to tighten; an alert that paged for nothing is a threshold to loosen or a rule to delete. Keep the history of changes next to the rule, because the next person to tune it needs to know why it is where it is.

### Complexity and cost

Each event is `O(log B)` for `B` minute buckets (a `TreeMap` insert), and each window query is `O(W)` for `W` minutes in the window: 60 for one hour, 4,320 for three days. Stuck detection scans all tracked payments, `O(P)`, which is fine for an in-process monitor of tens of thousands of in-flight payments and wrong for millions. Production systems keep a secondary index (or a database query on `state, entered_at`) so the scan is `O(stuck)`. Memory is `O(P + B * processors)`.

### What this monitor does not do

It does not deduplicate or route alerts; that is the alert manager's job. It does not compute latency percentiles (Phase 9, Lesson 06 does that with histograms). It does not distinguish soft from hard declines; Lesson 08 does. It holds all state in memory, so a restart loses window history and the coverage rule correctly silences the slow alerts until history rebuilds. And it does not fix anything: a stuck `CAPTURE_PENDING` payment needs a status query to the processor, which is Lesson 07's playbook.

## Build It

The code is `code/java/Main.java`. Time comes from a `Clock` interface; nothing reads the wall clock.

### Step 1: Injected time

```java
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
```

Every age, window, and alert is a function of `clock.now()`. The tests advance the clock by exactly 15 and then 16 minutes to pin the stuck boundary, and the simulation advances it one minute per tick so eight hours run in under a second. A monitor that called `Instant.now()` directly could not be tested for "fires at 16 minutes" without waiting 16 minutes.

**Predict:** if the monitor used `Instant.now()` and the test ran on a slow CI machine, which assertions would become flaky?

### Step 2: Tracking state entry

```java
void transition(String paymentId, String processor, Money amount, PaymentState to) {
    if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
    if (to.terminal()) payments.remove(paymentId);
    else payments.put(paymentId, new Tracked(processor, amount, to, clock.now()));
}
```

Each transition replaces the payment's record with a new state and a new `enteredAt`. Terminal states remove it, so the tracked set is exactly the in-flight set and exposure is a sum over it. Amounts are `Money` in one currency; an event in another currency is rejected, because summing USD and EUR into one exposure number is the bug the `Money` type exists to prevent.

**Predict:** a payment goes `CAPTURE_PENDING` at 12:00 and `CAPTURED` at 12:14. At 12:20, how old is it, and is it stuck?

### Step 3: Stuck detection

```java
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
```

The sort puts the oldest first and breaks ties by id, so the alert text names the same payment on every evaluation and in every replay. `stuckExposure()` sums the amounts, and the alert rule pages when that sum reaches the threshold (1,000.00 USD by default) and tickets otherwise.

**Predict:** 400.00 is stuck for 16 minutes (ticket). A second payment of 700.00 enters `CAPTURE_PENDING` and 16 minutes pass. What is the severity now, and which payment does the alert name as oldest?

### Step 4: Minute buckets and windows

```java
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
```

`MinuteCounter` keeps one array of counts per epoch minute, one slot per `AuthOutcome`. A window of `w` minutes sums the `w` buckets ending at the current minute, inclusive. This is the same structure as a Prometheus counter queried with `increase(...[1h])`, minus the interpolation. `covers(now, window)` reports whether the oldest bucket reaches back the whole window; Step 6 uses it.

**Predict:** at 12:00:30 with a 5-minute window, which minute buckets are summed? Does an event at 11:55:59 count?

### Step 5: Auth rate and burn rate

```java
long authRateBps(String processor, Duration window) {
    long[] c = counts(processor, window);
    long total = c[0] + c[1] + c[2];
    return total < minAttemptsForRate ? -1 : Ratio.bps(c[AuthOutcome.APPROVED.ordinal()], total);
}

long burnRateX100(Duration window) {
    long[] c = authAll.sum(clock.now(), window);
    return slo.burnRateX100(c[AuthOutcome.PROCESSOR_ERROR.ordinal()], c[0] + c[1] + c[2]);
}
```

Auth rate counts approvals over all attempts and refuses to answer below the minimum sample. Burn rate counts only `PROCESSOR_ERROR` as bad; `ISSUER_DECLINE` is in the denominator as a good event. `Slo.burnRateX100` is `bad * 10^8 / (total * budgetPpm)` with `multiplyExact`, so the value is exact and overflow is an exception.

**Predict:** a bank holiday doubles issuer declines from 8% to 16% and nothing else changes. What happens to the auth rate, the fleet comparison, and the burn rate?

### Step 6: Alert rules

```java
for (BurnRule r : burnRules) {
    if (!authAll.covers(clock.now(), r.longWindow())) continue; // a half-empty long window is a short window
    long lng = burnRateX100(r.longWindow()), shrt = burnRateX100(r.shortWindow());
    if (lng >= r.thresholdX100() && shrt >= r.thresholdX100()) {
        out.add(new Alert(r.severity(), "slo-burn-" + r.longWindow().toMinutes() + "m", ...));
    }
}
```

Four alert families are evaluated in order: the burn-rate rules from the SRE Workbook table, stuck payments (severity by amount), processor auth-rate drop against the fleet's 15-minute rate (page at 500 bps), and reconciliation break rate over the last four runs (ticket at 50 bps). `evaluateAlerts` returns the currently firing set; it is stateless, which is what an alert manager expects from a rule evaluator.

**Predict:** the monitor restarts at 13:10, in the middle of the outage. Which rules can fire at 13:11, and which stay silent until when?

### Step 7: The dashboard

`dashboard()` renders one screen: the time, total in-flight exposure, a row per non-terminal state with amount and age histogram, auth rate per processor at 5 minutes and 1 hour with attempt counts, burn rates at 5 minutes, 1 hour, and 6 hours, the reconciliation break rate, and the firing alerts. The order is the order an on-call engineer reads it in: how much money, where it is stuck, who is failing, how fast the budget is going, what is paging.

### Step 8: The simulation

```java
double errorP = degraded ? 0.25 : 0.0005;
AuthOutcome outcome = u < errorP ? AuthOutcome.PROCESSOR_ERROR : u < errorP + 0.08 ? AuthOutcome.ISSUER_DECLINE : AuthOutcome.APPROVED;
...
case PROCESSOR_ERROR -> {
    monitor.transition(id, processor, amount, PaymentState.FAILED);
    if (rnd.nextInt(5) == 0) reconBreaks++;
}
```

`Simulation` drives the monitor with a seeded `Random`, schedules captures one and two minutes after approval, runs an hourly settlement, injects the lost webhooks and the outage, and records the first minute each alert name fires. One in five processor errors becomes a reconciliation break, modelling a timeout that the processor actually approved.

### Step 9: Run it

```bash
cd phases/14-payments-at-scale/06-observability-for-money-movement/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Burn rate by hand: 99.9% SLO, budget 1,000 ppm ==
5m: 6 errors / 200 attempts   -> burn 30.00
1h: 40 errors / 2,400 attempts -> burn 16.66

== Eight hours of traffic, seed 42 ==
lost capture webhooks injected at minute 360; proc_b outage starts at minute 420
  first fired stuck-payments         minute 376 (12:16)
  first fired auth-rate-drop:proc_b  minute 423 (13:03)
  first fired slo-burn-60m           minute 425 (13:05)
  first fired slo-burn-360m          minute 436 (13:16)
  first fired recon-break-rate       minute 449 (13:29)

-- dashboard at minute 430 --
MONEY MOVEMENT  2026-03-31T13:10:00Z
in flight 101752.68 USD
  CAPTURE_PENDING  10065.45 USD       34      0      0      0      3      0
auth rate        5m         1h         attempts 1h
  proc_a         90.00%     93.08%     1200
  proc_b         69.00%     87.16%     1200
slo burn         5m 115.00  1h 25.00  6h 4.72
ALERT PAGE slo-burn-60m: auth availability 99.9% burning 25.00 over 60m and 115.00 over 5m (threshold 14.40)
ALERT PAGE stuck-payments: 3 stuck, 1050.00 USD; oldest pay_lost_30000 in CAPTURE_PENDING for 70m
ALERT PAGE auth-rate-drop:proc_b: proc_b approves 74.00% vs fleet 83.50% over 15m
```

`MainTest` runs 39 assertions: exact burn-rate and basis-point arithmetic, the stuck boundary at 15 versus 16 minutes, severity by amount, age reset on transition, exposure by state, histogram bands, windowed auth rates and the minimum sample, declines excluded from the SLI, the ended-spike case where the short window keeps the page quiet, the coverage rule, reconciliation break rate, and the seeded simulation's alert order and determinism.

### In Python

The Python port (`code/python/main.py`, tests in `test_main.py`) keeps `ManualClock`, `MinuteCounter`, `Ratio`, `Slo`, `BurnRule`, `MoneyMovementMonitor`, and `Simulation`, and prints the same three dashboards. What differs: **Time is `datetime` with `timezone.utc` and `timedelta`.** The clock hands out aware datetimes; `MinuteCounter.minute` buckets by `int(at.timestamp()) // 60`, which is `Math.floorDiv(epochSecond, 60)`; `minutes_of` reproduces `Duration.toMinutes` (truncation toward zero), and `iso_z` prints the `2026-03-31T13:10:00Z` form `Instant.toString` uses. A naive `datetime` compared with the clock raises `TypeError`, which is the right failure. **Dicts replace `TreeMap` and `EnumMap`:** the counter keeps a plain dict of minute buckets and filters by range (a few hundred buckets, so a scan beats a sorted structure); `processors()` returns `sorted(...)` so alerts and the dashboard list processors in the same order as Java; the exposure and histogram maps iterate `PaymentState` in declaration order, which is what `EnumMap` did. **Integer arithmetic is exact and unbounded:** `burn_rate_x100` and `Ratio.bps` use `//`; the inputs are non-negative, so floor division equals Java's truncation, and there is no `multiplyExact` to port because Python integers do not overflow. **The simulation's randomness is `java.util.Random`, reimplemented:** `Simulation` draws from a `JavaRandom` with the JDK's 48-bit LCG (`next_int` with its rejection loop, `next_double` from 53 bits), so seed 42 produces the same amounts, outcomes, first-fired minutes, and dashboards as the Java demo; the scheduled callbacks are closures created inside `_attempt`, one scope per call, so they capture the right payment id without the `final` dance Java needs. The Python tests port every Java check (54 assertions).

Run it from `code/python/` with `python3 main.py`; test with `python3 -m unittest -v`.

#### Step 1: Injected time

`ManualClock.advance` adds a `timedelta` and refuses a negative one with `ValueError`; every age, window, and alert below reads `clock.now()`.

```python
class Clock:
    def now(self) -> datetime:
        raise NotImplementedError


class ManualClock(Clock):
    def __init__(self, start: datetime) -> None:
        self._now = start

    def now(self) -> datetime:
        return self._now

    def advance(self, d: timedelta) -> None:
        if d < timedelta(0):
            raise ValueError("time does not go backwards")
        self._now = self._now + d
```

#### Step 2: Tracking state entry

`transition` rejects a foreign currency, pops the payment on a terminal state, and otherwise stores a fresh `Tracked` record with the current time, so the tracked dict is exactly the in-flight set.

```python
    def transition(self, payment_id: str, processor: str, amount: Money, to: PaymentState) -> None:
        """Record that a payment entered a state now. Terminal states stop tracking the payment."""
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        if to.terminal():
            self.payments.pop(payment_id, None)
        else:
            self.payments[payment_id] = Tracked(processor, amount, to, self.clock.now())
```

#### Step 3: Stuck detection

`stuck` computes each age as a `timedelta`, compares with the state's limit, and sorts by `(-age.total_seconds(), payment_id)` so the oldest comes first with a deterministic tie-break. `stuck_exposure` sums the amounts.

```python
    def stuck(self) -> list[StuckPayment]:
        """Payments whose time in their current state exceeds that state's limit, oldest first."""
        out = []
        for payment_id, t in self.payments.items():
            age = self.clock.now() - t.entered_at
            limit = self.stuck_after.get(t.state)
            if limit is not None and age > limit:
                out.append(StuckPayment(payment_id, t.processor, t.state, age, t.amount))
        out.sort(key=lambda s: (-s.age.total_seconds(), s.payment_id))
        return out

    def stuck_exposure(self) -> Money:
        total = Money.zero(self.currency)
        for s in self.stuck():
            total = total.plus(s.amount)
        return total
```

#### Step 4: Minute buckets and windows

`minute` is `int(timestamp) // 60`; `sum` scans the dict and keeps buckets in `[now - w + 1, now]`; `covers` checks that the oldest bucket reaches back the whole window.

```python
class MinuteCounter:
    """Counts per minute bucket, keyed by epoch minute. A window query sums the buckets whose minute is in
    (now - window, now]. Old buckets are dropped once they fall outside the longest window we query."""

    def __init__(self, width: int) -> None:
        self.buckets: dict[int, list[int]] = {}
        self.width = width

    @staticmethod
    def minute(at: datetime) -> int:
        return int(at.timestamp()) // 60  # Math.floorDiv(epochSecond, 60)

    def add(self, at: datetime, slot: int, n: int) -> None:
        counts = self.buckets.get(self.minute(at))
        if counts is None:
            counts = self.buckets[self.minute(at)] = [0] * self.width
        counts[slot] += n

    def sum(self, now: datetime, window: timedelta) -> list[int]:
        now_minute = self.minute(now)
        start = now_minute - minutes_of(window) + 1
        total = [0] * self.width
        for minute, counts in self.buckets.items():
            if start <= minute <= now_minute:
                for i in range(self.width):
                    total[i] += counts[i]
        return total

    def covers(self, now: datetime, window: timedelta) -> bool:
        """True when the counter has data reaching back at least the whole window."""
        if not self.buckets:
            return False
        return min(self.buckets) <= self.minute(now) - minutes_of(window) + 1
```

#### Step 5: Auth rate and burn rate

`auth_rate_bps` returns -1 below the minimum sample; `burn_rate_x100` feeds only `PROCESSOR_ERROR` as bad into `Slo.burn_rate_x100`, which is exact integer arithmetic with `//`.

```python
class Slo:
    """Burn rate for an SLO with error budget given in parts per million (99.9% SLO = 1,000 ppm budget).
    Returned x100 so that 14.4 is 1440 and every comparison is an exact integer comparison."""

    name: str
    budget_ppm: int

    def burn_rate_x100(self, bad: int, total: int) -> int:
        if total == 0:
            return 0
        # (bad / total) / (budgetPpm / 1e6) * 100 = bad * 1e8 / (total * budgetPpm)
        return bad * 100_000_000 // (total * self.budget_ppm)

    def auth_rate_bps(self, processor: Optional[str], window: timedelta) -> int:
        """Approvals over all attempts, in basis points; -1 if fewer than min_attempts_for_rate attempts."""
        c = self._counts(processor, window)
        total = c[0] + c[1] + c[2]
        return -1 if total < self.min_attempts_for_rate else Ratio.bps(c[AuthOutcome.APPROVED.value], total)

    def attempts(self, processor: Optional[str], window: timedelta) -> int:
        c = self._counts(processor, window)
        return c[0] + c[1] + c[2]

    def _counts(self, processor: Optional[str], window: timedelta) -> list[int]:
        if processor is None:
            return self.auth_all.sum(self.clock.now(), window)
        mc = self.auth_by_processor.get(processor)
        return [0, 0, 0] if mc is None else mc.sum(self.clock.now(), window)

    def burn_rate_x100(self, window: timedelta) -> int:
        """Availability SLI burn rate: processor errors are bad, issuer declines are good events."""
        c = self.auth_all.sum(self.clock.now(), window)
        return self.slo.burn_rate_x100(c[AuthOutcome.PROCESSOR_ERROR.value], c[0] + c[1] + c[2])
```

#### Step 6: Alert rules

`evaluate_alerts` is stateless: burn rules gated by `covers`, stuck payments with severity by exposure, per-processor auth-rate drop against the fleet, and the reconciliation break rate, in that order.

```python
    def evaluate_alerts(self) -> list[Alert]:
        out: list[Alert] = []
        for r in self.burn_rules:
            if not self.auth_all.covers(self.clock.now(), r.long_window):
                continue  # a half-empty long window is a short window
            lng, shrt = self.burn_rate_x100(r.long_window), self.burn_rate_x100(r.short_window)
            if lng >= r.threshold_x100 and shrt >= r.threshold_x100:
                out.append(Alert(r.severity, f"slo-burn-{minutes_of(r.long_window)}m",
                                 f"{self.slo.name} burning {x100(lng)} over {minutes_of(r.long_window)}m and {x100(shrt)} over "
                                 f"{minutes_of(r.short_window)}m (threshold {x100(r.threshold_x100)})"))
        stuck = self.stuck()
        if stuck:
            exposure = self.stuck_exposure()
            sev = Severity.PAGE if exposure >= self.stuck_page_threshold else Severity.TICKET
            oldest = stuck[0]
            out.append(Alert(sev, "stuck-payments", f"{len(stuck)} stuck, {exposure}; oldest {oldest.payment_id} in {oldest.state} "
                                                    f"for {minutes_of(oldest.age)}m"))
        fleet = self.auth_rate_bps(None, timedelta(minutes=15))
        for p in self.processors():
            rate = self.auth_rate_bps(p, timedelta(minutes=15))
            if rate >= 0 and fleet >= 0 and fleet - rate >= self.auth_drop_bps:
                out.append(Alert(Severity.PAGE, f"auth-rate-drop:{p}", f"{p} approves {Ratio.pct(rate)} vs fleet {Ratio.pct(fleet)} over 15m"))
        recon = self.recon_break_rate_bps(4)
        if recon >= self.recon_break_ticket_bps:
            out.append(Alert(Severity.TICKET, "recon-break-rate", f"break rate {Ratio.pct(recon)} over last 4 runs"))
        return out
```

#### Step 7: The dashboard

`dashboard` renders the same screen with f-string alignment: exposure and age histogram per non-terminal state, auth rates per processor, burn rates, break rate, and the firing alerts.

```python
    def dashboard(self) -> str:
        lines = [f"MONEY MOVEMENT  {iso_z(self.clock.now())}\n", f"in flight {self.total_in_flight()}\n"]
        hist = self.state_age_histogram()
        lines.append(f"  {'state':<16} {'amount':<14} {'<1m':>6} {'1-5m':>6} {'5-15m':>6} {'15-60m':>6} {'1-24h':>6} {'>24h':>6}\n")
        for state, amount in self.in_flight_exposure().items():
            h = hist[state]
            lines.append(f"  {state.name:<16} {str(amount):<14} {h[0]:>6} {h[1]:>6} {h[2]:>6} {h[3]:>6} {h[4]:>6} {h[5]:>6}\n")
        lines.append(f"auth rate        {'5m':<10} {'1h':<10} {'attempts 1h':<10}\n")
        for p in self.processors():
            lines.append(f"  {p:<14} {Ratio.pct(self.auth_rate_bps(p, timedelta(minutes=5))):<10} "
                         f"{Ratio.pct(self.auth_rate_bps(p, timedelta(hours=1))):<10} {self.attempts(p, timedelta(hours=1))}\n")
        lines.append(f"slo burn         5m {x100(self.burn_rate_x100(timedelta(minutes=5)))}  1h {x100(self.burn_rate_x100(timedelta(hours=1)))}"
                     f"  6h {x100(self.burn_rate_x100(timedelta(hours=6)))}\n")
        lines.append(f"recon breaks     {Ratio.pct(self.recon_break_rate_bps(4))} (last 4 runs)\n")
        alerts = self.evaluate_alerts()
        if not alerts:
            lines.append("alerts           none\n")
        for a in alerts:
            lines.append(f"ALERT {a.severity} {a.name}: {a.detail}\n")
        return "".join(lines)
```

#### Step 8: The simulation

`_attempt` draws the amount and outcome from `JavaRandom`, transitions the payment, schedules the capture steps as closures (one scope per call), and turns one in five processor errors into a reconciliation break.

```python
    def _attempt(self, minute: int, processor: str) -> None:
        payment_id = f"pay_{self._seq:06d}"
        self._seq += 1
        amount = Money.of_minor(500 + self.rnd.next_int(49_500), USD)
        u = self.rnd.next_double()
        degraded = processor == "proc_b" and minute >= self.OUTAGE_MINUTE
        error_p = 0.25 if degraded else 0.0005
        outcome = (AuthOutcome.PROCESSOR_ERROR if u < error_p
                   else AuthOutcome.ISSUER_DECLINE if u < error_p + 0.08
                   else AuthOutcome.APPROVED)
        monitor = self.monitor
        monitor.transition(payment_id, processor, amount, PaymentState.CREATED)
        monitor.auth_result(processor, outcome)
        if outcome is AuthOutcome.APPROVED:
            monitor.transition(payment_id, processor, amount, PaymentState.AUTHORIZED)
            self._at(minute + 1, lambda: monitor.transition(payment_id, processor, amount, PaymentState.CAPTURE_PENDING))

            def capture() -> None:
                monitor.transition(payment_id, processor, amount, PaymentState.CAPTURED)
                self._captured[payment_id] = amount
                self._recon_matched += 1

            self._at(minute + 2, capture)
        elif outcome is AuthOutcome.ISSUER_DECLINE:
            monitor.transition(payment_id, processor, amount, PaymentState.FAILED)
        else:
            monitor.transition(payment_id, processor, amount, PaymentState.FAILED)
            # A timeout is not a decline: one in five was actually approved at the processor,
            # which reconciliation later reports as a capture we do not have.
            if self.rnd.next_int(5) == 0:
                self._recon_breaks += 1
```

#### Step 9: Run it

```bash
cd phases/14-payments-at-scale/06-observability-for-money-movement/code/python
python3 main.py
python3 -m unittest -v
```

The output is identical to the Java run:

```console
== Eight hours of traffic, seed 42 ==
lost capture webhooks injected at minute 360; proc_b outage starts at minute 420
  first fired stuck-payments         minute 376 (12:16)
  first fired auth-rate-drop:proc_b  minute 423 (13:03)
  first fired slo-burn-60m           minute 425 (13:05)
  first fired slo-burn-360m          minute 436 (13:16)
  first fired recon-break-rate       minute 449 (13:29)
```

### In Go

The Go port (`code/go/main.go`, tests in `main_test.go`) keeps `Clock`/`ManualClock`, `MinuteCounter`, `Ratio`, `Slo`, `BurnRule`, `MoneyMovementMonitor`, and `Simulation`; `javarandom.go` carries the JDK generator so seed 42 prints the same first-fired minutes and the same three dashboards. What differs: **Time is `time.Time` and `time.Duration`.** The clock hands out UTC times; `epochMinute` reproduces `Math.floorDiv(epochSecond, 60)` with an explicit floor, `minutesOf` is `Duration.toMinutes`, and the dashboard header prints `Format(time.RFC3339)`, which is the `2026-03-31T13:10:00Z` form `Instant.toString` uses; `Advance` returns `ErrTimeBackwards` instead of throwing. **Maps replace `TreeMap` and `EnumMap`:** the counter keeps a plain map of minute buckets and filters by range; `Processors()` sorts its keys so alerts and the dashboard list processors in Java's `TreeMap` order; `nonTerminalStates` fixes the `EnumMap` iteration order for exposure and the histogram; `Simulation.FiredOrder` records the `LinkedHashMap` insertion order next to the `FirstFired` map. **Overflow is checked, not thrown:** `Money.Plus` and `Ratio.Bps` go through `addChecked`/`mulChecked`, which return `ErrOverflow` where Java's `addExact`/`multiplyExact` throw; a `need` helper panics on the error, since no sum of minor units in this lesson can overflow; `Transition` returns a wrapped `ErrCurrencyMismatch` and the test checks it with `errors.Is`; `AgeOf` returns `(time.Duration, bool)` where Java returned `null` for an untracked payment. **Single-goroutine by design:** nothing in the monitor is shared across goroutines; if ingestion and evaluation ran concurrently, a `sync.Mutex` around the monitor would be the first change, and `go test -race` would catch the omission. The Go tests port all 42 Java checks (53 assertions).

Run it from `code/go/` with `go run .`; test with `go test ./...`.

#### Step 1: Injected time

`Clock` is a one-method interface; `ManualClock.Advance` returns `ErrTimeBackwards` for a negative duration.

```go
type Clock interface{ Now() time.Time }

type ManualClock struct{ now time.Time }

func NewManualClock(start time.Time) *ManualClock { return &ManualClock{start} }

func (c *ManualClock) Now() time.Time { return c.now }

// ErrTimeBackwards is returned when a test tries to advance the clock by a negative duration.
var ErrTimeBackwards = errors.New("time does not go backwards")

func (c *ManualClock) Advance(d time.Duration) error {
	if d < 0 {
		return ErrTimeBackwards
	}
	c.now = c.now.Add(d)
	return nil
}
```

#### Step 2: Tracking state entry

`Transition` returns a wrapped `ErrCurrencyMismatch`, deletes on a terminal state, and otherwise stores a `tracked` value with `clock.Now()`.

```go
func (m *MoneyMovementMonitor) Transition(paymentId, processor string, amount Money, to PaymentState) error {
	if amount.Currency != m.currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.currency.Code, amount.Currency.Code)
	}
	if to.Terminal() {
		delete(m.payments, paymentId)
	} else {
		m.payments[paymentId] = tracked{processor, amount, to, m.clock.Now()}
	}
	return nil
}
```

#### Step 3: Stuck detection

`Stuck` iterates the (unordered) map, so the `sort.Slice` by age descending then id is what makes the result deterministic; `StuckExposure` sums with `need(...Plus)`.

```go
func (m *MoneyMovementMonitor) Stuck() []StuckPayment {
	out := []StuckPayment{}
	for id, t := range m.payments {
		age := m.clock.Now().Sub(t.enteredAt)
		if limit, ok := m.stuckAfter[t.state]; ok && age > limit {
			out = append(out, StuckPayment{id, t.processor, t.state, age, t.amount})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Age != out[j].Age {
			return out[i].Age > out[j].Age
		}
		return out[i].PaymentId < out[j].PaymentId
	})
	return out
}

func (m *MoneyMovementMonitor) StuckExposure() Money {
	total := Zero(m.currency)
	for _, s := range m.Stuck() {
		total = need(total.Plus(s.Amount))
	}
	return total
}
```

#### Step 4: Minute buckets and windows

`epochMinute` floors with a double-modulo so negative seconds would round the right way; `Sum` filters the map by range; `Covers` finds the oldest bucket by scanning, since a Go map has no `firstKey`.

```go
func minutesOf(d time.Duration) int64 { return int64(d / time.Minute) }

func epochMinute(at time.Time) int64 {
	s := at.Unix()
	return (s - ((s%60)+60)%60) / 60 // Math.floorDiv(epochSecond, 60)
}

// MinuteCounter counts per minute bucket, keyed by epoch minute. A window query sums the buckets whose minute is
// in (now - window, now]. Old buckets are dropped once they fall outside the longest window we query.
type MinuteCounter struct {
	buckets map[int64][]int64
	width   int
}

func NewMinuteCounter(width int) *MinuteCounter { return &MinuteCounter{map[int64][]int64{}, width} }

func (c *MinuteCounter) Add(at time.Time, slot int, n int64) {
	minute := epochMinute(at)
	counts, ok := c.buckets[minute]
	if !ok {
		counts = make([]int64, c.width)
		c.buckets[minute] = counts
	}
	counts[slot] += n
}

func (c *MinuteCounter) Sum(now time.Time, window time.Duration) []int64 {
	nowMinute := epochMinute(now)
	from := nowMinute - minutesOf(window) + 1
	total := make([]int64, c.width)
	for minute, counts := range c.buckets {
		if minute >= from && minute <= nowMinute {
			for i := 0; i < c.width; i++ {
				total[i] += counts[i]
			}
		}
	}
	return total
}

// Covers is true when the counter has data reaching back at least the whole window.
func (c *MinuteCounter) Covers(now time.Time, window time.Duration) bool {
	if len(c.buckets) == 0 {
		return false
	}
	first := int64(0)
	started := false
	for minute := range c.buckets {
		if !started || minute < first {
			first, started = minute, true
		}
	}
	return first <= epochMinute(now)-minutesOf(window)+1
}
```

#### Step 5: Auth rate and burn rate

`AuthRateBps` uses `""` for "all processors" where Java used `null`; `Slo.BurnRateX100` does the exact arithmetic through `mulChecked`.

```go
type Slo struct {
	Name      string
	BudgetPpm int64
}

func (s Slo) BurnRateX100(bad, total int64) int64 {
	if total == 0 {
		return 0
	}
	// (bad / total) / (budgetPpm / 1e6) * 100 = bad * 1e8 / (total * budgetPpm)
	return need(mulChecked(bad, 100_000_000)) / need(mulChecked(total, s.BudgetPpm))
}

func (m *MoneyMovementMonitor) AuthRateBps(processor string, window time.Duration) int64 {
	c := m.counts(processor, window)
	total := c[0] + c[1] + c[2]
	if total < m.minAttemptsForRate {
		return -1
	}
	return Ratio.Bps(c[Approved], total)
}

func (m *MoneyMovementMonitor) Attempts(processor string, window time.Duration) int64 {
	c := m.counts(processor, window)
	return c[0] + c[1] + c[2]
}

func (m *MoneyMovementMonitor) counts(processor string, window time.Duration) []int64 {
	if processor == "" {
		return m.authAll.Sum(m.clock.Now(), window)
	}
	mc, ok := m.authByProcessor[processor]
	if !ok {
		return make([]int64, authOutcomes)
	}
	return mc.Sum(m.clock.Now(), window)
}

// BurnRateX100 is the availability SLI burn rate: processor errors are bad, issuer declines are good events.
func (m *MoneyMovementMonitor) BurnRateX100(window time.Duration) int64 {
	c := m.authAll.Sum(m.clock.Now(), window)
	return m.slo.BurnRateX100(c[ProcessorError], c[0]+c[1]+c[2])
}
```

#### Step 6: Alert rules

`EvaluateAlerts` evaluates the four families in the same order; `Processors()` is sorted so the per-processor alerts appear in `TreeMap` order.

```go
func (m *MoneyMovementMonitor) EvaluateAlerts() []Alert {
	out := []Alert{}
	for _, r := range m.burnRules {
		if !m.authAll.Covers(m.clock.Now(), r.LongWindow) {
			continue // a half-empty long window is a short window
		}
		lng, shrt := m.BurnRateX100(r.LongWindow), m.BurnRateX100(r.ShortWindow)
		if lng >= r.ThresholdX100 && shrt >= r.ThresholdX100 {
			out = append(out, Alert{r.Severity, fmt.Sprintf("slo-burn-%dm", minutesOf(r.LongWindow)),
				fmt.Sprintf("%s burning %s over %dm and %s over %dm (threshold %s)", m.slo.Name, X100(lng),
					minutesOf(r.LongWindow), X100(shrt), minutesOf(r.ShortWindow), X100(r.ThresholdX100))})
		}
	}
	if stuck := m.Stuck(); len(stuck) > 0 {
		exposure := m.StuckExposure()
		sev := Ticket
		if exposure.Compare(m.stuckPageThreshold) >= 0 {
			sev = Page
		}
		oldest := stuck[0]
		out = append(out, Alert{sev, "stuck-payments", fmt.Sprintf("%d stuck, %v; oldest %s in %v for %dm",
			len(stuck), exposure, oldest.PaymentId, oldest.State, minutesOf(oldest.Age))})
	}
	fleet := m.AuthRateBps("", 15*time.Minute)
	for _, p := range m.Processors() {
		rate := m.AuthRateBps(p, 15*time.Minute)
		if rate >= 0 && fleet >= 0 && fleet-rate >= m.authDropBps {
			out = append(out, Alert{Page, "auth-rate-drop:" + p,
				fmt.Sprintf("%s approves %s vs fleet %s over 15m", p, Ratio.Pct(rate), Ratio.Pct(fleet))})
		}
	}
	if recon := m.ReconBreakRateBps(4); recon >= m.reconBreakTicketBps {
		out = append(out, Alert{Ticket, "recon-break-rate", fmt.Sprintf("break rate %s over last 4 runs", Ratio.Pct(recon))})
	}
	return out
}
```

#### Step 7: The dashboard

`Dashboard` writes into a `strings.Builder` with `%-16s`/`%6d` widths matching the Java format, iterating `nonTerminalStates` for the `EnumMap` order.

```go
func (m *MoneyMovementMonitor) Dashboard() string {
	var sb strings.Builder
	sb.WriteString("MONEY MOVEMENT  " + m.clock.Now().UTC().Format(time.RFC3339) + "\n")
	fmt.Fprintf(&sb, "in flight %v\n", m.TotalInFlight())
	hist := m.StateAgeHistogram()
	exposure := m.InFlightExposure()
	fmt.Fprintf(&sb, "  %-16s %-14s %6s %6s %6s %6s %6s %6s\n", "state", "amount", "<1m", "1-5m", "5-15m", "15-60m", "1-24h", ">24h")
	for _, s := range nonTerminalStates {
		h := hist[s]
		fmt.Fprintf(&sb, "  %-16s %-14s %6d %6d %6d %6d %6d %6d\n", s, exposure[s], h[0], h[1], h[2], h[3], h[4], h[5])
	}
	fmt.Fprintf(&sb, "auth rate        %-10s %-10s %-10s\n", "5m", "1h", "attempts 1h")
	for _, p := range m.Processors() {
		fmt.Fprintf(&sb, "  %-14s %-10s %-10s %d\n", p, Ratio.Pct(m.AuthRateBps(p, 5*time.Minute)),
			Ratio.Pct(m.AuthRateBps(p, time.Hour)), m.Attempts(p, time.Hour))
	}
	fmt.Fprintf(&sb, "slo burn         5m %s  1h %s  6h %s\n", X100(m.BurnRateX100(5*time.Minute)),
		X100(m.BurnRateX100(time.Hour)), X100(m.BurnRateX100(6*time.Hour)))
	sb.WriteString("recon breaks     " + Ratio.Pct(m.ReconBreakRateBps(4)) + " (last 4 runs)\n")
	alerts := m.EvaluateAlerts()
	if len(alerts) == 0 {
		sb.WriteString("alerts           none\n")
	}
	for _, a := range alerts {
		fmt.Fprintf(&sb, "ALERT %v %s: %s\n", a.Severity, a.Name, a.Detail)
	}
	return sb.String()
}
```

#### Step 8: The simulation

`attempt` draws from `JavaRandom`, wraps `Transition` in `need(0, ...)` because a currency mismatch here would be a bug, and schedules the capture closures; Go closures capture `id` per call, so no re-binding is needed.

```go
func (s *Simulation) attempt(minute int, processor string) {
	id := fmt.Sprintf("pay_%06d", s.seq)
	s.seq++
	amount := OfMinor(500+int64(s.rnd.NextIntBound(49_500)), USD)
	u := s.rnd.NextDouble()
	degraded := processor == "proc_b" && minute >= OutageMinute
	errorP := 0.0005
	if degraded {
		errorP = 0.25
	}
	outcome := Approved
	switch {
	case u < errorP:
		outcome = ProcessorError
	case u < errorP+0.08:
		outcome = IssuerDecline
	}
	need(0, s.Monitor.Transition(id, processor, amount, Created))
	s.Monitor.AuthResult(processor, outcome)
	switch outcome {
	case Approved:
		need(0, s.Monitor.Transition(id, processor, amount, Authorized))
		s.at(minute+1, func() { need(0, s.Monitor.Transition(id, processor, amount, CapturePending)) })
		s.at(minute+2, func() {
			need(0, s.Monitor.Transition(id, processor, amount, Captured))
			if _, dup := s.captured[id]; !dup {
				s.capturedIds = append(s.capturedIds, id)
			}
			s.captured[id] = amount
			s.reconMatched++
		})
	case IssuerDecline:
		need(0, s.Monitor.Transition(id, processor, amount, Failed))
	case ProcessorError:
		need(0, s.Monitor.Transition(id, processor, amount, Failed))
		// A timeout is not a decline: one in five was actually approved at the processor,
		// which reconciliation later reports as a capture we do not have.
		if s.rnd.NextIntBound(5) == 0 {
			s.reconBreaks++
		}
	}
}
```

#### Step 9: Run it

```bash
cd phases/14-payments-at-scale/06-observability-for-money-movement/code/go
go run .
go test ./...
```

The output is identical to the Java run; the minute-430 dashboard opens:

```console
-- dashboard at minute 430 --
MONEY MOVEMENT  2026-03-31T13:10:00Z
in flight 101752.68 USD
  state            amount            <1m   1-5m  5-15m 15-60m  1-24h   >24h
```

## Use It

**Prometheus and the SRE Workbook rules.** In production the counters are Prometheus counters (`payments_auth_attempts_total{processor, outcome}`), the windows are `increase(...[1h])`, and the burn-rate rules are recording rules plus alerting rules in exactly the two-window form above. What Prometheus adds: durable history (so coverage is rarely a problem), label-based fan-out across processors, regions, and card brands, and Alertmanager for routing, grouping, and silencing. What it does not do: track per-payment state. A counter cannot tell you which payment is stuck.

**Stuck-payment queries.** Per-payment age lives in the payment database: `SELECT state, count(*), sum(amount_minor) FROM payments WHERE state NOT IN ('SETTLED','FAILED') AND state_entered_at < now() - interval '15 minutes' GROUP BY state`, run every minute with an index on `(state, state_entered_at)`. Exported as a gauge per state and age band, it becomes the histogram on the dashboard. Teams that run Temporal or similar workflow engines get the same signal from workflow timers that fire when a step exceeds its deadline.

**Processor dashboards.** Adyen, Stripe, Checkout.com, and Worldpay all expose authorization-rate reporting by card brand, issuer country, and decline code, and status pages for their own incidents. The platform's own by-processor metric is what tells you before their status page does. Datadog and Grafana both ship SLO features that implement multi-burn-rate alerting from the Workbook.

**Distributed tracing.** Phase 9, Lesson 07 traces a request. A payment spans many requests over days; teams that try to trace a payment end to end usually use the payment id as a correlation attribute on every span and log line (Phase 9, Lesson 08) and query by it, rather than one long trace.

## Ship It

`outputs/checklist-money-movement-observability.md` is a review checklist for a payment service's monitoring: the per-state limits, the exposure and histogram gauges, the SLI definition that excludes declines, the burn-rate rule table, coverage after deploys, the processor-versus-fleet rule, and the reconciliation backstop. Run it against your service's dashboards and alert rules before the next on-call rotation.

## Exercises

1. **Reinforce.** Add a `latency` SLI: record each authorization's duration and treat any attempt slower than 2 seconds as a bad event for a second SLO (99% under 2 s). Add the four burn rules for it and show in a test that a slow-but-successful processor pages on latency and not on availability.
2. **Apply.** Make the auth-rate-drop rule dimensional: break the fleet comparison down by card brand (Visa, Mastercard, Amex) so that an outage at one network on one processor is detected even when it is 30% of that processor's traffic. Generate traffic with a brand mix in `Simulation` and inject an outage only for `proc_b` + Amex. Report how many minutes earlier the per-brand rule fires than the per-processor rule.
3. **Extend.** Connect this monitor to Lesson 05's settlement pipeline: emit `CAPTURED -> SETTLED` transitions from the batch run, add a `settlement-batch-late` alert when the batch has not run by 02:00 on T+1, and expose `payouts_in_transit` from the ledger as a gauge. Then inject an ACH file rejection and show which alerts fire, in which order, and which human first learns that merchants will not be paid.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| State age | "how long it's been" | Time since the payment entered its current state, compared with a per-state limit derived from the business process |
| In-flight exposure | "pending volume" | The sum of amounts in non-terminal states, by state: money committed but not yet final |
| Availability SLI | "success rate" | Of attempts that reached a processor, the fraction that got a definitive answer; issuer declines are good events |
| Error budget | "allowed failures" | `1 - SLO` of events over the SLO period; 99.9% allows 1,000 bad events per million |
| Burn rate | "how bad it is" | Observed bad-event ratio divided by the budget ratio; 1 exhausts the budget exactly at the period's end, 14.4 spends 2% per hour |
| Multiwindow alert | "two thresholds" | A rule that fires only when both a long window (significance) and a short window (recency) exceed the same burn threshold |
| Coverage | "enough history" | Whether the counter holds data for the whole long window; without it, a long-window rule behaves like a short one |
| Break rate | "recon failures" | Unmatched records over all records across recent reconciliation runs; the lagging backstop for money that moved without the platform knowing |

## Further Reading

- [Google SRE Workbook, Chapter 5: Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/) — the derivation of the burn-rate table, the reasons for two windows, and the failure modes of the simpler alternatives.
- [Google SRE Book, Chapter 6: Monitoring Distributed Systems](https://sre.google/sre-book/monitoring-distributed-systems/) — symptoms versus causes, and why pages should be about user-visible harm.
- [Prometheus documentation: Alerting rules and `increase()`](https://prometheus.io/docs/prometheus/latest/configuration/alerting_rules/) — how windowed counters and alert rules are expressed in the most common open-source stack.
- [Visa Core Rules and Visa Product and Service Rules](https://usa.visa.com/support/consumer/visa-rules.html) — authorization validity periods and processing timeframes that set the `AUTHORIZED` and `CAPTURED` limits.
- [Beyer, Murphy, Rensin, Kawahara, Thorne (eds.), "The Site Reliability Workbook" (O'Reilly, 2018)](https://sre.google/workbook/table-of-contents/) — Chapter 2 on implementing SLOs, including choosing the SLI and the bad-event definition.
