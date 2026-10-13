---
name: checklist-sca-integration
description: Review checklist for a card or account payment integration that must satisfy PSD2 SCA, UK SCA, India AFA and e-mandate rules, and US liability rules
phase: 13
lesson: 9
tags: [psd2, sca, 3ds, exemptions, tra, e-mandate, compliance-as-code]
---

# SCA and Regional Authentication Integration Review

## Scope and flags
- [ ] Every payment classified as CIT, MIT, or MOTO before authorisation; MIT carries the CIT network transaction ID
- [ ] One-leg-out detection uses issuer and acquirer country from BIN data, not the customer's address
- [ ] Mandates store card token, CIT reference, CIT ECI, agreed ceiling, jurisdiction, and status

## Exemption strategy
- [ ] TRA band derived from the current 90-day fraud rate, recomputed in integer minor units and versioned
- [ ] Exemption requests driven by the risk score and an expected-cost comparison (fraud liability vs challenge abandonment)
- [ ] Low-value requests expect issuer soft declines; no assumption that the merchant can track counters

## Authentication flow
- [ ] 3-D Secure requests send full device, account-age, and address-match data to maximise frictionless rate
- [ ] Soft declines (Visa 1A, Mastercard 65, acquirer equivalents) trigger exactly one challenge-and-resubmit, under the same idempotency key
- [ ] Soft-declined MITs notify the customer to return for a new CIT; they are never challenged with nobody present
- [ ] ECI, cryptogram, 3DS transaction ID, exemption requested and granted stored with the payment and in the decision log

## Regional rules
- [ ] UK rules configured separately from EU (contactless and liability limits differ)
- [ ] India: AFA on card-not-present, e-mandate limits per category, pre-debit notification at least 24 hours before with delivery evidence, no stored PANs
- [ ] US: Reg E error-resolution timelines and provisional credit for account debits; Reg Z dispute handling for credit cards

## Compliance as code
- [ ] Rule table has jurisdiction, effective dates, legal reference, and owner; changes reviewed by compliance
- [ ] Golden test cases taken from regulation text (low-value counters, TRA bands, MIT scope)
- [ ] Every decision returns and logs the rule ID and legal reference it relied on
