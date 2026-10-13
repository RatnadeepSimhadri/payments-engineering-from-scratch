// Lesson: phases/06-replication-and-consensus/05-raft-leader-election/docs/en.md
// Raft I: Terms, Leader Election, and a Simulated Network
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2;
//          Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, Stanford 2014) for Pre-Vote.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.function.BiConsumer;

/**
 * SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces the same
 * election timeouts, delays, and drops in Java, Python, and Go: a bug found in one language replays in all three.
 */
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

    /** Uniform integer in [0, bound). Uses the top 31 bits so the result is identical across languages. */
    int nextInt(int bound) { return (int) ((nextLong() >>> 33) % bound); }

    /** Uniform double in [0, 1) built from the top 53 bits. */
    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }
}

enum Role { FOLLOWER, CANDIDATE, LEADER }

/** Raft has two RPCs. On a simulated network each RPC is two one-way messages: the request and its reply. */
enum MessageKind { REQUEST_VOTE, REQUEST_VOTE_REPLY, APPEND_ENTRIES, APPEND_ENTRIES_REPLY }

/**
 * One message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the term).
 * lastLogIndex/lastLogTerm are the candidate's, used by the receiver's "at least as up-to-date" check (section 5.4.1).
 * granted doubles as "success" on an AppendEntries reply.
 */
record Message(MessageKind kind, int from, int to, long term, int lastLogIndex, int lastLogTerm, boolean granted) {
    static Message requestVote(int from, int to, long term, int lastLogIndex, int lastLogTerm) {
        return new Message(MessageKind.REQUEST_VOTE, from, to, term, lastLogIndex, lastLogTerm, false);
    }

    static Message voteReply(int from, int to, long term, boolean granted) {
        return new Message(MessageKind.REQUEST_VOTE_REPLY, from, to, term, 0, 0, granted);
    }

    /** Lesson 05 sends AppendEntries with no entries: a heartbeat. Lesson 06 fills it with log entries. */
    static Message heartbeat(int from, int to, long term) {
        return new Message(MessageKind.APPEND_ENTRIES, from, to, term, 0, 0, false);
    }

    static Message heartbeatReply(int from, int to, long term, boolean success) {
        return new Message(MessageKind.APPEND_ENTRIES_REPLY, from, to, term, 0, 0, success);
    }
}

/**
 * Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)".
 * In this simulation a Storage object survives a crash; everything else on the node is lost.
 * votedFor == -1 means null.
 */
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    int writes = 0;

    void save(long term, int votedFor) {
        this.currentTerm = term;
        this.votedFor = votedFor;
        writes++;
    }
}

/** Timing in ticks. The paper's recommendation: broadcastTime << electionTimeout << MTBF; defaults mirror its 150-300 ms. */
record Config(int electionTimeoutMin, int electionTimeoutMax, int heartbeatInterval) {
    static final Config DEFAULT = new Config(150, 300, 50);

    Config {
        if (electionTimeoutMin <= 0 || electionTimeoutMax < electionTimeoutMin || heartbeatInterval <= 0) {
            throw new IllegalArgumentException("bad timing config");
        }
        if (heartbeatInterval >= electionTimeoutMin) {
            throw new IllegalArgumentException("heartbeat interval must be well below the election timeout");
        }
    }
}

/** A participant on the simulated network. */
interface Node {
    int id();

    /** Called once per tick with the current time; returns messages to send. */
    List<Message> tick(int now);

    /** Called once per delivered message; returns replies to send. */
    List<Message> handle(Message m, int now);
}

/**
 * One Raft server, election rules only (Figure 2 minus the log). The log is represented by lastLogIndex/lastLogTerm so
 * the "at least as up-to-date" vote check can be built and tested now; Lesson 06 replaces the two fields with a real log.
 */
final class RaftNode implements Node {
    final int id;
    final List<Integer> peers;
    final Storage storage;
    private final Rng rng;
    private final Config config;
    private final BiConsumer<Integer, String> tracer;

    Role role = Role.FOLLOWER;
    int leaderId = -1;
    int lastLogIndex = 0;
    int lastLogTerm = 0;
    private final Set<Integer> votesGranted = new HashSet<>();
    int electionDeadline;
    int nextHeartbeat;
    int electionsStarted = 0;

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
    int votes() { return votesGranted.size(); }

    /** Randomized timeout in [min, max]: the paper's whole answer to split votes (section 5.2). */
    void resetElectionTimer(int now) {
        int span = config.electionTimeoutMax() - config.electionTimeoutMin() + 1;
        electionDeadline = now + config.electionTimeoutMin() + rng.nextInt(span);
    }

    @Override
    public List<Message> tick(int now) {
        if (role == Role.LEADER) {
            return now >= nextHeartbeat ? sendHeartbeats(now) : List.of();
        }
        // Followers: election timeout elapses without AppendEntries from current leader or granting a vote -> candidate.
        // Candidates: election timeout elapses -> start a new election.
        return now >= electionDeadline ? startElection(now) : List.of();
    }

    /** Figure 2, Candidates, "On conversion to candidate". */
    List<Message> startElection(int now) {
        role = Role.CANDIDATE;
        storage.save(storage.currentTerm + 1, id);   // increment currentTerm, vote for self, persist before sending
        leaderId = -1;
        votesGranted.clear();
        votesGranted.add(id);
        electionsStarted++;
        resetElectionTimer(now);
        tracer.accept(now, Main.name(id) + " election timeout -> candidate for term " + storage.currentTerm + ", votes for itself");
        List<Message> out = new ArrayList<>();
        for (int p : peers) out.add(Message.requestVote(id, p, storage.currentTerm, lastLogIndex, lastLogTerm));
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
            case APPEND_ENTRIES_REPLY -> List.of();   // Lesson 06 uses replies to drive nextIndex/matchIndex
        };
    }

    void becomeFollower(long term, int now) {
        Role before = role;
        role = Role.FOLLOWER;
        leaderId = -1;
        votesGranted.clear();
        if (term > storage.currentTerm) {
            storage.save(term, -1);   // a new term means votedFor is null again
            tracer.accept(now, Main.name(id) + " sees term " + term + " > its own -> follower"
                    + (before == Role.LEADER ? " (was LEADER)" : before == Role.CANDIDATE ? " (was candidate)" : ""));
        }
        // A leader keeps no election timer running, so give it a fresh one when it steps down.
        if (before == Role.LEADER) resetElectionTimer(now);
    }

    /** Figure 2, RequestVote RPC, "Receiver implementation". */
    List<Message> onRequestVote(Message m, int now) {
        if (m.term() < storage.currentTerm) {   // 1. Reply false if term < currentTerm
            return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
        }
        // 2. If votedFor is null or candidateId, and candidate's log is at least as up-to-date as receiver's log, grant vote
        boolean canVote = storage.votedFor == -1 || storage.votedFor == m.from();
        boolean logUpToDate = m.lastLogTerm() > lastLogTerm
                || (m.lastLogTerm() == lastLogTerm && m.lastLogIndex() >= lastLogIndex);
        if (canVote && logUpToDate) {
            storage.save(storage.currentTerm, m.from());   // persist the vote before the reply leaves
            resetElectionTimer(now);                      // granting a vote resets the election timer
            tracer.accept(now, Main.name(id) + " grants vote to " + Main.name(m.from()) + " for term " + m.term());
            return List.of(Message.voteReply(id, m.from(), storage.currentTerm, true));
        }
        return List.of(Message.voteReply(id, m.from(), storage.currentTerm, false));
    }

    List<Message> onVoteReply(Message m, int now) {
        // A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
        if (role != Role.CANDIDATE || m.term() != storage.currentTerm) return List.of();
        if (m.granted()) {
            votesGranted.add(m.from());
            if (votesGranted.size() >= majority()) return becomeLeader(now);
        }
        return List.of();
    }

    List<Message> becomeLeader(int now) {
        role = Role.LEADER;
        leaderId = id;
        tracer.accept(now, Main.name(id) + " wins " + votesGranted.size() + "/" + clusterSize() + " votes -> LEADER of term " + storage.currentTerm);
        return sendHeartbeats(now);   // Leaders: upon election send initial empty AppendEntries to each server
    }

    List<Message> sendHeartbeats(int now) {
        nextHeartbeat = now + config.heartbeatInterval();
        List<Message> out = new ArrayList<>();
        for (int p : peers) out.add(Message.heartbeat(id, p, storage.currentTerm));
        return out;
    }

    /** Figure 2, AppendEntries RPC, receiver rules 1 and the Candidates rule "if AppendEntries received from new leader: convert to follower". */
    List<Message> onAppendEntries(Message m, int now) {
        if (m.term() < storage.currentTerm) {
            return List.of(Message.heartbeatReply(id, m.from(), storage.currentTerm, false));
        }
        if (role == Role.LEADER) {
            throw new IllegalStateException("two leaders in term " + storage.currentTerm + ": " + id + " and " + m.from());
        }
        if (role == Role.CANDIDATE) {
            role = Role.FOLLOWER;
            votesGranted.clear();
            tracer.accept(now, Main.name(id) + " hears leader " + Main.name(m.from()) + " for term " + m.term() + " -> candidate steps down");
        }
        leaderId = m.from();
        resetElectionTimer(now);
        return List.of(Message.heartbeatReply(id, m.from(), storage.currentTerm, true));
    }
}

/**
 * Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
 * (deliverAt, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
 * explicit operations, which is what makes every bug replayable. No threads, no sleeping.
 */
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

    /** Split the cluster into sideA and everyone else; messages across the cut are dropped. */
    void partition(Set<Integer> sideA) { this.sideA = Set.copyOf(sideA); partitioned = true; }
    void heal() { partitioned = false; sideA = Set.of(); }

    /** A crashed node receives nothing, sends nothing, and does not tick. In-flight messages to it are lost. */
    void crash(int id) { down.add(id); }

    /** Bring a node back with fresh volatile state (the caller rebuilds it from its Storage). */
    void restart(Node node) { down.remove(node.id()); nodes.put(node.id(), node); }

    private boolean crossesPartition(int a, int b) { return partitioned && sideA.contains(a) != sideA.contains(b); }

    void send(Message m) {
        sent++;
        if (down.contains(m.from()) || down.contains(m.to()) || crossesPartition(m.from(), m.to())) { dropped++; return; }
        double roll = rng.nextDouble();            // always draw both numbers so the rng stream is identical across languages
        int delay = 1 + rng.nextInt(maxDelay);
        if (roll < dropRate) { dropped++; return; }
        inFlight.add(new Envelope(now + delay, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    /** Advance the clock one tick: let every live node act on the time, then deliver everything that is due. */
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

/** Wires N RaftNodes onto one network, drives the clock, and checks Election Safety after every delivery. */
final class Cluster {
    final int size;
    final long seed;
    final Config config;
    final Network network;
    final List<Storage> storages = new ArrayList<>();
    final List<RaftNode> nodes = new ArrayList<>();
    final List<String> events = new ArrayList<>();
    final Map<Long, Integer> leadersByTerm = new TreeMap<>();
    private final int[] restarts;

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
        network.setObserver(this::checkElectionSafety);
    }

    Cluster(int size, long seed, double dropRate, int maxDelay) { this(size, seed, dropRate, maxDelay, Config.DEFAULT); }

    List<Integer> peersOf(int id) {
        List<Integer> ps = new ArrayList<>();
        for (int j = 0; j < size; j++) if (j != id) ps.add(j);
        return ps;
    }

    /** Each node gets its own rng stream derived from the seed, so the network's and the nodes' draws never interleave. */
    static Rng nodeRng(long seed, int id, int restart) { return new Rng(seed * 7919L + 1000003L * (id + 1) + 17L * restart); }

    void trace(int now, String text) { events.add(String.format("t=%-4d %s", now, text)); }

    int now() { return network.now(); }
    RaftNode node(int id) { return nodes.get(id); }

    void tick() { network.tick(); checkElectionSafety(); }

    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    /** Run until exactly one live node is a leader and every other live node follows it (same term). Returns the tick, or -1. */
    int runUntilStableLeader(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            if (stableLeader() != null) return now();
        }
        return -1;
    }

    /** The leader whose term every live, reachable node agrees on, or null. */
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

    long maxTerm() {
        long t = 0;
        for (RaftNode n : nodes) t = Math.max(t, n.currentTerm());
        return t;
    }

    /** Election Safety (Figure 3): at most one leader can be elected in a given term. */
    void checkElectionSafety() {
        for (RaftNode n : nodes) {
            if (n.role != Role.LEADER) continue;
            Integer existing = leadersByTerm.putIfAbsent(n.currentTerm(), n.id);
            if (existing != null && existing != n.id) {
                throw new IllegalStateException("ELECTION SAFETY VIOLATED: term " + n.currentTerm() + " has leaders " + existing + " and " + n.id);
            }
        }
    }

    void partition(Set<Integer> sideA) {
        network.partition(sideA);
        trace(now(), "network partition: " + new java.util.TreeSet<>(sideA) + " | rest");
    }

    void heal() { network.heal(); trace(now(), "network heals"); }

    void crash(int id) { network.crash(id); trace(now(), Main.name(id) + " crashes (volatile state lost, Storage kept)"); }

    /** Restart from stable storage: currentTerm and votedFor survive; role, leaderId, votes do not. */
    RaftNode restart(int id) {
        restarts[id]++;
        RaftNode n = new RaftNode(id, peersOf(id), storages.get(id), nodeRng(seed, id, restarts[id]), config, now(), this::trace);
        nodes.set(id, n);
        network.restart(n);
        trace(now(), Main.name(id) + " restarts as follower, term " + n.currentTerm() + ", votedFor "
                + (n.votedFor() < 0 ? "null" : Main.name(n.votedFor())));
        return n;
    }

    /** Force a node's election timer to expire on the next tick (for scripted scenarios). */
    void expireTimer(int id) { nodes.get(id).electionDeadline = now(); }
}

public final class Main {
    static String name(int id) { return "n" + id; }

    static void printEvents(Cluster c, int from) {
        for (int i = from; i < c.events.size(); i++) System.out.println(c.events.get(i));
    }

    public static void main(String[] args) {
        System.out.println("== Election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==");
        System.out.println("t=0    all nodes are followers in term 0; election timeouts drawn from [150, 300] ticks, heartbeat every 50");
        Cluster c = new Cluster(5, 7, 0.0, 5);
        int electedAt = c.runUntilStableLeader(2000);
        printEvents(c, 0);
        RaftNode leader = c.stableLeader();
        System.out.printf("leader %s, term %d, stable at tick %d; messages sent=%d delivered=%d dropped=%d%n",
                name(leader.id), leader.currentTerm(), electedAt, c.network.sent, c.network.delivered, c.network.dropped);
        int before = c.events.size();
        c.run(1000);
        System.out.printf("after 1000 more ticks of heartbeats: term still %d, %d new events, leader unchanged = %b%n",
                c.maxTerm(), c.events.size() - before, c.stableLeader() == leader);

        System.out.println();
        System.out.println("== Partition: isolate the leader with one follower ==");
        int follower = (leader.id + 1) % 5;
        before = c.events.size();
        c.partition(Set.of(leader.id, follower));
        c.run(700);
        printEvents(c, before);
        RaftNode newest = c.leaders().get(0);
        for (RaftNode l : c.leaders()) if (l.currentTerm() > newest.currentTerm()) newest = l;
        System.out.printf("minority side: %s still believes it leads term %d; majority side elected %s for term %d (%d leaders alive, different terms)%n",
                name(leader.id), leader.currentTerm(), name(newest.id), newest.currentTerm(), c.leaders().size());
        before = c.events.size();
        c.heal();
        int healedAt = c.runUntilStableLeader(2000);
        printEvents(c, before);
        RaftNode after = c.stableLeader();
        System.out.printf("after heal: single leader %s in term %d at tick %d; old leader %s is now %s%n",
                name(after.id), after.currentTerm(), healedAt, name(leader.id), leader.role);

        System.out.println();
        System.out.println("== Split vote on a 4-node cluster: first seed whose first leader needs more than one term ==");
        for (long seed = 1; seed <= 500; seed++) {
            Cluster s = new Cluster(4, seed, 0.0, 3);
            s.runUntilStableLeader(3000);
            RaftNode l = s.stableLeader();
            if (l != null && l.currentTerm() > 1) {
                System.out.println("seed " + seed + ":");
                printEvents(s, 0);
                break;
            }
        }

        System.out.println();
        System.out.println("== votedFor must be persisted ==");
        Storage durable = new Storage();
        RaftNode n1 = new RaftNode(1, List.of(0, 2), durable, new Rng(1), Config.DEFAULT, 0, (t, s) -> {});
        Message r1 = n1.handle(Message.requestVote(0, 1, 1, 0, 0), 10).get(0);
        System.out.printf("n1 receives RequestVote(term 1) from n0 -> granted=%b, votedFor=%s%n", r1.granted(), name(n1.votedFor()));
        RaftNode n1b = new RaftNode(1, List.of(0, 2), durable, new Rng(2), Config.DEFAULT, 11, (t, s) -> {});
        Message r2 = n1b.handle(Message.requestVote(2, 1, 1, 0, 0), 12).get(0);
        System.out.printf("n1 crashes and restarts from Storage (term %d, votedFor %s); RequestVote(term 1) from n2 -> granted=%b%n",
                n1b.currentTerm(), name(n1b.votedFor()), r2.granted());
        RaftNode n1c = new RaftNode(1, List.of(0, 2), new Storage(), new Rng(3), Config.DEFAULT, 11, (t, s) -> {});
        Message r3 = n1c.handle(Message.requestVote(2, 1, 1, 0, 0), 12).get(0);
        System.out.printf("counterfactual, votedFor lost on restart: RequestVote(term 1) from n2 -> granted=%b  (two votes in one term: Election Safety breaks)%n", r3.granted());

        System.out.println();
        System.out.println("== 30 seeds, 5 nodes, 20% drops, delay 1-10, 3000 ticks each: Election Safety holds ==");
        int elected = 0, worst = 0;
        long terms = 0;
        for (long seed = 1; seed <= 30; seed++) {
            Cluster s = new Cluster(5, seed, 0.2, 10);
            int t = s.runUntilStableLeader(3000);
            if (t > 0) { elected++; worst = Math.max(worst, t); }
            s.run(3000 - Math.max(t, 0));
            terms += s.leadersByTerm.size();
        }
        System.out.printf("seeds with a stable leader: %d/30, slowest election at tick %d, %d (term, leader) pairs recorded, never two leaders in one term%n",
                elected, worst, terms);
    }
}
