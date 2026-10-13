// Lesson: phases/04-concurrency/06-executors-goroutines-and-async/docs/en.md
// Executors, Goroutines, and asyncio
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: JEP 444 (Virtual Threads, Java 21); java.util.concurrent.ExecutorService and CompletableFuture Javadoc;
// Go Memory Model and `go` statement spec (go.dev/ref/mem, go.dev/ref/spec); Python asyncio documentation (gather, wait_for).
// Java 21, standard library only. Time is injected through Timer; nothing sleeps for real.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.concurrent.Callable;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** Exact amount in minor units of one currency. Immutable, so a quote can be shared across threads freely. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String fracText = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", frac);
        return (minorUnits < 0 ? "-" : "") + whole + fracText + " " + currency.code();
    }
}

record PaymentRequest(String paymentId, Money amount) {}

enum Outcome { OK, TIMED_OUT, FAILED }

/** One processor's answer: a fee quote, or why there is none. {@code elapsedMillis} is simulated time from the injected Timer. */
record Quote(String processor, Money fee, Outcome outcome, String detail) {
    static Quote ok(String processor, Money fee) { return new Quote(processor, fee, Outcome.OK, ""); }
    static Quote timedOut(String processor, long afterMillis) { return new Quote(processor, null, Outcome.TIMED_OUT, "no answer within " + afterMillis + " ms"); }
    static Quote failed(String processor, Throwable t) {
        Throwable cause = t instanceof java.util.concurrent.CompletionException && t.getCause() != null ? t.getCause() : t;
        return new Quote(processor, null, Outcome.FAILED, cause.getClass().getSimpleName() + ": " + cause.getMessage());
    }
}

/** A card processor's quote endpoint. The real one is an HTTP call; here it is whatever the adapter decides. */
interface ProcessorAdapter {
    String name();
    CompletableFuture<Quote> quote(PaymentRequest request);
}

/**
 * Deterministic stand-in for a remote processor. Immediate adapters answer at once; scripted ones answer when the
 * test (or the demo) calls {@code respond} or {@code fail}; a hung adapter is a scripted one nobody ever answers.
 */
final class ScriptedAdapter implements ProcessorAdapter {
    private final String name;
    private final Money immediateFee;                   // null -> scripted
    private final List<CompletableFuture<Quote>> pending = new ArrayList<>();
    private final AtomicInteger calls = new AtomicInteger();

    private ScriptedAdapter(String name, Money immediateFee) { this.name = name; this.immediateFee = immediateFee; }

    static ScriptedAdapter immediate(String name, Money fee) { return new ScriptedAdapter(name, fee); }
    static ScriptedAdapter scripted(String name) { return new ScriptedAdapter(name, null); }

    @Override public String name() { return name; }

    @Override
    public synchronized CompletableFuture<Quote> quote(PaymentRequest request) {
        calls.incrementAndGet();
        if (immediateFee != null) return CompletableFuture.completedFuture(Quote.ok(name, immediateFee));
        CompletableFuture<Quote> f = new CompletableFuture<>();
        pending.add(f);
        return f;
    }

    /** Answer every outstanding call with this fee. */
    synchronized void respond(Money fee) {
        for (CompletableFuture<Quote> f : pending) f.complete(Quote.ok(name, fee));
        pending.clear();
    }

    synchronized void fail(String message) {
        for (CompletableFuture<Quote> f : pending) f.completeExceptionally(new IllegalStateException(message));
        pending.clear();
    }

    int calls() { return calls.get(); }
    synchronized int outstanding() { return pending.size(); }
}

/** The one place time enters the design. Production uses the delayed executor; tests and the demo advance a ManualTimer. */
interface Timer {
    CompletableFuture<Void> after(long millis);
    long nowMillis();
}

final class RealTimer implements Timer {
    private final long start = System.nanoTime();

    @Override public CompletableFuture<Void> after(long millis) {
        return CompletableFuture.runAsync(() -> {}, CompletableFuture.delayedExecutor(millis, TimeUnit.MILLISECONDS));
    }

    @Override public long nowMillis() { return (System.nanoTime() - start) / 1_000_000; }
}

/** Simulated clock: {@code advance} fires every timer whose deadline has passed, in deadline order. */
final class ManualTimer implements Timer {
    private record Pending(long deadline, long seq, CompletableFuture<Void> future) {}

    private final List<Pending> pending = new ArrayList<>();
    private long now = 0;
    private long seq = 0;

    @Override public synchronized CompletableFuture<Void> after(long millis) {
        if (millis < 0) throw new IllegalArgumentException("negative delay");
        CompletableFuture<Void> f = new CompletableFuture<>();
        pending.add(new Pending(now + millis, seq++, f));
        return f;
    }

    @Override public synchronized long nowMillis() { return now; }

    void advance(long millis) {
        List<Pending> due = new ArrayList<>();
        synchronized (this) {
            now += millis;
            pending.sort(Comparator.comparingLong(Pending::deadline).thenComparingLong(Pending::seq));
            for (var it = pending.iterator(); it.hasNext();) {
                Pending p = it.next();
                if (p.future.isCancelled()) { it.remove(); continue; }     // the caller no longer needs this alarm
                if (p.deadline <= now) { due.add(p); it.remove(); }
            }
        }
        for (Pending p : due) p.future.complete(null);                  // outside the lock: completion runs callbacks
    }

    /** Alarms that are still armed (not fired, not cancelled). */
    synchronized int outstanding() { return (int) pending.stream().filter(p -> !p.future.isCancelled()).count(); }
}

/**
 * Fan-out to every processor, fan-in to one list. Each adapter call races its own timeout; the combined future completes
 * when every adapter has either answered, failed, or been timed out. Nothing in here blocks a thread.
 */
final class FanOut {
    private FanOut() {}

    static CompletableFuture<Quote> withTimeout(ProcessorAdapter adapter, PaymentRequest request, long timeoutMillis, Timer timer) {
        CompletableFuture<Quote> call;
        try {
            call = adapter.quote(request);
        } catch (RuntimeException e) {                                   // a synchronous throw is still a failed quote, not a crashed fan-out
            return CompletableFuture.completedFuture(Quote.failed(adapter.name(), e));
        }
        CompletableFuture<Void> alarm = timer.after(timeoutMillis);
        CompletableFuture<Quote> deadline = alarm.thenApply(v -> Quote.timedOut(adapter.name(), timeoutMillis));
        call.whenComplete((q, t) -> alarm.cancel(false));                // the answer arrived: the alarm is no longer needed
        return call.applyToEither(deadline, q -> q)
                   .exceptionally(t -> Quote.failed(adapter.name(), t));
    }

    static CompletableFuture<List<Quote>> quoteAll(List<ProcessorAdapter> adapters, PaymentRequest request, long timeoutMillis, Timer timer) {
        List<CompletableFuture<Quote>> futures = new ArrayList<>();
        for (ProcessorAdapter a : adapters) futures.add(withTimeout(a, request, timeoutMillis, timer));
        return CompletableFuture.allOf(futures.toArray(CompletableFuture[]::new))
                .thenApply(v -> futures.stream().map(CompletableFuture::join).toList());   // join() cannot block: allOf already completed
    }

    /** First adapter to answer OK wins; timeouts and failures are ignored unless everyone produced one. */
    static CompletableFuture<Optional<Quote>> firstOk(List<ProcessorAdapter> adapters, PaymentRequest request, long timeoutMillis, Timer timer) {
        List<CompletableFuture<Quote>> futures = new ArrayList<>();
        for (ProcessorAdapter a : adapters) futures.add(withTimeout(a, request, timeoutMillis, timer));
        CompletableFuture<Optional<Quote>> winner = new CompletableFuture<>();
        AtomicInteger remaining = new AtomicInteger(futures.size());
        for (CompletableFuture<Quote> f : futures) {
            f.thenAccept(q -> {
                if (q.outcome() == Outcome.OK) winner.complete(Optional.of(q));           // first OK completes; later ones are no-ops
                if (remaining.decrementAndGet() == 0) winner.complete(Optional.empty());  // everyone answered, none OK
            });
        }
        return winner;
    }

    static Optional<Quote> cheapest(List<Quote> quotes) {
        return quotes.stream().filter(q -> q.outcome() == Outcome.OK).min(Comparator.comparing(Quote::fee));
    }
}

/**
 * The same fan-out written in the blocking style: one task per adapter, each task blocks until its answer arrives.
 * How well this works depends entirely on who supplies the threads, which is the point of the executor comparison.
 */
final class BlockingFanOut {
    private BlockingFanOut() {}

    static List<Quote> quoteAll(ExecutorService executor, List<ProcessorAdapter> adapters, PaymentRequest request) throws InterruptedException {
        List<Callable<Quote>> tasks = new ArrayList<>();
        for (ProcessorAdapter a : adapters) tasks.add(() -> a.quote(request).join());    // join() blocks this task's thread
        List<Quote> out = new ArrayList<>();
        List<Future<Quote>> futures = executor.invokeAll(tasks);          // returns only when every task is done
        for (int i = 0; i < futures.size(); i++) {
            try { out.add(futures.get(i).get()); }
            catch (java.util.concurrent.ExecutionException e) { out.add(Quote.failed(adapters.get(i).name(), e.getCause())); }
        }
        return out;
    }
}

/** A blocking task that records when it started running and then waits for a gate. Used to show pool starvation. */
final class GatedTask implements Callable<Integer> {
    private final int id;
    private final CountDownLatch gate;
    private final AtomicInteger started;
    private final CountDownLatch startedLatch;

    GatedTask(int id, CountDownLatch gate, AtomicInteger started, CountDownLatch startedLatch) {
        this.id = id; this.gate = gate; this.started = started; this.startedLatch = startedLatch;
    }

    @Override public Integer call() throws InterruptedException {
        started.incrementAndGet();
        startedLatch.countDown();
        gate.await();                                                     // a blocked thread: a card network that has not answered yet
        return id;
    }
}

public final class Main {

    /**
     * Submits {@code tasks} blocking tasks, waits until {@code expectedStarted} of them are running, then reports how many
     * started. With a fixed pool of N < tasks, exactly N start; the rest wait for a thread that is busy waiting. With virtual
     * threads, all of them start, because a blocked virtual thread costs a few hundred bytes, not a carrier.
     */
    static int countStartedBeforeRelease(ExecutorService executor, int tasks, int expectedStarted) throws InterruptedException {
        CountDownLatch gate = new CountDownLatch(1);
        CountDownLatch startedLatch = new CountDownLatch(expectedStarted);
        AtomicInteger started = new AtomicInteger();
        List<Future<Integer>> futures = new ArrayList<>();
        for (int i = 0; i < tasks; i++) futures.add(executor.submit(new GatedTask(i, gate, started, startedLatch)));
        startedLatch.await();
        int observed = started.get();                                      // tasks beyond the pool size cannot have started: no thread exists for them
        gate.countDown();
        for (Future<Integer> f : futures) { try { f.get(); } catch (Exception e) { throw new IllegalStateException(e); } }
        return observed;
    }

    static String describe(List<Quote> quotes) {
        StringBuilder sb = new StringBuilder();
        for (Quote q : quotes) {
            sb.append("  ").append(String.format("%-8s", q.processor())).append(q.outcome());
            if (q.outcome() == Outcome.OK) sb.append(" fee ").append(q.fee()); else sb.append(" (").append(q.detail()).append(")");
            sb.append('\n');
        }
        return sb.toString();
    }

    public static void main(String[] args) throws Exception {
        PaymentRequest request = new PaymentRequest("pay_001", Money.ofMinor(12_999, Currency.USD));

        System.out.println("== Model 1: a fixed thread pool and blocking calls ==");
        try (ExecutorService pool = Executors.newFixedThreadPool(2)) {
            int started = countStartedBeforeRelease(pool, 3, 2);
            System.out.println("3 blocking adapter calls on a pool of 2 threads: " + started + " started; the third waits for a thread that is itself waiting");
        }

        System.out.println();
        System.out.println("== Model 2: virtual threads, same blocking code ==");
        try (ExecutorService virtual = Executors.newVirtualThreadPerTaskExecutor()) {
            int started = countStartedBeforeRelease(virtual, 3, 3);
            System.out.println("3 blocking adapter calls on virtual threads: " + started + " started; a blocked virtual thread parks, it does not hold a carrier");
            ScriptedAdapter alpha = ScriptedAdapter.immediate("alpha", Money.ofMinor(329, Currency.USD));
            ScriptedAdapter beta = ScriptedAdapter.immediate("beta", Money.ofMinor(301, Currency.USD));
            List<Quote> quotes = BlockingFanOut.quoteAll(virtual, List.of(alpha, beta), request);
            System.out.print(describe(quotes));
            System.out.println("cheapest: " + FanOut.cheapest(quotes).map(Quote::processor).orElse("none"));
        }

        System.out.println();
        System.out.println("== Model 3: CompletableFuture fan-out with per-adapter timeouts (simulated clock) ==");
        ManualTimer timer = new ManualTimer();
        ScriptedAdapter alpha = ScriptedAdapter.immediate("alpha", Money.ofMinor(329, Currency.USD));
        ScriptedAdapter beta = ScriptedAdapter.scripted("beta");
        ScriptedAdapter gamma = ScriptedAdapter.scripted("gamma");
        ScriptedAdapter delta = ScriptedAdapter.scripted("delta");
        CompletableFuture<List<Quote>> all = FanOut.quoteAll(List.of(alpha, beta, gamma, delta), request, 800, timer);
        System.out.println("t=" + timer.nowMillis() + "ms  fan-out issued to 4 processors; alpha answered immediately; combined future done: " + all.isDone());
        timer.advance(250);
        beta.respond(Money.ofMinor(275, Currency.USD));
        System.out.println("t=" + timer.nowMillis() + "ms  beta answered; combined future done: " + all.isDone());
        timer.advance(300);
        delta.fail("HTTP 503 from processor");
        System.out.println("t=" + timer.nowMillis() + "ms  delta failed; gamma silent; combined future done: " + all.isDone());
        timer.advance(250);
        System.out.println("t=" + timer.nowMillis() + "ms  deadline passed; combined future done: " + all.isDone());
        List<Quote> quotes = all.join();
        System.out.print(describe(quotes));
        System.out.println("cheapest OK quote: " + FanOut.cheapest(quotes).map(q -> q.processor() + " at " + q.fee()).orElse("none"));
        System.out.println("gamma still has " + gamma.outstanding() + " outstanding call: the timeout abandoned the wait, it did not cancel the request");

        System.out.println();
        System.out.println("== Fan-in variant: first OK wins ==");
        ManualTimer timer2 = new ManualTimer();
        ScriptedAdapter slow = ScriptedAdapter.scripted("slow");
        ScriptedAdapter fast = ScriptedAdapter.scripted("fast");
        CompletableFuture<Optional<Quote>> first = FanOut.firstOk(List.of(slow, fast), request, 800, timer2);
        timer2.advance(100);
        fast.respond(Money.ofMinor(350, Currency.USD));
        System.out.println("fast answered at t=100ms -> winner: " + first.join().map(Quote::processor).orElse("none") + " (slow never answered and nobody waited for it)");
    }
}
