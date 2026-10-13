---
name: interview-card-skip-lists
description: One-page interview and design card for skip lists — mechanism, expected cost, spans for rank, production users, failure mode, and the decision rule against trees
phase: 3
lesson: 7
tags: [skip-list, redis, sorted-set, concurrency, interview, ordered-map]
---

# Skip Lists: Interview and Design Card

## Mechanism in three sentences

A sorted linked list where each node is also linked into `k - 1` sparser "express" lists, with `k` drawn from a geometric distribution (`p` chance of each extra level). A search starts on the top lane, moves forward while the next key is smaller, and steps down when it is not. An insert is a search that records the last node visited at each level (the `update` array) and splices the new node in after each of them; there is no rebalancing.

## Numbers to know (p = 1/2)

| Quantity | Value |
|----------|-------|
| Nodes reaching level `i` | `n / 2^(i-1)` |
| Levels in use | about `log2(n)`; cap `maxLevel = ceil(log_(1/p)(n))` |
| Pointers per node | `1 / (1 - p)` = 2 (1.33 at `p = 1/4`, Redis's choice) |
| Expected comparisons per search | `log_(1/p)(n) / p + 1/(1-p)` = about `2 log2(n) + 2` (33 for 100,000 keys) |
| Worst case | `O(n)`, with probability vanishing polynomially in `n` |
| Effect of insertion order | none; only the coin matters |

## Spans and rank

Each forward pointer stores how many level-0 positions it skips. Summing spans along a search path yields the rank of the node reached, so `rank(key)` and `byRank(r)` are `O(log n)`. On insert: `update[i].span[i] = rank[0] - rank[i] + 1`, new node gets the remainder, levels above the new node's height gain `+1`. On delete: pointers that bypass the victim absorb `span - 1`, pointers above it lose `1`. A tree needs subtree sizes at every node for the same; `TreeMap` has none, so rank is `O(n)` there.

## Who uses it and why

| System | Role | Reason given |
|--------|------|--------------|
| Redis sorted sets (`ZADD`, `ZRANK`, `ZRANGE`) | ordered index by score, with a hash for member lookup | memory comparable to a tree at `p = 1/4`; range walks are list walks; simpler to implement and extend (spans for `ZRANK`) |
| `java.util.concurrent.ConcurrentSkipListMap` | the JDK's concurrent ordered map | inserts are per-level CAS splices; rotations would need multi-pointer atomic updates |
| LevelDB / RocksDB memtable | sorted write buffer | readers traverse lock-free while writers insert; flush is a sequential scan |

## The one failure mode

A level source that is not an independent fair coin. `() -> 1` gives a linked list (`O(n)`); a level derived from the key lets an adversary choose a bad shape. The RNG is injected and seeded for tests, and never computed from the data.

## Decision rule

- Ordered map, single thread, need hard worst-case bounds: red-black tree (`TreeMap`).
- Ordered map, many threads: skip list (`ConcurrentSkipListMap`).
- Ordered set with rank queries: skip list with spans (Redis), or a tree augmented with subtree sizes.
- Ordered data on disk or too large for RAM: B-tree (Phase 3, Lesson 04).
- Write-heavy buffer that will be flushed sorted: skip list memtable (Phase 3, Lesson 05).

## Questions to expect

- Walk a search for a key through a five-level list; count the comparisons. (Forward while next < target; down otherwise; about `2 log2 n`.)
- Why is insert `O(log n)` expected with no rebalancing? (The search finds the `update` nodes; the splice touches only the new node's `k` predecessors; `k` has mean `1/(1-p)`.)
- How would you implement `ZRANK`? (Spans.)
- What goes wrong if `randomLevel()` uses `key.hashCode()`? (Shape becomes a function of the data; adversarial or skewed keys degrade it to `O(n)`.)
- Why not a skip list on disk? (One cache miss or page read per node visited; B-trees pack hundreds of keys per page.)
