---
name: checklist-retry-policy
description: Review checklist for any client code that retries a call to a processor, bank, or internal service
phase: 5
lesson: 10
tags: [retries, backoff, jitter, resilience, review]
---

# Retry Policy Review Checklist

Use on any pull request that adds or changes a retry loop, a client library configuration, or an integration with an external dependency. A retry loop with an unchecked box below is a retry storm waiting for its trigger.

## Classification

- [ ] Every failure the call can return is classified as retryable or not in one place (an enum, a status-code table), not at each call site.
- [ ] Timeouts and connection resets are retried only when the request carries an idempotency key. If the dependency does not support one, the timeout is surfaced as "unknown outcome" and reconciled, not retried.
- [ ] 4xx responses other than 429 are never retried. 401 and 403 in particular stop immediately so credentials are not brute-forced.
- [ ] Business answers (card declined, insufficient funds, account closed) are not errors and are never retried. Reattempt rules from the card networks are honoured where they apply.
- [ ] 429 and `Retry-After` are honoured: the delay before the next attempt is at least what the dependency asked for.

## Schedule

- [ ] The delay is exponential with an explicit base, cap, and maximum attempt count. All three are configuration, not literals in the loop.
- [ ] The cap is applied inside the doubling loop so a large attempt count cannot overflow the delay type.
- [ ] There is no sleep after the final failed attempt.
- [ ] The worst-case total wait (sum of the capped schedule) is shorter than the deadline of the operation that contains the retry. Deadlines and retries were designed together, and the number is written in the PR description.
- [ ] Retries at this layer are not multiplied by retries at a layer above or below (client library, service mesh, caller). Only one layer retries, or the budgets are set so the product is bounded.

## Jitter

- [ ] A named jitter strategy is in use (full, equal, or decorrelated). "None" requires a written justification, such as a single client talking to a dependency built for it.
- [ ] The strategy's bounds are tested: for a sweep of seeds, every delay lies inside the documented interval.
- [ ] If full jitter is used, the attempt count is high enough that a run of short delays cannot exhaust it before a plausible outage ends. If that cannot be guaranteed, equal jitter is used instead.
- [ ] The random source is injected so the schedule is reproducible in tests from a seed.

## Budget

- [ ] A retry budget bounds retries to a fixed fraction of organic traffic (typically 10-20%). The policy's maximum attempt count alone is not the bound.
- [ ] The budget is kept in integers (millitokens), not floats.
- [ ] Budget exhaustion is a distinct failure reason that is counted in metrics, so an operator can see "retries refused" separately from "attempts exhausted".
- [ ] If the budget has a time window, there is a minimum retry floor so a quiet service can still discover that its dependency recovered.

## Evidence

- [ ] The policy was run through a storm simulation (many clients, a short outage, a capacity-limited dependency) and the peak load after recovery is below the dependency's capacity. The histogram is attached to the PR.
- [ ] Metrics exist for attempts per request, retry rate, exhaustion rate, and budget refusals, broken down by dependency.
- [ ] A runbook entry says how to turn retries down or off for this dependency during an incident without a deploy.

## Questions to ask the dependency owner

1. What is your capacity per second per client, and what happens when it is exceeded: queueing, 429, or dropped connections?
2. Do you send `Retry-After`? On which status codes?
3. Do you support an idempotency key? For how long is it remembered?
4. How long do your outages typically last? This sets the cap and the attempt count.
5. Do you already retry on your side? If so, our retries multiply yours.
