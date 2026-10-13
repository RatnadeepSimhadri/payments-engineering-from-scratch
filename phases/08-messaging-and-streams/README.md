# Phase 8: Messaging, Streams & Event-Driven Systems

> The log is the database turned inside out.

11 lessons, 4 complete, ~15 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [The Append-Only Log: Partitions, Offsets, and Segments](01-the-append-only-log/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/08-messaging-and-streams/01-the-append-only-log --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [The Append-Only Log: Partitions, Offsets, and Segments](01-the-append-only-log/) | Build | ~120 min | ✅ | Build a Kafka-shaped log with files and offsets. |
| 02 | [Consumer Groups, Offsets, and Rebalancing](02-consumer-groups-and-rebalancing/) | Build | ~90 min | ✅ | Who owns partition 7 after a consumer dies? |
| 03 | [At-Most-Once, At-Least-Once, and Effectively-Once](03-delivery-guarantees/) | Build | ~90 min | ✅ | Exactly-once delivery is impossible; exactly-once effects are engineering. |
| 04 | [Message Ordering, Keys, and Head-of-Line Blocking](04-message-ordering-and-keys/) | Build | ~60 min | 🚧 | Order per account, parallelism across accounts. |
| 05 | [Event Sourcing: State as a Fold Over Events](05-event-sourcing/) | Build | ~120 min | ✅ | An account is the sum of its events; a balance is a cache. |
| 06 | [CQRS and Projections](06-cqrs-and-projections/) | Build | ~75 min | 🚧 | Rebuild a read model from the log in one command. |
| 07 | [Stream Processing: Tumbling, Sliding, and Session Windows](07-stream-processing-windows/) | Build | ~90 min | 🚧 | Transactions per card per minute, computed as they arrive. |
| 08 | [Watermarks, Event Time, and Late Data](08-watermarks-and-late-data/) | Build | ~90 min | 🚧 | The event happened at 11:59; it arrived at 12:03. Which window? |
| 09 | [Backpressure and Flow Control](09-backpressure/) | Build | ~75 min | 🚧 | The fast producer, the slow consumer, and the queue in between. |
| 10 | [Dead-Letter Queues and Poison Messages](10-dead-letter-queues-and-poison-messages/) | Build | ~60 min | 🚧 | One malformed event must not stall every account behind it. |
| 11 | [Schema Registries and Compatible Evolution](11-schema-registry-and-evolution/) | Learn | ~60 min | 🚧 | Add a field without breaking last month's consumer. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
