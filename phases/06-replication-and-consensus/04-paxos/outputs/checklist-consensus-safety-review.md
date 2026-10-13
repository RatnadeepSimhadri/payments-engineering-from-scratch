---
name: checklist-consensus-safety-review
description: Pull-request review checklist for any change to a Paxos or Raft implementation, or to a payments service that relies on one, mapping each safety rule to the test that proves it
phase: 6
lesson: 4
tags: [consensus, paxos, raft, safety, review]
---

# Consensus Safety Review Checklist

Use on every pull request that touches acceptor or follower state, ballot or term handling, message handlers, persistence, or retry logic in a consensus path. One unchecked box in the first half is a blocking finding.

## Part 1: The rules

### Ballots (terms)

- [ ] Ballots are totally ordered and globally unique: `(round, nodeId)` compared lexicographically, or an equivalent construction. No two nodes can ever generate the same ballot.
- [ ] A proposer never reuses a ballot after a restart. The last round used is persisted, or the new round is derived from a persisted higher value (for example, the highest promised ballot seen).
- [ ] On NACK, the next round jumps above the ballot reported in the NACK rather than incrementing by one.

### Acceptor (P1: never accept below a promise)

- [ ] `PREPARE(b)` is promised only if `b > promised`; `ACCEPT(b, v)` is accepted only if `b >= promised`. The comparison operators are reviewed character by character.
- [ ] `promised`, `acceptedBallot`, and `acceptedValue` are persisted with `fsync` (or an equivalent durable write) **before** the reply leaves the node.
- [ ] Restart recovers all three fields from durable storage; a node that cannot read them refuses to participate rather than starting empty.
- [ ] Disk or checksum corruption of acceptor state is detected and treated as "amnesia": the node rejoins as a new member through reconfiguration, not as itself.

### Proposer (P2: adopt the reported value)

- [ ] After a majority of promises, the proposer proposes the value with the **highest reported accepted ballot**, and only proposes its own value when no promise reported one.
- [ ] Replies (PROMISE, ACCEPTED, NACK) for a ballot other than the current one are ignored.
- [ ] A client request that loses the slot is re-proposed in a **new** slot, never re-submitted as a different value for the same slot.
- [ ] The proposer's "decided" flag is not treated as the moment of choice by any caller; success to a client means a learner or the proposer observed a majority of ACCEPTED for one ballot.

### Learners and clients

- [ ] Learners count ACCEPTED votes per ballot, not per value across ballots.
- [ ] Every money-moving client operation carries an idempotency key, so a retry after an ambiguous timeout cannot post twice.

### Liveness

- [ ] Contention is bounded by a distinguished proposer (leader) or randomized backoff drawn from an injected, seeded RNG.
- [ ] Timeouts come from an injected clock, never from `sleep` or wall-clock reads in the protocol code.

## Part 2: Which test proves it

Ask the author to point at the test for each row. A rule without a failing-when-broken test is a rule nobody will notice breaking.

| Rule | Test that fails if the rule is broken (lesson 04 names) |
|------|----------------------------------------------------------|
| Ballot uniqueness and ordering | `ballot: proposer id breaks ties`, `ballot: round dominates proposer id` |
| P1 on prepare | `acceptor NACKs a lower prepare` |
| P1 on accept | `acceptor refuses to accept below its promise`, `acceptor accepts the ballot it promised` |
| P2 reporting | `later promise carries the accepted ballot and value` |
| P2 adoption | `proposer adopts the value of the highest prior ballot`, `losing proposer decides the same value` |
| Stale reply filtering | `stale replies for an old ballot are ignored` |
| NACK jump | `NACK restarts with a round above the promised one` |
| Global agreement | `200 lossy runs: never two chosen values` (god's-eye `checkSafety` after every delivery) |
| Learner correctness | `200 lossy runs: learners and proposers only ever learn the chosen value` |
| Durability before reply | add a crash/restart test that wipes acceptor state and shows `checkSafety` firing (Exercise 2) |
| Determinism | `determinism: identical seed gives identical outcome and message counts` |

## Part 3: Questions to ask in the review

1. Which line of code is the P1 comparison, and what happens if it becomes `>` instead of `>=`?
2. Where is acceptor state written, and is the write durable before the reply is sent?
3. If every ACCEPTED reply is lost, what does the client see, and what does it do next?
4. Is there a god's-eye invariant check running in the simulation tests, and does it run after every message?
5. How many seeds and what loss rate does CI run, and is a failing seed reproducible locally?
