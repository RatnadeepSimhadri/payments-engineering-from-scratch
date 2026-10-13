// Lesson: phases/04-concurrency/08-the-actor-model/docs/en.md
// The Actor Model
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Hewitt, Bishop & Steiger, "A Universal Modular ACTOR Formalism for Artificial Intelligence" (IJCAI 1973);
// Armstrong, "Making reliable distributed systems in the presence of software errors" (PhD thesis, 2003);
// Akka documentation: Dispatchers, Mailboxes, Supervision (doc.akka.io). Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.concurrent.ConcurrentLinkedQueue;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Supplier;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount in minor units of one currency. Immutable, which is what makes it safe to put in a message. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isNegative() { return minorUnits < 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String fracText = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", frac);
        return (minorUnits < 0 ? "-" : "") + whole + fracText + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The actor runtime: messages, references, mailboxes, cells, supervision, schedulers.
// ---------------------------------------------------------------------------------------------------------------------

/** Every message is an immutable record. Actors share nothing else. */
sealed interface Message permits Deposit, Withdraw, GetBalance, Deposited, Withdrawn, InsufficientFunds, BalanceIs,
        Transfer, TransferResult, Increment, CountIs, Crash {}

record Deposit(String correlationId, Money amount, ActorRef replyTo) implements Message {}
record Withdraw(String correlationId, Money amount, ActorRef replyTo) implements Message {}
record GetBalance(ActorRef replyTo) implements Message {}
record Deposited(String correlationId, String account, Money balance) implements Message {}
record Withdrawn(String correlationId, String account, Money amount, Money balance) implements Message {}
record InsufficientFunds(String correlationId, String account, Money requested, Money balance) implements Message {}
record BalanceIs(String account, Money balance) implements Message {}
record Transfer(String transferId, String from, String to, Money amount, ActorRef replyTo) implements Message {}
record TransferResult(String transferId, boolean ok, String reason) implements Message {}
record Increment() implements Message {}
record CountIs(long count) implements Message {}
/** Makes any actor throw: the supervision demo's fault injector. */
record Crash(String reason) implements Message {}

/** What an actor does. One instance, one mailbox, one message at a time: no field of an Actor is ever read by two threads at once. */
abstract class Actor {
    private ActorContext context;

    final void bind(ActorContext context) { this.context = context; }
    protected final ActorContext context() { return context; }
    protected final ActorRef self() { return context.self(); }

    abstract void receive(Message message);

    /** Called after a RESTART before the first message of the new incarnation. Default: nothing. */
    void preStart() {}
}

/** What the runtime gives an actor: who it is, and how to reach others. Never a reference to another actor's object. */
final class ActorContext {
    private final ActorRef self;
    private final ActorSystem system;

    ActorContext(ActorRef self, ActorSystem system) { this.self = self; this.system = system; }

    ActorRef self() { return self; }
    ActorRef lookup(String name) { return system.ref(name); }
}

/** Decides what the runtime does when receive() throws. */
enum SupervisorStrategy {
    RESUME,   // drop the failing message, keep the actor's state
    RESTART,  // drop the failing message, replace the actor with a fresh instance from its factory; mailbox survives
    STOP      // drop the actor; later messages are dead letters
}

/** The only handle other code ever holds. tell() is the only operation. */
final class ActorRef {
    private final String name;
    private final ActorCell cell;

    ActorRef(String name, ActorCell cell) { this.name = name; this.cell = cell; }

    String name() { return name; }

    void tell(Message message) { cell.enqueue(Objects.requireNonNull(message, "message")); }

    @Override public String toString() { return name; }
}

/** Runtime bookkeeping for one actor: its mailbox, its current instance, its factory for restarts, and its counters. */
final class ActorCell {
    final String name;
    final Supplier<Actor> factory;
    final SupervisorStrategy strategy;
    final ConcurrentLinkedQueue<Message> mailbox = new ConcurrentLinkedQueue<>();
    final AtomicBoolean scheduled = new AtomicBoolean(false);          // ThreadedScheduler: at most one run queued or running
    final AtomicInteger concurrentReceives = new AtomicInteger();      // invariant check: never exceeds 1
    final AtomicInteger maxConcurrentReceives = new AtomicInteger();
    final AtomicLong processed = new AtomicLong();
    final AtomicInteger restarts = new AtomicInteger();
    final AtomicInteger failures = new AtomicInteger();
    final AtomicInteger deadLetters = new AtomicInteger();
    volatile boolean stopped = false;
    private volatile Actor actor;
    private final ActorSystem system;
    ActorRef self;

    ActorCell(String name, Supplier<Actor> factory, SupervisorStrategy strategy, ActorSystem system) {
        this.name = name; this.factory = factory; this.strategy = strategy; this.system = system;
    }

    void start() {
        self = new ActorRef(name, this);
        actor = factory.get();
        actor.bind(new ActorContext(self, system));
        actor.preStart();
    }

    void enqueue(Message message) {
        if (stopped) { deadLetters.incrementAndGet(); system.deadLetter(name, message); return; }
        mailbox.offer(message);
        system.scheduler().schedule(this);
    }

    /** Process one message. Returns false if the mailbox was empty. The scheduler guarantees no two calls overlap. */
    boolean processOne() {
        Message message = mailbox.poll();
        if (message == null) return false;
        if (stopped) { deadLetters.incrementAndGet(); system.deadLetter(name, message); return true; }
        int now = concurrentReceives.incrementAndGet();
        maxConcurrentReceives.accumulateAndGet(now, Math::max);
        try {
            system.trace(name, message);
            actor.receive(message);
            processed.incrementAndGet();
        } catch (RuntimeException e) {
            failures.incrementAndGet();
            supervise(message, e);
        } finally {
            concurrentReceives.decrementAndGet();
        }
        return true;
    }

    private void supervise(Message message, RuntimeException e) {
        system.failure(name, message, e, strategy);
        switch (strategy) {
            case RESUME -> { /* state kept, message dropped */ }
            case RESTART -> {
                restarts.incrementAndGet();
                actor = factory.get();
                actor.bind(new ActorContext(self, system));
                actor.preStart();
            }
            case STOP -> {
                stopped = true;
                Message m;
                while ((m = mailbox.poll()) != null) { deadLetters.incrementAndGet(); system.deadLetter(name, m); }
            }
        }
    }
}

/** How mailboxes get processor time. Two implementations: one deterministic for tests, one threaded for production. */
interface Scheduler {
    void schedule(ActorCell cell);
}

/**
 * Single-threaded, replayable. Every actor with a non-empty mailbox is runnable; each step picks one runnable actor
 * (by seeded Random, or round-robin with seed 0) and delivers exactly one message. The interleaving is a function of
 * the seed, so a failing test can be re-run with the same seed and the same trace.
 */
final class DeterministicScheduler implements Scheduler {
    private final ArrayDeque<ActorCell> runnable = new ArrayDeque<>();
    private final Random random;
    private final boolean roundRobin;
    private long steps = 0;

    DeterministicScheduler(long seed) { this.random = new Random(seed); this.roundRobin = seed == 0; }

    @Override public void schedule(ActorCell cell) {
        if (!runnable.contains(cell)) runnable.addLast(cell);
    }

    /** Deliver one message to one actor. Returns false when nothing is runnable. */
    boolean step() {
        if (runnable.isEmpty()) return false;
        ActorCell cell;
        if (roundRobin) {
            cell = runnable.pollFirst();
        } else {
            int index = random.nextInt(runnable.size());
            List<ActorCell> snapshot = new ArrayList<>(runnable);
            cell = snapshot.get(index);
            runnable.remove(cell);
        }
        cell.processOne();
        steps++;
        if (!cell.mailbox.isEmpty() && !runnable.contains(cell)) runnable.addLast(cell);
        return true;
    }

    int runUntilIdle() {
        int delivered = 0;
        while (step()) delivered++;
        return delivered;
    }

    long steps() { return steps; }
}

/**
 * Multi-threaded. When a message arrives and the actor is not already scheduled, one run is submitted to the executor.
 * The run drains up to {@code throughput} messages, clears the flag, and re-schedules itself if more arrived meanwhile.
 * The CAS on {@code scheduled} is the whole guarantee that an actor's receive() never runs on two threads at once.
 */
final class ThreadedScheduler implements Scheduler {
    private final ExecutorService executor;
    private final int throughput;

    ThreadedScheduler(ExecutorService executor, int throughput) { this.executor = executor; this.throughput = throughput; }

    @Override public void schedule(ActorCell cell) {
        if (cell.scheduled.compareAndSet(false, true)) executor.execute(() -> run(cell));
    }

    private void run(ActorCell cell) {
        try {
            for (int i = 0; i < throughput && cell.processOne(); i++) { /* drain a batch */ }
        } finally {
            cell.scheduled.set(false);                                    // release first ...
            if (!cell.mailbox.isEmpty()) schedule(cell);                  // ... then re-check: a message may have landed in between
        }
    }
}

/** Owns the cells, the scheduler, and the trace. */
final class ActorSystem {
    private final Scheduler scheduler;
    private final Map<String, ActorCell> cells = new LinkedHashMap<>();
    private final List<String> trace = new CopyOnWriteArrayList<>();
    private final List<String> failures = new CopyOnWriteArrayList<>();
    private final AtomicInteger deadLetters = new AtomicInteger();
    private final boolean tracing;

    ActorSystem(Scheduler scheduler, boolean tracing) { this.scheduler = scheduler; this.tracing = tracing; }

    Scheduler scheduler() { return scheduler; }

    synchronized ActorRef spawn(String name, Supplier<Actor> factory, SupervisorStrategy strategy) {
        if (cells.containsKey(name)) throw new IllegalArgumentException("actor already exists: " + name);
        ActorCell cell = new ActorCell(name, factory, strategy, this);
        cell.start();
        cells.put(name, cell);
        return cell.self;
    }

    ActorRef ref(String name) {
        ActorCell cell = cells.get(name);
        if (cell == null) throw new IllegalArgumentException("no such actor: " + name);
        return cell.self;
    }

    ActorCell cell(String name) { return cells.get(name); }

    void trace(String actor, Message message) { if (tracing) trace.add(actor + " <- " + describe(message)); }
    void failure(String actor, Message message, RuntimeException e, SupervisorStrategy strategy) {
        failures.add(actor + " failed on " + describe(message) + ": " + e.getMessage() + " -> " + strategy);
    }
    void deadLetter(String actor, Message message) { deadLetters.incrementAndGet(); }

    List<String> trace() { return trace; }
    List<String> failures() { return failures; }
    int deadLetters() { return deadLetters.get(); }

    static String describe(Message m) {
        return switch (m) {
            case Deposit d -> "Deposit(" + d.correlationId() + ", " + d.amount() + ")";
            case Withdraw w -> "Withdraw(" + w.correlationId() + ", " + w.amount() + ")";
            case Deposited d -> "Deposited(" + d.correlationId() + ", " + d.account() + ", " + d.balance() + ")";
            case Withdrawn w -> "Withdrawn(" + w.correlationId() + ", " + w.account() + ", " + w.amount() + ")";
            case InsufficientFunds f -> "InsufficientFunds(" + f.correlationId() + ", " + f.account() + ", wanted " + f.requested() + ", has " + f.balance() + ")";
            case Transfer t -> "Transfer(" + t.transferId() + ", " + t.from() + " -> " + t.to() + ", " + t.amount() + ")";
            case TransferResult r -> "TransferResult(" + r.transferId() + ", " + (r.ok() ? "ok" : "failed: " + r.reason()) + ")";
            case GetBalance g -> "GetBalance";
            case BalanceIs b -> "BalanceIs(" + b.account() + ", " + b.balance() + ")";
            case Increment i -> "Increment";
            case CountIs c -> "CountIs(" + c.count() + ")";
            case Crash c -> "Crash(" + c.reason() + ")";
        };
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The payments actors: one account = one actor, a coordinator for transfers, a probe for tests.
// ---------------------------------------------------------------------------------------------------------------------

/** Owns one balance. No lock anywhere: the mailbox serialises every access. */
final class AccountActor extends Actor {
    private final String accountId;
    private Money balance;

    AccountActor(String accountId, Money opening) {
        if (opening.isNegative()) throw new IllegalArgumentException("opening balance must not be negative");
        this.accountId = accountId;
        this.balance = opening;
    }

    @Override
    void receive(Message message) {
        switch (message) {
            case Deposit d -> {
                balance = balance.plus(d.amount());                          // throws CurrencyMismatch for a wrong-currency deposit
                if (d.replyTo() != null) d.replyTo().tell(new Deposited(d.correlationId(), accountId, balance));
            }
            case Withdraw w -> {
                if (balance.compareTo(w.amount()) < 0) {                     // check ...
                    if (w.replyTo() != null) w.replyTo().tell(new InsufficientFunds(w.correlationId(), accountId, w.amount(), balance));
                } else {
                    balance = balance.minus(w.amount());                     // ... and act: nothing can interleave between them
                    if (w.replyTo() != null) w.replyTo().tell(new Withdrawn(w.correlationId(), accountId, w.amount(), balance));
                }
            }
            case GetBalance g -> g.replyTo().tell(new BalanceIs(accountId, balance));
            case Crash c -> throw new IllegalStateException(c.reason());
            default -> throw new IllegalArgumentException("account does not understand " + ActorSystem.describe(message));
        }
    }

    Money balance() { return balance; }
}

/**
 * Moves money between two account actors with two messages: Withdraw from the source, then Deposit to the destination.
 * The coordinator never holds both balances; it holds the transfer's state and reacts to replies. A failed withdrawal
 * fails the transfer with nothing to undo; a deposit cannot fail for funds reasons.
 */
final class TransferCoordinator extends Actor {
    private final Map<String, Transfer> inFlight = new HashMap<>();
    private int completed = 0, rejected = 0;

    @Override
    void receive(Message message) {
        switch (message) {
            case Transfer t -> {
                inFlight.put(t.transferId(), t);
                context().lookup(t.from()).tell(new Withdraw(t.transferId(), t.amount(), self()));
            }
            case Withdrawn w -> {
                Transfer t = inFlight.get(w.correlationId());
                context().lookup(t.to()).tell(new Deposit(t.transferId(), t.amount(), self()));
            }
            case Deposited d -> {
                Transfer t = inFlight.remove(d.correlationId());
                completed++;
                if (t.replyTo() != null) t.replyTo().tell(new TransferResult(t.transferId(), true, ""));
            }
            case InsufficientFunds f -> {
                Transfer t = inFlight.remove(f.correlationId());
                rejected++;
                if (t.replyTo() != null) t.replyTo().tell(new TransferResult(t.transferId(), false,
                        f.account() + " has " + f.balance() + ", needs " + f.requested()));
            }
            default -> throw new IllegalArgumentException("coordinator does not understand " + ActorSystem.describe(message));
        }
    }

    int completed() { return completed; }
    int rejected() { return rejected; }
    int inFlight() { return inFlight.size(); }
}

/** Counts increments; used to show what RESTART does to state and RESUME does not. */
final class CounterActor extends Actor {
    private long count = 0;

    @Override
    void receive(Message message) {
        switch (message) {
            case Increment i -> count++;
            case GetBalance g -> g.replyTo().tell(new CountIs(count));
            case Crash c -> throw new IllegalStateException(c.reason());
            default -> throw new IllegalArgumentException("counter does not understand " + ActorSystem.describe(message));
        }
    }
}

/** A test inbox: records everything it receives. */
final class ProbeActor extends Actor {
    final List<Message> received = new CopyOnWriteArrayList<>();
    final CountDownLatch latch;

    ProbeActor(int expected) { this.latch = new CountDownLatch(expected); }
    ProbeActor() { this(0); }

    @Override void receive(Message message) { received.add(message); latch.countDown(); }

    List<TransferResult> transferResults() {
        List<TransferResult> out = new ArrayList<>();
        for (Message m : received) if (m instanceof TransferResult r) out.add(r);
        return out;
    }

    Map<String, Money> balances() {
        Map<String, Money> out = new LinkedHashMap<>();
        for (Message m : received) if (m instanceof BalanceIs b) out.put(b.account(), b.balance());
        return out;
    }
}

public final class Main {
    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    /** Builds three accounts and a coordinator on the given scheduler. Total money in the system: 300.00 USD. */
    static ActorSystem bankWith(Scheduler scheduler, boolean tracing) {
        ActorSystem system = new ActorSystem(scheduler, tracing);
        system.spawn("acct-A", () -> new AccountActor("acct-A", usd(10_000)), SupervisorStrategy.RESUME);
        system.spawn("acct-B", () -> new AccountActor("acct-B", usd(10_000)), SupervisorStrategy.RESUME);
        system.spawn("acct-C", () -> new AccountActor("acct-C", usd(10_000)), SupervisorStrategy.RESUME);
        system.spawn("transfers", TransferCoordinator::new, SupervisorStrategy.RESUME);
        return system;
    }

    static Money total(ActorSystem system, String... accounts) {
        Money sum = usd(0);
        for (String a : accounts) sum = sum.plus(((AccountActor) actorOf(system, a)).balance());
        return sum;
    }

    /** Test-only peek at the actor instance; production code never does this, which is the point of the model. */
    static Actor actorOf(ActorSystem system, String name) {
        try {
            var f = ActorCell.class.getDeclaredField("actor");
            f.setAccessible(true);
            return (Actor) f.get(system.cell(name));
        } catch (ReflectiveOperationException e) { throw new IllegalStateException(e); }
    }

    static List<Transfer> scriptedTransfers(ActorRef replyTo) {
        return List.of(
                new Transfer("t1", "acct-A", "acct-B", usd(7_500), replyTo),
                new Transfer("t2", "acct-B", "acct-C", usd(12_000), replyTo),
                new Transfer("t3", "acct-C", "acct-A", usd(5_000), replyTo),
                new Transfer("t4", "acct-A", "acct-C", usd(9_000), replyTo),
                new Transfer("t5", "acct-B", "acct-A", usd(2_500), replyTo),
                new Transfer("t6", "acct-C", "acct-B", usd(1_000), replyTo));
    }

    static void demoDeterministic(long seed) {
        DeterministicScheduler scheduler = new DeterministicScheduler(seed);
        ActorSystem system = bankWith(scheduler, true);
        ActorRef probe = system.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        for (Transfer t : scriptedTransfers(probe)) system.ref("transfers").tell(t);
        int delivered = scheduler.runUntilIdle();
        ProbeActor p = (ProbeActor) actorOf(system, "probe");
        System.out.println("seed " + seed + ": " + delivered + " messages delivered, " + system.trace().size() + " traced");
        for (TransferResult r : p.transferResults()) System.out.println("  " + ActorSystem.describe(r));
        System.out.println("  balances: A=" + ((AccountActor) actorOf(system, "acct-A")).balance() + " B=" + ((AccountActor) actorOf(system, "acct-B")).balance()
                + " C=" + ((AccountActor) actorOf(system, "acct-C")).balance() + "  total=" + total(system, "acct-A", "acct-B", "acct-C"));
    }

    public static void main(String[] args) throws Exception {
        System.out.println("== One account, one mailbox: six transfers on a deterministic scheduler ==");
        demoDeterministic(42);
        System.out.println("first 6 trace lines (seed 42):");
        DeterministicScheduler s42 = new DeterministicScheduler(42);
        ActorSystem sys42 = bankWith(s42, true);
        ActorRef probe42 = sys42.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        for (Transfer t : scriptedTransfers(probe42)) sys42.ref("transfers").tell(t);
        s42.runUntilIdle();
        for (String line : sys42.trace().subList(0, 6)) System.out.println("  " + line);
        System.out.println();
        System.out.println("Same script, different seed: a different interleaving, the same conservation of money.");
        demoDeterministic(7);
        System.out.println();

        System.out.println("== Supervision: RESUME keeps state, RESTART resets it, STOP turns the mailbox into dead letters ==");
        DeterministicScheduler sched = new DeterministicScheduler(0);
        ActorSystem system = new ActorSystem(sched, false);
        ActorRef probe = system.spawn("probe", ProbeActor::new, SupervisorStrategy.RESUME);
        ActorRef account = system.spawn("acct-X", () -> new AccountActor("acct-X", usd(5_000)), SupervisorStrategy.RESUME);
        ActorRef resumed = system.spawn("counter-resume", CounterActor::new, SupervisorStrategy.RESUME);
        ActorRef restarted = system.spawn("counter-restart", CounterActor::new, SupervisorStrategy.RESTART);
        ActorRef stopped = system.spawn("counter-stop", CounterActor::new, SupervisorStrategy.STOP);
        account.tell(new Deposit("d1", Money.ofMinor(100, Currency.EUR), probe));            // wrong currency: receive() throws
        account.tell(new GetBalance(probe));
        for (ActorRef c : List.of(resumed, restarted, stopped)) {
            c.tell(new Increment()); c.tell(new Increment()); c.tell(new Crash("simulated bug")); c.tell(new Increment()); c.tell(new GetBalance(probe));
        }
        sched.runUntilIdle();
        for (String f : system.failures()) System.out.println("  " + f);
        ProbeActor p = (ProbeActor) actorOf(system, "probe");
        System.out.println("  acct-X balance after the bad deposit was dropped: " + p.balances().get("acct-X"));
        List<Long> counts = new ArrayList<>();
        for (Message m : p.received) if (m instanceof CountIs c) counts.add(c.count());
        System.out.println("  counter-resume reports " + counts.get(0) + " (2 before the crash + 1 after); counter-restart reports " + counts.get(1)
                + " (state reset); counter-stop reports nothing, dead letters = " + system.deadLetters());
        System.out.println();

        System.out.println("== Threaded scheduler: 4 threads x 25,000 deposits into one account, no locks in AccountActor ==");
        try (ExecutorService pool = java.util.concurrent.Executors.newFixedThreadPool(4)) {
            ActorSystem threaded = new ActorSystem(new ThreadedScheduler(pool, 64), false);
            ActorRef acct = threaded.spawn("acct-hot", () -> new AccountActor("acct-hot", usd(0)), SupervisorStrategy.RESUME);
            int threads = 4, perThread = 25_000;
            CountDownLatch done = new CountDownLatch(threads);
            for (int t = 0; t < threads; t++) {
                pool.execute(() -> {
                    for (int i = 0; i < perThread; i++) acct.tell(new Deposit("x", usd(1), null));
                    done.countDown();
                });
            }
            done.await();
            ActorRef probeRef = threaded.spawn("probe", () -> new ProbeActor(1), SupervisorStrategy.RESUME);
            acct.tell(new GetBalance(probeRef));
            ProbeActor pr = (ProbeActor) actorOf(threaded, "probe");
            pr.latch.await(10, TimeUnit.SECONDS);
            ActorCell cell = threaded.cell("acct-hot");
            System.out.println("  balance = " + pr.balances().get("acct-hot") + " (expected 1000.00 USD); messages processed = " + cell.processed.get()
                    + "; max concurrent receive() = " + cell.maxConcurrentReceives.get());
        }
    }
}
