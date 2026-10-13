---
name: runbook-disputes
description: Operations runbook for a card dispute desk (daily sweep, evidence by reason code, accept-or-fight economics, ledger checks, escalation)
phase: 12
lesson: 7
tags: [disputes, chargebacks, evidence, deadlines, ledger, runbook]
---

# Dispute Desk Runbook

## Daily, before 10:00

1. Run the deadline sweep for today's date. Record every transition it made and every overdue inquiry it reported.
2. Open the queue sorted by deadline ascending. Anything due within 3 business days is worked first.
3. For each new first chargeback, confirm: the amount was debited from the merchant balance, the chargeback fee was posted, and any refund on the same capture is blocked.
4. Check that the dispute hold balance equals the sum of amounts for disputes in REPRESENTED, PRE_ARBITRATION, PRE_ARB_RESPONDED and ARBITRATION.

## Accept or fight

Compute both outcomes before deciding:

| Path | Merchant cost |
|------|---------------|
| Accept now | amount + chargeback fee |
| Fight and win | chargeback fee |
| Fight, lose at pre-arbitration | amount + chargeback fee |
| Fight, lose at arbitration | amount + chargeback fee + arbitration fee |

Fight only if the evidence answers the reason code completely. Escalation to arbitration needs a second reviewer when the arbitration fee exceeds the disputed amount.

## Evidence by reason code

| Claim | Must have | Strengthens |
|-------|-----------|-------------|
| Fraud, card-absent (Visa 10.4, MC 4837) | 3-D Secure authentication result, or two prior undisputed transactions 120-365 days old with matching device or IP | AVS and CVV results, account login history |
| Not received (Visa 13.1) | Carrier proof of delivery to the billing or agreed address; receipt | Signed delivery, photo of delivery, customer correspondence after delivery |
| Not as described (Visa 13.3, MC 4853) | Product description as shown at purchase; refund policy or customer correspondence | Evidence the item was not returned, or returned in different condition |
| Credit not processed (Visa 13.6) | Refund record with date and reference, or the policy that refused it | Customer acceptance of the policy at checkout |
| Duplicate processing (Visa 12.6.1, MC 4834) | Two distinct orders, invoices, or deliveries | Separate authorization codes and timestamps |
| No authorization (Visa 11.3) | Authorization record matching amount and date | |

## Handling network events

| Event | Action |
|-------|--------|
| Retrieval request | Answer within the inquiry window with the same evidence a representment would need. No money moves. |
| First chargeback | Block refunds on the capture, assemble evidence, decide accept or fight within the PSP deadline. |
| Representment accepted by issuer | Confirm the hold was released to the merchant balance. |
| Pre-arbitration | Re-review with any new claim from the issuer; respond only with new evidence. |
| Arbitration filed | Notify the merchant of the fee exposure. No action until the ruling. |
| Ruling | Confirm the hold moved (released or returned) and the fee posted if lost. |

## Ledger checks (daily reconciliation)

- [ ] Journal balances.
- [ ] Dispute hold equals the sum over disputes in post-representment, non-terminal states.
- [ ] Each terminal dispute's merchant impact equals the table above for its path.
- [ ] Network dispute report matches open disputes by case number and amount.

## Escalation

- A deadline within 24 hours with incomplete evidence: escalate to the merchant's owner and decide accept or fight that day. A missed deadline is the most expensive outcome.
- Dispute ratio for a merchant approaching network monitoring thresholds: notify risk and the account manager, and review pre-dispute alert enrolment.
