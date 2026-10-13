// Tests for phases/05-distributed-foundations/09-idempotency-keys. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ConcurrentLinkedQueue;
import java.util.concurrent.CountDownLatch;

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

    static final long DAY_MS = 24L * 60 * 60 * 1000;
    static final String EXPECTED_FINGERPRINT = "795fe63bfb540da3cf414455d7494e879034c285ddc8d254e3b0ec2c396cb96f";

    static ChargeRequest request(String key, long cents) {
        return new ChargeRequest("acct_1", key, Money.ofMinor(cents, Currency.USD), "tok_visa_4242", "Order 8812");
    }

    /** A processor that blocks inside charge() until released, so concurrent duplicates really overlap. */
    static final class BlockingProcessor implements Processor {
        final CountDownLatch release = new CountDownLatch(1);
        volatile int executions = 0;

        @Override public Response charge(ChargeRequest request) {
            synchronized (this) { executions++; }
            try { release.await(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            return Response.of(200, "{\"id\":\"ch_blocking\"}");
        }
    }

    public static void main(String[] args) throws Exception {
        // the fingerprint is a stable function of the effectful fields, identical in all three languages
        check("fingerprint matches Python and Go", request("k", 1999).fingerprint().equals(EXPECTED_FINGERPRINT));
        check("fingerprint ignores the key and the account", request("other-key", 1999).fingerprint().equals(EXPECTED_FINGERPRINT)
                && new ChargeRequest("acct_2", "k", Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 8812").fingerprint().equals(EXPECTED_FINGERPRINT));
        check("fingerprint changes with the amount", !request("k", 2999).fingerprint().equals(EXPECTED_FINGERPRINT));
        check("fingerprint changes with the currency", !new ChargeRequest("acct_1", "k", Money.ofMinor(1999, Currency.EUR), "tok_visa_4242", "Order 8812").fingerprint().equals(EXPECTED_FINGERPRINT));
        checkThrows("blank key is rejected", IllegalArgumentException.class, () -> request(" ", 1999));
        checkThrows("over-long key is rejected", IllegalArgumentException.class, () -> request("x".repeat(256), 1999));
        checkThrows("non-positive amount is rejected", IllegalArgumentException.class, () -> request("k", 0));

        // happy path: first executes, retry replays, exactly one effect
        FakeClock clock = new FakeClock(1_000);
        FakeProcessor processor = new FakeProcessor();
        IdempotencyStore store = new IdempotencyStore(clock, DAY_MS, 30_000);
        PaymentsApi api = new PaymentsApi(store, processor);
        Response first = api.charge(request("order-1", 1999));
        Response retry = api.charge(request("order-1", 1999));
        check("first request executes with 200", first.status() == 200 && !first.replayed());
        check("retry returns the identical body", retry.body().equals(first.body()));
        check("retry is marked as a replay", retry.replayed());
        check("exactly one processor execution for the pair", processor.executions() == 1);
        check("store holds one entry", store.size() == 1);

        // without a key every send executes: the failure the lesson warns about
        ChargeRequest noKey = new ChargeRequest("acct_1", null, Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 8812");
        api.charge(noKey); api.charge(noKey);
        check("no key means a retry is a second charge", processor.executions() == 3);

        // same key, different body: 422 and no execution
        Response conflict = api.charge(request("order-1", 2999));
        check("conflict is 422", conflict.status() == 422 && !conflict.replayed());
        check("conflict does not execute", processor.executions() == 3);
        check("conflict does not disturb the stored response", api.charge(request("order-1", 1999)).body().equals(first.body()));

        // keys are scoped per account
        ChargeRequest otherAccount = new ChargeRequest("acct_2", "order-1", Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 8812");
        Response other = api.charge(otherAccount);
        check("same key on another account is a new request", other.status() == 200 && !other.replayed() && processor.executions() == 4);

        // concurrent duplicates at the store level: one NEW, the other IN_PROGRESS, then REPLAY
        String fp = request("k", 1999).fingerprint();
        Begin a = store.begin("acct_1", "order-2", fp);
        Begin b = store.begin("acct_1", "order-2", fp);
        check("first begin reserves", a.outcome() == Outcome.NEW);
        check("second begin while in progress", b.outcome() == Outcome.IN_PROGRESS && b.response() == null);
        check("conflict is detected even while in progress", store.begin("acct_1", "order-2", "other-fp").outcome() == Outcome.CONFLICT);
        store.complete("acct_1", "order-2", Response.of(200, "{\"id\":\"ch_x\"}"));
        Begin c = store.begin("acct_1", "order-2", fp);
        check("after completion the duplicate replays", c.outcome() == Outcome.REPLAY && c.response().replayed() && c.response().body().equals("{\"id\":\"ch_x\"}"));
        checkThrows("complete without reservation is a bug", IllegalStateException.class, () -> store.complete("acct_1", "never-begun", Response.of(200, "{}")));
        checkThrows("double complete is a bug", IllegalStateException.class, () -> store.complete("acct_1", "order-2", Response.of(200, "{}")));

        // the API maps IN_PROGRESS to 409 and does not execute
        store.begin("acct_1", "order-3", request("order-3", 1999).fingerprint());
        int beforeInProgress = processor.executions();
        Response inProgress = api.charge(request("order-3", 1999));
        check("in-progress duplicate gets 409 without executing", inProgress.status() == 409 && processor.executions() == beforeInProgress);

        // declines and 5xx are responses: stored and replayed, one execution
        processor.declineNext();
        Response d1 = api.charge(request("order-4", 5000));
        Response d2 = api.charge(request("order-4", 5000));
        check("a 402 decline replays as 402", d1.status() == 402 && d2.status() == 402 && d2.replayed());
        processor.serverErrorNext();
        Response e1 = api.charge(request("order-5", 5000));
        Response e2 = api.charge(request("order-5", 5000));
        check("a 502 from the processor replays as 502 (Stripe semantics)", e1.status() == 502 && e2.status() == 502 && e2.replayed());

        // a crash releases the reservation so the retry executes
        processor.crashNext();
        int beforeCrash = processor.executions();
        checkThrows("crash propagates to the caller", RuntimeException.class, () -> api.charge(request("order-6", 1200)));
        Response afterCrash = api.charge(request("order-6", 1200));
        check("retry after a crash executes again", afterCrash.status() == 200 && !afterCrash.replayed() && processor.executions() == beforeCrash + 2);

        // in-progress lease: a reservation whose owner died is taken over after the lease
        store.begin("acct_1", "order-7", fp);
        clock.advance(29_999);
        check("still in progress just before the lease ends", store.begin("acct_1", "order-7", fp).outcome() == Outcome.IN_PROGRESS);
        clock.advance(1);
        check("lease expired: the retry takes over as NEW", store.begin("acct_1", "order-7", fp).outcome() == Outcome.NEW);

        // TTL: replay until expiry, then the key is forgotten
        FakeClock clock2 = new FakeClock(0);
        FakeProcessor processor2 = new FakeProcessor();
        IdempotencyStore store2 = new IdempotencyStore(clock2, DAY_MS, 30_000);
        PaymentsApi api2 = new PaymentsApi(store2, processor2);
        Response t1 = api2.charge(request("order-8", 1999));
        clock2.advance(DAY_MS - 1);
        check("replays one millisecond before the TTL", api2.charge(request("order-8", 1999)).replayed() && processor2.executions() == 1);
        clock2.advance(1);
        check("purge removes the expired entry", store2.purgeExpired() == 1 && store2.size() == 0);
        Response t2 = api2.charge(request("order-8", 1999));
        check("after the TTL the same request executes again", !t2.replayed() && processor2.executions() == 2 && !t2.body().equals(t1.body()));
        checkThrows("ttl must be positive", IllegalArgumentException.class, () -> new IdempotencyStore(clock2, 0, 1));

        // real threads: eight concurrent duplicates, exactly one execution
        BlockingProcessor blocking = new BlockingProcessor();
        PaymentsApi api3 = new PaymentsApi(new IdempotencyStore(new FakeClock(0), DAY_MS, 30_000), blocking);
        ConcurrentLinkedQueue<Response> results = new ConcurrentLinkedQueue<>();
        CountDownLatch start = new CountDownLatch(1);
        List<Thread> threads = new ArrayList<>();
        for (int i = 0; i < 8; i++) {
            Thread t = new Thread(() -> {
                try { start.await(); } catch (InterruptedException e) { return; }
                results.add(api3.charge(request("order-9", 1999)));
            });
            threads.add(t);
            t.start();
        }
        start.countDown();
        while (results.size() < 7) Thread.onSpinWait();      // the seven losers finish with 409 while the winner is blocked
        blocking.release.countDown();
        for (Thread t : threads) t.join();
        long ok = results.stream().filter(r -> r.status() == 200).count();
        long busy = results.stream().filter(r -> r.status() == 409).count();
        check("eight concurrent duplicates: one 200", ok == 1);
        check("eight concurrent duplicates: seven 409", busy == 7);
        check("eight concurrent duplicates: one execution", blocking.executions == 1);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
