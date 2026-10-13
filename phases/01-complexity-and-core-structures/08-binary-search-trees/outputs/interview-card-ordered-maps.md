---
name: interview-card-ordered-maps
description: One-page revision card for binary search trees and ordered maps, with the questions interviewers actually ask
phase: 1
lesson: 8
tags: [bst, ordered-map, range-query, interview]
---

# Interview Card: Binary Search Trees and Ordered Maps

## The invariant (say it exactly)

For every node, **every** key in the left subtree is strictly less than the node's key and **every** key in the right subtree is strictly greater. Subtrees, not children. Keys are unique; duplicates need a composite key.

## Costs, by insert order

| Insert order | Height | Lookup / insert / delete |
|--------------|--------|--------------------------|
| Balanced | floor(log2 n) | O(log n) |
| Random | ~1.39 log2 n expected | O(log n) expected |
| Sorted (timestamps, auto-increment ids) | n - 1 | O(n): the tree is a linked list |

Everything is O(h). The entire subject of balanced trees (red-black, AVL, B-tree) is keeping h = O(log n) regardless of input order.

## The operations

| Operation | How | Cost |
|-----------|-----|------|
| get / containsKey | one root-to-leaf walk | O(h) |
| put | walk, attach at the null, or replace value on equal key | O(h) |
| min / max | walk left / walk right | O(h) |
| floor / ceiling | walk, remember the last key passed on the way right / left | O(h) |
| remove, 0 or 1 child | splice the child into the parent's slot | O(h) |
| remove, 2 children | copy in-order successor (min of right subtree) up, delete that node (it has no left child) | O(h) |
| in-order traversal | left, node, right; iterative with an explicit stack | O(n) |
| range [lo, hi] | in-order, skip left subtree when key < lo, stop when key > hi | O(h + k) |

## Delete with successor, in one breath

"Find the node. If it has two children, find the leftmost node of its right subtree, copy that key and value into the node, then unlink the leftmost node, which has at most a right child. Otherwise, replace the node by its only child or by null."

## The three questions they ask

1. **Why not a hash map?** A hash map gives O(1) exact lookup by destroying order. Any query with a BETWEEN, a "nearest", a "first after", or a sorted iteration needs an ordered map. If the only query is exact-key, the hash map wins.
2. **Why do you need balancing?** Because real keys arrive nearly sorted (time, sequence numbers), and a plain BST on sorted input is a linked list. Show the number: 4,096 sorted inserts give height 4,095; shuffled give about 25; balanced would be 12.
3. **How does a range query avoid touching everything?** In-order traversal with two cuts: never descend left from a key below lo, stop at the first key above hi. Cost O(h + k) where k is the result size.

## When to reach for an ordered map in a design review

| Query | Structure |
|-------|-----------|
| Is this idempotency key present | hash map |
| Transactions between t1 and t2 | ordered map (TreeMap, B-tree index, sorted set) |
| Latest FX rate at or before t | ordered map, `floorKey(t)` |
| Next retry due | heap (Lesson 09), or ordered map if you also need cancel-by-key |
| Top-k amounts today | heap of size k, or ordered map with rank |
| Prefix match on a BIN | trie (Lesson 13) |

## Traps to mention

- Comparator inconsistent with `equals`, or `a - b` on longs overflowing: `TreeMap` builds a non-BST and returns wrong answers with no exception.
- Recursive traversal on a degenerate tree: stack overflow. Use an explicit stack.
- `subMap` in Java is a live view, not a snapshot.
- Concurrent writers corrupt pointers; a plain BST is single-writer.
