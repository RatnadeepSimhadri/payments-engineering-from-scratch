// Lesson: phases/08-messaging-and-streams/05-event-sourcing/docs/en.md
// Event Sourcing: State as a Fold Over Events
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Fowler, "Event Sourcing" (martinfowler.com, 2005); Young, "CQRS Documents" (2010) on expected-version
//          concurrency; EventStoreDB docs on optimistic concurrency and snapshots. Money follows Phase 10, Lesson 01.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.TreeMap;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency JPY = new Currency("JPY", 0);

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
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Events: facts that happened. Past tense, immutable, never validated (validation happened before they were written).
// ---------------------------------------------------------------------------------------------------------------------

sealed interface Event permits Opened, Deposited, Withdrawn, Held, Released {}

record Opened(String accountId, Currency currency) implements Event {}

record Deposited(Money amount) implements Event {}

record Withdrawn(Money amount) implements Event {}

record Held(String holdId, Money amount) implements Event {}

record Released(String holdId) implements Event {}

// ---------------------------------------------------------------------------------------------------------------------
// Commands: requests that may be refused. Imperative mood.
// ---------------------------------------------------------------------------------------------------------------------

sealed interface Command permits OpenAccount, Deposit, Withdraw, PlaceHold, ReleaseHold {}

record OpenAccount(String accountId, Currency currency) implements Command {}

record Deposit(Money amount) implements Command {}

record Withdraw(Money amount) implements Command {}

record PlaceHold(String holdId, Money amount) implements Command {}

record ReleaseHold(String holdId) implements Command {}

final class CommandRejectedException extends RuntimeException {
    CommandRejectedException(String message) { super(message); }
}

final class ConcurrencyException extends RuntimeException {
    final long expected;
    final long actual;

    ConcurrencyException(String streamId, long expected, long actual) {
        super("stream " + streamId + ": expected version " + expected + " but found " + actual);
        this.expected = expected;
        this.actual = actual;
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The aggregate: state is a pure function of the event sequence. apply() has no branches that can fail.
// ---------------------------------------------------------------------------------------------------------------------

/** Immutable account state. version = number of events applied so far. */
record AccountState(String accountId, Currency currency, Money balance, Map<String, Money> holds, long version) {

    static AccountState empty() { return new AccountState(null, null, null, Map.of(), 0); }

    boolean isOpen() { return accountId != null; }

    /** Balance minus every active hold: what a withdrawal or new hold may use. */
    Money available() {
        Money held = Money.zero(currency);
        for (Money m : holds.values()) held = held.plus(m);
        return balance.minus(held);
    }

    /** The fold step. Total: every event type is handled, every result is a new state with version + 1. */
    AccountState apply(Event event) {
        long next = version + 1;
        return switch (event) {
            case Opened e -> new AccountState(e.accountId(), e.currency(), Money.zero(e.currency()), Map.of(), next);
            case Deposited e -> new AccountState(accountId, currency, balance.plus(e.amount()), holds, next);
            case Withdrawn e -> new AccountState(accountId, currency, balance.minus(e.amount()), holds, next);
            case Held e -> {
                Map<String, Money> h = new LinkedHashMap<>(holds);
                h.put(e.holdId(), e.amount());
                yield new AccountState(accountId, currency, balance, Collections.unmodifiableMap(h), next);
            }
            case Released e -> {
                Map<String, Money> h = new LinkedHashMap<>(holds);
                h.remove(e.holdId());   // the balance never moved; only the available amount changes
                yield new AccountState(accountId, currency, balance, Collections.unmodifiableMap(h), next);
            }
        };
    }

    /** Fold a whole history from the empty state. replay(events) == replay(prefix).applyAll(suffix). */
    static AccountState replay(List<Event> events) { return empty().applyAll(events); }

    AccountState applyAll(List<Event> events) {
        AccountState s = this;
        for (Event e : events) s = s.apply(e);
        return s;
    }

    /**
     * Decide: validate a command against the current state and return the events it produces. This is the only place
     * business rules live. It never mutates anything; the caller appends the events and folds them.
     */
    List<Event> decide(Command command) {
        return switch (command) {
            case OpenAccount c -> {
                if (isOpen()) throw new CommandRejectedException("account " + accountId + " is already open");
                yield List.of(new Opened(c.accountId(), c.currency()));
            }
            case Deposit c -> {
                requireOpen();
                requirePositiveSameCurrency(c.amount());
                yield List.of(new Deposited(c.amount()));
            }
            case Withdraw c -> {
                requireOpen();
                requirePositiveSameCurrency(c.amount());
                if (c.amount().compareTo(available()) > 0) {
                    throw new CommandRejectedException("insufficient available funds: " + available() + " < " + c.amount());
                }
                yield List.of(new Withdrawn(c.amount()));
            }
            case PlaceHold c -> {
                requireOpen();
                requirePositiveSameCurrency(c.amount());
                if (holds.containsKey(c.holdId())) throw new CommandRejectedException("hold " + c.holdId() + " already exists");
                if (c.amount().compareTo(available()) > 0) {
                    throw new CommandRejectedException("insufficient available funds for hold: " + available() + " < " + c.amount());
                }
                yield List.of(new Held(c.holdId(), c.amount()));
            }
            case ReleaseHold c -> {
                requireOpen();
                if (!holds.containsKey(c.holdId())) throw new CommandRejectedException("no such hold: " + c.holdId());
                yield List.of(new Released(c.holdId()));
            }
        };
    }

    private void requireOpen() {
        if (!isOpen()) throw new CommandRejectedException("account is not open");
    }

    private void requirePositiveSameCurrency(Money amount) {
        if (!amount.isPositive()) throw new CommandRejectedException("amount must be positive: " + amount);
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Storage: an append-only stream per aggregate with optimistic concurrency, plus a snapshot store.
// ---------------------------------------------------------------------------------------------------------------------

/** An event with the version it occupies in its stream (1-based: the first event of a stream is version 1). */
record StoredEvent(long version, Event event) {}

/** In-memory event store. One list per stream id; append checks the expected version. */
final class EventStore {
    private final Map<String, List<StoredEvent>> streams = new HashMap<>();
    private final List<StoredEvent> all = new ArrayList<>();

    /** Current version of a stream (= number of events). 0 for a stream that does not exist. */
    long version(String streamId) { return streams.getOrDefault(streamId, List.of()).size(); }

    /**
     * Append events if and only if the stream is at expectedVersion. Returns the new version. The check-and-append is
     * one step, so two writers that both loaded version 3 cannot both succeed.
     */
    long append(String streamId, long expectedVersion, List<Event> events) {
        List<StoredEvent> stream = streams.computeIfAbsent(streamId, k -> new ArrayList<>());
        if (stream.size() != expectedVersion) throw new ConcurrencyException(streamId, expectedVersion, stream.size());
        for (Event e : events) {
            StoredEvent stored = new StoredEvent(stream.size() + 1, e);
            stream.add(stored);
            all.add(stored);
        }
        return stream.size();
    }

    /** Events with version > afterVersion, in order. read(id, 0) is the whole stream. */
    List<Event> read(String streamId, long afterVersion) {
        List<Event> out = new ArrayList<>();
        for (StoredEvent s : streams.getOrDefault(streamId, List.of())) {
            if (s.version() > afterVersion) out.add(s.event());
        }
        return out;
    }

    int totalEvents() { return all.size(); }
}

record Snapshot(AccountState state, long version) {}

/** Latest snapshot per stream. A snapshot is a cache of a fold prefix; losing it costs time, never correctness. */
final class SnapshotStore {
    private final Map<String, Snapshot> latest = new HashMap<>();
    private int writes = 0;

    void save(String streamId, AccountState state) {
        latest.put(streamId, new Snapshot(state, state.version()));
        writes++;
    }

    Optional<Snapshot> load(String streamId) { return Optional.ofNullable(latest.get(streamId)); }

    int writes() { return writes; }
}

/**
 * The command side: load (snapshot + tail), decide, append with expected version, snapshot every N events.
 */
final class AccountService {
    private final EventStore events;
    private final SnapshotStore snapshots;
    private final int snapshotEvery;
    private int eventsReplayedOnLastLoad = 0;

    AccountService(EventStore events, SnapshotStore snapshots, int snapshotEvery) {
        if (snapshotEvery <= 0) throw new IllegalArgumentException("snapshotEvery must be positive");
        this.events = events;
        this.snapshots = snapshots;
        this.snapshotEvery = snapshotEvery;
    }

    /** Current state: start from the latest snapshot (or empty) and fold the events after it. */
    AccountState load(String streamId) {
        Optional<Snapshot> snap = snapshots.load(streamId);
        AccountState base = snap.map(Snapshot::state).orElse(AccountState.empty());
        List<Event> tail = events.read(streamId, base.version());
        eventsReplayedOnLastLoad = tail.size();
        return base.applyAll(tail);
    }

    /** Full replay from version 0, ignoring snapshots. Used by tests and by "rebuild everything" tooling. */
    AccountState replayFromScratch(String streamId) { return AccountState.replay(events.read(streamId, 0)); }

    int eventsReplayedOnLastLoad() { return eventsReplayedOnLastLoad; }

    /**
     * Handle a command against the version the caller saw. If another writer got in first, ConcurrencyException: the
     * caller reloads, re-decides against the new state, and retries (or gives up). Returns the new state.
     */
    AccountState handle(String streamId, Command command, long expectedVersion) {
        AccountState current = load(streamId);
        if (current.version() != expectedVersion) throw new ConcurrencyException(streamId, expectedVersion, current.version());
        List<Event> produced = current.decide(command);
        long newVersion = events.append(streamId, expectedVersion, produced);
        AccountState next = current.applyAll(produced);
        if (newVersion / snapshotEvery > expectedVersion / snapshotEvery) snapshots.save(streamId, next);
        return next;
    }

    /** Convenience for callers that always want the latest state: load, then handle at that version. */
    AccountState handle(String streamId, Command command) {
        return handle(streamId, command, load(streamId).version());
    }
}

public final class Main {
    static Money usd(long cents) { return Money.ofMinor(cents, Currency.USD); }

    public static void main(String[] args) {
        EventStore store = new EventStore();
        SnapshotStore snapshots = new SnapshotStore();
        AccountService service = new AccountService(store, snapshots, 4);
        String acct = "acct-42";

        System.out.println("== Commands become events; state is the fold ==");
        AccountState s = service.handle(acct, new OpenAccount(acct, Currency.USD));
        print(s, "opened");
        s = service.handle(acct, new Deposit(usd(10_000)));
        print(s, "deposited 100.00");
        s = service.handle(acct, new PlaceHold("auth-1", usd(2_500)));
        print(s, "held 25.00 for auth-1");
        s = service.handle(acct, new Withdraw(usd(6_000)));
        print(s, "withdrew 60.00");

        System.out.println("\n== Rejected commands produce no events ==");
        try {
            service.handle(acct, new Withdraw(usd(2_000)));
        } catch (CommandRejectedException e) {
            System.out.println("withdraw 20.00 rejected: " + e.getMessage() + " (version still " + store.version(acct) + ")");
        }
        try {
            service.handle(acct, new ReleaseHold("auth-9"));
        } catch (CommandRejectedException e) {
            System.out.println("release auth-9 rejected: " + e.getMessage());
        }
        try {
            service.handle(acct, new Deposit(Money.ofMinor(500, Currency.EUR)));
        } catch (CurrencyMismatchException e) {
            System.out.println("deposit 5.00 EUR rejected: " + e.getMessage());
        }

        System.out.println("\n== Optimistic concurrency: two writers load version 4 ==");
        long seen = store.version(acct);
        AccountState a = service.handle(acct, new ReleaseHold("auth-1"), seen);
        System.out.println("writer A released auth-1 at expected version " + seen + " -> version " + a.version() + ", available " + a.available());
        try {
            service.handle(acct, new Withdraw(usd(4_000)), seen);
        } catch (ConcurrencyException e) {
            System.out.println("writer B withdraw 40.00 at expected version " + seen + " rejected: " + e.getMessage());
        }
        AccountState b = service.handle(acct, new Withdraw(usd(4_000)));
        System.out.println("writer B reloaded and retried -> version " + b.version() + ", balance " + b.balance());

        System.out.println("\n== Snapshots: every 4 events, load folds only the tail ==");
        System.out.println("snapshot writes so far: " + snapshots.writes() + " (versions 4)");
        service.handle(acct, new Deposit(usd(1_000)));
        service.handle(acct, new Deposit(usd(1_000)));
        System.out.println("after 8 events: snapshot writes = " + snapshots.writes() + ", latest snapshot version = "
                + snapshots.load(acct).map(Snapshot::version).orElse(-1L));
        service.handle(acct, new Deposit(usd(1_000)));
        AccountState loaded = service.load(acct);
        System.out.println("load at version " + loaded.version() + " replayed " + service.eventsReplayedOnLastLoad()
                + " event(s) on top of the snapshot; balance " + loaded.balance());
        AccountState full = service.replayFromScratch(acct);
        System.out.println("full replay from version 0 gives the same state: " + loaded.equals(full));

        System.out.println("\n== The stream as stored ==");
        long v = 1;
        for (Event e : store.read(acct, 0)) System.out.printf("v%-2d %s%n", v++, e);
        System.out.println("balance is derived: " + full.balance() + " = sum of deposits - sum of withdrawals; available = " + full.available());
    }

    private static void print(AccountState s, String what) {
        System.out.printf("%-22s v%d balance %s available %s holds %s%n", what, s.version(), s.balance(), s.available(), s.holds().keySet());
    }
}
