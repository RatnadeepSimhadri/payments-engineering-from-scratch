---
name: interview-card-python-gil
description: Interview and design-review card on the Python GIL, free-threaded CPython, and choosing a concurrency mechanism for a Python payments service
phase: 4
lesson: 10
tags: [python, gil, concurrency, interview, design-review]
---

# Interview Card: The Python GIL

Use as a self-test before an interview, or as the question list when reviewing a design that proposes Python for a latency- or throughput-sensitive payments component.

## The thirty-second answer

The GIL is a mutex that lets one thread at a time execute Python bytecode. It exists so that reference counts and the interpreter's internal structures are never updated concurrently. It is released when a thread blocks on I/O and every 5 ms when another thread is waiting. It protects the interpreter, not your program: `x += 1` and check-then-act are still races. PEP 703's free-threaded build removes it, replacing it with per-object locks, biased reference counting, and immortal objects, at a single-threaded cost that is shrinking each release.

## Questions an interviewer asks, and the answer they want

| Question | The answer | The trap |
|----------|------------|----------|
| Can Python use multiple cores? | Yes: processes, subinterpreters, C extensions that release the GIL, and the free-threaded build. Python *bytecode* on the standard build cannot. | Saying "no" |
| Is `list.append` thread-safe? | Yes on both builds: one C-level operation under the GIL, or under the list's own lock when free-threaded. | Extending that to `lst[i] += 1` |
| Is `counter += 1` thread-safe? | No. Several bytecodes; the interpreter can switch at defined points between them, and a `__iadd__` in Python is one. Rare to observe on 3.12+, routine on the free-threaded build. The language promises nothing. | "The GIL makes it atomic" |
| When do threads help? | I/O-bound work, and CPU work inside C extensions that release the GIL (hashlib, zlib, NumPy). | Using threads for pure-Python CPU loops |
| What is the convoy effect? | An I/O-bound thread waits up to a full switch interval behind a CPU-bound thread every time it needs the GIL for microseconds of work. | Not knowing it exists |
| What does the process model cost? | N copies of the interpreter, models, caches, connection pools; shared state must move out of process or is silently sharded. | Counting only CPU |
| What does free-threaded Python change for correct code? | Nothing: code that used locks stays correct. Code that relied on the GIL by accident breaks. | Assuming either everything or nothing breaks |
| How would you check which build is running? | `sysconfig.get_config_var("Py_GIL_DISABLED")` for the build, `sys._is_gil_enabled()` for the live state; an incompatible extension can re-enable the GIL at import. | Checking only the version string |

## Design-review questions for a Python payments component

- [ ] Where does each request's time go: I/O wait, C extension, Python bytecode? Show the profile.
- [ ] If processes: what is the memory per worker, the total connection count, and the home of every in-process counter and cache?
- [ ] If threads: is any thread CPU-bound in Python? If so, what is the measured latency impact on the I/O-bound ones?
- [ ] If asyncio: is every call on the request path async, and what happens when one synchronous call takes 200 ms?
- [ ] If free-threaded: do all C extensions declare free-threading support, and does the service assert `sys._is_gil_enabled()` is `False` at startup?
- [ ] Every read-modify-write and check-then-act on shared state: where is its lock? "The GIL" is not an answer.
- [ ] The idempotency store and the rate limiter: external, shared, or sharded by accident?
- [ ] Ledger invariants: enforced in the database transaction, not in process memory?

## Numbers to know

- Switch interval: 5 ms (`sys.getswitchinterval()`).
- `hashlib` releases the GIL above roughly 2 KB of input.
- Free-threaded build: PEP 703 accepted 2023; experimental in 3.13 (`python3.13t`), supported in 3.14 (PEP 779). Single-threaded overhead measured in the low tens of percent in 3.13, single digits targeted after.
- Per-interpreter GIL: PEP 684 (3.12); stdlib API `concurrent.interpreters`: PEP 734 (3.14).
- `multiprocessing` default start method on Linux: `fork` through 3.13, `forkserver` from 3.14.
