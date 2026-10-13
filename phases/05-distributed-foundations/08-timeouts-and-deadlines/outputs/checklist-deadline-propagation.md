---
name: checklist-deadline-propagation
description: Design-review and postmortem checklist for any service-to-service call path that must finish inside a caller's budget
phase: 5
lesson: 8
tags: [timeouts, deadlines, rpc, retries, hedging, review]
---

# Deadline Propagation Review Checklist

Use for every new RPC path, every change to a timeout value, and every postmortem whose timeline contains the word "timeout." One unchecked box is a reason to request changes.

## The edge

- [ ] Exactly one place sets the top-level timeout for a request, and the value is traceable to a user-facing or SLA requirement (page budget, API contract), not copied from another service.
- [ ] A budget table exists for the critical path: per-hop work at a high percentile (p95 or p99, not median) plus network hops, summed. The edge timeout is at least that sum or the path is redesigned.
- [ ] The edge distinguishes a fail-fast `DEADLINE_EXCEEDED` from the dependency (no side effect) from its own timer firing (side effect unknown), and logs and meters them separately.

## Every hop

- [ ] The incoming deadline is read from the request (`grpc-timeout`, framework context, explicit field), never assumed.
- [ ] Outgoing calls derive their deadline as `min(incoming deadline, now + local timeout)`. No outgoing call uses a fresh context with no deadline (`context.Background()`, a new HTTP client with a hard-coded timeout, `Context.root()`).
- [ ] The deadline crosses the wire as a remaining duration recomputed against the receiver's clock, not as an absolute timestamp.
- [ ] Remaining budget is clamped at zero before being handed to anything that interprets zero or negative as "no limit."

## Before expensive or irreversible work

- [ ] The service knows the expected cost of its own work (measured, at a high percentile) and compares it with the remaining budget before starting. Insufficient budget is refused with a fail-fast error.
- [ ] Any call that moves money, sends a message, or writes to an external system happens only after that check, and carries an idempotency key (Phase 5, Lesson 09) because the check shrinks the ambiguous window but cannot close it.
- [ ] Long-running work re-checks the deadline between phases and stops early when it has passed.

## Retries

- [ ] The retry loop reads the request deadline and gives each attempt `min(perAttemptTimeout, remaining)`.
- [ ] No attempt starts with less remaining budget than a useful attempt needs.
- [ ] Backoff sleeps are clipped to the remaining budget.
- [ ] Retries are only issued for operations that are idempotent or carry an idempotency key.

## Hedging

- [ ] Hedged requests are used only for idempotent reads.
- [ ] The hedge delay is set at a high percentile (p95 or above) of the primary's latency and the resulting extra load is measured and under an agreed ceiling.
- [ ] The losing request is cancelled where the transport allows it, and the extra load is visible on the replica's dashboards.

## Postmortem questions

- [ ] At what simulated or wall-clock time did the edge give up, and at what time did the last downstream side effect land? If the second is later than the first, which hop dropped the deadline?
- [ ] Did any retry attempt start after the edge deadline? Which loop?
- [ ] Did the customer-facing error say "failed" for an operation whose side effect had in fact completed?
