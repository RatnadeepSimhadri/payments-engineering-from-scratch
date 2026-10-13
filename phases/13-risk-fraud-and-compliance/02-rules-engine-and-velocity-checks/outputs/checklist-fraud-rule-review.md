---
name: checklist-fraud-rule-review
description: Review checklist for any new or changed fraud rule before it goes live
phase: 13
lesson: 2
tags: [fraud, rules-engine, velocity, review, shadow-mode]
---

# Fraud Rule Review Checklist

Use this for every pull request or config change that adds, edits, or removes a rule in an authorization rules engine. The questions are the ones a dispute analyst, a card-network compliance reviewer, or the merchant whose customers you declined will eventually ask. A rule that cannot answer them is not ready to act on live traffic.

## 1. The rule as data

- [ ] The rule is expressed as `field`, `operator`, `value`, `action`, `priority`, `score`, `explanation`, `shadow`. No code, no `AND`. If the condition needs two facts, it is two rules under `SCORE` or one derived fact computed by enrichment.
- [ ] The rule has a unique, stable name. Renaming a rule breaks every dashboard and every audit query that references it.
- [ ] The `explanation` is a sentence a non-engineer can read and a customer-facing team can paraphrase. "rule 17" and "velocity breach" are not explanations.
- [ ] The `value` has the same type as the fact. `card_age_days LT 1` compares a `Long` with a `Long`; `amount GTE 5000.00 USD` compares `Money` with `Money` in the same currency. A mismatch fails loudly at evaluation, so test the rule against a real transaction context before merging.

## 2. The fact it reads

- [ ] Name the producer of the fact (the transaction record, the velocity service, the device signal pipeline) and its owner.
- [ ] State what the rule does when the fact is absent (nothing fires; the trace shows `<absent>`). Confirm a monitor alarms on absent-fact rates for this field, so an enrichment outage does not look like clean traffic.
- [ ] For a velocity fact: write down the key dimension (card token, account, device, IP, merchant), the window, the bucket count, and the metric (count, sum, distinct). State whether the current attempt counts itself (record-then-read) and make the threshold match: `GT 5` with record-then-read means the sixth attempt.
- [ ] For a velocity fact: confirm declined attempts are recorded too. A counter that only counts approvals resets the attacker's budget every time the rule fires.
- [ ] For a sum fact: confirm the counter is single-currency and the sum uses overflow-checked integer minor units.

## 3. Ordering and strategy

- [ ] Under `FIRST_MATCH`: what rules sit at a lower priority number than this one, and is it acceptable that any of them decides first? Write the one case where this rule is expected to be the deciding rule.
- [ ] Under `SCORE`: what is the new rule's score relative to the `reviewAt` and `declineAt` thresholds, and which combinations of existing rules now cross a threshold that did not before?
- [ ] Confirm that the default when nothing fires is still `ALLOW`, and that removing this rule leaves a rule set that still evaluates.

## 4. Shadow-mode evidence

- [ ] The rule ran as a shadow for at least one full weekly cycle on production traffic. Record: transactions seen, would-have-fired count, fire rate.
- [ ] Join would-have-fired transactions with the chargeback and dispute feed. Record the precision (fired and later disputed / fired) and the estimated good-customer loss (fired and never disputed, times average order value).
- [ ] Compare the shadow action with the live action on the same transactions: how often would this rule have been stricter than what actually happened? That number is the customer impact of promoting it.
- [ ] State the fire-rate ceiling above which the rule is paged and demoted back to shadow automatically.

## 5. Explanation and audit

- [ ] Run one real declined transaction through `explain()` with the new rule live and paste the output into the review. Every line should show observed value, operator, expected value.
- [ ] Confirm sets print sorted and facts print in insertion order, so identical decisions produce identical text (the hash-chained decision log in Lesson 11 depends on it).
- [ ] Confirm the rule set version changes with this rule, and that version appears in every decision record.

## 6. Roll-back plan

- [ ] Name the config change that demotes the rule to shadow and the one that removes it. Both must be possible without a deploy.
- [ ] Name the dashboard where fire rate, decline rate, and review-queue depth are visible within five minutes of promotion.
- [ ] Name the person on call for the first 24 hours after promotion and the threshold at which they roll back.

## Sign-off

| Role | Name | Date | Decision |
|------|------|------|----------|
| Rule author | | | |
| Risk analyst | | | |
| Engineering reviewer | | | |
