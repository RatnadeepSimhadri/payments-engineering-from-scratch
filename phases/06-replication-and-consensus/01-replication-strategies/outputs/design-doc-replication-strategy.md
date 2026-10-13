---
name: design-doc-replication-strategy
description: Design-document template for choosing and documenting a replication strategy for one data set, with the failover and conflict questions a reviewer will ask
phase: 6
lesson: 1
tags: [replication, failover, design-review, availability, durability]
---

# Replication Strategy: Design Document Template

One copy per data set (a ledger, a catalogue, a session store). A data set that cannot answer every question below is not ready to carry money.

## 1. The data

- Data set and owner:
- Records and approximate size:
- Write rate (peak / typical) and read rate:
- Writers: services, humans, feeds, offline devices? From which regions?

## 2. The five questions

| # | Question | Answer | Consequence |
|---|----------|--------|-------------|
| 1 | Is there an invariant across records that every write must respect (debits equal credits, balance above limit, unique key)? | | A yes rules out multi-leader and leaderless for this data |
| 2 | Can two concurrent writes to the same record be merged without losing meaning? For which fields? | | A no rules out multi-leader and leaderless; a partial yes needs per-field rules |
| 3 | What does one lost acknowledged write cost (money, a regulatory report, a redo)? | | Sets the replication mode: semi-synchronous or consensus if the cost is money |
| 4 | What does write unavailability for one region during a partition cost? | | Decides whether failing closed is acceptable |
| 5 | Who writes, from where, how often? | | Decides whether cross-region write latency is a real constraint |

## 3. The strategy

- [ ] Leader-based / multi-leader / leaderless (circle one) and why, in two sentences referring to the table above.
- [ ] Replication mode: synchronous / semi-synchronous (quorum size: ___) / asynchronous. The exact configuration knob (for example `synchronous_commit = on`, `synchronous_standby_names = 'ANY 1 (...)'`, `w: majority`, `acks=all` + `min.insync.replicas`).
- [ ] What an acknowledgement means, in one sentence: "When the client receives OK, the write is durable on ___ of ___ nodes in ___ failure domains."
- [ ] Replication log format (WAL shipping / logical / statement) and the reason.
- [ ] Replica placement across failure domains (racks, zones, regions), and which failures the placement survives.

## 4. Failover

- [ ] Detection: what signal, what timeout, who decides. Why that timeout and not half or double it.
- [ ] Choice: the rule for which follower is eligible (must have acknowledged the last commit) and which is chosen among eligible ones.
- [ ] Fencing: how the old leader is prevented from accepting writes after promotion (term/epoch rejected by storage, STONITH, storage revocation). "It will be dead" is not an answer.
- [ ] Reconfiguration: how clients learn the new leader (DNS, service discovery, proxy) and the maximum time a client can keep writing to the old one.
- [ ] Loss window: at the 99th-percentile replication lag observed, how many acknowledged writes would a failover lose under this configuration? Expected answer for money: zero.
- [ ] Rejoin: how the old leader is rebuilt (`pg_rewind`, fresh base backup, snapshot) and how its orphaned writes, if any, are extracted and reconciled.
- [ ] Drill: date of the last practised failover and what broke.

## 5. Conflicts (multi-leader and leaderless only)

- [ ] How concurrent writes are detected (version vectors, per-replica counters).
- [ ] Resolution rule per field: last-writer-wins (whose clock?), union, numeric merge, human review.
- [ ] Where unresolved conflicts surface (an editing UI, a queue, a report) and who owns them.
- [ ] Causal ordering: how a dependent write arriving before its cause is handled.
- [ ] Identifier allocation across writers (per-node ranges, UUIDs).

## 6. Reads

- [ ] Which reads go to followers, and with what guarantee (none / read-your-writes via version token / linearizable from the leader). Refer to the Phase 5 Lesson 05 decision procedure.
- [ ] How replication lag is measured, exported, and alerted on, with the threshold.

## 7. Reviewer sign-off

- [ ] A reviewer who did not write this document has traced one failover by hand with log positions and agrees with the loss window in section 4.
