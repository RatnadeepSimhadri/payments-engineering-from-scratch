---
name: interview-card-range-queries
description: One-page card for choosing and whiteboarding Fenwick trees and segment trees in a systems interview or design review
phase: 3
lesson: 10
tags: [fenwick, segment-tree, range-queries, interview, balances]
---

# Range Queries Interview Card

## Five questions to ask before choosing

1. **Is the data mutable after load?** No: prefix-sum array, done. Yes: keep going.
2. **Is the operation invertible?** Sum, count, XOR: Fenwick is enough. Min, max, gcd, "any true": segment tree.
3. **Are updates to a point or to a range?** Point: Fenwick or plain segment tree. Range: lazy segment tree (or two Fenwick trees for range-add-range-sum).
4. **Is the index space fixed?** Positions cannot be inserted or deleted. If they can, you need a balanced tree or skip list with subtree aggregates instead.
5. **How many elements, and how much memory?** Fenwick: n + 1 words. Segment tree: 2 x next power of two, up to 4n. Lazy: 8n.

## Decision table

| Need | Structure | Query | Update | Memory |
|------|-----------|-------|--------|--------|
| Immutable data, range sum | prefix array | O(1) | O(n) | n |
| Point update, prefix or range sum | Fenwick | O(log n) | O(log n) | n + 1 |
| Point update, range min/max | segment tree | O(log n) | O(log n) | 2n to 4n |
| Range add, point query | Fenwick over differences | O(log n) | O(log n) | n + 1 |
| Range add, range sum | lazy segment tree, or two Fenwick trees | O(log n) | O(log n) | 8n or 2n |
| Range assign plus range add | lazy segment tree with tagged pending | O(log n) | O(log n) | 8n+ |
| Order statistic / rank over counts | Fenwick with descent | O(log n) | O(log n) | n + 1 |

## Three things to write on the whiteboard without hesitating

```java
// Fenwick: point add, prefix sum, 1-indexed array, lowbit = i & -i
void add(int i, long d)  { for (i++; i <= n; i += i & -i) tree[i] += d; }
long prefix(int i)       { long s = 0; for (i++; i > 0; i -= i & -i) s += tree[i]; return s; }

// Segment tree, bottom-up range sum, leaves at [size, 2*size), half-open [l, r)
long sum(int l, int r) {
    long a = 0, b = 0;
    for (l += size, r += size; l < r; l >>= 1, r >>= 1) {
        if ((l & 1) == 1) a += t[l++];
        if ((r & 1) == 1) b = t[--r] + b;
    }
    return a + b;
}

// Lazy: fully covered -> apply and stop; partially covered -> push, recurse, recompute
```

## Pitfalls and the test that catches each

| Pitfall | Symptom | Test |
|---------|---------|------|
| 0-indexed Fenwick | `lowbit(0) = 0`, infinite loop or wrong first block | `add(0, x); prefix(0) == x` |
| Forgetting `prefix(lo - 1)` can be `prefix(-1)` | off-by-one at the left edge | `rangeSum(0, 0) == values[0]` |
| Counting padding leaves | wrong sums when n is not a power of two | build with n = 5, query `[4..4]` and `[0..4]` |
| Lazy read without push | stale child returned | add to `[0..9]`, then `get(7)` equals 1 |
| Pending applied without `count` | sum off by a factor of the range length | `rangeAdd(2, 6, 10); rangeSum(2, 6) == 50` |
| Plain `+` on money | silent wrap to negative | `add(0, Long.MAX_VALUE); add(1, 1); prefix(1)` throws |
| Min on a Fenwick tree | correct until the first update that raises a value | raise a value and query the block |
| Non-commutative op with a single accumulator | wrong order of combination | matrix product or string concat over `[1..3]` |

## The payments framing in one paragraph

Closing balances per day are a prefix sum over daily postings. A backdated posting is a range add over every closing balance from the effective date to today. Average daily balance, the basis for deposit interest, is a range sum over the period divided by the day count, rounded with an explicit mode. A lazy segment tree makes both the posting and the query O(log n); a `running_balance` column with a trigger makes the posting O(days since effective date), which is the incident you are being asked to prevent.
