---
name: checklist-event-time-windows
description: Design-review checklist for any stream job that aggregates by time (settlement totals, velocity counts, SLA windows)
phase: 8
lesson: 8
tags: [streams, event-time, watermarks, late-data, review]
---

# Event-Time Window Review Checklist

Use in the design review of any job that groups events by time. Each unchecked box is a question to answer before the job owns a number anyone reconciles against.

## Which clock

- [ ] The job states explicitly whether windows are over event time or processing time, and the business question it answers is phrased in the same clock.
- [ ] Event time is taken from a field the producer stamped at the source, not from the broker's append timestamp or the consumer's poll time.
- [ ] Producer clock skew is bounded: event times more than a tolerance ahead of processing time are rejected or clamped before they reach the watermark generator.
- [ ] Window boundaries are half-open `[start, end)` and the assigner uses floor division, so no instant belongs to two windows or to none.

## The watermark

- [ ] The watermark strategy is named (bounded out-of-orderness, punctuated, source-provided) and the bound is written down with its justification: measured skew percentiles over at least a week including a failover.
- [ ] The watermark is derived from data and never from the wall clock, so an outage stalls windows instead of firing them early.
- [ ] Per-partition watermarks are combined by minimum, and idle sources are excluded from the minimum after a stated timeout.
- [ ] The watermark is monotonic: the implementation cannot lower it on a late event.

## Lateness policy

- [ ] Allowed lateness is a stated number with an owner, matched to how long downstream can accept a correction (settlement cutoff, alert validity, report amendment window).
- [ ] Window state is garbage-collected at `end + allowedLateness`, so memory is bounded by the lateness parameters rather than by uptime.
- [ ] Results carry a firing kind (`ON_TIME` or `LATE_UPDATE`) and the full corrected aggregate, so consumers overwrite by window key rather than add.
- [ ] Every consumer of the results has been checked for double-counting on `LATE_UPDATE`.

## The side output

- [ ] Events past bound plus lateness go to a side output, never a silent drop. The side-output record carries the event, its target window, the watermark at arrival, and a reason.
- [ ] The side output has a named consumer (reconciliation job, manual adjustment queue) and a retention policy.
- [ ] The side-output rate is a metric with an alert threshold; a rise means a source's skew changed and the bound is stale.

## Tests

- [ ] A test replays the canonical late event (happened before the window end, arrived after it) and asserts it lands in the correct window.
- [ ] A test asserts `ON_TIME` fires exactly once per window.
- [ ] A test asserts the accounting invariant: events seen equals events in open windows plus events in closed windows plus side-output size.
- [ ] A test with zero bound and zero lateness shows the degenerate behaviour, so the team knows what the parameters protect against.

## Sign-off

| Parameter | Value | Justification | Owner |
|-----------|-------|---------------|-------|
| Window size | | | |
| Watermark bound | | | |
| Allowed lateness | | | |
| Side-output consumer | | | |
