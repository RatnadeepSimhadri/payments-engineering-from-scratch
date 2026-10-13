// Tests for phases/10-money-and-ledgers/11-ledger-concurrency. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

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

    static final Currency USD = Currency.USD;

    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    public static void main(String[] args) throws InterruptedException {
        // ---- deterministic interleavings ----
        List<Op> twoDeposits = List.of(new Op("A", 100), new Op("B", 100));
        Exploration naiveDeposits = InterleavingChecker.explore(Strategy.NAIVE, 0, twoDeposits);
        check("naive: 2 ops x 2 steps = 6 schedules", naiveDeposits.schedules() == 6);
        check("naive: 4 of 6 schedules lose a deposit", naiveDeposits.violations() == 4);
        check("versioned: two deposits never lose an update", InterleavingChecker.explore(Strategy.VERSIONED, 0, twoDeposits).violations() == 0);
        check("locked: two deposits never lose an update", InterleavingChecker.explore(Strategy.LOCKED, 0, twoDeposits).violations() == 0);

        List<Op> overdraft = List.of(new Op("P1", -60), new Op("P2", -60));
        Exploration naiveOver = InterleavingChecker.explore(Strategy.NAIVE, 100, overdraft);
        check("naive: two 60 payouts from 100 can both 'succeed'", naiveOver.violations() > 0 && naiveOver.firstViolation().contains("lost update"));
        check("versioned: double payout prevented in every schedule", InterleavingChecker.explore(Strategy.VERSIONED, 100, overdraft).violations() == 0);
        check("locked: double payout prevented in every schedule", InterleavingChecker.explore(Strategy.LOCKED, 100, overdraft).violations() == 0);

        List<Op> three = List.of(new Op("P1", -6_000), new Op("P2", -6_000), new Op("D", 3_000));
        Exploration n3 = InterleavingChecker.explore(Strategy.NAIVE, 10_000, three);
        Exploration v3 = InterleavingChecker.explore(Strategy.VERSIONED, 10_000, three);
        Exploration l3 = InterleavingChecker.explore(Strategy.LOCKED, 10_000, three);
        check("naive: 6!/(2!2!2!) = 90 schedules", n3.schedules() == 90);
        check("naive: most schedules violate", n3.violations() == 76);
        check("versioned: retries add schedules but no violations", v3.schedules() > 90 && v3.violations() == 0);
        check("locked: only the 3! serial orders exist", l3.schedules() == 6 && l3.violations() == 0);

        // ---- single-threaded behaviour of each ledger ----
        for (ConcurrentLedger l : List.of(new PessimisticLedger(), new OptimisticLedger(), new ShardedLedger())) {
            String n = l.name() + ": ";
            l.openAccount("a", USD, 1_000, false);
            l.openAccount("b", USD, 0, false);
            l.openAccount("e", Currency.EUR, 0, false);
            l.transfer("a", "b", usd(400));
            check(n + "transfer moves money", l.balance("a").minorUnits() == 600 && l.balance("b").minorUnits() == 400);
            checkThrows(n + "overdraw rejected", InsufficientFundsException.class, () -> l.transfer("a", "b", usd(601)));
            check(n + "rejected transfer changed nothing", l.balance("a").minorUnits() == 600 && l.totalMinor() == 1_000);
            checkThrows(n + "currency mismatch", CurrencyMismatchException.class, () -> l.transfer("a", "e", usd(1)));
            checkThrows(n + "same account", IllegalArgumentException.class, () -> l.transfer("a", "a", usd(1)));
            checkThrows(n + "non-positive amount", IllegalArgumentException.class, () -> l.transfer("a", "b", usd(0)));
            checkThrows(n + "unknown account", IllegalArgumentException.class, () -> l.transfer("a", "zzz", usd(1)));
            checkThrows(n + "duplicate account", IllegalArgumentException.class, () -> l.openAccount("a", USD, 0, false));
        }
        OptimisticLedger opt = new OptimisticLedger();
        opt.openAccount("x", USD, 100, false);
        opt.openAccount("y", USD, 0, false);
        opt.transfer("x", "y", usd(10));
        opt.transfer("x", "y", usd(10));
        check("optimistic: every commit bumps the version", opt.version("x") == 2 && opt.version("y") == 2);

        // ---- sharding ----
        ShardedLedger sh = new ShardedLedger();
        sh.openHotAccount("m", USD, 10, 4);
        sh.openAccount("c", USD, 1_000, false);
        sh.openAccount("bank", USD, 0, true);
        check("hot float allocated across shards (3,3,2,2)", sh.shardBalance("m", 0) == 3 && sh.shardBalance("m", 1) == 3
                && sh.shardBalance("m", 2) == 2 && sh.shardBalance("m", 3) == 2);
        for (int i = 0; i < 8; i++) sh.transfer("c", "m", usd(100));
        check("credits spread round-robin, two per shard", sh.shardBalance("m", 0) == 203 && sh.shardBalance("m", 3) == 202);
        check("hot balance sums shards", sh.balance("m").minorUnits() == 810);
        sh.transfer("m", "bank", usd(500));
        check("debit spanning shards succeeds", sh.balance("m").minorUnits() == 310 && sh.balance("bank").minorUnits() == 500);
        checkThrows("debit beyond all shards rejected", InsufficientFundsException.class, () -> sh.transfer("m", "bank", usd(311)));
        check("rejected sharded debit restored every shard", sh.balance("m").minorUnits() == 310 && sh.balance("m#inflight").isZero());
        long before = sh.totalMinor();
        sh.rollUp("m");
        check("roll-up moves everything to shard 0", sh.shardBalance("m", 0) == 310 && sh.shardBalance("m", 1) == 0 && sh.shardBalance("m", 3) == 0);
        check("roll-up conserves the total", sh.totalMinor() == before);
        sh.openHotAccount("m2", USD, 0, 2);
        checkThrows("hot-to-hot rejected", IllegalArgumentException.class, () -> sh.transfer("m", "m2", usd(1)));

        // ---- multithreaded stress with a concurrent auditor taking consistent totals ----
        Workload w = new Workload(4, 5_000, 50, 10_000_000L, 500_000_000L, 10, 7);
        Map<String, Long> expected = w.expected();
        long initialTotal = 50 * 10_000_000L + 500_000_000L;
        for (ConcurrentLedger l : List.of(new PessimisticLedger(), new OptimisticLedger(), new ShardedLedger())) {
            w.open(l, 4);
            AtomicBoolean running = new AtomicBoolean(true);
            AtomicLong badCuts = new AtomicLong(), cuts = new AtomicLong();
            Thread auditor = new Thread(() -> {
                while (running.get()) {
                    cuts.incrementAndGet();
                    if (l.totalMinor() != initialTotal) badCuts.incrementAndGet();
                }
            });
            auditor.start();
            long failed = w.run(l);
            running.set(false);
            auditor.join();
            if (l instanceof ShardedLedger s) s.rollUp("merchant");
            boolean match = true;
            for (Map.Entry<String, Long> e : expected.entrySet()) match &= l.balance(e.getKey()).minorUnits() == e.getValue();
            check(l.name() + " stress: no operation failed", failed == 0);
            check(l.name() + " stress: total conserved", l.totalMinor() == initialTotal);
            check(l.name() + " stress: every balance equals the serial expectation", match);
            check(l.name() + " stress: every mid-run total the auditor took was exact", badCuts.get() == 0 && cuts.get() > 0);
            check(l.name() + " stress: no account overdrawn", l.balance("merchant").minorUnits() >= 0);
        }

        // ---- deadlock freedom: opposite-direction transfers on the same pair ----
        for (ConcurrentLedger l : List.of(new PessimisticLedger(), new OptimisticLedger())) {
            l.openAccount("p", USD, 1_000_000, false);
            l.openAccount("q", USD, 1_000_000, false);
            Thread t1 = new Thread(() -> { for (int i = 0; i < 20_000; i++) l.transfer("p", "q", usd(1)); });
            Thread t2 = new Thread(() -> { for (int i = 0; i < 20_000; i++) l.transfer("q", "p", usd(1)); });
            t1.start(); t2.start();
            t1.join(10_000); t2.join(10_000);
            check(l.name() + ": opposite transfers finish (global lock order)", !t1.isAlive() && !t2.isAlive());
            check(l.name() + ": pair balances restored", l.balance("p").minorUnits() == 1_000_000 && l.balance("q").minorUnits() == 1_000_000);
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
