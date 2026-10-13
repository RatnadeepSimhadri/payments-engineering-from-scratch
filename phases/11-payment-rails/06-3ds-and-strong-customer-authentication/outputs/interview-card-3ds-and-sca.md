---
name: interview-card-3ds-and-sca
description: Interview card on EMV 3-D Secure and PSD2 strong customer authentication - the questions a payments interviewer asks and the answers that show you understand protocol, regulation, and liability together
phase: 11
lesson: 6
tags: [3ds, sca, psd2, liability-shift, interview]
---

# Interview Card: 3-D Secure and SCA

Each question has the answer a strong candidate gives and the follow-up that separates understanding from recitation.

## 1. Walk me through a 3DS2 authentication.

**Answer.** The 3DS Server sends an AReq with card, amount, device, and account data to the scheme's Directory Server, which routes it by card range to the issuer's ACS. The ACS risk-scores it and returns an ARes: `Y` (frictionless), `C` (challenge), or a failure status. On `C`, the browser or SDK talks to the ACS directly with CReq/CRes; the ACS then sends the final result to the 3DS Server in an RReq via the DS. The merchant authorizes with ECI and the CAVV/AAV.

**Follow-up.** Why does the result come in the RReq and not from the browser? (The browser is untrusted; the RReq is server to server and signed.)

## 2. What is the difference between authentication and authorization?

**Answer.** Authentication proves the cardholder is who they claim (3DS, the ACS). Authorization asks whether the issuer will fund the payment (ISO 8583, the issuer host). An authenticated payment can be declined for funds; an authorized payment may be unauthenticated.

## 3. When does liability shift?

**Answer.** For fraud chargebacks, when the transaction was authenticated (`Y`, ECI 05/02) or, under scheme rules, attempted (`A`, ECI 06/01), and the authorization carries the authentication value for the same amount. Not for acquirer-applied exemptions, data-only, or non-fraud disputes.

**Follow-up.** Which exemption application shifts liability? (An issuer applying its own TRA inside 3DS and returning `Y`.)

## 4. What does PSD2 SCA require?

**Answer.** Two independent factors from knowledge, possession, inherence; for remote payments, dynamic linking of the code to amount and payee. Applies to payer-initiated electronic payments with both PSPs in the EEA. It is the issuer's obligation.

**Follow-up.** Is the CVV a knowledge factor? (No; it is printed on the card, per the EBA opinion.)

## 5. Name three exemptions and their limits.

**Answer.** Low value: at most 30 EUR, cumulative 100 EUR or 5 transactions since the last SCA. TRA: up to 100/250/500 EUR if the PSP's fraud rate is at most 0.13%/0.06%/0.01%. Recurring: same amount and payee after the first with SCA. Out of scope, not exemptions: MITs, MOTO, one-leg-out.

## 6. Your approval rate dropped after an issuer started soft-declining. What do you build?

**Answer.** On decline codes `1A`/`65`, retry once with full 3DS and challenge allowed, within the same session, never with another exemption. Measure per issuer: share sent to 3DS, frictionless rate, challenge success, post-authentication approval. Fill optional AReq fields to cut challenges.

## 7. How do subscriptions work under SCA?

**Answer.** Authenticate the customer-initiated setup, store the scheme transaction id, flag later charges as MITs referencing it. Use 3RI when a fresh authentication value is needed without the cardholder.
