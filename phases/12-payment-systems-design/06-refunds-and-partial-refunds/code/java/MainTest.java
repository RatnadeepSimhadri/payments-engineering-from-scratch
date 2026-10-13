// Tests for phases/12-payment-systems-design/06-refunds-and-partial-refunds. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = Collections.synchronizedList(new ArrayList<>());

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

    static final Currency USD = Currency.USD;
    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    record Fixture(FakeClock clock, Ledger ledger, RefundService svc) {}

    static Fixture fixture(FeeRefundPolicy fees) {
        FakeClock clock = new FakeClock(LocalDate.of(2026, 3, 1));
        Ledger ledger = new Ledger(USD);
        return new Fixture(clock, ledger, new RefundService(ledger, clock, new RefundPolicy(180, fees)));
    }

    public static void main(String[] args) throws Exception {
        // --- capture posting -----------------------------------------------------------------------
        Fixture f = fixture(FeeRefundPolicy.PROPORTIONAL);
        RefundService svc = f.svc();
        Capture c = svc.capture("cap", "pay", usd(10_000), 290, 30);
        check("fee 2.90% + 0.30 on 100.00 = 3.20", c.fee().equals(usd(320)));
        check("capture: scheme owes gross, merchant owed net, fee kept",
                f.ledger().balance(RefundService.SCHEME).equals(usd(10_000)) && f.ledger().balance(RefundService.MERCHANT).equals(usd(9_680))
                        && f.ledger().balance(RefundService.FEES).equals(usd(320)));
        check("remaining refundable starts at captured", svc.remainingRefundable("cap").equals(usd(10_000)));

        // --- partial refunds -------------------------------------------------------------------------
        RefundResult r1 = svc.requestRefund("r1", "cap", usd(3_000), "partial");
        check("request -> PENDING, not replayed", r1.refund().state() == RefundState.PENDING && !r1.replayed());
        check("pending consumes remaining", svc.remainingRefundable("cap").equals(usd(7_000)) && svc.pending("cap").equals(usd(3_000)));
        check("pending moved to refunds_payable", f.ledger().balance(RefundService.REFUNDS).equals(usd(3_000))
                && f.ledger().balance(RefundService.MERCHANT).equals(usd(6_680)));
        check("succeed -> true", svc.succeed("r1"));
        check("refunded 30.00, nothing pending", svc.refunded("cap").equals(usd(3_000)) && svc.pending("cap").isZero());
        check("proportional fee share floor(320 x 3000 / 10000) = 96", svc.refund("r1").feeReturned().equals(usd(96)));
        check("redelivered success is a no-op", !svc.succeed("r1") && svc.feeReturned("cap").equals(usd(96)));
        svc.requestRefund("r2", "cap", usd(2_000), "second partial");
        svc.succeed("r2");
        check("multiple partials accumulate", svc.refunded("cap").equals(usd(5_000)) && svc.remainingRefundable("cap").equals(usd(5_000)));
        check("invariants hold mid-way", svc.checkInvariants().isEmpty());

        // --- over-refund ------------------------------------------------------------------------------
        checkThrows("refund above remaining refused", OverRefundException.class, () -> svc.requestRefund("r3", "cap", usd(5_001), "too much"));
        svc.requestRefund("r4", "cap", usd(4_000), "pending big one");
        checkThrows("pending counts: 10.01 over 10.00 remaining refused", OverRefundException.class,
                () -> svc.requestRefund("r5", "cap", usd(1_001), "race"));
        check("refused requests left no refund record", svc.refundsFor("cap").size() == 3);
        RefundResult exact = svc.requestRefund("r6", "cap", usd(1_000), "exact remainder");
        check("exact remainder accepted", exact.refund().state() == RefundState.PENDING && svc.remainingRefundable("cap").isZero());
        checkThrows("one minor unit more refused", OverRefundException.class, () -> svc.requestRefund("r7", "cap", usd(1), "x"));

        // --- failure releases the reservation -----------------------------------------------------------
        svc.fail("r4", "issuer rejected credit");
        check("failed refund -> FAILED and balance refundable again", svc.refund("r4").state() == RefundState.FAILED
                && svc.remainingRefundable("cap").equals(usd(4_000)));
        check("refunds_payable back to the still-pending r6 only", f.ledger().balance(RefundService.REFUNDS).equals(usd(1_000)));
        checkThrows("cannot succeed a FAILED refund", RefundException.class, () -> svc.succeed("r4"));
        checkThrows("cannot fail a SUCCEEDED refund", RefundException.class, () -> svc.fail("r1", "late"));

        // --- full refund: fee shares sum to the fee exactly ----------------------------------------------
        svc.succeed("r6");
        svc.requestRefund("r8", "cap", usd(4_000), "rest");
        svc.succeed("r8");
        check("fully refunded", svc.refunded("cap").equals(usd(10_000)) && svc.remainingRefundable("cap").isZero());
        check("fee returned exactly 3.20", svc.feeReturned("cap").equals(usd(320)));
        check("merchant and fee accounts back to zero", f.ledger().balance(RefundService.MERCHANT).isZero()
                && f.ledger().balance(RefundService.FEES).isZero() && f.ledger().balance(RefundService.SCHEME).isZero());
        check("invariants hold after full refund", svc.checkInvariants().isEmpty());

        // --- rounding drift ------------------------------------------------------------------------------
        Fixture g = fixture(FeeRefundPolicy.PROPORTIONAL);
        Capture small = g.svc().capture("c2", "p2", usd(1_000), 290, 30);
        check("10.00 capture fee = 0.59", small.fee().equals(usd(59)));
        long[] parts = {333, 333, 334};
        for (int i = 0; i < parts.length; i++) { g.svc().requestRefund("s" + i, "c2", usd(parts[i]), "split"); g.svc().succeed("s" + i); }
        check("cumulative shares 0.19, 0.20, 0.20", g.svc().refund("s0").feeReturned().equals(usd(19))
                && g.svc().refund("s1").feeReturned().equals(usd(20)) && g.svc().refund("s2").feeReturned().equals(usd(20)));
        check("shares sum to the fee, no lost cents", g.svc().feeReturned("c2").equals(usd(59)));
        Fixture h = fixture(FeeRefundPolicy.PROPORTIONAL);
        h.svc().capture("c3", "p3", usd(1_000), 290, 30);
        for (int i = 0; i < 1_000; i++) { h.svc().requestRefund("u" + i, "c3", usd(1), "cent"); h.svc().succeed("u" + i); }
        check("1000 one-cent refunds still return exactly 0.59", h.svc().feeReturned("c3").equals(usd(59)) && h.svc().checkInvariants().isEmpty());

        // --- fee policy NONE -----------------------------------------------------------------------------
        Fixture n = fixture(FeeRefundPolicy.NONE);
        n.svc().capture("cn", "pn", usd(10_000), 290, 30);
        n.svc().requestRefund("rn", "cn", usd(10_000), "all");
        n.svc().succeed("rn");
        check("NONE: no fee returned", n.svc().feeReturned("cn").isZero());
        check("NONE: merchant owes the fee (negative payable)", n.ledger().balance(RefundService.MERCHANT).equals(usd(-320)) && n.ledger().balances());

        // --- idempotency -----------------------------------------------------------------------------------
        Fixture i = fixture(FeeRefundPolicy.PROPORTIONAL);
        i.svc().capture("ci", "pi", usd(5_000), 290, 30);
        RefundResult first = i.svc().requestRefund("ri", "ci", usd(1_000), "a");
        RefundResult replay = i.svc().requestRefund("ri", "ci", usd(1_000), "a retry");
        check("same id, same amount -> replay of the same refund", replay.replayed() && replay.refund() == first.refund());
        check("replay did not reserve twice", i.svc().pending("ci").equals(usd(1_000)) && i.ledger().balance(RefundService.REFUNDS).equals(usd(1_000)));
        checkThrows("same id, different amount -> conflict", RefundIdempotencyConflictException.class,
                () -> i.svc().requestRefund("ri", "ci", usd(2_000), "bug"));

        // --- validation ------------------------------------------------------------------------------------
        checkThrows("zero refund refused", IllegalArgumentException.class, () -> i.svc().requestRefund("z", "ci", usd(0), "x"));
        checkThrows("EUR refund on USD capture refused", CurrencyMismatchException.class,
                () -> i.svc().requestRefund("e", "ci", Money.ofMinor(100, Currency.EUR), "x"));
        checkThrows("unknown capture refused", IllegalArgumentException.class, () -> i.svc().requestRefund("u", "nope", usd(1), "x"));
        checkThrows("unbalanced journal entry refused", IllegalArgumentException.class,
                () -> new JournalEntry("bad", "x", List.of(Posting.debit("a", usd(2)), Posting.credit("b", usd(1)))));

        // --- refund window ------------------------------------------------------------------------------------
        Fixture w = fixture(FeeRefundPolicy.PROPORTIONAL);
        w.svc().capture("cw", "pw", usd(5_000), 290, 30);
        w.clock().advanceDays(180);
        check("day 180 is inside a 180-day window", w.svc().requestRefund("w1", "cw", usd(100), "edge").refund().state() == RefundState.PENDING);
        w.clock().advanceDays(1);
        checkThrows("day 181 is outside", RefundWindowClosedException.class, () -> w.svc().requestRefund("w2", "cw", usd(100), "late"));
        check("a pending refund requested in-window can still settle after it closes", w.svc().succeed("w1"));

        // --- concurrency: 10 threads race for 15.00 each on a 100.00 capture ----------------------------------
        Fixture cc = fixture(FeeRefundPolicy.PROPORTIONAL);
        cc.svc().capture("cr", "pr", usd(10_000), 290, 30);
        AtomicInteger accepted = new AtomicInteger();
        AtomicInteger refused = new AtomicInteger();
        CountDownLatch start = new CountDownLatch(1);
        List<Thread> threads = new ArrayList<>();
        for (int t = 0; t < 10; t++) {
            final int id = t;
            Thread th = new Thread(() -> {
                try {
                    start.await();
                    cc.svc().requestRefund("race" + id, "cr", usd(1_500), "race");
                    accepted.incrementAndGet();
                } catch (OverRefundException e) {
                    refused.incrementAndGet();
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                }
            });
            threads.add(th);
            th.start();
        }
        start.countDown();
        for (Thread th : threads) th.join();
        check("exactly 6 of 10 concurrent 15.00 refunds fit in 100.00", accepted.get() == 6 && refused.get() == 4);
        check("remaining after the race is 10.00", cc.svc().remainingRefundable("cr").equals(usd(1_000)) && cc.svc().checkInvariants().isEmpty());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String fl : failures) System.out.println("FAIL: " + fl);
        if (!failures.isEmpty()) System.exit(1);
    }
}
