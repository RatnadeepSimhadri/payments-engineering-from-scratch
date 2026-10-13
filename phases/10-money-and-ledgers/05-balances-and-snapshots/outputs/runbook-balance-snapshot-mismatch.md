---
name: runbook-balance-snapshot-mismatch
description: On-call runbook for when a cached balance or balance snapshot disagrees with the journal
phase: 10
lesson: 5
tags: [ledger, balances, snapshots, runbook, incident]
---

# Runbook: Balance or Snapshot Disagrees with the Journal

Trigger: the nightly verification job (or a support ticket) reports that a running total, a `balance_after` column value, or a balance snapshot differs from the full fold of the journal for one or more accounts.

**Rule zero: the journal is the source of truth.** Every number below the journal is a cache. You will fix caches. You will not edit the journal to match a cache.

## 1. Stop the bleeding (first 15 minutes)

- [ ] Identify which read path is wrong: running total, per-posting running balance, or snapshot. Note the account ids, the seq (or snapshot date), the cached value, and the fold value.
- [ ] If a snapshot is wrong, mark it quarantined so readers fall back to the previous verified snapshot plus replay. Reads get slower; they do not get wrong.
- [ ] If the running total is wrong, switch the affected accounts' balance reads to "latest verified snapshot + replay" until fixed.
- [ ] Pause any payout, sweep, or statement job that reads the affected accounts. A wrong balance that pays out becomes a recovery problem with a real merchant.

## 2. Scope (first hour)

- [ ] Find the first seq at which cache and fold disagree. Binary-search over verified snapshots: the last snapshot that verifies and the first that does not bracket it.
- [ ] Check whether the difference equals one or more specific entries (an entry applied twice, or skipped). Search the journal between the bracketing snapshots for entries whose amount equals the difference.
- [ ] Check deploy and change logs for the window: snapshot writer changes, manual `UPDATE` statements on balance tables, restores from backup, schema migrations.
- [ ] List every downstream consumer that read the bad value: payouts, statements, risk limits, reporting exports.

## 3. Repair

- [ ] Recompute the cache from the last verified snapshot by replaying the journal, using the same code path the verifier uses. Do not hand-type a corrected number.
- [ ] Verify the recomputed cache against the full fold before un-quarantining it.
- [ ] If a payout or statement used the wrong value, open a separate correction: a new journal entry in the current period that references the incident (see Lesson 06). Never rewrite the old payout entry.

## 4. Prevent

- [ ] Add a regression test that replays the incident's entries and asserts `snapshot + delta == fold` at every seq around the failure.
- [ ] Remove any write path that updates a balance table without inserting postings in the same transaction.
- [ ] Confirm the verification job runs on every new snapshot, not only nightly, if the snapshot is used by payouts.
- [ ] Record the incident in the audit evidence log (Lesson 12): what was wrong, from when to when, how it was found, how it was fixed.

## Do not

- Do not "true up" a balance by posting an unexplained adjustment entry to make the cache and the journal agree.
- Do not delete or rewrite journal entries, even ones that look duplicated; duplicates are corrected by reversal (Lesson 06).
- Do not resume payouts until the recomputed balances verify.
