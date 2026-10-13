---
name: checklist-snapshot-isolation-review
description: Review checklist for any service whose transactions run on a snapshot-isolation database (PostgreSQL REPEATABLE READ, InnoDB, Oracle, SQL Server SNAPSHOT, CockroachDB)
phase: 7
lesson: 6
tags: [mvcc, snapshot-isolation, write-skew, vacuum, review]
---

# Snapshot Isolation Review Checklist

Use when reviewing a transaction boundary in a service that talks to an MVCC database. One unchecked box is a question for the author, not necessarily a defect.

## Where is the snapshot taken?

- [ ] The code knows whether the database takes the snapshot at `BEGIN` or at the first statement (PostgreSQL: first statement). Any "open transaction, then wait for something, then read" pattern is flagged.
- [ ] The configured isolation level is named in the code or the connection pool, not assumed. `READ COMMITTED` takes a new snapshot per statement and allows read skew across statements.
- [ ] Every multi-statement read that must be self-consistent (reconciliation, statement generation, balance plus history) runs in one `REPEATABLE READ` or `SNAPSHOT` transaction.

## Write-skew shaped checks

- [ ] Every "read something, decide, write something else" sequence has been listed. For each one: does another concurrent transaction read what this one writes and write what this one reads?
- [ ] Constraints that span rows (joint overdraft, "at most N approvals", "refunds <= order total", "exactly two on-call") are enforced by one of: a constraint row that both writers update, `SELECT ... FOR UPDATE` on the rows the decision depends on, a database constraint, or `SERIALIZABLE`.
- [ ] Uniqueness and existence checks ("insert if not present") rely on a unique index, not on a preceding `SELECT`.

## Retries on serialization failure

- [ ] Every transaction that writes under SI is wrapped in a retry loop that catches the engine's serialization error (PostgreSQL `40001`, SQL Server `3960`, CockroachDB `40001`) and re-runs the *whole* transaction, re-reading its inputs.
- [ ] The retry loop has a bounded attempt count and jittered backoff, and the metric for retries is on a dashboard.
- [ ] Side effects outside the database (sending a webhook, calling a processor) happen after commit, never inside the retried body (see Lesson 13, Transactional Outbox).

## The horizon

- [ ] The longest expected transaction duration is written down, and `idle_in_transaction_session_timeout` (or the equivalent) is set below it.
- [ ] Batch jobs that read the whole book use a replica, or chunk their work into bounded transactions, or are explicitly accepted as pinning the horizon for their duration.
- [ ] Replication slots and prepared transactions are monitored as horizon pins, not only client sessions.
- [ ] Dead-version bloat is observable: `n_dead_tup` and autovacuum lag in PostgreSQL, `History list length` in InnoDB, undo retention in Oracle.

## Tests

- [ ] There is a test that runs two transactions interleaved against the same key and asserts that exactly one commits.
- [ ] There is a test for the service's most important cross-row constraint that interleaves two transactions and asserts the constraint still holds after both finish.
