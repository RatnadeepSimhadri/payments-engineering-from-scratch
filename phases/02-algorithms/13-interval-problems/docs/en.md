# Interval Problems: Merging, Scheduling, and Sweep Lines

> Holds, authorizations, and overlapping settlement windows.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 2 Lesson 01 (Sorting), Phase 2 Lesson 07 (Greedy Algorithms and Exchange Arguments), Phase 0 Lesson 07 (Decimal and Integer Arithmetic)
**Time:** ~60 minutes

## Learning Objectives

- Define a half-open interval, explain why a timeline must use half-open intervals, and show the off-by-one that closed intervals cause
- Merge a set of intervals into a disjoint sorted list in `O(n log n)` and state the invariant the output satisfies
- Build a sweep line over start and end events, order the events correctly at equal timestamps, and compute the maximum overlap and the peak weighted exposure
- Prove the earliest-finish greedy rule for interval scheduling with an exchange argument and cross-check it against brute force
- Apply all three to card authorization holds: total exposure at an instant, peak exposure over a day, and expired holds to release

## The Problem

A card issuer's risk engine decides whether to approve each authorization by comparing the request against the cardholder's available credit: limit minus posted balance minus **open holds**. A hold is an authorization that has been approved but not yet captured; the merchant has reserved the funds and may capture any amount up to the hold within a window (seven days for most merchants, up to thirty for hotels and car rentals, and the hold releases when the window closes if no capture arrives). The engine stores holds as rows with `amount`, `authorized_at`, and `expires_at`, and computes open holds with `SELECT SUM(amount) WHERE authorized_at <= now AND now <= expires_at`.

Note the second `<=`. A cardholder has a hotel hold for 250.00 USD expiring at exactly 17:00:00 and buys a coffee at 17:00:00. The query counts the hotel hold as still open, the available credit is 250.00 too low, and the coffee is declined. The cardholder calls. Support sees the hold was released "at 17:00" and the coffee was attempted "at 17:00" and cannot explain the decline. The same inclusive bound causes a double count when two holds abut: a car rental hold `[11:00, 15:00]` and a parking hold `[15:00, 16:00]` both contain 15:00:00, and at that instant the engine briefly believes the cardholder has both amounts reserved. On a card near its limit this is the difference between an approval and a decline, and it is not reproducible a second later.

The second failure is in the daily risk report, which asks "what was this cardholder's peak exposure today": the largest amount held simultaneously at any instant. The first implementation samples every minute: 1,440 samples times every hold. It misses any peak that lasts under a minute, it costs `O(samples * holds)`, and when someone switches it to sampling every second to catch the short peaks the report takes four hours. The correct computation is a sweep line: sort the start and end events, walk them once, and track the running total; it finds the exact peak and the exact instant it occurred in `O(n log n)` for `n` holds.

Both failures are about intervals on a timeline: how to represent them so that adjacent ones do not overlap, how to merge them, how to find the busiest instant, and how to choose among them. The same four operations appear in settlement cutoff windows that overlap across schemes, in maintenance scheduling, in meeting rooms, and in every "is this time slot free" question a system asks.

## The Concept

### Half-open intervals

An interval `[start, end)` contains every instant `t` with `start <= t < end`. The start is in; the end is out. Three consequences make this the only sane representation for a timeline:

1. **Adjacent intervals partition time.** `[9:00, 17:00)` and `[17:00, 17:15)` share no instant and leave no gap. With closed intervals `[9:00, 17:00]` and `[17:00, 17:15]`, the instant 17:00 belongs to both; with open intervals it belongs to neither. The hotel-and-coffee decline is the closed-interval bug.
2. **Length is `end - start`.** No `+ 1`, no question of whether the unit is seconds or milliseconds. `[0, 10)` has length 10 and contains exactly the ten integers `0..9`.
3. **Empty intervals exist and are harmless.** `[5, 5)` has length 0 and contains nothing. A hold whose window has not opened yet, or a placeholder, can be represented without a special case; the algorithms skip empties.

Two intervals **overlap** when they share at least one instant: `a.start < b.end && b.start < a.end`. Note the strict inequalities; with `<=` touching intervals would count as overlapping. They **touch** when `a.end == b.start`, and for merging purposes touching intervals should become one, so `merge` uses `<=`. The two predicates are different operations and the code has both.

| Representation | `[0,10)` and `[10,20)` | Length of `[0,10)` | Instant 10 belongs to | Verdict |
|---|---|---|---|---|
| Half-open `[s, e)` | disjoint, touching | 10 | the second only | use this |
| Closed `[s, e]` | overlap at 10 | 11 | both | double counts at every boundary |
| Open `(s, e)` | disjoint with a gap | 9 | neither | loses every boundary instant |

Everything below uses `long` instants. Whether they are minutes, seconds, or epoch milliseconds is the caller's business; the algorithms only compare and subtract. This mirrors the `Money` discipline: an instant is an integer count of a unit, never a float.

### Merging

Given any set of intervals, produce the smallest set of disjoint intervals with the same union. Sort by start. Walk the sorted list keeping the last merged interval: if the next interval starts at or before the last one's end (overlaps or touches), extend the last one's end to the maximum of the two ends; otherwise start a new merged interval.

Worked example, settlement cutoff windows in minutes after midnight (`14:00 = 840`):

```text
input:  [840,930) scheme A   [900,960) scheme B   [960,1005) scheme C   [540,600) morning   [1080,1080) empty
sorted: [540,600) [840,930) [900,960) [960,1005) [1080,1080)

[540,600)   -> out = [540,600)
[840,930)   840 > 600, new    -> out = [540,600) [840,930)
[900,960)   900 <= 930, extend end to max(930,960)=960 -> [840,960)
[960,1005)  960 <= 960, touches, extend to 1005        -> [840,1005)
[1080,1080) empty, skipped

merged: [540,600) [840,1005)     coverage = 60 + 165 = 225 minutes
```

The output invariant: starts are strictly increasing and `out[i].end < out[i+1].start` (strictly less, because touching ones were merged). Cost: `O(n log n)` for the sort, `O(n)` for the walk. The sort is the whole cost, and it is also why the output is deterministic regardless of input order.

Two derived operations fall out of merging. **Coverage** is the sum of merged lengths. **Gaps** within a range: walk the merged list with a cursor starting at the range's start; each merged interval that begins after the cursor leaves a gap. For the windows above, the gaps in `[08:00, 19:00)` are `[480,540)`, `[600,840)`, and `[1005,1140)`: the times when no settlement window is open.

### Sweep line

A sweep line turns a question about intervals into a question about sorted events. Each interval contributes two events: `(start, +1)` and `(end, -1)`. Sort the events by time. Walk them with a running count; the count after each event is the number of intervals open at that instant, and its maximum is the maximum overlap.

The one subtle point is the order of events at the same timestamp. With half-open intervals, an interval ending at `t` is *not* open at `t`, and one starting at `t` *is*. So at equal times, `-1` events must be processed before `+1` events, or the count will briefly include both and report an overlap that never existed. This is the sort key `(time, delta)`, where `-1 < +1` does the work. Get it wrong and `[0,10), [10,20)` reports a peak of 2.

```text
windows: [540,600) [840,930) [900,960) [960,1005)
events sorted by (time, delta):
  540 +1 -> 1
  600 -1 -> 0
  840 +1 -> 1
  900 +1 -> 2     peak = 2, first reached at 900 (15:00)
  930 -1 -> 1
  960 -1 -> 0     the -1 at 960 comes before the +1 at 960
  960 +1 -> 1
 1005 -1 -> 0
```

Weight the events and the same sweep answers the risk question. A hold contributes `(start, +amount)` and `(end, -amount)`; the running total is the open exposure, and its maximum is the peak exposure. Six holds on one card, amounts in USD, times in minutes:

```text
hotel      250.00  [540,1020)      car-rental 500.00  [660,900)
fuel       125.00  [630,690)       parking     15.00  [780,840)
restaurant  80.00  [720,780)       coffee       6.00  [1020,1035)

 540 +hotel      250.00   1 open
 630 +fuel       375.00   2
 660 +car        875.00   3        <- peak, 11:00
 690 -fuel       750.00   2
 720 +restaurant 830.00   3
 780 -restaurant 750.00   2        release first...
 780 +parking    765.00   3        ...then reserve, at the same instant
 840 -parking    750.00   2
 900 -car        250.00   1
1020 -hotel        0.00   0        hotel releases at 17:00...
1020 +coffee       6.00   1        ...so the coffee is approved against a clear card
1035 -coffee       0.00   0
```

Peak exposure 875.00 USD at 11:00 with three holds open. The minute-sampling report would find this one (it lasts 30 minutes) but would miss a peak lasting 20 seconds; the sweep finds every peak because it visits every instant at which the total changes, and nothing changes between events.

The sweep line is `O(n log n)` for the sort and `O(n)` for the walk, independent of how long the timeline is or how fine its resolution. It generalizes: with a balanced tree instead of a counter it answers "which intervals are open" at each event (Shamos and Hoey, 1976, for segment intersection); with a segment tree (Phase 3, Lesson 10) it answers range queries on the counts.

### Interval scheduling

Given intervals that each need the same single resource (a maintenance window, a meeting room, a settlement slot on one connection), choose the largest set that are pairwise non-overlapping. Several greedy rules suggest themselves: earliest start, shortest, fewest conflicts. All three are wrong. **Earliest end** is right.

The rule: sort by end time; take the first; then repeatedly take the next interval whose start is at or after the end of the last one taken.

```text
slots sorted by end: [1,4) [3,5) [0,6) [5,7) [3,9) [5,9) [6,10) [8,11) [8,12) [2,14) [12,16)

take [1,4)      lastEnd = 4
[3,5)  3 < 4   skip
[0,6)  0 < 4   skip
take [5,7)      lastEnd = 7
[3,9) [5,9) [6,10)   skip
take [8,11)     lastEnd = 11
[8,12) [2,14)  skip
take [12,16)    lastEnd = 16

chosen: [1,4) [5,7) [8,11) [12,16)      4 slots
```

The exchange argument from Lesson 07, applied here. Let the greedy choice be `g1, g2, ..., gk` and any optimal solution be `o1, o2, ..., om` with `m >= k`, both sorted by end. Claim: `end(gi) <= end(oi)` for every `i <= k`. For `i = 1` it holds because `g1` has the earliest end of all. Assume it holds for `i - 1`. Then `oi` starts at or after `end(o(i-1)) >= end(g(i-1))`, so `oi` was available to the greedy algorithm when it chose `gi`, and greedy chose the earliest-ending available interval, so `end(gi) <= end(oi)`. Now if `m > k`, then `o(k+1)` starts at or after `end(ok) >= end(gk)`, so it was available after greedy's last choice and greedy would have taken it. Contradiction; `m = k`.

Why the other rules fail, in one counterexample each: earliest start picks `[0,100)` and loses `[1,2), [3,4)`; shortest picks a tiny interval straddling two long disjoint ones; fewest conflicts fails on a slightly larger construction that Kleinberg and Tardos draw in Figure 4.1. The tests include the earliest-start counterexample and a brute-force check over all subsets for 100 random instances.

Half-open intervals matter here too: `start >= lastEnd` accepts an interval that begins exactly when the previous one ends, so `[0,5)` and `[5,10)` can both be scheduled, which is what a maintenance window that ends at 17:00 followed by one that starts at 17:00 should mean.

### Interval partitioning: the sweep and the greedy meet

The sibling problem to scheduling is partitioning: every interval must be hosted, and you want the minimum number of identical resources (connections to a settlement bank, meeting rooms, worker threads). The answer is the maximum overlap from the sweep line, and the proof is a matching pair of bounds.

Lower bound: if `d` intervals contain a single instant, no assignment to fewer than `d` resources avoids a conflict at that instant. Upper bound: sort by start, and assign each interval to any resource whose last interval ended at or before this one's start, opening a new resource only when none is free. When the greedy opens its `k`-th resource, the current interval overlaps the last interval on each of the `k - 1` existing resources, and all of those started earlier and have not ended, so `k` intervals contain the current start instant: `k <= d`. The greedy never uses more than `d`, and `d` is necessary, so `d` is exact.

On the maintenance slots from the scheduling example:

```text
sorted by start: [0,6) [1,4) [2,14) [3,5) [3,9) [5,7) [5,9) [6,10) [8,11) [8,12) [12,16)

[0,6)  -> resource 1
[1,4)  -> resource 2      (1 is busy until 6)
[2,14) -> resource 3
[3,5)  -> resource 4      (2 is busy until 4)
[3,9)  -> resource 5
[5,7)  -> resource 2      (free since 4; the first free one in order)
[5,9)  -> resource 4      (free since 5)
[6,10) -> resource 1      (free since 6)
[8,11) -> resource 2      (free since 7)
[8,12) -> resource 6      (1 busy until 10, 2 until 11, 3 until 14, 4 and 5 until 9: none free)
[12,16) -> resource 1     (free since 10)
```

Check the hand assignment against the sweep: at instant 8 the open slots are `[2,14)`, `[3,9)`, `[5,9)`, `[6,10)`, `[8,11)`, `[8,12)`: six. The sweep says six resources are necessary at 8, and the greedy opened its sixth exactly there. The sweep gives the number in one pass; the greedy gives the actual assignment. With a min-heap of resources keyed by their current end time, the greedy runs in `O(n log n)`, and "is any resource free" is one `peek`.

### What these tools do not do

Merging loses identity: after merging you know when *something* was held, not which hold. The `HoldBook` keeps the holds and merges only their windows for the `busyPeriods` view. The sweep line reports the peak and the instant, not which intervals were open at the peak; recording that needs an extra pass or a set maintained alongside the counter (Exercise 2). Interval scheduling assumes one resource and equal value per interval; with weights (each slot is worth a different amount) the greedy rule fails and the problem becomes weighted interval scheduling, a dynamic program over intervals sorted by end with a binary search for the latest compatible predecessor (Lesson 06 plus Lesson 02). With `k` identical resources it becomes interval partitioning, solved by a different greedy (sort by start, assign to any free resource), and the minimum number of resources needed is exactly the maximum overlap from the sweep line. And none of this handles intervals that arrive one at a time with queries interleaved; that is the interval tree or segment tree of Phase 3.

## Build It

The code is in `code/java/Main.java`: `Interval`, `Intervals` (merge, coverage, gaps, maxOverlap, maxNonOverlapping, stabbing), and the payments layer `Money`, `Hold`, `HoldBook`, `PeakExposure`.

### Step 1: The interval type

```java
record Interval(long start, long end) implements Comparable<Interval> {
    Interval {
        if (end < start) throw new IllegalArgumentException("end " + end + " is before start " + start);
    }

    long length() { return end - start; }
    boolean isEmpty() { return end == start; }
    boolean overlaps(Interval o) { return start < o.end && o.start < end; }
    boolean overlapsOrTouches(Interval o) { return start <= o.end && o.start <= end; }
    boolean contains(long t) { return start <= t && t < end; }

    @Override public int compareTo(Interval o) {
        int c = Long.compare(start, o.start);
        return c != 0 ? c : Long.compare(end, o.end);
    }
}
```

The validation allows `end == start` (empty) and rejects `end < start`. `overlaps` uses strict inequalities and `overlapsOrTouches` uses `<=`; the difference between them is the entire content of the half-open discussion. `contains` is `start <= t < end`. The natural order is by start then end, which is what `merge` sorts by; a sort by end alone is used by scheduling and is passed as an explicit comparator there.

**Predict:** `new Interval(0, 10).overlaps(new Interval(10, 20))` and `.overlapsOrTouches(...)` on the same pair: what does each return?

### Step 2: Merge

```java
static List<Interval> merge(List<Interval> input) {
    List<Interval> sorted = new ArrayList<>(input);
    Collections.sort(sorted);
    List<Interval> out = new ArrayList<>();
    for (Interval iv : sorted) {
        if (iv.isEmpty()) continue;
        if (out.isEmpty() || !out.get(out.size() - 1).overlapsOrTouches(iv)) {
            out.add(iv);
        } else {
            Interval last = out.remove(out.size() - 1);
            out.add(new Interval(last.start(), Math.max(last.end(), iv.end())));
        }
    }
    return out;
}
```

The copy before sorting is what keeps the method pure; callers' lists are not reordered under them. `Math.max` on the end matters when a later-starting interval is entirely inside the last merged one (`[0,100)` then `[10,20)`): extending to `iv.end()` unconditionally would shrink the merged interval to `[0,20)`. The test "contained interval absorbed" exists because that bug ships regularly.

**Predict:** what does `merge` return for `[5,5), [7,7)`, and why is that the right answer rather than two zero-length intervals?

### Step 3: Gaps

```java
static List<Interval> gaps(List<Interval> input, Interval range) {
    List<Interval> out = new ArrayList<>();
    long cursor = range.start();
    for (Interval iv : merge(input)) {
        if (iv.end() <= range.start()) continue;
        if (iv.start() >= range.end()) break;
        if (iv.start() > cursor) out.add(new Interval(cursor, iv.start()));
        cursor = Math.max(cursor, iv.end());
    }
    if (cursor < range.end()) out.add(new Interval(cursor, range.end()));
    return out;
}
```

The cursor is the earliest instant not yet known to be covered. Merged intervals entirely before the range are skipped, one entirely after ends the loop, and each one in between either leaves a gap before it or not. The final check closes the last gap up to the range's end. Because the input is merged first, no two merged intervals overlap and the cursor logic never has to handle an interval that starts before the cursor and ends after it, except via the `Math.max`, which handles a merged interval that started before the range.

### Step 4: The sweep

```java
static Peak maxOverlap(List<Interval> input) {
    List<long[]> events = new ArrayList<>(input.size() * 2);
    for (Interval iv : input) {
        if (iv.isEmpty()) continue;
        events.add(new long[] {iv.start(), +1});
        events.add(new long[] {iv.end(), -1});
    }
    events.sort(Comparator.<long[]>comparingLong(e -> e[0]).thenComparingLong(e -> e[1]));
    int open = 0, best = 0;
    long bestAt = input.isEmpty() ? 0 : Long.MIN_VALUE;
    for (long[] e : events) {
        open += (int) e[1];
        if (open > best) {
            best = open;
            bestAt = e[0];
        }
    }
    return new Peak(best, best == 0 ? 0 : bestAt);
}
```

The comparator is the lesson: `(time, delta)` with `-1` sorting before `+1`. The `open > best` is strict so the *first* instant the peak is reached is reported, which is deterministic and is what a risk analyst wants ("when did it first get this bad"). Empty intervals are skipped so a `[5,5)` placeholder does not add and remove in the same instant and distort nothing.

**Predict:** change `thenComparingLong(e -> e[1])` to `thenComparingLong(e -> -e[1])`. What does `maxOverlap` return for `[0,10), [10,20)`?

### Step 5: Earliest-finish scheduling

```java
static List<Interval> maxNonOverlapping(List<Interval> input) {
    List<Interval> byEnd = new ArrayList<>(input);
    byEnd.sort(Comparator.comparingLong(Interval::end).thenComparingLong(Interval::start));
    List<Interval> chosen = new ArrayList<>();
    long lastEnd = Long.MIN_VALUE;
    for (Interval iv : byEnd) {
        if (iv.isEmpty()) continue;
        if (iv.start() >= lastEnd) {
            chosen.add(iv);
            lastEnd = iv.end();
        }
    }
    return chosen;
}
```

Eight lines once the proof is done. The secondary sort key on start makes ties on end deterministic; without it, two intervals ending at the same time would be ordered by the input and the chosen set could differ between runs with the same multiset of intervals. `>=` not `>`: touching is allowed.

**Predict:** on `[0,100), [1,2), [3,4)`, what does the earliest-end rule choose, and what would an earliest-start rule choose?

### Step 6: Weighted sweep for exposure

```java
PeakExposure peakExposure() {
    List<long[]> events = new ArrayList<>(holds.size() * 2);
    for (int i = 0; i < holds.size(); i++) {
        Hold h = holds.get(i);
        if (h.window().isEmpty()) continue;
        events.add(new long[] {h.window().start(), +1, i});
        events.add(new long[] {h.window().end(), -1, i});
    }
    events.sort(Comparator.<long[]>comparingLong(e -> e[0]).thenComparingLong(e -> e[1]));
    Money open = Money.zero(currency), best = Money.zero(currency);
    long bestAt = 0;
    int openCount = 0, bestCount = 0;
    for (long[] e : events) {
        Money amt = holds.get((int) e[2]).amount();
        open = e[1] > 0 ? open.plus(amt) : open.minus(amt);
        openCount += (int) e[1];
        if (open.compareTo(best) > 0) { best = open; bestAt = e[0]; bestCount = openCount; }
    }
    return new PeakExposure(best, bestAt, bestCount);
}
```

Same sweep, with the event carrying the index of its hold so the amount can be looked up, and the counter replaced by a `Money` running total. `Money.plus` and `minus` are checked arithmetic in one currency, so a hold in EUR added to a USD book fails at `add`, and a running total that overflowed `long` would throw rather than wrap to a negative exposure. The `exposureAt(t)` method is the `O(n)` point query; the test asserts `exposureAt(peak.at) == peak.amount`, tying the sweep's answer to the direct computation.

### Step 7: Run it

```bash
cd phases/02-algorithms/13-interval-problems/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Settlement windows: merge overlapping cutoffs ==
input:  [[840, 930), [900, 960), [960, 1005), [540, 600), [1080, 1080)]
merged: [[540, 600), [840, 1005)]
coverage: 225 minutes; gaps in 08:00-19:00: [[480, 540), [600, 840), [1005, 1140)]

== Max overlap: how many windows are open at once ==
peak 2 windows open, first reached at minute 900 (15:00)

== Interval scheduling: most non-overlapping maintenance slots ==
chosen by earliest end: [[1, 4), [5, 7), [8, 11), [12, 16)]

== Authorization holds on one card: exposure over the day ==
exposure at 12:30 = 830.00 USD
exposure at 17:00 = 6.00 USD  (hotel released at 17:00, coffee starts: half-open)
peak exposure 875.00 USD with 3 holds open, first at minute 660
busy periods: [[540, 1035)]
expired by 14:00: [fuel, restaurant, parking]
```

The 46 tests cover the half-open predicates on touching intervals, the hand-traced merge, contained and unsorted input, coverage and gaps including the empty and out-of-range cases, the event-order rule for touching intervals in the sweep, the textbook scheduling instance and the earliest-start counterexample, a brute-force cross-check of greedy size, sweep peak, and coverage on 100 seeded random instances, and the hold book's exposure at the 17:00 boundary.

### In Python

```python
events: list[tuple[int, int]] = []
for iv in input_:
    if iv.is_empty():
        continue
    events.append((iv.start, +1))
    events.append((iv.end, -1))
events.sort(key=lambda e: (e[0], e[1]))  # -1 sorts before +1
```

`Interval`, `Intervals`, `Hold`, `HoldBook`, `Peak`, and `PeakExposure` keep their names and algorithms; three things differ, all in `code/python/main.py`:

- **Ordering comes from the dataclass.** `Interval` is `@dataclass(frozen=True, order=True)` with fields `(start, end)`, so `sorted(intervals)` uses exactly the start-then-end order of the Java `compareTo` (which is also provided as `compare_to`). Events are tuples, and `sort(key=(time, delta))` is stable like `List.sort`, so the hold events that tie on both keep insertion order and the peak is reported at the same instant with the same count.
- **`Money` guards by hand.** Python integers do not overflow, so `plus`/`minus` raise `ArithmeticError` outside the signed 64-bit range where `Math.addExact` would throw. `Money` also rejects a `float` (or `bool`) amount at construction with `TypeError`, as Phase 10, Lesson 01's Python port does; Java's `long` field makes that mistake a compile error, Python needs the runtime check, and one extra test pins it.
- **Half-open is spelled `start <= t < end`.** Chained comparison makes `contains` read like the definition. The `-1 before +1` rule is the second element of the sort key; changing `-1` to `1` and `+1` to `0` (a common "make it a boolean" refactor) silently counts touching windows as overlapping, which the "touching intervals count as 1" test catches.

```bash
cd phases/02-algorithms/13-interval-problems/code/python
python3 main.py
python3 -m unittest -v
```

The demo output is identical to the Java run; the suite covers the Java's 46 checks plus the float guard. The test file carries a port of `java.util.Random` so the 100 random instances match.

### In Go

```go
// event is one endpoint of an interval in a sweep: +1 at a start, -1 at an end.
type event struct {
    time  int64
    delta int
    idx   int
}

// -1 before +1 at equal times: an interval ending at t does not overlap one starting at t.
func byTimeThenDelta(a, b event) int {
    return cmp.Or(cmp.Compare(a.time, b.time), cmp.Compare(a.delta, b.delta))
}
...
slices.SortStableFunc(events, byTimeThenDelta)
```

`Interval`, `Peak`, `Hold`, `HoldBook`, and `PeakExposure` keep their names and algorithms; three things differ, all in `code/go/main.go`:

- **Ordering is a function, and the event sort is stable on purpose.** `Interval.Compare` is the start-then-end order of the Java `compareTo`, passed as a method expression to `slices.SortFunc(sorted, Interval.Compare)`. Events are a small struct rather than a `long[]`, and `byTimeThenDelta` is the two-key comparator; `MaxOverlap` and `PeakExposure` sort with `slices.SortStableFunc` because Java's `List.sort` is stable and two holds that start at the same instant must be processed in insertion order for `HoldsOpen` to match. Changing `-1`/`+1` to a boolean silently counts touching windows as overlapping; the "touching intervals count as 1" test catches it.
- **`Money` arithmetic is checked.** Go's `int64` wraps like Java's `long`, so `Plus` and `Minus` in `money.go` go through `addChecked`/`subChecked` and return `ErrOverflow` where `Math.addExact` throws. That makes `Coverage`, `ExposureAt`, and `PeakExposure` return `(T, error)`; a test sums an interval of length `MaxInt64` with another to see `Coverage` report rather than wrap.
- **Errors are values.** `NewInterval`, `NewHold`, and `HoldBook.Add` return errors with the sentinels `ErrEndBeforeStart`, `ErrNonPositive`, and `ErrWrongBook`; `NewCurrency` rejects a lowercase code as the Java compact constructor does. Structs compare with `==`, so `MaxOverlap(windows) == Peak{2, 900}` reads like the Java record equality.

The test file replays `java.util.Random(13)` through `javarandom.go`, so the 100 random instances are the ones the Java test checks.

```bash
cd phases/02-algorithms/13-interval-problems/code/go
go run .
go test ./...
```

The demo output is identical to the Java run; the suite covers the Java's 46 checks plus 4 for the overflow guards and the currency-code rule.

## Use It

**Databases.** PostgreSQL has first-class range types (`int8range`, `tstzrange`) with half-open as the default bound style `[)`, operators `&&` (overlaps), `-|-` (adjacent), `+` (union, only if overlapping or adjacent), and GiST indexes that answer "which rows overlap this range" in logarithmic time. An `EXCLUDE USING gist (card_id WITH =, hold_window WITH &&)` constraint rejects overlapping holds on the same card at insert time, which is the interval-scheduling feasibility check as a constraint. The documentation's own example for exclusion constraints is room bookings.

**Standard libraries.** Java has no interval type; `java.time` has `Duration` and `Instant` but not ranges, and `Range` lives in Guava (`Range.closedOpen`, `RangeSet` with `add` performing merge, `TreeRangeMap` for weighted overlap). Go's standard library has none either; the community uses `google/btree` or writes the twelve lines above. Python has none in the standard library; `portion` and `intervaltree` are the common packages.

**Payments.** Card networks define the authorization hold rules this lesson models: Visa and Mastercard specify the maximum time an authorization may remain outstanding by merchant category, after which the issuer must release it; these are the `expires_at` values. Issuers compute "open to buy" as limit minus balance minus open authorizations, exactly `exposureAt(now)`. Settlement cutoff windows across schemes and currencies are merged to compute operational coverage and gaps, which is the first demo. Dispute and chargeback timelines are intervals with deadlines; "can we still file" is a `contains(now)`.

**Infrastructure.** Calendar systems compute free/busy by merging; meeting-room assignment is interval partitioning; Kubernetes' maintenance windows, CDN cache TTLs, and TLS certificate validity (`notBefore`, `notAfter`, which X.509 defines as *inclusive* on both ends, a closed interval, and a classic source of boundary bugs) are all intervals.

## Ship It

`outputs/checklist-time-windows.md` is a review checklist for any code that stores or compares time windows: bound style, boundary tests, event ordering in sweeps, time zones and clock sources, and the questions to ask about expiry and release of holds.

## Exercises

1. **Reinforce.** Add `Intervals.intersectAll(List<Interval> a, List<Interval> b)` returning the intersection of two merged lists using a two-pointer walk (Phase 2, Lesson 03). Verify with coverage: `coverage(intersectAll(a, b)) == coverage(a) + coverage(b) - coverage(a + b)`.
2. **Apply.** Extend `peakExposure` to also return the ids of the holds open at the peak. Keep a `LinkedHashSet<String>` of open hold ids beside the running total and snapshot it when a new peak is set. Then write the query the risk team actually asks: for each hold, by how much did it contribute to exceeding a given threshold, i.e. the sum over the sweep of `max(0, open - threshold)` weighted by the time until the next event.
3. **Extend.** Weighted interval scheduling (Phase 2, Lesson 06 and Lesson 02): each maintenance slot has a value; choose the non-overlapping set of maximum total value. Sort by end, compute `p(i)` = the index of the last interval ending at or before `start(i)` by binary search, and fill `best[i] = max(best[i-1], value(i) + best[p(i)])`. Show on a three-interval instance that the unweighted greedy picks two slots worth 2 while the DP picks one slot worth 10.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Half-open interval | "start inclusive, end exclusive" | `[s, e)` contains `t` iff `s <= t < e`; adjacent intervals share no instant and leave no gap, and length is `e - s` |
| Overlap vs touch | "they meet" | Overlap: share at least one instant (`a.start < b.end && b.start < a.end`). Touch: `a.end == b.start`. Merging unites both; counting concurrency counts only overlap |
| Merge | "flatten the ranges" | Sort by start, extend or append; output is sorted and strictly disjoint, and its total length is the coverage of the input |
| Sweep line | "walk the timeline" | Convert each interval to a start and an end event, sort by `(time, delta)` with ends first, and process them in order with a running count or total |
| Maximum overlap | "peak concurrency" | The largest number (or weight) of intervals containing a single instant; also the minimum number of resources needed to host them all without conflict |
| Interval scheduling | "pick the most meetings" | Largest set of pairwise non-overlapping intervals; the earliest-end greedy is optimal by an exchange argument |
| Authorization hold | "pending charge" | Funds reserved on a card by an approved authorization, open from approval until capture or expiry; open holds reduce available credit |
| Exposure | "how much is tied up" | The sum of open hold amounts at an instant; peak exposure over a period is the weighted maximum overlap |

## Further Reading

- [Kleinberg and Tardos, *Algorithm Design*, Chapter 4.1 (Interval Scheduling and Interval Partitioning)](https://www.cs.princeton.edu/~wayne/kleinberg-tardos/) — the exchange-argument proof in this lesson is theirs, with the figure showing why earliest-start, shortest, and fewest-conflicts all fail.
- [Shamos and Hoey, "Geometric intersection problems", FOCS 1976](https://doi.org/10.1109/SFCS.1976.16) — the paper that introduced the sweep line (they call it a scan), for detecting whether any two of `n` segments intersect in `O(n log n)`.
- [PostgreSQL documentation: Range Types](https://www.postgresql.org/docs/current/rangetypes.html) — a production implementation of half-open ranges, overlap and adjacency operators, and exclusion constraints; read the section on inclusive and exclusive bounds.
- [Dijkstra, "Why numbering should start at zero", EWD831, 1982](https://www.cs.utexas.edu/users/EWD/transcriptions/EWD08xx/EWD831.html) — the two-page argument for half-open ranges, which is the same argument this lesson makes for timelines.
- [Visa Core Rules and Visa Product and Service Rules](https://www.visa.com/content/dam/VCOM/download/about-visa/visa-rules-public.pdf) — the public rulebook; search for authorization validity periods by merchant category to see where the `expires_at` values in a real hold book come from.
- [RFC 5280, Section 4.1.2.5 (Validity)](https://www.rfc-editor.org/rfc/rfc5280#section-4.1.2.5) — X.509 certificate validity is defined as a *closed* interval (`notBefore` and `notAfter` both inclusive), a documented example of the other convention and the boundary bugs it produces.
