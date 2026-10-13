---
name: design-doc-monitoring-scenario
description: Template for proposing, approving, and documenting a transaction-monitoring scenario so that compliance and the examiner can read the same page as the engineer
phase: 13
lesson: 6
tags: [aml, monitoring, scenario, design-doc, compliance]
---

# Monitoring Scenario Design Document

Fill this in before writing a detector. Compliance signs section 7; the signed copy is what the examiner reads. One document per scenario; re-issue it on every parameter change.

## 1. Identity

- **Scenario id:** (e.g. `STR-01`)
- **Kind / typology:** (e.g. structuring; FATF placement stage)
- **Severity:** LOW / MEDIUM / HIGH
- **Owner (compliance):**
- **Owner (engineering):**
- **Version and date:**

## 2. Regulatory basis

- Statute or regulation this scenario supports (e.g. 31 U.S.C. 5324; 31 CFR 1010.311; local equivalent).
- Published typology or guidance it maps to (FATF report, FinCEN advisory, FIU bulletin), with a link.
- What the examiner will expect this scenario to catch, in one sentence.

## 3. Detection logic in plain language

Write it so a compliance officer can verify it against the code without reading the code.

- Which transactions qualify (direction, channel, amount band, counterparty type).
- The window: rolling or calendar, and its length.
- The aggregation or pattern (sum over window, inbound-then-outbound, count and spread).
- The trigger condition.
- How the score is computed and what it is for (queue ordering only, or also severity).
- How overlapping triggers are merged into one alert.

## 4. Parameters

| Name | Initial value | Unit | Why this value | Who may change it |
|------|---------------|------|----------------|-------------------|
| | | | | |

- Every value must have a reason: a regulatory number, a back-test result, or an explicit judgement with its author.
- State the above-the-line and below-the-line values that will be tested at each review (e.g. floor 75% and 85% around a production 80%).

## 5. Alert contents

- Evidence: exactly which transactions are attached (all qualifying in the window; the inbound and the legs that met the rule; etc.).
- Narrative template, with the fields that are substituted.
- Fields the investigator will need that are not in the evidence (customer type, account open date, prior alerts) and where they come from.

## 6. Back-test

- Population used (date range, number of accounts, number of transactions).
- Labelled cases available (known true positives from past filings; synthetic plants if none).
- Results at the proposed parameters: true positives, false positives, projected alerts per week.
- Results above and below the line.
- Analyst capacity in alerts per week and whether the projection fits.
- Known false-positive classes and the clearing rule or reason code for each.

## 7. Approval

| Role | Name | Date | Signature |
|------|------|------|-----------|
| Compliance officer | | | |
| Engineering lead | | | |
| Model risk / internal audit (if required) | | | |

## 8. Review cadence

- Scheduled review: quarterly, or on any list, product, or customer-base change.
- Triggers for an out-of-cycle review: alert volume moves more than 25% week over week; a true case found by another route; a regulatory change; any code change to the detector.
- Where close reason codes from triage are captured, and who turns them into the next back-test's labels.

## 9. Change log

| Version | Date | Change | Reason | Approved by |
|---------|------|--------|--------|-------------|
| | | | | |
