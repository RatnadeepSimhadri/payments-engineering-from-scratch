# Phase 2: Algorithms

> An algorithm is a proof you can run.

16 lessons, 16 complete, ~20 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Sorting: Merge, Quick, Heap, and Why Libraries Use Timsort](01-sorting-algorithms/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/02-algorithms/01-sorting-algorithms --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Hook |
|:---:|--------|:----:|:----:|------|
| 01 | [Sorting: Merge, Quick, Heap, and Why Libraries Use Timsort](01-sorting-algorithms/) | Build | ~120 min | Stability matters when you sort transactions by amount and need time order preserved. |
| 02 | [Binary Search and Its Variants](02-binary-search-and-its-variants/) | Build | ~75 min | Lower bound, upper bound, and search over answer spaces. |
| 03 | [Two Pointers and Sliding Windows](03-two-pointers-and-sliding-windows/) | Build | ~60 min | Velocity checks are sliding-window problems. |
| 04 | [Recursion and Backtracking](04-recursion-and-backtracking/) | Build | ~75 min | Enumerating settlement allocations is a constrained search. |
| 05 | [Divide and Conquer and the Master Theorem](05-divide-and-conquer/) | Learn | ~60 min | Recurrences you can solve without memorizing. |
| 06 | [Dynamic Programming: From Recurrence to Table](06-dynamic-programming/) | Build | ~120 min | Coin change is literally change-making; knapsack is payout batching. |
| 07 | [Greedy Algorithms and Exchange Arguments](07-greedy-algorithms/) | Build | ~60 min | When taking the best local step is provably optimal, and when it is a bug. |
| 08 | [Graph Traversal: BFS, DFS, and Connected Components](08-graph-traversal-bfs-dfs/) | Build | ~90 min | Money-flow graphs, dependency graphs, and fraud rings all start here. |
| 09 | [Shortest Paths: Dijkstra, Bellman-Ford, and Negative Cycles](09-shortest-paths/) | Build | ~120 min | Cheapest FX route across currency pairs is a shortest-path problem with a twist. |
| 10 | [Topological Sort and DAG Scheduling](10-topological-sort-and-dags/) | Build | ~60 min | Settlement dependencies and build graphs. |
| 11 | [Minimum Spanning Trees: Kruskal and Prim](11-minimum-spanning-trees/) | Build | ~60 min | Union-find meets greedy. |
| 12 | [String Matching: KMP, Rabin-Karp, and Aho-Corasick](12-string-matching/) | Build | ~90 min | Sanctions screening scans millions of names against thousands of patterns. |
| 13 | [Interval Problems: Merging, Scheduling, and Sweep Lines](13-interval-problems/) | Build | ~60 min | Holds, authorizations, and overlapping settlement windows. |
| 14 | [Randomized Algorithms and Reservoir Sampling](14-randomized-algorithms/) | Build | ~60 min | Sampling transactions for audit without storing them all. |
| 15 | [Numerical Algorithms: GCD, Modular Arithmetic, and Check Digits](15-numerical-algorithms/) | Build | ~60 min | Luhn, mod-97 IBAN checks, and why they catch the errors they catch. |
| 16 | [Algorithm Design Patterns: A Field Guide](16-algorithm-design-patterns/) | Learn | ~45 min | How to recognize which pattern a new problem belongs to. |

Browse the [full curriculum](../../README.md#contents) or the status [roadmap](../../ROADMAP.md).
