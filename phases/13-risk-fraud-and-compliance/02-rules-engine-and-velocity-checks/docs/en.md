# A Rules Engine with Velocity Checks

> Declarative rules, sliding-window counters, and explainable decisions.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 13 Lesson 01 (Fraud Patterns), Phase 10 Lesson 01 (Representing Money), Phase 1 Lesson 06 (Hash Maps)
**Time:** ~120 minutes

## Learning Objectives

- Express a fraud control as data (`field`, `operator`, `value`, `action`, `priority`, `score`) and explain why a rule that lives in a config table beats a rule that lives in an `if` statement
- Build a sliding-window velocity counter from a ring of time buckets, state its memory bound, and name the precision it trades away
- Evaluate a rule set over a transaction context with two decision strategies, first-match-by-priority and accumulated score, and say when each is the right one
- Produce a decision record that lists every rule evaluated, every rule that fired, the observed value, and the human-readable reason, so an analyst or a cardholder can be told why
- Test the two boundaries that matter: the velocity threshold trips on exactly the Nth attempt, and the window releases the key at exactly the right time, with the clock injected
- Run a candidate rule in shadow mode, so it is evaluated and traced on live traffic but never decides and never scores, and read its would-have-fired rate before promoting it

## The Problem

At 02:14 on a Saturday a merchant's authorization endpoint starts receiving 1.00 USD purchases. Each one carries a different card number, the same device, the same IP range, and a card that was added to the wallet seconds earlier. By 02:20 there have been 4,200 attempts; 310 were approved. None of this is the merchant's customer. Someone bought a list of stolen card numbers and is using the checkout page as a free card validator: an approval tells them the card is live, and the live cards are resold at ten times the price. This is card testing, Lesson 01's first pattern, and it has three costs. The merchant pays an authorization fee on every attempt, approved or not, so 4,200 attempts at 0.10 USD is 420 USD gone before breakfast. The approved 1.00 USD charges turn into chargebacks at 15.00 USD each. And the issuers whose cards were tested see the merchant's name on a wave of fraud and start declining its legitimate traffic for days afterwards.

The on-call engineer sees the spike, writes `if (amount < 2.00 && cardAgeDays == 0) return DECLINE;`, and deploys at 02:40. The attack stops. On Monday, a legitimate customer who just added a card and bought a 1.99 USD app upgrade is declined with no explanation. Support cannot tell her why, because the decision was an `if` statement with no record of what it compared. The product team asks for the rule to be relaxed to `amount < 1.50`; the change needs a code review, a build, and a deploy, so it lands on Wednesday. On Thursday the attacker returns with 1.75 USD attempts.

Three things went wrong, and they are the three things this lesson fixes. The control was code, so changing it took a deploy cycle while the attacker iterated in minutes. The control looked at one transaction in isolation, when the signal was the *rate*: one 1.00 USD purchase on a fresh card is a customer, six in two minutes on the same card is an attack. And the decision left no evidence, so nobody could explain it to the customer, tune it against data, or defend it to the card network when the chargeback ratio was questioned. A rules engine makes the controls data, a velocity service makes rate a fact a rule can read, and a decision record makes every outcome explainable.

## The Concept

### Rules are data, decisions are evidence

A rule is a predicate over a set of named facts plus a consequence:

```text
IF <field> <operator> <value> THEN <action>   with priority p and score s, and a reason
```

The facts are everything the engine knows about the authorization attempt: the amount, the card's issuer range (BIN), the issuing country, how old the card is in the wallet, and, after enrichment, the velocity counters: how many attempts this card made in the last ten minutes, how much this account has spent today. Every fact has a name, and a rule names the fact it reads. That is the entire coupling between the rule language and the system: a rule set can be stored in a database table, reviewed by the risk team, versioned, and swapped without a deploy, because no rule contains code.

The engine evaluates **every** rule, not just until the first hit, and records a trace per rule: the field, the observed value, the operator, the expected value, and whether it fired. That trace is the decision's evidence. It costs a few dozen comparisons, which is nothing against a network call to the issuer, and it is what lets you answer three questions after the fact: why was this declined (which rule decided), what else was wrong with it (which other rules fired), and what did the engine see (the observed values, including which facts were absent).

Two decision strategies share the same trace:

| Strategy | How the action is chosen | Strength | Weakness |
|----------|--------------------------|----------|----------|
| `FIRST_MATCH` | Rules are sorted by priority; the first rule that fires supplies the action | Predictable; a blocklist at priority 5 always wins; easy to explain with one rule name | Cannot express "three weak signals together are strong"; priority ordering becomes a political artifact |
| `SCORE` | Every fired rule adds its score; the total is compared with `reviewAt` and `declineAt` thresholds | Combines weak signals; thresholds are tunable against a labelled set (Lesson 03) | No single rule "decided", so the explanation is a list; a badly weighted rule shifts every decision |

Production engines usually run both: a short list of hard rules under first-match (sanctions hit, blocklisted BIN, velocity breach) that short-circuit everything, then a scored layer for the grey area. This lesson builds both strategies on the same `RulesEngine` with a flag, so you can see that the difference is twenty lines of decision logic over an identical trace.

### Velocity: the signal is the rate

Velocity is a count or a sum of events per key over a trailing time window: attempts per card in ten minutes, amount per account in a day, distinct cards per device in an hour. The key is the dimension you suspect is shared by the attack: card testing shares a card or a device, account takeover shares an account, a mule network shares a destination. The window is how long the attacker's burst lasts. The threshold is what separates the burst from a customer's normal behaviour, and Lesson 03 is about choosing it.

The exact way to compute a sliding count is to keep every event's timestamp and, at query time, discard the ones older than `now - W`. That is correct and simple, and it has two problems in a system that screens tens of thousands of authorizations per second: memory grows with traffic (a card under attack accumulates thousands of timestamps), and every query walks and prunes a list. The standard engineering answer is to approximate the window with a **ring of time buckets**.

Split the window `W` into `n` buckets of width `b = W / n`. For each key, keep an array of `n` slots, each holding the bucket's start time, a count, and a sum. An event at time `t` belongs to bucket number `floor(t / b)`, and the slot it lands in is that number modulo `n`. Because the ring has exactly `n` slots and the window is exactly `n` buckets wide, the slot that bucket `k` uses was last used by bucket `k - n`, which started exactly `W` ago and is therefore outside any window ending at or after `t`. So when an event arrives and finds its slot holding an older bucket, it simply resets the slot. A query at time `now` sums every slot whose bucket start is strictly newer than `now - W`.

```mermaid
flowchart LR
  E["event at t=T0+59s<br/>amount 1.00"] --> B["bucket floor(t/b)<br/>starts at T0, slot 0"]
  B --> R["ring of n=10 slots<br/>each: start, count, sum"]
  Q["query at now"] --> F["sum slots with<br/>start > now - W"]
  R --> F
  F --> C["count=6, sum=6.00 USD"]
  C --> X["fact: attempts_per_card_10m = 6"]
  X --> RL["rule: attempts_per_card_10m GT 5 -> DECLINE"]
```

The costs and the invariant:

- **Memory per key is O(n)**, fixed, regardless of how many events arrive. A card under a 10,000-attempt attack costs the same 10 slots as a card with one purchase.
- **Record and query are O(n)** in the number of buckets, which is a constant you chose (10 or 24 or 60), not the number of events.
- **The window is approximate at the trailing edge.** An event is counted until its *bucket* leaves the window, not until the event itself is `W` old. An event recorded at the last millisecond of a bucket is dropped as soon as `W - b` has passed; an event at the first millisecond lasts the full `W`. So the effective window is somewhere between `W - b` and `W`. Ten buckets of one minute for a ten-minute window means the count may undercount by up to one minute of history.
- **The leading edge is exact.** An event recorded now is counted now. The attempt that trips the threshold is always counted, and the threshold trips on exactly the Nth attempt, which is the boundary the tests check.

The bucket count is the knob. More buckets means a tighter approximation and more memory; `n = W` in milliseconds would be exact and absurd. Ten to sixty is typical. Production systems that need exact windows for a few high-value keys keep per-event logs for those keys and buckets for everything else.

### Worked example by hand

Window `W = 10 min`, `n = 10`, so `b = 1 min`. `T0 = 1,700,000,040,000 ms`, which is `28,333,334 * 60,000` exactly, so `T0` is on a minute boundary and bucket edges fall where you expect them. Six 1.00 USD attempts on card `tok_mallory`, twenty seconds apart, starting at `T0 + 20 s`:

```text
attempt  time        bucket = floor(t / 60000)   slot = bucket mod 10   slot state after
   1     T0 + 20s    28,333,334                   4                      start=T0,     count=1, sum=100
   2     T0 + 40s    28,333,334                   4                      count=2, sum=200
   3     T0 + 60s    28,333,335                   5                      start=T0+60s, count=1, sum=100
   4     T0 + 80s    28,333,335                   5                      count=2, sum=200
   5     T0 + 100s   28,333,335                   5                      count=3, sum=300
   6     T0 + 120s   28,333,336                   6                      start=T0+120s, count=1, sum=100
```

Query at `T0 + 120 s`, right after the sixth attempt: `now - W = T0 - 480 s`. Slots 4, 5, and 6 have starts `T0`, `T0 + 60 s`, `T0 + 120 s`, all newer than the cutoff, so `count = 2 + 3 + 1 = 6`. The fact `attempts_per_card_10m = 6`, the rule `attempts_per_card_10m GT 5` fires, and the sixth attempt is declined. The fifth attempt, at `T0 + 100 s`, saw `count = 2 + 3 = 5`, and `5 GT 5` is false. The threshold trips on exactly the sixth.

Now advance to `T0 + 10 min + 20 s`, a seventh attempt: `now - W = T0 + 20 s`. Slot 4's start is `T0`, which is not newer than `T0 + 20 s`, so slot 4 is out; the two events in it (at `T0 + 20 s` and `T0 + 40 s`) are gone even though the second of them is only 9 min 40 s old. Slots 5 and 6 are still in: `count = 3 + 1 = 4`, plus the seventh attempt itself, which lands in bucket `28,333,344`, slot 4, resets it, and makes the count 5. Still not `GT 5`. The window has slid; the card is released from the velocity rule but still trips `card_age_days LT 1`, so the decision is `REVIEW`, not `ALLOW`. That is the correct answer: the burst is over, the card is still brand new.

### Priority, short-circuit, and what "fired" means

Rules are sorted by priority once, at engine construction, with a stable sort so two rules at the same priority keep their declaration order. Under `FIRST_MATCH` the action comes from the first fired rule in that order; the engine does not stop evaluating, because the trace of the later rules is the evidence. "Short-circuit" in this design means the *decision* is settled early, not that evaluation stops. If evaluation were expensive (a rule that called an external service), you would add a second flag that skips evaluation after the decision is made; the trace would then record those rules as `skipped`, which is a different thing from `pass`, and the explanation would say so.

### Shadow mode: measure before you act

Every rule carries a `shadow` flag. A shadow rule is evaluated like any other and its trace line is written with the observed value, the expected value, and whether the condition held, but it is excluded from the decision in both strategies: it can never be the deciding rule under `FIRST_MATCH`, and its score is never added under `SCORE`. The decision record reports it separately: `fired()` is the list of live rules that acted, `shadowFired()` is the list of candidates that would have. In the explanation the marker is `[shadow]` instead of `[fire]`.

This is how a rule gets promoted safely. A new rule is deployed as a shadow for a week; its would-have-fired count divided by the traffic it saw is its fire rate, and joining its would-have-fired transactions with the chargeback feed that arrives weeks later gives its precision (Lesson 03). A rule that would have declined 4% of good customers to catch 0.1% more fraud is visible in the numbers before it costs anything. The flag belongs on the rule, not on the engine, because a rule set in production is always a mixture: dozens of live rules and two or three candidates, and a second "shadow engine" running a whole copy of the rule set doubles the velocity writes and still cannot tell you which rule made the diff.

A rule reads one fact. If the fact is absent from the context, the rule does not fire and the trace shows `<absent>`. This is a deliberate choice: a missing velocity counter is a system problem (the velocity service is down, or the rule names a fact nobody produces), and silently treating it as zero would make a counter outage look like clean traffic. Absence is visible in the trace so a monitor can alarm on it. The opposite error, a rule whose `value` has a different type from the fact (`country GT 5`), is a configuration bug and fails loudly at evaluation. A rule that can never evaluate is not a risk signal; it is a broken rule set, and the right response is to refuse to decide rather than decide wrongly.

### What this engine does not do

- It does not learn. The thresholds and scores are set by a person. Lesson 03 is about choosing them against labelled data; Lesson 07 is about the device and behavioural signals that become facts here.
- It does not persist. The counters live in memory, in one process. A production velocity service shards keys across a cluster, replicates the ring state, and accepts that a failover loses a bucket. Phase 7's replication lessons cover why that is a reasonable trade for a fraud counter and an unacceptable one for a balance.
- It does not compose predicates. A rule is one field, one operator, one value. `amount GT 100 AND country IN {...}` is two rules under `SCORE`, or one derived fact computed by the enrichment step. Keeping rules atomic keeps them explainable; the moment a rule has an `AND`, the explanation has to say which half held.

## Build It

The primary walkthrough is in Java. The Python and Go files implement the same design with the same names; the subsections at the end show only what differs. `Money` and `Currency` are copied from Phase 10, Lesson 01, because the daily-spend velocity is a sum of amounts and amounts are never floats.

### Step 1: Inject the clock

```java
interface Clock {
    long nowMillis();
}

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
```

Nothing in the lesson calls `System.currentTimeMillis()`. The demo and the tests move the clock by hand, so "ten minutes later" is `clock.advance(10 * MINUTE)` and the window-expiry test runs in microseconds. A velocity counter that reads the wall clock cannot be tested at its boundary, and the boundary is where it fails.

**Predict:** if `ManualClock` allowed negative advances, which test in `MainTest` would become impossible to write correctly, and why?

### Step 2: The transaction and its facts

```java
record Transaction(String id, String accountId, String cardToken, String bin, String country,
                   long cardAgeDays, Money amount) {

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
```

The card is identified by a token, never by a PAN. PCI DSS Requirement 3 (Lesson 08) is the reason, and it costs nothing here: the velocity key is `"card:" + cardToken`, and a token is as good a key as a PAN. `facts()` is the bridge from the typed record to the untyped context the rules read. The map is insertion-ordered so explanations print the same way every run. Values keep their types (`Money`, `Long`, `String`), because the operators compare typed values; a `Money` fact is compared with a `Money` rule value and currency mismatch is an error, exactly as in Lesson 01.

**Predict:** `cardAgeDays` is put into the map as a `Long`. If the demo rule were written `new Rule("fresh-card", "card_age_days", LT, 1, ...)` with an `int` literal instead of `1L`, what happens at evaluation time, and is that the behaviour you want?

### Step 3: The ring of buckets

```java
final class SlidingWindowCounter {
    private final long windowMillis;
    private final long bucketMillis;
    private final int buckets;
    private final Currency currency;
    private final Map<String, Ring> rings = new HashMap<>();

    private static final class Ring {
        final long[] starts;   // Long.MIN_VALUE marks a never-used slot
        final long[] counts;
        final long[] sums;
        Ring(int n) { starts = new long[n]; counts = new long[n]; sums = new long[n]; Arrays.fill(starts, Long.MIN_VALUE); }
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
}
```

One counter is one window, one bucket count, and one currency. The window must divide evenly into buckets so that `n * b == W` exactly; otherwise the "slot reuse means the old bucket is outside the window" argument breaks. A counter that sums amounts in mixed currencies would produce a number with no meaning, so the currency is fixed at construction and `record` rejects anything else.

```java
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
```

The two lines that matter are `bucketStart` and `idx`. `floorDiv` rather than `/` so that the arithmetic is right for any timestamp, including the negative ones a test might use. The reset-on-lap check compares the slot's recorded start with the start the current event wants; if they differ, the slot belongs to an older lap and is wiped. `addExact` on the sum because a counter that wraps at `Long.MAX_VALUE` would turn a flood of large amounts into a small negative number and silently stop firing the rule it feeds.

```java
long count(String key, long nowMillis) {
    Ring ring = rings.get(key);
    if (ring == null) return 0;
    long n = 0;
    for (int i = 0; i < buckets; i++) {
        if (ring.starts[i] > nowMillis - windowMillis) n += ring.counts[i];
    }
    return n;
}
```

The query is a linear scan over `n` slots with the single predicate `start > now - W`. Strictly greater: a bucket that started exactly `W` ago is out, which is what makes the window `[now - W, now)` half-open and the expiry test deterministic. `sum` is the same loop over `sums` and returns a `Money`.

**Predict:** with `W = 10 min`, `n = 10`, an event at `T0 + 59 s`, and a query at `T0 + 10 min`: is the event counted? What is the event's actual age at that moment?

### Step 4: Velocity as facts

```java
final class VelocityService {
    enum Dimension { CARD, ACCOUNT;
        String keyFor(Transaction tx) {
            return switch (this) {
                case CARD -> "card:" + tx.cardToken();
                case ACCOUNT -> "account:" + tx.accountId();
            };
        }
    }
    enum Metric { COUNT, SUM }
    record Definition(String factName, Dimension dimension, Metric metric, SlidingWindowCounter counter) {}

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
```

`enrich` records first, then reads, so the current attempt counts itself: the sixth attempt sees `attempts_per_card_10m = 6`. This is the convention that makes the threshold `GT 5` mean "the sixth and later". The alternative, reading before recording, means `GT 5` fires on the seventh, and a reviewer reading the rule will get it wrong by one. Pick one convention, write it in the rule's explanation text, and test the exact boundary.

Declined attempts are recorded too. That is not an accident: a card tester's declines *are* the signal, and a counter that only counted approvals would reset the attacker's budget every time a rule fired.

**Predict:** the same card is used at two different merchants that share this velocity service. Does the `CARD` dimension count both? What would the key have to include for the count to be per merchant, and which of the two is the better card-testing signal?

### Step 5: Operators that fail loudly

```java
enum Operator {
    EQ, NE, GT, GTE, LT, LTE, IN, NOT_IN;

    boolean apply(Object observed, Object expected) {
        switch (this) {
            case EQ: return Objects.equals(observed, expected);
            case NE: return !Objects.equals(observed, expected);
            case IN: return asSet(expected).contains(observed);
            case NOT_IN: return !asSet(expected).contains(observed);
            default: {
                int cmp = compare(observed, expected);
                return switch (this) {
                    case GT -> cmp > 0; case GTE -> cmp >= 0; case LT -> cmp < 0; case LTE -> cmp <= 0;
                    default -> throw new IllegalStateException();
                };
            }
        }
    }

    private static int compare(Object a, Object b) {
        if (a instanceof Long x && b instanceof Long y) return Long.compare(x, y);
        if (a instanceof Money x && b instanceof Money y) return x.compareTo(y);   // throws on currency mismatch
        throw new IllegalArgumentException("cannot order " + typeName(a) + " against " + typeName(b));
    }
}
```

Eight operators cover every rule in this lesson and most rules in production. Ordering is defined for exactly two types, `Long` and `Money`; anything else is a configuration error and throws. There is no coercion: `"5"` is not `5L`, and `5L` is not `5.00 USD`. The reason is the same as in Lesson 01: an engine that coerces will one day compare `amount GT 5000` where `5000` was meant as dollars and is read as cents, and approve a 49,999.99 USD purchase without a trace of why.

### Step 6: The rule and its trace

```java
record Rule(String name, String field, Operator operator, Object value, Action action, int priority, int score, String explanation,
            boolean shadow) {
    Rule(String name, String field, Operator operator, Object value, Action action, int priority, int score, String explanation) {
        this(name, field, operator, value, action, priority, score, explanation, false);
    }

    Rule {
        if (name == null || name.isBlank()) throw new IllegalArgumentException("rule needs a name");
        if (explanation == null || explanation.isBlank()) throw new IllegalArgumentException("rule needs an explanation");
        if (score < 0) throw new IllegalArgumentException("score must be non-negative");
        if (value instanceof Set<?> s) value = Collections.unmodifiableSet(new TreeSet<>(s));   // sorted: deterministic text
    }

    Rule asShadow() { return new Rule(name, field, operator, value, action, priority, score, explanation, true); }
}

record RuleTrace(String rule, int priority, String field, Object observed, Operator operator, Object expected,
                 Action action, int score, boolean fired, boolean shadow, String explanation) {}
```

The eight-argument constructor builds a live rule; `asShadow()` returns a copy with the flag set, so a candidate is declared next to the live rules with one extra call and nothing else about it changes. A rule without an explanation is rejected at construction. The explanation is what the analyst reads in the case queue and what the customer-facing team paraphrases; a rule set where half the rules say `"rule 17"` is a rule set nobody can tune. Sets are copied into a sorted, unmodifiable `TreeSet` so that `IN [888888, 999999]` prints identically every time; the trace is going to be hashed into an audit log in Lesson 11, and a `HashSet`'s iteration order would make two identical decisions hash differently.

### Step 7: The engine

```java
Decision decide(String transactionId, Map<String, Object> facts, long nowMillis) {
    List<RuleTrace> traces = new ArrayList<>();
    int total = 0;
    String first = null;
    Action firstAction = null;
    for (Rule rule : rules) {                                   // already sorted by priority
        Object observed = facts.get(rule.field());
        boolean fired = observed != null && rule.operator().apply(observed, rule.value());
        traces.add(new RuleTrace(rule.name(), rule.priority(), rule.field(), observed, rule.operator(), rule.value(),
                rule.action(), rule.score(), fired, rule.explanation()));
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
    return new Decision(transactionId, action, strategy, total, null, traces, nowMillis);
}
```

One loop, every rule, one trace per rule. `observed != null &&` is the absent-fact rule: a missing fact never fires. `&& !rule.shadow()` is the entire implementation of shadow mode: the trace already recorded `fired`, so the only thing left to do is skip the two lines that act on it. The score is accumulated under both strategies so that a `FIRST_MATCH` decision still reports what the score *would* have been, which is the data you need when deciding whether to move a rule set to `SCORE`. The default when nothing fires is `ALLOW`; a rules engine that defaults to `DECLINE` on an empty rule set is a rules engine that takes the business down when someone deploys an empty config.

**Predict:** under `SCORE` with `reviewAt = 50` and `declineAt = 80`, three rules each scoring 30 fire. What is the action? Under `FIRST_MATCH` with the same rules, all with action `REVIEW`?

### Step 8: The explanation

```java
String explain() {
    StringBuilder sb = new StringBuilder();
    sb.append("decision for ").append(transactionId).append(": ").append(action)
      .append(" [").append(strategy).append(", score=").append(score).append("]");
    if (strategy == Strategy.SCORE) sb.append(" by accumulated score");
    else if (decidingRule == null) sb.append(" default: no rule matched");
    else sb.append(" by \"").append(decidingRule).append("\"");
    sb.append(" at t=").append(decidedAtMillis).append('\n');
    for (RuleTrace t : traces) {
        sb.append(t.fired() ? "  [fire] " : "  [pass] ")
          .append("p").append(t.priority()).append(' ').append(t.rule()).append(": ")
          .append(t.field()).append('=').append(t.observed() == null ? "<absent>" : t.observed())
          .append(' ').append(t.operator()).append(' ').append(t.expected());
        if (t.fired()) sb.append(" -> ").append(t.action()).append(" (+").append(t.score()).append("): ").append(t.explanation());
        sb.append('\n');
    }
    return sb.toString();
}
```

The explanation is derived from the trace, never stored separately, so it cannot drift from the decision. Every line shows the observed value next to the expected one: `attempts_per_card_10m=6 GT 5` is the sentence an analyst needs. `<absent>` is printed for a missing fact so that a velocity outage is visible in every decision made during it, and `[shadow]` marks a candidate that held but did not act, so the same log that explains live decisions is also the measurement of every candidate.

**Predict:** a shadow rule at priority 10 with action `DECLINE` and a live rule at priority 20 with action `REVIEW` both hold. What does the header line say, what does `firedRuleNames()` return, and what does `shadowFired()` return?

### Step 9: Run it

```bash
cd phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Card testing: six $1 attempts on a fresh card in two minutes ==
  attempt 1 -> REVIEW fired=[fresh-card-micro-auth]
  ...
  attempt 5 -> REVIEW fired=[fresh-card-micro-auth]
  attempt 6 -> DECLINE fired=[card-testing-burst, fresh-card-micro-auth]
decision for tx-106: DECLINE [FIRST_MATCH, score=85] by "card-testing-burst" at t=1700000160000
  [pass] p5 bin-blocklist: bin=411111 IN [888888, 999999]
  [fire] p10 card-testing-burst: attempts_per_card_10m=6 GT 5 -> DECLINE (+60): more than five authorizations ...
  [fire] p20 fresh-card-micro-auth: card_age_days=0 LT 1 -> REVIEW (+25): card was added less than a day ago
  [pass] p30 high-risk-country: country=US IN [NG, RU, VE]
  [pass] p40 daily-spend: amount_per_account_1d=6.00 USD GT 2000.00 USD
  [pass] p50 large-ticket: amount=1.00 USD GTE 5000.00 USD

== Ten minutes later the window has slid past the burst ==
decision for tx-107: REVIEW [FIRST_MATCH, score=25] by "fresh-card-micro-auth" at t=1700000760000
  [pass] p10 card-testing-burst: attempts_per_card_10m=5 GT 5
  ...
== Same rules, SCORE strategy: four REVIEW signals add up to a DECLINE ==
decision for tx-400: DECLINE [SCORE, score=125] by accumulated score at t=1700001660000
FIRST_MATCH would have said: REVIEW

== Shadow mode: a candidate rule is traced but does not act ==
decision for tx-500: ALLOW [FIRST_MATCH, score=0] default: no rule matched at t=1700001660000
  ...
  [shadow] p45 mid-ticket-candidate: amount=750.00 USD GTE 500.00 USD -> REVIEW (+20): candidate: ...
  [pass] p50 large-ticket: amount=750.00 USD GTE 5000.00 USD
acted on: []  would have fired: [mid-ticket-candidate]
```

The `SCORE` block is the argument for a scored layer: four independent `REVIEW` signals on one 5,500 USD purchase from a day-old card in a high-risk country is not a review case, and only a strategy that adds them up can say so.

### In Python

```python
class _Ring:
    __slots__ = ("starts", "counts", "sums")

    def __init__(self, n: int) -> None:
        self.starts = [None] * n  # None marks a never-used slot
        self.counts = [0] * n
        self.sums = [0] * n
```

Differences, all in `code/python/main.py`:

- **`//` is already floor division**, so `now_millis // bucket_millis` replaces `Math.floorDiv`. The comment in `record` says timestamps are non-negative here; if you ever feed negative ones, `//` still floors correctly, which is what the ring needs.
- **No overflow**, so there is no `addExact`; the test suite does not assert an overflow error. `_compare` excludes `bool` explicitly because `True` is an `int` in Python and `Operator.GT.apply(True, 0)` would otherwise be a legal comparison.
- **`shadow` is a dataclass field with a default** (`shadow: bool = False`), so the eight-argument call site stays the same and `as_shadow()` builds the copy explicitly rather than through `dataclasses.replace`, to keep the same name as the Java and Go methods.
- **Sets become `frozenset`** in `Rule.__post_init__` via `object.__setattr__` (the dataclass is frozen), and `_fmt` sorts them for printing. Python's `set` iteration order depends on hash seeds, so without the sort two runs of the same demo would print different explanations.

### In Go

```go
func (o Operator) Apply(observed, expected any) (bool, error) {
    switch o {
    case EQ:
        return observed == expected, nil
    case IN, NotIn:
        set, ok := expected.(StringSet)
        if !ok {
            return false, fmt.Errorf("%w: IN / NOT_IN need a set, got %s", ErrConfiguration, typeName(expected))
        }
        // ...
    }
    cmp, err := compareFacts(observed, expected)
    // ...
}
```

Differences, all in `code/go/main.go`:

- **Errors are values.** `Apply`, `Record`, `Enrich`, `Decide`, and `Screen` return `error`; `ErrConfiguration`, `ErrCurrencyMismatch`, and `ErrOverflow` are sentinels for `errors.Is`. A type mismatch in a rule surfaces as `rule bad: configuration error: cannot order string against int64`, wrapped so the rule name is in the message.
- **Facts are a small ordered map type**, because Go's `map` iterates in random order and the explanation must print deterministically. `Facts.Put` records insertion order; `Facts.Get` returns `(value, present)` so absence is distinguishable from a nil value.
- **Integer facts are `int64`**, and rule values must be `int64` too: `GT, 5` (an untyped `int`) will not compare against an `int64` fact and fails loudly, which is the Go version of the "no coercion" rule. The demo writes `int64(5)`.
- **Set printing** is `StringSet.String()`, which sorts keys, the same reason as the Java `TreeSet`.
- **`AsShadow` is a value-receiver method** that sets the field on its copy and returns it; because `Rule` is a struct passed by value, the original in the slice is untouched, which is the same immutability the Java record gives you for free.

## Use It

### Production rules engines

Stripe Radar, Adyen RevenueProtect, and Sift all expose the same shape you built: rules written in a small expression language over named attributes, with velocity attributes precomputed and available as fields (Radar's `total_charges_per_card_number_hourly`, `blocked_charges_per_email_daily`, and so on), a per-rule action (block, review, allow, 3DS challenge), and a per-decision list of which rules matched. What they do that the scratch version made explicit: they maintain the velocity counters as a globally replicated service across every merchant, so a card tested at merchant A is already hot at merchant B; they compute hundreds of derived facts (distance between billing and IP geolocation, email-domain age) before the rules see anything; and they version rule sets with a test-mode that evaluates new rules without acting on them, producing the trace so you can measure what a rule *would* have done.

### Redis and the velocity counter

Most in-house velocity services are Redis. The idiom is either a key per `(dimension, bucket)` with `INCR` and `EXPIRE` (which is exactly a ring of buckets with Redis doing the slot reuse via expiry), or a sorted set per key with timestamps as scores and `ZREMRANGEBYSCORE` to prune (the exact per-event log, with its unbounded memory). Redis's `INCRBY` on a 64-bit integer wraps silently, so sums of minor units need the same `addExact` discipline on the client. The trap: `EXPIRE` is wall-clock on the Redis server, so a clock skew between the application and Redis makes the window wider or narrower than configured, and there is no injected clock to test it with. The ring-in-process design here can be tested at the millisecond boundary; a Redis design has to be tested with a fake Redis or trusted.

### Drools, Easy Rules, and when not to use them

Java has general-purpose rules engines (Drools implements the Rete algorithm for forward-chaining over many facts and many rules). They are designed for rule sets of thousands of interdependent rules where one rule's conclusion is another's premise. A fraud rule set is flat: every rule reads facts, none produces them, and a linear scan of fifty rules takes microseconds. Rete's indexing buys nothing and its chaining makes decisions harder to explain. If a colleague proposes Drools for authorization screening, ask them to write the explanation line for a declined transaction first.

### What this engine still lacks for production

Hot reload of the rule set without a restart (a `volatile` reference to an immutable `RulesEngine`, swapped atomically), persistence of the shadow traces into a table keyed by rule name so the would-have-fired rate is a query rather than a log grep, per-rule metrics (fire rate, decline rate, chargeback rate on the rules that allowed), and a case-management hook for `REVIEW`. Each is a straightforward extension once the trace exists; none is possible without it.

## Ship It

`outputs/checklist-fraud-rule-review.md` is a review checklist for any new or changed fraud rule: the fact it reads and who produces it, the boundary convention (does the Nth attempt count itself), the explanation text, the shadow-mode numbers, and the roll-back plan. Attach it to the rule-change template in your risk team's tracker; the questions are the ones a card network's dispute analyst will eventually ask.

## Exercises

1. **Reinforce.** Add a `DISTINCT` metric to `SlidingWindowCounter` that counts distinct values of a secondary key per bucket (distinct cards per device in an hour, the classic card-testing signature when the attacker rotates cards). Each bucket needs a set instead of a count; state the new memory bound and write the test that proves two attempts with the same card in one bucket count once.
2. **Apply.** Implement rule-set versioning and a shadow report: give `RulesEngine` a `version` string, include it in every `Decision` and in `explain()`, and write a `ShadowReport` that consumes a stream of decisions and prints, per shadow rule, the number of transactions seen, the would-have-fired count, and how often the shadow action was stricter than the live action. Run the demo's traffic through it with `mid-ticket-candidate` in shadow and decide, from the numbers, whether you would promote it.
3. **Extend.** Combine with Phase 11, Lesson 11 (Auditability, hash-chained logs): append every `Decision` to a hash-chained log where each entry's hash covers the previous hash and the full `explain()` text. Then prove that the `TreeSet` sorting of `IN` values and the `LinkedHashMap` ordering of facts are load-bearing: replace one with an unordered collection and show that two identical decisions produce different chain hashes.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Velocity | "how fast the card is being used" | A count or sum of events per key over a trailing window; the key, window, and metric are three independent choices |
| Sliding window | "the last ten minutes" | A window `[now - W, now)` that moves with every query; distinct from a fixed window that resets on the clock and lets a burst straddle the reset |
| Bucketed ring | "approximate sliding window" | `n` fixed slots of width `W / n`; O(n) memory per key, exact at the leading edge, up to one bucket of undercount at the trailing edge |
| First match | "the first rule wins" | Rules sorted by priority; the earliest fired rule supplies the action; every rule is still evaluated for the trace |
| Scored decision | "risk score" | Fired rules add their weights; the total is compared with ordered thresholds; combines weak signals, has no single deciding rule |
| Absent fact | "the counter was null" | A fact the context does not contain; never fires a rule and is printed as `<absent>` so an enrichment outage is visible in every decision |
| Shadow mode | "dry run", "monitor only" | A per-rule flag: the rule is evaluated and traced on live traffic, its trace line says `[shadow]`, and it is excluded from both the first-match decision and the accumulated score |
| Card testing | "carding", "BIN attack" | Using a merchant's authorization endpoint to validate stolen card numbers with small purchases; the signal is attempts per card or per device, not any one transaction |

## Further Reading

- [Stripe Radar rules reference](https://docs.stripe.com/radar/rules/reference) — a production rule language with the same field-operator-value shape and the list of precomputed velocity attributes; compare its `total_charges_per_card_number_hourly` with `attempts_per_card_10m`.
- [Redis `INCR` command documentation, rate-limiter patterns](https://redis.io/docs/latest/commands/incr/) — the official write-up of the fixed-bucket and sliding-window rate limiter idioms, which are the velocity counter in production clothing.
- [PCI DSS v4.0 Requirement 3: Protect stored account data](https://www.pcisecuritystandards.org/document_library/) — why the velocity key is a token and not a PAN, and what a counter keyed by PAN would do to your compliance scope (Lesson 08).
- [Visa Rules: card testing and the Visa Acquirer Monitoring Program](https://usa.visa.com/support/consumer/visa-rules.html) — the network's definition of excessive authorization attempts and the thresholds at which an acquirer is penalised, which is where the ten-minute window comes from.
- [Forgy, "Rete: A Fast Algorithm for the Many Pattern/Many Object Pattern Match Problem" (1982)](https://doi.org/10.1016/0004-3702(82)90020-0) — the algorithm behind general-purpose rules engines, to understand what Drools buys and why a flat fraud rule set does not need it.
- [Federal Reserve, "Synthetic Identity Fraud in the U.S. Payment System" (2019)](https://fedpaymentsimprovement.org/wp-content/uploads/frs-synthetic-identity-payments-fraud-white-paper-july-2019.pdf) — the behavioural patterns (dormant accounts, bust-out) that velocity rules on account dimensions are built to catch.
