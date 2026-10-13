---
name: interview-card-minimum-spanning-trees
description: One-page card on Kruskal, Prim, the cut property, and the traps that separate an MST from a shortest-path tree
phase: 2
lesson: 11
tags: [graphs, greedy, union-find, interview]
---

# Minimum Spanning Trees: Interview Card

## The problem in one sentence

Given a connected undirected graph with edge weights, choose a subset of edges of minimum total weight that still connects every node. The answer always has exactly `V - 1` edges and no cycle.

## Two algorithms

| | Kruskal | Prim |
|---|---|---|
| Idea | lightest edge anywhere that does not close a cycle | lightest edge leaving the tree built so far |
| Structure | edges sorted by weight + union-find | min-heap of frontier edges, lazy deletion |
| Time | `O(E log E)` | `O(E log E)` binary heap; `O(E + V log V)` Fibonacci heap |
| Space | `O(V)` union-find | `O(E)` heap |
| Prefer when | sparse graph, edges already sorted, or you want clustering | dense graph with adjacency lists |
| Disconnected input | forest for free | restart from each unvisited node |

## The proof, in four lines

1. Take any cut `(S, V - S)` and a lightest edge `e` crossing it.
2. Suppose an MST `T` omits `e`. The tree path between `e`'s endpoints crosses the cut at some edge `f`, with `w(f) >= w(e)`.
3. `T - f + e` is a spanning tree of weight `<= w(T)`, so it is also an MST, and it contains `e`.
4. Kruskal's edge and Prim's edge are each a lightest edge across some cut, so each greedy step is safe and never needs undoing.

## Say these out loud

- "Union by rank plus path compression makes the cycle test amortized inverse-Ackermann, effectively constant; the sort dominates."
- "Prim is Dijkstra without the `dist[u] +` term in the heap key."
- "With distinct weights the MST is unique; with ties, both algorithms are correct but may choose different edges, so the tie-break must be deterministic."
- "I can verify any claimed MST independently: for every tree edge, no lighter graph edge crosses the cut that edge defines."

## Traps

- **A shortest-path tree is not an MST.** Dijkstra minimizes distance from one root; MST minimizes total edge weight. The MST path between two nodes can be up to `V - 1` times longer than the shortest path.
- **No redundancy.** Removing any MST edge disconnects the graph. Real network designs add edges for a second disjoint path; the MST is the cost floor, not the design.
- **Hash-ordered iteration breaks reproducibility.** Equal-weight edges will be picked in a different order on each run unless the sort is stable and the graph preserves insertion order.
- **Negative weights are fine for MST** (unlike Dijkstra) but usually indicate a data error in a cost graph; decide which and validate accordingly.
- **Directed graphs are a different problem.** Minimum spanning arborescence (Edmonds/Chu-Liu) is not solved by either algorithm here.

## Connections to name

- **Clustering:** cut the `k - 1` heaviest MST edges for single-linkage clustering into `k` groups (SciPy `linkage(method="single")`, HDBSCAN's backbone). Fraud rings in a similarity graph.
- **Approximation:** MST doubling gives a 2-approximation for metric TSP; MST of terminals bounds Steiner tree.
- **Parallel:** Borůvka's algorithm (every component grabs its lightest outgoing edge, `O(log V)` rounds) is the one used on GPUs and in distributed settings.

## Follow-up questions to expect

1. "How would you find the second-best MST?" For each tree edge, swap it for the lightest non-tree edge that reconnects the cut; take the best swap. `O(VE)` naively, `O(E log V)` with LCA tricks.
2. "Maximum spanning tree?" Negate the comparator. Same algorithms, same proof with inequalities flipped.
3. "Edges arrive online; maintain the MST." Add the edge, find the heaviest edge on the cycle it closes, drop that one. Link-cut trees make it polylogarithmic.
