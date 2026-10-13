// Lesson: phases/01-complexity-and-core-structures/13-tries/docs/en.md
// Tries and Prefix Trees
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Fredkin, "Trie Memory" (CACM 1960); ISO/IEC 7812-1 (issuer identification numbers, PAN length 8-19 digits);
//          Mastercard 2-series BIN range 2221-2720 (public announcement, 2017). Java 21, standard library only.

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** The result of a longest-prefix lookup: which stored prefix matched and the value stored under it. */
record Match<V>(String prefix, V value) {}

/**
 * A trie keyed by strings, one node per character. Invariant: every stored key corresponds to exactly one
 * root-to-node path whose final node is terminal; every node that is neither terminal nor an ancestor of a
 * terminal node has been pruned. Children are kept sorted so iteration order is deterministic.
 */
final class Trie<V> {
    private static final class Node<V> {
        final Map<Character, Node<V>> children = new TreeMap<>();
        boolean terminal;
        V value;
    }

    private final Node<V> root = new Node<>();
    private int size;          // number of keys
    private int nodeCount = 1; // including the root

    /** Insert or replace. Returns the previous value for this exact key, or null. */
    V insert(String key, V value) {
        Objects.requireNonNull(key, "key");
        Objects.requireNonNull(value, "value");
        Node<V> node = root;
        for (int i = 0; i < key.length(); i++) {
            char c = key.charAt(i);
            Node<V> next = node.children.get(c);
            if (next == null) {
                next = new Node<>();
                node.children.put(c, next);
                nodeCount++;
            }
            node = next;
        }
        V old = node.terminal ? node.value : null;
        if (!node.terminal) size++;
        node.terminal = true;
        node.value = value;
        return old;
    }

    /** Exact-match lookup: the value stored under this key, or null. "41" does not match a stored "411". */
    V search(String key) {
        Node<V> node = walk(key);
        return node != null && node.terminal ? node.value : null;
    }

    boolean contains(String key) { return search(key) != null; }

    /** True if any stored key begins with this prefix (including the prefix itself). */
    boolean startsWith(String prefix) { return walk(prefix) != null; }

    /**
     * The longest stored key that is a prefix of the query, with its value. Walks the query once, remembering
     * the deepest terminal node seen, so a stored "4" and a stored "4111" both match "41111..." and "4111" wins.
     */
    Match<V> longestPrefixMatch(String query) {
        Node<V> node = root;
        Match<V> best = root.terminal ? new Match<>("", root.value) : null;
        for (int i = 0; i < query.length(); i++) {
            node = node.children.get(query.charAt(i));
            if (node == null) break;
            if (node.terminal) best = new Match<>(query.substring(0, i + 1), node.value);
        }
        return best;
    }

    /**
     * Remove a key. Returns false if it was not stored. Prunes every node on the path that is left with no
     * children and no terminal mark, so shared prefixes of other keys are untouched and nothing leaks.
     */
    boolean delete(String key) {
        List<Node<V>> path = new ArrayList<>(key.length() + 1);
        Node<V> node = root;
        path.add(node);
        for (int i = 0; i < key.length(); i++) {
            node = node.children.get(key.charAt(i));
            if (node == null) return false;
            path.add(node);
        }
        if (!node.terminal) return false;
        node.terminal = false;
        node.value = null;
        size--;
        for (int i = key.length(); i > 0; i--) {
            Node<V> child = path.get(i);
            if (child.terminal || !child.children.isEmpty()) break;
            path.get(i - 1).children.remove(key.charAt(i - 1));
            nodeCount--;
        }
        return true;
    }

    /** Every stored key that begins with the prefix, in sorted order. The autocomplete operation. */
    List<String> keysWithPrefix(String prefix) {
        List<String> out = new ArrayList<>();
        Node<V> start = walk(prefix);
        if (start != null) collect(start, new StringBuilder(prefix), out);
        return out;
    }

    private void collect(Node<V> node, StringBuilder acc, List<String> out) {
        if (node.terminal) out.add(acc.toString());
        for (Map.Entry<Character, Node<V>> e : node.children.entrySet()) {
            acc.append(e.getKey());
            collect(e.getValue(), acc, out);
            acc.setLength(acc.length() - 1);
        }
    }

    private Node<V> walk(String s) {
        Node<V> node = root;
        for (int i = 0; i < s.length() && node != null; i++) node = node.children.get(s.charAt(i));
        return node;
    }

    int size() { return size; }
    int nodeCount() { return nodeCount; }
}

/** Where a card number routes: the network that owns the BIN, the issuer if known, and the prefix that decided it. */
record Route(String network, String issuer, String matchedPrefix) {}

final class NoRouteException extends RuntimeException {
    NoRouteException(String message) { super(message); }
}

/**
 * BIN routing: the leading digits of a PAN (the Bank Identification Number, ISO/IEC 7812 "issuer identification
 * number") decide which network and which issuer a transaction goes to. Networks publish both prefixes ("4" is
 * Visa) and numeric ranges (Mastercard's 2221-2720); ranges are decomposed into the minimal set of prefixes so
 * one longest-prefix-match answers every lookup.
 */
final class BinRouter {
    private final Trie<String> networks = new Trie<>();
    private final Trie<String> issuers = new Trie<>();

    void addNetwork(String prefix, String network) { networks.insert(requireDigits(prefix), network); }

    void addNetworkRange(String lo, String hi, String network) {
        for (String prefix : rangeToPrefixes(lo, hi)) networks.insert(prefix, network);
    }

    void addIssuer(String prefix, String issuer) { issuers.insert(requireDigits(prefix), issuer); }

    Route route(String pan) {
        requireDigits(pan);
        if (pan.length() < 12 || pan.length() > 19) {
            throw new IllegalArgumentException("PAN must be 12-19 digits, got " + pan.length());
        }
        Match<String> network = networks.longestPrefixMatch(pan);
        if (network == null) throw new NoRouteException("no network owns a prefix of " + mask(pan));
        Match<String> issuer = issuers.longestPrefixMatch(pan);
        return new Route(network.value(), issuer == null ? "UNKNOWN" : issuer.value(), network.prefix());
    }

    /**
     * Decompose a closed numeric range [lo, hi] of equal-length digit strings into the fewest prefixes that
     * cover exactly the numbers in it. Greedy: from lo, take the widest prefix (most trailing zeros replaced)
     * whose block still ends at or before hi, emit it, and continue from the number after that block.
     */
    static List<String> rangeToPrefixes(String lo, String hi) {
        requireDigits(lo);
        requireDigits(hi);
        if (lo.length() != hi.length()) throw new IllegalArgumentException("lo and hi must have equal length: " + lo + ", " + hi);
        if (lo.compareTo(hi) > 0) throw new IllegalArgumentException("lo must not exceed hi: " + lo + " > " + hi);
        int len = lo.length();
        List<String> out = new ArrayList<>();
        String cur = lo;
        while (cur.compareTo(hi) <= 0) {
            int k = 0;      // number of trailing digits the prefix will leave free
            while (k < len && cur.charAt(len - 1 - k) == '0'
                    && (cur.substring(0, len - 1 - k) + "9".repeat(k + 1)).compareTo(hi) <= 0) {
                k++;
            }
            String prefix = cur.substring(0, len - k);
            out.add(prefix);
            String blockEnd = prefix + "9".repeat(k);
            if (blockEnd.equals("9".repeat(len))) break;             // the block ran to the end of the digit space
            cur = increment(blockEnd);
        }
        return out;
    }

    /** Add one to a digit string of fixed width; the caller guarantees it is not all nines. */
    private static String increment(String digits) {
        char[] d = digits.toCharArray();
        int i = d.length - 1;
        while (d[i] == '9') { d[i] = '0'; i--; }
        d[i]++;
        return new String(d);
    }

    static String requireDigits(String s) {
        if (s == null || s.isEmpty() || !s.chars().allMatch(Character::isDigit)) {
            throw new IllegalArgumentException("expected a non-empty digit string, got " + s);
        }
        return s;
    }

    /** First six and last four digits: the only form of a PAN that may ever be logged (PCI DSS 3.4). */
    static String mask(String pan) {
        if (pan.length() <= 10) return "*".repeat(pan.length());
        return pan.substring(0, 6) + "*".repeat(pan.length() - 10) + pan.substring(pan.length() - 4);
    }

    int networkPrefixCount() { return networks.size(); }
}

public final class Main {
    static String show(String value) { return value == null ? "(none)" : value; }

    static String show(Match<String> m) { return m == null ? "(none)" : "\"" + m.prefix() + "\" -> " + m.value(); }

    /** Test BINs and PANs: these are the publicly documented test numbers every gateway sandbox accepts. */
    static BinRouter sampleRouter() {
        BinRouter r = new BinRouter();
        r.addNetwork("4", "VISA");
        r.addNetworkRange("51", "55", "MASTERCARD");
        r.addNetworkRange("2221", "2720", "MASTERCARD");
        r.addNetwork("34", "AMEX");
        r.addNetwork("37", "AMEX");
        r.addNetwork("6011", "DISCOVER");
        r.addNetwork("65", "DISCOVER");
        r.addNetworkRange("644", "649", "DISCOVER");
        r.addNetworkRange("3528", "3589", "JCB");
        r.addIssuer("411111", "Visa sandbox issuer");
        r.addIssuer("555555", "Mastercard sandbox issuer");
        r.addIssuer("378282", "Amex sandbox issuer");
        return r;
    }

    public static void main(String[] args) {
        System.out.println("== A trie of card prefixes ==");
        Trie<String> t = new Trie<>();
        t.insert("4", "VISA");
        t.insert("4111", "VISA (sandbox range)");
        t.insert("41", "VISA (sub-range)");
        t.insert("51", "MASTERCARD");
        System.out.printf("keys=%d nodes=%d%n", t.size(), t.nodeCount());
        System.out.println("search(\"41\")   = " + show(t.search("41")));
        System.out.println("search(\"411\")  = " + show(t.search("411")) + "   (a path, not a key)");
        System.out.println("startsWith(\"411\") = " + t.startsWith("411"));
        System.out.println("longestPrefixMatch(\"4111111111111111\") = " + show(t.longestPrefixMatch("4111111111111111")));
        System.out.println("longestPrefixMatch(\"4000000000000002\") = " + show(t.longestPrefixMatch("4000000000000002")));
        System.out.println("longestPrefixMatch(\"6011000000000004\") = " + show(t.longestPrefixMatch("6011000000000004")));
        System.out.println("keysWithPrefix(\"4\") = " + t.keysWithPrefix("4"));
        t.delete("4111");
        System.out.printf("after delete(\"4111\"): keys=%d nodes=%d search(\"41\")=%s startsWith(\"41\")=%s%n",
                t.size(), t.nodeCount(), show(t.search("41")), t.startsWith("41"));

        System.out.println("\n== A numeric range becomes a handful of prefixes ==");
        List<String> ms = BinRouter.rangeToPrefixes("2221", "2720");
        System.out.println("2221-2720 -> " + ms.size() + " prefixes: " + ms);
        System.out.println("51-55     -> " + BinRouter.rangeToPrefixes("51", "55"));
        System.out.println("3528-3589 -> " + BinRouter.rangeToPrefixes("3528", "3589"));

        System.out.println("\n== BIN routing with longest-prefix match ==");
        BinRouter router = sampleRouter();
        System.out.println("network prefixes stored: " + router.networkPrefixCount());
        for (String pan : List.of("4111111111111111", "5555555555554444", "2221000000000009", "2720999999999996",
                "378282246310005", "6011111111111117", "6451111111111110", "3530111333300000")) {
            Route route = router.route(pan);
            System.out.printf("%-20s -> %-10s issuer=%-27s via prefix %s%n",
                    BinRouter.mask(pan), route.network(), route.issuer(), route.matchedPrefix());
        }
        for (String pan : List.of("2721000000000008", "9999000000000000")) {
            try {
                router.route(pan);
            } catch (NoRouteException e) {
                System.out.println("rejected: " + e.getMessage());
            }
        }
    }
}
