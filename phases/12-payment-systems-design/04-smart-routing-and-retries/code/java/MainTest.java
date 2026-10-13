// Tests for phases/12-payment-systems-design/04-smart-routing-and-retries. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.Random;
import java.util.Set;

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

    static final Currency USD = Currency.USD;
    static int seq = 0;

    static PaymentRequest visa(long minor) { return new PaymentRequest("pay_t" + (++seq), Money.ofMinor(minor, USD), CardNetwork.VISA, "fp"); }

    /** acq_a is cheaper; acq_b is trusted more. With these counts acq_b ranks first for Visa. */
    static AuthRateEstimator trained() {
        AuthRateEstimator est = new AuthRateEstimator(0.85, 20, 0.995);
        for (int i = 0; i < 100; i++) est.record("acq_a", CardNetwork.VISA, i < 86);
        for (int i = 0; i < 100; i++) est.record("acq_b", CardNetwork.VISA, i < 93);
        return est;
    }

    static Router router(ScriptedGateway gw, FakeClock clock, AuthRateEstimator est, RetryBudget budget, int maxAttempts) {
        return new Router(Main.processors(), (id, c) -> new ProcessorHealth(c, 10, 5, 30_000), est, budget, gw, clock,
                new RoutingPolicy(maxAttempts, 0), new Random(1));
    }

    static String leader(Router r) { return r.rank(visa(10_000)).get(0).processor().id(); }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(0);

        // --- fees and classification --------------------------------------------------------------
        ProcessorConfig a = Main.processors().get(0);
        check("fee 2.20% + 0.20 on 100.00 = 2.40", a.fee(Money.ofMinor(10_000, USD)).equals(Money.ofMinor(240, USD)));
        check("fee rounds half-up: 2.20% of 0.25 = 0.55 -> 1 minor, + 20", a.fee(Money.ofMinor(25, USD)).equals(Money.ofMinor(21, USD)));
        check("00 is APPROVED", DeclineCodes.classify(ProcessorResponse.approved()) == DeclineClass.APPROVED);
        check("91 cascades", DeclineCodes.classify(ProcessorResponse.of("91")) == DeclineClass.SOFT_CASCADE);
        check("connect error cascades", DeclineCodes.classify(ProcessorResponse.connectError()) == DeclineClass.SOFT_CASCADE);
        check("51 is retry-later, not cascade", DeclineCodes.classify(ProcessorResponse.of("51")) == DeclineClass.SOFT_RETRY_LATER);
        check("43 stolen is HARD", DeclineCodes.classify(ProcessorResponse.of("43")) == DeclineClass.HARD);
        check("1A asks for authentication", DeclineCodes.classify(ProcessorResponse.of("1A")) == DeclineClass.AUTH_REQUIRED);
        check("timeout is AMBIGUOUS", DeclineCodes.classify(ProcessorResponse.timeout()) == DeclineClass.AMBIGUOUS);
        check("unknown code fails safe to HARD", DeclineCodes.classify(ProcessorResponse.of("Z9")) == DeclineClass.HARD);
        check("MAC 03 turns a soft 05 into HARD", DeclineCodes.classify(ProcessorResponse.of("05", "03")) == DeclineClass.HARD);
        check("MAC 21 turns a soft 91 into HARD", DeclineCodes.classify(ProcessorResponse.of("91", "21")) == DeclineClass.HARD);
        check("MAC 02 (try later) leaves 05 soft", DeclineCodes.classify(ProcessorResponse.of("05", "02")) == DeclineClass.SOFT_CASCADE);
        check("technical() only for connect error and timeout",
                ProcessorResponse.timeout().technical() && ProcessorResponse.connectError().technical() && !ProcessorResponse.of("91").technical());

        // --- estimator --------------------------------------------------------------------------------
        AuthRateEstimator fresh = new AuthRateEstimator(0.85, 20, 1.0);
        check("no data -> prior", Math.abs(fresh.estimate("x", CardNetwork.VISA) - 0.85) < 1e-12);
        for (int i = 0; i < 80; i++) fresh.record("x", CardNetwork.VISA, i < 40);
        check("80 obs at 50% + prior 20 at 85% -> 57%", Math.abs(fresh.estimate("x", CardNetwork.VISA) - 0.57) < 1e-9);
        check("estimates are per network", Math.abs(fresh.estimate("x", CardNetwork.MASTERCARD) - 0.85) < 1e-12);
        AuthRateEstimator decaying = new AuthRateEstimator(0.85, 20, 0.95);
        for (int i = 0; i < 200; i++) decaying.record("y", CardNetwork.VISA, true);
        for (int i = 0; i < 40; i++) decaying.record("y", CardNetwork.VISA, false);
        AuthRateEstimator flat = new AuthRateEstimator(0.85, 20, 1.0);
        for (int i = 0; i < 200; i++) flat.record("y", CardNetwork.VISA, true);
        for (int i = 0; i < 40; i++) flat.record("y", CardNetwork.VISA, false);
        check("decay reacts to a recent drop faster than plain counts",
                decaying.estimate("y", CardNetwork.VISA) < 0.5 && flat.estimate("y", CardNetwork.VISA) > 0.8);
        checkThrows("prior outside (0,1) refused", IllegalArgumentException.class, () -> new AuthRateEstimator(1.0, 20, 0.99));

        // --- ranking -----------------------------------------------------------------------------------
        ScriptedGateway gw = new ScriptedGateway();
        Router r = router(gw, clock, trained(), new RetryBudget(100, 10_000, 10_000), 3);
        List<Candidate> ranked = r.rank(visa(10_000));
        check("amex-only processor not eligible for Visa", ranked.size() == 2);
        check("higher auth rate beats lower fee at 100.00", ranked.get(0).processor().id().equals("acq_b"));
        check("margin = rate x (amount - fee)", Math.abs(ranked.get(0).expectedMarginMinor() - ranked.get(0).authRate() * (10_000 - 320)) < 1e-6);
        AuthRateEstimator equal = new AuthRateEstimator(0.85, 20, 1.0);
        Router tie = router(gw, clock, equal, new RetryBudget(100, 10_000, 10_000), 3);
        check("equal auth rates -> cheaper processor first", tie.rank(visa(10_000)).get(0).processor().id().equals("acq_a"));
        check("no processor for AMEX in EUR -> NO_ROUTE",
                r.route(new PaymentRequest("p_amex", Money.ofMinor(100, Currency.EUR), CardNetwork.AMEX, "fp")).outcome() == RouteOutcome.NO_ROUTE);

        // --- cascade rules -----------------------------------------------------------------------------
        gw.script("acq_b", ProcessorResponse.of("91"));
        RouteResult cascaded = r.route(visa(10_000));
        check("91 on acq_b cascades to acq_a and approves", cascaded.outcome() == RouteOutcome.APPROVED
                && cascaded.attempts().size() == 2 && "acq_a".equals(cascaded.approvedBy()));
        // Hard declines lower acq_b's estimate, so the ranking may flip; always script the current leader.
        for (String code : List.of("43", "54", "59", "14", "N7")) {
            gw.script(leader(r), ProcessorResponse.of(code));
            RouteResult res = r.route(visa(10_000));
            check("hard " + code + " stops after one attempt", res.outcome() == RouteOutcome.DECLINED && res.attempts().size() == 1);
        }
        gw.script(leader(r), ProcessorResponse.of("51"));
        RouteResult nsf = r.route(visa(10_000));
        check("51 stops: same issuer would say the same", nsf.attempts().size() == 1 && nsf.stopReason().contains("retry later"));
        gw.script(leader(r), ProcessorResponse.of("1A"));
        check("1A stops for step-up", r.route(visa(10_000)).stopReason().contains("3-D Secure"));
        int callsBefore = gw.calls().size();
        gw.script(leader(r), ProcessorResponse.timeout());
        RouteResult to = r.route(visa(10_000));
        check("timeout -> PENDING_UNKNOWN, never cascaded", to.outcome() == RouteOutcome.PENDING_UNKNOWN
                && to.attempts().size() == 1 && gw.calls().size() == callsBefore + 1);
        gw.script("acq_b", ProcessorResponse.of("05")).script("acq_a", ProcessorResponse.of("05"));
        RouteResult both = r.route(visa(10_000));
        check("two soft declines exhaust the eligible list", both.outcome() == RouteOutcome.DECLINED && both.attempts().size() == 2
                && both.stopReason().contains("all eligible"));

        // --- attempt cap ---------------------------------------------------------------------------------
        List<ProcessorConfig> three = List.of(
                new ProcessorConfig("p1", EnumSet.of(CardNetwork.VISA), Set.of(USD), 200, 0),
                new ProcessorConfig("p2", EnumSet.of(CardNetwork.VISA), Set.of(USD), 250, 0),
                new ProcessorConfig("p3", EnumSet.of(CardNetwork.VISA), Set.of(USD), 300, 0));
        ScriptedGateway g3 = new ScriptedGateway().script("p1", ProcessorResponse.of("91")).script("p2", ProcessorResponse.of("91"));
        Router capped = new Router(three, (id, c) -> new ProcessorHealth(c, 10, 5, 30_000), new AuthRateEstimator(0.85, 20, 1.0),
                new RetryBudget(100, 10_000, 10_000), g3, clock, new RoutingPolicy(2, 0), new Random(1));
        RouteResult capRes = capped.route(visa(1_000));
        check("attempt cap 2 stops before p3", capRes.attempts().size() == 2 && capRes.stopReason().contains("attempt cap")
                && !g3.calls().contains("p3:" + capRes.paymentId()));

        // --- learning: health and auth rate are separate signals -----------------------------------------
        AuthRateEstimator sep = new AuthRateEstimator(0.85, 20, 1.0);
        ScriptedGateway g4 = new ScriptedGateway();
        Router learner = router(g4, clock, sep, new RetryBudget(100, 10_000, 10_000), 3);
        g4.script("acq_a", ProcessorResponse.connectError());
        learner.route(visa(1_000));
        check("connect error does not touch auth-rate counts", sep.observations("acq_a", CardNetwork.VISA) == 0);
        g4.script("acq_a", ProcessorResponse.of("05"));
        learner.route(visa(1_000));
        check("issuer decline is counted against acq_a", sep.observations("acq_a", CardNetwork.VISA) == 1
                && sep.estimate("acq_a", CardNetwork.VISA) < 0.85);

        // --- circuit breaker ------------------------------------------------------------------------------
        FakeClock hc = new FakeClock(0);
        ProcessorHealth h = new ProcessorHealth(hc, 10, 5, 30_000);
        for (int i = 0; i < 4; i++) h.recordFailure();
        check("4 failures keep it CLOSED", h.state() == CircuitState.CLOSED);
        h.recordFailure();
        check("5th failure in the window trips OPEN", h.state() == CircuitState.OPEN && !h.allows());
        hc.advance(29_999);
        check("still OPEN before cool-down", h.state() == CircuitState.OPEN);
        hc.advance(1);
        check("HALF_OPEN after cool-down", h.state() == CircuitState.HALF_OPEN && h.allows());
        h.recordFailure();
        check("failed probe re-opens", h.state() == CircuitState.OPEN);
        hc.advance(30_000);
        h.recordSuccess();
        check("successful probe closes", h.state() == CircuitState.CLOSED);
        ProcessorHealth spread = new ProcessorHealth(hc, 10, 5, 30_000);
        for (int i = 0; i < 20; i++) { if (i % 3 == 0) spread.recordFailure(); else spread.recordSuccess(); }
        check("failures spread thinly never reach 5-in-10", spread.state() == CircuitState.CLOSED);

        ScriptedGateway g5 = new ScriptedGateway();
        Router cb = router(g5, hc, trained(), new RetryBudget(100, 10_000, 10_000), 3);
        for (int i = 0; i < 5; i++) { g5.script("acq_b", ProcessorResponse.connectError()); cb.route(visa(1_000)); }
        check("open circuit removes acq_b from ranking", cb.rank(visa(1_000)).stream().noneMatch(c -> c.processor().id().equals("acq_b")));
        int before = g5.calls().size();
        RouteResult rerouted = cb.route(visa(1_000));
        check("traffic goes straight to acq_a while open", "acq_a".equals(rerouted.approvedBy()) && g5.calls().size() == before + 1);

        // --- retry budget -----------------------------------------------------------------------------------
        RetryBudget budget = new RetryBudget(100, 10_000, 2_000);
        check("initial 2 tokens pay for 2 retries", budget.tryAcquireRetry() && budget.tryAcquireRetry() && !budget.tryAcquireRetry());
        for (int i = 0; i < 10; i++) budget.recordFirstAttempt();
        check("10 first attempts earn exactly 1 retry at a 10% ratio", budget.tryAcquireRetry() && !budget.tryAcquireRetry());
        RetryBudget capB = new RetryBudget(100, 1_500, 0);
        for (int i = 0; i < 100; i++) capB.recordFirstAttempt();
        check("budget is capped", capB.balanceMilli() == 1_500);
        ScriptedGateway storm = new ScriptedGateway();
        Router sr = router(storm, clock, trained(), new RetryBudget(100, 10_000, 3_000), 3);
        int calls = 0;
        for (int i = 0; i < 20; i++) {
            storm.script("acq_a", ProcessorResponse.of("91")).script("acq_b", ProcessorResponse.of("91"));
            calls += sr.route(visa(1_000)).attempts().size();
        }
        check("storm of 20 soft declines: 20 first attempts + 5 budgeted cascades", calls == 25);

        // --- validation ---------------------------------------------------------------------------------------
        checkThrows("zero amount refused", IllegalArgumentException.class, () -> visa(0));
        checkThrows("maxAttempts 0 refused", IllegalArgumentException.class, () -> new RoutingPolicy(0, 0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
