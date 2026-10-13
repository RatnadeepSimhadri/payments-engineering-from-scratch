---
name: runbook-deadlock-triage
description: On-call runbook for a database deadlock alert on a payments service (PostgreSQL or MySQL InnoDB), from reading the cycle report to choosing and verifying the fix
phase: 7
lesson: 5
tags: [deadlock, locking, postgresql, mysql, runbook, on-call]
---

# Deadlock Triage Runbook

Use when an alert fires on `deadlock detected` (PostgreSQL `40P01`), `Deadlock found when trying to get lock` (MySQL error 1213), or SQL Server error 1205, or when a lock-wait latency alert points at a payments table.

## 1. Confirm it is a deadlock and not a long wait

| Symptom | Deadlock | Long lock wait |
|---------|----------|----------------|
| Server log | explicit cycle report with two or more process ids and the statements | `still waiting for ShareLock` after `deadlock_timeout`, no cycle |
| Connections | a burst of errors, then recovery within seconds | a growing pile of idle-in-transaction waiters, no errors until the statement timeout |
| Fix category | ordering, scope, or retry | find the holder: an open transaction doing a network call, a forgotten `COMMIT`, a batch job |

PostgreSQL: `SELECT pid, state, wait_event_type, wait_event, query, xact_start FROM pg_stat_activity WHERE wait_event_type = 'Lock';` and `pg_blocking_pids(pid)` to draw the wait-for graph by hand.
MySQL: `SHOW ENGINE INNODB STATUS\G`, section `LATEST DETECTED DEADLOCK`, and `SELECT * FROM performance_schema.data_lock_waits;`.

## 2. Read the cycle report

For each transaction on the cycle, record:

- [ ] the statement that was waiting and the lock mode it wanted (S, X, gap, next-key)
- [ ] the row or range it was waiting on (table, index, key value)
- [ ] the locks it already held (the report lists them for InnoDB; for PostgreSQL, infer from the earlier statements of the same transaction in your application logs)
- [ ] which transaction was chosen as victim and what the application did with the error

Write it as a table with one row per transaction, columns `holds` and `wants`. The cycle is visible when the `wants` of one row is in the `holds` of another and back again.

## 3. Classify the shape

| Shape | Signature | Typical payments code |
|-------|-----------|-----------------------|
| Opposite-order writes | two transactions touch the same two rows in different orders | transfer A to B against transfer B to A; settle merchant then customer against refund customer then merchant |
| Lock upgrade | both transactions hold S on one row and both want X on it | `SELECT balance` then `UPDATE balance` without `FOR UPDATE`; "check then debit" |
| Gap or range | InnoDB gap/next-key locks on an index range, often with inserts | inserting ledger postings while another transaction scans the same account's postings |
| Foreign key | a child insert waits on the parent row's lock while the parent update waits on the child | inserting a payment row referencing a merchant that another transaction is updating |
| Cross-system | one lock inside the database, one outside (a distributed lock, a queue, an HTTP call under an open transaction) | holding a row lock while calling the processor |

## 4. Choose the fix

Apply the first fix that is possible for the shape:

1. **Ordering.** If every transaction knows its full row set up front, acquire in one global order (ascending primary key). Transfers always can. Prove it with a test that reproduces the original interleaving and asserts zero deadlocks.
2. **Lock scope.** Take the strongest lock you will need at the first access (`SELECT ... FOR UPDATE` instead of a plain read followed by an `UPDATE`) so there is no upgrade. Shorten the transaction: no network calls, no user think-time, no batch work inside the transaction that holds the hot row.
3. **Single-statement update.** Replace read-modify-write with `UPDATE accounts SET balance = balance - ? WHERE id = ? AND balance >= ?` and check the affected-row count. One statement, one lock, no upgrade.
4. **Retry.** The deadlock error means the whole transaction was rolled back. Retry the whole transaction, re-reading every value, with the same idempotency key, with jitter, at most 3 to 5 times. This is a mitigation that must exist regardless of the other fixes, because gap and foreign-key deadlocks can appear from schema or plan changes you did not make.
5. **Isolation change.** Only after the above: moving reads to a snapshot level (PostgreSQL `REPEATABLE READ`, InnoDB consistent reads) removes reader/writer waits but changes which anomalies are possible (Lesson 04 and Lesson 06). Do not do this during an incident.

Never: increase `deadlock_timeout` or `innodb_lock_wait_timeout` to "make the errors go away". That converts a one-second detection into a fifty-second stall.

## 5. Verify

- [ ] A test reproduces the exact interleaving from the cycle report (two scripted transactions, explicit step order, as in `MainTest.java` of this lesson) and fails before the fix
- [ ] The same test passes after the fix with zero deadlocks, and the final balances equal those of a serial run
- [ ] The retry path is exercised by a test that injects the deadlock error once and asserts exactly one effect
- [ ] Deadlock count per minute is on a dashboard with the alert threshold written down (a handful per day on a busy ledger is normal; a step change is not)
- [ ] The postmortem records the shape from step 3 so the next engineer can skip to step 4

## 6. Reference: what each engine does

| | PostgreSQL | MySQL InnoDB | SQL Server |
|--|-----------|--------------|------------|
| Detection | after `deadlock_timeout` (1 s) of waiting, graph search | on every lock wait | lock monitor every 5 s, faster under load |
| Victim | the backend that ran the detector | fewest rows modified | lowest estimated rollback cost, or `DEADLOCK_PRIORITY` |
| Error | `40P01` | 1213 | 1205 |
| Where to look | server log, `pg_locks`, `pg_blocking_pids` | `SHOW ENGINE INNODB STATUS` | `sys.dm_tran_locks`, deadlock graph XML in Extended Events |
