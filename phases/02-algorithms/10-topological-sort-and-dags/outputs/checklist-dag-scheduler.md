---
name: checklist-dag-scheduler
description: Review checklist for any batch, pipeline, or orchestration change whose jobs depend on one another
phase: 2
lesson: 10
tags: [dag, scheduling, batch, settlement, review]
---

# DAG Scheduler Review Checklist

Use on any change to a settlement batch, data pipeline, build graph, or workflow definition. Each unchecked box is a question for the author before merge.

## Dependencies are data

- [ ] Every "runs after" relationship is an explicit edge in a graph the scheduler reads, not a clock time, a sleep, or a comment.
- [ ] A job starts only when the scheduler has observed every upstream job's completion, not when a wall-clock time passes.
- [ ] Each job publishes an explicit completion marker (row count, checksum, or watermark) and downstream jobs check it; "the table exists" is not completion.
- [ ] Jobs with no declared dependencies are intentionally independent; a reviewer has asked "does this really not need X?" for each one.

## Ordering is deterministic

- [ ] Node and edge iteration uses an insertion-ordered or sorted structure (`LinkedHashMap`, a sorted list), never a hash-ordered map; the same graph always yields the same order.
- [ ] The tie-break rule (FIFO, priority, lexicographic) is named in the code and in the design doc.
- [ ] A test asserts the exact order for a representative graph, and a second test asserts that order satisfies the topological invariant (every edge points forward).

## Cycles fail early and loudly

- [ ] The graph is checked for cycles when the definition is loaded or deployed, not when the batch first runs at midnight.
- [ ] The error names the cycle in edge order (`a -> b -> c -> a`), not "cycle detected" and not the full list of downstream jobs.
- [ ] Adding a dependency requires the same review as adding code; the reviewer has looked at the resulting graph, not just the diff.

## Time budget

- [ ] Durations are recorded per job per run and the critical path is computed from recent history, not from the original estimates.
- [ ] The makespan on the critical path, with a margin for the worst recent day (quarter-end, Black Friday), fits inside the external cutoff (bank, scheme, regulator).
- [ ] Performance work targets zero-slack jobs; a ticket to speed up a job with slack cites why it is worth doing.
- [ ] Parallelism is bounded by the worker pool and the downstream systems' rate limits, not by the number of jobs in a level.

## Failure and retry

- [ ] A failed job blocks exactly its downstream set and nothing else; independent branches continue.
- [ ] Retrying a job re-runs its downstream set, and the downstream jobs are idempotent (Phase 12, Lesson 01) so a re-run cannot double-post.
- [ ] A job cannot be skipped manually without recording who skipped it and why; the audit trail is part of the batch output.
- [ ] The last job before an irreversible external action (submit to bank, send files to a scheme) is a reconciliation check, and it is on the critical path by design.
