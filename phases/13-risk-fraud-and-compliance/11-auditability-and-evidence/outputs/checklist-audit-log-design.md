---
name: checklist-audit-log-design
description: Design-review checklist for any decision or audit log that must later serve as evidence to a regulator, auditor, card network, or court
phase: 13
lesson: 11
tags: [audit, evidence, hash-chain, merkle, timestamping, worm, compliance]
---

# Evidence-Grade Audit Log Review

Use when a service starts writing records someone may dispute: risk decisions, AML case actions, threshold and model changes, refunds above a limit, access to card data. Each unchecked box is a question an examiner will ask.

## 1. What is logged, and when

- [ ] Every decision is appended **before** its result is returned or acted on (log-before-act); a failed append fails the action
- [ ] Each entry carries the inputs needed to explain it: model version, thresholds, rule set version, score, top reasons, actor (service or human)
- [ ] Configuration changes (thresholds, rules, model deploys) are entries in the same log, with approver and ticket
- [ ] A reconciliation job matches ledger events (authorizations, payouts) to log entries daily and alerts on any unmatched event

## 2. Encoding and chaining

- [ ] One canonical, injective byte encoding per entry (length-prefixed fields or RFC 8785 JCS), with a version tag
- [ ] Entry hash covers sequence number, timestamp, previous hash, kind, subject, and payload
- [ ] Amounts are integer minor units with currency in the payload, never floats
- [ ] Application credentials have INSERT and SELECT only; no UPDATE or DELETE path exists in code or grants

## 3. Anchoring and time

- [ ] Checkpoints (size, Merkle root, head hash) are signed by a party outside the operator's administrative control (RFC 3161 TSA, transparency log, auditor)
- [ ] Seal interval is chosen and written down as the backdating window (for example every 5 minutes)
- [ ] Append rejects or explicitly records backwards clock steps; the clock source and its NTP configuration are documented
- [ ] Verifier checks no entry is dated after the seal that covers it

## 4. Evidence bundles and the verifier

- [ ] Bundles contain only the subject's entries, as canonical bytes, with O(log n) inclusion proofs and the signed checkpoint
- [ ] The verifier takes the TSA public key from an independent channel and fails if the bundle names another key
- [ ] The verifier is a separate, short, standard-library program that the recipient can read and run
- [ ] A sample bundle and verifier run are rehearsed each quarter, end to end, by someone outside the team

## 5. Privacy, retention, and storage

- [ ] Personal data in payloads is encrypted per subject so erasure is crypto-shredding; hashes are computed over ciphertext (Lesson 10)
- [ ] Retention period per entry kind is mapped to its legal basis (PCI DSS 10.5.1 at least 12 months; record-keeping rules up to 5-7 years)
- [ ] Log segments and checkpoints are replicated to WORM storage (for example S3 Object Lock compliance mode) in a separate account
- [ ] TSA and signing keys live in an HSM or KMS; key rotations are logged and old public keys stay published for verification

## 6. Known limits, written down

- [ ] The design document states what the log cannot prove (omission, events before the first checkpoint, compromise of the TSA key)
- [ ] Owners are named for the reconciliation job, the sealing job, and evidence requests
