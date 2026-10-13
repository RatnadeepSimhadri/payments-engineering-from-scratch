// Lesson: phases/14-payments-at-scale/03-exactly-once-money-movement/docs/en.md
// Exactly-Once Money Movement End to End
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012); Kleppmann, DDIA ch. 11;
//          Richardson, microservices.io "Transactional outbox"; Stripe docs "Idempotent requests".
// Java 21, standard library only. Money is integer minor units (Phase 10, Lesson 01).

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.TreeMap;

// ---------------------------------------------------------------- Money (Phase 10, Lesson 01) ----

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount of one currency as a signed count of minor units. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }

    boolean isZero() { return minorUnits == 0; }

    boolean isPositive() { return minorUnits > 0; }

    @Override
    public String toString() {
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

// ---------------------------------------------------------------- Faults ----

/** Where the harness may crash a component. Each point is a real place a process dies in production. */
enum FaultPoint {
    API_BEFORE_COMMIT,      // handler dies before the transaction commits: nothing is written
    API_AFTER_COMMIT,       // transaction committed, response lost: the client will retry
    RELAY_AFTER_PUBLISH,    // relay published to the bus but died before marking the row: a duplicate will follow
    BUS_DUPLICATE,          // the bus delivers the same message twice
    BUS_REORDER,            // the bus delivers a batch out of order
    CONSUMER_BEFORE_COMMIT, // consumer died mid-handle: nothing written, the message is redelivered
    CONSUMER_AFTER_COMMIT,  // consumer committed but died before acknowledging: the message is redelivered anyway
}

final class CrashException extends RuntimeException {
    final FaultPoint point;

    CrashException(FaultPoint point) {
        super("crash at " + point);
        this.point = point;
    }
}

/** Seeded fault injector: the only source of randomness in the pipeline, so every run is replayable by seed. */
final class FaultInjector {
    private final Random random;
    private final Map<FaultPoint, Double> rates = new EnumMap<>(FaultPoint.class);
    private final Map<FaultPoint, Integer> fired = new EnumMap<>(FaultPoint.class);
    private boolean armed = true;

    FaultInjector(long seed, Map<FaultPoint, Double> rates) {
        this.random = new Random(seed);
        this.rates.putAll(rates);
    }

    static FaultInjector none() { return new FaultInjector(0, Map.of()); }

    /** Every fault point fires with the same probability. */
    static Map<FaultPoint, Double> uniform(double rate) {
        Map<FaultPoint, Double> m = new EnumMap<>(FaultPoint.class);
        for (FaultPoint p : FaultPoint.values()) m.put(p, rate);
        return m;
    }

    /** A fault schedule drawn from a seed: each point gets its own rate in [0, 0.25). */
    static Map<FaultPoint, Double> randomSchedule(long seed) {
        Random r = new Random(seed ^ 0x5DEECE66DL);
        Map<FaultPoint, Double> m = new EnumMap<>(FaultPoint.class);
        for (FaultPoint p : FaultPoint.values()) m.put(p, r.nextInt(25) / 100.0);
        return m;
    }

    boolean fire(FaultPoint point) {
        if (!armed) return false;
        double rate = rates.getOrDefault(point, 0.0);
        if (rate <= 0) return false;
        boolean hit = random.nextDouble() < rate;
        if (hit) fired.merge(point, 1, Integer::sum);
        return hit;
    }

    /** Stop injecting faults so a run can drain to quiescence; counts are kept. */
    void disarm() { armed = false; }

    int count(FaultPoint point) { return fired.getOrDefault(point, 0); }

    int totalFired() { return fired.values().stream().mapToInt(Integer::intValue).sum(); }

    Random random() { return random; }
}

// ---------------------------------------------------------------- Defences ----

/**
 * The three mechanisms that together give exactly-once effects. Each one is a flag so the harness can
 * remove it and show which invariant breaks. No two of them cover the same seam.
 */
record Defences(boolean apiDedupe, boolean atomicOutbox, boolean consumerDedupe) {
    static Defences all() { return new Defences(true, true, true); }

    Defences without(String name) {
        return switch (name) {
            case "apiDedupe" -> new Defences(false, atomicOutbox, consumerDedupe);
            case "atomicOutbox" -> new Defences(apiDedupe, false, consumerDedupe);
            case "consumerDedupe" -> new Defences(apiDedupe, atomicOutbox, false);
            default -> throw new IllegalArgumentException("unknown defence " + name);
        };
    }
}

// ---------------------------------------------------------------- Ledger (both sides) ----

/** A signed posting: positive debits the account, negative credits it. */
record Posting(String account, Money amount) {}

record JournalEntry(String entryId, List<Posting> postings) {
    static JournalEntry transfer(String entryId, String debitAccount, String creditAccount, Money amount) {
        if (!amount.isPositive()) throw new IllegalArgumentException("transfer amount must be positive: " + amount);
        return new JournalEntry(entryId, List.of(new Posting(debitAccount, amount), new Posting(creditAccount, amount.negate())));
    }

    boolean balanced() {
        Money sum = Money.zero(postings.get(0).amount().currency());
        for (Posting p : postings) sum = sum.plus(p.amount());
        return sum.isZero();
    }
}

/**
 * Append-only double-entry ledger. It checks that every entry balances; it does NOT check entry-id
 * uniqueness, because the lesson's point is that dedupe is the job of the layer in front of the ledger.
 * Exercise 1 adds the unique constraint and shows what defence in depth hides.
 */
final class Ledger {
    final List<JournalEntry> journal = new ArrayList<>();
    private final Map<String, Integer> entryCounts = new HashMap<>();
    private final Map<String, Money> balances = new TreeMap<>();
    private final Currency currency;

    Ledger(Currency currency) { this.currency = currency; }

    void post(JournalEntry entry) {
        if (!entry.balanced()) throw new IllegalArgumentException("unbalanced entry " + entry.entryId());
        journal.add(entry);
        entryCounts.merge(entry.entryId(), 1, Integer::sum);
        for (Posting p : entry.postings()) balances.merge(p.account(), p.amount(), Money::plus);
    }

    Money balance(String account) { return balances.getOrDefault(account, Money.zero(currency)); }

    /** Debits positive, credits negative: a correct ledger always sums to zero. */
    Money trialBalance() {
        Money sum = Money.zero(currency);
        for (Money m : balances.values()) sum = sum.plus(m);
        return sum;
    }

    /** Sum of the credit balances of every account whose name starts with the prefix, as a positive amount. */
    Money creditTotal(String prefix) {
        Money sum = Money.zero(currency);
        for (Map.Entry<String, Money> e : balances.entrySet()) {
            if (e.getKey().startsWith(prefix)) sum = sum.plus(e.getValue().negate());
        }
        return sum;
    }

    int countEntries(String entryId) { return entryCounts.getOrDefault(entryId, 0); }

    Map<String, Money> balances() { return Collections.unmodifiableMap(balances); }
}

// ---------------------------------------------------------------- Gateway side ----

record PaymentRequest(String idempotencyKey, String customerId, String merchantId, Money amount) {
    /** What must be identical for two requests with the same key to be "the same request". */
    String fingerprint() { return customerId + "|" + merchantId + "|" + amount; }
}

record Payment(String paymentId, String idempotencyKey, String customerId, String merchantId, Money amount) {
    String fingerprint() { return customerId + "|" + merchantId + "|" + amount; }
}

/** The event written to the outbox in the same transaction as the payment and later carried by the bus. */
record PaymentEvent(String eventId, String paymentId, String customerId, String merchantId, Money amount) {
    static PaymentEvent accepted(Payment p) {
        return new PaymentEvent("evt-" + p.paymentId(), p.paymentId(), p.customerId(), p.merchantId(), p.amount());
    }
}

record OutboxRow(PaymentEvent event, boolean published) {}

final class IdempotencyConflictException extends IllegalArgumentException {
    IdempotencyConflictException(String key) { super("idempotency key " + key + " reused with a different request"); }
}

/**
 * An in-memory stand-in for the gateway's database. The only thing it models faithfully is atomicity:
 * {@link #transaction} either applies every write or none, and a crash is only possible strictly before
 * or strictly after the commit, never half-way through.
 */
final class GatewayStore {
    static final String RECEIVABLE = "asset:receivable:";          // + customerId
    static final String CLEARING = "liability:clearing:acquirer";  // owed onward to the downstream ledger
    final Map<String, Payment> payments = new LinkedHashMap<>();       // paymentId -> payment
    final Map<String, String> idempotencyKeys = new LinkedHashMap<>(); // key -> paymentId (unique index)
    final Map<String, OutboxRow> outbox = new LinkedHashMap<>();       // eventId -> row, insertion order
    final Ledger ledger = new Ledger(Currency.USD);                    // the gateway's own books
    int commits = 0;

    void transaction(FaultInjector faults, FaultPoint before, FaultPoint after, Runnable writes) {
        if (before != null && faults.fire(before)) throw new CrashException(before);
        writes.run();
        commits++;
        if (after != null && faults.fire(after)) throw new CrashException(after);
    }

    List<PaymentEvent> unpublished() {
        List<PaymentEvent> out = new ArrayList<>();
        for (OutboxRow row : outbox.values()) if (!row.published()) out.add(row.event());
        return out;
    }
}

record Response(Payment payment, boolean replayed) {}

/** The idempotent API handler: look up the key, else write payment + key + ledger entry + outbox row in one transaction. */
final class PaymentApi {
    private final GatewayStore store;
    private final FaultInjector faults;
    private final Defences defences;
    private int sequence = 0;

    PaymentApi(GatewayStore store, FaultInjector faults, Defences defences) {
        this.store = store;
        this.faults = faults;
        this.defences = defences;
    }

    Response createPayment(PaymentRequest request) {
        if (defences.apiDedupe()) {
            String existingId = store.idempotencyKeys.get(request.idempotencyKey());
            if (existingId != null) {
                Payment existing = store.payments.get(existingId);
                if (!existing.fingerprint().equals(request.fingerprint())) {
                    throw new IdempotencyConflictException(request.idempotencyKey());
                }
                return new Response(existing, true);
            }
        }
        String paymentId = String.format("pay-%06d", ++sequence);
        Payment payment = new Payment(paymentId, request.idempotencyKey(), request.customerId(), request.merchantId(), request.amount());
        PaymentEvent event = PaymentEvent.accepted(payment);
        JournalEntry entry = JournalEntry.transfer(paymentId, GatewayStore.RECEIVABLE + request.customerId(), GatewayStore.CLEARING, request.amount());
        Runnable record = () -> {
            store.payments.put(paymentId, payment);
            store.idempotencyKeys.put(request.idempotencyKey(), paymentId);
            store.ledger.post(entry);
        };
        Runnable enqueue = () -> store.outbox.put(event.eventId(), new OutboxRow(event, false));
        if (defences.atomicOutbox()) {
            store.transaction(faults, FaultPoint.API_BEFORE_COMMIT, FaultPoint.API_AFTER_COMMIT, () -> { record.run(); enqueue.run(); });
        } else {
            // The dual-write anti-pattern: record, then enqueue in a second transaction. A crash between them
            // (API_AFTER_COMMIT) leaves a payment that no downstream system will ever hear about.
            store.transaction(faults, FaultPoint.API_BEFORE_COMMIT, FaultPoint.API_AFTER_COMMIT, record);
            store.transaction(faults, null, null, enqueue);
        }
        return new Response(payment, false);
    }
}

/** Publishes unpublished outbox rows to the bus. At-least-once: a crash between publish and mark republishes. */
final class OutboxRelay {
    private final GatewayStore store;
    private final Bus bus;
    private final FaultInjector faults;

    OutboxRelay(GatewayStore store, Bus bus, FaultInjector faults) {
        this.store = store;
        this.bus = bus;
        this.faults = faults;
    }

    int relayOnce() {
        int published = 0;
        for (PaymentEvent event : store.unpublished()) {
            bus.publish(event);
            published++;
            if (faults.fire(FaultPoint.RELAY_AFTER_PUBLISH)) throw new CrashException(FaultPoint.RELAY_AFTER_PUBLISH);
            store.transaction(faults, null, null, () -> store.outbox.put(event.eventId(), new OutboxRow(event, true)));
        }
        return published;
    }
}

// ---------------------------------------------------------------- Bus ----

interface EventHandler { void handle(PaymentEvent event); }

/** In-memory at-least-once bus: may duplicate on publish, may reorder a batch, redelivers on consumer crash. */
final class Bus {
    private final Deque<PaymentEvent> queue = new ArrayDeque<>();
    private final FaultInjector faults;
    int published = 0, duplicated = 0, delivered = 0, redelivered = 0, reorderedBatches = 0;

    Bus(FaultInjector faults) { this.faults = faults; }

    void publish(PaymentEvent event) {
        queue.addLast(event);
        published++;
        if (faults.fire(FaultPoint.BUS_DUPLICATE)) {
            queue.addLast(event);
            duplicated++;
        }
    }

    int deliver(EventHandler handler, int batchSize) {
        List<PaymentEvent> batch = new ArrayList<>();
        while (!queue.isEmpty() && batch.size() < batchSize) batch.add(queue.pollFirst());
        if (batch.size() > 1 && faults.fire(FaultPoint.BUS_REORDER)) {
            Collections.shuffle(batch, faults.random());
            reorderedBatches++;
        }
        int ok = 0;
        for (int i = 0; i < batch.size(); i++) {
            try {
                handler.handle(batch.get(i));
                delivered++;
                ok++;
            } catch (CrashException crash) {
                // nothing from this message on was acknowledged: put the rest back, in order, for redelivery
                for (int j = batch.size() - 1; j >= i; j--) queue.addFirst(batch.get(j));
                redelivered++;
                throw crash;
            }
        }
        return ok;
    }

    int pending() { return queue.size(); }
}

// ---------------------------------------------------------------- Downstream ledger side ----

/** The consumer's database: processed-message inbox and the downstream ledger, written in one transaction. */
final class LedgerStore {
    static final String CLEARING = "asset:clearing:acquirer";   // what the gateway owes us
    static final String PAYABLE = "liability:payable:";         // + merchantId
    final Set<String> inbox = new HashSet<>();
    final Ledger ledger = new Ledger(Currency.USD);
    int commits = 0;

    void transaction(FaultInjector faults, FaultPoint before, FaultPoint after, Runnable writes) {
        if (before != null && faults.fire(before)) throw new CrashException(before);
        writes.run();
        commits++;
        if (after != null && faults.fire(after)) throw new CrashException(after);
    }
}

/** Dedupes by event id (inbox pattern), then posts to the downstream ledger with entryId == eventId. */
final class LedgerConsumer implements EventHandler {
    private final LedgerStore store;
    private final FaultInjector faults;
    private final Defences defences;
    int skippedDuplicates = 0;

    LedgerConsumer(LedgerStore store, FaultInjector faults, Defences defences) {
        this.store = store;
        this.faults = faults;
        this.defences = defences;
    }

    @Override
    public void handle(PaymentEvent event) {
        if (defences.consumerDedupe() && store.inbox.contains(event.eventId())) {
            skippedDuplicates++;
            return;
        }
        JournalEntry entry = JournalEntry.transfer(event.eventId(), LedgerStore.CLEARING, LedgerStore.PAYABLE + event.merchantId(), event.amount());
        store.transaction(faults, FaultPoint.CONSUMER_BEFORE_COMMIT, FaultPoint.CONSUMER_AFTER_COMMIT, () -> {
            store.inbox.add(event.eventId());
            store.ledger.post(entry);
        });
    }
}

// ---------------------------------------------------------------- Harness ----

record HarnessConfig(long seed, int operations, Map<FaultPoint, Double> rates, Defences defences) {
    static HarnessConfig standard(long seed, int operations) {
        return new HarnessConfig(seed, operations, FaultInjector.uniform(0.08), Defences.all());
    }

    static HarnessConfig randomSchedule(long seed, int operations) {
        return new HarnessConfig(seed, operations, FaultInjector.randomSchedule(seed), Defences.all());
    }

    HarnessConfig without(String defence) { return new HarnessConfig(seed, operations, rates, defences.without(defence)); }
}

record HarnessReport(long seed, int clientRequests, int apiCalls, int crashes, int acceptedPayments, int busPublished,
                     int busDuplicated, int busRedelivered, int gatewayEntries, int downstreamEntries, int consumerSkipped,
                     Money gatewayTrialBalance, Money downstreamTrialBalance, List<String> violations) {
    boolean passed() { return violations.isEmpty(); }

    /** The invariant named in the first violation, e.g. "one-payment-per-key". Empty when the run passed. */
    String firstBrokenInvariant() {
        if (violations.isEmpty()) return "";
        String v = violations.get(0);
        return v.substring(1, v.indexOf(']'));
    }

    String summary() {
        return String.format("seed=%d requests=%d apiCalls=%d crashes=%d accepted=%d published=%d dup=%d redelivered=%d"
                        + " gatewayEntries=%d downstreamEntries=%d skipped=%d trial=%s/%s violations=%d",
                seed, clientRequests, apiCalls, crashes, acceptedPayments, busPublished, busDuplicated, busRedelivered,
                gatewayEntries, downstreamEntries, consumerSkipped, gatewayTrialBalance, downstreamTrialBalance, violations.size());
    }
}

/** Drives thousands of operations through the pipeline under injected faults and checks the invariants. */
final class Harness {
    static final int MAX_CLIENT_ATTEMPTS = 12;

    static HarnessReport run(HarnessConfig cfg) {
        FaultInjector faults = new FaultInjector(cfg.seed(), cfg.rates());
        Random workload = new Random(cfg.seed() * 1_000_003L + 17);
        GatewayStore gateway = new GatewayStore();
        PaymentApi api = new PaymentApi(gateway, faults, cfg.defences());
        Bus bus = new Bus(faults);
        OutboxRelay relay = new OutboxRelay(gateway, bus, faults);
        LedgerStore downstream = new LedgerStore();
        LedgerConsumer consumer = new LedgerConsumer(downstream, faults, cfg.defences());

        List<PaymentRequest> issued = new ArrayList<>();            // every distinct request a client ever made
        Map<String, Payment> acknowledged = new LinkedHashMap<>();   // key -> payment the client saw
        int apiCalls = 0, crashes = 0, clientRequests = 0;

        for (int i = 0; i < cfg.operations(); i++) {
            PaymentRequest request;
            if (!issued.isEmpty() && workload.nextInt(100) < 15) {
                request = issued.get(workload.nextInt(issued.size())); // a client re-sends an old key (retry storm)
            } else {
                String key = "key-" + issued.size();
                Money amount = Money.ofMinor(100 + workload.nextInt(20_000), Currency.USD);
                request = new PaymentRequest(key, "cust-" + workload.nextInt(50), "merch-" + workload.nextInt(8), amount);
                issued.add(request);
            }
            clientRequests++;
            for (int attempt = 0; attempt < MAX_CLIENT_ATTEMPTS; attempt++) {
                apiCalls++;
                try {
                    Response r = api.createPayment(request);
                    acknowledged.put(request.idempotencyKey(), r.payment());
                    break;
                } catch (CrashException crash) {
                    crashes++;   // the client saw a timeout and retries with the same key
                }
            }
            if (i % 3 == 0) crashes += step(() -> relay.relayOnce());
            if (i % 5 == 0) crashes += step(() -> bus.deliver(consumer, 8));
        }

        faults.disarm();
        while (!gateway.unpublished().isEmpty() || bus.pending() > 0) {
            relay.relayOnce();
            bus.deliver(consumer, 64);
        }

        List<String> violations = checkInvariants(gateway, downstream.ledger, issued, acknowledged);
        return new HarnessReport(cfg.seed(), clientRequests, apiCalls, crashes, gateway.payments.size(), bus.published,
                bus.duplicated, bus.redelivered, gateway.ledger.journal.size(), downstream.ledger.journal.size(),
                consumer.skippedDuplicates, gateway.ledger.trialBalance(), downstream.ledger.trialBalance(), violations);
    }

    private static int step(Runnable action) {
        try {
            action.run();
            return 0;
        } catch (CrashException crash) {
            return 1;
        }
    }

    /** Each violation is "[invariant-name] detail" so a failing test can say which layer is missing. */
    static List<String> checkInvariants(GatewayStore gateway, Ledger downstream, List<PaymentRequest> issued, Map<String, Payment> acknowledged) {
        List<String> violations = new ArrayList<>();
        Ledger books = gateway.ledger;

        // 1. One payment per idempotency key, and the payment the client was told about is the stored one.
        Map<String, Integer> paymentsPerKey = new TreeMap<>();
        for (Payment p : gateway.payments.values()) paymentsPerKey.merge(p.idempotencyKey(), 1, Integer::sum);
        for (Map.Entry<String, Integer> e : paymentsPerKey.entrySet()) {
            if (e.getValue() != 1) violations.add("[one-payment-per-key] " + e.getKey() + " has " + e.getValue() + " payments");
        }
        for (Map.Entry<String, Payment> e : acknowledged.entrySet()) {
            String storedId = gateway.idempotencyKeys.get(e.getKey());
            if (!e.getValue().paymentId().equals(storedId)) {
                violations.add("[one-payment-per-key] " + e.getKey() + " acknowledged " + e.getValue().paymentId() + " but store has " + storedId);
            }
        }

        // 2. Atomicity of record + enqueue: a payment never exists without its outbox row.
        for (Payment p : gateway.payments.values()) {
            if (!gateway.outbox.containsKey("evt-" + p.paymentId())) violations.add("[payment-has-outbox-row] " + p.paymentId() + " has no outbox row");
        }

        // 3. Every accepted payment is posted exactly once in each ledger; nothing else is posted.
        for (Payment p : gateway.payments.values()) {
            int g = books.countEntries(p.paymentId());
            if (g != 1) violations.add("[posted-once-gateway] " + p.paymentId() + " posted " + g + " times");
            int d = downstream.countEntries("evt-" + p.paymentId());
            if (d != 1) violations.add("[posted-once-downstream] " + p.paymentId() + " posted " + d + " times downstream");
        }
        if (books.journal.size() != gateway.payments.size()) {
            violations.add("[posted-once-gateway] gatewayEntries=" + books.journal.size() + " payments=" + gateway.payments.size());
        }
        if (downstream.journal.size() != gateway.payments.size()) {
            violations.add("[posted-once-downstream] downstreamEntries=" + downstream.journal.size() + " payments=" + gateway.payments.size());
        }

        // 4. Money is conserved: both trial balances are zero and the clearing amounts agree with the accepted total.
        if (!books.trialBalance().isZero()) violations.add("[trial-balance] gateway " + books.trialBalance());
        if (!downstream.trialBalance().isZero()) violations.add("[trial-balance] downstream " + downstream.trialBalance());
        Money accepted = Money.zero(Currency.USD);
        for (Payment p : gateway.payments.values()) accepted = accepted.plus(p.amount());
        Money gatewayClearing = books.creditTotal(GatewayStore.CLEARING);
        Money payables = downstream.creditTotal(LedgerStore.PAYABLE);
        if (!gatewayClearing.equals(accepted)) violations.add("[money-conserved] gateway clearing " + gatewayClearing + " != accepted " + accepted);
        if (!payables.equals(accepted)) violations.add("[money-conserved] merchant payables " + payables + " != accepted " + accepted);

        // 5. Per customer, the receivable equals the sum of that customer's distinct acknowledged requests.
        Map<String, Money> intended = new TreeMap<>();
        for (PaymentRequest r : issued) {
            if (acknowledged.containsKey(r.idempotencyKey())) intended.merge(r.customerId(), r.amount(), Money::plus);
        }
        for (Map.Entry<String, Money> e : intended.entrySet()) {
            Money receivable = books.balance(GatewayStore.RECEIVABLE + e.getKey());
            if (!receivable.equals(e.getValue())) violations.add("[charged-what-was-asked] " + e.getKey() + " intended " + e.getValue() + " receivable " + receivable);
        }
        return violations;
    }
}

// ---------------------------------------------------------------- Demo ----

public final class Main {
    public static void main(String[] args) {
        System.out.println("== One payment, a crash at every seam, exactly one posting in each ledger ==");
        FaultInjector faults = new FaultInjector(7, Map.of(FaultPoint.API_AFTER_COMMIT, 1.0));
        GatewayStore gateway = new GatewayStore();
        PaymentApi api = new PaymentApi(gateway, faults, Defences.all());
        PaymentRequest request = new PaymentRequest("key-1", "cust-7", "merch-2", Money.ofMinor(1999, Currency.USD));
        try {
            api.createPayment(request);
        } catch (CrashException c) {
            System.out.println("attempt 1: " + c.getMessage() + "  (payment + ledger entry + outbox row committed, client saw a timeout)");
        }
        faults.disarm();
        Response retry = api.createPayment(request);
        System.out.println("attempt 2: replayed=" + retry.replayed() + " paymentId=" + retry.payment().paymentId()
                + "  payments=" + gateway.payments.size() + " gatewayEntries=" + gateway.ledger.journal.size() + " outboxRows=" + gateway.outbox.size());

        faults = new FaultInjector(7, Map.of(FaultPoint.RELAY_AFTER_PUBLISH, 1.0));
        Bus bus = new Bus(faults);
        OutboxRelay relay = new OutboxRelay(gateway, bus, faults);
        try {
            relay.relayOnce();
        } catch (CrashException c) {
            System.out.println("relay 1: " + c.getMessage() + "  (event on the bus, row still unpublished)");
        }
        faults.disarm();
        relay.relayOnce();
        System.out.println("relay 2: bus now holds " + bus.pending() + " copies of evt-" + retry.payment().paymentId());

        LedgerStore downstream = new LedgerStore();
        LedgerConsumer consumer = new LedgerConsumer(downstream, FaultInjector.none(), Defences.all());
        bus.deliver(consumer, 10);
        System.out.println("consumer: delivered=" + bus.delivered + " skippedDuplicates=" + consumer.skippedDuplicates
                + " downstreamEntries=" + downstream.ledger.journal.size() + " trialBalance=" + downstream.ledger.trialBalance());
        System.out.println("  gateway:    " + GatewayStore.RECEIVABLE + "cust-7 = " + gateway.ledger.balance(GatewayStore.RECEIVABLE + "cust-7")
                + ", " + GatewayStore.CLEARING + " = " + gateway.ledger.balance(GatewayStore.CLEARING));
        System.out.println("  downstream: " + LedgerStore.CLEARING + " = " + downstream.ledger.balance(LedgerStore.CLEARING)
                + ", " + LedgerStore.PAYABLE + "merch-2 = " + downstream.ledger.balance(LedgerStore.PAYABLE + "merch-2"));

        System.out.println("\n== Seeded fault-injection harness: 3000 operations, 8% fault rate at every seam ==");
        for (long seed = 1; seed <= 3; seed++) {
            HarnessReport report = Harness.run(HarnessConfig.standard(seed, 3000));
            System.out.println((report.passed() ? "PASS " : "FAIL ") + report.summary());
        }

        System.out.println("\n== Remove one defence at a time: the harness names the invariant that breaks ==");
        for (String defence : List.of("apiDedupe", "atomicOutbox", "consumerDedupe")) {
            HarnessReport broken = Harness.run(HarnessConfig.standard(1, 3000).without(defence));
            System.out.printf("%s without %-14s -> %s%n", broken.passed() ? "PASS" : "FAIL", defence,
                    broken.passed() ? "(still holds)" : broken.violations().get(0));
        }
    }
}
