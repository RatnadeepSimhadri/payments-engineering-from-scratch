---
name: checklist-ordering-review
description: Review checklist for any producer or consumer of a keyed topic where the order of events changes money
phase: 8
lesson: 4
tags: [kafka, ordering, keys, head-of-line, idempotent-producer, commit, review]
---

# Ordering Review Checklist

Use when reviewing a producer or consumer of a topic whose events must be applied in order (ledger postings, payment state transitions, balance changes). Pair it with Lesson 01's topic-design checklist, which covers key choice and partition count; this one covers what happens to the order after the key is chosen.

## What the key declares

- [ ] The design doc states, in one sentence, which events must be ordered relative to each other ("all events for one account", "all state changes for one payment intent"). The key is that entity's id and nothing else.
- [ ] Every event that must be ordered after another carries the same key. A capture keyed by merchant and an authorization keyed by account are not ordered, whatever the topic looks like.
- [ ] No consumer relies on order between different keys that happen to share a partition. Grep the consumer for logic that assumes "the previous record" is related to the current one.
- [ ] The partition count is treated as immutable for the topic's lifetime. If it must change, there is a migration plan (drain, switch, replay) and a date, not a config flag.

## Producer: can a retry reorder?

- [ ] `enable.idempotence=true` (or the equivalent sequence-number mechanism on your broker) is set and the broker version supports it. If it cannot be enabled, `max.in.flight.requests.per.connection=1` and the throughput cost is accepted in writing.
- [ ] Retries are enabled. A producer with `retries=0` cannot reorder, but it loses records on every transient error, which is the wrong trade for a ledger.
- [ ] The producer stamps a per-key sequence number into the event payload itself, independent of the broker's, so a consumer can detect reordering and gaps end to end (across topics, mirrors, and replays).
- [ ] Only one producer instance writes a given key at a time, or the sequence number includes a producer epoch so two writers cannot interleave counters.

## Consumer: how is the partition parallelised?

- [ ] If the consumer processes a partition on one thread, the review has a number for the slowest key's service time and for the p99 latency it imposes on the other keys in the partition.
- [ ] If the consumer uses per-key lanes (or a library that does), the lane assignment is a pure function of the key, and the test suite asserts per-key order under concurrent completion.
- [ ] The number of lanes is justified by host capacity (threads, downstream connections) and by the active-key count per partition, not copied from an example.
- [ ] Known slow keys (platform accounts, batch merchants) have a plan: their own topic, asynchronous processing with a placeholder, or an explicit lane reservation.

## Consumer: what is committed?

- [ ] The committed offset is the smallest unfinished offset plus nothing (the low-water mark), never the highest finished offset.
- [ ] There is an explicit bound on how many records may finish ahead of the mark, and the dispatcher stops polling when it is reached. The bound is the maximum redelivery after a crash, and the reviewer knows the number.
- [ ] Every record handler is idempotent (Lesson 03), because records finished ahead of the mark are redelivered after a crash.
- [ ] A crash test exists: kill the consumer with a full backlog, restart, and assert every record was applied exactly once.

## Detection

- [ ] A per-key sequence tracker runs in the consumer (or in a shadow consumer) and emits a metric for GAP and DUPLICATE verdicts.
- [ ] A GAP followed by a lower sequence for the same key within a short window pages someone; it means a reordered retry reached the log.
- [ ] Latency is measured per key class, not only per partition, so a slow key is visible as "account X is slow", not "partition 3 is slow".

## Questions to ask in the review

1. Show me the key. Show me two events that must be ordered. Do they have the same key?
2. What is `max.in.flight` and is idempotence on? Which broker version?
3. What does the consumer commit after it finishes offset 4 while offset 3 is still running?
4. What is the most records that can be redelivered after a crash, and is every handler safe under that?
5. Which key is the slowest, and what does it cost everyone else?
