---
name: checklist-reversals-and-corrections
description: Review checklist for refunds, chargebacks, voids, corrections, and any admin tool that undoes money movement
phase: 10
lesson: 6
tags: [ledger, reversals, refunds, corrections, immutability, review]
---

# Reversals and Corrections Review Checklist

Use on any pull request that adds or changes a refund, chargeback, void, correction, or back-office "fix" path.

## Immutability

- [ ] No code path issues `UPDATE` or `DELETE` against journal entries or postings. Grep the diff and the migrations.
- [ ] The database role used by the application has no `UPDATE`/`DELETE` privilege on journal tables, or a trigger rejects them.
- [ ] Admin and support tools undo money movement by posting entries through the same ledger API, never by direct SQL.
- [ ] Entry objects are immutable in code (records, frozen dataclasses, unexported slices with copy-out accessors).

## Linking and reasons

- [ ] Every reversal and correction references the entry it undoes (`relatesTo` or equivalent foreign key).
- [ ] Every reversal and correction carries a non-empty reason: an external reference (refund id, dispute id) or an incident/approval id.
- [ ] The entry kind distinguishes a business undo (refund, chargeback) from an error correction.
- [ ] A reversal cannot itself be reversed; re-booking is a new original with a reason.

## Bounds

- [ ] The amount already reversed is tracked per original (per leg, or at least in total) and checked before every new reversal.
- [ ] A refund or chargeback larger than the remaining amount is rejected with a specific error, not clamped silently.
- [ ] Partial reversals allocate across legs with a conservation-preserving method; a test asserts each reversal balances and no leg goes past zero.
- [ ] Sequences of partial reversals followed by a full reversal end with every affected account at its pre-original balance (a property test exists).

## Idempotency

- [ ] Every undo operation has its own idempotency key derived from the external event, not a fresh UUID per attempt.
- [ ] A retry with the same key and same parameters returns the original result, including after the entry became fully reversed.
- [ ] The same key with different parameters (amount, original) is rejected as a conflict.

## Atomicity

- [ ] A correction validates the replacement before posting the reversal; both are committed in one transaction.
- [ ] A failed reversal or correction leaves no partial state (journal length, balances, reversed-so-far all unchanged); a test asserts it.

## Evidence

- [ ] The journal (or its export) is hash-chained or otherwise tamper-evident, and the head is published daily to a location the application cannot overwrite.
- [ ] An audit view can show, for any original, every linked reversal and correction with reasons and timestamps.
- [ ] Corrections to closed periods post in the current open period and reference the original period (see Lesson 12).
