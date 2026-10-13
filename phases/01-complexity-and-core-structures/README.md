# Phase 1: Complexity & Core Data Structures

> Every structure is a trade between time, space, and what you can promise.

14 lessons, 4 complete, ~16 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Complexity Analysis: Big-O, Big-Theta, and What Hardware Does to Them](01-complexity-and-big-o/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Complexity Analysis: Big-O, Big-Theta, and What Hardware Does to Them](01-complexity-and-big-o/) | Learn | ~75 min | ✅ | O(n) with cache misses loses to O(n log n) without them. |
| 02 | [Amortized Analysis: Why Dynamic Arrays Are O(1)](02-amortized-analysis/) | Learn | ~60 min | ⬚ | The accounting method, applied to the structure you use most. |
| 03 | [Dynamic Arrays from Scratch](03-dynamic-arrays/) | Build | ~75 min | ⬚ | Growth factors, memory layout, and why 1.5x beats 2x in some runtimes. |
| 04 | [Linked Lists: Singly, Doubly, and Intrusive](04-linked-lists/) | Build | ~75 min | ⬚ | Pointer chasing is slow; here is when linked lists still win. |
| 05 | [Stacks, Queues, and Ring Buffers](05-stacks-queues-and-deques/) | Build | ~60 min | ⬚ | A ring buffer is the data structure inside every message broker. |
| 06 | [Hash Maps from Scratch: Chaining, Open Addressing, and Resizing](06-hash-maps-from-scratch/) | Build | ~120 min | ✅ | The structure behind every idempotency store, built with load factors you can measure. |
| 07 | [Hash Functions, Collisions, and HashDoS](07-hash-functions-and-collisions/) | Learn | ~60 min | ⬚ | An attacker who knows your hash function can turn O(1) into O(n). |
| 08 | [Binary Search Trees and Ordered Maps](08-binary-search-trees/) | Build | ~90 min | ⬚ | Range queries over transactions by timestamp need order, not just hashing. |
| 09 | [Heaps and Priority Queues from Scratch](09-heaps-and-priority-queues/) | Build | ~90 min | ✅ | The structure behind every scheduler, timer wheel, and retry queue. |
| 10 | [Disjoint Sets (Union-Find) with Path Compression](10-disjoint-sets/) | Build | ~60 min | ⬚ | Linking accounts, merging entities, and detecting fraud rings. |
| 11 | [Bit Manipulation and Compact Encodings](11-bit-manipulation/) | Build | ~60 min | ⬚ | Flags, bitmaps, and the bit tricks in ISO 8583 and Snowflake IDs. |
| 12 | [Strings, Unicode, and Byte Encodings](12-strings-and-encodings/) | Learn | ~60 min | ⬚ | Why a name on a card is bytes, not characters, and what that breaks. |
| 13 | [Tries and Prefix Trees](13-tries/) | Build | ~75 min | ✅ | BIN range lookup for card routing is a trie problem. |
| 14 | [Choosing a Structure: A Decision Procedure](14-choosing-a-structure/) | Learn | ~45 min | ⬚ | Access pattern first, then invariants, then constants. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
