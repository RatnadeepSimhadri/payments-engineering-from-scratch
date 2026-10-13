---
name: checklist-schema-change
description: Pull-request checklist for any change to an event or message schema shared between independently deployed producers and consumers
phase: 8
lesson: 11
tags: [streams, schema, avro, protobuf, compatibility, review]
---

# Schema Change Review Checklist

Attach to every pull request that edits an `.avsc`, `.proto`, JSON Schema, or event class that is serialized onto a topic, queue, or event store. The registry will run the structural part; the reviewer owns the rest.

## Before reading the diff

- [ ] The subject's compatibility mode is stated in the PR description, and it is a transitive mode if the topic retains data for longer than one deploy cycle or feeds an event store.
- [ ] The PR says which consumers read this subject (a list, not "downstream") and which team owns each.
- [ ] The PR states the rollout order the mode implies (consumers first for BACKWARD, producers first for FORWARD) and links the deploy plan that enforces it.

## Field-level checks

For each changed field, one row:

| Field | Change (add / remove / rename / retype / enum) | Default present and true for all old data? | Promotion allowed by the format? | Identifier reuse? | Verdict |
|-------|------------------------------------------------|---------------------------------------------|----------------------------------|-------------------|---------|
| | | | | | |

- [ ] Every added field has a default, and the default is the value that is *correct* for every event written before the field existed (not merely a value that type-checks: `0` for a fee that did not exist is correct; `""` for a date is not).
- [ ] Every removed field had a default in every retained version if the mode is FORWARD or FULL.
- [ ] No field is renamed. Clarifications go in `doc` or a comment. If a rename is unavoidable, Avro `aliases` are declared on the reader and the PR explains how every consumer gets the new reader schema before the producer deploys.
- [ ] Every type change is in the format's promotion list (Avro: `int` to `long` to `float` to `double`, `string` and `bytes`; Protobuf: within a wire-compatible group). Otherwise a new field is added and the old one deprecated.
- [ ] Protobuf: every deleted field number is listed in `reserved`. No number is reused.
- [ ] Enum additions: the reader enum has a default (Avro) or consumers are confirmed to tolerate unknown values (Protobuf).

## Money fields

- [ ] Amounts are integer minor units (`long` / `int64`) paired with an ISO 4217 code, or a `decimal` logical type with a fixed scale that is never changed in place.
- [ ] No amount is a `double`, `float`, or a decimal string that consumers might parse as a float.
- [ ] A change to a `decimal` scale or precision is treated as a new field, because the structural check cannot see it.

## Semantics the registry cannot check

- [ ] No field changed meaning while keeping its name and type (gross to net, inclusive to exclusive, millis to micros, local time to UTC). If one did, it is a new field or a new event version.
- [ ] Event-sourced subjects: a change that alters meaning ships with an upcaster from the previous version, the upcaster is pure and total, and no existing upcaster is deleted.
- [ ] The PR states whether a new consumer bootstrapping from offset zero will read every retained version correctly, and how that was tested.

## Pipeline

- [ ] The schema is registered by the CI pipeline against the real registry (or a contract-test suite against every published reader schema) and a compatibility failure blocks the merge.
- [ ] The producer's tests include at least one message encoded with the previous schema version and decoded with the new reader schema, and one the other way round if the mode is FULL.
- [ ] Monitoring: deserialization failures and dead-letter rates for every listed consumer are on a dashboard the deploying team will watch during rollout.

## Sign-off

| Role | Name | Confirms |
|------|------|----------|
| Producer owner | | Field table complete; rollout order in deploy plan |
| Each consumer owner | | Reader schema compatible; deploy scheduled in the required order |
| Reviewer | | Semantic checks done; money fields correct |
