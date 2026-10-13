// Project: projects/idempotent-payment-gateway — Stage 04: webhook dispatch.
// Events are signed with HMAC-SHA256 over "<timestamp>.<body>" (RFC 2104) and carried in a header
// "t=<millis>,v1=<hex>". Delivery is at-least-once: a failed attempt is retried on a fixed schedule from the
// injected clock, then dead-lettered. The consumer verifies the signature inside a tolerance window and
// dedupes by event id, which is what turns at-least-once delivery into exactly-once processing.
// Java 21, standard library only (javax.crypto). Same design as the Python and Go versions.

import java.nio.charset.StandardCharsets;
import java.security.InvalidKeyException;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/** One lifecycle event. The body is a fixed-order JSON object so that the same event always produces the same bytes. */
record WebhookEvent(String id, String type, String paymentId, String data, long createdAt) {
    WebhookEvent {
        for (String s : new String[] {id, type, paymentId, data}) {
            Objects.requireNonNull(s);
            if (s.contains("\"") || s.contains("\\")) throw new IllegalArgumentException("fields must not contain quotes or backslashes");
        }
        if (id.isBlank() || type.isBlank()) throw new IllegalArgumentException("id and type are required");
    }

    String toBody() {
        return "{\"id\":\"" + id + "\",\"type\":\"" + type + "\",\"payment_id\":\"" + paymentId
                + "\",\"data\":\"" + data + "\",\"created_at\":" + createdAt + "}";
    }

    /** Inverse of toBody() for the fixed format above; anything else is rejected. */
    static WebhookEvent parse(String body) {
        try {
            String id = field(body, "id");
            String type = field(body, "type");
            String paymentId = field(body, "payment_id");
            String data = field(body, "data");
            String marker = "\"created_at\":";
            int at = body.indexOf(marker);
            if (at < 0) throw new IllegalArgumentException("missing created_at");
            String digits = body.substring(at + marker.length(), body.lastIndexOf('}'));
            return new WebhookEvent(id, type, paymentId, data, Long.parseLong(digits));
        } catch (RuntimeException e) {
            throw new IllegalArgumentException("malformed webhook body: " + e.getMessage(), e);
        }
    }

    private static String field(String body, String key) {
        String marker = "\"" + key + "\":\"";
        int start = body.indexOf(marker);
        if (start < 0) throw new IllegalArgumentException("missing " + key);
        start += marker.length();
        int end = body.indexOf('"', start);
        if (end < 0) throw new IllegalArgumentException("unterminated " + key);
        return body.substring(start, end);
    }
}

/** Produces the signature header the producer sends: "t=<millis>,v1=<hmac-sha256 hex>". */
final class WebhookSigner {
    private final byte[] secret;

    WebhookSigner(String secret) {
        if (secret == null || secret.isEmpty()) throw new IllegalArgumentException("secret is required");
        this.secret = secret.getBytes(StandardCharsets.UTF_8);
    }

    static String hmacSha256Hex(byte[] secret, String message) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(secret, "HmacSHA256"));
            byte[] out = mac.doFinal(message.getBytes(StandardCharsets.UTF_8));
            StringBuilder hex = new StringBuilder(out.length * 2);
            for (byte b : out) hex.append(String.format("%02x", b));
            return hex.toString();
        } catch (NoSuchAlgorithmException | InvalidKeyException e) {
            throw new IllegalStateException(e);
        }
    }

    /** The signed payload is "<timestamp>.<body>": binding the timestamp defeats replay of an old signature. */
    String signature(long timestampMillis, String body) {
        return hmacSha256Hex(secret, timestampMillis + "." + body);
    }

    String header(long timestampMillis, String body) {
        return "t=" + timestampMillis + ",v1=" + signature(timestampMillis, body);
    }
}

enum VerifyResult { OK, MALFORMED_HEADER, STALE_TIMESTAMP, BAD_SIGNATURE }

/** The consumer side of the signature: same secret, same string, constant-time compare, bounded clock skew. */
final class WebhookVerifier {
    private final byte[] secret;
    private final long toleranceMillis;
    private final Clock clock;

    WebhookVerifier(String secret, long toleranceMillis, Clock clock) {
        if (secret == null || secret.isEmpty()) throw new IllegalArgumentException("secret is required");
        if (toleranceMillis < 0) throw new IllegalArgumentException("tolerance must be non-negative");
        this.secret = secret.getBytes(StandardCharsets.UTF_8);
        this.toleranceMillis = toleranceMillis;
        this.clock = Objects.requireNonNull(clock);
    }

    VerifyResult verify(String body, String header) {
        if (body == null || header == null) return VerifyResult.MALFORMED_HEADER;
        long timestamp;
        String provided;
        try {
            String[] parts = header.split(",");
            if (parts.length != 2 || !parts[0].startsWith("t=") || !parts[1].startsWith("v1=")) return VerifyResult.MALFORMED_HEADER;
            timestamp = Long.parseLong(parts[0].substring(2));
            provided = parts[1].substring(3);
        } catch (NumberFormatException e) {
            return VerifyResult.MALFORMED_HEADER;
        }
        if (Math.abs(clock.nowMillis() - timestamp) > toleranceMillis) return VerifyResult.STALE_TIMESTAMP;
        String expected = WebhookSigner.hmacSha256Hex(secret, timestamp + "." + body);
        boolean equal = MessageDigest.isEqual(expected.getBytes(StandardCharsets.UTF_8), provided.getBytes(StandardCharsets.UTF_8));
        return equal ? VerifyResult.OK : VerifyResult.BAD_SIGNATURE;
    }
}

enum ReceiveStatus { ACCEPTED, DUPLICATE, REJECTED }

record ReceiveResult(ReceiveStatus status, VerifyResult verification, WebhookEvent event) {}

/** A merchant's webhook handler: verify, dedupe by event id, then process exactly once. */
final class WebhookConsumer {
    private final WebhookVerifier verifier;
    private final Set<String> seenIds = new LinkedHashSet<>();
    private final List<WebhookEvent> processed = new ArrayList<>();

    WebhookConsumer(WebhookVerifier verifier) {
        this.verifier = Objects.requireNonNull(verifier);
    }

    ReceiveResult receive(String body, String header) {
        VerifyResult verification = verifier.verify(body, header);
        if (verification != VerifyResult.OK) return new ReceiveResult(ReceiveStatus.REJECTED, verification, null);
        WebhookEvent event;
        try {
            event = WebhookEvent.parse(body);
        } catch (IllegalArgumentException e) {
            return new ReceiveResult(ReceiveStatus.REJECTED, VerifyResult.OK, null);
        }
        if (!seenIds.add(event.id())) return new ReceiveResult(ReceiveStatus.DUPLICATE, verification, event);
        processed.add(event);
        return new ReceiveResult(ReceiveStatus.ACCEPTED, verification, event);
    }

    List<WebhookEvent> processed() {
        return Collections.unmodifiableList(processed);
    }
}

/** Where the dispatcher posts to. Returns true for a 2xx. */
interface WebhookEndpoint {
    boolean deliver(String body, String header);
}

/** Scripted endpoint: 'S' succeed, 'F' fail; the last character repeats. Records every delivery it saw. */
final class ScriptedEndpoint implements WebhookEndpoint {
    record Delivery(String body, String header) {}

    private final String script;
    private final List<Delivery> deliveries = new ArrayList<>();

    ScriptedEndpoint(String script) {
        if (script == null || script.isEmpty() || !script.chars().allMatch(c -> c == 'S' || c == 'F')) {
            throw new IllegalArgumentException("script must be a non-empty string of S and F: " + script);
        }
        this.script = script;
    }

    @Override
    public boolean deliver(String body, String header) {
        char step = script.charAt(Math.min(deliveries.size(), script.length() - 1));
        deliveries.add(new Delivery(body, header));
        return step == 'S';
    }

    List<Delivery> deliveries() {
        return Collections.unmodifiableList(deliveries);
    }
}

/** Adapts a WebhookConsumer as an endpoint: ACCEPTED and DUPLICATE are both 2xx, REJECTED is not. */
final class ConsumerEndpoint implements WebhookEndpoint {
    private final WebhookConsumer consumer;

    ConsumerEndpoint(WebhookConsumer consumer) {
        this.consumer = Objects.requireNonNull(consumer);
    }

    @Override
    public boolean deliver(String body, String header) {
        return consumer.receive(body, header).status() != ReceiveStatus.REJECTED;
    }
}

/** A queued delivery: the event, how many attempts have been made, and when the next one is due. */
record DeliveryJob(WebhookEvent event, int attempts, long nextAttemptAt) {}

/**
 * At-least-once delivery. enqueue() schedules the first attempt now; runDue() performs every attempt whose
 * time has come. A failure schedules the next attempt retryDelays[attempts-1] later; when the schedule is
 * used up the job goes to the dead-letter list for a human.
 */
final class WebhookDispatcher {
    private final WebhookSigner signer;
    private final WebhookEndpoint endpoint;
    private final Clock clock;
    private final long[] retryDelays;
    private final List<DeliveryJob> pending = new ArrayList<>();
    private final List<WebhookEvent> delivered = new ArrayList<>();
    private final List<DeliveryJob> deadLetters = new ArrayList<>();

    WebhookDispatcher(WebhookSigner signer, WebhookEndpoint endpoint, Clock clock, long[] retryDelaysMillis) {
        this.signer = Objects.requireNonNull(signer);
        this.endpoint = Objects.requireNonNull(endpoint);
        this.clock = Objects.requireNonNull(clock);
        this.retryDelays = Objects.requireNonNull(retryDelaysMillis).clone();
        for (long d : retryDelays) if (d < 0) throw new IllegalArgumentException("retry delays must be non-negative");
    }

    /** Total attempts a job may receive: the first one plus one per retry delay. */
    int maxAttempts() {
        return retryDelays.length + 1;
    }

    void enqueue(WebhookEvent event) {
        pending.add(new DeliveryJob(Objects.requireNonNull(event), 0, clock.nowMillis()));
    }

    /** Attempt every due job once. Returns the number of attempts made. */
    int runDue() {
        long now = clock.nowMillis();
        int attemptsMade = 0;
        List<DeliveryJob> snapshot = new ArrayList<>(pending);
        for (DeliveryJob job : snapshot) {
            if (job.nextAttemptAt() > now) continue;
            pending.remove(job);
            attemptsMade++;
            String body = job.event().toBody();
            boolean ok = endpoint.deliver(body, signer.header(now, body));
            int attempts = job.attempts() + 1;
            if (ok) {
                delivered.add(job.event());
            } else if (attempts >= maxAttempts()) {
                deadLetters.add(new DeliveryJob(job.event(), attempts, now));
            } else {
                pending.add(new DeliveryJob(job.event(), attempts, now + retryDelays[attempts - 1]));
            }
        }
        return attemptsMade;
    }

    List<DeliveryJob> pending() { return Collections.unmodifiableList(pending); }
    List<WebhookEvent> delivered() { return Collections.unmodifiableList(delivered); }
    List<DeliveryJob> deadLetters() { return Collections.unmodifiableList(deadLetters); }
}
