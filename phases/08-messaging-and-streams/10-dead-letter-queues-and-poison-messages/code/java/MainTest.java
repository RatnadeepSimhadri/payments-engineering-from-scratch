// Tests for phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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

    /** A handler that fails transiently a fixed number of times per offset, then succeeds. Fully deterministic. */
    static final class CountingHandler implements Handler {
        final Map<Long, Integer> failuresLeft;
        final List<Long> handled = new ArrayList<>();

        CountingHandler(Map<Long, Integer> failuresLeft) { this.failuresLeft = new java.util.HashMap<>(failuresLeft); }

        @Override public void handle(Event e) {
            int left = failuresLeft.getOrDefault(e.offset(), 0);
            if (left > 0) {
                failuresLeft.put(e.offset(), left - 1);
                throw new HandlerException(FailureKind.TRANSIENT, "transient #" + left);
            }
            handled.add(e.offset());
        }
    }

    static Event ev(long offset, String key, String amount, String ccy) { return new Event(offset, key, amount, ccy); }

    public static void main(String[] args) {
        List<Event> partition = Main.partition();

        // Stop-the-world: head-of-line blocking
        var naive = new StopTheWorldConsumer(new LedgerPostingHandler(new FaultInjector(1, 0.0)), 3);
        naive.consume(partition);
        check("stop-the-world stalls at the poison offset", naive.stuckOn() != null && naive.stuckOn().offset() == 2);
        check("stop-the-world processes nothing past the poison", naive.processed() == 2 && naive.committedOffset() == 2);

        // Resilient consumer with no transient faults: only poison and quarantine
        var clock = new SimClock();
        var handler = new LedgerPostingHandler(new FaultInjector(1, 0.0));
        var dlq = new DeadLetterQueue();
        var consumer = new ResilientConsumer(handler, dlq, clock, 3, 100);
        consumer.consume(partition);
        ConsumerStats s = consumer.stats();
        check("offset commits past the poison message", s.committedOffset() == 10);
        check("every event is processed or parked", s.processed() + s.parked() == partition.size());
        check("poison message parked as PERMANENT_FAILURE on first attempt",
                dlq.all().stream().anyMatch(d -> d.event().offset() == 2 && d.reason() == ParkReason.PERMANENT_FAILURE && d.attempts() == 1));
        check("unknown currency parked as PERMANENT_FAILURE",
                dlq.all().stream().anyMatch(d -> d.event().offset() == 6 && d.reason() == ParkReason.PERMANENT_FAILURE));
        check("permanent failures are never retried", s.retries() == 0 && clock.now() == 0);
        check("later events for the poisoned key are quarantined, not applied",
                dlq.all().stream().filter(d -> d.event().accountKey().equals("acct-A") && d.reason() == ParkReason.KEY_QUARANTINED).count() == 2);
        check("quarantined followers never reach the handler: acct-A balance is only offset 0", handler.balance("acct-A").minorUnits() == 10000);
        check("other keys keep flowing: acct-B fully posted", handler.balance("acct-B").minorUnits() == 3975);
        check("other keys keep flowing: acct-C fully posted in JPY", handler.balance("acct-C").equals(new Money(1500, Currency.JPY)));
        check("quarantine is per key", consumer.isQuarantined("acct-A") && consumer.isQuarantined("acct-D") && !consumer.isQuarantined("acct-B"));
        check("dead letter records the cause offset", dlq.forKey("acct-A").get(1).lastError().contains("offset 2"));

        // Replay with a correction: in offset order, key leaves quarantine, balance is exact
        int replayed = consumer.replay("acct-A", Map.of(2L, ev(2, "acct-A", "12.34", "USD")));
        check("replay applies all three parked acct-A events", replayed == 3);
        check("replay restores per-key order: 100.00 + 12.34 - 40.00 + 5.00", handler.balance("acct-A").minorUnits() == 7734);
        check("replayed key leaves quarantine", !consumer.isQuarantined("acct-A"));
        check("DLQ keeps only the unfixed key", dlq.size() == 1 && dlq.all().get(0).event().accountKey().equals("acct-D"));
        check("stats reflect the replay", consumer.stats().processed() == 9 && consumer.stats().parked() == 1);

        // Replay without a correction fails again and re-parks the followers in order
        int replayedD = consumer.replay("acct-D", Map.of());
        check("replay of an unfixed poison message replays nothing", replayedD == 0);
        check("unfixed key is quarantined again", consumer.isQuarantined("acct-D") && dlq.forKey("acct-D").size() == 1);

        // Idempotent handler: replaying an applied offset does not double-post
        handler.handle(ev(0, "acct-A", "100.00", "USD"));
        check("handler is idempotent per offset", handler.balance("acct-A").minorUnits() == 7734);

        // Transient failures: retried with exponential backoff on the simulated clock, then succeed
        var clock2 = new SimClock();
        var counting = new CountingHandler(Map.of(1L, 2));          // offset 1 fails twice, then succeeds
        var dlq2 = new DeadLetterQueue();
        var c2 = new ResilientConsumer(counting, dlq2, clock2, 3, 100);
        c2.consume(List.of(ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k1", "3.00", "USD")));
        check("transient failure within maxAttempts eventually succeeds", counting.handled.equals(List.of(0L, 1L, 2L)));
        check("two retries recorded", c2.stats().retries() == 2);
        check("backoff is exponential: 100 + 200 ms on the simulated clock", clock2.now() == 300);
        check("nothing parked when retries succeed", dlq2.size() == 0 && !c2.isQuarantined("k1"));

        // Transient failures beyond maxAttempts: parked as MAX_ATTEMPTS_EXCEEDED with the attempt count
        var clock3 = new SimClock();
        var counting3 = new CountingHandler(Map.of(1L, 5));          // offset 1 fails five times: more than maxAttempts
        var dlq3 = new DeadLetterQueue();
        var c3 = new ResilientConsumer(counting3, dlq3, clock3, 3, 100);
        c3.consume(List.of(ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k2", "3.00", "USD"), ev(3, "k1", "4.00", "USD")));
        check("exhausted retries park the event", dlq3.forKey("k1").get(0).reason() == ParkReason.MAX_ATTEMPTS_EXCEEDED);
        check("attempt count equals maxAttempts", dlq3.forKey("k1").get(0).attempts() == 3);
        check("parkedAt is the simulated clock after the backoffs", dlq3.forKey("k1").get(0).parkedAt() == 300);
        check("follower on the same key is quarantined", dlq3.forKey("k1").get(1).reason() == ParkReason.KEY_QUARANTINED);
        check("a different key is unaffected", counting3.handled.contains(2L));
        check("offset still commits to the end", c3.stats().committedOffset() == 4);

        // Replay after the transient cause clears: the remaining failures are consumed by retries
        counting3.failuresLeft.put(1L, 1);
        int replayed3 = c3.replay("k1", Map.of());
        check("replay after recovery applies both parked events in order", replayed3 == 2 && counting3.handled.equals(List.of(0L, 2L, 1L, 3L)));

        // Boundaries and misuse
        checkThrows("maxAttempts must be at least one", IllegalArgumentException.class,
                () -> new ResilientConsumer(counting, dlq2, clock2, 0, 100));
        checkThrows("clock cannot go backwards", IllegalArgumentException.class, () -> new SimClock().advance(-1));
        checkThrows("Money.parse rejects excess precision rather than rounding", ArithmeticException.class,
                () -> Money.parse("12.345", Currency.USD));
        checkThrows("Money.plus rejects mixed currencies", IllegalArgumentException.class,
                () -> new Money(1, Currency.USD).plus(new Money(1, Currency.JPY)));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
