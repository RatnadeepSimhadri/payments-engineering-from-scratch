// Lesson: phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks/docs/en.md
// A Rules Engine with Velocity Checks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 4217 (currency exponents); ISO/IEC 7812 (issuer identification numbers / BINs);
//          PCI DSS v4.0 Requirement 3 for why a card is identified by a token, never a PAN.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.TreeSet;

// ---------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01: integer minor units paired with an ISO 4217 currency.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) {
            throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
        }
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {

    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    /** Parse "12.34" exactly; reject more decimals than the currency allows. */
    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(
                Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = result + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Nothing in this file calls System.currentTimeMillis().
// ---------------------------------------------------------------------------------------------

interface Clock {
    long nowMillis();
}

/** A clock the test or demo moves by hand. Monotone: advance() never goes backwards. */
final class ManualClock implements Clock {
    private long now;

    ManualClock(long startMillis) { this.now = startMillis; }

    @Override
    public long nowMillis() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("clock never goes backwards");
        now += millis;
    }
}

// ---------------------------------------------------------------------------------------------
// The input: one authorization attempt. The card is a token (PCI DSS), never a PAN.
// ---------------------------------------------------------------------------------------------

record Transaction(String id, String accountId, String cardToken, String bin, String country,
                   long cardAgeDays, Money amount) {
    Transaction {
        Objects.requireNonNull(id, "id");
        Objects.requireNonNull(accountId, "accountId");
        Objects.requireNonNull(cardToken, "cardToken");
        Objects.requireNonNull(amount, "amount");
    }

    /** The facts a rule can see before any velocity enrichment. Keys are the rule `field` names. */
    Map<String, Object> facts() {
        Map<String, Object> f = new LinkedHashMap<>();
        f.put("amount", amount);
        f.put("bin", bin);
        f.put("country", country);
        f.put("card_age_days", cardAgeDays);
        f.put("account_id", accountId);
        return f;
    }
}

// ---------------------------------------------------------------------------------------------
// Velocity: a sliding window approximated by a ring of time buckets.
// ---------------------------------------------------------------------------------------------

/**
 * Counts and sums events per key over a sliding window. The window of W milliseconds is split
 * into n buckets of width b = W / n; each key owns a ring of n buckets. Recording an event
 * lands it in the bucket for floor(now / b); if that ring slot holds an older lap it is reset
 * first. A query adds up every slot whose start is newer than now - W. Memory per key is O(n)
 * regardless of traffic, and the effective window is between W - b and W (the bucket width is
 * the precision you traded for that bound).
 */
final class SlidingWindowCounter {
    private final long windowMillis;
    private final long bucketMillis;
    private final int buckets;
    private final Currency currency;
    private final Map<String, Ring> rings = new HashMap<>();

    private static final class Ring {
        final long[] starts;
        final long[] counts;
        final long[] sums;

        Ring(int n) {
            starts = new long[n];
            counts = new long[n];
            sums = new long[n];
            java.util.Arrays.fill(starts, Long.MIN_VALUE);
        }
    }

    SlidingWindowCounter(long windowMillis, int buckets, Currency currency) {
        if (buckets <= 0) throw new IllegalArgumentException("buckets must be positive");
        if (windowMillis <= 0 || windowMillis % buckets != 0) {
            throw new IllegalArgumentException("window must be a positive multiple of the bucket count");
        }
        this.windowMillis = windowMillis;
        this.bucketMillis = windowMillis / buckets;
        this.buckets = buckets;
        this.currency = Objects.requireNonNull(currency);
    }

    long windowMillis() { return windowMillis; }

    long bucketMillis() { return bucketMillis; }

    /** Record one event for the key at time now. One counter is one currency: others are a bug. */
    void record(String key, Money amount, long nowMillis) {
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
        Ring ring = rings.computeIfAbsent(key, k -> new Ring(buckets));
        long bucketStart = Math.floorDiv(nowMillis, bucketMillis) * bucketMillis;
        int idx = (int) Math.floorMod(Math.floorDiv(nowMillis, bucketMillis), (long) buckets);
        if (ring.starts[idx] != bucketStart) {          // slot holds a previous lap: reset it
            ring.starts[idx] = bucketStart;
            ring.counts[idx] = 0;
            ring.sums[idx] = 0;
        }
        ring.counts[idx]++;
        ring.sums[idx] = Math.addExact(ring.sums[idx], amount.minorUnits());
    }

    /** Events for the key inside the window ending at now. */
    long count(String key, long nowMillis) {
        Ring ring = rings.get(key);
        if (ring == null) return 0;
        long n = 0;
        for (int i = 0; i < buckets; i++) {
            if (ring.starts[i] > nowMillis - windowMillis) n += ring.counts[i];
        }
        return n;
    }

    /** Sum of amounts for the key inside the window ending at now. */
    Money sum(String key, long nowMillis) {
        Ring ring = rings.get(key);
        long total = 0;
        if (ring != null) {
            for (int i = 0; i < buckets; i++) {
                if (ring.starts[i] > nowMillis - windowMillis) total = Math.addExact(total, ring.sums[i]);
            }
        }
        return Money.ofMinor(total, currency);
    }

    /** Drop keys with no live bucket. Called by a housekeeping job, never on the hot path. */
    int evictIdle(long nowMillis) {
        int before = rings.size();
        rings.values().removeIf(ring -> {
            for (long start : ring.starts) if (start > nowMillis - windowMillis) return false;
            return true;
        });
        return before - rings.size();
    }

    int trackedKeys() { return rings.size(); }
}

/**
 * Named counters, each keyed by one transaction dimension. `enrich` records the attempt and then
 * adds one fact per counter so the engine sees velocity as just another field.
 */
final class VelocityService {
    enum Dimension {
        CARD, ACCOUNT;

        String keyFor(Transaction tx) {
            return switch (this) {
                case CARD -> "card:" + tx.cardToken();
                case ACCOUNT -> "account:" + tx.accountId();
            };
        }
    }

    enum Metric { COUNT, SUM }

    record Definition(String factName, Dimension dimension, Metric metric, SlidingWindowCounter counter) {}

    private final List<Definition> definitions = new ArrayList<>();

    VelocityService define(String factName, Dimension dimension, Metric metric, SlidingWindowCounter counter) {
        for (Definition d : definitions) {
            if (d.factName().equals(factName)) throw new IllegalArgumentException("duplicate fact: " + factName);
        }
        definitions.add(new Definition(factName, dimension, metric, counter));
        return this;
    }

    /**
     * Record the attempt, then return the transaction's facts plus one velocity fact per counter.
     * The attempt counts itself: the sixth attempt sees attempts = 6. Declined attempts are
     * recorded too, because a burst of declines is exactly the card-testing signal.
     */
    Map<String, Object> enrich(Transaction tx, long nowMillis) {
        Map<String, Object> facts = tx.facts();
        for (Definition d : definitions) {
            String key = d.dimension().keyFor(tx);
            d.counter().record(key, tx.amount(), nowMillis);
            Object value = d.metric() == Metric.COUNT ? (Object) d.counter().count(key, nowMillis) : d.counter().sum(key, nowMillis);
            facts.put(d.factName(), value);
        }
        return facts;
    }
}

// ---------------------------------------------------------------------------------------------
// Rules as data: field, operator, value, action, priority, score, explanation.
// ---------------------------------------------------------------------------------------------

/** Ordered by severity so "the harsher of two actions" is a max(). */
enum Action { ALLOW, REVIEW, DECLINE }

enum Operator {
    EQ, NE, GT, GTE, LT, LTE, IN, NOT_IN;

    /** Apply to an observed fact and the rule's value. Types must agree; a mismatch is a configuration bug. */
    boolean apply(Object observed, Object expected) {
        switch (this) {
            case EQ: return Objects.equals(observed, expected);
            case NE: return !Objects.equals(observed, expected);
            case IN: return asSet(expected).contains(observed);
            case NOT_IN: return !asSet(expected).contains(observed);
            default: {
                int cmp = compare(observed, expected);
                return switch (this) {
                    case GT -> cmp > 0;
                    case GTE -> cmp >= 0;
                    case LT -> cmp < 0;
                    case LTE -> cmp <= 0;
                    default -> throw new IllegalStateException();
                };
            }
        }
    }

    private static Set<?> asSet(Object expected) {
        if (expected instanceof Set<?> s) return s;
        throw new IllegalArgumentException("IN / NOT_IN need a set, got " + typeName(expected));
    }

    private static int compare(Object a, Object b) {
        if (a instanceof Long x && b instanceof Long y) return Long.compare(x, y);
        if (a instanceof Money x && b instanceof Money y) return x.compareTo(y);   // throws on currency mismatch
        throw new IllegalArgumentException("cannot order " + typeName(a) + " against " + typeName(b));
    }

    static String typeName(Object o) { return o == null ? "null" : o.getClass().getSimpleName(); }
}

/**
 * One rule. Lower priority number means evaluated and, under FIRST_MATCH, decided first.
 * `score` is only used under SCORE. `explanation` is what an analyst or a customer reads.
 */
record Rule(String name, String field, Operator operator, Object value, Action action, int priority, int score, String explanation,
            boolean shadow) {
    /** A live rule. Shadow mode is opt-in via {@link #asShadow()}. */
    Rule(String name, String field, Operator operator, Object value, Action action, int priority, int score, String explanation) {
        this(name, field, operator, value, action, priority, score, explanation, false);
    }

    Rule {
        if (name == null || name.isBlank()) throw new IllegalArgumentException("rule needs a name");
        if (field == null || field.isBlank()) throw new IllegalArgumentException("rule needs a field");
        Objects.requireNonNull(operator, "operator");
        Objects.requireNonNull(value, "value");
        Objects.requireNonNull(action, "action");
        if (score < 0) throw new IllegalArgumentException("score must be non-negative");
        if (explanation == null || explanation.isBlank()) throw new IllegalArgumentException("rule needs an explanation");
        if (value instanceof Set<?> s) value = Collections.unmodifiableSet(new TreeSet<>(s));   // sorted: deterministic text
    }

    /** Same rule, evaluated and traced but never acting: it cannot decide and its score is not counted. */
    Rule asShadow() { return new Rule(name, field, operator, value, action, priority, score, explanation, true); }

    String describe() {
        return "IF " + field + " " + operator + " " + value + " THEN " + action + " (p" + priority + ", +" + score + ")"
                + (shadow ? " [shadow]" : "");
    }
}

/** One line of the decision record: what the rule compared and whether it fired. */
record RuleTrace(String rule, int priority, String field, Object observed, Operator operator, Object expected,
                 Action action, int score, boolean fired, boolean shadow, String explanation) {}

enum Strategy { FIRST_MATCH, SCORE }

/**
 * The decision and its evidence. `fired` is every rule whose condition held, in priority order;
 * `traces` is every rule evaluated. Under FIRST_MATCH the action is the first fired rule's;
 * under SCORE the fired scores are summed and compared with the thresholds.
 */
record Decision(String transactionId, Action action, Strategy strategy, int score, String decidingRule,
                List<RuleTrace> traces, long decidedAtMillis) {

    /** Live rules whose condition held. Shadow rules are excluded: they did not act. */
    List<RuleTrace> fired() {
        List<RuleTrace> out = new ArrayList<>();
        for (RuleTrace t : traces) if (t.fired() && !t.shadow()) out.add(t);
        return out;
    }

    /** Shadow rules whose condition held: what a candidate rule would have done, measured without acting. */
    List<RuleTrace> shadowFired() {
        List<RuleTrace> out = new ArrayList<>();
        for (RuleTrace t : traces) if (t.fired() && t.shadow()) out.add(t);
        return out;
    }

    List<String> firedRuleNames() {
        List<String> out = new ArrayList<>();
        for (RuleTrace t : fired()) out.add(t.rule());
        return out;
    }

    String explain() {
        StringBuilder sb = new StringBuilder();
        sb.append("decision for ").append(transactionId).append(": ").append(action)
          .append(" [").append(strategy).append(", score=").append(score).append("]");
        if (strategy == Strategy.SCORE) sb.append(" by accumulated score");
        else if (decidingRule == null) sb.append(" default: no rule matched");
        else sb.append(" by \"").append(decidingRule).append("\"");
        sb.append(" at t=").append(decidedAtMillis).append('\n');
        for (RuleTrace t : traces) {
            sb.append(!t.fired() ? "  [pass] " : t.shadow() ? "  [shadow] " : "  [fire] ")
              .append("p").append(t.priority()).append(' ').append(t.rule()).append(": ")
              .append(t.field()).append('=').append(t.observed() == null ? "<absent>" : t.observed())
              .append(' ').append(t.operator()).append(' ').append(t.expected());
            if (t.fired()) sb.append(" -> ").append(t.action()).append(" (+").append(t.score()).append("): ").append(t.explanation());
            sb.append('\n');
        }
        return sb.toString();
    }
}

final class RulesEngine {
    private final List<Rule> rules;
    private final Strategy strategy;
    private final int reviewAt;
    private final int declineAt;

    RulesEngine(List<Rule> rules, Strategy strategy, int reviewAt, int declineAt) {
        Set<String> names = new HashSet<>();
        for (Rule r : rules) {
            if (!names.add(r.name())) throw new IllegalArgumentException("duplicate rule name: " + r.name());
        }
        if (reviewAt <= 0 || declineAt <= reviewAt) throw new IllegalArgumentException("need 0 < reviewAt < declineAt");
        List<Rule> sorted = new ArrayList<>(rules);
        sorted.sort((a, b) -> Integer.compare(a.priority(), b.priority()));   // stable: ties keep declaration order
        this.rules = Collections.unmodifiableList(sorted);
        this.strategy = Objects.requireNonNull(strategy);
        this.reviewAt = reviewAt;
        this.declineAt = declineAt;
    }

    List<Rule> rules() { return rules; }

    /**
     * Evaluate every rule against the facts (cheap, and the full trace is the audit record), then
     * decide: FIRST_MATCH takes the first fired rule's action; SCORE sums fired scores.
     * An absent fact never fires a rule. A type mismatch between fact and value throws, because
     * a rule that can never evaluate is a configuration error, not a risk signal.
     */
    Decision decide(String transactionId, Map<String, Object> facts, long nowMillis) {
        List<RuleTrace> traces = new ArrayList<>();
        int total = 0;
        String first = null;
        Action firstAction = null;
        for (Rule rule : rules) {
            Object observed = facts.get(rule.field());
            boolean fired = observed != null && rule.operator().apply(observed, rule.value());
            traces.add(new RuleTrace(rule.name(), rule.priority(), rule.field(), observed, rule.operator(), rule.value(),
                    rule.action(), rule.score(), fired, rule.shadow(), rule.explanation()));
            if (fired && !rule.shadow()) {                        // a shadow rule is observed, never obeyed
                total += rule.score();
                if (first == null) { first = rule.name(); firstAction = rule.action(); }
            }
        }
        if (strategy == Strategy.FIRST_MATCH) {
            Action action = first == null ? Action.ALLOW : firstAction;
            return new Decision(transactionId, action, strategy, total, first, traces, nowMillis);
        }
        Action action = total >= declineAt ? Action.DECLINE : total >= reviewAt ? Action.REVIEW : Action.ALLOW;
        return new Decision(transactionId, action, strategy, total, null, traces, nowMillis);   // no single rule decides
    }
}

/** Glue: record velocity, enrich facts, decide. One call per authorization attempt. */
final class Screener {
    private final RulesEngine engine;
    private final VelocityService velocity;
    private final Clock clock;

    Screener(RulesEngine engine, VelocityService velocity, Clock clock) {
        this.engine = Objects.requireNonNull(engine);
        this.velocity = Objects.requireNonNull(velocity);
        this.clock = Objects.requireNonNull(clock);
    }

    Decision screen(Transaction tx) {
        long now = clock.nowMillis();
        return engine.decide(tx.id(), velocity.enrich(tx, now), now);
    }
}

public final class Main {
    static final long SECOND = 1_000, MINUTE = 60 * SECOND, HOUR = 60 * MINUTE, DAY = 24 * HOUR;

    static Money usd(String s) { return Money.parse(s, Currency.USD); }

    static Transaction tx(String id, String account, String card, String bin, String country, long cardAge, String amount) {
        return new Transaction(id, account, card, bin, country, cardAge, usd(amount));
    }

    /** The rule set the demo and the tests share. Rules are data: this could be a JSON file. */
    static List<Rule> cardTestingRules() {
        return List.of(
                new Rule("bin-blocklist", "bin", Operator.IN, Set.of("999999", "888888"), Action.DECLINE, 5, 100,
                        "issuer range is on the internal blocklist"),
                new Rule("card-testing-burst", "attempts_per_card_10m", Operator.GT, 5L, Action.DECLINE, 10, 60,
                        "more than five authorizations on one card in ten minutes is the card-testing signature"),
                new Rule("fresh-card-micro-auth", "card_age_days", Operator.LT, 1L, Action.REVIEW, 20, 25,
                        "card was added less than a day ago"),
                new Rule("high-risk-country", "country", Operator.IN, Set.of("NG", "RU", "VE"), Action.REVIEW, 30, 30,
                        "issuing country is on the elevated-risk list"),
                new Rule("daily-spend", "amount_per_account_1d", Operator.GT, usd("2000.00"), Action.REVIEW, 40, 40,
                        "account spent more than 2000.00 USD in 24 hours"),
                new Rule("large-ticket", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30,
                        "single authorization at or above 5000.00 USD"));
    }

    static VelocityService velocity() {
        return new VelocityService()
                .define("attempts_per_card_10m", VelocityService.Dimension.CARD, VelocityService.Metric.COUNT,
                        new SlidingWindowCounter(10 * MINUTE, 10, Currency.USD))
                .define("amount_per_account_1d", VelocityService.Dimension.ACCOUNT, VelocityService.Metric.SUM,
                        new SlidingWindowCounter(DAY, 24, Currency.USD));
    }

    public static void main(String[] args) {
        ManualClock clock = new ManualClock(1_700_000_040_000L);
        RulesEngine engine = new RulesEngine(cardTestingRules(), Strategy.FIRST_MATCH, 50, 80);
        Screener screener = new Screener(engine, velocity(), clock);

        System.out.println("== Rules in priority order ==");
        for (Rule r : engine.rules()) System.out.println("  " + r.name() + ": " + r.describe());

        System.out.println("\n== A normal purchase ==");
        System.out.print(screener.screen(tx("tx-001", "acct_alice", "tok_alice", "411111", "US", 400, "42.50")).explain());

        System.out.println("\n== Card testing: six $1 attempts on a fresh card in two minutes ==");
        Decision last = null;
        for (int i = 1; i <= 6; i++) {
            clock.advance(20 * SECOND);
            last = screener.screen(tx("tx-10" + i, "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"));
            System.out.println("  attempt " + i + " -> " + last.action() + " fired=" + last.firedRuleNames());
        }
        System.out.print(last.explain());

        System.out.println("\n== Ten minutes later the window has slid past the burst ==");
        clock.advance(10 * MINUTE);
        System.out.print(screener.screen(tx("tx-107", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")).explain());

        System.out.println("\n== Priority: a blocked BIN decides before anything else, but every rule is still traced ==");
        System.out.print(screener.screen(tx("tx-200", "acct_bob", "tok_bob", "999999", "NG", 0, "900.00")).explain());

        System.out.println("\n== Velocity on money, not count: three $700 purchases trip the daily sum ==");
        for (int i = 1; i <= 3; i++) {
            clock.advance(5 * MINUTE);
            last = screener.screen(tx("tx-30" + i, "acct_dave", "tok_dave", "411111", "US", 90, "700.00"));
        }
        System.out.print(last.explain());

        System.out.println("\n== Same rules, SCORE strategy: four REVIEW signals add up to a DECLINE ==");
        RulesEngine scored = new RulesEngine(cardTestingRules(), Strategy.SCORE, 50, 80);
        Screener scoredScreener = new Screener(scored, velocity(), clock);
        Decision d = scoredScreener.screen(tx("tx-400", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00"));
        System.out.print(d.explain());
        System.out.println("FIRST_MATCH would have said: "
                + new Screener(engine, velocity(), clock).screen(tx("tx-401", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00")).action());

        System.out.println("\n== Shadow mode: a candidate rule is traced but does not act ==");
        List<Rule> withCandidate = new ArrayList<>(cardTestingRules());
        withCandidate.add(new Rule("mid-ticket-candidate", "amount", Operator.GTE, usd("500.00"), Action.REVIEW, 45, 20,
                "candidate: single authorization at or above 500.00 USD").asShadow());
        Screener shadowScreener = new Screener(new RulesEngine(withCandidate, Strategy.FIRST_MATCH, 50, 80), velocity(), clock);
        Decision sd = shadowScreener.screen(tx("tx-500", "acct_frank", "tok_frank", "411111", "US", 200, "750.00"));
        System.out.print(sd.explain());
        System.out.println("acted on: " + sd.firedRuleNames() + "  would have fired: "
                + sd.shadowFired().stream().map(RuleTrace::rule).toList());
    }
}
