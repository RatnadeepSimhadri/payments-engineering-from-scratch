---
name: runbook-routing-and-retries
description: Review checklist and incident runbook for a multi-acquirer payment router (ranking, cascade codes, retry budget, circuit breakers)
phase: 12
lesson: 4
tags: [routing, cascading, retries, decline-codes, circuit-breaker, runbook]
---

# Payment Router: Review Checklist and Runbook

## Before enabling a new processor

- [ ] Eligibility is explicit: networks, currencies, card types, countries, MCCs, and any amount limits are in configuration, not in code branches.
- [ ] The fee schedule is entered in basis points plus a fixed minor-unit fee, and the fee for a 1.00, 10.00, 100.00 and 1,000.00 payment has been checked by hand against the contract.
- [ ] The processor's raw response codes are mapped to network response codes, and every mapped code appears in the decline classification table. Unmapped codes classify as HARD.
- [ ] The prior for its approval rate is stated (rate and weight) and justified. A new processor with no history should not start above the best existing processor.
- [ ] Exploration traffic for the new processor is capped and has an owner who reviews it daily for the first two weeks.
- [ ] The processor supports status inquiry or reversal by our reference, and the PENDING_UNKNOWN path has been tested end to end against its sandbox.

## Before adding a code to the cascade list

- [ ] The code is a path failure (the issuer was not reached, or the decline is attributable to the acquirer), not an issuer decision about the card or balance.
- [ ] Data from at least two weeks shows the second attempt approves often enough to pay for its latency and reattempt exposure. Record the approval rate of cascaded attempts for this code.
- [ ] Mastercard Merchant Advice Codes 03 and 21 still override to HARD.
- [ ] The change is a one-line table change in a reviewed pull request with the data attached.

## Dashboards that must exist

| Panel | Alert when |
|-------|------------|
| Approval rate per processor and network, 1h and 24h | 1h rate drops more than 3 points below 24h rate |
| First-attempt share per processor | Shifts more than 20 points in an hour without a config change |
| Cascade rate (cascades / first attempts) | Above the budget ratio for 10 minutes |
| Retry budget balance | Zero for more than 5 minutes |
| Circuit breaker state per processor | Any OPEN |
| PENDING_UNKNOWN count and age | Any older than 15 minutes |
| HARD declines retried (should be zero) | Any |

## Incident: one acquirer's approval rate drops

1. Confirm on the dashboard that the drop is per processor, not per network or issuer (a network-wide drop is not a routing problem).
2. Check that the estimator has noticed: the processor's estimate should be falling and its first-attempt share shrinking. If it is not, the decay is too slow for this incident.
3. If the drop is severe, set the processor's prior weight to override (or remove it from eligibility for the affected network) and record the change.
4. Open a ticket with the acquirer with the affected BIN ranges and response code distribution.

## Incident: issuer or network outage (spike of 91 / 96)

1. Expect the retry budget to drain; that is the budget working. Do not raise the budget during the incident.
2. Confirm cascades are going to processors with healthy circuits.
3. Communicate to merchants that declines are temporary; do not encourage client-side retries, which bypass the budget.

## Incident: timeouts at an acquirer

1. Confirm no timeout was cascaded (query attempts where code = TIMEOUT and attempt number < last attempt; expect zero).
2. Drain PENDING_UNKNOWN by status inquiry; reverse any authorization found for a payment that has since been retried or abandoned.
3. If the circuit breaker has not opened, check the failure threshold against the observed timeout rate.
