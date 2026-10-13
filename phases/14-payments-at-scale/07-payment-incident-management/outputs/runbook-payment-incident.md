---
name: runbook-payment-incident
description: First-hour runbook and four failure-class playbooks for payment incidents, from declaration to post-incident reconciliation
phase: 14
lesson: 7
tags: [incident, runbook, payments, reconciliation, regulatory]
---

# Payment Incident Runbook

## First 15 minutes

1. **Declare.** Severity from the money-at-risk table. Is it still growing? If yes, it is at least SEV2.
2. **Assign roles.** Incident commander, operations lead, reconciliation lead, communications lead, compliance liaison, scribe.
3. **Record two timestamps** in the channel: when the firm became aware, and (when decided) when the incident was classified.
4. **Contain.** If money is moving wrongly, flip levers before diagnosing:
   - [ ] Route away from the failing processor (scoped)
   - [ ] Disable automatic retries
   - [ ] Timeouts recorded as UNKNOWN; idempotency key bound to the order
   - [ ] Pause capture job (write the earliest auth-expiry deadline in the channel)
   - [ ] Pause payout batch
   - [ ] Freeze specific merchants or accounts
5. **Roll back** any deploy or config change in the last hour.
6. **Comms.** Publish approved wording to checkout, status page, and support. Support stops ad hoc refunds for the incident cohort.

## Playbooks

| Failure class | Contain | Reconcile against | Remediate | Never |
|---------------|---------|-------------------|-----------|-------|
| Processor outage | Route away; UNKNOWN not FAILED; no retries | Processor status endpoint and auth report | Void orphans; keep successful retries | Capture orphans "to be safe" |
| Duplicate charges | Stop the source of duplication | Payments grouped by card, merchant, amount, order | Void or refund one per group via keyed batch | Refund before the source is stopped |
| Stuck captures | Pause the capture job if captures fail silently | Processor capture status per reference | Mark captured, re-send with same key, or record expiry | Bulk re-capture without status queries |
| Recon break storm | Pause payouts if merchant balances are affected | Input files' control totals; re-run | Fix input, re-run; real breaks via normal process | Auto-resolve breaks |

## Closing the incident

- [ ] Reconciliation lead has signed off: every affected payment enumerated and matched to processor and bank records.
- [ ] Remediation batch ran with per-payment idempotency keys; a second run changes nothing.
- [ ] Customers and merchants received the final update.
- [ ] Compliance has recorded the classification decision and filed any reports.
- [ ] Review scheduled: money affected, recovered, written off; customers affected; detection gap; regulatory section.
