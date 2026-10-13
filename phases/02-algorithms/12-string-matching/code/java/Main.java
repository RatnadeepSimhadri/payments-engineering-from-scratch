// Lesson: phases/02-algorithms/12-string-matching/docs/en.md
// String Matching: KMP, Rabin-Karp, and Aho-Corasick
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth, Morris, Pratt, "Fast pattern matching in strings" (SIAM J. Comput. 1977); Karp & Rabin,
// "Efficient randomized pattern-matching algorithms" (IBM J. R&D 1987); Aho & Corasick, "Efficient string
// matching: an aid to bibliographic search" (CACM 1975). Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;

/** A match of one pattern in a text: the pattern and the index of its first character. */
record Match(String pattern, int start) {
    int end() { return start + pattern.length(); }
}

/** Baseline: compare the pattern at every alignment. O(n * m) worst case, and the oracle the others are tested against. */
final class NaiveMatcher {
    private NaiveMatcher() {}

    static List<Integer> search(String text, String pattern) {
        requireNonEmpty(pattern);
        List<Integer> hits = new ArrayList<>();
        for (int i = 0; i + pattern.length() <= text.length(); i++) {
            int j = 0;
            while (j < pattern.length() && text.charAt(i + j) == pattern.charAt(j)) j++;
            if (j == pattern.length()) hits.add(i);
        }
        return hits;
    }

    static void requireNonEmpty(String pattern) {
        if (pattern == null || pattern.isEmpty()) throw new IllegalArgumentException("pattern must be non-empty");
    }
}

/**
 * Knuth-Morris-Pratt. The failure function fail[i] is the length of the longest proper prefix of
 * pattern[0..i] that is also a suffix of it. On a mismatch after matching j characters, the pattern
 * slides so that its first fail[j-1] characters line up with the text already matched; the text
 * index never moves backwards. O(n + m) time, O(m) space, and no hashing or randomness.
 */
final class KmpMatcher {
    private final String pattern;
    private final int[] fail;

    KmpMatcher(String pattern) {
        NaiveMatcher.requireNonEmpty(pattern);
        this.pattern = pattern;
        this.fail = failureFunction(pattern);
    }

    static int[] failureFunction(String p) {
        int[] fail = new int[p.length()];
        int k = 0;                                   // length of the current longest prefix-suffix
        for (int i = 1; i < p.length(); i++) {
            while (k > 0 && p.charAt(i) != p.charAt(k)) k = fail[k - 1];
            if (p.charAt(i) == p.charAt(k)) k++;
            fail[i] = k;
        }
        return fail;
    }

    int[] failure() { return fail.clone(); }

    List<Integer> search(String text) {
        List<Integer> hits = new ArrayList<>();
        int j = 0;                                   // characters of pattern matched so far
        for (int i = 0; i < text.length(); i++) {
            while (j > 0 && text.charAt(i) != pattern.charAt(j)) j = fail[j - 1];
            if (text.charAt(i) == pattern.charAt(j)) j++;
            if (j == pattern.length()) {
                hits.add(i - j + 1);
                j = fail[j - 1];                     // keep going: matches may overlap
            }
        }
        return hits;
    }
}

/**
 * Rabin-Karp. Hash the pattern and every window of the text with a polynomial rolling hash; compare
 * characters only when the hashes agree. Expected O(n + m), worst case O(n * m) when every window
 * collides. The verification step makes it exact; the counters make the collision rate observable.
 */
final class RabinKarpMatcher {
    static final long BASE = 256;
    static final long MOD = 1_000_000_007L;          // a large prime so collisions are ~ 1 / MOD per window

    private final String pattern;
    private final long patternHash;
    private final long highPower;                    // BASE^(m-1) mod MOD, the weight of the outgoing char
    private int hashMatches = 0;
    private int falsePositives = 0;

    RabinKarpMatcher(String pattern) {
        NaiveMatcher.requireNonEmpty(pattern);
        this.pattern = pattern;
        this.patternHash = hash(pattern);
        long h = 1;
        for (int i = 1; i < pattern.length(); i++) h = (h * BASE) % MOD;
        this.highPower = h;
    }

    static long hash(String s) {
        long h = 0;
        for (int i = 0; i < s.length(); i++) h = (h * BASE + s.charAt(i)) % MOD;
        return h;
    }

    /** Slide the window one character: drop `out`, shift, add `in`. O(1). */
    long roll(long windowHash, char out, char in) {
        long h = (windowHash - (out * highPower) % MOD + MOD) % MOD;
        return (h * BASE + in) % MOD;
    }

    List<Integer> search(String text) {
        List<Integer> hits = new ArrayList<>();
        int m = pattern.length(), n = text.length();
        if (m > n) return hits;
        long window = hash(text.substring(0, m));
        for (int i = 0; ; i++) {
            if (window == patternHash) {
                hashMatches++;
                if (text.regionMatches(i, pattern, 0, m)) hits.add(i); else falsePositives++;
            }
            if (i + m >= n) break;
            window = roll(window, text.charAt(i), text.charAt(i + m));
        }
        return hits;
    }

    int hashMatches() { return hashMatches; }
    int falsePositives() { return falsePositives; }
}

/**
 * Aho-Corasick automaton: a trie of all patterns plus failure links (longest proper suffix of the
 * current node's string that is also a trie node) and output links (nearest node on the failure chain
 * that ends a pattern). One pass over the text finds every occurrence of every pattern:
 * O(n + m_total + z) where z is the number of matches.
 */
final class AhoCorasick {
    private static final class Node {
        final Map<Character, Node> next = new HashMap<>();
        Node fail;
        Node output;                                 // nearest node up the fail chain that ends a pattern, or null
        final List<String> patterns = new ArrayList<>();   // patterns ending exactly here
        final int depth;
        Node(int depth) { this.depth = depth; }
    }

    private final Node root = new Node(0);
    private int nodeCount = 1;
    private final List<String> patterns = new ArrayList<>();

    AhoCorasick(List<String> patterns) {
        for (String p : patterns) insert(p);
        buildLinks();
    }

    private void insert(String p) {
        NaiveMatcher.requireNonEmpty(p);
        Node cur = root;
        for (int i = 0; i < p.length(); i++) {
            char c = p.charAt(i);
            Node child = cur.next.get(c);
            if (child == null) {
                child = new Node(cur.depth + 1);
                cur.next.put(c, child);
                nodeCount++;
            }
            cur = child;
        }
        if (!cur.patterns.contains(p)) {
            cur.patterns.add(p);
            patterns.add(p);
        }
    }

    /** BFS from the root: a node's fail link is found by following its parent's fail chain until a node has the same edge. */
    private void buildLinks() {
        Deque<Node> queue = new ArrayDeque<>();
        root.fail = root;
        for (Node child : root.next.values()) {
            child.fail = root;
            queue.add(child);
        }
        while (!queue.isEmpty()) {
            Node node = queue.poll();
            node.output = node.fail != root && !node.fail.patterns.isEmpty() ? node.fail : node.fail.output;
            for (Map.Entry<Character, Node> e : node.next.entrySet()) {
                char c = e.getKey();
                Node child = e.getValue();
                Node f = node.fail;
                while (f != root && !f.next.containsKey(c)) f = f.fail;
                Node target = f.next.get(c);
                child.fail = (target == null || target == child) ? root : target;
                queue.add(child);
            }
        }
        root.output = null;
    }

    int nodeCount() { return nodeCount; }
    List<String> patterns() { return Collections.unmodifiableList(patterns); }

    /** Every occurrence of every pattern, in order of the position where the match ends. */
    List<Match> search(String text) {
        List<Match> hits = new ArrayList<>();
        Node cur = root;
        for (int i = 0; i < text.length(); i++) {
            char c = text.charAt(i);
            while (cur != root && !cur.next.containsKey(c)) cur = cur.fail;
            Node step = cur.next.get(c);
            cur = step == null ? root : step;
            for (Node out = cur.patterns.isEmpty() ? cur.output : cur; out != null; out = out.output) {
                for (String p : out.patterns) hits.add(new Match(p, i - p.length() + 1));
            }
        }
        return hits;
    }
}

/**
 * Sanctions screening: normalize a counterparty name, run the automaton over it, and report which list
 * entries appear. Normalization is where real screening systems spend their effort; this one does the
 * minimum (case, punctuation, whitespace) so the matching algorithm is the thing on display.
 */
final class SanctionsScreener {
    private final AhoCorasick automaton;

    SanctionsScreener(List<String> listedNames) {
        List<String> normalized = new ArrayList<>();
        for (String n : listedNames) normalized.add(normalize(n));
        this.automaton = new AhoCorasick(normalized);
    }

    static String normalize(String name) {
        Objects.requireNonNull(name, "name");
        StringBuilder sb = new StringBuilder();
        boolean lastSpace = true;
        for (char c : name.toUpperCase(Locale.ROOT).toCharArray()) {
            if (Character.isLetterOrDigit(c)) {
                sb.append(c);
                lastSpace = false;
            } else if (!lastSpace) {
                sb.append(' ');
                lastSpace = true;
            }
        }
        return sb.toString().strip();
    }

    /** Listed names found as whole-token sequences inside the (normalized) counterparty name, in order of appearance. */
    List<Match> screen(String counterpartyName) {
        String padded = " " + normalize(counterpartyName) + " ";
        List<Match> raw = automaton.search(padded);
        List<Match> whole = new ArrayList<>();
        for (Match m : raw) {
            int start = m.start();
            int end = m.end();
            // whole-token: bounded by spaces on both sides (the padding guarantees indices are in range)
            if (padded.charAt(start - 1) == ' ' && padded.charAt(end) == ' ') {
                whole.add(new Match(m.pattern(), start - 1));
            }
        }
        return whole;
    }

    int automatonSize() { return automaton.nodeCount(); }
}

public final class Main {
    static List<String> sampleList() {
        return List.of(
                "Ivanov Trading LLC",
                "Ivanov",
                "Northern Star Shipping",
                "Star Shipping",
                "Al-Rashid Exchange",
                "Rashid");
    }

    public static void main(String[] args) {
        System.out.println("== Naive vs KMP on a pathological input ==");
        String text = "a".repeat(30) + "b";
        String pattern = "aaaab";
        System.out.println("text = a*30 + b, pattern = " + pattern);
        System.out.println("naive hits:  " + NaiveMatcher.search(text, pattern));
        KmpMatcher kmp = new KmpMatcher(pattern);
        System.out.println("kmp hits:    " + kmp.search(text) + "  failure function " + java.util.Arrays.toString(kmp.failure()));
        KmpMatcher abab = new KmpMatcher("ABABAC");
        System.out.println("failure(ABABAC) = " + java.util.Arrays.toString(abab.failure()));

        System.out.println();
        System.out.println("== Rabin-Karp: hash the window, verify on hash match ==");
        RabinKarpMatcher rk = new RabinKarpMatcher("ACQ");
        String ledger = "ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003";
        System.out.println("text = " + ledger);
        System.out.println("hits " + rk.search(ledger) + ", hash matches " + rk.hashMatches() + ", false positives " + rk.falsePositives());

        System.out.println();
        System.out.println("== Aho-Corasick: every pattern in one pass ==");
        AhoCorasick ac = new AhoCorasick(List.of("he", "she", "his", "hers"));
        System.out.println("patterns he, she, his, hers -> " + ac.nodeCount() + " trie nodes");
        System.out.println("search(\"ushers\") = " + ac.search("ushers"));

        System.out.println();
        System.out.println("== Sanctions screening: " + sampleList().size() + " listed names, one pass per payment ==");
        SanctionsScreener screener = new SanctionsScreener(sampleList());
        System.out.println("automaton nodes: " + screener.automatonSize());
        List<String> beneficiaries = List.of(
                "IVANOV TRADING, LLC",
                "Northern Star Shipping Co.",
                "Dmitri Ivanova",
                "Star Shipping Partners",
                "al rashid exchange house",
                "Rashida Patel",
                "Acme Widgets Inc");
        for (String b : beneficiaries) {
            List<Match> hits = screener.screen(b);
            List<String> names = new ArrayList<>();
            for (Match m : hits) names.add(m.pattern());
            System.out.printf("%-30s -> %s%n", b, names.isEmpty() ? "clear" : "HIT " + names);
        }
    }
}
