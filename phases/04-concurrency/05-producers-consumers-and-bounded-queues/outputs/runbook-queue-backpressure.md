---
name: runbook-queue-backpressure
description: On-call runbook for a queue-depth or blocked-producer alert on any bounded in-process queue, channel, or producer buffer
phase: 4
lesson: 5
tags: [concurrency, queues, backpressure, runbook, on-call]
---

# Queue Backpressure Runbook

For the engineer paged on `queue_depth_high`, `producer_blocked_time`, or `requests_rejected_429`. Works for an in-process `BoundedBlockingQueue`, `ArrayBlockingQueue`, `queue.Queue`, a buffered Go channel, or a Kafka producer buffer; the numbers have different names but mean the same things.

## 1. Read the four numbers first

| Number | This lesson's name | What it tells you |
|--------|--------------------|-------------------|
| Current depth vs capacity | `size()` / `capacity()` | At capacity: producer is being held or refused right now. Near zero: the consumer is keeping up; the alert may be stale. |
| High-water mark | `stats().highWaterMark` | Has ever equalled capacity: the bound has done its job at least once. Never near capacity: the bound is not involved; look elsewhere. |
| Producer waits / blocked time | `stats().producerWaits`, `waitingProducers` | Rising: the consumer is slower than the producer. `waitingProducers > 0` for long: producer threads are pinned; check what else they hold. |
| Consumer waits | `stats().consumerWaits`, `waitingConsumers` | Rising while depth is at capacity: impossible; check for a bug. Rising while depth is zero: consumers are starved; the producer is the bottleneck or has stopped. |

If you have none of these numbers, the first remediation is to add them. A queue without a high-water mark cannot be sized.

## 2. Decide: slow consumer or producer burst?

- **Slow consumer**: depth at capacity for longer than the consumer's normal processing time of `capacity` items; downstream latency (database, ledger, network) elevated at the same time. Go to section 3.
- **Producer burst**: depth rose and fell within seconds; downstream latency normal; producer-side rate metric shows a spike. Go to section 4.
- **Consumer stopped**: depth at capacity, consumer throughput zero, no downstream errors. Thread dump or goroutine dump; look for a consumer blocked on something other than the queue (a lock, a connection pool, an unbounded downstream queue). This is Lesson 07 territory if two consumers wait on each other.

## 3. Slow consumer

1. **Confirm the downstream is the cause.** Latency of the consumer's dependency (p99 of the ledger write, for example) should track the depth. If the dependency is healthy, profile the consumer; the slowness is local.
2. **Do not raise the capacity first.** A larger bound buys seconds of burst absorption at the cost of memory and of a longer recovery tail. It does not change throughput.
3. **Add consumer parallelism** if the downstream can take it and ordering allows it (per-key ordering survives if each key maps to one consumer; global FIFO does not).
4. **Shed at the edge** if the downstream cannot take more: switch the admission boundary from `put` to `offer(item, budget)` or `offer(item, 0)` with a `429`/`503` plus `Retry-After`, so clients retry instead of your heap absorbing the backlog.
5. **Check what blocked producers are holding.** A producer parked in `put` while holding a database connection or a lock turns backpressure into a pool exhaustion or a deadlock elsewhere. Release before you block, or use a timed offer.

## 4. Producer burst

1. Verify the burst is legitimate (promotion, batch replay, retry storm). A retry storm from a client with no backoff shows as a burst that repeats at a fixed interval; fix the client's backoff or rate-limit the client.
2. If bursts of this size are expected, size the capacity to the burst: `capacity >= burst_rate * burst_duration - consumer_rate * burst_duration`, rounded up. Document the formula next to the constant.
3. If bursts are not expected, keep the capacity and keep the refusals; they are the system working.

## 5. Unbounded-queue symptoms (recognize before the heap dump)

- Heap grows monotonically while input rate is flat; GC frequency rises; consumer throughput falls as GC pauses lengthen (a positive feedback loop).
- No `429`/`503` anywhere in the path; every upstream reports success while the downstream is hours behind.
- `LinkedList`, `ArrayList`, `ConcurrentLinkedQueue`, `list.append`, or `make(chan T)` with no capacity between a producer and a slower consumer.

Remediation: introduce a bound with an explicit policy (block / timed / refuse) at the point where the producer can act on it, and add the four numbers from section 1.

## 6. After the incident

- [ ] High-water mark and blocked-time metrics exported for every bounded queue in the path.
- [ ] Each queue boundary has a written policy: block, timed offer with a stated budget, or refuse with `Retry-After`.
- [ ] Capacity constants have a comment with the sizing formula and the date it was last checked.
- [ ] A test holds the consumer back until the producer is blocked and asserts the high-water mark equals the capacity (not a sleep; a spin on the queue's own waiting count).
- [ ] If the consumer can stop permanently (shutdown, poison pill), blocked producers and consumers have a way out (timeout, interrupt, closed channel, cancellation).
