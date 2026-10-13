// Stage 4 tests for projects/idempotent-payment-gateway: HMAC-SHA256 signatures over "<timestamp>.<body>",
// verification inside a tolerance window with a constant-time compare, at-least-once delivery on a retry
// schedule with dead-lettering, consumer dedupe by event id, and the Gateway emitting lifecycle events.
// Run by scripts/project_test.py; exits 1 on failure.
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

public final class Stage04Test {
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static final long S4_T0 = 1_700_000_000_000L;

    static WebhookEvent s4Event(String id) {
        return new WebhookEvent(id, "payment.succeeded", "pay_1", "amount=19.99 USD", S4_T0);
    }

    public static void main(String[] args) {
        // Event body is a fixed-order JSON object and round-trips through parse()
        WebhookEvent evt = s4Event("evt_1");
        String body = evt.toBody();
        check("body is fixed-order JSON", body.equals("{\"id\":\"evt_1\",\"type\":\"payment.succeeded\",\"payment_id\":\"pay_1\",\"data\":\"amount=19.99 USD\",\"created_at\":1700000000000}"));
        check("parse inverts toBody", WebhookEvent.parse(body).equals(evt));
        checkThrows("malformed body rejected", IllegalArgumentException.class, () -> WebhookEvent.parse("{\"id\":\"evt_1\"}"));
        checkThrows("quotes in fields rejected", IllegalArgumentException.class, () -> new WebhookEvent("evt\"1", "t", "p", "d", 0));
        checkThrows("blank id rejected", IllegalArgumentException.class, () -> new WebhookEvent(" ", "t", "p", "d", 0));

        // HMAC-SHA256 (RFC 2104 / RFC 4231 style vector) and the signature header
        check("hmac known vector", WebhookSigner.hmacSha256Hex("key".getBytes(StandardCharsets.UTF_8), "The quick brown fox jumps over the lazy dog")
                .equals("f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"));
        WebhookSigner signer = new WebhookSigner("whsec_test");
        String sig = signer.signature(S4_T0, body);
        check("signature is 64 lowercase hex chars", sig.length() == 64 && sig.matches("[0-9a-f]{64}"));
        check("signature known value", sig.equals("c189aab766a9d122c3d7fed8734fab41c76bd4290e69b3639d4e841f309816fd"));
        check("header format", signer.header(S4_T0, body).equals("t=1700000000000,v1=" + sig));
        check("signature binds the timestamp", !signer.signature(S4_T0 + 1, body).equals(sig));
        check("signature binds the body", !signer.signature(S4_T0, body.replace("19.99", "190.99")).equals(sig));
        check("signature binds the secret", !new WebhookSigner("whsec_other").signature(S4_T0, body).equals(sig));
        checkThrows("empty secret rejected", IllegalArgumentException.class, () -> new WebhookSigner(""));

        // Verification: window, constant-time compare, malformed headers
        FakeClock clock = new FakeClock(S4_T0);
        WebhookVerifier verifier = new WebhookVerifier("whsec_test", 5 * 60_000, clock);
        String header = signer.header(S4_T0, body);
        check("valid header verifies", verifier.verify(body, header) == VerifyResult.OK);
        check("tampered body fails", verifier.verify(body.replace("pay_1", "pay_2"), header) == VerifyResult.BAD_SIGNATURE);
        check("wrong secret fails", new WebhookVerifier("whsec_other", 5 * 60_000, clock).verify(body, header) == VerifyResult.BAD_SIGNATURE);
        check("forged timestamp fails the signature", verifier.verify(body, "t=" + (S4_T0 + 1) + ",v1=" + sig) == VerifyResult.BAD_SIGNATURE);
        check("missing v1 is malformed", verifier.verify(body, "t=1700000000000") == VerifyResult.MALFORMED_HEADER);
        check("non-numeric timestamp is malformed", verifier.verify(body, "t=now,v1=" + sig) == VerifyResult.MALFORMED_HEADER);
        check("null header is malformed", verifier.verify(body, null) == VerifyResult.MALFORMED_HEADER);
        clock.advance(5 * 60_000);
        check("exactly at the tolerance edge still OK", verifier.verify(body, header) == VerifyResult.OK);
        clock.advance(1);
        check("one ms past tolerance is STALE", verifier.verify(body, header) == VerifyResult.STALE_TIMESTAMP);
        clock.set(S4_T0 - 5 * 60_000 - 1);
        check("a signature from the future is also STALE", verifier.verify(body, header) == VerifyResult.STALE_TIMESTAMP);
        clock.set(S4_T0);
        check("zero tolerance accepts only the exact instant", new WebhookVerifier("whsec_test", 0, clock).verify(body, header) == VerifyResult.OK
                && new WebhookVerifier("whsec_test", 0, new FakeClock(S4_T0 + 1)).verify(body, header) == VerifyResult.STALE_TIMESTAMP);

        // Consumer: verify, dedupe by id, process once
        WebhookConsumer consumer = new WebhookConsumer(verifier);
        ReceiveResult r1 = consumer.receive(body, header);
        check("first delivery ACCEPTED", r1.status() == ReceiveStatus.ACCEPTED && r1.verification() == VerifyResult.OK && r1.event().equals(evt));
        ReceiveResult r2 = consumer.receive(body, header);
        check("redelivery is DUPLICATE, not processed twice", r2.status() == ReceiveStatus.DUPLICATE && consumer.processed().size() == 1);
        check("redelivery with a fresh signature is still DUPLICATE", consumer.receive(body, signer.header(S4_T0 + 1000, body)).status() == ReceiveStatus.DUPLICATE);
        ReceiveResult r3 = consumer.receive(body, "t=" + S4_T0 + ",v1=" + "0".repeat(64));
        check("bad signature REJECTED with the reason", r3.status() == ReceiveStatus.REJECTED && r3.verification() == VerifyResult.BAD_SIGNATURE && r3.event() == null);
        String other = s4Event("evt_2").toBody();
        check("a different event id is ACCEPTED", consumer.receive(other, signer.header(S4_T0, other)).status() == ReceiveStatus.ACCEPTED && consumer.processed().size() == 2);
        String garbage = "{\"nope\":1}";
        check("verified but unparsable body is REJECTED", consumer.receive(garbage, signer.header(S4_T0, garbage)).status() == ReceiveStatus.REJECTED);

        // Dispatcher: at-least-once on a schedule, then dead-letter
        FakeClock dc = new FakeClock(S4_T0);
        ScriptedEndpoint flaky = new ScriptedEndpoint("FFS");
        WebhookDispatcher dispatcher = new WebhookDispatcher(signer, flaky, dc, new long[] {60_000, 300_000});
        check("maxAttempts is retries + 1", dispatcher.maxAttempts() == 3);
        dispatcher.enqueue(s4Event("evt_10"));
        check("enqueued job is pending with 0 attempts", dispatcher.pending().size() == 1 && dispatcher.pending().get(0).attempts() == 0 && dispatcher.pending().get(0).nextAttemptAt() == S4_T0);
        check("first run attempts once and fails", dispatcher.runDue() == 1 && flaky.deliveries().size() == 1 && dispatcher.delivered().isEmpty());
        check("retry scheduled 60s later", dispatcher.pending().size() == 1 && dispatcher.pending().get(0).attempts() == 1 && dispatcher.pending().get(0).nextAttemptAt() == S4_T0 + 60_000);
        check("nothing due before the schedule", dispatcher.runDue() == 0 && flaky.deliveries().size() == 1);
        dc.advance(60_000);
        check("second attempt at +60s fails", dispatcher.runDue() == 1 && dispatcher.pending().get(0).attempts() == 2 && dispatcher.pending().get(0).nextAttemptAt() == S4_T0 + 360_000);
        dc.advance(300_000);
        check("third attempt at +360s succeeds", dispatcher.runDue() == 1 && dispatcher.delivered().size() == 1 && dispatcher.pending().isEmpty() && dispatcher.deadLetters().isEmpty());
        check("every attempt was signed with the clock at send time", flaky.deliveries().get(0).header().startsWith("t=" + S4_T0 + ",")
                && flaky.deliveries().get(2).header().startsWith("t=" + (S4_T0 + 360_000) + ","));
        check("every attempt carried the same body", flaky.deliveries().get(0).body().equals(flaky.deliveries().get(2).body()));
        check("each delivery verifies against a clock at send time", new WebhookVerifier("whsec_test", 1000, new FakeClock(S4_T0 + 360_000))
                .verify(flaky.deliveries().get(2).body(), flaky.deliveries().get(2).header()) == VerifyResult.OK);

        ScriptedEndpoint down = new ScriptedEndpoint("F");
        WebhookDispatcher dead = new WebhookDispatcher(signer, down, dc, new long[] {1_000});
        dead.enqueue(s4Event("evt_20"));
        dead.enqueue(s4Event("evt_21"));
        check("two due jobs run in one pass", dead.runDue() == 2);
        dc.advance(1_000);
        check("after the schedule both are dead-lettered", dead.runDue() == 2 && dead.deadLetters().size() == 2 && dead.pending().isEmpty());
        check("dead letter keeps the event and attempt count", dead.deadLetters().get(0).event().id().equals("evt_20") && dead.deadLetters().get(0).attempts() == 2);
        check("no further attempts once dead", dead.runDue() == 0 && down.deliveries().size() == 4);
        checkThrows("negative retry delay rejected", IllegalArgumentException.class, () -> new WebhookDispatcher(signer, down, dc, new long[] {-1}));

        // End to end: the gateway emits events, the dispatcher delivers them, the consumer processes each once
        FakeClock gc = new FakeClock(S4_T0);
        IdempotencyStore store = new IdempotencyStore(gc, 60_000);
        RetryingClient client = new RetryingClient(new ScriptedProcessor("ADU"), new BackoffPolicy(100, 2_000, 1), new RetryBudget(10, 10), new CircuitBreaker(gc, 10, 30_000, 1), new Rng(42), gc);
        Gateway gateway = new Gateway(store, client, gc);
        WebhookConsumer merchant = new WebhookConsumer(new WebhookVerifier("whsec_live", 5 * 60_000, gc));
        WebhookDispatcher live = new WebhookDispatcher(new WebhookSigner("whsec_live"), new ConsumerEndpoint(merchant), gc, new long[] {60_000});
        gateway.attachWebhooks(live);
        gateway.charge(new ChargeRequest("m_1", "k1", Money.parse("19.99", Currency.USD), "tok_visa", "order-1"));
        gateway.charge(new ChargeRequest("m_1", "k1", Money.parse("19.99", Currency.USD), "tok_visa", "order-1"));
        gateway.charge(new ChargeRequest("m_1", "k2", Money.parse("5.00", Currency.USD), "tok_visa", "order-2"));
        gateway.charge(new ChargeRequest("m_1", "k3", Money.parse("7.00", Currency.USD), "tok_visa", "order-3"));
        check("succeeded and failed emit, replay and unavailable do not", live.pending().size() == 2
                && live.pending().get(0).event().type().equals("payment.succeeded") && live.pending().get(0).event().paymentId().equals("pay_1")
                && live.pending().get(1).event().type().equals("payment.failed") && live.pending().get(1).event().paymentId().equals("pay_2"));
        check("event ids are unique", !live.pending().get(0).event().id().equals(live.pending().get(1).event().id()));
        check("event data carries the amount", live.pending().get(0).event().data().equals("amount=19.99 USD"));
        live.runDue();
        check("both events delivered and processed once", live.delivered().size() == 2 && merchant.processed().size() == 2);
        WebhookEvent delivered = merchant.processed().get(0);
        check("redelivering a processed event is a 2xx DUPLICATE", merchant.receive(delivered.toBody(), new WebhookSigner("whsec_live").header(gc.nowMillis(), delivered.toBody())).status() == ReceiveStatus.DUPLICATE
                && merchant.processed().size() == 2);

        System.out.println("Stage04Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
