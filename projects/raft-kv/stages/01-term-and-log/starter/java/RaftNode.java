// Project: projects/raft-kv — Stages 2-4: one Raft server. YOUR CODE GOES HERE (nothing to do in Stage 1).
// Stage 2 adds election (Figure 2: Followers, Candidates, RequestVote RPC), Stage 3 log replication (AppendEntries RPC,
// nextIndex/matchIndex), Stage 4 commit and apply (commitIndex, lastApplied, the KV state machine).
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
//          sections 5.2, 5.3, 5.4.1, 5.4.2, 8. Java 21, standard library only. Python port in solution/python; Go port follows later.

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
        resetElectionTimer(now);   // Stage 2: a fresh node must draw its first election deadline here
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

    /** Randomized timeout in [min, max] drawn from this node's own seeded Rng (section 5.2). */
    void resetElectionTimer(int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.resetElectionTimer");
    }

    @Override
    public List<Message> tick(int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.tick");
    }

    /** Figure 2, Candidates, "On conversion to candidate". Returns the RequestVote messages to send. */
    List<Message> startElection(int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.startElection");
    }

    @Override
    public List<Message> handle(Message m, int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.handle");
    }

    /** Figure 2, All Servers: a higher term converts any role to follower. */
    void becomeFollower(long term, int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.becomeFollower");
    }

    /** Figure 2, RequestVote RPC, receiver implementation. */
    List<Message> onRequestVote(Message m, int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.onRequestVote");
    }

    List<Message> onVoteReply(Message m, int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.onVoteReply");
    }

    /** Figure 2, Leaders, "upon election". Returns the first round of AppendEntries. */
    List<Message> becomeLeader(int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.becomeLeader");
    }

    /** One AppendEntries per peer (a heartbeat when there is nothing new). Resets the heartbeat timer. */
    List<Message> replicateAll(int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.replicateAll");
    }

    /** Figure 2, AppendEntries RPC, receiver implementation. Stage 2: rules 1 and "candidate steps down"; Stage 3: rules 2-4; Stage 4: rule 5. */
    List<Message> onAppendEntries(Message m, int now) {
        throw new UnsupportedOperationException("stage 2: RaftNode.onAppendEntries");
    }

    // ------------------------------------------------------------------ Stage 3: log replication

    /** Client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader. */
    int propose(String command) {
        throw new UnsupportedOperationException("stage 3: RaftNode.propose");
    }

    /** The AppendEntries for one peer: everything from nextIndex[peer] onward, preceded by the (index, term) it must already hold. */
    Message appendEntriesFor(int peer) {
        throw new UnsupportedOperationException("stage 3: RaftNode.appendEntriesFor");
    }

    /** Figure 2, Leaders: on success update nextIndex/matchIndex; on failure decrement nextIndex and retry. */
    List<Message> onAppendReply(Message m, int now) {
        throw new UnsupportedOperationException("stage 3: RaftNode.onAppendReply");
    }

    /** How many nodes (counting the leader) are known to hold the entry at `index`. */
    int replicatedCount(int index) {
        throw new UnsupportedOperationException("stage 3: RaftNode.replicatedCount");
    }

    // ------------------------------------------------------------------ Stage 4: commit and apply

    /** Figure 2, Leaders, last rule, with the section 5.4.2 current-term condition. */
    void advanceCommitIndex(int now) {
        throw new UnsupportedOperationException("stage 4: RaftNode.advanceCommitIndex");
    }

    /** Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied]. */
    void applyCommitted(int now) {
        throw new UnsupportedOperationException("stage 4: RaftNode.applyCommitted");
    }
}
