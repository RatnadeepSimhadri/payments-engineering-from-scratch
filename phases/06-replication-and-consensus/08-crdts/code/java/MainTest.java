// Tests for phases/06-replication-and-consensus/08-crdts. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Random;
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
        Random rng = new Random(7);

        // G-Counter
        GCounter a = new GCounter(), b = new GCounter();
        a.increment("A", 3); b.increment("B", 5); b.increment("A", 1);
        check("gcounter value sums slots", a.value() == 3 && b.value() == 6);
        check("gcounter merge takes per-slot max, not sum", a.merge(b).value() == 8);
        check("gcounter merge is commutative", a.merge(b).equals(b.merge(a)));
        check("gcounter merge is idempotent", a.merge(b).merge(b).equals(a.merge(b)));
        check("gcounter merge leaves inputs untouched", a.value() == 3 && b.value() == 6);
        checkThrows("gcounter rejects negative increment", IllegalArgumentException.class, () -> a.increment("A", -1));
        GCounter big = new GCounter();
        big.increment("A", Long.MAX_VALUE);
        checkThrows("gcounter overflow is an error", ArithmeticException.class, () -> big.increment("A", 1));

        // PN-Counter
        PNCounter p = new PNCounter(), q = new PNCounter();
        p.increment("A", 10); p.decrement("A", 4);
        q.increment("B", 2); q.decrement("B", 7);
        check("pncounter value is P minus N", p.value() == 6 && q.value() == -5);
        check("pncounter merge", p.merge(q).value() == 1);
        check("pncounter merge commutative", p.merge(q).equals(q.merge(p)));
        check("pncounter idempotent", p.merge(p).equals(p));
        // concurrent decrements on the same replica id are the classic mistake: merge keeps the max, not both
        PNCounter x = new PNCounter(), y = new PNCounter();
        x.decrement("same", 5); y.decrement("same", 5);
        check("same replica id on two nodes loses a decrement (why ids must be unique)", x.merge(y).value() == -5);

        // G-Set
        GSet<String> s1 = new GSet<>(), s2 = new GSet<>();
        s1.add("a"); s2.add("b"); s2.add("a");
        check("gset merge is union", s1.merge(s2).elements().equals(Set.of("a", "b")));
        check("gset commutative", s1.merge(s2).equals(s2.merge(s1)));

        // OR-Set: add-wins under concurrent add/remove
        TagSource tagsA = new TagSource("A"), tagsB = new TagSource("B");
        ORSet<String> oa = new ORSet<>();
        oa.add("book", tagsA);
        ORSet<String> ob = oa.merge(new ORSet<>());
        check("orset sync carries element", ob.contains("book"));
        oa.remove("book");
        check("orset local remove hides element", !oa.contains("book"));
        ob.add("book", tagsB);
        ORSet<String> merged = oa.merge(ob);
        check("orset concurrent add wins over remove", merged.contains("book"));
        check("orset merge commutative", oa.merge(ob).equals(ob.merge(oa)));
        check("orset tombstone retained after merge", merged.tombstoneCount() == 1);
        ORSet<String> oc = merged.merge(new ORSet<>());
        oc.remove("book");
        check("orset remove after observing both tags removes element", !oc.contains("book") && oc.tombstoneCount() == 2);
        ORSet<String> stale = new ORSet<>();
        stale.remove("never-added");
        check("orset remove of unseen element is a no-op", !stale.contains("never-added") && stale.tombstoneCount() == 0);
        check("orset re-merging the stale pre-remove state does not resurrect", oc.merge(ob).contains("book") == false);

        // LWW-Register
        LWWRegister<String> r1 = new LWWRegister<>("v1", 10, "A");
        LWWRegister<String> r2 = new LWWRegister<>("v2", 12, "B");
        LWWRegister<String> r3 = new LWWRegister<>("v3", 12, "A");
        check("lww higher timestamp wins", r1.merge(r2).value().equals("v2"));
        check("lww commutative", r1.merge(r2).equals(r2.merge(r1)));
        check("lww timestamp tie broken by replica id", r2.merge(r3).value().equals("v2") && r3.merge(r2).value().equals("v2"));
        check("lww loses the other write entirely", !r1.merge(r2).value().contains("v1"));

        // Replicated balance: merge is safe, non-negative invariant is not enforceable
        ReplicatedBalance east = new ReplicatedBalance(Currency.USD);
        east.credit("east", Money.parse("100.00", Currency.USD));
        ReplicatedBalance west = east.merge(new ReplicatedBalance(Currency.USD));
        Money eighty = Money.parse("80.00", Currency.USD);
        if (!east.balance().isNegative()) east.debit("east", eighty);
        if (!west.balance().isNegative()) west.debit("west", eighty);
        check("each replica locally saw a valid balance", east.balance().minorUnits() == 2000 && west.balance().minorUnits() == 2000);
        check("merged balance goes negative despite local checks", east.merge(west).balance().minorUnits() == -6000);
        check("merged balance commutative", east.merge(west).equals(west.merge(east)));
        check("balance toString", east.merge(west).balance().toString().equals("-60.00 USD"));
        checkThrows("balance rejects currency mismatch", CurrencyMismatchException.class,
                () -> east.credit("east", Money.parse("1.00", Currency.EUR)));
        checkThrows("balance rejects negative amounts", IllegalArgumentException.class,
                () -> east.debit("east", Money.ofMinor(-1, Currency.USD)));

        // property-style laws on seeded random states
        CrdtLaws.Result gl = CrdtLaws.check(() -> Main.randomGCounter(rng), 300);
        check("gcounter laws hold on 300 random states", gl.allHold());
        CrdtLaws.Result pl = CrdtLaws.check(() -> Main.randomPNCounter(rng), 300);
        check("pncounter laws hold on 300 random states", pl.allHold());
        Map<String, TagSource> sources = Map.of("A", new TagSource("A"), "B", new TagSource("B"), "C", new TagSource("C"));
        CrdtLaws.Result ol = CrdtLaws.check(() -> Main.randomORSet(rng, sources), 300);
        check("orset laws hold on 300 random states", ol.allHold());
        CrdtLaws.Result nl = CrdtLaws.check(() -> new NaiveSumCounter(1 + rng.nextInt(100)), 100);
        check("naive sum counter fails idempotence", nl.idempotentFailures() == 100 && nl.commutativeFailures() == 0);
        List<PNCounter> replicas = new ArrayList<>();
        for (int i = 0; i < 6; i++) replicas.add(Main.randomPNCounter(rng));
        check("replicas converge regardless of merge order", CrdtLaws.converges(replicas, rng));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
