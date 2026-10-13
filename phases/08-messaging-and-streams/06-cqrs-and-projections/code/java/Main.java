// Lesson: phases/08-messaging-and-streams/06-cqrs-and-projections/docs/en.md
// CQRS and Projections
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Greg Young, "CQRS Documents" (2010); Fowler, "CQRS" (martinfowler.com, 2011); Kleppmann, "Designing
//          Data-Intensive Applications", ch. 11 (deriving state from an event log); EventStoreDB docs on $all and checkpoints.
// Java 21, standard library only. Time comes from an injected SimClock; nothing sleeps.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

final class CurrencyMismatchException extends RuntimeException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Integer minor units plus a currency. Addition is checked; mixing currencies is an error. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    boolean isPositive() { return minorUnits > 0; }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override
    public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/** Simulated clock: the demo advances it explicitly. Milliseconds since an arbitrary epoch. */
final class SimClock {
    private long now;

    SimClock(long start) { this.now = start; }

    long now() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("cannot go backwards");
        now += millis;
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Write side: commands are validated against the aggregate's current state; the result is events appended to the store.
// ---------------------------------------------------------------------------------------------------------------------

sealed interface Event permits AccountOpened, Deposited, Withdrawn {}

record AccountOpened(String accountId, String owner, Currency currency) implements Event {}

record Deposited(Money amount, String reference) implements Event {}

record Withdrawn(Money amount, String reference) implements Event {}

sealed interface Command permits OpenAccount, Deposit, Withdraw {}

record OpenAccount(String accountId, String owner, Currency currency) implements Command {}

record Deposit(Money amount, String reference) implements Command {}

record Withdraw(Money amount, String reference) implements Command {}

final class CommandRejectedException extends RuntimeException {
    CommandRejectedException(String message) { super(message); }
}

final class ConcurrencyException extends RuntimeException {
    ConcurrencyException(String streamId, long expected, long actual) {
        super("stream " + streamId + " expected version " + expected + " but is at " + actual);
    }
}

/** The write model: only what is needed to validate commands. No owner name, no history, no daily totals. */
record AccountState(String accountId, Currency currency, Money balance, long version) {
    static final AccountState EMPTY = new AccountState(null, null, null, 0);

    boolean exists() { return accountId != null; }

    AccountState apply(Event event) {
        return switch (event) {
            case AccountOpened e -> new AccountState(e.accountId(), e.currency(), Money.zero(e.currency()), version + 1);
            case Deposited e -> new AccountState(accountId, currency, balance.plus(e.amount()), version + 1);
            case Withdrawn e -> new AccountState(accountId, currency, balance.minus(e.amount()), version + 1);
        };
    }

    /** Decide: validate the command against current state and return the events it produces, or reject it. */
    List<Event> decide(Command command) {
        return switch (command) {
            case OpenAccount c -> {
                if (exists()) throw new CommandRejectedException("account " + c.accountId() + " already open");
                yield List.of(new AccountOpened(c.accountId(), c.owner(), c.currency()));
            }
            case Deposit c -> {
                requireOpen();
                if (!c.amount().isPositive()) throw new CommandRejectedException("deposit must be positive");
                yield List.of(new Deposited(c.amount(), c.reference()));
            }
            case Withdraw c -> {
                requireOpen();
                if (!c.amount().isPositive()) throw new CommandRejectedException("withdrawal must be positive");
                if (balance.compareTo(c.amount()) < 0) throw new CommandRejectedException("insufficient funds: balance " + balance + ", requested " + c.amount());
                yield List.of(new Withdrawn(c.amount(), c.reference()));
            }
        };
    }

    private void requireOpen() {
        if (!exists()) throw new CommandRejectedException("account does not exist");
    }
}

/**
 * An event as stored: its position in the global log (dense, 1-based, across all streams), its stream and version
 * within that stream, and when the store recorded it. Projections consume the global log in position order.
 */
record EventEnvelope(long position, String streamId, long version, long recordedAt, Event event) {}

/** In-memory event store with per-stream optimistic concurrency and a single global log that projections read. */
final class EventStore {
    private final Map<String, List<EventEnvelope>> streams = new HashMap<>();
    private final List<EventEnvelope> all = new ArrayList<>();

    long version(String streamId) { return streams.getOrDefault(streamId, List.of()).size(); }

    /** Position of the newest event in the global log; 0 when empty. */
    long lastPosition() { return all.size(); }

    /** Check-and-append in one step: the stream must be at expectedVersion. Returns the position of the last event written. */
    long append(String streamId, long expectedVersion, List<Event> events, long recordedAt) {
        List<EventEnvelope> stream = streams.computeIfAbsent(streamId, k -> new ArrayList<>());
        if (stream.size() != expectedVersion) throw new ConcurrencyException(streamId, expectedVersion, stream.size());
        for (Event e : events) {
            EventEnvelope env = new EventEnvelope(all.size() + 1, streamId, stream.size() + 1, recordedAt, e);
            stream.add(env);
            all.add(env);
        }
        return all.size();
    }

    List<EventEnvelope> readStream(String streamId) {
        return Collections.unmodifiableList(streams.getOrDefault(streamId, List.of()));
    }

    /** Up to max events with position > afterPosition, in position order. The read-side's only entry point. */
    List<EventEnvelope> readAll(long afterPosition, int max) {
        if (afterPosition < 0 || afterPosition > all.size()) throw new IllegalArgumentException("position " + afterPosition + " not in [0, " + all.size() + "]");
        int from = (int) afterPosition;
        int to = Math.min(all.size(), from + Math.max(0, max));
        return new ArrayList<>(all.subList(from, to));
    }
}

/** Loads the stream, folds it into state, decides, appends with the loaded version. Returns the global position written. */
final class CommandHandler {
    private final EventStore store;
    private final SimClock clock;

    CommandHandler(EventStore store, SimClock clock) {
        this.store = store;
        this.clock = clock;
    }

    long handle(String accountId, Command command) {
        AccountState state = AccountState.EMPTY;
        for (EventEnvelope env : store.readStream(accountId)) state = state.apply(env.event());
        List<Event> produced = state.decide(command);
        return store.append(accountId, state.version(), produced, clock.now());
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Read side: projections fold the global log into query-shaped state. Each has a name and a version; a checkpoint per
// (name, version) records how far into the log it has read.
// ---------------------------------------------------------------------------------------------------------------------

interface Projection {
    String name();

    int version();

    /** Apply one event. Must be safe to call twice with the same envelope (the projector may redeliver after a crash). */
    void apply(EventEnvelope envelope);
}

/** Durable "how far has this projection read": one position per (name, version). In production a row next to the view's tables. */
final class CheckpointStore {
    private final Map<String, Long> positions = new HashMap<>();

    static String key(Projection p) { return p.name() + "@v" + p.version(); }

    long position(Projection p) { return positions.getOrDefault(key(p), 0L); }

    void save(Projection p, long position) {
        if (position < position(p)) throw new IllegalStateException("checkpoint for " + key(p) + " would move backwards");
        positions.put(key(p), position);
    }

    void reset(Projection p) { positions.remove(key(p)); }
}

/** Thrown by the fault injector to simulate a process dying between applying events and saving the checkpoint. */
final class CrashException extends RuntimeException {
    CrashException(String message) { super(message); }
}

/**
 * Drives one projection: read from its checkpoint, apply, save the checkpoint. If the process dies after apply and
 * before save (crashAfterApplies), the next run re-applies the same events, so apply must be idempotent.
 */
final class Projector {
    private final EventStore store;
    private final CheckpointStore checkpoints;
    private final Projection projection;
    private final int batchSize;
    private int crashAfterApplies = -1;
    private long lastRunAt = Long.MIN_VALUE;

    Projector(EventStore store, CheckpointStore checkpoints, Projection projection, int batchSize) {
        if (batchSize <= 0) throw new IllegalArgumentException("batchSize must be positive");
        this.store = store;
        this.checkpoints = checkpoints;
        this.projection = projection;
        this.batchSize = batchSize;
    }

    Projection projection() { return projection; }

    long position() { return checkpoints.position(projection); }

    long lag() { return store.lastPosition() - position(); }

    boolean caughtUp() { return lag() == 0; }

    void crashAfterApplies(int n) { this.crashAfterApplies = n; }

    /** One batch: returns how many events were applied. Call repeatedly (or use catchUp) to drain the log. */
    int runOnce(long now) {
        lastRunAt = now;
        List<EventEnvelope> batch = store.readAll(position(), batchSize);
        int applied = 0;
        for (EventEnvelope env : batch) {
            projection.apply(env);
            applied++;
            if (crashAfterApplies >= 0 && applied >= crashAfterApplies) {
                crashAfterApplies = -1;
                throw new CrashException("projector for " + CheckpointStore.key(projection) + " died after applying " + applied + " events, before saving the checkpoint");
            }
        }
        if (!batch.isEmpty()) checkpoints.save(projection, batch.get(batch.size() - 1).position());
        return applied;
    }

    /** Run batches until the projection has read everything. Returns total events applied. */
    int catchUp(long now) {
        int total = 0;
        while (!caughtUp()) total += runOnce(now);
        return total;
    }

    long lastRunAt() { return lastRunAt; }
}

/** Read model 1: current balance per account. Naive version: applies whatever it is given. */
class BalanceView implements Projection {
    protected final Map<String, Money> balances = new TreeMap<>();
    protected final Map<String, String> owners = new HashMap<>();

    @Override
    public String name() { return "balances"; }

    @Override
    public int version() { return 1; }

    @Override
    public void apply(EventEnvelope env) {
        switch (env.event()) {
            case AccountOpened e -> {
                balances.put(e.accountId(), Money.zero(e.currency()));
                owners.put(e.accountId(), e.owner());
            }
            case Deposited e -> balances.merge(env.streamId(), e.amount(), Money::plus);
            case Withdrawn e -> balances.put(env.streamId(), balances.get(env.streamId()).minus(e.amount()));
        }
    }

    Money balance(String accountId) { return balances.get(accountId); }

    String owner(String accountId) { return owners.get(accountId); }

    Map<String, Money> all() { return Collections.unmodifiableMap(balances); }
}

/**
 * Read model 1, done right: each row remembers the last position applied to it and ignores anything at or below it.
 * This is what makes redelivery after a crash harmless.
 */
final class IdempotentBalanceView extends BalanceView {
    private final Map<String, Long> appliedThrough = new HashMap<>();
    private int skipped = 0;

    @Override
    public int version() { return 2; }

    @Override
    public void apply(EventEnvelope env) {
        long seen = appliedThrough.getOrDefault(env.streamId(), 0L);
        if (env.position() <= seen) {
            skipped++;
            return;
        }
        super.apply(env);
        appliedThrough.put(env.streamId(), env.position());
    }

    int skipped() { return skipped; }
}

/** Read model 2: activity per day, which the write model has no reason to know about. */
final class DailyActivityView implements Projection {
    record Day(int deposits, int withdrawals, Money net) {}

    private final TreeMap<Long, Day> days = new TreeMap<>();
    private long appliedThrough = 0;
    private final Currency currency;

    DailyActivityView(Currency currency) { this.currency = currency; }

    static long dayOf(long millis) { return millis / 86_400_000L; }

    @Override
    public String name() { return "daily-activity"; }

    @Override
    public int version() { return 1; }

    @Override
    public void apply(EventEnvelope env) {
        if (env.position() <= appliedThrough) return;
        appliedThrough = env.position();
        long day = dayOf(env.recordedAt());
        Day current = days.getOrDefault(day, new Day(0, 0, Money.zero(currency)));
        switch (env.event()) {
            case Deposited e -> days.put(day, new Day(current.deposits() + 1, current.withdrawals(), current.net().plus(e.amount())));
            case Withdrawn e -> days.put(day, new Day(current.deposits(), current.withdrawals() + 1, current.net().minus(e.amount())));
            case AccountOpened e -> { }
        }
    }

    Day day(long day) { return days.get(day); }

    Map<Long, Day> all() { return Collections.unmodifiableMap(days); }
}

/**
 * Read model 3: withdrawals at or above a threshold, for a compliance screen. Version 1 reports the account id and
 * amount above 1000.00; version 2 lowers the threshold to 500.00 and adds the owner name, which requires having seen
 * the AccountOpened event. Changing either is a new version, rebuilt from position 0.
 */
final class LargeWithdrawalsView implements Projection {
    record Alert(long position, String accountId, String owner, Money amount, String reference) {}

    private final int version;
    private final Money threshold;
    private final Map<String, String> owners = new HashMap<>();
    private final List<Alert> alerts = new ArrayList<>();
    private long appliedThrough = 0;

    LargeWithdrawalsView(int version, Money threshold) {
        this.version = version;
        this.threshold = threshold;
    }

    @Override
    public String name() { return "large-withdrawals"; }

    @Override
    public int version() { return version; }

    @Override
    public void apply(EventEnvelope env) {
        if (env.position() <= appliedThrough) return;
        appliedThrough = env.position();
        switch (env.event()) {
            case AccountOpened e -> owners.put(e.accountId(), e.owner());
            case Withdrawn e -> {
                if (e.amount().compareTo(threshold) >= 0) {
                    String owner = version >= 2 ? owners.getOrDefault(env.streamId(), "?") : null;
                    alerts.add(new Alert(env.position(), env.streamId(), owner, e.amount(), e.reference()));
                }
            }
            case Deposited e -> { }
        }
    }

    List<Alert> alerts() { return Collections.unmodifiableList(alerts); }
}

/**
 * The query side's front door: by projection name, which version is live. A new version is registered, caught up in
 * the background, and promoted only once it has read the whole log; queries never see a half-built view.
 */
final class ReadSide {
    private final Map<String, Projector> live = new LinkedHashMap<>();
    private final Map<String, Projector> building = new LinkedHashMap<>();

    void register(Projector projector) {
        String name = projector.projection().name();
        if (live.containsKey(name)) building.put(name, projector); else live.put(name, projector);
    }

    Projector live(String name) {
        Projector p = live.get(name);
        if (p == null) throw new IllegalArgumentException("no live projection named " + name);
        return p;
    }

    Projector building(String name) { return building.get(name); }

    /** Promote the building version to live. Refuses while it is behind the log. Returns the retired projector. */
    Projector promote(String name) {
        Projector next = building.get(name);
        if (next == null) throw new IllegalStateException("nothing is being built for " + name);
        if (!next.caughtUp()) throw new IllegalStateException(CheckpointStore.key(next.projection()) + " is " + next.lag() + " events behind; not promoting");
        Projector old = live.put(name, next);
        building.remove(name);
        return old;
    }

    /** Block until the live view has read at least {@code position}: read-your-own-writes for one request. */
    boolean waitFor(String name, long position, long now, int maxRuns) {
        Projector p = live(name);
        for (int i = 0; i < maxRuns && p.position() < position; i++) p.runOnce(now);
        return p.position() >= position;
    }
}

/** Measures the eventual-consistency window: for each event, projectedAt - recordedAt, under a polling projector. */
final class ConsistencyProbe {
    record Window(int events, long min, long mean, long max) {}

    /**
     * Simulate {@code commands} commands issued one every {@code commandGapMillis}, with a projector that runs every
     * {@code pollIntervalMillis}. Returns the staleness distribution. Deterministic: driven by the SimClock only.
     */
    static Window run(int commands, long commandGapMillis, long pollIntervalMillis, int batchSize) {
        SimClock clock = new SimClock(1_700_000_000_000L);
        EventStore store = new EventStore();
        CommandHandler handler = new CommandHandler(store, clock);
        IdempotentBalanceView view = new IdempotentBalanceView();
        Projector projector = new Projector(store, new CheckpointStore(), view, batchSize);
        handler.handle("acct-probe", new OpenAccount("acct-probe", "Probe", Currency.USD));
        projector.catchUp(clock.now());
        Map<Long, Long> recordedAt = new HashMap<>();
        List<Long> staleness = new ArrayList<>();
        long nextCommand = clock.now(), nextPoll = clock.now() + pollIntervalMillis;
        int issued = 0;
        while (issued < commands || !projector.caughtUp()) {
            if (issued < commands && clock.now() >= nextCommand) {
                long pos = handler.handle("acct-probe", new Deposit(Money.ofMinor(100 + issued, Currency.USD), "d-" + issued));
                recordedAt.put(pos, clock.now());
                issued++;
                nextCommand += commandGapMillis;
            }
            if (clock.now() >= nextPoll) {
                long before = projector.position();
                projector.runOnce(clock.now());
                for (long p = before + 1; p <= projector.position(); p++) staleness.add(clock.now() - recordedAt.get(p));
                nextPoll += pollIntervalMillis;
            }
            clock.advance(1);
        }
        long min = Long.MAX_VALUE, max = Long.MIN_VALUE, sum = 0;
        for (long s : staleness) { min = Math.min(min, s); max = Math.max(max, s); sum += s; }
        return new Window(staleness.size(), staleness.isEmpty() ? 0 : min, staleness.isEmpty() ? 0 : sum / staleness.size(), staleness.isEmpty() ? 0 : max);
    }
}

public final class Main {
    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    public static void main(String[] args) {
        SimClock clock = new SimClock(1_700_000_000_000L);
        EventStore store = new EventStore();
        CommandHandler commands = new CommandHandler(store, clock);
        CheckpointStore checkpoints = new CheckpointStore();

        System.out.println("== 1. Command side: validate against the write model, append events ==");
        commands.handle("acct-alice", new OpenAccount("acct-alice", "Alice Ng", Currency.USD));
        commands.handle("acct-bob", new OpenAccount("acct-bob", "Bob Osei", Currency.USD));
        commands.handle("acct-alice", new Deposit(usd(250_000), "payroll-oct"));
        commands.handle("acct-bob", new Deposit(usd(80_000), "refund-4471"));
        clock.advance(3_600_000);
        commands.handle("acct-alice", new Withdraw(usd(120_000), "rent"));
        commands.handle("acct-bob", new Withdraw(usd(60_000), "card-9921"));
        clock.advance(86_400_000);
        commands.handle("acct-alice", new Withdraw(usd(75_000), "tuition"));
        long lastWrite = commands.handle("acct-bob", new Deposit(usd(15_000), "cashback"));
        try {
            commands.handle("acct-bob", new Withdraw(usd(99_999), "overdraft-attempt"));
        } catch (CommandRejectedException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        System.out.printf("global log has %d events; acct-alice at version %d, acct-bob at version %d%n", store.lastPosition(), store.version("acct-alice"), store.version("acct-bob"));
        for (EventEnvelope env : store.readAll(0, 100)) {
            System.out.printf("  pos %d  %-10s v%d  %s%n", env.position(), env.streamId(), env.version(), env.event());
        }

        System.out.println("\n== 2. Read side: three projections fold the same log into three shapes ==");
        ReadSide readSide = new ReadSide();
        Projector balances = new Projector(store, checkpoints, new IdempotentBalanceView(), 3);
        Projector daily = new Projector(store, checkpoints, new DailyActivityView(Currency.USD), 3);
        Projector large = new Projector(store, checkpoints, new LargeWithdrawalsView(1, usd(100_000)), 3);
        for (Projector p : List.of(balances, daily, large)) {
            readSide.register(p);
            int applied = p.catchUp(clock.now());
            System.out.printf("%-22s applied %d events, checkpoint at position %d%n", CheckpointStore.key(p.projection()), applied, p.position());
        }
        IdempotentBalanceView balanceView = (IdempotentBalanceView) readSide.live("balances").projection();
        System.out.println("balances: " + balanceView.all());
        DailyActivityView dailyView = (DailyActivityView) readSide.live("daily-activity").projection();
        for (Map.Entry<Long, DailyActivityView.Day> e : dailyView.all().entrySet()) System.out.printf("day %d: %s%n", e.getKey(), e.getValue());
        LargeWithdrawalsView largeV1 = (LargeWithdrawalsView) readSide.live("large-withdrawals").projection();
        System.out.println("large withdrawals v1 (>= 1000.00): " + largeV1.alerts());

        System.out.println("\n== 3. New events arrive; projections are behind until they poll ==");
        clock.advance(500);
        long pos = commands.handle("acct-alice", new Deposit(usd(5_000), "interest"));
        System.out.printf("wrote position %d; balances projection is at %d (lag %d): a read now returns %s%n",
                pos, balances.position(), balances.lag(), balanceView.balance("acct-alice"));
        boolean ok = readSide.waitFor("balances", pos, clock.now(), 10);
        System.out.printf("waitFor(position %d) -> %b; a read now returns %s%n", pos, ok, balanceView.balance("acct-alice"));

        System.out.println("\n== 4. Projection versioning: rebuild v2 from position 0, promote when caught up ==");
        Projector largeV2 = new Projector(store, checkpoints, new LargeWithdrawalsView(2, usd(50_000)), 4);
        readSide.register(largeV2);
        System.out.printf("v2 registered; checkpoint %s = %d, live is still v%d%n", CheckpointStore.key(largeV2.projection()), largeV2.position(), readSide.live("large-withdrawals").projection().version());
        largeV2.runOnce(clock.now());
        try {
            readSide.promote("large-withdrawals");
        } catch (IllegalStateException e) {
            System.out.println("promote refused: " + e.getMessage());
        }
        largeV2.catchUp(clock.now());
        Projector retired = readSide.promote("large-withdrawals");
        System.out.printf("promoted v%d; retired v%d (its checkpoint stays at %d until it is deleted)%n",
                readSide.live("large-withdrawals").projection().version(), retired.projection().version(), retired.position());
        for (LargeWithdrawalsView.Alert a : ((LargeWithdrawalsView) largeV2.projection()).alerts()) System.out.println("  " + a);

        System.out.println("\n== 5. Crash between apply and checkpoint: naive vs idempotent projection ==");
        for (BalanceView view : List.of(new BalanceView(), new IdempotentBalanceView())) {
            Projector p = new Projector(store, new CheckpointStore(), view, 4);
            p.runOnce(clock.now());
            p.crashAfterApplies(2);
            try {
                p.runOnce(clock.now());
            } catch (CrashException e) {
                System.out.println(view.getClass().getSimpleName() + ": " + e.getMessage());
            }
            p.catchUp(clock.now());
            System.out.printf("  after restart: alice=%s bob=%s (truth: alice=%s bob=%s)%n",
                    view.balance("acct-alice"), view.balance("acct-bob"), balanceView.balance("acct-alice"), balanceView.balance("acct-bob"));
        }

        System.out.println("\n== 6. The eventual-consistency window, measured ==");
        for (long poll : new long[] {50, 10, 1}) {
            ConsistencyProbe.Window w = ConsistencyProbe.run(200, 7, poll, 100);
            System.out.printf("200 commands every 7 ms, projector polls every %2d ms: staleness min=%d mean=%d max=%d ms over %d events%n",
                    poll, w.min(), w.mean(), w.max(), w.events());
        }
        ConsistencyProbe.Window small = ConsistencyProbe.run(200, 7, 10, 1);
        System.out.printf("same, polling every 10 ms but batchSize=1 (projector slower than the producer): staleness mean=%d max=%d ms%n", small.mean(), small.max());
        System.out.printf("%nlast write position in the main store: %d; every live projection is at %d%n", store.lastPosition(), readSide.live("balances").position());
        if (lastWrite <= 0) throw new IllegalStateException("unreachable");
    }
}
