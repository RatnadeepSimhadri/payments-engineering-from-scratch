---
name: design-doc-auth-optimization
description: One-page design-doc template for proposing an authorization-rate or payment-cost lever, with the guarantees and measurement it must include
phase: 14
lesson: 8
tags: [authorization, retries, network-tokens, interchange, design-doc]
---

# Design Doc: <Lever name>

**Author:** <name> **Reviewers:** payments platform, finance, risk **Status:** draft

## 1. Target population

- Which transactions does this lever act on? Name the response codes, card types, and credential states.
- How many per month, and what share of declines? Cite the query.
- Which existing levers already act on the same population? (Token and updater both target stale credentials; failover and retry both target timeouts.)

## 2. Mechanism

- Exact decision rule, in the order it runs relative to the other levers.
- Maximum attempts per transaction and per card per 30 days, across all systems that can retry.

## 3. Guarantees (must all be "yes")

- [ ] Never-retry responses (lost, stolen, closed, not permitted, network "do not try again" advice codes) are never re-sent.
- [ ] Fix-data responses are re-sent only with new credential data.
- [ ] Every retry carries the original transaction's idempotency key and stored-credential indicators.
- [ ] The lever cannot raise approvals on transactions the fraud system declined.
- [ ] Network reattempt limits are enforced in code and tested.

## 4. Cost lines (integer minor units, per month)

| Line | Before | After | Delta |
|------|--------|-------|-------|
| Authorization fees (per attempt) | | | |
| Token / updater / lookup fees | | | |
| Network fees | | | |
| Interchange (including level 2/3 effects) | | | |
| Network penalty exposure | | | |

## 5. Expected value

- Simulated uplift with common random numbers (approvals, rate in bps, volume), from the experiment harness.
- Net value at our contribution margin: `margin * extra volume - extra cost`.
- Combined uplift with existing levers (not the sum of single-lever uplifts).

## 6. Measurement in production

- Holdout: randomized by **card**, not merchant or day; size for the expected uplift (standard error `sqrt(2p(1-p)/n)`).
- Metrics (Lesson 06): auth rate by processor and by response code, attempts per transaction, cost per approval, reattempt-limit violations (must be zero).
- Duration and the decision rule for keeping or rolling back.

## 7. Rollback

- Feature flag and the exact behaviour when it is off.
- What happens to in-flight retries scheduled before rollback.
