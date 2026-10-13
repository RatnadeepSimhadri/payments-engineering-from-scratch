---
name: checklist-sorting-review
description: Review checklist for any code or query that sorts records people or downstream systems will act on
phase: 2
lesson: 1
tags: [sorting, stability, review, comparators]
---

# Sorting Review Checklist

Use on any pull request that sorts transactions, events, or rows that will be written to a file, paginated, or fed to another system. An unchecked box is a reason to request changes.

## The key

- [ ] The sort key is a total order over the data that will actually be sorted. Money keys compare one currency only, or the key is `(currency, amount)`.
- [ ] If two records can compare equal, the code either requires stability explicitly (see below) or appends a tie-break column (`created_at`, `seq`, `id`) so the key is unique.
- [ ] The comparator is consistent with `equals` where the collection cares (`TreeMap`, `TreeSet`, `sorted` + `distinct`).
- [ ] The comparator is transitive and never throws for a valid pair; a `Comparator` that reads a nullable field uses `Comparator.nullsFirst`/`nullsLast`.

## Stability

- [ ] It is written down whether downstream order inside equal keys matters (processor de-dup rules, pagination cursors, audit replays).
- [ ] If it matters, the call is a documented stable sort: `Arrays.sort(Object[])`, `List.sort`, `Collections.sort`, `Stream.sorted`, Python `sorted`, Go `sort.Stable` / `slices.SortStableFunc`, or a total key.
- [ ] There is no sort of a primitive array (`int[]`, `long[]`) paired with a parallel array of identities.
- [ ] A test asserts stability on an input with ties (`isStablePermutation` pattern), not only ascending order on distinct keys.

## Algorithm and memory

- [ ] For in-memory sorts of objects, the O(n/2) Timsort buffer is acceptable for the batch size; hot paths sorting tiny lists millions of times have been measured.
- [ ] Data that may exceed memory uses an external merge (chunk, sort, k-way merge), not an in-memory sort with a bigger heap setting.
- [ ] Quicksort implementations (hand-written or library) use a random or median-of-three pivot and are not fed sorted input through a fixed-pivot path.
- [ ] Duplicate-heavy keys (status, currency, fee tier) are sorted by an algorithm with three-way partitioning or a stable merge, never a two-way quicksort.

## SQL

- [ ] Every `ORDER BY` used for pagination, files, or settlement includes a unique tail column: `ORDER BY amount, created_at, id`.
- [ ] Keyset pagination uses the same columns in the `WHERE (...) > (...)` tuple as in the `ORDER BY`.
- [ ] `ORDER BY` on a non-unique column with `LIMIT` is treated as nondeterministic and flagged.
