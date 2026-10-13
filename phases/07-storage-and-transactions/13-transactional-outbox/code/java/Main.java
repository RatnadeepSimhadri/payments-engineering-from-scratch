// Lesson: phases/07-storage-and-transactions/13-transactional-outbox/docs/en.md
// The Transactional Outbox and Dual-Write Problem
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Richardson, "Microservices Patterns" ch. 3 (transactional outbox); Debezium outbox event router docs;
//          Helland, "Life beyond Distributed Transactions" (CIDR 2007). Money is integer minor units (Phase 10, Lesson 01).

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

/** ISO 4217 currency, copied from Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount of one currency in signed minor units. Copied from Phase 10 Lesson 01. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

/** A clock that only moves when the simulation says so. */
final class SimClock {
    private long nowMs;
    SimClock(long startMs) { this.nowMs = startMs; }
    long now() { return nowMs; }
    void advance(long ms) { nowMs += ms; }
}

/** Thrown at an injected crash point. The simulation catches it where the real process would die. */
final class CrashException extends RuntimeException {
    CrashException(String where) { super("crash " + where); }
}

/** The broker refused or lost a publish. Transient by assumption: the caller retries. */
final class BrokerUnavailableException extends RuntimeException {
    BrokerUnavailableException() { super("broker unavailable"); }
}

/** The business row. A payment that was accepted and must be reflected in the ledger. */
record Payment(String paymentId, String merchant, Money amount, long createdAtMs) { }

/**
 * One outbox row. `seq` is the store's insertion order (the primary key); `eventId` is the identity the consumer
 * deduplicates on; `aggregateId` is the partition key downstream. `published` flips only when the relay confirms.
 */
record OutboxEvent(long seq, String eventId, String aggregateId, String type, String payload, long createdAtMs, boolean published) {
    OutboxEvent markPublished() { return new OutboxEvent(seq, eventId, aggregateId, type, payload, createdAtMs, true); }
    @Override public String toString() { return "#" + seq + " " + eventId + " " + type + " " + payload + (published ? " [published]" : " [pending]"); }
}

/**
 * An in-memory store with real transactions: writes are buffered in the Tx and applied to the tables only on
 * commit, all of them or none. This is the one property the outbox pattern relies on: the payment row and the
 * outbox row live in the same store, so one commit covers both.
 */
final class Store {
    private final Map<String, Payment> payments = new LinkedHashMap<>();
    private final Map<Long, OutboxEvent> outbox = new LinkedHashMap<>();
    private long nextSeq = 1;
    int commits = 0, rollbacks = 0;

    final class Tx {
        private final List<Payment> pendingPayments = new ArrayList<>();
        private final List<OutboxEvent> pendingEvents = new ArrayList<>();
        private final List<Long> pendingMarks = new ArrayList<>();
        private boolean open = true;

        private void requireOpen() { if (!open) throw new IllegalStateException("transaction already closed"); }

        Tx insertPayment(Payment p) {
            requireOpen();
            if (payments.containsKey(p.paymentId())) throw new IllegalStateException("duplicate payment " + p.paymentId());
            pendingPayments.add(p);
            return this;
        }

        /** Buffers an outbox row. The sequence number is assigned at commit so commit order equals seq order. */
        Tx insertOutbox(String eventId, String aggregateId, String type, String payload, long atMs) {
            requireOpen();
            pendingEvents.add(new OutboxEvent(0, eventId, aggregateId, type, payload, atMs, false));
            return this;
        }

        Tx markPublished(long seq) {
            requireOpen();
            pendingMarks.add(seq);
            return this;
        }

        void commit() {
            requireOpen();
            open = false;
            for (Payment p : pendingPayments) payments.put(p.paymentId(), p);
            for (OutboxEvent e : pendingEvents) {
                long seq = nextSeq++;
                outbox.put(seq, new OutboxEvent(seq, e.eventId(), e.aggregateId(), e.type(), e.payload(), e.createdAtMs(), false));
            }
            for (long seq : pendingMarks) {
                OutboxEvent e = outbox.get(seq);
                if (e != null) outbox.put(seq, e.markPublished());
            }
            commits++;
        }

        void rollback() {
            requireOpen();
            open = false;
            rollbacks++;
        }
    }

    Tx begin() { return new Tx(); }

    Payment payment(String id) { return payments.get(id); }
    int paymentCount() { return payments.size(); }
    List<OutboxEvent> outboxRows() { return List.copyOf(outbox.values()); }

    /** The relay's query: unpublished rows in commit order, at most `limit`. */
    List<OutboxEvent> unpublished(int limit) {
        List<OutboxEvent> out = new ArrayList<>();
        for (OutboxEvent e : outbox.values()) {
            if (!e.published()) out.add(e);
            if (out.size() == limit) break;
        }
        return out;
    }

    long unpublishedCount() { return outbox.values().stream().filter(e -> !e.published()).count(); }
}

/** A message as the broker holds it. The broker assigns its own offset; the eventId travels inside. */
record Message(long offset, String eventId, String aggregateId, String type, String payload) { }

/** An in-memory broker topic. Append-only, with a switch to simulate an outage. */
final class Broker {
    private final List<Message> log = new ArrayList<>();
    boolean down = false;
    int publishAttempts = 0;

    long publish(String eventId, String aggregateId, String type, String payload) {
        publishAttempts++;
        if (down) throw new BrokerUnavailableException();
        long offset = log.size();
        log.add(new Message(offset, eventId, aggregateId, type, payload));
        return offset;
    }

    List<Message> messages() { return List.copyOf(log); }
    List<Message> from(long offset) { return offset >= log.size() ? List.of() : List.copyOf(log.subList((int) offset, log.size())); }
    int size() { return log.size(); }
}

/** Where a process dies. */
enum CrashPoint { NONE, AFTER_COMMIT_BEFORE_PUBLISH, AFTER_PUBLISH_BEFORE_COMMIT, AFTER_PUBLISH_BEFORE_MARK }

/** Builds the payload once so the dual-write and outbox services emit byte-identical events. */
final class Events {
    static String eventIdFor(Payment p) { return "evt-" + p.paymentId(); }
    static String payloadFor(Payment p) { return p.paymentId() + "|" + p.merchant() + "|" + p.amount(); }
    static final String PAYMENT_ACCEPTED = "PaymentAccepted";
}

/**
 * The naive service: write the row, then publish the event (or the other way round). Two systems, two
 * commits, no atomicity between them. Crash points show both ways it loses data.
 */
final class DualWritePaymentService {
    private final Store store;
    private final Broker broker;
    CrashPoint crashPoint = CrashPoint.NONE;
    boolean publishFirst = false;

    DualWritePaymentService(Store store, Broker broker) { this.store = store; this.broker = broker; }

    void accept(Payment p) {
        if (publishFirst) {
            broker.publish(Events.eventIdFor(p), p.merchant(), Events.PAYMENT_ACCEPTED, Events.payloadFor(p));
            if (crashPoint == CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT) throw new CrashException("after publish, before commit");
            store.begin().insertPayment(p).commit();
        } else {
            store.begin().insertPayment(p).commit();
            if (crashPoint == CrashPoint.AFTER_COMMIT_BEFORE_PUBLISH) throw new CrashException("after commit, before publish");
            broker.publish(Events.eventIdFor(p), p.merchant(), Events.PAYMENT_ACCEPTED, Events.payloadFor(p));
        }
    }
}

/** The fixed service: the payment row and the outbox row go into one transaction. The broker is never touched here. */
final class OutboxPaymentService {
    private final Store store;
    private final SimClock clock;

    OutboxPaymentService(Store store, SimClock clock) { this.store = store; this.clock = clock; }

    void accept(Payment p) {
        store.begin()
                .insertPayment(p)
                .insertOutbox(Events.eventIdFor(p), p.merchant(), Events.PAYMENT_ACCEPTED, Events.payloadFor(p), clock.now())
                .commit();
    }

    /** A request that fails validation after the row was staged: rollback must drop the outbox row with it. */
    void acceptThenFail(Payment p) {
        Store.Tx tx = store.begin()
                .insertPayment(p)
                .insertOutbox(Events.eventIdFor(p), p.merchant(), Events.PAYMENT_ACCEPTED, Events.payloadFor(p), clock.now());
        tx.rollback();
    }
}

/**
 * The relay (a polling publisher). Reads unpublished rows in seq order, publishes each, then marks it published
 * in a second transaction. A crash between publish and mark re-publishes on the next poll: at-least-once.
 */
final class OutboxRelay {
    private final Store store;
    private final Broker broker;
    final int batchSize;
    CrashPoint crashPoint = CrashPoint.NONE;
    int published = 0, failedAttempts = 0, polls = 0;

    OutboxRelay(Store store, Broker broker, int batchSize) { this.store = store; this.broker = broker; this.batchSize = batchSize; }

    /** One poll. Returns how many rows were marked published. Stops at the first broker failure to keep order. */
    int poll() {
        polls++;
        int marked = 0;
        for (OutboxEvent e : store.unpublished(batchSize)) {
            try {
                broker.publish(e.eventId(), e.aggregateId(), e.type(), e.payload());
            } catch (BrokerUnavailableException ex) {
                failedAttempts++;
                return marked;                                   // leave the row pending; retry next poll
            }
            published++;
            if (crashPoint == CrashPoint.AFTER_PUBLISH_BEFORE_MARK) {
                crashPoint = CrashPoint.NONE;
                throw new CrashException("after publish of " + e.eventId() + ", before mark");
            }
            store.begin().markPublished(e.seq()).commit();
            marked++;
        }
        return marked;
    }

    /** Poll until nothing is pending or the broker refuses. Bounded so a dead broker cannot loop forever. */
    int drain(int maxPolls) {
        int total = 0;
        for (int i = 0; i < maxPolls && store.unpublishedCount() > 0; i++) {
            int n = poll();
            total += n;
            if (n == 0) break;                                   // broker refused; nothing changes until it recovers
        }
        return total;
    }
}

/**
 * A downstream consumer: the ledger projection. Reads the broker from its offset, applies each event once,
 * and ignores any eventId it has already applied. The dedupe set is what turns at-least-once into effectively-once.
 */
final class LedgerConsumer {
    private final Broker broker;
    private final Map<String, Money> balances = new LinkedHashMap<>();
    private final Set<String> applied = new LinkedHashSet<>();
    private long offset = 0;
    int duplicatesIgnored = 0;

    LedgerConsumer(Broker broker) { this.broker = broker; }

    int consume() {
        int appliedNow = 0;
        for (Message m : broker.from(offset)) {
            offset = m.offset() + 1;
            if (!applied.add(m.eventId())) { duplicatesIgnored++; continue; }
            String[] parts = m.payload().split("\\|");
            Money amount = parseMoney(parts[2]);
            balances.merge(m.aggregateId(), amount, Money::plus);
            appliedNow++;
        }
        return appliedNow;
    }

    static Money parseMoney(String text) {
        String[] p = text.split(" ");
        boolean neg = p[0].startsWith("-");
        String digits = (neg ? p[0].substring(1) : p[0]).replace(".", "");
        long minor = Long.parseLong(digits);
        return Money.ofMinor(neg ? -minor : minor, Currency.USD);
    }

    Money balance(String merchant) { return balances.getOrDefault(merchant, Money.zero(Currency.USD)); }
    int appliedCount() { return applied.size(); }
    long offset() { return offset; }
}

public final class Main {
    static Money usd(String text) { return LedgerConsumer.parseMoney(text + " USD"); }

    static Payment payment(String id, String merchant, String amount, SimClock clock) {
        return new Payment(id, merchant, usd(amount), clock.now());
    }

    static void line(String s) { System.out.println(s); }

    public static void main(String[] args) {
        SimClock clock = new SimClock(0);

        line("== 1. Dual write, commit then publish: the broker is down at the wrong moment ==");
        {
            Store store = new Store();
            Broker broker = new Broker();
            DualWritePaymentService svc = new DualWritePaymentService(store, broker);
            svc.accept(payment("pay-1", "acme", "20.00", clock));
            broker.down = true;
            try {
                svc.accept(payment("pay-2", "acme", "35.00", clock));
            } catch (BrokerUnavailableException e) {
                line("  pay-2: row committed, then publish failed: " + e.getMessage());
            }
            broker.down = false;
            LedgerConsumer ledger = new LedgerConsumer(broker);
            ledger.consume();
            line("  store has " + store.paymentCount() + " payments; broker has " + broker.size() + " messages");
            line("  ledger balance for acme = " + ledger.balance("acme") + "  (store says 55.00: pay-2 is an orphan row, the ledger will never see it)");
        }

        line("\n== 2. Dual write, publish then commit: the process dies between the two ==");
        {
            Store store = new Store();
            Broker broker = new Broker();
            DualWritePaymentService svc = new DualWritePaymentService(store, broker);
            svc.publishFirst = true;
            svc.crashPoint = CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT;
            try {
                svc.accept(payment("pay-3", "acme", "12.50", clock));
            } catch (CrashException e) {
                line("  pay-3: " + e.getMessage());
            }
            LedgerConsumer ledger = new LedgerConsumer(broker);
            ledger.consume();
            line("  store has " + store.paymentCount() + " payments; ledger balance for acme = " + ledger.balance("acme")
                    + "  (a ghost event: the ledger credits a payment that does not exist)");
        }

        line("\n== 3. Outbox: one transaction, two rows; the relay delivers when the broker comes back ==");
        {
            Store store = new Store();
            Broker broker = new Broker();
            OutboxPaymentService svc = new OutboxPaymentService(store, clock);
            OutboxRelay relay = new OutboxRelay(store, broker, 10);
            LedgerConsumer ledger = new LedgerConsumer(broker);
            broker.down = true;
            svc.accept(payment("pay-4", "acme", "20.00", clock));
            clock.advance(5);
            svc.accept(payment("pay-5", "acme", "35.00", clock));
            line("  broker down; store has " + store.paymentCount() + " payments and " + store.unpublishedCount() + " pending outbox rows");
            for (OutboxEvent e : store.outboxRows()) line("    " + e);
            int n = relay.drain(3);
            line("  relay polled " + relay.polls + " times, delivered " + n + ", failed attempts " + relay.failedAttempts);
            broker.down = false;
            n = relay.drain(3);
            ledger.consume();
            line("  broker back; relay delivered " + n + "; pending now " + store.unpublishedCount());
            line("  ledger balance for acme = " + ledger.balance("acme") + "  (matches the store)");
        }

        line("\n== 4. Relay crashes after publish, before mark: at-least-once, and the consumer dedupes ==");
        {
            Store store = new Store();
            Broker broker = new Broker();
            OutboxPaymentService svc = new OutboxPaymentService(store, clock);
            OutboxRelay relay = new OutboxRelay(store, broker, 10);
            LedgerConsumer ledger = new LedgerConsumer(broker);
            svc.accept(payment("pay-6", "globex", "100.00", clock));
            svc.accept(payment("pay-7", "globex", "1.00", clock));
            relay.crashPoint = CrashPoint.AFTER_PUBLISH_BEFORE_MARK;
            try {
                relay.poll();
            } catch (CrashException e) {
                line("  relay: " + e.getMessage());
            }
            line("  broker has " + broker.size() + " message(s); store still has " + store.unpublishedCount() + " pending rows");
            relay.drain(3);
            line("  relay restarted and drained; broker now has " + broker.size() + " messages for 2 payments");
            for (Message m : broker.messages()) line("    offset " + m.offset() + " " + m.eventId());
            ledger.consume();
            line("  ledger applied " + ledger.appliedCount() + ", ignored " + ledger.duplicatesIgnored + " duplicate; balance for globex = " + ledger.balance("globex"));
        }

        line("\n== 5. Rollback drops the outbox row with the payment: no event for a payment that never existed ==");
        {
            Store store = new Store();
            OutboxPaymentService svc = new OutboxPaymentService(store, clock);
            svc.acceptThenFail(payment("pay-8", "acme", "999.00", clock));
            line("  payments=" + store.paymentCount() + " outbox rows=" + store.outboxRows().size() + " rollbacks=" + store.rollbacks);
        }

        line("\n== 6. Ordering: outbox seq is commit order, and the relay publishes in that order per aggregate ==");
        {
            Store store = new Store();
            Broker broker = new Broker();
            OutboxPaymentService svc = new OutboxPaymentService(store, clock);
            OutboxRelay relay = new OutboxRelay(store, broker, 2);
            for (int i = 1; i <= 5; i++) svc.accept(payment("pay-" + (10 + i), "acme", i + ".00", clock));
            int polls = 0;
            while (store.unpublishedCount() > 0) { relay.poll(); polls++; }
            StringBuilder order = new StringBuilder();
            for (Message m : broker.messages()) order.append(m.eventId().replace("evt-pay-", "")).append(' ');
            line("  batch size 2, " + polls + " polls, broker order: " + order.toString().trim());
        }
    }
}
