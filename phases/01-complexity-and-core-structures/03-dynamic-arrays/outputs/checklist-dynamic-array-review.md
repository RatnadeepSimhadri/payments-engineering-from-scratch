---
name: checklist-dynamic-array-review
description: Review checklist for code that accumulates records into an in-memory growable list (batch parsers, result buffers, worker queues)
phase: 1
lesson: 3
tags: [data-structures, arrays, memory, review, batch]
---

# Growable List Review Checklist

Use when a pull request builds a list of unknown or large size in memory: file parsers, query result buffers, batch accumulators, in-process queues.

## Sizing

- [ ] If the element count is known or bounded up front (file header, page size, batch limit), the list is constructed with that capacity. A default-capacity list that grows to millions of elements is a finding.
- [ ] If the count is unbounded, there is an explicit cap or a streaming/chunking path. A dynamic array grows until the heap is exhausted; that is not a bound.
- [ ] Peak memory has been estimated with slack included: up to 2x the element count for a 2x policy, 1.5x for `ArrayList`.

## Growth

- [ ] The list is a library type or, if hand-rolled, grows by a geometric factor. Constant-increment growth is O(n) per append and is a blocking finding.
- [ ] Latency-sensitive paths (request handlers, authorization decisions) do not append to a list that may reallocate mid-request. Either pre-size, or accumulate off the hot path.
- [ ] `System.arraycopy` / `Arrays.copyOf` appearing high in a profile has been traced to a specific growth loop, not dismissed as "the JVM".

## Shrinking

- [ ] If the structure shrinks, the shrink trigger has hysteresis (shrink below 1/4 full, to 1/2), never "shrink when below half full".
- [ ] If the structure never shrinks and lives for the process lifetime, the long-term memory footprint after a burst is understood and acceptable.

## Bounds and lifetime

- [ ] Index checks compare against size, not capacity. Reading a slot past size is a use-after-free in disguise.
- [ ] Removal nulls the vacated slot (or the library does) so dead objects are not pinned by a long-lived list.
- [ ] Nobody holds an index across an operation that can reorder or shift elements (`insert`, `remove` in the middle).

## Hidden O(n)

- [ ] No `insert(0, x)` or `remove(0)` in a loop. That is a queue; use a ring buffer or `ArrayDeque`.
- [ ] No `contains` or `indexOf` in a loop over a list of more than a few hundred elements. That is a set lookup; use a hash set.
- [ ] Iteration over boxed primitives (`List<Long>` of amounts) on a hot path has been measured against a primitive array if throughput matters.

## Tests present

- [ ] A test that appends past the initial capacity and verifies every element survived the copy.
- [ ] A test that `get(size)` throws even when capacity exceeds size.
- [ ] For a shrinking structure, a test that alternating add/remove at the boundary performs zero reallocations.
