---
name: runbook-screening-threshold-review
description: Quarterly procedure for reviewing and documenting a sanctions-screening threshold against a labelled set of past alerts
phase: 13
lesson: 4
tags: [sanctions, screening, threshold, compliance, runbook]
---

# Screening Threshold Review Runbook

Run quarterly, after any change to the matching engine or the list sources, and before any regulatory examination. Output is a one-page memo to the compliance officer plus the precision/recall table.

## 1. Assemble the labelled set

- [ ] Export every alert from the last quarter with its final disposition: `true match` (escalated and confirmed), `false positive` (cleared), or `pending`. Drop pending.
- [ ] Export a sample of cleared-without-alert transactions whose names share a token or a Soundex key with a listed name. These are the hard negatives; without them the table overstates precision.
- [ ] Add every known true match from previous quarters so the set accumulates. True matches are rare; never discard one.
- [ ] Add synthetic variants of listed names if true matches number fewer than 50: reordered tokens, one-character misspellings, initials, particle dropped, common transliteration swaps. Label them clearly as synthetic in the memo.
- [ ] Record the set's size, the true/false split, and the date range.

## 2. Compute the table

- [ ] Run the current matcher over every pair at thresholds 0.80, 0.85, 0.88, 0.90, 0.92, 0.95, 1.00.
- [ ] For each threshold record tp, fp, fn, tn, precision, recall, and the projected weekly alert volume (fp rate multiplied by weekly screened names).
- [ ] Confirm monotonicity: recall and fp both non-increasing as the threshold rises. A violation means the matcher is non-deterministic or the labelled set has duplicates with conflicting labels.
- [ ] For every false negative at the current threshold, write one line: the pair, the score, and which evidence kind would have been needed to catch it (initial, phonetic, nickname, particle, transliteration).

## 3. Decide

| Downstream | Favour | Reasoning to document |
|------------|--------|----------------------|
| Fully automated block, no analyst | Higher threshold plus a second stage | Blocking legitimate customers is a harm; the misses must go somewhere |
| Analyst queue with capacity | Lowest threshold at which recall is 1.000 on the labelled set | Missed matches are the fined failure; analyst hours are the budget |
| Analyst queue over capacity | Current threshold plus secondary-identifier auto-clear | Lowering recall is not an option; cut false positives with date of birth and nationality instead |

- [ ] State the chosen threshold, the recall and precision at that threshold, and the projected weekly alert volume.
- [ ] State the analyst capacity in alerts per week and show the projected volume fits.
- [ ] If the threshold changed, run both old and new over the last quarter's screened names and list every name whose outcome differs.

## 4. Document for the examiner

- [ ] The matching rules in plain language: normalization steps, similarity measure, phonetic key, token alignment, particle handling, alias handling.
- [ ] The threshold, who approved it, and the date.
- [ ] The table from step 2 and the labelled set's provenance.
- [ ] The list of false negatives from step 2 and what was done about each (rule added, accepted risk with rationale).
- [ ] Evidence that the screener and the review tool produce identical scores for the same pair (same normalization table, same constants). Attach the test vector output.
- [ ] The explanation format shown to analysts, with three real examples (redacted) of a cleared hit and one of an escalated hit.

## 5. Signals to review out of cycle

- Alert volume changes by more than 25% week over week without a list update.
- A list update adds entries in a script or naming culture the normalization table does not cover.
- A true match is found by any route other than the screener (a counterparty's notification, a news report).
- The matching engine, its constants, or its normalization table are changed for any reason.
