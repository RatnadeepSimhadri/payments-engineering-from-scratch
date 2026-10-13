// Lesson: phases/10-money-and-ledgers/05-balances-and-snapshots/docs/en.md
// Balances, Running Totals, and Snapshots
// Run:  javac -d .out *.java && java -cp .out Main          (add --timing to print wall-clock measurements)
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Fowler, "Event Sourcing" and "Snapshot" patterns; TigerBeetle account balances; Money from Lesson 01.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;

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
// The ledger (shape from Lesson 03, trimmed to what balances need) with three ways to read a balance.
// ---------------------------------------------------------------------------------------------

enum Side { DEBIT, CREDIT }

enum AccountType {
    ASSET(Side.DEBIT), EXPENSE(Side.DEBIT), LIABILITY(Side.CREDIT), EQUITY(Side.CREDIT), REVENUE(Side.CREDIT);

    private final Side normalBalance;
    AccountType(Side normalBalance) { this.normalBalance = normalBalance; }
    Side normalBalance() { return normalBalance; }
}

record Account(String id, String name, AccountType type, Currency currency) {}

record Posting(String accountId, Side side, Money amount) {
    Posting {
        Objects.requireNonNull(accountId, "accountId");
        if (!amount.isPositive()) throw new LedgerException("posting amount must be positive, got " + amount);
    }

    static Posting debit(String accountId, Money amount) { return new Posting(accountId, Side.DEBIT, amount); }
    static Posting credit(String accountId, Money amount) { return new Posting(accountId, Side.CREDIT, amount); }

    /** Debits count positive, credits negative: the "net debit" convention used by every fold below. */
    long signedMinor() { return side == Side.DEBIT ? amount.minorUnits() : -amount.minorUnits(); }
}

/** A balanced entry with an effective timestamp (epoch seconds, injected by the caller, never read from a clock). */
record JournalEntry(String id, long effectiveAt, List<Posting> postings) {
    JournalEntry {
        if (postings == null || postings.size() < 2) throw new LedgerException("entry " + id + " needs at least two postings");
        postings = List.copyOf(postings);
        Map<Currency, Long> net = new HashMap<>();
        for (Posting p : postings) net.merge(p.amount().currency(), p.signedMinor(), Math::addExact);
        for (Map.Entry<Currency, Long> e : net.entrySet()) {
            if (e.getValue() != 0) throw new LedgerException("entry " + id + " unbalanced in " + e.getKey().code());
        }
    }
}

class LedgerException extends IllegalArgumentException {
    LedgerException(String message) { super(message); }
}

/** A point-in-time copy of every account's net debit, valid after the entry at journal position {@code seq}. */
record Snapshot(long seq, long effectiveAt, Map<String, Long> netDebits) {
    Snapshot { netDebits = Map.copyOf(netDebits); }

    long netDebit(String accountId) { return netDebits.getOrDefault(accountId, 0L); }
}

/** A balance reading plus how many postings had to be examined to produce it: the cost, made visible. */
record Reading(Money balance, long postingsScanned) {}

/** Growable primitive long array: a million boxed Longs would cost 16 MB of headers for nothing. */
final class LongList {
    private long[] data = new long[8];
    private int size = 0;

    void add(long value) {
        if (size == data.length) data = Arrays.copyOf(data, size * 2);
        data[size++] = value;
    }

    long get(int index) { return data[index]; }
    int size() { return size; }
}

/**
 * Append-only journal with:
 *   1. a running balance per account (O(1) current balance),
 *   2. a running-balance column per account (balance after each of its postings; O(log n) as-of reads),
 *   3. periodic snapshots of every account every {@code snapshotInterval} entries (bounded replay).
 * Every read path must agree with {@link #foldNetDebit}, the slow, obviously-correct full scan.
 */
final class Ledger {
    private final int snapshotInterval;
    private final Map<String, Account> accounts = new LinkedHashMap<>();
    private final List<JournalEntry> journal = new ArrayList<>();       // journal.get(i) has seq i + 1
    private final Map<String, Long> running = new HashMap<>();          // current net debit per account
    private final Map<String, LongList> columnSeq = new HashMap<>();    // per account: seq of each posting
    private final Map<String, LongList> columnAfter = new HashMap<>();  // per account: net debit after it
    private final List<Snapshot> snapshots = new ArrayList<>();         // ascending by seq; seq 0 is empty

    Ledger(int snapshotInterval) {
        if (snapshotInterval < 1) throw new IllegalArgumentException("snapshotInterval must be >= 1");
        this.snapshotInterval = snapshotInterval;
        snapshots.add(new Snapshot(0, Long.MIN_VALUE, Map.of()));
    }

    Account openAccount(String id, String name, AccountType type, Currency currency) {
        Account account = new Account(id, name, type, currency);
        if (accounts.putIfAbsent(id, account) != null) throw new LedgerException("account exists: " + id);
        running.put(id, 0L);
        columnSeq.put(id, new LongList());
        columnAfter.put(id, new LongList());
        return account;
    }

    Account account(String id) {
        Account a = accounts.get(id);
        if (a == null) throw new LedgerException("unknown account: " + id);
        return a;
    }

    long head() { return journal.size(); }

    List<Snapshot> snapshots() { return Collections.unmodifiableList(snapshots); }

    /** Validate everything, then mutate everything. Returns the entry's journal position (seq). */
    long post(JournalEntry entry) {
        for (Posting p : entry.postings()) {
            if (!account(p.accountId()).currency().equals(p.amount().currency())) {
                throw new LedgerException("entry " + entry.id() + ": currency does not match account " + p.accountId());
            }
        }
        if (!journal.isEmpty() && entry.effectiveAt() < journal.get(journal.size() - 1).effectiveAt()) {
            throw new LedgerException("entry " + entry.id() + " is backdated; post a correction at the current time instead");
        }
        journal.add(entry);
        long seq = journal.size();
        for (Posting p : entry.postings()) {
            long after = Math.addExact(running.get(p.accountId()), p.signedMinor());
            running.put(p.accountId(), after);
            columnSeq.get(p.accountId()).add(seq);
            columnAfter.get(p.accountId()).add(after);
        }
        if (seq % snapshotInterval == 0) takeSnapshot();
        return seq;
    }

    /** Copy every running total. O(accounts). Called automatically every snapshotInterval entries. */
    Snapshot takeSnapshot() {
        long seq = head();
        Snapshot last = snapshots.get(snapshots.size() - 1);
        if (last.seq() == seq) return last;
        Snapshot s = new Snapshot(seq, journal.get((int) seq - 1).effectiveAt(), running);
        snapshots.add(s);
        return s;
    }

    // ---- read path 0: the definition. Slow, obviously correct, used to check every other path. ----

    /** Net debit of an account after the entry at {@code seq}, by summing every posting from the start. O(postings). */
    Reading foldNetDebit(String accountId, long seq) {
        Account a = account(accountId);
        long acc = 0, scanned = 0;
        for (int i = 0; i < seq; i++) {
            for (Posting p : journal.get(i).postings()) {
                scanned++;
                if (p.accountId().equals(accountId)) acc = Math.addExact(acc, p.signedMinor());
            }
        }
        return new Reading(Money.ofMinor(acc, a.currency()), scanned);
    }

    // ---- read path 1: the running total. O(1), but only for "now". ----

    Money currentNetDebit(String accountId) { return Money.ofMinor(running.get(accountId), account(accountId).currency()); }

    /** Balance in the account's normal direction (positive = what you expect for that account type). */
    Money balance(String accountId) { return toNormal(account(accountId), running.get(accountId)); }

    // ---- read path 2: nearest snapshot at or before seq, then replay the delta. O(snapshotInterval). ----

    Reading snapshotNetDebit(String accountId, long seq) {
        Account a = account(accountId);
        requireSeq(seq);
        Snapshot base = nearestSnapshot(seq);
        long acc = base.netDebit(accountId), scanned = 0;
        for (long s = base.seq(); s < seq; s++) {
            for (Posting p : journal.get((int) s).postings()) {
                scanned++;
                if (p.accountId().equals(accountId)) acc = Math.addExact(acc, p.signedMinor());
            }
        }
        return new Reading(Money.ofMinor(acc, a.currency()), scanned);
    }

    /** Binary search: the last snapshot whose seq <= target. Snapshot 0 (all zero) always qualifies. */
    Snapshot nearestSnapshot(long seq) {
        int lo = 0, hi = snapshots.size() - 1;
        while (lo < hi) {
            int mid = (lo + hi + 1) >>> 1;
            if (snapshots.get(mid).seq() <= seq) lo = mid; else hi = mid - 1;
        }
        return snapshots.get(lo);
    }

    // ---- read path 3: the per-account running-balance column. O(log postings of that account). ----

    Reading columnNetDebit(String accountId, long seq) {
        Account a = account(accountId);
        requireSeq(seq);
        LongList seqs = columnSeq.get(accountId), after = columnAfter.get(accountId);
        int lo = 0, hi = seqs.size() - 1, found = -1, probes = 0;
        while (lo <= hi) {                       // last posting of this account with seq <= target
            int mid = (lo + hi) >>> 1;
            probes++;
            if (seqs.get(mid) <= seq) { found = mid; lo = mid + 1; } else hi = mid - 1;
        }
        return new Reading(Money.ofMinor(found < 0 ? 0 : after.get(found), a.currency()), probes);
    }

    // ---- time-based queries: translate a timestamp into a journal position, then read. ----

    /** The journal position of the last entry with effectiveAt <= t; 0 if none. Binary search: timestamps never decrease. */
    long seqAsOf(long timestamp) {
        int lo = 0, hi = journal.size();          // answer in [0, size]
        while (lo < hi) {
            int mid = (lo + hi + 1) >>> 1;
            if (journal.get(mid - 1).effectiveAt() <= timestamp) lo = mid; else hi = mid - 1;
        }
        return lo;
    }

    Money balanceAsOf(String accountId, long timestamp) {
        Account a = account(accountId);
        return toNormal(a, snapshotNetDebit(accountId, seqAsOf(timestamp)).balance().minorUnits());
    }

    /** Check a snapshot against the definition: every account's value must equal the full fold at its seq. */
    boolean verify(Snapshot s) {
        for (String id : accounts.keySet()) {
            if (s.netDebit(id) != foldNetDebit(id, s.seq()).balance().minorUnits()) return false;
        }
        for (String id : s.netDebits().keySet()) if (!accounts.containsKey(id)) return false;
        return true;
    }

    private void requireSeq(long seq) {
        if (seq < 0 || seq > head()) throw new LedgerException("seq " + seq + " outside journal 0.." + head());
    }

    private static Money toNormal(Account a, long netDebit) {
        long signed = a.type().normalBalance() == Side.DEBIT ? netDebit : Math.negateExact(netDebit);
        return Money.ofMinor(signed, a.currency());
    }
}

/** Seeded generator of two-leg card payments into many merchant accounts: deterministic, no wall clock. */
final class PaymentStream {
    private final Random random;
    private final int merchants;
    private long clock;
    private long counter = 0;

    PaymentStream(long seed, int merchants, long startEpochSeconds) {
        this.random = new Random(seed);
        this.merchants = merchants;
        this.clock = startEpochSeconds;
    }

    static String merchantId(int i) { return String.format("m%04d", i); }

    JournalEntry next() {
        clock += random.nextInt(3);                                   // 0..2 seconds between payments
        long amount = 100 + random.nextInt(50_000);                   // 1.00 .. 500.99 USD
        String merchant = merchantId(random.nextInt(merchants));
        Money m = Money.ofMinor(amount, Currency.USD);
        return new JournalEntry("p" + (counter++), clock, List.of(Posting.debit("1200", m), Posting.credit(merchant, m)));
    }
}

public final class Main {
    public static void main(String[] args) {
        boolean timing = args.length > 0 && args[0].equals("--timing");
        Currency USD = Currency.USD;

        System.out.println("== A small ledger, snapshot every 3 entries ==");
        Ledger ledger = new Ledger(3);
        ledger.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        ledger.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, USD);
        ledger.openAccount("4100", "Fee Revenue", AccountType.REVENUE, USD);
        long day = 1_767_225_600L;                                     // 2026-01-01T00:00:00Z
        long[] sales = { 10_000, 2_500, 4_000, 1_999, 7_500 };
        for (int i = 0; i < sales.length; i++) {
            Money gross = Money.ofMinor(sales[i], USD), fee = gross.times(29, 1000, Rounding.HALF_EVEN);
            long seq = ledger.post(new JournalEntry("pay-" + (i + 1), day + i * 3600L, List.of(
                    Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee))));
            System.out.printf("seq %d  t=+%dh  gross %-11s fee %-9s  payable now %s%n",
                    seq, i, gross, fee, ledger.balance("2100"));
        }
        System.out.println("snapshots at seq " + ledger.snapshots().stream().map(Snapshot::seq).toList());
        for (long seq = 0; seq <= ledger.head(); seq++) {
            Reading fold = ledger.foldNetDebit("2100", seq);
            Reading snap = ledger.snapshotNetDebit("2100", seq);
            Reading col = ledger.columnNetDebit("2100", seq);
            System.out.printf("2100 net debit after seq %d: fold %-12s (%2d postings)  snapshot %-12s (%d)  column %-12s (%d probes)%n",
                    seq, fold.balance(), fold.postingsScanned(), snap.balance(), snap.postingsScanned(), col.balance(), col.postingsScanned());
        }
        System.out.println("payable as of t=+2h30m: " + ledger.balanceAsOf("2100", day + 9000) + " (seq " + ledger.seqAsOf(day + 9000) + ")");
        System.out.println("payable as of before the first payment: " + ledger.balanceAsOf("2100", day - 1));
        try {
            ledger.post(new JournalEntry("late", day, List.of(
                    Posting.debit("1200", Money.ofMinor(100, USD)), Posting.credit("2100", Money.ofMinor(100, USD)))));
        } catch (LedgerException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== One million postings, 1,000 merchants, snapshot every 10,000 entries ==");
        Ledger big = new Ledger(10_000);
        big.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        int merchants = 1_000;
        for (int i = 0; i < merchants; i++) big.openAccount(PaymentStream.merchantId(i), "Merchant " + i, AccountType.LIABILITY, USD);
        PaymentStream stream = new PaymentStream(7, merchants, day);
        long t0 = System.nanoTime();
        for (int i = 0; i < 500_000; i++) big.post(stream.next());     // 2 postings each
        long t1 = System.nanoTime();
        long target = 437_123;
        String merchant = PaymentStream.merchantId(42);
        long t2 = System.nanoTime();
        Reading fold = big.foldNetDebit(merchant, target);
        long t3 = System.nanoTime();
        Reading snap = big.snapshotNetDebit(merchant, target);
        long t4 = System.nanoTime();
        Reading col = big.columnNetDebit(merchant, target);
        long t5 = System.nanoTime();
        System.out.println("entries=" + big.head() + " snapshots=" + big.snapshots().size());
        System.out.println("merchant " + merchant + " as of seq " + target + ":");
        System.out.println("  full fold      " + fold.balance() + "  postings scanned " + fold.postingsScanned());
        System.out.println("  snapshot+delta " + snap.balance() + "  postings scanned " + snap.postingsScanned()
                + "  (from snapshot at seq " + big.nearestSnapshot(target).seq() + ")");
        System.out.println("  running column " + col.balance() + "  binary-search probes " + col.postingsScanned());
        System.out.println("  all three agree: " + (fold.balance().equals(snap.balance()) && snap.balance().equals(col.balance())));
        Money sum = Money.zero(USD);
        for (int i = 0; i < merchants; i++) sum = sum.plus(big.balance(PaymentStream.merchantId(i)));
        System.out.println("receivable " + big.balance("1200") + " == sum of merchant payables " + sum + ": " + big.balance("1200").equals(sum));
        if (timing) {
            System.out.printf("timing: post 500k entries %d ms; fold %.3f ms; snapshot %.3f ms; column %.4f ms%n",
                    (t1 - t0) / 1_000_000, (t3 - t2) / 1e6, (t4 - t3) / 1e6, (t5 - t4) / 1e6);
        }
    }
}
