// Tests for phases/05-distributed-foundations/08-timeouts-and-deadlines. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        // Deadline arithmetic
        FakeClock clock = new FakeClock(1_000);
        Deadline d = Deadline.after(clock, 500);
        check("deadline is absolute", d.atMs() == 1_500);
        check("remaining at creation", d.remainingMs(clock) == 500);
        clock.advance(200);
        check("remaining shrinks as time passes", d.remainingMs(clock) == 300);
        clock.advance(400);
        check("remaining clamps at zero, never negative", d.remainingMs(clock) == 0);
        check("expired once now >= at", d.isExpired(clock));
        check("NONE never expires", !Deadline.NONE.isExpired(clock) && Deadline.NONE.remainingMs(clock) == Long.MAX_VALUE);
        check("earliest picks the tighter deadline", new Deadline(10).earliest(new Deadline(5)).atMs() == 5);
        check("saturating add does not wrap", Deadline.saturatingAdd(Long.MAX_VALUE - 1, 10) == Long.MAX_VALUE);
        checkThrows("negative timeout rejected", IllegalArgumentException.class, () -> Deadline.after(clock, -1));

        // Context: children tighten, never extend
        FakeClock c = new FakeClock(0);
        Context parent = Context.root("r").withTimeout(c, 500);
        Context child = parent.withTimeout(c, 2_000);
        check("child cannot extend parent's deadline", child.deadline().atMs() == 500);
        Context tighter = parent.withTimeout(c, 100);
        check("child can tighten parent's deadline", tighter.deadline().atMs() == 100);
        check("requestId travels with the context", child.requestId().equals("r"));
        checkThrows("requireBudget refuses work that cannot fit", DeadlineExceededException.class,
                () -> parent.requireBudget(c, 600, "test"));
        c.advance(500);
        checkThrows("checkpoint throws once expired", DeadlineExceededException.class, () -> parent.checkpoint(c, "test"));

        // Wire encoding is relative to the sender's clock and rebuilt against the receiver's
        FakeClock sender = new FakeClock(1_000_000);
        FakeClock receiver = new FakeClock(7);        // wildly different clock: must not matter
        Context out = Context.root("w").withTimeout(sender, 250);
        sender.advance(50);
        String header = out.toWireTimeout(sender);
        check("wire header carries remaining ms with unit", header.equals("200m"));
        Context in = Context.fromWire("w", header, receiver);
        check("receiver reconstructs the remaining budget on its own clock", in.remainingMs(receiver) == 200 && in.deadline().atMs() == 207);
        check("no deadline encodes as absent header", Context.root("x").toWireTimeout(sender) == null
                && Context.fromWire("x", null, receiver).deadline().isNone());
        check("seconds unit decodes", Context.fromWire("x", "2S", receiver).remainingMs(receiver) == 2_000);
        check("microseconds unit decodes", Context.fromWire("x", "1500u", receiver).remainingMs(receiver) == 1);
        checkThrows("unknown unit rejected", IllegalArgumentException.class, () -> Context.fromWire("x", "5x", receiver));

        // The downstream call that outlives its caller
        FakeClock c1 = new FakeClock(0);
        EffectLog log1 = new EffectLog();
        CallOutcome o1 = new Client(c1).invoke(Main.buildChain(c1, log1, false), "p1", 500);
        check("without propagation the caller times out", o1.status() == Status.TIMED_OUT);
        check("without propagation the charge still happened", log1.contains("CHARGE"));
        check("the charge landed after the caller gave up", log1.all().get(0).atMs() > 500);

        FakeClock c2 = new FakeClock(0);
        EffectLog log2 = new EffectLog();
        CallOutcome o2 = new Client(c2).invoke(Main.buildChain(c2, log2, true), "p2", 500);
        check("with propagation the processor refuses before charging", o2.status() == Status.FAILED_FAST);
        check("with propagation no side effect is recorded", log2.all().isEmpty());
        check("fail-fast returns well inside the budget", o2.elapsedMs() < 500 && o2.elapsedMs() == 220);

        FakeClock c3 = new FakeClock(0);
        EffectLog log3 = new EffectLog();
        CallOutcome o3 = new Client(c3).invoke(Main.buildChain(c3, log3, true), "p3", 700);
        check("a budget that fits the chain succeeds", o3.status() == Status.OK && log3.all().size() == 1);

        // Retries inside a deadline
        FakeClock c4 = new FakeClock(0);
        FlakyService f4 = new FlakyService(c4, 150, 2);
        RetryOutcome r4 = new DeadlineAwareRetrier(c4, 300, 50, 50).call(f4, Context.root("rt").withTimeout(c4, 1_000));
        check("retrier succeeds on the third attempt within a generous deadline", r4.status() == Status.OK && r4.attempts() == 3);
        check("retrier elapsed is attempts plus backoffs", r4.elapsedMs() == 150 + 50 + 150 + 50 + 150);

        FakeClock c5 = new FakeClock(0);
        FlakyService f5 = new FlakyService(c5, 150, 2);
        RetryOutcome r5 = new DeadlineAwareRetrier(c5, 300, 50, 50).call(f5, Context.root("rt").withTimeout(c5, 400));
        check("retrier stops at the deadline instead of starting a doomed attempt", r5.status() == Status.FAILED_FAST && r5.attempts() == 2);
        check("second attempt budget was clipped to the remaining time", r5.attemptTimeoutsMs().equals(List.of(300L, 200L)));
        check("retrier never overran the deadline", c5.nowMs() <= 400);

        FakeClock c6 = new FakeClock(0);
        FlakyService f6 = new FlakyService(c6, 150, 0);
        RetryOutcome r6 = new DeadlineAwareRetrier(c6, 300, 50, 50).call(f6, Context.root("rt").withTimeout(c6, 100));
        check("an attempt that cannot fit is refused before any work", r6.status() == Status.FAILED_FAST && c6.nowMs() == 0);

        // Hedging
        Hedger.Result h1 = Hedger.call(800, 40, 60);
        check("hedge wins when the primary stalls", h1.latencyMs() == 100 && h1.winner().equals("backup") && h1.requestsSent() == 2);
        Hedger.Result h2 = Hedger.call(30, 40, 60);
        check("no hedge sent when the primary is fast", h2.requestsSent() == 1 && h2.latencyMs() == 30);
        Hedger.Result h3 = Hedger.call(70, 500, 60);
        check("primary still wins when the backup is slower", h3.winner().equals("primary") && h3.requestsSent() == 2 && h3.latencyMs() == 70);
        Hedger.Study s = Hedger.study(42, 10_000, 60);
        check("hedging cuts p99 by a large factor", s.p99Hedged() * 4 < s.p99());
        check("hedging costs only a few percent extra load", s.extraLoadPercent() < 10.0);
        check("study is deterministic under the seed", Hedger.study(42, 10_000, 60).equals(s));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
