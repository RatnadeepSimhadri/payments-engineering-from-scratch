// Lesson: phases/06-replication-and-consensus/03-quorums/docs/en.md
// Quorums: R + W > N and Its Limits
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), sections 4.5-4.7;
//          Gifford, "Weighted Voting for Replicated Data" (SOSP 1979); Apache Cassandra docs on consistency levels and read repair.
// Java 21, standard library only. Deterministic: seeded SplitMix64 for drops, a tick counter for time, no threads.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/** SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language. */
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

/**
 * A value with its version. Last-writer-wins without version vectors: the version is (timestamp, writerId), compared
 * lexicographically so two writes at the same tick still have one deterministic winner. This is the Cassandra model; the
 * price is that concurrent writes are not detected, one of them silently loses, and that is a feature of the demo.
 */
record Versioned(String value, long timestamp, int writerId) {
    static final Versioned NONE = new Versioned(null, -1, -1);

    boolean newerThan(Versioned other) {
        return timestamp > other.timestamp || (timestamp == other.timestamp && writerId > other.writerId);
    }

    @Override public String toString() { return value == null ? "<none>" : value + "@" + timestamp + "/w" + writerId; }
}

/** Result of a quorum read: the winning version and which replicas answered, so the caller can see what the quorum saw. */
record ReadResult(Versioned winner, int responses, List<Integer> respondents, int repaired) {
    String value() { return winner.value(); }
}

/** Thrown when fewer than W acks or fewer than R responses arrive. The client must retry or report failure; never pretend. */
final class QuorumNotReached extends RuntimeException {
    QuorumNotReached(String message) { super(message); }
}

/**
 * One storage node. Holds its own data plus hints: writes it accepted on behalf of a node that was unreachable, to be
 * handed off when that node comes back (Dynamo 4.6). Hints are keyed by the intended node.
 */
final class StorageNode {
    final int id;
    final Map<String, Versioned> data = new TreeMap<>();
    final Map<Integer, Map<String, Versioned>> hints = new TreeMap<>();
    int writesAccepted = 0, readsServed = 0, hintsStored = 0, hintsDelivered = 0, repairsApplied = 0;

    StorageNode(int id) { this.id = id; }

    /** Store only if newer. Returns true when the stored version changed. Idempotent, so replays and repairs are harmless. */
    boolean put(String key, Versioned v) {
        writesAccepted++;
        Versioned current = data.getOrDefault(key, Versioned.NONE);
        if (v.newerThan(current)) { data.put(key, v); return true; }
        return false;
    }

    Versioned get(String key) { readsServed++; return data.getOrDefault(key, Versioned.NONE); }

    void storeHint(int forNode, String key, Versioned v) {
        hintsStored++;
        Map<String, Versioned> forTarget = hints.computeIfAbsent(forNode, k -> new TreeMap<>());
        Versioned existing = forTarget.getOrDefault(key, Versioned.NONE);
        if (v.newerThan(existing)) forTarget.put(key, v);
    }

    int hintCount() { int n = 0; for (Map<String, Versioned> m : hints.values()) n += m.size(); return n; }

}

/** The simulated network: a set of unreachable nodes, a partition, and a seeded drop rate. Requests are synchronous RPCs. */
final class Network {
    private final Rng rng;
    private final double dropRate;
    private final Set<Integer> down = new HashSet<>();
    private Set<Integer> sideA = Set.of();
    private boolean partitioned = false;
    int attempts = 0, dropped = 0;

    Network(long seed, double dropRate) { this.rng = new Rng(seed); this.dropRate = dropRate; }

    void crash(int id) { down.add(id); }
    void recover(int id) { down.remove(id); }
    boolean isDown(int id) { return down.contains(id); }
    void partition(Set<Integer> a) { sideA = Set.copyOf(a); partitioned = true; }
    void heal() { partitioned = false; sideA = Set.of(); }

    /** Can `from` (the coordinator) reach `to` right now? Deterministic given the seed and the call order. */
    boolean reachable(int from, int to) {
        attempts++;
        if (down.contains(to) || (partitioned && sideA.contains(from) != sideA.contains(to))) { dropped++; return false; }
        if (dropRate > 0 && rng.nextDouble() < dropRate) { dropped++; return false; }
        return true;
    }
}

/** The N/R/W knobs plus the two Dynamo options this lesson is about. */
record QuorumConfig(int n, int r, int w, boolean sloppy, boolean readRepair) {
    QuorumConfig {
        if (n < 1 || r < 1 || w < 1 || r > n || w > n) throw new IllegalArgumentException("need 1 <= R, W <= N");
    }

    boolean overlaps() { return r + w > n; }

    @Override public String toString() { return "N=" + n + " R=" + r + " W=" + w + (sloppy ? " sloppy" : " strict") + (readRepair ? " read-repair" : ""); }
}

/**
 * A leaderless replicated store. Any node may coordinate. Each key has N home replicas (the first N nodes on the ring
 * starting at hash(key)); a sloppy quorum may spill onto the next nodes with a hint when a home node is unreachable.
 */
final class QuorumStore {
    final List<StorageNode> nodes = new ArrayList<>();
    final Network network;
    final QuorumConfig config;
    private int clock = 0;
    int writes = 0, writeFailures = 0, reads = 0, readFailures = 0, hintedWrites = 0, repairs = 0;
    final List<String> events = new ArrayList<>();

    QuorumStore(int totalNodes, QuorumConfig config, long seed, double dropRate) {
        if (totalNodes < config.n()) throw new IllegalArgumentException("need at least N nodes");
        this.config = config;
        this.network = new Network(seed, dropRate);
        for (int i = 0; i < totalNodes; i++) nodes.add(new StorageNode(i));
    }

    int now() { return clock; }
    void tick() { clock++; }

    /** Deterministic placement: the key's home replicas are N consecutive ring positions. */
    List<Integer> homeNodes(String key) {
        int start = Math.floorMod(key.hashCode(), nodes.size());
        List<Integer> home = new ArrayList<>();
        for (int i = 0; i < config.n(); i++) home.add((start + i) % nodes.size());
        return home;
    }

    /** The home replicas with the coordinator first when it is one of them: a coordinator always consults its own copy first. */
    List<Integer> contactOrder(int coordinator, String key) {
        List<Integer> order = new ArrayList<>(homeNodes(key));
        int at = order.indexOf(coordinator);
        if (at > 0) Collections.rotate(order, -at);
        if (config.sloppy()) order.addAll(fallbackNodes(key));
        return order;
    }

    /** The ring beyond the home set, in order: where a sloppy quorum spills. */
    List<Integer> fallbackNodes(String key) {
        int start = Math.floorMod(key.hashCode(), nodes.size());
        List<Integer> rest = new ArrayList<>();
        for (int i = config.n(); i < nodes.size(); i++) rest.add((start + i) % nodes.size());
        return rest;
    }

    /**
     * Write through a coordinator. Each home replica that answers stores the version; with sloppy quorums, each home
     * replica that does not answer is replaced by the next ring node, which stores the value plus a hint for the absentee.
     * Success means at least W acks. Nothing is rolled back on failure: some replicas may hold the value anyway.
     */
    int put(int coordinator, String key, String value) {
        writes++;
        clock++;
        Versioned v = new Versioned(value, clock, coordinator);
        int acks = 0;
        List<Integer> acked = new ArrayList<>(), missed = new ArrayList<>();
        for (int node : homeNodes(key)) {
            if (node == coordinator || network.reachable(coordinator, node)) { nodes.get(node).put(key, v); acks++; acked.add(node); }
            else missed.add(node);
        }
        if (config.sloppy() && !missed.isEmpty()) {
            List<Integer> spare = fallbackNodes(key);
            int si = 0;
            for (int absent : missed) {
                while (si < spare.size()) {
                    int candidate = spare.get(si++);
                    if (candidate == coordinator || network.reachable(coordinator, candidate)) {
                        nodes.get(candidate).put(key, v);
                        nodes.get(candidate).storeHint(absent, key, v);
                        hintedWrites++;
                        acks++;
                        acked.add(candidate);
                        break;
                    }
                }
            }
        }
        if (acks < config.w()) {
            writeFailures++;
            events.add("t=" + clock + " PUT " + key + "=" + value + " via n" + coordinator + " FAILED: " + acks + " acks < W=" + config.w());
            throw new QuorumNotReached("write " + key + " got " + acks + " acks, needs W=" + config.w());
        }
        events.add("t=" + clock + " PUT " + key + "=" + value + " via n" + coordinator + " acked by " + acked + (missed.isEmpty() ? "" : " (missed " + missed + ")"));
        return acks;
    }

    /**
     * Read through a coordinator: ask the home replicas (and, for a sloppy quorum, the fallback ring after them), take the
     * first R answers, return the newest version. With read repair, push the winner back to every responder that returned
     * something older.
     */
    ReadResult get(int coordinator, String key) {
        reads++;
        Versioned winner = Versioned.NONE;
        List<Integer> respondents = new ArrayList<>();
        Map<Integer, Versioned> seen = new HashMap<>();
        for (int node : contactOrder(coordinator, key)) {
            if (respondents.size() >= config.r()) break;
            if (node == coordinator || network.reachable(coordinator, node)) {
                Versioned v = nodes.get(node).get(key);
                respondents.add(node);
                seen.put(node, v);
                if (v.newerThan(winner)) winner = v;
            }
        }
        if (respondents.size() < config.r()) {
            readFailures++;
            throw new QuorumNotReached("read " + key + " got " + respondents.size() + " responses, needs R=" + config.r());
        }
        int repaired = 0;
        if (config.readRepair() && winner.value() != null) {
            for (int node : respondents) {
                if (winner.newerThan(seen.get(node)) && nodes.get(node).put(key, winner)) { nodes.get(node).repairsApplied++; repaired++; }
            }
            repairs += repaired;
        }
        return new ReadResult(winner, respondents.size(), respondents, repaired);
    }

    /** Hinted handoff: every node that holds hints for a now-reachable target delivers them. Returns how many were delivered. */
    int handoff() {
        int delivered = 0;
        for (StorageNode holder : nodes) {
            for (Map.Entry<Integer, Map<String, Versioned>> e : new ArrayList<>(holder.hints.entrySet())) {
                int target = e.getKey();
                if (!network.reachable(holder.id, target)) continue;
                for (Map.Entry<String, Versioned> kv : e.getValue().entrySet()) { nodes.get(target).put(kv.getKey(), kv.getValue()); delivered++; }
                holder.hintsDelivered += e.getValue().size();
                holder.hints.remove(target);
            }
        }
        if (delivered > 0) events.add("t=" + clock + " hinted handoff delivered " + delivered + " value(s)");
        return delivered;
    }

    /**
     * Anti-entropy: for every key, converge all reachable home replicas to the newest version. Production systems
     * compare Merkle trees per key range first so that agreeing ranges cost one hash exchange; here every key is visited.
     * Returns the number of replica updates.
     */
    int antiEntropy() {
        int updates = 0;
        Set<String> keys = new HashSet<>();
        for (StorageNode n : nodes) keys.addAll(n.data.keySet());
        for (String key : keys) {
            List<Integer> home = homeNodes(key);
            Versioned newest = Versioned.NONE;
            for (int node : home) if (!network.isDown(node)) { Versioned v = nodes.get(node).data.getOrDefault(key, Versioned.NONE); if (v.newerThan(newest)) newest = v; }
            if (newest.value() == null) continue;
            for (int node : home) if (!network.isDown(node) && nodes.get(node).put(key, newest)) updates++;
        }
        if (updates > 0) events.add("t=" + clock + " anti-entropy updated " + updates + " replica value(s)");
        return updates;
    }

    /** How many of the home replicas of `key` hold `value` as their current version. */
    int replicasHolding(String key, String value) {
        int count = 0;
        for (int node : homeNodes(key)) if (value.equals(nodes.get(node).data.getOrDefault(key, Versioned.NONE).value())) count++;
        return count;
    }

    /** Do all live home replicas of `key` hold the same version? The convergence check anti-entropy is trying to make true. */
    boolean replicasAgree(String key) {
        Set<Versioned> versions = new HashSet<>();
        for (int node : homeNodes(key)) if (!network.isDown(node)) versions.add(nodes.get(node).data.getOrDefault(key, Versioned.NONE));
        return versions.size() <= 1;
    }

    String replicaView(String key) {
        StringBuilder sb = new StringBuilder();
        for (int node : homeNodes(key)) sb.append(" n").append(node).append('=').append(nodes.get(node).data.getOrDefault(key, Versioned.NONE)).append(network.isDown(node) ? "(down)" : "");
        return sb.toString().trim();
    }
}

/**
 * Runs the one experiment that matters: write, then read from a different coordinator, and count how often the read
 * misses the latest write. Reports the stale rate for one configuration under crashes and drops.
 */
final class Experiment {
    record Outcome(int writes, int writeFailures, int readFailures, int staleReads, int hintedWrites, int repairs) {
        double staleRate() { int ok = writes - writeFailures - readFailures; return ok == 0 ? 0 : (double) staleReads / ok; }
    }

    static Outcome run(QuorumConfig config, int totalNodes, long seed, double dropRate, int operations, int crashEvery) {
        QuorumStore store = new QuorumStore(totalNodes, config, seed, dropRate);
        Rng rng = new Rng(seed * 31 + 7);
        int stale = 0, hinted = 0, repairs = 0, writeFailures = 0, readFailures = 0;
        for (int i = 1; i <= operations; i++) {
            String key = "k" + rng.nextInt(5);
            String value = "v" + i;
            int writer = rng.nextInt(totalNodes);
            int reader = rng.nextInt(totalNodes);
            if (crashEvery > 0 && i % crashEvery == 0) {
                int victim = rng.nextInt(totalNodes);
                int downCount = 0;
                for (int n = 0; n < totalNodes; n++) if (store.network.isDown(n)) downCount++;
                if (store.network.isDown(victim)) { store.network.recover(victim); store.handoff(); }
                else if (downCount < 2) store.network.crash(victim);
            }
            boolean written = false;
            try { store.put(writer, key, value); written = true; } catch (QuorumNotReached e) { writeFailures++; }
            if (!written) continue;
            try {
                ReadResult r = store.get(reader, key);
                if (!value.equals(r.value())) stale++;
            } catch (QuorumNotReached e) { readFailures++; }
        }
        return new Outcome(operations, writeFailures, readFailures, stale, store.hintedWrites, store.repairs);
    }
}

public final class Main {
    static void section(String title) { System.out.println(); System.out.println("== " + title + " =="); }

    public static void main(String[] args) {
        System.out.println("== R + W > N: a strict quorum read always overlaps the last quorum write (N=3, R=2, W=2, node 1 crashed) ==");
        QuorumStore s = new QuorumStore(5, new QuorumConfig(3, 2, 2, false, false), 1, 0.0);
        String key = "merchant:m-77:settlement-currency";
        System.out.println("home replicas of " + key + ": " + s.homeNodes(key) + "; fallback ring: " + s.fallbackNodes(key));
        int crashed = s.homeNodes(key).get(1);
        s.network.crash(crashed);
        s.put(s.homeNodes(key).get(0), key, "EUR");
        ReadResult r = s.get(s.homeNodes(key).get(2), key);
        System.out.println(s.events.get(s.events.size() - 1));
        System.out.println("read from a different coordinator with R=2 saw " + r.winner() + " from " + r.respondents() + "  (replicas: " + s.replicaView(key) + ")");
        System.out.println("the write set {" + s.homeNodes(key).get(0) + "," + s.homeNodes(key).get(2) + "} and the read set " + r.respondents() + " share at least one node: 2 + 2 > 3");

        section("R + W <= N: the quorums can miss each other (N=3, R=1, W=1), one node briefly unreachable");
        QuorumStore weak = new QuorumStore(5, new QuorumConfig(3, 1, 1, false, false), 1, 0.0);
        weak.put(weak.homeNodes(key).get(0), key, "EUR");
        weak.network.crash(weak.homeNodes(key).get(2));
        weak.put(weak.homeNodes(key).get(1), key, "GBP");
        weak.network.recover(weak.homeNodes(key).get(2));
        ReadResult w1 = weak.get(weak.homeNodes(key).get(0), key), w2 = weak.get(weak.homeNodes(key).get(1), key), w3 = weak.get(weak.homeNodes(key).get(2), key);
        System.out.println("PUT EUR; node " + weak.homeNodes(key).get(2) + " unreachable during PUT GBP (1 ack suffices), then back: " + weak.replicaView(key));
        System.out.println("R=1 reads via n" + weak.homeNodes(key).get(0) + ", n" + weak.homeNodes(key).get(1) + ", n" + weak.homeNodes(key).get(2) + " return: " + w1.value() + ", " + w2.value() + ", " + w3.value() + "  <- every node is healthy and one of them is wrong");

        section("Sloppy quorum: the write reaches W nodes, but not the home set, so a later strict-looking read can miss it");
        QuorumStore sloppy = new QuorumStore(5, new QuorumConfig(3, 2, 2, true, false), 1, 0.0);
        List<Integer> home = sloppy.homeNodes(key);
        sloppy.network.crash(home.get(1));
        sloppy.network.crash(home.get(2));
        sloppy.put(home.get(0), key, "EUR");
        System.out.println(sloppy.events.get(sloppy.events.size() - 1) + "  <- 2 acks reached W=2, one of them is a hint on a non-home node");
        System.out.println("replicas now: " + sloppy.replicaView(key) + "; hints held: " + sloppy.nodes.get(sloppy.fallbackNodes(key).get(0)).hintCount());
        sloppy.network.recover(home.get(1));
        sloppy.network.recover(home.get(2));
        sloppy.network.crash(home.get(0));
        ReadResult stale = sloppy.get(home.get(1), key);
        System.out.println("home nodes " + home.get(1) + "," + home.get(2) + " recover, node " + home.get(0) + " crashes; R=2 read sees " + stale.winner() + " from " + stale.respondents() + "  <- R + W > N held and the read is still stale");
        int delivered = sloppy.handoff();
        ReadResult fixed = sloppy.get(home.get(1), key);
        System.out.println("hinted handoff delivers " + delivered + " value(s); the same read now sees " + fixed.winner() + "  (replicas: " + sloppy.replicaView(key) + ")");

        section("Read repair: a stale replica is fixed by the next quorum read that notices it");
        QuorumStore rr = new QuorumStore(5, new QuorumConfig(3, 2, 2, false, true), 1, 0.0);
        rr.network.crash(home.get(2));
        rr.put(home.get(0), key, "EUR");
        rr.network.recover(home.get(2));
        System.out.println("after a write with node " + home.get(2) + " down and back: " + rr.replicaView(key));
        rr.network.crash(home.get(0));
        ReadResult repairRead = rr.get(home.get(1), key);
        System.out.println("R=2 read via n" + home.get(1) + " (node " + home.get(0) + " now down) saw " + repairRead.winner() + " from " + repairRead.respondents() + ", repaired " + repairRead.repaired() + " replica -> " + rr.replicaView(key));

        section("Last-writer-wins: two concurrent writers, one of them silently loses, and why a balance must never be a register");
        QuorumStore lww = new QuorumStore(5, new QuorumConfig(3, 2, 2, false, true), 1, 0.0);
        String bal = "wallet:w-9:balance";
        lww.put(0, bal, "100.00 USD");
        ReadResult seenByA = lww.get(1, bal), seenByB = lww.get(3, bal);
        System.out.println("coordinator A reads " + seenByA.value() + ", coordinator B reads " + seenByB.value() + "; A applies a 30.00 USD debit, B applies a 50.00 USD credit");
        int acksA = lww.put(1, bal, "70.00 USD");
        System.out.println("A's write lands with " + acksA + " acks: " + lww.replicaView(bal));
        int acksB = lww.put(3, bal, "150.00 USD");
        System.out.println("B's write lands with " + acksB + " acks one tick later and both clients receive success: " + lww.replicaView(bal));
        ReadResult after = lww.get(lww.homeNodes(bal).get(2), bal);
        lww.antiEntropy();
        System.out.println("quorum read returns " + after.winner() + "; after anti-entropy every replica holds " + lww.replicaView(bal));
        System.out.println("the 30.00 USD debit vanished with no error anywhere: LWW chose by timestamp, not by meaning. Lesson 08 (CRDTs) and Phase 10 (ledgers) are the answer.");

        section("Anti-entropy converges replicas that no read ever touched");
        QuorumStore ae = new QuorumStore(5, new QuorumConfig(3, 2, 2, true, false), 1, 0.0);
        ae.network.crash(2);
        for (int i = 0; i < 8; i++) ae.put(i % 5 == 2 ? 0 : i % 5, "cfg:" + i, "value" + i);
        ae.network.recover(2);
        int before = 0;
        for (int i = 0; i < 8; i++) if (ae.replicasAgree("cfg:" + i)) before++;
        int updates = ae.antiEntropy();
        int afterCount = 0;
        for (int i = 0; i < 8; i++) if (ae.replicasAgree("cfg:" + i)) afterCount++;
        System.out.println("node 2 missed its share of 8 writes; keys whose home replicas agree before: " + before + "/8, anti-entropy applied " + updates + " update(s), after: " + afterCount + "/8");

        section("20 seeds x 400 write-then-read pairs, 5 nodes, 5% drops, a crash or recovery every 25 ops: stale reads by configuration");
        QuorumConfig[] configs = {
            new QuorumConfig(3, 1, 1, false, false), new QuorumConfig(3, 1, 3, false, false), new QuorumConfig(3, 2, 2, false, false),
            new QuorumConfig(3, 2, 2, false, true), new QuorumConfig(3, 2, 2, true, false), new QuorumConfig(3, 3, 1, false, false),
        };
        System.out.printf("%-34s %8s %10s %10s %7s %7s%n", "config", "stale", "writeFail", "readFail", "hinted", "repair");
        for (QuorumConfig cfg : configs) {
            int stale2 = 0, wf = 0, rf = 0, hinted = 0, repairsTotal = 0;
            for (long seed = 1; seed <= 20; seed++) {
                Experiment.Outcome o = Experiment.run(cfg, 5, seed, 0.05, 400, 25);
                stale2 += o.staleReads(); wf += o.writeFailures(); rf += o.readFailures(); hinted += o.hintedWrites(); repairsTotal += o.repairs();
            }
            System.out.printf("%-34s %8d %10d %10d %7d %7d%n", cfg, stale2, wf, rf, hinted, repairsTotal);
        }
        System.out.println("R + W > N buys zero stale reads only when every ack came from a home replica; sloppy quorums trade that for availability.");
    }
}
