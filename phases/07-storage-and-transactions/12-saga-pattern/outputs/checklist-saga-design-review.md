---
name: checklist-saga-design-review
description: Design-review checklist for any multi-service flow that moves money through a sequence of local transactions
phase: 7
lesson: 12
tags: [saga, compensation, idempotency, recovery, payments]
---

# Saga Design Review Checklist

Use when a design document says "we call service A, then service B" and at least one of those calls moves money, reserves inventory, or sends something a customer will see. One unchecked box is a question for the author, not a reason to block; two in the same section usually is.

## Step and compensation inventory

- [ ] Every step is listed with its service, its forward action, and its compensation, in a table.
- [ ] Every compensation is a forward action in its own right (refund, release, cancel, correction notice), not a "delete" or "rollback" of a committed row.
- [ ] Steps with no true inverse (emails, outbound wires, third-party calls) are identified, and their compensation is named explicitly (a second message, a recall request, a human task).
- [ ] The pivot step is identified: the step after which the saga retries forward until it succeeds instead of compensating. Steps after the pivot are retriable; steps before it are compensatable.
- [ ] No compensation can be refused for a business reason. If a compensation can be declined, the design is wrong at that step.

## Ordering

- [ ] Compensations run in strict reverse order of the steps that completed.
- [ ] The failed step itself is not compensated.
- [ ] Any step that depends on a previous step's output (a hold id, a reservation token) gets it from the saga's persisted state, not from memory.

## Idempotency

- [ ] Every forward call carries an idempotency key derived from the saga id (plus step name if a service is called more than once).
- [ ] Every compensation call carries the same key discipline.
- [ ] Each participant stores the key with the outcome and replays the outcome on a repeat; the orchestrator never keeps an in-memory "already called" flag as the only record.
- [ ] Every compensation is legal when its forward step never ran: it finds nothing to undo, records that it ran, and returns success.
- [ ] A test exists that invokes each step and each compensation twice and asserts that money, stock, or state moved exactly once.

## The saga log and the two-write rule

- [ ] A `STARTED` record is committed before each step or compensation is invoked, and a `COMPLETED` record after it returns.
- [ ] The log is append-only and durable (a table with an insert per record, or a WAL), not a status column overwritten in place.
- [ ] The first record carries everything needed to rebuild the request (the order, the amounts, the account ids) so recovery needs no other store.
- [ ] Step names in the log are treated as a stable contract; renaming a step comes with a migration of existing log records.

## Recovery

- [ ] A recovery job scans for sagas that are neither completed nor compensated and resumes each from its log.
- [ ] Recovery re-invokes any step that has `STARTED` without `COMPLETED`.
- [ ] Recovery is idempotent: running it twice, or on two instances at once, does not double-execute a step. State how concurrent recovery is prevented (lease, lock, single instance).
- [ ] A test injects a crash between a step's side effect and its `COMPLETED` record and asserts the final state after recovery.
- [ ] A test injects the same crash during a compensation.
- [ ] Transient compensation failures are retried with backoff; there is an alert for a saga that has been compensating for longer than N minutes.

## Isolation

- [ ] The document answers "what does a concurrent flow see between step k and step k+1?" for every k.
- [ ] Where a concurrent flow could overspend, overbook, or read a value that will be compensated, a semantic lock (hold, reservation, pending flag) makes the intent visible, and the document says who checks it.
- [ ] Every hold has an expiry, and the document says what happens to a saga whose hold expired before capture.
- [ ] Reports and dashboards that read intermediate states (balance with an in-flight capture) are listed and either exclude pending sagas or label them.

## Observability

- [ ] A saga id appears in every log line, metric label, and trace span emitted by every participant.
- [ ] There is a way to list every saga in `COMPENSATING` state right now, with its failed step and reason.
- [ ] There is a metric for compensations per step per hour; a sustained rise at one step is a participant bug, not a saga bug.

## Questions to ask the author

1. Pick the step most likely to fail. Walk the compensations for a failure there by hand, with the numbers from the design's own example.
2. Pick the step with the largest side effect. What does the world look like if the orchestrator dies one millisecond after that step returns?
3. Two of these sagas run for the same customer, two seconds apart. Which one is refused, and is the customer told why?
