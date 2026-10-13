---
name: interview-card-consistency-models
description: One-page interview and design-review card on consistency models, with definitions, the anomaly each model forbids, and the payments read that needs it
phase: 5
lesson: 5
tags: [consistency, distributed-systems, interview, design-review, replication]
---

# Consistency Models: Interview and Design-Review Card

Use it to prepare for a systems interview or to annotate every read in a payments design document with the model it needs.

## The hierarchy in one line each

| Model | Definition in one sentence | The anomaly it forbids that the level below allows |
|-------|----------------------------|-----------------------------------------------------|
| Linearizability | One total order that respects real time; the system behaves as a single copy | A client reads a value that another client's acknowledged write has already replaced |
| Sequential consistency | One total order that respects each client's program order but not real time | Two clients disagree about the order of writes |
| Causal consistency | Causally related writes are seen in causal order everywhere; concurrent writes may differ | A client sees an effect (a payment) without its cause (the top-up it depended on) |
| Session guarantees | Read-your-writes, monotonic reads, monotonic writes, writes-follow-reads, for one session | A client fails to see its own write, or sees its reads go backwards |
| Eventual consistency | If writes stop, replicas converge | Nothing: any value ever written may be returned |

Strict serializability = linearizability + multi-object transactions. "Strongly consistent" in vendor documentation usually means linearizable for one key.

## Questions you will be asked, and the answer that shows understanding

1. **"Is this system strongly consistent?"** Ask back: for which operation, and across which objects? A linearizable single-key read does not make check-then-act atomic.
2. **"Why not make everything linearizable?"** One round trip to a leader or quorum per read, and unavailability when the leader is unreachable. At a 40:1 read-to-write ratio across regions, that is the latency budget of the whole product.
3. **"What is the weakest model that fixes 'I topped up and the balance did not change'?"** Read-your-writes, with a version token or sticky session. Not linearizability.
4. **"Can an eventually consistent store be used for a balance?"** For display and analytics, yes. For authorization, no: two concurrent checks can both pass. The authorization needs a linearizable read and an atomic conditional debit.
5. **"How would you prove a store is linearizable?"** Record a history (invocation, response, value per operation) and search for a total order that respects real time and makes every read return the latest write. Jepsen does this; the brute-force version is a few lines.
6. **"What is the strongest model you can offer while partitioned?"** Causal. Anything stronger must refuse or wait on at least one side of the partition.

## Decision procedure for a read in a payment flow

1. Does the result decide whether money moves? Linearizable read + atomic conditional write or transaction.
2. Will the writer see it next? Read-your-writes + monotonic reads via a version token.
3. Must several values be mutually consistent? Causal, or a single snapshot.
4. Otherwise: eventual, with replication lag measured and alerted on.

## Four histories to carry in your head

Register `x`, `W(v)` writes, `R=v` reads, brackets are real time.

- **Stale read by another client.** `A: W(100)[0,2]  B: W(70)[3,5]  D: R=100[8,9]`. Not linearizable; sequentially consistent.
- **One client's reads go backwards.** `P: W(100), W(70)  C: R=70, R=100`. Not even causal; violates monotonic reads.
- **Cannot see own write.** `C: W(70)  C: R=100`. Violates read-your-writes.
- **Concurrent writes seen in different orders.** `P: W(a)  Q: W(b)  C: R=a, R=b  D: R=b, R=a`. Causal accepts; sequential rejects.

## What the model does not tell you

- How stale "stale" is (bounded staleness is a separate promise).
- Which concurrent write wins (conflict resolution, Phase 6 Lesson 09).
- Anything about transactions over several objects (Phase 7).
