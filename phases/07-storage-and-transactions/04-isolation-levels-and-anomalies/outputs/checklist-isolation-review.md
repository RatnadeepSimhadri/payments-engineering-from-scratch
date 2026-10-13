---
name: checklist-isolation-review
description: Review checklist for any transaction that reads money and then writes money, matching the invariant it protects to the isolation mechanism the engine actually provides
phase: 7
lesson: 4
tags: [isolation, transactions, anomalies, review, write-skew]
---

# Isolation Review Checklist

Use on every pull request that opens a transaction, reads one or more rows, and writes based on what it read. One unchecked box is a reason to request changes.

## 1. Name the invariant

- [ ] The invariant the transaction protects is written in one sentence in the PR description (for example "available balance never goes below the account floor", "a merchant never has more than N open holds", "one effect per idempotency key").
- [ ] The rows the invariant depends on are listed. Mark each one **read-only** (the transaction reads it) or **written** (the transaction writes it).
- [ ] If the invariant depends on any **read-only** row, or on the *count* or *sum* of a set of rows, this PR is in write-skew or phantom territory. Continue to section 3; snapshot isolation alone will not protect it.

## 2. Name the mechanism, not the level

- [ ] The requested isolation level is stated (`READ COMMITTED`, `REPEATABLE READ`, `SNAPSHOT`, `SERIALIZABLE`) and the engine is stated (PostgreSQL, MySQL InnoDB, Oracle, SQL Server, other).
- [ ] The mechanism the engine actually runs under that name is written next to it, from this table:

| Level requested | PostgreSQL | MySQL InnoDB | Oracle | SQL Server |
|-----------------|------------|--------------|--------|------------|
| READ COMMITTED | per-statement snapshot | per-statement consistent read; locking reads lock | per-statement snapshot | statement-scoped S locks (or snapshot with RCSI) |
| REPEATABLE READ | snapshot isolation (write skew possible) | snapshot reads plus gap/next-key locks on locking reads | not offered | S locks held to commit (phantoms possible) |
| SNAPSHOT | same as REPEATABLE READ | same as REPEATABLE READ | SERIALIZABLE is snapshot isolation | snapshot isolation (write skew possible) |
| SERIALIZABLE | SSI | strict 2PL with gap locks | snapshot isolation (write skew possible) | strict 2PL with range locks |

- [ ] If the service runs against a different engine in tests than in production (H2, SQLite, an in-memory fake), the PR says which anomalies the test engine cannot reproduce.

## 3. Match the invariant to a sufficient technique

Tick exactly one, and make sure the code matches it:

- [ ] **Single conditional write.** The check and the write are one statement: `UPDATE ... SET balance = balance - ? WHERE id = ? AND balance >= ?`, and the code verifies the affected-row count. Sufficient at every level for an invariant over the written row only.
- [ ] **Lock what you read.** Every read-only row the invariant depends on is read with `SELECT ... FOR UPDATE` (or the engine's equivalent), turning the read into a write so write-write conflict detection or locking catches the race. Sufficient for invariants over a fixed set of rows; not for counts or sums over a range.
- [ ] **Serialize on a parent row.** All checks for an account group lock one row (the account, the customer, the merchant) first, so concurrent checks are ordered. Sufficient for counts and sums within that group; document the hot-row cost.
- [ ] **Serializable.** The transaction runs at a level that is genuinely serializable on this engine (PostgreSQL SERIALIZABLE, InnoDB SERIALIZABLE, SQL Server SERIALIZABLE; not Oracle SERIALIZABLE), and the retry path for serialization failures exists (section 5).
- [ ] **Unique constraint.** For "at most one of" invariants (idempotency keys, one active subscription per customer), a database unique constraint enforces it and the code handles the duplicate-key error. An isolation level is not the mechanism here.

## 4. Reproduce the anomaly

- [ ] A test exists that runs the forbidden interleaving with two transactions and explicit step ordering (as in this lesson's `AnomalyLab`) and fails without the chosen technique.
- [ ] The same test passes with the technique in place, and asserts the invariant on the final state, not just that no exception was thrown.
- [ ] For a sum or count invariant, the test includes an insert into the range between the two reads (the phantom case), not only an update of an existing row.

## 5. Handle refusal

- [ ] Serialization failures (`40001`), deadlocks (`40P01`, 1213, 1205), and first-committer-wins aborts are caught at the transaction boundary, not inside it.
- [ ] The handler retries the whole transaction from the beginning, re-reading every value, with the same idempotency key, a bounded number of times with jitter.
- [ ] After the bound, the failure is surfaced with a distinct error so it can be counted; it is not reported as a decline of the customer's payment.

## 6. Reports and read-only transactions

- [ ] Any transaction that reads more than one row and must be internally consistent (statements, reconciliation, trial balance) is declared read-only and runs on a snapshot (PostgreSQL `REPEATABLE READ`, Oracle `READ ONLY`, SQL Server `SNAPSHOT`).
- [ ] It does not run at a lock-based level on an engine where readers block writers for the duration of the report.
