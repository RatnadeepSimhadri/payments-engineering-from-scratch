// Tests for phases/04-concurrency/06-executors-goroutines-and-async. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

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

    static final PaymentRequest REQUEST = new PaymentRequest("pay_001", Money.ofMinor(12_999, Currency.USD));

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    /** An adapter whose quote() throws synchronously, as a client library with a broken configuration would. */
    static final class ThrowingAdapter implements ProcessorAdapter {
        @Override public String name() { return "broken"; }
        @Override public CompletableFuture<Quote> quote(PaymentRequest request) { throw new IllegalArgumentException("no API key configured"); }
    }

    public static void main(String[] args) throws Exception {
        money();
        manualTimer();
        withTimeout();
        quoteAllAndCheapest();
        firstOk();
        executorModels();
        blockingFanOut();

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }

    static void money() {
        check("toString pads minor units", usd(305).toString().equals("3.05 USD"));
        check("compareTo orders fees", usd(275).compareTo(usd(329)) < 0);
        checkThrows("compare across currencies is an error", IllegalArgumentException.class, () -> usd(1).compareTo(Money.ofMinor(1, Currency.EUR)));
    }

    static void manualTimer() {
        ManualTimer timer = new ManualTimer();
        CompletableFuture<Void> a = timer.after(100);
        CompletableFuture<Void> b = timer.after(50);
        check("alarms are not done before their deadline", !a.isDone() && !b.isDone() && timer.outstanding() == 2);
        timer.advance(49);
        check("advance short of the deadline fires nothing", !b.isDone() && timer.nowMillis() == 49);
        timer.advance(1);
        check("advance to the deadline fires exactly that alarm", b.isDone() && !a.isDone() && timer.outstanding() == 1);
        timer.advance(1000);
        check("a large advance fires everything due", a.isDone() && timer.outstanding() == 0);
        List<String> order = new ArrayList<>();
        timer.after(30).thenRun(() -> order.add("30"));
        timer.after(10).thenRun(() -> order.add("10"));
        timer.after(10).thenRun(() -> order.add("10b"));
        timer.advance(100);
        check("alarms fire in deadline order, ties in creation order", order.equals(List.of("10", "10b", "30")));
        CompletableFuture<Void> cancelled = timer.after(5);
        cancelled.cancel(false);
        check("a cancelled alarm is not outstanding", timer.outstanding() == 0);
        timer.advance(10);
        check("advancing past a cancelled alarm is harmless", cancelled.isCancelled());
        checkThrows("negative delay rejected", IllegalArgumentException.class, () -> timer.after(-1));
    }

    static void withTimeout() {
        ManualTimer timer = new ManualTimer();
        Quote immediate = FanOut.withTimeout(ScriptedAdapter.immediate("alpha", usd(329)), REQUEST, 800, timer).join();
        check("immediate adapter yields OK with its fee", immediate.outcome() == Outcome.OK && immediate.fee().equals(usd(329)) && immediate.processor().equals("alpha"));
        check("an answered call cancels its alarm", timer.outstanding() == 0);

        ScriptedAdapter beta = ScriptedAdapter.scripted("beta");
        CompletableFuture<Quote> betaQuote = FanOut.withTimeout(beta, REQUEST, 800, timer);
        timer.advance(799);
        check("not timed out one ms before the deadline", !betaQuote.isDone());
        beta.respond(usd(275));
        check("answer just before the deadline is OK", betaQuote.join().outcome() == Outcome.OK && betaQuote.join().fee().equals(usd(275)));
        check("alarm cancelled after a late answer", timer.outstanding() == 0);

        ScriptedAdapter gamma = ScriptedAdapter.scripted("gamma");
        CompletableFuture<Quote> gammaQuote = FanOut.withTimeout(gamma, REQUEST, 800, timer);
        timer.advance(800);
        check("silent adapter is TIMED_OUT at the deadline", gammaQuote.isDone() && gammaQuote.join().outcome() == Outcome.TIMED_OUT);
        check("timeout detail names the budget", gammaQuote.join().detail().equals("no answer within 800 ms"));
        check("timeout does not cancel the underlying request", gamma.outstanding() == 1);
        gamma.respond(usd(1));
        check("a late answer after timeout is ignored", gammaQuote.join().outcome() == Outcome.TIMED_OUT);

        ScriptedAdapter delta = ScriptedAdapter.scripted("delta");
        CompletableFuture<Quote> deltaQuote = FanOut.withTimeout(delta, REQUEST, 800, timer);
        delta.fail("HTTP 503 from processor");
        check("exceptional completion becomes FAILED with the cause", deltaQuote.join().outcome() == Outcome.FAILED && deltaQuote.join().detail().equals("IllegalStateException: HTTP 503 from processor"));

        Quote broken = FanOut.withTimeout(new ThrowingAdapter(), REQUEST, 800, timer).join();
        check("a synchronous throw is FAILED, not a crashed fan-out", broken.outcome() == Outcome.FAILED && broken.detail().startsWith("IllegalArgumentException"));
    }

    static void quoteAllAndCheapest() {
        ManualTimer timer = new ManualTimer();
        ScriptedAdapter alpha = ScriptedAdapter.immediate("alpha", usd(329));
        ScriptedAdapter beta = ScriptedAdapter.scripted("beta");
        ScriptedAdapter gamma = ScriptedAdapter.scripted("gamma");
        ScriptedAdapter delta = ScriptedAdapter.scripted("delta");
        CompletableFuture<List<Quote>> all = FanOut.quoteAll(List.of(alpha, beta, gamma, delta), REQUEST, 800, timer);
        check("every adapter called exactly once", alpha.calls() == 1 && beta.calls() == 1 && gamma.calls() == 1 && delta.calls() == 1);
        check("combined future waits for the slowest", !all.isDone());
        timer.advance(250);
        beta.respond(usd(275));
        timer.advance(300);
        delta.fail("HTTP 503 from processor");
        check("still waiting on the silent adapter", !all.isDone());
        timer.advance(250);
        check("combined future completes at the deadline", all.isDone());
        List<Quote> quotes = all.join();
        check("results keep adapter order", quotes.get(0).processor().equals("alpha") && quotes.get(1).processor().equals("beta")
                && quotes.get(2).processor().equals("gamma") && quotes.get(3).processor().equals("delta"));
        check("outcomes per adapter", quotes.get(0).outcome() == Outcome.OK && quotes.get(1).outcome() == Outcome.OK
                && quotes.get(2).outcome() == Outcome.TIMED_OUT && quotes.get(3).outcome() == Outcome.FAILED);
        Optional<Quote> cheapest = FanOut.cheapest(quotes);
        check("cheapest ignores non-OK quotes and picks the lowest fee", cheapest.isPresent() && cheapest.get().processor().equals("beta"));
        check("cheapest of nothing is empty", FanOut.cheapest(List.of(quotes.get(2), quotes.get(3))).isEmpty());
        check("fan-out over zero adapters completes with an empty list", FanOut.quoteAll(List.of(), REQUEST, 800, timer).join().isEmpty());
    }

    static void firstOk() {
        ManualTimer timer = new ManualTimer();
        ScriptedAdapter slow = ScriptedAdapter.scripted("slow");
        ScriptedAdapter fast = ScriptedAdapter.scripted("fast");
        CompletableFuture<Optional<Quote>> first = FanOut.firstOk(List.of(slow, fast), REQUEST, 800, timer);
        check("no winner yet", !first.isDone());
        fast.respond(usd(350));
        check("first OK answer wins immediately", first.isDone() && first.join().get().processor().equals("fast"));
        slow.respond(usd(1));
        check("a later, cheaper answer does not change the winner", first.join().get().processor().equals("fast"));

        ManualTimer timer2 = new ManualTimer();
        ScriptedAdapter a = ScriptedAdapter.scripted("a");
        ScriptedAdapter b = ScriptedAdapter.scripted("b");
        CompletableFuture<Optional<Quote>> none = FanOut.firstOk(List.of(a, b), REQUEST, 800, timer2);
        a.fail("down");
        check("one failure is not a verdict", !none.isDone());
        timer2.advance(800);
        check("all failed or timed out yields empty", none.isDone() && none.join().isEmpty());
    }

    static void executorModels() throws InterruptedException {
        try (ExecutorService pool = Executors.newFixedThreadPool(2)) {
            check("fixed pool of 2: only 2 of 3 blocking tasks start", Main.countStartedBeforeRelease(pool, 3, 2) == 2);
        }
        try (ExecutorService pool = Executors.newFixedThreadPool(4)) {
            check("fixed pool of 4: all 3 start", Main.countStartedBeforeRelease(pool, 3, 3) == 3);
        }
        try (ExecutorService virtual = Executors.newVirtualThreadPerTaskExecutor()) {
            check("virtual threads: all 3 start", Main.countStartedBeforeRelease(virtual, 3, 3) == 3);
            check("virtual threads: 200 blocked tasks all start", Main.countStartedBeforeRelease(virtual, 200, 200) == 200);
        }
    }

    static void blockingFanOut() throws InterruptedException {
        try (ExecutorService virtual = Executors.newVirtualThreadPerTaskExecutor()) {
            ScriptedAdapter alpha = ScriptedAdapter.immediate("alpha", usd(329));
            ScriptedAdapter beta = ScriptedAdapter.immediate("beta", usd(301));
            List<Quote> quotes = BlockingFanOut.quoteAll(virtual, List.of(alpha, new ThrowingAdapter(), beta), REQUEST);
            check("blocking fan-out keeps order", quotes.get(0).processor().equals("alpha") && quotes.get(2).processor().equals("beta"));
            check("blocking fan-out reports a thrown task as FAILED under the adapter's name", quotes.get(1).outcome() == Outcome.FAILED && quotes.get(1).processor().equals("broken"));
            check("blocking fan-out cheapest", FanOut.cheapest(quotes).get().processor().equals("beta"));
        }
    }
}
