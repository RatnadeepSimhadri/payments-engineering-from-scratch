---
name: checklist-log-topic-design
description: Design-review checklist for a new log topic (Kafka, Pulsar, Kinesis, Redis Streams) that carries money-moving events
phase: 8
lesson: 1
tags: [kafka, log, partitions, retention, offsets, review]
---

# Log Topic Design Review Checklist

Use before creating a topic that carries payment events, and again before changing its partition count or retention. Each unchecked box is a question to answer in the design doc, not a reason to skip the review.

## Partition key

- [ ] The key is the entity whose events must be ordered (account, merchant, card, payment intent), not a random ID and not null.
- [ ] Every producer of this topic computes the key the same way (same field, same normalisation, same encoding). Two producers that disagree put one entity in two partitions.
- [ ] The key's cardinality is far larger than the partition count, so no single key dominates one partition. A "platform account" that appears in 40% of events is a hot partition.
- [ ] If events must be ordered across two entity types (an authorization and its capture), both carry the same key.

## Partition count

- [ ] The count is chosen for the peak consumer parallelism needed over the topic's lifetime, not today's. Increasing it later re-homes every key and breaks per-key ordering across the change.
- [ ] The count is documented with the throughput assumption behind it (events per second per partition the slowest consumer can sustain).
- [ ] A plan exists for "we need more partitions": a new topic with a cutover, not an in-place increase, if the topic is keyed.

## Retention versus consumers

- [ ] Retention (bytes and time) is longer than the longest expected consumer outage plus the time to notice and fix it. If a consumer can be down for a weekend, retention is at least a week.
- [ ] Retention is longer than the longest replay you will ever need (reprocessing after a bug fix, rebuilding a projection). If not, an archive consumer copies the topic to cold storage.
- [ ] Alerting exists for consumer lag approaching the retention boundary; a committed offset below log start resets silently to earliest or latest and either outcome is a data incident.
- [ ] Segment size is small enough that retention granularity is acceptable (retention deletes whole segments, so a 1 GiB segment on a low-volume topic may hold months).

## Offset commit discipline

- [ ] Consumers commit the next offset to read (last processed + 1), never the last processed offset.
- [ ] Commits happen after the side effect is durable (ledger row written, message published), not before. Anything polled and not committed is redelivered; the consumer handles that duplicate (idempotency key, Phase 5 Lesson 09).
- [ ] Auto-commit is disabled for any consumer that moves money; the commit is explicit and tied to the unit of work.
- [ ] `auto.offset.reset` is set deliberately (`earliest` for ledger consumers that must not skip; `latest` only for consumers where gaps are acceptable) and the choice is written down.

## Record format

- [ ] The record carries its own identifier and the producer's idempotency key so duplicates after redelivery or producer retry are detectable.
- [ ] Amounts are integer minor units with a currency code, never floats (Phase 10 Lesson 01).
- [ ] The schema has a version field and the evolution policy is agreed (Phase 8 Lesson 11).
- [ ] Record size is bounded; a record larger than the segment limit lands alone in its own segment and a record larger than the broker's max is rejected outright.

## Operations

- [ ] Someone owns the topic: a team name in the topic's metadata, with an on-call rotation that receives lag and under-replication alerts.
- [ ] There is a runbook entry for "consumer group committed offset is out of range" that explains how to decide between reset to earliest, reset to latest, and reset to a timestamp.
- [ ] Deleting or recreating the topic is gated: a recreated topic starts offsets at zero and every committed offset for every group becomes wrong.
