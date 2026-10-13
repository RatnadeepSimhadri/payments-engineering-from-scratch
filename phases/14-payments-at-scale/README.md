# Phase 14: Payments at Scale

> At scale, every rare bug is a daily event.

11 lessons, 4 complete, ~17 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Architecture of a High-Availability Payment Gateway](01-high-availability-payment-gateway/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Architecture of a High-Availability Payment Gateway](01-high-availability-payment-gateway/) | Learn | ~90 min | ✅ | Stateless edges, durable cores, and where the idempotency store lives. |
| 02 | [Multi-Region Ledgers and Home-Region Routing](02-multi-region-ledgers/) | Learn | ~90 min | ⬚ | An account lives somewhere; route to it. |
| 03 | [Exactly-Once Money Movement End to End](03-exactly-once-money-movement/) | Build | ~150 min | ✅ | Idempotency, outbox, consumer dedupe, and a ledger that cannot double-post. |
| 04 | [Reconciliation at Scale: Streams, Buckets, and Breaks](04-reconciliation-at-scale/) | Build | ~120 min | ⬚ | Match a billion records a day and surface the breaks. |
| 05 | [Batch vs Real-Time Settlement Pipelines](05-batch-vs-real-time-settlement/) | Build | ~90 min | ⬚ | The nightly job that moves the actual money. |
| 06 | [Observability for Money Movement](06-observability-for-money-movement/) | Build | ~90 min | ⬚ | Metrics that detect a stuck payment before a customer does. |
| 07 | [Incident Management for Payments](07-payment-incident-management/) | Learn | ~60 min | ⬚ | Stop the bleeding, then reconcile. |
| 08 | [Cost and Authorization-Rate Optimization](08-cost-and-authorization-optimization/) | Build | ~75 min | ⬚ | Network tokens, retries, and routing that lifts approvals. |
| 09 | [Migrating a Live Ledger](09-ledger-migrations/) | Learn | ~90 min | ⬚ | Dual-write, shadow-read, cut over, and prove the balances match. |
| 10 | [Deterministic Simulation Testing of Payment Systems](10-simulation-testing-of-payment-systems/) | Build | ~120 min | ✅ | Run a year of payments in a minute with a seeded network. |
| 11 | [Payments System Design: Interview Patterns](11-payments-system-design-interviews/) | Learn | ~90 min | ✅ | Design a payment system in 45 minutes, with the questions interviewers actually ask. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
