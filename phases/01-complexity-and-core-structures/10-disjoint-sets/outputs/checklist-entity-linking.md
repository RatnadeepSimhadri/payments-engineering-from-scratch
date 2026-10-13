---
name: checklist-entity-linking
description: Design-review checklist for any system that merges identities into groups (fraud rings, customer 360, KYC de-duplication)
phase: 1
lesson: 10
tags: [union-find, entity-resolution, fraud, review]
---

# Entity Linking Review Checklist

Use when reviewing a design that answers "are these two identifiers the same actor" by merging groups. The underlying structure is almost always a disjoint-set forest or its distributed equivalent (connected components); the mistakes are almost always in what gets merged and what happens afterwards.

## Link rules

- [ ] Every identifier type that triggers a union is listed, with its evidential strength (card fingerprint: strong; device id: strong unless emulator; email: medium; IP address: weak; postal address: weak).
- [ ] Weak identifiers do not trigger unions on their own. They are features in the risk score, not edges in the graph.
- [ ] A cap exists on how many accounts one identifier may connect before it stops propagating (a device on 10,000 accounts is a kiosk or a farm; either way it must not fuse the whole graph into one component).
- [ ] The rule for what counts as "the same" identifier is normalised before linking: lower-cased emails, canonical phone formats, Unicode-normalised names (Phase 1, Lesson 12). Two spellings of one email must not be two elements.

## The structure

- [ ] `find` uses path compression and `union` uses rank or size; the amortised cost is documented as O(alpha(n)), effectively constant.
- [ ] `find` is iterative. A recursive find on a degenerate chain overflows the stack exactly when the data is most adversarial.
- [ ] Component size and component count are maintained incrementally, not recomputed by scanning.
- [ ] Any per-ring aggregate (total disputed amount, first-seen date, flagged status) is stored at the root and merged on union, with money as integer minor units (Phase 10, Lesson 01).
- [ ] Concurrency is addressed: single writer, or sharded by identifier hash, or an explicit lock. Path compression writes during reads, so a reader-writer lock does not help.

## Undo and audit

- [ ] The observation log (which identifier was seen with which, when, from which source) is kept as the source of truth. The forest is an index that can be rebuilt from it.
- [ ] There is a documented procedure for a false-positive link: remove the observation from the log and rebuild, or mark the component. The design does not claim to "unlink" in the forest itself.
- [ ] Rebuild time from the full log is measured and acceptable (it is O(m alpha(n)) and usually minutes for billions of edges on one machine).
- [ ] Every union is attributable: given a ring, a reviewer can see the chain of observations that connected any two members (BFS over the small component once the forest has identified it).

## Downstream use

- [ ] The component id is a feature in the risk model, not a verdict. Ring size, ring exposure, and ring age feed the score (Phase 13).
- [ ] Thresholds are stated: minimum ring size to flag, minimum exposure to escalate, and the false-positive rate measured against reviewed cases.
- [ ] Legitimate sharing patterns are enumerated and tested: family devices, corporate cards, shared household addresses, payment facilitators whose sub-merchants share a bank account.
- [ ] Component ids are not exposed to users or used as stable keys. They change on every rebuild and on every merge.

## Tests the design must pass

- [ ] Transitivity: A~B and B~C observed, A~C never observed, `sameRing(A, C)` is true.
- [ ] Idempotence: observing the same link twice does not change size or count.
- [ ] Cap: an identifier beyond the propagation cap does not merge the accounts it touches.
- [ ] Rebuild: deleting one observation and rebuilding yields the partition that would have existed without it.
- [ ] Randomised: thousands of random unions agree with a brute-force label array.
