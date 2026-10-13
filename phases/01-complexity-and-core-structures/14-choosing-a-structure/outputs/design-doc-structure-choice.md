---
name: design-doc-structure-choice
description: Half-page design-note template for recording why a component uses the data structure it does, following the access pattern, invariants, constants procedure
phase: 1
lesson: 14
tags: [design, data-structures, review, template]
---

# Structure Choice: <component name>

Fill in every section before writing the implementation. Review it with the code. Keep it next to the code. A section you cannot fill in is a question you have not answered yet.

## 1. Access pattern

List every operation. Include the rare ones (cancel, correct, un-link) and the scans (export, reconciliation, migration).

| Operation | Frequency (steady / burst) | Latency budget | Notes |
|-----------|---------------------------|----------------|-------|
| | | | |
| | | | |
| | | | |

Working set: <number of live entries> entries, <bytes per entry, including object overhead> bytes each, about <total> in memory. Growth: <per day / per year>.

## 2. Invariants

Properties that must hold after every operation. For each, say what enforces it.

| Invariant | Enforced by (structure / code / database constraint / test only) |
|-----------|------------------------------------------------------------------|
| | |
| | |

Explicitly not required: <durability, ordering, exact timing, ...>. Say why.

## 3. Constants

- Memory: <total from above>; fits in <cache / process heap / external store>.
- Hot-path cost: <comparisons, cache misses, allocations per operation>.
- Library implementation available: <yes: which / no: why not>.
- Operational: <how it is inspected in production, how it is rebuilt, how it is reloaded>.

## 4. Structure chosen

<Primary structure and the lesson or library that defines it.>
<Secondary structures and what each one is for (index, expiry, cancel).>
<How the primary and secondary are kept consistent.>

### Candidates rejected

| Candidate | Fails on |
|-----------|----------|
| | access pattern: <operation it cannot do in budget> |
| | invariant: <which one it cannot enforce> |
| | constant: <memory, latency, or operational cost> |

## 5. Impossible or expensive operations

<Each operation this structure makes O(n) or cannot express, and the current answer if someone asks for it (secondary index, rebuild from log, not supported).>

## 6. Invariant tests

<One line per invariant: the test that proves it, including the failure it would catch.>

- 
- 
- 

## 7. Revisit when

<The measurable conditions under which this choice should be re-examined: a new operation, a constant off by 10x, a latency profile, an invariant violation.>

---

Author: <name>  Date: <date>  Reviewed by: <name>
