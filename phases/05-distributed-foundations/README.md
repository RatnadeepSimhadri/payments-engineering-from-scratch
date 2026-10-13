# Phase 5: Distributed Systems Foundations

> The network is not reliable, and the clock is lying.

13 lessons, 6 complete, ~18 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [The Fallacies of Distributed Computing](01-the-fallacies-of-distributed-computing/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [The Fallacies of Distributed Computing](01-the-fallacies-of-distributed-computing/) | Learn | ~45 min | ✅ | Eight assumptions that have each caused a payments outage. |
| 02 | [Failure Models: Crash-Stop, Omission, Byzantine](02-failure-models/) | Learn | ~60 min | ⬚ | Which failures you tolerate determines which protocol you need. |
| 03 | [Time, Order, and Clocks: Lamport and Vector Clocks](03-lamport-and-vector-clocks/) | Build | ~120 min | ✅ | Two events, two machines, no shared clock: who went first? |
| 04 | [Physical Clocks, NTP, and Hybrid Logical Clocks](04-physical-clocks-ntp-and-hybrid-logical-clocks/) | Build | ~75 min | ⬚ | Clock skew is why your timestamps cannot order transactions. |
| 05 | [Consistency Models: Linearizability to Eventual](05-consistency-models/) | Learn | ~90 min | ✅ | The hierarchy, with a history checker you can run. |
| 06 | [CAP, PACELC, and What They Actually Constrain](06-cap-and-pacelc/) | Learn | ~60 min | ⬚ | Partition tolerance is not optional, so the choice is simpler than the triangle. |
| 07 | [RPC, Framing, and Serialization Over Raw Sockets](07-rpc-and-serialization/) | Build | ~120 min | ⬚ | Length-prefixed frames, request IDs, and a client that survives a half-open connection. |
| 08 | [Timeouts and Deadline Propagation](08-timeouts-and-deadlines/) | Build | ~60 min | ⬚ | The downstream call that outlives its caller. |
| 09 | [Idempotency Keys: Exactly-Once Effects on At-Least-Once Networks](09-idempotency-keys/) | Build | ~120 min | ✅ | The single most important pattern in payments, with a store that handles concurrent duplicates. |
| 10 | [Retries, Exponential Backoff, and Jitter](10-retries-backoff-and-jitter/) | Build | ~90 min | ✅ | Retry storms are self-inflicted outages; simulate one and then prevent it. |
| 11 | [Circuit Breakers and Bulkheads](11-circuit-breakers-and-bulkheads/) | Build | ~75 min | ✅ | Fail fast when the processor is down instead of queueing a million timeouts. |
| 12 | [Leases, Fencing Tokens, and Distributed Locks](12-leases-and-fencing-tokens/) | Build | ~90 min | ⬚ | Why a lock with a timeout needs a monotonic token to be safe. |
| 13 | [Gossip Protocols and Failure Detection](13-gossip-and-failure-detection/) | Build | ~75 min | ⬚ | SWIM-style membership without a coordinator. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
