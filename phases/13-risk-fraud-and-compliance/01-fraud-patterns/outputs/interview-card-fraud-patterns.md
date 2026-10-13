---
name: interview-card-fraud-patterns
description: One-page reference for classifying a fraud incident by pattern, signal, control, and who bears the loss
phase: 13
lesson: 1
tags: [fraud, risk, chargebacks, interview]
---

# Fraud Patterns Interview Card

Use when a dashboard moves, when a design review asks "what does this control stop", or when an interviewer says "design a fraud system".

## Classify first

| Pattern | One-line mechanism | First signal to compute | First control to ship | Who eats the loss |
|---------|-------------------|-------------------------|-----------------------|-------------------|
| Card testing | Checkout used as an oracle for stolen card validity | Distinct PANs per device or IP per 10 minutes; decline ratio | Multi-key rate limit; CAPTCHA after first decline; $0 verification | Merchant (auth fees, chargebacks, fines) |
| Account takeover | Reused credentials from another breach, then spend saved value | New device + profile change + purchase within minutes | Notify old contact; step-up on new address or instrument | Platform, or issuer if 3DS used |
| Friendly fraud | Real customer disputes a real purchase | "Fraud" reason code on a card with prior good history and same device | Descriptors, renewal reminders, pre-dispute alerts, CE 3.0 evidence | Merchant unless representment wins |
| Triangulation | Fake seller fulfils real orders with stolen cards | New seller, below-cost prices, retailer-origin tracking | KYB; payout hold matched to chargeback window | Retailer, then marketplace |
| Refund abuse | Policy or refund-to-new-instrument laundering | Refund ratio per account; refunds to non-original instruments | Original-instrument rule enforced in the ledger | Merchant |
| Synthetic identity | Manufactured identity builds a credit file | Thin file, conflicting elements, shared phone or address | Document plus database verification; SSA eCBSV | Lender, at default |
| Mule | Pass-through account for stolen funds | Many senders in, one transfer out, near-zero balance, new account | Holds on new accounts; inbound monitoring | Scam victim or receiving bank |
| Bust-out | Good behaviour, then max and abandon | Utilisation acceleration; payment from a new source just before | Release limit only on irrevocably cleared funds | Lender |

## Questions an interviewer will ask

- "Why not block everything above a score?" The insult rate. Blocked good customers are lost revenue; the loss formula has four terms, not one.
- "What does 3-D Secure actually buy you?" Liability shift to the issuer on card-not-present fraud, at a conversion cost. It does nothing for friendly fraud except move the loss.
- "Why hold seller payouts?" Triangulation and chargeback lag. The hold should match the window in which disputes arrive.
- "Why are labels late?" Chargebacks arrive 30 to 120 days after the transaction. Real-time signals exist because ground truth is late.
- "Does winning a dispute help?" It returns the amount, not the fee, and the dispute still counts toward network monitoring thresholds.

## Data to retain, and for how long

- Device ID, IP, account login per transaction: 365 days (Compelling Evidence 3.0 window).
- Shipping address with first-seen date per account: life of account plus dispute window.
- Delivery confirmation joined to order: dispute window (typically 120 days from delivery).
- Every authorisation response, including declines: at least 13 months for monitoring-programme reconciliation.
- Profile change events with old and new values: 365 days.

## Red flags in a design review

- A control proposed without naming the pattern it targets.
- A CAPTCHA proposed in response to disputes.
- Refunds that can go to an instrument other than the original.
- Logging only successful authorisations.
- A seller payout schedule shorter than the chargeback window with no reserve.
