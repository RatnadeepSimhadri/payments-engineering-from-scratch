// Tests for phases/04-concurrency/12-testing-concurrent-code. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) throws Exception {
        // --- The machine executes one instruction at a time, on the caller's thread ---
        Program racy = Programs.racyIncrement(10_000, 2_500);
        Machine m = new Machine(racy);
        check("both threads enabled at start", m.enabledThreads().equals(List.of(0, 1)));
        m.step(0); m.step(0); m.step(0);
        check("thread 0 finished after three steps", m.finished(0) && !m.finished(1));
        check("memory updated by the store", m.memory.get("balance") == 12_500);
        checkThrows("stepping a finished thread is an error", IllegalStateException.class, () -> m.step(0));
        m.step(1); m.step(1); m.step(1);
        check("sequential schedule is correct", m.allFinished() && m.memory.get("balance") == 15_000);
        check("trace records six steps in order", m.trace().size() == 6 && m.trace().get(0).thread().equals("T1") && m.trace().get(5).thread().equals("T2"));

        // --- Counting interleavings ---
        check("2 threads x 3 steps = C(6,3) = 20", racy.interleavingsUpperBound() == 20);
        check("2 threads x 5 steps = C(10,5) = 252", Programs.lockedIncrement(0, 1).interleavingsUpperBound() == 252);
        Program three = new Program(Map.of("x", 0L), List.of(
                new ThreadSpec("A", List.of(new AtomicAdd("x", 1), new AtomicAdd("x", 1))),
                new ThreadSpec("B", List.of(new AtomicAdd("x", 1))),
                new ThreadSpec("C", List.of(new AtomicAdd("x", 1)))));
        check("multinomial 4!/(2!1!1!) = 12", three.interleavingsUpperBound() == 12);

        // --- Exhaustive exploration finds the lost update and proves the lock ---
        ExplorationResult r = Explorer.exhaustive(racy, Programs.balanceEquals(15_000));
        check("explores every interleaving", r.schedulesExplored() == 20);
        check("finds the lost update in 18 of 20 schedules", r.violations() == 18 && r.deadlocks() == 0);
        check("counterexample ends with balance 12500", r.finalMemory().orElseThrow().get("balance") == 12_500);
        check("counterexample is a full six-step trace", r.counterexample().orElseThrow().size() == 6);
        List<TraceStep> trace = r.counterexample().orElseThrow();
        boolean t2LoadsBeforeT1Stores = false;
        int t1Store = -1, t2Load = -1;
        for (int i = 0; i < trace.size(); i++) {
            if (trace.get(i).thread().equals("T1") && trace.get(i).instruction() instanceof Store) t1Store = i;
            if (trace.get(i).thread().equals("T2") && trace.get(i).instruction() instanceof Load) t2Load = i;
        }
        t2LoadsBeforeT1Stores = t2Load < t1Store;
        check("in the counterexample T2 loads before T1 stores", t2LoadsBeforeT1Stores);

        ExplorationResult locked = Explorer.exhaustive(Programs.lockedIncrement(10_000, 2_500), Programs.balanceEquals(15_000));
        check("the lock leaves exactly two schedules", locked.schedulesExplored() == 2);
        check("locked increment never violates", locked.clean() && locked.counterexample().isEmpty());

        // --- Exhaustive exploration finds the overdraft even though the subtraction is atomic ---
        ExplorationResult debit = Explorer.exhaustive(Programs.checkThenActDebit(150, 100), Programs.balanceNonNegative());
        check("check-then-act overdraws in 18 of 20 schedules", debit.violations() == 18);
        check("overdraft counterexample ends at -50", debit.finalMemory().orElseThrow().get("balance") == -50);
        ExplorationResult safeDebit = Explorer.exhaustive(Programs.checkThenActDebit(250, 100), Programs.balanceNonNegative());
        check("with enough funds no schedule overdraws", safeDebit.clean());

        // --- Deadlock detection ---
        ExplorationResult dl = Explorer.exhaustive(Programs.transferUnordered(), Programs.conserved(200));
        check("unordered transfer deadlocks in two schedules", dl.deadlocks() == 2 && dl.violations() == 0);
        check("deadlock trace is the two lock acquisitions", dl.counterexample().orElseThrow().size() == 2
                && dl.counterexample().orElseThrow().stream().allMatch(s -> s.instruction() instanceof Lock));
        ExplorationResult ok = Explorer.exhaustive(Programs.transferOrdered(), Programs.conserved(200));
        check("ordered locks: two schedules, no deadlock, money conserved", ok.schedulesExplored() == 2 && ok.clean());

        Machine dead = new Machine(Programs.transferUnordered());
        dead.step(0); dead.step(1);
        check("machine reports deadlock when nobody is enabled", dead.deadlocked() && dead.enabledThreads().isEmpty());
        checkThrows("unlock of a lock you do not hold is an error", IllegalStateException.class, () -> {
            Machine bad = new Machine(new Program(Map.of(), List.of(new ThreadSpec("X", List.of(new Unlock("L"))))));
            bad.step(0);
        });

        // --- Random exploration is seeded and reproducible ---
        ExplorationResult s1 = Explorer.random(racy, Programs.balanceEquals(15_000), 42, 500);
        ExplorationResult s2 = Explorer.random(racy, Programs.balanceEquals(15_000), 42, 500);
        check("same seed, same result", s1.equals(s2) && s1.schedulesExplored() == 500);
        check("random schedules find the bug in most samples", s1.violations() > 300);
        ExplorationResult s3 = Explorer.random(racy, Programs.balanceEquals(15_000), 7, 500);
        check("different seed, different sample", s3.violations() != s1.violations() || !s3.counterexample().equals(s1.counterexample()));
        check("random exploration of the locked program is clean", Explorer.random(Programs.lockedIncrement(0, 1), Programs.balanceEquals(2), 1, 200).clean());

        // --- The stress harness: exact for the lock, never a proof for the race ---
        StressResult lockedStress = StressTest.run(LockedCounter::new, 4, 5_000, 1, 5);
        check("locked counter is exact in every round", lockedStress.roundsWithLostUpdates() == 0 && lockedStress.expectedPerRound() == 20_000);
        StressResult racyStress = StressTest.run(RacyCounter::new, 4, 5_000, 1, 5);
        check("racy counter never gains updates", racyStress.totalLost() >= 0);
        check("stress harness reports every round", racyStress.rounds() == 5);

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
