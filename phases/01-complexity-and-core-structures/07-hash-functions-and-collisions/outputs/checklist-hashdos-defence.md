---
name: checklist-hashdos-defence
description: Review checklist for any hash map or set whose keys can be influenced by an untrusted party
phase: 1
lesson: 7
tags: [hashing, security, dos, review, idempotency]
---

# HashDoS Defence Checklist

Use on any code path where a hash map, hash set, or dictionary is keyed by data an untrusted party can influence: request headers, form or JSON fields, URL parameters, idempotency keys, merchant reference IDs, webhook payloads, cache keys derived from user input.

## Identify the exposure

- [ ] List every map/set on the path and where each key comes from. Flag any key an external party can choose or shape.
- [ ] For each flagged map, is it on a shared hot path (one tenant's keys can stall others) or holding a lock while it inserts? Those are the high-severity ones.
- [ ] Is the hash function unseeded and public for the key type (e.g. Java `String.hashCode`, a custom `hashCode` with no per-process key)?

## Pick the right defence for the concern

- [ ] **CPU / availability** (attacker wants to stall you): use a per-process keyed hash (SipHash or equivalent) for the map, or a structure with treeified bins, plus input limits below.
- [ ] **Confidentiality** (the key is itself a secret, e.g. a PAN or token): use a keyed cryptographic hash (HMAC-SHA-256) with the key in a KMS/vault; never a bare digest of a small-domain value. This is a different key from the CPU defence.
- [ ] **Cross-process determinism** (sharding, persistence): use a fixed well-specified function; a per-process random seed is wrong here. If the input is sensitive, key it with a shared secret.

## Language and library specifics

- [ ] Java: know that `HashMap` treeifies bins at 8 entries only when keys are mutually `Comparable` and the table has >= 64 buckets. A `HashSet` of a non-`Comparable` custom type is still O(n) per bucket. Hand-rolled maps have no treeification.
- [ ] Python/Ruby/Rust: hash randomization is on by default (SipHash). Do not disable it (`PYTHONHASHSEED=0`) on a service that takes untrusted keys.
- [ ] Any language: a custom `hashCode`/`__hash__` on a key type resets you to the unseeded case unless you seed it yourself.

## Defences that help regardless of the hash

- [ ] Cap the number of distinct keys per request and per tenant (the `max_input_vars` lesson from 2011).
- [ ] Bound request body size and field count before parsing into a map.
- [ ] Put a timeout or work budget on request handling so a single request cannot pin a core indefinitely.
- [ ] Per-tenant rate limits and isolation so one tenant's keys cannot monopolize a shared store.

## Tests and evidence

- [ ] A test inserts a known colliding key set (or a large set under a fixed seed) and asserts the operation stays within a time or comparison budget.
- [ ] A benchmark records the worst-case, not just the mean, insert time for the map under a flooded-bucket workload.
- [ ] If relying on a keyed hash, a test confirms the key is per-process random (two processes produce different bucket assignments for the same keys) and is not logged or persisted.
