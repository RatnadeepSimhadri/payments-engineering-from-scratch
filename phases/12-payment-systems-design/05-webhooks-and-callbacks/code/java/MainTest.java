// Tests for phases/12-payment-systems-design/05-webhooks-and-callbacks. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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

    static final String SECRET = "whsec_test_secret_0123456789abcdef";

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000L);
        WebhookSigner signer = new WebhookSigner(SECRET, clock);
        WebhookVerifier verifier = new WebhookVerifier(SECRET, clock);
        Event ev = new Event("evt_1", "payment.captured", "pay_1", 1, clock.nowSeconds(), "captured");
        String payload = ev.toJson();

        // RFC 2104 / RFC 4231 test vector: key = 0x0b * 20, data = "Hi There"
        byte[] key = new byte[20];
        java.util.Arrays.fill(key, (byte) 0x0b);
        check("HMAC-SHA256 matches RFC 4231 test case 1",
                WebhookSigner.hex(WebhookSigner.hmacSha256(key, "Hi There".getBytes(StandardCharsets.UTF_8)))
                        .equals("b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"));
        check("hex is lowercase and 64 chars", signer.signature(1, "x").matches("[0-9a-f]{64}"));

        // signing and verification
        String header = signer.header(payload);
        check("header shape t=...,v1=...", header.matches("t=\\d+,v1=[0-9a-f]{64}"));
        check("verify ok", verifier.verify(payload, header) == Verification.OK);
        check("signature deterministic for same ts+payload", signer.header(payload).equals(header));
        check("different payload, different signature", !signer.signature(clock.nowSeconds(), payload).equals(signer.signature(clock.nowSeconds(), payload + " ")));
        check("tampered payload rejected", verifier.verify(payload.replace("captured", "refunded"), header) == Verification.BAD_SIGNATURE);
        check("wrong secret rejected", new WebhookVerifier("whsec_other_secret_0000000000", clock).verify(payload, header) == Verification.BAD_SIGNATURE);
        check("timestamp is bound into the MAC", verifier.verify(payload, header.replace("t=" + clock.nowSeconds(), "t=" + (clock.nowSeconds() + 1))) == Verification.BAD_SIGNATURE);
        check("malformed header: empty", verifier.verify(payload, "") == Verification.MALFORMED_HEADER);
        check("malformed header: null", verifier.verify(payload, null) == Verification.MALFORMED_HEADER);
        check("malformed header: no v1", verifier.verify(payload, "t=123") == Verification.MALFORMED_HEADER);
        check("malformed header: bad ts", verifier.verify(payload, "t=abc,v1=" + "0".repeat(64)) == Verification.MALFORMED_HEADER);
        check("malformed header: short sig", verifier.verify(payload, "t=1,v1=abcd") == Verification.MALFORMED_HEADER);
        check("unknown scheme ignored", verifier.verify(payload, header + ",v0=deadbeef") == Verification.OK);
        check("rotation: any one v1 matching passes", verifier.verify(payload, header + ",v1=" + "f".repeat(64)) == Verification.OK);

        // tolerance window
        clock.advance(300);
        check("exactly at tolerance still ok", verifier.verify(payload, header) == Verification.OK);
        clock.advance(1);
        check("one second past tolerance is stale", verifier.verify(payload, header) == Verification.STALE_TIMESTAMP);
        check("stale beats bad signature (checked first)", verifier.verify(payload + "x", header) == Verification.STALE_TIMESTAMP);
        String future = "t=" + (clock.nowSeconds() + 400) + ",v1=" + signer.signature(clock.nowSeconds() + 400, payload);
        check("timestamp from the future is also stale", verifier.verify(payload, future) == Verification.STALE_TIMESTAMP);
        check("custom tolerance honoured", new WebhookVerifier(SECRET, clock, 3600).verify(payload, header) == Verification.OK);

        // constant-time compare
        check("ct equal", WebhookVerifier.constantTimeEquals(new byte[]{1, 2, 3}, new byte[]{1, 2, 3}));
        check("ct differs at end", !WebhookVerifier.constantTimeEquals(new byte[]{1, 2, 3}, new byte[]{1, 2, 4}));
        check("ct length mismatch", !WebhookVerifier.constantTimeEquals(new byte[]{1, 2}, new byte[]{1, 2, 3}));
        check("unhex round trip", WebhookSigner.hex(WebhookVerifier.unhex("00ff7a")).equals("00ff7a"));
        checkThrows("short secret rejected", IllegalArgumentException.class, () -> new WebhookSigner("short", clock));

        // event JSON round trip
        Event back = Event.fromJson(payload);
        check("event json round trip", back.equals(ev));
        checkThrows("missing field rejected", IllegalArgumentException.class, () -> Event.fromJson("{\"id\":\"x\"}"));

        // delivery: happy path
        FakeEndpoint ok = new FakeEndpoint();
        WebhookDispatcher dsp = new WebhookDispatcher(clock, signer, ok);
        Delivery d = dsp.enqueue(ev);
        check("tick attempts a due delivery", dsp.tick() == 1);
        check("delivered first try", d.state == DeliveryState.DELIVERED && d.attempts == 1);
        check("endpoint received signed header", ok.receivedHeaders.get(0).containsKey(WebhookSigner.HEADER_NAME));
        check("attempt header is 1", ok.receivedHeaders.get(0).get("Webhook-Attempt").equals("1"));
        check("second tick does nothing", dsp.tick() == 0);

        // delivery: backoff schedule is exact
        FakeEndpoint flaky = new FakeEndpoint().failWith(503, 500, 429);
        WebhookDispatcher dsp2 = new WebhookDispatcher(clock, signer, flaky, new long[]{10, 20, 40});
        long t0 = clock.nowSeconds();
        Delivery d2 = dsp2.enqueue(ev);
        dsp2.tick();
        check("not retried before backoff elapses", d2.nextAttemptAt == t0 + 10 && dsp2.tick() == 0);
        clock.advance(9);
        check("still not due at 9s", dsp2.tick() == 0);
        clock.advance(1);
        check("due at exactly 10s", dsp2.tick() == 1 && d2.attempts == 2);
        dsp2.drain(clock);
        check("delivered on fourth attempt", d2.state == DeliveryState.DELIVERED && d2.attempts == 4);
        check("attempt offsets 0,10,30,70", d2.attemptTimes.equals(List.of(t0, t0 + 10, t0 + 30, t0 + 70)));
        check("statuses recorded", d2.statuses.equals(List.of(503, 500, 429, 200)));
        check("retry is re-signed with a fresh timestamp", flaky.receivedHeaders.get(0).get(WebhookSigner.HEADER_NAME).startsWith("t=" + (t0 + 70)));
        check("at-least-once: payload delivered exactly once here", flaky.received.size() == 1);

        // delivery: dead letter
        FakeEndpoint down = new FakeEndpoint().failWith(500, 500, 500, 500, 500, 500, 500, 500);
        WebhookDispatcher dsp3 = new WebhookDispatcher(clock, signer, down, new long[]{1, 2, 4});
        Delivery d3 = dsp3.enqueue(ev);
        dsp3.drain(clock);
        check("max attempts = backoff length + 1", dsp3.maxAttempts() == 4 && d3.attempts == 4);
        check("dead lettered", d3.state == DeliveryState.DEAD_LETTERED && dsp3.deadLetters.contains(d3));
        check("no further attempts after dead letter", dsp3.tick() == 0 && down.calls == 4);
        check("default schedule has 6 attempts", new WebhookDispatcher(clock, signer, down).maxAttempts() == 6);

        // consumer: dedupe, ordering, rejection
        WebhookConsumer consumer = new WebhookConsumer(verifier);
        Event a1 = new Event("evt_a1", "payment.authorized", "pay_9", 1, clock.nowSeconds(), "authorized");
        Event a2 = new Event("evt_a2", "payment.captured", "pay_9", 2, clock.nowSeconds(), "captured");
        Map<String, String> h2 = Map.of(WebhookSigner.HEADER_NAME, signer.header(a2.toJson()));
        Map<String, String> h1 = Map.of(WebhookSigner.HEADER_NAME, signer.header(a1.toJson()));
        check("first event accepted", consumer.handle(a2.toJson(), h2).outcome() == Outcome.ACCEPTED);
        check("duplicate id returns DUPLICATE with 200", consumer.handle(a2.toJson(), h2).equals(new WebhookConsumer.Handled(200, Outcome.DUPLICATE, Verification.OK)));
        check("older sequence does not roll state back", consumer.handle(a1.toJson(), h1).outcome() == Outcome.OUT_OF_ORDER && consumer.objectStatus.get("pay_9").equals("captured"));
        check("forged signature rejected with 400", consumer.handle(a2.toJson(), Map.of(WebhookSigner.HEADER_NAME, "t=" + clock.nowSeconds() + ",v1=" + "0".repeat(64))).httpStatus() == 400);
        check("missing header rejected", consumer.handle(a2.toJson(), Map.of()).verification() == Verification.MALFORMED_HEADER);
        check("rejected events are not marked seen", consumer.latestSequence.get("pay_9") == 2L && consumer.log.stream().filter(s -> s.startsWith("applied")).count() == 1);

        // end to end: dispatcher retries into a consumer; duplicates are absorbed
        WebhookConsumer c2 = new WebhookConsumer(verifier);
        Endpoint flakyConsumer = new Endpoint() {
            int n = 0;
            @Override public int receive(String p, Map<String, String> h) {
                n++;
                int status = c2.handle(p, h).httpStatus();
                return n == 1 ? 504 : status;   // consumer processed it, but the producer saw a timeout
            }
        };
        WebhookDispatcher dsp4 = new WebhookDispatcher(clock, signer, flakyConsumer, new long[]{5});
        Delivery d4 = dsp4.enqueue(a2);
        dsp4.drain(clock);
        check("timeout after processing causes a redelivery", d4.attempts == 2 && d4.state == DeliveryState.DELIVERED);
        check("consumer saw the duplicate and applied once", c2.log.stream().filter(s -> s.startsWith("duplicate")).count() == 1
                && c2.log.stream().filter(s -> s.startsWith("applied")).count() == 1);

        checkThrows("fake clock refuses to go backwards", IllegalArgumentException.class, () -> clock.advance(-1));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
