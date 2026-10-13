---
name: checklist-flow-control
description: Design-review checklist for any queue, buffer, channel, or thread pool between two components with independent rates
phase: 8
lesson: 9
tags: [streams, backpressure, queues, capacity, review]
---

# Flow Control Review Checklist

Use when a design document or pull request introduces a queue, buffer, channel, executor, or any hand-off between a producer and a consumer that do not run at the same speed. Every unchecked box is a question the author must answer in writing.

## The rates

- [ ] The expected producer rate and consumer rate are stated with numbers (per second, p50 and peak), and the source of each number is named (load test, production metric, contract).
- [ ] The longest burst the system must absorb without throttling is stated in seconds and in events.
- [ ] The design says what happens when the producer rate exceeds the consumer rate for longer than that burst. "It won't" is not an answer.

## The buffer

- [ ] The buffer has an explicit capacity. No `LinkedBlockingQueue()`, no `Executors.newFixedThreadPool`, no `make(chan T)` with an unexamined size, no `deque()` without a policy.
- [ ] The capacity is derived from a latency budget (`depth / consumer rate = maximum staleness`) and the budget is written next to the number.
- [ ] The memory cost of a full buffer is computed (capacity times event size) and is acceptable.
- [ ] Queue depth and maximum depth are exported as metrics.

## The overflow policy

- [ ] The policy when the buffer is full is named: block, drop newest, drop oldest, or shed at the edge. It appears in the code as a named constant or type, not as a side effect of a library default.
- [ ] If the policy drops data, the data has been classified as droppable (samples, latest-value state) and the owner of the data has agreed. Money, orders, and anything reconciled are never droppable.
- [ ] Every drop increments a counter with an alert threshold. For reconciled data the threshold is one.
- [ ] If the policy blocks, the block has a timeout and the design says what happens when it fires.

## Where the pressure goes

- [ ] The design traces the backpressure signal upstream to the component that will act on it (HTTP handler, batch reader, Kafka consumer) and says what that component does: `429` with `Retry-After`, slower accept, smaller batches, pause polling.
- [ ] If the ultimate producer cannot be slowed (an external party pushing files), the buffer is a durable tier (log, WAL, object store) whose retention is the capacity, and the consumer pulls from it.
- [ ] Callers that receive a throttle signal back off with jitter; a retry storm has been considered.
- [ ] The dependency graph has been checked for cycles in which two components could block on each other.

## Pull where possible

- [ ] Where the framework supports demand signalling (Reactive Streams `request(n)`, Kafka `poll`, gRPC streaming, TCP windows), the consumer requests what it can take rather than the producer pushing until refused.
- [ ] No code path calls `request(Long.MAX_VALUE)` or its equivalent without a comment explaining why unbounded demand is safe here.
- [ ] The consumer requests exactly the capacity it has freed, not a fixed amount per interval.

## Tests

- [ ] A test runs the pipeline with the producer sustainably faster than the consumer and asserts bounded queue depth, bounded latency, and zero loss (or counted loss, for droppable data).
- [ ] A test asserts the conservation identity: offered equals admitted plus throttled; admitted equals delivered plus dropped plus in flight plus queued.
- [ ] A test runs the pipeline with the consumer faster than the producer and asserts the bounded design delivers everything with no throttling, so backpressure is shown to cost nothing in the normal case.

## Sign-off

| Question | Answer | Owner |
|----------|--------|-------|
| Capacity and the latency budget it encodes | | |
| Overflow policy | | |
| Component that acts on backpressure, and how | | |
| Alert threshold on drops or throttles | | |
