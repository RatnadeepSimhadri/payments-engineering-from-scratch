// Tests for phases/04-concurrency/07-deadlocks-and-lock-ordering. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    public static void main(String[] args) throws Exception {
        // The detector on hand-built graphs
        WaitForGraph two = new WaitForGraph();
        two.addEdge("T1", "T2");
        two.addEdge("T2", "T1");
        check("two-node cycle is found", two.findCycle().equals(List.of("T1", "T2")));
        WaitForGraph three = new WaitForGraph();
        three.addEdge("T2", "T3");
        three.addEdge("T3", "T1");
        three.addEdge("T1", "T2");
        check("three-node cycle reported in edge order from the smallest id", three.findCycle().equals(List.of("T1", "T2", "T3")));
        WaitForGraph chain = new WaitForGraph();
        chain.addEdge("T1", "T2");
        chain.addEdge("T2", "T3");
        chain.addEdge("T4", "T3");
        check("a chain of waiters is not a deadlock", chain.findCycle().isEmpty());
        WaitForGraph behind = new WaitForGraph();
        behind.addEdge("T4", "T1");                   // T4 waits behind the cycle but is not on it
        behind.addEdge("T1", "T2");
        behind.addEdge("T2", "T1");
        check("a waiter behind the cycle is not reported as part of it", behind.findCycle().equals(List.of("T1", "T2")));
        WaitForGraph diamond = new WaitForGraph();
        diamond.addEdge("T1", "T2");
        diamond.addEdge("T1", "T3");
        diamond.addEdge("T2", "T4");
        diamond.addEdge("T3", "T4");
        check("a diamond (shared successor) is not a cycle", diamond.findCycle().isEmpty());
        WaitForGraph self = new WaitForGraph();
        self.addEdge("T1", "T1");
        check("a self-loop is a cycle of one", self.findCycle().equals(List.of("T1")));
        check("empty graph has no cycle", new WaitForGraph().findCycle().isEmpty());

        // The tracker, scripted: the classic two-transfer deadlock replayed from recorded requests
        LockTracker scripted = new LockTracker();
        scripted.acquired("T1", "lock:A");
        scripted.acquired("T2", "lock:B");
        scripted.requesting("T1", "lock:B");
        check("one waiter, no cycle", scripted.detect().isEmpty());
        checkThrows("second request closes the cycle and is refused", DeadlockDetectedException.class, () -> scripted.requesting("T2", "lock:A"));
        try { scripted.requesting("T2", "lock:A"); } catch (DeadlockDetectedException e) {
            check("the exception names the cycle", e.cycle().equals(List.of("T1", "T2")));
        }
        check("the refused request is not left in the graph", scripted.graph().successors("T2").isEmpty());
        scripted.released("T2", "lock:B");
        scripted.acquired("T1", "lock:B");
        check("after T2 releases and T1 acquires, nothing waits", scripted.detect().isEmpty() && scripted.graph().nodes().isEmpty());
        check("events were recorded in order", scripted.events().get(0).equals("T1 acquired lock:A")
                && scripted.events().contains("T2 aborted: deadlock [T1, T2]"));
        LockTracker reentrant = new LockTracker();
        reentrant.acquired("T1", "lock:A");
        reentrant.requesting("T1", "lock:A");
        check("re-requesting a lock you hold is not a self-deadlock", reentrant.detect().isEmpty());

        // Live: two naive transfers forced into the deadlock interleaving, exactly one aborted by the detector
        LockTracker tracker = new LockTracker();
        Account a = new Account("A", Currency.USD, 10_000, tracker);
        Account b = new Account("B", Currency.USD, 10_000, tracker);
        TransferService naive = new TransferService(new Barrier(2), new Backoff(1));
        AtomicInteger aborted = new AtomicInteger(), committed = new AtomicInteger();
        Thread t1 = new Thread(() -> {
            try { naive.naiveTransfer(a, b, usd(2_500), "T1"); committed.incrementAndGet(); }
            catch (DeadlockDetectedException e) { aborted.incrementAndGet(); }
        });
        Thread t2 = new Thread(() -> {
            try { naive.naiveTransfer(b, a, usd(1_000), "T2"); committed.incrementAndGet(); }
            catch (DeadlockDetectedException e) { aborted.incrementAndGet(); }
        });
        t1.start(); t2.start();
        t1.join(10_000); t2.join(10_000);
        check("both threads finished (no hang)", !t1.isAlive() && !t2.isAlive());
        check("exactly one transfer was aborted by the detector", aborted.get() == 1 && committed.get() == 1);
        long sum = a.balanceMinor() + b.balanceMinor();
        check("money was conserved", sum == 20_000);
        check("the committed transfer is one of the two, not a mix",
                (a.balanceMinor() == 7_500 && b.balanceMinor() == 12_500) || (a.balanceMinor() == 11_000 && b.balanceMinor() == 9_000));
        check("no lock is left held", !a.lock().isLocked() && !b.lock().isLocked());

        // Ordered transfers under contention: terminates, conserves
        Account c = new Account("C", Currency.USD, 100_000, null);
        Account d = new Account("D", Currency.USD, 100_000, null);
        TransferService ordered = new TransferService(Thread::yield, new Backoff(7));
        Main.runAll(8, t -> {
            for (int i = 0; i < 500; i++) {
                if (t % 2 == 0) ordered.orderedTransfer(c, d, usd(1), "w" + t);
                else ordered.orderedTransfer(d, c, usd(1), "w" + t);
            }
        });
        check("ordered transfers: sum conserved", c.balanceMinor() + d.balanceMinor() == 200_000);
        check("ordered transfers: equal traffic each way leaves balances unchanged", c.balanceMinor() == 100_000 && d.balanceMinor() == 100_000);
        TransferService orderedTracked = new TransferService(() -> { }, new Backoff(3));
        LockTracker orderTracker = new LockTracker();
        Account e = new Account("E", Currency.USD, 1_000, orderTracker);
        Account f = new Account("F", Currency.USD, 1_000, orderTracker);
        orderedTracked.orderedTransfer(f, e, usd(300), "T9");
        check("ordered transfer locks the smaller id first even when sending from the larger",
                orderTracker.events().get(0).equals("T9 requests lock:E") && e.balanceMinor() == 1_300 && f.balanceMinor() == 700);

        // Try-lock transfers under contention: terminates, conserves, and never waits while holding
        Account g = new Account("G", Currency.USD, 100_000, null);
        Account h = new Account("H", Currency.USD, 100_000, null);
        Backoff backoff = new Backoff(7);
        TransferService trying = new TransferService(Thread::yield, backoff);
        Main.runAll(8, t -> {
            for (int i = 0; i < 500; i++) {
                if (t % 2 == 0) trying.tryLockTransfer(g, h, usd(1), "w" + t);
                else trying.tryLockTransfer(h, g, usd(1), "w" + t);
            }
        });
        check("try-lock transfers: sum conserved", g.balanceMinor() + h.balanceMinor() == 200_000 && g.balanceMinor() == 100_000);
        check("try-lock transfers: no lock left held", !g.lock().isLocked() && !h.lock().isLocked());
        Account i = new Account("I", Currency.USD, 100, null);
        Account j = new Account("J", Currency.USD, 100, null);
        java.util.concurrent.CountDownLatch holding = new java.util.concurrent.CountDownLatch(1);
        java.util.concurrent.CountDownLatch release = new java.util.concurrent.CountDownLatch(1);
        Thread holder = new Thread(() -> {             // another thread keeps J busy (a reentrant lock would let this thread in)
            j.lock().lock("someone-else");
            holding.countDown();
            try { release.await(); } catch (InterruptedException ex) { Thread.currentThread().interrupt(); }
            j.lock().unlock("someone-else");
        });
        holder.start();
        holding.await();
        Backoff counting = new Backoff(1);
        TransferService givesUp = new TransferService(() -> { }, counting, 5);
        checkThrows("try-lock transfer gives up after maxAttempts instead of hanging", IllegalStateException.class,
                () -> givesUp.tryLockTransfer(i, j, usd(1), "T5"));
        check("it backed off once per failed attempt and released I each time", counting.pauses() == 5 && !i.lock().isLocked());
        release.countDown();
        holder.join();

        // Business rules under the locks
        Account k = new Account("K", Currency.USD, 100, null);
        Account l = new Account("L", Currency.USD, 100, null);
        checkThrows("insufficient funds is refused with both locks held", InsufficientFundsException.class,
                () -> ordered.orderedTransfer(k, l, usd(101), "T6"));
        check("a refused transfer changes nothing and releases both locks",
                k.balanceMinor() == 100 && l.balanceMinor() == 100 && !k.lock().isLocked() && !l.lock().isLocked());
        checkThrows("negative amount is refused", IllegalArgumentException.class, () -> ordered.orderedTransfer(k, l, usd(-1), "T7"));
        checkThrows("same-account transfer is refused before any lock is taken", IllegalArgumentException.class,
                () -> ordered.orderedTransfer(k, k, usd(1), "T8"));
        check("Money renders minor units", usd(12_500).toString().equals("125.00 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(x -> System.out.println("FAIL: " + x));
        if (!failures.isEmpty()) System.exit(1);
    }
}
