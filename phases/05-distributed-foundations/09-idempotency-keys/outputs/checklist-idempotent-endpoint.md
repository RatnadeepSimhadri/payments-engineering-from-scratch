---
name: checklist-idempotent-endpoint
description: Pull-request review checklist for any POST endpoint that moves money or creates a resource and may be retried
phase: 5
lesson: 9
tags: [idempotency, payments, review, api-design, retries]
---

# Idempotent Endpoint Review Checklist

Use on every pull request that adds or changes a non-idempotent HTTP handler (a charge, a refund, a payout, a transfer, a resource creation). The question behind every box: "If the client retries this in the world where the first attempt succeeded but the response was lost, what stops the duplicate?"

## The key

- [ ] The endpoint accepts an idempotency key (header `Idempotency-Key`, or a documented body field) on every call that has a side effect.
- [ ] Keys are scoped per account or tenant in the store's lookup key, so two clients can use the same string without colliding.
- [ ] Key length and character set are validated (for example 1..255 characters, non-blank) and a bad key is a 400, not a silent fallback to "no key".
- [ ] Client documentation says to generate the key *before* the first attempt, persist it with the business record, and reuse it on every retry.
- [ ] The retention period (TTL) is documented in the API reference, with the consequence spelled out: after it, the same key executes again.

## The fingerprint

- [ ] The server computes a fingerprint from a canonical form of the effectful fields (amount in minor units, currency, destination, instrument, route). Field order in the wire body does not change it.
- [ ] The fingerprint excludes the key itself and the account id.
- [ ] Amounts enter the fingerprint as integer minor units plus currency, never as a formatted or floating-point string.
- [ ] A test pins the fingerprint of a known request so a change to the canonical form is a visible diff.

## The four outcomes

- [ ] `NEW`: reserve, execute once, store the response, return it.
- [ ] `REPLAY`: return the stored status and body without executing; mark the response (for example `Idempotent-Replayed: true`).
- [ ] `IN_PROGRESS`: return 409 without executing; documentation tells clients to wait and retry.
- [ ] `CONFLICT` (same key, different fingerprint): return 422 (or the provider's documented equivalent) without executing; this is a client bug and is logged as one.
- [ ] The decision about what to store is written down: responses of every status once execution began (the Stripe rule), nothing for validation failures or for 409/422 from the store itself.

## Concurrency

- [ ] The lookup and the reservation are one atomic step: a lock around both, `INSERT ... ON CONFLICT DO NOTHING` with a primary key on (account, key), `SET ... NX`, or a conditional put.
- [ ] A test runs N concurrent duplicates against a blocked downstream and asserts exactly one execution and N-1 in-progress rejections.
- [ ] The store is shared by every instance of the service (a database or cache), not per-process memory.

## Time limits

- [ ] The in-progress lease is longer than the longest honest execution of the handler, including downstream timeouts, so a slow live attempt is not duplicated by its own retry.
- [ ] The TTL starts at completion, not reservation.
- [ ] Expired entries are purged (or expire natively) and the purge is tested with an injected clock.

## The crash hazard

- [ ] The pull request states which of the following closes the gap between the side effect and its record, and why: (a) side effect and idempotency row written in one transaction; (b) the downstream call carries its own key derived from ours so it replays; (c) a recovery point written before the call, with reconciliation after.
- [ ] If none of the three applies, the PR says so explicitly and names the reconciliation process (Phase 10, Lesson 08) that will catch the duplicate.

## Client side

- [ ] The retry policy (Lesson 10) never retries a 422 and treats 409 as "poll or wait", not as a failure to re-send.
- [ ] Retries reuse the same key; the key is not generated inside the retry loop.
