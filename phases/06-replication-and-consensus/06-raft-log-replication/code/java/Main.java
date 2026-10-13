// Lesson: phases/06-replication-and-consensus/06-raft-log-replication/docs/en.md
// Raft II: Log Replication and Commit
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
//          sections 5.3 (log replication), 5.4.2 (committing entries from previous terms), 8 (no-op on election).
// Java 21, standard library only. Same design as the Python and Go versions; the network is Lesson 05's.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.function.BiConsumer;

/** SplitMix64 (Steele, Lea, Flood, OOPSLA 2014): identical streams in Java, Python, and Go, so a seed replays everywhere. */
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    int nextInt(int bound) { return (int) ((nextLong() >>> 33) % bound); }

    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }
}

enum Role { FOLLOWER, CANDIDATE, LEADER }

enum MessageKind { REQUEST_VOTE, REQUEST_VOTE_REPLY, APPEND_ENTRIES, APPEND_ENTRIES_REPLY }

/** One log entry: the term in which the leader created it and the client command. Index is its position in the log. */
record LogEntry(long term, String command) {
    @Override public String toString() { return "t" + term + ":" + command; }
}

/**
 * One message. For REQUEST_VOTE, prevLogIndex/prevLogTerm carry the candidate's last log index and term. For
 * APPEND_ENTRIES they are the entry immediately before `entries`, which is the consistency check. `success` doubles as
 * voteGranted; `matchIndex` on a successful APPEND_ENTRIES_REPLY tells the leader how far the follower's log now matches.
 */
record Message(MessageKind kind, int from, int to, long term, int prevLogIndex, long prevLogTerm,
               List<LogEntry> entries, int leaderCommit, boolean success, int matchIndex) {
    static Message requestVote(int from, int to, long term, int lastLogIndex, long lastLogTerm) {
        return new Message(MessageKind.REQUEST_VOTE, from, to, term, lastLogIndex, lastLogTerm, List.of(), 0, false, 0);
    }

    static Message voteReply(int from, int to, long term, boolean granted) {
        return new Message(MessageKind.REQUEST_VOTE_REPLY, from, to, term, 0, 0, List.of(), 0, granted, 0);
    }

    static Message appendEntries(int from, int to, long term, int prevLogIndex, long prevLogTerm, List<LogEntry> entries, int leaderCommit) {
        return new Message(MessageKind.APPEND_ENTRIES, from, to, term, prevLogIndex, prevLogTerm, List.copyOf(entries), leaderCommit, false, 0);
    }

    static Message appendReply(int from, int to, long term, boolean success, int matchIndex) {
        return new Message(MessageKind.APPEND_ENTRIES_REPLY, from, to, term, 0, 0, List.of(), 0, success, matchIndex);
    }
}

/**
 * Figure 2, persistent state: currentTerm, votedFor, and log[]. The log is 1-indexed as in the paper; slot 0 holds a
 * sentinel entry with term 0 so that prevLogIndex = 0 always matches. Survives crash(); everything else on the node is lost.
 */
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    final List<LogEntry> log = new ArrayList<>(List.of(new LogEntry(0, "<sentinel>")));
    int writes = 0;

    void save(long term, int votedFor) { this.currentTerm = term; this.votedFor = votedFor; writes++; }

    void append(LogEntry e) { log.add(e); writes++; }

    /** Delete the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3). */
    void truncate(int fromIndex) { while (log.size() > fromIndex) log.remove(log.size() - 1); writes++; }

    int lastIndex() { return log.size() - 1; }

    long termAt(int index) { return log.get(index).term(); }
}

/** The replicated state machine: a key-value store. Commands are "SET k v", "DEL k", or "NOOP". Values are strings. */
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

    String get(String key) { return data.get(key); }

    @Override public String toString() { return data.toString(); }
}

record Config(int electionTimeoutMin, int electionTimeoutMax, int heartbeatInterval) {
    static final Config DEFAULT = new Config(150, 300, 50);

    Config {
        if (electionTimeoutMin <= 0 || electionTimeoutMax < electionTimeoutMin || heartbeatInterval <= 0) throw new IllegalArgumentException("bad timing config");
        if (heartbeatInterval >= electionTimeoutMin) throw new IllegalArgumentException("heartbeat interval must be well below the election timeout");
    }
}

interface Node {
    int id();
    List<Message> tick(int now);
    List<Message> handle(Message m, int now);
}

/** One Raft server: Lesson 05's election plus Figure 2's log replication, commit, and apply rules. */
final class RaftNode implements Node {
    final int id;
    final List<Integer> peers;
    final Storage storage;
    private final Rng rng;
    private final Config config;
    private final BiConsumer<Integer, String> tracer;

    Role role = Role.FOLLOWER;
    int leaderId = -1;
    // Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
    int commitIndex = 0;
    int lastApplied = 0;
    final KVStore stateMachine = new KVStore();
    // Volatile state on leaders, reinitialised after election.
    final Map<Integer, Integer> nextIndex = new HashMap<>();
    final Map<Integer, Integer> matchIndex = new HashMap<>();

    private final Set<Integer> votesGranted = new HashSet<>();
    int electionDeadline;
    int nextHeartbeat;
    int consistencyRejections = 0;

    RaftNode(int id, List<Integer> peers, Storage storage, Rng rng, Config config, int now, BiConsumer<Integer, String> tracer) {
        this.id = id;
        this.peers = List.copyOf(peers);
        this.storage = storage;
        this.rng = rng;
        this.config = config;
        this.tracer = tracer;
        resetElectionTimer(now);
    }

    @Override public int id() { return id; }
    long currentTerm() { return storage.currentTerm; }
    int votedFor() { return storage.votedFor; }
    int clusterSize() { return peers.size() + 1; }
    int majority() { return clusterSize() / 2 + 1; }
    List<LogEntry> log() { return storage.log; }
    int lastLogIndex() { return storage.lastIndex(); }
    long lastLogTerm() { return storage.termAt(storage.lastIndex()); }

    void resetElectionTimer(int now) {
        int span = config.electionTimeoutMax() - config.electionTimeoutMin() + 1;
        electionDeadline = now + config.electionTimeoutMin() + rng.nextInt(span);
    }

    @Override
    public List<Message> tick(int now) {
        if (role == Role.LEADER) return now >= nextHeartbeat ? replicateAll(now) : List.of();
        return now >= electionDeadline ? startElection(now) : List.of();
    }

    // ---------------------------------------------------------------- election (Lesson 05, unchanged except the log check)

    List<Message> startElection(int now) {
        role = Role.CANDIDATE;
        storage.save(storage.currentTerm + 1, id);
        leaderId = -1;
        votesGranted.clear();
        votesGranted.add(id);
        resetElectionTimer(now);
        tracer.accept(now, Main.name(id) + " election timeout -> candidate for term " + storage.currentTerm);
        List<Message> out = new ArrayList<>();
        for (int p : peers) out.add(Message.requestVote(id, p, storage.currentTerm, lastLogIndex(), lastLogTerm()));
        return out;
    }

    @Override
    public List<Message> handle(Message m, int now) {
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
            storage.save(term, -1);
            tracer.accept(now, Main.name(id) + " sees term " + term + " -> follower" + (before == Role.LEADER ? " (was LEADER)" : ""));
        }
        if (before == Role.LEADER) resetElectionTimer(now);
    }

    List<Message> onRequestVote(Message m, int now) {
        if (m.term() < storage.currentTerm) return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
        boolean canVote = storage.votedFor == -1 || storage.votedFor == m.from();
        // Section 5.4.1: the candidate's log must be at least as up-to-date as ours, now measured on the real log.
        boolean logUpToDate = m.prevLogTerm() > lastLogTerm() || (m.prevLogTerm() == lastLogTerm() && m.prevLogIndex() >= lastLogIndex());
        if (canVote && logUpToDate) {
            storage.save(storage.currentTerm, m.from());
            resetElectionTimer(now);
            return List.of(Message.voteReply(id, m.from(), storage.currentTerm, true));
        }
        return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
    }

    List<Message> onVoteReply(Message m, int now) {
        if (role != Role.CANDIDATE || m.term() != storage.currentTerm) return List.of();
        if (m.success()) {
            votesGranted.add(m.from());
            if (votesGranted.size() >= majority()) return becomeLeader(now);
        }
        return List.of();
    }

    /** Figure 2, Leaders: reinitialise nextIndex/matchIndex; section 8: commit a no-op to learn the commit index quickly. */
    List<Message> becomeLeader(int now) {
        role = Role.LEADER;
        leaderId = id;
        for (int p : peers) { nextIndex.put(p, lastLogIndex() + 1); matchIndex.put(p, 0); }
        tracer.accept(now, Main.name(id) + " wins -> LEADER of term " + storage.currentTerm + ", log ends at index " + lastLogIndex());
        storage.append(new LogEntry(storage.currentTerm, "NOOP"));
        return replicateAll(now);
    }

    // ---------------------------------------------------------------- log replication (section 5.3)

    /** Client entry point. Appends to the leader's log and replicates immediately. Returns the new index, or -1 if not leader. */
    int propose(String command) {
        if (role != Role.LEADER) return -1;
        storage.append(new LogEntry(storage.currentTerm, command));
        return lastLogIndex();
    }

    /** One AppendEntries per peer, carrying everything from nextIndex onward (an empty list is a heartbeat). */
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

    /** Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order. */
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
            tracer.accept(now, Main.name(id) + " rejects AppendEntries: no entry (" + m.prevLogIndex() + ", t" + m.prevLogTerm()
                    + "); my log ends at " + lastLogIndex());
            return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
        }
        int index = m.prevLogIndex();
        for (LogEntry e : m.entries()) {
            index++;
            if (index <= lastLogIndex()) {
                if (storage.termAt(index) == e.term()) continue;                                 // 4: already have it
                tracer.accept(now, Main.name(id) + " conflict at index " + index + ": has " + storage.log.get(index) + ", leader has " + e
                        + " -> truncating " + (lastLogIndex() - index + 1) + " entries");
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

    /** Figure 2, Leaders: on success update nextIndex/matchIndex and try to commit; on failure decrement nextIndex and retry. */
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

    /**
     * Figure 2, Leaders, last rule: if there exists an N > commitIndex such that a majority of matchIndex[i] >= N and
     * log[N].term == currentTerm, set commitIndex = N. The term condition is section 5.4.2 (Figure 8).
     */
    void advanceCommitIndex(int now) {
        for (int n = lastLogIndex(); n > commitIndex; n--) {
            if (storage.termAt(n) != storage.currentTerm) continue;
            int count = 1;   // the leader itself
            for (int p : peers) if (matchIndex.get(p) >= n) count++;
            if (count >= majority()) {
                tracer.accept(now, Main.name(id) + " commits through index " + n + " (" + count + "/" + clusterSize() + " replicas)");
                commitIndex = n;
                applyCommitted(now);
                return;
            }
        }
    }

    /** Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied]. */
    void applyCommitted(int now) {
        while (lastApplied < commitIndex) {
            lastApplied++;
            stateMachine.apply(storage.log.get(lastApplied).command());
        }
    }

    int replicatedCount(int index) {
        int count = 1;
        for (int p : peers) if (matchIndex.getOrDefault(p, 0) >= index) count++;
        return count;
    }
}

/** Lesson 05's deterministic network, unchanged: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes. */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Rng rng;
    private final double dropRate;
    private final int maxDelay;
    private final Map<Integer, Node> nodes = new TreeMap<>();
    private final PriorityQueue<Envelope> inFlight =
            new PriorityQueue<>(Comparator.comparingInt(Envelope::deliverAt).thenComparingLong(Envelope::seq));
    private final Set<Integer> down = new HashSet<>();
    private Set<Integer> sideA = Set.of();
    private boolean partitioned = false;
    private Runnable observer = () -> {};
    private int now = 0;
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;

    Network(long seed, double dropRate, int maxDelay) {
        this.rng = new Rng(seed);
        this.dropRate = dropRate;
        this.maxDelay = Math.max(1, maxDelay);
    }

    void add(Node node) { nodes.put(node.id(), node); }
    void setObserver(Runnable r) { observer = r; }
    int now() { return now; }
    int inFlight() { return inFlight.size(); }
    boolean isDown(int id) { return down.contains(id); }
    void partition(Set<Integer> sideA) { this.sideA = Set.copyOf(sideA); partitioned = true; }
    void heal() { partitioned = false; sideA = Set.of(); }
    void crash(int id) { down.add(id); }
    void restart(Node node) { down.remove(node.id()); nodes.put(node.id(), node); }

    private boolean crossesPartition(int a, int b) { return partitioned && sideA.contains(a) != sideA.contains(b); }

    void send(Message m) {
        sent++;
        if (down.contains(m.from()) || down.contains(m.to()) || crossesPartition(m.from(), m.to())) { dropped++; return; }
        double roll = rng.nextDouble();
        int delay = 1 + rng.nextInt(maxDelay);
        if (roll < dropRate) { dropped++; return; }
        inFlight.add(new Envelope(now + delay, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    void tick() {
        now++;
        for (Node n : nodes.values()) if (!down.contains(n.id())) sendAll(n.tick(now));
        while (!inFlight.isEmpty() && inFlight.peek().deliverAt() <= now) deliver(inFlight.poll());
    }

    private void deliver(Envelope e) {
        Message m = e.message();
        if (down.contains(m.to()) || crossesPartition(m.from(), m.to())) { dropped++; return; }
        delivered++;
        sendAll(nodes.get(m.to()).handle(m, now));
        observer.run();
    }
}

/** Wires N nodes onto one network and checks the Figure 3 properties after every delivery. */
final class Cluster {
    final int size;
    final long seed;
    final Config config;
    final Network network;
    final List<Storage> storages = new ArrayList<>();
    final List<RaftNode> nodes = new ArrayList<>();
    final List<String> events = new ArrayList<>();
    final Map<Long, Integer> leadersByTerm = new TreeMap<>();
    final Map<Integer, LogEntry> committedByIndex = new TreeMap<>();
    private final Map<Integer, List<LogEntry>> leaderLogSnapshot = new HashMap<>();
    private final int[] restarts;
    int proposals = 0;

    Cluster(int size, long seed, double dropRate, int maxDelay, Config config) {
        this.size = size;
        this.seed = seed;
        this.config = config;
        this.network = new Network(seed, dropRate, maxDelay);
        this.restarts = new int[size];
        for (int i = 0; i < size; i++) {
            storages.add(new Storage());
            RaftNode n = new RaftNode(i, peersOf(i), storages.get(i), nodeRng(seed, i, 0), config, 0, this::trace);
            nodes.add(n);
            network.add(n);
        }
        network.setObserver(this::checkInvariants);
    }

    Cluster(int size, long seed, double dropRate, int maxDelay) { this(size, seed, dropRate, maxDelay, Config.DEFAULT); }

    List<Integer> peersOf(int id) {
        List<Integer> ps = new ArrayList<>();
        for (int j = 0; j < size; j++) if (j != id) ps.add(j);
        return ps;
    }

    static Rng nodeRng(long seed, int id, int restart) { return new Rng(seed * 7919L + 1000003L * (id + 1) + 17L * restart); }

    void trace(int now, String text) { events.add(String.format("t=%-4d %s", now, text)); }

    int now() { return network.now(); }
    RaftNode node(int id) { return nodes.get(id); }
    void tick() { network.tick(); checkInvariants(); }
    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    int runUntilStableLeader(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) { tick(); if (stableLeader() != null) return now(); }
        return -1;
    }

    RaftNode stableLeader() {
        RaftNode leader = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id)) continue;
            if (n.role == Role.LEADER) { if (leader != null) return null; leader = n; }
        }
        if (leader == null) return null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id) || n == leader) continue;
            if (n.currentTerm() != leader.currentTerm() || n.leaderId != leader.id) return null;
        }
        return leader;
    }

    List<RaftNode> leaders() {
        List<RaftNode> ls = new ArrayList<>();
        for (RaftNode n : nodes) if (!network.isDown(n.id) && n.role == Role.LEADER) ls.add(n);
        return ls;
    }

    /** Submit a client command to the current stable leader; returns its log index or -1. */
    int propose(String command) {
        RaftNode l = stableLeader();
        return l == null ? -1 : proposeTo(l.id, command);
    }

    /** Submit to a specific node (which may be a stale leader inside a partition). */
    int proposeTo(int nodeId, String command) {
        RaftNode n = nodes.get(nodeId);
        int index = n.propose(command);
        if (index > 0) {
            proposals++;
            trace(now(), Main.name(nodeId) + " appends index " + index + " " + n.log().get(index) + " and replicates");
            network.sendAll(n.replicateAll(now()));
        }
        return index;
    }

    /** Run until every live node has applied through `index`, or maxTicks pass. Returns the tick or -1. */
    int runUntilApplied(int index, int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            boolean all = true;
            for (RaftNode n : nodes) if (!network.isDown(n.id) && n.lastApplied < index) all = false;
            if (all) return now();
        }
        return -1;
    }

    /** Election Safety, Leader Append-Only, Log Matching, and State Machine Safety (Figure 3), checked after every delivery. */
    void checkInvariants() {
        for (RaftNode n : nodes) {
            if (n.role == Role.LEADER) {
                Integer existing = leadersByTerm.putIfAbsent(n.currentTerm(), n.id);
                if (existing != null && existing != n.id) throw new IllegalStateException("ELECTION SAFETY: term " + n.currentTerm() + " has leaders " + existing + " and " + n.id);
                List<LogEntry> previous = leaderLogSnapshot.get(n.id);
                if (previous != null && !n.log().subList(0, Math.min(previous.size(), n.log().size())).equals(previous)) {
                    throw new IllegalStateException("LEADER APPEND-ONLY: " + n.id + " changed an existing entry while leader");
                }
                leaderLogSnapshot.put(n.id, new ArrayList<>(n.log()));
            } else {
                leaderLogSnapshot.remove(n.id);
            }
        }
        for (int i = 0; i < size; i++) {
            for (int j = i + 1; j < size; j++) checkLogMatching(nodes.get(i).log(), nodes.get(j).log(), i, j);
        }
        for (RaftNode n : nodes) {
            for (int k = 1; k <= n.commitIndex; k++) {
                LogEntry seen = committedByIndex.putIfAbsent(k, n.log().get(k));
                if (seen != null && !seen.equals(n.log().get(k))) throw new IllegalStateException("STATE MACHINE SAFETY: index " + k + " committed as " + seen + " and " + n.log().get(k));
            }
        }
    }

    /** Log Matching: if two logs contain an entry with the same index and term, the logs are identical up to that index. */
    static void checkLogMatching(List<LogEntry> a, List<LogEntry> b, int ia, int ib) {
        int limit = Math.min(a.size(), b.size()) - 1;
        for (int k = limit; k >= 1; k--) {
            if (a.get(k).term() == b.get(k).term()) {
                for (int i = 1; i <= k; i++) {
                    if (!a.get(i).equals(b.get(i))) throw new IllegalStateException("LOG MATCHING: nodes " + ia + " and " + ib
                            + " agree at index " + k + " (term " + a.get(k).term() + ") but differ at index " + i + ": " + a.get(i) + " vs " + b.get(i));
                }
                return;
            }
        }
    }

    void partition(Set<Integer> sideA) { network.partition(sideA); trace(now(), "network partition: " + new java.util.TreeSet<>(sideA) + " | rest"); }
    void heal() { network.heal(); trace(now(), "network heals"); }
    void crash(int id) { network.crash(id); trace(now(), Main.name(id) + " crashes (volatile state lost, Storage kept)"); }

    /** Restart from stable storage: term, vote, and log survive; commitIndex, lastApplied, and the KV store are rebuilt. */
    RaftNode restart(int id) {
        restarts[id]++;
        RaftNode n = new RaftNode(id, peersOf(id), storages.get(id), nodeRng(seed, id, restarts[id]), config, now(), this::trace);
        nodes.set(id, n);
        network.restart(n);
        trace(now(), Main.name(id) + " restarts: term " + n.currentTerm() + ", log length " + n.lastLogIndex() + ", commitIndex 0, empty state machine");
        return n;
    }

    boolean stateMachinesConverged() {
        String first = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id)) continue;
            String s = n.stateMachine.toString() + "@" + n.lastApplied;
            if (first == null) first = s; else if (!first.equals(s)) return false;
        }
        return true;
    }

    String logOf(int id) { return nodes.get(id).log().subList(1, nodes.get(id).log().size()).toString(); }
}

public final class Main {
    static String name(int id) { return "n" + id; }

    static void printEvents(Cluster c, int from) { for (int i = from; i < c.events.size(); i++) System.out.println(c.events.get(i)); }

    /** A real client retries against the new leader after an election; here that is "run until a stable leader accepts". */
    static int proposeUntilAccepted(Cluster c, String command) {
        for (int attempt = 0; attempt < 500; attempt++) {
            int index = c.propose(command);
            if (index > 0) return index;
            c.run(10);
        }
        throw new IllegalStateException("no leader accepted " + command);
    }

    static void printLogs(Cluster c) {
        for (RaftNode n : c.nodes) {
            System.out.printf("  %s %-9s term %d log=%s commit=%d applied=%d kv=%s%n", name(n.id), n.role, n.currentTerm(),
                    c.logOf(n.id), n.commitIndex, n.lastApplied, n.stateMachine);
        }
    }

    public static void main(String[] args) {
        System.out.println("== Replicate three commands on a 5-node cluster (seed 7, no drops, delay 1-5) ==");
        Cluster c = new Cluster(5, 7, 0.0, 5);
        c.runUntilStableLeader(2000);
        int before = c.events.size();
        c.propose("SET alice 100");
        c.propose("SET bob 50");
        int last = c.propose("SET alice 75");
        int appliedAt = c.runUntilApplied(last, 1000);
        printEvents(c, before);
        System.out.println("all five state machines applied through index " + last + " at tick " + appliedAt + ":");
        printLogs(c);

        System.out.println();
        System.out.println("== A stale leader's uncommitted entries are overwritten ==");
        RaftNode old = c.stableLeader();
        int follower = (old.id + 1) % 5;
        before = c.events.size();
        c.partition(Set.of(old.id, follower));
        c.proposeTo(old.id, "SET alice 999");
        c.proposeTo(old.id, "SET carol 1");
        c.run(600);
        printEvents(c, before);
        System.out.println("during the partition (minority side cannot commit; majority elected a new leader and committed its own entries):");
        printLogs(c);
        before = c.events.size();
        c.heal();
        c.runUntilStableLeader(2000);
        c.propose("SET dave 7");
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 1000);
        printEvents(c, before);
        System.out.println("after heal: the minority's term-1 entries at the conflicting indexes are truncated and replaced:");
        printLogs(c);
        System.out.println("state machines converged = " + c.stateMachinesConverged() + "; alice=" + c.node(0).stateMachine.get("alice") + " (the 999 write was never committed, so it never happened)");

        System.out.println();
        System.out.println("== Commit needs a majority of matchIndex, not a reply from anyone ==");
        Cluster q = new Cluster(5, 11, 0.0, 5);
        q.runUntilStableLeader(2000);
        RaftNode lead = q.stableLeader();
        List<Integer> others = new ArrayList<>(lead.peers);
        q.crash(others.get(0)); q.crash(others.get(1)); q.crash(others.get(2));
        int idx = q.propose("SET balance 1000");
        q.run(400);
        System.out.printf("3 of 5 crashed; proposed index %d; after 400 ticks leader commitIndex=%d, entry replicated on %d/5 nodes -> not committed%n",
                idx, lead.commitIndex, lead.replicatedCount(idx));
        q.restart(others.get(0));
        q.run(400);
        System.out.printf("one node restarted (2 live followers + leader = 3): commitIndex=%d, replicated on %d/5 -> committed and applied: balance=%s%n",
                lead.commitIndex, lead.replicatedCount(idx), lead.stateMachine.get("balance"));

        System.out.println();
        System.out.println("== Only entries from the leader's current term are committed by counting replicas (section 5.4.2) ==");
        Storage s = new Storage();
        s.append(new LogEntry(2, "SET x 1"));
        s.append(new LogEntry(2, "SET x 2"));
        s.save(4, 0);
        RaftNode l = new RaftNode(0, List.of(1, 2, 3, 4), s, new Rng(1), Config.DEFAULT, 0, (t, msg) -> {});
        l.startElection(1);
        l.handle(Message.voteReply(1, 0, l.currentTerm(), true), 2);
        l.handle(Message.voteReply(2, 0, l.currentTerm(), true), 3);
        System.out.printf("leader of term %d with log %s%n", l.currentTerm(), l.log().subList(1, l.log().size()));
        l.handle(Message.appendReply(1, 0, l.currentTerm(), true, 2), 4);
        l.handle(Message.appendReply(2, 0, l.currentTerm(), true, 2), 5);
        System.out.printf("term-2 entries replicated on 3/5 (indexes 1-2): commitIndex=%d  (counting replicas of an older-term entry would be unsafe)%n", l.commitIndex);
        l.handle(Message.appendReply(1, 0, l.currentTerm(), true, 3), 6);
        l.handle(Message.appendReply(2, 0, l.currentTerm(), true, 3), 7);
        System.out.printf("term-%d NOOP at index 3 replicated on 3/5: commitIndex=%d  (the current-term entry commits everything before it)%n", l.currentTerm(), l.commitIndex);

        System.out.println();
        System.out.println("== 20 seeds, 5 nodes, 20% drops, delay 1-10, a crash and restart, 12 commands: all four Figure 3 properties hold ==");
        int converged = 0;
        for (long seed = 1; seed <= 20; seed++) {
            Cluster r = new Cluster(5, seed, 0.2, 10);
            r.runUntilStableLeader(3000);
            for (int k = 1; k <= 6; k++) { proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(40); }
            RaftNode victim = r.stableLeader() != null ? r.stableLeader() : r.node(0);
            r.crash(victim.id);
            r.runUntilStableLeader(3000);
            for (int k = 7; k <= 12; k++) { proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(40); }
            r.restart(victim.id);
            r.runUntilStableLeader(3000);
            RaftNode fin = r.stableLeader();
            if (fin != null) r.runUntilApplied(fin.lastLogIndex(), 3000);
            if (r.stateMachinesConverged() && fin != null && fin.stateMachine.get("k12") != null) converged++;
        }
        System.out.println("seeds whose five state machines converged on all 12 writes: " + converged + "/20; no invariant check fired");
    }
}
