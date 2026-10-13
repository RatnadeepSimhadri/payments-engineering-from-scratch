// Project: projects/idempotent-payment-gateway — Stage 01: idempotency store.
// Keyed by (merchantId, idempotencyKey); every record carries the SHA-256 fingerprint of the canonical
// request so a reused key with a different body is detected instead of silently replayed.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;

/** SHA-256 over a canonical string, rendered as 64 lowercase hex characters. */
final class Fingerprint {
    private Fingerprint() {}

    static String sha256Hex(String canonical) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] bytes = digest.digest(canonical.getBytes(StandardCharsets.UTF_8));
            StringBuilder hex = new StringBuilder(bytes.length * 2);
            for (byte b : bytes) hex.append(String.format("%02x", b));
            return hex.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is mandatory in every JDK", e);
        }
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

    /** "1999\nUSD\ntok_visa\norder-42": the fields that define the intent, in a fixed order. The key is not part of it. */
    String canonical() {
        return amount.minorUnits() + "\n" + amount.currency().code() + "\n" + source + "\n" + description;
    }

    String fingerprint() {
        return Fingerprint.sha256Hex(canonical());
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

/**
 * In-memory idempotency store. All public methods are synchronized so that concurrent begin() calls with
 * the same key admit exactly one caller; the rest see IN_PROGRESS.
 */
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

    /** Claim the key for this request or report why the caller must not proceed. */
    synchronized BeginResult begin(String merchantId, String key, String fingerprint) {
        Objects.requireNonNull(fingerprint, "fingerprint");
        StoreKey storeKey = new StoreKey(merchantId, key);
        long now = clock.nowMillis();
        IdempotencyRecord existing = records.get(storeKey);
        if (existing != null && existing.isExpired(now)) {
            records.remove(storeKey);
            existing = null;
        }
        if (existing == null) {
            records.put(storeKey, new IdempotencyRecord(merchantId, key, fingerprint, KeyState.IN_PROGRESS, null, now, now + ttlMillis));
            return BeginResult.of(Decision.NEW);
        }
        if (!existing.fingerprint().equals(fingerprint)) return BeginResult.of(Decision.CONFLICT);
        if (existing.state() == KeyState.IN_PROGRESS) return BeginResult.of(Decision.IN_PROGRESS);
        return new BeginResult(Decision.REPLAY, existing.response());
    }

    /** Store the response for an IN_PROGRESS key. Completing twice, or a key never begun, is a bug. */
    synchronized void complete(String merchantId, String key, ApiResponse response) {
        Objects.requireNonNull(response, "response");
        StoreKey storeKey = new StoreKey(merchantId, key);
        IdempotencyRecord existing = records.get(storeKey);
        if (existing == null || existing.isExpired(clock.nowMillis())) {
            throw new IllegalStateException("no in-progress record for " + merchantId + "/" + key);
        }
        if (existing.state() == KeyState.COMPLETED) {
            throw new IllegalStateException("record already completed for " + merchantId + "/" + key);
        }
        records.put(storeKey, new IdempotencyRecord(merchantId, key, existing.fingerprint(), KeyState.COMPLETED,
                new ApiResponse(response.status(), response.body()), existing.createdAt(), existing.expiresAt()));
    }

    /** Give the key back after a failure that must not be replayed (timeout, circuit open). Completed keys are kept. */
    synchronized boolean release(String merchantId, String key) {
        StoreKey storeKey = new StoreKey(merchantId, key);
        IdempotencyRecord existing = records.get(storeKey);
        if (existing == null) return false;
        if (existing.state() == KeyState.COMPLETED) {
            throw new IllegalStateException("cannot release a completed record for " + merchantId + "/" + key);
        }
        records.remove(storeKey);
        return true;
    }

    /** The live record, if any; expired records read as absent. */
    synchronized Optional<IdempotencyRecord> get(String merchantId, String key) {
        IdempotencyRecord existing = records.get(new StoreKey(merchantId, key));
        if (existing == null || existing.isExpired(clock.nowMillis())) return Optional.empty();
        return Optional.of(existing);
    }

    /** Drop every expired record; returns how many were removed. */
    synchronized int purgeExpired() {
        long now = clock.nowMillis();
        int removed = 0;
        Iterator<IdempotencyRecord> it = records.values().iterator();
        while (it.hasNext()) {
            if (it.next().isExpired(now)) {
                it.remove();
                removed++;
            }
        }
        return removed;
    }

    /** Records held, expired or not; call purgeExpired() first for the live count. */
    synchronized int size() {
        return records.size();
    }
}
