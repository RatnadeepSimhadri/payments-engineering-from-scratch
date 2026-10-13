# Stage 02 starter

Continue editing the workspace created at stage 1 (`--init`). There is nothing to copy: fill in the election half of
`RaftNode.java`. `Sim.java` and `Cluster.java` stay as provided; `Storage.java` from stage 1 must not change shape,
because the grader runs the stage 1 tests again before the stage 2 tests.

## Signatures to implement (all in `RaftNode.java`, all already stubbed)

| Method | Does |
|---|---|
| `RaftNode(int id, List<Integer> peers, Storage storage, Rng rng, Config config, int now, Tracer tracer)` | the constructor already calls `resetElectionTimer(now)`; keep it that way |
| `void resetElectionTimer(int now)` | `electionDeadline = now + min + rng.nextInt(max - min + 1)` from this node's own `Rng` |
| `List<Message> tick(int now)` | leader: `replicateAll(now)` when `now >= nextHeartbeat`; others: `startElection(now)` when `now >= electionDeadline`; else empty |
| `List<Message> startElection(int now)` | candidate, `save(term + 1, id)`, votes `{id}`, `electionsStarted++`, timer reset, one `requestVote` per peer |
| `List<Message> handle(Message m, int now)` | higher term -> `becomeFollower`; then dispatch on `m.kind()` |
| `void becomeFollower(long term, int now)` | role FOLLOWER, `leaderId = -1`, votes cleared; `save(term, -1)` only if `term > currentTerm`; re-arm timer if it was leader |
| `List<Message> onRequestVote(Message m, int now)` | Figure 2 receiver rules; persist the vote, reset the timer, then reply |
| `List<Message> onVoteReply(Message m, int now)` | count only `role == CANDIDATE && m.term() == currentTerm`; majority -> `becomeLeader(now)` |
| `List<Message> becomeLeader(int now)` | role LEADER, `leaderId = id`, `nextIndex[p] = lastLogIndex() + 1`, `matchIndex[p] = 0`, return `replicateAll(now)` |
| `List<Message> replicateAll(int now)` | `nextHeartbeat = now + heartbeatInterval`; one `appendEntries(id, p, term, lastLogIndex(), lastLogTerm(), List.of(), commitIndex)` per peer |
| `List<Message> onAppendEntries(Message m, int now)` | stage 2 subset: lower term -> `appendReply(..., false, 0)`; leader in same term -> `IllegalStateException`; candidate steps down; `leaderId = m.from()`, timer reset, `appendReply(..., true, m.prevLogIndex())` |

`onAppendReply` may return an empty list for now; `propose`, `appendEntriesFor`, `replicatedCount`, `advanceCommitIndex`,
and `applyCommitted` stay stubbed until stages 3 and 4. Read `docs/en.md` for the worked trace and the exact rules.
