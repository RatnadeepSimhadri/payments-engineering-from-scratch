// Lesson: phases/05-distributed-foundations/13-gossip-and-failure-detection/docs/en.md
// Gossip Protocols and Failure Detection
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Das, Gupta, Motivala, "SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol" (DSN 2002),
//          sections 3 (failure detector), 4.1 (infection-style dissemination), 4.2 (suspicion mechanism), 4.3 (round-robin probing).
// Java 21, standard library only. Deterministic simulated network: seeded delays, drops, partitions, crashes. Nothing sleeps.

import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

/** SplitMix64: the same stream in every language, so a seed replays everywhere. */
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

    /** Fisher-Yates with this generator, so permutations replay with the seed. */
    <T> void shuffle(List<T> list) {
        for (int i = list.size() - 1; i > 0; i--) Collections.swap(list, i, nextInt(i + 1));
    }
}

/** Membership states. DEAD is terminal for an incarnation; a node that comes back must rejoin with a fresh identity in production. */
enum MemberState { ALIVE, SUSPECT, DEAD }

/**
 * One piece of membership information: node, state, incarnation number. The incarnation is owned by the node it describes;
 * only that node may increase it, and it does so to refute a suspicion. Precedence rules (SWIM section 4.2):
 *   alive(i)   overrides alive(j), suspect(j)  when i > j
 *   suspect(i) overrides alive(j)              when i >= j
 *   suspect(i) overrides suspect(j)            when i > j
 *   dead(any)  overrides everything
 */
record Update(int member, MemberState state, long incarnation) {
    boolean overrides(Update current) {
        if (current == null) return true;
        if (current.state == MemberState.DEAD) return false;
        if (state == MemberState.DEAD) return true;
        if (state == MemberState.ALIVE) return incarnation > current.incarnation;
        // SUSPECT
        if (current.state == MemberState.ALIVE) return incarnation >= current.incarnation;
        return incarnation > current.incarnation;
    }

    @Override public String toString() { return "n" + member + ":" + state + "@" + incarnation; }
}

enum MessageKind { PING, ACK, PING_REQ, INDIRECT_ACK }

/**
 * One protocol message. PING/ACK carry a probe sequence number so an ack can be matched to its probe. PING_REQ asks the
 * receiver to ping `target` on the sender's behalf; the resulting INDIRECT_ACK is relayed back. Every message piggybacks
 * a bounded list of membership updates, which is the entire dissemination mechanism: there is no separate broadcast.
 */
record Message(MessageKind kind, int from, int to, int target, long seq, List<Update> piggyback) {
    static Message ping(int from, int to, long seq, List<Update> gossip) { return new Message(MessageKind.PING, from, to, to, seq, List.copyOf(gossip)); }
    static Message ack(int from, int to, long seq, List<Update> gossip) { return new Message(MessageKind.ACK, from, to, from, seq, List.copyOf(gossip)); }
    static Message pingReq(int from, int to, int target, long seq, List<Update> gossip) { return new Message(MessageKind.PING_REQ, from, to, target, seq, List.copyOf(gossip)); }
    static Message indirectAck(int from, int to, int target, long seq, List<Update> gossip) { return new Message(MessageKind.INDIRECT_ACK, from, to, target, seq, List.copyOf(gossip)); }
}

interface Node {
    int id();
    List<Message> tick(int now);
    List<Message> handle(Message m, int now);
}

/** Timing of one protocol period, in ticks. Defaults: probe every 12 ticks, wait 4 for a direct ack, suspect for 4 periods. */
record Config(int protocolPeriod, int ackTimeout, int indirectProbes, int suspicionPeriods, int maxPiggyback, int retransmitFactor) {
    static final Config DEFAULT = new Config(12, 4, 3, 4, 6, 3);

    Config {
        if (ackTimeout <= 0 || ackTimeout * 2 >= protocolPeriod) throw new IllegalArgumentException("need ackTimeout*2 < protocolPeriod so the indirect probe fits in the period");
        if (indirectProbes < 0 || suspicionPeriods <= 0 || maxPiggyback <= 0 || retransmitFactor <= 0) throw new IllegalArgumentException("bad config");
    }
}

/** A pending direct probe: who we pinged, when the direct ack is due, whether we already escalated to indirect probing. */
final class Probe {
    final int target;
    final long seq;
    final int startedAt;
    boolean indirectSent = false;
    boolean acked = false;
    Probe(int target, long seq, int startedAt) { this.target = target; this.seq = seq; this.startedAt = startedAt; }
}

/** One SWIM member. Holds its view of the group, runs the probe cycle, and gossips updates by piggybacking. */
final class SwimNode implements Node {
    final int id;
    private final Config config;
    private final Rng rng;
    private final Tracer tracer;

    /** My incarnation number. Bumped only to refute a suspicion about myself. */
    long incarnation = 0;
    /** What I believe about every member I know of, including myself. */
    final Map<Integer, Update> view = new TreeMap<>();
    /** When each SUSPECT entry was first suspected locally, for the suspicion timeout. */
    final Map<Integer, Integer> suspectedAt = new HashMap<>();
    /** Updates still being disseminated, with how many times each has been piggybacked. */
    private final LinkedHashMap<Update, Integer> gossipQueue = new LinkedHashMap<>();

    private List<Integer> probeOrder = new ArrayList<>();
    private int probeCursor = 0;
    private int nextPeriodAt;
    private long nextSeq;
    private Probe current = null;
    /** Indirect probes we are relaying for others: seq -> origin. */
    private final Map<Long, Integer> relaying = new HashMap<>();

    int messagesSent = 0, directAcks = 0, indirectAcks = 0, suspicionsRaised = 0, refutations = 0, declaredDead = 0;

    interface Tracer { void trace(int now, String text); }

    SwimNode(int id, List<Integer> initialMembers, Config config, Rng rng, int now, Tracer tracer) {
        this.id = id; this.config = config; this.rng = rng; this.tracer = tracer;
        for (int m : initialMembers) view.put(m, new Update(m, MemberState.ALIVE, 0));
        view.put(id, new Update(id, MemberState.ALIVE, 0));
        this.nextPeriodAt = now + 1 + rng.nextInt(config.protocolPeriod());   // desynchronise the periods across nodes
        this.nextSeq = ((long) id << 32) + 1;                                 // probe seqs are unique across the whole group
    }

    @Override public int id() { return id; }

    MemberState stateOf(int member) { Update u = view.get(member); return u == null ? null : u.state(); }

    Set<Integer> aliveMembers() {
        Set<Integer> s = new TreeSet<>();
        for (Update u : view.values()) if (u.state() == MemberState.ALIVE) s.add(u.member());
        return s;
    }

    Set<Integer> deadMembers() {
        Set<Integer> s = new TreeSet<>();
        for (Update u : view.values()) if (u.state() == MemberState.DEAD) s.add(u.member());
        return s;
    }

    // ---------------------------------------------------------------- the protocol period (section 3, 4.3)

    @Override
    public List<Message> tick(int now) {
        List<Message> out = new ArrayList<>();
        if (current != null && !current.acked && !current.indirectSent && now >= current.startedAt + config.ackTimeout()) {
            out.addAll(sendIndirectProbes(now));
        }
        if (now >= nextPeriodAt) {
            nextPeriodAt = now + config.protocolPeriod();
            finishProbe(now);
            expireSuspicions(now);
            Integer target = nextProbeTarget();
            if (target != null) {
                current = new Probe(target, nextSeq++, now);
                out.add(Message.ping(id, target, current.seq, takeGossip()));
            }
        }
        messagesSent += out.size();
        return out;
    }

    /** Round-robin over a random permutation of members, reshuffled each pass (section 4.3): bounded worst-case detection time. */
    Integer nextProbeTarget() {
        List<Integer> candidates = new ArrayList<>();
        for (Update u : view.values()) if (u.member() != id && u.state() != MemberState.DEAD) candidates.add(u.member());
        if (candidates.isEmpty()) return null;
        if (probeCursor >= probeOrder.size() || !new HashSet<>(candidates).containsAll(probeOrder)) {
            probeOrder = candidates;
            rng.shuffle(probeOrder);
            probeCursor = 0;
        }
        return probeOrder.get(probeCursor++);
    }

    /** No direct ack in time: ask k other members to ping the target for us (section 3, step 2). */
    List<Message> sendIndirectProbes(int now) {
        current.indirectSent = true;
        List<Integer> helpers = new ArrayList<>();
        for (Update u : view.values()) if (u.member() != id && u.member() != current.target && u.state() == MemberState.ALIVE) helpers.add(u.member());
        rng.shuffle(helpers);
        List<Message> out = new ArrayList<>();
        for (int i = 0; i < Math.min(config.indirectProbes(), helpers.size()); i++) {
            out.add(Message.pingReq(id, helpers.get(i), current.target, current.seq, takeGossip()));
        }
        return out;
    }

    /** End of the period: a probe with no direct and no indirect ack makes the target SUSPECT, not DEAD (section 4.2). */
    void finishProbe(int now) {
        if (current == null) return;
        if (!current.acked) {
            Update known = view.get(current.target);
            if (known != null && known.state() == MemberState.ALIVE) {
                suspicionsRaised++;
                tracer.trace(now, "n" + id + " got no ack from n" + current.target + " (direct + " + config.indirectProbes() + " indirect) -> SUSPECT");
                applyUpdate(new Update(current.target, MemberState.SUSPECT, known.incarnation()), now);
            }
        }
        current = null;
    }

    /** A suspicion that nobody refuted within suspicionPeriods periods becomes a confirmed death. */
    void expireSuspicions(int now) {
        for (Map.Entry<Integer, Integer> e : new ArrayList<>(suspectedAt.entrySet())) {
            if (now - e.getValue() >= config.suspicionPeriods() * config.protocolPeriod()) {
                Update known = view.get(e.getKey());
                if (known != null && known.state() == MemberState.SUSPECT) {
                    declaredDead++;
                    tracer.trace(now, "n" + id + " suspicion of n" + e.getKey() + " expired -> DEAD");
                    applyUpdate(new Update(e.getKey(), MemberState.DEAD, known.incarnation()), now);
                }
            }
        }
    }

    // ---------------------------------------------------------------- message handling

    @Override
    public List<Message> handle(Message m, int now) {
        for (Update u : m.piggyback()) applyUpdate(u, now);
        List<Message> out = new ArrayList<>();
        switch (m.kind()) {
            case PING -> out.add(Message.ack(id, m.from(), m.seq(), takeGossip()));
            case ACK -> {
                if (current != null && m.seq() == current.seq && m.from() == current.target) { current.acked = true; directAcks++; }
            }
            case PING_REQ -> {
                relaying.put(m.seq(), m.from());
                out.add(Message.ping(id, m.target(), m.seq(), takeGossip()));
            }
            case INDIRECT_ACK -> {
                if (current != null && m.seq() == current.seq && m.target() == current.target) { current.acked = true; indirectAcks++; }
            }
        }
        // An ACK for a seq we are relaying belongs to someone else's probe: forward it as INDIRECT_ACK.
        if (m.kind() == MessageKind.ACK && relaying.containsKey(m.seq()) && (current == null || m.seq() != current.seq)) {
            int origin = relaying.remove(m.seq());
            out.add(Message.indirectAck(id, origin, m.from(), m.seq(), takeGossip()));
        }
        messagesSent += out.size();
        return out;
    }

    /** Merge one update into the view using the precedence rules; refute suspicions about myself; queue what changed. */
    void applyUpdate(Update u, int now) {
        if (u.member() == id) {
            if (u.state() != MemberState.ALIVE && u.incarnation() >= incarnation) {
                incarnation = u.incarnation() + 1;                       // refutation (section 4.2): I am alive, and newer
                refutations++;
                tracer.trace(now, "n" + id + " hears it is " + u.state() + "@" + u.incarnation() + " -> refutes with incarnation " + incarnation);
                Update alive = new Update(id, MemberState.ALIVE, incarnation);
                view.put(id, alive);
                enqueue(alive);
            }
            return;
        }
        Update known = view.get(u.member());
        if (!u.overrides(known)) return;
        view.put(u.member(), u);
        if (u.state() == MemberState.SUSPECT) suspectedAt.putIfAbsent(u.member(), now);
        else suspectedAt.remove(u.member());
        enqueue(u);
    }

    private void enqueue(Update u) {
        gossipQueue.keySet().removeIf(old -> old.member() == u.member());   // one entry per member: the newest wins
        gossipQueue.put(u, 0);
    }

    /** Up to maxPiggyback updates, fewest-transmissions first; retire an update after lambda*log2(n) sends (section 4.1). */
    List<Update> takeGossip() {
        int n = Math.max(2, view.size());
        int limit = (int) Math.ceil(config.retransmitFactor() * (Math.log(n) / Math.log(2)));
        List<Map.Entry<Update, Integer>> entries = new ArrayList<>(gossipQueue.entrySet());
        entries.sort(Comparator.comparingInt(Map.Entry::getValue));
        List<Update> out = new ArrayList<>();
        for (Map.Entry<Update, Integer> e : entries) {
            if (out.size() >= config.maxPiggyback()) break;
            out.add(e.getKey());
            int sent = e.getValue() + 1;
            if (sent >= limit) gossipQueue.remove(e.getKey()); else gossipQueue.put(e.getKey(), sent);
        }
        return out;
    }

    int gossipQueueSize() { return gossipQueue.size(); }
}

/** Deterministic network: (deliverAt, seq) ordering, seeded delays and drops, partitions, crashes, one-way link cuts. */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Rng rng;
    private final double dropRate;
    private final int maxDelay;
    private final Map<Integer, Node> nodes = new TreeMap<>();
    private final PriorityQueue<Envelope> inFlight = new PriorityQueue<>(Comparator.comparingInt(Envelope::deliverAt).thenComparingLong(Envelope::seq));
    private final Set<Integer> down = new HashSet<>();
    private final Set<Long> cutLinks = new HashSet<>();        // directed a->b pairs that silently drop
    private Set<Integer> sideA = Set.of();
    private boolean partitioned = false;
    private int now = 0;
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;
    final Map<MessageKind, Integer> byKind = new TreeMap<>();

    Network(long seed, double dropRate, int maxDelay) { this.rng = new Rng(seed); this.dropRate = dropRate; this.maxDelay = Math.max(1, maxDelay); }

    void add(Node node) { nodes.put(node.id(), node); }
    int now() { return now; }
    boolean isDown(int id) { return down.contains(id); }
    void crash(int id) { down.add(id); }
    void partition(Set<Integer> sideA) { this.sideA = Set.copyOf(sideA); partitioned = true; }
    void heal() { partitioned = false; sideA = Set.of(); cutLinks.clear(); }
    /** Cut traffic in both directions between exactly two nodes; everyone else still reaches both. */
    void cutLink(int a, int b) { cutLinks.add(key(a, b)); cutLinks.add(key(b, a)); }
    private static long key(int a, int b) { return ((long) a << 32) | (b & 0xFFFFFFFFL); }

    private boolean blocked(int a, int b) {
        return down.contains(a) || down.contains(b) || (partitioned && sideA.contains(a) != sideA.contains(b)) || cutLinks.contains(key(a, b));
    }

    void send(Message m) {
        sent++;
        byKind.merge(m.kind(), 1, Integer::sum);
        if (blocked(m.from(), m.to())) { dropped++; return; }
        double roll = rng.nextDouble();
        int delay = 1 + rng.nextInt(maxDelay);
        if (roll < dropRate) { dropped++; return; }
        inFlight.add(new Envelope(now + delay, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    void tick() {
        now++;
        while (!inFlight.isEmpty() && inFlight.peek().deliverAt() <= now) {
            Message m = inFlight.poll().message();
            if (blocked(m.from(), m.to())) { dropped++; continue; }
            delivered++;
            sendAll(nodes.get(m.to()).handle(m, now));
        }
        for (Node n : nodes.values()) if (!down.contains(n.id())) sendAll(n.tick(now));
    }
}

/** N SWIM nodes on one network, with the measurements the lesson is about: detection time, dissemination time, false positives. */
final class Cluster {
    final int size;
    final long seed;
    final Config config;
    final Network network;
    final List<SwimNode> nodes = new ArrayList<>();
    final List<String> events = new ArrayList<>();

    Cluster(int size, long seed, double dropRate, int maxDelay, Config config) {
        this.size = size; this.seed = seed; this.config = config;
        this.network = new Network(seed, dropRate, maxDelay);
        List<Integer> all = new ArrayList<>();
        for (int i = 0; i < size; i++) all.add(i);
        for (int i = 0; i < size; i++) {
            SwimNode n = new SwimNode(i, all, config, new Rng(seed * 7919L + 1000003L * (i + 1)), 0, this::trace);
            nodes.add(n);
            network.add(n);
        }
    }

    Cluster(int size, long seed, double dropRate, int maxDelay) { this(size, seed, dropRate, maxDelay, Config.DEFAULT); }

    void trace(int now, String text) { events.add(String.format("t=%-4d %s", now, text)); }
    int now() { return network.now(); }
    SwimNode node(int id) { return nodes.get(id); }
    void tick() { network.tick(); }
    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }
    void crash(int id) { network.crash(id); trace(now(), "n" + id + " crashes"); }

    /** Ticks until every live node holds `state` for `member`, or -1. */
    int runUntilAllBelieve(int member, MemberState state, int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            if (allBelieve(member, state)) return now();
        }
        return -1;
    }

    boolean allBelieve(int member, MemberState state) {
        for (SwimNode n : nodes) if (!network.isDown(n.id) && n.id != member && n.stateOf(member) != state) return false;
        return true;
    }

    int countBelieving(int member, MemberState state) {
        int c = 0;
        for (SwimNode n : nodes) if (!network.isDown(n.id) && n.id != member && n.stateOf(member) == state) c++;
        return c;
    }

    /** A false positive: a live node marked DEAD by any other live node. */
    int falsePositives() {
        int c = 0;
        for (SwimNode n : nodes) {
            if (network.isDown(n.id)) continue;
            for (int dead : n.deadMembers()) if (!network.isDown(dead)) c++;
        }
        return c;
    }

    boolean viewsConverged() {
        String first = null;
        for (SwimNode n : nodes) {
            if (network.isDown(n.id)) continue;
            String v = n.deadMembers().toString();
            if (first == null) first = v; else if (!first.equals(v)) return false;
        }
        return true;
    }

    int totalSent() { int s = 0; for (SwimNode n : nodes) s += n.messagesSent; return s; }
}

public final class Main {
    static void printEvents(Cluster c, int from) { for (int i = from; i < c.events.size(); i++) System.out.println(c.events.get(i)); }

    public static void main(String[] args) {
        System.out.println("== Precedence rules for membership updates (SWIM section 4.2) ==");
        Update alive0 = new Update(3, MemberState.ALIVE, 0), suspect0 = new Update(3, MemberState.SUSPECT, 0);
        Update alive1 = new Update(3, MemberState.ALIVE, 1), dead0 = new Update(3, MemberState.DEAD, 0);
        System.out.println(suspect0 + " overrides " + alive0 + " = " + suspect0.overrides(alive0) + "   (same incarnation: suspicion wins)");
        System.out.println(alive0 + " overrides " + suspect0 + " = " + alive0.overrides(suspect0) + "  (a stale alive cannot clear a suspicion)");
        System.out.println(alive1 + " overrides " + suspect0 + " = " + alive1.overrides(suspect0) + "   (refutation: higher incarnation wins)");
        System.out.println(dead0 + " overrides " + alive1 + " = " + dead0.overrides(alive1) + "    (dead is terminal)");

        System.out.println();
        System.out.println("== 8 nodes, seed 3, delay 1-2, no drops: n5 crashes at t=100 ==");
        Cluster c = new Cluster(8, 3, 0.0, 2);
        c.run(100);
        int before = c.events.size();
        c.crash(5);
        int suspectAt = c.runUntilAllBelieve(5, MemberState.SUSPECT, 2000);
        int deadAt = c.runUntilAllBelieve(5, MemberState.DEAD, 2000);
        printEvents(c, before);
        System.out.println("first suspicion reached everyone by t=" + suspectAt + ", DEAD everywhere by t=" + deadAt
                + " (protocol period " + c.config.protocolPeriod() + ", suspicion timeout " + c.config.suspicionPeriods() * c.config.protocolPeriod() + ")");
        System.out.println("messages per node per period: " + String.format("%.2f", c.totalSent() / (double) (8 * (c.now() / c.config.protocolPeriod()))) + "  (constant: does not grow with N)");
        System.out.println("false positives: " + c.falsePositives());

        System.out.println();
        System.out.println("== Load per node is constant in N (the reason gossip beats all-to-all heartbeats) ==");
        for (int n : new int[]{8, 16, 32, 64}) {
            Cluster l = new Cluster(n, 5, 0.0, 2);
            l.run(600);
            double perNodePerPeriod = l.totalSent() / (double) (n * (600 / l.config.protocolPeriod()));
            System.out.printf("N=%-3d messages per node per period = %.2f   (all-to-all heartbeats would need %d)%n", n, perNodePerPeriod, n - 1);
        }

        System.out.println();
        System.out.println("== A slow link is not a dead node: n1 cannot reach n2, but n0, n3, n4 can; indirect probes save n2 ==");
        Cluster s = new Cluster(5, 11, 0.0, 2);
        s.run(50);
        before = s.events.size();
        s.network.cutLink(1, 2);
        s.run(600);
        printEvents(s, before);
        System.out.println("after 600 ticks with the n1<->n2 link cut: n1 believes n2 is " + s.node(1).stateOf(2) + "; false positives = " + s.falsePositives()
                + "; indirect acks received by n1 = " + s.node(1).indirectAcks);

        System.out.println();
        System.out.println("== Without indirect probes the same cut becomes a false suspicion, and n2 refutes it by bumping its incarnation ==");
        Cluster noIndirect = new Cluster(5, 11, 0.0, 2, new Config(12, 4, 0, 4, 6, 3));
        noIndirect.run(50);
        before = noIndirect.events.size();
        noIndirect.network.cutLink(1, 2);
        noIndirect.run(120);
        printEvents(noIndirect, before);
        System.out.println("n2 incarnation = " + noIndirect.node(2).incarnation + ", refutations = " + noIndirect.node(2).refutations
                + "; n1 believes n2 is " + noIndirect.node(1).stateOf(2) + " (the refutation arrived via other members' piggyback)");

        System.out.println();
        System.out.println("== Convergence over 20 seeds: 12 nodes, 10% message loss, delay 1-2, one crash at t=200 ==");
        int converged = 0, worstDead = 0, totalFalse = 0;
        long sumDead = 0;
        for (long seed = 1; seed <= 20; seed++) {
            Cluster r = new Cluster(12, seed, 0.10, 2);
            r.run(200);
            r.crash(7);
            int t = r.runUntilAllBelieve(7, MemberState.DEAD, 3000);
            if (t > 0) { converged++; sumDead += t - 200; worstDead = Math.max(worstDead, t - 200); }
            totalFalse += r.falsePositives();
        }
        System.out.printf("seeds where every live node declared n7 DEAD: %d/20; mean detection+dissemination %d ticks, worst %d ticks; false positives across all seeds: %d%n",
                converged, converged == 0 ? 0 : sumDead / converged, worstDead, totalFalse);
        System.out.println("expected: suspicion timeout (48) + one or two periods to notice + a few ticks to gossip; never a false positive because every suspect gets " + Config.DEFAULT.suspicionPeriods() + " periods to refute");
    }
}
