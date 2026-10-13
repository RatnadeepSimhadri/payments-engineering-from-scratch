// Lesson: phases/07-storage-and-transactions/08-two-phase-commit/docs/en.md
// Two-Phase Commit and Its Blocking Problem
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Gray, "Notes on Data Base Operating Systems" (1978); Mohan, Lindsay, Obermarck,
// "Transaction Management in the R* Distributed Database Management System" (1986); X/Open XA spec.
// Java 21, standard library only. Money is integer minor units (Phase 10, Lesson 01).

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;

/** ISO 4217 currency, copied from Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

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

/** Exact amount of one currency in signed minor units. Copied from Phase 10 Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    boolean isNegative() { return minorUnits < 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

/** A clock that only moves when the simulation says so. */
final class SimClock {
    private long nowMs;
    SimClock(long startMs) { this.nowMs = startMs; }
    long now() { return nowMs; }
    void advance(long ms) { nowMs += ms; }
}

enum Vote { YES, NO }
enum Decision { COMMIT, ABORT }
/** What a participant knows about a transaction. PREPARED is the in-doubt state. */
enum TxState { NONE, PREPARED, COMMITTED, ABORTED }

/** One account change on one participant. A negative delta is a debit. */
record Posting(String account, Money delta) { }

/** One durable log record. Both participants and the coordinator write these; both replay them. */
record LogRecord(String txId, String kind, String detail) {
    @Override public String toString() { return txId + " " + kind + (detail.isEmpty() ? "" : " " + detail); }
}

/** Append-only durable log. Append is the durability point: a record in the list survives a crash. */
final class DurableLog {
    private final List<LogRecord> records = new ArrayList<>();
    void append(String txId, String kind, String detail) { records.add(new LogRecord(txId, kind, detail)); }
    List<LogRecord> records() { return List.copyOf(records); }
    int size() { return records.size(); }
}

/** Thrown by an injected crash point. The simulation catches it where the real process would die. */
final class CrashException extends RuntimeException {
    CrashException(String where) { super("crash at " + where); }
}

/** Where the coordinator dies, if anywhere. The two interesting points sit on either side of the decision record. */
enum CrashPoint { NONE, AFTER_VOTES_BEFORE_DECISION_LOG, AFTER_DECISION_LOG_BEFORE_SEND }

/**
 * A participant: one ledger with its own balances, locks, and log.
 * prepare() is a promise: once it has voted YES it can no longer change its mind, so it must
 * hold the lock and keep the posting durable until the coordinator tells it the decision.
 */
final class Ledger {
    final String name;
    private final Map<String, Money> initialBalances;
    private final Map<String, Money> balances = new LinkedHashMap<>();
    private final Map<String, Posting> prepared = new LinkedHashMap<>();   // txId -> posting held in doubt
    private final Map<String, TxState> states = new HashMap<>();
    private final Set<String> lockedAccounts = new HashSet<>();
    private final DurableLog log;

    Ledger(String name, Map<String, Money> openingBalances) {
        this(name, openingBalances, new DurableLog());
    }

    private Ledger(String name, Map<String, Money> openingBalances, DurableLog log) {
        this.name = name;
        this.initialBalances = Map.copyOf(openingBalances);
        this.balances.putAll(openingBalances);
        this.log = log;
    }

    Money balance(String account) { return balances.get(account); }
    TxState stateOf(String txId) { return states.getOrDefault(txId, TxState.NONE); }
    boolean isLocked(String account) { return lockedAccounts.contains(account); }
    List<LogRecord> logRecords() { return log.records(); }
    /** The in-doubt set: prepared and waiting. Non-empty means this ledger is blocked on the coordinator. */
    Set<String> inDoubt() { return Set.copyOf(prepared.keySet()); }

    /** Phase 1. Validate, lock, log PREPARED, then vote. The log write comes before the vote leaves. */
    Vote prepare(String txId, Posting posting) {
        if (states.get(txId) == TxState.PREPARED) return Vote.YES;      // duplicate prepare: same answer
        if (states.containsKey(txId)) return Vote.NO;                     // already decided: too late
        Money current = balances.get(posting.account());
        if (current == null || lockedAccounts.contains(posting.account())) {
            log.append(txId, "ABORTED", "vote NO: " + (current == null ? "unknown account" : "account locked"));
            states.put(txId, TxState.ABORTED);
            return Vote.NO;
        }
        if (current.plus(posting.delta()).isNegative()) {
            log.append(txId, "ABORTED", "vote NO: insufficient funds in " + posting.account());
            states.put(txId, TxState.ABORTED);
            return Vote.NO;
        }
        lockedAccounts.add(posting.account());
        prepared.put(txId, posting);
        states.put(txId, TxState.PREPARED);
        log.append(txId, "PREPARED", posting.account() + " " + posting.delta());
        return Vote.YES;
    }

    /** Phase 2, commit. Idempotent: a second COMMIT for the same tx is acknowledged, not re-applied. */
    void commit(String txId) {
        TxState state = stateOf(txId);
        if (state == TxState.COMMITTED) return;
        if (state != TxState.PREPARED) throw new IllegalStateException(name + ": commit of " + txId + " in state " + state);
        Posting p = prepared.remove(txId);
        balances.put(p.account(), balances.get(p.account()).plus(p.delta()));
        lockedAccounts.remove(p.account());
        states.put(txId, TxState.COMMITTED);
        log.append(txId, "COMMITTED", "");
    }

    /** Phase 2, abort. Idempotent, and legal from NONE (the coordinator may abort a tx we never prepared). */
    void abort(String txId) {
        TxState state = stateOf(txId);
        if (state == TxState.ABORTED) return;
        if (state == TxState.COMMITTED) throw new IllegalStateException(name + ": cannot abort committed " + txId);
        Posting p = prepared.remove(txId);
        if (p != null) lockedAccounts.remove(p.account());
        states.put(txId, TxState.ABORTED);
        log.append(txId, "ABORTED", "");
    }

    /**
     * Lose all memory, then rebuild from the log. PREPARED records with no later decision come back
     * as in-doubt: locks re-taken, posting held. The participant cannot resolve them on its own.
     */
    Ledger crashAndRecover() {
        Ledger fresh = new Ledger(name, initialBalances, log);
        Map<String, Posting> pending = new LinkedHashMap<>();
        for (LogRecord r : log.records()) {
            switch (r.kind()) {
                case "PREPARED" -> {
                    String[] parts = r.detail().split(" ");
                    Money delta = parseMoney(parts[1], parts[2]);
                    pending.put(r.txId(), new Posting(parts[0], delta));
                    fresh.states.put(r.txId(), TxState.PREPARED);
                }
                case "COMMITTED" -> {
                    Posting p = pending.remove(r.txId());
                    fresh.balances.put(p.account(), fresh.balances.get(p.account()).plus(p.delta()));
                    fresh.states.put(r.txId(), TxState.COMMITTED);
                }
                case "ABORTED" -> {
                    pending.remove(r.txId());
                    fresh.states.put(r.txId(), TxState.ABORTED);
                }
                default -> throw new IllegalStateException("unknown record " + r);
            }
        }
        for (Map.Entry<String, Posting> e : pending.entrySet()) {
            fresh.prepared.put(e.getKey(), e.getValue());
            fresh.lockedAccounts.add(e.getValue().account());
        }
        return fresh;
    }

    private static Money parseMoney(String amount, String code) {
        boolean negative = amount.startsWith("-");
        String s = negative ? amount.substring(1) : amount;
        String digits = s.replace(".", "");
        long minor = Long.parseLong(digits);
        return Money.ofMinor(negative ? -minor : minor, new Currency(code, s.contains(".") ? s.length() - s.indexOf('.') - 1 : 0));
    }

    Money total(Currency currency) {
        Money sum = Money.ofMinor(0, currency);
        for (Money m : balances.values()) sum = sum.plus(m);
        return sum;
    }
}

/**
 * Deterministic network between the coordinator and its participants. A partitioned node never
 * answers; the caller waits out the timeout and the clock advances. Every call is counted.
 */
final class SimNetwork {
    private final Map<String, Ledger> nodes = new LinkedHashMap<>();
    private final Set<String> partitioned = new HashSet<>();
    private final Set<String> crashAfterVote = new HashSet<>();
    private final SimClock clock;
    final long latencyMs;
    final long timeoutMs;
    int messagesSent = 0;
    int timeouts = 0;

    SimNetwork(SimClock clock, long latencyMs, long timeoutMs) {
        this.clock = clock; this.latencyMs = latencyMs; this.timeoutMs = timeoutMs;
    }

    void register(Ledger ledger) { nodes.put(ledger.name, ledger); }
    /** Replace a node after it crashed and recovered: the new object has the same name. */
    void replace(Ledger recovered) { nodes.put(recovered.name, recovered); }
    void partition(String node) { partitioned.add(node); }
    void heal(String node) { partitioned.remove(node); }
    /** The node answers the next prepare and then goes dark: a participant that crashes right after voting. */
    void crashAfterVote(String node) { crashAfterVote.add(node); }

    private boolean reachable(String node) {
        messagesSent++;
        if (partitioned.contains(node)) { clock.advance(timeoutMs); timeouts++; return false; }
        clock.advance(latencyMs);
        return true;
    }

    /** Empty means the request timed out; the coordinator must treat that as NO. */
    Optional<Vote> prepare(String node, String txId, Posting posting) {
        if (!reachable(node)) return Optional.empty();
        Vote vote = nodes.get(node).prepare(txId, posting);
        if (crashAfterVote.remove(node)) partitioned.add(node);
        return Optional.of(vote);
    }

    /** False means no acknowledgement arrived; the coordinator must retry later. */
    boolean decide(String node, String txId, Decision decision) {
        if (!reachable(node)) return false;
        if (decision == Decision.COMMIT) nodes.get(node).commit(txId); else nodes.get(node).abort(txId);
        return true;
    }
}

/** What the coordinator reports to the caller. BLOCKED means the coordinator itself died mid-protocol. */
record Outcome(String txId, Decision decision, boolean complete, String reason) {
    @Override public String toString() { return txId + " -> " + decision + (complete ? "" : " (incomplete: " + reason + ")"); }
}

/**
 * The coordinator. Its log is the only place the atomic decision lives. The commit point of the
 * whole protocol is the single append of a DECISION record; everything after that is delivery.
 */
final class Coordinator {
    private final DurableLog log = new DurableLog();
    private final SimNetwork net;
    private final Map<String, Map<String, Posting>> participants = new LinkedHashMap<>();   // txId -> node -> posting
    private final Map<String, Decision> decisions = new HashMap<>();
    private final Map<String, Set<String>> unacked = new LinkedHashMap<>();                 // txId -> nodes still to ack
    CrashPoint crashPoint = CrashPoint.NONE;

    Coordinator(SimNetwork net) { this.net = net; }

    List<LogRecord> logRecords() { return log.records(); }
    Set<String> pendingTransactions() { return Set.copyOf(unacked.keySet()); }
    Optional<Decision> decisionOf(String txId) { return Optional.ofNullable(decisions.get(txId)); }

    /** Move amount from one account on one ledger to another account on another ledger, atomically. */
    Outcome transfer(String txId, Ledger from, String fromAccount, Ledger to, String toAccount, Money amount) {
        Map<String, Posting> postings = new LinkedHashMap<>();
        postings.put(from.name, new Posting(fromAccount, amount.negate()));
        postings.put(to.name, new Posting(toAccount, amount));
        return run(txId, postings);
    }

    Outcome run(String txId, Map<String, Posting> postings) {
        participants.put(txId, postings);
        log.append(txId, "BEGIN", String.join(",", postings.keySet()));

        // Phase 1: collect votes. Any NO or any timeout means ABORT.
        Decision decision = Decision.COMMIT;
        String reason = "all voted YES";
        for (Map.Entry<String, Posting> e : postings.entrySet()) {
            Optional<Vote> vote = net.prepare(e.getKey(), txId, e.getValue());
            if (vote.isEmpty()) { decision = Decision.ABORT; reason = e.getKey() + " timed out in prepare"; break; }
            if (vote.get() == Vote.NO) { decision = Decision.ABORT; reason = e.getKey() + " voted NO"; break; }
        }

        if (crashPoint == CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG) throw new CrashException("after votes, before decision log");

        // The commit point. Before this append the outcome is undecided; after it, it is fixed forever.
        log.append(txId, "DECISION", decision.name());
        decisions.put(txId, decision);
        unacked.put(txId, new HashSet<>(postings.keySet()));

        if (crashPoint == CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND) throw new CrashException("after decision log, before send");

        // Phase 2: deliver the decision until every participant acknowledges.
        boolean complete = deliver(txId);
        return new Outcome(txId, decision, complete, complete ? reason : reason + "; awaiting acks from " + unacked.get(txId));
    }

    /** One delivery round: send the decision to every unacked participant; log END when none remain. */
    boolean deliver(String txId) {
        Decision decision = decisions.get(txId);
        Set<String> waiting = unacked.get(txId);
        if (waiting == null) return true;
        for (String node : new ArrayList<>(waiting)) {
            if (net.decide(node, txId, decision)) waiting.remove(node);
        }
        if (waiting.isEmpty()) { unacked.remove(txId); log.append(txId, "END", ""); return true; }
        return false;
    }

    /** Retry delivery for every transaction still waiting on acknowledgements. */
    int retryPending() {
        int finished = 0;
        for (String txId : new ArrayList<>(unacked.keySet())) if (deliver(txId)) finished++;
        return finished;
    }

    /**
     * Restart with nothing but the log. BEGIN without DECISION: presumed abort, because no commit
     * record exists, so no participant can have been told COMMIT. DECISION without END: redeliver.
     */
    static Coordinator recover(Coordinator crashed, SimNetwork net) {
        Coordinator fresh = new Coordinator(net);
        Map<String, List<String>> begun = new LinkedHashMap<>();
        Set<String> ended = new HashSet<>();
        for (LogRecord r : crashed.log.records()) {
            fresh.log.append(r.txId(), r.kind(), r.detail());
            switch (r.kind()) {
                case "BEGIN" -> begun.put(r.txId(), List.of(r.detail().split(",")));
                case "DECISION" -> fresh.decisions.put(r.txId(), Decision.valueOf(r.detail()));
                case "END" -> ended.add(r.txId());
                default -> { }
            }
        }
        for (Map.Entry<String, List<String>> e : begun.entrySet()) {
            String txId = e.getKey();
            if (ended.contains(txId)) continue;
            if (!fresh.decisions.containsKey(txId)) {
                fresh.log.append(txId, "DECISION", Decision.ABORT.name());   // presumed abort
                fresh.decisions.put(txId, Decision.ABORT);
            }
            fresh.unacked.put(txId, new HashSet<>(e.getValue()));
            fresh.deliver(txId);
        }
        return fresh;
    }
}

public final class Main {
    static Money usd(String text) {
        boolean neg = text.startsWith("-");
        String s = neg ? text.substring(1) : text;
        long minor = Long.parseLong(s.replace(".", ""));
        return Money.ofMinor(neg ? -minor : minor, Currency.USD);
    }

    static void show(Ledger a, Ledger b) {
        System.out.println("  " + a.name + ": alice=" + a.balance("alice") + " locked=" + a.isLocked("alice")
                + " | " + b.name + ": bob=" + b.balance("bob") + " locked=" + b.isLocked("bob")
                + " | total=" + a.total(Currency.USD).plus(b.total(Currency.USD)));
    }

    public static void main(String[] args) {
        SimClock clock = new SimClock(0);
        SimNetwork net = new SimNetwork(clock, 5, 1_000);
        Ledger bankA = new Ledger("bank-a", Map.of("alice", usd("500.00")));
        Ledger bankB = new Ledger("bank-b", Map.of("bob", usd("100.00")));
        net.register(bankA); net.register(bankB);
        Coordinator coord = new Coordinator(net);

        System.out.println("== 1. Happy path: 150.00 USD alice@bank-a -> bob@bank-b ==");
        System.out.println("  " + coord.transfer("tx-1", bankA, "alice", bankB, "bob", usd("150.00")));
        show(bankA, bankB);
        System.out.println("  coordinator log: " + coord.logRecords());

        System.out.println("\n== 2. Participant votes NO: 900.00 exceeds alice's balance ==");
        System.out.println("  " + coord.transfer("tx-2", bankA, "alice", bankB, "bob", usd("900.00")));
        show(bankA, bankB);

        System.out.println("\n== 3. bank-b unreachable during prepare: timeout is a NO ==");
        net.partition("bank-b");
        long before = clock.now();
        System.out.println("  " + coord.transfer("tx-3", bankA, "alice", bankB, "bob", usd("10.00")));
        System.out.println("  waited " + (clock.now() - before) + " ms; bank-a state for tx-3 = " + bankA.stateOf("tx-3"));
        net.heal("bank-b");
        show(bankA, bankB);

        System.out.println("\n== 4. Coordinator dies after both YES votes, before logging the decision ==");
        coord.crashPoint = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG;
        try {
            coord.transfer("tx-4", bankA, "alice", bankB, "bob", usd("25.00"));
        } catch (CrashException e) {
            System.out.println("  coordinator: " + e.getMessage());
        }
        System.out.println("  bank-a in doubt: " + bankA.inDoubt() + ", bank-b in doubt: " + bankB.inDoubt());
        show(bankA, bankB);
        System.out.println("  another coordinator tries alice while she is locked: "
                + bankA.prepare("tx-5", new Posting("alice", usd("-1.00"))) + "  <- this is the blocking problem");
        System.out.println("  bank-b restarts from its own log and is still in doubt: " + bankB.crashAndRecover().inDoubt());
        coord = Coordinator.recover(coord, net);
        System.out.println("  recovered coordinator: presumed abort -> tx-4 = " + coord.decisionOf("tx-4").orElse(null));
        show(bankA, bankB);

        System.out.println("\n== 5. Coordinator dies after logging COMMIT, before telling anyone ==");
        coord.crashPoint = CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND;
        try {
            coord.transfer("tx-6", bankA, "alice", bankB, "bob", usd("40.00"));
        } catch (CrashException e) {
            System.out.println("  coordinator: " + e.getMessage());
        }
        System.out.println("  participants still PREPARED: " + bankA.stateOf("tx-6") + ", " + bankB.stateOf("tx-6"));
        coord = Coordinator.recover(coord, net);
        System.out.println("  recovered coordinator redelivers " + coord.decisionOf("tx-6").orElse(null)
                + " -> " + bankA.stateOf("tx-6") + ", " + bankB.stateOf("tx-6"));
        show(bankA, bankB);

        System.out.println("\n== 6. bank-b votes YES then crashes: the decision is final, delivery retries ==");
        net.crashAfterVote("bank-b");
        Outcome out = coord.transfer("tx-7", bankA, "alice", bankB, "bob", usd("5.00"));
        System.out.println("  " + out);
        System.out.println("  bank-a already " + bankA.stateOf("tx-7") + ", bank-b crashed while " + bankB.stateOf("tx-7"));
        bankB = bankB.crashAndRecover();
        net.replace(bankB);
        net.heal("bank-b");
        System.out.println("  bank-b recovered from its log: in doubt " + bankB.inDoubt() + ", bob locked=" + bankB.isLocked("bob"));
        System.out.println("  retryPending finished " + coord.retryPending() + " tx; bank-b now " + bankB.stateOf("tx-7"));
        show(bankA, bankB);
        System.out.println("\nmessages sent=" + net.messagesSent + " timeouts=" + net.timeouts + " clock=" + clock.now() + " ms");
    }
}
