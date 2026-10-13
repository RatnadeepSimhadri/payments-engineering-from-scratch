---
name: checklist-idempotent-endpoint
description: Review checklist for any endpoint that creates, captures, refunds, or otherwise moves money
phase: 12
lesson: 1
tags: [idempotency, api-design, retries, http, payments]
---

# Idempotent Money-Moving Endpoint Checklist

Use this on every pull request that adds or changes a `POST` (or non-idempotent `PATCH`) with a financial side effect: create payment, capture, refund, payout, transfer. One unchecked box is a reason to request changes.

## Which endpoints

- [ ] Every write with an external side effect (processor, bank, ledger) requires an `Idempotency-Key` header and returns `400 idempotency_key_required` without it.
- [ ] Each money-moving sub-resource (`/capture`, `/refund`, `/cancel`) has its own key scope; a create key cannot be reused to capture.
- [ ] `GET` endpoints take no key, write no idempotency record, and return live state.

## Key and scope

- [ ] The key is client-generated and opaque; the server never parses it.
- [ ] Key grammar is enforced: maximum length (for example 255 bytes), no control characters, rejected with `400 idempotency_key_invalid`.
- [ ] The store key is `(authenticated caller, endpoint, key)`, built with a separator the key grammar forbids, or as a composite column.
- [ ] The caller comes from authentication, never from the request body.

## Fingerprint

- [ ] The fingerprint is SHA-256 over a canonical encoding: fixed field set, sorted names, amounts as integer minor units, currency as ISO code.
- [ ] Harmless re-serialization (field order, whitespace, `19.99` vs `19.990`) produces the same fingerprint, with a test proving it.
- [ ] Every field that changes the money (amount, currency, source, destination, capture flag) is in the fingerprint.
- [ ] A cross-language test vector pins the fingerprint if more than one service computes it.
- [ ] Same key, different fingerprint returns `422` and never proceeds with either body.

## Reserve and concurrency

- [ ] Check-and-insert is one atomic operation: unique index plus `INSERT ... ON CONFLICT`, compare-and-set, or a single lock.
- [ ] A duplicate that finds `IN_PROGRESS` within the lease returns `409` with `Retry-After`.
- [ ] Client SDKs treat `409` as "retry after the header's delay", not as a failure shown to the customer.
- [ ] A test fires two identical requests concurrently and asserts exactly one processor call.

## Crash recovery

- [ ] `IN_PROGRESS` records carry an `updatedAt` and a lease (for example 30 s) longer than the processor timeout.
- [ ] An expired lease lets a retry take over the record instead of returning `409` forever.
- [ ] The processor reference is derived deterministically from the scoped key, so a recovering caller can look it up.
- [ ] Recovery asks the processor whether the side effect happened before redoing it.
- [ ] A test kills the request between charge and store and asserts one charge after recovery.

## Stored responses

- [ ] Every final outcome is stored, including declines (`402`) and validation failures, and replayed byte-identical.
- [ ] Replays carry `Idempotent-Replayed: true`.
- [ ] Responses that mean "no decision reached" (`429`, `5xx` before the side effect) are not stored as final, so the client may retry for real.
- [ ] The completed record and the payment row commit in the same transaction.

## Retention

- [ ] TTL is documented to clients (for example 24 hours) and measured from creation, not last access.
- [ ] Expired keys are pruned by a job with a bounded batch size.
- [ ] Clients are told that `422` is not a safety net after the TTL.
- [ ] The number of `IN_PROGRESS` records per caller is bounded or monitored.

## Observability

- [ ] Metrics exist for replays, `409`s, `422`s, and recoveries, per caller.
- [ ] A spike in `422`s alerts the integration team: it means a client is reusing keys.
- [ ] Recoveries are logged with the scoped key, reference, and lookup result.

## Tests the pull request must contain

- [ ] Replay returns the stored status and body.
- [ ] Mismatched body returns `422`.
- [ ] Concurrent duplicate returns `409` and one processor call.
- [ ] Crash after charge recovers without a second charge.
- [ ] TTL expiry admits a new request under the same key.
- [ ] Two callers with the same key do not see each other's responses.
- [ ] A declined payment is replayed, not retried.
