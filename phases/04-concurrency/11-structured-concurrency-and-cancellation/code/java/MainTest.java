// Tests for phases/04-concurrency/11-structured-concurrency-and-cancellation. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
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

    public static void main(String[] args) throws Exception {
        ExecutorService pool = Executors.newVirtualThreadPerTaskExecutor();

        // --- Deadline: composition and expiry on an injected clock ---
        ManualClock clock = new ManualClock();
        Deadline d200 = Deadline.after(clock, Duration.ofMillis(200));
        Deadline d800 = Deadline.after(clock, Duration.ofMillis(800));
        check("earliest picks the tighter deadline", d800.earliest(d200).equals(d200) && d200.earliest(d800).equals(d200));
        check("NONE never wins over a real deadline", Deadline.NONE.earliest(d200).equals(d200));
        check("remaining before expiry", d200.remaining(clock).toMillis() == 200);
        check("not expired yet", !d200.isExpired(clock));
        clock.advance(Duration.ofMillis(200));
        check("expired exactly at the instant", d200.isExpired(clock) && d200.remaining(clock).isZero());
        check("NONE is never expired", !Deadline.NONE.isExpired(clock));

        // --- CancellationToken: tree, idempotence, callbacks, lazy deadline ---
        ManualClock c2 = new ManualClock();
        CancellationToken root = CancellationToken.root(c2);
        CancellationToken child = root.child(Deadline.after(c2, Duration.ofMillis(100)));
        CancellationToken grandchild = child.child(Deadline.after(c2, Duration.ofMillis(900)));
        check("child deadline is inherited by grandchild", grandchild.deadline().equals(child.deadline()));
        AtomicInteger callbacks = new AtomicInteger();
        grandchild.onCancel(callbacks::incrementAndGet);
        check("not cancelled initially", !root.isCancelled() && !child.isCancelled() && !grandchild.isCancelled());
        check("first cancel wins", root.cancel("operator abort") && !root.cancel("second reason"));
        check("reason is the first one", root.reason().equals("operator abort"));
        check("cancel propagates down two levels", child.isCancelled() && grandchild.isCancelled());
        check("callback ran exactly once", callbacks.get() == 1);
        grandchild.onCancel(callbacks::incrementAndGet);
        check("late registration runs immediately", callbacks.get() == 2);
        checkThrows("throwIfCancelled throws CancelledException", CancelledException.class, grandchild::throwIfCancelled);

        CancellationToken parent2 = CancellationToken.root(c2);
        CancellationToken kid = parent2.child();
        kid.cancel("child only");
        check("cancelling a child never cancels the parent", kid.isCancelled() && !parent2.isCancelled());
        CancellationToken lateKid = kid.child();
        check("child of a cancelled token is born cancelled", lateKid.isCancelled() && lateKid.reason().equals("child only"));

        ManualClock c3 = new ManualClock();
        CancellationToken timed = CancellationToken.root(c3).child(Deadline.after(c3, Duration.ofMillis(50)));
        c3.advance(Duration.ofMillis(49));
        check("deadline not yet reached", !timed.isCancelled());
        c3.advance(Duration.ofMillis(1));
        check("deadline expiry becomes a cancellation lazily", timed.isCancelled() && timed.reason().equals("deadline exceeded"));

        // --- StructuredScope: all succeed ---
        SystemClock sys = new SystemClock();
        try (StructuredScope scope = new StructuredScope("ok", pool, CancellationToken.root(sys), sys)) {
            Subtask<Integer> a = scope.fork("a", t -> 1);
            Subtask<Integer> b = scope.fork("b", t -> 2);
            scope.join();
            scope.throwIfFailed();
            check("both subtasks succeed", a.state() == Subtask.State.SUCCESS && b.state() == Subtask.State.SUCCESS);
            check("values available after join", a.get() + b.get() == 3);
            check("nothing active after join", scope.active() == 0);
            checkThrows("fork after join is rejected", IllegalStateException.class, () -> scope.fork("late", t -> 3));
        }

        // --- StructuredScope: one failure cancels the siblings, cleanup runs ---
        AtomicInteger cleanups = new AtomicInteger();
        CountDownLatch siblingReady = new CountDownLatch(1);
        try (StructuredScope scope = new StructuredScope("fail", pool, CancellationToken.root(sys), sys)) {
            Subtask<Integer> slow = scope.fork("slow", t -> {
                t.onCancel(cleanups::incrementAndGet);
                siblingReady.countDown();
                Main.waitForSignalOrCancel(t);
                return 1;
            });
            Subtask<Integer> boom = scope.fork("boom", t -> {
                siblingReady.await();
                throw new IllegalStateException("fraud rule tripped");
            });
            scope.join();
            check("failed subtask is FAILED", boom.state() == Subtask.State.FAILED && boom.exception() instanceof IllegalStateException);
            check("sibling is CANCELLED, not FAILED", slow.state() == Subtask.State.CANCELLED);
            check("sibling cleanup ran once", cleanups.get() == 1);
            check("scope reason names the failed task", scope.cancellationReason().contains("boom"));
            checkThrows("get on a cancelled subtask is an error", IllegalStateException.class, slow::get);
            boolean threw = false;
            try { scope.throwIfFailed(); } catch (ExecutionException e) { threw = e.getCause().getMessage().equals("fraud rule tripped"); } catch (TimeoutException e) { }
            check("throwIfFailed rethrows the first failure as the cause", threw);
            check("nothing active after a failed join", scope.active() == 0);
        }

        // --- StructuredScope: deadline on a manual clock cancels the stragglers ---
        ManualClock mc = new ManualClock();
        CountDownLatch started = new CountDownLatch(1);
        try (StructuredScope scope = new StructuredScope("deadline", pool, CancellationToken.root(mc), Deadline.after(mc, Duration.ofMillis(300)), mc)) {
            Subtask<Integer> straggler = scope.fork("straggler", t -> {
                started.countDown();
                Main.waitForSignalOrCancel(t);
                return 1;
            });
            started.await();
            mc.advance(Duration.ofMillis(301));          // the clock moves; nobody sleeps
            scope.join();
            check("straggler cancelled by the deadline", straggler.state() == Subtask.State.CANCELLED);
            check("scope reports deadline exceeded", "deadline exceeded".equals(scope.cancellationReason()));
            boolean timeout = false;
            try { scope.throwIfFailed(); } catch (TimeoutException e) { timeout = true; } catch (ExecutionException e) { }
            check("throwIfFailed turns a deadline into TimeoutException", timeout);
        }

        // --- Nested scopes: the child inherits the tighter parent budget ---
        ManualClock nc = new ManualClock();
        CancellationToken reqRoot = CancellationToken.root(nc);
        try (StructuredScope outer = new StructuredScope("request", pool, reqRoot, Deadline.after(nc, Duration.ofMillis(100)), nc)) {
            try (StructuredScope inner = new StructuredScope("bank-call", pool, outer.token(), Deadline.after(nc, Duration.ofMillis(5000)), nc)) {
                check("inner scope cannot outlive its parent's deadline", inner.deadline().equals(outer.deadline()));
                inner.join();
            }
            outer.join();
        }

        // --- Cancelling the parent token from outside stops a running scope ---
        CancellationToken external = CancellationToken.root(sys);
        CountDownLatch running = new CountDownLatch(1);
        try (StructuredScope scope = new StructuredScope("external", pool, external, sys)) {
            Subtask<Integer> worker = scope.fork("worker", t -> { running.countDown(); Main.waitForSignalOrCancel(t); return 1; });
            running.await();
            external.cancel("client disconnected");
            scope.join();
            check("external cancel reaches the subtask", worker.state() == Subtask.State.CANCELLED);
            check("reason is propagated unchanged", "client disconnected".equals(scope.cancellationReason()));
        }

        // --- close() without join: no task outlives the scope ---
        AtomicInteger completedNormally = new AtomicInteger();
        StructuredScope leaked;
        CountDownLatch allStarted = new CountDownLatch(3);
        try (StructuredScope scope = new StructuredScope("leaky", pool, CancellationToken.root(sys), sys)) {
            leaked = scope;
            for (int i = 0; i < 3; i++) {
                scope.fork("w" + i, t -> { allStarted.countDown(); Main.waitForSignalOrCancel(t); completedNormally.incrementAndGet(); return 1; });
            }
            allStarted.await();
        }
        check("close cancels an un-joined scope", leaked.isCancelled() && leaked.cancellationReason().contains("closed without join"));
        check("no subtask is still running after close", leaked.active() == 0);
        check("every subtask ended CANCELLED", leaked.subtasks().stream().allMatch(s -> s.state() == Subtask.State.CANCELLED));
        check("none completed normally", completedNormally.get() == 0);
        checkThrows("fork after close is rejected", IllegalStateException.class, () -> leaked.fork("late", t -> 1));

        // --- A task forked after cancellation never runs its body ---
        CancellationToken dead = CancellationToken.root(sys);
        dead.cancel("already dead");
        AtomicInteger bodyRuns = new AtomicInteger();
        try (StructuredScope scope = new StructuredScope("dead", pool, dead, sys)) {
            Subtask<Integer> never = scope.fork("never", t -> { bodyRuns.incrementAndGet(); return 1; });
            scope.join();
            check("body of a task under a cancelled token never runs", bodyRuns.get() == 0 && never.state() == Subtask.State.CANCELLED);
        }

        // --- Money guard used by the demo ---
        check("covers compares minor units", Money.ofMinor(10_000, Currency.USD).covers(Money.ofMinor(2_500, Currency.USD)));
        check("toString pads minor units", Money.ofMinor(2_505, Currency.USD).toString().equals("25.05 USD"));
        checkThrows("covers rejects currency mismatch", IllegalArgumentException.class,
                () -> Money.ofMinor(1, Currency.USD).covers(Money.ofMinor(1, new Currency("EUR", 2))));

        pool.shutdown();
        pool.awaitTermination(5, TimeUnit.SECONDS);

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
