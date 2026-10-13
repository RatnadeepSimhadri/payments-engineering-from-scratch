---
name: interview-card-ssi
description: One-page explanation of serializable snapshot isolation for design reviews and interviews, with the write-skew trace and the questions interviewers ask
phase: 7
lesson: 7
tags: [ssi, serializable, isolation, postgresql, interview]
---

# Interview Card: Serializable Snapshot Isolation

## The thirty-second version

Snapshot isolation gives every transaction a frozen view and lets writers through unless two of them write the same key. It still allows write skew: two transactions each read what the other writes and commit disjoint, jointly inconsistent results. SSI keeps SI exactly as it is and adds bookkeeping: record every read (a SIREAD lock that blocks nothing), record every read-write antidependency between concurrent transactions, and abort a transaction that has both an incoming and an outgoing one. That shape, the dangerous structure, appears in every non-serializable SI history, so aborting on it is sufficient. Nothing ever waits.

## The three edges

| Edge | Meaning | Between concurrent SI transactions? |
|------|---------|-------------------------------------|
| T1 -wr-> T2 | T2 read what T1 wrote | No (snapshots hide it) |
| T1 -ww-> T2 | T2 overwrote what T1 wrote | No (first committer wins) |
| T1 -rw-> T2 | T1 read the old version, T2 wrote the new one | Yes: the only one |

## The dangerous structure

```text
T_in  -rw->  T_pivot  -rw->  T_out        (all three pairwise concurrent)
```

The pivot has an in-edge and an out-edge. Abort it at commit; if it already committed, abort whichever live transaction completes the structure.

## The write-skew trace (joint accounts A = B = 100, rule A + B >= 0)

```text
T1 reads A, B (SIREAD A, B)     T2 reads A, B (SIREAD A, B)
T1 writes A = -50               -> T2 read A: edge T2 -rw-> T1
                                T2 writes B = -50  -> T1 read B: edge T1 -rw-> T2
T1 commits: in={T2}, out={T2}   -> pivot, ABORT; remove T1's edges
                                T2 commits: in={}, out={}  -> OK.   A + B = 50
```

## Two detection points

1. **On read** of key k: for every version newer than my snapshot (committed, or buffered in an active writer W), record me -rw-> W. If W committed and already has an out-edge, I am aborted now.
2. **On write** of key k: for every SIREAD holder R concurrent with me, record R -rw-> me. If R committed and already has an in-edge, I am aborted now.
3. **On commit:** first-committer-wins, then if I have both edge kinds, abort me.

## Five questions interviewers ask

1. *Why not just lock?* Serializable 2PL makes every reader a blocker; a long report stalls payments. SSI readers never block and are never blocked.
2. *Does SSI ever abort a correct transaction?* Yes: it detects a structure, not a cycle, so there are false positives. PostgreSQL reduces them with the commit-order condition (T_out must commit first) and safe snapshots for read-only transactions.
3. *Why keep locks after commit?* A transaction that began before I committed can still write what I read; that edge is real and I might be its T_in. Locks are released when no concurrent transaction remains.
4. *What does the application have to do?* Retry on SQLSTATE `40001` with fresh reads, bounded attempts, jittered backoff, and no external side effects inside the retried body.
5. *What is the operational trap?* A sequential scan takes a relation-level SIREAD and conflicts with every writer to that table; a long transaction pins every SIREAD taken since it began. Give reports an index path or `READ ONLY DEFERRABLE`.

## When to pick what

| Rule shape | Pick |
|------------|------|
| Per-entity constraint with a natural key | Materialised constraint row under SI |
| Few rows, high value, low contention | `SELECT ... FOR UPDATE` |
| Constraints spanning many rows or many transaction types | `SERIALIZABLE` (SSI) with a retry loop |
| Long read-only reports | `SERIALIZABLE READ ONLY DEFERRABLE` or a replica |
