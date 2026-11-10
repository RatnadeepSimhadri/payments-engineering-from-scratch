# Phase 4: Concurrency & Runtime Internals

> Shared mutable state is where money goes missing.

12 lessons, 12 complete, ~15 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Threads and the Memory Model](01-threads-and-the-memory-model/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Hook |
|:---:|--------|:----:|:----:|------|
| 01 | [Threads and the Memory Model](01-threads-and-the-memory-model/) | Learn | ~75 min | Happens-before in the JVM, Go, and Python, and why a race is not a bug you can test for. |
| 02 | [Locks, Atomics, and a Balance That Never Lies](02-locks-and-atomic-counters/) | Build | ~90 min | Lose money with a race, then fix it three ways and measure each. |
| 03 | [Compare-and-Swap and Lock-Free Structures](03-compare-and-swap-and-lock-free-structures/) | Build | ~90 min | A lock-free stack and the ABA problem that breaks it. |
| 04 | [Concurrent Hash Maps: Striping and Lock-Free Reads](04-concurrent-hash-maps/) | Build | ~90 min | How ConcurrentHashMap and sync.Map avoid a global lock. |
| 05 | [Producers, Consumers, and Bounded Queues](05-producers-consumers-and-bounded-queues/) | Build | ~75 min | Backpressure begins with a queue that refuses to grow. |
| 06 | [Executors, Goroutines, and asyncio](06-executors-goroutines-and-async/) | Build | ~90 min | Three concurrency models, one fan-out/fan-in problem. |
| 07 | [Deadlocks, Lock Ordering, and Detection](07-deadlocks-and-lock-ordering/) | Build | ~60 min | Transfer from A to B while B transfers to A. |
| 08 | [The Actor Model](08-the-actor-model/) | Build | ~75 min | One account, one mailbox, no locks. |
| 09 | [JVM Garbage Collection and the Go Runtime](09-jvm-gc-and-go-runtime/) | Learn | ~75 min | Pause times are latency SLOs in disguise. |
| 10 | [The Python GIL and Free-Threaded Python](10-the-python-gil/) | Learn | ~45 min | What the GIL protects you from, and what it does not. |
| 11 | [Structured Concurrency and Cancellation](11-structured-concurrency-and-cancellation/) | Build | ~75 min | Context in Go, virtual threads in Java, task groups in Python. |
| 12 | [Testing Concurrent Code: Stress, Model Checking, and Determinism](12-testing-concurrent-code/) | Build | ~75 min | Make the scheduler your test input. |

Browse the [full curriculum](../../README.md#contents) or the status [roadmap](../../ROADMAP.md).
