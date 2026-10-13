---
name: runbook-rebalance-incident
description: On-call runbook for consumer-group symptoms (lag on a subset of partitions, rebalance storms, duplicates after a deploy) with the coordinator facts to check first
phase: 8
lesson: 2
tags: [kafka, consumer-group, rebalance, on-call, runbook]
---

# Consumer Group Incident Runbook

For any consumer group that processes money-moving events. Each symptom lists what to look at before changing anything, because the usual first move (raising a timeout) often makes the next incident worse.

## Symptom: lag climbing on some partitions, flat on others

Facts to gather:

- [ ] Current generation and assignment (`kafka-consumer-groups --describe`, or the coordinator's history). Which member owns the lagging partitions?
- [ ] Is that member alive but not polling? Check its last `poll` time against `max.poll.interval.ms`. A member that heartbeats but does not poll holds its partitions while doing nothing.
- [ ] Is that member stuck on one record? Look for a repeated error at the same offset. That is a poison message (Lesson 10), not a group problem.

Fix: if the member is stuck on a record, park the record and move on. If the member is wedged, kill it; the session timeout will reassign its partitions. Do not add members: with range or sticky assignment the lagging partitions may stay where they are.

## Symptom: rebalance storm (generation increasing every few seconds)

Facts to gather:

- [ ] Generation history: which member is joining and leaving repeatedly?
- [ ] Observed GC pauses and network blips for that member versus `session.timeout.ms`. If the pauses exceed the timeout, the member is evicted, re-joins, is evicted again.
- [ ] Batch processing time versus `max.poll.interval.ms`. A slow batch looks like a crash to the coordinator.
- [ ] Is a deploy in progress? A rolling restart of M members with the eager protocol is 2M rebalances by design.

Fix, in order of preference: make the slow thing fast; move heartbeats off the processing path (Kafka does this since 0.10.1); enable static membership (`group.instance.id`) so restarts do not rebalance; switch to the cooperative sticky assignor so the partitions that are not moving keep processing. Raising `session.timeout.ms` is last, because it lengthens the time a genuinely dead member holds its partitions.

## Symptom: duplicate side effects after a rebalance

Facts to gather:

- [ ] Which partitions moved in the rebalance (compare assignments across the two generations)?
- [ ] For each moved partition, the committed offset at the moment of the move versus the last offset the old owner had processed. The gap is the redelivery window.
- [ ] Did the old owner keep running after eviction? Look for `ILLEGAL_GENERATION` or `UNKNOWN_MEMBER_ID` in its logs: those are its rejected commits, and every record it processed before them was processed twice.

Fix: the duplicates are expected under at-least-once delivery. The consumer must be idempotent (Lesson 03). If it is not, this incident is a bug in the consumer, not in the group.

## Questions to answer in the postmortem

1. What is the worst pause this consumer can experience (GC, dependency timeout, deploy), and is the session timeout above it?
2. How long can a partition sit unowned before money is late? Is the session timeout below that?
3. Which assignor is configured, and how many partitions does one member change move?
4. Is every side effect this consumer performs idempotent under redelivery of the last uncommitted batch?
5. Does the deploy process use static membership, and if not, how many rebalances does one deploy cause?

## Settings to record in the service's runbook

| Setting | Value | Reason |
|---------|-------|--------|
| `session.timeout.ms` | | Above worst observed pause, below acceptable unowned time |
| `heartbeat.interval.ms` | | At most one third of the session timeout |
| `max.poll.interval.ms` | | Above the slowest batch ever observed, with margin |
| `partition.assignment.strategy` | | Cooperative sticky unless co-partitioned joins need range |
| `group.instance.id` | | Set per instance for deploys without rebalances |
