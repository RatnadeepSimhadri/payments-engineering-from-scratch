---
name: checklist-exactly-once-pipeline
description: Design and PR review checklist for any change that moves money across a service boundary; one section per seam where a crash or duplicate can land
phase: 14
lesson: 3
tags: [idempotency, outbox, dedupe, ledger, review]
---

# Exactly-Once Pipeline Review Checklist

Use in design review before code exists and again in PR review. For every seam, the three questions are the same: where does the dedupe record live, what is its key, and is it written in the same transaction as the effect it protects? An answer of "the bus handles it" to any of them is a reason to request changes.

## Seam 1: Client to API

- [ ] Every money-moving `POST` accepts a client-supplied idempotency key; the server never generates it on the client's behalf.
- [ ] The key maps to the stored response or resource; a retry returns the original result with a replay marker, not a fresh resource.
- [ ] A reused key with a different request fingerprint returns a conflict error. The fingerprint fields are listed in the design doc.
- [ ] The key lookup and the resource insert are protected by a unique index, so two concurrent handlers cannot both insert.
- [ ] Key TTL is documented and is longer than the longest client retry window, including queued retries after an outage.

## Seam 2: State change to event (outbox)

- [ ] The outbox row is written in the same database transaction as the state change and the local ledger entry. No "save then publish" code path exists, including in error handlers.
- [ ] The event id is derived deterministically from the resource (for example `evt-<paymentId>`), so every republish carries the same id.
- [ ] The event is self-contained: a consumer can act on it without reading back from the producer's database.
- [ ] A test asserts that a crash after the state-change commit leaves the outbox row present.

## Seam 3: Relay to bus

- [ ] The relay publishes first and marks the row second. The design doc states that the relay is at-least-once and names the consumer-side mechanism that absorbs duplicates.
- [ ] Unpublished rows are read in commit order; a restart resumes from the oldest unpublished row.
- [ ] Alerting exists for outbox rows older than a threshold (stuck relay), because a lost event is invisible to every other check.

## Seam 4: Bus to consumer

- [ ] The consumer tolerates duplicates, redelivery after its own crash, and arbitrary reordering across keys. If per-key ordering is required, the partition key is documented and tested.
- [ ] The consumer keeps an inbox of processed event ids in its own database.
- [ ] The inbox insert and the side effect are in one transaction. Inbox-then-effect in two transactions loses effects; effect-then-inbox in two transactions duplicates them.
- [ ] Inbox retention is at least as long as the bus's message retention plus the maximum redelivery delay.

## Seam 5: Consumer to ledger

- [ ] The ledger entry id equals the event id, so the inbox and the journal can be joined to prove the correspondence.
- [ ] A unique constraint on entry id exists as a last line of defence, and a test proves the inbox catches the duplicate *before* the constraint does.
- [ ] Entries are balanced, in integer minor units, in one currency (Phase 10, Lessons 01 and 03).

## Invariants checked in tests

- [ ] One resource per idempotency key, and the resource the client was told about is the stored one.
- [ ] No state change without its outbox row.
- [ ] Every accepted resource posted exactly once in each ledger; nothing else posted.
- [ ] Trial balance zero on every ledger. Documented as necessary, not sufficient.
- [ ] Sums conserved: clearing equals accepted total; payables equal accepted total; each customer charged exactly the sum of their distinct acknowledged requests.
- [ ] A seeded fault-injection run of at least a few thousand operations with every fault point armed passes, and a failing seed is printed so it can be replayed.
- [ ] Each defence can be disabled individually and the test names the invariant that breaks.
