---
name: checklist-velocity-rule
description: Specification and review checklist for a velocity, rate-limit, or stream-window rule
phase: 2
lesson: 3
tags: [fraud, rate-limiting, sliding-window, review]
---

# Velocity Rule Checklist

Use when writing or reviewing a fraud velocity rule, an API rate limiter, or a stream-processing window. Every box is a decision that two implementations of the "same" rule have disagreed on in production.

## Window semantics

- [ ] Trailing (one window per event, `(t - w, t]`) or bucketed (fixed tumbling or hopping intervals)? Written down, with the reason.
- [ ] Boundary inclusivity stated and tested: an event exactly `w` old is in or out. Test both `w` and `w - 1 unit`.
- [ ] Time unit and resolution fixed (ms since epoch, UTC). No local time, no seconds-since-boot.
- [ ] Time is an input to the rule (event timestamp), never read from the wall clock inside the rule, so replays are deterministic.
- [ ] Out-of-order events: rejected, buffered until a watermark, or accepted with a documented error bound. Never silently appended.

## What counts

- [ ] Denied attempts count toward the window (they are the attack signal) or do not; stated and tested.
- [ ] Reversals, refunds, and voids: do they reduce the amount total, and does a reversed event still count toward the count limit?
- [ ] The key is explicit (card, account, device, IP, merchant) and the rule says what happens when the key is missing.
- [ ] Zero-amount and negative-amount events are handled, not assumed away.

## Aggregate and structure

- [ ] The aggregate supports O(1) removal (count, sum, frequency map) or uses the right structure when it does not (monotonic deque for max/min, two heaps for median).
- [ ] Amounts are `Money` in integer minor units with a currency; mixed currencies in one window are rejected or converted at a stated rate.
- [ ] Memory per key is bounded, or the rule documents why unbounded (exact sliding log) is acceptable for this key space.
- [ ] Order of checks when several limits are exceeded at once is specified (count before amount, etc.) and the decision code reflects it.

## Correctness evidence

- [ ] A brute-force oracle test (recompute the window from scratch) agrees with the incremental implementation on randomized event streams.
- [ ] A replay test runs a recorded day of events through the rule and produces byte-identical decisions on every run.
- [ ] Hot-key test: one key receiving 100x the normal rate does not change per-event latency.
- [ ] Clock-skew test: events from two sources with 500 ms skew produce the documented behaviour.

## Operations

- [ ] The rule's parameters (window, limits) are configuration with a change log, not literals.
- [ ] Fail-open or fail-closed on timeout is a deliberate, documented choice with an alert.
- [ ] A decision log records the window state (count, total, oldest timestamp) at decision time so an analyst can reconstruct why.
