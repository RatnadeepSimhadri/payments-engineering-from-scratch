// Project: projects/idempotent-payment-gateway — reference solution, worked example.
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang java --all --solution
// Stages: 01 idempotency store, 02 payment state machine, 03 retries + circuit breaker + gateway, 04 webhooks.
// Java 21, standard library only. Same design as the Python port in solution/python and the Go port in solution/go.

public final class Main {
    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        IdempotencyStore store = new IdempotencyStore(clock, 24L * 60 * 60 * 1000);

        System.out.println("== Stage 01: idempotency store ==");
        ChargeRequest req = new ChargeRequest("m_1", "key-1", Money.parse("19.99", Currency.USD), "tok_visa", "order-42");
        System.out.println("canonical  = " + req.canonical().replace("\n", "\\n"));
        System.out.println("fingerprint= " + req.fingerprint());
        System.out.println("first begin -> " + store.begin("m_1", "key-1", req.fingerprint()).decision());
        System.out.println("while open  -> " + store.begin("m_1", "key-1", req.fingerprint()).decision());
        store.complete("m_1", "key-1", new ApiResponse(201, "{\"payment_id\":\"pay_1\"}"));
        System.out.println("after done  -> " + store.begin("m_1", "key-1", req.fingerprint()));
        ChargeRequest other = new ChargeRequest("m_1", "key-1", Money.parse("29.99", Currency.USD), "tok_visa", "order-42");
        System.out.println("other body  -> " + store.begin("m_1", "key-1", other.fingerprint()).decision());

        System.out.println("\n== Stage 02: payment state machine ==");
        Payment p = new Payment("pay_demo", Money.parse("50.00", Currency.USD), clock);
        p.startProcessing();
        p.succeed("auth-77");
        System.out.println("refund 20 -> remaining " + p.refund(Money.parse("20.00", Currency.USD), "customer request"));
        System.out.println("refund 30 -> remaining " + p.refund(Money.parse("30.00", Currency.USD), "customer request") + " state " + p.state());
        try {
            p.refund(Money.parse("1.00", Currency.USD), "again");
        } catch (IllegalTransitionException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        for (Transition t : p.history()) System.out.println("  " + t.from() + " -> " + t.to() + " : " + t.reason());

        System.out.println("\n== Stage 03: retries with full jitter, budget, breaker ==");
        BackoffPolicy policy = new BackoffPolicy(100, 2_000, 4);
        Rng rng = new Rng(42);
        for (int retry = 1; retry <= 4; retry++) {
            System.out.println("retry " + retry + ": ceiling " + policy.ceilingFor(retry) + " ms, drawn " + policy.delayBefore(retry, new Rng(42 + retry)) + " ms");
        }
        CircuitBreaker breaker = new CircuitBreaker(clock, 3, 30_000, 1);
        RetryingClient client = new RetryingClient(new ScriptedProcessor("UUA"), policy, new RetryBudget(10, 10), breaker, rng, clock);
        Gateway gateway = new Gateway(store, client, clock);
        ApiResponse first = gateway.charge(new ChargeRequest("m_1", "key-2", Money.parse("19.99", Currency.USD), "tok_visa", "order-43"));
        System.out.println("charge -> " + first.status() + " " + first.body());
        ApiResponse replay = gateway.charge(new ChargeRequest("m_1", "key-2", Money.parse("19.99", Currency.USD), "tok_visa", "order-43"));
        System.out.println("replay -> " + replay.status() + " replayed=" + replay.replayed());

        System.out.println("\n== Stage 04: signed webhooks, at-least-once, dedupe ==");
        WebhookSigner signer = new WebhookSigner("whsec_demo");
        WebhookConsumer consumer = new WebhookConsumer(new WebhookVerifier("whsec_demo", 5 * 60_000, clock));
        WebhookDispatcher dispatcher = new WebhookDispatcher(signer, new ConsumerEndpoint(consumer), clock, new long[] {60_000, 300_000});
        gateway.attachWebhooks(dispatcher);
        gateway.charge(new ChargeRequest("m_1", "key-3", Money.parse("5.00", Currency.USD), "tok_visa", "order-44"));
        System.out.println("pending before run: " + dispatcher.pending().size());
        dispatcher.runDue();
        WebhookEvent evt = consumer.processed().get(0);
        String body = evt.toBody();
        String header = signer.header(clock.nowMillis(), body);
        System.out.println("delivered " + evt.id() + " " + evt.type() + " header " + header);
        System.out.println("same event again -> " + consumer.receive(body, header).status());
        System.out.println("tampered body    -> " + consumer.receive(body.replace("5.00", "500.00"), header).status());
    }
}
