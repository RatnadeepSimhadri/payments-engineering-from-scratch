---
name: template-pacelc-classification
description: Fill-in table for classifying every stateful component of a service by its partition-time and normal-time trade-off, with the partition-mode operation list
phase: 5
lesson: 6
tags: [cap, pacelc, design-review, partitions, consistency]
---

# PACELC Classification Template

Complete one row per stateful component before the design review. A row with an empty "Damage" or "Merge rule" cell is not finished.

## Component table

| Component | Data it holds | P: during partition (A / C / escrow) | E: normal operation (L / C) | Damage if wrong (money, duplicates) | Who loses service if refused | Merge rule on heal | Detection signal and threshold |
|-----------|---------------|---------------------------------------|-----------------------------|--------------------------------------|------------------------------|--------------------|--------------------------------|
| Ledger | | | | | | | |
| Balance for authorisation | | | | | | | |
| Idempotency key store | | | | | | | |
| Payment state machine store | | | | | | | |
| Outbox / webhook queue | | | | | | | |
| Merchant read model | | | | | | | |
| Rate / velocity limiter | | | | | | | |
| Session store | | | | | | | |

## Rules for filling the P column

- Write **C** when a wrong answer is worse than no answer and the quantity cannot be split (ledger postings, idempotency keys, state-machine transitions).
- Write **escrow** when a wrong answer is worse than no answer but the quantity can be allocated in advance (balances, limits, inventory). Record the allocation rule and the overshoot bound.
- Write **A** when a stale or partial answer is acceptable and the merge rule on heal loses nothing that matters (sessions, read models, feature stores).
- Never write **A** with a merge rule of last-writer-wins for data where losing an acknowledged write costs money.

## Rules for filling the E column

- Write **C** when the component is on the money path and the quorum round trip (1 to 2 ms in-region, 40 to 100 ms cross-region) is small relative to the end-to-end operation.
- Write **L** when the component is on every request, off the money path, and a stale read is harmless.
- Record the configuration knob that implements the choice (consistency level, `acks`, `synchronous_commit`, `ConsistentRead`, write and read concern) so a reviewer can check it.

## Partition-mode operation list

For every component marked **A** or **escrow**, list the operations and whether each is allowed while partitioned.

| Component | Operation | Allowed in partition mode | Rule or bound |
|-----------|-----------|---------------------------|---------------|
| | | | |

## Recovery checklist

- [ ] Each side keeps a log of partition-mode operations, not just the resulting value.
- [ ] The merge replays or sums logs; it does not pick one side's value by timestamp.
- [ ] Compensations for operations now known to be wrong are defined and idempotent.
- [ ] Escrow allocations are rebalanced after merge and the rebalancing rule is written down.
- [ ] Replication lag is exported as a metric and alarmed at the detection threshold above.
- [ ] A partition has been rehearsed (block the inter-region link in staging) and the component table matched what happened.

## Five questions to ask of any CAP claim

1. Which register and which operation?
2. What does the minority side do: refuse, serve stale, or serve from an allocation?
3. What is the merge rule on heal, and can it lose an acknowledged write?
4. What is the detection threshold, and how much divergence accumulates before it fires?
5. What does consistency cost per request when the network is healthy, and who pays it?
