---
name: interview-card-pattern-recognition
description: One-page card for recognizing which algorithmic pattern a new problem belongs to, with signals, counter-signals, and the proof each pattern owes
phase: 2
lesson: 16
tags: [algorithms, patterns, interview, design-review]
---

# Pattern Recognition Card

Use in the first minute of an algorithms interview or a design review, before any code.

## The four questions

1. **Shape of the input?** sequence / stream, intervals, graph, weighted items, a number in a range.
2. **What is returned?** yes-no, count, optimum, optimum plus the choice, all solutions, an ordering.
3. **Can a local choice ever need undoing?** No (exchange argument) means greedy. Yes with overlapping subproblems means DP. Yes without overlap means search.
4. **Is anything monotone?** A feasibility check monotone in a number means binary search on the answer. A window property monotone in its size means sliding window or two pointers.

Then: input size times per-item cost against the budget. `10^5`-`10^6` wants `n log n`; `10^7`+ wants one pass; a stream wants `O(1)` per item and `O(k)` memory.

## The catalogue in one line each

| Pattern | Reach for it when | Not when | Owes |
|---|---|---|---|
| Sliding window | contiguous range, property updates in `O(1)` | property needs a rescan (median) | the incremental update |
| Two pointers | sorted input, monotone pointer movement | unsorted and cannot sort | the direction rule |
| BFS / DFS | reachability, components, hops, cycles, ordering | weighted costs | a visited set |
| Shortest path | minimize additive cost on a weighted graph | negative cycles | non-negative weights (Dijkstra) |
| DP | count or optimize with overlapping subproblems | exponential state, or greedy works | the recurrence and base cases |
| Greedy | sort by a key, one pass, exchange argument | no exchange argument (then it is a heuristic) | the exchange argument |
| Binary search on answer | min or max `X` with a monotone check | non-monotone check | monotonicity |
| Union-find | incremental "same group" facts | need paths or edge deletion | nothing beyond amortized bounds |
| Heap | repeated min or max from a changing set | one-shot `k`-th element (quickselect) | key stability or lazy deletion |

## Combinations to name out loud

- Binary search on the answer with a greedy check inside (`O(n log range)`).
- Topological order with a DP inside (critical path, path counting).
- Sort, then two pointers or a sweep (`O(n log n)`): intervals, pair sums, Kruskal.
- Graph traversal with a heap as the queue: Dijkstra, Prim.
- A hash map inside any of them to make "seen before" `O(1)`.

## Sentences that end a wrong turn

- "Edges have costs, so BFS gives hops, not cost."
- "Can I write the exchange argument? If not, this greedy is a heuristic and I should say so."
- "Is the check monotone? If 5,000,000 works but 5,000,001 does not, binary search converges on nothing."
- "Is the window property incrementally updatable, or is this `O(nk)` in disguise?"
- "Will anyone ask to remove a link later? Then union-find is the wrong structure."
- "Coins 1, 3, 4, amount 6: greedy gives three coins, optimal is two. Is this fee table canonical?"

## The answer format

"This is a *[pattern]* over *[shape]*, `O(...)` per *[unit]*, because *[the property that makes it apply]*. It owes *[the proof]*, which holds because *[reason]*. The tempting wrong answer is *[pattern]*, which fails because *[counter-signal]*."

If you cannot fill the last sentence, you have not understood the problem yet.
