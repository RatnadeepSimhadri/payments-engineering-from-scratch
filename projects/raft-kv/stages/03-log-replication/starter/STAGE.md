# Stage 03 starter

Continue editing the workspace created at stage 1. There is nothing to copy: implement the replication half of
`RaftNode.java` and upgrade two stage-2 methods. `Storage.java` and the stage-2 election code must keep passing; the
grader runs stages 1 and 2 again before stage 3.

## Signatures to implement (all in `RaftNode.java`)

| Method | Does |
|---|---|
| `int propose(String command)` | not leader -> `-1`; else `storage.append(new LogEntry(currentTerm, command))`, return `lastLogIndex()` |
| `Message appendEntriesFor(int peer)` | `next = nextIndex[peer]`, `prev = next - 1`; `Message.appendEntries(id, peer, currentTerm, prev, storage.termAt(prev), storage.entriesFrom(next), commitIndex)` |
| `List<Message> onAppendReply(Message m, int now)` | leader and same term only; success -> `matchIndex`/`nextIndex` forward (never backward); failure -> `nextIndex = max(1, nextIndex - 1)` and resend `appendEntriesFor(from)` |
| `int replicatedCount(int index)` | `1 +` number of peers whose `matchIndex >= index` |

## Methods to upgrade

| Method | Change |
|---|---|
| `List<Message> becomeLeader(int now)` | after initialising `nextIndex`/`matchIndex`, `storage.append(new LogEntry(currentTerm, "NOOP"))`, then `replicateAll(now)` |
| `List<Message> replicateAll(int now)` | `nextHeartbeat = now + interval`; return `appendEntriesFor(p)` for every peer (an empty `entries` list is the heartbeat) |
| `List<Message> onAppendEntries(Message m, int now)` | after the stage-2 bookkeeping add the consistency check (rule 2), truncate-on-conflict (rule 3), append-if-new (rule 4), and reply `true` with `matchIndex = prevLogIndex + entries.size()` |

`advanceCommitIndex` and `applyCommitted` stay stubbed; `commitIndex` remains 0 throughout this stage. Read `docs/en.md`
for the worked traces, the receiver loop, and the failure table.
