---
name: design-doc-conflict-policy
description: One-page template for the conflict-resolution section of a design review for any replicated, multi-writer entity
phase: 6
lesson: 9
tags: [replication, conflict-resolution, design-review, lww, version-vectors]
---

# Conflict Resolution Policy: <entity name>

Fill in one copy per replicated entity that can be written in more than one place (region, device, service). If an entity has a single writer by construction, say so in section 1 and stop.

## 1. Can writes to this entity be concurrent?

- Writers: <list of regions / services / clients that may write>
- Is there a single writer per key by design? <yes / no>. If yes, what enforces it (leader lease, partition ownership, routing)?
- Realistic concurrent scenario (two sentences, with real field names):
  <e.g. "agent in east sets status=FUNDS_HELD; agent in west sets status=UNDER_REVIEW within the replication lag">

## 2. If we chose last-writer-wins, what would be lost?

| Field | Can two writers legitimately change it concurrently? | Cost of losing one write | Reversible? |
|-------|------------------------------------------------------|--------------------------|-------------|
| | | | |

- Clock source for the timestamp: <wall clock / HLC / server-assigned>. Known skew bound: <ms>.
- LWW is acceptable for this entity: <yes / no>. If yes, state the field-level argument above that justifies it.

## 3. Detection: how will we know a conflict happened?

- Versioning scheme: <none / version vector / dotted version vector / revision tree / store-managed>
- Where the context travels: <client carries it / server session / not needed because single writer>
- What a reader sees when siblings exist: <error / list of versions / auto-resolved value>
- Alarm threshold on siblings per key: <n>. Owner of the alarm: <team>.

## 4. Resolution: the merge rule

Plain language (one paragraph a product manager can read):
<e.g. "Keep every note from every agent. For status, the more restrictive status wins, because releasing funds wrongly cannot be undone.">

Function signature and per-field rule:

| Field | Rule | Type from Lesson 08 if applicable |
|-------|------|-----------------------------------|
| notes | union | G-Set / OR-Set |
| status | max by severity order | application merge |
| amount | ??? | never LWW; see Lesson 08 PN-Counter or route to single writer |

- Which error does the rule prefer when it must choose? <keep too much / keep too little>. Why is that the reversible one?
- Is the rule commutative, associative, and idempotent? <yes / no>. If no, concurrent resolutions on two replicas can diverge; explain how that is prevented.
- Who owns changes to this rule? <team>. Is it reviewed as a business rule, not an implementation detail? <yes / no>

## 5. Invariants the merge cannot protect

List any predicate over the entity that spans replicas (non-negative balance, uniqueness, at-most-one). For each, name the write-time mechanism (leader, quorum, consensus) that enforces it, or state explicitly that the invariant may be violated during a partition and how that is reconciled afterward.

## 6. Tests that must exist

- [ ] Two concurrent writes produce two siblings (or the documented auto-resolution), never a silent overwrite.
- [ ] A causally later write (context covers the earlier one) replaces its ancestor and leaves one version.
- [ ] A blind write (no context) becomes a sibling rather than clobbering.
- [ ] The merge function applied to the scenario in section 1 yields the value section 4 promises.
- [ ] Merging twice, or in the other order, yields the same result.
- [ ] If LWW is chosen anywhere: a test documents the exact write that is discarded, so the loss is a decision on record.
