// Lesson: phases/05-distributed-foundations/09-idempotency-keys/docs/en.md
// Idempotency Keys: Exactly-Once Effects on At-Least-Once Networks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Stripe API reference, "Idempotent requests"; IETF draft-ietf-httpapi-idempotency-key-header;
// RFC 9110 section 9.2.2 (idempotent methods). Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HashMap;
import java.util.Map;
import java.util.Objects;

/** A clock the store reads for TTL and lease decisions. Tests use FakeClock so expiry is replayable. */
interface Clock {
    long nowMs();
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMs) { this.now = startMs; }

    @Override public long nowMs() { return now; }

    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("cannot advance a negative duration");
        now += ms;
    }
}

/** ISO 4217 currency, copied from Phase 10 Lesson 01. Amounts are integer minor units, never floats. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public String toString() {
        if (currency.exponent() == 0) return minorUnits + " " + currency.code();
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = String.valueOf(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/**
 * One POST /v1/charges as the server sees it. The idempotency key is chosen by the client; the fingerprint
 * is computed by the server from the parts of the request that have an effect. Two requests with the same
 * key and different fingerprints are a client bug, not a retry.
 */
record ChargeRequest(String accountId, String idempotencyKey, Money amount, String cardToken, String description) {
    ChargeRequest {
        Objects.requireNonNull(accountId, "accountId");
        Objects.requireNonNull(amount, "amount");
        Objects.requireNonNull(cardToken, "cardToken");
        if (idempotencyKey != null && (idempotencyKey.isBlank() || idempotencyKey.length() > 255)) {
            throw new IllegalArgumentException("Idempotency-Key must be 1..255 characters");
        }
        if (amount.minorUnits() <= 0) throw new IllegalArgumentException("amount must be positive");
    }

    /** Canonical form: fixed field order, one field per line, so a reordered JSON body hashes the same. */
    String canonical() {
        return "POST /v1/charges\n" + amount.minorUnits() + "\n" + amount.currency().code() + "\n"
                + cardToken + "\n" + (description == null ? "" : description);
    }

    String fingerprint() {
        return Fingerprint.sha256Hex(canonical());
    }
}

final class Fingerprint {
    private Fingerprint() {}

    static String sha256Hex(String text) {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(text.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder(64);
            for (byte b : digest) sb.append(String.format("%02x", b));
            return sb.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is mandatory in every JRE", e);
        }
    }
}

/** What the client receives. `replayed` is the Idempotent-Replayed: true header a replay carries. */
record Response(int status, String body, boolean replayed) {
    static Response of(int status, String body) { return new Response(status, body, false); }

    Response asReplay() { return new Response(status, body, true); }
}

/** The four answers the store can give when a request arrives. */
enum Outcome {
    NEW,          // no usable entry: the caller must execute and then complete() or abandon()
    REPLAY,       // a completed entry with the same fingerprint: return its response, do not execute
    IN_PROGRESS,  // another request with this key is executing right now: 409, retry shortly
    CONFLICT      // same key, different fingerprint: 422, the client reused a key for a different request
}

record Begin(Outcome outcome, Response response) {
    static Begin of(Outcome outcome) { return new Begin(outcome, null); }
}

/** One row of the store. A row is IN_PROGRESS until complete() stores a response. */
final class Entry {
    final String fingerprint;
    final long startedAtMs;
    Response response;       // null while in progress
    long expiresAtMs;        // meaningful once completed

    Entry(String fingerprint, long startedAtMs) {
        this.fingerprint = fingerprint;
        this.startedAtMs = startedAtMs;
    }

    boolean inProgress() { return response == null; }
}

/**
 * The idempotency store. Keyed by (account, idempotency key); each entry remembers the request fingerprint
 * and, once the handler has finished, the response to replay. Single monitor: begin() is the atomic
 * check-and-reserve that makes concurrent duplicates safe, and it holds the lock for a map lookup only.
 */
final class IdempotencyStore {
    private final Map<String, Entry> entries = new HashMap<>();
    private final Clock clock;
    private final long ttlMs;
    private final long inProgressLeaseMs;

    IdempotencyStore(Clock clock, long ttlMs, long inProgressLeaseMs) {
        if (ttlMs <= 0 || inProgressLeaseMs <= 0) throw new IllegalArgumentException("ttl and lease must be positive");
        this.clock = clock;
        this.ttlMs = ttlMs;
        this.inProgressLeaseMs = inProgressLeaseMs;
    }

    private static String scoped(String accountId, String key) { return accountId + "\u0000" + key; }

    /**
     * Atomically: look up the key; if there is a live entry decide REPLAY / IN_PROGRESS / CONFLICT;
     * otherwise reserve the key for this caller and answer NEW.
     */
    synchronized Begin begin(String accountId, String key, String fingerprint) {
        long now = clock.nowMs();
        String k = scoped(accountId, key);
        Entry e = entries.get(k);
        if (e != null) {
            if (!e.inProgress() && now >= e.expiresAtMs) {
                entries.remove(k);                                    // TTL passed: forget it
                e = null;
            } else if (e.inProgress() && now - e.startedAtMs >= inProgressLeaseMs) {
                entries.remove(k);                                    // the first attempt died: let a retry take over
                e = null;
            }
        }
        if (e != null) {
            if (!e.fingerprint.equals(fingerprint)) return Begin.of(Outcome.CONFLICT);
            if (e.inProgress()) return Begin.of(Outcome.IN_PROGRESS);
            return new Begin(Outcome.REPLAY, e.response.asReplay());
        }
        entries.put(k, new Entry(fingerprint, now));
        return Begin.of(Outcome.NEW);
    }

    /** Store the handler's response, whatever its status, and start the TTL. */
    synchronized void complete(String accountId, String key, Response response) {
        Entry e = entries.get(scoped(accountId, key));
        if (e == null || !e.inProgress()) throw new IllegalStateException("complete without a live reservation: " + key);
        e.response = response;
        e.expiresAtMs = clock.nowMs() + ttlMs;
    }

    /** The handler crashed before producing a response: release the reservation so a retry can execute. */
    synchronized void abandon(String accountId, String key) {
        Entry e = entries.get(scoped(accountId, key));
        if (e != null && e.inProgress()) entries.remove(scoped(accountId, key));
    }

    /** Remove every completed entry whose TTL has passed. Returns how many were removed. */
    synchronized int purgeExpired() {
        long now = clock.nowMs();
        int before = entries.size();
        entries.values().removeIf(e -> !e.inProgress() && now >= e.expiresAtMs);
        return before - entries.size();
    }

    synchronized int size() { return entries.size(); }
}

/** The thing with side effects: the card network. One call here is one movement of money. */
interface Processor {
    Response charge(ChargeRequest request);
}

/** Deterministic stand-in: succeeds with ch_1, ch_2, ... and can be told to decline, fail, or crash. */
final class FakeProcessor implements Processor {
    private int executions = 0;
    private int declineNext = 0;
    private int serverErrorNext = 0;
    private int crashNext = 0;

    @Override public synchronized Response charge(ChargeRequest request) {
        executions++;
        if (crashNext > 0) { crashNext--; throw new RuntimeException("processor connection reset"); }
        if (serverErrorNext > 0) { serverErrorNext--; return Response.of(502, "{\"error\":\"processor_unavailable\"}"); }
        if (declineNext > 0) { declineNext--; return Response.of(402, "{\"error\":\"card_declined\"}"); }
        return Response.of(200, "{\"id\":\"ch_" + executions + "\",\"amount\":\"" + request.amount() + "\",\"status\":\"succeeded\"}");
    }

    synchronized int executions() { return executions; }

    synchronized void declineNext() { declineNext++; }

    synchronized void serverErrorNext() { serverErrorNext++; }

    synchronized void crashNext() { crashNext++; }
}

/**
 * The endpoint. Without a key every call executes. With a key: reserve, execute, store; replay on retry;
 * 409 while another attempt is running; 422 when the key is reused with a different body.
 */
final class PaymentsApi {
    private final IdempotencyStore store;
    private final Processor processor;

    PaymentsApi(IdempotencyStore store, Processor processor) {
        this.store = store;
        this.processor = processor;
    }

    Response charge(ChargeRequest request) {
        if (request.idempotencyKey() == null) return processor.charge(request);
        String key = request.idempotencyKey();
        Begin b = store.begin(request.accountId(), key, request.fingerprint());
        switch (b.outcome()) {
            case REPLAY -> { return b.response(); }
            case IN_PROGRESS -> { return Response.of(409, "{\"error\":\"idempotency_key_in_progress\"}"); }
            case CONFLICT -> { return Response.of(422, "{\"error\":\"idempotency_key_reused_with_different_request\"}"); }
            case NEW -> { /* fall through to execute */ }
        }
        Response response;
        try {
            response = processor.charge(request);
        } catch (RuntimeException crash) {
            store.abandon(request.accountId(), key);
            throw crash;
        }
        store.complete(request.accountId(), key, response);
        return response;
    }
}

public final class Main {
    static final long DAY_MS = 24L * 60 * 60 * 1000;

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        FakeProcessor processor = new FakeProcessor();
        IdempotencyStore store = new IdempotencyStore(clock, DAY_MS, 30_000);
        PaymentsApi api = new PaymentsApi(store, processor);
        ChargeRequest req = new ChargeRequest("acct_1", "order-8812-attempt", Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 8812");

        System.out.println("== Without a key: a retry is a second charge ==");
        ChargeRequest noKey = new ChargeRequest("acct_1", null, Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 8812");
        api.charge(noKey);
        api.charge(noKey);
        System.out.printf("two sends of the same request executed %d charges%n", processor.executions());

        System.out.println("\n== With a key: the retry replays the stored response ==");
        System.out.println("fingerprint = " + req.fingerprint().substring(0, 16) + "...");
        Response first = api.charge(req);
        System.out.printf("first:  %d %s replayed=%s%n", first.status(), first.body(), first.replayed());
        Response retry = api.charge(req);
        System.out.printf("retry:  %d %s replayed=%s%n", retry.status(), retry.body(), retry.replayed());
        System.out.printf("processor executions so far: %d (the keyed pair added exactly one)%n", processor.executions());

        System.out.println("\n== Same key, different amount: 422, nothing executes ==");
        ChargeRequest tampered = new ChargeRequest("acct_1", "order-8812-attempt", Money.ofMinor(2999, Currency.USD), "tok_visa_4242", "Order 8812");
        Response conflict = api.charge(tampered);
        System.out.printf("conflict: %d %s%n", conflict.status(), conflict.body());

        System.out.println("\n== Concurrent duplicates: one wins, the other sees 409 until it is done ==");
        String fp = req.fingerprint();
        Begin a = store.begin("acct_1", "order-9001", fp);
        Begin b = store.begin("acct_1", "order-9001", fp);
        System.out.printf("request A begin -> %s ; request B begin -> %s%n", a.outcome(), b.outcome());
        store.complete("acct_1", "order-9001", Response.of(200, "{\"id\":\"ch_9001\"}"));
        Begin bRetry = store.begin("acct_1", "order-9001", fp);
        System.out.printf("A completes; B retries -> %s with body %s%n", bRetry.outcome(), bRetry.response().body());

        System.out.println("\n== Declines are responses too: they replay ==");
        processor.declineNext();
        ChargeRequest declined = new ChargeRequest("acct_1", "order-7001", Money.ofMinor(5000, Currency.USD), "tok_visa_0002", null);
        Response d1 = api.charge(declined);
        Response d2 = api.charge(declined);
        System.out.printf("first %d, retry %d replayed=%s, executions for this key: 1%n", d1.status(), d2.status(), d2.replayed());

        System.out.println("\n== A crash mid-execution releases the reservation; the retry executes ==");
        processor.crashNext();
        ChargeRequest crashy = new ChargeRequest("acct_1", "order-7002", Money.ofMinor(1200, Currency.USD), "tok_visa_4242", null);
        int before = processor.executions();
        try { api.charge(crashy); } catch (RuntimeException e) { System.out.println("first attempt: " + e.getMessage()); }
        Response recovered = api.charge(crashy);
        System.out.printf("retry: %d, executions for this key: %d (the hazard: did the first attempt move money?)%n", recovered.status(), processor.executions() - before);

        System.out.println("\n== TTL: after 24 h the key is forgotten and a retry executes again ==");
        System.out.printf("entries before purge: %d%n", store.size());
        clock.advance(DAY_MS);
        System.out.printf("purged %d expired entries; entries now: %d%n", store.purgeExpired(), store.size());
        Response late = api.charge(req);
        System.out.printf("same request after TTL: %d replayed=%s -> a new charge %s%n", late.status(), late.replayed(), late.body());
    }
}
