// Lesson: phases/12-payment-systems-design/05-webhooks-and-callbacks/docs/en.md
// Webhooks: Signing, Delivery, Ordering, and Replay
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 2104 (HMAC); FIPS 180-4 (SHA-256); Stripe "Check the webhook signatures" docs for the
//          t=...,v1=... header shape. Java 21, standard library only (javax.crypto for HMAC).

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

// ---------------------------------------------------------------------------------------------
// Time is injected. The demo and the tests drive a FakeClock in whole seconds; nothing sleeps.
// ---------------------------------------------------------------------------------------------
interface Clock { long nowSeconds(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long startSeconds) { now = startSeconds; }
    @Override public long nowSeconds() { return now; }
    void advance(long seconds) {
        if (seconds < 0) throw new IllegalArgumentException("clocks do not run backwards in this lesson");
        now += seconds;
    }
}

// ---------------------------------------------------------------------------------------------
// Step 1: the event. The id is the dedupe key; the sequence is the per-object ordering key.
// ---------------------------------------------------------------------------------------------
record Event(String id, String type, String objectId, long sequence, long createdAt, String status) {
    /** A fixed-shape JSON document. Keys are always emitted in this order so the bytes are deterministic. */
    String toJson() {
        return "{\"id\":\"" + id + "\",\"type\":\"" + type + "\",\"object\":\"" + objectId
                + "\",\"sequence\":" + sequence + ",\"created\":" + createdAt + ",\"status\":\"" + status + "\"}";
    }

    /** Parses exactly the shape toJson emits. Not a general JSON parser; the lesson is not about parsing. */
    static Event fromJson(String json) {
        return new Event(field(json, "id"), field(json, "type"), field(json, "object"),
                Long.parseLong(field(json, "sequence")), Long.parseLong(field(json, "created")), field(json, "status"));
    }

    private static String field(String json, String key) {
        String needle = "\"" + key + "\":";
        int at = json.indexOf(needle);
        if (at < 0) throw new IllegalArgumentException("missing field " + key);
        int start = at + needle.length();
        if (json.charAt(start) == '"') {
            int end = json.indexOf('"', start + 1);
            return json.substring(start + 1, end);
        }
        int end = start;
        while (end < json.length() && (Character.isDigit(json.charAt(end)) || json.charAt(end) == '-')) end++;
        return json.substring(start, end);
    }
}

// ---------------------------------------------------------------------------------------------
// Step 2: signing. HMAC-SHA256 over "<timestamp>.<payload>" with a shared secret (RFC 2104).
// The header shape follows the widely copied Stripe scheme: t=<unix seconds>,v1=<hex mac>.
// ---------------------------------------------------------------------------------------------
final class WebhookSigner {
    static final String HEADER_NAME = "Webhook-Signature";
    private final byte[] secret;
    private final Clock clock;

    WebhookSigner(String secret, Clock clock) {
        if (secret == null || secret.length() < 16) throw new IllegalArgumentException("secret must be at least 16 characters");
        this.secret = secret.getBytes(StandardCharsets.UTF_8);
        this.clock = clock;
    }

    /** The bytes that are authenticated. Binding the timestamp into the MAC is what makes replay detectable. */
    static String signedPayload(long timestamp, String payload) { return timestamp + "." + payload; }

    static byte[] hmacSha256(byte[] key, byte[] message) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(key, "HmacSHA256"));
            return mac.doFinal(message);
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("HmacSHA256 is mandatory in every Java runtime", e);
        }
    }

    static String hex(byte[] bytes) {
        StringBuilder sb = new StringBuilder(bytes.length * 2);
        for (byte b : bytes) sb.append(Character.forDigit((b >> 4) & 0xF, 16)).append(Character.forDigit(b & 0xF, 16));
        return sb.toString();
    }

    String signature(long timestamp, String payload) {
        return hex(hmacSha256(secret, signedPayload(timestamp, payload).getBytes(StandardCharsets.UTF_8)));
    }

    /** Header value for a payload signed now. */
    String header(String payload) {
        long ts = clock.nowSeconds();
        return "t=" + ts + ",v1=" + signature(ts, payload);
    }
}

// ---------------------------------------------------------------------------------------------
// Step 3: verification. Parse the header, check freshness, recompute, compare in constant time.
// ---------------------------------------------------------------------------------------------
enum Verification { OK, MALFORMED_HEADER, STALE_TIMESTAMP, BAD_SIGNATURE }

final class WebhookVerifier {
    static final long DEFAULT_TOLERANCE_SECONDS = 300;
    private final byte[] secret;
    private final Clock clock;
    private final long toleranceSeconds;

    WebhookVerifier(String secret, Clock clock, long toleranceSeconds) {
        this.secret = secret.getBytes(StandardCharsets.UTF_8);
        this.clock = clock;
        this.toleranceSeconds = toleranceSeconds;
    }

    WebhookVerifier(String secret, Clock clock) { this(secret, clock, DEFAULT_TOLERANCE_SECONDS); }

    /** Parses "t=123,v1=abc,v1=def" into timestamp and candidate signatures. Returns null when malformed. */
    static ParsedHeader parse(String header) {
        if (header == null || header.isEmpty()) return null;
        Long ts = null;
        List<String> sigs = new ArrayList<>();
        for (String part : header.split(",")) {
            int eq = part.indexOf('=');
            if (eq <= 0) return null;
            String k = part.substring(0, eq).trim(), v = part.substring(eq + 1).trim();
            if (k.equals("t")) {
                try { ts = Long.parseLong(v); } catch (NumberFormatException e) { return null; }
            } else if (k.equals("v1")) {
                if (!v.matches("[0-9a-f]{64}")) return null;
                sigs.add(v);
            }
            // unknown schemes (v0, v2) are ignored so the producer can rotate schemes without breaking consumers
        }
        if (ts == null || sigs.isEmpty()) return null;
        return new ParsedHeader(ts, List.copyOf(sigs));
    }

    record ParsedHeader(long timestamp, List<String> signatures) {}

    /** Byte-wise comparison whose running time does not depend on where the first difference is. */
    static boolean constantTimeEquals(byte[] a, byte[] b) {
        if (a.length != b.length) return false;
        int diff = 0;
        for (int i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
        return diff == 0;
    }

    Verification verify(String payload, String header) {
        ParsedHeader parsed = parse(header);
        if (parsed == null) return Verification.MALFORMED_HEADER;
        long age = clock.nowSeconds() - parsed.timestamp();
        if (Math.abs(age) > toleranceSeconds) return Verification.STALE_TIMESTAMP;
        byte[] expected = WebhookSigner.hmacSha256(secret,
                WebhookSigner.signedPayload(parsed.timestamp(), payload).getBytes(StandardCharsets.UTF_8));
        for (String candidate : parsed.signatures()) {
            if (constantTimeEquals(expected, unhex(candidate))) return Verification.OK;
        }
        return Verification.BAD_SIGNATURE;
    }

    static byte[] unhex(String hex) {
        byte[] out = new byte[hex.length() / 2];
        for (int i = 0; i < out.length; i++) out[i] = (byte) Integer.parseInt(hex.substring(2 * i, 2 * i + 2), 16);
        return out;
    }
}

// ---------------------------------------------------------------------------------------------
// Step 4: delivery. At-least-once with a backoff schedule and a dead letter after the last attempt.
// ---------------------------------------------------------------------------------------------
interface Endpoint {
    /** Returns an HTTP-style status code. Anything outside 200-299 is a failure to be retried. */
    int receive(String payload, Map<String, String> headers);
}

/** An endpoint whose failures are scripted per attempt, so a retry schedule can be tested exactly. */
final class FakeEndpoint implements Endpoint {
    final List<String> received = new ArrayList<>();
    final List<Map<String, String>> receivedHeaders = new ArrayList<>();
    private final Deque<Integer> scriptedStatuses = new ArrayDeque<>();
    int calls = 0;

    /** The next calls return these statuses in order; after the script is exhausted every call returns 200. */
    FakeEndpoint failWith(int... statuses) {
        for (int s : statuses) scriptedStatuses.add(s);
        return this;
    }

    @Override public int receive(String payload, Map<String, String> headers) {
        calls++;
        int status = scriptedStatuses.isEmpty() ? 200 : scriptedStatuses.poll();
        if (status >= 200 && status < 300) {
            received.add(payload);
            receivedHeaders.add(headers);
        }
        return status;
    }
}

enum DeliveryState { PENDING, DELIVERED, DEAD_LETTERED }

final class Delivery {
    final Event event;
    final String payload;
    DeliveryState state = DeliveryState.PENDING;
    int attempts = 0;
    long nextAttemptAt;
    final List<Integer> statuses = new ArrayList<>();
    final List<Long> attemptTimes = new ArrayList<>();

    Delivery(Event event, long firstAttemptAt) {
        this.event = event;
        this.payload = event.toJson();
        this.nextAttemptAt = firstAttemptAt;
    }
}

final class WebhookDispatcher {
    /** Seconds to wait after attempt n fails before attempt n+1. Length + 1 = max attempts. */
    static final long[] DEFAULT_BACKOFF_SECONDS = {30, 120, 600, 3600, 21600};
    private final Clock clock;
    private final WebhookSigner signer;
    private final Endpoint endpoint;
    private final long[] backoff;
    final List<Delivery> deliveries = new ArrayList<>();
    final List<Delivery> deadLetters = new ArrayList<>();

    WebhookDispatcher(Clock clock, WebhookSigner signer, Endpoint endpoint, long[] backoffSeconds) {
        this.clock = clock;
        this.signer = signer;
        this.endpoint = endpoint;
        this.backoff = backoffSeconds.clone();
    }

    WebhookDispatcher(Clock clock, WebhookSigner signer, Endpoint endpoint) {
        this(clock, signer, endpoint, DEFAULT_BACKOFF_SECONDS);
    }

    int maxAttempts() { return backoff.length + 1; }

    Delivery enqueue(Event event) {
        Delivery d = new Delivery(event, clock.nowSeconds());
        deliveries.add(d);
        return d;
    }

    /** Attempts every delivery that is due. Signs at send time so the timestamp reflects the retry, not the enqueue. */
    int tick() {
        int attempted = 0;
        for (Delivery d : deliveries) {
            if (d.state != DeliveryState.PENDING || d.nextAttemptAt > clock.nowSeconds()) continue;
            attempted++;
            Map<String, String> headers = new LinkedHashMap<>();
            headers.put(WebhookSigner.HEADER_NAME, signer.header(d.payload));
            headers.put("Webhook-Id", d.event.id());
            headers.put("Webhook-Attempt", Integer.toString(d.attempts + 1));
            int status = endpoint.receive(d.payload, headers);
            d.attempts++;
            d.statuses.add(status);
            d.attemptTimes.add(clock.nowSeconds());
            if (status >= 200 && status < 300) {
                d.state = DeliveryState.DELIVERED;
            } else if (d.attempts >= maxAttempts()) {
                d.state = DeliveryState.DEAD_LETTERED;
                deadLetters.add(d);
            } else {
                d.nextAttemptAt = clock.nowSeconds() + backoff[d.attempts - 1];
            }
        }
        return attempted;
    }

    /** Drives the clock forward to each due time until nothing is pending. Returns the number of ticks that did work. */
    int drain(FakeClock fake) {
        int ticks = 0;
        while (true) {
            long next = Long.MAX_VALUE;
            for (Delivery d : deliveries) if (d.state == DeliveryState.PENDING) next = Math.min(next, d.nextAttemptAt);
            if (next == Long.MAX_VALUE) return ticks;
            if (next > fake.nowSeconds()) fake.advance(next - fake.nowSeconds());
            tick();
            ticks++;
        }
    }
}

// ---------------------------------------------------------------------------------------------
// Step 5: the consumer. Verify, dedupe by event id, and never let an older event overwrite a newer state.
// ---------------------------------------------------------------------------------------------
enum Outcome { ACCEPTED, DUPLICATE, OUT_OF_ORDER, REJECTED }

final class WebhookConsumer {
    private final WebhookVerifier verifier;
    private final Set<String> seenEventIds = new HashSet<>();
    final Map<String, Long> latestSequence = new HashMap<>();
    final Map<String, String> objectStatus = new HashMap<>();
    final List<String> log = new ArrayList<>();

    WebhookConsumer(WebhookVerifier verifier) { this.verifier = verifier; }

    /** Returns the HTTP status the endpoint should answer with, alongside what happened. */
    record Handled(int httpStatus, Outcome outcome, Verification verification) {}

    Handled handle(String payload, Map<String, String> headers) {
        Verification v = verifier.verify(payload, headers.get(WebhookSigner.HEADER_NAME));
        if (v != Verification.OK) {
            log.add("rejected " + v);
            return new Handled(400, Outcome.REJECTED, v);   // 4xx: the producer should not retry a forgery
        }
        Event e = Event.fromJson(payload);
        if (!seenEventIds.add(e.id())) {
            log.add("duplicate " + e.id());
            return new Handled(200, Outcome.DUPLICATE, v);   // 200: tell the producer to stop retrying
        }
        long latest = latestSequence.getOrDefault(e.objectId(), -1L);
        if (e.sequence() <= latest) {
            log.add("out-of-order " + e.id() + " seq " + e.sequence() + " <= " + latest);
            return new Handled(200, Outcome.OUT_OF_ORDER, v);
        }
        latestSequence.put(e.objectId(), e.sequence());
        objectStatus.put(e.objectId(), e.status());
        log.add("applied " + e.id() + " -> " + e.objectId() + "=" + e.status());
        return new Handled(200, Outcome.ACCEPTED, v);
    }
}

/** Adapter so a WebhookConsumer can sit behind the dispatcher as an Endpoint. */
final class ConsumerEndpoint implements Endpoint {
    final WebhookConsumer consumer;
    ConsumerEndpoint(WebhookConsumer consumer) { this.consumer = consumer; }
    @Override public int receive(String payload, Map<String, String> headers) {
        return consumer.handle(payload, headers).httpStatus();
    }
}

public final class Main {
    static final String SECRET = "whsec_demo_secret_do_not_reuse_0001";

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000L);
        WebhookSigner signer = new WebhookSigner(SECRET, clock);
        WebhookVerifier verifier = new WebhookVerifier(SECRET, clock);

        System.out.println("== Signing and verifying ==");
        Event authorized = new Event("evt_001", "payment.authorized", "pay_42", 1, clock.nowSeconds(), "authorized");
        String payload = authorized.toJson();
        String header = signer.header(payload);
        System.out.println("payload: " + payload);
        System.out.println("header:  " + header);
        System.out.println("verify untouched        -> " + verifier.verify(payload, header));
        System.out.println("verify tampered payload -> " + verifier.verify(payload.replace("authorized\"}", "captured\"}"), header));
        System.out.println("verify wrong secret     -> " + new WebhookVerifier("whsec_some_other_secret_9999", clock).verify(payload, header));
        clock.advance(301);
        System.out.println("verify 301s later       -> " + verifier.verify(payload, header) + " (tolerance 300s: replay window closed)");

        System.out.println("\n== At-least-once delivery with backoff ==");
        FakeEndpoint flaky = new FakeEndpoint().failWith(503, 500);
        WebhookDispatcher dispatcher = new WebhookDispatcher(clock, signer, flaky);
        long t0 = clock.nowSeconds();
        Delivery d = dispatcher.enqueue(authorized);
        dispatcher.drain(clock);
        System.out.println("attempts: " + d.attempts + ", statuses: " + d.statuses + ", state: " + d.state);
        List<Long> offsets = new ArrayList<>();
        for (long t : d.attemptTimes) offsets.add(t - t0);
        System.out.println("attempt offsets (s): " + offsets + "  <- 0, +30, +150");

        System.out.println("\n== Dead letter after " + dispatcher.maxAttempts() + " attempts ==");
        FakeEndpoint dead = new FakeEndpoint().failWith(500, 500, 500, 500, 500, 500, 500);
        WebhookDispatcher dispatcher2 = new WebhookDispatcher(clock, signer, dead);
        t0 = clock.nowSeconds();
        Delivery dl = dispatcher2.enqueue(new Event("evt_002", "payment.captured", "pay_42", 2, clock.nowSeconds(), "captured"));
        dispatcher2.drain(clock);
        offsets.clear();
        for (long t : dl.attemptTimes) offsets.add(t - t0);
        System.out.println("attempt offsets (s): " + offsets);
        System.out.printf("state: %s, dead letters: %d, total wall time: %.2fh%n", dl.state, dispatcher2.deadLetters.size(),
                (clock.nowSeconds() - t0) / 3600.0);

        System.out.println("\n== Consumer: dedupe and ordering ==");
        WebhookConsumer consumer = new WebhookConsumer(verifier);
        Event captured = new Event("evt_003", "payment.captured", "pay_77", 2, clock.nowSeconds(), "captured");
        Event authorized77 = new Event("evt_004", "payment.authorized", "pay_77", 1, clock.nowSeconds(), "authorized");
        for (Event e : List.of(captured, captured, authorized77)) {
            Map<String, String> h = Map.of(WebhookSigner.HEADER_NAME, signer.header(e.toJson()));
            WebhookConsumer.Handled r = consumer.handle(e.toJson(), h);
            System.out.printf("%-8s seq=%d -> %-12s http %d%n", e.id(), e.sequence(), r.outcome(), r.httpStatus());
        }
        System.out.println("pay_77 status: " + consumer.objectStatus.get("pay_77") + "  (captured, not rolled back to authorized)");
        WebhookConsumer.Handled forged = consumer.handle(captured.toJson(), Map.of(WebhookSigner.HEADER_NAME, "t=" + clock.nowSeconds() + ",v1=" + "0".repeat(64)));
        System.out.println("forged signature -> " + forged.outcome() + " (" + forged.verification() + ") http " + forged.httpStatus());
    }
}
