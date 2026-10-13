// Lesson: phases/10-money-and-ledgers/11-ledger-concurrency/docs/en.md
// Ledger Concurrency: Hot Accounts and Balance Checks
// Run:  javac -d .out *.java && java -cp .out Main          (add --stats for contention counters and timings)
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981); JLS chapter 17 (memory model);
// java.util.concurrent.locks; Money from Lesson 01. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import java.util.concurrent.locks.ReentrantLock;

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
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

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

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
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    Money abs() { return minorUnits < 0 ? negate() : this; }
    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isPositive() { return minorUnits > 0; }

    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twiceRemainder = Math.abs(remainder) * 2;
        long absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twiceRemainder >= absDivisor ? quotient + sign : quotient;
            case HALF_EVEN -> {
                if (twiceRemainder > absDivisor) yield quotient + sign;
                if (twiceRemainder < absDivisor) yield quotient;
                yield (quotient % 2 == 0) ? quotient : quotient + sign;
            }
        };
    }

    /** Largest-remainder split (Lesson 01): the parts always sum exactly to the whole; ties go to the lower index. */
    Money[] allocate(long... ratios) {
        if (ratios.length == 0) throw new IllegalArgumentException("need at least one ratio");
        long total = 0;
        for (long r : ratios) {
            if (r < 0) throw new IllegalArgumentException("ratios must be non-negative");
            total = Math.addExact(total, r);
        }
        if (total == 0) throw new IllegalArgumentException("ratios must not all be zero");
        long[] shares = new long[ratios.length];
        long[] remainders = new long[ratios.length];
        long distributed = 0;
        long magnitude = Math.abs(minorUnits);
        for (int i = 0; i < ratios.length; i++) {
            long scaled = Math.multiplyExact(magnitude, ratios[i]);
            shares[i] = scaled / total;
            remainders[i] = scaled % total;
            distributed += shares[i];
        }
        long leftover = magnitude - distributed;
        Integer[] order = new Integer[ratios.length];
        for (int i = 0; i < order.length; i++) order[i] = i;
        Arrays.sort(order, (a, b) -> Long.compare(remainders[b], remainders[a]));
        for (int k = 0; k < leftover; k++) shares[order[k]]++;
        Money[] result = new Money[ratios.length];
        for (int i = 0; i < ratios.length; i++) result[i] = new Money(minorUnits < 0 ? -shares[i] : shares[i], currency);
        return result;
    }

    static Money sum(Currency currency, Money... amounts) {
        Money acc = zero(currency);
        for (Money m : amounts) acc = acc.plus(m);
        return acc;
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
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
// Part 1: a deterministic model checker. Each strategy is a tiny step machine; we explore EVERY
// interleaving of a few concurrent operations on one account and check the invariants at the end.
// ---------------------------------------------------------------------------------------------

enum Strategy { NAIVE, VERSIONED, LOCKED }

/** A deposit (amount > 0, never rejected) or a payout (amount < 0, rejected if it would overdraw). */
record Op(String name, long amount) {}

/** Result of exhaustive exploration: how many complete schedules, and how many broke an invariant. */
record Exploration(Strategy strategy, int schedules, int violations, String firstViolation) {}

final class InterleavingChecker {
    private static final int DONE = 99;

    /** Mutable world state, copied at every branch. */
    private static final class World {
        long balance, version;
        int lockHolder = -1;
        int[] pc;
        long[] localBalance, localVersion;
        boolean[] applied;
        StringBuilder trace = new StringBuilder();

        World copy() {
            World w = new World();
            w.balance = balance; w.version = version; w.lockHolder = lockHolder;
            w.pc = pc.clone(); w.localBalance = localBalance.clone(); w.localVersion = localVersion.clone();
            w.applied = applied.clone();
            w.trace = new StringBuilder(trace);
            return w;
        }
    }

    static Exploration explore(Strategy strategy, long initialBalance, List<Op> ops) {
        World w = new World();
        int n = ops.size();
        w.balance = initialBalance;
        w.pc = new int[n];
        w.localBalance = new long[n];
        w.localVersion = new long[n];
        w.applied = new boolean[n];
        int[] counts = new int[2];
        String[] first = new String[1];
        dfs(strategy, initialBalance, ops, w, counts, first, 0);
        return new Exploration(strategy, counts[0], counts[1], first[0]);
    }

    private static void dfs(Strategy s, long initial, List<Op> ops, World w, int[] counts, String[] first, int depth) {
        if (depth > 200) throw new IllegalStateException("exploration did not terminate");
        boolean anyEnabled = false;
        for (int t = 0; t < ops.size(); t++) {
            if (w.pc[t] == DONE || !enabled(s, w, t)) continue;
            anyEnabled = true;
            World next = w.copy();
            step(s, next, t, ops.get(t));
            dfs(s, initial, ops, next, counts, first, depth + 1);
        }
        if (!anyEnabled) {
            counts[0]++;
            String violation = check(initial, ops, w);
            if (violation != null) {
                counts[1]++;
                if (first[0] == null) first[0] = violation + " via " + w.trace.toString().strip();
            }
        }
    }

    private static boolean enabled(Strategy s, World w, int t) {
        return !(s == Strategy.LOCKED && w.pc[t] == 0 && w.lockHolder != -1);   // blocked waiting for the lock
    }

    /** One atomic step of thread t. Each case is exactly one shared-memory access (or lock action). */
    private static void step(Strategy s, World w, int t, Op op) {
        String who = op.name();
        switch (s) {
            case NAIVE -> {
                if (w.pc[t] == 0) { w.localBalance[t] = w.balance; w.pc[t] = 1; w.trace.append(who).append(":read "); }
                else {
                    long next = w.localBalance[t] + op.amount();
                    if (next >= 0) { w.balance = next; w.applied[t] = true; w.trace.append(who).append(":write "); }
                    else w.trace.append(who).append(":reject ");
                    w.pc[t] = DONE;
                }
            }
            case VERSIONED -> {
                if (w.pc[t] == 0) {
                    w.localBalance[t] = w.balance; w.localVersion[t] = w.version; w.pc[t] = 1;
                    w.trace.append(who).append(":read ");
                } else if (w.version != w.localVersion[t]) {
                    w.pc[t] = 0;                                               // stale read: retry from the top
                    w.trace.append(who).append(":conflict ");
                } else {                                                        // validate + write, one atomic step
                    long next = w.localBalance[t] + op.amount();
                    if (next >= 0) { w.balance = next; w.version++; w.applied[t] = true; w.trace.append(who).append(":commit "); }
                    else w.trace.append(who).append(":reject ");
                    w.pc[t] = DONE;
                }
            }
            case LOCKED -> {
                switch (w.pc[t]) {
                    case 0 -> { w.lockHolder = t; w.pc[t] = 1; w.trace.append(who).append(":lock "); }
                    case 1 -> { w.localBalance[t] = w.balance; w.pc[t] = 2; w.trace.append(who).append(":read "); }
                    case 2 -> {
                        long next = w.localBalance[t] + op.amount();
                        if (next >= 0) { w.balance = next; w.applied[t] = true; w.trace.append(who).append(":write "); }
                        else w.trace.append(who).append(":reject ");
                        w.pc[t] = 3;
                    }
                    default -> { w.lockHolder = -1; w.pc[t] = DONE; w.trace.append(who).append(":unlock "); }
                }
            }
        }
    }

    /** Invariants at the end of a schedule: conservation (balance = initial + applied ops) and no overdraft. */
    private static String check(long initial, List<Op> ops, World w) {
        long expected = initial;
        for (int t = 0; t < ops.size(); t++) if (w.applied[t]) expected += ops.get(t).amount();
        if (w.balance != expected) return "lost update: balance " + w.balance + " but applied ops imply " + expected;
        if (w.balance < 0) return "overdraft: balance " + w.balance;
        return null;
    }
}

// ---------------------------------------------------------------------------------------------
// Part 2: real threads. Three ledgers behind one interface, each a different answer to the hot account.
// ---------------------------------------------------------------------------------------------

final class InsufficientFundsException extends RuntimeException {
    InsufficientFundsException(String account, long balance, long amount) {
        super("account " + account + " has " + balance + " minor units, needs " + amount);
    }
}

interface ConcurrentLedger {
    String name();

    /** Open an account. {@code overdraftAllowed=false} means a transfer out of it must never drive it below zero. */
    void openAccount(String id, Currency currency, long initialMinor, boolean overdraftAllowed);

    /** Move {@code amount} from one account to another atomically, or throw without changing anything. */
    void transfer(String from, String to, Money amount);

    /** A consistent balance. For sharded accounts this sums the shards. */
    Money balance(String id);

    /** Sum of every account's balance: constant under transfers, the conservation check. */
    long totalMinor();

    /** Retries (optimistic) or lock waits (pessimistic): how much the hot account cost. Not deterministic. */
    long contention();
}

/** Mutable account cell for the lock-based ledgers. Guarded by its lock. */
final class Cell {
    final String id;
    final Currency currency;
    final boolean overdraftAllowed;
    final ReentrantLock lock = new ReentrantLock();
    long balance;

    Cell(String id, Currency currency, long balance, boolean overdraftAllowed) {
        this.id = id; this.currency = currency; this.balance = balance; this.overdraftAllowed = overdraftAllowed;
    }
}

/** Pessimistic: lock both accounts (in a global order), check, apply, unlock. */
class PessimisticLedger implements ConcurrentLedger {
    protected final Map<String, Cell> cells = new ConcurrentHashMap<>();
    protected final AtomicLong waits = new AtomicLong();

    public String name() { return "pessimistic"; }

    public void openAccount(String id, Currency currency, long initialMinor, boolean overdraftAllowed) {
        if (cells.putIfAbsent(id, new Cell(id, currency, initialMinor, overdraftAllowed)) != null) {
            throw new IllegalArgumentException("account exists: " + id);
        }
    }

    protected Cell cell(String id) {
        Cell c = cells.get(id);
        if (c == null) throw new IllegalArgumentException("unknown account: " + id);
        return c;
    }

    public void transfer(String from, String to, Money amount) {
        Cell a = cell(from), b = cell(to);
        validate(a, b, amount);
        Cell first = a.id.compareTo(b.id) < 0 ? a : b, second = first == a ? b : a;   // global order: no deadlock
        lock(first);
        try {
            lock(second);
            try {
                long m = amount.minorUnits();
                if (!a.overdraftAllowed && a.balance < m) throw new InsufficientFundsException(a.id, a.balance, m);
                a.balance = Math.subtractExact(a.balance, m);
                b.balance = Math.addExact(b.balance, m);
            } finally {
                second.lock.unlock();
            }
        } finally {
            first.lock.unlock();
        }
    }

    protected void lock(Cell c) {
        if (!c.lock.tryLock()) {
            waits.incrementAndGet();
            c.lock.lock();
        }
    }

    /** Move min(src.balance, max) from src to dst under both locks (global order). Returns what moved. */
    protected long moveUpTo(Cell src, Cell dst, long max) {
        Cell first = src.id.compareTo(dst.id) < 0 ? src : dst, second = first == src ? dst : src;
        lock(first);
        try {
            lock(second);
            try {
                long take = Math.min(Math.max(src.balance, 0), max);
                src.balance -= take;
                dst.balance = Math.addExact(dst.balance, take);
                return take;
            } finally {
                second.lock.unlock();
            }
        } finally {
            first.lock.unlock();
        }
    }

    protected static void validate(Cell a, Cell b, Money amount) {
        if (a == b) throw new IllegalArgumentException("transfer to the same account");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive");
        if (!a.currency.equals(amount.currency())) throw new CurrencyMismatchException(a.currency, amount.currency());
        if (!b.currency.equals(amount.currency())) throw new CurrencyMismatchException(b.currency, amount.currency());
    }

    public Money balance(String id) {
        Cell c = cell(id);
        c.lock.lock();
        try { return Money.ofMinor(c.balance, c.currency); } finally { c.lock.unlock(); }
    }

    /** Locks every account in order, so the total is a consistent cut, not a smear across in-flight transfers. */
    public long totalMinor() {
        List<Cell> all = new ArrayList<>(cells.values());
        all.sort((x, y) -> x.id.compareTo(y.id));
        for (Cell c : all) c.lock.lock();
        try {
            long sum = 0;
            for (Cell c : all) sum = Math.addExact(sum, c.balance);
            return sum;
        } finally {
            for (Cell c : all) c.lock.unlock();
        }
    }

    public long contention() { return waits.get(); }
}

/** Immutable versioned state: the row an optimistic UPDATE ... WHERE version = ? would read. */
record AccountState(long version, long balance) {}

/**
 * Optimistic: read both versions without locking, run the balance check, then commit only if neither version
 * changed (a short validate-and-write critical section, as in Kung and Robinson's serial validation). On conflict, retry.
 */
final class OptimisticLedger implements ConcurrentLedger {
    private record Row(String id, Currency currency, boolean overdraftAllowed, AtomicReference<AccountState> state, ReentrantLock commit) {}

    private final Map<String, Row> rows = new ConcurrentHashMap<>();
    private final AtomicLong retries = new AtomicLong();

    public String name() { return "optimistic"; }

    public void openAccount(String id, Currency currency, long initialMinor, boolean overdraftAllowed) {
        Row r = new Row(id, currency, overdraftAllowed, new AtomicReference<>(new AccountState(0, initialMinor)), new ReentrantLock());
        if (rows.putIfAbsent(id, r) != null) throw new IllegalArgumentException("account exists: " + id);
    }

    private Row row(String id) {
        Row r = rows.get(id);
        if (r == null) throw new IllegalArgumentException("unknown account: " + id);
        return r;
    }

    public void transfer(String from, String to, Money amount) {
        Row a = row(from), b = row(to);
        if (a == b) throw new IllegalArgumentException("transfer to the same account");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive");
        if (!a.currency().equals(amount.currency())) throw new CurrencyMismatchException(a.currency(), amount.currency());
        if (!b.currency().equals(amount.currency())) throw new CurrencyMismatchException(b.currency(), amount.currency());
        long m = amount.minorUnits();
        Row first = a.id().compareTo(b.id()) < 0 ? a : b, second = first == a ? b : a;
        while (true) {
            AccountState sa = a.state().get(), sb = b.state().get();          // read phase: no locks
            boolean insufficient = !a.overdraftAllowed() && sa.balance() < m;  // business check on the read
            first.commit().lock();
            second.commit().lock();
            try {
                if (a.state().get() == sa && b.state().get() == sb) {          // validate: nothing changed since the read
                    if (insufficient) throw new InsufficientFundsException(a.id(), sa.balance(), m);
                    a.state().set(new AccountState(sa.version() + 1, Math.subtractExact(sa.balance(), m)));
                    b.state().set(new AccountState(sb.version() + 1, Math.addExact(sb.balance(), m)));
                    return;
                }
            } finally {
                second.commit().unlock();
                first.commit().unlock();
            }
            retries.incrementAndGet();                                         // stale read: go again
            Thread.onSpinWait();
        }
    }

    public Money balance(String id) {
        Row r = row(id);
        return Money.ofMinor(r.state().get().balance(), r.currency());
    }

    long version(String id) { return row(id).state().get().version(); }

    public long totalMinor() {
        List<Row> all = new ArrayList<>(rows.values());
        all.sort((x, y) -> x.id().compareTo(y.id()));
        for (Row r : all) r.commit().lock();
        try {
            long sum = 0;
            for (Row r : all) sum = Math.addExact(sum, r.state().get().balance());
            return sum;
        } finally {
            for (Row r : all) r.commit().unlock();
        }
    }

    public long contention() { return retries.get(); }
}

/**
 * Sharded: a hot account is K sub-accounts "id#0".."id#K-1". Credits pick one shard (by a caller-supplied
 * routing key), so K writers proceed in parallel. Debits drain shards greedily, one shard lock at a time, and
 * give everything back if the total is short, through an in-flight account. rollUp() sweeps every shard into shard 0.
 */
final class ShardedLedger extends PessimisticLedger {
    private final Map<String, Integer> shardCount = new HashMap<>();
    private final AtomicLong routing = new AtomicLong();

    public String name() { return "sharded"; }

    void openHotAccount(String id, Currency currency, long initialMinor, int shards) {
        if (shards < 1) throw new IllegalArgumentException("need at least one shard");
        Money[] parts = Money.ofMinor(initialMinor, currency).allocate(evenRatios(shards));
        for (int k = 0; k < shards; k++) super.openAccount(shardId(id, k), currency, parts[k].minorUnits(), false);
        super.openAccount(id + "#inflight", currency, 0, false);
        shardCount.put(id, shards);
    }

    static String shardId(String id, int k) { return id + "#" + k; }

    boolean isHot(String id) { return shardCount.containsKey(id); }

    int shards(String id) { return shardCount.getOrDefault(id, 1); }

    @Override
    public void transfer(String from, String to, Money amount) {
        if (isHot(from) && isHot(to)) throw new IllegalArgumentException("hot-to-hot transfers are not supported");
        if (isHot(to)) {
            int k = (int) Math.floorMod(routing.getAndIncrement(), shards(to));       // spread credits across shards
            super.transfer(from, shardId(to, k), amount);
        } else if (isHot(from)) {
            debitHot(from, to, amount);
        } else {
            super.transfer(from, to, amount);
        }
    }

    /**
     * Greedy drain across shards into an in-flight account, then one move to the destination. Every step is a
     * two-account locked move, so a consistent cut (totalMinor) sees the money in exactly one place at all times.
     * If the shards are short, every unit is moved back and the payout is rejected.
     */
    private void debitHot(String hot, String to, Money amount) {
        Cell inflight = cell(hot + "#inflight"), dest = cell(to);
        validate(inflight, dest, amount);
        long remaining = amount.minorUnits();
        long[] taken = new long[shards(hot)];
        int n = shards(hot), start = (int) Math.floorMod(routing.getAndIncrement(), n);
        for (int i = 0; i < n && remaining > 0; i++) {
            int k = (start + i) % n;
            long got = moveUpTo(cell(shardId(hot, k)), inflight, remaining);
            taken[k] += got;
            remaining -= got;
        }
        if (remaining > 0) {
            for (int k = 0; k < n; k++) if (taken[k] > 0) moveUpTo(inflight, cell(shardId(hot, k)), taken[k]);
            throw new InsufficientFundsException(hot, amount.minorUnits() - remaining, amount.minorUnits());
        }
        moveUpTo(inflight, dest, amount.minorUnits());
    }

    /** Sum of the shards, read under all shard locks: a consistent balance for one hot account. */
    @Override
    public Money balance(String id) {
        if (!isHot(id)) return super.balance(id);
        int n = shards(id);
        Cell[] cs = new Cell[n];
        for (int k = 0; k < n; k++) cs[k] = cell(shardId(id, k));
        for (Cell c : cs) c.lock.lock();
        try {
            long sum = 0;
            for (Cell c : cs) sum = Math.addExact(sum, c.balance);
            return Money.ofMinor(sum, cs[0].currency);
        } finally {
            for (Cell c : cs) c.lock.unlock();
        }
    }

    /** Sweep shards 1..K-1 into shard 0 (in a real ledger, one sweep entry per shard). Total is unchanged. */
    long rollUp(String id) {
        long moved = 0;
        for (int k = 1; k < shards(id); k++) {
            Cell shard = cell(shardId(id, k));
            lock(shard);
            long amount;
            try { amount = shard.balance; } finally { shard.lock.unlock(); }
            if (amount > 0) {
                super.transfer(shardId(id, k), shardId(id, 0), Money.ofMinor(amount, shard.currency));
                moved += amount;
            }
        }
        return moved;
    }

    long shardBalance(String id, int k) { return super.balance(shardId(id, k)).minorUnits(); }

    private static long[] evenRatios(int n) {
        long[] r = new long[n];
        Arrays.fill(r, 1);
        return r;
    }
}

/** The workload: many customers paying one merchant (the hot account), plus payouts out of it to the bank. */
record Workload(int threads, int opsPerThread, int customers, long customerFloat, long merchantFloat, int payoutEvery, long seed) {

    static String customer(int i) { return String.format("c%03d", i); }

    void open(ConcurrentLedger ledger, int merchantShards) {
        for (int i = 0; i < customers; i++) ledger.openAccount(customer(i), Currency.USD, customerFloat, false);
        ledger.openAccount("bank", Currency.USD, 0, true);
        if (ledger instanceof ShardedLedger s && merchantShards > 1) s.openHotAccount("merchant", Currency.USD, merchantFloat, merchantShards);
        else ledger.openAccount("merchant", Currency.USD, merchantFloat, false);
    }

    /** Deterministic per-thread script: what each thread will do, independent of how threads interleave. */
    List<long[]> script(int thread) {
        Random r = new Random(seed * 31 + thread);
        List<long[]> ops = new ArrayList<>(opsPerThread);
        for (int i = 0; i < opsPerThread; i++) {
            boolean payout = i % payoutEvery == payoutEvery - 1;
            ops.add(new long[] {payout ? -1 : r.nextInt(customers), 1 + r.nextInt(10_000)});
        }
        return ops;
    }

    /** Expected final balances: computable from the scripts alone because every op is guaranteed to succeed. */
    Map<String, Long> expected() {
        Map<String, Long> m = new HashMap<>();
        for (int i = 0; i < customers; i++) m.put(customer(i), customerFloat);
        m.put("merchant", merchantFloat);
        m.put("bank", 0L);
        for (int t = 0; t < threads; t++) {
            for (long[] op : script(t)) {
                if (op[0] < 0) { m.merge("merchant", -op[1], Long::sum); m.merge("bank", op[1], Long::sum); }
                else { m.merge(customer((int) op[0]), -op[1], Long::sum); m.merge("merchant", op[1], Long::sum); }
            }
        }
        return m;
    }

    long payoutTotal() {
        long sum = 0;
        for (int t = 0; t < threads; t++) for (long[] op : script(t)) if (op[0] < 0) sum += op[1];
        return sum;
    }

    /** Run every thread's script concurrently, released together by a latch. Returns failures seen (should be 0). */
    long run(ConcurrentLedger ledger) throws InterruptedException {
        CountDownLatch start = new CountDownLatch(1);
        AtomicLong failures = new AtomicLong();
        List<Thread> ts = new ArrayList<>();
        for (int t = 0; t < threads; t++) {
            List<long[]> ops = script(t);
            Thread th = new Thread(() -> {
                try { start.await(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); return; }
                for (long[] op : ops) {
                    try {
                        Money m = Money.ofMinor(op[1], Currency.USD);
                        if (op[0] < 0) ledger.transfer("merchant", "bank", m);
                        else ledger.transfer(customer((int) op[0]), "merchant", m);
                    } catch (RuntimeException e) {
                        failures.incrementAndGet();
                    }
                }
            });
            ts.add(th);
            th.start();
        }
        start.countDown();
        for (Thread th : ts) th.join();
        return failures.get();
    }
}

public final class Main {
    public static void main(String[] args) throws InterruptedException {
        boolean stats = args.length > 0 && args[0].equals("--stats");

        System.out.println("== Exhaustive interleavings: balance 100.00, payouts of 60.00 and 60.00, deposit of 30.00 ==");
        List<Op> ops = List.of(new Op("P1", -6_000), new Op("P2", -6_000), new Op("D", 3_000));
        for (Strategy s : Strategy.values()) {
            Exploration e = InterleavingChecker.explore(s, 10_000, ops);
            System.out.printf("%-9s schedules %4d  violations %4d%n", s, e.schedules(), e.violations());
            if (e.firstViolation() != null) System.out.println("          e.g. " + e.firstViolation());
        }

        System.out.println("\n== 8 threads x 20,000 ops: customers pay one hot merchant account; every 20th op pays out ==");
        Workload w = new Workload(8, 20_000, 200, 50_000_000L, 2_000_000_000L, 20, 42);
        Map<String, Long> expected = w.expected();
        long initialTotal = 200 * 50_000_000L + 2_000_000_000L;
        System.out.println("payouts requested " + Money.ofMinor(w.payoutTotal(), Currency.USD) + " against a merchant float of "
                + Money.ofMinor(2_000_000_000L, Currency.USD) + " (every op can succeed in any order)");
        List<ConcurrentLedger> ledgers = List.of(new PessimisticLedger(), new OptimisticLedger(), new ShardedLedger());
        for (ConcurrentLedger ledger : ledgers) {
            w.open(ledger, 8);
            long t0 = System.nanoTime();
            long failures = w.run(ledger);
            long ms = (System.nanoTime() - t0) / 1_000_000;
            if (ledger instanceof ShardedLedger s) s.rollUp("merchant");
            boolean match = true;
            for (Map.Entry<String, Long> e : expected.entrySet()) match &= ledger.balance(e.getKey()).minorUnits() == e.getValue();
            System.out.printf("%-12s failures %d  total conserved %-5s  every balance as expected %-5s  merchant %s%n",
                    ledger.name(), failures, ledger.totalMinor() == initialTotal, match, ledger.balance("merchant"));
            if (stats) System.out.printf("             contention %d  elapsed %d ms%n", ledger.contention(), ms);
        }

        System.out.println("\n== A payout that would overdraw is rejected atomically, in every strategy ==");
        for (ConcurrentLedger ledger : List.of(new PessimisticLedger(), new OptimisticLedger(), new ShardedLedger())) {
            ledger.openAccount("bank", Currency.USD, 0, true);
            if (ledger instanceof ShardedLedger s) s.openHotAccount("merchant", Currency.USD, 10_000, 4);
            else ledger.openAccount("merchant", Currency.USD, 10_000, false);
            try {
                ledger.transfer("merchant", "bank", Money.parse("100.01", Currency.USD));
            } catch (InsufficientFundsException e) {
                System.out.println(ledger.name() + ": rejected; merchant still " + ledger.balance("merchant") + ", bank " + ledger.balance("bank"));
            }
        }
    }
}
