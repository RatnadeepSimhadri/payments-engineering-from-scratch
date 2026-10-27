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

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you; the names below are the Java ones in snake_case.

```python
class RaftNode:
    def advance_commit_index(self, now: int) -> None: ...          # highest N > commit_index with term_at(N) == current_term and a majority
    def apply_committed(self, now: int) -> None: ...               # while last_applied < commit_index: apply log[last_applied].command
    # upgraded: on_append_reply calls advance_commit_index; on_append_entries applies rule 5 (min(leader_commit, last_new))

class KVStore:                 # fields: data (dict), applied_commands, results
    def apply(self, command: str) -> str: ...                      # PUT -> "OK"; GET -> value or ""; NOOP -> ""; malformed -> ValueError, not recorded
    def get(self, key: str) -> str | None; applied() -> int        # given

class Checker:
    def check(self, history: History) -> CheckResult: ...         # CheckResult(ok, order: tuple[int, ...], longest_prefix: tuple[int, ...], explored)
    @staticmethod
    def step(state: dict[str, str], op: Op) -> dict[str, str] | None: ...   # PUT -> a new dict; GET -> the same `state` object, or None

# Provided: Op(id, client, kind: OpKind, key, value, invoked_at, returned_at) with precedes(other); History().put(...).get(...),
# ops() / size() / op(id); Cluster.submit(client, command) -> op id, submit_until_accepted, run_until_settled, run_until_applied,
# pending_count(), completed (list[Op]), failed (list[ClientOp]), history(), state_machines_converged().
```
