# Stage 04 starter

Continue editing the workspace created at stage 1. There is nothing to copy: finish `RaftNode.java`, implement
`KVStore.apply` in `KVStore.java`, and implement `Checker` in `Linearizability.java` (`Op`, `History`, and
`CheckResult` there are provided and must not change). Stages 1 to 3 run again first.

## Signatures to implement

| Where | Method | Does |
|---|---|---|
| `RaftNode` | `void advanceCommitIndex(int now)` | leader only; highest `N > commitIndex` with `termAt(N) == currentTerm` and `replicatedCount(N) >= majority()`; set `commitIndex = N`, `applyCommitted(now)` |
| `RaftNode` | `void applyCommitted(int now)` | `while (lastApplied < commitIndex) { lastApplied++; stateMachine.apply(log[lastApplied].command()); }` |
| `KVStore` | `String apply(String command)` | `PUT k v` -> `"OK"`; `GET k` -> value or `""`; `NOOP` -> `""`; malformed or unknown -> `IllegalArgumentException` before recording; append to `appliedCommands` and `results` |
| `Checker` | `CheckResult check(History history)` | Wing-Gong search with memo; `ok`, witness `order`, `longestPrefix`, `explored` |
| `Checker` | `static Map<String, String> step(Map<String, String> state, Op op)` | `PUT` -> new map with the write; `GET` -> `state` if `state.getOrDefault(key, "")` equals `op.value()`, else `null` |

## Methods to upgrade

| Method | Change |
|---|---|
| `onAppendReply` | after advancing `matchIndex`/`nextIndex`, call `advanceCommitIndex(now)` |
| `onAppendEntries` | rule 5, after the entry loop: `if (m.leaderCommit() > commitIndex) { commitIndex = Math.min(m.leaderCommit(), lastNew); applyCommitted(now); }` |

The harness's `Cluster.submit(client, command)`, `runUntilSettled`, `completed`, `failed`, and `history()` are already
there; the tests drive them. Read `docs/en.md` for the Figure 8 trace, the client history, and the checker outline.
