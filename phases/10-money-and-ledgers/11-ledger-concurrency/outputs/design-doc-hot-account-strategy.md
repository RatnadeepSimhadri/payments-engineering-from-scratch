---
name: design-doc-hot-account-strategy
description: Design-review template for choosing and proving a concurrency strategy for hot ledger accounts
phase: 10
lesson: 11
tags: [ledger, concurrency, hot-account, locking, sharding, design-review]
---

# Design Doc: Concurrency Strategy for <ACCOUNT CLASS>

## 1. Workload

- Account class: <e.g. merchant payable for top-100 merchants; platform fee revenue; FX clearing>
- Peak writes per second to a single account (measured or forecast, with source): <n>
- Ratio of credits to debits: <e.g. 95:5>
- Which operations need a balance check (debits against non-negative or available balance): <list>
- Read patterns: <dashboard (approximate ok) / payout (must be exact) / statement (as-of)>

## 2. Critical section

- What runs while the account is exclusively held: <validate, read, write, commit, network round trips?>
- Measured hold time p50 / p99: <values>
- Resulting ceiling = 1 / hold time: <value>. Headroom against peak: <factor>

## 3. Chosen design

- [ ] Pessimistic: ordered locks (`SELECT ... FOR UPDATE ORDER BY id`, or in-memory locks by id)
- [ ] Optimistic: version column / CAS with retry; max retries <n>; backoff <policy>
- [ ] Sharded: K = <n> sub-accounts; routing <round-robin / hash of payment id>; roll-up every <interval>
- [ ] Append-only credits with asynchronous balance; only debits take a lock
- [ ] Batched application of credits: batch size <n>, max delay <ms>

Why this design and not the others, in two sentences:

## 4. Invariants and how they are enforced

| Invariant | Enforced by | Tested by |
|-----------|-------------|-----------|
| Conservation: sum of balances constant under transfers | <mechanism> | stress test with auditor taking consistent cuts |
| No debit overdraws a non-negative account | <check inside lock / validated version / greedy drain> | interleaving model + stress |
| No deadlock | global lock order by account id | opposite-direction transfer test |
| Money in transit is always in some account | in-flight / sweep account | auditor cuts |

## 5. Behaviour under contention and failure

- What callers see when the account is saturated: <queueing latency / retries / rejections>
- Timeouts and client retry policy (idempotency keys required, see Lesson 03): <values>
- What happens to an in-flight sharded debit if the process crashes between steps: <recovery procedure>
- Can a valid payout be refused? Under what conditions, and how is it retried: <answer>

## 6. Reads

- Exact balance read cost: <lock 1 row / lock K shards / version read>
- Approximate balance for dashboards: <lock-free sum of shards / cached>
- As-of balances (Lesson 05): <how shards and snapshots interact>

## 7. Verification plan

- [ ] Interleaving model of the read/check/write steps explores all schedules for 2-3 operations with zero violations.
- [ ] Stress test: N threads, deterministic scripts, expected final state computed from scripts, auditor thread.
- [ ] Load test at 2x forecast peak on production-like hardware; record lock waits / retries per second.
- [ ] Alert on: retry rate, lock wait time, shard imbalance, non-zero in-flight balance older than <duration>.
