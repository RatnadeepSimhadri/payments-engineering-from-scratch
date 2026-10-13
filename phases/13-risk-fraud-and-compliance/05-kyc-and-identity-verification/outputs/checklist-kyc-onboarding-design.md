---
name: checklist-kyc-onboarding-design
description: Design-review checklist for a customer or business onboarding flow that must meet CIP, CDD, and data-minimisation requirements
phase: 13
lesson: 5
tags: [kyc, cip, cdd, kyb, onboarding, state-machine, pii]
---

# KYC Onboarding Design Review

## Requirements captured
- [ ] Applicable regimes named per market (for example US CIP and CDD Rule, UK MLR 2017, EU AMLR, RBI KYC Direction) with the sponsor bank's or licence holder's own policy attached
- [ ] Required data elements per customer type (individual, sole trader, company) listed with legal source
- [ ] Risk factors and tier definitions written down, with limits per tier and refresh cycle per tier

## State machine
- [ ] Every onboarding state is persisted; transitions record actor, timestamp, and evidence relied on
- [ ] Vendor callbacks are idempotent, keyed by vendor reference and attempt, and illegal transitions are rejected
- [ ] Deadlines (verify within N days, refresh due) are durable timers that drive transitions
- [ ] Vendor outage fails closed: pending or restricted tier, never full limits
- [ ] Restricted and offboarded states exist, with neutral customer messaging and a defined funds-exit path

## Verification
- [ ] Waterfall documented: which method first, when to step up, when to send to manual review
- [ ] Verification stored as method, assurance level, vendor reference, result details, and expiry, not a boolean
- [ ] Beneficial ownership computed as summed path products over the ownership graph, with cycle detection and a control-prong person
- [ ] Sanctions and PEP screening at onboarding and on every list update, for the whole base

## Data handling
- [ ] Raw identifiers and documents live in an identity vault; other services hold tokens and masked values
- [ ] Retention per field: CIP records for the required period after closure; images and biometrics only as long as justified
- [ ] Logs, analytics, and tickets are scanned for ID numbers and redacted at source
- [ ] KYC-purpose fields are flagged in the data map and excluded from marketing use

## Limits and monitoring
- [ ] Every money-moving endpoint reads the customer's current tier and limits, not a cached signup flag
- [ ] Monitoring alerts and material-change events feed the state machine (periodic review, EDD, restricted)
