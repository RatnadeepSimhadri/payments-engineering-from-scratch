---
name: checklist-merkle-reconciliation
description: Design-review checklist for any system that compares replicas or proves inclusion with hash trees
phase: 3
lesson: 9
tags: [merkle, reconciliation, replication, hashing, audit]
---

# Hash-Tree Reconciliation Review Checklist

Use when a design says "we will compare hashes" to reconcile two datasets, repair replicas, or let a third party verify records. One unchecked box is a question for the design review.

## Canonical serialization

- [ ] Every leaf is hashed from a byte encoding that is fully determined by the record's fields: no `toString()`, no locale formatting, no map iteration order.
- [ ] Amounts are encoded as integer minor units plus a currency code, never as a formatted decimal string.
- [ ] Field boundaries cannot be forged by field contents (length prefixes or an escaped separator), so `a|b` + `c` and `a` + `b|c` hash differently.
- [ ] A cross-language test pins the root of a fixed sample dataset as a constant, so a Java and a Go implementation are proven to agree.
- [ ] Schema changes to the record (new field, renamed field) are versioned in the encoding so old and new roots are never compared as if equal.

## Tree construction

- [ ] Leaf hashes and internal hashes use distinct domain prefixes (RFC 6962 `0x00` / `0x01`) or an equivalent separation.
- [ ] Odd nodes are carried up (or the RFC 6962 power-of-two split is used); the odd node is never hashed with a copy of itself.
- [ ] The hash function is a current cryptographic hash (SHA-256 or stronger); CRC, MurmurHash, and MD5 are not acceptable for this purpose.
- [ ] The empty dataset has a defined, documented root (or is rejected), and both sides agree on it.
- [ ] Both sides agree on the key range, ordering, and leaf count before comparing roots; a count mismatch is reported as such, not as "content differs".

## Diff and repair

- [ ] The diff descends only into subtrees whose hashes differ, and the design states the expected number of round trips for k divergent leaves (about 2k log n hashes, or log n round trips if a level is fetched per round).
- [ ] Hashes are fetched in batches per level or per subtree, not one request per node, when the trees live on different machines.
- [ ] The design says which side is authoritative when a divergence is found, or who decides; the tree only locates the disagreement.
- [ ] Tree depth is bounded for very large datasets (a leaf may cover a range of rows) and the repair granularity that implies is acceptable.
- [ ] Divergences are logged with the leaf identity and both hashes before any repair writes happen.

## Proofs and trust

- [ ] The root is delivered over a trusted channel or signed; the design does not treat an unsigned root as proof of anything.
- [ ] The verifier checks the proof's side tags against the claimed index and tree size, so a valid proof cannot be presented for a different position.
- [ ] The final root comparison is constant-time when the verifier is network-exposed.
- [ ] Nobody relies on the tree to prove absence unless it is a sorted or sparse Merkle tree designed for non-membership proofs.
- [ ] For append-only logs, consistency proofs between successive roots are produced and checked, so an operator cannot rewrite history between snapshots.

## Operations

- [ ] Rebuilding the tree after one record changes rehashes only the log n ancestors, and the code path for that is tested.
- [ ] Inserting in the middle of the leaf list is either impossible by design (append-only, keyed) or understood to invalidate every existing proof.
- [ ] The reconciliation job's cost is measured in hashes exchanged and rows read, and both are alerted on, so a regression to full-table compare is visible.
