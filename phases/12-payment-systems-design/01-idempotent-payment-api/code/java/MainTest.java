// Tests for phases/12-payment-systems-design/01-idempotent-payment-api. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicReference;

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

    record Fixture(FakeClock clock, FakeProcessor processor, IdempotencyStore store, PaymentsHandler api) {
        static Fixture create() {
            FakeClock clock = new FakeClock(1_700_000_000_000L);
            FakeProcessor processor = new FakeProcessor();
            IdempotencyStore store = new IdempotencyStore(clock, PaymentsHandler.KEY_TTL_MILLIS, PaymentsHandler.LEASE_MILLIS);
            return new Fixture(clock, processor, store, new PaymentsHandler(store, processor));
        }
    }

    static final String M = "merchant_acme";
    static final PaymentRequest ORDER = new PaymentRequest(Money.parse("19.99", Currency.USD), "tok_visa_4242", "Order 1001");

    public static void main(String[] args) throws Exception {
        // fingerprint: canonical body, stable across field order and languages
        check("canonical body sorts fields", ORDER.canonical().equals("amount=1999\ncurrency=USD\ndescription=Order 1001\nsource=tok_visa_4242\n"));
        check("fingerprint matches the cross-language vector",
                ORDER.fingerprint().equals("a3e98e16f24b06155d4539aa040c1d1362dcaafddc3debe88ee16fb23f9a644f"));
        check("fingerprint changes with amount",
                !ORDER.fingerprint().equals(new PaymentRequest(Money.parse("20.00", Currency.USD), "tok_visa_4242", "Order 1001").fingerprint()));
        check("fingerprint changes with currency",
                !ORDER.fingerprint().equals(new PaymentRequest(Money.ofMinor(1999, Currency.EUR), "tok_visa_4242", "Order 1001").fingerprint()));
        check("scoped key joins merchant and key with a control separator",
                PaymentsHandler.scopedKey(M, "key-1001").equals("merchant_acme\u001fkey-1001"));
        check("reference id is a pure function of the scoped key",
                PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey(M, "key-1001")).equals("ref_349281adde6ae74d"));
        checkThrows("request rejects non-positive amount", IllegalArgumentException.class,
                () -> new PaymentRequest(Money.ofMinor(0, Currency.USD), "tok_visa_4242", "x"));

        // happy path and replay
        {
            Fixture f = Fixture.create();
            PaymentResponse first = f.api().post(M, "key-1", ORDER);
            PaymentResponse second = f.api().post(M, "key-1", ORDER);
            check("first call is 201", first.status() == 201);
            check("first call is not marked replayed", !first.replayed());
            check("duplicate returns the stored status", second.status() == 201);
            check("duplicate returns the stored body", second.body().equals(first.body()));
            check("duplicate carries Idempotent-Replayed: true", second.replayed());
            check("processor charged exactly once", f.processor().chargeCalls() == 1);
            check("store holds one record", f.store().size() == 1);
            check("record is COMPLETED", f.store().get(PaymentsHandler.scopedKey(M, "key-1")).orElseThrow().state() == KeyState.COMPLETED);
        }

        // missing key
        {
            Fixture f = Fixture.create();
            check("missing key -> 400", f.api().post(M, null, ORDER).status() == 400);
            check("blank key -> 400", f.api().post(M, "   ", ORDER).status() == 400);
            check("control characters in a key -> 400", f.api().post(M, "key\u001fx", ORDER).status() == 400);
            check("missing merchant -> 401", f.api().post(null, "key-1", ORDER).status() == 401);
            check("missing key never reaches the processor", f.processor().chargeCalls() == 0);
        }

        // keys are scoped per merchant
        {
            Fixture f = Fixture.create();
            PaymentResponse acme = f.api().post("merchant_acme", "order-1001", ORDER);
            PaymentResponse globex = f.api().post("merchant_globex", "order-1001", ORDER);
            check("same key from two merchants -> two charges", f.processor().chargeCalls() == 2);
            check("second merchant is not a replay", acme.status() == 201 && globex.status() == 201 && !globex.replayed());
            check("second merchant gets its own payment id", !acme.body().equals(globex.body()));
            check("each merchant replays its own", f.api().post("merchant_globex", "order-1001", ORDER).body().equals(globex.body()));
            check("store holds one record per merchant", f.store().size() == 2);
        }

        // reads are not idempotent operations: no key, no record, current state
        {
            Fixture f = Fixture.create();
            PaymentResponse created = f.api().post(M, "key-r", ORDER);
            String id = PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey(M, "key-r"));
            PaymentResponse read = f.api().get(id);
            check("GET returns 200 with the current body", read.status() == 200 && read.body().equals(created.body()));
            check("GET is never marked replayed", !read.replayed() && !f.api().get(id).replayed());
            check("GET of an unknown id is 404", f.api().get("ref_missing").status() == 404);
            check("reads create no idempotency records", f.store().size() == 1);
            check("reads never call charge", f.processor().chargeCalls() == 1);
        }

        // same key, different fingerprint
        {
            Fixture f = Fixture.create();
            f.api().post(M, "key-2", ORDER);
            PaymentRequest changed = new PaymentRequest(Money.parse("29.99", Currency.USD), "tok_visa_4242", "Order 1001");
            PaymentResponse r = f.api().post(M, "key-2", changed);
            check("key reuse with different body -> 422", r.status() == 422);
            check("mismatch is not replayed", !r.replayed());
            check("mismatch does not charge", f.processor().chargeCalls() == 1);
            PaymentRequest reordered = new PaymentRequest(Money.ofMinor(1999, Currency.USD), "tok_visa_4242", "Order 1001");
            check("same meaning, different construction -> same fingerprint -> replay", f.api().post(M, "key-2", reordered).replayed());
        }

        // declines are stored and replayed with their status
        {
            Fixture f = Fixture.create();
            PaymentRequest declined = new PaymentRequest(Money.parse("10.00", Currency.USD), "tok_decline_0002", "Order 7");
            PaymentResponse d1 = f.api().post(M, "key-3", declined);
            PaymentResponse d2 = f.api().post(M, "key-3", declined);
            check("decline is 402", d1.status() == 402);
            check("decline replay keeps 402", d2.status() == 402 && d2.replayed());
            check("decline replay does not retry the card", f.processor().chargeCalls() == 1);
        }

        // concurrent duplicate while the first is IN_PROGRESS
        {
            Fixture f = Fixture.create();
            CountDownLatch inFlight = new CountDownLatch(1);
            CountDownLatch release = new CountDownLatch(1);
            f.processor().onBeforeCommit(() -> {
                inFlight.countDown();
                try { release.await(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            });
            AtomicReference<PaymentResponse> firstResult = new AtomicReference<>();
            Thread owner = new Thread(() -> firstResult.set(f.api().post(M, "key-4", ORDER)));
            owner.start();
            inFlight.await();
            PaymentResponse concurrent = f.api().post(M, "key-4", ORDER);
            check("concurrent duplicate while IN_PROGRESS -> 409", concurrent.status() == 409);
            check("409 carries Retry-After", concurrent.retryAfterSeconds() == PaymentsHandler.RETRY_AFTER_SECONDS);
            check("concurrent duplicate did not reach the processor", f.processor().chargeCalls() == 1);
            release.countDown();
            owner.join();
            f.processor().onBeforeCommit(() -> {});
            check("owner completed with 201", firstResult.get().status() == 201);
            check("after completion the duplicate is replayed", f.api().post(M, "key-4", ORDER).replayed());
        }

        // many threads, one key: exactly one charge, every response is a legal verdict
        {
            Fixture f = Fixture.create();
            int threads = 32;
            CountDownLatch start = new CountDownLatch(1);
            CountDownLatch done = new CountDownLatch(threads);
            ConcurrentHashMap<Integer, Integer> statuses = new ConcurrentHashMap<>();
            for (int i = 0; i < threads; i++) {
                new Thread(() -> {
                    try { start.await(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
                    PaymentResponse r = f.api().post(M, "key-5", ORDER);
                    statuses.merge(r.status(), 1, Integer::sum);
                    done.countDown();
                }).start();
            }
            start.countDown();
            done.await();
            check("32 racing duplicates -> exactly one processor charge", f.processor().chargeCalls() == 1);
            check("racing duplicates only ever see 201 or 409", statuses.keySet().stream().allMatch(s -> s == 201 || s == 409));
            check("every racing thread received a verdict", statuses.values().stream().mapToInt(Integer::intValue).sum() == threads);
        }

        // crash between processor commit and storing the response
        {
            Fixture f = Fixture.create();
            f.processor().crashAfterNextCharge();
            checkThrows("crash propagates out of the handler", SimulatedCrash.class, () -> f.api().post(M, "key-6", ORDER));
            check("record left IN_PROGRESS after crash", f.store().get(PaymentsHandler.scopedKey(M, "key-6")).orElseThrow().state() == KeyState.IN_PROGRESS);
            check("processor did commit the charge", f.processor().chargesStored() == 1);
            PaymentResponse tooSoon = f.api().post(M, "key-6", ORDER);
            check("retry inside the lease -> 409 (cannot tell crash from slow owner yet)", tooSoon.status() == 409);
            f.clock().advance(PaymentsHandler.LEASE_MILLIS);
            PaymentResponse recovered = f.api().post(M, "key-6", ORDER);
            check("retry after lease expiry recovers with 201", recovered.status() == 201);
            check("recovery looked up, did not re-charge", f.processor().chargeCalls() == 1);
            check("recovered body carries the original reference", recovered.body().contains(PaymentsHandler.referenceIdFor(PaymentsHandler.scopedKey(M, "key-6"))));
            check("record is now COMPLETED", f.store().get(PaymentsHandler.scopedKey(M, "key-6")).orElseThrow().state() == KeyState.COMPLETED);
            check("subsequent call is a plain replay", f.api().post(M, "key-6", ORDER).replayed());
        }

        // crash before the processor committed: recovery must charge
        {
            Fixture f = Fixture.create();
            // Simulate a death before charge(): reserve the key, then never complete it.
            String scoped = PaymentsHandler.scopedKey(M, "key-7");
            f.store().reserve(scoped, ORDER.fingerprint(), PaymentsHandler.referenceIdFor(scoped));
            f.clock().advance(PaymentsHandler.LEASE_MILLIS);
            PaymentResponse r = f.api().post(M, "key-7", ORDER);
            check("recovery with no prior charge charges once", r.status() == 201 && f.processor().chargeCalls() == 1);
        }

        // 24h TTL via the injected clock
        {
            Fixture f = Fixture.create();
            f.api().post(M, "key-8", ORDER);
            f.clock().advance(PaymentsHandler.KEY_TTL_MILLIS - 1);
            check("one ms before TTL still replays", f.api().post(M, "key-8", ORDER).replayed());
            f.clock().advance(1);
            PaymentResponse fresh = f.api().post(M, "key-8", ORDER);
            check("at TTL the key is forgotten: new charge", !fresh.replayed() && f.processor().chargeCalls() == 2);
            PaymentRequest other = new PaymentRequest(Money.parse("1.00", Currency.USD), "tok_visa_4242", "new order");
            f.clock().advance(PaymentsHandler.KEY_TTL_MILLIS);
            check("after TTL the key may carry a different body", f.api().post(M, "key-8", other).status() == 201);
        }

        // store invariants
        {
            Fixture f = Fixture.create();
            checkThrows("complete() without reserve() is a bug", IllegalStateException.class,
                    () -> f.store().complete("never-reserved", PaymentResponse.of(201, "{}")));
            ReserveOutcome a = f.store().reserve("k", "fp", "ref");
            ReserveOutcome b = f.store().reserve("k", "fp", "ref");
            check("first reserve wins", a.decision() == Reservation.RESERVED);
            check("second reserve sees IN_PROGRESS", b.decision() == Reservation.IN_PROGRESS);
            check("different fingerprint beats state", f.store().reserve("k", "other", "ref").decision() == Reservation.FINGERPRINT_MISMATCH);
        }

        // Money discipline
        check("Money toString", Money.parse("19.99", Currency.USD).toString().equals("19.99 USD"));
        checkThrows("Money rejects currency mix", CurrencyMismatchException.class,
                () -> Money.parse("1.00", Currency.USD).plus(Money.parse("1.00", Currency.EUR)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
