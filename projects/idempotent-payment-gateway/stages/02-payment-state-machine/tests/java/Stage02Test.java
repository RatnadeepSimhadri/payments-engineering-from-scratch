// Stage 2 tests for projects/idempotent-payment-gateway: the transition table, versioned history, terminal
// states, and partial refunds in integer minor units. Run by scripts/project_test.py; exits 1 on failure.
import java.util.ArrayList;
import java.util.List;

public final class Stage02Test {
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

    static Money s2Usd(String text) {
        return Money.parse(text, Currency.USD);
    }

    public static void main(String[] args) {
        // The table is the single source of truth
        check("created -> processing", PaymentStateMachine.canTransition(PaymentState.CREATED, PaymentState.PROCESSING));
        check("created -> canceled", PaymentStateMachine.canTransition(PaymentState.CREATED, PaymentState.CANCELED));
        check("created -/-> succeeded", !PaymentStateMachine.canTransition(PaymentState.CREATED, PaymentState.SUCCEEDED));
        check("processing -> succeeded|failed", PaymentStateMachine.nextStates(PaymentState.PROCESSING).size() == 2
                && PaymentStateMachine.canTransition(PaymentState.PROCESSING, PaymentState.SUCCEEDED)
                && PaymentStateMachine.canTransition(PaymentState.PROCESSING, PaymentState.FAILED));
        check("processing -/-> canceled", !PaymentStateMachine.canTransition(PaymentState.PROCESSING, PaymentState.CANCELED));
        check("succeeded -> refunded only", PaymentStateMachine.nextStates(PaymentState.SUCCEEDED).size() == 1
                && PaymentStateMachine.canTransition(PaymentState.SUCCEEDED, PaymentState.REFUNDED));
        check("terminal states have no exits", PaymentStateMachine.nextStates(PaymentState.FAILED).isEmpty()
                && PaymentStateMachine.nextStates(PaymentState.CANCELED).isEmpty()
                && PaymentStateMachine.nextStates(PaymentState.REFUNDED).isEmpty());
        check("isTerminal", PaymentState.FAILED.isTerminal() && PaymentState.CANCELED.isTerminal() && PaymentState.REFUNDED.isTerminal()
                && !PaymentState.SUCCEEDED.isTerminal() && !PaymentState.CREATED.isTerminal() && !PaymentState.PROCESSING.isTerminal());
        check("wire names are lower-case", PaymentState.SUCCEEDED.wire().equals("succeeded") && PaymentState.CANCELED.wire().equals("canceled"));

        // Happy path with versions and history
        FakeClock clock = new FakeClock(5_000);
        Payment p = new Payment("pay_1", s2Usd("50.00"), clock);
        check("new payment is CREATED v1", p.state() == PaymentState.CREATED && p.version() == 1 && p.history().isEmpty());
        check("nothing captured yet", p.captured().isZero() && p.refunded().isZero() && p.refundable().isZero());
        p.startProcessing();
        check("processing v2", p.state() == PaymentState.PROCESSING && p.version() == 2);
        clock.advance(250);
        p.succeed("auth-77");
        check("succeeded v3", p.state() == PaymentState.SUCCEEDED && p.version() == 3);
        check("captured equals amount", p.captured().equals(s2Usd("50.00")) && p.refundable().equals(s2Usd("50.00")));
        check("processor reference kept", p.processorReference().equals("auth-77"));
        check("history has two entries", p.history().size() == 2);
        check("history records from/to", p.history().get(0).from() == PaymentState.CREATED && p.history().get(0).to() == PaymentState.PROCESSING
                && p.history().get(1).from() == PaymentState.PROCESSING && p.history().get(1).to() == PaymentState.SUCCEEDED);
        check("history timestamps come from the clock", p.history().get(0).atMillis() == 5_000 && p.history().get(1).atMillis() == 5_250);
        checkThrows("history is read-only", UnsupportedOperationException.class, () -> p.history().add(null));

        // Illegal moves are rejected and leave the payment untouched
        checkThrows("succeed twice", IllegalTransitionException.class, () -> p.succeed("again"));
        checkThrows("fail after success", IllegalTransitionException.class, () -> p.fail("late"));
        checkThrows("cancel after success", IllegalTransitionException.class, () -> p.cancel("late"));
        check("rejected moves do not bump the version", p.version() == 3 && p.state() == PaymentState.SUCCEEDED);
        checkThrows("IllegalTransitionException is an IllegalStateException", IllegalStateException.class, () -> p.succeed("again"));

        Payment fresh = new Payment("pay_2", s2Usd("10.00"), clock);
        checkThrows("succeed from CREATED", IllegalTransitionException.class, () -> fresh.succeed("x"));
        checkThrows("fail from CREATED", IllegalTransitionException.class, () -> fresh.fail("x"));
        fresh.cancel("customer left");
        check("canceled from CREATED", fresh.state() == PaymentState.CANCELED && fresh.version() == 2);
        checkThrows("nothing leaves CANCELED", IllegalTransitionException.class, fresh::startProcessing);

        Payment declined = new Payment("pay_3", s2Usd("10.00"), clock);
        declined.startProcessing();
        checkThrows("cancel while PROCESSING", IllegalTransitionException.class, () -> declined.cancel("too late"));
        declined.fail("declined decl-3");
        check("failed payment captured nothing", declined.state() == PaymentState.FAILED && declined.captured().isZero());
        checkThrows("refund a FAILED payment", IllegalTransitionException.class, () -> declined.refund(s2Usd("1.00"), "no"));

        // Refunds: partial, then full, never more than captured
        check("partial refund returns remaining", p.refund(s2Usd("20.00"), "customer request").equals(s2Usd("30.00")));
        check("still SUCCEEDED after partial refund", p.state() == PaymentState.SUCCEEDED && p.refunded().equals(s2Usd("20.00")));
        check("partial refund bumps version and logs SUCCEEDED -> SUCCEEDED", p.version() == 4
                && p.history().get(2).from() == PaymentState.SUCCEEDED && p.history().get(2).to() == PaymentState.SUCCEEDED
                && p.history().get(2).reason().contains("20.00 USD"));
        checkThrows("refund more than refundable", IllegalArgumentException.class, () -> p.refund(s2Usd("30.01"), "too much"));
        checkThrows("refund zero", IllegalArgumentException.class, () -> p.refund(s2Usd("0.00"), "zero"));
        checkThrows("refund negative", IllegalArgumentException.class, () -> p.refund(s2Usd("-1.00"), "negative"));
        checkThrows("refund in another currency", CurrencyMismatchException.class, () -> p.refund(Money.parse("1.00", Currency.EUR), "eur"));
        check("rejected refunds change nothing", p.refunded().equals(s2Usd("20.00")) && p.version() == 4);
        check("second partial refund", p.refund(s2Usd("29.99"), "goodwill").equals(s2Usd("0.01")));
        check("final cent closes the payment", p.refund(s2Usd("0.01"), "last cent").isZero() && p.state() == PaymentState.REFUNDED);
        check("refunded equals captured", p.refunded().equals(p.captured()) && p.version() == 6);
        check("last transition is SUCCEEDED -> REFUNDED", p.history().get(p.history().size() - 1).to() == PaymentState.REFUNDED);
        checkThrows("nothing leaves REFUNDED", IllegalTransitionException.class, () -> p.refund(s2Usd("0.01"), "again"));

        // Construction guards
        checkThrows("amount must be positive", IllegalArgumentException.class, () -> new Payment("pay_x", s2Usd("0.00"), clock));
        checkThrows("id is required", IllegalArgumentException.class, () -> new Payment(" ", s2Usd("1.00"), clock));

        System.out.println("Stage02Test: " + passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
