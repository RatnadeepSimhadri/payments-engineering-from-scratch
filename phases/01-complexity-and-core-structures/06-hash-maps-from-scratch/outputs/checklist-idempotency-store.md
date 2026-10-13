---
name: checklist-idempotency-store
description: Review checklist for any service that stores and replays responses by idempotency key
phase: 1
lesson: 6
tags: [hash-map, idempotency, review, retries]
---

# Idempotency Store Review Checklist

Use when a new endpoint accepts an `Idempotency-Key` (or equivalent) or when auditing an existing one. Each unchecked box is a question for the author, not an automatic rejection.

## The key

- [ ] The key is client-generated and at least 128 bits of entropy (UUIDv4 or better); the server never derives it from the payload alone.
- [ ] The key is scoped: the same key from two different merchants or API credentials is two different keys. The map key is `(account, idempotency-key)`, not the header alone.
- [ ] A reused key with a *different* request body is rejected with an error, not silently replayed. The stored value includes a fingerprint (hash) of the request to detect this.
- [ ] Key length and character set are bounded and validated before hashing.

## The value

- [ ] The stored response includes the status code and the full body; a replay is byte-for-byte what the first caller received.
- [ ] An in-flight request stores a "processing" marker before the side effect so a concurrent retry can wait or receive 409, rather than running the handler twice.
- [ ] Handler failures are stored too (or deliberately not stored) and the choice is documented: should a retry of a 500 run again?

## Expiry and deletion

- [ ] Keys expire (Stripe uses 24 hours) and the expiry is a TTL on the store, not a cron job that scans the table.
- [ ] If the store is an in-process open-addressing table, deletes use tombstones or backward-shift, never "mark empty". There is a test that a key inserted past a deleted slot is still found.
- [ ] Tombstone accumulation is bounded: the resize trigger counts `size + tombstones`, or the table is rebuilt periodically.

## The table

- [ ] The load factor is measured in production (a gauge), not assumed. Alert if it exceeds the resize threshold for more than a minute: the table is not growing.
- [ ] Resizes are either incremental or happen off the request path; a 2-million-entry rehash on a request thread is a p99 incident.
- [ ] The hash is seeded or keyed (SipHash, per-process seed) if keys are client-controlled, so an attacker cannot force collisions.
- [ ] There is a test with a deliberately constant hash function proving the table stays *correct* (every key still found) even when it is slow.

## Across processes

- [ ] The store is shared by every instance that serves the endpoint (Redis, PostgreSQL unique index, a replicated KV store); an in-process map is only acceptable behind consistent hashing of keys to instances.
- [ ] "Insert if absent" is one atomic operation on the store (`SET NX`, `INSERT ... ON CONFLICT DO NOTHING`), not a read followed by a write.
- [ ] The store and the side effect commit together, or the side effect is itself idempotent, so a crash between them does not leave a charge with no stored key.

## Tests present

- [ ] Same key three times: handler runs once, three identical responses.
- [ ] Same key, different body: rejected.
- [ ] Two concurrent requests with one key: exactly one handler run.
- [ ] A thousand inserts across several resizes: every key still retrievable.
- [ ] Insert, delete, insert into the same probe run: no key lost (open addressing only).
