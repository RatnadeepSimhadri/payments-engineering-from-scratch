// Lesson: phases/06-replication-and-consensus/12-linearizability-checking/docs/en.md
// Linearizability Checking with Recorded Histories
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Herlihy & Wing, "Linearizability: A Correctness Condition for Concurrent Objects" (TOPLAS 1990);
//          Wing & Gong, "Testing and Verifying Concurrent Objects" (JPDC 1993); Lowe, "Testing for Linearizability" (CCPE 2017).
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

enum OpKind { READ, WRITE }

/** The consistency model the checker searches for. Both use the same search; only the "may go next" rule differs. */
enum Consistency { LINEARIZABLE, SEQUENTIAL }

/**
 * One completed client operation on a key-value register. For a WRITE, value is what was written; for a READ, value is
 * what the client got back. invokedAt and returnedAt are the client's own timestamps, taken before sending and after
 * receiving. An op that returned before another was invoked precedes it in real time; otherwise they are concurrent.
 */
record Op(int id, String client, OpKind kind, String key, int value, int invokedAt, int returnedAt) {
    Op {
        if (returnedAt < invokedAt) throw new IllegalArgumentException("op " + id + " returned before it was invoked");
    }

    boolean precedes(Op other) { return returnedAt < other.invokedAt; }

    boolean concurrentWith(Op other) { return !precedes(other) && !other.precedes(this); }

    @Override public String toString() {
        String body = kind == OpKind.WRITE ? "write(" + key + "=" + value + ")" : "read(" + key + ")->" + value;
        return "#" + id + " " + client + " " + body + " [" + invokedAt + "," + returnedAt + "]";
    }
}

/** An ordered collection of completed ops. Ids are dense 0..n-1 so the checker can use a bitmask of linearized ops. */
final class History {
    static final int MAX_OPS = 64;
    private final List<Op> ops = new ArrayList<>();

    History add(String client, OpKind kind, String key, int value, int invokedAt, int returnedAt) {
        if (ops.size() >= MAX_OPS) throw new IllegalArgumentException("this checker handles at most " + MAX_OPS + " ops per history");
        ops.add(new Op(ops.size(), client, kind, key, value, invokedAt, returnedAt));
        return this;
    }

    History write(String client, String key, int value, int invokedAt, int returnedAt) { return add(client, OpKind.WRITE, key, value, invokedAt, returnedAt); }

    History read(String client, String key, int value, int invokedAt, int returnedAt) { return add(client, OpKind.READ, key, value, invokedAt, returnedAt); }

    List<Op> ops() { return List.copyOf(ops); }

    int size() { return ops.size(); }

    Op op(int id) { return ops.get(id); }
}

/** Injected clock: the recorder never reads System.nanoTime, so recorded histories are reproducible. */
final class FakeClock {
    private int now = 0;

    int now() { return now; }

    int tick() { return ++now; }

    void advance(int ticks) { now += ticks; }
}

/**
 * Records a history the way a test harness would: stamp the invocation before the request leaves, stamp the return
 * after the response arrives. The op's value is unknown for a READ until completion, so pending ops are kept apart.
 */
final class HistoryRecorder {
    private record Pending(String client, OpKind kind, String key, int value, int invokedAt) {}

    private final FakeClock clock;
    private final Map<Integer, Pending> pending = new HashMap<>();
    private final List<Op> completed = new ArrayList<>();
    private int nextId = 0;

    HistoryRecorder(FakeClock clock) { this.clock = clock; }

    int invokeWrite(String client, String key, int value) { return invoke(client, OpKind.WRITE, key, value); }

    int invokeRead(String client, String key) { return invoke(client, OpKind.READ, key, 0); }

    private int invoke(String client, OpKind kind, String key, int value) {
        int id = nextId++;
        pending.put(id, new Pending(client, kind, key, value, clock.now()));
        return id;
    }

    /** Complete a write (value ignored) or a read (value is what came back). The return timestamp is taken now. */
    void complete(int id, int value) {
        Pending p = pending.remove(id);
        if (p == null) throw new IllegalArgumentException("no pending op " + id);
        int v = p.kind() == OpKind.WRITE ? p.value() : value;
        completed.add(new Op(id, p.client(), p.kind(), p.key(), v, p.invokedAt(), clock.now()));
    }

    int pendingCount() { return pending.size(); }

    /** The history so far. Ops are renumbered densely in invocation order; pending ops are an error (see Exercise 2). */
    History history() {
        if (!pending.isEmpty()) throw new IllegalStateException(pending.size() + " ops never completed; this checker needs a complete history");
        List<Op> sorted = new ArrayList<>(completed);
        sorted.sort((a, b) -> a.id() - b.id());
        History h = new History();
        for (Op o : sorted) h.add(o.client(), o.kind(), o.key(), o.value(), o.invokedAt(), o.returnedAt());
        return h;
    }
}

/**
 * The sequential specification: a map of registers, each initially 0. step returns the next state, or null if the op's
 * observed result is impossible from this state (a read that returns anything but the current value).
 */
final class RegisterModel {
    Map<String, Integer> initial() { return new TreeMap<>(); }

    Map<String, Integer> step(Map<String, Integer> state, Op op) {
        int current = state.getOrDefault(op.key(), 0);
        if (op.kind() == OpKind.READ) return current == op.value() ? state : null;
        Map<String, Integer> next = new TreeMap<>(state);
        next.put(op.key(), op.value());
        return next;
    }

    /** Canonical, hashable form for the memo table: "a=1,b=2". TreeMap makes it deterministic. */
    String key(Map<String, Integer> state) {
        StringBuilder sb = new StringBuilder();
        for (Map.Entry<String, Integer> e : state.entrySet()) {
            if (sb.length() > 0) sb.append(',');
            sb.append(e.getKey()).append('=').append(e.getValue());
        }
        return sb.toString();
    }
}

/** The verdict plus what the search did: the witness order if ok, the longest prefix it ever linearized if not. */
record CheckResult(boolean ok, Consistency model, List<Integer> order, List<Integer> longestPrefix, int explored, int memoHits) {
    String describe(History h) {
        StringBuilder sb = new StringBuilder();
        if (ok) {
            sb.append(model).append(": yes. Witness order:");
            for (int id : order) sb.append("\n  ").append(h.op(id));
        } else {
            sb.append(model).append(": NO. Longest ").append(model.name().toLowerCase()).append(" prefix (").append(longestPrefix.size()).append(" of ").append(h.size()).append(" ops):");
            for (int id : longestPrefix) sb.append("\n  ").append(h.op(id));
            sb.append("\n  no remaining op can go next from here");
        }
        sb.append("\n  (").append(explored).append(" states explored, ").append(memoHits).append(" memo hits)");
        return sb.toString();
    }
}

/**
 * Wing-Gong backtracking search with Lowe's (linearized-set, state) memo. Finds a sequential order of all ops that
 * (a) the model accepts and (b) respects real time (LINEARIZABLE) or per-client program order (SEQUENTIAL).
 */
final class Checker {
    private final RegisterModel model;
    private final boolean memoize;
    private final int stateBudget;

    private List<Op> ops;
    private Consistency consistency;
    private Set<String> failedConfigs;
    private List<Integer> order;
    private List<Integer> longestPrefix;
    private int explored;
    private int memoHits;

    Checker(RegisterModel model) { this(model, true, 5_000_000); }

    Checker(RegisterModel model, boolean memoize, int stateBudget) {
        this.model = model;
        this.memoize = memoize;
        this.stateBudget = stateBudget;
    }

    CheckResult check(History history, Consistency consistency) {
        this.ops = history.ops();
        this.consistency = consistency;
        this.failedConfigs = new HashSet<>();
        this.order = new ArrayList<>();
        this.longestPrefix = new ArrayList<>();
        this.explored = 0;
        this.memoHits = 0;
        boolean ok = search(0L, model.initial());
        return new CheckResult(ok, consistency, List.copyOf(order), List.copyOf(longestPrefix), explored, memoHits);
    }

    CheckResult check(History history) { return check(history, Consistency.LINEARIZABLE); }

    private boolean search(long done, Map<String, Integer> state) {
        explored++;
        if (explored > stateBudget) throw new IllegalStateException("state budget of " + stateBudget + " exceeded; history too concurrent for this checker");
        if (order.size() > longestPrefix.size()) longestPrefix = new ArrayList<>(order);
        if (Long.bitCount(done) == ops.size()) return true;
        String memoKey = memoize ? done + "|" + model.key(state) : null;
        if (memoize && failedConfigs.contains(memoKey)) { memoHits++; return false; }
        for (int i = 0; i < ops.size(); i++) {
            if ((done & (1L << i)) != 0 || !mayGoNext(i, done)) continue;
            Map<String, Integer> next = model.step(state, ops.get(i));
            if (next == null) continue;
            order.add(i);
            if (search(done | (1L << i), next)) return true;
            order.remove(order.size() - 1);
        }
        if (memoize) failedConfigs.add(memoKey);
        return false;
    }

    /**
     * LINEARIZABLE: op i may go next only if no not-yet-linearized op returned before i was invoked (i is "minimal" in
     * the real-time partial order). SEQUENTIAL: only if no not-yet-linearized op of the same client was invoked earlier.
     */
    private boolean mayGoNext(int i, long done) {
        Op candidate = ops.get(i);
        for (int j = 0; j < ops.size(); j++) {
            if (j == i || (done & (1L << j)) != 0) continue;
            Op other = ops.get(j);
            if (consistency == Consistency.LINEARIZABLE) {
                if (other.precedes(candidate)) return false;
            } else if (other.client().equals(candidate.client()) && other.invokedAt() < candidate.invokedAt()) {
                return false;
            }
        }
        return true;
    }
}

/** Builds histories that are linearizable by construction: pick a sequential run, then widen each op into an interval. */
final class HistoryGenerator {
    /** SplitMix64 (Steele, Lea, Flood 2014): identical streams in all three languages. */
    static final class Rng {
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
    }

    /**
     * n ops from `clients` clients on one key. Each op gets a linearization point t = 10*k and an interval
     * [t - a, t + b] with a, b < window, so overlapping intervals are common and a valid order always exists.
     */
    static History linearizable(long seed, int n, int clients, int window) {
        Rng rng = new Rng(seed);
        History h = new History();
        int value = 0;
        for (int k = 0; k < n; k++) {
            int point = 10 * (k + 1);
            int invoked = point - rng.nextInt(window), returned = point + rng.nextInt(window);
            String client = "c" + rng.nextInt(clients);
            if (rng.nextInt(2) == 0) { value = 1 + rng.nextInt(9); h.write(client, "x", value, invoked, returned); }
            else h.read(client, "x", value, invoked, returned);
        }
        return h;
    }
}

public final class Main {
    static void show(String title, History h, CheckResult r) {
        System.out.println("== " + title + " ==");
        for (Op o : h.ops()) System.out.println("  " + o);
        System.out.println(r.describe(h));
        System.out.println();
    }

    public static void main(String[] args) {
        Checker checker = new Checker(new RegisterModel());

        History good = new History()
                .write("ledger", "bal", 500, 0, 4)
                .write("ledger", "bal", 300, 6, 9)
                .read("risk", "bal", 300, 10, 12)
                .read("app", "bal", 300, 13, 15);
        show("A correct replica: every read sees the latest completed write", good, checker.check(good));

        History stale = new History()
                .write("ledger", "bal", 500, 0, 4)
                .write("ledger", "bal", 300, 6, 9)
                .read("risk", "bal", 500, 10, 12)
                .read("app", "bal", 300, 13, 15);
        show("A lagging replica: risk reads 500 after the write of 300 returned (stale read)", stale, checker.check(stale));

        History seqOnly = new History()
                .write("ledger", "bal", 300, 0, 4)
                .read("risk", "bal", 0, 6, 8);
        System.out.println("== Sequentially consistent but not linearizable ==");
        for (Op o : seqOnly.ops()) System.out.println("  " + o);
        System.out.println(checker.check(seqOnly, Consistency.LINEARIZABLE).describe(seqOnly));
        System.out.println(checker.check(seqOnly, Consistency.SEQUENTIAL).describe(seqOnly));
        System.out.println("  (sequential consistency may put risk's read before ledger's write; linearizability may not, because the write returned at 4 and the read was invoked at 6)");
        System.out.println();

        History window = new History()
                .write("ledger", "bal", 300, 0, 10)
                .read("risk", "bal", 0, 5, 15)
                .read("app", "bal", 300, 6, 8);
        show("Linearizable only because of the concurrency window: risk's read of 0 overlaps the write", window, checker.check(window));
        History shrunk = new History()
                .write("ledger", "bal", 300, 0, 10)
                .read("risk", "bal", 0, 11, 15)
                .read("app", "bal", 300, 6, 8);
        show("Same ops, but risk's read starts after the write returned", shrunk, checker.check(shrunk));

        System.out.println("== Recording a history with an injected clock: a read served by a replica 3 ticks behind ==");
        FakeClock clock = new FakeClock();
        HistoryRecorder rec = new HistoryRecorder(clock);
        int w1 = rec.invokeWrite("ledger", "bal", 500); clock.advance(2); rec.complete(w1, 0);
        clock.advance(1);
        int w2 = rec.invokeWrite("ledger", "bal", 300); clock.advance(2); rec.complete(w2, 0);
        clock.advance(1);
        int r1 = rec.invokeRead("risk", "bal"); clock.advance(1); rec.complete(r1, 500);   // replica had not applied w2 yet
        History recorded = rec.history();
        for (Op o : recorded.ops()) System.out.println("  " + o);
        System.out.println(checker.check(recorded).describe(recorded));
        System.out.println();

        System.out.println("== Why the memo matters: 8 fully concurrent writes followed by one read ==");
        History burst = new History();
        for (int i = 0; i < 8; i++) burst.write("c" + i, "x", i + 1, 0, 100);
        burst.read("reader", "x", 1, 101, 102);   // the FIRST write must be linearized last
        CheckResult withMemo = new Checker(new RegisterModel(), true, 5_000_000).check(burst);
        CheckResult withoutMemo = new Checker(new RegisterModel(), false, 5_000_000).check(burst);
        System.out.printf("  with memo:    ok=%b, %d states explored, %d memo hits%n", withMemo.ok(), withMemo.explored(), withMemo.memoHits());
        System.out.printf("  without memo: ok=%b, %d states explored (depth-first in id order keeps retrying permutations that put write(x=1) first)%n", withoutMemo.ok(), withoutMemo.explored());
        System.out.println();

        System.out.println("== 50 generated histories (20 ops, 3 clients, window 15) that are linearizable by construction ==");
        int passed = 0, maxExplored = 0;
        for (long seed = 1; seed <= 50; seed++) {
            CheckResult r = checker.check(HistoryGenerator.linearizable(seed, 20, 3, 15));
            if (r.ok()) passed++;
            maxExplored = Math.max(maxExplored, r.explored());
        }
        System.out.println("  passed " + passed + "/50; most states explored by any one check: " + maxExplored);
    }
}
