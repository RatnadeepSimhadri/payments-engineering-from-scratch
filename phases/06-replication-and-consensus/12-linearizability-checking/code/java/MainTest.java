// Tests for phases/06-replication-and-consensus/12-linearizability-checking. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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

    static History burst(int writers, int expectedRead) {
        History h = new History();
        for (int i = 0; i < writers; i++) h.write("c" + i, "x", i + 1, 0, 100);
        return h.read("reader", "x", expectedRead, 101, 102);
    }

    public static void main(String[] args) {
        Checker checker = new Checker(new RegisterModel());

        // --- known-good history passes, in real-time order
        History good = new History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
                .read("risk", "bal", 300, 10, 12).read("app", "bal", 300, 13, 15);
        CheckResult g = checker.check(good);
        check("known-good history is linearizable", g.ok());
        check("witness order is the real-time order when ops do not overlap", g.order().equals(List.of(0, 1, 2, 3)));
        check("empty history is linearizable", checker.check(new History()).ok());
        check("a read of the initial value 0 before any write passes", checker.check(new History().read("a", "x", 0, 0, 1)).ok());
        check("a read of a never-written value fails", !checker.check(new History().read("a", "x", 7, 0, 1)).ok());

        // --- stale read: a read that starts after a write returned must see it
        History stale = new History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
                .read("risk", "bal", 500, 10, 12).read("app", "bal", 300, 13, 15);
        CheckResult s = checker.check(stale);
        check("stale read is not linearizable", !s.ok());
        check("failure reports the longest linearizable prefix (both writes)", s.longestPrefix().equals(List.of(0, 1)));
        check("failure result carries no witness order", s.order().isEmpty());

        // --- sequentially consistent but not linearizable
        History seqOnly = new History().write("ledger", "bal", 300, 0, 4).read("risk", "bal", 0, 6, 8);
        check("write-then-stale-read by another client fails linearizability", !checker.check(seqOnly, Consistency.LINEARIZABLE).ok());
        CheckResult sq = checker.check(seqOnly, Consistency.SEQUENTIAL);
        check("the same history is sequentially consistent", sq.ok());
        check("sequential witness puts the read before the write", sq.order().equals(List.of(1, 0)));
        History programOrder = new History().write("a", "x", 1, 0, 1).read("a", "x", 0, 2, 3);
        check("sequential consistency still enforces one client's program order", !checker.check(programOrder, Consistency.SEQUENTIAL).ok());
        check("every linearizable history is sequentially consistent", checker.check(good, Consistency.SEQUENTIAL).ok());

        // --- concurrency windows: overlapping ops may be ordered either way
        History window = new History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 5, 15).read("app", "bal", 300, 6, 8);
        CheckResult w = checker.check(window);
        check("a read of the old value that overlaps the write is linearizable", w.ok());
        check("the witness linearizes the overlapping read before the write", w.order().equals(List.of(1, 0, 2)));
        History shrunk = new History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 11, 15).read("app", "bal", 300, 6, 8);
        check("move the read past the write's return and it fails", !checker.check(shrunk).ok());
        History twoWritesA = new History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 1, 11, 12);
        History twoWritesB = new History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 2, 11, 12);
        check("two concurrent writes: the search finds the order where the first write lands last", checker.check(twoWritesA).ok() && checker.check(twoWritesA).order().equals(List.of(1, 0, 2)));
        check("two concurrent writes: and the other order", checker.check(twoWritesB).ok() && checker.check(twoWritesB).order().equals(List.of(0, 1, 2)));
        check("a read must see one of the concurrent writes once both returned", !checker.check(new History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 0, 11, 12)).ok());
        check("Op.precedes and concurrentWith", good.op(0).precedes(good.op(1)) && window.op(0).concurrentWith(window.op(1)) && !window.op(1).precedes(window.op(0)));

        // --- keys are independent registers
        History keys = new History().write("a", "x", 1, 0, 1).write("a", "y", 2, 2, 3).read("b", "x", 1, 4, 5).read("b", "y", 2, 4, 5);
        check("independent keys each hold their own value", checker.check(keys).ok());
        check("a read of the wrong key's value fails", !checker.check(new History().write("a", "x", 1, 0, 1).read("b", "y", 1, 2, 3)).ok());

        // --- recorder with an injected clock
        FakeClock clock = new FakeClock();
        HistoryRecorder rec = new HistoryRecorder(clock);
        int w1 = rec.invokeWrite("ledger", "bal", 500); clock.advance(2); rec.complete(w1, 0);
        clock.advance(1);
        int r1 = rec.invokeRead("risk", "bal"); clock.advance(1); rec.complete(r1, 500);
        History recorded = rec.history();
        check("recorder stamps invoke and return from the clock", recorded.op(0).invokedAt() == 0 && recorded.op(0).returnedAt() == 2 && recorded.op(1).invokedAt() == 3 && recorded.op(1).returnedAt() == 4);
        check("recorder keeps the read's returned value and the write's written value", recorded.op(0).value() == 500 && recorded.op(1).value() == 500 && recorded.op(1).kind() == OpKind.READ);
        check("recorded history is linearizable", checker.check(recorded).ok());
        HistoryRecorder pendingRec = new HistoryRecorder(clock);
        pendingRec.invokeWrite("a", "x", 1);
        check("pending count", pendingRec.pendingCount() == 1);
        checkThrows("history() refuses incomplete ops", IllegalStateException.class, pendingRec::history);
        checkThrows("completing an unknown op is an error", IllegalArgumentException.class, () -> pendingRec.complete(99, 0));

        // --- validation
        checkThrows("an op cannot return before it was invoked", IllegalArgumentException.class, () -> new History().read("a", "x", 0, 5, 4));
        checkThrows("more than 64 ops is rejected", IllegalArgumentException.class, () -> {
            History h = new History();
            for (int i = 0; i <= History.MAX_OPS; i++) h.write("a", "x", i, i, i);
        });

        // --- the memo prunes repeated (linearized set, state) configurations
        CheckResult withMemo = new Checker(new RegisterModel(), true, 5_000_000).check(burst(8, 1));
        CheckResult withoutMemo = new Checker(new RegisterModel(), false, 5_000_000).check(burst(8, 1));
        check("both searches reach the same verdict", withMemo.ok() && withoutMemo.ok() && withMemo.order().get(7) == 0);
        check("the memo explores far fewer states", withMemo.explored() * 5 < withoutMemo.explored() && withMemo.memoHits() > 0);
        check("memoised search on 8 concurrent writes stays under n * 2^n states", withMemo.explored() <= 8 * 256);

        // --- performance bound on small histories
        Checker bounded = new Checker(new RegisterModel(), true, 100_000);
        check("12 fully concurrent writes check within the state budget with the memo", bounded.check(burst(12, 1)).ok());
        checkThrows("without the memo the same history blows the budget", IllegalStateException.class, () -> new Checker(new RegisterModel(), false, 100_000).check(burst(12, 1)));
        int ok = 0, maxExplored = 0;
        for (long seed = 1; seed <= 50; seed++) {
            CheckResult r = checker.check(HistoryGenerator.linearizable(seed, 20, 3, 15));
            if (r.ok()) ok++;
            maxExplored = Math.max(maxExplored, r.explored());
        }
        check("50 generated linearizable histories all pass", ok == 50);
        check("each generated check explores at most a few hundred states", maxExplored <= 500);
        History gen = HistoryGenerator.linearizable(7, 20, 3, 15);
        check("generator is deterministic for a seed", gen.ops().equals(HistoryGenerator.linearizable(7, 20, 3, 15).ops()) && gen.size() == 20);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(msg -> System.out.println("FAIL: " + msg));
        if (!failures.isEmpty()) System.exit(1);
    }
}
