---
name: interview-card-stream-windows
description: Interview card for the stream-processing round, built around a card-velocity rule: window shapes, their state costs, the boundary problem, and processing time versus event time
phase: 8
lesson: 7
tags: [stream-processing, windows, flink, kafka-streams, fraud, velocity, interview]
---

# Interview Card: Windows in Stream Processing

The prompt this card prepares for: *"Design the system that declines a card's fourth transaction within sixty seconds."* Variants: rate limiting an API key, detecting a retry storm, computing spend per shopping session.

## The thirty-second answer

A window assigner maps each record's timestamp to the set of windows it belongs to; the operator keeps one constant-size aggregate per (key, window); a trigger emits a window and drops its state when it is complete. Pick the window shape from the rule's wording: "per minute" is tumbling, "within any sixty seconds" is sliding or per-record, "per burst" is session. A velocity rule enforced in the authorization path needs the per-record variant consulted at arrival time, on event time if the result must survive replay.

## The three shapes

| Shape | A record lands in | State per key | Fails when |
|-------|-------------------|---------------|------------|
| Tumbling(size) | exactly one epoch-aligned window | 1 window | the span straddles a boundary (the boundary problem) |
| Sliding(size, slide) | size/slide windows | size/slide windows | slide is too small (state and duplicate alerts explode) |
| Session(gap) | the session within `gap`, else a new one | 1 per active burst | the gap is tuned for the wrong population |
| Per-record (deque) | the last `size` of time relative to this record | records in the window | the window must hold raw records, not an aggregate |

## The boundary problem, in one example

Four transactions at 0:50, 0:56, 1:03, 1:09. Tumbling(60 s) sees 2 and 2: no breach. Sliding(60 s, 10 s) sees all four in `[0:10, 1:10)` and four more windows: breach, reported five times. Per-record sees count 4 at 1:09 exactly: breach, once, in time to decline. Say the numbers; interviewers remember them.

## What the aggregate must be

Count, sum, min, max, or anything with an `add(record)` and a `merge(other)` that is associative and commutative. Sliding windows write each record into several aggregates; session windows merge aggregates when sessions merge. Exact percentiles and distinct counts do not merge; name the approximations (t-digest, HyperLogLog) if asked.

## Processing time versus event time

- Processing time: when the record reached the operator. Monotonic, nothing is ever late, trivial trigger ("time has passed the window end"), **not reproducible**: a replay lands in today's windows.
- Event time: when the transaction happened. Reproducible, but records arrive late and out of order; a watermark estimates completeness, allowed lateness bounds how long state is kept, and late records are dropped, update the result, or retract it.
- The rule of thumb: operational dashboards can use processing time; anything audited, replayed, or billed cannot.

## The five questions interviewers ask

1. **"Why not a cron job and a SQL query?"** Latency (the decision arrives after the authorization was already approved) and the boundary problem (calendar minutes are tumbling windows).
2. **"What is the state size?"** Keys with activity times windows per record times aggregate size. Give the sliding multiplier `size/slide` and say what a 1-second slide would do.
3. **"What happens on restart?"** Open windows are in memory; without checkpointing they are lost and the rule is blind for one window length. With a changelog or checkpointed state, the operator resumes; name Lesson 06's projector pattern.
4. **"How do you avoid alerting five times for one burst?"** Suppress per key until the count drops below the threshold, or use the per-record variant that fires once at the record that crosses it.
5. **"The merchant's clock is wrong / the message arrived two minutes late."** Event time with watermarks and allowed lateness; explain the trade between waiting longer (complete results, more state, later decisions) and emitting early (fast, revisable).

## Traps to avoid saying

- "A tumbling window every minute is fine for a per-minute rule." It is fine for a per-calendar-minute report; it has a hole for an any-sixty-seconds rule.
- "Make the slide one second to be safe." That multiplies state by sixty and reports every breach sixty times.
- "Windows are emitted when the last record arrives." Nothing knows a record is the last; a trigger (timer or watermark) decides completeness.
- "Keep the records so we can compute anything later." That turns O(1) state per window into O(records) and is how windowed jobs run out of memory.
