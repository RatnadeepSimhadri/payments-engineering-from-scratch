---
name: decision-card-distributed-transactions
description: One-page decision card for design reviews where someone proposes a transaction that spans two systems
phase: 7
lesson: 8
tags: [2pc, transactions, saga, outbox, design-review]
---

# Decision Card: A Transaction That Spans Two Systems

Use when a design touches two databases, two services, or a database and a message broker in one logical operation. Answer every question in writing before choosing a mechanism.

## 1. Is it really one transaction?

- [ ] Name the invariant that must hold across both systems (for example: "sum of wallet balances + sum of merchant balances is constant").
- [ ] State what a reader sees between the two halves. If a temporary inconsistency is acceptable for seconds, you do not need atomic commitment; you need eventual delivery (outbox) or compensation (saga).
- [ ] State who detects and repairs a half-applied operation today. If the answer is "reconciliation finds it", that is the cost you are comparing against.

## 2. Can every participant prepare?

Two-phase commit requires each participant to:

- [ ] Expose a prepare verb that validates everything, takes locks, and writes a durable record before answering.
- [ ] Hold those locks until the coordinator decides, with no local timeout that releases them.
- [ ] Expose a recover verb listing in-doubt branches so an operator can resolve them.

If any participant is a third party (card network, bank API, SaaS fraud engine), it fails this section. Stop here and go to section 5.

## 3. How available is the coordinator?

- [ ] Where does the coordinator log live, and what is its failover time? That time is the lock hold time of every in-flight transaction when the coordinator dies.
- [ ] Is the coordinator a single process (JTA in one app server), a replicated group (Spanner, CockroachDB), or a database with `PREPARE TRANSACTION`?
- [ ] Who is paged when `pg_prepared_xacts` or the XA recovery scan shows branches older than one minute?

## 4. What does it cost per operation?

| Cost | 2PC | Saga | Outbox |
|------|-----|------|--------|
| Round trips on the critical path | 2 plus acks | 1 per step, no coordination round | 1 local commit |
| Forced log writes | 2n + 2 | 1 per step plus the saga log | 1 (same transaction as the row) |
| Lock hold time across systems | Full protocol duration | None; semantic lock via state instead | None |
| Reader sees intermediate state | Yes, between participant commits | Yes, by design | Yes, until the relay delivers |
| Failure of the orchestrator | Participants block | Saga resumes from its log | Relay resumes from the outbox table |

## 5. Choose

| Situation | Choose | Lesson |
|-----------|--------|--------|
| Two shards of one database system that already speaks XA, same ops team | 2PC | This lesson |
| A database and a message broker that both support XA, low volume, short transactions | 2PC via XA, with the recovery scan monitored | This lesson |
| Several services owned by your organisation, each step reversible | Orchestrated saga with a persisted saga log | Lesson 12 |
| One local write plus events that other systems must eventually see | Transactional outbox with an at-least-once relay | Lesson 13 |
| An external rail or third party on any step | Saga; model the external step as a pivot after which only forward recovery exists | Lesson 12 |

## 6. If you still choose 2PC, write down

- [ ] The exact crash points tested: coordinator before DECISION, coordinator after DECISION before send, participant after PREPARED, participant after COMMITTED before ack.
- [ ] The runbook step for a heuristic resolution of an in-doubt branch, who may execute it, and how the resulting divergence is reconciled.
- [ ] The alert on in-doubt age and on coordinator log growth.
- [ ] A load test on the hottest account showing throughput with locks held for the full two round trips.
