---
name: interview-card-linked-lists
description: One-page card for linked list interview questions and for reviewing code that reaches for a linked list
phase: 1
lesson: 4
tags: [data-structures, linked-lists, interview, review, cache]
---

# Linked Lists: Interview and Review Card

## The contract in one sentence

A linked list gives O(1) insertion and removal **at a position you already hold** and O(1) splicing of whole lists, with stable element identity. Everything else, an array does faster.

## Cost table

| Operation | Singly (head + tail) | Doubly (sentinel) | Array |
|-----------|----------------------|-------------------|-------|
| addFirst / removeFirst | O(1) | O(1) | O(n) |
| addLast | O(1) | O(1) | O(1) amortized |
| removeLast | O(n) | O(1) | O(1) |
| get(i) | O(i) | O(min(i, n-i)) | O(1) |
| remove given the node | O(n) | O(1) | n/a |
| remove given the value | O(n) | O(n) | O(n) |
| splice B onto A | O(1) | O(1) (O(size B) with owner tracking) | O(size B) |
| iterate n | one cache miss per node | one cache miss per node | one miss per 8 to 16 elements |

## Three questions before choosing a linked list

1. Will any caller ever index by position? If yes, it is an array.
2. Will the caller hold the node (or be the node) when it removes? If no, removal is O(n) and the list buys nothing.
3. Is the list iterated more often than it is mutated in the middle? If yes, it is an array; measured gap is 8x to 200x.

## Patterns worth knowing cold

**Reverse in place (three pointers):**

```java
Node prev = null, cur = head;
while (cur != null) { Node next = cur.next; cur.next = prev; prev = cur; cur = next; }
head = prev;   // and tail = old head
```

**LRU cache:** `HashMap<K, Node>` plus a doubly linked list. `get` = map lookup + `moveToFront(node)`; `put` on full = `removeLast()` + map remove; all O(1). This is `LinkedHashMap(capacity, 0.75f, true)` with `removeEldestEntry`.

**Intrusive list:** the element extends the node (or embeds `list_head` in C). No allocation on insert, O(1) `remove(element)`, O(1) `contains(element)` via an owner field. Constraint: one list per element per set of links.

**Sentinel:** a permanent dummy node framing the list. `linkBefore(value, succ)` and `unlink(node)` become branch-free; the empty list is `sentinel.next == sentinel`.

## Trap questions and answers

- *"LinkedList.get(i) in a loop"* — O(n^2). Use an iterator or an array.
- *"LinkedList.remove(Object) on a hot path"* — O(n) scan; nodes are hidden so the caller cannot hold one. Use an intrusive list or a map to the node.
- *"Which is faster to iterate, LinkedList or ArrayList?"* — ArrayList by one to two orders of magnitude; the cause is dependent loads and cache misses, not big-O.
- *"When would you still pick a linked list?"* — O(1) removal by handle (LRU, timer wheels, free lists), O(1) splice (schedulers, bulk returns), stable node identity under modification, or `ListIterator.remove` during traversal.
- *"Why is Java's LinkedList almost never the right choice?"* — No exposed nodes (so no O(1) removal by handle), no sentinel, 24+ bytes per node, and `ArrayDeque` beats it as a queue or stack.
- *"Detect a cycle"* — two pointers, one moving twice as fast (Floyd); they meet if and only if there is a cycle. O(n) time, O(1) space.

## Code review red flags

- A `List<T>` parameter whose implementation is a `LinkedList` and whose body calls `get(int)`.
- `remove(Object)` or `contains(Object)` on any linked list of more than a few hundred elements.
- A hand-rolled list whose unlink does not null the removed node's links or check that the node belongs to this list.
- A singly linked list with a `tail` field and a `removeFirst` that does not reset `tail` when the list empties.
