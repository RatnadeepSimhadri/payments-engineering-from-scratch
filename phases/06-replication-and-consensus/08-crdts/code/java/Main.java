// Lesson: phases/06-replication-and-consensus/08-crdts/docs/en.md
// CRDTs: Counters, Sets, and Convergence
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Shapiro, Preguica, Baquero, Zawirski, "A comprehensive study of Convergent and Commutative
// Replicated Data Types" (INRIA RR-7506, 2011); Money type from Phase 10 Lesson 01. Java 21, stdlib only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.function.Supplier;

/**
 * A state-based (convergent) replicated data type. {@code merge} must be a join on a lattice:
 * commutative, associative, and idempotent. Every implementation in this file returns a NEW value
 * from merge and leaves both inputs untouched, so the laws can be tested on values directly.
 */
interface Crdt<T extends Crdt<T>> {
    T merge(T other);
}

/** Grow-only counter: one monotonically increasing slot per replica; value is the sum of slots. */
final class GCounter implements Crdt<GCounter> {
    private final TreeMap<String, Long> slots = new TreeMap<>();

    GCounter() {}

    GCounter(Map<String, Long> initial) { slots.putAll(initial); }

    void increment(String replica, long by) {
        if (by < 0) throw new IllegalArgumentException("a G-Counter only grows; got " + by);
        slots.merge(replica, by, Math::addExact);
    }

    long value() {
        long total = 0;
        for (long v : slots.values()) total = Math.addExact(total, v);
        return total;
    }

    Map<String, Long> slots() { return Collections.unmodifiableMap(slots); }

    @Override public GCounter merge(GCounter other) {
        GCounter out = new GCounter(slots);
        for (var e : other.slots.entrySet()) out.slots.merge(e.getKey(), e.getValue(), Math::max);
        return out;
    }

    @Override public boolean equals(Object o) { return o instanceof GCounter g && slots.equals(g.slots); }
    @Override public int hashCode() { return slots.hashCode(); }
    @Override public String toString() { return "GCounter" + slots + "=" + value(); }
}

/** Positive-negative counter: two G-Counters. value = increments - decrements. */
final class PNCounter implements Crdt<PNCounter> {
    final GCounter positive;
    final GCounter negative;

    PNCounter() { this(new GCounter(), new GCounter()); }

    private PNCounter(GCounter p, GCounter n) { positive = p; negative = n; }

    void increment(String replica, long by) { positive.increment(replica, by); }

    void decrement(String replica, long by) { negative.increment(replica, by); }

    long value() { return Math.subtractExact(positive.value(), negative.value()); }

    @Override public PNCounter merge(PNCounter other) {
        return new PNCounter(positive.merge(other.positive), negative.merge(other.negative));
    }

    @Override public boolean equals(Object o) {
        return o instanceof PNCounter c && positive.equals(c.positive) && negative.equals(c.negative);
    }
    @Override public int hashCode() { return Objects.hash(positive, negative); }
    @Override public String toString() { return "PNCounter(+" + positive.value() + " -" + negative.value() + ")=" + value(); }
}

/** Grow-only set: add only; merge is set union. */
final class GSet<E extends Comparable<E>> implements Crdt<GSet<E>> {
    private final TreeSet<E> elements = new TreeSet<>();

    void add(E e) { elements.add(e); }

    boolean contains(E e) { return elements.contains(e); }

    Set<E> elements() { return Collections.unmodifiableSet(elements); }

    @Override public GSet<E> merge(GSet<E> other) {
        GSet<E> out = new GSet<>();
        out.elements.addAll(elements);
        out.elements.addAll(other.elements);
        return out;
    }

    @Override public boolean equals(Object o) { return o instanceof GSet<?> s && elements.equals(s.elements); }
    @Override public int hashCode() { return elements.hashCode(); }
    @Override public String toString() { return "GSet" + elements; }
}

/** A globally unique tag minted by one replica: (replica, sequence). Comparable so sets stay deterministic. */
record Tag(String replica, long seq) implements Comparable<Tag> {
    @Override public int compareTo(Tag o) {
        int c = replica.compareTo(o.replica);
        return c != 0 ? c : Long.compare(seq, o.seq);
    }
    @Override public String toString() { return replica + "#" + seq; }
}

/** Per-replica tag source. Deterministic, no clock, no randomness. */
final class TagSource {
    private final String replica;
    private long next = 1;

    TagSource(String replica) { this.replica = replica; }

    Tag next() { return new Tag(replica, next++); }
}

/**
 * Observed-Remove set (add-wins). Each add mints a unique tag; a remove tombstones only the tags the
 * remover has observed. An element is present if it has at least one live (non-tombstoned) tag.
 */
final class ORSet<E extends Comparable<E>> implements Crdt<ORSet<E>> {
    private final TreeMap<E, TreeSet<Tag>> added = new TreeMap<>();
    private final TreeSet<Tag> tombstones = new TreeSet<>();

    Tag add(E e, TagSource tags) {
        Tag t = tags.next();
        added.computeIfAbsent(e, k -> new TreeSet<>()).add(t);
        return t;
    }

    /** Removes every tag of e that THIS replica currently observes. Tags it has not seen are unaffected. */
    void remove(E e) {
        TreeSet<Tag> tags = added.get(e);
        if (tags != null) tombstones.addAll(tags);
    }

    boolean contains(E e) {
        TreeSet<Tag> tags = added.get(e);
        if (tags == null) return false;
        for (Tag t : tags) if (!tombstones.contains(t)) return true;
        return false;
    }

    Set<E> elements() {
        TreeSet<E> out = new TreeSet<>();
        for (E e : added.keySet()) if (contains(e)) out.add(e);
        return out;
    }

    int tombstoneCount() { return tombstones.size(); }

    @Override public ORSet<E> merge(ORSet<E> other) {
        ORSet<E> out = new ORSet<>();
        for (var e : added.entrySet()) out.added.put(e.getKey(), new TreeSet<>(e.getValue()));
        for (var e : other.added.entrySet()) out.added.computeIfAbsent(e.getKey(), k -> new TreeSet<>()).addAll(e.getValue());
        out.tombstones.addAll(tombstones);
        out.tombstones.addAll(other.tombstones);
        return out;
    }

    @Override public boolean equals(Object o) {
        return o instanceof ORSet<?> s && added.equals(s.added) && tombstones.equals(s.tombstones);
    }
    @Override public int hashCode() { return Objects.hash(added, tombstones); }
    @Override public String toString() { return "ORSet" + elements() + " tombstones=" + tombstones.size(); }
}

/**
 * Last-writer-wins register. The "writer" is identified by (timestamp, replica); the replica id breaks
 * timestamp ties so that merge is deterministic. The losing value is discarded: that is the data loss
 * Lesson 09 examines.
 */
record LWWRegister<V>(V value, long timestamp, String replica) implements Crdt<LWWRegister<V>> {

    LWWRegister<V> set(V newValue, long newTimestamp, String writer) {
        return new LWWRegister<>(newValue, newTimestamp, writer);
    }

    boolean dominates(LWWRegister<V> other) {
        if (timestamp != other.timestamp) return timestamp > other.timestamp;
        return replica.compareTo(other.replica) > 0;
    }

    @Override public LWWRegister<V> merge(LWWRegister<V> other) {
        return dominates(other) ? this : other;
    }
}

/** ISO 4217 currency, copied from Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

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

/** Integer minor units + currency. Only the operations this lesson needs. */
record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (frac.length() > currency.exponent()) throw new ArithmeticException(text + " has too many decimals for " + currency.code());
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    boolean isNegative() { return minorUnits < 0; }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + abs + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return sign + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/**
 * A balance replicated as a PN-Counter of minor units. Credits increment, debits decrement, merge is
 * coordination-free. The type can tell you the balance went negative; it cannot prevent it.
 */
final class ReplicatedBalance implements Crdt<ReplicatedBalance> {
    final Currency currency;
    final PNCounter counter;

    ReplicatedBalance(Currency currency) { this(currency, new PNCounter()); }

    private ReplicatedBalance(Currency currency, PNCounter counter) { this.currency = currency; this.counter = counter; }

    private void requireCurrency(Money m) {
        if (!m.currency().equals(currency)) throw new CurrencyMismatchException(currency, m.currency());
        if (m.minorUnits() < 0) throw new IllegalArgumentException("amount must be non-negative: " + m);
    }

    void credit(String replica, Money amount) { requireCurrency(amount); counter.increment(replica, amount.minorUnits()); }

    /** Debits unconditionally. A local check against balance() is NOT a global guarantee; see the lesson. */
    void debit(String replica, Money amount) { requireCurrency(amount); counter.decrement(replica, amount.minorUnits()); }

    Money balance() { return Money.ofMinor(counter.value(), currency); }

    @Override public ReplicatedBalance merge(ReplicatedBalance other) {
        if (!other.currency.equals(currency)) throw new CurrencyMismatchException(currency, other.currency);
        return new ReplicatedBalance(currency, counter.merge(other.counter));
    }

    @Override public boolean equals(Object o) {
        return o instanceof ReplicatedBalance b && currency.equals(b.currency) && counter.equals(b.counter);
    }
    @Override public int hashCode() { return Objects.hash(currency, counter); }
    @Override public String toString() { return "Balance " + balance(); }
}

/**
 * Property-style checks of the three merge laws on random replica states. The generator is a
 * {@link Supplier} of random states driven by a seeded Random, so every failure is replayable.
 */
final class CrdtLaws {
    record Result(int trials, int commutativeFailures, int associativeFailures, int idempotentFailures) {
        boolean allHold() { return commutativeFailures == 0 && associativeFailures == 0 && idempotentFailures == 0; }
        @Override public String toString() {
            return trials + " trials: commutative " + (commutativeFailures == 0 ? "ok" : commutativeFailures + " failures")
                    + ", associative " + (associativeFailures == 0 ? "ok" : associativeFailures + " failures")
                    + ", idempotent " + (idempotentFailures == 0 ? "ok" : idempotentFailures + " failures");
        }
    }

    static <T extends Crdt<T>> Result check(Supplier<T> gen, int trials) {
        int comm = 0, assoc = 0, idem = 0;
        for (int i = 0; i < trials; i++) {
            T a = gen.get(), b = gen.get(), c = gen.get();
            if (!a.merge(b).equals(b.merge(a))) comm++;
            if (!a.merge(b).merge(c).equals(a.merge(b.merge(c)))) assoc++;
            if (!a.merge(a).equals(a)) idem++;
        }
        return new Result(trials, comm, assoc, idem);
    }

    /** Merge every state into every other in a random order and report whether all replicas agree. */
    static <T extends Crdt<T>> boolean converges(List<T> states, Random rng) {
        List<T> result = new ArrayList<>(states);
        for (int i = 0; i < result.size(); i++) {
            List<Integer> order = new ArrayList<>();
            for (int j = 0; j < states.size(); j++) if (j != i) order.add(j);
            Collections.shuffle(order, rng);
            T acc = result.get(i);
            for (int j : order) acc = acc.merge(states.get(j));
            result.set(i, acc);
        }
        for (T t : result) if (!t.equals(result.get(0))) return false;
        return true;
    }
}

/** A deliberately broken "counter" whose merge adds instead of taking the max. Fails idempotence. */
final class NaiveSumCounter implements Crdt<NaiveSumCounter> {
    final long total;
    NaiveSumCounter(long total) { this.total = total; }
    @Override public NaiveSumCounter merge(NaiveSumCounter other) { return new NaiveSumCounter(total + other.total); }
    @Override public boolean equals(Object o) { return o instanceof NaiveSumCounter n && n.total == total; }
    @Override public int hashCode() { return Long.hashCode(total); }
}

public final class Main {

    static GCounter randomGCounter(Random rng) {
        GCounter g = new GCounter();
        for (String r : List.of("A", "B", "C")) if (rng.nextBoolean()) g.increment(r, rng.nextInt(50));
        return g;
    }

    static PNCounter randomPNCounter(Random rng) {
        PNCounter c = new PNCounter();
        for (String r : List.of("A", "B", "C")) {
            if (rng.nextBoolean()) c.increment(r, rng.nextInt(50));
            if (rng.nextBoolean()) c.decrement(r, rng.nextInt(50));
        }
        return c;
    }

    static ORSet<String> randomORSet(Random rng, Map<String, TagSource> sources) {
        ORSet<String> s = new ORSet<>();
        String[] items = {"apple", "pear", "fig"};
        for (int i = 0; i < 4; i++) {
            String replica = List.of("A", "B", "C").get(rng.nextInt(3));
            String item = items[rng.nextInt(items.length)];
            if (rng.nextInt(3) == 0) s.remove(item); else s.add(item, sources.get(replica));
        }
        return s;
    }

    public static void main(String[] args) {
        Random rng = new Random(20261013L);

        System.out.println("== G-Counter: page views on three replicas, merged in any order ==");
        GCounter a = new GCounter(), b = new GCounter(), c = new GCounter();
        a.increment("A", 3); b.increment("B", 5); c.increment("C", 2);
        b.increment("B", 1);
        GCounter ab = a.merge(b), abc = ab.merge(c), cba = c.merge(b).merge(a);
        System.out.println("A=" + a + "  B=" + b + "  C=" + c);
        System.out.println("merge(A,B,C) = " + abc + "   merge(C,B,A) = " + cba + "   equal: " + abc.equals(cba));
        System.out.println("merge(A,B) twice = " + ab.merge(b) + "   (idempotent: a stale message cannot double count)");

        System.out.println();
        System.out.println("== PN-Counter as a wallet balance: the merge is safe, the invariant is not ==");
        ReplicatedBalance east = new ReplicatedBalance(Currency.USD);
        east.credit("east", Money.parse("100.00", Currency.USD));
        ReplicatedBalance west = east.merge(new ReplicatedBalance(Currency.USD));  // west syncs the deposit
        System.out.println("both regions see " + east.balance());
        // the partition: each region checks locally, each sees 100.00, each approves an 80.00 debit
        Money debit = Money.parse("80.00", Currency.USD);
        if (!east.balance().isNegative()) east.debit("east", debit);
        if (!west.balance().isNegative()) west.debit("west", debit);
        System.out.println("east after local debit: " + east.balance() + "   west after local debit: " + west.balance());
        ReplicatedBalance merged = east.merge(west);
        System.out.println("after the partition heals: " + merged.balance()
                + "   negative=" + merged.balance().isNegative() + "  (both debits survive; nothing was lost, and nothing was prevented)");

        System.out.println();
        System.out.println("== G-Set and OR-Set: removing something a peer re-added ==");
        TagSource tagsA = new TagSource("A"), tagsB = new TagSource("B");
        ORSet<String> cartA = new ORSet<>(), cartB;
        cartA.add("book", tagsA);
        cartB = cartA.merge(new ORSet<>());              // B syncs
        cartA.remove("book");                            // A removes the tag it observed (A#1)
        cartB.add("book", tagsB);                        // B concurrently adds again (B#1)
        ORSet<String> cart = cartA.merge(cartB);
        System.out.println("A removed book, B concurrently added book -> merged: " + cart.elements()
                + "  (add wins: B's tag was never observed by A's remove)");
        GSet<String> gs = new GSet<>();
        gs.add("book");
        System.out.println("G-Set has no remove at all: " + gs);

        System.out.println();
        System.out.println("== LWW-Register: deterministic, and lossy ==");
        LWWRegister<String> r1 = new LWWRegister<>("addr=Oak St", 10, "A");
        LWWRegister<String> r2 = new LWWRegister<>("addr=Elm St", 10, "B");
        System.out.println("A wrote '" + r1.value() + "' at t=10, B wrote '" + r2.value() + "' at t=10 -> merged keeps '"
                + r1.merge(r2).value() + "' (tie broken by replica id; the other write is gone)");

        System.out.println();
        System.out.println("== Merge laws, property-style (seed 20261013) ==");
        System.out.println("G-Counter  " + CrdtLaws.check(() -> randomGCounter(rng), 500));
        System.out.println("PN-Counter " + CrdtLaws.check(() -> randomPNCounter(rng), 500));
        Map<String, TagSource> sources = Map.of("A", new TagSource("A"), "B", new TagSource("B"), "C", new TagSource("C"));
        System.out.println("OR-Set     " + CrdtLaws.check(() -> randomORSet(rng, sources), 500));
        System.out.println("NaiveSum   " + CrdtLaws.check(() -> new NaiveSumCounter(rng.nextInt(100)), 500)
                + "  <- merge by addition is not a join");
        List<PNCounter> replicas = new ArrayList<>();
        for (int i = 0; i < 5; i++) replicas.add(randomPNCounter(rng));
        System.out.println("5 PN-Counter replicas, all-to-all merge in shuffled orders, converge: "
                + CrdtLaws.converges(replicas, rng));
    }
}
