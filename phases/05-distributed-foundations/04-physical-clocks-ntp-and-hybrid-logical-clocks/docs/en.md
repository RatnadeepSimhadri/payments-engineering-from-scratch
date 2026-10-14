# Physical Clocks, NTP, and Hybrid Logical Clocks

> Clock skew is why your timestamps cannot order transactions.

**Type:** Build
**Languages:** Java, Python
**Prerequisites:** Phase 5, Lesson 03 (Time, Order, and Clocks: Lamport and Vector Clocks), Phase 5, Lesson 02 (Failure Models)
**Time:** ~75 minutes

## Learning Objectives

- Quantify how a quartz clock drifts, in parts per million and in milliseconds per day, and explain why correction is never finished
- Derive NTP's offset and delay equations from four timestamps and state the error bound that follows from them
- Explain why a wall-clock timestamp cannot order two events on different machines closer together than the clock skew
- Implement a Hybrid Logical Clock that is never behind physical time, never more than a bound ahead of it, and respects happens-before
- Compare wall-clock, Lamport, and HLC timestamps on the same message trace and state what each one promises
- Choose between a monotonic clock, a wall clock, and an HLC for a given job in a payment system

## The Problem

A payment platform stores every state transition of a payment with a `created_at` timestamp taken from `System.currentTimeMillis()` on whichever service performed it. The gateway authorises; the ledger posts; the notifier emails. A reporting job later reconstructs each payment's history by sorting its events by `created_at`. The job has run for two years without complaint.

One Thursday the ledger host's NTP daemon is restarted during a kernel patch and comes back pointing at a misconfigured internal time server. For the next six hours the ledger's clock runs 80 milliseconds behind the gateway's. The platform processes about 400 payments per second, and for each of them the ledger's `posted` event now carries a timestamp earlier than the gateway's `authorized` event that caused it. The reporting job dutifully sorts and produces histories in which money is posted before it is authorised. A downstream risk model that computes "time from authorisation to posting" sees negative durations, treats them as data errors, and drops 8.6 million rows. A dispute team pulls a payment's timeline for a chargeback and cannot explain to the card network why the ledger entry predates the authorisation. The compliance archive, which is append-only and ordered by timestamp, now contains an ordering that cannot be corrected without a documented exception.

Nothing was lost and no money moved wrongly. The system's claim that timestamps order events was simply false, and had been false by smaller margins the entire time: before the incident the two hosts disagreed by 2 to 6 milliseconds, which was enough to misorder a few hundred payments a day whose authorise-to-post latency was under that. Nobody noticed because the misorderings were rare and the consequences were quiet.

Lesson 03 built clocks that order events correctly but say nothing about time: a Lamport timestamp of 40,000 could be last Tuesday or ten seconds ago. This lesson starts from the other side. It builds the physical clock and shows exactly how far it can be trusted, implements the algorithm NTP uses to discipline it and the bound on what that algorithm can achieve, and then combines the two ideas into a Hybrid Logical Clock: a 64-bit timestamp that reads like a wall clock to within the skew bound, and orders causally related events correctly regardless of skew. CockroachDB, MongoDB, and YugabyteDB stamp every transaction with one.

## The Concept

### What a physical clock is

Every server keeps time with a quartz crystal oscillating at a nominal frequency, and a counter. The crystal is never exactly at its nominal frequency. The error is expressed in parts per million (ppm): a 50 ppm crystal gains or loses 50 microseconds per second, which is 4.3 seconds per day and about 26 minutes per year. Temperature changes the rate; the same crystal reads differently at 20 and 40 degrees. The drift of commodity server hardware is typically 10 to 100 ppm, and it is not constant.

Two consequences shape everything else in this lesson. First, two clocks that were set to the same value will disagree by milliseconds within minutes and by seconds within a day, so synchronisation is a continuous process, not a configuration step. Second, the operating system exposes two different clocks and conflating them is the most common timing bug in application code:

| Clock | Java | What it measures | Can it go backwards? | Use for |
|-------|------|------------------|----------------------|---------|
| Wall clock (realtime) | `System.currentTimeMillis()`, `Instant.now()` | Milliseconds since 1970-01-01 UTC, as currently believed | Yes: NTP steps, manual changes, leap-second handling | Timestamps that humans or other systems will read |
| Monotonic clock | `System.nanoTime()` | Elapsed time since an arbitrary origin | No | Measuring durations: timeouts, latency, rate limits, leases |

A timeout computed as `deadline = currentTimeMillis() + 5000` fires early if NTP steps the clock forward and never fires if it steps backward. A lease (Lesson 12) whose expiry is compared against the wall clock can be extended for free by a clock step. Durations use the monotonic clock; only points in time that leave the machine use the wall clock.

### Leap seconds, UTC, and the 2017 incident

UTC inserts a leap second when the Earth's rotation drifts far enough from atomic time; 27 have been inserted since 1972, the last on 2016-12-31. POSIX time cannot represent `23:59:60`, so operating systems either repeat a second (the wall clock goes backwards by one second) or smear it (run the clock slow for several hours, which Google and Amazon do on their fleets). On 2017-01-01 Cloudflare's DNS service computed a duration as the difference of two wall-clock readings across the leap second, obtained a negative number, passed it to a random-number generator that panicked on negative input, and lost about 0.2% of its DNS queries for 90 minutes. The code had used the wall clock to measure a duration. The fix was `time.Since`, Go's monotonic clock. The same bug exists in any codebase that subtracts two `currentTimeMillis()` readings and assumes the result is non-negative.

### NTP: estimating the offset

The Network Time Protocol (RFC 5905) disciplines a clock by asking a server what time it is and correcting for the network delay. One exchange produces four timestamps:

| Symbol | Taken by | Meaning |
|--------|----------|---------|
| `t1` | client | request sent, client clock |
| `t2` | server | request received, server clock |
| `t3` | server | response sent, server clock |
| `t4` | client | response received, client clock |

Let the client clock be behind the server by `theta`, the outbound network delay be `d1`, and the return delay be `d2`. Then in server time the request arrives at `t1 + theta + d1 = t2` and the response arrives back at `t3 + d2 = t4 + theta`. Two equations, three unknowns (`theta`, `d1`, `d2`). NTP assumes the path is symmetric, `d1 = d2`, and solves:

```text
offset  theta = ((t2 - t1) + (t3 - t4)) / 2
delay   delta = (t4 - t1) - (t3 - t2)
```

The delay subtracts the server's processing time `t3 - t2` from the client's observed round trip, so it measures only the network. Trace one exchange with numbers: client is 100 ms behind, 10 ms each way, no processing.

```text
t1 = 1000 (client)         true time 1100
t2 = 1110 (server)         true time 1110, arrived after 10 ms
t3 = 1110 (server)
t4 = 1020 (client)         true time 1120, client reads 1020 because it is 100 behind
theta = ((1110 - 1000) + (1110 - 1020)) / 2 = (110 + 90) / 2 = 100    correct
delta = (1020 - 1000) - (1110 - 1110) = 20                            correct
```

Now break the symmetry: 30 ms out, 10 ms back.

```text
t1 = 1000, t2 = 1130, t3 = 1130, t4 = 1040
theta = ((1130 - 1000) + (1130 - 1040)) / 2 = (130 + 90) / 2 = 110    wrong by 10
delta = (1040 - 1000) - 0 = 40
```

The estimate is off by `(d1 - d2) / 2 = 10 ms`. Since `d1 + d2 = delta` and both are non-negative, the asymmetry is at most `delta`, and the error is at most `delta / 2`. **The offset error of one exchange is bounded by half its measured round-trip delay, and the client can compute that bound.** This is the single most useful fact about NTP for an application engineer: the delay tells you how much to trust the offset.

NTP's *clock filter* follows directly. Poll several times and keep the sample with the smallest delay, because the fastest round trip is the one with the least queueing and therefore the smallest possible asymmetry. Then correct the local clock: if the offset is under 128 ms, *slew* it (run the clock slightly fast or slow, at most 500 ppm, until it catches up, so time never jumps or reverses); if larger, *step* it (jump), which is what happened on the Thursday in The Problem and what makes `currentTimeMillis()` non-monotonic.

In a datacentre with 0.2 ms round trips, NTP holds clocks within a few hundred microseconds. Across the public internet with 20 to 80 ms round trips, a few milliseconds is normal and tens of milliseconds happen. A misconfigured or unreachable server makes the bound meaningless, which is why production fleets monitor the offset and the stratum of every host and alarm on both.

### Why timestamps cannot order transactions

Call the maximum clock skew between any two machines `epsilon`. If event `a` on machine A and event `b` on machine B have wall-clock stamps that differ by less than `epsilon`, the stamps say nothing about which happened first; either order is consistent with the readings. The authorise-to-post latency in The Problem was 4 ms and the skew was 80 ms, so the stamps were worthless for ordering those two events. Even at a healthy 2 ms of skew, every pair of events closer together than 2 ms is unordered, and in a payment pipeline most causally related events are that close.

There are three honest responses. Spanner's TrueTime exposes `epsilon` explicitly as an interval `[earliest, latest]` from GPS and atomic clocks, keeps it around 7 ms, and makes a transaction *wait out* the uncertainty before committing so that its timestamp is guaranteed to be in the past for every other node (commit-wait). That works if you own the hardware. Lamport clocks (Lesson 03) abandon physical meaning entirely. Hybrid Logical Clocks keep the physical meaning and fix the ordering, at the cost of a counter.

### Hybrid Logical Clocks

Kulkarni, Demirbas, Madeppa, Avva, and Leone (2014) define an HLC timestamp as a pair `(l, c)`: `l` is the *logical wall time*, a value in physical-clock units that tracks the physical clock `pt` but never runs behind it, and `c` is a counter that distinguishes events whose `l` is the same. Timestamps compare lexicographically: first by `l`, then by `c`.

Each node keeps its own `(l, c)` and applies two rules. For a local event or a message send:

```text
l' = max(l, pt)
c' = (l' == l) ? c + 1 : 0
```

If the physical clock has moved past the previous `l`, adopt it and reset the counter; otherwise the physical clock has not advanced (or is behind), so keep `l` and bump the counter. For a message receive carrying `(l_m, c_m)`:

```text
l' = max(l, l_m, pt)
if l' == l == l_m:   c' = max(c, c_m) + 1      both sides at the same l: step past both counters
elif l' == l:        c' = c + 1                our l wins: just a local tick
elif l' == l_m:      c' = c_m + 1              the message's l wins: step past its counter
else:                c' = 0                    physical time has passed both: fresh counter
```

Three invariants follow, and the tests check each one:

1. **`l >= pt`.** The timestamp is never behind the local physical clock, so it reads like a wall clock that is at worst slightly ahead.
2. **`l - pt <= epsilon`.** The timestamp is never more than the clock skew ahead of physical time, because the only way `l` exceeds `pt` is by adopting a remote `l_m`, and a correct remote clock is within `epsilon` of ours. If a message arrives with `l_m` further ahead than that, something is wrong (a node with a broken clock, a corrupted message) and the implementation refuses it rather than poisoning every downstream clock. The `maxDrift` parameter in the code is this bound.
3. **`e happens-before f` implies `hlc(e) < hlc(f)`.** On one node, every event strictly increases `(l, c)`. Across a message, the receiver's `l'` is at least `l_m`, and if equal, its `c'` exceeds `c_m`. By induction along any causal chain, timestamps strictly increase.

The second invariant is what makes HLC practical: `c` stays small (it only grows while `pt` is stuck behind `l`, which lasts at most `epsilon`), so the pair fits in 64 bits as 48 bits of milliseconds plus 16 bits of counter. 48 bits of milliseconds cover 8,900 years; 16 bits allow 65,535 events per millisecond per node before the counter overflows, which the code treats as an error rather than wrapping.

### Worked example by hand

Three nodes: the gateway's clock is exact, the ledger's is 80 ms behind, the notifier's is 35 ms ahead. True time starts at `T`. Trace the first four events of the payment.

```text
event                        pt (node)   previous (l,c)   rule                              new (l,c)
gateway: send authorize      T+0         (T+0, 0)         l'=max(T,T)=T == l  -> c+1         (T+0, 1)
ledger: recv authorize       T-76        (T-80, 0)        l'=max(T-80, T, T-76)=T = l_m      (T+0, 2)
                                                          -> c' = c_m + 1 = 2
ledger: send posted          T-74        (T+0, 2)         l'=max(T, T-74)=T == l -> c+1      (T+0, 3)
gateway: recv posted         T+9         (T+0, 1)         l'=max(T, T, T+9)=T+9: pt wins     (T+9, 0)
```

The ledger's wall clock says `T-76` for an event caused by one at `T+0`: the wall order is wrong. The ledger's HLC says `(T+0, 2)`, after the gateway's `(T+0, 1)`: the HLC order is right, and `l` is still `T+0`, within 80 ms of the ledger's own physical reading and exactly equal to true time. When the gateway receives the reply at `T+9`, its physical clock has passed `T`, so `l` jumps to `T+9` and the counter resets. The counter did its job for the few milliseconds the ledger's clock lagged, and then disappeared.

### Four clocks compared

| Property | Wall clock | Lamport | Vector | HLC |
|----------|-----------|---------|--------|-----|
| `a -> b` implies `ts(a) < ts(b)` | No (skew) | Yes | Yes | Yes |
| `ts(a) < ts(b)` implies `a -> b` or concurrent-and-arbitrary | Meaningless under skew | Yes | Yes, and detects concurrency | Yes |
| Detects concurrent events | No | No | Yes | No |
| Relates to physical time | Yes, within `epsilon` | No | No | Yes, within `epsilon` |
| Size | 64 bits | 64 bits | `n` x 64 bits | 64 bits |
| Needs membership knowledge | No | No | Yes | No |
| Can be used for "give me everything since 14:02" | Yes, approximately | No | No | Yes, approximately |
| Can go backwards on one node | Yes (step) | No | No | No |

HLC gives up what vector clocks have (concurrency detection) to keep what wall clocks have (meaning), and it inherits from Lamport the guarantee wall clocks lack. It is the right default for a timestamp that will be stored, compared across machines, and read by humans.

### Complexity

Every operation on every clock in this lesson is `O(1)` time and `O(1)` space per node. The HLC's cost over a wall clock is one `max` and one comparison per event, and 16 bits of storage if packed. The cost that matters is not computational: it is that every message must carry the sender's timestamp, and every receive must merge it. A service that reads a database row stamped by another service and then stamps its own row must treat that read as a receive, or the causal chain is broken. This is the discipline the Build section enforces with the `Node.receive` method.

### What these clocks do not do

- NTP does not make clocks agree; it bounds how much they disagree, and the bound is only known if you measure the delay. A host that cannot reach its time servers drifts at its crystal's rate with no warning unless you monitor it.
- An HLC does not detect concurrency. Two events on different nodes with no causal path will get some order; the order is consistent (everyone agrees) and roughly chronological, but it is not "what really happened first" in any physical sense within `epsilon`. If you need to know that two writes conflicted, you need a vector clock (Lesson 03) or a protocol (Phase 6, Lesson 09).
- An HLC does not fix a broken clock. It tolerates skew up to the bound and refuses beyond it. A node whose clock is an hour off will have every message it sends rejected, which is the correct, loud failure.
- Neither gives you a transaction order across nodes without further protocol. Spanner needs commit-wait on top of TrueTime; CockroachDB needs its uncertainty-interval restarts on top of HLC. Phase 7, Lesson 09 covers both.

## Build It

The Java implementation is in `code/java/Main.java`; the walkthrough follows its structure. Every clock reads from a `TrueTime` object that the demo and tests advance by hand, so nothing sleeps and every run is identical.

### Step 1: True time and a drifting clock

```java
final class TrueTime {
    private long nowMs;
    TrueTime(long startMs) { this.nowMs = startMs; }
    long now() { return nowMs; }
    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("time does not go backwards");
        nowMs += ms;
    }
}

final class DriftingClock {
    private final TrueTime trueTime;
    private final double driftPpm;
    private long offsetMs;
    private final long epochTrueMs;

    long now() {
        long elapsed = trueTime.now() - epochTrueMs;
        long drifted = Math.round(elapsed * (driftPpm / 1_000_000.0));
        return epochTrueMs + elapsed + drifted + offsetMs;
    }

    void step(long deltaMs) { offsetMs += deltaMs; }
    long error() { return now() - trueTime.now(); }
}
```

`TrueTime` is the simulation's oracle: the time nobody in the system can see. `DriftingClock` is what a machine sees: true time, plus a rate error proportional to elapsed time, plus an offset. `step` is NTP's correction; note that it changes the offset and leaves the drift alone, so a corrected clock immediately starts drifting again. The `error()` method exists only for the demo and tests; a real machine cannot call it, which is the entire problem.

**Predict:** a 50 ppm clock is stepped to perfect accuracy. How wrong is it 1,000 seconds later?

### Step 2: The NTP sample

```java
record NtpSample(long t1, long t2, long t3, long t4) {
    long offset() { return ((t2 - t1) + (t3 - t4)) / 2; }
    long delay()  { return (t4 - t1) - (t3 - t2); }
}
```

Two lines of arithmetic, straight from RFC 5905 section 8. The tests check them against the hand traces above: symmetric delay recovers the exact offset; server processing time is excluded from delay and does not bias the offset; an asymmetric path biases the offset by half the asymmetry, which is within `delay / 2`.

**Predict:** `t1 = 500, t2 = 700, t3 = 710, t4 = 560`. What are the offset and delay, and is the client ahead or behind?

### Step 3: The exchange and the clock filter

```java
NtpSample exchange() {
    long outbound = minLatencyMs + (jitterMs == 0 ? 0 : random.nextLong(jitterMs + 1));
    long inbound  = minLatencyMs + (jitterMs == 0 ? 0 : random.nextLong(jitterMs + 1));
    long t1 = client.now();
    trueTime.advance(outbound);
    long t2 = server.now();
    long t3 = server.now();
    trueTime.advance(inbound);
    long t4 = client.now();
    return new NtpSample(t1, t2, t3, t4);
}

NtpSample bestOf(int n) {
    NtpSample best = null;
    for (int i = 0; i < n; i++) {
        NtpSample s = exchange();
        if (best == null || s.delay() < best.delay()) best = s;
    }
    return best;
}

long synchronize(int polls) {
    NtpSample best = bestOf(polls);
    client.step(best.offset());
    return best.offset();
}
```

The network is a seeded `Random` drawing independent outbound and inbound latencies, so paths are asymmetric by chance exactly as real ones are. `bestOf` is the clock filter: minimum delay wins. `synchronize` steps the clock by the best estimate. A real NTP client would slew for small offsets and use a weighted combination of several servers; the simplification keeps the arithmetic legible and the demo's residual error (a few milliseconds with 60 ms of jitter) shows the filter working.

**Predict:** with `jitterMs = 0`, how many polls does `bestOf` need to recover the exact offset?

### Step 4: The HLC timestamp

```java
record HlcTimestamp(long l, int c) implements Comparable<HlcTimestamp> {
    static final int COUNTER_BITS = 16;
    static final long COUNTER_MAX = (1L << COUNTER_BITS) - 1;

    long pack() { return (l << COUNTER_BITS) | c; }
    static HlcTimestamp unpack(long packed) {
        return new HlcTimestamp(packed >>> COUNTER_BITS, (int) (packed & COUNTER_MAX));
    }

    @Override public int compareTo(HlcTimestamp o) {
        int byL = Long.compare(l, o.l);
        return byL != 0 ? byL : Integer.compare(c, o.c);
    }
}
```

The record's compact constructor (in the full file) rejects a counter above 65,535 and an `l` beyond 48 bits. `pack` places `l` in the high 48 bits and `c` in the low 16, so comparing two packed `long` values gives the same order as `compareTo`. That property is why a packed HLC can be a database column, a sort key, or a Kafka message timestamp without the consumer knowing it is an HLC. The tests check that order is preserved through `pack` and that `unpack` round-trips.

**Predict:** two timestamps `(T, 65535)` and `(T + 1, 0)`. Which packs to the larger `long`, and by how much?

### Step 5: The clock itself

```java
HlcTimestamp now() {
    long pt = physical.now();
    long newL = Math.max(l, pt);
    int newC = (newL == l) ? bumpCounter(c) : 0;
    return commit(newL, newC, pt);
}

HlcTimestamp receive(HlcTimestamp m) {
    long pt = physical.now();
    long newL = Math.max(Math.max(l, m.l()), pt);
    int newC;
    if (newL == l && newL == m.l()) newC = bumpCounter(Math.max(c, m.c()));
    else if (newL == l)             newC = bumpCounter(c);
    else if (newL == m.l())         newC = bumpCounter(m.c());
    else                            newC = 0;
    return commit(newL, newC, pt);
}

private HlcTimestamp commit(long newL, int newC, long pt) {
    if (newL - pt > maxDriftMs) throw new ClockDriftException(newL, pt, maxDriftMs);
    l = newL;
    c = newC;
    return new HlcTimestamp(l, c);
}
```

This is Figure 5 of the Kulkarni paper, with one engineering addition: the new values are computed into locals and validated by `commit` *before* the fields change. An earlier version of this code updated `l` and then checked the drift bound, so a rejected message left the clock with a poisoned `l` and every later timestamp on that node was wrong. The test named "rejected receive did not corrupt the clock" caught it. Validation before mutation is the same discipline as `Money`'s checked arithmetic (Phase 10, Lesson 01): an exception must leave the object as it was.

`bumpCounter` throws on overflow rather than wrapping. 65,535 events in one millisecond on one node means the physical clock is stuck or something is spinning; silently wrapping to zero would produce a timestamp smaller than its predecessor and break the first invariant without a sound.

**Predict:** the clock is at `(T+300, 4)` and `pt = T+200`. A message arrives with `(T+100, 9)`. What is the new state, and which branch fired?

### Step 6: A node with three clocks and a trace

```java
final class Node {
    final DriftingClock wall;
    final LamportClock lamport = new LamportClock();
    final HybridLogicalClock hlc;

    TraceEvent local(String what) {
        return new TraceEvent(name, what, trueTime.now(), wall.now(), lamport.tick(), hlc.now());
    }
    TraceEvent receive(String what, TraceEvent message) {
        return new TraceEvent(name, "recv " + what, trueTime.now(), wall.now(),
                lamport.receive(message.lamport()), hlc.receive(message.hlc()));
    }
}
```

Every event is stamped three ways at once so the stamps can be compared on identical history. `Trace.deliver` records each send/receive pair as a causal pair, and `Trace.report` counts, for each clock, how many pairs it orders wrongly. The scenario in `Main.paymentTrace` is the one from The Problem: gateway exact, ledger 80 ms behind, notifier 35 ms ahead, with 1 to 6 ms of network latency between events. The report says: four causal pairs, wall clock wrong on two of them, Lamport wrong on none, HLC wrong on none, and the HLC's `l` never more than 35 ms (the largest skew) from true time.

**Predict:** if the ledger's offset were `-3` ms instead of `-80`, how many wall-clock violations would the trace show? Which pairs?

### Step 7: Run it

```bash
cd phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== A quartz clock drifts ==
50 ppm clock after one day: 4320 ms fast

== NTP: estimating offset from four timestamps ==
symmetric 10 ms each way: offset=150 ms (true -150), delay=20 ms
jittery sample 1: offset=158 ms, delay=69 ms, error=8 ms (bound delay/2=34)
jittery sample 2: offset=150 ms, delay=35 ms, error=0 ms (bound delay/2=17)
best-of-8 correction applied: 146 ms; residual client error after step: -4 ms

== Three clocks on one payment trace ==
node      event                  true     wall     lamport  hlc (l shown relative to T0)
gateway   send authorize pay_7f3 0        0        1        0.1
ledger    recv authorize pay_7f3 4        -76      2        0.2
ledger    send posted pay_7f3    6        -74      3        0.3
gateway   recv posted pay_7f3    9        9        4        9.0
causal pairs: 4; wall-clock order violated: 2; Lamport violated: 0; HLC violated: 0
HLC stayed within 35 ms of true time; Lamport's largest value was 9 (unrelated to time)

== Bounded drift: HLC refuses to run away from physical time ==
rejected: logical time 1700000002000 is 2000 ms ahead of physical time 1700000000000; bound is 500
```

The test run prints `40 passed, 0 failed`.

### In Python

`code/python/main.py` keeps every type and method (`TrueTime`, `DriftingClock`, `NtpSample`, `NtpClient`, `HlcTimestamp`, `HybridLogicalClock`, `Node`, `Trace`) and prints the same demo, digit for digit, including the jittery NTP samples. Four things had to be made explicit to get there.

- **Java's integer division truncates; Python's `//` floors.** An NTP offset is negative whenever the client is ahead, and `((t2 - t1) + (t3 - t4)) / 2` on `-301` is `-150` in Java but `-301 // 2` is `-151`. `NtpSample.offset` uses a `tdiv` helper that truncates toward zero, and a test pins the `-150` case.
- **`Math.round` is not `round`.** `Math.round(x)` is `floor(x + 0.5)`; Python's `round` is banker's rounding (`round(2.5) == 2`). `DriftingClock.now` uses `java_round`, so a drift that lands on exactly half a millisecond rounds the same way in both languages.
- **The seeded generator is ported, not substituted.** `JavaRandom` reproduces `java.util.Random`'s 48-bit LCG and the JDK 17+ `nextLong(bound)` that `Random` inherits from `RandomGenerator`, including its rejection loop. Seed 7 draws `33, 16, 8, 7` in both languages, which is why the jittery samples and the best-of-8 correction (146 ms) match.
- **No 64-bit integer.** `HlcTimestamp` is a frozen dataclass whose `__post_init__` enforces the 48-bit `l` and 16-bit `c` bounds that a Java `long` and `int` would otherwise suggest, so `pack` can never produce a value wider than 64 bits. The counter overflow is `ArithmeticError` (Java's `ArithmeticException`), and `ClockDriftException` keeps its name.

```python
def tdiv(a: int, b: int) -> int:
    """Java's long division truncates toward zero; Python's // floors."""
    q = abs(a) // abs(b)
    return q if (a >= 0) == (b > 0) else -q
```

For real clocks, `time.monotonic_ns()` is the monotonic clock and `time.time_ns()` the wall clock; the lesson reads neither, because every clock derives from the injected `TrueTime`.

```bash
cd phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks/code/python
python3 main.py
python3 -m unittest -v
```

### Porting notes

Go's `time.Time` carries both a wall and a monotonic reading since Go 1.9, which is why `time.Since` was the Cloudflare fix; an HLC in Go would hold `l` and `c` as `int64`/`uint16` and return `(HlcTimestamp, error)` from `Receive` for the drift bound instead of throwing. Keep the same type names (`DriftingClock`, `NtpSample`, `HybridLogicalClock`) and the same 40 test cases. `math/rand` is a different generator from `java.util.Random`, so either port the 48-bit LCG as the Python version does or document the seed and accept that the jittery-sample values in the demo differ; the invariants the tests check will not.

## Use It

### `java.time.Clock` and the two JDK clocks

`System.currentTimeMillis()` and `Instant.now()` read the wall clock; `System.nanoTime()` reads the monotonic clock. `java.time.Clock` is an injectable abstraction over the wall clock, and `Clock.fixed` and `Clock.offset` exist so tests can do exactly what `TrueTime` does here. Production code that reads `Instant.now()` directly inside business logic is untestable for every scenario in this lesson. The idiom is to accept a `Clock` in the constructor and call `clock.instant()`; the JDK's own `java.time` tests do this.

### chrony and ntpd

Linux hosts run `chronyd` (or the older `ntpd`) as the NTP client. `chronyc tracking` shows the current offset estimate, the root delay, and the root dispersion, which together are the host's own bound on its error; `chronyc sources` shows each server's stratum and the delay to it. Cloud providers run a time service on a link-local address (`169.254.169.123` on AWS, `metadata.google.internal` on GCP) with sub-millisecond delay, so in-cloud skew is typically under a millisecond when configured. The Thursday incident in The Problem is a host pointed at the wrong server; the alert that would have caught it is on `chronyc tracking`'s offset exceeding a threshold, or on the stratum rising above the expected value.

### HLC in production databases

CockroachDB stamps every transaction and every MVCC version with an HLC, encoded as 64 bits of wall time plus 32 bits of logical counter, and enforces a `--max-offset` (default 500 ms): a node whose clock drifts beyond it shuts itself down, which is this lesson's `ClockDriftException` as an operational policy. MongoDB's `$clusterTime` is an HLC carried on every message and used for causally consistent sessions. YugabyteDB does the same. In each case the HLC gives the system a timestamp that is both a usable "as of" time for queries and a correct ordering for causally related writes, which is precisely what the reporting job in The Problem needed.

### Spanner and TrueTime

Spanner takes the other path: GPS receivers and atomic clocks in every datacentre give an uncertainty interval of a few milliseconds, and a transaction waits out that interval before reporting commit so that its timestamp is unambiguously in the past everywhere. The result is external consistency from wall-clock timestamps alone, at the cost of hardware most teams do not have and a commit-wait of several milliseconds. Phase 7, Lesson 09 compares the two designs in detail.

### Kafka timestamps

A Kafka record carries one timestamp, and the topic's `message.timestamp.type` decides whose clock it is: `CreateTime` (the producer's wall clock, so subject to every skew problem here) or `LogAppendTime` (the broker's wall clock, consistent within a partition but unrelated to the event). Neither is causal across partitions. Stream processors that window by event time (Phase 8, Lessons 07 and 08) are therefore reasoning about producer clocks, and watermarks exist because those clocks skew.

## Ship It

`outputs/runbook-clock-skew.md` is an on-call runbook for time: what to check when timestamps look wrong (`chronyc tracking`, stratum, offset history), the thresholds to alarm on, the two-clock rule for code review, and the questions to ask before trusting a `created_at` column for ordering. Use the review section on any pull request that compares timestamps from two machines.

## Exercises

1. **Reinforce.** Add slewing to `DriftingClock`: a `slew(long deltaMs, long maxRatePpm)` method that applies the correction gradually as true time advances, never letting `now()` go backwards. Change `NtpClient.synchronize` to slew when `|offset| < 128` and step otherwise, matching RFC 5905's rule. Write a test that a slewed clock is monotonic across the correction and a stepped one is not.
2. **Apply.** The reporting job in The Problem needs "all events for payment `pay_7f3` in causal order" and "all events between 14:02:00 and 14:03:00". Store `TraceEvent`s in a list keyed by packed HLC and implement both queries. Then show, with a test, that the second query returns events whose true time is within `epsilon` of the requested range and no further, and explain in a comment why that is the best any clock can do.
3. **Extend.** Combine this lesson with Lesson 03: give each `Node` a vector clock as well, run `paymentTrace` plus two concurrent, unrelated events on the gateway and the notifier, and show that the HLC assigns them an order while the vector clock reports them concurrent. Then design the timestamp for a ledger posting (Phase 10, Lesson 03) that must be both human-readable and causally correct, and justify why it is an HLC and not a vector clock, in terms of the `n x 64 bits` row of the comparison table.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Drift | "the clock is off" | The rate error of an oscillator, in parts per million; 50 ppm is 4.3 s per day, and it varies with temperature, so correction is continuous |
| Offset | "the clock is wrong by X" | The difference between a clock's reading and the reference at one instant; what NTP estimates and corrects |
| Skew | "the servers disagree" | The offset between two clocks; the `epsilon` below which wall-clock timestamps cannot order events |
| Slew vs step | "NTP fixed the clock" | Slew runs the clock slightly fast or slow so it stays monotonic; step jumps it, which makes the wall clock go backwards or skip |
| Monotonic clock | "nanoTime" | A clock that only moves forward and has no relation to calendar time; the only correct clock for durations, timeouts, and leases |
| Round-trip delay | "ping time" | `(t4 - t1) - (t3 - t2)` in NTP; half of it is the bound on the offset error of that sample |
| Hybrid Logical Clock | "a Lamport clock with a wall clock in it" | A pair `(l, c)` where `l` tracks physical time within the skew bound and `c` breaks ties, so causally related events are always ordered and the value still means a time |
| Commit-wait | "Spanner waits for the clock" | Delaying a transaction's commit acknowledgement until its timestamp is guaranteed past on every node, using a known uncertainty bound |

## Further Reading

- [RFC 5905: Network Time Protocol Version 4](https://www.rfc-editor.org/rfc/rfc5905) — section 8 has the four-timestamp offset and delay equations; section 10 the clock filter; section 11 the step/slew thresholds this lesson simplifies.
- [Logical Physical Clocks and Consistent Snapshots in Globally Distributed Databases](https://cse.buffalo.edu/tech-reports/2014-04.pdf) (Kulkarni, Demirbas, Madeppa, Avva, Leone, 2014) — the HLC paper; Figure 5 is the algorithm in Step 5 and Section 4 proves the bounded-drift invariant.
- [Time, Clocks, and the Ordering of Events in a Distributed System](https://lamport.azurewebsites.net/pubs/time-clocks.pdf) (Lamport, 1978) — Section 4 on physical clocks is the part Lesson 03 skipped; it derives how closely clocks can be synchronised given message delay uncertainty, which is NTP's bound in a different notation.
- [Spanner: Google's Globally-Distributed Database](https://research.google/pubs/pub39966/) (Corbett et al., 2012) — Section 3 defines TrueTime and Section 4.1.2 commit-wait; read it to see what buying the skew bound in hardware makes possible.
- [How and why the leap second affected Cloudflare DNS](https://blog.cloudflare.com/how-and-why-the-leap-second-affected-cloudflare-dns/) (Cloudflare, 2017) — a public post-mortem of measuring a duration with the wall clock; the fix is one function call and the lesson is the two-clock rule.
- [CockroachDB: Living Without Atomic Clocks](https://www.cockroachlabs.com/blog/living-without-atomic-clocks/) — how a production database uses HLC plus a maximum offset instead of TrueTime, and what it costs in transaction restarts.
- [`java.time.Clock` documentation](https://docs.oracle.com/en/java/javase/21/docs/api/java.base/java/time/Clock.html) — the injectable clock abstraction; `Clock.fixed` and `Clock.offset` are `TrueTime` with a standard API.
