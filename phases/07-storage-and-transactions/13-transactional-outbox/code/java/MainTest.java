// Tests for phases/07-storage-and-transactions/13-transactional-outbox. Run: javac -d .out *.java && java -cp .out MainTest
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

    /** A fresh world: one store, one broker, an outbox service, a relay with the given batch size, a ledger consumer. */
    static final class World {
        final SimClock clock = new SimClock(0);
        final Store store = new Store();
        final Broker broker = new Broker();
        final OutboxPaymentService svc = new OutboxPaymentService(store, clock);
        final OutboxRelay relay;
        final LedgerConsumer ledger = new LedgerConsumer(broker);
        World(int batchSize) { relay = new OutboxRelay(store, broker, batchSize); }
        Payment pay(String id, String merchant, String amount) { return Main.payment(id, merchant, amount, clock); }
    }

    public static void main(String[] args) {
        { // the store's transaction is atomic: both rows or neither
            Store store = new Store();
            Store.Tx tx = store.begin().insertPayment(new Payment("p1", "m", Main.usd("1.00"), 0))
                    .insertOutbox("evt-p1", "m", "PaymentAccepted", "x", 0);
            check("nothing visible before commit", store.paymentCount() == 0 && store.outboxRows().isEmpty());
            tx.commit();
            check("both rows visible after commit", store.paymentCount() == 1 && store.outboxRows().size() == 1);
            check("outbox seq assigned at commit", store.outboxRows().get(0).seq() == 1 && !store.outboxRows().get(0).published());
            checkThrows("closed transaction cannot be reused", IllegalStateException.class, tx::commit);
            Store.Tx tx2 = store.begin().insertPayment(new Payment("p2", "m", Main.usd("2.00"), 0))
                    .insertOutbox("evt-p2", "m", "PaymentAccepted", "y", 0);
            tx2.rollback();
            check("rollback drops payment and outbox row together", store.paymentCount() == 1 && store.outboxRows().size() == 1 && store.rollbacks == 1);
            checkThrows("duplicate payment id is rejected at insert", IllegalStateException.class,
                    () -> store.begin().insertPayment(new Payment("p1", "m", Main.usd("1.00"), 0)));
        }
        { // dual write, commit then publish: orphan row
            SimClock clock = new SimClock(0);
            Store store = new Store();
            Broker broker = new Broker();
            DualWritePaymentService svc = new DualWritePaymentService(store, broker);
            broker.down = true;
            checkThrows("publish fails when the broker is down", BrokerUnavailableException.class,
                    () -> svc.accept(Main.payment("d1", "acme", "20.00", clock)));
            check("row committed without an event", store.paymentCount() == 1 && broker.size() == 0);
            broker.down = false;
            svc.crashPoint = CrashPoint.AFTER_COMMIT_BEFORE_PUBLISH;
            checkThrows("crash after commit", CrashException.class, () -> svc.accept(Main.payment("d2", "acme", "5.00", clock)));
            check("second orphan row, still no events", store.paymentCount() == 2 && broker.size() == 0);
            LedgerConsumer ledger = new LedgerConsumer(broker);
            ledger.consume();
            check("ledger disagrees with store forever", ledger.balance("acme").equals(Main.usd("0.00")));
        }
        { // dual write, publish then commit: ghost event
            SimClock clock = new SimClock(0);
            Store store = new Store();
            Broker broker = new Broker();
            DualWritePaymentService svc = new DualWritePaymentService(store, broker);
            svc.publishFirst = true;
            svc.crashPoint = CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT;
            checkThrows("crash after publish", CrashException.class, () -> svc.accept(Main.payment("g1", "acme", "12.50", clock)));
            LedgerConsumer ledger = new LedgerConsumer(broker);
            ledger.consume();
            check("event without a row", store.paymentCount() == 0 && broker.size() == 1);
            check("ledger credits a payment that does not exist", ledger.balance("acme").equals(Main.usd("12.50")));
        }
        { // outbox: broker outage at write time loses nothing
            World w = new World(10);
            w.broker.down = true;
            w.svc.accept(w.pay("o1", "acme", "20.00"));
            w.svc.accept(w.pay("o2", "acme", "35.00"));
            check("writes succeed while broker is down", w.store.paymentCount() == 2 && w.store.unpublishedCount() == 2);
            check("service never touched the broker", w.broker.publishAttempts == 0);
            check("relay delivers nothing while broker is down", w.relay.drain(5) == 0 && w.relay.failedAttempts >= 1);
            check("rows stay pending after a failed poll", w.store.unpublishedCount() == 2);
            w.broker.down = false;
            check("relay delivers both once broker is back", w.relay.drain(5) == 2 && w.store.unpublishedCount() == 0);
            w.ledger.consume();
            check("ledger matches store", w.ledger.balance("acme").equals(Main.usd("55.00")));
            check("relay with nothing pending is a no-op", w.relay.poll() == 0);
        }
        { // relay crash between publish and mark: duplicate on the broker, deduped by the consumer
            World w = new World(10);
            w.svc.accept(w.pay("r1", "globex", "100.00"));
            w.svc.accept(w.pay("r2", "globex", "1.00"));
            w.relay.crashPoint = CrashPoint.AFTER_PUBLISH_BEFORE_MARK;
            checkThrows("relay crashes after first publish", CrashException.class, w.relay::poll);
            check("published but not marked", w.broker.size() == 1 && w.store.unpublishedCount() == 2);
            w.relay.drain(5);
            check("at-least-once: three messages for two events", w.broker.size() == 3 && w.store.unpublishedCount() == 0);
            check("the duplicate carries the same eventId", w.broker.messages().get(0).eventId().equals(w.broker.messages().get(1).eventId()));
            int applied = w.ledger.consume();
            check("consumer applies each eventId once", applied == 2 && w.ledger.duplicatesIgnored == 1);
            check("balance is not double-counted", w.ledger.balance("globex").equals(Main.usd("101.00")));
            check("consumer offset advanced past every message", w.ledger.offset() == 3);
            check("re-consuming applies nothing new", w.ledger.consume() == 0 && w.ledger.balance("globex").equals(Main.usd("101.00")));
        }
        { // ordering: seq is commit order, relay honours it across batches
            World w = new World(2);
            for (int i = 1; i <= 5; i++) w.svc.accept(w.pay("s" + i, "acme", i + ".00"));
            int polls = 0;
            while (w.store.unpublishedCount() > 0) { w.relay.poll(); polls++; }
            check("batch size 2 needs three polls for five rows", polls == 3);
            StringBuilder order = new StringBuilder();
            for (Message m : w.broker.messages()) order.append(m.eventId()).append(',');
            check("broker order equals commit order", order.toString().equals("evt-s1,evt-s2,evt-s3,evt-s4,evt-s5,"));
            check("unpublished query respects the limit", new World(10).store.unpublished(3).isEmpty());
            World w2 = new World(10);
            for (int i = 1; i <= 5; i++) w2.svc.accept(w2.pay("t" + i, "acme", "1.00"));
            check("unpublished returns at most limit rows in seq order", w2.store.unpublished(3).size() == 3 && w2.store.unpublished(3).get(2).seq() == 3);
        }
        { // broker failure mid-batch stops the batch so later rows cannot overtake earlier ones
            World w = new World(10);
            w.svc.accept(w.pay("b1", "acme", "1.00"));
            w.svc.accept(w.pay("b2", "acme", "2.00"));
            w.broker.down = true;
            check("poll publishes nothing when the first publish fails", w.relay.poll() == 0 && w.store.unpublishedCount() == 2);
            w.broker.down = false;
            w.relay.poll();
            check("after recovery b1 precedes b2", w.broker.messages().get(0).eventId().equals("evt-b1") && w.broker.messages().get(1).eventId().equals("evt-b2"));
        }
        { // rollback path in the service
            World w = new World(10);
            w.svc.acceptThenFail(w.pay("x1", "acme", "999.00"));
            check("failed request leaves no payment and no outbox row", w.store.paymentCount() == 0 && w.store.outboxRows().isEmpty());
            check("relay has nothing to publish for a rolled-back request", w.relay.poll() == 0 && w.broker.size() == 0);
        }
        { // money discipline and payload round trip
            check("payload carries the exact amount", Events.payloadFor(new Payment("p", "m", Main.usd("0.05"), 0)).endsWith("0.05 USD"));
            check("parseMoney round-trips minor units", LedgerConsumer.parseMoney("1234.56 USD").minorUnits() == 123456);
            checkThrows("currency mismatch is rejected", CurrencyMismatchException.class,
                    () -> Main.usd("1.00").plus(Money.ofMinor(100, new Currency("EUR", 2))));
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
