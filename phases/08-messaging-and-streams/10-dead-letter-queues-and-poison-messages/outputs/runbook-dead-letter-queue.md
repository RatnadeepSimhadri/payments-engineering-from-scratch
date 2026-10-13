---
name: runbook-dead-letter-queue
description: On-call runbook for a consumer with a dead-letter queue - alerts, triage by park reason, replay procedure, and sign-off
phase: 8
lesson: 10
tags: [streams, dead-letter-queue, poison-messages, replay, runbook, on-call]
---

# Dead-Letter Queue Runbook

For any consumer that parks events it cannot process. Fill in the service-specific fields once; the procedure is the same for every DLQ.

| Field | Value |
|-------|-------|
| Service | |
| Source topic / queue and partition count | |
| DLQ topic / queue / table | |
| Per-key ordering key | |
| Handler idempotency key (offset, event id) | |
| `maxAttempts` and backoff schedule | |
| Replay tool and who may run it | |
| Business approver for payload corrections | |

## Alerts

| Alert | Threshold | Meaning |
|-------|-----------|---------|
| DLQ size | Greater than 0 for reconciled data (money, orders); greater than N for droppable data | Something is parked and someone must look |
| DLQ oldest entry age | Greater than the replay SLA (for example 4 hours) | A parked event is becoming a reconciliation break |
| `PERMANENT_FAILURE` rate | Any sustained rise | An upstream release changed the payload; find the deploy |
| `MAX_ATTEMPTS_EXCEEDED` rate | Any sustained rise | A downstream dependency is unhealthy; check it before touching the DLQ |
| Quarantined key count | Greater than a small number, or any single key quarantined longer than the SLA | Accounts are frozen behind a parked event |
| Consumer committed offset | Not advancing | The consumer is stalling despite the DLQ; the failure path is not parking |

## Triage

1. Read the dead letters grouped by `reason`, then by `lastError`, then by key. One cause usually explains most of the entries.
2. `PERMANENT_FAILURE`
   - Identify the upstream change (deploy time, schema version, producer team) from the `lastError` and `parkedAt`.
   - Decide with the business approver: correct the payloads, reject the transactions, or fix the handler to accept the new form. Record the decision.
   - If the handler is fixed, deploy it before replay. If payloads are corrected, prepare the corrections map (offset to corrected event) and have the approver sign it.
3. `MAX_ATTEMPTS_EXCEEDED`
   - Check the dependency named in `lastError`. Do not replay until it is healthy; a replay into a sick dependency re-parks everything and resets `parkedAt`, hiding the real age.
   - If the dependency is healthy and the failures were a brief outage, replay with no corrections.
4. `KEY_QUARANTINED`
   - These are followers. They need no action of their own; they are released when the blocking offset for their key is replayed successfully. Never replay a follower alone.

## Replay procedure

Preconditions, all required:

- [ ] The handler is idempotent on the recorded key. Verified by a test that calls it twice with the same event.
- [ ] The cause is fixed: handler deployed, dependency healthy, or corrections approved and attached.
- [ ] The replay is per key and sorts by offset. Never replay by `parkedAt` order.
- [ ] A dry run (parse and validate every event in the key's dead letters without posting) passes for every corrected payload.

Steps:

1. Replay one key with the smallest number of dead letters first. Confirm the handler applied them in offset order and the key left quarantine.
2. Reconcile that key: the sum of its postings equals the sum of its source events.
3. Replay the remaining keys for the same cause in batches. Watch the `PERMANENT_FAILURE` rate; a rise means the fix is incomplete and the replay is re-parking.
4. Confirm DLQ size and quarantined key count return to zero for the cause. Leave entries for other causes alone.
5. Record offsets replayed, corrections applied, approver, and operator in the incident log.

## Do not

- Do not skip an offset by hand to unblock a partition. The DLQ exists so that is never necessary.
- Do not edit a dead letter in place. Corrections are a separate, approved map applied at replay; the original bytes stay in the DLQ record.
- Do not raise `maxAttempts` to make `MAX_ATTEMPTS_EXCEEDED` go away. It lengthens the partition stall per event and does not fix the dependency.
- Do not purge the DLQ to clear an alert. For reconciled data, every purged entry is a reconciliation break with no record.

## Post-incident

| Question | Answer |
|----------|--------|
| What produced the poison messages, and is the producer fixed or contract-tested? | |
| Did the handler classify the failure correctly on the first attempt? | |
| How long were keys quarantined, and did any business process notice? | |
| Did the replay reconcile exactly? | |
| What alert would have paged earlier? | |
