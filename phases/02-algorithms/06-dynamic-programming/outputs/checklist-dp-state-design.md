---
name: checklist-dp-state-design
description: Design-review checklist for any dynamic-programming solution before the table is written, with the pseudo-polynomial trap for amounts in minor units
phase: 2
lesson: 6
tags: [algorithms, dynamic-programming, knapsack, batching, review]
---

# DP State Design Checklist

Run this before writing a table for any batching, allocation, change-making, or alignment problem. Answer every item in the design doc; the recurrence is the specification and the table is only its implementation.

## 1. Is it a DP problem at all?

- [ ] Optimal substructure is argued, not assumed: "if the optimal whole used choice X first, the rest is an optimal solution to the smaller problem, because otherwise swapping in the better remainder would improve the whole."
- [ ] Overlapping subproblems are shown: the number of distinct states is written down and is far smaller than the number of decision paths.
- [ ] Greedy has been ruled out explicitly. Either the exchange argument fails on a concrete counterexample (write it down: `{1, 3, 4}` for 6) or the team has chosen not to rely on a proof that the denomination set or priority structure stays canonical.

## 2. The state

- [ ] The state is written as a tuple with named components and their ranges (`(i: 0..n, c: 0..limit)`).
- [ ] Everything the future depends on is in the state. Pairwise exclusions, per-merchant caps, "at most k items", and currency all change the state; check each one.
- [ ] Base cases are listed with their values (`best[0][c] = 0`, `ways[0] = 1`, `len[0][j] = 0`) and a one-line reason for each.
- [ ] The recurrence has been checked against brute-force enumeration on at least three tiny inputs, including one where greedy is wrong and one where the answer is "unreachable".

## 3. Fill order and dependencies

- [ ] For every cell, the cells it reads are strictly earlier in the fill order. State the order ("amounts increasing", "rows increasing then columns increasing").
- [ ] If an item may be used once (0/1), the "take" branch reads the previous row. If it may be reused (unbounded, coin change), it reads the current row or a 1D array iterated upward. The choice is deliberate and commented.
- [ ] If the table counts things, the loop order is justified: denominations-outer counts unordered bundles; amounts-outer counts ordered sequences.
- [ ] `INF` or "unreachable" is represented so that `INF + 1` cannot overflow or wrap into a winning value.

## 4. Reconstruction

- [ ] The deliverable is the solution (which payouts, which instruments, which lines), not only its value. The design says where the choice is recorded (a `choice[]` array, a `took[][]` bitmap, or re-derivation from neighbours).
- [ ] The tie rule is stated ("lowest denomination index wins", "prefer consuming from the left sequence") and is identical in every implementation and every service that recomputes the plan.
- [ ] The reconstructed solution is re-validated with independent arithmetic: parts sum to the amount, total within the limit, common sequence is a subsequence of both inputs.

## 5. Cost

- [ ] Table size is computed for realistic inputs in cells and in bytes. Write the number down.
- [ ] If a dimension is an amount in minor units, the pseudo-polynomial trap has been assessed: a 10,000,000.00 limit in cents is a billion columns.
- [ ] If the resolution is reduced for the table, weights are rounded UP (never to nearest), and the chosen set is verified against the exact limit in minor units before anything is sent to a rail.
- [ ] Space optimisation (one rolling row) is used only when reconstruction is not needed or is handled separately by a bitmap.
- [ ] Recursion depth is bounded for memoised forms (Python's default limit is 1,000 frames); the production form is tabulated.

## 6. Money discipline (Phase 10, Lesson 01)

- [ ] Weights, limits, and amounts are integer minor units with a currency. No `double` anywhere in the table.
- [ ] Mixed currencies are rejected before the table is built.
- [ ] Priorities and counts use checked arithmetic (`Math.addExact`, hand-written overflow checks) or unbounded integers.

## Sign-off

| Question | Answer |
|----------|--------|
| State tuple and ranges | |
| Base cases | |
| Fill order | |
| Tie rule for reconstruction | |
| Table size for the largest realistic input | |
| What happens when the input is unreachable | |
