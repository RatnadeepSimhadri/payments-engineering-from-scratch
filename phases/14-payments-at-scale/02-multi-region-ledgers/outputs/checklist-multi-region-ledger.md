---
name: checklist-multi-region-ledger
description: Design-review checklist for a ledger deployed in more than one region, covering ownership, routing, cross-region transfers, DR, and residency
phase: 14
lesson: 2
tags: [ledger, multi-region, residency, disaster-recovery, design-review]
---

# Multi-Region Ledger Design Review

## Ownership and routing

- [ ] Every account has exactly one home region, recorded in a strongly consistent (or immutable) directory.
- [ ] Every write carries an ownership epoch; the home rejects writes at a stale epoch.
- [ ] Balance checks run in the same home-region transaction as the debit.
- [ ] Scheduled jobs that debit accounts run in the account's home region.
- [ ] Each read path has a written staleness budget; no stale read feeds a decision.
- [ ] Clients get read-your-writes via a returned journal position.

## Cross-region money movement

- [ ] No distributed transaction spans regions; transfers are local debit + outbox + idempotent remote credit.
- [ ] One clearing account per region pair per currency, in each region.
- [ ] Mirror reconciliation (`due_to + due_from + in flight = 0`) runs every few minutes and alerts on residuals that persist across two runs.
- [ ] Return path for a closed or frozen destination is implemented and tested.
- [ ] Exactly one region performs FX on a cross-currency transfer.

## Durability and DR

- [ ] Within a region, commits are synchronously replicated across at least two other zones.
- [ ] Cross-region replication lag is measured; the promised RPO is the measured p99.99 lag, not the median.
- [ ] Failover runbook: decide, fence (bulk epoch bump), promote, reconstruct tail from external records, diff old journal on return, post corrections.
- [ ] The tail-reconciliation sources (outbox deliveries, processor reports, bank acknowledgements, client idempotency keys) are listed and reachable from outside the failed region.

## Residency and legal

- [ ] Home region follows the legal entity that holds the customer's funds.
- [ ] Replicas, backups, logs, and analytics copies obey the same residency rules as the primary.
- [ ] Account migration procedure exists (freeze, drain holds, transfer balance, bump epoch, unfreeze) and history stays where residency requires.
- [ ] Shared global accounts (fees, treasury) are split per region or deliberately placed on a consensus store.
