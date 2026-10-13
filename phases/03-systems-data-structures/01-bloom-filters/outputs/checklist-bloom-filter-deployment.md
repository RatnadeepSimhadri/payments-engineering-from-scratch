---
name: checklist-bloom-filter-deployment
description: Review checklist for any change that puts a Bloom filter or other probabilistic membership structure in front of an authoritative store
phase: 3
lesson: 1
tags: [bloom-filter, caching, idempotency, review, probabilistic]
---

# Bloom Filter Deployment Checklist

Use on any pull request that adds a membership filter in front of a database, cache, or remote service. One unchecked box is a reason to request changes.

## The guarantee is used the right way round

- [ ] The code acts on `mightContain == false` only (skip the lookup). A `true` always falls through to the authoritative store; no path returns a cached or default result on a positive.
- [ ] The authoritative store still enforces the invariant on writes (unique constraint, conditional insert, compare-and-set). The filter reduces reads; it protects nothing.
- [ ] The hot-path answer is usually "absent". If most lookups are expected to hit, the filter saves nothing and the PR should say why it is still wanted.

## Sizing

- [ ] `m` and `k` come from `m = -n ln p / (ln 2)^2` and `k = (m/n) ln 2`, and the PR states the `n` and `p` used and where `n` comes from (peak keys between rebuilds, not average).
- [ ] The chosen `p` has a cost attached: expected extra lookups per second at peak traffic is written down.
- [ ] Memory per process at the chosen `m` is written down and compared with the hash-set alternative.
- [ ] There is a plan for when `n` is exceeded: rotate by time window, scalable sub-filters, or a rebuild trigger on fill ratio.

## Hashing and cross-service agreement

- [ ] The hash scheme (functions, seeds, double-hashing formula, byte encoding of keys) is named and versioned, and a version identifier travels with any serialised filter.
- [ ] Signed/unsigned and wraparound behaviour is handled explicitly: `remainderUnsigned` or masking where the language has no unsigned 64-bit type.
- [ ] If more than one language or service touches the same filter, a test asserts identical bit positions for a fixed set of keys across all of them.
- [ ] Key encoding is fixed (UTF-8, canonical form). `"PAY_1"` and `"pay_1"` are either both normalised or both distinct, on purpose.

## Lifecycle

- [ ] A freshly started process has an empty filter. The PR describes how it is populated (rebuild from the store, load a snapshot) and confirms the authoritative store carries the load until then.
- [ ] Deletes are not required, or the structure is a counting/cuckoo filter, or deletes are handled by rotation. A plain Bloom filter with a `remove` method is a bug.
- [ ] Concurrent `add` and `mightContain` use atomic word operations or a lock. A torn read can only yield a false negative, which is the one error the design forbids.

## Observability

- [ ] Fill ratio (`bitCount / m`) is exported as a gauge with an alarm well below saturation (0.7 is a reasonable default for `k` around 7).
- [ ] The measured false-positive rate (positives that found no row / total positives) is exported and compared with the configured `p` on a dashboard.
- [ ] A metric distinguishes "skipped lookup", "lookup after positive, row found", and "lookup after positive, no row" so the filter's value is visible.

## Tests present

- [ ] No-false-negative test: every inserted key is reported present.
- [ ] Measured false-positive rate within a tolerance of the formula at the sized `n`.
- [ ] Known-vector test for the hash functions.
- [ ] Overfill test showing the rate climbs when `n` is exceeded, so the alarm threshold is justified.
- [ ] Parameter-mismatch test for `union` or any merge path.
