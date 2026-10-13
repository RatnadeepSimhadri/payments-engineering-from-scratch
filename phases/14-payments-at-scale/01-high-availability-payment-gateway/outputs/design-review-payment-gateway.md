---
name: design-review-payment-gateway
description: Design-review template for a payment gateway or any service that accepts money-moving requests; one question per failure mode, answered with a component name
phase: 14
lesson: 1
tags: [architecture, availability, idempotency, outbox, review]
---

# Payment Gateway Design Review

Answer every question with the name of a component, a table, or a message. "It is handled" is not an answer. A reviewer should be able to read the answers and redraw the architecture.

## 1. The source of truth

- Which single store is authoritative for a payment's state? ______
- Is every other component either stateless or holding state derivable from that store? List any exception and why it exists.
- How does a message leave the store: outbox table in the same transaction, CDC on the log, or application code after commit? (The third answer is a finding.)
- Replication mode of the store: synchronous to how many replicas? What does "acknowledged" mean in machines?

## 2. The idempotency store

- Where does the key live? Same transaction as the payment row, or a separate store?
- If separate: describe the state after a crash between the two writes, in both orders.
- Scope of the key: per merchant? per region? What happens on reuse with a different body?
- Retention: how long is a key honoured, and what does a retry after expiry do?
- Concurrent duplicate while the first request is still open: lock, 409, or second payment?

## 3. The edge

- Does the edge call any external processor synchronously inside a request? If yes, mark on the diagram the instant where a crash leaves the processor knowing something the store does not.
- Health-check interval and the time for the load balancer to stop routing to a dead edge.
- Per-edge concurrency cap, and the residence time it was sized for (average, or the slow-dependency case?).

## 4. Processor adapters

- Timeout per processor and the measured p99 it was derived from. Date of the measurement.
- Breaker thresholds: failures per window to open, half-open trickle rate, close condition.
- Bulkhead: separate pools per processor? What happens to Processor B traffic when Processor A hangs?
- Processor idempotency key: where is it stored before the first call, and is the same value used on every retry?
- For a processor with no idempotency key: how does the adapter query by reference before retrying?

## 5. Unknown outcomes

- Name the durable state for "processor contacted, result not known".
- Which job resolves it, how often, by what query, and what does it do when the processor has no record?
- Alert threshold on the count and age of unknowns.
- Confirm in writing that no code path maps a timeout to a decline.

## 6. Webhooks and ledger

- Dispatcher source: outbox or application code? Signing scheme and replay bound.
- Ordering guarantee per payment, and what the merchant is told to dedupe on.
- Retry schedule and total retry horizon.
- Which transitions post to the ledger, and what happens when the ledger disagrees with the payment store?

## 7. Failover and regions

- Store failover: automatic or operator-driven? Expected unavailability window?
- Region model: home-region per merchant, or active-active writes? If active-active, where is the idempotency key unique?
- Cross-region replication mode and lag. What is done about keys in the lag window before the surviving region accepts traffic?

## 8. Capacity

| Quantity | Value | Derived from |
|----------|-------|--------------|
| Average and peak payments per second | | forecast |
| End-to-end p99 target and processor p99 | | measurement |
| Processor timeout | | processor p99 + margin |
| In-flight requests at peak, slow case | | Little's law with slow-dependency residence |
| Edge count per region | | peak / per-node capacity at 60%, plus N+2 |
| Write transactions per second on the store | | payments x transitions |
| Outbox growth per day and trim policy | | row sizes x rate |

## 9. The four walkthroughs

For each, write the sequence of component actions and the final state of a payment that was in flight. Then name the row, message, or timer that prevented a duplicate or a loss.

1. Primary processor returns connection resets for 17 minutes.
2. Store primary dies with transactions open; replica promoted 55 seconds later.
3. A client retries with the same key two seconds after the first request, then again with a changed amount.
4. The home region loses power with 300 ms of replication lag.

## Findings

| # | Severity | Finding | Component | Owner | Due |
|---|----------|---------|-----------|-------|-----|
| | | | | | |

A design with an unanswered question in sections 1, 2, or 5 does not pass review.
