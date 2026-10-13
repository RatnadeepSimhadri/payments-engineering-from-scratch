---
name: interview-card-binary-search
description: One-page card on the firstTrue primitive, its variants as predicates, the two classic bugs, and the oracle test
phase: 2
lesson: 2
tags: [binary-search, interview, invariants, lower-bound]
---

# Binary Search Interview Card

## The one loop

```java
static int firstTrue(int lo, int hi, IntPredicate p) {   // smallest i in [lo, hi) with p(i), else hi
    while (lo < hi) {
        int mid = lo + ((hi - lo) >>> 1);
        if (p.test(mid)) hi = mid; else lo = mid + 1;
    }
    return lo;
}
```

Invariant to say out loud: everything before `lo` is false, everything from `hi` onward is true, the range is half-open and shrinks every iteration. Terminates in at most `ceil(log2(hi - lo)) + 1` probes.

## Every variant is a predicate

| Want | Predicate on i | Answer |
|------|----------------|--------|
| First index of t (lower bound) | `a[i] >= t` | `firstTrue` |
| One past last t (upper bound) | `a[i] > t` | `firstTrue` |
| Count of t | both | `upper - lower` |
| Count in [x, y] | both | `upper(y) - lower(x)` |
| Insertion point before equals | `a[i] >= t` | lower bound |
| Insertion point after equals | `a[i] > t` | upper bound |
| Last index where p is true (true-then-false) | `!p(i)` | `firstTrue - 1` |
| Minimum of rotated sorted array | `a[i] <= a[n-1]` | `firstTrue` |
| Largest x with f(x) <= limit, f monotone | `f(x) > limit` over values x | `firstTrue - 1` |
| Smallest x with g(x) >= need, g monotone | `g(x) >= need` over values x | `firstTrue` |

## The two bugs everyone ships

1. **Midpoint overflow.** `(lo + hi) / 2` wraps past 2^31 elements (or past 2^63 in a `long` answer space). Write `lo + ((hi - lo) >>> 1)`. In the JDK until 2006.
2. **Range convention mixing.** Closed `[lo, hi]` with `while (lo <= hi)` and `hi = mid - 1`; or half-open `[lo, hi)` with `while (lo < hi)` and `hi = mid`. Mixing them gives an infinite loop or a skipped boundary. Pick one per function, comment the invariant.

Also: `Arrays.binarySearch` with duplicate keys returns an unspecified matching index. If which one matters, use lower/upper bound.

## The test that proves it

Oracle test: for 200 random small sorted arrays with heavy duplicates, for every target from below min to above max, assert `lowerBound == linearLowerBound` and `upperBound == linearUpperBound`. Add: empty array, one element, all equal, target below all, target above all, and a range whose `lo + hi` exceeds `Integer.MAX_VALUE`.

## Answer-space checklist

- Is the feasibility check monotone in the answer? Prove it or test it on a sweep before searching.
- What are the tight bounds `[lo, hi)`? Make `hi` exclusive and large enough that a true always exists, or handle "none true" explicitly.
- Cost is `log(range) * cost(check)`. If the check is `O(n)` the search is `O(n log range)`.
- Return `firstTrue - 1` for "largest feasible", `firstTrue` for "smallest feasible"; decide what `-1` or `hi` means and test both.

## Say in the interview

"Binary search is a loop over a monotone predicate with an invariant on a half-open range. Lower bound and upper bound are two predicates on the same loop. Search over the answer space is the same loop with a feasibility check as the predicate. The midpoint is `lo + (hi - lo) / 2` because the sum overflows."
