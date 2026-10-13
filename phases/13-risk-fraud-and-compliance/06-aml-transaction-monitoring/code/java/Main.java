// Lesson: phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring/docs/en.md
// AML Transaction Monitoring and Structuring Detection
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: 31 U.S.C. 5324 and 31 CFR 1010.311 (currency transaction reports, structuring); FATF Recommendations 10 and 20;
//          FinCEN SAR filing instructions. Java 21, standard library only. Every account and name is synthetic.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.Set;
import java.util.TreeMap;

// ---------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01. Integer minor units; never a float.
// ---------------------------------------------------------------------------------------------
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    /** this * bps / 10000, rounded HALF_EVEN. Used for "at least 80% of the threshold" style bands. */
    Money basisPoints(long bps) {
        long product = Math.multiplyExact(minorUnits, bps);
        long q = product / 10_000, r = product % 10_000;
        long twice = Math.abs(r) * 2;
        if (twice > 10_000 || (twice == 10_000 && q % 2 != 0)) q += product < 0 ? -1 : 1;
        return new Money(q, currency);
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Step 1: the transaction. Direction and channel are what the scenarios filter on.
// ---------------------------------------------------------------------------------------------
enum Direction { IN, OUT }
enum Channel { CASH, WIRE, ACH, CARD }

record Transaction(String id, String accountId, String counterpartyId, Direction direction, Channel channel, Money amount, long timestamp) {
    Transaction {
        if (amount.minorUnits() <= 0) throw new IllegalArgumentException("transaction amount must be positive: " + amount);
    }

    String brief() {
        return String.format("%s %s %s %s %s @%s", id, direction, channel, amount, direction == Direction.IN ? "from " + counterpartyId : "to " + counterpartyId,
                Hours.format(timestamp));
    }
}

final class Hours {
    static final long HOUR = 3600, DAY = 24 * HOUR;
    private Hours() {}
    /** Day/hour offset from a fixed epoch, so output reads as "d2 13:00" rather than a Unix timestamp. */
    static String format(long ts) {
        long rel = ts - Main.T0;
        return String.format("d%d %02d:%02d", rel / DAY, (rel % DAY) / HOUR, (rel % HOUR) / 60);
    }
}

// ---------------------------------------------------------------------------------------------
// Step 2: scenarios as data. A scenario is a kind plus named parameters; the engine interprets them.
// ---------------------------------------------------------------------------------------------
enum ScenarioKind { STRUCTURING, RAPID_PASS_THROUGH, VELOCITY }
enum Severity { LOW, MEDIUM, HIGH }

record Scenario(String id, ScenarioKind kind, Severity severity, Map<String, Long> params, String description) {
    long param(String name) {
        Long v = params.get(name);
        if (v == null) throw new IllegalArgumentException("scenario " + id + " missing parameter " + name);
        return v;
    }

    /** Cash deposits each under the reporting threshold but at least `floorBps` of it, summing to >= threshold inside `windowSeconds`. */
    static Scenario structuring(String id, long thresholdMinor, long floorBps, long windowSeconds, long minCount) {
        return new Scenario(id, ScenarioKind.STRUCTURING, Severity.HIGH,
                Map.of("thresholdMinor", thresholdMinor, "floorBps", floorBps, "windowSeconds", windowSeconds, "minCount", minCount),
                "cash deposits just under the CTR threshold that aggregate above it within " + (windowSeconds / Hours.DAY) + " days");
    }

    /** Inbound funds followed by outbound transfers of at least `outBps` of the inbound amount inside `windowSeconds`. */
    static Scenario rapidPassThrough(String id, long minInMinor, long outBps, long windowSeconds) {
        return new Scenario(id, ScenarioKind.RAPID_PASS_THROUGH, Severity.HIGH,
                Map.of("minInMinor", minInMinor, "outBps", outBps, "windowSeconds", windowSeconds),
                "funds received and at least " + (outBps / 100) + "% sent onward within " + (windowSeconds / Hours.HOUR) + " hours");
    }

    /** Too many transactions or too many distinct counterparties in a window; scored, alert at >= `scoreThreshold`. */
    static Scenario velocity(String id, long windowSeconds, long baselineCount, long baselineCounterparties, long scoreThreshold) {
        return new Scenario(id, ScenarioKind.VELOCITY, Severity.MEDIUM,
                Map.of("windowSeconds", windowSeconds, "baselineCount", baselineCount, "baselineCounterparties", baselineCounterparties, "scoreThreshold", scoreThreshold),
                "transaction count and counterparty spread far above baseline within " + (windowSeconds / Hours.HOUR) + " hours");
    }
}

// ---------------------------------------------------------------------------------------------
// Step 3: alerts carry their evidence. An alert without the transactions behind it is an opinion.
// ---------------------------------------------------------------------------------------------
record Alert(String scenarioId, ScenarioKind kind, Severity severity, String accountId, long score, List<Transaction> evidence, String narrative) {
    long windowStart() { return evidence.stream().mapToLong(Transaction::timestamp).min().orElse(0); }
    long windowEnd() { return evidence.stream().mapToLong(Transaction::timestamp).max().orElse(0); }

    Money total() {
        Money t = Money.zero(evidence.get(0).amount().currency());
        for (Transaction tx : evidence) t = t.plus(tx.amount());
        return t;
    }

    String explain() {
        StringBuilder sb = new StringBuilder(String.format("[%s %s] %s on %s score=%d %s..%s: %s", severity, scenarioId, kind, accountId, score,
                Hours.format(windowStart()), Hours.format(windowEnd()), narrative));
        for (Transaction tx : evidence) sb.append("\n    ").append(tx.brief());
        return sb.toString();
    }
}

// ---------------------------------------------------------------------------------------------
// Step 4: the engine. Groups by account, sorts by time, runs each scenario's detector, merges overlaps.
// ---------------------------------------------------------------------------------------------
final class MonitoringEngine {
    private final List<Scenario> scenarios;

    MonitoringEngine(List<Scenario> scenarios) {
        Set<String> ids = new LinkedHashSet<>();
        for (Scenario s : scenarios) if (!ids.add(s.id())) throw new IllegalArgumentException("duplicate scenario id " + s.id());
        this.scenarios = List.copyOf(scenarios);
    }

    List<Alert> evaluate(List<Transaction> transactions) {
        Map<String, List<Transaction>> byAccount = new TreeMap<>();
        for (Transaction tx : transactions) byAccount.computeIfAbsent(tx.accountId(), k -> new ArrayList<>()).add(tx);
        List<Alert> alerts = new ArrayList<>();
        for (Map.Entry<String, List<Transaction>> e : byAccount.entrySet()) {
            List<Transaction> sorted = new ArrayList<>(e.getValue());
            sorted.sort(Comparator.comparingLong(Transaction::timestamp).thenComparing(Transaction::id));
            for (Scenario s : scenarios) {
                List<Alert> raw = switch (s.kind()) {
                    case STRUCTURING -> structuring(s, e.getKey(), sorted);
                    case RAPID_PASS_THROUGH -> rapidPassThrough(s, e.getKey(), sorted);
                    case VELOCITY -> velocity(s, e.getKey(), sorted);
                };
                alerts.addAll(mergeOverlapping(raw));
            }
        }
        alerts.sort(Comparator.comparing((Alert a) -> a.severity().ordinal()).reversed()
                .thenComparing(Comparator.comparingLong(Alert::score).reversed())
                .thenComparing(Alert::accountId).thenComparing(Alert::scenarioId));
        return alerts;
    }

    /** Rolling window over qualifying cash deposits: each in [floor, threshold), window sum >= threshold, count >= minCount. */
    static List<Alert> structuring(Scenario s, String account, List<Transaction> sorted) {
        Money threshold = Money.ofMinor(s.param("thresholdMinor"), Currency.USD);
        Money floor = threshold.basisPoints(s.param("floorBps"));
        long window = s.param("windowSeconds");
        int minCount = (int) s.param("minCount");
        List<Transaction> qualifying = new ArrayList<>();
        for (Transaction tx : sorted) {
            if (tx.direction() == Direction.IN && tx.channel() == Channel.CASH
                    && tx.amount().compareTo(threshold) < 0 && tx.amount().compareTo(floor) >= 0) qualifying.add(tx);
        }
        List<Alert> out = new ArrayList<>();
        int left = 0;
        Money sum = Money.zero(Currency.USD);
        for (int right = 0; right < qualifying.size(); right++) {
            sum = sum.plus(qualifying.get(right).amount());
            while (qualifying.get(right).timestamp() - qualifying.get(left).timestamp() > window) {
                sum = sum.minus(qualifying.get(left).amount());
                left++;
            }
            int count = right - left + 1;
            if (count >= minCount && sum.compareTo(threshold) >= 0) {
                List<Transaction> evidence = List.copyOf(qualifying.subList(left, right + 1));
                long score = Math.min(100, 50 + 10L * count + (sum.minorUnits() - threshold.minorUnits()) * 10 / threshold.minorUnits());
                out.add(new Alert(s.id(), s.kind(), s.severity(), account, score, evidence,
                        count + " cash deposits of " + floor + " to " + threshold + " totalling " + sum + " within " + (window / Hours.DAY) + " days"));
            }
        }
        return out;
    }

    /** For each inbound of at least minIn, outbound transfers inside the window totalling >= outBps of it. */
    static List<Alert> rapidPassThrough(Scenario s, String account, List<Transaction> sorted) {
        Money minIn = Money.ofMinor(s.param("minInMinor"), Currency.USD);
        long outBps = s.param("outBps"), window = s.param("windowSeconds");
        List<Alert> out = new ArrayList<>();
        for (int i = 0; i < sorted.size(); i++) {
            Transaction in = sorted.get(i);
            if (in.direction() != Direction.IN || in.amount().compareTo(minIn) < 0) continue;
            Money required = in.amount().basisPoints(outBps);
            Money sentOn = Money.zero(Currency.USD);
            List<Transaction> evidence = new ArrayList<>(List.of(in));
            for (int j = i + 1; j < sorted.size() && sorted.get(j).timestamp() - in.timestamp() <= window; j++) {
                Transaction tx = sorted.get(j);
                if (tx.direction() != Direction.OUT || tx.channel() == Channel.CARD) continue;
                sentOn = sentOn.plus(tx.amount());
                evidence.add(tx);
                if (sentOn.compareTo(required) >= 0) break;
            }
            if (sentOn.compareTo(required) >= 0) {
                long elapsedHours = (evidence.get(evidence.size() - 1).timestamp() - in.timestamp()) / Hours.HOUR;
                long score = Math.min(100, 60 + (window / Hours.HOUR - elapsedHours) * 40 / Math.max(1, window / Hours.HOUR));
                out.add(new Alert(s.id(), s.kind(), s.severity(), account, score, List.copyOf(evidence),
                        in.amount() + " in, " + sentOn + " out to " + (evidence.size() - 1) + " counterparties within " + elapsedHours + "h"));
            }
        }
        return out;
    }

    /** Score = 50 * count/baselineCount + 50 * counterparties/baselineCounterparties over the window; alert at >= scoreThreshold. */
    static List<Alert> velocity(Scenario s, String account, List<Transaction> sorted) {
        long window = s.param("windowSeconds"), baseCount = s.param("baselineCount"), baseCp = s.param("baselineCounterparties");
        long scoreThreshold = s.param("scoreThreshold");
        List<Alert> out = new ArrayList<>();
        int left = 0;
        for (int right = 0; right < sorted.size(); right++) {
            while (sorted.get(right).timestamp() - sorted.get(left).timestamp() > window) left++;
            List<Transaction> win = sorted.subList(left, right + 1);
            Set<String> cps = new LinkedHashSet<>();
            for (Transaction tx : win) cps.add(tx.counterpartyId());
            long score = 50L * win.size() / baseCount + 50L * cps.size() / baseCp;
            if (score >= scoreThreshold) {
                out.add(new Alert(s.id(), s.kind(), s.severity(), account, Math.min(100, score), List.copyOf(win),
                        win.size() + " transactions with " + cps.size() + " counterparties in " + (window / Hours.HOUR) + "h (baseline " + baseCount + "/" + baseCp + ")"));
            }
        }
        return out;
    }

    /** Alerts from the same scenario whose evidence overlaps are one pattern, not several. Keep the highest score. */
    static List<Alert> mergeOverlapping(List<Alert> raw) {
        List<Alert> merged = new ArrayList<>();
        for (Alert a : raw) {
            if (!merged.isEmpty()) {
                Alert last = merged.get(merged.size() - 1);
                Set<String> lastIds = new LinkedHashSet<>();
                for (Transaction tx : last.evidence()) lastIds.add(tx.id());
                boolean overlaps = a.evidence().stream().anyMatch(tx -> lastIds.contains(tx.id()));
                if (overlaps) {
                    LinkedHashMap<String, Transaction> union = new LinkedHashMap<>();
                    for (Transaction tx : last.evidence()) union.put(tx.id(), tx);
                    for (Transaction tx : a.evidence()) union.put(tx.id(), tx);
                    List<Transaction> ev = new ArrayList<>(union.values());
                    ev.sort(Comparator.comparingLong(Transaction::timestamp).thenComparing(Transaction::id));
                    Alert better = a.score() > last.score() ? a : last;
                    String peak = better.narrative().contains("; merged") ? better.narrative().substring(0, better.narrative().indexOf("; merged")) : better.narrative();
                    Alert mergedAlert = new Alert(a.scenarioId(), a.kind(), a.severity(), a.accountId(), better.score(), List.copyOf(ev), peak);
                    merged.set(merged.size() - 1, new Alert(a.scenarioId(), a.kind(), a.severity(), a.accountId(), better.score(), List.copyOf(ev),
                            peak + "; merged window holds " + ev.size() + " transactions totalling " + mergedAlert.total()));
                    continue;
                }
            }
            merged.add(a);
        }
        return merged;
    }
}

// ---------------------------------------------------------------------------------------------
// Step 5: a seeded synthetic population with planted patterns, so recall and false positives are measurable.
// ---------------------------------------------------------------------------------------------
final class SyntheticGenerator {
    private final Random rng;
    private int seq = 0;
    final Map<String, String> planted = new LinkedHashMap<>(); // account -> pattern it carries

    SyntheticGenerator(long seed) { rng = new Random(seed); }

    private String nextId() { return String.format("tx%05d", ++seq); }

    private Transaction tx(String account, String cp, Direction d, Channel ch, long minor, long ts) {
        return new Transaction(nextId(), account, cp, d, ch, Money.ofMinor(minor, Currency.USD), ts);
    }

    /** Ordinary retail behaviour: payroll in, card spend out, the odd cash deposit well under the threshold. */
    List<Transaction> normalAccount(String account, long t0, int days) {
        List<Transaction> out = new ArrayList<>();
        for (int d = 0; d < days; d++) {
            long day = t0 + d * Hours.DAY;
            if (d % 14 == 0) out.add(tx(account, "employer-" + account, Direction.IN, Channel.ACH, 180_000 + rng.nextInt(60_000), day + 6 * Hours.HOUR));
            int spends = 1 + rng.nextInt(4);
            for (int i = 0; i < spends; i++) {
                out.add(tx(account, "merchant-" + rng.nextInt(40), Direction.OUT, Channel.CARD, 500 + rng.nextInt(12_000), day + (8 + rng.nextInt(12)) * Hours.HOUR + rng.nextInt(3600)));
            }
            if (rng.nextInt(10) == 0) out.add(tx(account, "branch", Direction.IN, Channel.CASH, 2_000 + rng.nextInt(300_000), day + 10 * Hours.HOUR));
        }
        return out;
    }

    /** The classic: N cash deposits each a little under the threshold, spread over a few days. */
    List<Transaction> structurer(String account, long t0, int deposits, long thresholdMinor) {
        planted.put(account, "STRUCTURING");
        List<Transaction> out = new ArrayList<>(normalAccount(account, t0, 10));
        for (int i = 0; i < deposits; i++) {
            long amount = thresholdMinor - 5_000 - rng.nextInt(60_000); // 9,400.00 to 9,950.00 on a 10,000.00 threshold
            out.add(tx(account, "branch-" + rng.nextInt(3), Direction.IN, Channel.CASH, amount, t0 + 2 * Hours.DAY + i * 7 * Hours.HOUR));
        }
        return out;
    }

    /** A mule: a large wire in, then almost all of it wired on to several counterparties within hours. */
    List<Transaction> passThrough(String account, long t0, long inMinor, int legs) {
        planted.put(account, "RAPID_PASS_THROUGH");
        List<Transaction> out = new ArrayList<>(normalAccount(account, t0, 10));
        long in = t0 + 3 * Hours.DAY + 9 * Hours.HOUR;
        out.add(tx(account, "shell-co", Direction.IN, Channel.WIRE, inMinor, in));
        long remaining = inMinor * 9_600 / 10_000; // 96% sent on, integer arithmetic
        for (int i = 0; i < legs; i++) {
            long leg = i == legs - 1 ? remaining : remaining / (legs - i);
            remaining -= leg;
            out.add(tx(account, "beneficiary-" + i, Direction.OUT, Channel.WIRE, leg, in + (2 + i * 3) * Hours.HOUR));
        }
        return out;
    }

    /** A burst: dozens of small transfers to many counterparties in one afternoon. */
    List<Transaction> burst(String account, long t0, int count) {
        planted.put(account, "VELOCITY");
        List<Transaction> out = new ArrayList<>(normalAccount(account, t0, 10));
        long start = t0 + 5 * Hours.DAY + 13 * Hours.HOUR;
        for (int i = 0; i < count; i++) {
            out.add(tx(account, "peer-" + i, Direction.OUT, Channel.ACH, 2_000 + rng.nextInt(20_000), start + i * 5 * 60));
        }
        return out;
    }

    /** A legitimate cash-intensive business (a laundromat, a food truck): daily cash deposits that sit near the floor. */
    List<Transaction> cashBusiness(String account, long t0, int days) {
        List<Transaction> out = new ArrayList<>();
        for (int d = 0; d < days; d++) {
            long day = t0 + d * Hours.DAY;
            out.add(tx(account, "branch-0", Direction.IN, Channel.CASH, 550_000 + rng.nextInt(400_000), day + 18 * Hours.HOUR));
            if (d % 7 == 6) out.add(tx(account, "supplier-" + rng.nextInt(5), Direction.OUT, Channel.ACH, 1_500_000 + rng.nextInt(1_000_000), day + 9 * Hours.HOUR));
        }
        return out;
    }

    List<Transaction> population(int normalAccounts, long t0) {
        List<Transaction> all = new ArrayList<>();
        for (int i = 0; i < normalAccounts; i++) all.addAll(normalAccount(String.format("acct-%03d", i), t0, 10));
        all.addAll(cashBusiness("acct-laundromat", t0, 10));
        all.addAll(cashBusiness("acct-foodtruck", t0, 10));
        all.addAll(structurer("acct-structurer", t0, 10, 1_000_000));
        all.addAll(passThrough("acct-mule", t0, 4_850_000, 3));
        all.addAll(burst("acct-burst", t0, 36));
        return all;
    }
}

public final class Main {
    static final long T0 = 1_700_000_000L - (1_700_000_000L % Hours.DAY);

    static List<Scenario> standardScenarios() {
        return List.of(
                Scenario.structuring("STR-01", 1_000_000, 8_000, 3 * Hours.DAY, 3),
                Scenario.rapidPassThrough("RPT-01", 1_000_000, 9_000, 24 * Hours.HOUR),
                Scenario.velocity("VEL-01", 6 * Hours.HOUR, 6, 4, 150));
    }

    public static void main(String[] args) {
        System.out.println("== Scenarios (data, not code) ==");
        for (Scenario s : standardScenarios()) System.out.printf("  %-7s %-18s %-6s %s%n    params %s%n", s.id(), s.kind(), s.severity(), s.description(), s.params());

        System.out.println("\n== Hand-built case: ten deposits of 9,900.00 over three days ==");
        List<Transaction> tenDeposits = new ArrayList<>();
        for (int i = 0; i < 10; i++) {
            tenDeposits.add(new Transaction("dep" + i, "acct-demo", "branch", Direction.IN, Channel.CASH, Money.ofMinor(990_000, Currency.USD), T0 + i * 7 * Hours.HOUR));
        }
        MonitoringEngine engine = new MonitoringEngine(standardScenarios());
        for (Alert a : engine.evaluate(tenDeposits)) System.out.println("  " + a.explain().replace("\n", "\n  "));

        System.out.println("\n== Synthetic population: 60 ordinary accounts, two cash businesses, three planted patterns (seed 7) ==");
        SyntheticGenerator gen = new SyntheticGenerator(7);
        List<Transaction> population = gen.population(60, T0);
        List<Alert> alerts = engine.evaluate(population);
        System.out.println("  transactions: " + population.size() + ", alerts: " + alerts.size());
        for (Alert a : alerts) {
            String planted = gen.planted.getOrDefault(a.accountId(), "none");
            System.out.printf("  %-5s %-7s %-16s score=%3d planted=%-18s %s%n", a.severity(), a.scenarioId(), a.accountId(), a.score(), planted, a.narrative());
        }
        int truePositives = 0, falsePositives = 0;
        Set<String> found = new LinkedHashSet<>();
        for (Alert a : alerts) {
            if (a.kind().name().equals(gen.planted.get(a.accountId()))) { truePositives++; found.add(a.accountId()); } else falsePositives++;
        }
        System.out.printf("  planted patterns found: %d of %d; true-positive alerts %d, false-positive alerts %d%n", found.size(), gen.planted.size(), truePositives, falsePositives);

        System.out.println("\n== The first alert in full ==");
        if (!alerts.isEmpty()) System.out.println("  " + alerts.get(0).explain().replace("\n", "\n  "));
    }
}
