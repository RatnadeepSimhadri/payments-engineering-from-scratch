---
name: runbook-reconciliation-breaks
description: Morning-after runbook for a large-scale reconciliation run, from the tie-out check through triage of each break type, ownership, and escalation by age
phase: 14
lesson: 4
tags: [reconciliation, breaks, aging, tie-out, runbook]
---

# Reconciliation Breaks Runbook

For the engineer or finance-operations analyst who owns the nightly reconciliation report. Work top to bottom. Nothing below the first section is worth reading if the report does not tie out.

## 1. Before reading a single break

- [ ] **The report ties out.** `internalTotal == matchedTotal + internal-side break amounts` and `processorTotal == matchedTotal + processor-side break amounts`, exactly, in minor units, per currency. If either fails, the report is wrong even if every break in it looks right. Stop, mark the run invalid, and page the reconciliation owner.
- [ ] **Side totals match the sources' own totals.** The processor total equals the settlement file's header or trailer (for `camt.053`, the closing balance minus opening balance; for MT940, fields 60F and 62F). The internal total equals the ledger's capture total for the same transaction-date range. A mismatch here means the feed was truncated or double-loaded before the reconciler saw it.
- [ ] **Record counts are plausible.** Each side's count is within 10% of the trailing 7-day average for the same weekday. A file with half its usual lines is a delivery problem, not 20 million breaks.
- [ ] **Both feeds were sorted.** The run completed without a "feed is not sorted" rejection. If it was rejected, re-sort by `(transaction date, currency, amount minor units)` and rerun; never disable the check.
- [ ] **Fast-path ratio is normal.** Fast-matched buckets divided by buckets seen is within its usual band (typically above 99.5%). A sudden drop means a systemic cause such as a bucket key built from settlement date, a currency code change, or an FX rounding change on one side.
- [ ] **Leftover count is within bounds.** If the cross-bucket pass held more than the configured ceiling (for example 1% of records), the job should have stopped. Treat a near-ceiling count as a feed-level incident, not as a list of individual breaks.

## 2. Triage by break type

| Type | First three checks | Owner | Escalate when |
|------|--------------------|-------|---------------|
| `MISSING_AT_PROCESSOR` | Capture message sent and acknowledged? Present in the next day's file? Same transaction under a different reference? | Payments ops, then acquirer | Age 3 days; age 30 days goes to acquirer relationship manager |
| `MISSING_INTERNAL` | Ledger write logged and committed? Capture posted under another reference or merchant? Refund or reversal booked instead? | Ledger team | Same day: we hold money we cannot attribute |
| `AMOUNT_MISMATCH` | Partial capture or incremental auth? FX applied on one side only? Rounding mode differs (HALF_EVEN vs HALF_UP)? | Both sides, with evidence | Any single difference over the materiality threshold; any pattern of more than 10 same-sized differences |
| `TIMING` | Lag explained by weekend or bank holiday calendar? Acquirer batch delayed for the whole day? Tolerance configured for this acquirer? | Usually waits | Lag over 7 days, or the record has not settled by the next run |
| `DUPLICATE` | Same line twice in the file? Capture retried without an idempotency key? Duplicate across files from two delivery attempts? | Whichever side has two | Immediately if internal: a duplicate capture may have charged a cardholder twice |

## 3. Aging and deadlines

- [ ] Every open break has an age measured from its transaction date, not its settlement date or the date it was first reported.
- [ ] Breaks in the 0-1 day band are reviewed but not escalated unless they are `MISSING_INTERNAL` or internal `DUPLICATE`.
- [ ] Breaks in the 2-7 day band each have a named owner and a ticket.
- [ ] Breaks in the 8-30 day band are listed in the weekly finance review with amount and owner.
- [ ] Breaks over 30 days are checked against the scheme's dispute and retrieval deadlines (commonly 45 to 120 days). Any break within 15 days of its deadline is raised with the acquirer now, with evidence attached.
- [ ] The oldest open break and its age are exported as a metric so an alert fires when it crosses the threshold (Lesson 06).

## 4. Patterns that mean "fix the system, not the break"

- [ ] Many `AMOUNT_MISMATCH` breaks differing by exactly one minor unit: a rounding-rule drift between services. Compare the rounding mode and division semantics on both sides (Phase 10, Lesson 01).
- [ ] Many `TIMING` breaks with the same lag: the tolerance is wrong for this acquirer or a holiday calendar is missing.
- [ ] `MISSING_INTERNAL` and `MISSING_AT_PROCESSOR` with the same amounts and dates but different references: a reference-mapping change on one side. Pair them manually, then fix the mapping.
- [ ] Cross-bucket duplicates surfacing as `MISSING_INTERNAL`: the reconciler forgets matched references by design; check whether the reference was already matched in an earlier bucket before chasing a missing ledger entry.

## 5. Declaring the report final

- [ ] Tie-out holds per currency and side totals match the source files.
- [ ] Every break has a type, an age, and an owner.
- [ ] No break was "resolved" by editing the report; corrections are new ledger entries or a corrected file followed by a rerun.
- [ ] Digest collisions are accepted as a documented risk (roughly `k / 2^64` per bucket); nobody has proposed fixing a break by assuming one.
- [ ] The run identifier, input file checksums, and summary are archived so the auditor can reproduce the report.

Ask before signing off: if one record had silently gone missing from either feed, which line of today's summary would have shown it?
