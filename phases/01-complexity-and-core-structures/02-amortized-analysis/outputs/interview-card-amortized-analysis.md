---
name: interview-card-amortized-analysis
description: One-page card on amortized analysis for interviews and for reviewing latency claims about growable structures
phase: 1
lesson: 2
tags: [algorithms, complexity, amortized, interview, latency]
---

# Amortized Analysis: Interview and Review Card

## The claim, exactly

Amortized cost `a`: for **every** sequence of `n` operations starting from empty, `sum of actual costs <= n * a`. A bound on totals. Says nothing about any one operation. Not probabilistic.

| Phrase in docs | Bound | One call | n calls |
|----------------|-------|----------|---------|
| "constant time" | worst case | O(1) | O(n) |
| "amortized constant time" | amortized | unbounded | O(n) from empty |
| "constant time assuming good hashing" | expected | O(1) on average | O(n) on average |

## Three methods, one answer

- **Aggregate:** sum the actual costs in closed form, divide by `n`. Doubling array: copies `1 + 2 + ... < 2n`, total `< 3n`, so 3 per append.
- **Accounting:** charge each op an amortized price; bank the surplus as credit on the structure; credit must never go negative. Doubling array: charge 3 (1 write, 1 for its own future copy, 1 for an old element's).
- **Potential:** `Phi(state) >= 0`, `Phi(empty) = 0`, `a_i = c_i + Phi_i - Phi_{i-1}`. Doubling array: `Phi = 2 * size - capacity`; every append has `a = 3` exactly.

## Worked numbers to have ready

- 16 appends from capacity 1: 16 writes + 15 copies = 31 total; worst single append costs 9.
- Doubling array with growth factor `r`: copies per element approach `1 / (r - 1)`; 1.0 for 2x, 2.0 for 1.5x.
- Multipop stack: each element popped at most once, so total `<= 2n`; charge 2 per push, 0 per pop.
- Union-find: one `find` on a chain of 8 costs 7 and makes the next 7 finds cost 1; with union by rank and path compression, `O(alpha(n)) <= 4` per op.
- Shrink thrash: "shrink below half" gives amortized cost `O(n)` per op; "shrink below one quarter, to half" restores 3 (`Phi = |2 * size - capacity|`).

## When amortized is the wrong bound

- A per-operation deadline (request path, p99.9 SLO).
- A structure shared across tenants or threads: one party banks credit, another spends it.
- Reachable old versions (snapshots, undo, persistent structures): credit can be spent twice.
- An adversary who controls the sequence or the input distribution.

**Fix:** de-amortize. Spread the expensive step across the cheap ones: two-array migration for dynamic arrays (3 writes per append worst case, 3m peak memory), incremental rehash for hash maps (Redis, Go), pre-allocated next segment for logs.

## Interview questions and the one-line answers

- *"Why is ArrayList.add O(1)?"* Amortized: copies form a geometric series bounded by 2n.
- *"Is it O(1) for a single call?"* No. A single add can copy the whole array.
- *"Why charge 3 and not 2 in the accounting method?"* Half the elements spent their credit at the last doubling; each new element pays for itself and one of them.
- *"Give a potential function for the doubling array."* `2 * size - capacity`.
- *"What is wrong with shrinking at half full?"* The shrink lands exactly at full; the next add grows; no cheap operations separate the copies.
- *"How would you make append worst-case O(1)?"* Keep both arrays, migrate two elements per append; prove migration finishes before the new array fills.
- *"Amortized vs average case?"* Amortized is worst case over every sequence of totals; average case needs an input distribution.

## Review red flags

- A latency SLO justified by "the operation is O(1) amortized".
- A benchmark reporting only the mean for a structure with a resize or rehash.
- A shared or long-lived growable structure on a request path with no pre-sizing and no incremental resize.
- A shrink policy whose trigger is the inverse of its grow trigger.
