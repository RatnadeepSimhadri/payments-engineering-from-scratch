// Tests for phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
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

    static final long SECOND = 1_000, MINUTE = 60 * SECOND, HOUR = 60 * MINUTE, DAY = 24 * HOUR;
    static final long T0 = 1_700_000_040_000L;   // aligned to a minute boundary, so bucket edges are predictable

    static Money usd(String s) { return Money.parse(s, Currency.USD); }

    static Transaction tx(String id, String account, String card, String bin, String country, long cardAge, String amount) {
        return new Transaction(id, account, card, bin, country, cardAge, usd(amount));
    }

    static Rule rule(String name, String field, Operator op, Object value, Action action, int priority, int score) {
        return new Rule(name, field, op, value, action, priority, score, "test rule " + name);
    }

    public static void main(String[] args) {
        operators();
        slidingWindow();
        velocityService();
        engineFirstMatch();
        engineScore();
        explanations();
        cardTestingScenario();
        shadowMode();
        configurationErrors();

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    // --- each operator, on the types it supports ------------------------------------------------

    static void operators() {
        check("EQ on strings", Operator.EQ.apply("US", "US"));
        check("EQ on Money compares value and currency", Operator.EQ.apply(usd("1.00"), Money.ofMinor(100, Currency.USD)));
        check("NE on strings", Operator.NE.apply("US", "NG"));
        check("GT on longs", Operator.GT.apply(6L, 5L) && !Operator.GT.apply(5L, 5L));
        check("GTE on longs is inclusive", Operator.GTE.apply(5L, 5L));
        check("LT on longs", Operator.LT.apply(0L, 1L) && !Operator.LT.apply(1L, 1L));
        check("LTE on longs is inclusive", Operator.LTE.apply(1L, 1L));
        check("GT on Money", Operator.GT.apply(usd("2000.01"), usd("2000.00")));
        check("LT on Money", Operator.LT.apply(usd("0.99"), usd("1.00")));
        check("IN on a set", Operator.IN.apply("NG", Set.of("NG", "RU")));
        check("NOT_IN on a set", Operator.NOT_IN.apply("US", Set.of("NG", "RU")));
        checkThrows("GT on strings is a configuration error", IllegalArgumentException.class, () -> Operator.GT.apply("b", "a"));
        checkThrows("GT long vs Money is a configuration error", IllegalArgumentException.class, () -> Operator.GT.apply(5L, usd("5.00")));
        checkThrows("GT across currencies is rejected", CurrencyMismatchException.class,
                () -> Operator.GT.apply(usd("1.00"), Money.parse("1.00", Currency.EUR)));
        checkThrows("IN needs a set", IllegalArgumentException.class, () -> Operator.IN.apply("US", "US"));
    }

    // --- the ring of buckets ---------------------------------------------------------------------

    static void slidingWindow() {
        SlidingWindowCounter c = new SlidingWindowCounter(10 * MINUTE, 10, Currency.USD);
        check("bucket width is window / buckets", c.bucketMillis() == MINUTE);
        check("unknown key counts zero", c.count("card:none", T0) == 0 && c.sum("card:none", T0).minorUnits() == 0);

        for (int i = 0; i < 6; i++) c.record("card:a", usd("1.00"), T0 + i * 10 * SECOND);
        check("six events counted inside the window", c.count("card:a", T0 + MINUTE) == 6);
        check("sum inside the window", c.sum("card:a", T0 + MINUTE).equals(usd("6.00")));
        check("still six at the last millisecond before the oldest bucket expires", c.count("card:a", T0 + 10 * MINUTE - 1) == 6);
        check("the window rolls: bucket of T0 drops out at T0 + window", c.count("card:a", T0 + 10 * MINUTE) == 0);

        // an event late in a bucket expires with its bucket: effective window is between W - b and W
        SlidingWindowCounter d = new SlidingWindowCounter(10 * MINUTE, 10, Currency.USD);
        d.record("k", usd("1.00"), T0 + 59 * SECOND);
        check("event aged 9m01s is dropped because its bucket started 10m ago", d.count("k", T0 + 10 * MINUTE) == 0);
        check("event aged 9m is still counted", d.count("k", T0 + 9 * MINUTE + 59 * SECOND) == 1);

        // a ring slot reused one lap later is reset, not accumulated
        SlidingWindowCounter e = new SlidingWindowCounter(10 * MINUTE, 10, Currency.USD);
        e.record("k", usd("5.00"), T0);
        e.record("k", usd("7.00"), T0 + 10 * MINUTE);          // same slot index, next lap
        check("reused slot holds only the new lap", e.count("k", T0 + 10 * MINUTE) == 1 && e.sum("k", T0 + 10 * MINUTE).equals(usd("7.00")));

        check("keys are independent", c.count("card:b", T0 + MINUTE) == 0);
        check("evictIdle removes keys with no live bucket", c.evictIdle(T0 + HOUR) == 1 && c.trackedKeys() == 0);
        checkThrows("one counter is one currency", CurrencyMismatchException.class, () -> c.record("k", Money.parse("1.00", Currency.EUR), T0));
        checkThrows("window must divide into buckets", IllegalArgumentException.class, () -> new SlidingWindowCounter(10 * MINUTE + 1, 10, Currency.USD));
    }

    static void velocityService() {
        VelocityService v = Main.velocity();
        Map<String, Object> f1 = v.enrich(tx("t1", "acct", "card", "411111", "US", 10, "700.00"), T0);
        check("enrich keeps the transaction facts", f1.get("country").equals("US") && f1.get("card_age_days").equals(10L));
        check("the attempt counts itself", f1.get("attempts_per_card_10m").equals(1L));
        check("sum fact is Money", f1.get("amount_per_account_1d").equals(usd("700.00")));
        Map<String, Object> f2 = v.enrich(tx("t2", "acct", "card", "411111", "US", 10, "700.00"), T0 + 5 * MINUTE);
        check("second attempt sees two", f2.get("attempts_per_card_10m").equals(2L) && f2.get("amount_per_account_1d").equals(usd("1400.00")));
        Map<String, Object> f3 = v.enrich(tx("t3", "acct", "card2", "411111", "US", 10, "1.00"), T0 + 5 * MINUTE);
        check("card dimension is per card, account dimension is per account",
                f3.get("attempts_per_card_10m").equals(1L) && f3.get("amount_per_account_1d").equals(usd("1401.00")));
        checkThrows("duplicate fact names rejected", IllegalArgumentException.class,
                () -> v.define("attempts_per_card_10m", VelocityService.Dimension.CARD, VelocityService.Metric.COUNT,
                        new SlidingWindowCounter(MINUTE, 1, Currency.USD)));
    }

    // --- the engine ------------------------------------------------------------------------------

    static void engineFirstMatch() {
        RulesEngine engine = new RulesEngine(List.of(
                rule("review-country", "country", Operator.IN, Set.of("NG"), Action.REVIEW, 30, 30),
                rule("block-bin", "bin", Operator.EQ, "999999", Action.DECLINE, 5, 100),
                rule("large", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30)),
                Strategy.FIRST_MATCH, 50, 80);
        check("rules sorted by priority regardless of declaration order",
                engine.rules().get(0).name().equals("block-bin") && engine.rules().get(2).name().equals("large"));

        Decision clean = engine.decide("tx", tx("tx", "a", "c", "411111", "US", 10, "10.00").facts(), T0);
        check("no match -> ALLOW by default", clean.action() == Action.ALLOW && clean.decidingRule() == null && clean.fired().isEmpty());
        check("every rule is traced even when nothing fires", clean.traces().size() == 3);

        Decision both = engine.decide("tx", tx("tx", "a", "c", "999999", "NG", 10, "10.00").facts(), T0);
        check("first match by priority decides: DECLINE beats the later REVIEW", both.action() == Action.DECLINE && both.decidingRule().equals("block-bin"));
        check("but all fired rules are reported", both.firedRuleNames().equals(List.of("block-bin", "review-country")));

        Decision later = engine.decide("tx", tx("tx", "a", "c", "411111", "NG", 10, "6000.00").facts(), T0);
        check("priority, not severity or position, picks the decider", later.decidingRule().equals("review-country") && later.action() == Action.REVIEW);

        Map<String, Object> missing = tx("tx", "a", "c", "411111", "US", 10, "10.00").facts();
        missing.remove("country");
        Decision absent = engine.decide("tx", missing, T0);
        check("an absent fact never fires", absent.action() == Action.ALLOW && absent.traces().get(1).observed() == null);
    }

    static void engineScore() {
        RulesEngine engine = new RulesEngine(List.of(
                rule("country", "country", Operator.IN, Set.of("NG"), Action.REVIEW, 30, 30),
                rule("fresh", "card_age_days", Operator.LT, 1L, Action.REVIEW, 20, 25),
                rule("large", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30)),
                Strategy.SCORE, 50, 80);
        check("one signal below reviewAt -> ALLOW",
                engine.decide("t", tx("t", "a", "c", "4", "NG", 10, "10.00").facts(), T0).action() == Action.ALLOW);
        Decision two = engine.decide("t", tx("t", "a", "c", "4", "NG", 0, "10.00").facts(), T0);
        check("two signals sum to 55 -> REVIEW", two.score() == 55 && two.action() == Action.REVIEW);
        Decision three = engine.decide("t", tx("t", "a", "c", "4", "NG", 0, "6000.00").facts(), T0);
        check("three REVIEW rules sum to 85 -> DECLINE although no single rule says DECLINE", three.score() == 85 && three.action() == Action.DECLINE);
        check("score at exactly reviewAt is REVIEW", new RulesEngine(List.of(
                rule("a", "country", Operator.EQ, "NG", Action.REVIEW, 1, 50)), Strategy.SCORE, 50, 80)
                .decide("t", tx("t", "a", "c", "4", "NG", 10, "1.00").facts(), T0).action() == Action.REVIEW);
    }

    static void explanations() {
        RulesEngine engine = new RulesEngine(Main.cardTestingRules(), Strategy.FIRST_MATCH, 50, 80);
        Map<String, Object> facts = tx("tx-9", "a", "c", "999999", "NG", 10, "10.00").facts();
        Decision d = engine.decide("tx-9", facts, T0);
        String text = d.explain();
        check("explanation names the transaction and the action", text.startsWith("decision for tx-9: DECLINE"));
        check("explanation names the deciding rule", text.contains("by \"bin-blocklist\""));
        check("explanation shows the observed value against the rule value", text.contains("bin=999999 IN [888888, 999999]"));
        check("explanation carries the human-readable reason", text.contains("issuer range is on the internal blocklist"));
        check("explanation marks rules that did not fire", text.contains("[pass] p50 large-ticket: amount=10.00 USD GTE 5000.00 USD"));
        check("absent velocity facts are shown as absent", text.contains("attempts_per_card_10m=<absent>"));
    }

    // --- the scenario the lesson is about ---------------------------------------------------------

    static void cardTestingScenario() {
        ManualClock clock = new ManualClock(T0);
        Screener screener = new Screener(new RulesEngine(Main.cardTestingRules(), Strategy.FIRST_MATCH, 50, 80), Main.velocity(), clock);

        Decision normal = screener.screen(tx("n1", "acct_alice", "tok_alice", "411111", "US", 400, "42.50"));
        check("normal traffic is allowed", normal.action() == Action.ALLOW);

        Action[] seen = new Action[6];
        for (int i = 0; i < 6; i++) {
            clock.advance(20 * SECOND);
            seen[i] = screener.screen(tx("m" + i, "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")).action();
        }
        check("fresh card: first five micro-auths go to REVIEW, not DECLINE", seen[0] == Action.REVIEW && seen[4] == Action.REVIEW);
        check("sixth attempt inside ten minutes is DECLINED", seen[5] == Action.DECLINE);

        clock.advance(20 * SECOND);
        Decision other = screener.screen(tx("a2", "acct_alice", "tok_alice", "411111", "US", 400, "19.99"));
        check("the burst on one card does not touch another card", other.action() == Action.ALLOW);

        clock.advance(10 * MINUTE);
        Decision cooled = screener.screen(tx("m7", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"));
        check("after the window rolls the card is no longer blocked for velocity", cooled.action() == Action.REVIEW
                && !cooled.firedRuleNames().contains("card-testing-burst"));

        for (int i = 0; i < 3; i++) {
            clock.advance(MINUTE);
            screener.screen(tx("d" + i, "acct_dave", "tok_dave", "411111", "US", 90, "700.00"));
        }
        clock.advance(MINUTE);
        Decision spend = screener.screen(tx("d4", "acct_dave", "tok_dave", "411111", "US", 90, "1.00"));
        check("sum velocity: 2101.00 USD in a day trips daily-spend", spend.action() == Action.REVIEW && spend.decidingRule().equals("daily-spend"));
    }

    // --- shadow mode: a candidate rule is evaluated and traced but never acts ------------------

    static void shadowMode() {
        Rule live = rule("live-review", "country", Operator.EQ, "NG", Action.REVIEW, 20, 30);
        Rule candidate = rule("candidate-decline", "amount", Operator.GTE, usd("100.00"), Action.DECLINE, 10, 60).asShadow();
        Map<String, Object> facts = tx("t", "a", "c", "4", "NG", 1, "250.00").facts();

        Decision fm = new RulesEngine(List.of(live, candidate), Strategy.FIRST_MATCH, 50, 80).decide("t", facts, T0);
        check("shadow rule with higher priority does not decide under FIRST_MATCH",
                fm.action() == Action.REVIEW && fm.decidingRule().equals("live-review"));
        check("shadow rule is excluded from fired() but reported by shadowFired()",
                fm.firedRuleNames().equals(List.of("live-review"))
                && fm.shadowFired().size() == 1 && fm.shadowFired().get(0).rule().equals("candidate-decline"));
        check("shadow rule is marked in the explanation", fm.explain().contains("[shadow] p10 candidate-decline"));

        Decision sc = new RulesEngine(List.of(live, candidate), Strategy.SCORE, 50, 80).decide("t", facts, T0);
        check("shadow rule's score is not accumulated under SCORE", sc.score() == 30 && sc.action() == Action.ALLOW);

        Decision alone = new RulesEngine(List.of(candidate), Strategy.FIRST_MATCH, 50, 80).decide("t", facts, T0);
        check("a rule set of only shadow rules decides ALLOW by default", alone.action() == Action.ALLOW && alone.decidingRule() == null);
        check("asShadow keeps every other field and describe() says so",
                candidate.name().equals("candidate-decline") && candidate.score() == 60 && candidate.describe().endsWith("[shadow]"));
    }

    static void configurationErrors() {
        checkThrows("duplicate rule names rejected", IllegalArgumentException.class, () -> new RulesEngine(List.of(
                rule("x", "country", Operator.EQ, "US", Action.ALLOW, 1, 0),
                rule("x", "country", Operator.EQ, "NG", Action.DECLINE, 2, 0)), Strategy.FIRST_MATCH, 50, 80));
        checkThrows("thresholds must be ordered", IllegalArgumentException.class, () -> new RulesEngine(List.of(), Strategy.SCORE, 80, 50));
        checkThrows("rule needs an explanation", IllegalArgumentException.class,
                () -> new Rule("x", "country", Operator.EQ, "US", Action.ALLOW, 1, 0, ""));
        checkThrows("a type mismatch between fact and value fails loudly at evaluation", IllegalArgumentException.class,
                () -> new RulesEngine(List.of(rule("bad", "country", Operator.GT, 5L, Action.DECLINE, 1, 0)), Strategy.FIRST_MATCH, 50, 80)
                        .decide("t", tx("t", "a", "c", "4", "US", 1, "1.00").facts(), T0));
    }
}
