// Lesson: phases/04-concurrency/11-structured-concurrency-and-cancellation/docs/en.md
// Structured Concurrency and Cancellation
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: JEP 453 (Structured Concurrency, preview) for the scope/fork/join shape; Go `context` package docs
// for deadline and cancellation propagation. Java 21, standard library only, no preview features.

import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Objects;
import java.util.concurrent.CancellationException;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;

/** ISO 4217 currency, copied from Phase 10 Lesson 01: minor units + currency, never a float. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    boolean covers(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch");
        return minorUnits >= other.minorUnits;
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String f = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", frac);
        return (minorUnits < 0 ? "-" : "") + whole + f + " " + currency.code();
    }
}

/** A monotonic clock in nanoseconds. Injected so deadlines can be tested without sleeping. */
interface Clock {
    long nanos();
}

final class SystemClock implements Clock {
    @Override public long nanos() { return System.nanoTime(); }
}

/** A clock that only moves when the test or the demo moves it. */
final class ManualClock implements Clock {
    private final AtomicLong now = new AtomicLong();

    @Override public long nanos() { return now.get(); }

    void advance(Duration d) { now.addAndGet(d.toNanos()); }
}

/**
 * An absolute instant on the injected clock by which work must be done. `NONE` never expires.
 * Deadlines compose by taking the earliest, which is how a child inherits its parent's budget.
 */
record Deadline(long atNanos) {
    static final Deadline NONE = new Deadline(Long.MAX_VALUE);

    static Deadline after(Clock clock, Duration d) {
        return new Deadline(Math.addExact(clock.nanos(), d.toNanos()));
    }

    Deadline earliest(Deadline other) { return atNanos <= other.atNanos ? this : other; }

    boolean isExpired(Clock clock) { return atNanos != Long.MAX_VALUE && clock.nanos() >= atNanos; }

    Duration remaining(Clock clock) {
        if (atNanos == Long.MAX_VALUE) return Duration.ofNanos(Long.MAX_VALUE);
        return Duration.ofNanos(Math.max(0, atNanos - clock.nanos()));
    }

    boolean isNone() { return atNanos == Long.MAX_VALUE; }
}

/** Thrown by cooperative code when it notices its token has been cancelled. */
final class CancelledException extends RuntimeException {
    CancelledException(String reason) { super(reason); }
}

/**
 * A cooperative cancellation signal arranged in a tree: cancelling a parent cancels every
 * descendant; cancelling a child never touches the parent. The token carries the effective deadline
 * (the earliest along the path to the root) and, because it knows the clock, expiry turns into a
 * cancellation lazily the next time anyone asks. This is Go's `context.Context` in forty lines.
 */
final class CancellationToken {
    private final CancellationToken parent;
    private final Clock clock;
    private final Deadline deadline;
    private final AtomicReference<String> reason = new AtomicReference<>();
    private final List<Runnable> callbacks = new CopyOnWriteArrayList<>();
    private final List<CancellationToken> children = new CopyOnWriteArrayList<>();

    private CancellationToken(CancellationToken parent, Clock clock, Deadline deadline) {
        this.parent = parent;
        this.clock = Objects.requireNonNull(clock);
        this.deadline = Objects.requireNonNull(deadline);
    }

    static CancellationToken root(Clock clock) { return new CancellationToken(null, clock, Deadline.NONE); }

    /** A child whose deadline is the earlier of its own and its parent's. */
    CancellationToken child(Deadline own) {
        CancellationToken c = new CancellationToken(this, clock, deadline.earliest(own));
        children.add(c);
        if (isCancelled()) c.cancel(reason.get());
        return c;
    }

    CancellationToken child() { return child(Deadline.NONE); }

    Deadline deadline() { return deadline; }

    /** Idempotent. The first reason wins; callbacks run exactly once, then children are cancelled. */
    boolean cancel(String why) {
        if (!reason.compareAndSet(null, Objects.requireNonNull(why))) return false;
        for (Runnable r : callbacks) r.run();
        for (CancellationToken c : children) c.cancel(why);
        return true;
    }

    /** True once cancelled explicitly or once the deadline has passed on the clock. */
    boolean isCancelled() {
        if (reason.get() == null && deadline.isExpired(clock)) cancel("deadline exceeded");
        return reason.get() != null;
    }

    String reason() { isCancelled(); return reason.get(); }

    /** Registers cleanup. If already cancelled the callback runs now, so registration can never miss the signal. */
    void onCancel(Runnable callback) {
        callbacks.add(callback);
        if (isCancelled()) callback.run();
    }

    void throwIfCancelled() {
        if (isCancelled()) throw new CancelledException(reason.get());
    }

    CancellationToken parent() { return parent; }
}

/** The body of a subtask. It receives the scope's token so it can check for cancellation and inherit the deadline. */
@FunctionalInterface
interface Task<T> {
    T call(CancellationToken token) throws Exception;
}

/** The handle a scope returns from fork. Values are available only after join. */
final class Subtask<T> {
    enum State { UNAVAILABLE, SUCCESS, FAILED, CANCELLED }

    private final String name;
    private volatile State state = State.UNAVAILABLE;
    private volatile T value;
    private volatile Throwable failure;
    private Thread runner;             // guarded by this; non-null only while the body is executing

    Subtask(String name) { this.name = name; }

    synchronized void started(Thread t) { runner = t; }
    synchronized void finished() { runner = null; }

    /** Interrupts the body only while it is running, so a pooled thread never carries a stale interrupt. */
    synchronized void interrupt() { if (runner != null) runner.interrupt(); }

    String name() { return name; }
    State state() { return state; }

    T get() {
        if (state != State.SUCCESS) throw new IllegalStateException(name + " is " + state + ", not SUCCESS");
        return value;
    }

    Throwable exception() { return failure; }

    void succeed(T v) { value = v; state = State.SUCCESS; }
    void fail(Throwable t) { failure = t; state = State.FAILED; }
    void cancelled() { state = State.CANCELLED; }
}

/**
 * A structured scope: every task forked inside it is joined before the scope exits, no exceptions.
 * Policy is shutdown-on-failure: the first failure cancels the siblings. The deadline is inherited from
 * the parent token and tightened by the scope's own budget. `close` guarantees that no forked task is
 * still running when it returns, so a scope has the same lifetime discipline as a block of code.
 */
final class StructuredScope implements AutoCloseable {
    private final String name;
    private final ExecutorService executor;
    private final Clock clock;
    private final CancellationToken token;
    private final List<Subtask<?>> subtasks = new ArrayList<>();
    private final AtomicInteger running = new AtomicInteger();
    private final Object completion = new Object();
    private volatile boolean joined;
    private volatile boolean closed;
    private volatile Subtask<?> firstFailure;

    StructuredScope(String name, ExecutorService executor, CancellationToken parent, Deadline deadline, Clock clock) {
        this.name = Objects.requireNonNull(name);
        this.executor = Objects.requireNonNull(executor);
        this.clock = Objects.requireNonNull(clock);
        this.token = parent.child(deadline);
    }

    StructuredScope(String name, ExecutorService executor, CancellationToken parent, Clock clock) {
        this(name, executor, parent, Deadline.NONE, clock);
    }

    CancellationToken token() { return token; }
    Deadline deadline() { return token.deadline(); }
    String name() { return name; }

    <T> Subtask<T> fork(String taskName, Task<T> body) {
        if (joined || closed) throw new IllegalStateException(name + ": cannot fork after join or close");
        Subtask<T> subtask = new Subtask<>(taskName);
        subtasks.add(subtask);
        running.incrementAndGet();
        executor.execute(() -> {
            subtask.started(Thread.currentThread());
            try {
                if (token.isCancelled()) {
                    subtask.cancelled();
                } else {
                    subtask.succeed(body.call(token));
                }
            } catch (CancelledException | InterruptedException | CancellationException e) {
                subtask.cancelled();
            } catch (Throwable t) {
                if (token.isCancelled()) {
                    subtask.cancelled();
                } else {
                    subtask.fail(t);
                    onFailure(subtask);
                }
            } finally {
                subtask.finished();
                running.decrementAndGet();
                synchronized (completion) { completion.notifyAll(); }
            }
        });
        return subtask;
    }

    private void onFailure(Subtask<?> failed) {
        synchronized (this) {
            if (firstFailure == null) firstFailure = failed;
        }
        token.cancel(name + ": " + failed.name() + " failed: " + failed.exception());
        interruptRunning();
    }

    private void interruptRunning() {
        for (Subtask<?> s : subtasks) s.interrupt();
    }

    /**
     * Waits until every subtask has finished, or the deadline passes (then cancels and still waits for
     * every subtask to acknowledge). Polls in short real slices so a manual clock advanced by another
     * thread is noticed; never sleeps for simulated time.
     */
    void join() throws InterruptedException {
        joined = true;
        while (running.get() > 0) {
            if (token.isCancelled()) interruptRunning();
            synchronized (completion) {
                if (running.get() > 0) completion.wait(5);
            }
        }
        for (Subtask<?> s : subtasks) {
            if (s.state() == Subtask.State.UNAVAILABLE) s.cancelled();
        }
    }

    /** After join: rethrows the first failure. A cancelled-by-deadline scope throws TimeoutException. */
    void throwIfFailed() throws ExecutionException, TimeoutException {
        if (!joined) throw new IllegalStateException(name + ": throwIfFailed before join");
        Subtask<?> f = firstFailure;
        if (f != null) throw new ExecutionException(f.name() + " failed", f.exception());
        if (token.isCancelled() && token.reason().startsWith("deadline")) throw new TimeoutException(name + ": " + token.reason());
    }

    boolean isCancelled() { return token.isCancelled(); }
    String cancellationReason() { return token.reason(); }
    /** Number of forked tasks that have not yet finished; zero after join or close. */
    int active() { return running.get(); }
    List<Subtask<?>> subtasks() { return Collections.unmodifiableList(subtasks); }

    /** Leaving the scope without joining is a bug; close cancels what is left and waits for it anyway. */
    @Override public void close() {
        if (closed) return;
        closed = true;
        if (!joined) {
            token.cancel(name + ": scope closed without join");
            boolean interrupted = false;
            while (true) {
                try { join(); break; }
                catch (InterruptedException e) { interrupted = true; }
            }
            if (interrupted) Thread.currentThread().interrupt();
        }
    }
}

public final class Main {

    /** A fan-out authorization: three checks, one answer. All three share one token and one deadline. */
    static String authorize(ExecutorService pool, CancellationToken parent, Clock clock, Duration budget,
                            Money balance, Money amount, boolean fraudFails, ManualClock slowBank, List<String> log)
            throws InterruptedException {
        CountDownLatch siblingsStarted = new CountDownLatch(2);   // makes the demo's ordering deterministic
        try (StructuredScope scope = new StructuredScope("authorize", pool, parent, Deadline.after(clock, budget), clock)) {
            Subtask<Boolean> fraud = scope.fork("fraud-check", token -> {
                siblingsStarted.await();
                if (fraudFails) throw new IllegalStateException("velocity rule tripped");
                return true;
            });
            Subtask<Boolean> funds = scope.fork("balance-check", token -> {
                token.onCancel(() -> log.add("balance-check: released hold"));
                siblingsStarted.countDown();
                waitForSignalOrCancel(token);
                return balance.covers(amount);
            });
            Subtask<Long> fx = scope.fork("fx-quote", token -> {
                token.onCancel(() -> log.add("fx-quote: closed quote stream"));
                siblingsStarted.countDown();
                if (slowBank != null) slowBank.advance(budget.plusMillis(1));   // the bank is slower than our budget
                waitForSignalOrCancel(token);
                return 10_000L;
            });
            scope.join();
            try {
                scope.throwIfFailed();
            } catch (ExecutionException e) {
                return "DECLINED (" + e.getCause().getMessage() + ")";
            } catch (TimeoutException e) {
                return "TIMEOUT (" + e.getMessage() + ")";
            }
            return fraud.get() && funds.get() ? "APPROVED at rate " + fx.get() : "DECLINED (insufficient funds)";
        }
    }

    /** Simulates a remote call that completes only when cancelled or when a signal arrives; here nobody signals. */
    static void waitForSignalOrCancel(CancellationToken token) throws InterruptedException {
        Object monitor = new Object();
        token.onCancel(() -> { synchronized (monitor) { monitor.notifyAll(); } });
        synchronized (monitor) {
            while (!token.isCancelled()) monitor.wait(5);
        }
        token.throwIfCancelled();
    }

    public static void main(String[] args) throws Exception {
        ExecutorService pool = Executors.newVirtualThreadPerTaskExecutor();
        Clock clock = new SystemClock();
        Money balance = Money.ofMinor(10_000, Currency.USD);
        Money amount = Money.ofMinor(2_500, Currency.USD);

        System.out.println("== Deadlines compose by taking the earliest ==");
        ManualClock mc = new ManualClock();
        CancellationToken root = CancellationToken.root(mc);
        CancellationToken request = root.child(Deadline.after(mc, Duration.ofMillis(200)));
        CancellationToken bankCall = request.child(Deadline.after(mc, Duration.ofMillis(800)));
        System.out.println("request budget  = " + request.deadline().remaining(mc).toMillis() + " ms");
        System.out.println("bank-call asked = 800 ms, got " + bankCall.deadline().remaining(mc).toMillis() + " ms (inherited)");
        mc.advance(Duration.ofMillis(250));
        System.out.println("after 250 ms: request cancelled? " + request.isCancelled() + " (" + request.reason() + ")");
        System.out.println("              bank-call cancelled? " + bankCall.isCancelled() + ", root cancelled? " + root.isCancelled());

        System.out.println();
        System.out.println("== One sibling fails: the others are cancelled and clean up ==");
        List<String> log = new ArrayList<>();
        String r1 = authorize(pool, CancellationToken.root(clock), clock, Duration.ofSeconds(5), balance, amount, true, null, log);
        System.out.println(r1);
        Collections.sort(log);
        for (String line : log) System.out.println("  cleanup: " + line);

        System.out.println();
        System.out.println("== The bank is slower than the budget: deadline cancels everything ==");
        ManualClock slow = new ManualClock();
        List<String> log2 = new ArrayList<>();
        String r2 = authorize(pool, CancellationToken.root(slow), slow, Duration.ofMillis(300), balance, amount, false, slow, log2);
        System.out.println(r2);
        Collections.sort(log2);
        for (String line : log2) System.out.println("  cleanup: " + line);

        System.out.println();
        System.out.println("== Leaving a scope without join still waits for every child ==");
        AtomicInteger finished = new AtomicInteger();
        StructuredScope leaked;
        try (StructuredScope scope = new StructuredScope("leaky", pool, CancellationToken.root(clock), clock)) {
            leaked = scope;
            for (int i = 0; i < 3; i++) {
                scope.fork("worker-" + i, token -> { waitForSignalOrCancel(token); finished.incrementAndGet(); return null; });
            }
            // no join: the try block exits early, say because of an early return
        }
        System.out.println("scope cancelled on close: " + leaked.cancellationReason());
        long cancelled = leaked.subtasks().stream().filter(s -> s.state() == Subtask.State.CANCELLED).count();
        System.out.println("subtasks cancelled = " + cancelled + " of 3; completed normally = " + finished.get()
                + "; still running after close = " + leaked.active());

        pool.shutdown();
        pool.awaitTermination(5, TimeUnit.SECONDS);
    }
}
