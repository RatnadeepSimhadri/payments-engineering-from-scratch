---
name: design-doc-capacity-plan
description: Capacity plan template for a latency-sensitive service, with every input sourced and every rule stated
phase: 9
lesson: 10
tags: [capacity, queueing, littles-law, planning, headroom]
---

# Capacity Plan: <service>

**Owner:** <name>   **Horizon:** <months>   **Review date:** <date>   **Supersedes:** <previous plan>

## 1. Inputs (every row has a source and a date)

| Quantity | Value | Source | Measured on |
|----------|-------|--------|-------------|
| Average throughput (30 d) | ___ TPS | request counter | |
| Daily peak throughput | ___ TPS | p99.9 of per-second counts, 30 d | |
| Peak-to-average ratio | ___ x | derived | |
| Seasonal factor | ___ x | last year's peak histogram | |
| Growth over horizon | ___ % | 12-month volume trend | |
| Single-instance capacity at SLO | ___ TPS at p99 ___ ms | open-loop step test with real dependencies | |
| Mean residence time at that load | ___ ms | latency histogram mean | |
| Thread pool size per instance | ___ | config | |
| DB connection pool per instance | ___ | config | |
| Mean connection hold time | ___ ms | pool metrics | |
| Failure unit | 1 zone of ___ / 1 instance | deployment | |
| Smallest downstream limit | ___ TPS (<which dependency>) | contract or vendor doc | |

## 2. Arithmetic

```text
design peak                = daily peak x seasonal x (1 + growth)       = ___ TPS
instances at target rho    = design peak / single-instance capacity     = ___
with failure unit lost     = instances x (zones / (zones - 1))          = ___ -> ___ (divisible by zones)
threads in flight          = design peak x residence time                = ___ fleet-wide, ___ per instance
connections in flight      = design peak x connection hold time          = ___ fleet-wide, ___ per instance
fan-out check              = 1 - p^n for the widest fan-out              = ___ % exceed per-dependency percentile
chain check                = design peak vs smallest downstream limit    = ___ % headroom
```

## 3. Rules applied

- [ ] Target utilization for this service: ___ % of measured saturation (latency-sensitive: 50-60%; batch: 80-90%).
- [ ] Failure headroom: capacity holds with the largest failure unit gone.
- [ ] Per-dependency latency SLOs set at the percentile the fan-out requires (p99 end-to-end over n dependencies needs p(1 - 0.01/n) each, approximately).
- [ ] No pool is above 50% mean occupancy at design peak.
- [ ] Chain headroom at least ___ %, or a written agreement to raise the downstream limit before the horizon.

## 4. Decision

- Fleet: ___ instances, ___ per zone, by <date>.
- Alerts: per-instance throughput at ___ TPS (60% of measured capacity), pool occupancy at 50%, downstream 429 rate above ___ %.
- Not planned on: CPU percentage.

## 5. Triggers to recompute this plan

- Any dependency's p99 changes by more than 20% (residence time changes every pool number).
- A new fan-out is added to the request path.
- The downstream limit, zone count, or growth trend changes.
- The single-instance saturation test has not been re-run in ___ days.

## 6. Risks and the thinnest number

State which row in the arithmetic has the least margin and what happens if it is wrong by 25%.
