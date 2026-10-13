---
name: checklist-latency-metrics
description: Review checklist for a service's metrics and dashboards, focused on latency reporting that reflects what customers experience
phase: 9
lesson: 6
tags: [metrics, histograms, percentiles, prometheus, slo, observability]
---

# Latency Metrics Review Checklist

Use when reviewing a new service's instrumentation, a dashboard, or an SLO definition. The incident this checklist exists to prevent is a green dashboard over a service that is losing customers in its tail.

## Metric types

- [ ] Every metric is one of counter, gauge, or histogram, and the type matches the operations the code performs on it. A counter is never decremented; a rate is never stored in a counter.
- [ ] Counters end in `_total` and are exported as raw totals; rates are computed by the scraper.
- [ ] Gauges that are not additive across hosts (a percentage, a ratio, a per-host temperature) are not summed in any dashboard query.
- [ ] Every duration and size is a histogram, not a gauge of the last value and not a counter of the sum alone.

## Histograms

- [ ] Latency histograms use a log-linear or exponential layout (HdrHistogram, OpenTelemetry exponential, Prometheus native) with a stated precision, or classic fixed boundaries chosen so that every SLO threshold is a boundary.
- [ ] The histogram's maximum trackable value is above any latency the service can produce (at least the client timeout), and the behaviour for values above it (reject, clamp, auto-resize) is known.
- [ ] Units are in the metric name (`_micros`, `_millis`, `_seconds`) and are consistent across the service.
- [ ] If histograms are shipped from hosts to a central store, the layout is identical on every host so they can be merged.
- [ ] Rolling-window percentiles are computed by merging per-interval histograms, not by averaging per-interval percentiles.

## Dashboards and queries

- [ ] No latency panel shows a mean as its primary number. If a mean appears, a p50, p99, and p99.9 appear beside it.
- [ ] Fleet-wide percentiles are computed by merging histograms (`histogram_quantile(sum by (le) (...))` or equivalent), never by averaging per-host percentiles.
- [ ] SLO attainment is computed as "count at or below threshold / total count" from the histogram, and the threshold aligns with a bucket boundary.
- [ ] Rate queries over counters use a function that handles resets (`rate`, `increase`), not a raw difference.
- [ ] The dashboard has a per-host p99 panel so one slow host is visible even when the fleet p99 is within SLO.

## Measurement

- [ ] Latency is measured where the customer experiences it (at the edge or in the client), not only inside the handler after queueing.
- [ ] Load tests are checked for coordinated omission: the generator sends on a schedule independent of response times, or the tool corrects for it.
- [ ] The histogram is recorded on every request path including errors and timeouts; a timed-out request contributes its timeout value, not nothing.

## Alerts

- [ ] Latency alerts fire on a percentile or an SLO burn rate, never on a mean.
- [ ] An alert exists for the histogram's maximum observed value exceeding the client timeout, which indicates requests the client gave up on.
- [ ] A counter that stops increasing (zero rate) on a path that should always have traffic has its own alert.

## Questions to ask the author

1. One customer in a hundred waits two seconds and everyone else waits 12 ms. Which panel on this dashboard changes, and by how much?
2. The fleet p99 is within SLO. Can one host be at a p99 of 5 seconds? Where would you see it?
3. The process restarts. What does the request-rate graph show for the next scrape interval?
