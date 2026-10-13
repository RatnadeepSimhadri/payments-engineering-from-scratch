// Tests for phases/08-messaging-and-streams/06-cqrs-and-projections. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    /** A store with two accounts and nine events, the same shape as the demo. */
    static EventStore seeded(SimClock clock) {
        EventStore store = new EventStore();
        CommandHandler h = new CommandHandler(store, clock);
        h.handle("a", new OpenAccount("a", "Alice", Currency.USD));
        h.handle("b", new OpenAccount("b", "Bob", Currency.USD));
        h.handle("a", new Deposit(usd(250_000), "d1"));
        h.handle("b", new Deposit(usd(80_000), "d2"));
        clock.advance(86_400_000);
        h.handle("a", new Withdraw(usd(120_000), "w1"));
        h.handle("b", new Withdraw(usd(60_000), "w2"));
        h.handle("a", new Withdraw(usd(75_000), "w3"));
        h.handle("b", new Deposit(usd(15_000), "d3"));
        h.handle("a", new Deposit(usd(5_000), "d4"));
        return store;
    }

    public static void main(String[] args) {
        SimClock clock = new SimClock(1_700_000_000_000L);
        EventStore store = seeded(clock);
        CommandHandler handler = new CommandHandler(store, clock);

        // write side
        check("global positions are dense and 1-based", store.lastPosition() == 9 && store.readAll(0, 1).get(0).position() == 1);
        check("stream versions are independent of global position", store.version("a") == 5 && store.version("b") == 4);
        check("stream read returns only that stream in version order", store.readStream("a").size() == 5 && store.readStream("a").get(4).version() == 5);
        checkThrows("overdraft rejected by the write model", CommandRejectedException.class, () -> handler.handle("b", new Withdraw(usd(999_999), "x")));
        checkThrows("deposit to unknown account rejected", CommandRejectedException.class, () -> handler.handle("zz", new Deposit(usd(1), "x")));
        checkThrows("reopening an account rejected", CommandRejectedException.class, () -> handler.handle("a", new OpenAccount("a", "A", Currency.USD)));
        checkThrows("non-positive deposit rejected", CommandRejectedException.class, () -> handler.handle("a", new Deposit(usd(0), "x")));
        checkThrows("stale expected version is a concurrency error", ConcurrencyException.class, () -> store.append("a", 2, List.of(new Deposited(usd(1), "x")), clock.now()));
        check("rejected commands append nothing", store.lastPosition() == 9);
        checkThrows("readAll beyond the log is an error", IllegalArgumentException.class, () -> store.readAll(10, 1));
        check("readAll at the end is empty", store.readAll(9, 5).isEmpty());
        check("readAll respects max", store.readAll(0, 4).size() == 4 && store.readAll(0, 4).get(3).position() == 4);

        // projections fold the log
        CheckpointStore checkpoints = new CheckpointStore();
        IdempotentBalanceView balances = new IdempotentBalanceView();
        Projector balanceProjector = new Projector(store, checkpoints, balances, 3);
        check("projector starts at position 0 with full lag", balanceProjector.position() == 0 && balanceProjector.lag() == 9);
        check("one batch applies batchSize events and checkpoints", balanceProjector.runOnce(clock.now()) == 3 && balanceProjector.position() == 3);
        check("catchUp drains the rest", balanceProjector.catchUp(clock.now()) == 6 && balanceProjector.caughtUp());
        check("balances match the fold of the log", balances.balance("a").equals(usd(60_000)) && balances.balance("b").equals(usd(35_000)));
        check("read model carries data the write model does not keep (owner)", "Alice".equals(balances.owner("a")));

        DailyActivityView daily = new DailyActivityView(Currency.USD);
        new Projector(store, checkpoints, daily, 100).catchUp(clock.now());
        long day0 = DailyActivityView.dayOf(1_700_000_000_000L);
        check("daily view groups by recordedAt day", daily.day(day0).deposits() == 2 && daily.day(day0).withdrawals() == 0 && daily.day(day0 + 1).withdrawals() == 3);
        check("daily net is money arithmetic, not floats", daily.day(day0 + 1).net().equals(usd(-235_000)));

        LargeWithdrawalsView v1 = new LargeWithdrawalsView(1, usd(100_000));
        new Projector(store, checkpoints, v1, 100).catchUp(clock.now());
        check("v1 alerts only at or above 1000.00 and has no owner", v1.alerts().size() == 1 && v1.alerts().get(0).owner() == null);

        // versioning and promotion
        ReadSide readSide = new ReadSide();
        Projector liveLarge = new Projector(store, checkpoints, v1, 100);
        readSide.register(liveLarge);
        Projector v2 = new Projector(store, checkpoints, new LargeWithdrawalsView(2, usd(50_000)), 4);
        readSide.register(v2);
        check("v2 has its own checkpoint starting at 0", v2.position() == 0 && readSide.live("large-withdrawals") == liveLarge);
        v2.runOnce(clock.now());
        checkThrows("promotion refused while behind", IllegalStateException.class, () -> readSide.promote("large-withdrawals"));
        v2.catchUp(clock.now());
        Projector retired = readSide.promote("large-withdrawals");
        check("promotion swaps live and returns the retired projector", readSide.live("large-withdrawals") == v2 && retired == liveLarge);
        LargeWithdrawalsView v2View = (LargeWithdrawalsView) v2.projection();
        check("v2 sees three alerts with owners", v2View.alerts().size() == 3 && "Bob".equals(v2View.alerts().get(1).owner()));
        check("retired version's checkpoint is untouched", retired.position() == 9);
        checkThrows("promote with nothing building", IllegalStateException.class, () -> readSide.promote("large-withdrawals"));
        checkThrows("checkpoint never moves backwards", IllegalStateException.class, () -> checkpoints.save(v1, 1));

        // eventual consistency and read-your-writes
        readSide.register(balanceProjector);
        long pos = handler.handle("a", new Deposit(usd(1_000), "late"));
        check("a new write leaves the projection behind", balanceProjector.lag() == 1 && balances.balance("a").equals(usd(60_000)));
        check("waitFor catches the projection up to the write", readSide.waitFor("balances", pos, clock.now(), 5) && balances.balance("a").equals(usd(61_000)));
        check("waitFor with zero runs reports not caught up when behind", readSide.waitFor("balances", pos + 1, clock.now(), 0) == false);

        // crash between apply and checkpoint
        BalanceView naive = new BalanceView();
        Projector naiveProjector = new Projector(store, new CheckpointStore(), naive, 4);
        naiveProjector.runOnce(clock.now());
        naiveProjector.crashAfterApplies(2);
        checkThrows("crash is surfaced", CrashException.class, () -> naiveProjector.runOnce(clock.now()));
        check("crash left the checkpoint where it was", naiveProjector.position() == 4);
        naiveProjector.catchUp(clock.now());
        check("naive projection double-applies redelivered withdrawals", naive.balance("a").equals(usd(61_000 - 120_000)));
        IdempotentBalanceView safe = new IdempotentBalanceView();
        Projector safeProjector = new Projector(store, new CheckpointStore(), safe, 4);
        safeProjector.runOnce(clock.now());
        safeProjector.crashAfterApplies(2);
        try { safeProjector.runOnce(clock.now()); } catch (CrashException ignored) { }
        safeProjector.catchUp(clock.now());
        check("idempotent projection ignores redelivery", safe.balance("a").equals(usd(61_000)) && safe.skipped() == 2);

        // consistency window
        ConsistencyProbe.Window slow = ConsistencyProbe.run(100, 7, 50, 100);
        ConsistencyProbe.Window fast = ConsistencyProbe.run(100, 7, 10, 100);
        check("probe covers every event", slow.events() == 100 && fast.events() == 100);
        check("staleness is bounded by the poll interval when the projector keeps up", slow.max() <= 50 && fast.max() <= 10);
        check("shorter polling shrinks the window", fast.mean() < slow.mean());
        ConsistencyProbe.Window overloaded = ConsistencyProbe.run(100, 7, 10, 1);
        check("a projector slower than the producer falls behind without bound", overloaded.max() > 100);
        checkThrows("batchSize must be positive", IllegalArgumentException.class, () -> new Projector(store, checkpoints, naive, 0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
