---
name: runbook-erasure-request
description: Step-by-step runbook for handling a data-subject erasure or access request in a payments system with retention duties, crypto-shredding, and a data map
phase: 13
lesson: 10
tags: [gdpr, ccpa, dpdp, erasure, dsar, crypto-shredding, retention]
---

# Runbook: Data-Subject Erasure or Access Request

Deadline: one month from receipt under GDPR (extendable by two months for complex requests, with notice); 45 days under CCPA (extendable once). Record the receipt date first.

## 1. Intake (day 0)
- [ ] Log the request (ID, channel, receipt date, type) in the tamper-evident log; do not copy personal data into the ticket
- [ ] Identify the regime(s) from the customer's jurisdiction and your entity

## 2. Verify the requester (day 0-3)
- [ ] Authenticate through the account's existing strong authentication, or an equivalent channel bound to data already held
- [ ] If verification fails, respond with how to verify; do not disclose whether an account exists

## 3. Plan from the data map (day 3-5)
- [ ] Generate the per-store, per-category plan from the data map
- [ ] For each category: erase, crypto-shred, restrict, or retain, with the legal basis and end date
- [ ] Check legal holds, open disputes, and suspicious-activity cases (never disclose or mention SAR-related restrictions)

## 4. Execute (day 5-20)
- [ ] Run idempotent tasks per store; track partial failures against the deadline
- [ ] Delete the subject's contact DEK; schedule identity DEK deletion for the end of its retention
- [ ] Redact support tickets and free-text fields found by PII scan
- [ ] Instruct each processor (KYC vendor, CRM, email provider) and record confirmations
- [ ] For access requests: run DSAR exporters, redact third parties and restricted case data, package in a common format

## 5. Respond and record (by deadline)
- [ ] Response lists erased categories, retained categories with legal basis and erasure date, and recipients notified
- [ ] Evidence entry written: request ID, actions per category, dates, operator; no erased data in the evidence
- [ ] Subject ID added to the erasure journal for replay after any backup restore

## 6. Afterwards
- [ ] Scheduled deletions appear in the retention job's queue with their dates
- [ ] Any store discovered during the request that was missing from the data map is added, with an incident note
