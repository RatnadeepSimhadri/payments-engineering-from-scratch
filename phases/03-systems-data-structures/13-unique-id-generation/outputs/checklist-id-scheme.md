---
name: checklist-id-scheme
description: Decision checklist for choosing and reviewing the unique-id scheme of a new table, message type, or API object
phase: 3
lesson: 13
tags: [ids, uuid, ulid, snowflake, indexing, review]
---

# ID Scheme Checklist

Answer the questions, then pick from the table. Review the code against the traps at the end.

## Questions

1. **Who generates it?** The client (then it is an idempotency key and must be client-chosen, usually UUIDv4) or the server.
2. **How many generators run concurrently, and can they coordinate once at startup?** No coordination possible: UUIDv7, ULID, or UUIDv4. One-time worker-id assignment acceptable: Snowflake.
3. **Peak ids per millisecond, fleet-wide?** Fill in the birthday bound: `p ~ n^2 / 2^(b+1)` with `n` = ids per millisecond and `b` = random bits per id. Target `p < 10^-12` per millisecond.
4. **Will the id be a B-tree primary key or a frequently indexed column?** Yes: it must be time-ordered (UUIDv7, ULID, Snowflake), or you store a random id with a separate indexed timestamp and accept the second index.
5. **Will outsiders see it?** Yes: a time-ordered id leaks creation time, and Snowflake leaks worker and rate. Use a prefixed random id for the external reference and keep the ordered id internal.
6. **What width can every downstream system store?** 8 bytes: Snowflake. 16 bytes: UUID or ULID in binary. 26 chars: ULID text. 36 chars: UUID text. Anything smaller: redo question 3 with the bits you actually have.
7. **Must it be strictly increasing within a millisecond?** Then the generator needs a counter (UUIDv7 method 1, ULID monotonic mode, Snowflake sequence) and a documented overflow rule.
8. **What happens when the clock steps backwards?** Decide: count on the stale timestamp (UUID schemes), tolerate a small step (Snowflake), or refuse and alarm. Write it down.

## Decision table

| Need | Pick | Store as | Note |
|------|------|----------|------|
| Opaque, unguessable, no index locality concern | UUIDv4 from a CSPRNG | 16-byte UUID / BINARY(16) | Default `randomUUID()` |
| Time-ordered primary key, no coordination | UUIDv7 | 16-byte UUID | Unsigned comparison in code |
| Time-ordered, human-readable, URL-safe | ULID | BINARY(16) or CHAR(26) | Monotonic mode for same-ms bursts |
| 8-byte key, strictly ordered per producer | Snowflake | BIGINT | Worker-id assignment per process |
| External reference shown to customers | Prefixed random (type_ + base62) | VARCHAR | Separate `created_at` column indexed |
| Retry-safe request key | Client UUIDv4 | 16 bytes, unique index | Expires with the idempotency window |

## Code-review traps

- [ ] Random bits come from `SecureRandom` / `secrets` / `crypto/rand` in production; seeded `Random` only in tests and demos.
- [ ] UUID comparison is unsigned (`Long.compareUnsigned`, or compare the string form); `java.util.UUID.compareTo` is signed and misorders half of all ids.
- [ ] The id is stored in its binary width, not as a 36-character string, unless a downstream system truly requires text.
- [ ] A monotonic generator has a test that issues more ids in one frozen millisecond than its counter holds and checks the overflow rule.
- [ ] A test steps the injected clock backwards by one millisecond and by one minute and asserts the documented behaviour for each.
- [ ] Snowflake worker ids are provably unique across every running process (lease, StatefulSet ordinal, or config checked at startup), not just per region or per host.
- [ ] Snowflake's borrowed-time counter and any `ClockMovedBackwards` events are exported as metrics.
- [ ] MySQL `UUID()` (version 1) is never used as a sortable key without `UUID_TO_BIN(uuid, 1)`.
- [ ] Nobody parses creation time out of an id in business logic; the record has its own timestamp column. Decoding the id's time is for debugging only.
- [ ] The id scheme and its bit layout are documented next to the table definition, with the custom epoch if there is one.
