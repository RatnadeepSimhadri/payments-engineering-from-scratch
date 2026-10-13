// Lesson: phases/12-payment-systems-design/01-idempotent-payment-api/docs/en.md
// Designing an Idempotent Payment API
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: IETF draft-ietf-httpapi-idempotency-key-header; Stripe API reference (Idempotent requests);
//          RFC 9110 (HTTP semantics: 409, 422, Retry-After). Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported:
// lessons are self-contained.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency JPY = new Currency("JPY", 0);

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

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Production passes a system clock; tests and the demo pass a FakeClock.
// ---------------------------------------------------------------------------------------------

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private final AtomicLong now;
    FakeClock(long startMillis) { now = new AtomicLong(startMillis); }
    @Override public long nowMillis() { return now.get(); }
    void advance(long millis) { now.addAndGet(millis); }
}

// ---------------------------------------------------------------------------------------------
// The request and its fingerprint. The fingerprint is SHA-256 over a canonical encoding of the
// body, so two requests that mean the same thing hash the same regardless of field order or
// whitespace in the JSON the client actually sent.
// ---------------------------------------------------------------------------------------------

record PaymentRequest(Money amount, String source, String description) {
    PaymentRequest {
        Objects.requireNonNull(amount, "amount");
        Objects.requireNonNull(source, "source");
        Objects.requireNonNull(description, "description");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
    }

    /** Canonical form: fields sorted by name, one `name=value` per line, amount in minor units. */
    String canonical() {
        TreeMap<String, String> fields = new TreeMap<>();
        fields.put("amount", Long.toString(amount.minorUnits()));
        fields.put("currency", amount.currency().code());
        fields.put("description", description);
        fields.put("source", source);
        StringBuilder sb = new StringBuilder();
        fields.forEach((k, v) -> sb.append(k).append('=').append(v).append('\n'));
        return sb.toString();
    }

    String fingerprint() { return Hashing.sha256Hex(canonical()); }
}

final class Hashing {
    static String sha256Hex(String text) {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(text.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder(64);
            for (byte b : digest) sb.append(String.format("%02x", b));
            return sb.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }
}

/** The HTTP-shaped response: status, body, and the two headers this lesson cares about. */
record PaymentResponse(int status, String body, Map<String, String> headers) {
    static final String REPLAYED = "Idempotent-Replayed";
    static final String RETRY_AFTER = "Retry-After";

    static PaymentResponse of(int status, String body) { return new PaymentResponse(status, body, Map.of()); }

    PaymentResponse withHeader(String name, String value) {
        Map<String, String> copy = new LinkedHashMap<>(headers);
        copy.put(name, value);
        return new PaymentResponse(status, body, Map.copyOf(copy));
    }

    boolean replayed() { return "true".equals(headers.get(REPLAYED)); }
    int retryAfterSeconds() { return Integer.parseInt(headers.getOrDefault(RETRY_AFTER, "0")); }
}

// ---------------------------------------------------------------------------------------------
// The processor behind the API. Real ones are remote and can be charged exactly once per
// reference id; FakeProcessor models approval, decline, a crash after the charge landed, and a
// hook that lets a test hold a charge in flight while a concurrent duplicate arrives.
// ---------------------------------------------------------------------------------------------

record ProcessorResult(String referenceId, boolean approved, String code, Money amount) {}

interface Processor {
    ProcessorResult charge(String referenceId, Money amount, String source);
    Optional<ProcessorResult> lookup(String referenceId);
}

/** Thrown by FakeProcessor to simulate the handler process dying after the processor committed. */
final class SimulatedCrash extends RuntimeException {
    SimulatedCrash() { super("process crashed after the processor committed the charge"); }
}

final class FakeProcessor implements Processor {
    private final Map<String, ProcessorResult> charges = new ConcurrentHashMap<>();
    private final AtomicInteger chargeCalls = new AtomicInteger();
    private volatile boolean crashAfterNextCharge = false;
    private volatile Runnable beforeCommit = () -> {};

    @Override public ProcessorResult charge(String referenceId, Money amount, String source) {
        chargeCalls.incrementAndGet();
        beforeCommit.run();
        if (charges.containsKey(referenceId)) {
            // A real processor would answer "duplicate reference" here. We model the common case: it
            // returns the original outcome so the caller can converge.
            return charges.get(referenceId);
        }
        boolean approved = !source.startsWith("tok_decline");
        ProcessorResult result = new ProcessorResult(referenceId, approved, approved ? "approved" : "card_declined", amount);
        charges.put(referenceId, result);
        if (crashAfterNextCharge) {
            crashAfterNextCharge = false;
            throw new SimulatedCrash();
        }
        return result;
    }

    @Override public Optional<ProcessorResult> lookup(String referenceId) { return Optional.ofNullable(charges.get(referenceId)); }

    int chargeCalls() { return chargeCalls.get(); }
    int chargesStored() { return charges.size(); }
    void crashAfterNextCharge() { crashAfterNextCharge = true; }
    void onBeforeCommit(Runnable hook) { beforeCommit = hook; }
}

// ---------------------------------------------------------------------------------------------
// The idempotency store. One record per key, reserved atomically before any side effect.
// ---------------------------------------------------------------------------------------------

enum KeyState { IN_PROGRESS, COMPLETED }

record IdempotencyRecord(String key, String fingerprint, String referenceId, KeyState state,
                         PaymentResponse response, long createdAtMillis, long updatedAtMillis) {
    IdempotencyRecord complete(PaymentResponse response, long now) {
        return new IdempotencyRecord(key, fingerprint, referenceId, KeyState.COMPLETED, response, createdAtMillis, now);
    }
    IdempotencyRecord renewLease(long now) {
        return new IdempotencyRecord(key, fingerprint, referenceId, KeyState.IN_PROGRESS, null, createdAtMillis, now);
    }
}

/** What reserve() decided. Exactly one caller per key gets RESERVED or RECOVER; everyone else gets a verdict. */
enum Reservation { RESERVED, RECOVER, IN_PROGRESS, COMPLETED, FINGERPRINT_MISMATCH }

record ReserveOutcome(Reservation decision, IdempotencyRecord record) {}

final class IdempotencyStore {
    private final Map<String, IdempotencyRecord> records = new HashMap<>();
    private final Clock clock;
    private final long ttlMillis;
    private final long leaseMillis;

    IdempotencyStore(Clock clock, long ttlMillis, long leaseMillis) {
        this.clock = clock;
        this.ttlMillis = ttlMillis;
        this.leaseMillis = leaseMillis;
    }

    /**
     * Atomic check-and-reserve. The whole decision is made under one lock so two concurrent callers
     * with the same key cannot both see "absent" and both proceed to charge.
     */
    synchronized ReserveOutcome reserve(String key, String fingerprint, String referenceId) {
        long now = clock.nowMillis();
        IdempotencyRecord existing = records.get(key);
        if (existing != null && now - existing.createdAtMillis() >= ttlMillis) {
            records.remove(key);                       // expired: the key may be reused with any body
            existing = null;
        }
        if (existing == null) {
            IdempotencyRecord fresh = new IdempotencyRecord(key, fingerprint, referenceId, KeyState.IN_PROGRESS, null, now, now);
            records.put(key, fresh);
            return new ReserveOutcome(Reservation.RESERVED, fresh);
        }
        if (!existing.fingerprint().equals(fingerprint)) {
            return new ReserveOutcome(Reservation.FINGERPRINT_MISMATCH, existing);
        }
        if (existing.state() == KeyState.COMPLETED) {
            return new ReserveOutcome(Reservation.COMPLETED, existing);
        }
        if (now - existing.updatedAtMillis() < leaseMillis) {
            return new ReserveOutcome(Reservation.IN_PROGRESS, existing);   // a live owner is still working
        }
        IdempotencyRecord renewed = existing.renewLease(now);               // owner died: this caller takes over
        records.put(key, renewed);
        return new ReserveOutcome(Reservation.RECOVER, renewed);
    }

    synchronized void complete(String key, PaymentResponse response) {
        IdempotencyRecord existing = records.get(key);
        if (existing == null) throw new IllegalStateException("complete() without reserve(): " + key);
        records.put(key, existing.complete(response, clock.nowMillis()));
    }

    synchronized Optional<IdempotencyRecord> get(String key) { return Optional.ofNullable(records.get(key)); }
    synchronized int size() { return records.size(); }
}

// ---------------------------------------------------------------------------------------------
// The handler: POST /payments modelled in-process.
// ---------------------------------------------------------------------------------------------

final class PaymentsHandler {
    static final long KEY_TTL_MILLIS = 24L * 60 * 60 * 1000;   // 24h
    static final long LEASE_MILLIS = 30_000L;                  // how long an IN_PROGRESS owner is trusted
    static final int RETRY_AFTER_SECONDS = 1;

    private final IdempotencyStore store;
    private final Processor processor;

    PaymentsHandler(IdempotencyStore store, Processor processor) {
        this.store = store;
        this.processor = processor;
    }

    /**
     * Keys are scoped to the merchant (the authenticated API caller). Two merchants may both send
     * "order-1001" and must never see each other's responses, so the store key is merchant + key.
     * The separator is a character the key grammar forbids, so "a:b"+"c" and "a"+"b:c" cannot collide.
     */
    static String scopedKey(String merchantId, String idempotencyKey) {
        return merchantId + "\u001f" + idempotencyKey;
    }

    /** Our reference at the processor is a pure function of the scoped key, so a retry can ask about it. */
    static String referenceIdFor(String scopedKey) {
        return "ref_" + Hashing.sha256Hex("ref:" + scopedKey).substring(0, 16);
    }

    /** POST /payments: the only idempotent operation. */
    PaymentResponse post(String merchantId, String idempotencyKey, PaymentRequest request) {
        if (merchantId == null || merchantId.isBlank()) {
            return PaymentResponse.of(401, "{\"error\":\"unauthenticated\"}");
        }
        if (idempotencyKey == null || idempotencyKey.isBlank()) {
            return PaymentResponse.of(400, "{\"error\":\"idempotency_key_required\"}");
        }
        if (idempotencyKey.length() > 255 || idempotencyKey.chars().anyMatch(Character::isISOControl)) {
            return PaymentResponse.of(400, "{\"error\":\"idempotency_key_invalid\"}");
        }
        String key = scopedKey(merchantId, idempotencyKey);
        String fingerprint = request.fingerprint();
        String referenceId = referenceIdFor(key);
        ReserveOutcome outcome = store.reserve(key, fingerprint, referenceId);
        switch (outcome.decision()) {
            case FINGERPRINT_MISMATCH:
                return PaymentResponse.of(422, "{\"error\":\"idempotency_key_reused_with_different_request\"}");
            case IN_PROGRESS:
                return PaymentResponse.of(409, "{\"error\":\"request_in_progress\"}")
                        .withHeader(PaymentResponse.RETRY_AFTER, Integer.toString(RETRY_AFTER_SECONDS));
            case COMPLETED:
                return outcome.record().response().withHeader(PaymentResponse.REPLAYED, "true");
            case RECOVER:
                return recover(key, request, referenceId);
            case RESERVED:
            default:
                return execute(key, request, referenceId);
        }
    }

    /**
     * GET /payments/{id}: a read. Reads are naturally idempotent (they have no side effect), so they
     * take no key, create no store record, and always return the *current* state, never a stored one.
     * Replaying a cached response here would hide a capture, refund, or dispute that happened since.
     */
    PaymentResponse get(String paymentId) {
        Optional<ProcessorResult> current = processor.lookup(paymentId);
        if (current.isEmpty()) return PaymentResponse.of(404, "{\"error\":\"not_found\"}");
        return new PaymentResponse(200, render(current.get()).body(), Map.of());
    }

    private PaymentResponse execute(String key, PaymentRequest request, String referenceId) {
        ProcessorResult result = processor.charge(referenceId, request.amount(), request.source());
        PaymentResponse response = render(result);
        store.complete(key, response);     // only reached if the process survived the charge
        return response;
    }

    /** The previous owner died between charge() and complete(). Ask before charging again. */
    private PaymentResponse recover(String key, PaymentRequest request, String referenceId) {
        Optional<ProcessorResult> prior = processor.lookup(referenceId);
        ProcessorResult result = prior.orElseGet(() -> processor.charge(referenceId, request.amount(), request.source()));
        PaymentResponse response = render(result);
        store.complete(key, response);
        return response;
    }

    private static PaymentResponse render(ProcessorResult result) {
        String body = "{\"id\":\"" + result.referenceId() + "\",\"status\":\"" + (result.approved() ? "succeeded" : "failed")
                + "\",\"code\":\"" + result.code() + "\",\"amount\":\"" + result.amount() + "\"}";
        return PaymentResponse.of(result.approved() ? 201 : 402, body);
    }
}

public final class Main {
    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        FakeProcessor processor = new FakeProcessor();
        IdempotencyStore store = new IdempotencyStore(clock, PaymentsHandler.KEY_TTL_MILLIS, PaymentsHandler.LEASE_MILLIS);
        PaymentsHandler api = new PaymentsHandler(store, processor);
        String acme = "merchant_acme";

        PaymentRequest order = new PaymentRequest(Money.parse("19.99", Currency.USD), "tok_visa_4242", "Order 1001");
        System.out.println("== Fingerprint: SHA-256 of the canonical body ==");
        System.out.print(order.canonical());
        System.out.println("fingerprint = " + order.fingerprint());

        System.out.println("\n== First call charges; the duplicate is replayed, not re-charged ==");
        PaymentResponse first = api.post(acme, "key-1001", order);
        System.out.println("POST #1 -> " + first.status() + " " + first.body() + " " + first.headers());
        PaymentResponse second = api.post(acme, "key-1001", order);
        System.out.println("POST #2 -> " + second.status() + " " + second.body() + " " + second.headers());
        System.out.println("processor charge calls = " + processor.chargeCalls());

        System.out.println("\n== Same key, different body: refuse, do not guess ==");
        PaymentRequest changed = new PaymentRequest(Money.parse("29.99", Currency.USD), "tok_visa_4242", "Order 1001");
        PaymentResponse mismatch = api.post(acme, "key-1001", changed);
        System.out.println("POST #3 -> " + mismatch.status() + " " + mismatch.body());

        System.out.println("\n== Same key, different merchant: a different payment ==");
        PaymentResponse otherMerchant = api.post("merchant_globex", "key-1001", order);
        System.out.println("POST by globex -> " + otherMerchant.status() + " replayed=" + otherMerchant.replayed()
                + " ref=" + PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey("merchant_globex", "key-1001")));
        System.out.println("processor charge calls = " + processor.chargeCalls());

        System.out.println("\n== Crash after the processor committed, before we stored the response ==");
        PaymentRequest order2 = new PaymentRequest(Money.parse("45.00", Currency.USD), "tok_visa_4242", "Order 1002");
        processor.crashAfterNextCharge();
        try {
            api.post(acme, "key-1002", order2);
        } catch (SimulatedCrash e) {
            System.out.println("POST #4 -> " + e.getMessage());
        }
        PaymentResponse tooSoon = api.post(acme, "key-1002", order2);
        System.out.println("retry at +0s   -> " + tooSoon.status() + " Retry-After=" + tooSoon.retryAfterSeconds() + " (owner lease still live)");
        clock.advance(PaymentsHandler.LEASE_MILLIS);
        PaymentResponse recovered = api.post(acme, "key-1002", order2);
        System.out.println("retry at +30s  -> " + recovered.status() + " " + recovered.body());
        System.out.println("processor charge calls = " + processor.chargeCalls() + ", charges stored = " + processor.chargesStored()
                + "  (recovery looked up " + PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey(acme, "key-1002")) + " instead of charging again)");

        System.out.println("\n== Keys expire after 24h ==");
        clock.advance(PaymentsHandler.KEY_TTL_MILLIS);
        PaymentResponse afterTtl = api.post(acme, "key-1001", order);
        System.out.println("POST key-1001 at +24h -> " + afterTtl.status() + " replayed=" + afterTtl.replayed()
                + " (new charge: " + processor.chargeCalls() + " calls total)");

        System.out.println("\n== Declines are stored and replayed too ==");
        PaymentRequest declined = new PaymentRequest(Money.parse("10.00", Currency.USD), "tok_decline_0002", "Order 1003");
        PaymentResponse d1 = api.post(acme, "key-1003", declined);
        PaymentResponse d2 = api.post(acme, "key-1003", declined);
        System.out.println("POST #1 -> " + d1.status() + ", POST #2 -> " + d2.status() + " replayed=" + d2.replayed());

        System.out.println("\n== Reads are not idempotent operations: no key, no stored response ==");
        String id = PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey(acme, "key-1002"));
        PaymentResponse read = api.get(id);
        System.out.println("GET /payments/" + id + " -> " + read.status() + " " + read.body());
        System.out.println("GET /payments/ref_missing -> " + api.get("ref_missing").status()
                + "; store records = " + store.size() + " (unchanged by reads)");
    }
}
