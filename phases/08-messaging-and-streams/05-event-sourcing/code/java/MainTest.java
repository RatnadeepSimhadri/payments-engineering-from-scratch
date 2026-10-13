// Tests for phases/08-messaging-and-streams/05-event-sourcing. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Money usd(long cents) { return Money.ofMinor(cents, Currency.USD); }

    public static void main(String[] args) {
        // --- the fold -------------------------------------------------------------------------------------------
        List<Event> history = List.of(
                new Opened("a1", Currency.USD), new Deposited(usd(10_000)), new Held("h1", usd(2_500)),
                new Withdrawn(usd(6_000)), new Released("h1"));
        AccountState s = AccountState.replay(history);
        check("replay: version equals event count", s.version() == 5);
        check("replay: balance = deposits - withdrawals", s.balance().equals(usd(4_000)));
        check("replay: released hold no longer reduces available", s.available().equals(usd(4_000)) && s.holds().isEmpty());
        AccountState afterHold = AccountState.replay(history.subList(0, 3));
        check("hold reduces available but not balance", afterHold.balance().equals(usd(10_000)) && afterHold.available().equals(usd(7_500)));
        check("fold is incremental: replay(all) == replay(prefix).applyAll(suffix)",
                s.equals(afterHold.applyAll(history.subList(3, 5))));
        check("empty state is not open and has version 0", !AccountState.empty().isOpen() && AccountState.empty().version() == 0);

        // --- decide: invariants ----------------------------------------------------------------------------------
        checkThrows("withdraw beyond available is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new Withdraw(usd(7_501))));
        check("withdraw exactly the available amount is allowed", afterHold.decide(new Withdraw(usd(7_500))).size() == 1);
        checkThrows("hold beyond available is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new PlaceHold("h2", usd(7_501))));
        checkThrows("duplicate hold id is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new PlaceHold("h1", usd(1))));
        checkThrows("release of unknown hold is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new ReleaseHold("nope")));
        checkThrows("deposit of zero is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new Deposit(usd(0))));
        checkThrows("deposit of a negative amount is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new Deposit(usd(-1))));
        checkThrows("currency mismatch is rejected", CurrencyMismatchException.class,
                () -> afterHold.decide(new Deposit(Money.ofMinor(100, Currency.EUR))));
        checkThrows("command on an unopened account is rejected", CommandRejectedException.class,
                () -> AccountState.empty().decide(new Deposit(usd(1))));
        checkThrows("opening twice is rejected", CommandRejectedException.class,
                () -> afterHold.decide(new OpenAccount("a1", Currency.USD)));
        check("decide does not mutate state", afterHold.version() == 3 && afterHold.holds().size() == 1);

        // --- event store: optimistic concurrency ------------------------------------------------------------------
        EventStore store = new EventStore();
        check("new stream is at version 0", store.version("x") == 0);
        check("append from 0 returns 2", store.append("x", 0, List.of(new Opened("x", Currency.USD), new Deposited(usd(1)))) == 2);
        checkThrows("append with stale expected version fails", ConcurrencyException.class,
                () -> store.append("x", 1, List.of(new Deposited(usd(1)))));
        check("failed append wrote nothing", store.version("x") == 2 && store.totalEvents() == 2);
        check("read after version 1 returns the tail", store.read("x", 1).size() == 1 && store.read("x", 1).get(0) instanceof Deposited);
        check("read of unknown stream is empty", store.read("nope", 0).isEmpty());

        // --- service: handle, retry, snapshots --------------------------------------------------------------------
        SnapshotStore snaps = new SnapshotStore();
        AccountService svc = new AccountService(store, snaps, 3);
        svc.handle("acct", new OpenAccount("acct", Currency.USD));
        svc.handle("acct", new Deposit(usd(5_000)));
        long seen = store.version("acct");
        svc.handle("acct", new Withdraw(usd(1_000)), seen);
        checkThrows("second writer at the same expected version loses", ConcurrencyException.class,
                () -> svc.handle("acct", new Withdraw(usd(1_000)), seen));
        check("loser wrote no event", store.version("acct") == 3);
        AccountState retried = svc.handle("acct", new Withdraw(usd(1_000)));
        check("retry against the fresh version succeeds", retried.version() == 4 && retried.balance().equals(usd(3_000)));
        check("snapshot taken at version 3", snaps.writes() == 1 && snaps.load("acct").get().version() == 3);
        AccountState loaded = svc.load("acct");
        check("load replays only the tail after the snapshot", svc.eventsReplayedOnLastLoad() == 1 && loaded.version() == 4);
        check("snapshot load equals full replay", loaded.equals(svc.replayFromScratch("acct")));
        svc.handle("acct", new Deposit(usd(1)));
        svc.handle("acct", new Deposit(usd(1)));
        check("snapshot taken again at version 6", snaps.writes() == 2 && snaps.load("acct").get().version() == 6);
        long before = store.version("acct");
        checkThrows("rejected command appends nothing", CommandRejectedException.class,
                () -> svc.handle("acct", new Withdraw(usd(999_999))));
        check("version unchanged after rejection", store.version("acct") == before);
        checkThrows("snapshotEvery must be positive", IllegalArgumentException.class,
                () -> new AccountService(store, snaps, 0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
