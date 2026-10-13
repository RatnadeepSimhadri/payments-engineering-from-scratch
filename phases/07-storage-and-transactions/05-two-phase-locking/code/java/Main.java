// Lesson: phases/07-storage-and-transactions/05-two-phase-locking/docs/en.md
// Two-Phase Locking and Deadlock Detection
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Eswaran, Gray, Lorie, Traiger, "The Notions of Consistency and Predicate Locks in a Database System"
//          (CACM 1976); Gray & Reuter, Transaction Processing (1993) ch. 7-8. Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

// ---------------------------------------------------------------------------------------------
// Money (Phase 10, Lesson 01 design): integer minor units plus a currency. Never a float.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent: " + exponent);
    }
}

record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch");
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return String.format("%s%d.%0" + currency.exponent() + "d %s", sign, whole, frac, currency.code());
    }
}

// ---------------------------------------------------------------------------------------------
// Lock manager: shared/exclusive locks per key, FIFO wait queues, and the wait-for graph.
// ---------------------------------------------------------------------------------------------

enum LockMode {
    S, X;

    /** Compatibility matrix: S/S is the only pair that can coexist. */
    boolean compatibleWith(LockMode other) { return this == S && other == S; }
}

/** Thrown when a transaction requests a lock after it has released one: the two-phase rule. */
final class TwoPhaseViolationException extends RuntimeException {
    TwoPhaseViolationException(String message) { super(message); }
}

record LockRequest(int tx, LockMode mode) {}

/** Per-key state: who holds the lock in which mode, and who is queued behind them in arrival order. */
final class LockEntry {
    final Map<Integer, LockMode> holders = new LinkedHashMap<>();
    final Deque<LockRequest> queue = new ArrayDeque<>();

    /** Could {@code tx} hold {@code mode} alongside every other current holder? Self is ignored (upgrades). */
    boolean compatible(int tx, LockMode mode) {
        for (Map.Entry<Integer, LockMode> h : holders.entrySet()) {
            if (h.getKey() != tx && !mode.compatibleWith(h.getValue())) return false;
        }
        return true;
    }

    boolean isEmpty() { return holders.isEmpty() && queue.isEmpty(); }
}

final class LockManager {
    final TreeMap<String, LockEntry> table = new TreeMap<>();
    final List<String> log;

    LockManager(List<String> log) { this.log = log; }

    /**
     * Request a lock. Returns true if granted now, false if the request was queued and the caller must wait.
     * FIFO: a request behind a queued request waits even if it would be compatible with the current holders,
     * so a stream of readers cannot starve a writer. An upgrade (S held, X wanted) is queued at the front.
     */
    boolean acquire(int tx, String key, LockMode mode) {
        LockEntry e = table.computeIfAbsent(key, k -> new LockEntry());
        LockMode held = e.holders.get(tx);
        if (held == LockMode.X || (held == LockMode.S && mode == LockMode.S)) return true;   // already covered
        boolean upgrade = held == LockMode.S && mode == LockMode.X;
        if ((e.queue.isEmpty() || upgrade) && e.compatible(tx, mode)) {
            e.holders.put(tx, mode);
            log.add("  T" + tx + " " + mode + "(" + key + ") granted");
            return true;
        }
        if (upgrade) e.queue.addFirst(new LockRequest(tx, mode)); else e.queue.addLast(new LockRequest(tx, mode));
        StringBuilder who = new StringBuilder();
        for (int b : blockersOf(tx, key, mode)) who.append(who.isEmpty() ? "" : ",").append("T").append(b);
        log.add("  T" + tx + " " + mode + "(" + key + ") WAITS for " + who);
        return false;
    }

    /** The transactions {@code tx} would wait for if it requested {@code mode} on {@code key} now. */
    Set<Integer> blockersOf(int tx, String key, LockMode mode) {
        Set<Integer> out = new TreeSet<>();
        LockEntry e = table.get(key);
        if (e == null) return out;
        for (Map.Entry<Integer, LockMode> h : e.holders.entrySet()) {
            if (h.getKey() != tx && !mode.compatibleWith(h.getValue())) out.add(h.getKey());
        }
        for (LockRequest ahead : e.queue) {
            if (ahead.tx() == tx) break;                                  // only requests ahead of ours
            if (!mode.compatibleWith(ahead.mode())) out.add(ahead.tx());
        }
        return out;
    }

    /** Release one lock early (basic 2PL). Returns the transactions whose queued requests were granted. */
    List<Integer> release(int tx, String key) {
        LockEntry e = table.get(key);
        if (e == null || e.holders.remove(tx) == null) return List.of();
        log.add("  T" + tx + " releases " + key);
        List<Integer> granted = grantWaiters(key, e);
        if (e.isEmpty()) table.remove(key);
        return granted;
    }

    /** Release every lock {@code tx} holds or waits for (commit or abort). Returns newly granted transactions. */
    List<Integer> releaseAll(int tx) {
        List<Integer> granted = new ArrayList<>();
        List<String> keys = new ArrayList<>(table.keySet());
        for (String key : keys) {
            LockEntry e = table.get(key);
            boolean held = e.holders.remove(tx) != null;
            e.queue.removeIf(r -> r.tx() == tx);
            if (held) granted.addAll(grantWaiters(key, e));
            if (e.isEmpty()) table.remove(key);
        }
        return granted;
    }

    private List<Integer> grantWaiters(String key, LockEntry e) {
        List<Integer> granted = new ArrayList<>();
        while (!e.queue.isEmpty() && e.compatible(e.queue.peekFirst().tx(), e.queue.peekFirst().mode())) {
            LockRequest r = e.queue.pollFirst();
            e.holders.put(r.tx(), r.mode());
            log.add("  T" + r.tx() + " " + r.mode() + "(" + key + ") granted (was waiting)");
            granted.add(r.tx());
        }
        return granted;
    }

    Set<String> keysHeldBy(int tx) {
        Set<String> out = new TreeSet<>();
        for (Map.Entry<String, LockEntry> e : table.entrySet()) if (e.getValue().holders.containsKey(tx)) out.add(e.getKey());
        return out;
    }

    /** Edges tx -> every transaction it is waiting for, derived from the queues. */
    WaitForGraph waitForGraph() {
        WaitForGraph g = new WaitForGraph();
        for (Map.Entry<String, LockEntry> e : table.entrySet()) {
            for (LockRequest r : e.getValue().queue) {
                for (int blocker : blockersOf(r.tx(), e.getKey(), r.mode())) g.addEdge(r.tx(), blocker);
            }
        }
        return g;
    }
}

/** Directed graph "waits for". A cycle is a deadlock: nobody on it will ever be granted. */
final class WaitForGraph {
    final TreeMap<Integer, TreeSet<Integer>> edges = new TreeMap<>();

    void addEdge(int from, int to) { edges.computeIfAbsent(from, k -> new TreeSet<>()).add(to); }

    /** Depth-first search with three colours; returns the first cycle found, in wait order, or empty. */
    Optional<List<Integer>> findCycle() {
        Map<Integer, Integer> colour = new TreeMap<>();       // 0 white, 1 grey (on stack), 2 black
        Deque<Integer> path = new ArrayDeque<>();
        for (int start : edges.keySet()) {
            Optional<List<Integer>> c = dfs(start, colour, path);
            if (c.isPresent()) return c;
        }
        return Optional.empty();
    }

    private Optional<List<Integer>> dfs(int node, Map<Integer, Integer> colour, Deque<Integer> path) {
        int c = colour.getOrDefault(node, 0);
        if (c == 2) return Optional.empty();
        if (c == 1) {                                         // back edge: the cycle is the path from node to here
            List<Integer> cycle = new ArrayList<>();
            boolean in = false;
            for (int n : path) { if (n == node) in = true; if (in) cycle.add(n); }
            return Optional.of(cycle);
        }
        colour.put(node, 1);
        path.addLast(node);
        for (int next : edges.getOrDefault(node, new TreeSet<>())) {
            Optional<List<Integer>> found = dfs(next, colour, path);
            if (found.isPresent()) return found;
        }
        path.removeLast();
        colour.put(node, 2);
        return Optional.empty();
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder();
        for (Map.Entry<Integer, TreeSet<Integer>> e : edges.entrySet()) {
            for (int to : e.getValue()) sb.append(sb.isEmpty() ? "" : ", ").append("T").append(e.getKey()).append(" -> T").append(to);
        }
        return sb.isEmpty() ? "(no waits)" : sb.toString();
    }
}

// ---------------------------------------------------------------------------------------------
// Transactions, operations, and the scheduler that interleaves them deterministically.
// ---------------------------------------------------------------------------------------------

enum VictimPolicy {
    /** Abort the transaction that started last: it has presumably done the least work. */
    YOUNGEST,
    /** Abort the transaction holding the fewest locks: the cheapest to undo and the least likely to be a batch job. */
    FEWEST_LOCKS
}

/** The steps a scripted transaction can take. {@code Add} is a read-modify-write that needs an X lock. */
sealed interface Op permits Op.Read, Op.Add, Op.Unlock, Op.Commit {
    record Read(String key) implements Op {}
    record Add(String key, Money delta) implements Op {}
    record Unlock(String key) implements Op {}
    record Commit() implements Op {}
}

final class Txn {
    enum Status { ACTIVE, BLOCKED, COMMITTED, ABORTED }
    enum Phase { GROWING, SHRINKING }

    final int id;
    final List<Op> script;
    int pc = 0;
    Status status = Status.ACTIVE;
    Phase phase = Phase.GROWING;
    int restarts = 0;
    final Map<String, Money> undo = new LinkedHashMap<>();      // key -> value before our first in-place write
    final Map<String, Money> reads = new LinkedHashMap<>();     // what this transaction observed (for tests)

    Txn(int id, List<Op> script) { this.id = id; this.script = List.copyOf(script); }

    boolean finished() { return status == Status.COMMITTED || status == Status.ABORTED; }
}

final class Scheduler {
    final Map<String, Money> db = new TreeMap<>();
    final List<String> log = new ArrayList<>();
    final LockManager locks = new LockManager(log);
    final Map<Integer, Txn> txns = new TreeMap<>();
    final VictimPolicy policy;
    final boolean restartVictims;
    int nextId = 1;
    int deadlocksDetected = 0;
    final List<List<Integer>> cycles = new ArrayList<>();
    final List<Integer> victims = new ArrayList<>();

    Scheduler(VictimPolicy policy, boolean restartVictims) {
        this.policy = policy;
        this.restartVictims = restartVictims;
    }

    void load(String key, Money value) { db.put(key, value); }

    Txn begin(List<Op> script) {
        Txn t = new Txn(nextId++, script);
        txns.put(t.id, t);
        log.add("T" + t.id + " begin");
        return t;
    }

    /**
     * Drive the transactions. {@code order} names which transaction takes its next step at each tick; a blocked or
     * finished transaction's turn is skipped. When the explicit order is exhausted, remaining steps run round-robin.
     */
    void run(int... order) {
        for (int id : order) step(txns.get(id));
        int guard = 0;
        while (txns.values().stream().anyMatch(t -> !t.finished())) {
            boolean progressed = false;
            for (Txn t : new ArrayList<>(txns.values())) progressed |= step(t);
            if (!progressed) {
                detectAndResolve();
                if (++guard > 10_000) throw new IllegalStateException("scheduler stuck: " + locks.waitForGraph());
            } else guard = 0;
        }
    }

    /** Execute one step of {@code t}. Returns true if the transaction advanced. */
    boolean step(Txn t) {
        if (t.status != Txn.Status.ACTIVE) return false;
        Op op = t.script.get(t.pc);
        if (execute(t, op)) { t.pc++; return true; }
        t.status = Txn.Status.BLOCKED;
        detectAndResolve();
        return true;                                           // blocking is progress for the scheduler's purposes
    }

    private boolean execute(Txn t, Op op) {
        switch (op) {
            case Op.Read r -> {
                if (!lock(t, r.key(), LockMode.S)) return false;
                Money v = db.get(r.key());
                t.reads.put(r.key(), v);
                log.add("  T" + t.id + " read " + r.key() + " = " + v);
            }
            case Op.Add a -> {
                if (!lock(t, a.key(), LockMode.X)) return false;
                Money before = db.get(a.key());
                t.undo.putIfAbsent(a.key(), before);
                db.put(a.key(), before.plus(a.delta()));          // in place: strict 2PL keeps it invisible until commit
                log.add("  T" + t.id + " " + a.key() + " " + before + " -> " + db.get(a.key()));
            }
            case Op.Unlock u -> {
                t.phase = Txn.Phase.SHRINKING;
                grant(locks.release(t.id, u.key()));
            }
            case Op.Commit c -> commit(t);
        }
        return true;
    }

    private boolean lock(Txn t, String key, LockMode mode) {
        if (t.phase == Txn.Phase.SHRINKING && !locks.keysHeldBy(t.id).contains(key)) {
            throw new TwoPhaseViolationException("T" + t.id + " requested " + mode + "(" + key + ") after releasing a lock");
        }
        return locks.acquire(t.id, key, mode);
    }

    void commit(Txn t) {
        t.status = Txn.Status.COMMITTED;
        log.add("  T" + t.id + " COMMIT");
        grant(locks.releaseAll(t.id));
    }

    void abort(Txn t) {
        List<Map.Entry<String, Money>> entries = new ArrayList<>(t.undo.entrySet());
        for (int i = entries.size() - 1; i >= 0; i--) db.put(entries.get(i).getKey(), entries.get(i).getValue());
        t.undo.clear();
        t.status = Txn.Status.ABORTED;
        log.add("  T" + t.id + " ABORT (undo " + entries.size() + " write" + (entries.size() == 1 ? "" : "s") + ")");
        grant(locks.releaseAll(t.id));
        if (restartVictims) {
            t.status = Txn.Status.ACTIVE;
            t.phase = Txn.Phase.GROWING;
            t.pc = 0;
            t.reads.clear();
            t.restarts++;
            log.add("  T" + t.id + " restarted (attempt " + (t.restarts + 1) + ")");
        }
    }

    /** Transactions whose queued request was just granted continue with the step they were blocked on. */
    private void grant(List<Integer> granted) {
        for (int id : granted) {
            Txn w = txns.get(id);
            if (w.status != Txn.Status.BLOCKED) continue;
            w.status = Txn.Status.ACTIVE;
            if (execute(w, w.script.get(w.pc))) w.pc++; else w.status = Txn.Status.BLOCKED;
        }
    }

    /** Build the wait-for graph; if it has a cycle, pick a victim by policy and abort it. Returns the victim id. */
    Optional<Integer> detectAndResolve() {
        WaitForGraph g = locks.waitForGraph();
        Optional<List<Integer>> cycle = g.findCycle();
        if (cycle.isEmpty()) return Optional.empty();
        deadlocksDetected++;
        cycles.add(cycle.get());
        int victim = chooseVictim(cycle.get());
        victims.add(victim);
        log.add("  DEADLOCK " + describe(cycle.get()) + " -> victim T" + victim + " (" + policy + ")");
        abort(txns.get(victim));
        return Optional.of(victim);
    }

    int chooseVictim(List<Integer> cycle) {
        return switch (policy) {
            case YOUNGEST -> cycle.stream().mapToInt(Integer::intValue).max().orElseThrow();
            case FEWEST_LOCKS -> {
                int best = cycle.get(0);
                for (int id : cycle) {
                    int mine = locks.keysHeldBy(id).size(), theirs = locks.keysHeldBy(best).size();
                    if (mine < theirs || (mine == theirs && id > best)) best = id;   // tie: youngest
                }
                yield best;
            }
        };
    }

    static String describe(List<Integer> cycle) {
        StringBuilder sb = new StringBuilder();
        for (int id : cycle) sb.append("T").append(id).append(" -> ");
        return sb.append("T").append(cycle.get(0)).toString();
    }

    Money total(Currency currency) {
        Money sum = Money.ofMinor(0, currency);
        for (Money m : db.values()) sum = sum.plus(m);
        return sum;
    }

    boolean lockTableEmpty() { return locks.table.isEmpty(); }
}

// ---------------------------------------------------------------------------------------------
// Worked example
// ---------------------------------------------------------------------------------------------

public final class Main {
    static final Currency USD = Currency.USD;

    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    /** Transfer {@code amount} from {@code from} to {@code to}: two read-modify-writes under X locks, then commit. */
    static List<Op> transfer(String from, String to, long amountMinor) {
        return List.of(new Op.Add(from, usd(-amountMinor)), new Op.Add(to, usd(amountMinor)), new Op.Commit());
    }

    public static void main(String[] args) {
        System.out.println("== Two transfers in opposite directions: A->B and B->A, strict 2PL ==");
        Scheduler s = new Scheduler(VictimPolicy.YOUNGEST, true);
        s.load("acct:A", usd(100_000));
        s.load("acct:B", usd(50_000));
        s.begin(transfer("acct:A", "acct:B", 10_000));     // T1: 100.00 from A to B
        s.begin(transfer("acct:B", "acct:A", 5_000));      // T2:  50.00 from B to A
        s.run(1, 2, 1, 2);                                  // T1 locks A, T2 locks B, T1 wants B, T2 wants A
        s.log.forEach(System.out::println);
        System.out.println("  => deadlocks detected: " + s.deadlocksDetected + ", victims: " + s.victims);
        System.out.println("  => final A = " + s.db.get("acct:A") + ", B = " + s.db.get("acct:B") + ", total = " + s.total(USD));

        System.out.println();
        System.out.println("== Same shape, FEWEST_LOCKS policy: T2 also debits a fee account, so it holds two locks ==");
        Scheduler f = new Scheduler(VictimPolicy.FEWEST_LOCKS, true);
        f.load("acct:A", usd(100_000));
        f.load("acct:B", usd(50_000));
        f.load("fees", usd(0));
        f.begin(transfer("acct:A", "acct:B", 10_000));
        f.begin(List.of(new Op.Add("acct:B", usd(-5_050)), new Op.Add("fees", usd(50)), new Op.Add("acct:A", usd(5_000)), new Op.Commit()));
        f.run(1, 2, 2, 1, 2);                                // T2 holds B and fees (2 locks) when the cycle forms; T1 holds A
        f.log.stream().filter(l -> l.contains("DEADLOCK") || l.contains("ABORT")).forEach(System.out::println);
        System.out.println("  => victim T" + f.victims.get(0) + ": T1 holds 1 lock, T2 holds 2, so the cheaper rollback is T1");

        System.out.println();
        System.out.println("== Lock upgrade deadlock: two deposits each read the balance (S) then write it (X) ==");
        Scheduler u = new Scheduler(VictimPolicy.YOUNGEST, true);
        u.load("acct:A", usd(100_000));
        u.begin(List.of(new Op.Read("acct:A"), new Op.Add("acct:A", usd(1_000)), new Op.Commit()));
        u.begin(List.of(new Op.Read("acct:A"), new Op.Add("acct:A", usd(2_000)), new Op.Commit()));
        u.run(1, 2, 1, 2);
        u.log.forEach(System.out::println);
        System.out.println("  => final A = " + u.db.get("acct:A") + " (both deposits landed; no lost update)");

        System.out.println();
        System.out.println("== Why strict: basic 2PL releases X(A) before commit, a reader sees a balance that never existed ==");
        Scheduler b = new Scheduler(VictimPolicy.YOUNGEST, false);
        b.load("acct:A", usd(100_000));
        Txn writer = b.begin(List.of(new Op.Add("acct:A", usd(-90_000)), new Op.Unlock("acct:A"), new Op.Read("acct:A")));
        Txn reader = b.begin(List.of(new Op.Read("acct:A"), new Op.Commit()));
        b.step(writer);                                      // X(A), A := 10.00
        b.step(writer);                                      // releases A early: legal in basic 2PL, phase -> SHRINKING
        b.step(reader);                                      // reads 10.00, an uncommitted value
        b.step(reader);                                      // and commits on it
        b.abort(writer);                                     // writer rolls back: A is 100.00 again
        b.log.forEach(System.out::println);
        System.out.println("  => T2 committed having read " + reader.reads.get("acct:A") + "; the committed balance is " + b.db.get("acct:A"));
        try {
            Scheduler b2 = new Scheduler(VictimPolicy.YOUNGEST, false);
            b2.load("k", usd(1));
            b2.load("j", usd(1));
            Txn t = b2.begin(List.of(new Op.Add("k", usd(1)), new Op.Unlock("k"), new Op.Read("j")));
            b2.step(t); b2.step(t); b2.step(t);
        } catch (TwoPhaseViolationException e) {
            System.out.println("  => " + e.getMessage() + ": the two-phase rule forbids growing after shrinking");
        }
    }
}
