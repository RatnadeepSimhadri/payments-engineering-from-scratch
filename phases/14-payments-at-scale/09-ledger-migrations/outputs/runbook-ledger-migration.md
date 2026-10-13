---
name: runbook-ledger-migration
description: Phase-by-phase runbook with exit criteria, proofs, and rollback gates for migrating a live balance table to a double-entry ledger
phase: 14
lesson: 9
tags: [ledger, migration, cdc, dual-write, reconciliation]
---

# Live Ledger Migration Runbook

## Phase 0: Design

- [ ] Inventory write paths from the database log over at least 30 days, not from the codebase.
- [ ] Map every legacy balance type and reason row to accounts and journal templates; finance reviews.
- [ ] Decide history scope: opening balances only, recent window (dispute + statement period), or full.
- [ ] Identify hidden state (reserved columns, open authorizations, negative balances) and its representation.

## Phase 1: Dual-write from the log

- [ ] CDC consumer posts `legacy:<lsn>:<account>` journals idempotently; checkpoint after each source transaction.
- [ ] Unexplained changes go to `migration:unexplained`; daily report of count and amount by source.
- [ ] Consumer lag is a metric with an alert.

## Phase 2: Backfill at one cut

- [ ] The cut is a single log position; snapshot and stream both defined by it.
- [ ] Opening balances posted against `migration:opening_equity`; its balance equals the negated sum.
- [ ] Legacy amounts converted to integer minor units; any rounding residue posted to a named account and reported.

## Phase 3: Shadow-read

- [ ] Balances, derived values, and debit decisions compared at the same log position.
- [ ] Every mismatch logged with both values and the position, classified, and fixed at the source.
- [ ] Exit: zero unexplained mismatches for the agreed period, covering a month-end, an interest run, and a statement cycle.

## Phase 4: Cohort cutover

- [ ] Cohorts by stable hash: internal, 1%, 10%, 50%, 100%.
- [ ] Per cohort: block writes, drain consumer, verify equality, flip, start reverse sync, unblock.
- [ ] Rollback rehearsed on the 1% cohort before 10%.

## Phase 5: Prove (nightly until decommission)

- [ ] Per-account equality for every account.
- [ ] Range checksums with row-level fallback.
- [ ] Trial balance zero; wallet totals equal old totals; opening equity correct; suspense cleared or accepted.

## Phase 6: Point of no return

- [ ] Written, dated decision with approvers (engineering and finance).
- [ ] Only after it: stop reverse sync, enable new-only features, archive the old table read-only.
