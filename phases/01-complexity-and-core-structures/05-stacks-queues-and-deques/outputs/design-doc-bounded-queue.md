---
name: design-doc-bounded-queue
description: Design-review template for any bounded producer/consumer handoff (in-process queue, ring buffer, broker topic)
phase: 1
lesson: 5
tags: [queues, ring-buffer, backpressure, design-review, messaging]
---

# Bounded Queue Design Review

Fill in every section before a queue, ring, or topic goes to production. If a section is "not applicable", write why. The questions are the ones the incident review will ask.

## 1. What flows through it

- Element type and typical size in bytes:
- Producers (how many, what rate at p50 and at peak):
- Consumers (how many, what rate when healthy, what rate when degraded):
- Is an element a **command** (must not be lost: a payment instruction, a ledger posting) or a **sample** (latest supersedes older: a metric, a price, a position)?

## 2. Capacity

- Capacity in elements and in bytes:
- Time to fill at peak producer rate with consumers stopped:
- Why this number and not 10x smaller or 10x larger:
- Is capacity fixed at construction? If it can grow, what is the hard ceiling and what happens there?

## 3. Overflow policy

Choose one and justify:

- [ ] **REJECT** (offer returns false). Required for commands.
- [ ] **DROP_OLDEST** (overwrite; `dropped` counter advances). Acceptable only for samples.
- [ ] **BLOCK** (producer waits, with a timeout of ___ ms). In-process handoff only; never on a request thread without a timeout.

What the producer does when rejected or timed out:

- [ ] retries with backoff (max attempts: ___, max total wait: ___)
- [ ] returns an error to its own caller (which one, and is it retriable)
- [ ] sheds load by category (which categories first)
- [ ] persists to a durable fallback (where, and who drains it)

## 4. Ordering and identity

- Are elements consumed strictly in production order? If multiple consumers, is order per key or global?
- Does each element carry a sequence number or offset that survives beyond the buffer? Who stores it?
- Can a consumer detect a gap (sequence jumped, `dropped > 0`)? What does it do about it?

## 5. Consumer failure

- A consumer stops for 5 minutes. What is in the buffer when it returns, and what was lost?
- A consumer's stored position is below the retention window. Does it reset to earliest, latest, or fail loudly? Who decides?
- Lag threshold that pages someone: ___ elements or ___ seconds behind.

## 6. Observability

- [ ] size (or lag) exported as a gauge
- [ ] rejections per second (REJECT) or drops per second (DROP_OLDEST) exported as a counter
- [ ] oldest element age exported as a gauge
- [ ] head and tail sequences logged on every restart

## 7. Concurrency

- Single producer and single consumer, or many? Which synchronization makes `head` and `tail` safe (lock, CAS, volatile with ordering)?
- Is the structure ever accessed from a request thread that cannot block?

## 8. Test evidence

- [ ] A test fills the buffer and asserts the overflow policy behaves as specified.
- [ ] A test wraps around at least twice and asserts order and contents.
- [ ] A test stops the consumer, drives the producer past capacity, resumes the consumer, and asserts exactly what was lost or rejected.
- [ ] A model-based test compares the structure against a reference deque under random operations.

## Sign-off

| Role | Name | Date | Notes |
|------|------|------|-------|
| Author | | | |
| Reviewer (owning team) | | | |
| Reviewer (consuming team) | | | |
