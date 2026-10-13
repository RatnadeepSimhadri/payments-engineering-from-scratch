// Project: projects/idempotent-payment-gateway — learner workspace entry point.
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 1 --path <workspace>
// Money, Clock and Rng are given. Idempotency.java holds the Stage 01 stubs; later stages add
// Payments.java, Resilience.java and Webhooks.java (see each stage's starter/STAGE.md).

public final class Main {
    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        IdempotencyStore store = new IdempotencyStore(clock, 24L * 60 * 60 * 1000);
        ChargeRequest req = new ChargeRequest("m_1", "key-1", Money.parse("19.99", Currency.USD), "tok_visa", "order-42");
        System.out.println("idempotent-payment-gateway workspace: store ttl " + store.ttlMillis() + " ms, request " + req.amount());
        try {
            System.out.println("fingerprint = " + req.fingerprint());
            System.out.println("first begin -> " + store.begin("m_1", "key-1", req.fingerprint()).decision());
        } catch (UnsupportedOperationException e) {
            System.out.println("not implemented yet: " + e.getMessage());
        }
    }
}
