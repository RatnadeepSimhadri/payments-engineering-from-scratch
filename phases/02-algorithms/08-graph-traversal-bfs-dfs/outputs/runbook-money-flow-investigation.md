---
name: runbook-money-flow-investigation
description: Step-by-step runbook for investigating a flagged account with graph traversal, so the result is reproducible and the right algorithm answers each question
phase: 2
lesson: 8
tags: [graphs, bfs, dfs, fraud, aml, runbook]
---

# Money-Flow Investigation Runbook

For an on-call risk or payments engineer handed a flagged account and a transfer log. Each step names the traversal that answers the question and the artefact to record. Function names refer to Phase 2, Lesson 08.

## 0. Freeze the inputs

- [ ] Record the exact transfer-log snapshot (table, partition, as-of timestamp, row count). The graph must be rebuildable from it.
- [ ] Record the time window. Rings are visible in days-to-weeks windows; a single-day window hides them, a one-year window merges everything.
- [ ] Record the currency filter. Build one graph per currency, or carry `Money` on edges and never sum across currencies (`volume()` throws on a mismatch for this reason).

## 1. Build the graph

- [ ] `fromTransfers(transfers)`: one directed edge per ordered (from, to) pair; parallel transfers collapse into one edge.
- [ ] `volume(transfers)`: total `Money` per edge, kept separately so amounts stay exact.
- [ ] Note node and edge counts. If E is in the billions, this runbook's single-machine traversals do not apply; use the distributed BFS job and stop here.
- [ ] Insertion order of the transfer log is the traversal order. Sort the log by (timestamp, transfer id) before loading so two investigators get identical output.

## 2. Who received money, and how far away are they?

- [ ] `bfs(g, flaggedAccount)`: hop distance and parent for every reachable account.
- [ ] Cap at 3 hops for the report. Beyond 3, the frontier typically contains a large fraction of all active accounts and the signal is gone.
- [ ] For each account of interest, `shortestHopPath(g, flagged, account)` gives the chain to cite. It is a fewest-hops chain, not a largest-amount chain; say so in the report.
- [ ] Record: distance histogram (how many accounts at 1, 2, 3 hops), and the paths for the top accounts by inbound `volume`.

## 3. Is there a closed group?

- [ ] `connectedComponents(g)` on the undirected view; `rings(g, minSize)` to filter by size.
- [ ] Treat a component as a *partition*, not a *finding*. A payroll fan-out and a laundering ring are both components. Do not escalate on component membership alone.
- [ ] Record: component id of the flagged account, its size, and the top edges by volume inside it.

## 4. Did money come back?

- [ ] `findCycle(g)` on the directed graph. A result is a closed walk `[a, b, c, a]`: transfers that return value to its origin.
- [ ] One cycle is returned, the first back edge in DFS order. If the graph has several, remove the reported cycle's weakest edge and run again, or implement the all-back-edges variant (Lesson 08, Exercise 2).
- [ ] Check the cycle's edges against `volume`: a 500.00 -> 480.00 -> 470.00 loop with 2-4% shrinkage per hop is the classic layering shape; a 500.00 -> 0.01 loop is probably a refund or a test transfer.
- [ ] For a stronger definition of "ring", compute strongly connected components (Exercise 3): every account in a non-trivial SCC is on some directed cycle.

## 5. Decide and document

| Finding | Evidence to attach | Typical action |
|---------|--------------------|----------------|
| Flagged account within 1 hop of a known-bad account | `shortestHopPath`, `volume` on the edge | Enhanced review |
| Flagged account in a directed cycle with shrinking amounts | `findCycle` output, per-edge `volume`, timestamps | Freeze the cycle's accounts, file a report per policy |
| Flagged account in a large component with no cycle | Component size, top edges | Note and monitor; not an escalation on its own |
| Flagged account isolated | `bfs` reaches nothing | Close the graph line of inquiry |

- [ ] Attach the input snapshot identifiers from step 0. Every list in the report (visit order, paths, components, cycle) must be regenerable from them.
- [ ] State which traversal produced each number. "4 hops" from a DFS depth is not a hop distance; reviewers will ask.

## Pitfalls seen in production

- Recursive DFS on a long forwarding chain overflowed the stack (Java default ~10k frames, Python 1,000). Use the iterative versions.
- Two-colour cycle detection flagged every account that paid two bills from one salary. Use three colours.
- Hash-map adjacency made the "hops" report differ between runs. Preserve insertion order.
- Summing edge amounts across currencies produced a meaningless total. Carry `Money` and let the mismatch throw.
