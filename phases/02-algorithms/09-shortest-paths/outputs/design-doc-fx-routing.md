---
name: design-doc-fx-routing
description: Design-document template for any service that routes value across weighted edges (FX conversion chains, fee-minimising rails, correspondent hops), covering the weight model, algorithm choice, numeric representation, and cycle policy
phase: 2
lesson: 9
tags: [fx, routing, dijkstra, bellman-ford, arbitrage, design-doc]
---

# Design Document: Routing Over Weighted Edges

Fill every section before writing the router. Each blank is a decision that has caused a production incident when left implicit. References are to Phase 2, Lesson 09.

## 1. The graph

| Question | Answer |
|----------|--------|
| Nodes are | (currencies / accounts / rails / ledgers) |
| An edge is | (a quote from provider P for pair X/Y at size S, a rail with fee F, ...) |
| Edge count and node count at peak | |
| Edges are refreshed | (per tick / per minute / per batch) and expire after |
| Parallel edges (two providers, same pair) are | (kept separately / collapsed to the best / collapsed to the first) |

## 2. The weight model

- [ ] The cost of a route is the **sum** / **product** of per-edge quantities (circle one). If product, weights are `-ln(rate)` and the sign can be negative; Dijkstra is ruled out.
- [ ] Weights can be negative: yes / no. State why.
- [ ] A negative cycle in this model means: (arbitrage / a stale quote / a fee rebate loop / impossible by construction).
- [ ] Ties between routes of equal weight are broken by: (insertion order / fewer hops / provider priority). The rule is deterministic and identical in every implementation.
- [ ] Nothing is "fixed" by adding a constant to every weight. If someone proposes it, point them at Lesson 09, The Concept.

## 3. Algorithm

| Need | Choice | Complexity at peak size |
|------|--------|-------------------------|
| Best route from one source, non-negative weights | Dijkstra on an indexed heap | O((V + E) log V) = |
| Best route with possibly negative weights | Bellman-Ford | O(V * E) = |
| Negative-cycle check over the whole graph | Bellman-Ford from a virtual source | O(V * E) = |
| Best route with at most k legs | Bellman-Ford limited to k rounds (Lesson 09, Exercise 2) | O(k * E) = |
| All pairs | Floyd-Warshall or repeated Dijkstra | O(V^3) or O(V (V + E) log V) = |

- [ ] The algorithm chosen matches the sign of the weights. Dijkstra validates non-negativity at entry and fails loudly.
- [ ] Early exit conditions are stated (target settled; a round changed nothing).

## 4. Numeric representation

| Quantity | Type | Scale | Rounding rule | Where it is applied |
|----------|------|-------|---------------|---------------------|
| Rate | integer | 10^8 | none: quotes are parsed exactly or rejected | ingestion |
| Weight | integer | 10^6 micro-nats | round to nearest, once, per edge | `weightOf` |
| Route total | integer | same as weight | none: integer sum | algorithm |
| Amount | `Money` minor units | currency exponent | HALF_EVEN per hop (or as policy states) | `convert` |
| Cycle product for confirmation | big integer rational | RATE_SCALE^k | none: exact comparison | `cycleProduct` |

- [ ] The only floating-point operation is listed above with its rounding rule and tie behaviour per language.
- [ ] Per-edge rounding error bound is stated (0.5 units per edge) and the consequence for k-edge cycles is stated (total may be off by k/2).
- [ ] Every negative cycle is confirmed by an exact product before it is acted on.
- [ ] Amounts never pass through a float. `Money.convert` applies both currencies' minor-unit scales and one named rounding.
- [ ] Overflow is an error (`multiplyExact`, `big.Int.IsInt64`, or unbounded ints), never a wrap.

## 5. Cycle policy

When `arbitrage()` returns a cycle:

- [ ] The cycle is logged with the exact gain in basis points, the quotes involved, and their timestamps.
- [ ] The default action is to **quarantine the quotes on the cycle** as stale and re-request them, not to trade the loop.
- [ ] Routing continues on the remaining quotes; `bestConversionPath` refuses to run on a graph with a known negative cycle, so the quarantine must happen first.
- [ ] A cycle whose exact gain is below the configured threshold (bps) is logged but not quarantined. The threshold is written here: ____ bps.
- [ ] One cycle is reported per run. If a second run on the quarantined graph finds another, repeat. The number of iterations is bounded and logged.

## 6. Size, time, and liquidity (what the graph does not model)

- [ ] Quotes carry a size. The route for amount A is computed on edges valid for A, or A is split into tranches.
- [ ] Quotes carry a timestamp. A route whose edges span more than ____ ms is rejected or re-priced.
- [ ] The number of legs is capped at ____ because each leg settles separately.

## 7. Tests

- [ ] Dijkstra and Bellman-Ford agree on a seeded random non-negative graph; every reconstructed path sums to its distance.
- [ ] Dijkstra rejects a negative edge; Bellman-Ford on the same graph gives the right (lower) distance.
- [ ] A hand-built negative cycle is detected and returned as a closed walk in travel order.
- [ ] A negative cycle not reachable from the source is found when the virtual source is used.
- [ ] An exact-product-1 cycle with rounded weight sum -1 is rejected by the exact check.
- [ ] The weights printed by every implementation for the same rates are identical, byte for byte.
- [ ] An amount converted along a multi-hop path matches the hand computation with the stated per-hop rounding.

## Sign-off

| Role | Name | Date |
|------|------|------|
| Author | | |
| Reviewer (algorithms) | | |
| Reviewer (treasury / risk) | | |
