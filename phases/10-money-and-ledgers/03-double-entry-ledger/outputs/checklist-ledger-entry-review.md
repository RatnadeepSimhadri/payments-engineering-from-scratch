---
name: checklist-ledger-entry-review
description: Review checklist for any code that constructs or posts journal entries to a double-entry ledger
phase: 10
lesson: 3
tags: [ledger, double-entry, review, idempotency, reversal]
---

# Ledger Entry Review Checklist

Use on every pull request that adds a new entry type (a new fee, a new payout flow, a new adjustment) or touches the posting path. One unchecked box is a reason to request changes.

## The entry

- [ ] Every posting is a positive amount on an explicit side (debit or credit). No signed amounts, no zero postings.
- [ ] The entry has at least two postings and, for every currency it touches, debits equal credits. The check is in the entry's constructor or an equivalent single choke point, not repeated at call sites.
- [ ] No entry nets one currency against another. A conversion is two balanced entries through an FX clearing account.
- [ ] The entry id is a deterministic function of the business event (processor capture id, payout batch id), never a fresh UUID per attempt.
- [ ] The description names the business event, not the code path ("card payment captured", not "handleWebhook").
- [ ] Amounts come from the `Money` type with an explicit rounding mode where a rate is involved (see `checklist-money-handling`).

## Accounts and sides

- [ ] Each account in the entry exists, has the posting's currency, and has a type whose normal balance the author can state.
- [ ] For a new entry type, a table in the PR description shows each posting, its side, and the effect on each account's normal-direction balance.
- [ ] A liability or revenue account is credited to increase it and debited to decrease it; an asset or expense account the reverse. Check this against the table above, not against intuition about "money in".

## Posting path

- [ ] All validation (idempotency, account existence, currency match, reversal target) runs before the first mutation. A rejected entry leaves journal, index, and totals unchanged.
- [ ] A replay (same id, identical content) returns success without appending. A conflict (same id, different content) is rejected with a specific error, never silently ignored or overwritten.
- [ ] Cached balances are updated in the same atomic step as the journal append, and a test asserts cache equals recomputation from postings.
- [ ] The journal is append-only from the outside: no public mutator, no exposed mutable list.

## Corrections

- [ ] Mistakes are fixed with a reversal entry (every side flipped, `reversalOf` set), never by editing or deleting the original.
- [ ] An entry cannot be reversed twice. A reversal of an unknown entry is rejected.
- [ ] Partial corrections post a new entry for the difference; they do not reverse and re-post unless the business event genuinely did not happen.

## Tests present

- [ ] Unbalanced entry rejected, with the imbalance amount in the message.
- [ ] Single-posting entry rejected.
- [ ] Mixed-currency entry rejected.
- [ ] Trial balance totals equal after the new entry type is posted alongside existing ones.
- [ ] Post then reverse is the identity on every account.
- [ ] Duplicate id with identical content is a no-op; duplicate id with different content is a conflict; neither changes a balance.
- [ ] A rejected entry leaves journal size and balances unchanged (partial-write test).
