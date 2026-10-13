// Lesson: phases/12-payment-systems-design/09-limits-and-velocity-controls/docs/en.md
// Limits, Velocity Controls, and Exposure
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: PCI DSS v4.0 (card testing guidance); Visa Core Rules (authorization velocity and
//          excessive-decline programs); RFC 9110 (429 and Retry-After). Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.atomic.AtomicLong;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency JPY = new Currency("JPY", 0);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

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
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }
    boolean isGreaterThan(Money other) { return compareTo(other) > 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Every window boundary in this lesson is computed from clock.nowMillis().
// ---------------------------------------------------------------------------------------------

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private final AtomicLong now;
    FakeClock(long startMillis) { now = new AtomicLong(startMillis); }
    @Override public long nowMillis() { return now.get(); }
    void advance(long millis) { now.addAndGet(millis); }
}

// ---------------------------------------------------------------------------------------------
// The attempt being judged, and the dimensions a limit can key on.
// ---------------------------------------------------------------------------------------------

/** One authorization attempt as the limit engine sees it. The card is identified by a fingerprint, never a PAN. */
record Attempt(String attemptId, String merchantId, String cardFingerprint, Money amount) {
    Attempt {
        Objects.requireNonNull(attemptId, "attemptId");
        Objects.requireNonNull(merchantId, "merchantId");
        Objects.requireNonNull(cardFingerprint, "cardFingerprint");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
    }
}

/** What a limit counts against. The scope key is the string the window is bucketed by. */
enum LimitScope {
    CARD, MERCHANT, CARD_AT_MERCHANT, GLOBAL;

    String keyFor(Attempt a) {
        return switch (this) {
            case CARD -> "card:" + a.cardFingerprint();
            case MERCHANT -> "merchant:" + a.merchantId();
            case CARD_AT_MERCHANT -> "merchant:" + a.merchantId() + "|card:" + a.cardFingerprint();
            case GLOBAL -> "global";
        };
    }
}

/** What a limit measures over its window. EXPOSURE has no window: it is the sum of currently open authorizations. */
enum LimitKind { COUNT, AMOUNT, EXPOSURE }

/** HARD breaches deny. SOFT breaches allow but flag for review (step-up, manual review, delayed capture). */
enum Severity { HARD, SOFT }

/**
 * A limit: at most {@code threshold} units (attempts, or minor units of money) per scope key within
 * {@code windowMillis}. {@code thresholdMoney} is non-null for AMOUNT and EXPOSURE limits, and the
 * currency it carries is the only currency the limit can judge: a limit in USD does not see EUR attempts.
 */
record Limit(String id, LimitScope scope, LimitKind kind, long windowMillis, long maxCount, Money maxAmount, Severity severity) {
    Limit {
        Objects.requireNonNull(id, "id");
        if (kind == LimitKind.COUNT && maxCount <= 0) throw new IllegalArgumentException("COUNT limit needs maxCount > 0");
        if (kind != LimitKind.COUNT && (maxAmount == null || !maxAmount.isPositive())) throw new IllegalArgumentException(kind + " limit needs a positive maxAmount");
        if (kind != LimitKind.EXPOSURE && windowMillis <= 0) throw new IllegalArgumentException("window must be positive for " + kind);
    }

    static Limit count(String id, LimitScope scope, long windowMillis, long maxCount, Severity severity) {
        return new Limit(id, scope, LimitKind.COUNT, windowMillis, maxCount, null, severity);
    }
    static Limit amount(String id, LimitScope scope, long windowMillis, Money maxAmount, Severity severity) {
        return new Limit(id, scope, LimitKind.AMOUNT, windowMillis, 0, maxAmount, severity);
    }
    static Limit exposure(String id, LimitScope scope, Money maxAmount, Severity severity) {
        return new Limit(id, scope, LimitKind.EXPOSURE, 0, 0, maxAmount, severity);
    }

    /** Does this limit apply to the attempt at all? Money limits only judge their own currency. */
    boolean applies(Attempt a) {
        return kind == LimitKind.COUNT || maxAmount.currency().equals(a.amount().currency());
    }
}

// ---------------------------------------------------------------------------------------------
// The sliding window: a deque of (timestamp, amount) entries, evicted lazily. The window is the
// half-open interval (now - W, now]: an entry exactly W old has left the window.
// ---------------------------------------------------------------------------------------------

final class SlidingWindow {
    record Entry(long atMillis, long amountMinor) {}

    private final long windowMillis;
    private final ArrayDeque<Entry> entries = new ArrayDeque<>();
    private long sumMinor = 0;              // maintained incrementally so sum() is O(evicted), not O(n)

    SlidingWindow(long windowMillis) {
        if (windowMillis <= 0) throw new IllegalArgumentException("window must be positive");
        this.windowMillis = windowMillis;
    }

    /** Drop every entry that has fallen out of (now - W, now]. Amortized O(1) per entry ever added. */
    void evict(long now) {
        long cutoff = now - windowMillis;
        while (!entries.isEmpty() && entries.peekFirst().atMillis() <= cutoff) {
            sumMinor -= entries.pollFirst().amountMinor();
        }
    }

    void add(long now, long amountMinor) {
        if (!entries.isEmpty() && now < entries.peekLast().atMillis()) {
            throw new IllegalArgumentException("entries must be appended in time order; clock went backwards");
        }
        evict(now);
        entries.addLast(new Entry(now, amountMinor));
        sumMinor = Math.addExact(sumMinor, amountMinor);
    }

    int count(long now) { evict(now); return entries.size(); }
    long sumMinor(long now) { evict(now); return sumMinor; }

    /** When the oldest entry leaves the window, i.e. the earliest instant a COUNT breach can clear. Null if empty. */
    Long nextExpiryMillis(long now) {
        evict(now);
        return entries.isEmpty() ? null : entries.peekFirst().atMillis() + windowMillis;
    }
}

// ---------------------------------------------------------------------------------------------
// Exposure: money authorized and not yet captured, voided, or expired. No window: it is a balance.
// ---------------------------------------------------------------------------------------------

final class ExposureTracker {
    private final Map<String, Map<String, Money>> openByScopeKey = new HashMap<>();   // scopeKey -> attemptId -> amount

    Money open(String scopeKey, Currency currency) {
        Money total = Money.zero(currency);
        for (Money m : openByScopeKey.getOrDefault(scopeKey, Map.of()).values()) {
            if (m.currency().equals(currency)) total = total.plus(m);
        }
        return total;
    }

    void add(String scopeKey, String attemptId, Money amount) {
        openByScopeKey.computeIfAbsent(scopeKey, k -> new LinkedHashMap<>()).put(attemptId, amount);
    }

    /** Capture, void, or expiry releases the exposure. Unknown ids are ignored: release is idempotent. */
    void release(String scopeKey, String attemptId) {
        Map<String, Money> open = openByScopeKey.get(scopeKey);
        if (open != null) open.remove(attemptId);
    }
}

// ---------------------------------------------------------------------------------------------
// Decisions carry their reasons. A denial you cannot explain is a support ticket you cannot close.
// ---------------------------------------------------------------------------------------------

enum Outcome { ALLOW, REVIEW, DENY }

/** One limit's verdict on one attempt: what was observed, what the attempt would make it, and the threshold. */
record Reason(String limitId, LimitScope scope, String scopeKey, LimitKind kind, Severity severity,
              String observed, String wouldBe, String threshold, long windowMillis, Long retryAfterMillis) {
    boolean breached() { return severity != null; }

    String explain() {
        String window = kind == LimitKind.EXPOSURE ? "open" : "in " + (windowMillis / 1000) + "s";
        String retry = retryAfterMillis == null ? "" : ", clears in " + (retryAfterMillis / 1000) + "s";
        return limitId + " [" + severity + "] " + scopeKey + ": " + observed + " -> " + wouldBe + " exceeds " + threshold + " " + window + retry;
    }
}

record Decision(Outcome outcome, List<Reason> reasons) {
    boolean allowed() { return outcome != Outcome.DENY; }

    /** The explanation a support engineer, a merchant dashboard, or an audit log needs. One line per breached limit. */
    List<String> explanations() {
        List<String> out = new ArrayList<>();
        for (Reason r : reasons) out.add(r.explain());
        return out;
    }

    /** Seconds until the earliest HARD breach clears, for a Retry-After header. 0 if nothing is time-bound. */
    long retryAfterSeconds() {
        long best = Long.MAX_VALUE;
        for (Reason r : reasons) {
            if (r.severity() == Severity.HARD && r.retryAfterMillis() != null) best = Math.min(best, r.retryAfterMillis());
        }
        return best == Long.MAX_VALUE ? 0 : (best + 999) / 1000;
    }
}

// ---------------------------------------------------------------------------------------------
// The limit hierarchy: platform defaults, overridden per merchant by limit id.
// ---------------------------------------------------------------------------------------------

final class LimitPolicy {
    private final Map<String, Limit> defaults = new LinkedHashMap<>();
    private final Map<String, Map<String, Limit>> merchantOverrides = new HashMap<>();

    LimitPolicy addDefault(Limit limit) {
        if (defaults.putIfAbsent(limit.id(), limit) != null) throw new IllegalArgumentException("duplicate limit id " + limit.id());
        return this;
    }

    /** A merchant override replaces the default with the same id; it never relaxes a limit the default does not have. */
    LimitPolicy override(String merchantId, Limit limit) {
        if (!defaults.containsKey(limit.id())) throw new IllegalArgumentException("no default limit to override: " + limit.id());
        merchantOverrides.computeIfAbsent(merchantId, k -> new HashMap<>()).put(limit.id(), limit);
        return this;
    }

    /** The effective limits for a merchant, in the order the defaults were declared. */
    List<Limit> limitsFor(String merchantId) {
        Map<String, Limit> overrides = merchantOverrides.getOrDefault(merchantId, Map.of());
        List<Limit> effective = new ArrayList<>();
        for (Limit d : defaults.values()) effective.add(overrides.getOrDefault(d.id(), d));
        return Collections.unmodifiableList(effective);
    }
}

// ---------------------------------------------------------------------------------------------
// The engine: evaluate every applicable limit, then record the attempt, under one lock.
// ---------------------------------------------------------------------------------------------

final class LimitEngine {
    private final LimitPolicy policy;
    private final Clock clock;
    private final Map<String, SlidingWindow> windows = new HashMap<>();   // limitId + "@" + scopeKey -> window
    private final ExposureTracker exposure = new ExposureTracker();
    private final Map<String, Attempt> openAttempts = new HashMap<>();   // attemptId -> attempt, for release()

    LimitEngine(LimitPolicy policy, Clock clock) {
        this.policy = policy;
        this.clock = clock;
    }

    private SlidingWindow windowFor(Limit limit, String scopeKey) {
        return windows.computeIfAbsent(limit.id() + "@" + scopeKey, k -> new SlidingWindow(limit.windowMillis()));
    }

    /**
     * Judge one attempt against every effective limit and, if not denied, record it in every window
     * and in exposure. Check-and-record is one critical section so two concurrent attempts cannot
     * both see "4 of 5" and both pass. All breaches are collected, not just the first: a denial
     * must name every limit it tripped so the explanation is complete.
     */
    synchronized Decision check(Attempt attempt) {
        long now = clock.nowMillis();
        List<Reason> reasons = new ArrayList<>();
        Outcome outcome = Outcome.ALLOW;
        for (Limit limit : policy.limitsFor(attempt.merchantId())) {
            if (!limit.applies(attempt)) continue;
            Reason r = evaluate(limit, attempt, now);
            if (r == null) continue;
            reasons.add(r);
            if (limit.severity() == Severity.HARD) outcome = Outcome.DENY;
            else if (outcome == Outcome.ALLOW) outcome = Outcome.REVIEW;
        }
        if (outcome != Outcome.DENY) record(attempt, now);
        return new Decision(outcome, List.copyOf(reasons));
    }

    private Reason evaluate(Limit limit, Attempt attempt, long now) {
        String scopeKey = limit.scope().keyFor(attempt);
        switch (limit.kind()) {
            case COUNT -> {
                SlidingWindow w = windowFor(limit, scopeKey);
                int observed = w.count(now);
                if (observed + 1 <= limit.maxCount()) return null;
                Long expiry = w.nextExpiryMillis(now);
                return new Reason(limit.id(), limit.scope(), scopeKey, limit.kind(), limit.severity(),
                        observed + " attempts", (observed + 1) + " attempts", limit.maxCount() + " attempts",
                        limit.windowMillis(), expiry == null ? null : expiry - now);
            }
            case AMOUNT -> {
                SlidingWindow w = windowFor(limit, scopeKey);
                Money observed = Money.ofMinor(w.sumMinor(now), attempt.amount().currency());
                Money wouldBe = observed.plus(attempt.amount());
                if (!wouldBe.isGreaterThan(limit.maxAmount())) return null;
                Long expiry = w.nextExpiryMillis(now);
                return new Reason(limit.id(), limit.scope(), scopeKey, limit.kind(), limit.severity(),
                        observed.toString(), wouldBe.toString(), limit.maxAmount().toString(),
                        limit.windowMillis(), expiry == null ? null : expiry - now);
            }
            case EXPOSURE -> {
                Money observed = exposure.open(scopeKey, attempt.amount().currency());
                Money wouldBe = observed.plus(attempt.amount());
                if (!wouldBe.isGreaterThan(limit.maxAmount())) return null;
                return new Reason(limit.id(), limit.scope(), scopeKey, limit.kind(), limit.severity(),
                        observed.toString(), wouldBe.toString(), limit.maxAmount().toString(), 0, null);
            }
            default -> throw new IllegalStateException("unknown kind " + limit.kind());
        }
    }

    private void record(Attempt attempt, long now) {
        for (Limit limit : policy.limitsFor(attempt.merchantId())) {
            if (!limit.applies(attempt)) continue;
            String scopeKey = limit.scope().keyFor(attempt);
            switch (limit.kind()) {
                case COUNT, AMOUNT -> windowFor(limit, scopeKey).add(now, attempt.amount().minorUnits());
                case EXPOSURE -> exposure.add(scopeKey, attempt.attemptId(), attempt.amount());
            }
        }
        openAttempts.put(attempt.attemptId(), attempt);
    }

    /** Capture, void, or authorization expiry: the attempt no longer counts toward exposure. Velocity history is untouched. */
    synchronized void release(String attemptId) {
        Attempt attempt = openAttempts.remove(attemptId);
        if (attempt == null) return;
        for (Limit limit : policy.limitsFor(attempt.merchantId())) {
            if (limit.kind() == LimitKind.EXPOSURE) exposure.release(limit.scope().keyFor(attempt), attemptId);
        }
    }

    synchronized Money openExposure(Limit limit, Attempt sample) {
        return exposure.open(limit.scope().keyFor(sample), sample.amount().currency());
    }

    synchronized int windowCount(Limit limit, Attempt sample) {
        return windowFor(limit, limit.scope().keyFor(sample)).count(clock.nowMillis());
    }
}

public final class Main {
    static final long SECOND = 1000L, MINUTE = 60 * SECOND, HOUR = 60 * MINUTE, DAY = 24 * HOUR;

    static LimitPolicy standardPolicy() {
        return new LimitPolicy()
                .addDefault(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD))
                .addDefault(Limit.amount("card-amount-24h", LimitScope.CARD, DAY, Money.parse("500.00", Currency.USD), Severity.SOFT))
                .addDefault(Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("10000.00", Currency.USD), Severity.HARD))
                .addDefault(Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("2000.00", Currency.USD), Severity.HARD))
                .override("merchant_bigbox", Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("250000.00", Currency.USD), Severity.HARD))
                .override("merchant_bigbox", Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("50000.00", Currency.USD), Severity.HARD));
    }

    static void show(String label, Decision d) {
        System.out.println(label + " -> " + d.outcome());
        for (String line : d.explanations()) System.out.println("    " + line);
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        LimitEngine engine = new LimitEngine(standardPolicy(), clock);
        String acme = "merchant_acme", card = "fp_4242";

        System.out.println("== Card testing: 5 attempts per card per hour, hard ==");
        for (int i = 1; i <= 6; i++) {
            Decision d = engine.check(new Attempt("att-" + i, acme, card, Money.parse("1.00", Currency.USD)));
            show("attempt " + i + " (1.00 USD)", d);
            if (d.outcome() == Outcome.DENY) System.out.println("    Retry-After: " + d.retryAfterSeconds() + "s");
            clock.advance(10 * SECOND);
        }

        System.out.println("\n== The window slides: 1h after the first attempt, one slot frees ==");
        clock.advance(HOUR - 60 * SECOND);                      // now exactly 1h after attempt 1
        show("attempt 7 at t0+1h", engine.check(new Attempt("att-7", acme, card, Money.parse("1.00", Currency.USD))));
        show("attempt 8 at t0+1h", engine.check(new Attempt("att-8", acme, card, Money.parse("1.00", Currency.USD))));

        System.out.println("\n== Soft limit: a big ticket is allowed but flagged for review ==");
        clock.advance(2 * HOUR);
        String card2 = "fp_1881";
        show("480.00 USD", engine.check(new Attempt("att-9", acme, card2, Money.parse("480.00", Currency.USD))));
        show("30.00 USD (would be 510.00 of 500.00)", engine.check(new Attempt("att-10", acme, card2, Money.parse("30.00", Currency.USD))));

        System.out.println("\n== Exposure: open authorizations count until captured or voided ==");
        String card3 = "fp_7777";
        show("1,400.00 USD auth", engine.check(new Attempt("att-11", acme, card3, Money.parse("1400.00", Currency.USD))));
        show("200.00 USD auth (open exposure 1,916.00)", engine.check(new Attempt("att-12", acme, "fp_7778", Money.parse("200.00", Currency.USD))));
        engine.release("att-11");
        System.out.println("captured att-11: exposure back to " + engine.openExposure(
                standardPolicy().limitsFor(acme).get(3), new Attempt("x", acme, card3, Money.parse("1.00", Currency.USD))));
        show("200.00 USD auth after capture", engine.check(new Attempt("att-13", acme, "fp_7779", Money.parse("200.00", Currency.USD))));

        System.out.println("\n== Hierarchy: bigbox overrides the merchant limits, keeps the card limits ==");
        show("bigbox 15,000.00 USD", engine.check(new Attempt("att-14", "merchant_bigbox", "fp_9001", Money.parse("15000.00", Currency.USD))));
        show("acme   15,000.00 USD", engine.check(new Attempt("att-15", acme, "fp_9002", Money.parse("15000.00", Currency.USD))));

        System.out.println("\n== A denial names every limit it tripped ==");
        Decision multi = engine.check(new Attempt("att-16", acme, card, Money.parse("1400.00", Currency.USD)));
        show("card fp_4242, 1,400.00 USD", multi);
        System.out.println("breached limits: " + multi.reasons().size());
    }
}
