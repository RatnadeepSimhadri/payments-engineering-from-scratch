---
name: design-doc-timeout-service
description: Design-document template for any component that schedules, cancels, and fires large numbers of timeouts
phase: 3
lesson: 12
tags: [timers, timeouts, timing-wheel, heap, design-review]
---

# Timeout Service Design Document

Fill in every section before choosing a structure. The numbers decide; the preferences do not.

## 1. Workload

| Question | Answer | Why it matters |
|----------|--------|----------------|
| Peak pending timers | | Heap cost is O(log n) per op; wheel cost is independent of n |
| Schedules per second | | Dominant operation for request deadlines |
| Cancels per second, as a fraction of schedules | | Above ~50%, lazy heap cancellation multiplies memory |
| Expirations per second | | Usually the smallest number; the one the heap optimizes for |
| Minimum delay / maximum delay | | Sets tick and number of wheel levels |
| Required precision | | Below 1 ms: heap. Coarser: wheel is a candidate |
| Tolerable direction of error | never early / never late / either | Decides the rounding rule |

## 2. Structure decision

- [ ] Fewer than ~10,000 timers, or exact firing required: use the platform heap (`ScheduledThreadPoolExecutor`, `time.AfterFunc`, `asyncio.call_later`) and stop here.
- [ ] Millions of timers, coarse precision, cancel-heavy: hierarchical timing wheel.
  - Tick: ______ ms. Wheel size: ______. Levels needed for max delay: ______.
  - Rounding rule: deadlines rounded up to the tick (never early) unless the workload says otherwise.
  - Idle strategy: dedicated thread sleeping one tick, or a delay queue of non-empty buckets.
- [ ] Long delays handled by: cascading levels / rounds counter / rounding to coarse granularity (Linux 4.8+ style).

## 3. Clock

- Source: `System.nanoTime()` / `CLOCK_MONOTONIC` / injected `Clock` interface. Never wall-clock time.
- Behaviour on a large forward jump (process paused, VM migrated): fire everything due, in deadline-tick order.
- Behaviour on a backward jump: impossible with a monotonic clock; assert, do not handle.

## 4. Callback execution

- Callbacks run: inline on the timer thread / handed to an executor / enqueued as events.
- Maximum callback duration tolerated inline: ______ ms. Anything doing I/O is handed off.
- A callback that throws: logged and the timer thread continues; the entry is still marked fired.

## 5. Cancellation semantics

- `cancel()` after `fire()` returns false; the caller treats it as "lost the race" and does not compensate twice.
- Cancellation frees the entry immediately (wheel) or at expiry (heap with lazy cancel); state the memory consequence.
- Concurrent cancel from a different thread than the ticker: lock on the facade / lock-free flag plus unlink on the ticker thread.

## 6. Persistence and restart

- Timers are / are not durable. If they are: appended to a log or stored with an `expires_at` column; on restart, replay and fire anything already due.
- Exactly-once firing across restart requires an idempotent callback keyed by the timer id.
- A periodic sweep (every ______) catches anything the in-memory timer lost, as the slow path.

## 7. Failure modes to test before shipping

| Scenario | Expected behaviour | Test exists |
|----------|--------------------|-------------|
| Deadline exactly on a tick boundary | fires on that tick, not the next | [ ] |
| Deadline beyond the top level's interval | a new level is created; fires on time | [ ] |
| Clock advances 100 ticks in one poll | all due timers fire, in deadline-tick order | [ ] |
| Cancel racing with fire | exactly one of them returns true | [ ] |
| Two timers in the same tick | fire in insertion order; no one depends on sub-tick order | [ ] |
| Slow callback | later timers in the same tick are delayed, not lost; alert fires | [ ] |
| Restart with pending timers | replayed; already-due ones fire immediately once | [ ] |
| 10x the expected pending count | schedule and cancel latency unchanged (wheel) or within budget (heap) | [ ] |

## 8. Observability

- Metrics: pending count per level, fires per second, cancels per second, cascades per second, maximum lateness (fired time minus deadline), callback duration histogram.
- Alert when maximum lateness exceeds two ticks: the ticker thread is starved or a callback is blocking.
