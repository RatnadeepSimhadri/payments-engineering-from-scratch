// Lesson: phases/06-replication-and-consensus/04-paxos/docs/en.md
// Single-Decree Paxos from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Lamport, "Paxos Made Simple" (2001); Lamport, "The Part-Time Parliament" (ACM TOCS 1998).
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;
import java.util.function.Predicate;

/**
 * SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Implemented by hand so that the same seed produces
 * the same message delays and drops in Java, Python, and Go: a bug found in one language is replayable in all three.
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

/** A ballot number: totally ordered, unique per proposer because the proposer id breaks ties between rounds. */
record Ballot(int round, int proposerId) implements Comparable<Ballot> {
    static final Ballot NONE = new Ballot(0, 0);

    boolean isNone() { return round == 0; }

    @Override
    public int compareTo(Ballot other) {
        int c = Integer.compare(round, other.round);
        return c != 0 ? c : Integer.compare(proposerId, other.proposerId);
    }

    @Override
    public String toString() { return isNone() ? "none" : round + "." + proposerId; }
}

enum MessageKind { PREPARE, PROMISE, ACCEPT, ACCEPTED, NACK }

/**
 * One message on the wire. Which fields are meaningful depends on the kind:
 * PREPARE(ballot); PROMISE(ballot, priorBallot + value previously accepted); ACCEPT(ballot, value);
 * ACCEPTED(ballot, value); NACK(ballot rejected, priorBallot = the ballot the acceptor has promised).
 */
record Message(MessageKind kind, int from, int to, Ballot ballot, Ballot priorBallot, String value) {
    static Message prepare(int from, int to, Ballot b) { return new Message(MessageKind.PREPARE, from, to, b, Ballot.NONE, null); }
    static Message promise(int from, int to, Ballot b, Ballot prior, String v) { return new Message(MessageKind.PROMISE, from, to, b, prior, v); }
    static Message accept(int from, int to, Ballot b, String v) { return new Message(MessageKind.ACCEPT, from, to, b, Ballot.NONE, v); }
    static Message accepted(int from, int to, Ballot b, String v) { return new Message(MessageKind.ACCEPTED, from, to, b, Ballot.NONE, v); }
    static Message nack(int from, int to, Ballot rejected, Ballot promised) { return new Message(MessageKind.NACK, from, to, rejected, promised, null); }
}

/** A node is a pure state machine: it consumes one message or one clock tick and returns the messages it wants sent. */
interface Node {
    int id();
    List<Message> handle(Message m, int now);
    default List<Message> tick(int now) { return List.of(); }
}

/**
 * Acceptor: the only role whose state matters for safety. Two fields, two promises.
 * P1: never accept a ballot lower than one already promised. P2: report what was accepted in every promise.
 */
final class Acceptor implements Node {
    private final int id;
    private final int[] learnerIds;
    Ballot promised = Ballot.NONE;
    Ballot acceptedBallot = Ballot.NONE;
    String acceptedValue = null;

    Acceptor(int id, int[] learnerIds) { this.id = id; this.learnerIds = learnerIds; }

    @Override public int id() { return id; }

    @Override
    public List<Message> handle(Message m, int now) {
        return switch (m.kind()) {
            case PREPARE -> onPrepare(m);
            case ACCEPT -> onAccept(m);
            default -> List.of();
        };
    }

    List<Message> onPrepare(Message m) {
        if (m.ballot().compareTo(promised) >= 0) {
            promised = m.ballot();
            return List.of(Message.promise(id, m.from(), m.ballot(), acceptedBallot, acceptedValue));
        }
        return List.of(Message.nack(id, m.from(), m.ballot(), promised));
    }

    List<Message> onAccept(Message m) {
        if (m.ballot().compareTo(promised) >= 0) {
            promised = m.ballot();
            acceptedBallot = m.ballot();
            acceptedValue = m.value();
            List<Message> out = new ArrayList<>();
            out.add(Message.accepted(id, m.from(), m.ballot(), m.value()));
            for (int learner : learnerIds) out.add(Message.accepted(id, learner, m.ballot(), m.value()));
            return out;
        }
        return List.of(Message.nack(id, m.from(), m.ballot(), promised));
    }
}

enum Phase { IDLE, PREPARING, ACCEPTING, DONE }

/** Proposer: drives the two phases, retries on NACK or timeout, and must adopt any value it learns was already accepted. */
final class Proposer implements Node {
    private final int id;
    private final int[] acceptorIds;
    private final int quorum;
    private final int timeout;
    int round = 0;
    Ballot ballot = Ballot.NONE;
    Phase phase = Phase.IDLE;
    String wanted = null;      // the value this proposer would like chosen
    String proposing = null;   // the value it is actually proposing in phase 2 (may differ from wanted)
    String decided = null;
    private final Set<Integer> promises = new HashSet<>();
    private final Set<Integer> accepts = new HashSet<>();
    private Ballot highestPrior = Ballot.NONE;
    private String highestPriorValue = null;
    private int deadline = Integer.MAX_VALUE;
    int restarts = 0;

    Proposer(int id, int[] acceptorIds, int timeout) {
        this.id = id;
        this.acceptorIds = acceptorIds;
        this.quorum = acceptorIds.length / 2 + 1;
        this.timeout = timeout;
    }

    @Override public int id() { return id; }

    /** Phase 1a: pick a ballot higher than any this proposer has used and ask every acceptor to promise. */
    List<Message> propose(String value, int now) {
        wanted = value;
        return startRound(now);
    }

    private List<Message> startRound(int now) {
        round++;
        ballot = new Ballot(round, id);
        phase = Phase.PREPARING;
        promises.clear();
        accepts.clear();
        highestPrior = Ballot.NONE;
        highestPriorValue = null;
        deadline = now + timeout;
        List<Message> out = new ArrayList<>();
        for (int a : acceptorIds) out.add(Message.prepare(id, a, ballot));
        return out;
    }

    @Override
    public List<Message> handle(Message m, int now) {
        if (phase == Phase.DONE || !m.ballot().equals(ballot)) return List.of(); // stale reply from an earlier round
        return switch (m.kind()) {
            case PROMISE -> onPromise(m, now);
            case ACCEPTED -> onAccepted(m);
            case NACK -> onNack(m, now);
            default -> List.of();
        };
    }

    /** Phase 1b -> 2a: once a majority has promised, propose the highest-ballot value any of them had accepted, else our own. */
    private List<Message> onPromise(Message m, int now) {
        if (phase != Phase.PREPARING) return List.of();
        promises.add(m.from());
        if (!m.priorBallot().isNone() && m.priorBallot().compareTo(highestPrior) > 0) {
            highestPrior = m.priorBallot();
            highestPriorValue = m.value();
        }
        if (promises.size() < quorum) return List.of();
        proposing = highestPriorValue != null ? highestPriorValue : wanted;
        phase = Phase.ACCEPTING;
        deadline = now + timeout;
        List<Message> out = new ArrayList<>();
        for (int a : acceptorIds) out.add(Message.accept(id, a, ballot, proposing));
        return out;
    }

    private List<Message> onAccepted(Message m) {
        if (phase != Phase.ACCEPTING) return List.of();
        accepts.add(m.from());
        if (accepts.size() >= quorum) {
            decided = proposing;
            phase = Phase.DONE;
        }
        return List.of();
    }

    /** A NACK carries the ballot the acceptor has promised; jump past it instead of creeping up one round at a time. */
    private List<Message> onNack(Message m, int now) {
        round = Math.max(round, m.priorBallot().round());
        restarts++;
        return startRound(now);
    }

    @Override
    public List<Message> tick(int now) {
        if ((phase == Phase.PREPARING || phase == Phase.ACCEPTING) && now >= deadline) {
            restarts++;
            return startRound(now);
        }
        return List.of();
    }
}

/** Learner: a value is chosen once a majority of acceptors have accepted the same ballot. It must never learn two values. */
final class Learner implements Node {
    private final int id;
    private final int quorum;
    private final Map<Ballot, Set<Integer>> votes = new HashMap<>();
    Ballot learnedBallot = Ballot.NONE;
    String learned = null;

    Learner(int id, int acceptorCount) { this.id = id; this.quorum = acceptorCount / 2 + 1; }

    @Override public int id() { return id; }

    @Override
    public List<Message> handle(Message m, int now) {
        if (m.kind() != MessageKind.ACCEPTED) return List.of();
        Set<Integer> voters = votes.computeIfAbsent(m.ballot(), b -> new HashSet<>());
        voters.add(m.from());
        if (voters.size() >= quorum) {
            if (learned != null && !learned.equals(m.value())) {
                throw new IllegalStateException("SAFETY VIOLATION: learner " + id + " learned " + learned + " then " + m.value());
            }
            learned = m.value();
            learnedBallot = m.ballot();
        }
        return List.of();
    }
}

/**
 * Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
 * (deliverAt, sequence number), so a run is a pure function of (seed, program). No threads, no sleeping.
 */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Rng rng;
    private final double dropRate;
    private final int maxDelay;
    private final Map<Integer, Node> nodes = new TreeMap<>();
    private PriorityQueue<Envelope> inFlight = newQueue();
    private final Set<Integer> isolated = new HashSet<>();
    private Runnable observer = () -> {};
    private int now = 0;
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;
    boolean trace = false;

    Network(long seed, double dropRate, int maxDelay) {
        this.rng = new Rng(seed);
        this.dropRate = dropRate;
        this.maxDelay = Math.max(1, maxDelay);
    }

    private static PriorityQueue<Envelope> newQueue() {
        return new PriorityQueue<>(Comparator.comparingInt(Envelope::deliverAt).thenComparingLong(Envelope::seq));
    }

    void add(Node node) { nodes.put(node.id(), node); }
    void setObserver(Runnable r) { observer = r; }
    int now() { return now; }
    int inFlight() { return inFlight.size(); }
    void isolate(int nodeId) { isolated.add(nodeId); }
    void heal() { isolated.clear(); }

    void send(Message m) {
        sent++;
        if (isolated.contains(m.from()) || isolated.contains(m.to())) { dropped++; return; }
        double roll = rng.nextDouble();
        int delay = 1 + rng.nextInt(maxDelay);
        if (roll < dropRate) { dropped++; return; }
        inFlight.add(new Envelope(now + delay, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    /** Advance the clock one tick: let every node act on the time, then deliver everything that is due. */
    void tick() {
        now++;
        for (Node n : nodes.values()) sendAll(n.tick(now));
        while (!inFlight.isEmpty() && inFlight.peek().deliverAt() <= now) deliver(inFlight.poll());
    }

    private void deliver(Envelope e) {
        delivered++;
        Node target = nodes.get(e.message().to());
        if (trace) System.out.printf("t=%-3d %s -> %s %s%n", now, Main.name(e.message().from()), Main.name(e.message().to()), describe(e.message()));
        sendAll(target.handle(e.message(), now));
        observer.run();
    }

    /** Deliver, ignoring delays, every in-flight message matching the filter, repeating until none match. Lets a test script an adversarial schedule. */
    int drain(Predicate<Message> filter) {
        int count = 0;
        while (true) {
            List<Envelope> matching = new ArrayList<>();
            PriorityQueue<Envelope> rest = newQueue();
            for (Envelope e : inFlight) (filter.test(e.message()) ? matching : rest).add(e);
            if (matching.isEmpty()) return count;
            matching.sort(Comparator.comparingLong(Envelope::seq));
            inFlight = rest;
            for (Envelope e : matching) { deliver(e); count++; }
        }
    }

    static String describe(Message m) {
        return switch (m.kind()) {
            case PREPARE -> "PREPARE " + m.ballot();
            case PROMISE -> "PROMISE " + m.ballot() + (m.priorBallot().isNone() ? " (nothing accepted)" : " (accepted " + m.value() + " at " + m.priorBallot() + ")");
            case ACCEPT -> "ACCEPT " + m.ballot() + " " + m.value();
            case ACCEPTED -> "ACCEPTED " + m.ballot() + " " + m.value();
            case NACK -> "NACK " + m.ballot() + " (promised " + m.priorBallot() + ")";
        };
    }
}

/** Wires acceptors, proposers, and learners onto one network and checks the safety invariant after every delivery. */
final class PaxosCluster {
    final int acceptorCount;
    final List<Acceptor> acceptors = new ArrayList<>();
    final List<Proposer> proposers = new ArrayList<>();
    final List<Learner> learners = new ArrayList<>();
    final Network network;
    String firstChosen = null;
    int chosenAt = -1;

    PaxosCluster(int acceptorCount, int proposerCount, int learnerCount, long seed, double dropRate, int maxDelay, int timeout) {
        this.acceptorCount = acceptorCount;
        this.network = new Network(seed, dropRate, maxDelay);
        int[] acceptorIds = new int[acceptorCount];
        for (int i = 0; i < acceptorCount; i++) acceptorIds[i] = i;
        int[] learnerIds = new int[learnerCount];
        for (int i = 0; i < learnerCount; i++) learnerIds[i] = acceptorCount + proposerCount + i;
        for (int i = 0; i < acceptorCount; i++) acceptors.add(new Acceptor(i, learnerIds));
        for (int i = 0; i < proposerCount; i++) proposers.add(new Proposer(acceptorCount + i, acceptorIds, timeout));
        for (int i = 0; i < learnerCount; i++) learners.add(new Learner(learnerIds[i], acceptorCount));
        for (Acceptor a : acceptors) network.add(a);
        for (Proposer p : proposers) network.add(p);
        for (Learner l : learners) network.add(l);
        network.setObserver(this::checkSafety);
    }

    void propose(int proposerIndex, String value) {
        network.sendAll(proposers.get(proposerIndex).propose(value, network.now()));
    }

    void tick() { network.tick(); }

    /** Run until every learner has learned a value or maxTicks elapse. Returns true if a value was chosen. */
    boolean run(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            if (allLearned()) return true;
        }
        return chosenValue() != null;
    }

    boolean allLearned() {
        for (Learner l : learners) if (l.learned == null) return false;
        return true;
    }

    /** The definition of "chosen": some ballot has been accepted by a majority of acceptors. Computed from acceptor state alone. */
    String chosenValue() {
        Map<Ballot, Integer> counts = new HashMap<>();
        for (Acceptor a : acceptors) {
            if (a.acceptedBallot.isNone()) continue;
            int n = counts.merge(a.acceptedBallot, 1, Integer::sum);
            if (n >= acceptorCount / 2 + 1) return a.acceptedValue;
        }
        return null;
    }

    void checkSafety() {
        String now = chosenValue();
        if (now == null) return;
        if (firstChosen == null) { firstChosen = now; chosenAt = network.now(); return; }
        if (!firstChosen.equals(now)) {
            throw new IllegalStateException("SAFETY VIOLATION: chosen " + firstChosen + " at t=" + chosenAt + " then " + now + " at t=" + network.now());
        }
    }

    int maxRound() {
        int r = 0;
        for (Proposer p : proposers) r = Math.max(r, p.round);
        return r;
    }
}

public final class Main {
    static int acceptorsForNames = 3, proposersForNames = 2;

    static String show(String value) { return value == null ? "(none)" : value; }

    static String name(int id) {
        if (id < acceptorsForNames) return "a" + id;
        if (id < acceptorsForNames + proposersForNames) return "p" + id;
        return "l" + id;
    }

    static void demoLossless() {
        System.out.println("== 1. Three acceptors, two proposers, one learner, lossless network, delay 1 ==");
        PaxosCluster c = new PaxosCluster(3, 2, 1, 7, 0.0, 1, 10);
        c.network.trace = true;
        c.propose(0, "debit:acct-42:19.99");
        c.propose(1, "credit:acct-42:5.00");
        for (int i = 0; i < 8; i++) c.tick();          // long enough for the losing proposer to retry and adopt the chosen value
        System.out.printf("chosen = %s at t=%d; learner learned %s at ballot %s; p3.decided=%s p4.decided=%s%n%n",
                c.firstChosen, c.chosenAt, c.learners.get(0).learned, c.learners.get(0).learnedBallot,
                c.proposers.get(0).decided, c.proposers.get(1).decided);
    }

    static void demoSeededRuns() {
        System.out.println("== 2. 200 seeded runs: 5 acceptors, 3 proposers, 20% message loss, delays 1..3 ==");
        int decided = 0, totalTicks = 0, maxRounds = 0;
        Map<String, Integer> winners = new TreeMap<>();
        for (long seed = 1; seed <= 200; seed++) {
            PaxosCluster c = new PaxosCluster(5, 3, 2, seed, 0.20, 3, 12);
            c.propose(0, "A");
            c.propose(1, "B");
            c.propose(2, "C");
            boolean ok = c.run(400);
            if (ok) { decided++; totalTicks += c.chosenAt; winners.merge(c.firstChosen, 1, Integer::sum); }
            maxRounds = Math.max(maxRounds, c.maxRound());
        }
        System.out.printf("decided in %d/200 runs, distinct chosen values per run: never more than 1 (checked after every delivery)%n", decided);
        System.out.printf("winners: %s; mean ticks to choose: %.1f; highest round seen: %d%n%n", winners, decided == 0 ? 0.0 : (double) totalTicks / decided, maxRounds);
    }

    static void demoDuel() {
        System.out.println("== 3. Constructed duel: two proposers whose prepares keep landing between each other's promise and accept ==");
        PaxosCluster c = new PaxosCluster(3, 2, 1, 1, 0.0, 1, 1_000);
        Predicate<Message> notAccept = m -> m.kind() != MessageKind.ACCEPT;
        c.propose(0, "A");
        c.network.drain(notAccept);                       // p3: prepare 1.3 -> promises -> ACCEPT 1.3 held in flight
        c.propose(1, "B");
        c.network.drain(notAccept);                       // p4: prepare 1.4 -> promises -> ACCEPT 1.4 held in flight
        for (int k = 1; k <= 3; k++) {
            c.network.drain(m -> m.kind() == MessageKind.ACCEPT && m.from() == 3);  // p3's accept arrives: NACK (promised 1.4)
            c.network.drain(notAccept);                                              // p3 re-prepares at a higher round and gets promises
            c.network.drain(m -> m.kind() == MessageKind.ACCEPT && m.from() == 4);  // p4's accept arrives: NACK
            c.network.drain(notAccept);
            System.out.printf("after exchange %d: p3 at ballot %s, p4 at ballot %s, chosen=%s%n", k, c.proposers.get(0).ballot, c.proposers.get(1).ballot, show(c.chosenValue()));
        }
        c.network.drain(m -> true);                       // stop scheduling adversarially: whichever accept lands first wins
        System.out.printf("schedule released: chosen=%s, rounds used=%d%n", show(c.chosenValue()), c.maxRound());
        System.out.println("fix: one distinguished proposer (a leader), or randomized backoff before retrying -- Lesson 05 builds the leader.");
    }

    public static void main(String[] args) {
        demoLossless();
        acceptorsForNames = 5; proposersForNames = 3;
        demoSeededRuns();
        acceptorsForNames = 3; proposersForNames = 2;
        demoDuel();
    }
}
