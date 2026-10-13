// Tests for phases/01-complexity-and-core-structures/13-tries. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
        // insert / search / startsWith
        Trie<String> t = new Trie<>();
        check("empty trie: size 0, one node (the root)", t.size() == 0 && t.nodeCount() == 1);
        check("empty trie: search misses", t.search("4") == null && !t.contains("4"));
        check("empty trie: startsWith(\"\") is true (root exists)", t.startsWith(""));
        check("empty trie: startsWith(\"4\") is false", !t.startsWith("4"));
        check("insert returns null for a new key", t.insert("4111", "A") == null);
        check("insert returns the old value on replace", "A".equals(t.insert("4111", "B")) && t.size() == 1);
        t.insert("411", "C");
        t.insert("51", "D");
        check("size counts keys, not nodes", t.size() == 3 && t.nodeCount() == 1 + 4 + 2);
        check("search finds exact keys", "B".equals(t.search("4111")) && "C".equals(t.search("411")) && "D".equals(t.search("51")));
        check("search does not match a bare path", t.search("41") == null && t.search("4") == null);
        check("search does not match an extension", t.search("41111") == null);
        check("startsWith matches paths and keys", t.startsWith("4") && t.startsWith("41") && t.startsWith("4111"));
        check("startsWith rejects absent prefixes", !t.startsWith("6") && !t.startsWith("4112"));
        check("keysWithPrefix is sorted and complete", t.keysWithPrefix("4").equals(List.of("411", "4111")));
        check("keysWithPrefix of absent prefix is empty", t.keysWithPrefix("9").isEmpty());
        checkThrows("null value rejected", NullPointerException.class, () -> t.insert("1", null));

        // longest prefix match
        Trie<String> lpm = new Trie<>();
        lpm.insert("4", "VISA");
        lpm.insert("4111", "VISA-SANDBOX");
        lpm.insert("51", "MC");
        Match<String> m1 = lpm.longestPrefixMatch("4111111111111111");
        check("longest prefix wins over shorter", m1 != null && m1.prefix().equals("4111") && m1.value().equals("VISA-SANDBOX"));
        Match<String> m2 = lpm.longestPrefixMatch("4000000000000002");
        check("falls back to shorter prefix", m2 != null && m2.prefix().equals("4") && m2.value().equals("VISA"));
        check("no match when no stored key is a prefix", lpm.longestPrefixMatch("6011000000000004") == null);
        check("query shorter than any key but equal to one matches it", lpm.longestPrefixMatch("4").prefix().equals("4"));
        check("query that is a bare path matches the shorter key", lpm.longestPrefixMatch("411").prefix().equals("4"));
        check("empty query matches nothing unless \"\" is stored", lpm.longestPrefixMatch("") == null);
        lpm.insert("", "DEFAULT");
        check("empty key acts as a default route", lpm.longestPrefixMatch("9999").value().equals("DEFAULT"));

        // deletion does not break shared prefixes
        Trie<String> d = new Trie<>();
        d.insert("4111", "A");
        d.insert("411", "B");
        d.insert("4", "C");
        int before = d.nodeCount();
        check("delete of a bare path returns false and changes nothing", !d.delete("41") && d.nodeCount() == before && d.size() == 3);
        check("delete of an absent key returns false", !d.delete("5"));
        check("delete removes only the leaf node", d.delete("4111") && d.nodeCount() == before - 1 && d.size() == 2);
        check("shared prefix survives", "B".equals(d.search("411")) && "C".equals(d.search("4")) && d.startsWith("41"));
        check("deleted key is gone but its path still serves startsWith of shorter keys", d.search("4111") == null && !d.startsWith("4111"));
        check("delete of an interior key keeps its descendants", d.delete("4") && d.search("4") == null && "B".equals(d.search("411")) && d.nodeCount() == before - 1);
        check("delete of the last key prunes back to the root", d.delete("411") && d.size() == 0 && d.nodeCount() == 1);
        check("re-insert after full delete works", d.insert("51", "X") == null && "X".equals(d.search("51")));

        // range decomposition
        check("range 51-55 enumerates", BinRouter.rangeToPrefixes("51", "55").equals(List.of("51", "52", "53", "54", "55")));
        check("range of one value is that value", BinRouter.rangeToPrefixes("4", "4").equals(List.of("4")));
        List<String> ms = BinRouter.rangeToPrefixes("2221", "2720");
        check("Mastercard 2-series range is 23 prefixes", ms.size() == 23);
        check("range contains collapsed prefixes", ms.contains("23") && ms.contains("26") && ms.contains("270") && ms.contains("2720"));
        check("range excludes neighbours", !ms.contains("222") && !ms.contains("27") && !ms.contains("2721"));
        check("range covering the whole digit space", BinRouter.rangeToPrefixes("00", "99").equals(List.of("")));
        check("range ending in all nines", BinRouter.rangeToPrefixes("3528", "3599").equals(List.of("3528", "3529", "353", "354", "355", "356", "357", "358", "359")));
        checkThrows("range with unequal lengths rejected", IllegalArgumentException.class, () -> BinRouter.rangeToPrefixes("51", "555"));
        checkThrows("range with lo > hi rejected", IllegalArgumentException.class, () -> BinRouter.rangeToPrefixes("56", "55"));
        checkThrows("range with non-digits rejected", IllegalArgumentException.class, () -> BinRouter.rangeToPrefixes("5a", "55"));
        // every number in the range is covered by exactly one prefix
        boolean exact = true;
        for (int n = 2000; n <= 2999 && exact; n++) {
            String s = Integer.toString(n);
            int covering = 0;
            for (String p : ms) if (s.startsWith(p)) covering++;
            boolean inRange = n >= 2221 && n <= 2720;
            exact = covering == (inRange ? 1 : 0);
        }
        check("every number in 2221-2720 is covered exactly once and nothing outside is", exact);

        // BIN routing of sample test PANs
        BinRouter r = Main.sampleRouter();
        check("Visa test PAN routes to VISA via prefix 4", r.route("4111111111111111").equals(new Route("VISA", "Visa sandbox issuer", "4")));
        check("Mastercard 5-series test PAN", r.route("5555555555554444").equals(new Route("MASTERCARD", "Mastercard sandbox issuer", "55")));
        check("Mastercard 2-series lower bound", r.route("2221000000000009").network().equals("MASTERCARD"));
        check("Mastercard 2-series upper bound", r.route("2720999999999996").network().equals("MASTERCARD"));
        check("Amex test PAN (15 digits)", r.route("378282246310005").equals(new Route("AMEX", "Amex sandbox issuer", "37")));
        check("Discover test PAN", r.route("6011111111111117").network().equals("DISCOVER") && r.route("6011111111111117").issuer().equals("UNKNOWN"));
        check("Discover 644-649 range", r.route("6451111111111110").matchedPrefix().equals("645"));
        check("JCB test PAN", r.route("3530111333300000").network().equals("JCB"));
        checkThrows("just past the 2-series range has no route", NoRouteException.class, () -> r.route("2721000000000008"));
        checkThrows("unknown leading digit has no route", NoRouteException.class, () -> r.route("9999000000000000"));
        checkThrows("too short to be a PAN", IllegalArgumentException.class, () -> r.route("41111111111"));
        checkThrows("too long to be a PAN", IllegalArgumentException.class, () -> r.route("41111111111111111111"));
        checkThrows("non-digit PAN rejected", IllegalArgumentException.class, () -> r.route("4111-1111-1111-1111"));
        check("mask shows first six and last four only", BinRouter.mask("4111111111111111").equals("411111******1111"));
        check("mask of a short string hides everything", BinRouter.mask("12345").equals("*****"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
