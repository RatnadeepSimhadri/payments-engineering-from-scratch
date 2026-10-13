---
name: runbook-dunning-and-renewals
description: Operations runbook for a daily subscription billing run, its reruns, and its dunning schedule
phase: 12
lesson: 11
tags: [subscriptions, billing, dunning, runbook, reconciliation]
---

# Runbook: Daily Renewals and Dunning

For the on-call engineer responsible for the subscription billing run. Every step assumes the invariants from Phase 12, Lesson 11: dates come from the anchor, invoices have deterministic ids, attempts have deterministic references, and attempts are gated by `next_attempt`.

## Before the run

- [ ] The clock source is the billing calendar's date in the merchant's configured time zone, not the server's local time.
- [ ] The previous day's run finished. If not, do not start today's run until you have read "Rerunning a day" below.
- [ ] The card-updater batch (if your processor runs one) finished before the renewal run, so expired cards are replaced before they are charged.

## Rerunning a day

A rerun is safe by design. Confirm the design still holds before relying on it:

```sql
-- Exactly one renewal invoice per subscription per period start
SELECT subscription_id, period_start, COUNT(*) FROM invoices
WHERE kind = 'renewal' GROUP BY 1, 2 HAVING COUNT(*) > 1;

-- Exactly one gateway charge per attempt reference
SELECT reference, COUNT(*) FROM gateway_charges GROUP BY 1 HAVING COUNT(*) > 1;
```

Both queries must return no rows. If either returns rows, stop the run, page the owner, and refund duplicates through the refunds path, never by editing balances.

## Recognizing drift

```sql
-- Anchor drift: the billing day no longer matches the anchor day
SELECT id, anchor, current_period_start FROM subscriptions
WHERE EXTRACT(DAY FROM current_period_start) <> LEAST(EXTRACT(DAY FROM anchor),
      EXTRACT(DAY FROM (date_trunc('month', current_period_start) + interval '1 month - 1 day')));

-- Proration drift: proration credit + used portion does not equal the old price
SELECT id FROM prorations WHERE credit_minor + used_minor <> old_price_minor;
```

Either query returning rows means a code path computes dates from the previous period or rounds proration shares independently. Fix the code before fixing the data, then migrate the data with the job paused.

## Dunning rules

| Decline reason | Enters the retry schedule? | Action |
|----------------|----------------------------|--------|
| Insufficient funds | Yes | Retry on schedule offsets from the first failure |
| Do not honor | Yes | Retry on schedule offsets from the first failure |
| Expired card | No | Card updater; charge the new credential immediately if one exists, otherwise exhaust |
| Lost, stolen, pick-up, fraud, closed account | Never | Exhaust immediately; one more attempt breaks network reattempt rules |
| Authentication required (SCA) | No | Email the customer a link to authenticate; wait |

- [ ] The schedule offsets are positive, strictly increasing, and measured from the first failure.
- [ ] After a missed day, the engine makes one attempt, not a burst of the missed ones.
- [ ] Exhaustion ends in the configured status (`CANCELED` or `UNPAID`) and the invoice is `UNCOLLECTIBLE`.

## After the run

- [ ] Count of invoices opened today equals count of subscriptions whose period ended today (plus catch-up periods).
- [ ] Sum of approved charges today equals the sum of `PAID` invoice amounts dated today, to the minor unit.
- [ ] The `UNKNOWN` decline reason count is zero, or there is a ticket to map the new code.
- [ ] Involuntary churn (subscriptions exhausted today) is within its usual range; a spike usually means a processor or updater outage, not a customer-behaviour change.
