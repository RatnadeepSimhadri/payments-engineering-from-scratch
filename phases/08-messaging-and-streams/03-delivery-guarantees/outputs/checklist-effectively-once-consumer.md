---
name: checklist-effectively-once-consumer
description: Review checklist for any consumer that performs a side effect from a log or queue; rejects designs with an unclosed redelivery window
phase: 8
lesson: 3
tags: [delivery, idempotency, transactions, kafka, review]
---

# Effectively-Once Consumer Review Checklist

Use on every pull request that adds or changes a consumer whose side effect moves money, sends a message, or writes to a system of record. A design that cannot answer a question here has a window a crash will find.

## Name the writes

- [ ] Every durable write the consumer performs per record is listed, with the store it goes to (ledger table, offset store, cache, external API).
- [ ] The order of those writes is stated explicitly, including where the offset commit sits relative to the side effect.
- [ ] If the writes go to more than one store, the design states which of the two routes below closes the window between them.

## Route one: idempotent effect

- [ ] The side effect is keyed by a business identifier of the real-world event (payment id, transfer id), not by the log offset or a per-attempt UUID.
- [ ] The processed-key check and the effect are one operation on one store (unique constraint, insert-if-absent, conditional write), not a lookup in one store followed by a write to another.
- [ ] Keys are retained at least as long as a redelivery or a producer retry is possible, and the retention is written down.
- [ ] Calls to external payment APIs pass an idempotency key derived from the business identifier, and the same key is reused on every retry.

## Route two: transactional progress

- [ ] The consumer's offset (or progress marker) is stored in the same store as the effect and committed in the same transaction.
- [ ] On startup the consumer seeks to the stored offset rather than trusting the broker's committed offset.
- [ ] Every side effect of the consumer lives in that store. Anything that does not (email, webhook, another service) goes through an outbox or is independently idempotent.
- [ ] If the effect is a write to another topic, the producer is transactional and the offset commit is part of the transaction with the consumer group metadata (fencing).

## Duplicates on the log

- [ ] The design states what happens when the same event appears twice at two offsets (producer retry). Offset-based mechanisms alone do not handle this.
- [ ] The producer is idempotent (sequence numbers) or the consumer applies route one regardless of route two.

## The crash test

- [ ] There is a test that kills the consumer after the side effect and before the commit, at a chosen record, restarts it, and asserts zero duplicates and zero losses by business key.
- [ ] The test sweeps the crash point across at least one full batch, including the first and last record of the batch.
- [ ] The test asserts the money total, not just record counts.

## Metrics to add

- [ ] A counter for redeliveries detected (route one returning "already processed").
- [ ] A counter for transactions rolled back on restart (route two).
- [ ] An alert when either counter is non-zero for longer than a deploy takes, because a steady rate means a producer is retrying or a consumer is crash-looping.
