---
name: interview-card-card-payment-flow
description: Interview card for explaining a card transaction end to end; the questions a payments interviewer asks and the answers that show you understand the three flows
phase: 11
lesson: 1
tags: [cards, authorization, clearing, settlement, interview]
---

# Interview Card: How a Card Payment Works

Use this to prepare for a payments system design interview, or to run one. Each question has the answer a strong candidate gives and the follow-up that separates understanding from recitation.

## 1. Walk me through what happens when I tap my card.

**Answer.** Card and terminal run an EMV exchange and the card produces a cryptogram. The terminal sends an authorization request through the gateway to the acquirer, which puts it on the network as an ISO 8583 0100. The network routes on the BIN to the issuer, which verifies the cryptogram, checks open-to-buy and fraud rules, and answers 0110 with a response code. Approval places a hold; no money moves. Clearing happens later in batch and carries the final amount. Settlement is the network netting every member's position daily and moving one net amount per member through a settlement bank. The acquirer then funds the merchant, net of the merchant discount, on T+1 to T+3.

**Follow-up.** "Where is the money at second two?" Still in the cardholder's account, with a hold against it.

## 2. What is the difference between an acquirer, a processor, and a gateway?

**Answer.** The acquirer is the scheme member bank that sponsors the merchant, holds settlement funds, and is liable to the network for the merchant. The processor is the technical platform that switches messages and builds clearing files, often the acquirer itself or a vendor operating under its licence. The gateway is the merchant-facing integration layer. One company can be all three.

**Follow-up.** "Who do you call when a clearing file is rejected?" The acquirer; the gateway may not even see clearing.

## 3. Authorization versus clearing versus settlement: give me timing, message, and what can reverse each.

| Flow | Timing | Message | Reversed by |
|------|--------|---------|-------------|
| Authorization | Under two seconds | 0100/0110 | 0400 reversal, or hold expiry (7 or 30 days) |
| Clearing | Hours to days, batch | BASE II / IPM clearing record | Refund (new record), chargeback |
| Settlement | Daily cycle | Net settlement instruction | Only by a later offsetting entry |

## 4. Dual-message versus single-message. When does it matter to my code?

**Answer.** Dual-message (credit, signature debit) separates authorize and clear, so "authorized but not captured" is a real state with an expiry, and partial, multiple, and incremental captures exist. Single-message (PIN debit, ATM) clears in the same 0200 message, so the amount is final on approval and there is no capture step. A payment state machine must not expose capture semantics on a single-message rail.

## 5. A $100 purchase: who gets what?

**Answer, in cents, with interchange 1.80% + 10c, assessment 0.13%, MDR 2.50%:** issuer 190, network 13, acquirer 47, merchant 9750. Total 10000. Interchange is paid by the acquirer to the issuer; the merchant only ever sees the discount rate.

**Follow-up.** "Why does the issuer get the most?" It carries cardholder credit risk and most of the fraud loss.

## 6. Who is liable when the transaction turns out to be fraud?

**Answer.** It depends on the evidence captured. Chip cryptogram present: issuer. Magstripe fallback on a chip card: merchant. Card-not-present without 3-D Secure: merchant. With successful 3-D Secure: issuer. The rule of thumb is that liability sits with the party that declined to use the available control.

## 7. The issuer did not respond. What happens?

**Answer.** The network may approve under stand-in processing within issuer-defined limits and send the issuer a 0120 advice later, or return 91. If the acquirer itself times out, it sends a 0400 reversal from a store-and-forward queue until acknowledged, so no phantom hold remains.

**Follow-up.** "What did Visa Europe's June 2018 outage look like to a merchant?" A mix of approvals, 91s, and timeouts for the same cards, because one of two data centres was partially alive; clearing and settlement were unaffected.

## 8. Why is my deposit smaller and later than my gateway's success webhook?

**Answer.** The webhook was authorization or capture. The deposit is settlement: batched by day, net of the merchant discount, net of refunds and chargebacks, delayed by the funding schedule and any reserve. Reconcile against the acquirer's settlement report, not the webhook log.

## Red flags in a candidate's answer

- Says an approval means the merchant "has the money".
- Cannot name who pays interchange to whom.
- Believes the network is a bank that holds the funds.
- Has no answer for timeouts other than "retry".
- Treats every non-00 response code the same way.
