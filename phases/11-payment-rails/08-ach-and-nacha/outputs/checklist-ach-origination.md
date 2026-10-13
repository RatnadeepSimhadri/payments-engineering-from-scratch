---
name: checklist-ach-origination
description: Pre-flight and operating checklist for a service that originates NACHA files or consumes ACH return files
phase: 11
lesson: 8
tags: [ach, nacha, returns, batch-files, reconciliation]
---

# ACH Origination and Returns Checklist

Use this as the acceptance criteria for an ACH integration ticket and as the agenda for the first call with your ODFI's implementation team. Every box is a defect the lesson's parser or tests catch; one unchecked box is a reason to delay the first production file.

## Field validation at the edge

- [ ] Routing numbers are validated with the 3-7-1 mod-10 check digit at the point of capture (onboarding form, API), not at file-write time. A failing check digit is rejected with a message the user can act on.
- [ ] Routing numbers and account numbers are stripped of whitespace and punctuation before storage; the file writer never sees a leading space.
- [ ] Account numbers are 1-17 characters, alphanumeric, left-justified in the file. Longer values are rejected, never truncated.
- [ ] Individual names and company names are truncated to the field width (22 and 16 characters) only for alphanumeric fields; numeric fields that do not fit are an error.
- [ ] Amounts are `Money` in USD minor units. A non-USD amount fails before a line is written. Zero-amount entries are only allowed for prenotes (transaction codes 23, 28, 33, 38).
- [ ] Every entry's transaction code matches the batch's Service Class Code (200 mixed, 220 credits only, 225 debits only).
- [ ] Every batch has exactly one SEC code and every entry in it was authorized under that code's rules (PPD written or similarly authenticated consumer authorization, CCD business agreement, WEB internet authorization with the required fraud screen).

## Control totals and structure

- [ ] The entry hash is the sum of the eight-digit receiving DFI identifiers, reduced to its rightmost ten digits, computed per batch and summed per file.
- [ ] Entry/addenda count, total debits, and total credits are recomputed from the entries, never maintained incrementally.
- [ ] The parser recomputes all four totals on every received file (returns, NOCs, acknowledgements) and rejects the file on any mismatch.
- [ ] The file is padded with lines of 94 nines to a multiple of ten records and the Block Count in the File Control equals total records divided by ten.
- [ ] Batch Number in Batch Header and Batch Control agree; batches are numbered from 1 within the file.
- [ ] Every Entry Detail with addenda indicator 1 is followed by the declared addenda records; indicator 0 is followed by none.
- [ ] File ID Modifier (position 34 of the File Header) is unique per file per day per ODFI; the writer increments A-Z and never reuses one within a day.

## Trace numbers and persistence

- [ ] The trace number (ODFI routing prefix + seven-digit sequence) is assigned before the file is written and persisted on the payment record in the same transaction that marks the payment as sent.
- [ ] Trace numbers are unique within a file and monotonically increasing within a batch.
- [ ] A return or NOC whose original trace number does not match any sent entry is logged and alerted on, never silently dropped.
- [ ] The file itself (the exact bytes sent) is archived with its hash for at least the 60-day extended return window plus the retention period your ODFI agreement requires.

## Submission and windows

- [ ] The Effective Entry Date is a banking day; weekends and Federal Reserve holidays are rolled forward by your code, not by the operator.
- [ ] Same-day entries are flagged and capped at 1,000,000.00 USD; the file is submitted before the ODFI's own cutoff, which is earlier than the operator's 10:30 / 14:45 / 16:45 ET windows.
- [ ] A file that misses its window is held and re-dated, not sent with a stale Effective Entry Date.
- [ ] The ODFI's acknowledgement (accepted / rejected with reason) is parsed and the payment state machine moves to `submitted` only on acceptance.

## Return handling

- [ ] Every return reason code in Nacha Appendix Four maps to one of three actions: retry (R01, R09 only), collect new details (R02, R03, R04), or stop and investigate (R05, R07, R08, R10, R11, R29).
- [ ] An unknown reason code is treated as stop-and-investigate, not ignored.
- [ ] R01 and R09 re-presentments are limited to two more attempts within 180 days of the original settlement date, with the Company Entry Description set to `RETRY PYMT`.
- [ ] Settlement is not treated as final. Goods, payouts, or balance credits that depend on an incoming debit wait for the two-banking-day window (or are reversible) for operational reasons, and the business accepts the 60-day exposure for unauthorized returns.
- [ ] The return deadline is computed by your code (two banking days after settlement, or sixty calendar days for extended-window reasons) and a return received after its deadline is flagged for dishonour review.
- [ ] The unauthorized return rate (R05, R07, R10, R11, R29, R51 over all debits, rolling 60 days) and the overall return rate are computed daily and alerted at half of the Nacha thresholds (0.5 percent and 15 percent).

## Notifications of Change

- [ ] COR entries (reason codes C01-C13) are parsed and the corrected routing number or account number is applied to the stored payee before the next origination.
- [ ] A customer whose details changed is not re-debited with the old details; the rules require the correction within six banking days or before the next entry, whichever is later.

## Testing before go-live

- [ ] One test file containing every record type and every SEC code you will use has been accepted by the ODFI's validation tool.
- [ ] One test file per defect in the lesson's test suite (bad check digit, hash mismatch, short line, debit in a credits-only batch, missing addenda) has been rejected by the ODFI with a reason you have mapped to your parser's own error.
- [ ] A synthetic return file containing R01, R02, and R10 has been run through the return classifier and produced the expected three action lists.
- [ ] The round trip `write -> parse -> verify -> write` produces byte-identical output for the payroll demo file.
