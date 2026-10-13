# The Python GIL and Free-Threaded Python

> What the GIL protects you from, and what it does not.

**Type:** Learn
**Languages:** —
**Prerequisites:** Phase 4, Lesson 01 (Threads and the Memory Model); Phase 4, Lesson 02 (Locks, Atomics, and a Balance That Never Lies); Phase 4, Lesson 06 (Executors, Goroutines, and asyncio)
**Time:** ~45 minutes

## Learning Objectives

- State precisely what the global interpreter lock protects (the interpreter's own data structures) and what it does not (your program's invariants), with the bytecode to prove it
- Predict whether a Python workload gains from threads by classifying it as I/O-bound, CPU-bound in C, or CPU-bound in Python, and explain the convoy effect that can make threads slower than one
- Choose among threads, `asyncio`, `multiprocessing`, subinterpreters, and native extensions for each part of a Python payments service, and state the cost of each
- Describe how free-threaded CPython (PEP 703) removes the GIL, what replaces it (per-object locks, biased reference counting, immortal objects), and what it costs single-threaded code
- Compare the CPython and JVM concurrency models on the four axes that matter to a service: parallelism, memory, shared state, and the number of connection pools you end up with
- Recognise the three bugs the GIL never prevented: lost updates, check-then-act, and ordering assumptions between threads

## The Problem

A payments team ports a webhook-processing service from Java to Python because the fraud team's models are in Python and the round trip to a separate scoring service added 30 ms. The service receives processor webhooks, verifies the HMAC signature, looks up the payment, runs the fraud score, writes a ledger posting, and acknowledges. In Java it ran on one JVM with 64 threads and handled 3,000 webhooks per second on a 16-core box. The Python port, with a 64-thread pool, handles 380 per second on the same box, and `top` shows the process at 105 percent CPU with 15 cores idle.

The team has heard of the GIL and reaches for the standard advice: run 16 processes behind a pre-fork server. Throughput goes to 2,900 per second. Then three new problems appear. Memory is 16 times what it was, because each process holds its own copy of the fraud model's 600 MB of feature tables. The database connection count is 16 times what it was, because each process has its own pool, and the connection limit on the Postgres primary is hit during a traffic spike. And the per-merchant rate limiter, which lived in a dictionary in the Java service, is now sixteen independent dictionaries, so a merchant limited to 100 webhooks per minute is receiving 1,600. The JVM's model of one process, many threads, one shared heap had been carrying architecture that nobody had written down.

There is a fourth problem that does not show up in `top`. During the port someone removed the lock around the rate limiter's `count += 1`, reasoning that "the GIL makes Python thread-safe". Under the 16-process deployment this is harmless, because each process has one dictionary and the race is between processes, not threads. But the team's next move is free-threaded Python 3.13 to get the memory and connection count back, and with the GIL gone the `+=` is the lost update from Lesson 02 again. The GIL never made that line safe; it made the window small. This lesson is about the difference, and about what to do with each kind of work in a Python service, compared throughout with the JVM the team came from.

## The Concept

### What the GIL is

CPython manages memory with reference counting: every object carries `ob_refcnt`, incremented when a reference is created and decremented when one dies, freed at zero. An increment is a load, add, store on a shared field, which is exactly the non-atomic read-modify-write from Lesson 01. If two threads could run Python code at the same time, every shared object's reference count would be a race, and a count that reached zero one decrement early would free a live object. The interpreter also has mutable internal state everywhere: the list and dict implementations, the small-object allocator, the import system, the specialising interpreter's inline caches.

The **global interpreter lock** is the mutex that makes all of this safe by the simplest means available: only one thread executes Python bytecode at a time. A thread acquires the GIL to run, and releases it in two situations. First, voluntarily, when it is about to block: every blocking call in the standard library (`socket.recv`, file reads, `time.sleep`, `select`, the database driver's `fetch`) releases the GIL before waiting and re-acquires it after. Second, when forced: a thread that has held the GIL for the **switch interval** (`sys.getswitchinterval()`, default 5 ms since Python 3.2) is asked to release it so another waiting thread can run. The eval loop checks for that request at defined points in the bytecode stream (in 3.12 and later, at backward jumps, calls, and function entry), not between every pair of instructions.

So the GIL is a lock on the interpreter, taken and dropped thousands of times a second. It is not a lock on your data.

### What the GIL protects, and what it does not

| Property | GIL build guarantees it? | Why |
|----------|--------------------------|-----|
| Reference counts never corrupt | yes | only one thread mutates them at a time |
| `list.append`, `dict[k] = v`, `d.get(k)` do not corrupt the container | yes | each is one C-level operation executed while holding the GIL |
| Interpreter never crashes due to a data race in its own code | yes | that is the whole purpose |
| `balance += amount` is atomic | **no** | it is several bytecodes; the language makes no promise about switches between them |
| `if balance >= amount: balance -= amount` is atomic | **no** | check-then-act; two threads can both pass the check |
| Thread A's writes are visible to thread B in order | no promise | CPython's implementation happens to publish through the lock handoff, but the language reference does not say so |
| Two threads run Python code in parallel | **no** | the lock serialises them; this is the throughput problem |

The bytecode makes the first "no" concrete. In Python 3.12:

```python
import dis

balance = 0
def credit(amount):
    global balance
    balance += amount

dis.dis(credit)
```

```text
  6  LOAD_GLOBAL    0 (balance)
     LOAD_FAST      0 (amount)
     BINARY_OP     13 (+=)
     STORE_GLOBAL   0 (balance)
     RETURN_CONST   0 (None)
```

Four instructions. If `balance` is an `int`, `BINARY_OP` is a C-level addition and the current interpreter will not switch threads in the middle of this sequence, because none of the four is a switch point. If `balance` is a `Money` object with a Python `__iadd__`, `BINARY_OP` calls into Python code, which **is** a switch point, and another thread can run a complete `credit` between the `LOAD_GLOBAL` and the `STORE_GLOBAL`. Lesson 02's lost update, in a language whose users believe they are protected.

Two points follow. The implementation detail that makes `int += int` hard to break is not a guarantee: it changed between 3.9 and 3.12 and it is gone in the free-threaded build. And a test that runs the loop ten million times without seeing a lost update has shown only that the switch points fell elsewhere today. Lesson 12's answer applies: make the schedule the input, or reason from the specification, which here says nothing. **Use a lock.** `threading.Lock` around the read-modify-write costs about 100 nanoseconds and is correct on every build.

### Three kinds of work

Whether threads help depends on where the time goes.

**I/O-bound.** The webhook handler spends most of its time waiting: the database round trip, the HTTP call to the processor, the write to the ledger. Every one of those waits releases the GIL. Sixty-four threads waiting on sixty-four sockets are sixty-four GIL releases; the small amount of Python between waits serialises, but it is small. Threads work, and `asyncio` works better for very high connection counts because it has no per-thread stack and no lock handoffs at all. The JVM comparison: this is where Python and the JVM look most alike, except that the JVM's threads also run the between-wait code in parallel.

**CPU-bound in C.** Hashing a 10 MB settlement file with `hashlib`, compressing it with `zlib`, multiplying matrices in NumPy: the library releases the GIL for the duration of the C call (`Py_BEGIN_ALLOW_THREADS` in the C source), and other threads run Python meanwhile. Threads give real parallelism here, which surprises people who have learned "Python cannot use multiple cores". It can; Python *bytecode* cannot. (Small inputs are the exception: `hashlib` holds the GIL for buffers under 2 KB because the release is more expensive than the hash.)

**CPU-bound in Python.** A Luhn check over ten million card numbers in a Python `for` loop, a rules engine written as nested `if` statements, JSON built by hand: this is bytecode, and bytecode is serialised. Four threads take as long as one, and often longer, because of the **convoy effect** David Beazley documented in 2010: a CPU-bound thread holds the GIL for the full 5 ms interval; an I/O-bound thread whose socket just became readable must wait up to 5 ms to run a few microseconds of code before it can issue its next read. Mixing CPU-bound and I/O-bound threads in one process makes the I/O-bound ones slow without making the CPU-bound ones fast.

### Worked example: the Luhn check four ways

Ten million card numbers, pure-Python Luhn validation, 16 cores. Timings are illustrative of the shape, not benchmarks; run the exercise to get yours.

```text
strategy                                  wall time   CPU cores used   notes
serial                                    8.0 s       1
4 threads (GIL build)                     8.3 s       1                lock handoffs add overhead; no parallelism
4 processes (multiprocessing.Pool)        2.3 s       4                4x memory; inputs pickled to workers
4 threads (free-threaded 3.13t build)     2.6 s       4                ~10-15 % single-thread overhead in this build
asyncio (single thread)                   8.0 s       1                nothing to await; async does not help CPU work
```

The row to remember is the second: threads did not help and were not supposed to. The row to think about is the fourth: the free-threaded build gives the process model's parallelism with the thread model's shared memory, at a cost in single-threaded speed that the CPython team has been reducing release by release.

### The mechanisms, and what each costs

| Mechanism | Parallel Python bytecode? | Shared memory? | Cost | Use for |
|-----------|---------------------------|----------------|------|---------|
| `threading` | no (GIL build) / yes (free-threaded) | yes | per-thread stack (~8 MB virtual); convoy effect | I/O-bound work; C-extension CPU work |
| `asyncio` | no | yes (one thread) | code must be async all the way down; one slow sync call blocks everything | high-connection I/O: thousands of webhooks or websockets |
| `multiprocessing` / `ProcessPoolExecutor` | yes | no (pickle, shared memory segments) | N copies of the interpreter, the model, the connection pool; IPC serialisation | CPU-bound Python work in bulk |
| Subinterpreters (PEP 684, 3.12; `concurrent.interpreters`, PEP 734, 3.14) | yes, one GIL per interpreter | limited: shareable types only | import cost per interpreter; most C extensions not yet compatible | isolated CPU work without process overhead |
| Native extension (C, Rust via PyO3, Cython) releasing the GIL | yes | yes | a second language in the build | the hot loop, once profiling has found it |
| Free-threaded CPython (PEP 703; 3.13 experimental, 3.14 supported) | yes | yes | single-thread slowdown; extensions must be free-threading-safe; your own races are now real | the whole service, once dependencies support it |

The process model's hidden costs are the ones from the Problem: memory multiplied by worker count, connection pools multiplied by worker count, and every in-process cache or counter silently becoming per-process. The JVM never made the team think about any of these because one process served everything. A Python service that uses processes must move shared state out of the process (Redis for the rate limiter and the idempotency store, a connection pooler like PgBouncer in front of Postgres) or accept that it is sharded by accident.

### Free-threaded CPython: removing the lock without removing the safety

PEP 703, accepted in 2023 and shipped as an experimental build in 3.13 (`python3.13t`, built with `--disable-gil`) and as a supported build in 3.14 (PEP 779), keeps the interpreter from crashing without a global lock by replacing the GIL with several finer mechanisms:

- **Biased reference counting** (Choi, Shull, and Torrellas, 2018): each object has two counts, one owned by the creating thread and updated without atomics, one shared and updated with atomic operations. Most objects are only ever touched by one thread, so most increments stay cheap.
- **Immortal objects** (PEP 683): `None`, `True`, small integers, interned strings, and other objects that live for the whole process have a reference count that is never updated, so the hottest objects in the interpreter generate no contention at all.
- **Deferred reference counting** for objects referenced from the stack and from module globals, which would otherwise be incremented on every function call.
- **Per-object locks** on `list`, `dict`, `set`, and other mutable containers, so a single `append` or item assignment is still atomic and the container never corrupts, exactly the guarantee the GIL gave for those operations.
- **mimalloc** as the allocator, with thread-safe heaps per size class.

What it does **not** change is the table in the second section. `balance += amount` was never atomic and still is not; the difference is that the lost update now happens routinely instead of rarely. Check-then-act is still two steps. Code that was correct under the GIL because it used locks is correct without it; code that was correct by accident is not. The free-threaded build has a switch, `PYTHON_GIL=1` or `-X gil=1`, to re-enable the GIL at runtime for a build that encounters an extension that needs it, and `sys._is_gil_enabled()` to ask which mode is live.

### Comparison: the Python service and the JVM service

| Axis | JVM | CPython with the GIL | Free-threaded CPython |
|------|-----|----------------------|-----------------------|
| Parallel execution of your code | yes, N threads on N cores | no; one process per core | yes, N threads on N cores |
| Memory for N-way parallelism | one heap | N interpreters, N copies of models and caches | one heap |
| Connection pools | one | N | one |
| In-process shared state (rate limiter, idempotency cache) | one, needs locks | N independent copies, each race-free by isolation | one, needs locks |
| `balance += amount` without a lock | lost updates | lost updates, rare, implementation-dependent | lost updates, frequent |
| Pause behaviour | GC pauses (Lesson 09) | refcount frees are immediate; cyclic GC is small and incremental in 3.14 | same, plus per-object lock contention on hot containers |
| Blocking a thread | cheap with virtual threads (Lesson 11) | releases the GIL; other threads run | just a blocked thread |

The row that decides architectures is the shared-state row. A JVM team gets one shared heap and must think about locks; a GIL-era Python team gets N heaps and must think about external stores; a free-threaded Python team is back to the JVM's problem with the JVM's tools.

### What it means for a Python payments service

Concretely, for the webhook service from the Problem:

1. **Signature verification** (`hmac.compare_digest` over a few KB): C code, releases the GIL for larger inputs, microseconds either way. Threads are fine.
2. **Database and HTTP calls**: I/O-bound. Threads or `asyncio`; `asyncio` if the service also holds thousands of idle connections.
3. **Fraud scoring**: if it is NumPy or a compiled model runtime, C code that releases the GIL; threads give parallelism. If it is a pure-Python rules engine, it is the Luhn row: processes, a subinterpreter pool, or rewrite the hot loop.
4. **Ledger posting**: the invariant (Phase 10, Lesson 03) is enforced by the database transaction, not by the process, so neither the GIL nor its absence changes correctness.
5. **The rate limiter and idempotency store**: must not live in process memory under a multi-process deployment, and must be locked under a free-threaded one. Lesson 02's `LockedCounter` is the right shape; a bare `+=` is the wrong one on every build.

### A decision procedure

```text
1. Profile one request. Split its time into: waiting on I/O, inside C extensions, executing Python bytecode.
2. Mostly I/O             -> threads (tens to hundreds of connections) or asyncio (thousands); one process per core only if CPU also saturates.
3. Mostly C extensions    -> threads; confirm the extension releases the GIL (its docs, or a quick 2-thread timing).
4. Mostly Python bytecode -> first try to move the hot loop into C/NumPy/Rust; if it stays Python, processes (or a subinterpreter pool);
                             free-threaded build once every dependency supports it.
5. Whatever you chose: every read-modify-write and check-then-act on shared state gets a lock, on every build, today.
6. Under processes: list every in-process resource (caches, counters, pools, models) and decide per item: externalise, share, or shard.
```

### Failure modes

- **"The GIL makes it thread-safe."** It makes the interpreter thread-safe. Check-then-act on a balance, read-modify-write on a counter, and "I set the flag, then the data" ordering assumptions are all still races. The GIL build hides them; the free-threaded build exposes them; the language promised nothing either way.
- **The convoy effect.** One CPU-bound thread in a service of I/O-bound threads raises every request's latency by up to the switch interval per GIL handoff. Move CPU-bound work to a process pool or lower `sys.setswitchinterval` and measure.
- **Forking a process with threads.** `multiprocessing` with the `fork` start method copies a process whose other threads may hold locks, including locks inside the C library. Python 3.14 changed the default on Linux from `fork` to `forkserver` for this reason; on older versions set `spawn` or `forkserver` explicitly in any service that has threads.
- **N-way everything.** Memory, connections, caches, metrics counters, and log files all multiply by the worker count under the process model. Each one needs an explicit decision.
- **Extensions that are not free-threading-safe.** On the free-threaded build, importing an extension built without the free-threading ABI re-enables the GIL for the whole process with a warning, which silently returns you to the GIL build's performance.

## Worked Example

### A lost update through a switch point

Two threads credit a `Money` balance whose `__iadd__` is written in Python. The switch interval expires while thread A is inside `__iadd__`, which is a function entry and therefore a point where the eval loop checks for a pending GIL request.

| Step | Thread A | Thread B | `balance` | GIL held by |
|------|----------|----------|-----------|-------------|
| 1 | `LOAD_GLOBAL balance` (100.00) | | 100.00 | A |
| 2 | `LOAD_FAST amount` (25.00) | | 100.00 | A |
| 3 | `BINARY_OP +=` enters `Money.__iadd__` | | 100.00 | A |
| 4 | switch request pending; A releases at function entry | | 100.00 | released |
| 5 | | `LOAD_GLOBAL balance` (100.00) | 100.00 | B |
| 6 | | `LOAD_FAST amount` (25.00) | 100.00 | B |
| 7 | | `BINARY_OP +=` builds 125.00 | 100.00 | B |
| 8 | | `STORE_GLOBAL balance` | 125.00 | B |
| 9 | | returns; B blocks on a socket, releases | 125.00 | released |
| 10 | resumes inside `__iadd__`, builds 125.00 from the 100.00 it loaded | | 125.00 | A |
| 11 | `STORE_GLOBAL balance` | | 125.00 | A |

Two credits of 25.00, final balance 125.00. The GIL was held correctly at every instant; no interpreter structure was corrupted; the program is still wrong. Compare the table with Lesson 02's, which has the same shape with "GIL held by" replaced by "CPU core". The fix is the same lock:

```python
import threading

_lock = threading.Lock()

def credit(amount: Money) -> None:
    global balance
    with _lock:                 # read-modify-write under one lock, on every build
        balance += amount
```

On the GIL build this costs about a hundred nanoseconds per call and changes nothing observable until the day the interpreter's switch points move or the GIL is removed. On the free-threaded build it is the difference between a correct ledger and Lesson 02's reconciliation gap.

### The Luhn benchmark harness

The timings in the worked example above come from a harness like this one. It is intentionally pure Python so that every strategy pays the interpreter's cost:

```python
import sys, time
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

def luhn_ok(pan: str) -> bool:
    total, double = 0, False
    for ch in reversed(pan):
        d = ord(ch) - 48
        if double:
            d *= 2
            if d > 9:
                d -= 9
        total += d
        double = not double
    return total % 10 == 0

def count_valid(pans: list[str]) -> int:
    return sum(1 for p in pans if luhn_ok(p))

def run(label: str, pool_cls, pans: list[str], workers: int) -> None:
    chunks = [pans[i::workers] for i in range(workers)]
    start = time.perf_counter()
    with pool_cls(max_workers=workers) as pool:
        total = sum(pool.map(count_valid, chunks))
    print(f"{label:<28} {time.perf_counter() - start:6.2f} s  valid={total}")

if __name__ == "__main__":
    pans = [f"{n:016d}" for n in range(10_000_000)]
    gil = getattr(sys, "_is_gil_enabled", lambda: True)()
    print(f"python {sys.version.split()[0]}, GIL enabled: {gil}")
    run("serial", ThreadPoolExecutor, pans, 1)
    run("4 threads", ThreadPoolExecutor, pans, 4)
    run("4 processes", ProcessPoolExecutor, pans, 4)
```

Three details worth noticing. `pans[i::workers]` slices the input so every worker gets the same amount of work, which matters because the slowest worker sets the wall time. `ProcessPoolExecutor` has to pickle each chunk to its worker, which is 2.5 million strings per process and is part of the cost the process model pays. And `sys._is_gil_enabled` exists only on 3.13 and later, so the `getattr` default lets the same script run on older interpreters and label its output honestly.

### Deployment arithmetic for the process model

A pre-fork server such as Gunicorn multiplies every per-process resource by the worker count. Before choosing `workers` and `threads`, write the arithmetic down:

```text
cores                              = 16
workers (processes)                = 16        (one per core for CPU-bound Python)
threads per worker                 = 4         (for the I/O waits inside each request)

interpreter + code                 = 90 MB per worker
fraud feature tables               = 600 MB per worker   <- the number that hurts
connection pool                    = 8 connections per worker

memory                             = 16 * (90 + 600) MB  = 11.0 GB   (Java service: 1.4 GB)
Postgres connections               = 16 * 8              = 128        (Java service: 16)
rate-limiter dictionaries          = 16                               (Java service: 1)
idempotency caches                 = 16                               (Java service: 1)
```

Each line in the right-hand column is a design decision the JVM made implicitly. The feature tables can go into shared memory (`multiprocessing.shared_memory`, or a memory-mapped file the OS shares between processes). The connections go behind PgBouncer so 128 client connections become 16 server connections. The rate limiter and the idempotency cache move to Redis, or the service accepts that limits are per-worker and divides them by 16, which is wrong the moment a worker restarts. None of this is hard; all of it is work the Java service never needed, and the free-threaded build's appeal is that it makes the work unnecessary again.

### Checking which build you are on

```python
import sys, sysconfig

print(sys.version)
print("free-threaded build:", bool(sysconfig.get_config_var("Py_GIL_DISABLED")))
print("GIL enabled now:    ", getattr(sys, "_is_gil_enabled", lambda: True)())
```

The two questions are different. A free-threaded build can be running with the GIL re-enabled because an extension module without free-threading support was imported, in which case the second line says `True` and the service has silently lost its parallelism. Log both at startup.

## Exercises

1. **Reinforce.** Write the Luhn check from the worked example and time it four ways: serial, `ThreadPoolExecutor(4)`, `ProcessPoolExecutor(4)`, and, if you have a `python3.13t` or later free-threaded build, `ThreadPoolExecutor(4)` there. Record the wall time and the CPU utilisation for each and explain the thread row in one sentence.
2. **Apply.** Take Lesson 02's Python `RacyCounter` and run its stress test on the GIL build with `sys.setswitchinterval(1e-6)`, then with the default, then on a free-threaded build. Report how many lost updates each configuration produces in ten runs, and write a paragraph on what the three numbers do and do not prove, using Lesson 12's vocabulary.
3. **Extend.** Redesign the Problem's webhook service for a 16-process deployment: decide where the rate limiter, the idempotency store, the fraud-model tables, and the database pool live, and what the per-process memory and total connection count become. Then redo the design for a free-threaded single process and list every lock you would need. Connect the idempotency decision to Phase 12, Lesson 01.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| GIL | "Python's thread lock" | The mutex that allows one thread at a time to execute bytecode, protecting reference counts and interpreter internals; released during blocking calls and every switch interval |
| Switch interval | "the GIL timeout" | The time (default 5 ms) after which a running thread is asked to release the GIL so a waiting thread can run; checked at specific bytecode points, not between every instruction |
| Convoy effect | "threads make Python slower" | An I/O-bound thread waiting up to a full switch interval behind a CPU-bound thread each time it needs the GIL for a few microseconds of work |
| Releasing the GIL | "C extensions run in parallel" | A C function dropping the lock around a long computation or blocking call (`Py_BEGIN_ALLOW_THREADS`), so other Python threads run meanwhile |
| Free-threaded build | "no-GIL Python" | CPython built with `--disable-gil` (PEP 703): per-object locks, biased and deferred reference counting, immortal objects; parallel bytecode at a single-thread cost |
| Biased reference counting | "two refcounts" | Each object keeps a cheap owner-thread count and an atomic shared count, so objects touched by one thread pay nothing for thread safety |
| Pre-fork worker model | "run N workers" | N interpreter processes behind a socket; parallelism by isolation, with N copies of every in-process resource and no shared state |
| Subinterpreter | "a thread with its own GIL" | An isolated interpreter in the same process (PEP 684) with its own GIL and object space; shares the process but not Python objects |

## Further Reading

- [PEP 703: Making the Global Interpreter Lock Optional in CPython](https://peps.python.org/pep-0703/) — the design of the free-threaded build, with the biased reference counting, immortal objects, and per-object locking described in this lesson and the measured overheads.
- [Python HOWTO: Free-threaded CPython](https://docs.python.org/3/howto/free-threading-python.html) — how to obtain and run the build, `PYTHON_GIL`, `sys._is_gil_enabled()`, and which standard-library operations are thread-safe.
- [PEP 684: A Per-Interpreter GIL](https://peps.python.org/pep-0684/) and [PEP 734: Multiple Interpreters in the Stdlib](https://peps.python.org/pep-0734/) — the subinterpreter route to parallelism and its sharing restrictions.
- [Python `threading` module documentation](https://docs.python.org/3/library/threading.html) — `Lock`, `RLock`, and the `sys.setswitchinterval` reference, with the note on which operations the GIL does not make atomic.
- [David Beazley, "Understanding the Python GIL" (PyCon 2010)](https://www.dabeaz.com/python/UnderstandingGIL.pdf) — the measurements behind the convoy effect and the motivation for the 3.2 "new GIL".
- [Choi, Shull, Torrellas, "Biased Reference Counting: Minimizing Atomic Operations in Garbage Collection" (PACT 2018)](https://dl.acm.org/doi/10.1145/3243176.3243195) — the paper the free-threaded build's reference counting is based on.
- [Python `multiprocessing` documentation: contexts and start methods](https://docs.python.org/3/library/multiprocessing.html#contexts-and-start-methods) — `fork` versus `spawn` versus `forkserver`, and why the default changed.
