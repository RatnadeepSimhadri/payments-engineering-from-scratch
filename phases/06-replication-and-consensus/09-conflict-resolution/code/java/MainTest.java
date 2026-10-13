// Tests for phases/06-replication-and-consensus/09-conflict-resolution. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) {
        // version vectors
        VersionVector empty = new VersionVector();
        VersionVector a1 = empty.increment("A");
        VersionVector a1b1 = a1.increment("B");
        VersionVector a2 = a1.increment("A");
        check("vv increment is non-mutating", empty.get("A") == 0 && a1.get("A") == 1);
        check("vv before", a1.compare(a1b1) == Ordering.BEFORE);
        check("vv after", a1b1.compare(a1) == Ordering.AFTER);
        check("vv equal", a1.compare(a1.copy()) == Ordering.EQUAL);
        check("vv concurrent", a1b1.compare(a2) == Ordering.CONCURRENT && a2.compare(a1b1) == Ordering.CONCURRENT);
        check("vv empty is before anything", empty.compare(a1) == Ordering.BEFORE);
        VersionVector m = a1b1.merge(a2);
        check("vv merge is pointwise max", m.get("A") == 2 && m.get("B") == 1);
        check("vv merge dominates both inputs", m.dominates(a1b1) && m.dominates(a2));
        check("vv merge commutative", a1b1.merge(a2).equals(a2.merge(a1b1)));

        // LWW loses data
        LWWStore<String> east = new LWWStore<>(), west = new LWWStore<>();
        east.put("k", "east-value", 1000, "east");
        west.put("k", "west-value", 1002, "west");
        LWWStore<String> merged = east.merge(west);
        check("lww keeps the later timestamp", merged.get("k").equals("west-value"));
        check("lww merge commutative", east.merge(west).get("k").equals(west.merge(east).get("k")));
        LWWStore<String> tie1 = new LWWStore<>(), tie2 = new LWWStore<>();
        tie1.put("k", "from-A", 5, "A");
        tie2.put("k", "from-B", 5, "B");
        check("lww tie broken by writer id, both directions", tie1.merge(tie2).get("k").equals("from-B") && tie2.merge(tie1).get("k").equals("from-B"));
        LWWStore<String> skew = new LWWStore<>();
        skew.put("k", "happened-first-slow-clock", 995, "slow");
        skew.put("k", "happened-second-fast-clock", 1000, "fast");
        skew.put("k", "happened-third-slow-clock", 997, "slow");
        check("lww with clock skew keeps a write that happened earlier in real time", skew.get("k").equals("happened-second-fast-clock"));
        check("lww counts every discarded write", skew.discarded() == 2);

        // sibling store: concurrent writes become siblings, causal writes replace
        SiblingStore<String> sA = new SiblingStore<>("A"), sB = new SiblingStore<>("B");
        VersionVector v1 = sA.put("k", "v1", new VersionVector());
        check("first write has version {A=1}", v1.get("A") == 1 && sA.siblingCount("k") == 1);
        sB.mergeFrom(sA);
        check("anti-entropy copies the single version", sB.siblingCount("k") == 1 && sB.get("k").values().equals(List.of("v1")));
        var readA = sA.get("k");
        sA.put("k", "v2-from-A", readA.context());
        var readB = sB.get("k");
        sB.put("k", "v2-from-B", readB.context());
        check("causal write replaces its ancestor locally", sA.siblingCount("k") == 1 && sA.get("k").values().equals(List.of("v2-from-A")));
        sA.mergeFrom(sB);
        check("concurrent writes become two siblings", sA.siblingCount("k") == 2 && sA.get("k").hasConflict());
        check("sibling context dominates both siblings", sA.get("k").context().get("A") == 2 && sA.get("k").context().get("B") == 1);
        sB.mergeFrom(sA);
        check("merge is idempotent on siblings", sB.siblingCount("k") == 2);
        sA.mergeFrom(sB);
        check("re-merging does not duplicate siblings", sA.siblingCount("k") == 2);
        // a write with the merged context collapses the siblings
        var conflicted = sA.get("k");
        sA.put("k", "v3-resolved", conflicted.context());
        check("write with merged context collapses siblings", sA.siblingCount("k") == 1 && sA.get("k").values().equals(List.of("v3-resolved")));
        check("resolved version dominates both former siblings", sA.get("k").context().get("A") == 3 && sA.get("k").context().get("B") == 1);
        // a blind write (no context) is concurrent with everything
        sA.putBlind("k", "blind");
        check("blind write adds a sibling instead of overwriting", sA.siblingCount("k") == 2);
        // a stale write (old context) also becomes a sibling rather than clobbering newer data
        SiblingStore<String> stale = new SiblingStore<>("A");
        VersionVector first = stale.put("k", "one", new VersionVector());
        stale.put("k", "two", first);
        stale.put("k", "stale-client", first);
        check("stale context write becomes a sibling, newer value survives", stale.siblingCount("k") == 2 && stale.get("k").values().contains("two"));

        // resolve with application merge: case file
        SiblingStore<CaseFile> cs = new SiblingStore<>("east");
        VersionVector c0 = cs.put("case", CaseFile.open(), new VersionVector());
        cs.put("case", CaseFile.open().withNote("n1").withStatus("FUNDS_HELD"), c0);
        SiblingStore<CaseFile> cw = new SiblingStore<>("west");
        cw.put("case", CaseFile.open().withNote("n2").withStatus("UNDER_REVIEW"), c0);
        cs.mergeFrom(cw);
        check("case file has two siblings before resolve", cs.siblingCount("case") == 2);
        CaseFile resolved = cs.resolve("case", CaseFile.UNION_NOTES_STRICTEST_STATUS);
        check("case merge keeps both notes", resolved.notes().equals(Set.of("n1", "n2")));
        check("case merge picks the strictest status", resolved.status().equals("FUNDS_HELD"));
        check("resolve writes back a single sibling", cs.siblingCount("case") == 1);
        check("resolve on a single sibling leaves it alone", cs.resolve("case", CaseFile.UNION_NOTES_STRICTEST_STATUS).equals(resolved) && cs.siblingCount("case") == 1);
        check("resolve on a missing key returns null", cs.resolve("nope", CaseFile.UNION_NOTES_STRICTEST_STATUS) == null);
        checkThrows("case file rejects unknown status", IllegalArgumentException.class, () -> CaseFile.open().withStatus("LOST"));

        // shopping cart anomaly
        SiblingStore<Cart> cA = new SiblingStore<>("A"), cB = new SiblingStore<>("B");
        cA.put("cart", Cart.empty().add("book", 1).add("pen", 2), new VersionVector());
        cB.mergeFrom(cA);
        var ra = cA.get("cart");
        cA.put("cart", ra.siblings().get(0).value().remove("pen"), ra.context());
        var rb = cB.get("cart");
        cB.put("cart", rb.siblings().get(0).value().add("book", 1), rb.context());
        cA.mergeFrom(cB);
        Cart mergedCart = cA.resolve("cart", Cart.UNION_MAX);
        check("cart merge keeps the max quantity of book", mergedCart.items().get("book") == 2);
        check("cart merge resurrects the deleted pen (the Dynamo anomaly)", mergedCart.items().containsKey("pen"));
        check("cart merge is order-independent", Cart.UNION_MAX.merge(List.of(Cart.empty().add("x", 1), Cart.empty().add("x", 3)))
                .equals(Cart.UNION_MAX.merge(List.of(Cart.empty().add("x", 3), Cart.empty().add("x", 1)))));

        // maximal() keeps only undominated versions
        Versioned<String> old = new Versioned<>("old", new Dot("A", 1), new VersionVector());           // version {A=1}
        Versioned<String> x = new Versioned<>("x", new Dot("B", 1), a1);                                 // saw A:1 -> {A=1,B=1}
        Versioned<String> y = new Versioned<>("y", new Dot("A", 2), a1);                                 // saw A:1 -> {A=2}
        List<Versioned<String>> max = SiblingStore.maximal(List.of(old, x, y));
        check("maximal drops obsoleted writes and keeps concurrent ones", max.size() == 2 && !max.contains(old) && max.contains(x) && max.contains(y));
        check("versioned.version() is context plus own dot", x.version().equals(a1b1) && y.version().equals(a2));
        check("obsoletedBy is exactly 'the other writer saw my dot'", old.obsoletedBy(x) && !x.obsoletedBy(y) && !y.obsoletedBy(x));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
