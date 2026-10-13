---
name: design-doc-exhaustive-search
description: Design-review template for any feature that enumerates combinations, allocations, or schedules by search
phase: 2
lesson: 4
tags: [backtracking, search, design-review, pruning]
---

# Exhaustive Search Design Review

Fill in every section before writing a backtracking search. A blank section is a reason the review does not pass.

## 1. The question

- What is enumerated? (subsets of ..., orderings of ..., assignments of ... to ...)
- All solutions, the first solution, or the best solution by a stated ranking?
- What does the caller do with more than one solution? With zero?

## 2. Size of the space

| Quantity | Typical | p99 | Maximum ever observed |
|----------|---------|-----|-----------------------|
| n (items) | | | |
| Unpruned leaves (2^n, n!, C(n,k), ...) | | | |
| Depth of recursion | | | |

If the p99 unpruned leaf count exceeds about 10^8, the search needs either strong pruning (section 3) with measured node counts, or a different algorithm (section 6).

## 3. Pruning rules

For each rule: the test at a node, why no leaf below a pruned node can be a solution (the proof of exactness), and the measured node count with and without it on representative inputs.

| Rule | Test | Exactness argument | Nodes without | Nodes with |
|------|------|--------------------|---------------|------------|
| | | | | |

Ordering heuristic used (largest first, most constrained first) and its measured effect.

## 4. Budget and fallback

- Node budget per search (a counter checked in the hot loop):
- Behaviour on budget exceeded (return partial results flagged incomplete, route to manual, fail the request):
- Wall-clock budget and how it is enforced (counter, not a timer thread):

## 5. Recursion depth

- Maximum depth = (items? data size?):
- Is it bounded by the input size or by the data volume? If data volume, the search is written with an explicit stack.
- Stack size assumption documented, or not relied on.

## 6. Regime check

Answer each; a "yes" points away from backtracking.

- [ ] n is large (> 30) and constraints are loose (target in the middle of the achievable range)? Consider dynamic programming over a bounded target (Phase 2, Lesson 06).
- [ ] One optimal solution is needed rather than all solutions? Consider branch-and-bound with a bound function, or DP.
- [ ] The same partial state is reachable by many paths? Memoise; the tree is a DAG.
- [ ] Hard constraints with hundreds of variables? Consider a constraint solver (CP-SAT, MiniZinc) rather than a custom search.

## 7. Tests

- [ ] Small-input oracle: pruned search equals unpruned search on every input small enough to run both.
- [ ] Every returned solution is verified against the original constraint (sum equals target, no two queens attack).
- [ ] Solutions are distinct.
- [ ] Zero-solution and single-solution inputs.
- [ ] Node counts asserted with bounds, so a regression in pruning fails a test.
- [ ] If an iterative version exists: same solutions, same order, same node count as the recursive one.
