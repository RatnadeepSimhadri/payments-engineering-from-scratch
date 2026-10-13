---
name: checklist-schema-evolution
description: Review checklist for any change to a message schema, event payload, or API response that other services consume
phase: 3
lesson: 15
tags: [serialization, protobuf, json, schema-evolution, review]
---

# Schema Evolution Review Checklist

Apply to every pull request that touches a `.proto`, an Avro schema, an OpenAPI response, or any hand-rolled wire format. Assume old readers and new writers will coexist for days, and that messages already in queues and logs will be read for years.

## The five rules

| Rule | Check | Test that proves it |
|------|-------|---------------------|
| 1. Field numbers never change | [ ] No existing field was renumbered, even when renamed | Decode a message captured from the previous release with the new reader; every field lands where it did |
| 2. Wire types never change | [ ] No field changed between varint, fixed, and length-delimited families (int to string, int32 to fixed32, message to bytes) | Old reader on new bytes: no type errors |
| 3. Deleted numbers are reserved | [ ] Every removed field's number appears in `reserved` (or a comment in a hand-rolled codec) and is not reused | A grep for the number finds the reservation |
| 4. New fields are optional with safe defaults | [ ] A new reader treats absence of each new field as a valid, defined state | New reader on old bytes: no errors, documented defaults |
| 5. Meaning does not depend on skipped fields | [ ] No new field changes how an old reader should interpret an existing field | Old reader on new bytes produces a *correct* (not just well-formed) result |

## Deployment order

- [ ] Old reader, new writer: tested (forward compatibility).
- [ ] New reader, old writer: tested (backward compatibility).
- [ ] Message written by the previous release, read a week later by two releases ahead: considered.
- [ ] A reader that reads, modifies, and forwards messages preserves unknown fields (or the design accepts that it strips them).
- [ ] Schema registry or `buf breaking` check runs in CI and blocks the merge on a violation.

## Numbers and money

- [ ] Amounts are integers in minor units with a currency field; never float, double, or a decimal string parsed with `parseFloat`.
- [ ] Fields that can be negative (amounts, adjustments, deltas) use `sint64`; counts and timestamps use `uint64`/`int64`.
- [ ] In JSON, every 64-bit integer that can exceed 2^53 (ids, cumulative totals) is serialized as a string, and the schema says so.
- [ ] A missing numeric field is distinguishable from zero where the distinction matters (wrapper message, explicit presence, or a separate flag).

## Decoder safety

- [ ] Truncated input produces an error, not a partially filled message.
- [ ] A wrong wire type for a known field produces an error, not a reinterpreted value.
- [ ] Unknown wire types (3, 4, 6, 7) are rejected.
- [ ] A maximum message size and a maximum nesting depth are enforced before allocation.
- [ ] Required semantic fields (currency on an amount, account on a posting) are validated after decoding, since the wire format has no concept of required.

## Canonical form and hashing

- [ ] Nothing hashes or signs serialized bytes unless a canonical encoding (field order, default omission, no unknown fields) is defined and tested.
- [ ] Idempotency keys and dedup keys come from explicit fields, not from a hash of the payload bytes.

## Documentation

- [ ] The schema file is the contract and is versioned alongside the code that produces it.
- [ ] Each field has a comment with its unit (minor units, milliseconds since epoch, basis points) and its default's meaning.
- [ ] The change is described in terms of the rules above in the pull-request body, including which test demonstrates each direction of compatibility.
