// Tests for phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;

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

    static final long T0 = Main.T0;
    static int seq = 0;

    static Transaction tx(String account, Direction d, Channel ch, long minor, long ts) {
        return new Transaction("t" + (++seq), account, "cp-" + seq, d, ch, Money.ofMinor(minor, Currency.USD), ts);
    }

    static Transaction tx(String account, String cp, Direction d, Channel ch, long minor, long ts) {
        return new Transaction("t" + (++seq), account, cp, d, ch, Money.ofMinor(minor, Currency.USD), ts);
    }

    static List<Alert> run(Scenario s, List<Transaction> txs) { return new MonitoringEngine(List.of(s)).evaluate(txs); }

    public static void main(String[] args) {
        Scenario str = Scenario.structuring("STR", 1_000_000, 8_000, 3 * Hours.DAY, 3);
        Scenario rpt = Scenario.rapidPassThrough("RPT", 1_000_000, 9_000, 24 * Hours.HOUR);
        Scenario vel = Scenario.velocity("VEL", 6 * Hours.HOUR, 6, 4, 150);

        // Money helpers used by the scenarios
        check("80% of 10,000.00 is 8,000.00", Money.ofMinor(1_000_000, Currency.USD).basisPoints(8_000).minorUnits() == 800_000);
        check("90% of 48,500.00 is 43,650.00", Money.ofMinor(4_850_000, Currency.USD).basisPoints(9_000).minorUnits() == 4_365_000);
        checkThrows("zero-amount transaction rejected", IllegalArgumentException.class, () -> tx("a", Direction.IN, Channel.CASH, 0, T0));
        checkThrows("missing scenario parameter is an error", IllegalArgumentException.class,
                () -> new Scenario("X", ScenarioKind.STRUCTURING, Severity.HIGH, Map.of(), "").param("thresholdMinor"));
        checkThrows("duplicate scenario ids rejected", IllegalArgumentException.class, () -> new MonitoringEngine(List.of(str, str)));

        // Structuring: the textbook case
        List<Transaction> ten = new ArrayList<>();
        for (int i = 0; i < 10; i++) ten.add(tx("s", Direction.IN, Channel.CASH, 990_000, T0 + i * 7 * Hours.HOUR));
        List<Alert> a = run(str, ten);
        check("ten deposits of 9,900 produce one alert", a.size() == 1 && a.get(0).kind() == ScenarioKind.STRUCTURING);
        check("overlapping windows merged into one alert with all ten as evidence", a.get(0).evidence().size() == 10);
        check("alert total is 99,000.00", a.get(0).total().minorUnits() == 9_900_000);
        check("alert is HIGH with score 100", a.get(0).severity() == Severity.HIGH && a.get(0).score() == 100);
        check("narrative names the count and window", a.get(0).narrative().contains("cash deposits") && a.get(0).narrative().contains("3 days"));

        // Structuring: boundaries
        List<Transaction> atThreshold = List.of(tx("b", Direction.IN, Channel.CASH, 1_000_000, T0), tx("b", Direction.IN, Channel.CASH, 1_000_000, T0 + Hours.HOUR),
                tx("b", Direction.IN, Channel.CASH, 1_000_000, T0 + 2 * Hours.HOUR));
        check("deposits AT the threshold are not structuring (they are reportable)", run(str, atThreshold).isEmpty());
        List<Transaction> belowFloor = List.of(tx("c", Direction.IN, Channel.CASH, 799_999, T0), tx("c", Direction.IN, Channel.CASH, 799_999, T0 + 1),
                tx("c", Direction.IN, Channel.CASH, 799_999, T0 + 2));
        check("deposits below the 80% floor do not qualify", run(str, belowFloor).isEmpty());
        List<Transaction> twoOnly = List.of(tx("d", Direction.IN, Channel.CASH, 990_000, T0), tx("d", Direction.IN, Channel.CASH, 990_000, T0 + 1));
        check("two deposits summing over threshold but under minCount do not alert", run(str, twoOnly).isEmpty());
        List<Transaction> spread = List.of(tx("e", Direction.IN, Channel.CASH, 990_000, T0), tx("e", Direction.IN, Channel.CASH, 990_000, T0 + 4 * Hours.DAY),
                tx("e", Direction.IN, Channel.CASH, 990_000, T0 + 8 * Hours.DAY));
        check("three deposits four days apart never share a 3-day window", run(str, spread).isEmpty());
        List<Transaction> edge = List.of(tx("f", Direction.IN, Channel.CASH, 990_000, T0), tx("f", Direction.IN, Channel.CASH, 990_000, T0 + Hours.DAY),
                tx("f", Direction.IN, Channel.CASH, 990_000, T0 + 3 * Hours.DAY));
        check("window is inclusive at exactly 3 days", run(str, edge).size() == 1);
        List<Transaction> wires = List.of(tx("g", Direction.IN, Channel.WIRE, 990_000, T0), tx("g", Direction.IN, Channel.WIRE, 990_000, T0 + 1),
                tx("g", Direction.IN, Channel.WIRE, 990_000, T0 + 2));
        check("wires are not cash: structuring scenario ignores them", run(str, wires).isEmpty());
        List<Transaction> unsorted = List.of(tx("h", Direction.IN, Channel.CASH, 990_000, T0 + 2 * Hours.HOUR), tx("h", Direction.IN, Channel.CASH, 990_000, T0),
                tx("h", Direction.IN, Channel.CASH, 990_000, T0 + Hours.HOUR));
        check("input order does not matter", run(str, unsorted).size() == 1 && run(str, unsorted).get(0).evidence().get(0).timestamp() == T0);

        // Rapid pass-through
        List<Transaction> mule = List.of(tx("m", "shell", Direction.IN, Channel.WIRE, 5_000_000, T0),
                tx("m", "b1", Direction.OUT, Channel.WIRE, 2_000_000, T0 + 2 * Hours.HOUR),
                tx("m", "b2", Direction.OUT, Channel.WIRE, 2_000_000, T0 + 5 * Hours.HOUR),
                tx("m", "b3", Direction.OUT, Channel.WIRE, 600_000, T0 + 8 * Hours.HOUR));
        List<Alert> ra = run(rpt, mule);
        check("50,000 in, 46,000 out in 8h alerts", ra.size() == 1 && ra.get(0).kind() == ScenarioKind.RAPID_PASS_THROUGH);
        check("evidence is the inbound plus the outbound legs that reached 90%", ra.get(0).evidence().size() == 4);
        check("faster pass-through scores higher", ra.get(0).score() > 60);
        List<Transaction> slow = List.of(tx("n", "shell", Direction.IN, Channel.WIRE, 5_000_000, T0),
                tx("n", "b1", Direction.OUT, Channel.WIRE, 4_600_000, T0 + 25 * Hours.HOUR));
        check("outbound after the window does not count", run(rpt, slow).isEmpty());
        List<Transaction> partial = List.of(tx("o", "shell", Direction.IN, Channel.WIRE, 5_000_000, T0),
                tx("o", "b1", Direction.OUT, Channel.WIRE, 4_400_000, T0 + Hours.HOUR));
        check("88% out is under the 90% rule", run(rpt, partial).isEmpty());
        List<Transaction> cardSpend = List.of(tx("p", "employer", Direction.IN, Channel.ACH, 5_000_000, T0),
                tx("p", "merchant", Direction.OUT, Channel.CARD, 4_800_000, T0 + Hours.HOUR));
        check("card spend is not onward transfer", run(rpt, cardSpend).isEmpty());
        List<Transaction> small = List.of(tx("q", "friend", Direction.IN, Channel.ACH, 50_000, T0), tx("q", "other", Direction.OUT, Channel.ACH, 50_000, T0 + 60));
        check("inbound under minIn is ignored", run(rpt, small).isEmpty());

        // Velocity
        List<Transaction> burst = new ArrayList<>();
        for (int i = 0; i < 20; i++) burst.add(tx("v", "peer-" + i, Direction.OUT, Channel.ACH, 1_000, T0 + i * 300));
        List<Alert> va = run(vel, burst);
        check("20 transfers to 20 counterparties in 100 minutes alerts once", va.size() == 1 && va.get(0).kind() == ScenarioKind.VELOCITY);
        check("velocity evidence covers the whole burst", va.get(0).evidence().size() == 20);
        List<Transaction> sameCp = new ArrayList<>();
        for (int i = 0; i < 9; i++) sameCp.add(tx("w", "landlord", Direction.OUT, Channel.ACH, 1_000, T0 + i * 300));
        check("nine transfers to one counterparty score 50*9/6 + 50*1/4 = 87, under 150", run(vel, sameCp).isEmpty());
        List<Transaction> slowBurst = new ArrayList<>();
        for (int i = 0; i < 20; i++) slowBurst.add(tx("x", "peer-" + i, Direction.OUT, Channel.ACH, 1_000, T0 + i * 2 * Hours.HOUR));
        check("the same 20 spread over 40 hours stays under threshold", run(vel, slowBurst).isEmpty());

        // Engine: ordering, independence of accounts, merged narrative
        List<Transaction> mixed = new ArrayList<>(ten);
        mixed.addAll(mule);
        mixed.addAll(burst);
        List<Alert> all = new MonitoringEngine(List.of(str, rpt, vel)).evaluate(mixed);
        check("three accounts, three scenarios, three alerts", all.size() == 3);
        check("HIGH alerts sort before MEDIUM", all.get(0).severity() == Severity.HIGH && all.get(2).severity() == Severity.MEDIUM);
        check("accounts do not contaminate each other", all.stream().map(Alert::accountId).collect(Collectors.toSet()).equals(Set.of("s", "m", "v")));
        check("merged narrative reports the full evidence count", all.stream().filter(x -> x.kind() == ScenarioKind.STRUCTURING).findFirst().get().narrative().contains("10 transactions"));
        check("empty input, no alerts", new MonitoringEngine(List.of(str, rpt, vel)).evaluate(List.of()).isEmpty());

        // Synthetic population: planted patterns are found, the generator is deterministic
        SyntheticGenerator g1 = new SyntheticGenerator(7), g2 = new SyntheticGenerator(7);
        List<Transaction> p1 = g1.population(60, T0), p2 = g2.population(60, T0);
        check("same seed, same population", p1.size() == p2.size() && p1.get(p1.size() - 1).equals(p2.get(p2.size() - 1)));
        check("population is in the expected size range", p1.size() > 1500 && p1.size() < 2200);
        List<Alert> pa = new MonitoringEngine(Main.standardScenarios()).evaluate(p1);
        Set<String> foundPlanted = pa.stream().filter(x -> x.kind().name().equals(g1.planted.get(x.accountId()))).map(Alert::accountId).collect(Collectors.toSet());
        check("all three planted patterns are found", foundPlanted.equals(g1.planted.keySet()));
        long fps = pa.stream().filter(x -> !x.kind().name().equals(g1.planted.get(x.accountId()))).count();
        check("the cash-intensive business is a false positive at an 80% floor", fps >= 1 && pa.stream().anyMatch(x -> x.accountId().equals("acct-laundromat")));
        check("no ordinary retail account alerts", pa.stream().noneMatch(x -> x.accountId().startsWith("acct-0")));
        Scenario tighter = Scenario.structuring("STR-TIGHT", 1_000_000, 9_300, 3 * Hours.DAY, 3);
        List<Alert> tight = new MonitoringEngine(List.of(tighter)).evaluate(p1);
        check("raising the floor to 93% keeps the structurer and drops the laundromat",
                tight.stream().anyMatch(x -> x.accountId().equals("acct-structurer")) && tight.stream().noneMatch(x -> x.accountId().equals("acct-laundromat")));
        check("a different seed changes amounts but not the planted accounts", new SyntheticGenerator(99).population(60, T0).size() != p1.size()
                || !new SyntheticGenerator(99).population(60, T0).equals(p1));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
