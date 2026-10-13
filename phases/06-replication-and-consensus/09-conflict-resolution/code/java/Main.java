// Lesson: phases/06-replication-and-consensus/09-conflict-resolution/docs/en.md
// Conflict Resolution: LWW, Version Vectors, Merge Functions
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), section 4.4;
// Parker et al., "Detection of Mutual Inconsistency in Distributed Systems" (IEEE TSE 1983). Java 21, stdlib only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

/** How two versions relate under the happens-before order. */
enum Ordering { BEFORE, AFTER, EQUAL, CONCURRENT }

/** Per-replica counters. v1 dominates v2 when every entry of v1 >= v2's and at least one is greater. */
final class VersionVector {
    private final TreeMap<String, Long> clocks = new TreeMap<>();

    VersionVector() {}

    VersionVector(Map<String, Long> initial) { clocks.putAll(initial); }

    VersionVector copy() { return new VersionVector(clocks); }

    long get(String replica) { return clocks.getOrDefault(replica, 0L); }

    /** Returns a new vector with this replica's entry advanced by one. */
    VersionVector increment(String replica) {
        VersionVector out = copy();
        out.clocks.merge(replica, 1L, Long::sum);
        return out;
    }

    /** Pointwise maximum: the smallest vector that dominates both. */
    VersionVector merge(VersionVector other) {
        VersionVector out = copy();
        for (var e : other.clocks.entrySet()) out.clocks.merge(e.getKey(), e.getValue(), Math::max);
        return out;
    }

    Ordering compare(VersionVector other) {
        boolean less = false, greater = false;
        TreeSet<String> keys = new TreeSet<>(clocks.keySet());
        keys.addAll(other.clocks.keySet());
        for (String k : keys) {
            long a = get(k), b = other.get(k);
            if (a < b) less = true;
            if (a > b) greater = true;
        }
        if (less && greater) return Ordering.CONCURRENT;
        if (less) return Ordering.BEFORE;
        if (greater) return Ordering.AFTER;
        return Ordering.EQUAL;
    }

    boolean dominates(VersionVector other) { return compare(other) == Ordering.AFTER; }

    boolean concurrentWith(VersionVector other) { return compare(other) == Ordering.CONCURRENT; }

    @Override public boolean equals(Object o) { return o instanceof VersionVector v && clocks.equals(v.clocks); }
    @Override public int hashCode() { return clocks.hashCode(); }
    @Override public String toString() { return clocks.toString(); }
}

/** A write's identity: the replica that coordinated it and that replica's monotonically increasing counter. */
record Dot(String replica, long seq) implements Comparable<Dot> {
    @Override public int compareTo(Dot o) {
        int c = replica.compareTo(o.replica);
        return c != 0 ? c : Long.compare(seq, o.seq);
    }
    @Override public String toString() { return replica + ":" + seq; }
}

/**
 * A value, the dot that identifies the write, and the context (version vector) the writer had observed.
 * A write is obsolete exactly when some other write's context covers its dot: that writer had seen it.
 */
record Versioned<V>(V value, Dot dot, VersionVector context) {
    /** The full version vector of this write: everything it saw plus itself. */
    VersionVector version() {
        VersionVector v = context.copy();
        return v.get(dot.replica()) >= dot.seq() ? v : v.merge(new VersionVector(Map.of(dot.replica(), dot.seq())));
    }
    boolean obsoletedBy(Versioned<V> other) { return other.context().get(dot.replica()) >= dot.seq(); }
    @Override public String toString() { return value + "@" + version(); }
}

/**
 * Last-writer-wins by wall-clock timestamp (replica id breaks ties). Keeps one value per key.
 * Any write that loses the comparison is discarded with no record that it ever happened.
 */
final class LWWStore<V> {
    record Entry<V>(V value, long timestamp, String writer) {
        boolean beats(Entry<V> other) {
            if (other == null) return true;
            if (timestamp != other.timestamp) return timestamp > other.timestamp;
            return writer.compareTo(other.writer) > 0;
        }
    }

    private final TreeMap<String, Entry<V>> entries = new TreeMap<>();
    private long discarded = 0;

    void put(String key, V value, long timestamp, String writer) {
        Entry<V> incoming = new Entry<>(value, timestamp, writer);
        Entry<V> current = entries.get(key);
        if (incoming.beats(current)) {
            if (current != null) discarded++;
            entries.put(key, incoming);
        } else {
            discarded++;
        }
    }

    V get(String key) {
        Entry<V> e = entries.get(key);
        return e == null ? null : e.value();
    }

    /** Count of writes that were overwritten or rejected: the data LWW lost. */
    long discarded() { return discarded; }

    LWWStore<V> merge(LWWStore<V> other) {
        LWWStore<V> out = new LWWStore<>();
        out.entries.putAll(entries);
        for (var e : other.entries.entrySet()) {
            Entry<V> cur = out.entries.get(e.getKey());
            if (e.getValue().beats(cur)) out.entries.put(e.getKey(), e.getValue());
        }
        return out;
    }
}

/** Application-level merge: given every concurrent sibling, produce the one value the application wants. */
interface MergeFunction<V> {
    V merge(List<V> siblings);
}

/**
 * Dynamo-style store with dotted version vectors: each key holds a set of concurrent siblings. A write
 * carries the context the client read; the store drops every sibling that context covers and keeps the
 * rest. Readers see every sibling and resolve them with a {@link MergeFunction}.
 */
final class SiblingStore<V> {
    private final String replica;
    private long counter = 0;
    private final TreeMap<String, List<Versioned<V>>> data = new TreeMap<>();

    SiblingStore(String replica) { this.replica = replica; }

    String replica() { return replica; }

    /** Read: every sibling plus the context (merged vector) a client must send back with its next write. */
    record ReadResult<V>(List<Versioned<V>> siblings, VersionVector context) {
        List<V> values() {
            List<V> out = new ArrayList<>();
            for (Versioned<V> s : siblings) out.add(s.value());
            return out;
        }
        boolean hasConflict() { return siblings.size() > 1; }
    }

    ReadResult<V> get(String key) {
        List<Versioned<V>> sibs = data.getOrDefault(key, List.of());
        VersionVector ctx = new VersionVector();
        for (Versioned<V> s : sibs) ctx = ctx.merge(s.version());
        return new ReadResult<>(List.copyOf(sibs), ctx);
    }

    /**
     * Write with the context the client read. The new write gets a fresh dot from this replica's counter;
     * it obsoletes every sibling its context covers and is concurrent with every sibling it does not.
     */
    VersionVector put(String key, V value, VersionVector context) {
        Versioned<V> write = new Versioned<>(value, new Dot(replica, ++counter), context.copy());
        List<Versioned<V>> kept = new ArrayList<>();
        for (Versioned<V> s : data.getOrDefault(key, List.of())) if (!s.obsoletedBy(write)) kept.add(s);
        kept.add(write);
        data.put(key, kept);
        return write.version();
    }

    /** Write with no context at all: the write is concurrent with everything already stored. */
    VersionVector putBlind(String key, V value) { return put(key, value, new VersionVector()); }

    /** Anti-entropy: take every sibling the other replica holds, keep only the ones nobody has obsoleted. */
    void mergeFrom(SiblingStore<V> other) {
        for (var e : other.data.entrySet()) {
            List<Versioned<V>> all = new ArrayList<>(data.getOrDefault(e.getKey(), List.of()));
            for (Versioned<V> s : e.getValue()) if (!all.contains(s)) all.add(s);
            data.put(e.getKey(), maximal(all));
        }
    }

    /** Read every sibling, resolve with the application merge, write back with the merged context. */
    V resolve(String key, MergeFunction<V> fn) {
        ReadResult<V> read = get(key);
        if (read.siblings().isEmpty()) return null;
        V merged = read.siblings().size() == 1 ? read.siblings().get(0).value() : fn.merge(read.values());
        if (read.siblings().size() > 1) put(key, merged, read.context());
        return merged;
    }

    int siblingCount(String key) { return data.getOrDefault(key, List.of()).size(); }

    /** Siblings that no other sibling's context has observed. */
    static <V> List<Versioned<V>> maximal(List<Versioned<V>> all) {
        List<Versioned<V>> out = new ArrayList<>();
        for (Versioned<V> candidate : all) {
            boolean obsolete = false;
            for (Versioned<V> other : all) {
                if (other != candidate && candidate.obsoletedBy(other)) { obsolete = true; break; }
            }
            if (!obsolete && !out.contains(candidate)) out.add(candidate);
        }
        return out;
    }
}


/** Shopping cart: item -> quantity. Immutable value; operations return new carts. */
record Cart(TreeMap<String, Integer> items) {
    static Cart empty() { return new Cart(new TreeMap<>()); }

    Cart add(String item, int qty) {
        TreeMap<String, Integer> m = new TreeMap<>(items);
        m.merge(item, qty, Integer::sum);
        return new Cart(m);
    }

    Cart remove(String item) {
        TreeMap<String, Integer> m = new TreeMap<>(items);
        m.remove(item);
        return new Cart(m);
    }

    /** Dynamo's cart merge: union of items, max quantity. Removed items can come back. */
    static final MergeFunction<Cart> UNION_MAX = siblings -> {
        TreeMap<String, Integer> m = new TreeMap<>();
        for (Cart c : siblings) for (var e : c.items().entrySet()) m.merge(e.getKey(), e.getValue(), Math::max);
        return new Cart(m);
    };

    @Override public String toString() { return items.toString(); }
}

/**
 * A dispute case file on a payment: free-text notes from agents plus a status. Two agents editing the
 * same case during a partition is the ledger-adjacent conflict this lesson resolves.
 */
record CaseFile(String status, TreeSet<String> notes) {
    static final List<String> SEVERITY = List.of("OPEN", "UNDER_REVIEW", "FUNDS_HELD", "CLOSED_REFUNDED");

    static CaseFile open() { return new CaseFile("OPEN", new TreeSet<>()); }

    CaseFile withNote(String note) {
        TreeSet<String> n = new TreeSet<>(notes);
        n.add(note);
        return new CaseFile(status, n);
    }

    CaseFile withStatus(String s) {
        if (!SEVERITY.contains(s)) throw new IllegalArgumentException("unknown status " + s);
        return new CaseFile(s, new TreeSet<>(notes));
    }

    /**
     * Application merge: keep every note (union), and for status pick the most restrictive one so a
     * partition can never silently release held funds. The rule is a business decision made explicit.
     */
    static final MergeFunction<CaseFile> UNION_NOTES_STRICTEST_STATUS = siblings -> {
        TreeSet<String> notes = new TreeSet<>();
        String status = "OPEN";
        for (CaseFile c : siblings) {
            notes.addAll(c.notes());
            if (SEVERITY.indexOf(c.status()) > SEVERITY.indexOf(status)) status = c.status();
        }
        return new CaseFile(status, notes);
    };

    @Override public String toString() { return status + " notes=" + notes; }
}

public final class Main {

    public static void main(String[] args) {
        System.out.println("== Version vectors: what 'concurrent' means ==");
        VersionVector base = new VersionVector();
        VersionVector a1 = base.increment("A");          // {A=1}
        VersionVector a1b1 = a1.increment("B");           // {A=1, B=1}: B wrote after seeing A's write
        VersionVector a2 = a1.increment("A");             // {A=2}: A wrote again without seeing B
        System.out.println(a1 + " vs " + a1b1 + " -> " + a1.compare(a1b1));
        System.out.println(a1b1 + " vs " + a2 + " -> " + a1b1.compare(a2) + "  (neither saw the other: a real conflict)");
        System.out.println("merge = " + a1b1.merge(a2) + "  (the context a resolver writes back with)");

        System.out.println();
        System.out.println("== LWW: deterministic, and it throws one write away ==");
        LWWStore<String> east = new LWWStore<>(), west = new LWWStore<>();
        east.put("case-9021/note", "agent-1: customer says card was stolen on 3 Oct", 1_000, "east");
        west.put("case-9021/note", "agent-2: merchant provided signed delivery receipt", 1_002, "west");
        LWWStore<String> lww = east.merge(west);
        System.out.println("east wrote at t=1000, west wrote at t=1002 -> merged value: \"" + lww.get("case-9021/note") + "\"");
        System.out.println("agent-1's note is gone; nothing in the store records that it existed");
        LWWStore<String> skewed = new LWWStore<>();
        skewed.put("balance-note", "newer write, but this node's clock is 5s behind", 995, "slow-clock");
        skewed.put("balance-note", "older write from a node with a fast clock", 1_000, "fast-clock");
        System.out.println("clock skew: \"" + skewed.get("balance-note") + "\" wins although it happened first");

        System.out.println();
        System.out.println("== Siblings: keep both, make the application decide ==");
        SiblingStore<CaseFile> sEast = new SiblingStore<>("east"), sWest = new SiblingStore<>("west");
        VersionVector v0 = sEast.put("case-9021", CaseFile.open(), new VersionVector());
        sWest.mergeFrom(sEast);                                           // both replicas hold version {east=1}
        // partition: each agent reads {east=1}, edits, writes back with that context
        var eastRead = sEast.get("case-9021");
        sEast.put("case-9021", eastRead.siblings().get(0).value().withNote("agent-1: card reported stolen 3 Oct").withStatus("FUNDS_HELD"), eastRead.context());
        var westRead = sWest.get("case-9021");
        sWest.put("case-9021", westRead.siblings().get(0).value().withNote("agent-2: merchant has signed receipt").withStatus("UNDER_REVIEW"), westRead.context());
        sEast.mergeFrom(sWest);
        var conflicted = sEast.get("case-9021");
        System.out.println("after anti-entropy east holds " + conflicted.siblings().size() + " siblings:");
        for (Versioned<CaseFile> s : conflicted.siblings()) System.out.println("  " + s);
        CaseFile resolved = sEast.resolve("case-9021", CaseFile.UNION_NOTES_STRICTEST_STATUS);
        System.out.println("application merge (union notes, strictest status): " + resolved);
        System.out.println("siblings after resolve: " + sEast.siblingCount("case-9021") + ", version " + sEast.get("case-9021").context());

        System.out.println();
        System.out.println("== The shopping-cart anomaly from the Dynamo paper ==");
        SiblingStore<Cart> cA = new SiblingStore<>("A"), cB = new SiblingStore<>("B");
        cA.put("cart-7", Cart.empty().add("book", 1).add("pen", 2), new VersionVector());
        cB.mergeFrom(cA);
        var ra = cA.get("cart-7");
        cA.put("cart-7", ra.siblings().get(0).value().remove("pen"), ra.context());                  // A deletes pen
        var rb = cB.get("cart-7");
        cB.put("cart-7", rb.siblings().get(0).value().add("book", 1), rb.context());                 // B adds another book
        cA.mergeFrom(cB);
        Cart merged = cA.resolve("cart-7", Cart.UNION_MAX);
        System.out.println("A removed pen, B concurrently added a book -> union/max merge: " + merged);
        System.out.println("the deleted pen is back: union of states cannot see a deletion (an OR-Set, Lesson 08, can)");
    }
}
