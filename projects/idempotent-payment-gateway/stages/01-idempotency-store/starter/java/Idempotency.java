// Project: projects/idempotent-payment-gateway — Stage 01: idempotency store (STARTER).
// The data types are given. Every method marked TODO throws UnsupportedOperationException until you
// implement it; the Stage 01 tests fail visibly until then. Keep the names: the tests import them.
// Java 21, standard library only.

import java.util.HashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;

/** SHA-256 over a canonical string, rendered as 64 lowercase hex characters. */
final class Fingerprint {
    private Fingerprint() {}

    /** TODO: java.security.MessageDigest "SHA-256" over the UTF-8 bytes, hex-encoded in lowercase. */
    static String sha256Hex(String canonical) {
        throw new UnsupportedOperationException("TODO stage 01: Fingerprint.sha256Hex");
    }
}

/**
 * A merchant's request to charge a customer. The idempotency key identifies the attempt; the canonical
 * form (amount, currency, source, description joined by newlines) identifies the intent.
 */
record ChargeRequest(String merchantId, String idempotencyKey, Money amount, String source, String description) {
    ChargeRequest {
        requireText(merchantId, "merchantId");
        requireText(idempotencyKey, "idempotencyKey");
        requireText(source, "source");
        Objects.requireNonNull(amount, "amount");
        Objects.requireNonNull(description, "description");
        if (source.contains("\n") || description.contains("\n")) {
            throw new IllegalArgumentException("source and description must not contain newlines");
        }
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
    }

    private static void requireText(String value, String field) {
        if (value == null || value.isBlank()) throw new IllegalArgumentException(field + " is required");
    }

    /** TODO: "<minorUnits>\n<currency code>\n<source>\n<description>". The key is NOT part of it. */
    String canonical() {
        throw new UnsupportedOperationException("TODO stage 01: ChargeRequest.canonical");
    }

    /** TODO: Fingerprint.sha256Hex(canonical()). */
    String fingerprint() {
        throw new UnsupportedOperationException("TODO stage 01: ChargeRequest.fingerprint");
    }
}

/** What the gateway returned to the merchant; the store keeps it so a replay returns the same bytes. */
record ApiResponse(int status, String body, boolean replayed) {
    ApiResponse(int status, String body) {
        this(status, body, false);
    }

    ApiResponse asReplay() {
        return new ApiResponse(status, body, true);
    }
}

enum KeyState { IN_PROGRESS, COMPLETED }

enum Decision { NEW, REPLAY, IN_PROGRESS, CONFLICT }

/** Outcome of begin(): the response is present only for REPLAY. */
record BeginResult(Decision decision, ApiResponse response) {
    static BeginResult of(Decision decision) {
        return new BeginResult(decision, null);
    }
}

record IdempotencyRecord(String merchantId, String key, String fingerprint, KeyState state,
                         ApiResponse response, long createdAt, long expiresAt) {
    boolean isExpired(long now) {
        return now >= expiresAt;
    }
}

/** In-memory idempotency store keyed by (merchantId, key). Records expire ttlMillis after creation. */
final class IdempotencyStore {
    private record StoreKey(String merchantId, String key) {}

    private final Clock clock;
    private final long ttlMillis;
    private final Map<StoreKey, IdempotencyRecord> records = new HashMap<>();

    IdempotencyStore(Clock clock, long ttlMillis) {
        this.clock = Objects.requireNonNull(clock, "clock");
        if (ttlMillis <= 0) throw new IllegalArgumentException("ttlMillis must be positive: " + ttlMillis);
        this.ttlMillis = ttlMillis;
    }

    long ttlMillis() {
        return ttlMillis;
    }

    /**
     * TODO: Claim the key for this request or report why the caller must not proceed.
     * No live record -> insert IN_PROGRESS (expired records count as absent) and return NEW.
     * Different fingerprint -> CONFLICT. Same fingerprint and IN_PROGRESS -> IN_PROGRESS.
     * Same fingerprint and COMPLETED -> REPLAY with the stored response.
     */
    synchronized BeginResult begin(String merchantId, String key, String fingerprint) {
        throw new UnsupportedOperationException("TODO stage 01: IdempotencyStore.begin");
    }

    /** TODO: Store the response (replayed=false) for an IN_PROGRESS key. Unknown, expired or already completed -> IllegalStateException. */
    synchronized void complete(String merchantId, String key, ApiResponse response) {
        throw new UnsupportedOperationException("TODO stage 01: IdempotencyStore.complete");
    }

    /** TODO: Drop an IN_PROGRESS record so the key can be used again; false if absent; IllegalStateException if COMPLETED. */
    synchronized boolean release(String merchantId, String key) {
        throw new UnsupportedOperationException("TODO stage 01: IdempotencyStore.release");
    }

    /** TODO: The live record, if any; expired records read as absent. */
    synchronized Optional<IdempotencyRecord> get(String merchantId, String key) {
        throw new UnsupportedOperationException("TODO stage 01: IdempotencyStore.get");
    }

    /** TODO: Drop every expired record; return how many were removed. */
    synchronized int purgeExpired() {
        throw new UnsupportedOperationException("TODO stage 01: IdempotencyStore.purgeExpired");
    }

    /** Records held, expired or not; call purgeExpired() first for the live count. */
    synchronized int size() {
        return records.size();
    }
}
