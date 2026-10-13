---
name: template-pre-mortem-fallacies
description: Fill-in pre-mortem for any service that makes a network call that moves money, one row per fallacy of distributed computing
phase: 5
lesson: 1
tags: [distributed-systems, review, pre-mortem, fallacies, payments]
---

# Pre-Mortem: Fallacies of Distributed Computing

Assume the service has already caused an incident. Work out which assumption broke before it does. Copy this file into the design review, fill in every row, and attach the result to the launch ticket.

**Service:** `<name>`
**Owner:** `<team>`
**Reviewed on:** `<date>`
**Network hops on the critical path:** `<list every HTTP call, DB query, cache read, publish, DNS lookup, including those inside libraries>`

## Procedure

1. Draw the calls. Every network hop on the critical path, including the hidden ones (DNS, connection pools, client libraries).
2. Walk the eight rows below for each hop. Write what the code assumes. If you cannot point at the line or setting that encodes the assumption, it is a default, and defaults are where fallacies live.
3. Name the failure in terms of money or availability.
4. Rank by severity times likelihood using the rubric at the end.
5. Turn the top rows into tickets with an acceptance test that fails today.

## The eight rows

| # | Fallacy | Question to ask | Where to look in the code | What this service assumes | Failure if the assumption breaks | Severity | Likelihood |
|---|---------|-----------------|---------------------------|---------------------------|----------------------------------|----------|------------|
| 1 | The network is reliable | On a timeout, is the retry safe? Is the idempotency key created and persisted before the first attempt and reused on every retry? Is a timeout ever recorded as "failed"? | Retry policy; `Idempotency-Key` header; outbox or intent table; timeout handler | | | | |
| 2 | Latency is zero | Does every hop have a timeout shorter than the caller's deadline? Is the deadline propagated? How many sequential hops are on the critical path? | HTTP client builder; per-request `timeout()`; deadline header; call graph | | | | |
| 3 | Bandwidth is infinite | What is the largest payload, how fast is it growing, and what shares the link? Is anything read fully into memory? | Batch jobs; replication config; payload size logs; file readers | | | | |
| 4 | The network is secure | Is every inbound message authenticated by something other than its URL or source IP? Are webhook signatures verified with a timestamp window? | Webhook handler; service-to-service auth; secrets handling | | | | |
| 5 | Topology does not change | Where are addresses cached and for how long? What does a stale leader or lock holder check before writing? | DNS resolution; connection pool; lock or lease code; failover handling | | | | |
| 6 | There is one administrator | Which partner-side changes would we only learn about from a rising error rate? Who owns each external boundary's alert? | Dashboards per partner; certificate expiry alerts; contract versions | | | | |
| 7 | Transport cost is zero | How many connections are opened per second? How many times is each payload re-encoded? | Connection pool settings; serialization code; CPU profile | | | | |
| 8 | The network is homogeneous | For each partner, which exact bytes do they send for amounts, timestamps, identifiers, and how is that converted to our canonical form? | Parsers; adapters; `Money` and timestamp boundaries | | | | |

## Ranking rubric

**Severity**

- `S1` A money movement can be duplicated, lost, or recorded against the wrong account.
- `S2` The service can become unavailable for the duration of a partner or network fault.
- `S3` Latency or cost degrades; no money or availability impact.
- `S4` Note for later; no action required before launch.

**Likelihood**

- `L1` Triggered by a routine fault rate (0.1 to 1% of calls) or by a scheduled event (deploy, failover, partner maintenance).
- `L2` Triggered by an uncommon but expected event (region failover, certificate rotation, partner API change).
- `L3` Requires an active attacker or a rare coincidence.

Any row rated `S1/L1` or `S2/L1` blocks launch until it has a ticket and a test.

## Tickets

| Row | Ticket | Acceptance test (must fail today) | Owner |
|-----|--------|-----------------------------------|-------|
| | | | |
| | | | |

## Sign-off

- [ ] Every row has an assumption written down, even if it is "none, this hop does not apply".
- [ ] Every `S1` and `S2` row at `L1` has a ticket with an acceptance test.
- [ ] The idempotency key for every money-moving call is generated and persisted before the first attempt.
- [ ] Every network hop has a bounded timeout and the deadline is propagated downstream.
- [ ] Every inbound webhook or callback is authenticated by signature, not by URL.
