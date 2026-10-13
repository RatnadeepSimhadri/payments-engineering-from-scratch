// Tests for phases/01-complexity-and-core-structures/06-hash-maps-from-scratch. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.function.ToIntFunction;

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

    static void sharedContract(String label, SimpleMap<String, Integer> map) {
        check(label + ": empty map has size 0", map.size() == 0);
        check(label + ": get on missing key is null", map.get("nope") == null);
        check(label + ": put returns null for a new key", map.put("a", 1) == null);
        check(label + ": put returns the old value on replace", map.put("a", 2) == 1);
        check(label + ": replace does not grow size", map.size() == 1);
        check(label + ": get after put", map.get("a") == 2);
        check(label + ": containsKey true", map.containsKey("a"));
        check(label + ": containsKey false", !map.containsKey("b"));
        check(label + ": remove returns the value", map.remove("a") == 2);
        check(label + ": remove of missing key is null", map.remove("a") == null);
        check(label + ": size back to 0", map.size() == 0);
        checkThrows(label + ": null key rejected", NullPointerException.class, () -> map.put(null, 1));

        // resize preserves every entry
        List<String> keys = Main.keys(1_000);
        for (int i = 0; i < keys.size(); i++) map.put(keys.get(i), i);
        boolean allPresent = true;
        for (int i = 0; i < keys.size(); i++) {
            Integer v = map.get(keys.get(i));
            if (v == null || v != i) { allPresent = false; break; }
        }
        check(label + ": 1000 inserts all retrievable after resizes", allPresent);
        check(label + ": size is 1000", map.size() == 1000);
        check(label + ": capacity grew past the default", map.capacity() > 8);
        check(label + ": load factor within bound", map.loadFactor() <= 0.75);
        for (int i = 0; i < keys.size(); i += 3) map.remove(keys.get(i));
        boolean removedGone = true, keptPresent = true;
        for (int i = 0; i < keys.size(); i++) {
            Integer v = map.get(keys.get(i));
            if (i % 3 == 0) removedGone &= (v == null); else keptPresent &= (v != null && v == i);
        }
        check(label + ": removed keys are gone", removedGone);
        check(label + ": kept keys survive removals", keptPresent);
        check(label + ": size after removals", map.size() == 1000 - 334);
    }

    public static void main(String[] args) {
        // hash function is the cross-language anchor
        check("fnv1a of empty string is the offset basis", Hashers.fnv1a("") == 0x811c9dc5);
        check("fnv1a(\"a\") matches the published vector", Hashers.fnv1a("a") == 0xe40c292c);
        check("fnv1a(\"idem-000001\") is stable", Hashers.fnv1a("idem-000001") == Hashers.fnv1a("idem-000001"));
        check("indexFor stays inside a power-of-two table", Hashers.indexFor(0xdeadbeef, 16) >= 0 && Hashers.indexFor(0xdeadbeef, 16) < 16);
        checkThrows("non power-of-two capacity rejected", IllegalArgumentException.class, () -> new ChainedHashMap<String, Integer>(Hashers::fnv1a, 12));

        sharedContract("chained", new ChainedHashMap<>(Hashers::fnv1a));
        sharedContract("open", new OpenAddressingHashMap<>(Hashers::fnv1a));

        // resize bookkeeping
        ChainedHashMap<String, Integer> c = new ChainedHashMap<>(Hashers::fnv1a);
        for (int i = 0; i < 7; i++) c.put("k" + i, i);
        check("chained: resizes once when 7th key exceeds 0.75 * 8", c.resizes() == 1 && c.capacity() == 16);
        OpenAddressingHashMap<String, Integer> o = new OpenAddressingHashMap<>(Hashers::fnv1a);
        for (int i = 0; i < 5; i++) o.put("k" + i, i);
        check("open: resizes when 5th key would exceed 0.5 * 8", o.resizes() == 1 && o.capacity() == 16);

        // tombstones: a deliberately constant hasher makes slot positions predictable
        ToIntFunction<String> zero = Hashers.constant();
        OpenAddressingHashMap<String, String> t = new OpenAddressingHashMap<>(zero, 16);
        t.put("A", "a"); t.put("B", "b"); t.put("C", "c");
        check("tombstone: A,B,C occupy slots 0,1,2", t.slotOf("A") == 0 && t.slotOf("B") == 1 && t.slotOf("C") == 2);
        t.remove("B");
        check("tombstone: removing B leaves one tombstone", t.tombstones() == 1 && t.size() == 2);
        check("tombstone: C is still reachable through B's grave", "c".equals(t.get("C")));
        check("tombstone: B itself is gone", t.get("B") == null && !t.containsKey("B"));
        t.put("D", "d");
        check("tombstone: D reuses B's slot instead of extending the cluster", t.slotOf("D") == 1 && t.tombstones() == 0);
        check("tombstone: D displacement counted from home", t.collisions() == 0 + 1 + 2 + 1);
        t.put("C", "c2");
        check("tombstone: replacing C keeps it in slot 2", t.slotOf("C") == 2 && "c2".equals(t.get("C")) && t.size() == 3);

        // tombstones are dropped on resize
        OpenAddressingHashMap<String, Integer> r = new OpenAddressingHashMap<>(Hashers::fnv1a, 8);
        for (int i = 0; i < 4; i++) r.put("x" + i, i);
        for (int i = 0; i < 4; i++) r.remove("x" + i);
        check("tombstones accumulate", r.tombstones() == 4 && r.size() == 0);
        r.put("y0", 0); r.put("y1", 1);                 // 0 + 4 + 1 > 0.5 * 8 forces a resize on the first put
        check("resize drops tombstones", r.tombstones() == 0 && r.capacity() == 16 && r.get("y1") == 1);

        // worst case: every key in one bucket
        ChainedHashMap<String, Integer> badChain = new ChainedHashMap<>(zero);
        OpenAddressingHashMap<String, Integer> badOpen = new OpenAddressingHashMap<>(zero);
        List<String> keys = Main.keys(64);
        for (int i = 0; i < 64; i++) { badChain.put(keys.get(i), i); badOpen.put(keys.get(i), i); }
        boolean chainOk = true, openOk = true;
        for (int i = 0; i < 64; i++) { chainOk &= badChain.get(keys.get(i)) == i; openOk &= badOpen.get(keys.get(i)) == i; }
        check("bad hash: chaining still finds every key", chainOk);
        check("bad hash: chaining collisions = n-1", badChain.collisions() == 63);
        check("bad hash: one chain holds everything", badChain.maxChainLength() == 64);
        check("bad hash: probing still finds every key", openOk);
        check("bad hash: probing displacement = 0+1+...+63", badOpen.collisions() == 2016);
        check("bad hash: probe run spans all 64 keys", badOpen.maxProbeRun() == 64);
        ChainedHashMap<String, Integer> goodChain = new ChainedHashMap<>(Hashers::fnv1a);
        for (int i = 0; i < 64; i++) goodChain.put(keys.get(i), i);
        check("good hash: far fewer collisions than the bad one", goodChain.collisions() < badChain.collisions() / 2);
        check("good hash: max chain is short", goodChain.maxChainLength() <= 4);

        // idempotency store: handler runs once per key
        IdempotencyStore store = new IdempotencyStore(new OpenAddressingHashMap<>(Hashers::fnv1a));
        int[] runs = {0};
        Outcome first = store.handle("k1", () -> { runs[0]++; return new StoredResponse(201, "created"); });
        Outcome second = store.handle("k1", () -> { runs[0]++; return new StoredResponse(500, "should not run"); });
        check("store: first call runs the handler", !first.replayed() && first.response().status() == 201);
        check("store: second call replays without running the handler", second.replayed() && second.response().equals(first.response()));
        check("store: handler ran exactly once", runs[0] == 1 && store.handlerRuns() == 1);
        Outcome other = store.handle("k2", () -> new StoredResponse(201, "other"));
        check("store: a different key is a fresh request", !other.replayed() && store.size() == 2);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
