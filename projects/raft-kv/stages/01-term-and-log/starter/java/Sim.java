// Project: projects/raft-kv — deterministic simulation substrate (PROVIDED; do not edit).
// Clock, seeded Rng, message types, Config, Node interface, and the Network with drop/delay/partition/crash.
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2.
// Java 21, standard library only. Python port in solution/python; Go port in solution/go.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.function.BiConsumer;

/** The injected clock. Time is an integer tick count; nothing in the project reads a wall clock. */
final class Clock {
    private int now = 0;

    int now() { return now; }

    void advance() { now++; }
}

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

/** One log entry: the term in which the leader created it and the client command. Its index is its position in the log. */
record LogEntry(long term, String command) {
    @Override public String toString() { return "t" + term + ":" + command; }
}

/**
 * One message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the term).
 * REQUEST_VOTE: prevLogIndex/prevLogTerm carry the candidate's last log index and term (section 5.4.1).
 * APPEND_ENTRIES: prevLogIndex/prevLogTerm name the entry immediately before `entries` (the consistency check);
 * leaderCommit is the leader's commitIndex. `success` doubles as voteGranted; `matchIndex` on a successful
 * APPEND_ENTRIES_REPLY tells the leader how far the follower's log now matches.
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

/** Receives one line per interesting event, with the tick at which it happened. */
interface Tracer extends BiConsumer<Integer, String> {
    Tracer NONE = (now, text) -> {};
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
 * Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
 * (deliverAt, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
 * explicit operations, which is what makes every bug replayable. No threads, no sleeping.
 */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Clock clock;
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
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;

    Network(Clock clock, long seed, double dropRate, int maxDelay) {
        this.clock = clock;
        this.rng = new Rng(seed);
        this.dropRate = dropRate;
        this.maxDelay = Math.max(1, maxDelay);
    }

    void add(Node node) { nodes.put(node.id(), node); }
    void setObserver(Runnable r) { observer = r; }
    int now() { return clock.now(); }
    int inFlight() { return inFlight.size(); }
    boolean isDown(int id) { return down.contains(id); }
    boolean isPartitioned() { return partitioned; }

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
        inFlight.add(new Envelope(clock.now() + delay, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    /** Advance the clock one tick: let every live node act on the time, then deliver everything that is due. */
    void tick() {
        clock.advance();
        int now = clock.now();
        for (Node n : new ArrayList<>(nodes.values())) if (!down.contains(n.id())) sendAll(n.tick(now));
        while (!inFlight.isEmpty() && inFlight.peek().deliverAt() <= now) deliver(inFlight.poll());
    }

    private void deliver(Envelope e) {
        Message m = e.message();
        if (down.contains(m.to()) || crossesPartition(m.from(), m.to())) { dropped++; return; }
        delivered++;
        sendAll(nodes.get(m.to()).handle(m, clock.now()));
        observer.run();
    }
}
