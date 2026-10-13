// Tests for phases/13-risk-fraud-and-compliance/04-sanctions-screening. Run: javac -d .out *.java && java -cp .out MainTest
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

    static boolean close(double a, double b) { return Math.abs(a - b) < 1e-9; }

    public static void main(String[] args) {
        // normalization
        check("normalize case and comma", Names.normalize("GRIMBLEWOOD, Thaddeus").equals("grimblewood thaddeus"));
        check("normalize diacritics and hyphen", Names.normalize("Ximena Fortaleza-Rúa").equals("ximena fortaleza rua"));
        check("normalize drops honorific", Names.normalize("Dr. Perpetua Nightshade").equals("perpetua nightshade"));
        check("normalize sharp s", Names.normalize("Straße").equals("strasse"));
        check("normalize collapses whitespace", Names.normalize("  Hamid   al-Qasrani ").equals("hamid al qasrani"));
        check("normalize empty", Names.normalize("...").isEmpty() && Names.tokenize("").isEmpty());

        // Jaro-Winkler: Winkler's published examples
        check("jaro martha/marhta", close(Names.jaro("martha", "marhta"), 0.9444444444444445));
        check("jaro-winkler martha/marhta", close(Names.jaroWinkler("martha", "marhta"), 0.9611111111111111));
        check("jaro-winkler dwayne/duane", close(Names.jaroWinkler("dwayne", "duane"), 0.84));
        check("jaro-winkler dixon/dicksonx", close(Names.jaroWinkler("dixon", "dicksonx"), 0.8133333333333332));
        check("jaro identical", Names.jaro("abc", "abc") == 1.0);
        check("jaro disjoint", Names.jaro("abc", "xyz") == 0.0);
        check("jaro empty", Names.jaro("", "abc") == 0.0);
        check("jaro-winkler symmetric", Names.jaroWinkler("kraskovitch", "kraskovich") == Names.jaroWinkler("kraskovich", "kraskovitch"));
        check("jaro-winkler bounded", Names.jaroWinkler("grimblewood", "grimblewod") <= 1.0 && Names.jaroWinkler("grimblewood", "grimblewod") > 0.98);
        check("prefix bonus capped at 4", Names.jaroWinkler("abcdefgh", "abcdexyz") <= Names.jaroWinkler("abcd", "abcd"));

        // Soundex: National Archives examples
        check("soundex robert", Names.soundex("Robert").equals("R163"));
        check("soundex rupert same key", Names.soundex("Rupert").equals("R163"));
        check("soundex tymczak", Names.soundex("Tymczak").equals("T522"));
        check("soundex pfister", Names.soundex("Pfister").equals("P236"));
        check("soundex ashcraft h is transparent", Names.soundex("Ashcraft").equals("A261"));
        check("soundex short name padded", Names.soundex("Lee").equals("L000"));
        check("soundex empty", Names.soundex("").isEmpty());

        // pair scoring and alignment
        NameMatcher m = new NameMatcher();
        check("exact pair", NameMatcher.pairScore("hamid", "hamid").kind() == ReasonKind.EXACT);
        check("initial pair", NameMatcher.pairScore("t", "thaddeus").kind() == ReasonKind.INITIAL);
        check("fuzzy pair", NameMatcher.pairScore("nitshade", "nightshade").kind() == ReasonKind.FUZZY);
        check("unmatched pair", NameMatcher.pairScore("marisol", "thaddeus").kind() == ReasonKind.UNMATCHED);
        check("reordered tokens score 1", m.compare("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood").score() == 1.0);
        check("reordered flag set", m.compare("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood").reordered());
        check("compare symmetric", close(m.compare("Hameed Qasrani", "Hamid al-Qasrani").score(), m.compare("Hamid al-Qasrani", "Hameed Qasrani").score()));
        check("unmatched particle not penalised", close(m.compare("Hameed Qasrani", "Hamid al-Qasrani").score(), (1.0 + Names.jaroWinkler("hameed", "hamid")) / 2));
        check("unmatched real token dilutes", m.compare("Theodora Grimblewood", "Thaddeus Grimblewood").score() == 0.5);
        check("score bounded", m.compare("Rafael Ocampo", "Octavian Blunderbuss").score() < 0.5);
        check("empty query scores zero", m.compare("", "Thaddeus Grimblewood").score() == 0.0);
        check("explain names the evidence", m.compare("Perpetua Nitshade", "Perpetua Nightshade").explain().contains("fuzzy 'nitshade'~'nightshade'"));

        // watchlist screening
        Watchlist wl = Main.demoWatchlist();
        ScreeningResult exact = wl.screen("Thaddeus Grimblewood", 0.85);
        check("exact name hits", exact.isHit() && exact.hits().get(0).entryId().equals("FIC-0001"));
        check("one hit per entry even with aliases", exact.hits().stream().filter(h -> h.entryId().equals("FIC-0001")).count() == 1);
        check("best alias chosen", wl.screen("Hameed Al Qasrani", 0.85).hits().get(0).matchedName().equals("Hameed Qasrani"));
        check("clear name is clear", !wl.screen("Marisol Everly", 0.85).isHit());
        check("initial alias produces false positive", wl.screen("Theodora Grimblewood", 0.85).isHit());
        check("higher threshold removes it", !wl.screen("Theodora Grimblewood", 0.95).isHit());
        check("hits sorted best first", wl.screen("Perpetua Nightshade", 0.5).hits().get(0).entryId().equals("FIC-0006"));
        checkThrows("threshold zero rejected", IllegalArgumentException.class, () -> wl.screen("x", 0.0));
        checkThrows("threshold above one rejected", IllegalArgumentException.class, () -> wl.screen("x", 1.5));
        checkThrows("duplicate ids rejected", IllegalArgumentException.class, () -> new Watchlist(List.of(
                new WatchlistEntry("A", "X Y", "P", List.of()), new WatchlistEntry("A", "Z W", "P", List.of()))));

        // threshold tuning
        List<LabelledPair> pairs = Main.labelledPairs();
        Evaluation at100 = Evaluation.evaluate(m, pairs, 1.0);
        check("threshold 1.0 has no false positives", at100.fp() == 0 && at100.precision() == 1.0);
        check("threshold 1.0 misses variants", at100.fn() == 8 && close(at100.recall(), 1.0 / 3));
        Evaluation at85 = Evaluation.evaluate(m, pairs, 0.85);
        check("threshold 0.85 full recall", at85.recall() == 1.0 && at85.fp() == 6);
        check("confusion matrix sums to pairs", at85.tp() + at85.fp() + at85.fn() + at85.tn() == pairs.size());
        List<Evaluation> table = Evaluation.thresholdTable(m, pairs, new double[]{0.7, 0.9, 1.0});
        boolean monotone = true;
        for (int i = 1; i < table.size(); i++) {
            if (table.get(i).recall() > table.get(i - 1).recall() || table.get(i).fp() > table.get(i - 1).fp()) monotone = false;
        }
        check("raising threshold never raises recall or fp", monotone);
        check("f1 of empty is zero", new Evaluation(0.5, 0, 0, 5, 0).f1() == 0.0);

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
