---
name: checklist-zero-downtime-migration
description: Pull-request review checklist for any DDL or backfill against a table that serves live payment traffic, organised by expand, migrate, verify, and contract
phase: 7
lesson: 14
tags: [migrations, schema, ddl, operations, review]
---

# Zero-Downtime Migration Checklist

Use on every pull request that contains DDL, a backfill, or a change to a column's readers or writers on a table serving live traffic. One unchecked box is a reason to request changes.

## Before any DDL

- [ ] The migration is split into expand, migrate, and contract steps in separate pull requests or separately gated steps; no PR both adds and drops, or both drops and stops using.
- [ ] Every DDL statement is preceded by `SET lock_timeout` (PostgreSQL) or `lock_wait_timeout` (MySQL) of a few seconds, and the runner retries on timeout.
- [ ] For each statement, the reviewer has checked the database's own documentation (not the ORM's) for whether it rewrites the table and which lock it takes. The answer is written in the PR.
- [ ] The migration was run against a production-sized copy and its wall-clock time and lock duration recorded.
- [ ] Schema changes ship and complete before the code that depends on them deploys; the application verifies at startup that the columns it needs exist.
- [ ] On a sharded table, the step runs on every shard before the next step begins anywhere.

## Expand

- [ ] New columns are nullable with no default, or have a constant default on a database version where that is catalog-only.
- [ ] No volatile defaults (`now()`, UUID generators); those are set by the application or a trigger.
- [ ] No `NOT NULL`, `CHECK`, `UNIQUE`, or `FOREIGN KEY` on existing rows yet.
- [ ] New indexes use `CREATE INDEX CONCURRENTLY` (PostgreSQL) or an online algorithm (MySQL), outside a transaction, with a check for an invalid index afterwards.
- [ ] A trigger or generated column fills the new column on every write path, if application dual-write cannot be guaranteed to cover all writers.
- [ ] Rollback is `DROP COLUMN` and is documented.

## Migrate: dual-write and backfill

- [ ] Application writes both old and new representations in the same row and transaction; the new value is computed with the integer-minor-units `Money` discipline, never through a float.
- [ ] Backfill batches by primary-key range, not `OFFSET`; batch size is tuned from measurement and recorded.
- [ ] Backfill predicate is idempotent (`... AND new_col IS NULL`); rerunning a batch is a no-op.
- [ ] Backfill checkpoints its last completed range after each commit and resumes from it.
- [ ] Backfill throttles on replica lag and primary load; thresholds are recorded.
- [ ] Downstream CDC consumers and replica users have been told when the backfill starts and how many update events it will emit.
- [ ] Backfill has no deadline that would tempt someone to raise the batch size past the measured safe point.

## Verify

- [ ] A per-row reconciliation query compares old and new representations across the whole table, batched, on a replica; the expected result is zero mismatches.
- [ ] An aggregate check (per-account sums) is run as a second opinion.
- [ ] Every mismatch category is classified: missing dual-write path, rounding disagreement, or data the new schema correctly cannot represent. The last category has a business ruling and an audited correction script.
- [ ] Shadow-read mode (read both, return old, count mismatches) has run at 100% for at least a day with zero mismatches.

## Migrate: constraints and switching reads

- [ ] `NOT NULL` is added as `CHECK (col IS NOT NULL) NOT VALID` then `VALIDATE CONSTRAINT`, or the engine's equivalent two-step form.
- [ ] Reads switch behind a runtime flag, by percentage, with the rollback being a flag flip, not a deploy.
- [ ] Every reader is enumerated: API, batch jobs, fraud, reconciliation, reporting replica queries, CDC consumers. Each switches and is observed before the next.
- [ ] Old column continues to be written until every reader has switched and been observed.

## Contract

- [ ] At least one release has shipped since the last writer of the old column was removed.
- [ ] The statement log on primary and replicas shows no query naming the old column for the observation window.
- [ ] CDC consumers with strict sink schemas have been updated or paused.
- [ ] A snapshot or backup from before the drop is within retention, and the restore path has been named.
- [ ] Triggers and helper functions created during migrate are dropped in the same step.
- [ ] The migration record (dates, batch sizes, mismatches found, rulings made) is written up for the next team that touches this table.
