---
name: checklist-holds-and-available-balance
description: Review checklist for any code path that authorizes, captures, voids, or expires a reservation of funds
phase: 10
lesson: 7
tags: [holds, available-balance, authorization, concurrency, review]
---

# Holds and Available Balance Review Checklist

Use on every pull request that touches an authorization, capture, release, or balance-check path. One unchecked box is a reason to request changes.

## The three balances

- [ ] Ledger balance, held total, and available balance are three distinct quantities in the code, not one `balance` column that is adjusted on authorization.
- [ ] Available is derived (`ledger - sum(active holds)`), or if it is cached there is an oracle that recomputes it from scratch and a test that compares the two after a randomized sequence of operations.
- [ ] Every authorization, incremental authorization, and unauthorized settled debit is checked against **available**, never against ledger.
- [ ] The invariant `available >= 0` is asserted in a test after every operation, including rejected ones.

## Transitions

- [ ] Placing a hold does not move the ledger balance.
- [ ] Capture moves the ledger by the **captured** amount, not the held amount, and releases any uncaptured remainder immediately (not at the original expiry).
- [ ] Over-capture is rejected by default; any tolerance (tips, fuel) is a named parameter with the scheme rule cited in a comment.
- [ ] Void returns the whole held amount to available and leaves the ledger untouched.
- [ ] Capture, void, and increment on a hold that is not `ACTIVE` fail with an error that names the hold's actual state.
- [ ] A hold records both its authorized amount and its captured amount so statements can show "authorized X, settled Y".

## Rejections leave no trace

- [ ] Every validation (positive amount, same currency, duplicate id, sufficient available, active status) runs before the first write.
- [ ] A test posts a rejected operation and asserts ledger, held, available, and the hold's state are unchanged.
- [ ] The hold id is a deterministic function of the business event (network authorization id), not a fresh UUID per attempt, and a duplicate id is an error rather than a second hold.

## Time

- [ ] The clock is injected. No `System.currentTimeMillis()`, `time.time()`, or `time.Now()` inside the account.
- [ ] Expiry is evaluated against the injected clock on every access (lazy sweep). A background sweeper may exist for freshness but is not required for correctness.
- [ ] A test checks the expiry boundary exactly: still active at `expiresAt - 1`, released at `expiresAt`.
- [ ] Hold types that must never expire on a timer (risk freezes, legal holds) are distinguished from card authorizations.

## Concurrency

- [ ] Check-then-reserve is one atomic step under a per-account lock (or a database row lock / conditional update). The read of available and the insertion of the hold cannot be interleaved by another writer.
- [ ] The lock is per account, not global.
- [ ] A race test exists: N threads request holds that together exceed available, and exactly the affordable number succeed. In Go the test runs under `-race`.
- [ ] Nested calls under the lock are safe: reentrant monitor (Java `synchronized`, Python `RLock`) or explicit `*Locked` helpers (Go `sync.Mutex`).

## Money

- [ ] Amounts are the integer-minor-units `Money` type with a currency. No floats.
- [ ] A hold in a different currency from the account is a `CurrencyMismatch`, not a silent conversion.

## Tests present

- [ ] Authorize reduces available but not ledger.
- [ ] Partial capture: ledger moves by captured amount, remainder released, hold marked captured.
- [ ] Over-capture rejected and state unchanged.
- [ ] Void returns the full amount; void twice fails.
- [ ] Expiry boundary at exactly `expiresAt`.
- [ ] Debit beyond available rejected even when ledger covers it.
- [ ] Seeded random sequence keeps the invariant.
- [ ] Concurrent authorizations admit exactly the affordable count.
