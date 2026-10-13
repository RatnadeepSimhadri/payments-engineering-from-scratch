---
name: checklist-greedy-review
description: Review checklist for any design or pull request that sorts by a criterion and takes items in order without revisiting
phase: 2
lesson: 7
tags: [greedy, review, exchange-argument, optimality]
---

# Greedy Algorithm Review Checklist

Use when a design says "largest first", "earliest first", "cheapest first", or "fill the biggest bucket". A greedy algorithm is fast by construction and correct only by proof.

## The claim

- [ ] The objective being optimised is written down as a formula, not a phrase ("fewest transfers", "maximum windows on one lane", "minimum total bits").
- [ ] The greedy criterion (the sort key) is written down, including the tie-break.
- [ ] It is stated whether the algorithm must be optimal or is a heuristic. A heuristic is acceptable only if the document says so and quantifies the gap.

## The proof

- [ ] An exchange argument exists: take any optimal solution, swap in the greedy choice, show it stays feasible and no worse, show the process terminates.
- [ ] Or a lower-bound argument exists: the greedy's result equals a quantity no solution can beat (max overlap depth for lanes).
- [ ] Or the problem is a known greedy-optimal problem (interval scheduling by end time, Huffman, fractional knapsack, MST, Dijkstra on non-negative weights, canonical change-making) and is cited.
- [ ] The obvious alternative criteria have been considered and their counterexamples are written down (shortest-first, earliest-start-first for intervals).

## The cost function

- [ ] If the cost has tiers, steps, minimums, or caps, greedy is presumed wrong until tested. Tier boundaries behave like non-canonical coin denominations.
- [ ] For denomination-like problems (coins, transfer caps, batch sizes), the canonicality check has been run: greedy equals the DP optimum for every amount below the sum of the two largest denominations.
- [ ] If the problem is 0/1 (items cannot be split), greedy by ratio has been rejected in favour of DP, or the gap has been measured.

## Determinism

- [ ] Ties in the sort key are broken by a total, deterministic rule (sequence number, creation order), so two runs and two implementations agree.
- [ ] Heaps (`PriorityQueue`, `heapq`, `container/heap`) are not relied on for tie order; the comparator includes the tie-break.
- [ ] Half-open versus closed intervals is stated, and the compatibility test (`>=` or `>`) matches it.

## Tests

- [ ] A brute-force or DP oracle test on small random instances: greedy equals the true optimum on every instance.
- [ ] The counterexample for each rejected alternative criterion is a test.
- [ ] Edge cases: empty input, one item, all items identical, items that touch exactly at a boundary.
- [ ] If the greedy is a heuristic: a test that measures its gap from the oracle and asserts a bound.
