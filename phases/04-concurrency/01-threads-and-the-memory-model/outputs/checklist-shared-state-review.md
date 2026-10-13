---
name: checklist-shared-state-review
description: Review checklist for any field, variable, or object that more than one thread or goroutine touches; forces a happens-before argument instead of a test result
phase: 4
lesson: 1
tags: [concurrency, memory-model, review, happens-before]
---

# Shared State Review Checklist

Use on any pull request where a variable is written by one thread and read or written by another. Fill the table first; the questions below are only meaningful once every shared variable is listed.

## Inventory

For each shared variable, one row. "Edge" names the happens-before rule that orders every write against every other access: a lock, a volatile or atomic, a channel, an executor hand-off, or `start`/`join`.

| Variable | Written by | Read by | Compound operation? | Edge that orders every pair |
|----------|------------|---------|---------------------|-----------------------------|
| | | | | |

A row with an empty "Edge" cell is a data race. It does not matter what the tests say.

## Atomicity

- [ ] Every `+=`, `-=`, `++`, check-then-act (`if (!map.containsKey(k)) map.put(k, v)`), and read-modify-write on a shared variable is inside one critical section or is a single hardware atomic (`AtomicLong.addAndGet`, `atomic.AddInt64`, `compareAndSet`).
- [ ] No reasoning of the form "it is a single line" or "it is a single instruction" appears in the review. Atomicity is granted by the language model, never by source or instruction count.
- [ ] In Python, no invariant that spans more than one bytecode relies on the GIL. `list.append` and `dict[k] = v` are atomic in CPython; `x += 1` and `d[k] = d.get(k, 0) + 1` are not.

## Visibility and ordering

- [ ] Every flag that one thread sets and another polls is `volatile` (Java), an `atomic.Bool`/`atomic.Int32` (Go), or replaced by a channel, latch, or `Event`.
- [ ] Every object handed from one thread to another travels through a synchronizing hand-off: `ExecutorService.submit`, a concurrent queue, a channel, `start()` before use, `join()` after. A plain field assignment is not a hand-off.
- [ ] No code depends on "stores happen in program order" across threads. If B must see `x` before `y`, the write to `y` is the volatile/atomic/unlock and `x` is written before it.
- [ ] Nobody has argued that `volatile` "goes to main memory" or that a cache flush fixes anything. The argument cites a rule: unlock before lock, volatile write before read, send before receive, start, join.

## Scope of the lock

- [ ] The critical section covers the *decision*, not just the access. "Read balance under lock, release, compare, reacquire, write" is a race condition even though every access is synchronized.
- [ ] Every path that writes the variable acquires the same lock. Grep for the field name; every write site appears inside the same `synchronized`, `lock()`, or `mu.Lock()`.
- [ ] Reads that need a consistent snapshot of two or more variables hold the lock across both reads.

## Evidence

- [ ] The PR description contains the happens-before argument, not a statement that a stress test passed.
- [ ] Go: `go test -race ./...` runs in CI for this package and passes. A `-race` pass is evidence that no race was observed, not that none exists.
- [ ] Java: any lock-free or `volatile`-based code is accompanied by a `jcstress` test or a written note on which JLS 17.4.5 rule orders each pair.
- [ ] Python: any code that is "safe because of the GIL" names the single bytecode it relies on and notes that the free-threaded build (3.13t) changes the per-object locking rules.

## Red flags that end the review

- A `volatile` added "to be safe" on a field that is also updated with `++` or `+=`.
- A `Thread.sleep`, `time.Sleep`, or `yield` inserted to make a test pass.
- A comment containing "this is fine because the window is tiny".
- A shared `HashMap`, `ArrayList`, Go `map`, or Python `dict` mutated from two goroutines or threads without a lock, even if "only one of them writes".
