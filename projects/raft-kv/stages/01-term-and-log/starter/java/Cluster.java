// Project: projects/raft-kv — test harness (PROVIDED; do not edit).
// Wires N RaftNodes onto one simulated Network, drives the clock, checks the Figure 3 safety properties after every
// delivery, and records client operations as a History for the Stage 4 linearizability check.
// Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

final class Cluster {
    /** A client operation the harness is waiting on: proposed to `leaderId` at log position (index, term). */
    record ClientOp(int opId, String client, String command, int leaderId, int index, long term, int invokedAt) {}

    final int size;
    final long seed;
    final Config config;
    final Clock clock = new Clock();
    final Network network;
    final List<Storage> storages = new ArrayList<>();
    final List<RaftNode> nodes = new ArrayList<>();
    final List<String> events = new ArrayList<>();
    final Map<Long, Integer> leadersByTerm = new TreeMap<>();
    final Map<Integer, LogEntry> committedByIndex = new TreeMap<>();
    final List<ClientOp> pending = new ArrayList<>();
    final List<Op> completed = new ArrayList<>();
    final List<ClientOp> failed = new ArrayList<>();
    private final Map<Integer, List<LogEntry>> leaderLogSnapshot = new HashMap<>();
    private final int[] restarts;
    int proposals = 0;
    private int nextOpId = 0;

    Cluster(int size, long seed, double dropRate, int maxDelay, Config config) {
        this.size = size;
        this.seed = seed;
        this.config = config;
        this.network = new Network(clock, seed, dropRate, maxDelay);
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

    /** Each node gets its own rng stream derived from the seed, so the network's and the nodes' draws never interleave. */
    static Rng nodeRng(long seed, int id, int restart) { return new Rng(seed * 7919L + 1000003L * (id + 1) + 17L * restart); }

    void trace(int now, String text) { events.add(String.format("t=%-4d %s", now, text)); }

    int now() { return clock.now(); }
    RaftNode node(int id) { return nodes.get(id); }

    void tick() { network.tick(); checkInvariants(); settlePending(); }

    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    /** Run until exactly one live node leads and every other live node follows it in the same term. Returns the tick, or -1. */
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

    long maxTerm() {
        long t = 0;
        for (RaftNode n : nodes) t = Math.max(t, n.currentTerm());
        return t;
    }

    // ------------------------------------------------------------------ proposals (Stage 3) and client ops (Stage 4)

    /** Submit a command to the current stable leader and replicate at once; returns its log index or -1. */
    int propose(String command) {
        RaftNode l = stableLeader();
        return l == null ? -1 : proposeTo(l.id, command);
    }

    /** Submit to a specific node (which may be a stale leader inside a partition). */
    int proposeTo(int nodeId, String command) {
        RaftNode n = nodes.get(nodeId);
        if (network.isDown(nodeId)) return -1;
        int index = n.propose(command);
        if (index > 0) {
            proposals++;
            trace(now(), RaftNode.name(nodeId) + " appends index " + index + " " + n.log().get(index) + " and replicates");
            network.sendAll(n.replicateAll(now()));
        }
        return index;
    }

    /**
     * A client submits PUT/GET through the stable leader. The op is pending until the node that accepted it applies that
     * log position: same term -> completed with the apply-time result; different term -> the entry was overwritten, failed.
     * Returns the op id, or -1 when there is no stable leader (the client should retry later).
     */
    int submit(String client, String command) {
        RaftNode l = stableLeader();
        if (l == null) return -1;
        int invokedAt = now();
        int index = proposeTo(l.id, command);
        if (index < 0) return -1;
        int opId = nextOpId++;
        pending.add(new ClientOp(opId, client, command, l.id, index, l.currentTerm(), invokedAt));
        return opId;
    }

    /** Retry submit every 10 ticks until a leader accepts, like a client that follows redirects. */
    int submitUntilAccepted(String client, String command, int maxAttempts) {
        for (int attempt = 0; attempt < maxAttempts; attempt++) {
            int opId = submit(client, command);
            if (opId >= 0) return opId;
            run(10);
        }
        throw new IllegalStateException("no leader accepted " + command + " after " + maxAttempts + " attempts");
    }

    private void settlePending() {
        for (int i = pending.size() - 1; i >= 0; i--) {
            ClientOp p = pending.get(i);
            RaftNode n = nodes.get(p.leaderId());
            if (n.lastApplied < p.index()) continue;
            pending.remove(i);
            if (n.storage.termAt(p.index()) == p.term()) {
                String[] parts = p.command().split(" ", 3);
                OpKind kind = parts[0].equals("GET") ? OpKind.GET : OpKind.PUT;
                String value = kind == OpKind.GET ? n.stateMachine.results.get(p.index() - 1) : parts[2];
                completed.add(new Op(p.opId(), p.client(), kind, parts[1], value, p.invokedAt(), now()));
            } else {
                failed.add(p);
                trace(now(), "client op #" + p.opId() + " " + p.command() + " lost: index " + p.index() + " was overwritten");
            }
        }
    }

    int pendingCount() { return pending.size(); }

    /** Run until no client op is pending, or maxTicks pass. Returns the tick or -1. */
    int runUntilSettled(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) { if (pending.isEmpty()) return now(); tick(); }
        return pending.isEmpty() ? now() : -1;
    }

    /** The completed ops as a History (ids renumbered densely in completion order). Pending ops are an error. */
    History history() {
        if (!pending.isEmpty()) throw new IllegalStateException(pending.size() + " client ops still pending; run until settled first");
        History h = new History();
        for (Op o : completed) h.add(o.client(), o.kind(), o.key(), o.value(), o.invokedAt(), o.returnedAt());
        return h;
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

    /** Run until every live node has the same log, or maxTicks pass. Returns the tick or -1. */
    int runUntilLogsConverged(int maxTicks) {
        for (int i = 0; i < maxTicks; i++) { tick(); if (logsConverged()) return now(); }
        return -1;
    }

    boolean logsConverged() {
        List<LogEntry> first = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id)) continue;
            if (first == null) first = n.log(); else if (!first.equals(n.log())) return false;
        }
        return true;
    }

    // ------------------------------------------------------------------ invariants (Figure 3)

    /** Election Safety, Leader Append-Only, Log Matching, and State Machine Safety, checked after every delivery. */
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
        for (int i = 0; i < size; i++) for (int j = i + 1; j < size; j++) checkLogMatching(nodes.get(i).log(), nodes.get(j).log(), i, j);
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

    // ------------------------------------------------------------------ faults

    void partition(Set<Integer> sideA) { network.partition(sideA); trace(now(), "network partition: " + new TreeSet<>(sideA) + " | rest"); }
    void heal() { network.heal(); trace(now(), "network heals"); }
    void crash(int id) { network.crash(id); trace(now(), RaftNode.name(id) + " crashes (volatile state lost, Storage kept)"); }

    /** Restart from stable storage: term, vote, and log survive; commitIndex, lastApplied, and the KV store are rebuilt. */
    RaftNode restart(int id) {
        restarts[id]++;
        RaftNode n = new RaftNode(id, peersOf(id), storages.get(id), nodeRng(seed, id, restarts[id]), config, now(), this::trace);
        nodes.set(id, n);
        network.restart(n);
        trace(now(), RaftNode.name(id) + " restarts: term " + n.currentTerm() + ", log length " + n.lastLogIndex() + ", commitIndex 0, empty state machine");
        return n;
    }

    /** Force a node's election timer to expire on the next tick (for scripted scenarios). */
    void expireTimer(int id) { nodes.get(id).electionDeadline = now(); }

    boolean stateMachinesConverged() {
        String first = null;
        for (RaftNode n : nodes) {
            if (network.isDown(n.id)) continue;
            String s = n.stateMachine.toString() + "@" + n.lastApplied;
            if (first == null) first = s; else if (!first.equals(s)) return false;
        }
        return true;
    }

    String logOf(int id) { return nodes.get(id).storage.describe(); }
}
