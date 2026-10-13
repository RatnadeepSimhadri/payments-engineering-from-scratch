---
name: runbook-clock-skew
description: On-call runbook and code-review checklist for clock skew, NTP health, and choosing the right clock for timestamps, durations, and ordering
phase: 5
lesson: 4
tags: [clocks, ntp, hlc, runbook, on-call, review]
---

# Clock Skew Runbook

## When to open this

- Events appear out of order across services (a child event stamped before its parent).
- Negative durations in metrics or reports.
- Leases, locks, or tokens expiring early or never.
- A batch cutoff ran at the wrong moment on one host.
- Certificate or token validation failing with "not yet valid" or "expired" on one host only.

## Triage on the suspect host

| Step | Command (chrony) | Healthy | Act if |
|------|------------------|---------|--------|
| 1. Current offset and bound | `chronyc tracking` | `System time` offset under 1 ms in-cloud, under 10 ms on the public internet; `Root dispersion` small | Offset above threshold, or `Leap status : Not synchronised` |
| 2. Sources | `chronyc sources -v` | At least one `^*` (selected) source at stratum 1 to 3 with low `Reach` loss | Only `^?` or `^x` sources; stratum above 4; `Reach` far below 377 |
| 3. Recent corrections | `journalctl -u chronyd` | Small slews; no `System clock was stepped` lines | A step in the incident window: that is the discontinuity |
| 4. Compare hosts | `chronyc tracking` on two hosts, or an NTP query from a third | Offsets agree within the bound | Disagreement is the skew; record it |
| 5. Configuration | `/etc/chrony.conf` | Points at the provider's time service (`169.254.169.123` on AWS, `metadata.google.internal` on GCP) or the organisation's stratum-2 pool | A host pointed somewhere else, or at a single server |

## Alarms that should already exist

- [ ] `chronyc tracking` offset exported as a metric per host; alarm at 10 ms (in-cloud) or 50 ms (public internet).
- [ ] Stratum per host; alarm when above the expected value or when unsynchronised for more than 10 minutes.
- [ ] Count of clock steps per host; any step in production is an event to review.
- [ ] Application-level: count of causally related event pairs whose wall-clock stamps are reversed (child before parent), sampled.

## Code review: the two-clock rule

For every timestamp in the change, answer which clock it needs.

| The value is | Correct clock | Red flag |
|--------------|---------------|----------|
| A duration, timeout, deadline, lease expiry, rate-limit window, latency measurement | Monotonic (`System.nanoTime()`, `time.monotonic_ns()`, Go `time.Since`) | Subtracting two `currentTimeMillis()` or `Instant.now()` readings |
| A point in time stored, displayed, or sent to another system | Wall clock (`Instant.now()` via an injected `Clock`) | Reading the clock directly in business logic (untestable) |
| An ordering key for events that cross machines | HLC (packed 48 + 16 bits) or a sequence from a single writer | `ORDER BY created_at` across rows written by different hosts |
| A "give me everything since X" query key | HLC or wall clock, with the skew bound acknowledged in the query's semantics | Assuming the result set is exact at the boundary |

Review questions:

- [ ] Is every `Clock` or time source injected so a test can set and advance it?
- [ ] Does any code assume a duration is non-negative when it was computed from the wall clock?
- [ ] Does any code assume two hosts' wall clocks agree to better than 10 ms?
- [ ] Does any ordering, dedup window, or cutoff depend on `created_at` from more than one writer?
- [ ] If an HLC is used: is the drift bound enforced, and what happens to the node when it is exceeded?
- [ ] If a lease is involved: is expiry measured on the monotonic clock, and is the lease fenced by a token (Lesson 12) in case it is not?

## Deciding between clocks for a new feature

```text
Need to measure how long something took?            -> monotonic clock
Need a time a human or another system will read?    -> wall clock, injected
Need to order events written by different hosts?    -> HLC; never wall clock alone
Need to detect that two writes conflicted?          -> vector clock or a protocol, not any timestamp
Need transactions ordered by timestamp globally?    -> HLC plus uncertainty restarts, or TrueTime plus commit-wait
```

## Recovery after a skew incident

1. Fix the time source and let chrony slew; do not manually step a production clock unless the offset is above 128 ms and the services on the host tolerate a step.
2. Identify the window: from the first bad `tracking` sample to the correction.
3. For data ordered by wall clock written in the window: re-derive order from causal evidence (request ids, parent references, HLC if present) and record a documented correction; do not re-stamp the originals.
4. For leases or locks held across the window: check the fencing-token log for rejected writes; if there is no fencing token, audit every write by the former holder.
5. Add the host to the alarm set above if it was missing, and record the skew magnitude observed in the post-mortem.
