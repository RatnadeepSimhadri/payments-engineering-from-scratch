---
name: interview-card-failure-models
description: One-page decision card for stating a component's failure model in a design review or a system-design interview
phase: 5
lesson: 2
tags: [failure-models, design-review, consensus, interview]
---

# Failure Model Decision Card

Fill this in for every component that replicates state or talks to a peer. The output is one sentence per trust boundary. If a clause cannot be filled, the design is not finished.

## The sentence

> This component tolerates `f = __` **[crash-stop | crash-recovery | omission | timing | Byzantine]** failures out of `n = __` under **[synchronous | partially synchronous | asynchronous]**; stale or paused writers are fenced by **__**; external inputs from **__** are treated as **[authenticated Byzantine | Byzantine]** and validated by **__**.

## The five questions

| # | Question | If yes | If no |
|---|----------|--------|-------|
| 1 | Is the peer inside our trust boundary (our code, our infrastructure)? | Crash-recovery plus timing; defend timing with fencing, not BFT | Byzantine; defend with signatures, schema validation, reconciliation |
| 2 | Does it have stable storage that survives a restart? | Crash-recovery; persist every promise before sending it | Crash-stop; recovery means rejoining from scratch |
| 3 | Can it pause longer than any timeout we depend on (GC, VM migration, suspend)? | Every timeout-driven takeover must be fenced by a monotonic token or epoch | Still assume yes; nobody has ever regretted it |
| 4 | Can messages between us be lost, duplicated, or reordered? | Idempotency keys on every effect; retries with backoff | Only true inside one TCP connection; reconnects reset this to yes |
| 5 | What synchrony do we need for liveness? | Partial synchrony; list every timeout as a tunable with its false-suspicion cost | If the answer is "synchronous", the design is one pause from unsafe |

## Resilience bounds to quote

| Model | Consensus needs | Quorum | Example |
|-------|-----------------|--------|---------|
| Crash / crash-recovery, partial synchrony | `2f + 1` | `f + 1` | Raft, Paxos, etcd, ZooKeeper |
| Byzantine, partial synchrony | `3f + 1` | `2f + 1` | PBFT and descendants |
| Crash, asynchronous | impossible (FLP) | n/a | Every real protocol trades liveness for safety |

Why the extra `f`: two crash quorums share one correct process; two Byzantine quorums must share `f + 1` so that one is honest.

## Red flags in a design or an answer

- [ ] "Tolerates one node failure" with no model named.
- [ ] Failover tested only with `kill -9`; never with `SIGSTOP`, a 60-second pause, or a clock jump.
- [ ] A timeout drives a safety decision (promote, commit, pay) without a fencing token.
- [ ] Replicas in one fault domain, or on one release, counted as independent.
- [ ] A counterparty's data (file, webhook, API response) trusted without schema validation and independent reconciliation.
- [ ] "We use TCP" offered as the answer to message loss or duplication across reconnects.
- [ ] Byzantine consensus proposed for replicas inside the trust boundary, with no threat that requires it.

## Mapping to this course

| Failure | Mechanism | Lesson |
|---------|-----------|--------|
| Link omission and duplication | Idempotency keys | Phase 5, Lesson 09 |
| Send omission | Retries with backoff and jitter | Phase 5, Lesson 10 |
| Timing failure of a lock holder | Leases with fencing tokens | Phase 5, Lesson 12 |
| Crash-stop detection | Gossip and SWIM | Phase 5, Lesson 13 |
| Crash-recovery of replicas | Raft | Phase 6, Lessons 05 to 07 |
| Byzantine replicas | PBFT | Phase 6, Lesson 11 |
| Node amnesia on restart | Write-ahead log | Phase 7, Lesson 02 |
| Byzantine sender at the edge | Signed webhooks | Phase 12, Lesson 05 |
| Byzantine counterparty data | Reconciliation | Phase 11 |
