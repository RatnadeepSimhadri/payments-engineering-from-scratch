// Tests for phases/12-payment-systems-design/02-payment-state-machine. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static final Currency USD = Currency.USD;
    static int counter = 0;
    static String id() { return "evt_" + (++counter); }

    static PaymentEvent event(EventKind kind) {
        return kind == EventKind.REFUND_REQUESTED
                ? PaymentEvent.refundRequested(id(), Money.ofMinor(100, USD))
                : PaymentEvent.of(id(), kind);
    }

    /** Drive a fresh 100.00 USD payment into the requested state along the canonical path. */
    static Payment paymentIn(PaymentState target, FakeClock clock) {
        Payment p = new Payment("pay_t", Money.ofMinor(10_000, USD), clock);
        switch (target) {
            case CREATED -> { }
            case PROCESSING -> p.apply(event(EventKind.CONFIRM), "t");
            case REQUIRES_ACTION -> { p.apply(event(EventKind.CONFIRM), "t"); p.apply(event(EventKind.REQUIRE_ACTION), "t"); }
            case SUCCEEDED -> { p.apply(event(EventKind.CONFIRM), "t"); p.apply(event(EventKind.PROCESSOR_APPROVED), "t"); }
            case FAILED -> { p.apply(event(EventKind.CONFIRM), "t"); p.apply(event(EventKind.PROCESSOR_DECLINED), "t"); }
            case CANCELED -> p.apply(event(EventKind.CANCEL), "t");
            case REFUNDING -> {
                p.apply(event(EventKind.CONFIRM), "t"); p.apply(event(EventKind.PROCESSOR_APPROVED), "t");
                p.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(2_500, USD)), "t");
            }
            case REFUNDED -> {
                p.apply(event(EventKind.CONFIRM), "t"); p.apply(event(EventKind.PROCESSOR_APPROVED), "t");
                p.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(10_000, USD)), "t");
                p.apply(event(EventKind.REFUND_SUCCEEDED), "t");
            }
        }
        return p;
    }

    public static void main(String[] args) {
        final FakeClock clock = new FakeClock(1_000);

        // --- the table: every rule moves as written ------------------------------------------------
        check("table has 11 rules", TransitionTable.RULES.size() == 11);
        for (Rule r : TransitionTable.RULES) {
            Payment p = paymentIn(r.from(), clock);
            PaymentEvent e;
            if (r.guard() == Guard.REFUND_CLEARS_BALANCE) {
                p = paymentIn(PaymentState.SUCCEEDED, clock);
                p.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(10_000, USD)), "full");
                e = event(EventKind.REFUND_SUCCEEDED);
            } else {
                e = event(r.kind());
            }
            int before = p.version();
            ApplyOutcome outcome = p.apply(e, "legal");
            check("rule " + r.from() + " + " + r.kind() + " [" + r.guard() + "] -> " + r.to(),
                    outcome == ApplyOutcome.APPLIED && p.state() == r.to() && p.version() == before + 1);
        }

        // --- every (state, kind) pair absent from the table is refused and leaves the payment untouched -----
        int illegal = 0;
        for (PaymentState from : PaymentState.values()) {
            for (EventKind kind : EventKind.values()) {
                if (TransitionTable.hasRow(from, kind)) continue;
                Payment p = paymentIn(from, clock);
                PaymentState state = p.state();
                int version = p.version();
                PaymentEvent e = event(kind);
                check("canApply false " + from + " + " + kind, !p.canApply(e));
                checkThrows("illegal " + from + " + " + kind, IllegalTransitionException.class, () -> p.apply(e, "illegal"));
                check("untouched " + from + " + " + kind, p.state() == state && p.version() == version && p.uncommitted().size() == version);
                illegal++;
            }
        }
        check("62 illegal pairs enumerated (8 states x 9 kinds - 10 legal pairs)", illegal == 62);

        // --- terminal states -----------------------------------------------------------------------------
        for (PaymentState s : PaymentState.values()) {
            boolean expected = s == PaymentState.REFUNDED || s == PaymentState.FAILED || s == PaymentState.CANCELED;
            check(s + ".isTerminal == " + expected, s.isTerminal() == expected);
        }
        check("PROCESSING cannot be canceled (money may be moving)", !TransitionTable.hasRow(PaymentState.PROCESSING, EventKind.CANCEL));

        // --- refund guards -------------------------------------------------------------------------------
        Payment s = paymentIn(PaymentState.SUCCEEDED, clock);
        checkThrows("refund over balance refused", IllegalTransitionException.class,
                () -> s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(10_001, USD)), "x"));
        checkThrows("zero refund refused", IllegalTransitionException.class,
                () -> s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(0, USD)), "x"));
        checkThrows("wrong-currency refund refused", CurrencyMismatchException.class,
                () -> s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(1, Currency.EUR)), "x"));
        check("refusals left SUCCEEDED v2", s.state() == PaymentState.SUCCEEDED && s.version() == 2);
        s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(4_000, USD)), "partial");
        check("REFUNDING holds the amount as pending", s.pendingRefund().equals(Money.ofMinor(4_000, USD)) && s.refundable().equals(Money.ofMinor(6_000, USD)));
        checkThrows("second refund while one is pending refused", IllegalTransitionException.class,
                () -> s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(1, USD)), "x"));
        s.apply(event(EventKind.REFUND_SUCCEEDED), "settled");
        check("partial refund returns to SUCCEEDED with refunded=40.00", s.state() == PaymentState.SUCCEEDED && s.refunded().equals(Money.ofMinor(4_000, USD)) && s.pendingRefund().isZero());
        s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(6_000, USD)), "rest");
        s.apply(event(EventKind.REFUND_FAILED), "processor refused");
        check("failed refund restores balance", s.state() == PaymentState.SUCCEEDED && s.refundable().equals(Money.ofMinor(6_000, USD)));
        s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(6_000, USD)), "rest again");
        s.apply(event(EventKind.REFUND_SUCCEEDED), "settled");
        check("exact remainder -> REFUNDED, refundable zero", s.state() == PaymentState.REFUNDED && s.refundable().isZero() && s.refunded().equals(s.amount()));
        checkThrows("REFUNDED refuses more refunds", IllegalTransitionException.class,
                () -> s.apply(PaymentEvent.refundRequested(id(), Money.ofMinor(1, USD)), "x"));

        // --- event validation ----------------------------------------------------------------------------
        checkThrows("REFUND_REQUESTED without amount is malformed", IllegalArgumentException.class, () -> new PaymentEvent("e", EventKind.REFUND_REQUESTED, null, ""));
        checkThrows("CONFIRM with amount is malformed", IllegalArgumentException.class, () -> new PaymentEvent("e", EventKind.CONFIRM, Money.ofMinor(1, USD), ""));
        checkThrows("non-positive payment amount refused", IllegalArgumentException.class, () -> new Payment("p", Money.ofMinor(0, USD), clock));

        // --- the log: one transition per applied event, versions contiguous, chained, timestamps from the clock --
        final FakeClock lc = new FakeClock(1_000);
        Payment p = new Payment("pay_log", Money.ofMinor(4_999, USD), lc);
        p.apply(PaymentEvent.of("e1", EventKind.CONFIRM), "confirmed");
        lc.advance(500);
        p.apply(PaymentEvent.of("e2", EventKind.REQUIRE_ACTION), "3ds");
        p.apply(PaymentEvent.of("e3", EventKind.ACTION_COMPLETED), "challenge done");
        p.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "auth 00");
        p.apply(PaymentEvent.refundRequested("e5", Money.ofMinor(1_500, USD)), "partial");
        List<Transition> log = p.history();
        check("history has one entry per applied event", log.size() == 5 && p.version() == 5);
        boolean chained = true, contiguous = true;
        for (int i = 0; i < log.size(); i++) {
            contiguous &= log.get(i).version() == i + 1;
            if (i > 0) chained &= log.get(i).fromState() == log.get(i - 1).toState();
        }
        check("versions are contiguous from 1", contiguous);
        check("entries chain fromState -> toState", chained);
        check("first entry starts at CREATED, last ends at current state", log.get(0).fromState() == PaymentState.CREATED && log.get(4).toState() == p.state());
        check("entries keep reason, event id and amount", log.get(1).reason().equals("3ds") && log.get(4).eventId().equals("e5") && log.get(4).amount().equals(Money.ofMinor(1_500, USD)));
        check("timestamps come from the clock", log.get(0).atMillis() == 1_000 && log.get(1).atMillis() == 1_500);
        checkThrows("history is read-only", UnsupportedOperationException.class, () -> p.history().clear());

        // --- duplicate event ids -------------------------------------------------------------------------
        check("redelivered event id -> DUPLICATE", p.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "redelivery") == ApplyOutcome.DUPLICATE);
        check("duplicate adds nothing", p.version() == 5 && p.state() == PaymentState.REFUNDING);
        check("same kind with a new id is judged on its merits", !p.canApply(PaymentEvent.of("e99", EventKind.PROCESSOR_APPROVED)));

        // --- replay ----------------------------------------------------------------------------------------
        Payment rebuilt = Payment.replay("pay_log", Money.ofMinor(4_999, USD), log, lc);
        check("replay reproduces state, version, pending refund", rebuilt.state() == p.state() && rebuilt.version() == 5 && rebuilt.pendingRefund().equals(Money.ofMinor(1_500, USD)));
        check("replay remembers applied event ids", rebuilt.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "x") == ApplyOutcome.DUPLICATE);
        check("replayed payment has no uncommitted work", rebuilt.uncommitted().isEmpty());
        List<Transition> gap = new ArrayList<>(log); gap.remove(2);
        checkThrows("replay detects a version gap", IllegalStateException.class, () -> Payment.replay("pay_log", Money.ofMinor(4_999, USD), gap, lc));
        List<Transition> swapped = new ArrayList<>(log);
        swapped.set(1, new Transition("pay_log", 2, "e2", EventKind.REQUIRE_ACTION, PaymentState.SUCCEEDED, PaymentState.REQUIRES_ACTION, null, "forged", 0));
        checkThrows("replay detects a fromState mismatch", IllegalStateException.class, () -> Payment.replay("pay_log", Money.ofMinor(4_999, USD), swapped, lc));

        // --- the store: save/load round trip and optimistic concurrency -------------------------------------
        FakeClock sc = new FakeClock(0);
        PaymentStore store = new PaymentStore(sc);
        Payment a = store.create("pay_s", Money.ofMinor(2_500, USD));
        a.apply(PaymentEvent.of("s1", EventKind.CONFIRM), "c");
        store.save(a);
        check("save clears uncommitted and bumps the store version", a.uncommitted().isEmpty() && store.currentVersion("pay_s") == 1);
        check("load replays to the saved state", store.load("pay_s").state() == PaymentState.PROCESSING && store.load("pay_s").version() == 1);
        check("load returns independent copies", store.load("pay_s") != store.load("pay_s"));
        Payment h1 = store.load("pay_s");
        Payment h2 = store.load("pay_s");
        h1.apply(PaymentEvent.of("s2", EventKind.PROCESSOR_APPROVED), "approved");
        h2.apply(PaymentEvent.of("s3", EventKind.PROCESSOR_DECLINED), "declined");
        store.save(h1);
        checkThrows("second writer at the same version is rejected", ConcurrencyConflictException.class, () -> store.save(h2));
        check("store kept the first writer's transition only", store.currentVersion("pay_s") == 2 && store.load("pay_s").state() == PaymentState.SUCCEEDED);
        check("loser reloads and finds its event now illegal", !store.load("pay_s").canApply(PaymentEvent.of("s4", EventKind.PROCESSOR_DECLINED)));
        Payment multi = store.load("pay_s");
        multi.apply(PaymentEvent.refundRequested("s5", Money.ofMinor(500, USD)), "r");
        multi.apply(PaymentEvent.of("s6", EventKind.REFUND_SUCCEEDED), "ok");
        store.save(multi);
        check("several uncommitted transitions save atomically", store.currentVersion("pay_s") == 4 && store.load("pay_s").refunded().equals(Money.ofMinor(500, USD)));
        checkThrows("duplicate create refused", IllegalStateException.class, () -> store.create("pay_s", Money.ofMinor(1, USD)));
        checkThrows("unknown payment load refused", IllegalStateException.class, () -> store.load("nope"));

        // --- the service and the clock-driven fake processor ------------------------------------------------
        FakeClock vc = new FakeClock(0);
        PaymentStore vs = new PaymentStore(vc);
        FakeProcessor fp = new FakeProcessor(vc, 1_000);
        PaymentService svc = new PaymentService(vs, fp);
        fp.script("pay_v", ProcessorOutcome.CHALLENGE, ProcessorOutcome.APPROVED);
        svc.create("pay_v", Money.ofMinor(4_999, USD));
        svc.confirm("pay_v", "c1");
        check("confirm persists PROCESSING before the processor answers", vs.load("pay_v").state() == PaymentState.PROCESSING && fp.calls().size() == 1);
        check("no callback before latency elapses", svc.pump() == 0 && vs.load("pay_v").state() == PaymentState.PROCESSING);
        vc.advance(999);
        check("still none at 999ms", svc.pump() == 0);
        vc.advance(1);
        check("challenge callback at 1000ms -> REQUIRES_ACTION", svc.pump() == 1 && vs.load("pay_v").state() == PaymentState.REQUIRES_ACTION);
        svc.completeAction("pay_v", "a1");
        vc.advance(1_000);
        check("re-authorization approved -> SUCCEEDED v4", svc.pump() == 1 && vs.load("pay_v").state() == PaymentState.SUCCEEDED && vs.currentVersion("pay_v") == 4);
        check("redelivered callback is a DUPLICATE", svc.deliver(new ProcessorCallback("pr_0002", "pay_v", ProcessorOutcome.APPROVED, "approved", 0)) == ApplyOutcome.DUPLICATE && vs.currentVersion("pay_v") == 4);
        fp.script("pay_v", ProcessorOutcome.REFUND_FAILED);
        svc.requestRefund("pay_v", "r1", Money.ofMinor(999, USD));
        vc.advance(1_000);
        svc.pump();
        check("failed refund -> SUCCEEDED with balance restored", vs.load("pay_v").state() == PaymentState.SUCCEEDED && vs.load("pay_v").refundable().equals(Money.ofMinor(4_999, USD)));
        svc.requestRefund("pay_v", "r2", Money.ofMinor(4_999, USD));
        vc.advance(1_000);
        svc.pump();
        check("full refund -> REFUNDED (terminal)", vs.load("pay_v").state() == PaymentState.REFUNDED);
        checkThrows("service refuses a refund on a REFUNDED payment", IllegalTransitionException.class, () -> svc.requestRefund("pay_v", "r3", Money.ofMinor(1, USD)));
        check("processor was called exactly 4 times (2 auth, 2 refund)", fp.calls().size() == 4);
        fp.script("pay_d", ProcessorOutcome.DECLINED);
        svc.create("pay_d", Money.ofMinor(100, USD));
        svc.confirm("pay_d", "d1");
        vc.advance(1_000);
        svc.pump();
        check("decline -> FAILED", vs.load("pay_d").state() == PaymentState.FAILED && vs.load("pay_d").state().isTerminal());
        checkThrows("cancel in PROCESSING refused by the service", IllegalTransitionException.class, () -> {
            svc.create("pay_c", Money.ofMinor(100, USD));
            svc.confirm("pay_c", "cc1");
            svc.cancel("pay_c", "cc2");
        });
        check("refused cancel left pay_c PROCESSING", vs.load("pay_c").state() == PaymentState.PROCESSING);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
