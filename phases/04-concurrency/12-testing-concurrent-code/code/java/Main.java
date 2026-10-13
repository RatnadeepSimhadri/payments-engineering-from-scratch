// Lesson: phases/04-concurrency/12-testing-concurrent-code/docs/en.md
// Testing Concurrent Code: Stress, Model Checking, and Determinism
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Godefroid, "Model Checking for Programming Languages using VeriSoft" (POPL 1997) for stateless
// exploration; Musuvathi et al., "Finding and Reproducing Heisenbugs in Concurrent Programs" (CHESS, OSDI 2008).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Random;
import java.util.concurrent.BrokenBarrierException;
import java.util.concurrent.CyclicBarrier;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.Predicate;

// ---------------------------------------------------------------------------------------------
// Part 1: a tiny machine whose scheduler is an input.
// ---------------------------------------------------------------------------------------------

/**
 * One atomic step of a modelled thread. A real `balance += 25` is three of these (Load, Add, Store);
 * a real `synchronized` block is Lock ... Unlock. The explorer may switch threads between any two.
 */
sealed interface Instruction permits Load, Store, Add, AtomicAdd, Lock, Unlock, BranchIfLess {}

/** reg = memory[var] */
record Load(String var, String reg) implements Instruction {
    @Override public String toString() { return reg + " = load " + var; }
}

/** memory[var] = reg */
record Store(String var, String reg) implements Instruction {
    @Override public String toString() { return "store " + var + " = " + reg; }
}

/** reg = reg + k */
record Add(String reg, long k) implements Instruction {
    @Override public String toString() { return reg + " += " + k; }
}

/** memory[var] += k in one indivisible step: AtomicLong.addAndGet, Redis INCRBY, UPDATE ... SET x = x + k. */
record AtomicAdd(String var, long k) implements Instruction {
    @Override public String toString() { return "atomic " + var + " += " + k; }
}

/** Blocks while another thread holds the lock. */
record Lock(String lock) implements Instruction {
    @Override public String toString() { return "lock " + lock; }
}

record Unlock(String lock) implements Instruction {
    @Override public String toString() { return "unlock " + lock; }
}

/** if (reg < k) jump to instruction index `target` in this thread, else fall through. */
record BranchIfLess(String reg, long k, int target) implements Instruction {
    @Override public String toString() { return "if " + reg + " < " + k + " goto " + target; }
}

/** A modelled thread: a name and a straight-line program with optional forward branches. */
record ThreadSpec(String name, List<Instruction> code) {
    ThreadSpec {
        Objects.requireNonNull(name);
        code = List.copyOf(code);
    }
}

/** The whole program: initial shared memory and the threads that run against it. */
record Program(Map<String, Long> initialMemory, List<ThreadSpec> threads) {
    Program {
        initialMemory = Collections.unmodifiableMap(new LinkedHashMap<>(initialMemory));
        threads = List.copyOf(threads);
    }

    /** Number of distinct interleavings if no instruction ever blocks: the multinomial coefficient. */
    long interleavingsUpperBound() {
        long total = 0, result = 1;
        for (ThreadSpec t : threads) {
            for (int i = 1; i <= t.code().size(); i++) {
                total++;
                result = Math.multiplyExact(result, total) / i;   // exact: C(total, i) is an integer
            }
        }
        return result;
    }
}

/** One scheduling decision: which thread ran, and what it executed. */
record TraceStep(String thread, Instruction instruction) {
    @Override public String toString() { return thread + ": " + instruction; }
}

/** A named end-state predicate. */
record Invariant(String name, Predicate<Map<String, Long>> holds) {}

/**
 * The machine. Shared memory, one program counter and register file per thread, lock ownership.
 * It never spawns a thread: `step(i)` runs exactly one instruction of thread i on the caller's thread.
 */
final class Machine {
    private final Program program;
    final Map<String, Long> memory = new HashMap<>();
    private final int[] pc;
    private final List<Map<String, Long>> registers = new ArrayList<>();
    private final Map<String, Integer> lockOwner = new HashMap<>();
    private final List<TraceStep> trace = new ArrayList<>();

    Machine(Program program) {
        this.program = program;
        memory.putAll(program.initialMemory());
        pc = new int[program.threads().size()];
        for (int i = 0; i < pc.length; i++) registers.add(new HashMap<>());
    }

    boolean finished(int t) { return pc[t] >= program.threads().get(t).code().size(); }

    boolean allFinished() {
        for (int t = 0; t < pc.length; t++) if (!finished(t)) return false;
        return true;
    }

    /** A thread is enabled when it has an instruction left and that instruction can run now. */
    boolean enabled(int t) {
        if (finished(t)) return false;
        Instruction next = program.threads().get(t).code().get(pc[t]);
        if (next instanceof Lock l) {
            Integer owner = lockOwner.get(l.lock());
            return owner == null || owner == t;
        }
        return true;
    }

    List<Integer> enabledThreads() {
        List<Integer> out = new ArrayList<>();
        for (int t = 0; t < pc.length; t++) if (enabled(t)) out.add(t);
        return out;
    }

    /** Deadlock: nobody finished everything, yet nobody can move. */
    boolean deadlocked() { return !allFinished() && enabledThreads().isEmpty(); }

    void step(int t) {
        if (!enabled(t)) throw new IllegalStateException("thread " + t + " is not enabled");
        ThreadSpec spec = program.threads().get(t);
        Instruction ins = spec.code().get(pc[t]);
        Map<String, Long> regs = registers.get(t);
        int next = pc[t] + 1;
        switch (ins) {
            case Load l -> regs.put(l.reg(), memory.getOrDefault(l.var(), 0L));
            case Store s -> memory.put(s.var(), regs.getOrDefault(s.reg(), 0L));
            case Add a -> regs.put(a.reg(), Math.addExact(regs.getOrDefault(a.reg(), 0L), a.k()));
            case AtomicAdd a -> memory.put(a.var(), Math.addExact(memory.getOrDefault(a.var(), 0L), a.k()));
            case Lock l -> lockOwner.put(l.lock(), t);
            case Unlock u -> {
                if (!Integer.valueOf(t).equals(lockOwner.get(u.lock()))) {
                    throw new IllegalStateException(spec.name() + " unlocks " + u.lock() + " it does not hold");
                }
                lockOwner.remove(u.lock());
            }
            case BranchIfLess b -> { if (regs.getOrDefault(b.reg(), 0L) < b.k()) next = b.target(); }
        }
        pc[t] = next;
        trace.add(new TraceStep(spec.name(), ins));
    }

    List<TraceStep> trace() { return Collections.unmodifiableList(trace); }

    Map<String, Long> memorySnapshot() { return new LinkedHashMap<>(memory); }
}

/** What an exploration found. `counterexample` is the first schedule that broke the invariant or deadlocked. */
record ExplorationResult(long schedulesExplored, long violations, long deadlocks,
                         Optional<List<TraceStep>> counterexample, Optional<Map<String, Long>> finalMemory) {
    boolean clean() { return violations == 0 && deadlocks == 0; }
}

/**
 * The explorer. `exhaustive` enumerates every interleaving by depth-first search over scheduling
 * choices and replays each from the initial state (stateless model checking). `random` samples
 * schedules with a seeded generator for programs too large to enumerate. Both are single-threaded
 * and deterministic: the same program and seed give the same result, byte for byte.
 */
final class Explorer {
    private long explored, violations, deadlocks;
    private List<TraceStep> counterexample;
    private Map<String, Long> finalMemory;

    static ExplorationResult exhaustive(Program program, Invariant invariant) {
        Explorer e = new Explorer();
        e.dfs(program, invariant, new ArrayList<>());
        return e.result();
    }

    static ExplorationResult random(Program program, Invariant invariant, long seed, int schedules) {
        Explorer e = new Explorer();
        Random rng = new Random(seed);
        for (int i = 0; i < schedules; i++) {
            Machine m = new Machine(program);
            while (!m.allFinished() && !m.deadlocked()) {
                List<Integer> choices = m.enabledThreads();
                m.step(choices.get(rng.nextInt(choices.size())));
            }
            e.record(m, invariant);
        }
        return e.result();
    }

    private void dfs(Program program, Invariant invariant, List<Integer> prefix) {
        Machine m = replay(program, prefix);
        if (m.allFinished() || m.deadlocked()) {
            record(m, invariant);
            return;
        }
        for (int t : m.enabledThreads()) {
            prefix.add(t);
            dfs(program, invariant, prefix);
            prefix.remove(prefix.size() - 1);
        }
    }

    private static Machine replay(Program program, List<Integer> schedule) {
        Machine m = new Machine(program);
        for (int t : schedule) m.step(t);
        return m;
    }

    private void record(Machine m, Invariant invariant) {
        explored++;
        boolean bad = false;
        if (m.deadlocked()) { deadlocks++; bad = true; }
        else if (!invariant.holds().test(m.memory)) { violations++; bad = true; }
        if (bad && counterexample == null) {
            counterexample = new ArrayList<>(m.trace());
            finalMemory = m.memorySnapshot();
        }
    }

    private ExplorationResult result() {
        return new ExplorationResult(explored, violations, deadlocks,
                Optional.ofNullable(counterexample), Optional.ofNullable(finalMemory));
    }
}

/** The programs this lesson checks. Every one mirrors a real class from Lesson 02 or Lesson 07. */
final class Programs {
    private Programs() {}

    /** Two threads each do `balance += delta` as load/add/store. The Lesson 02 lost update. */
    static Program racyIncrement(long initial, long delta) {
        List<Instruction> body = List.of(new Load("balance", "r"), new Add("r", delta), new Store("balance", "r"));
        return new Program(Map.of("balance", initial), List.of(new ThreadSpec("T1", body), new ThreadSpec("T2", body)));
    }

    /** The same increment under one lock: Lesson 02's LockedCounter. */
    static Program lockedIncrement(long initial, long delta) {
        List<Instruction> body = List.of(new Lock("L"), new Load("balance", "r"), new Add("r", delta),
                new Store("balance", "r"), new Unlock("L"));
        return new Program(Map.of("balance", initial), List.of(new ThreadSpec("T1", body), new ThreadSpec("T2", body)));
    }

    /** Two debits that each check the balance, then subtract atomically. Lesson 02's "a bare atomic cannot do a debit". */
    static Program checkThenActDebit(long initial, long debit) {
        List<Instruction> body = List.of(
                new Load("balance", "r"),              // 0: check...
                new BranchIfLess("r", debit, 3),       // 1: insufficient -> skip to end
                new AtomicAdd("balance", -debit));     // 2: ...then act, atomically but too late
        return new Program(Map.of("balance", initial), List.of(new ThreadSpec("D1", body), new ThreadSpec("D2", body)));
    }

    /** Transfer A->B while B->A, each locking source then destination. Lesson 07's deadlock. */
    static Program transferUnordered() {
        List<Instruction> ab = List.of(new Lock("A"), new Lock("B"), new Load("a", "x"), new Add("x", -10), new Store("a", "x"),
                new Load("b", "y"), new Add("y", 10), new Store("b", "y"), new Unlock("B"), new Unlock("A"));
        List<Instruction> ba = List.of(new Lock("B"), new Lock("A"), new Load("b", "x"), new Add("x", -10), new Store("b", "x"),
                new Load("a", "y"), new Add("y", 10), new Store("a", "y"), new Unlock("A"), new Unlock("B"));
        return new Program(Map.of("a", 100L, "b", 100L), List.of(new ThreadSpec("AtoB", ab), new ThreadSpec("BtoA", ba)));
    }

    /** The same two transfers with locks always taken in alphabetical order. Lesson 07's fix. */
    static Program transferOrdered() {
        List<Instruction> ab = List.of(new Lock("A"), new Lock("B"), new Load("a", "x"), new Add("x", -10), new Store("a", "x"),
                new Load("b", "y"), new Add("y", 10), new Store("b", "y"), new Unlock("B"), new Unlock("A"));
        List<Instruction> ba = List.of(new Lock("A"), new Lock("B"), new Load("b", "x"), new Add("x", -10), new Store("b", "x"),
                new Load("a", "y"), new Add("y", 10), new Store("a", "y"), new Unlock("B"), new Unlock("A"));
        return new Program(Map.of("a", 100L, "b", 100L), List.of(new ThreadSpec("AtoB", ab), new ThreadSpec("BtoA", ba)));
    }

    static Invariant balanceEquals(long expected) {
        return new Invariant("balance == " + expected, m -> m.getOrDefault("balance", 0L) == expected);
    }

    static Invariant balanceNonNegative() {
        return new Invariant("balance >= 0", m -> m.getOrDefault("balance", 0L) >= 0);
    }

    static Invariant conserved(long total) {
        return new Invariant("a + b == " + total, m -> m.getOrDefault("a", 0L) + m.getOrDefault("b", 0L) == total);
    }
}

// ---------------------------------------------------------------------------------------------
// Part 2: a stress harness with real threads, for the code the model abstracts.
// ---------------------------------------------------------------------------------------------

interface BalanceCounter {
    void add(long delta);
    long get();
}

/** Lesson 02's bug, unchanged. */
final class RacyCounter implements BalanceCounter {
    private long balance;
    @Override public void add(long delta) { long loaded = balance; balance = loaded + delta; }
    @Override public long get() { return balance; }
}

/** Lesson 02's first fix, unchanged. */
final class LockedCounter implements BalanceCounter {
    private final ReentrantLock lock = new ReentrantLock();
    private long balance;
    @Override public void add(long delta) { lock.lock(); try { balance += delta; } finally { lock.unlock(); } }
    @Override public long get() { lock.lock(); try { return balance; } finally { lock.unlock(); } }
}

record StressResult(int rounds, int roundsWithLostUpdates, long totalLost, long expectedPerRound) {}

/**
 * Runs `threads` real threads that each add `delta` `opsPerThread` times, `rounds` times over, releasing
 * every round through a CyclicBarrier so the threads collide as hard as the hardware allows.
 * A stress test can show a race; it can never show the absence of one.
 */
final class StressTest {
    static StressResult run(java.util.function.Supplier<BalanceCounter> factory, int threads, int opsPerThread,
                            long delta, int rounds) throws InterruptedException {
        long expected = (long) threads * opsPerThread * delta;
        int badRounds = 0;
        long totalLost = 0;
        for (int r = 0; r < rounds; r++) {
            BalanceCounter counter = factory.get();
            CyclicBarrier barrier = new CyclicBarrier(threads);
            Thread[] ts = new Thread[threads];
            for (int i = 0; i < threads; i++) {
                ts[i] = new Thread(() -> {
                    try { barrier.await(); } catch (InterruptedException | BrokenBarrierException e) { return; }
                    for (int k = 0; k < opsPerThread; k++) counter.add(delta);
                });
                ts[i].start();
            }
            for (Thread t : ts) t.join();
            long lost = expected - counter.get();
            if (lost != 0) { badRounds++; totalLost += lost; }
        }
        return new StressResult(rounds, badRounds, totalLost, expected);
    }
}

public final class Main {
    static void report(String title, Program p, ExplorationResult r) {
        System.out.println("-- " + title + " --");
        System.out.println("interleavings (upper bound) = " + p.interleavingsUpperBound()
                + ", explored = " + r.schedulesExplored() + ", violations = " + r.violations() + ", deadlocks = " + r.deadlocks());
        r.counterexample().ifPresent(trace -> {
            System.out.println("first counterexample, final memory " + r.finalMemory().orElseThrow() + ":");
            for (TraceStep s : trace) System.out.println("    " + s);
        });
    }

    public static void main(String[] args) throws Exception {
        System.out.println("== Exhaustive: balance += 25.00 USD twice, from 100.00 USD (minor units) ==");
        Program racy = Programs.racyIncrement(10_000, 2_500);
        report("racy load/add/store", racy, Explorer.exhaustive(racy, Programs.balanceEquals(15_000)));
        Program locked = Programs.lockedIncrement(10_000, 2_500);
        report("same increment under a lock", locked, Explorer.exhaustive(locked, Programs.balanceEquals(15_000)));

        System.out.println();
        System.out.println("== Exhaustive: two 1.00 USD debits from 1.50 USD, each checks first ==");
        Program debit = Programs.checkThenActDebit(150, 100);
        report("check-then-act debit", debit, Explorer.exhaustive(debit, Programs.balanceNonNegative()));

        System.out.println();
        System.out.println("== Exhaustive: transfer A->B while B->A ==");
        Program unordered = Programs.transferUnordered();
        report("locks taken source-then-destination", unordered, Explorer.exhaustive(unordered, Programs.conserved(200)));
        Program ordered = Programs.transferOrdered();
        report("locks taken in a global order", ordered, Explorer.exhaustive(ordered, Programs.conserved(200)));

        System.out.println();
        System.out.println("== Random schedules (seed 42): same bug, sampled instead of enumerated ==");
        ExplorationResult sampled = Explorer.random(racy, Programs.balanceEquals(15_000), 42, 1_000);
        System.out.println("1000 random schedules of the racy program: violations = " + sampled.violations());
        ExplorationResult again = Explorer.random(racy, Programs.balanceEquals(15_000), 42, 1_000);
        System.out.println("same seed again: violations = " + again.violations() + " (identical: " + sampled.equals(again) + ")");

        System.out.println();
        System.out.println("== Stress with real threads: 4 threads x 20000 adds, 20 rounds, barrier start ==");
        StressResult racyStress = StressTest.run(RacyCounter::new, 4, 20_000, 1, 20);
        StressResult lockedStress = StressTest.run(LockedCounter::new, 4, 20_000, 1, 20);
        System.out.println("racy  : rounds with lost updates = " + racyStress.roundsWithLostUpdates() + " of " + racyStress.rounds()
                + " (nondeterministic: this line may differ between runs)");
        System.out.println("locked: rounds with lost updates = " + lockedStress.roundsWithLostUpdates() + " of " + lockedStress.rounds()
                + " (the lock makes this exact)");
        System.out.println("The model checker proved the racy program wrong in " + Explorer.exhaustive(racy, Programs.balanceEquals(15_000)).schedulesExplored()
                + " schedules; the stress test only made it likely.");
    }
}
