// Project: projects/raft-kv — Stages 2-4: one Raft server.
// Stage 2 adds election (Figure 2: Followers, Candidates, RequestVote RPC), Stage 3 log replication (AppendEntries RPC,
// nextIndex/matchIndex), Stage 4 commit and apply (commitIndex, lastApplied, the KV state machine).
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
//          sections 5.2, 5.3, 5.4.1, 5.4.2, 8. Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

final class RaftNode implements Node {
    final int id;
    final List<Integer> peers;
    final Storage storage;
    private final Rng rng;
    private final Config config;
    private final Tracer tracer;

    Role role = Role.FOLLOWER;
    int leaderId = -1;
    // Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
    int commitIndex = 0;
    int lastApplied = 0;
    final KVStore stateMachine = new KVStore();
    // Volatile state on leaders, reinitialised after every election.
    final Map<Integer, Integer> nextIndex = new HashMap<>();
    final Map<Integer, Integer> matchIndex = new HashMap<>();

    final Set<Integer> votesGranted = new HashSet<>();
    int electionDeadline;
    int nextHeartbeat;
    int electionsStarted = 0;
    int consistencyRejections = 0;

    RaftNode(int id, List<Integer> peers, Storage storage, Rng rng, Config config, int now, Tracer tracer) {
        this.id = id;
        this.peers = List.copyOf(peers);
        this.storage = storage;
        this.rng = rng;
        this.config = config;
        this.tracer = tracer == null ? Tracer.NONE : tracer;
        resetElectionTimer(now);
    }

    @Override public int id() { return id; }
    long currentTerm() { return storage.currentTerm; }
    int votedFor() { return storage.votedFor; }
    int clusterSize() { return peers.size() + 1; }
    int majority() { return clusterSize() / 2 + 1; }
    int votes() { return votesGranted.size(); }
    List<LogEntry> log() { return storage.log; }
    int lastLogIndex() { return storage.lastIndex(); }
    long lastLogTerm() { return storage.lastTerm(); }
    static String name(int id) { return "n" + id; }

    // ------------------------------------------------------------------ Stage 2: election

    /** Randomized timeout in [min, max] from the node's own seeded Rng: the paper's whole answer to split votes (section 5.2). */
    void resetElectionTimer(int now) {
        int span = config.electionTimeoutMax() - config.electionTimeoutMin() + 1;
        electionDeadline = now + config.electionTimeoutMin() + rng.nextInt(span);
    }

    @Override
    public List<Message> tick(int now) {
        if (role == Role.LEADER) return now >= nextHeartbeat ? replicateAll(now) : List.of();
        // Followers and candidates: election timeout elapses -> start (another) election.
        return now >= electionDeadline ? startElection(now) : List.of();
    }

    /** Figure 2, Candidates, "On conversion to candidate": increment term, vote for self, persist, reset timer, ask everyone. */
    List<Message> startElection(int now) {
        role = Role.CANDIDATE;
        storage.save(storage.currentTerm + 1, id);
        leaderId = -1;
        votesGranted.clear();
        votesGranted.add(id);
        electionsStarted++;
        resetElectionTimer(now);
        tracer.accept(now, name(id) + " election timeout -> candidate for term " + storage.currentTerm);
        List<Message> out = new ArrayList<>();
        for (int p : peers) out.add(Message.requestVote(id, p, storage.currentTerm, lastLogIndex(), lastLogTerm()));
        return out;
    }

    @Override
    public List<Message> handle(Message m, int now) {
        // Figure 2, All Servers: if RPC request or response contains term T > currentTerm, set currentTerm = T, convert to follower.
        if (m.term() > storage.currentTerm) becomeFollower(m.term(), now);
        return switch (m.kind()) {
            case REQUEST_VOTE -> onRequestVote(m, now);
            case REQUEST_VOTE_REPLY -> onVoteReply(m, now);
            case APPEND_ENTRIES -> onAppendEntries(m, now);
            case APPEND_ENTRIES_REPLY -> onAppendReply(m, now);
        };
    }

    void becomeFollower(long term, int now) {
        Role before = role;
        role = Role.FOLLOWER;
        leaderId = -1;
        votesGranted.clear();
        if (term > storage.currentTerm) {
            storage.save(term, -1);   // a new term means votedFor is null again
            tracer.accept(now, name(id) + " sees term " + term + " -> follower" + (before == Role.LEADER ? " (was LEADER)" : before == Role.CANDIDATE ? " (was candidate)" : ""));
        }
        if (before == Role.LEADER) resetElectionTimer(now);   // a leader keeps no election timer; give it one when it steps down
    }

    /** Figure 2, RequestVote RPC, receiver implementation. */
    List<Message> onRequestVote(Message m, int now) {
        if (m.term() < storage.currentTerm) {                                              // 1. reply false if term < currentTerm
            return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
        }
        boolean canVote = storage.votedFor == -1 || storage.votedFor == m.from();            // 2. votedFor is null or candidateId
        boolean logOk = storage.upToDate(m.prevLogIndex(), m.prevLogTerm());               //    and candidate's log is at least as up-to-date
        if (canVote && logOk) {
            storage.save(storage.currentTerm, m.from());                                   // persist the vote before the reply leaves
            resetElectionTimer(now);                                                      // granting a vote resets the election timer
            tracer.accept(now, name(id) + " grants vote to " + name(m.from()) + " for term " + m.term());
            return List.of(Message.voteReply(id, m.from(), storage.currentTerm, true));
        }
        return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
    }

    List<Message> onVoteReply(Message m, int now) {
        // A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
        if (role != Role.CANDIDATE || m.term() != storage.currentTerm) return List.of();
        if (m.success()) {
            votesGranted.add(m.from());
            if (votesGranted.size() >= majority()) return becomeLeader(now);
        }
        return List.of();
    }

    /** Figure 2, Leaders: reinitialise nextIndex/matchIndex, append a no-op (section 8), send initial AppendEntries. */
    List<Message> becomeLeader(int now) {
        role = Role.LEADER;
        leaderId = id;
        for (int p : peers) { nextIndex.put(p, lastLogIndex() + 1); matchIndex.put(p, 0); }
        tracer.accept(now, name(id) + " wins " + votesGranted.size() + "/" + clusterSize() + " votes -> LEADER of term " + storage.currentTerm + ", log ends at index " + lastLogIndex());
        storage.append(new LogEntry(storage.currentTerm, "NOOP"));   // Stage 4: lets the leader commit earlier-term entries at once
        return replicateAll(now);
    }

    /** One AppendEntries per peer; with nothing new to ship it is a heartbeat. Resets the heartbeat timer. */
    List<Message> replicateAll(int now) {
        nextHeartbeat = now + config.heartbeatInterval();
        List<Message> out = new ArrayList<>();
        for (int p : peers) out.add(appendEntriesFor(p));
        return out;
    }

    // ------------------------------------------------------------------ Stage 3: log replication

    /** Client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader. */
    int propose(String command) {
        if (role != Role.LEADER) return -1;
        storage.append(new LogEntry(storage.currentTerm, command));
        return lastLogIndex();
    }

    /** The AppendEntries for one peer: everything from nextIndex[peer] onward, preceded by the (index, term) it must already hold. */
    Message appendEntriesFor(int peer) {
        int next = nextIndex.get(peer);
        int prev = next - 1;
        return Message.appendEntries(id, peer, storage.currentTerm, prev, storage.termAt(prev), storage.entriesFrom(next), commitIndex);
    }

    /** Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order. */
    List<Message> onAppendEntries(Message m, int now) {
        if (m.term() < storage.currentTerm) {                                                              // 1
            return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
        }
        if (role == Role.LEADER) throw new IllegalStateException("two leaders in term " + storage.currentTerm + ": " + id + " and " + m.from());
        if (role == Role.CANDIDATE) {
            role = Role.FOLLOWER;
            votesGranted.clear();
            tracer.accept(now, name(id) + " hears leader " + name(m.from()) + " for term " + m.term() + " -> candidate steps down");
        }
        leaderId = m.from();
        resetElectionTimer(now);
        if (m.prevLogIndex() > lastLogIndex() || storage.termAt(m.prevLogIndex()) != m.prevLogTerm()) {    // 2: consistency check
            consistencyRejections++;
            tracer.accept(now, name(id) + " rejects AppendEntries: no entry (" + m.prevLogIndex() + ", t" + m.prevLogTerm() + "); my log ends at " + lastLogIndex());
            return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
        }
        int index = m.prevLogIndex();
        for (LogEntry e : m.entries()) {
            index++;
            if (index <= lastLogIndex()) {
                if (storage.termAt(index) == e.term()) continue;                                           // 4: already have it
                tracer.accept(now, name(id) + " conflict at index " + index + ": has " + storage.log.get(index) + ", leader has " + e + " -> truncating " + (lastLogIndex() - index + 1));
                storage.truncate(index);                                                                  // 3: delete the conflict and all after it
            }
            storage.append(e);                                                                            // 4: append the new entry
        }
        int lastNew = m.prevLogIndex() + m.entries().size();
        if (m.leaderCommit() > commitIndex) {                                                             // 5 (Stage 4)
            commitIndex = Math.min(m.leaderCommit(), lastNew);
            applyCommitted(now);
        }
        return List.of(Message.appendReply(id, m.from(), storage.currentTerm, true, lastNew));
    }

    /** Figure 2, Leaders: on success update nextIndex/matchIndex and try to commit; on failure decrement nextIndex and retry. */
    List<Message> onAppendReply(Message m, int now) {
        if (role != Role.LEADER || m.term() != storage.currentTerm) return List.of();
        if (m.success()) {
            if (m.matchIndex() > matchIndex.get(m.from())) {
                matchIndex.put(m.from(), m.matchIndex());
                nextIndex.put(m.from(), m.matchIndex() + 1);
                advanceCommitIndex(now);   // Stage 4
            }
            return List.of();
        }
        nextIndex.put(m.from(), Math.max(1, nextIndex.get(m.from()) - 1));
        return List.of(appendEntriesFor(m.from()));
    }

    /** How many nodes (counting the leader) are known to hold the entry at `index`. */
    int replicatedCount(int index) {
        int count = 1;
        for (int p : peers) if (matchIndex.getOrDefault(p, 0) >= index) count++;
        return count;
    }

    // ------------------------------------------------------------------ Stage 4: commit and apply

    /**
     * Figure 2, Leaders, last rule: if there exists an N > commitIndex such that a majority of matchIndex[i] >= N and
     * log[N].term == currentTerm, set commitIndex = N. The term condition is section 5.4.2 (Figure 8): a leader never
     * commits an earlier-term entry by counting replicas; it commits it by committing one of its own entries after it.
     */
    void advanceCommitIndex(int now) {
        if (role != Role.LEADER) return;
        for (int n = lastLogIndex(); n > commitIndex; n--) {
            if (storage.termAt(n) != storage.currentTerm) continue;
            if (replicatedCount(n) >= majority()) {
                tracer.accept(now, name(id) + " commits through index " + n + " (" + replicatedCount(n) + "/" + clusterSize() + " replicas)");
                commitIndex = n;
                applyCommitted(now);
                return;
            }
        }
    }

    /** Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied] to the state machine. */
    void applyCommitted(int now) {
        while (lastApplied < commitIndex) {
            lastApplied++;
            stateMachine.apply(storage.log.get(lastApplied).command());
        }
    }
}
