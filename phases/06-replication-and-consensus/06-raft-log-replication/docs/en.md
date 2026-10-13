# Raft II: Log Replication and Commit

> AppendEntries, matchIndex, and the Log Matching property.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 6, Lesson 05 (Raft I: Terms, Leader Election, and a Simulated Network); Phase 6, Lesson 03 (Quorums: R + W > N and Its Limits)
**Time:** ~150 minutes

## Learning Objectives

- Implement the AppendEntries RPC exactly as Figure 2 of the Raft paper specifies it, including the `prevLogIndex`/`prevLogTerm` consistency check and the conflict-truncation rule
- Maintain `nextIndex` and `matchIndex` on the leader and use a majority of `matchIndex` to advance `commitIndex`
- State the Log Matching property and explain why the consistency check makes it hold by induction
- Explain why a leader may only commit entries from its current term by counting replicas, and reproduce the paper's Figure 8 scenario in a test
- Separate the persistent log from the volatile `commitIndex`, `lastApplied`, and state machine, and rebuild the volatile state after a restart
- Apply committed entries to a key-value state machine on every node and show by test that the state machines converge after partitions, crashes, and message loss

## The Problem

Lesson 05 ended with a cluster that reliably elects one leader per term. The leader does nothing. A client that wants to record a payment has nowhere to put it, and a leader that could accept a write and then crash would lose it just as surely as the single-primary design in Lesson 05's incident.

Consider the obvious extension. The leader appends each client command to its own log, forwards it to the followers, and replies to the client once it has heard back from a majority. Now run that design through the failure that Raft is built for. Five nodes hold a ledger of balance updates. The leader `n4` accepts `SET alice 999` and `SET carol 1`, writes them to its log, and sends them out. A switch fails and `n4` ends up on the minority side of a partition with one follower, `n0`. The two writes reach `n0`; `n4` now has two copies and waits for a third. On the majority side `n3` wins an election for term 2, accepts `SET dave 7`, and commits it with three replicas. Forty seconds later the partition heals. `n0` and `n4` each hold two entries at indexes 5 and 6 that `n1`, `n2`, and `n3` have never seen; `n3` holds a different entry at index 5 that is already applied to three state machines. Which log is right, and what is `alice`'s balance?

Without a rule, each design answers differently. "Longest log wins" would pick `n4`'s and roll back a committed write. "Newest leader wins" is correct here but needs a precise statement of what the new leader may overwrite and what it must keep. "Merge them" is how you end up with a ledger that contains both `alice=999` and `dave=7` in an order no client ever saw. The property you actually need is stronger than any of these: an entry that was ever acknowledged as committed must be in every future leader's log at the same index, and an entry that was never committed may be discarded. Those two sentences are the Log Matching property and State Machine Safety from the paper's Figure 3, and they are what this lesson builds and tests.

The payments reading is direct. A ledger posting that a service confirmed to a customer is committed; it must survive any sequence of crashes and partitions that leaves a majority alive. A posting that was accepted by a stale leader and never reached a majority never happened, and the system must say so consistently on every replica, otherwise the reconciliation job in Phase 10 finds a balance that exists on two nodes and not on three. This lesson adds the log to Lesson 05's node, keeps the simulated network unchanged so every scenario above is a seed, and checks all four of Figure 3's properties after every message delivery.

## The Concept

### The log is the state machine's input tape

A Raft cluster is a replicated state machine (Schneider, 1990): every node runs the same deterministic machine, every node feeds it the same sequence of commands, and so every node arrives at the same state. The log is the sequence. Each entry holds a client command and the term of the leader that created it, and is identified by its index, its position in the log. The state machine here is a key-value store with `SET k v`, `DEL k`, and `NOOP`; in Phase 10 it will be a ledger.

Three positions in the log matter:

| Name | Owner | Persistent? | Meaning |
|------|-------|-------------|---------|
| `lastLogIndex` | every node | yes (it is the log's length) | the last entry this node holds, committed or not |
| `commitIndex` | every node | no | the highest entry known to be stored on a majority; everything up to it is durable |
| `lastApplied` | every node | no | the highest entry fed to the state machine; trails `commitIndex` by at most one apply loop |

The paper's Figure 2 lists `currentTerm`, `votedFor`, and `log[]` as persistent and everything else as volatile. The split is not an optimisation. The log is the record of what was agreed; `commitIndex` is a cached conclusion that can be recomputed from the logs of a majority, so a node that restarts with `commitIndex = 0` and an empty state machine is not wrong, merely behind. It learns the commit index from the leader's next AppendEntries and re-applies. The demo shows a follower doing exactly this.

### Normal operation

```mermaid
sequenceDiagram
    participant C as Client
    participant L as Leader n4 (term 1)
    participant F1 as Follower n0
    participant F2 as Follower n1
    C->>L: SET alice 100
    L->>L: append (index 2, term 1) to own log
    L->>F1: AppendEntries(prev=(1,t1), [t1:SET alice 100], leaderCommit=1)
    L->>F2: AppendEntries(prev=(1,t1), [t1:SET alice 100], leaderCommit=1)
    F1->>F1: prev matches -> append, reply match=2
    F1-->>L: success, matchIndex=2
    L->>L: matchIndex[n0]=2; replicas of index 2 = {n4, n0} = 2 of 5
    F2-->>L: success, matchIndex=2
    L->>L: 3 of 5 -> commitIndex=2, apply SET alice 100
    L-->>C: ok
    L->>F1: next AppendEntries carries leaderCommit=2 -> F1 applies
```

The leader appends to its own log first, sends one AppendEntries per follower, and counts the follower's own log as one replica. The client gets an answer after the entry is on a majority, not after every follower has it: a slow or dead follower cannot stall the cluster. Followers learn that an entry is committed one round trip later, through the `leaderCommit` field on the next message, which in a quiet cluster is the next heartbeat.

### AppendEntries, receiver side, verbatim

Figure 2 gives the follower's rules as five numbered lines. The implementation runs them in order and nothing else:

1. Reply false if `term < currentTerm`.
2. Reply false if the log does not contain an entry at `prevLogIndex` whose term matches `prevLogTerm`.
3. If an existing entry conflicts with a new one (same index, different term), delete the existing entry and all that follow it.
4. Append any new entries not already in the log.
5. If `leaderCommit > commitIndex`, set `commitIndex = min(leaderCommit, index of last new entry)`.

Rule 2 is the consistency check and the whole lesson turns on it. Every AppendEntries names the entry immediately before the ones it carries, by index and term. If the follower does not have exactly that entry, it refuses the whole message. The leader then decrements `nextIndex` for that follower and tries again one entry earlier, and keeps going until it finds the last index where the two logs agree. From that point on the leader's entries overwrite the follower's.

Rule 3 is phrased carefully: delete from the first conflict onward, not from `prevLogIndex` onward. A heartbeat or a retransmission can arrive with a `prevLogIndex` that is behind entries the follower already holds and that match the leader's. Rule 4 says those are left alone. Truncating on every message would make a reordered duplicate destroy committed entries; the test `heartbeat with an older prefix does not truncate newer matching entries` is the regression for that bug.

Rule 5's `min` matters too. A follower that has just caught up to index 3 may receive `leaderCommit = 10`; it may only commit what it actually holds. Committing to 10 would mean applying entries it does not have, and the apply loop would read past the end of the log.

### The Log Matching property

> **Log Matching:** if two logs contain an entry with the same index and term, then the logs are identical in all entries up through the given index. (Figure 3)

This is two claims. First, a given index and term identify at most one command: a leader creates at most one entry per index in its term and never moves it, so `(index, term)` is a globally unique id for a command. Second, if two logs agree at `(index, term)` they agree on everything before it. The proof is induction on the consistency check. A log starts empty (both logs hold the sentinel at index 0, term 0, so `prevLogIndex = 0` always matches). Each AppendEntries that succeeds extends a follower's log only at a point where the follower already matches the leader's previous entry, which by the induction hypothesis means it matches everything before that too. A follower's log is therefore always a prefix of the leader's up to the last accepted entry, and two followers that agree at an index both match the leader there.

The invariant is what makes the leader's bookkeeping sound. When a follower replies `success, matchIndex = k`, the leader can conclude not just that the follower holds entry `k` but that it holds the leader's entries `1..k`. That is why a majority of `matchIndex >= N` means entry `N` and everything before it is on a majority.

The simulation checks the property after every delivery for every pair of nodes: find the highest index where the two logs have the same term, then assert they are identical below it. It is deliberately a pairwise, from-the-top check and not "the logs are equal", because logs are allowed to diverge above the last common entry (an uncommitted tail on a partitioned node). The checker's own tests confirm it fires on a planted violation and stays quiet on a legal divergence.

### Commit: a majority of matchIndex, and the current-term rule

Figure 2's last rule for leaders:

> If there exists an N such that N > commitIndex, a majority of matchIndex[i] >= N, and log[N].term == currentTerm: set commitIndex = N.

The first two conditions are what anyone would write. The third is the subtle one and is the subject of the paper's section 5.4.2 and Figure 8. Reproduced with this lesson's numbers:

```text
Five nodes. S1 leads term 2, replicates index 2 (term 2) to S2 only, then crashes.
S5 wins term 3 with votes from S3, S4 (their logs end at index 1, S5's log is as long), appends index 2 (term 3) locally, crashes.
S1 restarts, wins term 4, continues replicating its term-2 entry at index 2 and reaches S3: now on S1, S2, S3 = a majority.
  If S1 commits index 2 now (counting replicas of a term-2 entry while leading term 4)...
S1 crashes. S5 can win term 5 with votes from S2, S3, S4: its last entry (index 2, term 3) is newer than theirs (term 2).
S5 replicates its term-3 entry at index 2 to everyone, overwriting the "committed" term-2 entry.
```

A committed entry was lost. The fix is the third condition: a leader counts replicas only for entries from its own term. Older entries become committed indirectly, through the Log Matching property, the moment a current-term entry after them commits. In the scenario, S1 in term 4 would not commit index 2 until it had replicated a term-4 entry at index 3 to a majority; at that point S5 could no longer win an election, because a majority would have a term-4 last entry.

That is also why a new leader appends a `NOOP` the moment it wins (section 8 of the paper, and what etcd and every other production implementation do). Without it, entries from previous terms stay uncommitted until the next client write, and a read-only cluster could hold acknowledged-but-not-yet-committed entries indefinitely. The demo's fourth section is this exact case: a term-5 leader holds two term-2 entries replicated on three of five nodes and `commitIndex` stays at 0 until its own term-5 `NOOP` reaches three nodes, at which point all three entries commit together.

### Worked example: the partition, by hand

The demo's second section, seed 7, five nodes, delay 1-5 ticks. Everything below is copied from the trace.

```text
t=241  all five logs: [1:t1 NOOP, 2:t1 SET alice 100, 3:t1 SET bob 50, 4:t1 SET alice 75], commitIndex 4 everywhere
       partition {n0, n4} | {n1, n2, n3};  n4 is leader of term 1
       client sends SET alice 999 and SET carol 1 to n4 -> n4 appends indexes 5, 6 (term 1), replicates
       n0 accepts both (its prev (4, t1) matches). matchIndex[n0]=6. Replicas of index 5: {n4, n0} = 2 of 5. Not committed.
t=420  n3 times out, candidate for term 2. RequestVote carries last (4, t1). n1, n2 have last (4, t1): equal -> grant.
t=429  n3 LEADER of term 2, appends 5:t2 NOOP, replicates with prev (4, t1)
t=438  n1, n2 accept -> matchIndex 5 on both -> 3 of 5 and term 2 == currentTerm -> n3 commitIndex = 5
       Two leaders exist. n4's log is longer (6 entries). n3's is the one with a committed index 5.
t=841  heal
t=880  n3's heartbeat reaches n0. nextIndex[n0] on n3 is still 5 (set at its election, never advanced), so prev is (4, t1).
       n0 has (4, t1): rule 2 passes. Entry at index 5: n0 has t1, leader sends t2 -> rule 3: truncate from 5 (2 entries). Append 5:t2 NOOP.
       n0 replies success, matchIndex 5. Its log is now n3's log.
t=882  same for n4, which first steps down ("sees term 2 -> follower (was LEADER)") because the message's term is higher.
       client sends SET dave 7 -> index 6, term 2 -> committed at t=886 with 3 of 5.
final  all five logs: [..., 5:t2 NOOP, 6:t2 SET dave 7]; alice = 75 on every state machine. The 999 write never happened.
```

Three things to notice. The election rule from Lesson 05 ("at least as up-to-date") is what allowed `n3` to win: `n4` was not reachable, and among the majority every log ended at `(4, t1)`. Had `n0` been on the majority side with its two extra term-1 entries, it could still not have blocked `n3`, because up-to-date-ness compares last terms first and lengths second, and `n0`'s extra entries were term 1. And the uncommitted writes were discarded without any special recovery logic; rule 3 did it as a side effect of normal replication.

### Complexity and cost

| Quantity | Value | Why it matters |
|----------|-------|----------------|
| Messages per client write | `N-1` AppendEntries plus `N-1` replies, usually batched with heartbeats | A 5-node cluster at 10,000 writes/s sends 80,000 messages/s unless entries are batched per heartbeat interval |
| Commit latency | one round trip to the nearest majority, plus the leader's own persistent append | The slowest of the fastest `N/2` followers decides, not the slowest follower |
| Catch-up for a follower `k` entries behind | up to `k` rejected AppendEntries with the naive decrement, `O(1)` with the conflict-term hint of section 5.3 | Exercise 2 implements the hint; the naive version is correct but slow after a long outage |
| Persistent writes per entry per node | one append before the reply | Every follower fsyncs before acknowledging; the leader's commit waits for a majority of those fsyncs |
| Log Matching checker in the simulation | `O(N^2 * L)` per delivery, `L` the log length | Fine for tests at `N=5, L<50`; it is a test oracle, not part of the protocol |
| Space | the log grows without bound | Snapshotting and log compaction are Lesson 07 |

### What this lesson does not do

- **No snapshots or log compaction.** Logs grow forever; a restarted node replays from index 1. Lesson 07.
- **No membership changes.** `majority()` is `N/2 + 1` of a fixed peer list. Lesson 07.
- **No client sessions or exactly-once semantics.** A client whose request was appended but whose reply was lost will resubmit, and the log will contain the command twice. Section 8 of the paper handles this with client serial numbers; Phase 12, Lesson 01 handles the same problem at the API layer with idempotency keys.
- **No read path.** A linearizable read in Raft requires the leader to confirm it is still leader (a heartbeat round or a lease) before answering from its state machine, otherwise a deposed leader serves stale data. Lesson 12 (Linearizability Checking) builds the tool that catches that bug.
- **Naive `nextIndex` back-off.** On rejection the leader steps back one index per round trip. Correct, and the paper's own first description, but slow for a follower that missed thousands of entries. Exercise 2.

## Build It

The primary walkthrough is in Java. The Python and Go files implement the same design with the same names; the subsections at the end show only what differs. The `Rng`, `Network`, `Config`, and election code are Lesson 05's, copied into this lesson so it stays self-contained; this walkthrough covers only what the log adds.

### Step 1: Log entries and the AppendEntries message

```java
record LogEntry(long term, String command) {
    @Override public String toString() { return "t" + term + ":" + command; }
}

record Message(MessageKind kind, int from, int to, long term, int prevLogIndex, long prevLogTerm,
               List<LogEntry> entries, int leaderCommit, boolean success, int matchIndex) {
    static Message appendEntries(int from, int to, long term, int prevLogIndex, long prevLogTerm,
                                 List<LogEntry> entries, int leaderCommit) {
        return new Message(MessageKind.APPEND_ENTRIES, from, to, term, prevLogIndex, prevLogTerm,
                List.copyOf(entries), leaderCommit, false, 0);
    }

    static Message appendReply(int from, int to, long term, boolean success, int matchIndex) {
        return new Message(MessageKind.APPEND_ENTRIES_REPLY, from, to, term, 0, 0, List.of(), 0, success, matchIndex);
    }
}
```

An entry does not store its own index; the index is its position in the log, which is what makes "delete this entry and everything after it" a truncation rather than a search. One `Message` record carries all four RPCs with the paper's field names. `List.copyOf` matters: a follower's `entries` must be a snapshot of the leader's log at send time, not a view of a list that the leader will keep appending to while the message sits in the network queue.

The reply carries `matchIndex` rather than leaving the leader to infer it. The paper describes the leader remembering what it sent; carrying the follower's new last-matching index on the reply is equivalent, simpler, and robust to reordered replies, and is what etcd does.

**Predict:** why does `appendReply` on failure carry `matchIndex = 0` rather than the follower's last index?

### Step 2: Persistent storage, now with a log

```java
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    final List<LogEntry> log = new ArrayList<>(List.of(new LogEntry(0, "<sentinel>")));
    int writes = 0;

    void save(long term, int votedFor) { this.currentTerm = term; this.votedFor = votedFor; writes++; }
    void append(LogEntry e) { log.add(e); writes++; }
    void truncate(int fromIndex) { while (log.size() > fromIndex) log.remove(log.size() - 1); writes++; }
    int lastIndex() { return log.size() - 1; }
    long termAt(int index) { return log.get(index).term(); }
}
```

The log is 1-indexed as in the paper, and slot 0 holds a sentinel with term 0. The sentinel removes every special case: `prevLogIndex = 0` always matches, a new leader's `nextIndex` for an empty cluster is 1, and `termAt(prev)` never needs a bounds check for `prev >= 0`. `Storage` survives `Cluster.crash`; the `RaftNode` object that owned it is thrown away and a new one is built around the same `Storage` on restart. Everything that is not in `Storage` is, by construction, volatile.

### Step 3: The state machine

```java
final class KVStore {
    final Map<String, String> data = new TreeMap<>();
    final List<String> appliedCommands = new ArrayList<>();

    void apply(String command) {
        String[] parts = command.split(" ");
        switch (parts[0]) {
            case "SET" -> { if (parts.length != 3) throw new IllegalArgumentException("SET needs key and value: " + command); data.put(parts[1], parts[2]); }
            case "DEL" -> { if (parts.length != 2) throw new IllegalArgumentException("DEL needs key: " + command); data.remove(parts[1]); }
            case "NOOP" -> {}
            default -> throw new IllegalArgumentException("unknown command: " + command);
        }
        appliedCommands.add(command);
    }
}
```

The machine is deterministic and knows nothing about Raft. `appliedCommands` exists so tests can assert the exact order of application, which is the real definition of convergence: not "the maps are equal at the end" but "every node applied the same commands in the same order". A `TreeMap` makes `toString` deterministic across nodes and languages.

**Predict:** a malformed command reaches `apply` on one node. What happens to the cluster, and is it right that the node stops rather than skipping the entry?

### Step 4: Leader state and the election's one change

```java
// Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
int commitIndex = 0;
int lastApplied = 0;
final KVStore stateMachine = new KVStore();
// Volatile state on leaders, reinitialised after election.
final Map<Integer, Integer> nextIndex = new HashMap<>();
final Map<Integer, Integer> matchIndex = new HashMap<>();

List<Message> becomeLeader(int now) {
    role = Role.LEADER;
    leaderId = id;
    for (int p : peers) { nextIndex.put(p, lastLogIndex() + 1); matchIndex.put(p, 0); }
    storage.append(new LogEntry(storage.currentTerm, "NOOP"));
    return replicateAll(now);
}
```

`nextIndex[p]` is the leader's optimistic guess at the next entry `p` needs, initialised to the leader's own length plus one on the assumption that followers are caught up; the consistency check corrects it downward. `matchIndex[p]` is the leader's proven knowledge of how far `p` matches, initialised to 0 and only ever increased by a successful reply. The two move in opposite directions toward each other, and the gap between them is the follower's lag.

The `NOOP` append is the section 8 fix discussed in The Concept. The other change from Lesson 05 is in `onRequestVote`, where `lastLogIndex()` and `lastLogTerm()` now read the real log instead of placeholder fields, so the "at least as up-to-date" rule finally has teeth.

### Step 5: Proposing and replicating

```java
int propose(String command) {
    if (role != Role.LEADER) return -1;
    storage.append(new LogEntry(storage.currentTerm, command));
    return lastLogIndex();
}

List<Message> replicateAll(int now) {
    nextHeartbeat = now + config.heartbeatInterval();
    List<Message> out = new ArrayList<>();
    for (int p : peers) out.add(appendEntriesFor(p));
    return out;
}

Message appendEntriesFor(int peer) {
    int next = nextIndex.get(peer);
    int prev = next - 1;
    List<LogEntry> entries = new ArrayList<>(storage.log.subList(next, storage.log.size()));
    return Message.appendEntries(id, peer, storage.currentTerm, prev, storage.termAt(prev), entries, commitIndex);
}
```

A follower refuses `propose` with `-1`; a real system would redirect to `leaderId`. The leader does not wait for anything before returning the index: the entry is appended, not committed, and the index is a promise that if this entry ever commits it will be at that position. There is one AppendEntries shape for everything: it carries every entry from `nextIndex` onward, which for a caught-up follower is nothing, and an AppendEntries with no entries is what the paper calls a heartbeat. The heartbeat timer is reset on every `replicateAll`, so a busy leader never sends a separate heartbeat.

**Predict:** `nextIndex[p]` is 3 and the leader's log has 6 entries. How many entries does the message carry, and what is `prevLogTerm`?

### Step 6: The receiver, rules 1 to 5

```java
List<Message> onAppendEntries(Message m, int now) {
    if (m.term() < storage.currentTerm) {                                                    // 1
        return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
    }
    if (role == Role.LEADER) throw new IllegalStateException("two leaders in term " + storage.currentTerm);
    if (role == Role.CANDIDATE) { role = Role.FOLLOWER; votesGranted.clear(); }
    leaderId = m.from();
    resetElectionTimer(now);
    if (m.prevLogIndex() > lastLogIndex() || storage.termAt(m.prevLogIndex()) != m.prevLogTerm()) {   // 2
        consistencyRejections++;
        return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
    }
    int index = m.prevLogIndex();
    for (LogEntry e : m.entries()) {
        index++;
        if (index <= lastLogIndex()) {
            if (storage.termAt(index) == e.term()) continue;                                 // 4: already have it
            storage.truncate(index);                                                        // 3
        }
        storage.append(e);                                                                  // 4
    }
    int lastNew = m.prevLogIndex() + m.entries().size();
    if (m.leaderCommit() > commitIndex) {                                                   // 5
        commitIndex = Math.min(m.leaderCommit(), lastNew);
        applyCommitted(now);
    }
    return List.of(Message.appendReply(id, m.from(), storage.currentTerm, true, lastNew));
}
```

Read it against the five rules in The Concept; the numbers in the comments are the paper's. Two details are easy to miss. The election timer is reset after rule 1 and before rule 2: a message from the legitimate current leader that fails the consistency check is still proof that a leader exists, and the follower must not start an election because its log happens to be behind. And the loop implements rules 3 and 4 together, per entry: an entry that is already present with the same term is skipped, the first one that differs triggers a truncation from that index, and everything from there is appended. `IllegalStateException` for a leader receiving a same-term AppendEntries is an Election Safety violation turned into a crash, so a bug in the election code fails loudly rather than corrupting a log.

**Predict:** the follower holds `[t1, t1, t2]` and receives `prev = (1, t1), entries = [t3, t3]`. What is the log afterwards, and how many entries were truncated?

### Step 7: The leader's reaction to replies

```java
List<Message> onAppendReply(Message m, int now) {
    if (role != Role.LEADER || m.term() != storage.currentTerm) return List.of();
    if (m.success()) {
        if (m.matchIndex() > matchIndex.get(m.from())) {
            matchIndex.put(m.from(), m.matchIndex());
            nextIndex.put(m.from(), m.matchIndex() + 1);
            advanceCommitIndex(now);
        }
        return List.of();
    }
    nextIndex.put(m.from(), Math.max(1, nextIndex.get(m.from()) - 1));
    return List.of(appendEntriesFor(m.from()));
}
```

Replies from an older term are discarded: they describe a conversation this leader is no longer having. `matchIndex` only moves forward, because a reordered older reply must not undo knowledge from a newer one; the test `an older matchIndex never moves commitIndex backwards` covers it. On a rejection, the leader steps `nextIndex` back one and resends immediately rather than waiting for the next heartbeat, so a follower catches up at network speed. The floor at 1 is the sentinel doing its job: `prevLogIndex = 0, prevLogTerm = 0` always matches, so the retry loop terminates.

### Step 8: Advancing the commit index

```java
void advanceCommitIndex(int now) {
    for (int n = lastLogIndex(); n > commitIndex; n--) {
        if (storage.termAt(n) != storage.currentTerm) continue;
        int count = 1;   // the leader itself
        for (int p : peers) if (matchIndex.get(p) >= n) count++;
        if (count >= majority()) {
            commitIndex = n;
            applyCommitted(now);
            return;
        }
    }
}

void applyCommitted(int now) {
    while (lastApplied < commitIndex) {
        lastApplied++;
        stateMachine.apply(storage.log.get(lastApplied).command());
    }
}
```

This is Figure 2's last leader rule, scanning from the newest entry downward so the first hit is the largest valid `N`. The `continue` on the term check is the section 5.4.2 rule; delete that line and the test `term-2 entries on 3/5 replicas are not committed in term 5` fails, and so would a production cluster under Figure 8's schedule. `applyCommitted` is shared by leaders and followers and is the only code that touches the state machine. Because `lastApplied` and `commitIndex` are volatile, a restarted node runs this loop again from zero as the leader's `leaderCommit` catches it up, which is how the state machine is rebuilt.

**Predict:** the leader of term 3 has `matchIndex = {n1: 4, n2: 4, n3: 2, n4: 0}` and a log whose entries 1-2 are term 1 and 3-4 are term 3. What is `commitIndex` after the scan?

### Step 9: The invariant checker

```java
static void checkLogMatching(List<LogEntry> a, List<LogEntry> b, int ia, int ib) {
    int limit = Math.min(a.size(), b.size()) - 1;
    for (int k = limit; k >= 1; k--) {
        if (a.get(k).term() == b.get(k).term()) {
            for (int i = 1; i <= k; i++) {
                if (!a.get(i).equals(b.get(i))) throw new IllegalStateException("LOG MATCHING: nodes " + ia + " and " + ib
                        + " agree at index " + k + " (term " + a.get(k).term() + ") but differ at index " + i);
            }
            return;
        }
    }
}
```

`Cluster.checkInvariants` runs after every delivery and checks all four Figure 3 properties: Election Safety (from Lesson 05), Leader Append-Only (a snapshot of each leader's log is compared with its next state; a leader may only extend it), Log Matching (above, for every pair), and State Machine Safety (the first entry ever committed at each index is recorded, and any node committing a different entry there is a violation). The oracle is independent of the protocol code: it reads logs and roles, never `matchIndex`, so a bug in the leader's bookkeeping cannot hide from it.

### Step 10: Run it

```bash
cd phases/06-replication-and-consensus/06-raft-log-replication/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Replicate three commands on a 5-node cluster (seed 7, no drops, delay 1-5) ==
t=186  n4 appends index 2 t1:SET alice 100 and replicates
t=188  n4 commits through index 1 (3/5 replicas)
t=191  n4 commits through index 4 (3/5 replicas)
all five state machines applied through index 4 at tick 241:
  n4 LEADER    term 1 log=[t1:NOOP, t1:SET alice 100, t1:SET bob 50, t1:SET alice 75] commit=4 applied=4 kv={alice=75, bob=50}

== A stale leader's uncommitted entries are overwritten ==
t=241  network partition: [0, 4] | rest
t=429  n3 wins -> LEADER of term 2, log ends at index 4
t=438  n3 commits through index 5 (3/5 replicas)
t=880  n0 conflict at index 5: has t1:SET alice 999, leader has t2:NOOP -> truncating 2 entries
t=882  n4 sees term 2 -> follower (was LEADER)
state machines converged = true; alice=75 (the 999 write was never committed, so it never happened)

== Commit needs a majority of matchIndex, not a reply from anyone ==
3 of 5 crashed; proposed index 2; after 400 ticks leader commitIndex=1, entry replicated on 2/5 nodes -> not committed
one node restarted (2 live followers + leader = 3): commitIndex=2, replicated on 3/5 -> committed and applied: balance=1000

== Only entries from the leader's current term are committed by counting replicas (section 5.4.2) ==
term-2 entries replicated on 3/5 (indexes 1-2): commitIndex=0  (counting replicas of an older-term entry would be unsafe)
term-5 NOOP at index 3 replicated on 3/5: commitIndex=3  (the current-term entry commits everything before it)

== 20 seeds, 5 nodes, 20% drops, delay 1-10, a crash and restart, 12 commands: all four Figure 3 properties hold ==
seeds whose five state machines converged on all 12 writes: 20/20; no invariant check fired
```

The output of `main.py` and `main.go` is byte-identical to this, which you can verify with `diff`. The first section's tick numbers are the same as Lesson 05's election because the election code and the seed did not change; replication starts at tick 186, the first heartbeat after `n4` wins.

### In Python

```python
def advance_commit_index(self, now: int) -> None:
    for n in range(self.last_log_index, self.commit_index, -1):
        if self.storage.term_at(n) != self.current_term:
            continue
        count = 1 + sum(1 for p in self.peers if self.match_index[p] >= n)
        if count >= self.majority:
            self.commit_index = n
            self.apply_committed(now)
            return
```

Differences, all in `code/python/main.py`:

- **`Message.entries` is a tuple.** The dataclass is frozen and the constructor calls `tuple(entries)`, which is Python's version of `List.copyOf`: the leader's later appends cannot reach a message already in the queue, and a `list` slice `self.log[nxt:]` would already be a copy but the tuple makes the intent visible.
- **`truncate` is `del self.log[from_index:]`**, one statement where Java loops. Python slicing from index to end is exactly rule 3.
- **`KVStore.__str__` sorts the keys** because a plain `dict` prints in insertion order, which differs between nodes that applied `DEL` and re-`SET`. The Java `TreeMap` and Go's sorted-keys `String()` make the same choice so the demo output matches.
- **Everything else is Lesson 05's**: the `heapq` network, the masked SplitMix64, the `sorted(self.nodes)` tick order.

### In Go

```go
func (s *Storage) Truncate(fromIndex int) {
    s.Log = s.Log[:fromIndex]
    s.Writes++
}

func appendEntries(from, to int, term int64, prevLogIndex int, prevLogTerm int64, entries []LogEntry, leaderCommit int) Message {
    copied := append([]LogEntry(nil), entries...)
    return Message{Kind: AppendEntries, From: from, To: to, Term: term, PrevLogIndex: prevLogIndex,
        PrevLogTerm: prevLogTerm, Entries: copied, LeaderCommit: leaderCommit}
}
```

Differences, all in `code/go/main.go`:

- **Slices alias.** `s.Log[:fromIndex]` reslices in place, which is what a truncation should do, but it also means `s.Log[next:]` handed to a message would share the leader's backing array, and a later truncate-then-append would rewrite entries inside a message still in flight. The `appendEntries` constructor copies for that reason. This is the Go-specific version of the same rule as `List.copyOf` and `tuple()`.
- **`KVStore.Apply` returns an error** for malformed commands rather than panicking; `applyCommitted` panics on that error because a committed entry that cannot be applied is a broken invariant, not a recoverable condition.
- **`Get` returns `(string, bool)`**, the idiomatic missing-key signal, where Java returns `null` and Python `None`.
- **No goroutines.** Single-threaded simulation, so `go test -race ./...` passes without synchronisation and the determinism test holds.

## Use It

### etcd/raft and the `Ready` loop

`go.etcd.io/raft` exposes exactly the surface this lesson built: `Propose(data)` appends to the leader's log, `Step(msg)` is `handle`, and the `Ready` struct it returns contains `Entries` to persist, `CommittedEntries` to apply, and `Messages` to send. The contract that the caller must persist entries and the hard state *before* sending the messages in the same `Ready` is Figure 2's "updated on stable storage before responding to RPCs", and getting the order wrong is the most common way to break a Raft deployment. The library's `MsgApp` and `MsgAppResp` are AppendEntries and its reply, and `MsgAppResp` carries the follower's match index, as this lesson's reply does. Its `raftLog` type has `committed` and `applied` fields with the semantics of `commitIndex` and `lastApplied`, and a `maybeAppend` method that is rules 2 through 5 in about twenty lines.

### What production adds

- **Batching and pipelining.** The leader does not send one message per client write; it accumulates entries and sends them on the next heartbeat tick, and it does not wait for a reply before sending the next batch (`nextIndex` is advanced optimistically and corrected on rejection). This lesson's `replicateAll` on every `propose` is simple and correct but would saturate a network.
- **Fast log back-off.** The paper's section 5.3 describes a follower returning the term of its conflicting entry and the first index of that term, so the leader can skip a whole term per round trip. etcd and hashicorp/raft both implement a variant. Exercise 2.
- **Disk.** Every `storage.append` and `save` is an fsync in production, and the follower's reply waits for it. Commit latency is therefore bounded below by the fastest-majority fsync, which is why Raft clusters care about disk latency more than CPU, and why a leader with a slow disk should be replaced rather than tolerated.
- **Lease-based and ReadIndex reads.** Serving a read from the leader's state machine is only linearizable if the leader is still the leader. etcd's `ReadIndex` sends a heartbeat round before answering; `CheckQuorum` plus a clock lease avoids the round trip at the cost of a clock-skew assumption. Lesson 12 builds the checker that exposes the difference.

### Payment systems

CockroachDB, TiDB, and YugabyteDB run one Raft group per range of keys, so a ledger table partitioned by account id is thousands of independent logs, each with its own leader, `commitIndex`, and `NOOP`-on-election. A posting that writes two accounts in different ranges is a distributed transaction across two Raft groups (Phase 7). Kafka's KRaft controller stores cluster metadata in a single Raft log and exposes its high-water mark, which is `commitIndex` under another name; the same term appears in Kafka's data replication, where a message is visible to consumers only once it is below the high-water mark, the in-sync-replica analogue of "on a majority". When a payment event stream promises that an acknowledged event will not be lost, that promise is a `commitIndex` and the argument for it is the one in this lesson.

## Ship It

`outputs/runbook-raft-log-divergence.md` is an incident runbook for a replicated log whose replicas disagree: how to read a log dump against the Log Matching property, how to tell an uncommitted tail from a lost commit, which of the two is a bug in the protocol and which is normal, and the three implementation mistakes (truncating on every AppendEntries, committing old-term entries by replica count, acknowledging before the persistent write) that produce each symptom. It applies to etcd, Kafka ISR, and any homegrown primary-backup log.

## Exercises

1. **Reinforce.** Remove the `storage.termAt(n) != storage.currentTerm` check from `advanceCommitIndex` in one language and write a scripted test that reproduces Figure 8 with five standalone nodes driven by hand-built messages, ending with a committed entry being overwritten. Confirm `checkInvariants`'s State Machine Safety check fires. Restore the line and keep the test as a regression.
2. **Apply.** Implement the fast back-off from section 5.3: on a rule-2 rejection the follower returns `conflictTerm` (the term of its entry at `prevLogIndex`, or 0 if it has no such entry) and `conflictIndex` (the first index it holds with that term, or its length plus one). The leader sets `nextIndex` to `conflictIndex`, or to the index after its own last entry of `conflictTerm` if it has one. Measure the number of AppendEntries round trips needed to catch up a follower that missed 200 entries across 10 terms, before and after.
3. **Extend.** Replace `KVStore` with the double-entry ledger from Phase 10, Lesson 03: commands become `POST <debit-account> <credit-account> <minor-units> <currency>` and the state machine rejects a posting that would make a liability account negative. Then show by test that a rejected posting is still a committed log entry (consensus agreed on the command; the state machine deterministically refused it on every node) and that all five ledgers report the same balances and the same rejection list after a partition. This is the shape of every Raft-backed ledger in production.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Log entry | "a write" | A `(term, command)` pair at an index; the index is its position and the `(index, term)` pair is a globally unique id for the command |
| Consistency check | "prevLogIndex/prevLogTerm" | The follower refuses an AppendEntries unless it holds exactly the entry the leader names as the predecessor; it is what makes Log Matching hold by induction |
| nextIndex | "where the follower is" | The leader's optimistic guess of the next entry a follower needs; starts at the leader's length plus one and moves down on rejection |
| matchIndex | "what the follower has" | The leader's proven knowledge of the highest entry a follower holds that matches the leader's log; only ever increases; a majority of these decides the commit index |
| commitIndex | "durable up to here" | The highest index known to be on a majority; volatile, recomputable, and advanced on the leader only for current-term entries |
| Log Matching | "the logs agree" | If two logs share an entry's index and term they are identical up to that index; logs may still diverge above their last common entry |
| State Machine Safety | "no lost commits" | Once any node applies an entry at an index, no node ever applies a different entry at that index |
| Figure 8 | "the old-term commit bug" | The paper's schedule in which counting replicas of an earlier-term entry lets a later leader overwrite it; prevented by the current-term rule and the election no-op |

## Further Reading

- [In Search of an Understandable Consensus Algorithm (Extended Version)](https://raft.github.io/raft.pdf) (Ongaro and Ousterhout, 2014) — Figure 2 is the specification this code follows; section 5.3 is log replication, 5.4.2 and Figure 8 are the commit rule, section 8 is the election no-op and client interaction.
- [Consensus: Bridging Theory and Practice](https://web.stanford.edu/~ouster/cgi-bin/papers/OngaroPhD.pdf) (Ongaro, PhD thesis, 2014) — chapter 3.6 has the full Log Matching and State Machine Safety proofs; chapter 6 covers the read path that this lesson leaves out.
- [Implementing Fault-Tolerant Services Using the State Machine Approach: A Tutorial](https://www.cs.cornell.edu/fbs/publications/SMSurvey.pdf) (Schneider, 1990) — the replicated-state-machine model that explains why a deterministic `KVStore` plus an agreed log equals a fault-tolerant service.
- [etcd raft library documentation](https://pkg.go.dev/go.etcd.io/raft/v3) — the `Ready` struct and its persistence-before-send contract; compare `raftLog.maybeAppend` with Step 6.
- [Raft TLA+ specification](https://github.com/ongardie/raft.tla) (Ongaro) — the formal model; `AppendEntries` and `AdvanceCommitIndex` there are the actions of Steps 6 and 8, and the Log Matching invariant is stated as `LogMatching`.
- [KIP-500: Replace ZooKeeper with a Self-Managed Metadata Quorum](https://cwiki.apache.org/confluence/display/KAFKA/KIP-500) — Kafka's Raft-based controller; the high-water mark is `commitIndex` under another name.
