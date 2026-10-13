---
name: checklist-money-api-review
description: Design-review checklist for any public or internal API endpoint that creates, moves, or reports money
phase: 12
lesson: 13
tags: [api-design, idempotency, pagination, versioning, errors]
---

# Money API Design Review

Use when reviewing a new endpoint, a new resource, or a change to an existing payments API. Bring the endpoint's specification; answer every item with a pointer to the spec or a test.

## Outcomes and recovery

- [ ] Every status code and transport failure the endpoint can produce is classified as applied, not applied, or unknown, and the documentation gives the client action for each.
- [ ] Every write requires an `Idempotency-Key`, scoped per merchant and endpoint, with documented retention, fingerprint mismatch (`422`), and in-progress (`409` + `Retry-After`) behaviour.
- [ ] Every created object accepts a client reference that is unique per merchant and can be looked up with a read.
- [ ] `429` and admission-control `503` are decided before any side effect, and a test proves it.
- [ ] The documentation's retry guidance says "with the same idempotency key and body", in those words.

## Amounts

- [ ] Every amount is an integer in minor units, paired with an ISO 4217 currency on the same object.
- [ ] Any amount that can exceed 2^53 minor units is a string.
- [ ] No existing field changes unit or scale; a new representation is a new field.

## Lists and change feeds

- [ ] List endpoints use cursor pagination on an immutable, unique key (`created`, `id`) and return `has_more`.
- [ ] State changes are emitted as events, delivered by webhook and readable from an ordered events endpoint.
- [ ] Webhook payloads identify the object and event type; consumers are told to fetch current state or reconcile from the events feed.

## Versioning

- [ ] The change is classified as additive or breaking using the money-specific list: new enum values, changed defaults, and changed retryability are breaking.
- [ ] Every enum in the response is documented as open or closed.
- [ ] Breaking changes ship behind a new date-pinned version with a transformation to every older pinned version, including webhook renderings.

## Errors, limits, and expansion

- [ ] Errors are `application/problem+json` with a stable `code`, a normalized `decline_code` where relevant, `retryable`, and a `request_id`.
- [ ] Every response, success or error, carries a request id.
- [ ] Reads and writes have separate rate-limit budgets, and idempotent replays are not starved.
- [ ] `expand[]` has a maximum depth and returns only fields the caller is authorized to read directly.
