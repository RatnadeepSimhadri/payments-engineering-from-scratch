---
name: interview-card-balanced-trees
description: One-page interview and design-review card for AVL and red-black trees, with the decision rule for tree vs hash map vs skip list vs B-tree
phase: 3
lesson: 3
tags: [trees, avl, red-black, interview, ordered-map]
---

# Balanced Trees: Interview and Design-Review Card

## The one-sentence versions

- **Why balance:** every BST operation costs O(height); sorted input (timestamps, sequence IDs) makes a plain BST height n.
- **Rotation:** a three-pointer rearrangement of a node and one child that keeps in-order sequence and moves one subtree up a level.
- **AVL:** `|height(left) - height(right)| <= 1` at every node. Height < 1.44 log2(n+2). At most 2 rotations per insert, O(log n) per delete.
- **Red-black:** no red-red edge; equal black count on every root-to-null path; root black. Height <= 2 log2(n+1). Amortised O(1) rotations per insert or delete; most fixes are recolourings.

## Derive the height bounds in two lines each

- **AVL.** Smallest tree of height h = root + smallest trees of heights h-1 and h-2, so n(h) = n(h-1) + n(h-2) + 1, a Fibonacci recurrence; n(h) grows as phi^h, so h <= log_phi(n) ~ 1.44 log2(n).
- **Red-black.** All paths have black height bh and at most one red per black, so height <= 2bh. The all-black tree of black height bh has 2^bh - 1 nodes, so bh <= log2(n+1).

## AVL rebalance cases (at the first node with balance +2 or -2)

| Case | Detect | Fix |
|------|--------|-----|
| LL | bf(node) = +2, bf(left) >= 0 | rotateRight(node) |
| LR | bf(node) = +2, bf(left) < 0 | rotateLeft(left), then rotateRight(node) |
| RR | bf(node) = -2, bf(right) <= 0 | rotateLeft(node) |
| RL | bf(node) = -2, bf(right) > 0 | rotateRight(right), then rotateLeft(node) |

## Red-black insert fix-up (new node is red; loop while parent is red)

| Case | Uncle | New node | Action |
|------|-------|----------|--------|
| 1 | red | any | parent and uncle black, grandparent red; continue from grandparent |
| 2 | black | inner grandchild (zig-zag) | rotate at parent toward the outside; now case 3 |
| 3 | black | outer grandchild | parent black, grandparent red, rotate at grandparent; done |

Finally colour the root black. Delete has six cases (CLRS 13.4); know that it exists and is bounded by three rotations.

## Numbers to quote (from this lesson's demo, 100,000 keys)

| Input | AVL | Red-black |
|-------|-----|-----------|
| Sorted | height 17, 99,983 rotations | height 31, 99,969 rotations, 499,846 recolourings |
| Random | height 20, 69,356 rotations | height 20, 57,968 rotations, 231,220 recolourings |

Recolourings are byte writes; rotations rewrite three pointers. That is the trade.

## Who chose what, and why

- `java.util.TreeMap`, `TreeSet`: red-black (CLRS transcription). General-purpose, unknown read/write mix.
- C++ `std::map`, `std::set`: red-black in every major implementation.
- Linux kernel `rbtree`: CFS scheduler run queue, VMAs, hrtimers. Updates under a lock must be short.
- `java.util.concurrent.ConcurrentSkipListMap`: skip list, because rotations are hard to make lock-free.
- Databases: B-trees. Wide nodes match pages and cache lines; binary nodes are one cache miss each.

## Decision rule

1. Need only point lookups by key? Hash map.
2. Need order (range, predecessor, successor, min, sorted iteration) in memory, single writer? Red-black tree (`TreeMap`), or AVL if lookups dominate and updates are rare.
3. Need order with many concurrent writers? Skip list (`ConcurrentSkipListMap`).
4. Need order on disk or over tens of millions of keys? B-tree or LSM tree.
5. Need "everything expiring before now" at high rate? Timing wheel (Phase 3, Lesson 12) beats all of the above.

## Questions interviewers actually ask

- Insert 30, 20, 10 into an AVL tree and draw each step. (LL, one rotation, 20 at the root.)
- Insert 10, 5, 15, 1 into a red-black tree. (Case 1, zero rotations, root recoloured black.)
- Why is a hash map not enough for a leaderboard, an order book, or an expiry queue? (No order.)
- Why does `TreeMap` treat `BigDecimal("1.0")` and `BigDecimal("1.00")` as the same key? (It uses `compareTo`, not `equals`.)
- How would you make a balanced tree thread-safe? (Coarse lock; or do not: use a skip list.)
