# Phase 9: Reliability, Observability & Scale

> You cannot operate what you cannot observe.

13 lessons, 12 complete, ~16 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Load Balancing: Round Robin, Least Connections, Power of Two Choices](02-load-balancing-algorithms/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/09-reliability-and-scale/02-load-balancing-algorithms --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [SLIs, SLOs, and Error Budgets](01-slis-slos-and-error-budgets/) | Learn | ~60 min | 🚧 | Availability is a number you choose and then pay for. |
| 02 | [Load Balancing: Round Robin, Least Connections, Power of Two Choices](02-load-balancing-algorithms/) | Build | ~90 min | ✅ | Two random choices beat one by an exponential margin. |
| 03 | [Caching Strategies: Cache-Aside, Write-Through, Write-Behind](03-caching-strategies/) | Build | ~90 min | ✅ | Stale balances are worse than slow balances. |
| 04 | [Cache Invalidation, Stampedes, and Request Coalescing](04-cache-invalidation-and-stampedes/) | Build | ~75 min | ✅ | A hot key expires and ten thousand requests hit the database. |
| 05 | [Distributed Rate Limiting](05-distributed-rate-limiting/) | Build | ~90 min | ✅ | Token buckets across nodes without a coordinator. |
| 06 | [Metrics: Counters, Gauges, and Histograms Without Averages](06-metrics-and-histograms/) | Build | ~75 min | ✅ | p99 latency from a histogram you built. |
| 07 | [Distributed Tracing and Context Propagation](07-distributed-tracing/) | Build | ~90 min | ✅ | Follow one payment across six services. |
| 08 | [Structured Logging and Log Correlation](08-structured-logging/) | Build | ~60 min | ✅ | Every log line carries the payment ID or it is noise. |
| 09 | [Chaos Engineering and Fault Injection](09-chaos-engineering/) | Build | ~75 min | ✅ | Kill the processor connection on purpose, in test. |
| 10 | [Capacity Planning and Queueing Theory](10-capacity-planning-and-queueing-theory/) | Learn | ~75 min | ✅ | Little's Law predicts your queue depth before you build it. |
| 11 | [Multi-Region Architecture and Disaster Recovery](11-multi-region-and-disaster-recovery/) | Learn | ~90 min | ✅ | RPO, RTO, and the active-active ledger problem. |
| 12 | [Incident Response and Blameless Postmortems](12-incident-response/) | Learn | ~60 min | ✅ | The runbook you write before the pager goes off. |
| 13 | [Graceful Degradation and Load Shedding](13-graceful-degradation/) | Build | ~75 min | ✅ | Keep accepting payments when fraud scoring is down. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
