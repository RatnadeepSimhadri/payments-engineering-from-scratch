---
name: checklist-time-windows
description: Review checklist for any code that stores, compares, merges, or sweeps time windows such as holds, cutoffs, validity periods, and schedules
phase: 2
lesson: 13
tags: [intervals, time, holds, authorization, review]
---

# Time Window Review Checklist

For holds, settlement windows, maintenance slots, validity periods, rate-limit windows, and anything else with a start and an end. One unchecked box is a question for the author.

## Representation

- [ ] Every window is half-open `[start, end)`; the end instant belongs to whatever starts there, not to the window that ends there.
- [ ] If an external spec mandates closed bounds (X.509 validity, some scheme rulebooks), the conversion to half-open happens once at the boundary and is commented with the spec reference.
- [ ] Instants are integers in a stated unit (epoch seconds, millis, minutes after midnight) with a stated clock source, never floats and never local-time strings.
- [ ] `end < start` is rejected at construction; `end == start` (empty) is either allowed and skipped by every algorithm, or rejected, and the choice is written down.
- [ ] Time zones: instants are UTC; a business-day window ("17:00 local cutoff") is converted to UTC instants on the day it applies, with DST transitions in the tests.

## Boundary tests exist

- [ ] `contains(start)` is true and `contains(end)` is false.
- [ ] Two touching windows do not overlap, do merge, and count as 1 in a concurrency sweep.
- [ ] A window entirely inside another is absorbed by merge (the `max(end)` test).
- [ ] An empty input, a single window, and a window outside the queried range are all covered.
- [ ] Any sweep orders `-1` before `+1` at equal timestamps, and a test with touching windows proves it.

## Algorithms

- [ ] Merging sorts a copy of the input; callers' collections are never reordered.
- [ ] Sweep results that pick an instant (peak time) use a strict comparison so the first such instant is reported deterministically.
- [ ] Interval selection uses earliest-end greedy for unweighted problems and the DP for weighted ones; the author can say which problem this is.
- [ ] Running totals of amounts use the `Money` type with checked arithmetic and one currency per book; a running count uses an `int` that cannot realistically overflow.
- [ ] Point queries ("what is open at t") that run on hot paths use an index (sorted ends, interval tree, database range index), not a scan of every window.

## Holds and exposure specifically

- [ ] Each hold's `expires_at` comes from the scheme or merchant-category rule, not a constant, and the rule source is cited.
- [ ] Available credit is `limit - posted - exposureAt(now)` with `now` from an injected clock, and the computation is the same code in the authorization path and the reporting path.
- [ ] Expired holds are released by a job that uses `end <= now`, not `end < now`, and the release is idempotent.
- [ ] Captures reduce or close the hold they reference; a capture without a matching hold is flagged, not silently added.
- [ ] Peak exposure and the holds open at the peak are computed by a sweep, reproducible from the stored holds, and reconcile with `exposureAt(peak.at)`.
