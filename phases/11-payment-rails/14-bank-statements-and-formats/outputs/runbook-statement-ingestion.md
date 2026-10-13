---
name: runbook-statement-ingestion
description: On-call runbook for the job that ingests bank statements (MT940, camt.053, BAI2, OFX) - arrival checks, continuity failures, chain gaps, duplicates, and escalation to the bank
phase: 11
lesson: 14
tags: [statements, mt940, camt053, bai2, ofx, reconciliation, runbook]
---

# Runbook: Bank Statement Ingestion

For the on-call engineer of a pipeline that loads bank statements into a finance database ahead of reconciliation.

## On arrival (automated, every file)

1. **Detect the format** from the first bytes. Unknown format: quarantine the file, alert, do not guess.
2. **Parse structurally.** Structural errors (BAI2 control totals or record counts, malformed XML, an MT940 `:61:` that does not match, an amount with too many decimals) reject the whole file. Quarantine it and alert. Never load part of a structurally broken file.
3. **Check continuity per statement**: opening + booked lines = closing. A failure loads the statement flagged `discontinuous` and opens an incident; do not reconcile against it.
4. **Check the chain per account**: this statement's opening = previous statement's closing; statement sequence (`:28C:`, `ElctrncSeqNb`, as-of date) has no gap.
5. **De-duplicate lines** on (account, bank reference, booking date). A duplicate with an identical amount is dropped and counted; a duplicate with a different amount is an incident.
6. **Record provenance**: file name, transport, received-at time, file hash, format, parser version, for every loaded line.

## When continuity fails (opening + lines != closing)

| Difference looks like | Likely cause | Action |
|-----------------------|--------------|--------|
| Divisible by 9 in minor units | Transposed digits in a balance | Compare with the bank portal balance; ask the bank to re-send |
| Equal to one line's amount | Line dropped or duplicated by the parser | Diff the raw file against parsed lines; check continuation handling |
| Twice one line's amount | A reversal (RC/RD, RvslInd) read with the wrong sign | Fix the parser; re-parse the file |
| Equal to pending entries | Pending/info entries counted as booked (camt.053 `PDNG`) | Fix the status filter |
| Small, irregular | Rounding in a bank-side conversion | Escalate to the bank with the statement id and both numbers |

## When the chain has a gap (closing(n) != opening(n+1))

1. Check for a missing statement: sequence number skipped, or as-of date skipped on a business day. Request the missing statement from the bank.
2. Check for a re-issued statement: the bank may have corrected day n after sending it. Compare file hashes and received times; load the corrected statement as a new version, never overwrite silently.
3. If neither, the bank moved the balance without a line (a reversed fee is common). Open a case with the bank quoting both statement ids, both balances, and the difference.
4. Until resolved, mark the account `unreconciled from <date>` so downstream reports carry a warning.

## Format-specific checks

- **MT940**: multiple messages per file (split on `:20:`); `:86:` continuation lines; entry-date year inference at year end; `:60M:`/`:62M:` intermediate balances on multi-page statements.
- **camt.053**: only `BOOK` entries move the balance; `RvslInd` flips the effect; batch entries carry several `TxDtls`; `Amt` currency must equal the account currency.
- **BAI2**: `88` continuations; funds type decides the field count of a `16` record; control totals sum every amount including balances.
- **OFX**: no opening balance, so continuity is only checkable across downloads; de-duplicate on `FITID`; overlapping download windows are normal.

## Escalating to the bank

Have ready: account id, statement id and sequence, the raw file and its hash, the balances you computed and the ones the bank stated, the bank references of any disputed lines, and the transport receipt (SFTP log, EBICS order id, SWIFT message reference).
