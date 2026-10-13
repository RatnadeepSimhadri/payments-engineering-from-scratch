---
name: checklist-sketch-rollout
description: Design-review checklist for replacing an exact counter or set with a Count-Min Sketch or HyperLogLog
phase: 3
lesson: 2
tags: [sketch, count-min, hyperloglog, risk, review]
---

# Sketch Rollout Checklist

Use when a design proposes an approximate structure in place of an exact count or set. Every unchecked box is a question for the author, not a veto.

## The question being approximated

- [ ] The exact query is written down in one sentence ("how many distinct cards has merchant M seen in the current hour") and it is one a sketch can answer: frequency of a given key (Count-Min) or number of distinct keys (HyperLogLog).
- [ ] Nobody downstream needs the keys themselves. A sketch cannot list which cards exceeded the threshold; if a report needs that, the design also keeps a candidate list or an exact store.
- [ ] The direction of error is acceptable for the decision. Count-Min only overestimates (false alarms, never misses). HyperLogLog is unbiased (about as often high as low). State which one the rule tolerates.

## Error budget

- [ ] Count-Min: `epsilon` and `delta` are chosen and recorded, and the resulting `epsilon * N` absolute bound has been computed for the busiest expected stream. The reviewer has seen that number next to the threshold the rule uses.
- [ ] HyperLogLog: `p` is chosen and the standard error `1.04 / sqrt(2^p)` is recorded. Someone has checked that an error of three standard deviations at the threshold does not change the business outcome.
- [ ] Memory per sketch times number of sketches (per merchant, per window, per shard) fits the heap or the Redis instance with headroom, and does not grow with traffic.

## Hashing

- [ ] All producers of a mergeable sketch hash identical bytes (UTF-8 of the same canonical key string) with the identical function and finalizer. The hash function is named in the design and pinned in a shared test vector.
- [ ] The hash is 64-bit and well mixed. FNV-1a or `hashCode` alone is not acceptable: weak low bits correlate Count-Min rows and shorten HLL zero runs.
- [ ] Index computation treats the hash as unsigned (`Long.remainderUnsigned`, `uint64`, masking in Python).

## Windows, merges, and resets

- [ ] Time windows are implemented as one sketch per window (or a ring of sub-window sketches that are merged), never by decrementing a Count-Min.
- [ ] Merge semantics are stated: Count-Min merge is counter-wise sum (concatenated streams); HLL merge is register-wise max (set union). Merges across shapes or precisions are rejected at runtime.
- [ ] Serialization is integer-exact (raw bytes with a version prefix). No JSON numbers, no floats, no language-specific object serialization.
- [ ] Sketch state is treated as cache, not record. Losing it degrades a rule for one window; nothing reconciles against it.

## Operations

- [ ] A dashboard shows estimate versus an exact sample (for example, exact counts for 1% of merchants) so drift from a hash bug is visible.
- [ ] The runbook states what the rule does when the sketch service is unavailable, and the default is not "approve everything".
- [ ] Counters are 64-bit. A 32-bit counter wrapping negative silently breaks the never-underestimate guarantee.

## Questions this structure cannot answer

Write these in the design so an on-call engineer is not surprised:

- Which keys are heavy (needs a candidate list or Space-Saving).
- Set intersection or difference between two HLLs (use Theta sketches from Apache DataSketches if required).
- Exact counts for audit or settlement. Money is never counted with a sketch.
