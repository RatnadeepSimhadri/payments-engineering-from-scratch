// Lesson: phases/06-replication-and-consensus/07-raft-safety-and-membership/docs/en.md
// Raft III: Safety Proofs, Membership Changes, Snapshots
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014): Figure 2, section 5.4
//          (safety argument), section 6 (joint consensus, Figures 10-11), section 7 (log compaction, Figure 13: InstallSnapshot).
//          Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, 2014), chapter 4 (membership changes).
// Java 21, standard library only. Builds on Lesson 05's network and Lesson 06's log replication; same type and method names.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
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

enum MessageKind { REQUEST_VOTE, REQUEST_VOTE_REPLY, APPEND_ENTRIES, APPEND_ENTRIES_REPLY, INSTALL_SNAPSHOT, INSTALL_SNAPSHOT_REPLY }

// ---------------------------------------------------------------------------------------------- configurations (section 6)

/**
 * A cluster configuration. A simple configuration has only `oldMembers`. A joint configuration C_old,new has both sets, and
 * every decision (votes, commits) needs a majority of EACH set. That is the whole trick: no quorum of C_old,new can be
 * formed without a quorum of C_old and a quorum of C_new, so two leaders can never be elected by disjoint groups.
 */
record Configuration(Set<Integer> oldMembers, Set<Integer> newMembers) {
    static Configuration simple(Set<Integer> members) { return new Configuration(new TreeSet<>(members), null); }
    static Configuration joint(Set<Integer> old, Set<Integer> next) { return new Configuration(new TreeSet<>(old), new TreeSet<>(next)); }

    boolean isJoint() { return newMembers != null; }

    /** Everyone who must receive log entries: the union. */
    Set<Integer> all() {
        Set<Integer> s = new TreeSet<>(oldMembers);
        if (newMembers != null) s.addAll(newMembers);
        return s;
    }

    static boolean majorityOf(Set<Integer> members, Set<Integer> acks) {
        int count = 0;
        for (int m : members) if (acks.contains(m)) count++;
        return count >= members.size() / 2 + 1;
    }

    /** The quorum rule. Members outside the configuration (a departing leader, a joining server) are simply not counted. */
    boolean hasQuorum(Set<Integer> acks) {
        if (!majorityOf(oldMembers, acks)) return false;
        return newMembers == null || majorityOf(newMembers, acks);
    }

    /**
     * Figure 10's hazard, checked by brute force: can a majority of `a` and a majority of `b` be disjoint? If yes, switching
     * directly from a to b lets two leaders be elected in the same term. Joint consensus never has this property because
     * every quorum of C_old,new contains a quorum of C_old.
     */
    static boolean disjointQuorumsPossible(Set<Integer> a, Set<Integer> b) {
        List<Integer> union = new ArrayList<>(new TreeSet<>(unionOf(a, b)));
        int n = union.size();
        for (int maskA = 0; maskA < (1 << n); maskA++) {
            Set<Integer> qa = subset(union, maskA);
            if (!majorityOf(a, qa)) continue;
            for (int maskB = 0; maskB < (1 << n); maskB++) {
                if ((maskA & maskB) != 0) continue;
                if (majorityOf(b, subset(union, maskB))) return true;
            }
        }
        return false;
    }

    private static Set<Integer> unionOf(Set<Integer> a, Set<Integer> b) { Set<Integer> s = new TreeSet<>(a); s.addAll(b); return s; }

    private static Set<Integer> subset(List<Integer> items, int mask) {
        Set<Integer> s = new HashSet<>();
        for (int i = 0; i < items.size(); i++) if ((mask & (1 << i)) != 0) s.add(items.get(i));
        return s;
    }

    /** Configurations live in the log as commands so they replicate and persist like everything else. */
    String encode() {
        return newMembers == null ? "CFG " + join(oldMembers) : "CFG " + join(oldMembers) + " -> " + join(newMembers);
    }

    static Configuration decode(String command) {
        String body = command.substring(4).trim();
        int arrow = body.indexOf("->");
        if (arrow < 0) return simple(parse(body));
        return joint(parse(body.substring(0, arrow)), parse(body.substring(arrow + 2)));
    }

    static boolean isConfigCommand(String command) { return command.startsWith("CFG "); }

    private static String join(Set<Integer> s) {
        StringBuilder sb = new StringBuilder();
        for (int m : s) sb.append(sb.length() == 0 ? "" : ",").append(m);
        return sb.toString();
    }

    private static Set<Integer> parse(String s) {
        Set<Integer> out = new TreeSet<>();
        for (String p : s.trim().split(",")) if (!p.isBlank()) out.add(Integer.parseInt(p.trim()));
        return out;
    }

    @Override public String toString() { return newMembers == null ? oldMembers.toString() : oldMembers + "->" + newMembers; }
}

// ---------------------------------------------------------------------------------------------- log, snapshot, storage (section 7)

record LogEntry(long term, String command) {
    @Override public String toString() { return "t" + term + ":" + command; }
}

/** Figure 13: the state machine's state through lastIncludedIndex, plus the latest configuration in that prefix. */
record Snapshot(int lastIncludedIndex, long lastIncludedTerm, Map<String, String> data, Configuration config) {
    Snapshot {
        data = new TreeMap<>(data);
    }
}

record Message(MessageKind kind, int from, int to, long term, int prevLogIndex, long prevLogTerm, List<LogEntry> entries,
               int leaderCommit, boolean success, int matchIndex, Snapshot snapshot) {
    static Message requestVote(int from, int to, long term, int lastLogIndex, long lastLogTerm) {
        return new Message(MessageKind.REQUEST_VOTE, from, to, term, lastLogIndex, lastLogTerm, List.of(), 0, false, 0, null);
    }

    static Message voteReply(int from, int to, long term, boolean granted) {
        return new Message(MessageKind.REQUEST_VOTE_REPLY, from, to, term, 0, 0, List.of(), 0, granted, 0, null);
    }

    static Message appendEntries(int from, int to, long term, int prevLogIndex, long prevLogTerm, List<LogEntry> entries, int leaderCommit) {
        return new Message(MessageKind.APPEND_ENTRIES, from, to, term, prevLogIndex, prevLogTerm, List.copyOf(entries), leaderCommit, false, 0, null);
    }

    static Message appendReply(int from, int to, long term, boolean success, int matchIndex) {
        return new Message(MessageKind.APPEND_ENTRIES_REPLY, from, to, term, 0, 0, List.of(), 0, success, matchIndex, null);
    }

    static Message installSnapshot(int from, int to, long term, Snapshot snapshot) {
        return new Message(MessageKind.INSTALL_SNAPSHOT, from, to, term, snapshot.lastIncludedIndex(), snapshot.lastIncludedTerm(), List.of(), 0, false, 0, snapshot);
    }

    static Message snapshotReply(int from, int to, long term, int matchIndex) {
        return new Message(MessageKind.INSTALL_SNAPSHOT_REPLY, from, to, term, 0, 0, List.of(), 0, true, matchIndex, null);
    }
}

/**
 * Persistent state with compaction. The log is 1-indexed; everything at or before snapshotIndex has been discarded and is
 * summarised by `snapshot`. entries.get(0) is a sentinel standing for index snapshotIndex with term snapshotTerm, so the
 * consistency check for prevLogIndex == snapshotIndex still works.
 */
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    int snapshotIndex = 0;
    long snapshotTerm = 0;
    Snapshot snapshot = null;
    final List<LogEntry> entries = new ArrayList<>(List.of(new LogEntry(0, "<sentinel>")));
    int writes = 0;

    void save(long term, int votedFor) { this.currentTerm = term; this.votedFor = votedFor; writes++; }
    void append(LogEntry e) { entries.add(e); writes++; }

    int lastIndex() { return snapshotIndex + entries.size() - 1; }
    int firstIndex() { return snapshotIndex + 1; }
    boolean has(int index) { return index > snapshotIndex && index <= lastIndex(); }

    long termAt(int index) {
        if (index == snapshotIndex) return snapshotTerm;
        if (index < snapshotIndex) throw new IllegalStateException("index " + index + " is compacted (snapshot at " + snapshotIndex + ")");
        return entries.get(index - snapshotIndex).term();
    }

    LogEntry get(int index) {
        if (!has(index)) throw new IllegalStateException("no entry at " + index);
        return entries.get(index - snapshotIndex);
    }

    void truncate(int fromIndex) { while (lastIndex() >= fromIndex) entries.remove(entries.size() - 1); writes++; }

    /** Discard entries through the snapshot's index, keep the rest. Used by the node's own compaction and by rule 6 of Figure 13. */
    void compact(Snapshot s) {
        List<LogEntry> keep = new ArrayList<>();
        for (int i = s.lastIncludedIndex() + 1; i <= lastIndex(); i++) keep.add(get(i));
        entries.clear();
        entries.add(new LogEntry(s.lastIncludedTerm(), "<sentinel>"));
        entries.addAll(keep);
        snapshotIndex = s.lastIncludedIndex();
        snapshotTerm = s.lastIncludedTerm();
        snapshot = s;
        writes++;
    }

    /** Rule 7 of Figure 13: the snapshot replaces the whole log. */
    void replaceWithSnapshot(Snapshot s) {
        entries.clear();
        entries.add(new LogEntry(s.lastIncludedTerm(), "<sentinel>"));
        snapshotIndex = s.lastIncludedIndex();
        snapshotTerm = s.lastIncludedTerm();
        snapshot = s;
        writes++;
    }

    /** The latest configuration at or before `index`, searching the log then the snapshot. Null if none is known. */
    Configuration configAt(int index, Configuration bootstrap) {
        for (int i = Math.min(index, lastIndex()); i > snapshotIndex; i--) {
            String c = get(i).command();
            if (Configuration.isConfigCommand(c)) return Configuration.decode(c);
        }
        if (snapshot != null) return snapshot.config();
        return bootstrap;
    }

    /** Index of the latest configuration entry still in the log, or -1 if the latest known configuration is in the snapshot. */
    int latestConfigIndex() {
        for (int i = lastIndex(); i > snapshotIndex; i--) if (Configuration.isConfigCommand(get(i).command())) return i;
        return -1;
    }
}

/** The replicated state machine. Configuration entries and NOOPs are applied as no-ops but still counted. */
final class KVStore {
    Map<String, String> data = new TreeMap<>();
    final List<String> appliedCommands = new ArrayList<>();

    void apply(String command) {
        String[] parts = command.split(" ");
        switch (parts[0]) {
            case "SET" -> { if (parts.length != 3) throw new IllegalArgumentException("SET needs key and value: " + command); data.put(parts[1], parts[2]); }
            case "DEL" -> { if (parts.length != 2) throw new IllegalArgumentException("DEL needs key: " + command); data.remove(parts[1]); }
            case "NOOP", "CFG" -> {}
            default -> throw new IllegalArgumentException("unknown command: " + command);
        }
        appliedCommands.add(command);
    }

    void reset(Map<String, String> snapshotData) { data = new TreeMap<>(snapshotData); appliedCommands.add("<snapshot>"); }
    String get(String key) { return data.get(key); }
    @Override public String toString() { return data.toString(); }
}

record Config(int electionTimeoutMin, int electionTimeoutMax, int heartbeatInterval, int snapshotThreshold) {
    static final Config DEFAULT = new Config(150, 300, 50, 8);

    Config {
        if (electionTimeoutMin <= 0 || electionTimeoutMax < electionTimeoutMin || heartbeatInterval <= 0) throw new IllegalArgumentException("bad timing config");
        if (heartbeatInterval >= electionTimeoutMin) throw new IllegalArgumentException("heartbeat interval must be well below the election timeout");
        if (snapshotThreshold < 1) throw new IllegalArgumentException("snapshotThreshold must be positive");
    }
}

interface Node {
    int id();
    List<Message> tick(int now);
    List<Message> handle(Message m, int now);
}

// ---------------------------------------------------------------------------------------------- the server

/** One Raft server: Lesson 06 plus configuration-aware quorums, joint consensus, and snapshots. */
final class RaftNode implements Node {
    final int id;
    final Storage storage;
    private final Rng rng;
    private final Config config;
    private final BiConsumer<Integer, String> tracer;
    /** The configuration a server is created with. Null for a server that joins later and learns its membership from the log. */
    private final Configuration bootstrap;

    Role role = Role.FOLLOWER;
    int leaderId = -1;
    int commitIndex = 0;
    int lastApplied = 0;
    final KVStore stateMachine = new KVStore();
    final Map<Integer, Integer> nextIndex = new HashMap<>();
    final Map<Integer, Integer> matchIndex = new HashMap<>();
    private final Set<Integer> votesGranted = new HashSet<>();
    int electionDeadline;
    int nextHeartbeat;
    int snapshotsTaken = 0, snapshotsInstalled = 0, snapshotsSent = 0;
    boolean steppedDownAfterRemoval = false;

    RaftNode(int id, Configuration bootstrap, Storage storage, Rng rng, Config config, int now, BiConsumer<Integer, String> tracer) {
        this.id = id; this.bootstrap = bootstrap; this.storage = storage; this.rng = rng; this.config = config; this.tracer = tracer;
        resetElectionTimer(now);
        if (storage.snapshot != null) { stateMachine.reset(storage.snapshot.data()); lastApplied = storage.snapshotIndex; commitIndex = storage.snapshotIndex; }
    }

    @Override public int id() { return id; }
    long currentTerm() { return storage.currentTerm; }
    int lastLogIndex() { return storage.lastIndex(); }
    long lastLogTerm() { return storage.termAt(storage.lastIndex()); }

    /** The latest configuration in my log (Figure 11 rule: a server uses the latest configuration it has, committed or not). */
    Configuration configuration() { return storage.configAt(storage.lastIndex(), bootstrap); }

    boolean isMember() { Configuration c = configuration(); return c != null && c.all().contains(id); }

    /** Everyone I replicate to: the union of the current configuration, plus servers leaving C_old who still need C_new. */
    Set<Integer> replicationTargets() {
        Configuration c = configuration();
        if (c == null) return Set.of();
        Set<Integer> targets = new TreeSet<>(c.all());
        int cfgIdx = storage.latestConfigIndex();
        if (!c.isJoint() && cfgIdx > 0) {
            Configuration before = storage.configAt(cfgIdx - 1, bootstrap);
            if (before != null) for (int m : before.all()) if (matchIndex.getOrDefault(m, 0) < cfgIdx) targets.add(m);
        }
        targets.remove(id);
        return targets;
    }

    void resetElectionTimer(int now) {
        int span = config.electionTimeoutMax() - config.electionTimeoutMin() + 1;
        electionDeadline = now + config.electionTimeoutMin() + rng.nextInt(span);
    }

    @Override
    public List<Message> tick(int now) {
        if (role == Role.LEADER) return now >= nextHeartbeat ? replicateAll(now) : List.of();
        if (now < electionDeadline) return List.of();
        if (!isMember()) { resetElectionTimer(now); return List.of(); }       // joining or removed servers never campaign
        return startElection(now);
    }

    // ---------------------------------------------------------------- election (Lesson 05/06, quorum now configuration-aware)

    List<Message> startElection(int now) {
        role = Role.CANDIDATE;
        storage.save(storage.currentTerm + 1, id);
        leaderId = -1;
        votesGranted.clear();
        votesGranted.add(id);
        resetElectionTimer(now);
        tracer.accept(now, Main.name(id) + " election timeout -> candidate for term " + storage.currentTerm + " in config " + configuration());
        List<Message> out = new ArrayList<>();
        for (int p : configuration().all()) if (p != id) out.add(Message.requestVote(id, p, storage.currentTerm, lastLogIndex(), lastLogTerm()));
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
            case INSTALL_SNAPSHOT -> onInstallSnapshot(m, now);
            case INSTALL_SNAPSHOT_REPLY -> onSnapshotReply(m, now);
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
            if (configuration().hasQuorum(votesGranted)) return becomeLeader(now);
        }
        return List.of();
    }

    List<Message> becomeLeader(int now) {
        role = Role.LEADER;
        leaderId = id;
        nextIndex.clear(); matchIndex.clear();
        tracer.accept(now, Main.name(id) + " wins -> LEADER of term " + storage.currentTerm + " with " + votesGranted.size() + " votes in " + configuration() + ", log ends at " + lastLogIndex());
        storage.append(new LogEntry(storage.currentTerm, "NOOP"));
        return replicateAll(now);
    }

    // ---------------------------------------------------------------- replication (Lesson 06) with snapshots (section 7)

    int propose(String command) {
        if (role != Role.LEADER) return -1;
        storage.append(new LogEntry(storage.currentTerm, command));
        return lastLogIndex();
    }

    /**
     * Section 6: start a membership change. Refused while another change is in flight (latest configuration is joint or not yet
     * committed). Appends C_old,new; the leader uses it for quorums as soon as it is in its own log.
     */
    int changeMembership(Set<Integer> newMembers) {
        if (role != Role.LEADER) return -1;
        Configuration current = configuration();
        if (current.isJoint() || storage.latestConfigIndex() > commitIndex) return -1;
        return propose(Configuration.joint(current.oldMembers(), newMembers).encode());
    }

    List<Message> replicateAll(int now) {
        nextHeartbeat = now + config.heartbeatInterval();
        List<Message> out = new ArrayList<>();
        for (int p : replicationTargets()) out.add(appendEntriesFor(p));
        return out;
    }

    /** AppendEntries from nextIndex, or InstallSnapshot when the follower needs entries we have already compacted. */
    Message appendEntriesFor(int peer) {
        int next = nextIndex.computeIfAbsent(peer, p -> lastLogIndex() + 1);
        matchIndex.putIfAbsent(peer, 0);
        if (next <= storage.snapshotIndex) {
            snapshotsSent++;
            return Message.installSnapshot(id, peer, storage.currentTerm, storage.snapshot);
        }
        int prev = next - 1;
        List<LogEntry> entries = new ArrayList<>();
        for (int i = next; i <= lastLogIndex(); i++) entries.add(storage.get(i));
        return Message.appendEntries(id, peer, storage.currentTerm, prev, storage.termAt(prev), entries, commitIndex);
    }

    List<Message> onAppendEntries(Message m, int now) {
        if (m.term() < storage.currentTerm) return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
        if (role == Role.LEADER) throw new IllegalStateException("two leaders in term " + storage.currentTerm);
        if (role == Role.CANDIDATE) { role = Role.FOLLOWER; votesGranted.clear(); }
        leaderId = m.from();
        resetElectionTimer(now);
        if (m.prevLogIndex() < storage.snapshotIndex) {
            // Everything the leader is sending about is already inside my snapshot; tell it where I really am.
            return List.of(Message.appendReply(id, m.from(), storage.currentTerm, true, storage.snapshotIndex));
        }
        if (m.prevLogIndex() > lastLogIndex() || storage.termAt(m.prevLogIndex()) != m.prevLogTerm()) {
            return List.of(Message.appendReply(id, m.from(), storage.currentTerm, false, 0));
        }
        int index = m.prevLogIndex();
        for (LogEntry e : m.entries()) {
            index++;
            if (index <= lastLogIndex()) {
                if (storage.termAt(index) == e.term()) continue;
                tracer.accept(now, Main.name(id) + " conflict at index " + index + ": has " + storage.get(index) + ", leader has " + e + " -> truncating");
                storage.truncate(index);
            }
            storage.append(e);
        }
        int lastNew = m.prevLogIndex() + m.entries().size();
        if (m.leaderCommit() > commitIndex) {
            commitIndex = Math.min(m.leaderCommit(), lastNew);
            applyCommitted(now);
        }
        return List.of(Message.appendReply(id, m.from(), storage.currentTerm, true, lastNew));
    }

    List<Message> onAppendReply(Message m, int now) {
        if (role != Role.LEADER || m.term() != storage.currentTerm) return List.of();
        if (m.success()) {
            if (m.matchIndex() > matchIndex.getOrDefault(m.from(), 0)) {
                matchIndex.put(m.from(), m.matchIndex());
                nextIndex.put(m.from(), m.matchIndex() + 1);
                return advanceCommitIndex(now);
            }
            return List.of();
        }
        nextIndex.put(m.from(), Math.max(1, nextIndex.getOrDefault(m.from(), lastLogIndex() + 1) - 1));
        return List.of(appendEntriesFor(m.from()));
    }

    /** Figure 13, receiver implementation. Rule 6 keeps a log that already agrees at lastIncludedIndex; rule 7 discards it. */
    List<Message> onInstallSnapshot(Message m, int now) {
        if (m.term() < storage.currentTerm) return List.of(Message.snapshotReply(id, m.from(), storage.currentTerm, 0));
        if (role == Role.CANDIDATE) { role = Role.FOLLOWER; votesGranted.clear(); }
        leaderId = m.from();
        resetElectionTimer(now);
        Snapshot s = m.snapshot();
        if (s.lastIncludedIndex() <= storage.snapshotIndex) {
            return List.of(Message.snapshotReply(id, m.from(), storage.currentTerm, storage.snapshotIndex));   // already have it
        }
        snapshotsInstalled++;
        if (storage.has(s.lastIncludedIndex()) && storage.termAt(s.lastIncludedIndex()) == s.lastIncludedTerm()) {
            storage.compact(s);                                                                                   // 6
            tracer.accept(now, Main.name(id) + " installs snapshot through " + s.lastIncludedIndex() + ", keeps " + (lastLogIndex() - s.lastIncludedIndex()) + " later entries");
        } else {
            storage.replaceWithSnapshot(s);                                                                       // 7
            tracer.accept(now, Main.name(id) + " installs snapshot through " + s.lastIncludedIndex() + " (term " + s.lastIncludedTerm() + "), log discarded, config " + s.config());
        }
        if (lastApplied < s.lastIncludedIndex()) {                                                                // 8
            stateMachine.reset(s.data());
            lastApplied = s.lastIncludedIndex();
        }
        commitIndex = Math.max(commitIndex, s.lastIncludedIndex());
        return List.of(Message.snapshotReply(id, m.from(), storage.currentTerm, s.lastIncludedIndex()));
    }

    List<Message> onSnapshotReply(Message m, int now) {
        if (role != Role.LEADER || m.term() != storage.currentTerm) return List.of();
        if (m.matchIndex() > matchIndex.getOrDefault(m.from(), 0)) {
            matchIndex.put(m.from(), m.matchIndex());
            nextIndex.put(m.from(), m.matchIndex() + 1);
            return advanceCommitIndex(now);
        }
        return List.of();
    }

    /**
     * Figure 2's commit rule with a configuration-aware quorum. Returns messages because committing C_old,new makes the
     * leader append C_new immediately (section 6), and committing C_new may make it step down.
     */
    List<Message> advanceCommitIndex(int now) {
        for (int n = lastLogIndex(); n > commitIndex; n--) {
            if (storage.termAt(n) != storage.currentTerm) continue;
            Set<Integer> acks = new HashSet<>();
            acks.add(id);
            for (Map.Entry<Integer, Integer> e : matchIndex.entrySet()) if (e.getValue() >= n) acks.add(e.getKey());
            if (configuration().hasQuorum(acks)) {
                tracer.accept(now, Main.name(id) + " commits through index " + n + " (acks " + new TreeSet<>(acks) + " satisfy " + configuration() + ")");
                commitIndex = n;
                applyCommitted(now);
                return afterCommit(now);
            }
        }
        return List.of();
    }

    /**
     * Section 6: once C_old,new is committed, append C_new; once C_new is committed and I am not in it, step down. A joint
     * configuration whose entry has been compacted into the snapshot is committed by definition, so a new leader that
     * inherits a half-finished change (the old leader crashed between the two entries) finishes it here.
     */
    List<Message> afterCommit(int now) {
        Configuration c = configuration();
        int cfgIdx = storage.latestConfigIndex();
        boolean latestConfigCommitted = cfgIdx == -1 || cfgIdx <= commitIndex;
        if (c.isJoint() && latestConfigCommitted) {
            Configuration next = Configuration.simple(c.newMembers());
            tracer.accept(now, Main.name(id) + " C_old,new committed at " + cfgIdx + " -> appends C_new " + next);
            propose(next.encode());
            return replicateAll(now);
        }
        if (!c.isJoint() && latestConfigCommitted && !c.all().contains(id)) {
            tracer.accept(now, Main.name(id) + " C_new " + c + " committed and I am not in it -> step down");
            steppedDownAfterRemoval = true;
            List<Message> finalBeat = replicateAll(now);     // one last round so departing followers learn the commit index
            role = Role.FOLLOWER;
            leaderId = -1;
            return finalBeat;
        }
        return List.of();
    }

    void applyCommitted(int now) {
        while (lastApplied < commitIndex) {
            lastApplied++;
            stateMachine.apply(storage.get(lastApplied).command());
        }
        if (lastApplied - storage.snapshotIndex >= config.snapshotThreshold()) takeSnapshot(now);
    }

    /** Section 7: each server snapshots independently once enough applied entries have accumulated. */
    void takeSnapshot(int now) {
        Snapshot s = new Snapshot(lastApplied, storage.termAt(lastApplied), stateMachine.data, storage.configAt(lastApplied, bootstrap));
        storage.compact(s);
        snapshotsTaken++;
        tracer.accept(now, Main.name(id) + " snapshots through index " + s.lastIncludedIndex() + " (log now " + storage.firstIndex() + ".." + lastLogIndex() + ", config " + s.config() + ")");
    }
}

// ---------------------------------------------------------------------------------------------- network and cluster

/** Lesson 05's deterministic network: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes. */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Rng rng;
    private final double dropRate;
    private final int maxDelay;
    private final Map<Integer, Node> nodes = new TreeMap<>();
    private final PriorityQueue<Envelope> inFlight = new PriorityQueue<>(Comparator.comparingInt(Envelope::deliverAt).thenComparingLong(Envelope::seq));
    private final Set<Integer> down = new HashSet<>();
    private Set<Integer> sideA = Set.of();
    private boolean partitioned = false;
    private Runnable observer = () -> {};
    private int now = 0;
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;

    Network(long seed, double dropRate, int maxDelay) { this.rng = new Rng(seed); this.dropRate = dropRate; this.maxDelay = Math.max(1, maxDelay); }

    void add(Node node) { nodes.put(node.id(), node); }
    void setObserver(Runnable r) { observer = r; }
    int now() { return now; }
    boolean isDown(int id) { return down.contains(id); }
    void partition(Set<Integer> sideA) { this.sideA = Set.copyOf(sideA); partitioned = true; }
    void heal() { partitioned = false; sideA = Set.of(); }
    void crash(int id) { down.add(id); }
    void restart(Node node) { down.remove(node.id()); nodes.put(node.id(), node); }

    private boolean crossesPartition(int a, int b) { return partitioned && sideA.contains(a) != sideA.contains(b); }

    void send(Message m) {
        sent++;
        if (!nodes.containsKey(m.to()) || down.contains(m.from()) || down.contains(m.to()) || crossesPartition(m.from(), m.to())) { dropped++; return; }
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

/**
 * Wires `size` servers onto one network. Only `initialMembers` start in the configuration; the others are spare servers
 * that join via changeMembership. Checks Election Safety, Leader Completeness, Log Matching, and State Machine Safety
 * after every delivery.
 */
final class Cluster {
    final int size;
    final long seed;
    final Config config;
    final Network network;
    final Set<Integer> initialMembers;
    final List<Storage> storages = new ArrayList<>();
    final List<RaftNode> nodes = new ArrayList<>();
    final List<String> events = new ArrayList<>();
    final Map<Long, Integer> leadersByTerm = new TreeMap<>();
    final Map<Integer, LogEntry> committedByIndex = new TreeMap<>();
    final Map<Integer, String> appliedByIndex = new TreeMap<>();
    private final Map<Integer, Integer> appliedSeen = new HashMap<>();   // node -> highest index whose apply we recorded
    private final Set<Long> completenessChecked = new HashSet<>();
    private final int[] restarts;
    int proposals = 0;

    Cluster(int size, Set<Integer> initialMembers, long seed, double dropRate, int maxDelay, Config config) {
        this.size = size; this.seed = seed; this.config = config; this.initialMembers = new TreeSet<>(initialMembers);
        this.network = new Network(seed, dropRate, maxDelay);
        this.restarts = new int[size];
        Configuration boot = Configuration.simple(initialMembers);
        for (int i = 0; i < size; i++) {
            storages.add(new Storage());
            RaftNode n = new RaftNode(i, initialMembers.contains(i) ? boot : null, storages.get(i), nodeRng(seed, i, 0), config, 0, this::trace);
            nodes.add(n);
            network.add(n);
        }
        network.setObserver(this::checkInvariants);
    }

    Cluster(int size, long seed, double dropRate, int maxDelay) { this(size, allOf(size), seed, dropRate, maxDelay, Config.DEFAULT); }

    static Set<Integer> allOf(int n) { Set<Integer> s = new TreeSet<>(); for (int i = 0; i < n; i++) s.add(i); return s; }
    static Rng nodeRng(long seed, int id, int restart) { return new Rng(seed * 7919L + 1000003L * (id + 1) + 17L * restart); }

    void trace(int now, String text) { events.add(String.format("t=%-5d %s", now, text)); }
    int now() { return network.now(); }
    RaftNode node(int id) { return nodes.get(id); }
    void tick() { network.tick(); checkInvariants(); }
    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    int runUntilStableLeader(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) { tick(); if (stableLeader() != null) return now(); }
        return -1;
    }

    /** One live leader, and every live member of its configuration follows it in its term. */
    RaftNode stableLeader() {
        RaftNode leader = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id)) continue;
            if (n.role == Role.LEADER) { if (leader != null) return null; leader = n; }
        }
        if (leader == null) return null;
        for (int m : leader.configuration().all()) {
            RaftNode n = nodes.get(m);
            if (network.isDown(m) || n == leader) continue;
            if (n.currentTerm() != leader.currentTerm() || n.leaderId != leader.id) return null;
        }
        return leader;
    }

    /** The only live server claiming leadership, or null. Clients talk to whoever claims to lead; stability is a test-side notion. */
    RaftNode soleLeader() {
        RaftNode leader = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id) || n.role != Role.LEADER) continue;
            if (leader != null) return null;
            leader = n;
        }
        return leader;
    }

    int propose(String command) {
        RaftNode l = soleLeader();
        if (l == null) return -1;
        int index = l.propose(command);
        if (index > 0) { proposals++; network.sendAll(l.replicateAll(now())); }
        return index;
    }

    int changeMembership(Set<Integer> newMembers) {
        RaftNode l = soleLeader();
        if (l == null) return -1;
        int index = l.changeMembership(newMembers);
        if (index > 0) { trace(now(), Main.name(l.id) + " appends C_old,new " + l.configuration() + " at index " + index); network.sendAll(l.replicateAll(now())); }
        return index;
    }

    /** Run until every live member of the current configuration has applied through `index`. */
    int runUntilApplied(int index, int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            RaftNode l = stableLeader();
            if (l == null) continue;
            boolean all = true;
            for (int m : l.configuration().all()) if (!network.isDown(m) && nodes.get(m).lastApplied < index) all = false;
            if (all) return now();
        }
        return -1;
    }

    /** Run until the membership change is complete: latest config is simple, equals `members`, and is committed on the leader. */
    int runUntilConfigured(Set<Integer> members, int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            RaftNode l = stableLeader();
            if (l == null) continue;
            Configuration c = l.configuration();
            if (!c.isJoint() && c.all().equals(new TreeSet<>(members)) && l.storage.latestConfigIndex() <= l.commitIndex) return now();
        }
        return -1;
    }

    // ---------------------------------------------------------------- the safety checker (Figure 3)

    void checkInvariants() {
        for (RaftNode n : nodes) {
            if (n.role != Role.LEADER) continue;
            // Election Safety: at most one leader per term.
            Integer existing = leadersByTerm.putIfAbsent(n.currentTerm(), n.id);
            if (existing != null && existing != n.id) throw new IllegalStateException("ELECTION SAFETY: term " + n.currentTerm() + " has leaders " + existing + " and " + n.id);
            // Leader Completeness: every entry committed before this leader's term is in its log (or inside its snapshot).
            if (completenessChecked.add(n.currentTerm())) {
                for (Map.Entry<Integer, LogEntry> e : committedByIndex.entrySet()) {
                    int k = e.getKey();
                    if (k <= n.storage.snapshotIndex) continue;
                    if (!n.storage.has(k) || !n.storage.get(k).equals(e.getValue())) {
                        throw new IllegalStateException("LEADER COMPLETENESS: leader " + n.id + " of term " + n.currentTerm() + " lacks committed entry " + k + "=" + e.getValue());
                    }
                }
            }
        }
        // Record committed entries and check they never change (the committed prefix is immutable).
        for (RaftNode n : nodes) {
            for (int k = Math.max(1, n.storage.firstIndex()); k <= Math.min(n.commitIndex, n.lastLogIndex()); k++) {
                LogEntry seen = committedByIndex.putIfAbsent(k, n.storage.get(k));
                if (seen != null && !seen.equals(n.storage.get(k))) throw new IllegalStateException("STATE MACHINE SAFETY: index " + k + " committed as " + seen + " and " + n.storage.get(k) + " on " + n.id);
            }
            // State Machine Safety, directly: the command applied at index k is the same on every server.
            int from = appliedSeen.getOrDefault(n.id, 0);
            for (int k = from + 1; k <= n.lastApplied; k++) {
                if (!n.storage.has(k)) continue;                       // applied via snapshot: the snapshot's source recorded it
                String cmd = n.storage.get(k).command();
                String prior = appliedByIndex.putIfAbsent(k, cmd);
                if (prior != null && !prior.equals(cmd)) throw new IllegalStateException("STATE MACHINE SAFETY: index " + k + " applied as '" + prior + "' and '" + cmd + "'");
            }
            appliedSeen.put(n.id, n.lastApplied);
        }
        // Log Matching on the overlapping, uncompacted ranges.
        for (int i = 0; i < size; i++) for (int j = i + 1; j < size; j++) checkLogMatching(nodes.get(i).storage, nodes.get(j).storage, i, j);
    }

    static void checkLogMatching(Storage a, Storage b, int ia, int ib) {
        int low = Math.max(a.snapshotIndex, b.snapshotIndex) + 1;
        int high = Math.min(a.lastIndex(), b.lastIndex());
        for (int k = high; k >= low; k--) {
            if (a.termAt(k) == b.termAt(k)) {
                for (int i = low; i <= k; i++) {
                    if (!a.get(i).equals(b.get(i))) throw new IllegalStateException("LOG MATCHING: nodes " + ia + " and " + ib + " agree at " + k + " but differ at " + i);
                }
                return;
            }
        }
    }

    void partition(Set<Integer> sideA) { network.partition(sideA); trace(now(), "network partition: " + new TreeSet<>(sideA) + " | rest"); }
    void heal() { network.heal(); trace(now(), "network heals"); }
    void crash(int id) { network.crash(id); trace(now(), Main.name(id) + " crashes"); }

    RaftNode restart(int id) {
        restarts[id]++;
        Configuration boot = initialMembers.contains(id) ? Configuration.simple(initialMembers) : null;
        RaftNode n = new RaftNode(id, boot, storages.get(id), nodeRng(seed, id, restarts[id]), config, now(), this::trace);
        nodes.set(id, n);
        network.restart(n);
        trace(now(), Main.name(id) + " restarts: term " + n.currentTerm() + ", snapshot through " + n.storage.snapshotIndex + ", log to " + n.lastLogIndex() + ", config " + n.configuration());
        return n;
    }

    boolean stateMachinesConverged(Set<Integer> members) {
        String first = null;
        for (int m : members) {
            RaftNode n = nodes.get(m);
            if (network.isDown(m)) continue;
            String s = n.stateMachine.toString();
            if (first == null) first = s; else if (!first.equals(s)) return false;
        }
        return true;
    }

    String logOf(int id) {
        Storage s = nodes.get(id).storage;
        List<LogEntry> tail = new ArrayList<>();
        for (int i = s.firstIndex(); i <= s.lastIndex(); i++) tail.add(s.get(i));
        return (s.snapshotIndex > 0 ? "[snap<=" + s.snapshotIndex + "]" : "") + tail;
    }
}

public final class Main {
    static String name(int id) { return "n" + id; }
    static void printEvents(Cluster c, int from) { for (int i = from; i < c.events.size(); i++) System.out.println(c.events.get(i)); }

    static void printNodes(Cluster c) {
        for (RaftNode n : c.nodes) {
            System.out.printf("  %s %-9s term %d cfg=%-16s commit=%-3d applied=%-3d log=%s kv=%s%n", name(n.id), c.network.isDown(n.id) ? "DOWN" : n.role,
                    n.currentTerm(), n.configuration(), n.commitIndex, n.lastApplied, c.logOf(n.id), n.stateMachine);
        }
    }

    static int proposeUntilAccepted(Cluster c, String command) {
        for (int attempt = 0; attempt < 500; attempt++) {
            int index = c.propose(command);
            if (index > 0) return index;
            c.run(10);
        }
        throw new IllegalStateException("no leader accepted " + command);
    }

    static void changeUntilAccepted(Cluster c, Set<Integer> members) {
        for (int attempt = 0; attempt < 500; attempt++) {
            if (c.changeMembership(members) > 0) return;
            c.run(10);
        }
        throw new IllegalStateException("no leader accepted membership change to " + members);
    }

    public static void main(String[] args) {
        System.out.println("== Why you cannot just swap the configuration (Figure 10) ==");
        Set<Integer> three = Set.of(0, 1, 2), five = Set.of(0, 1, 2, 3, 4);
        System.out.println("C_old = " + new TreeSet<>(three) + " (majority 2), C_new = " + new TreeSet<>(five) + " (majority 3)");
        System.out.println("disjoint majorities possible with a direct switch? " + Configuration.disjointQuorumsPossible(three, five)
                + "   e.g. {0,1} is a majority of C_old and {2,3,4} is a majority of C_new: two leaders in one term");
        System.out.println("adding exactly one server, 3 -> 4: disjoint majorities possible? " + Configuration.disjointQuorumsPossible(three, Set.of(0, 1, 2, 3))
                + "   (majorities of 3 and of 4 always overlap: single-server changes need no joint phase, which is etcd's approach)");
        Configuration joint = Configuration.joint(three, five);
        System.out.println("joint C_old,new = " + joint + ": {0,1} has quorum? " + joint.hasQuorum(Set.of(0, 1)) + "; {2,3,4}? " + joint.hasQuorum(Set.of(2, 3, 4))
                + "; {0,1,3,4}? " + joint.hasQuorum(Set.of(0, 1, 3, 4)) + "  (needs a majority of BOTH)");

        System.out.println();
        System.out.println("== Grow a 3-server cluster to 5 with joint consensus, under client traffic (seed 7, delay 1-5) ==");
        Cluster c = new Cluster(5, Set.of(0, 1, 2), 7, 0.0, 5, new Config(150, 300, 50, 1000));
        c.runUntilStableLeader(2000);
        c.propose("SET alice 100");
        int idx = c.propose("SET bob 50");
        c.runUntilApplied(idx, 1000);
        int before = c.events.size();
        changeUntilAccepted(c, five);
        c.propose("SET carol 7");                       // traffic continues during the change
        int done = c.runUntilConfigured(five, 3000);
        c.propose("SET dave 1");
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 2000);
        printEvents(c, before);
        System.out.println("configuration " + c.stableLeader().configuration() + " committed at tick " + done + "; all five state machines agree = " + c.stateMachinesConverged(five));
        printNodes(c);

        System.out.println();
        System.out.println("== Remove the leader: it commits C_new without counting itself, then steps down ==");
        RaftNode leader = c.stableLeader();
        Set<Integer> withoutLeader = new TreeSet<>(five);
        withoutLeader.remove(leader.id);
        before = c.events.size();
        changeUntilAccepted(c, withoutLeader);
        c.runUntilConfigured(withoutLeader, 3000);
        c.runUntilStableLeader(3000);
        proposeUntilAccepted(c, "SET erin 9");
        c.runUntilApplied(c.stableLeader().lastLogIndex(), 2000);
        printEvents(c, before);
        System.out.println("old leader " + name(leader.id) + " is now " + leader.role + " (steppedDownAfterRemoval=" + leader.steppedDownAfterRemoval
                + "); new leader " + name(c.stableLeader().id) + " of term " + c.stableLeader().currentTerm() + " in " + c.stableLeader().configuration());

        System.out.println();
        System.out.println("== Snapshots: compaction every 8 applied entries; a follower that falls behind gets InstallSnapshot ==");
        Cluster s = new Cluster(3, 11, 0.0, 5);          // snapshotThreshold 8 from Config.DEFAULT
        s.runUntilStableLeader(2000);
        for (int k = 1; k <= 5; k++) s.propose("SET k" + k + " v" + k);
        s.runUntilApplied(s.stableLeader().lastLogIndex(), 1000);
        RaftNode lead = s.stableLeader();
        int follower = (lead.id + 1) % 3;
        before = s.events.size();
        s.crash(follower);
        for (int k = 6; k <= 16; k++) { s.propose("SET k" + k + " v" + k); s.run(20); }
        s.run(200);
        System.out.println("while " + name(follower) + " is down, the leader applies and compacts:");
        printEvents(s, before);
        System.out.println("  leader log: " + s.logOf(lead.id) + " (snapshots taken " + lead.snapshotsTaken + ")");
        before = s.events.size();
        s.restart(follower);
        s.runUntilApplied(lead.lastLogIndex(), 2000);
        printEvents(s, before);
        System.out.println("after restart: " + name(follower) + " log=" + s.logOf(follower) + " kv size=" + s.node(follower).stateMachine.data.size()
                + "; snapshots sent by leader=" + lead.snapshotsSent + "; state machines converged=" + s.stateMachinesConverged(Set.of(0, 1, 2)));

        System.out.println();
        System.out.println("== Safety checker over 12 seeds: 20% drops, delay 1-10, membership 3->5->4, crash+restart, snapshots every 8 entries ==");
        int converged = 0, snapshotsInstalled = 0, changesCompleted = 0;
        for (long seed = 1; seed <= 12; seed++) {
            Cluster r = new Cluster(5, Set.of(0, 1, 2), seed, 0.2, 10, Config.DEFAULT);
            r.runUntilStableLeader(4000);
            for (int k = 1; k <= 5; k++) { proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            changeUntilAccepted(r, five);
            for (int k = 6; k <= 10; k++) { proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            if (r.runUntilConfigured(five, 6000) > 0) changesCompleted++;
            RaftNode victim = r.stableLeader() != null ? r.stableLeader() : r.node(0);
            r.crash(victim.id);
            r.runUntilStableLeader(4000);
            for (int k = 11; k <= 18; k++) { proposeUntilAccepted(r, "SET k" + k + " v" + k); r.run(30); }
            r.restart(victim.id);
            r.runUntilStableLeader(4000);
            Set<Integer> four = new TreeSet<>(five);
            four.remove((victim.id + 1) % 5);
            changeUntilAccepted(r, four);
            if (r.runUntilConfigured(four, 6000) > 0) changesCompleted++;
            proposeUntilAccepted(r, "SET final yes");
            RaftNode fin = r.stableLeader();
            if (fin != null) r.runUntilApplied(fin.lastLogIndex(), 4000);
            for (RaftNode n : r.nodes) snapshotsInstalled += n.snapshotsInstalled;
            if (fin != null && r.stateMachinesConverged(four) && fin.stateMachine.get("final") != null && fin.stateMachine.get("k18") != null) converged++;
        }
        System.out.println("seeds converged on all writes in the final configuration: " + converged + "/12; membership changes completed: " + changesCompleted
                + "/24; InstallSnapshot handled: " + snapshotsInstalled);
        System.out.println("no Election Safety, Leader Completeness, Log Matching, or State Machine Safety check fired (each would have thrown)");
    }
}
