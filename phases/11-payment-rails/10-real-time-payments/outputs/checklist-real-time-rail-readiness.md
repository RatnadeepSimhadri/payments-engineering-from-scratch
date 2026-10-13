---
name: checklist-real-time-rail-readiness
description: Readiness checklist for a bank or platform about to send or receive on a real-time payment rail (RTP, FedNow, Faster Payments, SCT Inst, UPI)
phase: 11
lesson: 10
tags: [real-time-payments, rtp, fednow, fraud, liquidity, checklist]
---

# Checklist: Real-Time Rail Readiness

Every item is a yes/no question. A "no" is a launch blocker unless someone signs a written risk acceptance.

## Ledger and posting

- [ ] Payments post to the balance of record in real time, 24x7, including weekends and holidays; no memo balance reconciled overnight.
- [ ] Outgoing payments place a hold before the network message is sent and convert it to a posted debit on settlement confirmation (Phase 10, Lesson 07).
- [ ] Incoming payments credit available balance immediately, as the scheme requires.

## Unknown outcomes

- [ ] Every outgoing payment has stable identifiers (EndToEndId, TxId, idempotency key) persisted before sending.
- [ ] A timeout without a status response leads to an `UNKNOWN` state and a status inquiry, never to a resend with new identifiers and never to telling the customer "failed".
- [ ] Late responses after a timeout are matched and applied.
- [ ] Customer-facing copy distinguishes "sent", "processing", and "failed".

## Liquidity

- [ ] Real-time position against the prefunded balance or net sender cap is visible to treasury.
- [ ] Weekend and holiday net outflow is forecast, with a prefunding margin, before the RTGS system closes.
- [ ] Alerts fire before the cap is reached, with a runbook for what to throttle.

## Fraud (sending side)

- [ ] Fraud scoring is synchronous and fits inside the scheme's timeout with margin; the fallback on model timeout (allow or decline) is decided per risk band.
- [ ] New-payee controls: lower limits, step-up authentication, or a cooling-off period.
- [ ] Account-takeover signals (new device, SIM swap, password reset) feed the payment decision.
- [ ] Confirmation of Payee / Verification of Payee is performed where the market provides it, and its result is shown to the payer with clear warnings.
- [ ] Request-to-pay UI states plainly that approving sends money, and shows the payee's verified name.

## Fraud (receiving side)

- [ ] Inbound payments are scored for mule-account patterns (new account, many unrelated payers, rapid outflow).
- [ ] There is a procedure to freeze remaining funds on a credible fraud report from a sending bank.
- [ ] Return-of-funds requests have an owner and a response deadline.

## Regulation and disputes

- [ ] APP fraud reimbursement obligations for the market are implemented (UK: up to 85,000 GBP, 50:50 split, 5 business days), with claim intake and the inter-PSP cost-sharing process.
- [ ] Finality and the absence of a chargeback right are disclosed to customers.
- [ ] Sanctions screening runs synchronously within the timeout (Phase 13, Lesson 04).
