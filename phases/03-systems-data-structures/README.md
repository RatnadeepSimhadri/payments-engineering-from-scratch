# Phase 3: Systems Data Structures

> The structures that databases, caches, and routers are made of.

15 lessons, 15 complete, ~22 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Bloom Filters from Scratch](01-bloom-filters/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/03-systems-data-structures/01-bloom-filters --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Bloom Filters from Scratch](01-bloom-filters/) | Build | ~90 min | ✅ | A few bits per key to say 'definitely not seen' before hitting the database. |
| 02 | [Count-Min Sketch and HyperLogLog](02-count-min-sketch-and-hyperloglog/) | Build | ~90 min | ✅ | Counting distinct cards per merchant in constant memory. |
| 03 | [Balanced Trees: AVL and Red-Black](03-balanced-trees/) | Build | ~120 min | ✅ | Rotations, invariants, and why Java's TreeMap is red-black. |
| 04 | [B-Trees and Why Databases Love Them](04-b-trees/) | Build | ~120 min | ✅ | Pages, fan-out, and the disk access pattern behind every index. |
| 05 | [LSM Trees: Memtables, SSTables, and Compaction](05-lsm-trees/) | Build | ~120 min | ✅ | Write-optimized storage, built with a sorted map and some files. |
| 06 | [Consistent Hashing and Virtual Nodes](06-consistent-hashing/) | Build | ~90 min | ✅ | Add a shard and move 1/N of the keys instead of all of them. |
| 07 | [Skip Lists](07-skip-lists/) | Build | ~60 min | ✅ | The probabilistic ordered structure inside Redis sorted sets. |
| 08 | [LRU and LFU Caches from Scratch](08-lru-and-lfu-caches/) | Build | ~90 min | ✅ | O(1) eviction with a hash map and a doubly linked list. |
| 09 | [Merkle Trees and Content-Addressed Verification](09-merkle-trees/) | Build | ~75 min | ✅ | Verify two ledgers agree by comparing one hash. |
| 10 | [Segment Trees and Fenwick Trees](10-segment-trees-and-fenwick-trees/) | Build | ~90 min | ✅ | Range sums over balances with O(log n) updates. |
| 11 | [Rate Limiter Algorithms: Token Bucket, Leaky Bucket, Sliding Window](11-rate-limiter-algorithms/) | Build | ~90 min | ✅ | Protect a payment API from a retry storm with forty lines of code. |
| 12 | [Hierarchical Timing Wheels](12-time-wheels-and-timers/) | Build | ~75 min | ✅ | Millions of timeouts with O(1) insert and tick. |
| 13 | [Unique ID Generation: UUIDs, ULIDs, and Snowflake](13-unique-id-generation/) | Build | ~60 min | ✅ | Sortable, collision-resistant IDs for a distributed ledger. |
| 14 | [Memory Layout, Cache Lines, and False Sharing](14-memory-layout-and-cache-lines/) | Learn | ~60 min | ✅ | Why an array of structs can be 10x faster than a struct of arrays, or the reverse. |
| 15 | [Serialization: JSON, Protobuf-style Binary, and Schema Evolution](15-serialization-formats/) | Build | ~90 min | ✅ | Hand-encode a varint and never fear a wire format again. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
