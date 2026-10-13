---
name: checklist-load-balancer-config
description: Review checklist for a load balancer, reverse proxy, or service-mesh routing configuration in front of a latency-sensitive service
phase: 9
lesson: 2
tags: [load-balancing, health-checks, p2c, consistent-hashing, envoy, nginx]
---

# Load Balancer Configuration Review Checklist

Use when a pull request touches an nginx `upstream`, an HAProxy `backend`, an Envoy cluster, a gRPC client policy, or a Kubernetes Service in front of anything with a latency SLO. The incident this checklist exists to prevent is one slow instance taking the fleet's p99 with it while every dashboard stays green.

## Strategy

- [ ] The strategy is named explicitly in the config. No reliance on a default that a version upgrade could change.
- [ ] If the strategy is round robin or weighted round robin, the document states why the fleet is assumed homogeneous and what happens during a rolling deploy, a GC pause, or a noisy-neighbour event. If it cannot, use least request or P2C.
- [ ] If the strategy is least connections or least request, the document states how many balancer instances share the fleet and whether their load views are local or shared. Above a handful of balancers, prefer P2C (`choice_count: 2`).
- [ ] If the strategy is a hash (ring hash, Maglev, `hash ... consistent`), the document states the affinity requirement that justifies it, the key, and what bounds the load on a hot key. Hashing is never chosen "to spread load".
- [ ] Weights, if any, reflect measured capacity and have an owner who updates them when instance types change.

## Health checking

- [ ] Passive checks (outlier detection) are on, with a consecutive-failure threshold stated (typically 3 to 5) and the failure budget it implies per balancer per outage computed.
- [ ] Which responses count as failures is stated: connection errors and 5xx yes; 4xx no; timeouts yes with the timeout value given.
- [ ] Active probes exist, hit an endpoint that exercises the dependency the service needs most, and run at an interval short enough to readmit within the SLO window.
- [ ] A readmitted backend ramps (slow start) rather than receiving full traffic at once.
- [ ] The behaviour when every backend is evicted is stated: fail fast, serve from a fallback, or route to the unhealthy set anyway (panic threshold). "Route anyway" needs an alert.
- [ ] Eviction and readmission are logged and counted. A flapping backend shows up as a metric, not as a mystery in a latency graph.

## Limits and timeouts

- [ ] Per-backend max in-flight requests (or connections) is set, and what happens above it (queue, shed, spill to another backend) is stated.
- [ ] Request timeout at the balancer is shorter than the client's timeout and longer than the service's p99.9 under normal load.
- [ ] Retries at the balancer, if any, are bounded, idempotency-aware, and budgeted. A balancer that retries a payment POST on timeout is a double-charge machine.

## Observability

- [ ] Per-backend metrics exist for in-flight count, assigned requests, p50/p99 latency, and error rate, labelled by backend id and visible on one panel so a single slow instance stands out.
- [ ] A fleet-level panel shows the max per-backend queue depth, not just the mean. The slow-server incident is invisible in the mean until it is catastrophic.
- [ ] Balancer-level metrics exist for evictions, readmissions, shed requests, and panic-mode activations.

## Tests present in the PR or runbook

- [ ] A load test with one instance artificially slowed (CPU throttled, or a sleep injected) shows the fleet p99 staying within SLO.
- [ ] A test that kills one instance mid-load shows the failure count bounded by the consecutive-failure threshold times the number of balancers.
- [ ] A test that restores the instance shows it readmitted within the probe interval times the success threshold, with no p99 spike on readmission.

## Questions to ask the author

1. One instance is twice as slow as the rest starting now. Which line in this config notices, and how long does it take?
2. Every balancer instance thinks the same backend is the emptiest. What stops them all sending to it at once?
3. The health endpoint returns 200 while the service's database connection is dead. What routes traffic away from it?
