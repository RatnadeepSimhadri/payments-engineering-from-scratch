---
name: checklist-score-threshold-review
description: Review checklist for any change that introduces a risk score or moves a decision threshold, written in money rather than model metrics
phase: 13
lesson: 3
tags: [fraud, risk-score, threshold, calibration, review-queue, model-risk]
---

# Score and Threshold Change Review

Attach to every change that ships a new score, retrains a model, or moves a threshold. A threshold change without section 1 filled in is a guess.

## 1. Cost model (in money, signed off by finance or risk)

- [ ] Cost of a missed fraud `C_fn` stated in minor units with currency: average fraud ticket + chargeback fee + dispute ops cost
- [ ] Cost of a false decline `C_fp` stated: lost margin + support contact + measured churn, by segment if segments differ
- [ ] Cost of a manual review stated: analyst minutes x loaded rate, plus reviewer miss rate and reviewer insult rate
- [ ] Bayes threshold `C_fp / (C_fp + C_fn)` computed and written next to the proposed threshold

## 2. Data the decision is based on

- [ ] Labelled window stated (dates) and old enough for chargebacks to mature (at least 60-90 days for cards)
- [ ] Base rate in that window stated; compared with the last 30 days of provisional labels (TC40 / SAFE)
- [ ] Holdout traffic exists that bypassed the model, so declined-segment fraud rate is measured, not assumed
- [ ] Same feature code used offline and online (no training-serving skew); feature version recorded

## 3. Evidence tables (attach, do not summarise)

- [ ] Threshold sweep: TP, FP, FN, precision, recall, insult rate, expected cost, at the current and proposed thresholds
- [ ] Reliability table by bin with counts, mean predicted, observed; ECE stated but not relied on alone
- [ ] If the model is miscalibrated in the decision region, the threshold comes from an empirical cost sweep or the model is recalibrated first
- [ ] Segment cuts: new vs returning customers, top 5 countries, top 5 MCCs; no segment's insult rate more than doubles

## 4. Review band and capacity

- [ ] Band `[low, high)` stated with expected daily volume and the reviewers available to clear it within SLA
- [ ] Behaviour when the queue is full is defined (fall back to single threshold, or auto-approve below X)
- [ ] Reviewer outcomes are logged as labels and fed back to evaluation

## 5. Explanations and records

- [ ] Every decision records model version, integer score, thresholds in force, and top reason codes
- [ ] Reason codes are deterministic (stable tie-break) and mapped to customer-safe decline messages
- [ ] For credit decisions, reason codes satisfy adverse-action notice requirements (ECOA / Reg B in the US)

## 6. Rollout and rollback

- [ ] Shadow mode for at least one week: new threshold logged, old one enforced, daily diff reviewed
- [ ] Ramp plan (1% / 10% / 50% / 100%) with a named owner watching approval rate and review volume
- [ ] Rollback is a config change, not a deploy; tested
- [ ] Re-review date set (thresholds drift as fraud adapts; at most 90 days)
