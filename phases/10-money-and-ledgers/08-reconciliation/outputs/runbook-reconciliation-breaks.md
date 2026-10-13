---
name: runbook-reconciliation-breaks
description: Operations runbook for working a daily reconciliation break list, one section per break kind, plus the tie-out check
phase: 10
lesson: 8
tags: [reconciliation, operations, ledger, runbook]
---

# Reconciliation Breaks Runbook

Use this when the daily reconciliation report is not clean. Work the breaks in report order: missing money first, duplicates last. Never edit a posted entry; every correction is a new, dated adjusting entry that cites the break.

## Before touching any break: the tie-out

Confirm these three lines on the report with a calculator. If any fails, stop: the report is wrong, not the books.

```text
ledger total     == matched total + ours unmatched
statement total  == matched total + theirs unmatched
ledger total - statement total == ours unmatched - theirs unmatched
```

Also check the ratio of `EXACT_REFERENCE` to `AMOUNT_DATE` matches against yesterday. A sudden rise in fuzzy matches means the reference field broke upstream; fix that before working individual breaks, because many of today's breaks will disappear.

## MISSING_OURS (on their statement, not in our ledger)

1. Search the raw webhook / event log for the reference. If the event arrived but the posting failed, you have a consumer bug; post the entry and open an incident.
2. If the event never arrived, check the processor dashboard for delivery failures during the window; replay it through the normal consumer, never by hand.
3. If the transaction is unknown to the processor dashboard too, treat it as a possible fraud or test-mode leak and escalate before booking anything.

Adjusting entry: the same entry the consumer would have posted, dated today, description `recon MISSING_OURS <reference> <statement date>`.

## MISSING_THEIRS (in our ledger, not on their statement)

1. Check the age. Under the counterparty's settlement lag (T+1, T+2) it is a timing difference: carry forward, do nothing.
2. Over the lag: look up the transaction with the processor. Declined or voided after we posted means our optimistic posting must be reversed.
3. Check whether the same amount appears on their statement under a different reference on a nearby date and was consumed by another fuzzy match; if so, the other match may be wrong.

Adjusting entry: a reversal of the original entry (flip every side), description `recon MISSING_THEIRS reversal of <entry id>`.

## AMOUNT_MISMATCH (same reference, different amounts)

1. Read the sign of `diff = ours - theirs`. Negative means they recorded more.
2. A difference equal to a plausible fee means one side netted fees and the other did not; fix the mapping, not the number.
3. A difference whose digits are a permutation of each other (12.00 vs 21.00) is a keying error; find which side's source document is right.
4. Partial captures: their line may be one of several under the same id; confirm the sum.

Adjusting entry: only after the cause is known; book the difference to the account the cause names (fee expense, FX difference), never to a suspense account "for now".

## DUPLICATE_OURS (we have more copies than they do)

1. Compare the two entries' timestamps and originating request ids. A retry within seconds with no idempotency key is the usual cause.
2. Reverse the later copy. Keep the earlier one so that it still matches.
3. File the idempotency gap as a bug with the request ids attached.

## DUPLICATE_THEIRS (they have more copies than we do)

1. Do not book it. A double-posted line on a bank or processor statement is their error.
2. Check whether the second copy is actually a reversal encoded as a positive line with a code; some formats do this.
3. Raise a dispute with the counterparty quoting the reference and both line positions; carry the break forward until their corrected statement arrives.

## Closing the period

The period closes when every break is either matched on a later run, booked with an adjusting entry that cites it, or carried forward with an owner and an age. A break with none of those is an open control failure.
