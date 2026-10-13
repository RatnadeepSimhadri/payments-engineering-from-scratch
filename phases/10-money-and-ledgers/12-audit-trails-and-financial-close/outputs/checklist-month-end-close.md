---
name: checklist-month-end-close
description: Month-end close checklist for a payments ledger, with the evidence each step leaves for the auditor
phase: 10
lesson: 12
tags: [close, audit, trial-balance, controls, sox, evidence]
---

# Month-End Close Checklist (Payments Ledger)

Each step names the evidence it must leave. A step without evidence did not happen, as far as an auditor is concerned.

## Day 0: Cutoff

- [ ] Record the cutoff journal position (seq) and timestamp at period end. Evidence: close job log with seq.
- [ ] Produce the unadjusted trial balance as of the cutoff seq; confirm debit total equals credit total. Evidence: stored report with parameters.
- [ ] Confirm all batch jobs for the period completed (settlement ingestion, payouts, snapshots). Evidence: job run history.

## Days 1-3: Reconcile

- [ ] Processor reconciliation for every processor and currency: matched, breaks listed with owner and age. Evidence: reconciliation report.
- [ ] Bank reconciliation for every bank account, including safeguarded accounts. Evidence: report tied to bank statement balances.
- [ ] Safeguarding reconciliation: amounts owed to customers versus safeguarded funds, reviewed and signed. Evidence: signed report.
- [ ] Suspense and clearing accounts are zero or every residual item is explained with an owner. Evidence: aging list.
- [ ] Cutoff review: events occurring in the period but reported after it are booked as adjustments in the period. Evidence: list of adjustments with external references.

## Days 2-4: Adjust

- [ ] Accruals for fees and costs incurred but not yet invoiced. Evidence: calculation and later invoice match.
- [ ] FX revaluation of foreign-currency monetary balances at closing rates. Evidence: rate source, timestamp, recalculation.
- [ ] Reserve releases and allowances per policy. Evidence: aging report and policy reference.
- [ ] Every manual entry has a maker, a different checker, a reason, and a ticket. Evidence: manual entry listing from the ledger.
- [ ] Produce the adjusted trial balance; roll-forward key accounts (merchant payable, reserves, customer funds) from opening balance. Evidence: roll-forwards that tie.

## Day 5: Review, lock, preserve

- [ ] Finance owner reviews the adjusted trial balance, flux analysis versus prior month, and reconciliations. Evidence: sign-off record.
- [ ] Set the period lock; confirm the ledger rejects a test entry dated in the closed period (in a non-production check or via the control test). Evidence: lock configuration change record.
- [ ] Store the close snapshot (adjusted trial balance, cutoff seq, journal head hash). Evidence: snapshot id.
- [ ] Publish the head hash to write-once storage outside the ledger database. Evidence: object id and timestamp.
- [ ] Re-run the as-of trial balance at the cutoff seq and confirm it reproduces the snapshot exactly. Evidence: comparison output.

## Quarterly (controls that support every close)

- [ ] Access review: users with production write access to ledger data, with removals actioned. Evidence: signed review.
- [ ] Change review: every ledger code change in the quarter had an approving reviewer other than the author. Evidence: PR export.
- [ ] Break-glass access log reviewed. Evidence: review record.
- [ ] Report queries used for close (trial balance, roll-forwards, manual entry listing) unchanged or changes reviewed. Evidence: version history.
