// Tests for phases/08-messaging-and-streams/09-backpressure. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Stats overloaded(Strategy s) {
        // ~12/tick in, 10/tick out with 10% stalls, capacity 64, 2000 ticks, seed 42: the demo's load
        return new FlowSimulation(s, 10, 4, 10, 0.10, 64, 42).run(2000);
    }

    static Stats comfortable(Strategy s) {
        return new FlowSimulation(s, 10, 4, 13, 0.0, 64, 42).run(2000);
    }

    public static void main(String[] args) {
        // BoundedQueue: capacity and the three overflow policies
        var block = new BoundedQueue<String>(2, OverflowPolicy.BLOCK);
        check("offer accepted below capacity", block.offer("a") == OfferResult.ACCEPTED && block.offer("b") == OfferResult.ACCEPTED);
        check("queue reports full", block.isFull());
        check("BLOCK refuses when full", block.offer("c") == OfferResult.REJECTED_FULL);
        check("BLOCK does not count a refusal as a drop", block.dropped() == 0 && block.size() == 2);
        check("BLOCK preserves FIFO order", "a".equals(block.poll()) && "b".equals(block.poll()) && block.poll() == null);

        var newest = new BoundedQueue<String>(2, OverflowPolicy.DROP_NEWEST);
        newest.offer("a"); newest.offer("b");
        check("DROP_NEWEST discards the offered item", newest.offer("c") == OfferResult.DROPPED_NEWEST);
        check("DROP_NEWEST keeps the old items", newest.dropped() == 1 && "a".equals(newest.poll()) && "b".equals(newest.poll()));

        var oldest = new BoundedQueue<String>(2, OverflowPolicy.DROP_OLDEST);
        oldest.offer("a"); oldest.offer("b");
        check("DROP_OLDEST evicts the head", oldest.offer("c") == OfferResult.EVICTED_OLDEST);
        check("DROP_OLDEST keeps the newest items", oldest.dropped() == 1 && "b".equals(oldest.poll()) && "c".equals(oldest.poll()));
        check("maxDepth never exceeds capacity", oldest.maxDepth() == 2);
        checkThrows("zero capacity rejected", IllegalArgumentException.class, () -> new BoundedQueue<String>(0, OverflowPolicy.BLOCK));
        checkThrows("null item rejected", NullPointerException.class, () -> block.offer(null));

        // DemandSubscription: Reactive Streams request(n) semantics
        var sub = new DemandSubscription();
        check("no demand before request", !sub.hasDemand() && !sub.tryConsumeOne());
        sub.request(2);
        check("demand is additive", sub.outstanding() == 2);
        sub.request(3);
        check("demand accumulates", sub.outstanding() == 5);
        for (int i = 0; i < 5; i++) sub.tryConsumeOne();
        check("demand is consumed one emission at a time", !sub.hasDemand() && !sub.tryConsumeOne());
        checkThrows("request(0) is a spec violation", IllegalArgumentException.class, () -> sub.request(0));
        checkThrows("request(-1) is a spec violation", IllegalArgumentException.class, () -> sub.request(-1));
        sub.request(Long.MAX_VALUE);
        sub.request(1);
        check("demand saturates at Long.MAX_VALUE instead of wrapping (rule 3.17)", sub.outstanding() == Long.MAX_VALUE);
        sub.tryConsumeOne();
        check("unbounded demand stays unbounded", sub.outstanding() == Long.MAX_VALUE);

        // Simulation under sustained overload: every strategy conserves events
        Stats unbounded = overloaded(Strategy.UNBOUNDED);
        Stats dropNewest = overloaded(Strategy.DROP_NEWEST);
        Stats dropOldest = overloaded(Strategy.DROP_OLDEST);
        Stats blocked = overloaded(Strategy.BLOCK);
        Stats pulled = overloaded(Strategy.PULL);
        for (Stats s : List.of(unbounded, dropNewest, dropOldest, blocked, pulled)) {
            check("conservation holds for " + s.strategy(), s.conserves());
        }
        check("identical seeded load across strategies",
                unbounded.offeredLoad() == blocked.offeredLoad() && unbounded.offeredLoad() == pulled.offeredLoad());

        // UNBOUNDED: nothing lost, nothing throttled, but memory and latency are unbounded
        check("UNBOUNDED drops nothing and throttles nothing", unbounded.dropped() == 0 && unbounded.throttled() == 0);
        check("UNBOUNDED queue depth grows far past the bounded capacity", unbounded.maxQueueDepth() > 64 * 10);
        check("UNBOUNDED latency grows with the backlog", unbounded.maxLatencyTicks() > 100);
        check("UNBOUNDED backlog is the undelivered events", unbounded.inQueue() == unbounded.produced() - unbounded.delivered());

        // DROP_*: bounded memory and latency, but payment events are destroyed
        check("DROP_NEWEST bounds queue depth", dropNewest.maxQueueDepth() == 64);
        check("DROP_NEWEST loses events", dropNewest.dropped() > 0 && dropNewest.throttled() == 0);
        check("DROP_OLDEST loses the same number of events", dropOldest.dropped() == dropNewest.dropped());
        check("DROP_OLDEST has lower latency than DROP_NEWEST (fresh data wins)", dropOldest.maxLatencyTicks() < dropNewest.maxLatencyTicks());

        // BLOCK and PULL: bounded memory, bounded latency, nothing lost; overload moves to the producer
        check("BLOCK drops nothing", blocked.dropped() == 0);
        check("BLOCK bounds queue depth", blocked.maxQueueDepth() == 64);
        check("BLOCK bounds latency", blocked.maxLatencyTicks() <= 64 / 10 * 3);
        check("BLOCK throttles upstream requests instead", blocked.throttled() > 0);
        check("BLOCK discovers overload by collision", blocked.rejectedOffers() > 0);
        check("PULL drops nothing", pulled.dropped() == 0);
        check("PULL bounds queue depth to the initial demand", pulled.maxQueueDepth() == 64);
        check("PULL never offers into a full queue", pulled.rejectedOffers() == 0);
        check("PULL throttles the same load BLOCK does, give or take the in-flight item", Math.abs(pulled.throttled() - blocked.throttled()) <= 1);
        check("every strategy delivers the same number of events (the consumer is the bottleneck)",
                blocked.delivered() == unbounded.delivered() && pulled.delivered() == unbounded.delivered());
        check("Money sums stay exact under load", blocked.deliveredAmount().currency().equals(Currency.USD) && blocked.deliveredAmount().minorUnits() > 0);

        // When the consumer keeps up, backpressure costs nothing
        for (Strategy s : Strategy.values()) {
            Stats c = comfortable(s);
            check("comfortable load: " + s + " delivers everything", c.delivered() == c.offeredLoad() && c.dropped() == 0 && c.throttled() == 0);
        }

        // Determinism: same seed, same answer
        check("simulation is deterministic", overloaded(Strategy.BLOCK).equals(blocked));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
