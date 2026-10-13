---
name: interview-card-master-theorem
description: One-page card for solving divide-and-conquer recurrences by the master theorem and the recursion tree
phase: 2
lesson: 5
tags: [recurrences, master-theorem, interview, complexity]
---

# Master Theorem Interview Card

## Read the recurrence off the code

`T(n) = a T(n/b) + f(n)`: `a` recursive calls, each on `n/b`, plus `f(n)` work to divide and combine.

## Compute the critical exponent

`c = log_b a`. Memorise: `a = b` gives 1; `a = 1` gives 0; `a = b^2` gives 2; `log2 3 = 1.585`; `log2 7 = 2.807`; `log3 5 = 1.465`.

## Compare f(n) with n^c

| f(n) versus n^c | Case | Answer | Picture |
|-----------------|------|--------|---------|
| polynomially smaller (`n^(c-e)`) | 1 | `Θ(n^c)` | leaves dominate |
| equal (`Θ(n^c)`) | 2 | `Θ(n^c log n)` | every level equal |
| equal times `log^k n` | 2 extended | `Θ(n^c log^(k+1) n)` | every level nearly equal |
| polynomially larger (`n^(c+e)`), and `a f(n/b) <= k f(n)`, `k < 1` | 3 | `Θ(f(n))` | root dominates |

## Instant answers

| Recurrence | c | Case | T(n) |
|------------|---|------|------|
| `T(n/2) + 1` | 0 | 2 | `log n` |
| `T(n/2) + n` | 0 | 3 | `n` |
| `2T(n/2) + 1` | 1 | 1 | `n` |
| `2T(n/2) + n` | 1 | 2 | `n log n` |
| `2T(n/2) + n log n` | 1 | gap | `n log^2 n` |
| `2T(n/2) + n^2` | 1 | 3 | `n^2` |
| `3T(n/2) + n` | 1.585 | 1 | `n^1.585` |
| `4T(n/2) + n` | 2 | 1 | `n^2` |
| `4T(n/2) + n^2` | 2 | 2 | `n^2 log n` |
| `7T(n/2) + n^2` | 2.807 | 1 | `n^2.807` |
| `8T(n/2) + n^2` | 3 | 1 | `n^3` |
| `9T(n/3) + n` | 2 | 1 | `n^2` |

## Not master form

- `T(n-1) + f(n)`: unroll, `sum f(i)`. `T(n-1) + n` is `n^2`.
- `2T(n-1) + 1`: `2^n`. Any `a >= 2` with subtraction is exponential.
- `T(n/4) + T(3n/4) + n`: unequal split, tree has `Θ(log n)` levels of `n` each: `n log n`.
- `T(n-1) + T(n-2) + 1`: `φ^n`; memoise.

## Say in the interview

"Count the subproblems, read the size, add up the combine. The leaves do `n^(log_b a)` work; compare the root's `f(n)` to that. Smaller: leaves win. Equal: times log n. Larger: root wins. The number of subproblems sets the exponent; Karatsuba went from 4 to 3 and Strassen from 8 to 7, and that is where their speed comes from."

## Before writing code

Write the recurrence for the proposed design and for the simplest iterative alternative. If the recursion gives `n log n` and a single pass gives `n`, do not recurse. If the clever algorithm has a better exponent, find the crossover before assuming it is faster.
