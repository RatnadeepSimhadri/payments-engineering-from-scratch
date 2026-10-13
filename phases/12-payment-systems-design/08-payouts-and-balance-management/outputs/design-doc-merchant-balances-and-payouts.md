---
name: design-doc-merchant-balances-and-payouts
description: Design-document template for a merchant balance and payout system (balance states, ledger postings, settlement release, reserves, schedules, payout files, negative balances)
phase: 12
lesson: 8
tags: [payouts, balances, reserves, settlement, negative-balance, ledger, design-review]
---

# Design Document: Merchant Balances and Payouts for <platform>

Fill in every section. A reviewer should be able to answer "can this merchant be paid this amount today, and why" from this document and the ledger alone.

## 1. Balance states

| State | Ledger account | Enters from | Leaves to | Payable? |
|-------|----------------|-------------|-----------|----------|
| Pending | | capture | available, reserve | no |
| Available | | settlement, reserve release, returned payout | in transit, debits | yes |
| Reserve | | settlement | available | no |
| In transit | | payout initiated | bank (confirmed), available (returned) | no |
| <other, e.g. dispute hold> | | | | |

- [ ] Every balance shown to merchants or used by a job is read from the ledger, never from a separately updated column.

## 2. Postings

| Event | Debit | Credit | Posted by (service / job) |
|-------|-------|--------|---------------------------|
| Capture | | | |
| Settlement | | | |
| Payout initiated / confirmed / returned | | | |
| Refund, chargeback, fee | | | |
| Reserve release / applied | | | |
| Collection initiated / received / returned | | | |

## 3. Release condition

- What makes pending funds available? (Settlement report, delivery confirmation, service completion, a date)
- If a report: which checks must pass before it is applied? (Known captures, not already settled, totals equal, fees reconcile)
- What happens to captures that are never named in a report? Who is alerted, after how long?

## 4. Reserve policy

| Merchant segment | Percentage | Hold days | Fixed minimum | Who approves changes |
|------------------|------------|-----------|---------------|----------------------|
| | | | | |

- [ ] Reserves are held as dated tranches; the sum of tranches equals the reserve account at all times, and a test checks it.
- [ ] Rounding direction for the reserve share is stated.
- [ ] The reserve is applied to a negative balance before any collection, earliest tranche first.

## 5. Payout schedule

- Frequencies offered, business-day calendar used, cut-off time, minimum payout per currency.
- [ ] A payout amount is computed from available only, at initiation, under a per-merchant lock or conditional write.
- [ ] A second run on the same day cannot pay twice.

## 6. Payout file

- Format (NACHA, ISO 20022 pain.001, bank-proprietary), one file per run or per bank.
- Control totals in the trailer and how the bank's acknowledgement is reconciled against them.
- Where bank account numbers are detokenized, and who can see the file.

## 7. Bank responses

| Response | Ledger effect | Merchant effect |
|----------|---------------|-----------------|
| Confirmed | | |
| Returned: wrong destination (R02, R03, R04) | | pause until re-verified |
| Returned: insufficient funds on a collection (R01) | | deficit restored |
| Returned: unauthorized debit (R10) | | |

## 8. Negative balances

| Step | Trigger | Owner |
|------|---------|-------|
| Net against settlements | automatic | |
| Apply reserve | each run | |
| Pause payouts | automatic | |
| Collect by debit | negative for N days (N = ) | |
| Write off / pursue | | |

## 9. Invariants and the tests that prove them

- [ ] Ledger balances after every operation.
- [ ] Nothing pending or reserved is ever paid out.
- [ ] Gross captured = paid out + owed (all four balances) + fees, per merchant, to the minor unit.
- [ ] Bank cash = settlements received - payouts confirmed + collections received.
