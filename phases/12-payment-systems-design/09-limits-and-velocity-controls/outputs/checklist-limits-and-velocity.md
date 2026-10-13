---
name: checklist-limits-and-velocity
description: Design-review checklist for any new velocity limit, amount limit, exposure ceiling, or rate control on a payment path
phase: 12
lesson: 9
tags: [limits, velocity, exposure, fraud, rate-limiting]
---

# Limits and Velocity Controls Review Checklist

Use this when a product manager asks for "a limit on that", when a new merchant tier or override is configured, and again when support or the fraud team asks why a customer was locked out. One unchecked box is a reason to request changes.

## What kind of limit

- [ ] The limit is classified as count velocity, amount velocity, or exposure, and the reason is written down.
- [ ] Velocity limits name a window length; exposure limits name no window and instead name their release paths.
- [ ] The threat or loss it bounds is stated (card testing, new-seller bust-out, a single bad actor's maximum loss).
- [ ] It is not a fraud decision for one merchant in disguise; those go to the risk engine with an owner and an expiry.

## Scope key

- [ ] The scope key is explicit: card fingerprint, merchant id, card at merchant, or global.
- [ ] Card scopes use a keyed fingerprint of the PAN, never the PAN or an unkeyed hash.
- [ ] The memory cost is estimated: number of distinct keys times entries per window.

## Window semantics

- [ ] The window slides; no calendar or midnight reset an attacker can straddle.
- [ ] The interval is half-open `(now - W, now]`, and a test pins the exact instant an entry leaves.
- [ ] Time comes from an injected clock, so boundary tests advance a number instead of sleeping.
- [ ] If an approximate (bucketed) window is used, its error bound is measured and accepted.

## What is counted

- [ ] Attempts are counted, not only approvals, so a card-testing run made of declines is seen.
- [ ] Attempts denied by this engine are never recorded in any window or exposure total.
- [ ] Allowed and REVIEW attempts are recorded in every applicable window.
- [ ] Money limits judge only their own currency; no hidden FX conversion.
- [ ] Amounts are integer minor units, never floats.

## Hard versus soft

- [ ] Each limit is marked hard (deny) or soft (allow and flag), with the reason.
- [ ] Hard is reserved for unbounded downside (card testing, merchant exposure).
- [ ] What REVIEW triggers is defined by the caller: step-up authentication, delayed capture, or manual queue.
- [ ] The engine evaluates every limit before deciding; outcome is the worst severity breached.

## Hierarchy and overrides

- [ ] Every limit exists as a platform default; overrides replace a default by id and never add a new limit.
- [ ] Overrides record who approved them, why, and a review date.
- [ ] Card-level limits are not relaxed by a merchant-level override.

## Exposure

- [ ] Every path that resolves an authorization calls release: capture, void, and authorization expiry.
- [ ] An expiry job releases authorizations older than the network's expiry for the merchant category.
- [ ] Release of an unknown or already-released attempt id is a no-op, so duplicate webhooks cannot drive exposure negative.
- [ ] A dashboard shows open exposure per merchant against its ceiling.

## Atomicity

- [ ] Check-and-record is one atomic step: a lock in-process, a Lua script or MULTI/EXEC in Redis, or a database transaction.
- [ ] A concurrency test fires many simultaneous attempts at a limit of N and asserts exactly N are admitted.
- [ ] The behaviour when the limit store is unavailable (fail open or fail closed) is decided per limit and documented.

## The denial

- [ ] Every breached limit appears in the response: limit id, scope key, observed value, projected value, threshold, window.
- [ ] Velocity denials carry the earliest clearing instant and set `Retry-After` from it; exposure denials carry none.
- [ ] The HTTP status is chosen deliberately (`429` for velocity, a business error for exposure).
- [ ] Support tooling can display the same reason object without reading logs.

## Rollout

- [ ] New limits run in shadow mode first, logging would-be denials against real traffic.
- [ ] The shadow-mode false-positive rate on known-good customers is reviewed before switching to enforce.
- [ ] Alerts fire on a sudden rise in denials per limit, which is either an attack or a misconfiguration.
