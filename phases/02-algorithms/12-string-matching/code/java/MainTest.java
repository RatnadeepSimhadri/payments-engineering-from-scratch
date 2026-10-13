// Tests for phases/02-algorithms/12-string-matching. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Random;

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
        // naive baseline
        check("naive finds all", NaiveMatcher.search("abracadabra", "abra").equals(List.of(0, 7)));
        check("naive overlapping", NaiveMatcher.search("aaaa", "aa").equals(List.of(0, 1, 2)));
        check("naive no match", NaiveMatcher.search("abc", "d").isEmpty());
        check("naive pattern longer than text", NaiveMatcher.search("ab", "abc").isEmpty());
        checkThrows("naive rejects empty pattern", IllegalArgumentException.class, () -> NaiveMatcher.search("abc", ""));

        // KMP failure function: the hand-computed tables from the lesson
        check("failure(ABABAC)", Arrays.equals(KmpMatcher.failureFunction("ABABAC"), new int[] {0, 0, 1, 2, 3, 0}));
        check("failure(aaaab)", Arrays.equals(KmpMatcher.failureFunction("aaaab"), new int[] {0, 1, 2, 3, 0}));
        check("failure(abcabcabd)", Arrays.equals(KmpMatcher.failureFunction("abcabcabd"), new int[] {0, 0, 0, 1, 2, 3, 4, 5, 0}));
        check("failure(a)", Arrays.equals(KmpMatcher.failureFunction("a"), new int[] {0}));
        check("failure of distinct chars is all zero", Arrays.equals(KmpMatcher.failureFunction("abcd"), new int[] {0, 0, 0, 0}));

        // KMP search
        check("kmp matches naive on abracadabra", new KmpMatcher("abra").search("abracadabra").equals(List.of(0, 7)));
        check("kmp overlapping matches", new KmpMatcher("aa").search("aaaa").equals(List.of(0, 1, 2)));
        check("kmp pathological input", new KmpMatcher("aaaab").search("a".repeat(30) + "b").equals(List.of(26)));
        check("kmp pattern at the very end", new KmpMatcher("xyz").search("abcxyz").equals(List.of(3)));
        check("kmp pattern equals text", new KmpMatcher("abc").search("abc").equals(List.of(0)));
        check("kmp empty text", new KmpMatcher("abc").search("").isEmpty());
        check("kmp failure() returns a copy", new KmpMatcher("ab").failure() != new KmpMatcher("ab").failure());

        // Rabin-Karp
        RabinKarpMatcher rk = new RabinKarpMatcher("ACQ");
        check("rabin-karp finds all three", rk.search("ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003").equals(List.of(0, 18, 36)));
        check("rabin-karp verifies every hash match", rk.hashMatches() == 3 && rk.falsePositives() == 0);
        check("rabin-karp roll equals rehash", rollMatchesRehash("the quick brown fox", 5));
        check("rabin-karp overlapping", new RabinKarpMatcher("aa").search("aaaa").equals(List.of(0, 1, 2)));
        check("rabin-karp pattern longer than text", new RabinKarpMatcher("abcd").search("abc").isEmpty());
        check("rabin-karp unicode chars", new RabinKarpMatcher("ü").search("müller müller").equals(List.of(1, 8)));

        // the three single-pattern matchers agree on 200 seeded random inputs over a tiny alphabet
        Random rng = new Random(7);
        boolean agree = true;
        for (int t = 0; t < 200; t++) {
            String text = randomString(rng, 1 + rng.nextInt(60), 2);
            String pat = randomString(rng, 1 + rng.nextInt(4), 2);
            List<Integer> expect = NaiveMatcher.search(text, pat);
            agree &= new KmpMatcher(pat).search(text).equals(expect) && new RabinKarpMatcher(pat).search(text).equals(expect);
        }
        check("kmp and rabin-karp agree with naive on 200 random inputs", agree);

        // Aho-Corasick: the textbook example
        AhoCorasick ac = new AhoCorasick(List.of("he", "she", "his", "hers"));
        check("aho-corasick trie size", ac.nodeCount() == 10);
        check("aho-corasick ushers", ac.search("ushers").equals(List.of(
                new Match("she", 1), new Match("he", 2), new Match("hers", 2))));
        check("aho-corasick finds pattern that is a suffix of another", ac.search("she").equals(List.of(new Match("she", 0), new Match("he", 1))));
        check("aho-corasick no match", ac.search("xyz").isEmpty());
        check("aho-corasick duplicate pattern inserted once", new AhoCorasick(List.of("ab", "ab")).patterns().size() == 1);
        checkThrows("aho-corasick rejects empty pattern", IllegalArgumentException.class, () -> new AhoCorasick(List.of("a", "")));

        // Aho-Corasick agrees with running KMP per pattern, on 100 random texts
        List<String> pats = List.of("ab", "ba", "aab", "bb", "abab");
        AhoCorasick acRandom = new AhoCorasick(pats);
        boolean acAgree = true;
        for (int t = 0; t < 100; t++) {
            String text = randomString(rng, 1 + rng.nextInt(80), 2);
            List<Match> expect = new ArrayList<>();
            for (String p : pats) for (int s : new KmpMatcher(p).search(text)) expect.add(new Match(p, s));
            List<Match> got = acRandom.search(text);
            acAgree &= got.size() == expect.size() && got.containsAll(expect);
        }
        check("aho-corasick agrees with per-pattern KMP on 100 random texts", acAgree);

        // sanctions screening
        SanctionsScreener screener = new SanctionsScreener(Main.sampleList());
        check("normalize folds case and punctuation", SanctionsScreener.normalize("  Al-Rashid   Exchange, Ltd. ").equals("AL RASHID EXCHANGE LTD"));
        check("exact listed name hits", names(screener.screen("IVANOV TRADING, LLC")).equals(List.of("IVANOV", "IVANOV TRADING LLC")));
        check("listed name inside a longer name hits", names(screener.screen("Northern Star Shipping Co.")).equals(List.of("NORTHERN STAR SHIPPING", "STAR SHIPPING")));
        check("partial token does not hit (Ivanova)", screener.screen("Dmitri Ivanova").isEmpty());
        check("partial token does not hit (Rashida)", screener.screen("Rashida Patel").isEmpty());
        check("clean name is clear", screener.screen("Acme Widgets Inc").isEmpty());
        check("match start is relative to the normalized name", screener.screen("al rashid exchange house").get(0).start() == 3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean rollMatchesRehash(String text, int m) {
        RabinKarpMatcher rk = new RabinKarpMatcher(text.substring(0, m));
        long h = RabinKarpMatcher.hash(text.substring(0, m));
        for (int i = 0; i + m < text.length(); i++) {
            h = rk.roll(h, text.charAt(i), text.charAt(i + m));
            if (h != RabinKarpMatcher.hash(text.substring(i + 1, i + 1 + m))) return false;
        }
        return true;
    }

    static String randomString(Random rng, int len, int alphabet) {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < len; i++) sb.append((char) ('a' + rng.nextInt(alphabet)));
        return sb.toString();
    }

    static List<String> names(List<Match> matches) {
        List<String> out = new ArrayList<>();
        for (Match m : matches) out.add(m.pattern());
        return out;
    }
}
