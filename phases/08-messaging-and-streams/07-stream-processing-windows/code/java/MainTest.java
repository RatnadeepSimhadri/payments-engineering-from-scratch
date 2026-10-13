// Tests for phases/08-messaging-and-streams/07-stream-processing-windows. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();
    static final long SEC = 1_000, MIN = 60_000;

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

    static Txn txn(String card, long cents, long time) { return new Txn(card, Money.ofMinor(cents, Currency.USD), time); }

    static WindowResult find(List<WindowResult> results, String key, long start, long end) {
        for (WindowResult r : results) if (r.key().equals(key) && r.window().start() == start && r.window().end() == end) return r;
        return null;
    }

    public static void main(String[] args) {
        // assigners
        TumblingWindows tumbling = new TumblingWindows(MIN);
        check("tumbling: timestamp at window start belongs to that window", tumbling.assign(MIN).equals(List.of(new Window(MIN, 2 * MIN))));
        check("tumbling: last millisecond belongs to the same window as the start", tumbling.assign(2 * MIN - 1).equals(List.of(new Window(MIN, 2 * MIN))));
        check("tumbling: exactly one window per timestamp", tumbling.assign(12_345).size() == 1 && tumbling.assign(12_345).get(0).contains(12_345));
        SlidingWindows sliding = new SlidingWindows(MIN, 10 * SEC);
        List<Window> at63 = sliding.assign(63 * SEC);
        check("sliding: size/slide windows per timestamp", at63.size() == 6);
        check("sliding: every assigned window contains the timestamp", at63.stream().allMatch(w -> w.contains(63 * SEC)));
        check("sliding: windows are aligned to the slide and ordered", at63.get(0).equals(new Window(10 * SEC, 70 * SEC)) && at63.get(5).equals(new Window(60 * SEC, 120 * SEC)));
        check("sliding: a timestamp exactly on a slide boundary is in 6 windows too", sliding.assign(60 * SEC).size() == 6 && sliding.assign(60 * SEC).get(5).start() == 60 * SEC);
        checkThrows("sliding: slide larger than size rejected", IllegalArgumentException.class, () -> new SlidingWindows(MIN, 2 * MIN));
        checkThrows("sliding: size must be a multiple of slide", IllegalArgumentException.class, () -> new SlidingWindows(MIN, 7 * SEC));
        checkThrows("tumbling: size must be positive", IllegalArgumentException.class, () -> new TumblingWindows(0));
        checkThrows("window: zero length rejected", IllegalArgumentException.class, () -> new Window(5, 5));

        // aggregate arithmetic is money arithmetic
        Aggregate a = Aggregate.empty(Currency.USD).add(Money.ofMinor(1_299, Currency.USD)).add(Money.ofMinor(2_450, Currency.USD));
        check("aggregate: count, sum, max", a.count() == 2 && a.sum().minorUnits() == 3_749 && a.max().minorUnits() == 2_450);
        check("aggregate: merge is associative in effect", a.merge(Aggregate.empty(Currency.USD).add(Money.ofMinor(1, Currency.USD))).count() == 3);

        // tumbling operator: boundary problem and trigger timing
        List<Txn> burst = List.of(txn("A", 100, 50 * SEC), txn("A", 100, 56 * SEC), txn("A", 100, 63 * SEC), txn("A", 100, 69 * SEC));
        WindowedAggregator tumblingAgg = new WindowedAggregator(tumbling, Currency.USD);
        List<WindowResult> tumblingOut = new StreamRunner(tumblingAgg, 10 * SEC).run(burst, 3 * MIN);
        check("tumbling: a 19-second burst across a boundary is split 2 and 2", find(tumblingOut, "A", 0, MIN).aggregate().count() == 2 && find(tumblingOut, "A", MIN, 2 * MIN).aggregate().count() == 2);
        check("tumbling: windows are emitted when the timer passes their end", find(tumblingOut, "A", 0, MIN).emittedAt() == MIN && find(tumblingOut, "A", MIN, 2 * MIN).emittedAt() == 2 * MIN);
        check("tumbling: emitted windows are dropped from state", tumblingAgg.stateSize() == 0);
        check("tumbling: velocity rule (> 3) misses the burst", new VelocityRule(3).breaches(tumblingOut).isEmpty());

        WindowedAggregator direct = new WindowedAggregator(tumbling, Currency.USD);
        direct.process(txn("A", 100, 50 * SEC));
        check("not emitted before the window end", direct.onTimer(MIN - 1).isEmpty() && direct.peek("A", new Window(0, MIN)).count() == 1);
        check("emitted exactly at the window end", direct.onTimer(MIN).size() == 1);
        check("a window is emitted once", direct.onTimer(MIN).isEmpty() && direct.onTimer(10 * MIN).isEmpty());
        checkThrows("processing time cannot go backwards", IllegalArgumentException.class, () -> direct.process(txn("A", 1, 30 * SEC)));

        // sliding operator catches the burst, at the price of state
        WindowedAggregator slidingAgg = new WindowedAggregator(sliding, Currency.USD);
        StreamRunner slidingRunner = new StreamRunner(slidingAgg, 10 * SEC);
        List<WindowResult> slidingOut = slidingRunner.run(burst, 3 * MIN);
        List<WindowResult> breaches = new VelocityRule(3).breaches(slidingOut);
        check("sliding: the burst is caught", !breaches.isEmpty() && breaches.stream().allMatch(r -> r.aggregate().count() == 4));
        check("sliding: every window that fully contains 0:50..1:09 reports 4", find(slidingOut, "A", 10 * SEC, 70 * SEC).aggregate().count() == 4 && find(slidingOut, "A", 50 * SEC, 110 * SEC).aggregate().count() == 4);
        check("sliding: the edge windows that hold only half the burst report 2", find(slidingOut, "A", 0, MIN).aggregate().count() == 2 && find(slidingOut, "A", 60 * SEC, 120 * SEC).aggregate().count() == 2);
        check("sliding: the same burst is reported in 5 windows", breaches.size() == 5);
        check("sliding: peak state is larger than tumbling's", slidingRunner.maxStateSize() > 1 && slidingRunner.maxStateSize() <= 6 + 6);
        check("sliding: sum is identical in every full window", breaches.stream().allMatch(r -> r.aggregate().sum().minorUnits() == 400));

        // session operator
        SessionAggregator session = new SessionAggregator(30 * SEC, Currency.USD);
        session.process(txn("C", 500, 0));
        check("session: first record opens [t, t+gap)", session.openSessions("C").equals(List.of(new Window(0, 30 * SEC))));
        session.process(txn("C", 500, 25 * SEC));
        check("session: a record inside the gap extends the session", session.openSessions("C").equals(List.of(new Window(0, 55 * SEC))));
        session.process(txn("C", 500, 55 * SEC));
        check("session: a record exactly at the end starts a new session (half-open)", session.openSessions("C").size() == 2);
        check("session: nothing closes before last+gap", session.onTimer(54 * SEC).isEmpty());
        List<WindowResult> closed = session.onTimer(55 * SEC);
        check("session: closes when now >= last record + gap, with the gap included in the window", closed.size() == 1 && closed.get(0).window().equals(new Window(0, 55 * SEC)) && closed.get(0).aggregate().count() == 2);
        check("session: the open one remains", session.stateSize() == 1);
        checkThrows("session: gap must be positive", IllegalArgumentException.class, () -> new SessionAggregator(0, Currency.USD));

        List<Txn> mixed = new ArrayList<>(burst);
        for (int m = 0; m < 3; m++) mixed.add(txn("B", 4_000, m * MIN + 30 * SEC));
        mixed.sort((x, y) -> Long.compare(x.time(), y.time()));
        List<WindowResult> sessionOut = new StreamRunner(new SessionAggregator(30 * SEC, Currency.USD), 10 * SEC).run(mixed, 5 * MIN);
        check("session: the burst is one session of 4", sessionOut.stream().filter(r -> r.key().equals("A")).count() == 1 && sessionOut.stream().filter(r -> r.key().equals("A")).findFirst().get().aggregate().count() == 4);
        check("session: steady one-a-minute traffic is one session per record", sessionOut.stream().filter(r -> r.key().equals("B")).count() == 3);
        check("session: a session window spans first record to last record + gap", sessionOut.stream().filter(r -> r.key().equals("A")).findFirst().get().window().equals(new Window(50 * SEC, 99 * SEC)));

        // runner invariants
        checkThrows("runner: timer interval must be positive", IllegalArgumentException.class, () -> new StreamRunner(tumblingAgg, 0));
        List<WindowResult> coarse = new StreamRunner(new WindowedAggregator(new TumblingWindows(MIN), Currency.USD), 45 * SEC).run(burst, 3 * MIN);
        check("runner: a coarse timer delays emission to the next tick after the window end", find(coarse, "A", 0, MIN).emittedAt() == 90 * SEC);
        check("runner: results cover every record exactly once under tumbling", tumblingOut.stream().mapToInt(r -> r.aggregate().count()).sum() == 4);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
