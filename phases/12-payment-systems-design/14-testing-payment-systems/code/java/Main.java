// Lesson: phases/12-payment-systems-design/14-testing-payment-systems/docs/en.md
// Testing Payment Systems: Simulators, Property Tests, and Invariants
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Claessen & Hughes, "QuickCheck: A Lightweight Tool for Random Testing of Haskell Programs" (ICFP 2000);
//          Hughes, "Experiences with QuickCheck: Testing the Hard Stuff and Staying Sane" (2016). Java 21, stdlib only.

import java.util.ArrayList;
import java.util.EnumMap;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.function.Function;

// ---------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01: integer minor units plus a currency. Never a float.
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
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }

    /** Percentage in basis points (1 bp = 0.01%), rounded HALF_EVEN. */
    Money basisPoints(long bps) {
        long product = Math.multiplyExact(minorUnits, bps);
        long q = product / 10_000, r = product % 10_000;
        long twice = Math.abs(r) * 2;
        int sign = product < 0 ? -1 : 1;
        if (twice > 10_000 || (twice == 10_000 && q % 2 != 0)) q += sign;
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
// Step 1: the system under test. A payment with a ledger, talking to a processor that can fail.
// ---------------------------------------------------------------------------------------------
enum Account { CUSTOMER, MERCHANT, PLATFORM_FEES }

/** Three accounts, double-entry: every posting debits one and credits another by the same amount. */
final class Ledger {
    private final Map<Account, Money> balances = new EnumMap<>(Account.class);
    final List<String> postings = new ArrayList<>();

    Ledger(Currency currency) { for (Account a : Account.values()) balances.put(a, Money.zero(currency)); }

    void post(Account from, Account to, Money amount, String memo) {
        if (amount.isNegative()) throw new IllegalArgumentException("posting amount must not be negative: " + amount);
        balances.put(from, balances.get(from).minus(amount));
        balances.put(to, balances.get(to).plus(amount));
        postings.add(memo + ": " + from + " -> " + to + " " + amount);
    }

    /** A deliberately unbalanced write, reachable only through Bugs.unbalancedFee. */
    void creditOnly(Account to, Money amount, String memo) {
        balances.put(to, balances.get(to).plus(amount));
        postings.add(memo + ": (nothing) -> " + to + " " + amount);
    }

    Money balance(Account a) { return balances.get(a); }

    Money total() {
        Money t = Money.zero(balances.get(Account.CUSTOMER).currency());
        for (Money m : balances.values()) t = t.plus(m);
        return t;
    }
}

/** How the processor behaves on one call. A timeout is ambiguous: the operation may or may not have been applied. */
enum ProcessorBehaviour {
    APPROVE,              // applied, success returned
    DECLINE,              // not applied, decline returned
    TIMEOUT_AFTER_APPLY,  // applied, but the response was lost on the way back
    TIMEOUT_BEFORE_APPLY, // the request never arrived; nothing applied
    ERROR                 // not applied, 5xx returned
}

final class ProcessorTimeout extends RuntimeException {
    ProcessorTimeout(String op) { super("timed out waiting for processor response to " + op); }
}

/** The processor's own record of a payment. This is the external truth the ledger must reconcile to. */
final class ProcessorPayment {
    Money authorized, captured, refunded;
    boolean voided;
    ProcessorPayment(Currency c) { authorized = captured = refunded = Money.zero(c); }
}

/** A processor whose behaviour per call is scripted. Exhausting the script means APPROVE. */
final class FakeProcessor {
    private final List<ProcessorBehaviour> script = new ArrayList<>();
    private int cursor = 0;
    final ProcessorPayment record;
    int calls = 0;

    FakeProcessor(Currency currency) { record = new ProcessorPayment(currency); }

    FakeProcessor script(ProcessorBehaviour... behaviours) { for (ProcessorBehaviour b : behaviours) script.add(b); return this; }

    private ProcessorBehaviour next() {
        calls++;
        return cursor < script.size() ? script.get(cursor++) : ProcessorBehaviour.APPROVE;
    }

    /** Applies the operation if the processor would, then returns or throws according to the behaviour. */
    boolean call(String op, Runnable apply) {
        ProcessorBehaviour b = next();
        return switch (b) {
            case APPROVE -> { apply.run(); yield true; }
            case DECLINE -> false;
            case TIMEOUT_AFTER_APPLY -> { apply.run(); throw new ProcessorTimeout(op); }
            case TIMEOUT_BEFORE_APPLY -> throw new ProcessorTimeout(op);
            case ERROR -> throw new IllegalStateException("processor 5xx on " + op);
        };
    }

    boolean authorize(Money amount) { return call("authorize", () -> record.authorized = amount); }
    boolean capture(Money amount) { return call("capture", () -> record.captured = record.captured.plus(amount)); }
    boolean refund(Money amount) { return call("refund", () -> record.refunded = record.refunded.plus(amount)); }
    boolean voidAuth() { return call("void", () -> record.voided = true); }
}

/** Switches that re-introduce real bugs so the property tests have something to find. All false in production. */
record Bugs(boolean allowOverCapture, boolean allowOverRefund, boolean unbalancedFee, boolean postOnTimeout) {
    static final Bugs NONE = new Bugs(false, false, false, false);
}

enum PaymentState { NEW, AUTHORIZED, PARTIALLY_CAPTURED, CAPTURED, VOIDED, UNKNOWN }

final class PaymentService {
    static final long FEE_BPS = 290; // 2.9%
    final Ledger ledger;
    final FakeProcessor processor;
    final Bugs bugs;
    final Currency currency;
    PaymentState state = PaymentState.NEW;
    Money authorized, captured, refunded;
    String lastUnknownOp = null;

    PaymentService(Currency currency, FakeProcessor processor, Bugs bugs) {
        this.currency = currency;
        this.processor = processor;
        this.bugs = bugs;
        this.ledger = new Ledger(currency);
        authorized = captured = refunded = Money.zero(currency);
    }

    private void requirePositive(Money m) {
        if (m.minorUnits() <= 0) throw new IllegalArgumentException("amount must be positive: " + m);
    }

    boolean authorize(Money amount) {
        requirePositive(amount);
        if (state != PaymentState.NEW) throw new IllegalStateException("authorize in state " + state);
        try {
            if (!processor.authorize(amount)) return false;
        } catch (ProcessorTimeout t) {
            state = PaymentState.UNKNOWN; lastUnknownOp = "authorize:" + amount.minorUnits(); return false;
        }
        authorized = amount;
        state = PaymentState.AUTHORIZED;
        return true;
    }

    boolean capture(Money amount) {
        requirePositive(amount);
        if (state != PaymentState.AUTHORIZED && state != PaymentState.PARTIALLY_CAPTURED) throw new IllegalStateException("capture in state " + state);
        if (!bugs.allowOverCapture() && captured.plus(amount).compareTo(authorized) > 0) {
            throw new IllegalArgumentException("capture " + amount + " exceeds remaining authorization " + authorized.minus(captured));
        }
        try {
            if (!processor.capture(amount)) return false;
        } catch (ProcessorTimeout t) {
            if (bugs.postOnTimeout()) postCapture(amount);  // the bug: assuming a timeout meant success
            state = PaymentState.UNKNOWN; lastUnknownOp = "capture:" + amount.minorUnits(); return false;
        }
        postCapture(amount);
        return true;
    }

    private void postCapture(Money amount) {
        Money fee = amount.basisPoints(FEE_BPS);
        ledger.post(Account.CUSTOMER, Account.MERCHANT, amount, "capture");
        if (bugs.unbalancedFee()) ledger.creditOnly(Account.PLATFORM_FEES, fee, "fee");
        else ledger.post(Account.MERCHANT, Account.PLATFORM_FEES, fee, "fee");
        captured = captured.plus(amount);
        state = captured.compareTo(authorized) >= 0 ? PaymentState.CAPTURED : PaymentState.PARTIALLY_CAPTURED;
    }

    boolean refund(Money amount) {
        requirePositive(amount);
        if (state != PaymentState.PARTIALLY_CAPTURED && state != PaymentState.CAPTURED) throw new IllegalStateException("refund in state " + state);
        if (!bugs.allowOverRefund() && refunded.plus(amount).compareTo(captured) > 0) {
            throw new IllegalArgumentException("refund " + amount + " exceeds refundable " + captured.minus(refunded));
        }
        try {
            if (!processor.refund(amount)) return false;
        } catch (ProcessorTimeout t) {
            state = PaymentState.UNKNOWN; lastUnknownOp = "refund:" + amount.minorUnits(); return false;
        }
        postRefund(amount);
        return true;
    }

    private void postRefund(Money amount) {
        ledger.post(Account.MERCHANT, Account.CUSTOMER, amount, "refund");
        refunded = refunded.plus(amount);
    }

    boolean voidAuthorization() {
        if (state != PaymentState.AUTHORIZED) throw new IllegalStateException("void in state " + state);
        try {
            if (!processor.voidAuth()) return false;
        } catch (ProcessorTimeout t) {
            state = PaymentState.UNKNOWN; lastUnknownOp = "void"; return false;
        }
        state = PaymentState.VOIDED;
        return true;
    }

    /** After a timeout, ask the processor what actually happened and bring the ledger into line. */
    void reconcile() {
        if (state != PaymentState.UNKNOWN) return;
        ProcessorPayment p = processor.record;
        if (!p.authorized.equals(authorized)) { authorized = p.authorized; }
        Money missingCapture = p.captured.minus(captured);
        if (missingCapture.minorUnits() > 0) postCapture(missingCapture);
        Money missingRefund = p.refunded.minus(refunded);
        if (missingRefund.minorUnits() > 0) postRefund(missingRefund);
        if (p.voided) state = PaymentState.VOIDED;
        else if (captured.isZero()) state = authorized.isZero() ? PaymentState.NEW : PaymentState.AUTHORIZED;
        else state = captured.compareTo(authorized) >= 0 ? PaymentState.CAPTURED : PaymentState.PARTIALLY_CAPTURED;
        lastUnknownOp = null;
    }
}

// ---------------------------------------------------------------------------------------------
// Step 2: the invariants. Each returns null when it holds, or a sentence naming the violation.
// ---------------------------------------------------------------------------------------------
final class Invariants {
    static String check(PaymentService s) {
        if (!s.ledger.total().isZero()) return "money not conserved: ledger total " + s.ledger.total();
        if (s.captured.compareTo(s.authorized) > 0) return "captured " + s.captured + " > authorized " + s.authorized;
        if (s.refunded.compareTo(s.captured) > 0) return "refunded " + s.refunded + " > captured " + s.captured;
        Money customerExpected = s.captured.minus(s.refunded).negate();
        if (!s.ledger.balance(Account.CUSTOMER).equals(customerExpected)) {
            return "customer balance " + s.ledger.balance(Account.CUSTOMER) + " != -(captured - refunded) " + customerExpected;
        }
        if (s.ledger.balance(Account.PLATFORM_FEES).isNegative()) return "negative fee balance";
        if (s.state != PaymentState.UNKNOWN) {
            ProcessorPayment p = s.processor.record;
            if (!p.captured.equals(s.captured)) return "ledger captured " + s.captured + " != processor captured " + p.captured;
            if (!p.refunded.equals(s.refunded)) return "ledger refunded " + s.refunded + " != processor refunded " + p.refunded;
        }
        return null;
    }
}

// ---------------------------------------------------------------------------------------------
// Step 3: a property-based testing harness. Generators produce values and know how to shrink them.
// ---------------------------------------------------------------------------------------------
interface Gen<T> {
    T generate(Random rng);
    /** Smaller candidates, simplest first. Empty when the value cannot be made smaller. */
    List<T> shrink(T value);
}

final class Gens {
    private Gens() {}

    /** Uniform in [lo, hi]; shrinks toward lo by halving the distance, then by single steps. */
    static Gen<Long> longs(long lo, long hi) {
        return new Gen<>() {
            @Override public Long generate(Random rng) { return lo + (long) (rng.nextDouble() * (hi - lo + 1)); }
            @Override public List<Long> shrink(Long v) {
                List<Long> out = new ArrayList<>();
                if (v <= lo) return out;
                out.add(lo);
                long d = v - lo;
                for (long step = d / 2; step > 0; step /= 2) out.add(v - step);
                if (!out.contains(v - 1)) out.add(v - 1);
                return out;
            }
        };
    }

    static <T> Gen<T> oneOf(List<T> values) {
        return new Gen<>() {
            @Override public T generate(Random rng) { return values.get(rng.nextInt(values.size())); }
            @Override public List<T> shrink(T v) {
                List<T> out = new ArrayList<>();
                for (T c : values) { if (c.equals(v)) break; out.add(c); }   // earlier entries are "simpler"
                return out;
            }
        };
    }

    /** Lists of 1..maxLen elements; shrinks by dropping chunks, then single elements, then shrinking elements. */
    static <T> Gen<List<T>> lists(Gen<T> element, int maxLen) {
        return new Gen<>() {
            @Override public List<T> generate(Random rng) {
                int n = 1 + rng.nextInt(maxLen);
                List<T> out = new ArrayList<>(n);
                for (int i = 0; i < n; i++) out.add(element.generate(rng));
                return out;
            }
            @Override public List<List<T>> shrink(List<T> v) {
                List<List<T>> out = new ArrayList<>();
                int n = v.size();
                for (int chunk = n / 2; chunk >= 1; chunk /= 2) {
                    for (int start = 0; start + chunk <= n; start += chunk) {
                        List<T> c = new ArrayList<>(v.subList(0, start));
                        c.addAll(v.subList(start + chunk, n));
                        if (!c.isEmpty()) out.add(c);
                    }
                }
                for (int i = 0; i < n; i++) {
                    for (T smaller : element.shrink(v.get(i))) {
                        List<T> c = new ArrayList<>(v);
                        c.set(i, smaller);
                        out.add(c);
                    }
                }
                return out;
            }
        };
    }
}

record Counterexample<T>(T original, T shrunk, int shrinkSteps, String reason) {}

record PropertyResult<T>(String name, boolean passed, long seed, int trialsRun, Counterexample<T> counterexample) {
    String summary() {
        if (passed) return name + ": OK, " + trialsRun + " trials (seed " + seed + ")";
        return name + ": FAILED at trial " + trialsRun + " (seed " + seed + ")\n  original: " + counterexample.original()
                + "\n  shrunk (" + counterexample.shrinkSteps() + " steps): " + counterexample.shrunk()
                + "\n  reason: " + counterexample.reason();
    }
}

final class Property {
    static final int MAX_SHRINK_STEPS = 2_000;

    /** Runs `check` on `trials` generated values. `check` returns null when the property holds. */
    static <T> PropertyResult<T> forAll(String name, Gen<T> gen, Function<T, String> check, long seed, int trials) {
        Random rng = new Random(seed);
        for (int i = 1; i <= trials; i++) {
            T value = gen.generate(rng);
            String reason = safeCheck(check, value);
            if (reason == null) continue;
            T current = value;
            String currentReason = reason;
            int steps = 0;
            boolean progressed = true;
            while (progressed && steps < MAX_SHRINK_STEPS) {
                progressed = false;
                for (T candidate : gen.shrink(current)) {
                    steps++;
                    String r = safeCheck(check, candidate);
                    if (r != null) { current = candidate; currentReason = r; progressed = true; break; }
                    if (steps >= MAX_SHRINK_STEPS) break;
                }
            }
            return new PropertyResult<>(name, false, seed, i, new Counterexample<>(value, current, steps, currentReason));
        }
        return new PropertyResult<>(name, true, seed, trials, null);
    }

    private static <T> String safeCheck(Function<T, String> check, T value) {
        try {
            return check.apply(value);
        } catch (IllegalArgumentException | IllegalStateException expected) {
            return null;   // the system rejected the input on purpose: the property is about accepted inputs
        } catch (RuntimeException crash) {
            return "crash: " + crash;
        }
    }
}

// ---------------------------------------------------------------------------------------------
// Step 4: scenarios as data. A command list plus a processor script replays identically every time.
// ---------------------------------------------------------------------------------------------
enum CommandKind { AUTHORIZE, CAPTURE, REFUND, VOID, RECONCILE }

record Command(CommandKind kind, long amountMinor, ProcessorBehaviour behaviour) {
    @Override public String toString() {
        return switch (kind) {
            case VOID, RECONCILE -> kind + (behaviour == ProcessorBehaviour.APPROVE ? "" : "/" + behaviour);
            default -> kind + " " + amountMinor + (behaviour == ProcessorBehaviour.APPROVE ? "" : "/" + behaviour);
        };
    }
}

record Scenario(String name, List<Command> commands) {}

record ScenarioRun(Scenario scenario, PaymentService service, List<String> log, String violation) {
    boolean passed() { return violation == null; }
}

final class ScenarioRunner {
    static final Gen<Command> COMMANDS;
    static {
        Gen<Long> amounts = Gens.longs(1, 10_000);
        Gen<CommandKind> kinds = Gens.oneOf(List.of(CommandKind.AUTHORIZE, CommandKind.CAPTURE, CommandKind.REFUND, CommandKind.RECONCILE, CommandKind.VOID));
        Gen<ProcessorBehaviour> behaviours = Gens.oneOf(List.of(ProcessorBehaviour.APPROVE, ProcessorBehaviour.DECLINE,
                ProcessorBehaviour.TIMEOUT_AFTER_APPLY, ProcessorBehaviour.TIMEOUT_BEFORE_APPLY, ProcessorBehaviour.ERROR));
        COMMANDS = new Gen<>() {
            @Override public Command generate(Random rng) {
                CommandKind k = kinds.generate(rng);
                // bias toward the happy path so sequences get deep enough to reach refunds
                ProcessorBehaviour b = rng.nextInt(10) < 7 ? ProcessorBehaviour.APPROVE : behaviours.generate(rng);
                return new Command(k, amounts.generate(rng), b);
            }
            @Override public List<Command> shrink(Command c) {
                List<Command> out = new ArrayList<>();
                for (ProcessorBehaviour b : behaviours.shrink(c.behaviour())) out.add(new Command(c.kind(), c.amountMinor(), b));
                for (long a : amounts.shrink(c.amountMinor())) out.add(new Command(c.kind(), a, c.behaviour()));
                return out;
            }
        };
    }

    static Gen<List<Command>> sequences(int maxLen) { return Gens.lists(COMMANDS, maxLen); }

    /** Applies the commands in order. Commands the service rejects as illegal are logged and skipped. */
    static ScenarioRun run(Scenario scenario, Bugs bugs) {
        FakeProcessor processor = new FakeProcessor(Currency.USD);
        PaymentService service = new PaymentService(Currency.USD, processor, bugs);
        List<String> log = new ArrayList<>();
        for (Command c : scenario.commands()) {
            processor.script(c.behaviour());
            Money amount = Money.ofMinor(c.amountMinor(), Currency.USD);
            String outcome;
            try {
                outcome = switch (c.kind()) {
                    case AUTHORIZE -> service.authorize(amount) ? "ok" : "declined/unknown";
                    case CAPTURE -> service.capture(amount) ? "ok" : "declined/unknown";
                    case REFUND -> service.refund(amount) ? "ok" : "declined/unknown";
                    case VOID -> service.voidAuthorization() ? "ok" : "declined/unknown";
                    case RECONCILE -> { service.reconcile(); yield "ok"; }
                };
            } catch (IllegalArgumentException | IllegalStateException rejected) {
                outcome = "rejected: " + rejected.getMessage();
            }
            log.add(c + " -> " + outcome + " [" + service.state + "]");
            String violation = Invariants.check(service);
            if (violation != null) return new ScenarioRun(scenario, service, log, violation + " after " + c);
        }
        return new ScenarioRun(scenario, service, log, null);
    }

    /** The property: no command sequence, with any processor behaviour, violates the invariants. */
    static PropertyResult<List<Command>> invariantsHold(Bugs bugs, long seed, int trials, int maxLen) {
        return Property.forAll("invariants hold under " + bugs, sequences(maxLen),
                cmds -> run(new Scenario("generated", cmds), bugs).violation(), seed, trials);
    }
}

public final class Main {
    public static void main(String[] args) {
        Currency usd = Currency.USD;
        System.out.println("== One scripted scenario: capture times out, reconcile repairs the ledger ==");
        Scenario timeout = new Scenario("timeout then reconcile", List.of(
                new Command(CommandKind.AUTHORIZE, 5000, ProcessorBehaviour.APPROVE),
                new Command(CommandKind.CAPTURE, 5000, ProcessorBehaviour.TIMEOUT_AFTER_APPLY),
                new Command(CommandKind.RECONCILE, 0, ProcessorBehaviour.APPROVE),
                new Command(CommandKind.REFUND, 1250, ProcessorBehaviour.APPROVE)));
        ScenarioRun r = ScenarioRunner.run(timeout, Bugs.NONE);
        r.log().forEach(l -> System.out.println("  " + l));
        System.out.println("  ledger: customer " + r.service().ledger.balance(Account.CUSTOMER) + ", merchant "
                + r.service().ledger.balance(Account.MERCHANT) + ", fees " + r.service().ledger.balance(Account.PLATFORM_FEES)
                + ", total " + r.service().ledger.total());
        System.out.println("  invariants: " + (r.passed() ? "hold" : r.violation()));

        System.out.println("\n== Property: invariants hold for 500 random sequences (seed 42) ==");
        System.out.println(ScenarioRunner.invariantsHold(Bugs.NONE, 42, 500, 12).summary());

        System.out.println("\n== Same property with real bugs switched back on (500 trials) ==");
        for (Bugs bug : List.of(new Bugs(true, false, false, false), new Bugs(false, true, false, false),
                new Bugs(false, false, true, false), new Bugs(false, false, false, true))) {
            System.out.println(ScenarioRunner.invariantsHold(bug, 42, 500, 12).summary());
        }
        System.out.println("\n== The timeout bug needs AUTHORIZE, CAPTURE/TIMEOUT_BEFORE_APPLY, RECONCILE in that order: 2000 trials ==");
        System.out.println(ScenarioRunner.invariantsHold(new Bugs(false, false, false, true), 42, 2000, 12).summary());

        System.out.println("\n== Shrinking a plain integer property: 'truncating the 2.9% fee equals HALF_EVEN' ==");
        PropertyResult<Long> ints = Property.forAll("truncated fee == rounded fee", Gens.longs(1, 1_000_000),
                n -> {
                    long truncated = n * PaymentService.FEE_BPS / 10_000;
                    long rounded = Money.ofMinor(n, usd).basisPoints(PaymentService.FEE_BPS).minorUnits();
                    return truncated == rounded ? null : "n=" + n + ": truncated " + truncated + " != rounded " + rounded;
                }, 7, 10_000);
        System.out.println(ints.summary());
        System.out.println("\nFee on 100.00 USD at 2.9% = " + Money.ofMinor(10_000, usd).basisPoints(PaymentService.FEE_BPS));
    }
}
