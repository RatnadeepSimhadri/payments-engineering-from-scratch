// Lesson: phases/14-payments-at-scale/10-simulation-testing-of-payment-systems/docs/en.md
// Deterministic Simulation Testing of Payment Systems
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
//          FoundationDB "Simulation and Testing" docs; TigerBeetle VOPR design notes; FIPS 180-4 (SHA-256) for the trace fingerprint.
// Java 21, standard library only. Money is integer minor units (Phase 10, Lesson 01). No wall clock, no threads.
// SplitMix64 and integer-only probabilities are used so a later Python or Go port replays the same seed to the same trace.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;

// ---------------------------------------------------------------- Money (Phase 10, Lesson 01) ----

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

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

// ---------------------------------------------------------------- Deterministic primitives ----

/**
 * SplitMix64. Chosen over java.util.Random because the algorithm is six lines and trivial to port, so a seed
 * produces the identical event sequence in any later Python or Go implementation of this lesson. All arithmetic is on 64-bit words
 * with wrap-around; Java's long does that natively and >>> gives the unsigned shift.
 */
final class Rng {
    private long state;
    long draws = 0;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        draws++;
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    /** Uniform in [0, bound). Uses the top 32 bits so the result does not depend on signed-modulo semantics. */
    int nextInt(int bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive");
        return (int) ((nextLong() >>> 32) % bound);
    }

    /** Uniform in [0, bound) for bounds beyond 32 bits (fault start times over long horizons). Unsigned modulo. */
    long nextLongBounded(long bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive");
        return Long.remainderUnsigned(nextLong(), bound);
    }

    /** Uniform in [lo, hi]. */
    int between(int lo, int hi) { return lo + nextInt(hi - lo + 1); }

    /** True with probability basisPoints / 10000. Integer probabilities: no float compare, no platform-dependent rounding. */
    boolean chance(int basisPoints) { return nextInt(10_000) < basisPoints; }

    /** A statistically independent child stream; used so adding a component never shifts another's draws. */
    Rng split() { return new Rng(nextLong()); }
}

/**
 * SHA-256 over the processed-event trace, one line per event ("time label\n"). Two runs with the same fingerprint
 * executed the same events in the same order; a port in another language must produce the same fingerprint for the same seed.
 */
final class Fingerprint {
    private final MessageDigest digest;
    int count = 0;

    Fingerprint() {
        try { digest = MessageDigest.getInstance("SHA-256"); } catch (NoSuchAlgorithmException e) { throw new IllegalStateException(e); }
    }

    void add(String line) {
        digest.update(line.getBytes(StandardCharsets.UTF_8));
        digest.update((byte) '\n');
        count++;
    }

    /** First 16 hex digits of the digest: enough to compare runs, short enough to paste into a bug report. */
    String hex() {
        byte[] out;
        try { out = ((MessageDigest) digest.clone()).digest(); } catch (CloneNotSupportedException e) { throw new IllegalStateException(e); }
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < 8; i++) sb.append(String.format("%02x", out[i] & 0xFF));
        return sb.toString();
    }
}

record Event(long time, long seq, String label, Runnable action) {}

/**
 * The virtual clock and the event loop are the same object: time is whatever the next event says it is.
 * Events are ordered by (time, seq) so two events at the same instant run in the order they were scheduled.
 * Nothing in the lesson reads System.currentTimeMillis or sleeps.
 */
final class Simulator {
    private final PriorityQueue<Event> queue = new PriorityQueue<>(
            (a, b) -> a.time() != b.time() ? Long.compare(a.time(), b.time()) : Long.compare(a.seq(), b.seq()));
    private long seq = 0;
    long now = 0;
    long processed = 0;
    final Fingerprint trace = new Fingerprint();

    void schedule(long delayMs, String label, Runnable action) {
        if (delayMs < 0) throw new IllegalArgumentException("delay must be non-negative");
        queue.add(new Event(now + delayMs, seq++, label, action));
    }

    boolean step() {
        Event e = queue.poll();
        if (e == null) return false;
        now = e.time();
        processed++;
        trace.add(now + " " + e.label());
        e.action().run();
        return true;
    }

    void runUntil(long timeMs) {
        while (!queue.isEmpty() && queue.peek().time() <= timeMs) step();
        if (now < timeMs) now = timeMs;
    }

    /** Drain every remaining event; the guard turns an accidental infinite loop into a test failure. */
    void runUntilIdle(long maxEvents) {
        long budget = maxEvents;
        while (step()) if (--budget < 0) throw new IllegalStateException("simulation did not reach quiescence");
    }

    int pending() { return queue.size(); }
}

record NetworkConfig(int minLatencyMs, int maxLatencyMs, int dropBasisPoints) {
    static final NetworkConfig LAN = new NetworkConfig(20, 200, 100);   // 1% loss
}

/** Simulated network: every message takes a seeded latency, may be dropped, and is blocked while its link is partitioned. */
final class Network {
    private final Simulator sim;
    private final Rng rng;
    private final NetworkConfig cfg;
    private final Set<String> partitioned = new HashSet<>();
    int sent = 0, delivered = 0, dropped = 0, blocked = 0;

    Network(Simulator sim, Rng rng, NetworkConfig cfg) { this.sim = sim; this.rng = rng; this.cfg = cfg; }

    void send(String from, String to, String label, Runnable deliver) {
        sent++;
        if (partitioned.contains(from + ">" + to)) { blocked++; return; }
        if (rng.chance(cfg.dropBasisPoints())) { dropped++; return; }
        int latency = rng.between(cfg.minLatencyMs(), cfg.maxLatencyMs());
        sim.schedule(latency, "deliver " + to + "<-" + from + " " + label, () -> { delivered++; deliver.run(); });
    }

    void partition(String a, String b) { partitioned.add(a + ">" + b); partitioned.add(b + ">" + a); }

    void heal(String a, String b) { partitioned.remove(a + ">" + b); partitioned.remove(b + ">" + a); }

    boolean isPartitioned(String a, String b) { return partitioned.contains(a + ">" + b); }
}

// ---------------------------------------------------------------- Faults ----

enum FaultKind { PARTITION_CLIENT_GATEWAY, PARTITION_GATEWAY_PROCESSOR, PROCESSOR_OUTAGE, PROCESSOR_SLOW, GATEWAY_CRASH }

record Fault(long startMs, long endMs, FaultKind kind) {
    static final long MINUTE = 60_000L;

    /** Durations are kind-specific and drawn from the fault stream, so one fault more or less never shifts the workload. */
    static Fault draw(Rng rng, long horizonMs) {
        FaultKind kind = FaultKind.values()[rng.nextInt(FaultKind.values().length)];
        long start = rng.nextLongBounded(horizonMs);
        long duration = switch (kind) {
            case PARTITION_CLIENT_GATEWAY, PARTITION_GATEWAY_PROCESSOR -> rng.between(5, 120) * 1000L;
            case PROCESSOR_OUTAGE -> rng.between(5, 90) * 1000L;
            case PROCESSOR_SLOW -> rng.between(1, 10) * MINUTE;
            case GATEWAY_CRASH -> 0;
        };
        return new Fault(start, Math.min(horizonMs, start + duration), kind);
    }
}

/** A list of faults with start and end times. Armed onto the simulator before the run begins. */
final class FaultSchedule {
    final List<Fault> faults = new ArrayList<>();

    static FaultSchedule generate(Rng rng, long horizonMs, int count) {
        FaultSchedule s = new FaultSchedule();
        for (int i = 0; i < count; i++) s.faults.add(Fault.draw(rng, horizonMs));
        s.faults.sort((a, b) -> a.startMs() != b.startMs() ? Long.compare(a.startMs(), b.startMs()) : a.kind().compareTo(b.kind()));
        return s;
    }

    void arm(Simulator sim, Network net, Processor processor, Gateway gateway) {
        for (Fault f : faults) {
            sim.schedule(f.startMs(), "fault-begin " + f.kind(), () -> begin(f, net, processor, gateway));
            if (f.endMs() > f.startMs()) sim.schedule(f.endMs(), "fault-end " + f.kind(), () -> end(f, net, processor));
        }
    }

    private static void begin(Fault f, Network net, Processor processor, Gateway gateway) {
        switch (f.kind()) {
            case PARTITION_CLIENT_GATEWAY -> { for (int i = 0; i < 8; i++) net.partition("client-" + i, "gateway"); }
            case PARTITION_GATEWAY_PROCESSOR -> net.partition("gateway", "processor");
            case PROCESSOR_OUTAGE -> processor.down = true;
            case PROCESSOR_SLOW -> processor.extraLatencyMs = 5_000;
            case GATEWAY_CRASH -> gateway.crashWhenBusy();
        }
    }

    private static void end(Fault f, Network net, Processor processor) {
        switch (f.kind()) {
            case PARTITION_CLIENT_GATEWAY -> { for (int i = 0; i < 8; i++) net.heal("client-" + i, "gateway"); }
            case PARTITION_GATEWAY_PROCESSOR -> net.heal("gateway", "processor");
            case PROCESSOR_OUTAGE -> processor.down = false;
            case PROCESSOR_SLOW -> processor.extraLatencyMs = 0;
            case GATEWAY_CRASH -> { }
        }
    }
}

/** Defects the harness can plant in the gateway so the learner can watch the invariant checkers catch them. */
enum Defect {
    NONE,
    FRESH_AUTH_KEY_ON_RETRY,   // the gateway mints a new processor idempotency key on every retry
    RECOVERY_SKIPS_PENDING,    // after a crash the gateway does not re-drive payments that were in flight
}

// ---------------------------------------------------------------- The mini payment system ----

enum Status { PENDING, APPROVED, DECLINED }

record PaymentRequest(String key, String customerId, Money amount) {}

record AuthRequest(String authKey, String paymentId, String customerId, Money amount, int attempt) {}

record AuthResponse(String authKey, String paymentId, boolean approved) {}

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

/** Append-only double-entry ledger with a unique entry id (Phase 10, Lesson 03). */
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

    Money trialBalance() {
        Money sum = Money.zero(currency);
        for (Money m : balances.values()) sum = sum.plus(m);
        return sum;
    }

    /** Sum of the debit balances of every account with the prefix. */
    Money debitTotal(String prefix) {
        Money sum = Money.zero(currency);
        for (Map.Entry<String, Money> e : balances.entrySet()) if (e.getKey().startsWith(prefix)) sum = sum.plus(e.getValue());
        return sum;
    }

    int countEntries(String entryId) { return entryCounts.getOrDefault(entryId, 0); }

    Map<String, Money> balances() { return Collections.unmodifiableMap(balances); }
}

/** The card processor: idempotent on authKey, decides approve/decline from its own seeded stream, can be down or slow. */
final class Processor {
    static final String NAME = "processor";
    private final Simulator sim;
    private final Network net;
    private final Rng rng;
    private final int declineBasisPoints;
    final Map<String, AuthResponse> decisions = new LinkedHashMap<>();   // authKey -> decision (durable)
    final List<AuthResponse> approvals = new ArrayList<>();               // every approval ever granted, in order
    boolean down = false;
    int extraLatencyMs = 0;
    int received = 0, replayed = 0, swallowed = 0;

    Processor(Simulator sim, Network net, Rng rng, int declineBasisPoints) {
        this.sim = sim; this.net = net; this.rng = rng; this.declineBasisPoints = declineBasisPoints;
    }

    void receive(AuthRequest req, Gateway gateway) {
        received++;
        if (down) { swallowed++; return; }                                 // an outage looks like a timeout to the caller
        AuthResponse decision = decisions.get(req.authKey());
        if (decision != null) {
            replayed++;
        } else {
            boolean approved = !rng.chance(declineBasisPoints);
            decision = new AuthResponse(req.authKey(), req.paymentId(), approved);
            decisions.put(req.authKey(), decision);
            if (approved) approvals.add(decision);
        }
        AuthResponse reply = decision;
        sim.schedule(extraLatencyMs, "processor-respond " + req.paymentId(),
                () -> net.send(NAME, Gateway.NAME, "auth-response " + reply.paymentId(), () -> gateway.onAuthResponse(reply)));
    }

    /** Distinct payment ids that were approved, with how many times each was approved. */
    Map<String, Integer> approvalsPerPayment() {
        Map<String, Integer> m = new TreeMap<>();
        for (AuthResponse a : approvals) m.merge(a.paymentId(), 1, Integer::sum);
        return m;
    }
}

final class Payment {
    final String paymentId, key, customerId;
    final Money amount;
    Status status = Status.PENDING;
    String authKey;
    int attempts = 0;

    Payment(String paymentId, String key, String customerId, Money amount) {
        this.paymentId = paymentId; this.key = key; this.customerId = customerId; this.amount = amount;
        this.authKey = "auth-" + paymentId;
    }
}

/**
 * The gateway. Durable state (payments, idempotency keys, ledger) survives a crash; the in-memory table of
 * clients waiting for a reply does not, and neither do timers armed before the crash (checked via epoch).
 */
final class Gateway {
    static final String NAME = "gateway";
    static final String RECEIVABLE = "asset:receivable:";
    static final String CLEARING = "liability:clearing:processor";
    private final Simulator sim;
    private final Network net;
    private final Processor processor;
    private final Defect defect;
    final long processorTimeoutMs;
    final long recoveryDelayMs;
    // durable
    final Map<String, String> idempotencyKeys = new LinkedHashMap<>();  // key -> paymentId
    final Map<String, Payment> payments = new LinkedHashMap<>();
    final Ledger ledger = new Ledger(Currency.USD);
    // volatile
    private Map<String, List<Runnable>> waiting = new HashMap<>();
    private int epoch = 0;
    private boolean crashArmed = false;
    private int sequence = 0;                                              // durable: never reused after a crash
    int crashes = 0, authSends = 0, authTimeouts = 0, replays = 0, recovered = 0;

    Gateway(Simulator sim, Network net, Processor processor, Defect defect, long processorTimeoutMs, long recoveryDelayMs) {
        this.sim = sim; this.net = net; this.processor = processor; this.defect = defect;
        this.processorTimeoutMs = processorTimeoutMs; this.recoveryDelayMs = recoveryDelayMs;
    }

    /** Handle a client request; `reply` is how the response gets back to the client (over the network). */
    void handle(PaymentRequest req, java.util.function.Consumer<Status> reply) {
        String existingId = idempotencyKeys.get(req.key());
        if (existingId != null) {
            replays++;
            Payment p = payments.get(existingId);
            if (p.status != Status.PENDING) { reply.accept(p.status); return; }
            waiting.computeIfAbsent(p.paymentId, k -> new ArrayList<>()).add(() -> reply.accept(p.status));
            return;
        }
        String paymentId = String.format("pay-%06d", ++sequence);
        Payment p = new Payment(paymentId, req.key(), req.customerId(), req.amount());
        payments.put(paymentId, p);                                       // one transaction: payment + key
        idempotencyKeys.put(req.key(), paymentId);
        waiting.computeIfAbsent(paymentId, k -> new ArrayList<>()).add(() -> reply.accept(p.status));
        sendAuth(p);
        if (crashArmed) crash();                                          // the armed crash fires in the worst window: durable, in flight, unanswered
    }

    private void sendAuth(Payment p) {
        p.attempts++;
        if (defect == Defect.FRESH_AUTH_KEY_ON_RETRY && p.attempts > 1) p.authKey = "auth-" + p.paymentId + "-" + p.attempts;
        authSends++;
        AuthRequest req = new AuthRequest(p.authKey, p.paymentId, p.customerId, p.amount, p.attempts);
        net.send(NAME, Processor.NAME, "auth-request " + p.paymentId + " a" + p.attempts, () -> processor.receive(req, this));
        int armedEpoch = epoch, armedAttempt = p.attempts;
        sim.schedule(processorTimeoutMs, "auth-timeout " + p.paymentId + " a" + armedAttempt, () -> {
            if (armedEpoch != epoch || p.status != Status.PENDING || p.attempts != armedAttempt) return;
            authTimeouts++;
            sendAuth(p);                                                  // same authKey: the processor will dedupe
        });
    }

    void onAuthResponse(AuthResponse r) {
        Payment p = payments.get(r.paymentId());
        if (p == null || p.status != Status.PENDING) return;              // late duplicate: already resolved
        if (r.approved()) {
            p.status = Status.APPROVED;                                   // one transaction: status + ledger entry
            ledger.post(JournalEntry.transfer(p.paymentId, RECEIVABLE + p.customerId, CLEARING, p.amount));
        } else {
            p.status = Status.DECLINED;
        }
        List<Runnable> replies = waiting.remove(p.paymentId);
        if (replies != null) for (Runnable reply : replies) reply.run();
    }

    /**
     * A crash at 3 a.m. with nothing in flight tests nothing. The fault schedule arms the crash; it fires immediately if a
     * payment is pending, otherwise the next time one is accepted. VOPR-style harnesses bias faults toward interesting states.
     */
    void crashWhenBusy() {
        if (pendingCount() > 0) crash(); else crashArmed = true;
    }

    /** Lose everything in memory; durable state stays. Schedule the recovery sweep that re-drives in-flight payments. */
    void crash() {
        crashes++;
        epoch++;
        crashArmed = false;
        waiting = new HashMap<>();
        if (defect == Defect.RECOVERY_SKIPS_PENDING) return;
        sim.schedule(recoveryDelayMs, "gateway-recover", () -> {
            for (Payment p : payments.values()) if (p.status == Status.PENDING) { recovered++; sendAuth(p); }
        });
    }

    int pendingCount() {
        int n = 0;
        for (Payment p : payments.values()) if (p.status == Status.PENDING) n++;
        return n;
    }
}

record ClientConfig(long timeoutMs, int maxAttempts, long backoffMs) {
    static final ClientConfig DEFAULT = new ClientConfig(8_000, 6, 2_000);
}

/** A client that retries on timeout with the same idempotency key and records the final status it believes. */
final class Client {
    final String name;
    private final Simulator sim;
    private final Network net;
    private final Gateway gateway;
    private final ClientConfig cfg;
    final Map<String, Status> outcomes = new LinkedHashMap<>();  // key -> status the client believes (absent = unknown)
    final Set<String> unknown = new HashSet<>();
    final List<String> disagreements = new ArrayList<>();
    int requests = 0, retries = 0;

    Client(String name, Simulator sim, Network net, Gateway gateway, ClientConfig cfg) {
        this.name = name; this.sim = sim; this.net = net; this.gateway = gateway; this.cfg = cfg;
    }

    void pay(PaymentRequest req) { requests++; attempt(req, 1); }

    private void attempt(PaymentRequest req, int n) {
        boolean[] answered = {false};
        net.send(name, Gateway.NAME, "payment " + req.key() + " a" + n, () -> gateway.handle(req, status ->
                net.send(Gateway.NAME, name, "payment-reply " + req.key() + " " + status, () -> {
                    answered[0] = true;
                    Status previous = outcomes.putIfAbsent(req.key(), status);
                    if (previous != null && previous != status) disagreements.add(req.key() + " saw " + previous + " then " + status);
                })));
        sim.schedule(cfg.timeoutMs(), "client-timeout " + req.key() + " a" + n, () -> {
            if (answered[0] || outcomes.containsKey(req.key())) return;
            if (n >= cfg.maxAttempts()) { unknown.add(req.key()); return; }
            retries++;
            sim.schedule(cfg.backoffMs() * n, "client-retry " + req.key() + " a" + (n + 1), () -> attempt(req, n + 1));
        });
    }
}

// ---------------------------------------------------------------- Harness ----

record SimConfig(long seed, long virtualHours, int clients, long meanInterArrivalMs, int faults, Defect defect) {
    /** Four clients, one payment per client every ten virtual minutes on average, one fault every four virtual hours. */
    static SimConfig standard(long seed, long virtualHours) {
        return new SimConfig(seed, virtualHours, 4, 10 * 60_000L, (int) Math.max(1, virtualHours / 4), Defect.NONE);
    }

    SimConfig with(Defect d) { return new SimConfig(seed, virtualHours, clients, meanInterArrivalMs, faults, d); }

    SimConfig withSeed(long s) { return new SimConfig(s, virtualHours, clients, meanInterArrivalMs, faults, defect); }

    long horizonMs() { return virtualHours * 3_600_000L; }
}

record SimReport(long seed, long virtualHours, long events, int payments, int approved, int declined, int pending, int unknownAtClient,
                 int clientRetries, int authTimeouts, int netDropped, int netBlocked, int crashes, int faults,
                 Money trialBalance, long firstViolationMs, List<String> violations, String fingerprint) {
    boolean passed() { return violations.isEmpty(); }

    String firstBrokenInvariant() {
        if (violations.isEmpty()) return "";
        String v = violations.get(0);
        return v.substring(1, v.indexOf(']'));
    }

    String summary() {
        return String.format("seed=%d hours=%d events=%d payments=%d approved=%d declined=%d pending=%d unknown=%d retries=%d"
                        + " authTimeouts=%d dropped=%d blocked=%d crashes=%d faults=%d trial=%s violations=%d fp=%s",
                seed, virtualHours, events, payments, approved, declined, pending, unknownAtClient, clientRetries, authTimeouts,
                netDropped, netBlocked, crashes, faults, trialBalance, violations.size(), fingerprint);
    }

    /** What you paste into the bug report: enough to replay the exact run. */
    String reproduction() {
        return "reproduce with: Harness.run(SimConfig.standard(" + seed + ", " + virtualHours + "))"
                + (violations.isEmpty() ? "" : "; first violation at t=" + firstViolationMs + "ms: " + violations.get(0));
    }
}

/**
 * Wires the system, runs it for the configured virtual horizon, checks the safety invariants once per virtual day and
 * the quiescence invariants after the drain. Checks are O(payments), so checking every event would be quadratic.
 */
final class Harness {
    static final long HOUR = 3_600_000L;
    static final long CHECK_INTERVAL = 24 * HOUR;

    static SimReport run(SimConfig cfg) {
        Simulator sim = new Simulator();
        Rng master = new Rng(cfg.seed());
        Rng netRng = master.split(), processorRng = master.split(), faultRng = master.split(), workloadRng = master.split();
        Network net = new Network(sim, netRng, NetworkConfig.LAN);
        Processor processor = new Processor(sim, net, processorRng, 500);                       // 5% declines
        Gateway gateway = new Gateway(sim, net, processor, cfg.defect(), 3_000, 2_000);
        List<Client> clients = new ArrayList<>();
        for (int i = 0; i < cfg.clients(); i++) clients.add(new Client("client-" + i, sim, net, gateway, ClientConfig.DEFAULT));
        FaultSchedule schedule = FaultSchedule.generate(faultRng, cfg.horizonMs(), cfg.faults());
        schedule.arm(sim, net, processor, gateway);

        // Workload: each client's next payment is drawn from the workload stream; the stream never sees faults.
        int[] issued = {0};
        for (Client c : clients) scheduleNextPayment(sim, c, workloadRng, cfg, issued);

        List<String> violations = new ArrayList<>();
        long[] firstViolation = {-1};
        for (long t = CHECK_INTERVAL; t <= cfg.horizonMs(); t += CHECK_INTERVAL) {
            sim.schedule(t, "invariant-check", () -> {
                if (!violations.isEmpty()) return;
                violations.addAll(checkSafety(gateway, processor, clients));
                if (!violations.isEmpty()) firstViolation[0] = sim.now;
            });
        }
        sim.runUntil(cfg.horizonMs());
        sim.runUntilIdle(5_000_000);                                       // drain: no new payments, faults all ended

        if (violations.isEmpty()) {
            violations.addAll(checkSafety(gateway, processor, clients));
            violations.addAll(checkQuiescent(gateway, processor));
            if (!violations.isEmpty()) firstViolation[0] = sim.now;
        }
        int approved = 0, declined = 0, unknown = 0, retries = 0;
        for (Payment p : gateway.payments.values()) { if (p.status == Status.APPROVED) approved++; else if (p.status == Status.DECLINED) declined++; }
        for (Client c : clients) { unknown += c.unknown.size(); retries += c.retries; }
        return new SimReport(cfg.seed(), cfg.virtualHours(), sim.processed, gateway.payments.size(), approved, declined, gateway.pendingCount(),
                unknown, retries, gateway.authTimeouts, net.dropped, net.blocked, gateway.crashes, schedule.faults.size(),
                gateway.ledger.trialBalance(), firstViolation[0], violations, sim.trace.hex());
    }

    private static void scheduleNextPayment(Simulator sim, Client c, Rng rng, SimConfig cfg, int[] issued) {
        long gap = rng.between((int) (cfg.meanInterArrivalMs() / 2), (int) (cfg.meanInterArrivalMs() * 3 / 2));
        sim.schedule(gap, "client-pay " + c.name, () -> {
            if (sim.now >= cfg.horizonMs()) return;                       // the horizon has passed: stop generating load
            String key = "key-" + (issued[0]++);
            Money amount = Money.ofMinor(100 + rng.nextInt(50_000), Currency.USD);
            c.pay(new PaymentRequest(key, "cust-" + rng.nextInt(200), amount));
            scheduleNextPayment(sim, c, rng, cfg, issued);
        });
    }

    /**
     * Safety: true at every instant, so checked during the run. Each violation is "[invariant] detail".
     * Note what is NOT here: "every approval is posted". An approval whose response is still on the wire is not a bug,
     * so that property is only checkable at quiescence (checkQuiescent).
     */
    static List<String> checkSafety(Gateway gateway, Processor processor, List<Client> clients) {
        List<String> v = new ArrayList<>();
        Ledger ledger = gateway.ledger;
        Map<String, Integer> approvals = processor.approvalsPerPayment();

        for (Map.Entry<String, Integer> e : approvals.entrySet()) {
            if (e.getValue() != 1) v.add("[no-double-authorization] " + e.getKey() + " approved " + e.getValue() + " times by the processor");
        }
        for (String paymentId : approvals.keySet()) {
            int n = ledger.countEntries(paymentId);
            if (n > 1) v.add("[approval-posted-once] " + paymentId + " posted " + n + " times");
        }
        for (JournalEntry e : ledger.journal) {
            if (!approvals.containsKey(e.entryId())) v.add("[approval-posted-once] " + e.entryId() + " posted without a processor approval");
        }
        Money approvedAtGateway = Money.zero(Currency.USD);
        for (Payment p : gateway.payments.values()) {
            if (p.status == Status.APPROVED) {
                approvedAtGateway = approvedAtGateway.plus(p.amount);
                if (!approvals.containsKey(p.paymentId)) v.add("[approval-posted-once] " + p.paymentId + " marked APPROVED without an approval");
                if (ledger.countEntries(p.paymentId) != 1) v.add("[approval-posted-once] " + p.paymentId + " APPROVED but posted " + ledger.countEntries(p.paymentId) + " times");
            }
        }
        if (!ledger.trialBalance().isZero()) v.add("[trial-balance] " + ledger.trialBalance());
        Money receivables = ledger.debitTotal(Gateway.RECEIVABLE);
        if (!receivables.equals(approvedAtGateway)) v.add("[money-conserved] receivables " + receivables + " != approved at gateway " + approvedAtGateway);
        Map<String, Integer> perKey = new TreeMap<>();
        for (Payment p : gateway.payments.values()) perKey.merge(p.key, 1, Integer::sum);
        for (Map.Entry<String, Integer> e : perKey.entrySet()) if (e.getValue() != 1) v.add("[one-payment-per-key] " + e.getKey() + " has " + e.getValue() + " payments");
        for (Client c : clients) {
            for (String d : c.disagreements) v.add("[client-gateway-agree] " + c.name + " " + d);
            for (Map.Entry<String, Status> e : c.outcomes.entrySet()) {
                Payment p = gateway.payments.get(gateway.idempotencyKeys.get(e.getKey()));
                if (p == null || p.status != e.getValue()) v.add("[client-gateway-agree] " + c.name + " believes " + e.getKey() + " is " + e.getValue() + " but gateway has " + (p == null ? "nothing" : p.status));
            }
        }
        return v;
    }

    /** Quiescence: true only once every message has been delivered or dropped and every fault has ended. */
    static List<String> checkQuiescent(Gateway gateway, Processor processor) {
        List<String> v = new ArrayList<>();
        for (Payment p : gateway.payments.values()) {
            if (p.status == Status.PENDING) v.add("[no-stuck-payments] " + p.paymentId + " still PENDING after quiescence (" + p.attempts + " auth attempts)");
        }
        for (String paymentId : processor.approvalsPerPayment().keySet()) {
            int n = gateway.ledger.countEntries(paymentId);
            if (n != 1) v.add("[approval-posted-once] " + paymentId + " approved by the processor but posted " + n + " times");
        }
        Money approvedAtProcessor = Money.zero(Currency.USD);
        for (AuthResponse a : processor.approvals) approvedAtProcessor = approvedAtProcessor.plus(gateway.payments.get(a.paymentId()).amount);
        Money receivables = gateway.ledger.debitTotal(Gateway.RECEIVABLE);
        if (!receivables.equals(approvedAtProcessor)) v.add("[money-conserved] receivables " + receivables + " != approved at processor " + approvedAtProcessor);
        return v;
    }

    /** Try seeds in order until one fails; the report carries everything needed to replay it. */
    static SimReport findFailingSeed(SimConfig cfg, long firstSeed, int maxSeeds) {
        for (long seed = firstSeed; seed < firstSeed + maxSeeds; seed++) {
            SimReport r = run(cfg.withSeed(seed));
            if (!r.passed()) return r;
        }
        return null;
    }
}

// ---------------------------------------------------------------- Demo ----

public final class Main {
    public static void main(String[] args) {
        System.out.println("== SplitMix64: the same seed is the same universe, on every machine and every run ==");
        Rng rng = new Rng(0);
        System.out.printf("seed 0 first draw = %016x (reference value e220a8397b1dcdaf)%n", rng.nextLong());

        System.out.println("\n== Same seed twice: identical event trace ==");
        SimReport a = Harness.run(SimConfig.standard(1, 48));
        SimReport b = Harness.run(SimConfig.standard(1, 48));
        System.out.println(a.summary());
        System.out.println("fingerprints equal: " + a.fingerprint().equals(b.fingerprint()));

        System.out.println("\n== 480 virtual hours (20 days) of payments under injected faults, seed 2 ==");
        SimReport r = Harness.run(SimConfig.standard(2, 480));
        System.out.println((r.passed() ? "PASS " : "FAIL ") + r.summary());

        System.out.println("\n== Four more seeds, 120 virtual hours each ==");
        for (long seed = 3; seed <= 6; seed++) {
            SimReport s = Harness.run(SimConfig.standard(seed, 120));
            System.out.println((s.passed() ? "PASS " : "FAIL ") + s.summary());
        }

        System.out.println("\n== Plant a defect; the harness finds a failing seed and prints how to replay it ==");
        for (Defect d : new Defect[] {Defect.FRESH_AUTH_KEY_ON_RETRY, Defect.RECOVERY_SKIPS_PENDING}) {
            SimReport failing = Harness.findFailingSeed(SimConfig.standard(1, 24).with(d), 1, 40);
            if (failing == null) { System.out.println(d + ": no failing seed in 40 tries"); continue; }
            System.out.println(d + " -> seed " + failing.seed() + " breaks [" + failing.firstBrokenInvariant() + "]");
            System.out.println("  " + failing.violations().get(0));
            System.out.println("  " + failing.reproduction());
        }
    }
}
