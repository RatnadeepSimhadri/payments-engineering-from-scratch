// Lesson: phases/04-concurrency/07-deadlocks-and-lock-ordering/docs/en.md
// Deadlocks, Lock Ordering, and Detection
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Coffman, Elphick & Shoshani, "System Deadlocks" (ACM Computing Surveys, 1971); java.util.concurrent.locks
// Javadoc (tryLock); PostgreSQL deadlock_timeout and wait-for-graph detector documentation.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.locks.ReentrantLock;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Exact amount in minor units of one currency; immutable. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    boolean isNegative() { return minorUnits < 0; }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

final class InsufficientFundsException extends IllegalStateException {
    InsufficientFundsException(String accountId, long balance, long requested) {
        super("account " + accountId + " has " + balance + " minor units, debit of " + requested + " refused");
    }
}

/** Thrown by LockTracker when granting a lock request would close a cycle in the wait-for graph. */
final class DeadlockDetectedException extends IllegalStateException {
    private final List<String> cycle;

    DeadlockDetectedException(List<String> cycle) {
        super("deadlock: " + String.join(" -> ", cycle) + " -> " + cycle.get(0));
        this.cycle = List.copyOf(cycle);
    }

    List<String> cycle() { return cycle; }
}

/**
 * A directed graph of "owner X waits for a lock held by owner Y". A cycle is a deadlock: every
 * owner on it is waiting for the next and none can proceed. Node order is sorted so the cycle
 * reported is the same on every run.
 */
final class WaitForGraph {
    private final Map<String, Set<String>> edges = new TreeMap<>();

    void addEdge(String waiter, String holder) {
        edges.computeIfAbsent(waiter, k -> new TreeSet<>()).add(holder);
        edges.computeIfAbsent(holder, k -> new TreeSet<>());
    }

    Set<String> nodes() { return Collections.unmodifiableSet(edges.keySet()); }

    Set<String> successors(String node) { return edges.getOrDefault(node, Set.of()); }

    /**
     * Returns the owners on the first cycle found (in cycle order, starting from the smallest
     * owner id on it), or an empty list. Iterative depth-first search with three colours:
     * white (unvisited), grey (on the current path), black (finished). A grey successor closes a cycle.
     */
    List<String> findCycle() {
        Map<String, Integer> colour = new HashMap<>();           // absent = white, 1 = grey, 2 = black
        Map<String, String> parent = new HashMap<>();
        for (String start : edges.keySet()) {
            if (colour.containsKey(start)) continue;
            Deque<String> stack = new ArrayDeque<>();
            stack.push(start);
            while (!stack.isEmpty()) {
                String node = stack.peek();
                if (!colour.containsKey(node)) {
                    colour.put(node, 1);
                    for (String next : successors(node)) {
                        Integer c = colour.get(next);
                        if (c == null) {
                            parent.put(next, node);
                            stack.push(next);
                        } else if (c == 1) {
                            return pathOfCycle(parent, node, next);  // back edge: next is on the current path
                        }
                    }
                } else {
                    stack.pop();
                    colour.put(node, 2);
                }
            }
        }
        return List.of();
    }

    /** Walk parents from `from` back to `to` (which is on the path), then rotate so the smallest id leads. */
    private static List<String> pathOfCycle(Map<String, String> parent, String from, String to) {
        List<String> cycle = new ArrayList<>();
        String cursor = from;
        while (!cursor.equals(to)) {
            cycle.add(cursor);
            cursor = parent.get(cursor);
        }
        cycle.add(to);
        Collections.reverse(cycle);                                // now in edge order: to -> ... -> from
        int smallest = 0;
        for (int i = 1; i < cycle.size(); i++) if (cycle.get(i).compareTo(cycle.get(smallest)) < 0) smallest = i;
        Collections.rotate(cycle, -smallest);
        return cycle;
    }
}

/**
 * Records every lock request, acquisition, and release so a wait-for graph can be built at any
 * moment. requesting() is the detector: it refuses a request that would complete a cycle, which
 * is how a database aborts one transaction instead of letting both hang.
 */
final class LockTracker {
    private final Object mutex = new Object();
    private final Map<String, String> holders = new LinkedHashMap<>();   // lockId -> owner
    private final Map<String, String> waiting = new LinkedHashMap<>();   // owner -> lockId
    private final List<String> events = new ArrayList<>();

    void requesting(String owner, String lockId) {
        synchronized (mutex) {
            waiting.put(owner, lockId);
            events.add(owner + " requests " + lockId);
            List<String> cycle = graph().findCycle();
            if (cycle.contains(owner)) {
                waiting.remove(owner);
                events.add(owner + " aborted: deadlock " + cycle);
                throw new DeadlockDetectedException(cycle);
            }
        }
    }

    void acquired(String owner, String lockId) {
        synchronized (mutex) {
            waiting.remove(owner);
            holders.put(lockId, owner);
            events.add(owner + " acquired " + lockId);
        }
    }

    void released(String owner, String lockId) {
        synchronized (mutex) {
            if (owner.equals(holders.get(lockId))) holders.remove(lockId);
            events.add(owner + " released " + lockId);
        }
    }

    /** One edge per waiting owner: waiter -> whoever holds the lock it wants (if anyone does). */
    WaitForGraph graph() {
        synchronized (mutex) {
            WaitForGraph g = new WaitForGraph();
            for (Map.Entry<String, String> w : waiting.entrySet()) {
                String holder = holders.get(w.getValue());
                if (holder != null && !holder.equals(w.getKey())) g.addEdge(w.getKey(), holder);
            }
            return g;
        }
    }

    List<String> detect() { return graph().findCycle(); }

    List<String> events() {
        synchronized (mutex) { return List.copyOf(events); }
    }
}

/** A ReentrantLock that reports to a tracker. lock() may throw DeadlockDetectedException before blocking. */
final class TrackedLock {
    private final String id;
    private final ReentrantLock lock = new ReentrantLock();
    private final LockTracker tracker;

    TrackedLock(String id, LockTracker tracker) {
        this.id = Objects.requireNonNull(id);
        this.tracker = tracker;                      // null = untracked, plain lock
    }

    String id() { return id; }

    void lock(String owner) {
        if (tracker != null) tracker.requesting(owner, id);   // throws instead of blocking into a cycle
        lock.lock();
        if (tracker != null) tracker.acquired(owner, id);
    }

    boolean tryLock(String owner) {
        if (!lock.tryLock()) return false;
        if (tracker != null) tracker.acquired(owner, id);
        return true;
    }

    void unlock(String owner) {
        if (tracker != null) tracker.released(owner, id);
        lock.unlock();
    }

    boolean isLocked() { return lock.isLocked(); }
}

/** An account whose balance is guarded by its own lock. */
final class Account {
    private final String id;
    private final Currency currency;
    private final TrackedLock lock;
    private long balance;                            // guarded by lock

    Account(String id, Currency currency, long openingMinorUnits, LockTracker tracker) {
        this.id = id;
        this.currency = currency;
        this.balance = openingMinorUnits;
        this.lock = new TrackedLock("lock:" + id, tracker);
    }

    String id() { return id; }
    TrackedLock lock() { return lock; }

    /** Callers must hold lock(). */
    long balanceMinor() { return balance; }
    void adjust(long delta) { balance = Math.addExact(balance, delta); }

    Money balance() {
        lock.lock("reader");
        try { return Money.ofMinor(balance, currency); } finally { lock.unlock("reader"); }
    }
}

/** Pause between try-lock attempts: a seeded random spin then a yield. Counts pauses; never sleeps. */
final class Backoff {
    private final Random random;
    private final AtomicLong pauses = new AtomicLong();

    Backoff(long seed) { this.random = new Random(seed); }

    void pause(int attempt) {
        pauses.incrementAndGet();
        int spins;
        synchronized (random) { spins = random.nextInt(1 << Math.min(attempt, 10)); }  // exponential cap at 1024
        for (int i = 0; i < spins; i++) Thread.onSpinWait();
        Thread.yield();
    }

    long pauses() { return pauses.get(); }
}

/** Three ways to move money between two accounts. One is a deadlock waiting to happen. */
final class TransferService {
    private final Runnable betweenLocks;             // test seam: runs after the first lock, before the second
    private final Backoff backoff;
    private final int maxAttempts;

    TransferService(Runnable betweenLocks, Backoff backoff) { this(betweenLocks, backoff, 1_000_000); }

    TransferService(Runnable betweenLocks, Backoff backoff, int maxAttempts) {
        this.betweenLocks = Objects.requireNonNull(betweenLocks);
        this.backoff = Objects.requireNonNull(backoff);
        this.maxAttempts = maxAttempts;
    }

    /** Lock from, then to. Two transfers in opposite directions can each take their first lock and wait forever. */
    void naiveTransfer(Account from, Account to, Money amount, String owner) {
        requireDistinct(from, to);
        from.lock().lock(owner);
        try {
            betweenLocks.run();
            to.lock().lock(owner);
            try {
                move(from, to, amount);
            } finally {
                to.lock().unlock(owner);
            }
        } finally {
            from.lock().unlock(owner);
        }
    }

    /** Fix 1: a global order. Everyone locks the smaller account id first, so no cycle can form. */
    void orderedTransfer(Account from, Account to, Money amount, String owner) {
        requireDistinct(from, to);
        Account first = from.id().compareTo(to.id()) < 0 ? from : to;
        Account second = first == from ? to : from;
        first.lock().lock(owner);
        try {
            betweenLocks.run();
            second.lock().lock(owner);
            try {
                move(from, to, amount);
            } finally {
                second.lock().unlock(owner);
            }
        } finally {
            first.lock().unlock(owner);
        }
    }

    /** Fix 2: never wait while holding. Try both; if the second is busy, release the first, back off, retry. */
    void tryLockTransfer(Account from, Account to, Money amount, String owner) {
        requireDistinct(from, to);
        for (int attempt = 1; attempt <= maxAttempts; attempt++) {
            if (from.lock().tryLock(owner)) {
                try {
                    betweenLocks.run();
                    if (to.lock().tryLock(owner)) {
                        try {
                            move(from, to, amount);
                            return;
                        } finally {
                            to.lock().unlock(owner);
                        }
                    }
                } finally {
                    from.lock().unlock(owner);       // release before backing off: hold nothing while waiting
                }
            }
            backoff.pause(attempt);
        }
        throw new IllegalStateException("transfer " + from.id() + " -> " + to.id() + " gave up after " + maxAttempts + " attempts");
    }

    /** Both locks held. The check-then-act and the two writes are one atomic step from any observer's view. */
    private static void move(Account from, Account to, Money amount) {
        if (amount.isNegative()) throw new IllegalArgumentException("amount must not be negative: " + amount);
        if (from.balanceMinor() < amount.minorUnits()) throw new InsufficientFundsException(from.id(), from.balanceMinor(), amount.minorUnits());
        from.adjust(-amount.minorUnits());
        to.adjust(amount.minorUnits());
    }

    private static void requireDistinct(Account from, Account to) {
        if (from.id().equals(to.id())) throw new IllegalArgumentException("cannot transfer to the same account: " + from.id());
    }
}

/** A two-party meeting point; both threads must arrive before either continues. Used to force the deadlock interleaving. */
final class Barrier implements Runnable {
    private final int parties;
    private int arrived;
    private int generation;

    Barrier(int parties) { this.parties = parties; }

    @Override
    public synchronized void run() {
        int myGeneration = generation;
        arrived++;
        if (arrived == parties) {
            arrived = 0;
            generation++;
            notifyAll();
            return;
        }
        while (myGeneration == generation) {
            try { wait(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); return; }
        }
    }
}

public final class Main {
    static final Currency USD = Currency.USD;

    /** Runs the body on `threads` threads and joins them all; fails loudly rather than hanging on a real deadlock. */
    static void runAll(int threads, java.util.function.IntConsumer body) throws InterruptedException {
        Thread[] pool = new Thread[threads];
        Set<String> failures = Collections.synchronizedSet(new LinkedHashSet<>());
        for (int t = 0; t < threads; t++) {
            final int index = t;
            pool[t] = new Thread(() -> {
                try { body.accept(index); } catch (RuntimeException e) { failures.add(e.getClass().getSimpleName() + ": " + e.getMessage()); }
            }, "worker-" + t);
            pool[t].start();
        }
        for (Thread thread : pool) {
            thread.join(10_000);
            if (thread.isAlive()) throw new IllegalStateException("thread " + thread.getName() + " did not finish: deadlock?");
        }
        if (!failures.isEmpty()) throw new IllegalStateException("worker failures: " + failures);
    }

    public static void main(String[] args) throws InterruptedException {
        System.out.println("== A wait-for graph, by hand ==");
        WaitForGraph g = new WaitForGraph();
        g.addEdge("T1", "T2");                        // T1 waits for a lock T2 holds
        g.addEdge("T2", "T3");
        g.addEdge("T3", "T1");
        g.addEdge("T4", "T1");                        // T4 is stuck behind the cycle but not on it
        System.out.println("edges T1->T2, T2->T3, T3->T1, T4->T1: cycle = " + g.findCycle());
        WaitForGraph chain = new WaitForGraph();
        chain.addEdge("T1", "T2");
        chain.addEdge("T2", "T3");
        System.out.println("edges T1->T2, T2->T3: cycle = " + chain.findCycle() + " (a chain waits, it does not deadlock)");

        System.out.println("\n== The classic: A->B while B->A, replayed from recorded lock requests ==");
        LockTracker scripted = new LockTracker();
        scripted.acquired("T1", "lock:A");
        scripted.acquired("T2", "lock:B");
        scripted.requesting("T1", "lock:B");          // T1 now waits for T2: no cycle yet
        System.out.println("after T1 requests lock:B: cycle = " + scripted.detect());
        try {
            scripted.requesting("T2", "lock:A");      // T2 would wait for T1: cycle
        } catch (DeadlockDetectedException e) {
            System.out.println("T2 requests lock:A -> " + e.getMessage());
        }

        System.out.println("\n== Live: two naive transfers, forced to interleave, caught by the detector ==");
        LockTracker tracker = new LockTracker();
        Account a = new Account("A", USD, 10_000, tracker);
        Account b = new Account("B", USD, 10_000, tracker);
        TransferService naive = new TransferService(new Barrier(2), new Backoff(42));
        List<String> outcomes = Collections.synchronizedList(new ArrayList<>());
        Thread t1 = new Thread(() -> {
            try { naive.naiveTransfer(a, b, Money.ofMinor(2_500, USD), "T1"); outcomes.add("T1 A->B 25.00 committed"); }
            catch (DeadlockDetectedException e) { outcomes.add("T1 A->B aborted: " + e.getMessage()); }
        });
        Thread t2 = new Thread(() -> {
            try { naive.naiveTransfer(b, a, Money.ofMinor(1_000, USD), "T2"); outcomes.add("T2 B->A 10.00 committed"); }
            catch (DeadlockDetectedException e) { outcomes.add("T2 B->A aborted: " + e.getMessage()); }
        });
        t1.start(); t2.start(); t1.join(10_000); t2.join(10_000);
        outcomes.stream().sorted().forEach(o -> System.out.println("  " + o));
        System.out.println("  recorded events:");
        for (String e : tracker.events()) System.out.println("    " + e);
        System.out.println("  balances A=" + a.balance() + " B=" + b.balance() + " (sum " + Money.ofMinor(a.balanceMinor() + b.balanceMinor(), USD) + ")");

        System.out.println("\n== Fix 1: lock ordering, 8 threads x 500 transfers each way ==");
        Account c = new Account("C", USD, 100_000, null);
        Account d = new Account("D", USD, 100_000, null);
        TransferService ordered = new TransferService(Thread::yield, new Backoff(7));
        runAll(8, t -> {
            for (int i = 0; i < 500; i++) {
                if (t % 2 == 0) ordered.orderedTransfer(c, d, Money.ofMinor(1, USD), "worker-" + t);
                else ordered.orderedTransfer(d, c, Money.ofMinor(1, USD), "worker-" + t);
            }
        });
        System.out.println("  completed; C=" + c.balance() + " D=" + d.balance() + " sum " + Money.ofMinor(c.balanceMinor() + d.balanceMinor(), USD));

        System.out.println("\n== Fix 2: try-lock with backoff, same load ==");
        Account e = new Account("E", USD, 100_000, null);
        Account f = new Account("F", USD, 100_000, null);
        Backoff backoff = new Backoff(7);
        TransferService trying = new TransferService(Thread::yield, backoff);
        runAll(8, t -> {
            for (int i = 0; i < 500; i++) {
                if (t % 2 == 0) trying.tryLockTransfer(e, f, Money.ofMinor(1, USD), "worker-" + t);
                else trying.tryLockTransfer(f, e, Money.ofMinor(1, USD), "worker-" + t);
            }
        });
        System.out.println("  completed; E=" + e.balance() + " F=" + f.balance() + " sum " + Money.ofMinor(e.balanceMinor() + f.balanceMinor(), USD)
                + "; backoff pauses: " + backoff.pauses() + " (varies)");
    }
}
