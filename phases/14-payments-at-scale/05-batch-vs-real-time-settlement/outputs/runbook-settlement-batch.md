---
name: runbook-settlement-batch
description: Operating runbook for a nightly merchant payout batch, from pre-flight tie-out to bank rejection, returns, and reruns
phase: 14
lesson: 5
tags: [settlement, payouts, batch, nacha, runbook]
---

# Settlement Batch Runbook

For the engineer or operations analyst who releases the nightly payout batch. Nothing in this runbook moves money until the pre-flight section is fully green.

## Pre-flight (before the file leaves the building)

- [ ] **Tie-out holds.** For every merchant, ledger payable equals carried balance plus unsettled events. Trial balance is zero. If either fails, stop. Do not release a partial file.
- [ ] **Batch conservation.** Acquirer funding expected = payouts + fees retained - merchant deficits. Compute it and write the three numbers in the release ticket.
- [ ] **Funding received.** The acquirer's wire for this batch has arrived and matches the expected amount (Lesson 04 reconciliation). If it is short, release only if treasury approves prefunding the difference, in writing.
- [ ] **Control record recomputed.** Count, total, and entry hash recomputed from the detail lines by an independent check (not the code that wrote them).
- [ ] **Largest payouts reviewed.** Top 10 payouts by amount and any payout more than 3x the merchant's 30-day average have been eyeballed.
- [ ] **Holds honoured.** Every merchant flagged on hold has zero instructions in the file.
- [ ] **Deficits listed.** Merchants with a carried deficit over the threshold (for example 1,000.00) are listed for collections.
- [ ] **New bank details.** Any merchant whose bank account changed in the last 7 days is on the file only if the change was verified (account validation, Phase 11 Lesson 11). Changed bank details plus a large payout is the classic account-takeover pattern.

## Release

1. Approve in the four-eyes tool: one engineer, one finance approver.
2. Transmit the file. Record the bank's acknowledgement id against the batch id.
3. On the bank's execution confirmation, post the confirmation journal (in transit to cash). Not before.

## When things go wrong

| Symptom | First action | Do not |
|---------|--------------|--------|
| Bank rejects the file (control mismatch) | Regenerate from the stored batch; diff with the rejected bytes | Edit amounts in the file by hand |
| Job crashed mid-run | Rerun the same business date; it resumes by batch id | Run with a new date or "force" flag |
| Someone asks to "just rerun" after release | Confirm the rerun returns the stored batch and posts nothing | Delete the batch record to make it run again |
| ACH return received (R01, R02, R03...) | Post return journal back to the merchant payable, put merchant on hold, notify merchant | Re-send automatically to the same account |
| Funding wire short | Reconcile funding against the batch's acquirer-owed figure; open a break | Release payouts from operating cash without treasury sign-off |
| Duplicate payout suspected | Check payout journal ids for the batch: one per merchant | Initiate a reversal before confirming the duplicate in the ledger |

## Offering instant payout: decision table

| Condition | Instant payout? |
|-----------|-----------------|
| Merchant refund + chargeback rate over 2% of volume | No |
| Merchant tenure under 90 days | Only against a reserve |
| Fee charged covers cost of prefunding at current rates | Required |
| Platform's prefunded account covers the 99th-percentile daily instant volume | Required |
| Per-merchant daily instant cap configured | Required |
