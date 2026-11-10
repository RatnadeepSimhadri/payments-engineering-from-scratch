// Tests for phases/11-payment-rails/02-card-authorization-lifecycle. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.Duration;
import java.time.Instant;
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
    static final Instant T0 = Instant.parse("2026-03-01T09:00:00Z");

    static Money usd(String s) { return Money.parse(s, USD); }

    static CardTransaction auth(String amount, FakeClock clock, LifecyclePolicy policy) {
        return CardTransaction.authorize("t", usd(amount), clock, policy, "test authorization");
    }

    public static void main(String[] args) {
        // --- creation -------------------------------------------------------------------------------------
        FakeClock clock = new FakeClock(T0);
        CardTransaction t = auth("100.00", clock, LifecyclePolicy.STANDARD);
        check("authorize -> AUTHORIZED", t.state() == TransactionState.AUTHORIZED);
        check("authorize records one event", t.events().size() == 1);
        check("first event is AUTHORIZE with reason", t.events().get(0).action() == Action.AUTHORIZE
                && t.events().get(0).reason().equals("test authorization") && t.events().get(0).from() == null);
        check("expiry is now + 7 days", t.expiresAt().equals(T0.plus(Duration.ofDays(7))));
        check("nothing captured yet", t.capturedAmount().isZero() && t.remainingAuthorization().equals(usd("100.00")));
        checkThrows("authorize rejects zero amount", LimitExceededException.class,
                () -> CardTransaction.authorize("z", usd("0.00"), clock, LifecyclePolicy.STANDARD, "r"));
        checkThrows("every event needs a reason", IllegalArgumentException.class,
                () -> CardTransaction.authorize("z", usd("1.00"), clock, LifecyclePolicy.STANDARD, " "));

        // --- legal transitions ----------------------------------------------------------------------------
        CardTransaction full = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        full.capture(usd("100.00"), "ship");
        check("AUTHORIZED -capture(all)-> CAPTURED", full.state() == TransactionState.CAPTURED);
        check("captured equals authorized", full.capturedAmount().equals(usd("100.00")) && full.remainingAuthorization().isZero());

        CardTransaction partial = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        partial.capture(usd("40.00"), "first shipment");
        check("AUTHORIZED -capture(part)-> PARTIALLY_CAPTURED", partial.state() == TransactionState.PARTIALLY_CAPTURED);
        check("remaining authorization after partial capture", partial.remainingAuthorization().equals(usd("60.00")));
        partial.capture(usd("60.00"), "second shipment");
        check("PARTIALLY_CAPTURED -capture(rest)-> CAPTURED", partial.state() == TransactionState.CAPTURED);

        CardTransaction closed = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        closed.capture(usd("35.50"), "only item in stock");
        closed.closeCapture("rest cancelled");
        check("PARTIALLY_CAPTURED -close-> CAPTURED", closed.state() == TransactionState.CAPTURED);
        check("close releases the remainder as the event amount", closed.events().get(2).amount().equals(usd("64.50"))
                && closed.events().get(2).action() == Action.CLOSE_CAPTURE);
        check("captured amount unchanged by close", closed.capturedAmount().equals(usd("35.50")));
        check("remaining authorization is zero once the hold is closed", closed.remainingAuthorization().isZero());

        CardTransaction voided = auth("25.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        voided.voidAuthorization("customer cancelled");
        check("AUTHORIZED -void-> VOIDED", voided.state() == TransactionState.VOIDED && voided.state().isTerminal());

        // --- refund arithmetic ----------------------------------------------------------------------------
        CardTransaction r = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        r.capture(usd("60.00"), "ship");
        r.closeCapture("done");
        r.refund(usd("25.00"), "one item returned");
        check("CAPTURED -refund(part)-> PARTIALLY_REFUNDED", r.state() == TransactionState.PARTIALLY_REFUNDED);
        check("net settled = captured - refunded = 35.00", r.netSettled().equals(usd("35.00")));
        r.refund(usd("35.00"), "second item returned");
        check("PARTIALLY_REFUNDED -refund(rest)-> REFUNDED", r.state() == TransactionState.REFUNDED);
        check("net settled is zero after full refund", r.netSettled().isZero());
        checkThrows("REFUNDED is terminal: no further refund", IllegalTransitionException.class, () -> r.refund(usd("0.01"), "again"));

        CardTransaction over = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        over.capture(usd("100.00"), "ship");
        checkThrows("refund above captured rejected", LimitExceededException.class, () -> over.refund(usd("100.01"), "too much"));
        check("failed refund leaves state and amounts untouched", over.state() == TransactionState.CAPTURED && over.refundedAmount().isZero());
        checkThrows("refund of zero rejected", LimitExceededException.class, () -> over.refund(usd("0.00"), "zero"));

        // --- capture ceiling and tolerance ----------------------------------------------------------------
        CardTransaction strict = auth("100.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        checkThrows("capture above authorized rejected without tolerance", LimitExceededException.class,
                () -> strict.capture(usd("100.01"), "tip"));
        check("failed capture leaves AUTHORIZED", strict.state() == TransactionState.AUTHORIZED && strict.events().size() == 1);

        LifecyclePolicy tips = LifecyclePolicy.forMcc("5812");
        check("ceiling with 15% tolerance is 115.00", tips.captureCeiling(usd("100.00")).equals(usd("115.00")));
        check("ceiling rounds DOWN on fractional cents", tips.captureCeiling(usd("0.33")).equals(usd("0.37")));
        CardTransaction tolerant = auth("100.00", new FakeClock(T0), tips);
        tolerant.capture(usd("115.00"), "bill plus tip");
        check("over-capture within tolerance -> CAPTURED", tolerant.state() == TransactionState.CAPTURED
                && tolerant.capturedAmount().equals(usd("115.00")));
        CardTransaction tooMuch = auth("100.00", new FakeClock(T0), tips);
        checkThrows("over-capture above tolerance rejected", LimitExceededException.class, () -> tooMuch.capture(usd("115.01"), "tip"));
        CardTransaction cumulative = auth("100.00", new FakeClock(T0), tips);
        cumulative.capture(usd("90.00"), "part");
        checkThrows("cumulative captures are checked against the ceiling", LimitExceededException.class,
                () -> cumulative.capture(usd("25.01"), "part two"));
        check("unknown MCC gets the standard policy", LifecyclePolicy.forMcc("5999").overCaptureToleranceBps() == 0);

        // --- incremental authorization --------------------------------------------------------------------
        FakeClock incClock = new FakeClock(T0);
        CardTransaction inc = auth("50.00", incClock, LifecyclePolicy.STANDARD);
        incClock.advance(Duration.ofDays(2));
        inc.incrementAuthorization(usd("20.00"), "extended stay");
        check("increment raises authorized amount", inc.authorizedAmount().equals(usd("70.00")));
        check("increment keeps state AUTHORIZED", inc.state() == TransactionState.AUTHORIZED);
        check("increment resets the expiry window", inc.expiresAt().equals(T0.plus(Duration.ofDays(9))));
        inc.capture(usd("70.00"), "checkout");
        check("capture of incremented total -> CAPTURED", inc.state() == TransactionState.CAPTURED);

        // --- illegal transitions, one per forbidden edge -------------------------------------------------
        checkThrows("void after capture", IllegalTransitionException.class, () -> full.voidAuthorization("late"));
        checkThrows("capture after void", IllegalTransitionException.class, () -> voided.capture(usd("1.00"), "late"));
        checkThrows("refund from AUTHORIZED", IllegalTransitionException.class, () -> strict.refund(usd("1.00"), "early"));
        checkThrows("refund from PARTIALLY_CAPTURED", IllegalTransitionException.class, () -> {
            CardTransaction p = auth("10.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
            p.capture(usd("4.00"), "part");
            p.refund(usd("1.00"), "early");
        });
        checkThrows("increment after partial capture", IllegalTransitionException.class, () -> {
            CardTransaction p = auth("10.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
            p.capture(usd("4.00"), "part");
            p.incrementAuthorization(usd("1.00"), "late");
        });
        checkThrows("close capture from AUTHORIZED (nothing captured)", IllegalTransitionException.class, () -> strict.closeCapture("nothing"));
        checkThrows("capture after full capture", IllegalTransitionException.class, () -> full.capture(usd("1.00"), "again"));
        checkThrows("void after void", IllegalTransitionException.class, () -> voided.voidAuthorization("again"));
        try {
            full.voidAuthorization("late");
        } catch (IllegalTransitionException e) {
            check("exception carries from-state and action", e.from == TransactionState.CAPTURED && e.action == Action.VOID);
        }
        checkThrows("capture in another currency", CurrencyMismatchException.class,
                () -> strict.capture(Money.parse("1.00", Currency.EUR), "wrong currency"));

        // --- expiry via the injected clock ----------------------------------------------------------------
        FakeClock expClock = new FakeClock(T0);
        CardTransaction stale = auth("45.00", expClock, LifecyclePolicy.STANDARD);
        expClock.advance(Duration.ofDays(7).minusSeconds(1));
        check("not expired one second before the deadline", !stale.expireIfDue("probe") && stale.state() == TransactionState.AUTHORIZED);
        expClock.advance(Duration.ofSeconds(1));
        checkThrows("capture at the deadline fails as an illegal transition", IllegalTransitionException.class,
                () -> stale.capture(usd("45.00"), "late shipment"));
        check("state moved to EXPIRED", stale.state() == TransactionState.EXPIRED);
        check("expiry was persisted as an event with the hold amount", stale.events().size() == 2
                && stale.events().get(1).action() == Action.EXPIRE && stale.events().get(1).amount().equals(usd("45.00"))
                && stale.events().get(1).reason().contains("late shipment"));
        checkThrows("void after expiry", IllegalTransitionException.class, () -> stale.voidAuthorization("late"));
        check("expireIfDue is idempotent on a terminal state", !stale.expireIfDue("probe") && stale.events().size() == 2);

        FakeClock partialClock = new FakeClock(T0);
        CardTransaction partialStale = auth("100.00", partialClock, LifecyclePolicy.STANDARD);
        partialStale.capture(usd("30.00"), "first");
        partialClock.advance(Duration.ofDays(8));
        checkThrows("expired remainder of a partial capture cannot be captured", IllegalTransitionException.class,
                () -> partialStale.capture(usd("70.00"), "second"));
        check("expiry of a partial capture lands in CAPTURED, not EXPIRED", partialStale.state() == TransactionState.CAPTURED);
        check("expiry event releases the uncaptured remainder", partialStale.events().get(2).action() == Action.EXPIRE
                && partialStale.events().get(2).amount().equals(usd("70.00")) && partialStale.events().get(2).to() == TransactionState.CAPTURED);
        check("captured amount survives the expiry", partialStale.capturedAmount().equals(usd("30.00"))
                && partialStale.remainingAuthorization().isZero());
        partialStale.refund(usd("30.00"), "customer returned the shipped item");
        check("captured funds stay refundable after the hold expires", partialStale.state() == TransactionState.REFUNDED
                && partialStale.netSettled().isZero());

        FakeClock shortClock = new FakeClock(T0);
        CardTransaction shortLived = auth("10.00", shortClock, new LifecyclePolicy(Duration.ofHours(1), 0));
        shortClock.advance(Duration.ofMinutes(59));
        shortLived.capture(usd("10.00"), "in time");
        check("configurable validity: capture inside a one-hour window succeeds", shortLived.state() == TransactionState.CAPTURED);

        // --- the event log is the transaction -------------------------------------------------------------
        CardTransaction hotel = auth("120.00", new FakeClock(T0), LifecyclePolicy.STANDARD);
        hotel.incrementAuthorization(usd("30.00"), "minibar");
        hotel.capture(usd("100.00"), "folio");
        hotel.closeCapture("checkout");
        hotel.refund(usd("20.00"), "goodwill");
        List<TransactionEvent> log = hotel.events();
        check("five events in order", log.size() == 5 && log.get(0).sequence() == 1 && log.get(4).sequence() == 5);
        check("event from/to chain is consistent", log.get(1).from() == TransactionState.AUTHORIZED
                && log.get(1).to() == TransactionState.AUTHORIZED && log.get(2).to() == TransactionState.PARTIALLY_CAPTURED
                && log.get(3).to() == TransactionState.CAPTURED && log.get(4).to() == TransactionState.PARTIALLY_REFUNDED);
        check("amounts replay to the same totals", log.get(0).amount().plus(log.get(1).amount()).equals(hotel.authorizedAmount())
                && log.get(2).amount().equals(hotel.capturedAmount()) && log.get(4).amount().equals(hotel.refundedAmount()));
        check("every event has a non-blank reason", log.stream().allMatch(e -> !e.reason().isBlank()));
        checkThrows("event log is read-only", UnsupportedOperationException.class, () -> hotel.events().clear());

        // --- policy validation ----------------------------------------------------------------------------
        checkThrows("policy rejects non-positive validity", IllegalArgumentException.class, () -> new LifecyclePolicy(Duration.ZERO, 0));
        checkThrows("policy rejects tolerance above 100%", IllegalArgumentException.class, () -> new LifecyclePolicy(Duration.ofDays(1), 10_001));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
