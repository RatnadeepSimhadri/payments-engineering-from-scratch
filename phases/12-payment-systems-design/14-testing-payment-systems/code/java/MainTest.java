// Tests for phases/12-payment-systems-design/14-testing-payment-systems. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    static Command cmd(CommandKind k, long amount) { return new Command(k, amount, ProcessorBehaviour.APPROVE); }
    static Command cmd(CommandKind k, long amount, ProcessorBehaviour b) { return new Command(k, amount, b); }

    public static void main(String[] args) {
        // Money: fee rounding is HALF_EVEN on exact ties
        check("2.9% of 100.00 = 2.90", usd(10_000).basisPoints(290).minorUnits() == 290);
        check("2.9% of 0.18 rounds 0.522 up to 1", usd(18).basisPoints(290).minorUnits() == 1);
        check("2.9% of 0.17 rounds 0.493 down to 0", usd(17).basisPoints(290).minorUnits() == 0);
        check("tie 12.5 -> 12 (HALF_EVEN)", usd(125).basisPoints(1000).minorUnits() == 12);
        check("tie 13.5 -> 14 (HALF_EVEN)", usd(135).basisPoints(1000).minorUnits() == 14);
        check("toString", usd(-37_50).toString().equals("-37.50 USD"));

        // Ledger: double entry conserves money; negative postings are refused
        Ledger ledger = new Ledger(Currency.USD);
        ledger.post(Account.CUSTOMER, Account.MERCHANT, usd(1000), "capture");
        check("posting moves money", ledger.balance(Account.CUSTOMER).minorUnits() == -1000 && ledger.balance(Account.MERCHANT).minorUnits() == 1000);
        check("ledger total is zero", ledger.total().isZero());
        checkThrows("negative posting rejected", IllegalArgumentException.class, () -> ledger.post(Account.CUSTOMER, Account.MERCHANT, usd(-1), "x"));

        // Service: happy path and state machine guards
        FakeProcessor proc = new FakeProcessor(Currency.USD);
        PaymentService svc = new PaymentService(Currency.USD, proc, Bugs.NONE);
        check("authorize", svc.authorize(usd(5000)) && svc.state == PaymentState.AUTHORIZED);
        check("partial capture", svc.capture(usd(2000)) && svc.state == PaymentState.PARTIALLY_CAPTURED);
        checkThrows("capture beyond authorization rejected", IllegalArgumentException.class, () -> svc.capture(usd(3001)));
        check("capture remainder", svc.capture(usd(3000)) && svc.state == PaymentState.CAPTURED);
        checkThrows("refund beyond captured rejected", IllegalArgumentException.class, () -> svc.refund(usd(5001)));
        check("refund", svc.refund(usd(1250)) && svc.refunded.minorUnits() == 1250);
        checkThrows("void after capture rejected", IllegalStateException.class, svc::voidAuthorization);
        checkThrows("zero amount rejected", IllegalArgumentException.class, () -> svc.refund(usd(0)));
        check("fee posted to platform", svc.ledger.balance(Account.PLATFORM_FEES).minorUnits() == 58 + 87);
        check("invariants hold after happy path", Invariants.check(svc) == null);
        check("customer balance equals -(captured - refunded)", svc.ledger.balance(Account.CUSTOMER).minorUnits() == -(5000 - 1250));

        // Service: processor behaviours
        FakeProcessor declining = new FakeProcessor(Currency.USD).script(ProcessorBehaviour.DECLINE);
        PaymentService s2 = new PaymentService(Currency.USD, declining, Bugs.NONE);
        check("decline leaves state NEW", !s2.authorize(usd(100)) && s2.state == PaymentState.NEW);
        check("script exhausted means approve", s2.authorize(usd(100)) && declining.calls == 2);
        FakeProcessor erroring = new FakeProcessor(Currency.USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.ERROR);
        PaymentService s3 = new PaymentService(Currency.USD, erroring, Bugs.NONE);
        s3.authorize(usd(100));
        checkThrows("processor 5xx propagates", IllegalStateException.class, () -> s3.capture(usd(100)));
        check("5xx posts nothing", s3.captured.isZero() && s3.ledger.total().isZero());

        // Timeouts: the ledger waits for reconcile
        FakeProcessor lost = new FakeProcessor(Currency.USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.TIMEOUT_AFTER_APPLY);
        PaymentService s4 = new PaymentService(Currency.USD, lost, Bugs.NONE);
        s4.authorize(usd(5000));
        check("timeout returns false and marks UNKNOWN", !s4.capture(usd(5000)) && s4.state == PaymentState.UNKNOWN);
        check("nothing posted on timeout", s4.captured.isZero() && s4.ledger.postings.isEmpty());
        check("processor did apply it", lost.record.captured.equals(usd(5000)));
        checkThrows("no capture while UNKNOWN", IllegalStateException.class, () -> s4.capture(usd(1)));
        s4.reconcile();
        check("reconcile posts the missing capture", s4.captured.equals(usd(5000)) && s4.state == PaymentState.CAPTURED);
        check("invariants hold after reconcile", Invariants.check(s4) == null);
        FakeProcessor never = new FakeProcessor(Currency.USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.TIMEOUT_BEFORE_APPLY);
        PaymentService s5 = new PaymentService(Currency.USD, never, Bugs.NONE);
        s5.authorize(usd(5000));
        s5.capture(usd(5000));
        s5.reconcile();
        check("timeout before apply reconciles back to AUTHORIZED", s5.state == PaymentState.AUTHORIZED && s5.captured.isZero());
        check("reconcile is a no-op when not UNKNOWN", Invariants.check(s5) == null);

        // Invariants detect each injected bug on a hand-written scenario
        check("over-capture bug caught", ScenarioRunner.run(new Scenario("oc", List.of(cmd(CommandKind.AUTHORIZE, 1), cmd(CommandKind.CAPTURE, 2))),
                new Bugs(true, false, false, false)).violation().startsWith("captured"));
        check("over-refund bug caught", ScenarioRunner.run(new Scenario("or", List.of(cmd(CommandKind.AUTHORIZE, 1), cmd(CommandKind.CAPTURE, 1), cmd(CommandKind.REFUND, 2))),
                new Bugs(false, true, false, false)).violation().startsWith("refunded"));
        check("unbalanced fee bug caught", ScenarioRunner.run(new Scenario("uf", List.of(cmd(CommandKind.AUTHORIZE, 18), cmd(CommandKind.CAPTURE, 18))),
                new Bugs(false, false, true, false)).violation().startsWith("money not conserved"));
        check("post-on-timeout bug caught", ScenarioRunner.run(new Scenario("pt", List.of(cmd(CommandKind.AUTHORIZE, 1),
                cmd(CommandKind.CAPTURE, 1, ProcessorBehaviour.TIMEOUT_BEFORE_APPLY), cmd(CommandKind.RECONCILE, 0))),
                new Bugs(false, false, false, true)).violation().contains("!= processor captured"));
        check("same scenario, no bugs, passes", ScenarioRunner.run(new Scenario("ok", List.of(cmd(CommandKind.AUTHORIZE, 18), cmd(CommandKind.CAPTURE, 18))), Bugs.NONE).passed());
        check("illegal commands are skipped, not failures", ScenarioRunner.run(new Scenario("skip", List.of(cmd(CommandKind.REFUND, 5), cmd(CommandKind.VOID, 0))), Bugs.NONE).passed());

        // Generators
        Random rng = new Random(1);
        Gen<Long> longs = Gens.longs(1, 100);
        boolean inRange = true;
        for (int i = 0; i < 1000; i++) { long v = longs.generate(rng); if (v < 1 || v > 100) inRange = false; }
        check("longs stay in range", inRange);
        check("longs shrink toward lo, lo first", longs.shrink(100L).get(0) == 1L && longs.shrink(100L).contains(99L));
        check("lo does not shrink", longs.shrink(1L).isEmpty());
        check("oneOf shrinks to earlier entries", Gens.oneOf(List.of("a", "b", "c")).shrink("c").equals(List.of("a", "b")));
        Gen<List<Long>> lists = Gens.lists(longs, 5);
        List<Long> sample = lists.generate(new Random(3));
        check("lists are 1..maxLen", sample.size() >= 1 && sample.size() <= 5);
        List<List<Long>> shrunk = lists.shrink(List.of(10L, 20L, 30L, 40L));
        check("list shrink drops halves first", shrunk.get(0).equals(List.of(30L, 40L)) && shrunk.get(1).equals(List.of(10L, 20L)));
        check("list shrink never yields empty", shrunk.stream().noneMatch(List::isEmpty));
        check("same seed, same sequence", lists.generate(new Random(9)).equals(lists.generate(new Random(9))));

        // Property harness
        PropertyResult<Long> ok = Property.forAll("n >= 1", Gens.longs(1, 1000), n -> n >= 1 ? null : "negative", 5, 200);
        check("passing property reports trials", ok.passed() && ok.trialsRun() == 200 && ok.counterexample() == null);
        PropertyResult<Long> big = Property.forAll("n < 500", Gens.longs(1, 100_000), n -> n < 500 ? null : "n=" + n, 5, 200);
        check("failing property shrinks to boundary", !big.passed() && big.counterexample().shrunk() == 500L);
        check("original is at least as large as shrunk", big.counterexample().original() >= 500L);
        PropertyResult<Long> crash = Property.forAll("crash", Gens.longs(1, 10), n -> { throw new ArithmeticException("boom"); }, 5, 10);
        check("unexpected exception is a failure", !crash.passed() && crash.counterexample().reason().startsWith("crash"));
        PropertyResult<Long> rejected = Property.forAll("rejects", Gens.longs(1, 10), n -> { throw new IllegalArgumentException("not accepted"); }, 5, 10);
        check("IllegalArgumentException counts as rejected input, not failure", rejected.passed());

        // The headline property
        check("invariants hold for 300 random sequences", ScenarioRunner.invariantsHold(Bugs.NONE, 42, 300, 12).passed());
        check("invariants hold under a second seed", ScenarioRunner.invariantsHold(Bugs.NONE, 2024, 300, 12).passed());
        PropertyResult<List<Command>> oc = ScenarioRunner.invariantsHold(new Bugs(true, false, false, false), 42, 500, 12);
        check("over-capture found and shrunk to two commands", !oc.passed() && oc.counterexample().shrunk().size() == 2);
        check("shrunk amounts are minimal", oc.counterexample().shrunk().get(0).amountMinor() == 1 && oc.counterexample().shrunk().get(1).amountMinor() == 2);
        PropertyResult<List<Command>> pt = ScenarioRunner.invariantsHold(new Bugs(false, false, false, true), 42, 2000, 12);
        check("timeout bug found with enough trials", !pt.passed() && pt.counterexample().shrunk().size() == 3);
        check("counterexample is reproducible", ScenarioRunner.run(new Scenario("replay", pt.counterexample().shrunk()), new Bugs(false, false, false, true)).violation() != null);

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
