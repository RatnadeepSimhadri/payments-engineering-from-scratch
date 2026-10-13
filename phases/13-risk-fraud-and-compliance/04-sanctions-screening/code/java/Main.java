// Lesson: phases/13-risk-fraud-and-compliance/04-sanctions-screening/docs/en.md
// Sanctions Screening: Name Matching Under Uncertainty
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Winkler, "String Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter Model of
//          Record Linkage" (1990); U.S. National Archives, "The Soundex Indexing System"; OFAC Sanctions List Search FAQ.
// Java 21, standard library only. Same design as the Python and Go versions. Every watchlist name is fictional.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;

/** Step 1-3: normalization, Jaro-Winkler, and Soundex. Pure functions on strings. */
final class Names {
    private Names() {}

    /** Explicit diacritic table (not java.text.Normalizer) so the three languages fold identically. */
    static final Map<Character, String> DIACRITICS = new HashMap<>();
    static {
        String[][] table = {
            {"a", "àáâãäåāăą"}, {"c", "çćĉċč"}, {"d", "ďđ"}, {"e", "èéêëēĕėęě"}, {"g", "ĝğġģ"}, {"h", "ĥħ"},
            {"i", "ìíîïĩīĭįı"}, {"j", "ĵ"}, {"k", "ķ"}, {"l", "ĺļľŀł"}, {"n", "ñńņňŉ"}, {"o", "òóôõöøōŏő"},
            {"r", "ŕŗř"}, {"s", "śŝşš"}, {"t", "ţťŧ"}, {"u", "ùúûüũūŭůűų"}, {"w", "ŵ"}, {"y", "ýÿŷ"}, {"z", "źżž"},
        };
        for (String[] row : table) {
            for (char v : row[1].toCharArray()) {
                DIACRITICS.put(v, row[0]);
                DIACRITICS.put(Character.toUpperCase(v), row[0]);
            }
        }
        DIACRITICS.put('ß', "ss");
        DIACRITICS.put('æ', "ae");
        DIACRITICS.put('Æ', "ae");
        DIACRITICS.put('œ', "oe");
        DIACRITICS.put('Œ', "oe");
    }

    /** Titles carry no identity and are removed before matching. */
    static final Set<String> STOPWORDS = Set.of("mr", "mrs", "ms", "dr", "jr", "sr", "prof", "sir");
    /** Particles are matched when present on both sides; an unmatched particle is not evidence against a match. */
    static final Set<String> PARTICLES = Set.of("al", "el", "bin", "ibn", "abu", "de", "del", "da", "di", "la", "le",
            "van", "von", "der", "den");

    /** Lowercase ASCII letters and digits separated by single spaces; honorifics removed. */
    static String normalize(String name) {
        StringBuilder out = new StringBuilder();
        for (int i = 0; i < name.length(); i++) {
            char ch = name.charAt(i);
            String folded = DIACRITICS.get(ch);
            if (folded != null) {
                out.append(folded);
            } else if (ch < 128 && Character.isLetterOrDigit(ch)) {
                out.append(Character.toLowerCase(ch));
            } else {
                out.append(' '); // punctuation, hyphens, apostrophes, anything else
            }
        }
        List<String> tokens = new ArrayList<>();
        for (String t : out.toString().trim().split("\\s+")) {
            if (!t.isEmpty() && !STOPWORDS.contains(t)) tokens.add(t);
        }
        return String.join(" ", tokens);
    }

    static List<String> tokenize(String normalized) {
        if (normalized.isEmpty()) return List.of();
        return Arrays.asList(normalized.split(" "));
    }

    /** Jaro similarity in [0, 1]: matches within a window, half the transpositions count against it. */
    static double jaro(String a, String b) {
        if (a.equals(b)) return 1.0;
        if (a.isEmpty() || b.isEmpty()) return 0.0;
        int window = Math.max(a.length(), b.length()) / 2 - 1;
        if (window < 0) window = 0;
        boolean[] aMatched = new boolean[a.length()];
        boolean[] bMatched = new boolean[b.length()];
        int matches = 0;
        for (int i = 0; i < a.length(); i++) {
            int lo = Math.max(0, i - window);
            int hi = Math.min(b.length(), i + window + 1);
            for (int j = lo; j < hi; j++) {
                if (!bMatched[j] && b.charAt(j) == a.charAt(i)) {
                    aMatched[i] = true;
                    bMatched[j] = true;
                    matches++;
                    break;
                }
            }
        }
        if (matches == 0) return 0.0;
        int transpositions = 0;
        int j = 0;
        for (int i = 0; i < a.length(); i++) {
            if (!aMatched[i]) continue;
            while (!bMatched[j]) j++;
            if (a.charAt(i) != b.charAt(j)) transpositions++;
            j++;
        }
        int half = transpositions / 2;
        double m = matches;
        return (m / a.length() + m / b.length() + (m - half) / m) / 3.0;
    }

    static final double JW_PREFIX_SCALE = 0.1;
    static final int JW_MAX_PREFIX = 4;

    /** Winkler's boost: a shared prefix of up to four characters pulls the score toward 1. */
    static double jaroWinkler(String a, String b) {
        double base = jaro(a, b);
        int prefix = 0;
        int n = Math.min(a.length(), b.length());
        for (int i = 0; i < n; i++) {
            if (a.charAt(i) != b.charAt(i) || prefix == JW_MAX_PREFIX) break;
            prefix++;
        }
        return base + prefix * JW_PREFIX_SCALE * (1.0 - base);
    }

    private static char soundexCode(char c) {
        return switch (c) {
            case 'b', 'f', 'p', 'v' -> '1';
            case 'c', 'g', 'j', 'k', 'q', 's', 'x', 'z' -> '2';
            case 'd', 't' -> '3';
            case 'l' -> '4';
            case 'm', 'n' -> '5';
            case 'r' -> '6';
            default -> 0;
        };
    }

    /** American Soundex: first letter plus three digits; h and w are transparent, vowels separate. */
    static String soundex(String token) {
        StringBuilder letters = new StringBuilder();
        for (char c : token.toLowerCase(Locale.ROOT).toCharArray()) {
            if (c >= 'a' && c <= 'z') letters.append(c);
        }
        if (letters.isEmpty()) return "";
        StringBuilder key = new StringBuilder().append(Character.toUpperCase(letters.charAt(0)));
        char last = soundexCode(letters.charAt(0));
        for (int i = 1; i < letters.length() && key.length() < 4; i++) {
            char c = letters.charAt(i);
            if (c == 'h' || c == 'w') continue;
            char code = soundexCode(c);
            if (code == 0) {
                last = 0; // a vowel separates: the same code may be emitted again
                continue;
            }
            if (code != last) {
                key.append(code);
                last = code;
            }
        }
        while (key.length() < 4) key.append('0');
        return key.toString();
    }
}

enum ReasonKind { EXACT, FUZZY, PHONETIC, INITIAL, PARTICLE, UNMATCHED }

/** Step 4: one aligned token pair and the evidence it contributed. */
record MatchReason(ReasonKind kind, String queryToken, String listToken, double score) {
    @Override
    public String toString() {
        String either = queryToken.isEmpty() ? listToken : queryToken;
        return switch (kind) {
            case UNMATCHED -> String.format("unmatched '%s' (+0.00)", either);
            case PARTICLE -> String.format("particle '%s' ignored", either);
            case PHONETIC -> String.format("phonetic '%s'~'%s' %s (+%.2f)", queryToken, listToken, Names.soundex(queryToken), score);
            default -> String.format("%s '%s'~'%s' (+%.2f)", kind.name().toLowerCase(Locale.ROOT), queryToken, listToken, score);
        };
    }
}

record MatchScore(double score, List<MatchReason> reasons, boolean reordered) {
    String explain() {
        List<String> parts = new ArrayList<>();
        for (MatchReason r : reasons) parts.add(r.toString());
        if (reordered) parts.add("token order ignored");
        return String.format("score=%.3f [%s]", score, String.join("; ", parts));
    }
}

/** Greedy best-pair alignment of query tokens to list tokens. Order does not matter; every
 *  token on the longer side must find a partner or the score is diluted. */
final class NameMatcher {
    static final double PHONETIC_FLOOR = 0.85;
    static final double INITIAL_SCORE = 0.85;
    static final double FUZZY_FLOOR = 0.70; // a pair below this contributes nothing: noise, not evidence

    private record Pair(double score, int i, int j, ReasonKind kind) {}

    static MatchReason pairScore(String q, String c) {
        if (q.equals(c)) return new MatchReason(ReasonKind.EXACT, q, c, 1.0);
        if ((q.length() == 1 && c.startsWith(q)) || (c.length() == 1 && q.startsWith(c))) {
            return new MatchReason(ReasonKind.INITIAL, q, c, INITIAL_SCORE);
        }
        double jw = Names.jaroWinkler(q, c);
        if (Names.soundex(q).equals(Names.soundex(c)) && jw < PHONETIC_FLOOR) {
            return new MatchReason(ReasonKind.PHONETIC, q, c, PHONETIC_FLOOR);
        }
        if (jw >= FUZZY_FLOOR) return new MatchReason(ReasonKind.FUZZY, q, c, jw);
        return new MatchReason(ReasonKind.UNMATCHED, q, c, 0.0);
    }

    MatchScore compare(String query, String candidate) {
        List<String> q = Names.tokenize(Names.normalize(query));
        List<String> c = Names.tokenize(Names.normalize(candidate));
        if (q.isEmpty() || c.isEmpty()) return new MatchScore(0.0, List.of(), false);
        List<Pair> pairs = new ArrayList<>();
        for (int i = 0; i < q.size(); i++) {
            for (int j = 0; j < c.size(); j++) {
                MatchReason r = pairScore(q.get(i), c.get(j));
                if (r.kind() != ReasonKind.UNMATCHED) pairs.add(new Pair(r.score(), i, j, r.kind()));
            }
        }
        // best first; ties broken by position so the alignment is deterministic
        pairs.sort((x, y) -> {
            int byScore = Double.compare(y.score, x.score);
            if (byScore != 0) return byScore;
            if (x.i != y.i) return Integer.compare(x.i, y.i);
            return Integer.compare(x.j, y.j);
        });
        boolean[] usedQ = new boolean[q.size()];
        boolean[] usedC = new boolean[c.size()];
        List<MatchReason> reasons = new ArrayList<>();
        double total = 0.0;
        boolean reordered = false;
        for (Pair p : pairs) {
            if (usedQ[p.i] || usedC[p.j]) continue;
            usedQ[p.i] = true;
            usedC[p.j] = true;
            total += p.score;
            if (p.i != p.j) reordered = true;
            reasons.add(new MatchReason(p.kind, q.get(p.i), c.get(p.j), p.score));
        }
        int qCount = q.size(), cCount = c.size();
        for (int i = 0; i < q.size(); i++) {
            if (usedQ[i]) continue;
            if (Names.PARTICLES.contains(q.get(i))) {
                qCount--;
                reasons.add(new MatchReason(ReasonKind.PARTICLE, q.get(i), "", 0.0));
            } else {
                reasons.add(new MatchReason(ReasonKind.UNMATCHED, q.get(i), "", 0.0));
            }
        }
        for (int j = 0; j < c.size(); j++) {
            if (usedC[j]) continue;
            if (Names.PARTICLES.contains(c.get(j))) {
                cCount--;
                reasons.add(new MatchReason(ReasonKind.PARTICLE, "", c.get(j), 0.0));
            } else {
                reasons.add(new MatchReason(ReasonKind.UNMATCHED, "", c.get(j), 0.0));
            }
        }
        int denominator = Math.max(Math.max(qCount, cCount), 1);
        return new MatchScore(total / denominator, List.copyOf(reasons), reordered);
    }
}

/** Step 5: the watchlist and the screening result. */
record WatchlistEntry(String entryId, String name, String program, List<String> aliases) {
    List<String> allNames() {
        List<String> all = new ArrayList<>();
        all.add(name);
        all.addAll(aliases);
        return all;
    }
}

record Hit(String entryId, String matchedName, String program, MatchScore match) {
    String explain() {
        return String.format("%s '%s' [%s] %s", entryId, matchedName, program, match.explain());
    }
}

record ScreeningResult(String query, String normalized, double threshold, List<Hit> hits) {
    boolean isHit() { return !hits.isEmpty(); }

    String explain() {
        StringBuilder sb = new StringBuilder(String.format("screen '%s' -> '%s' @ %.2f: %s", query, normalized, threshold,
                hits.isEmpty() ? "CLEAR" : "HIT"));
        for (Hit h : hits) sb.append("\n  ").append(h.explain());
        return sb.toString();
    }
}

final class Watchlist {
    final List<WatchlistEntry> entries;
    final NameMatcher matcher;

    Watchlist(List<WatchlistEntry> entries, NameMatcher matcher) {
        Set<String> ids = new java.util.HashSet<>();
        for (WatchlistEntry e : entries) {
            if (!ids.add(e.entryId())) throw new IllegalArgumentException("duplicate watchlist entry id: " + e.entryId());
        }
        this.entries = List.copyOf(entries);
        this.matcher = matcher;
    }

    Watchlist(List<WatchlistEntry> entries) { this(entries, new NameMatcher()); }

    ScreeningResult screen(String name, double threshold) {
        if (!(threshold > 0.0 && threshold <= 1.0)) throw new IllegalArgumentException("threshold must be in (0, 1]");
        List<Hit> hits = new ArrayList<>();
        for (WatchlistEntry entry : entries) {
            Hit best = null;
            for (String candidate : entry.allNames()) { // best alias per entry: one hit per listed person
                MatchScore m = matcher.compare(name, candidate);
                if (best == null || m.score() > best.match().score()) best = new Hit(entry.entryId(), candidate, entry.program(), m);
            }
            if (best != null && best.match().score() >= threshold) hits.add(best);
        }
        hits.sort((a, b) -> {
            int byScore = Double.compare(b.match().score(), a.match().score());
            return byScore != 0 ? byScore : a.entryId().compareTo(b.entryId());
        });
        return new ScreeningResult(name, Names.normalize(name), threshold, List.copyOf(hits));
    }
}

/** Step 6: threshold tuning against a labelled synthetic set. */
record LabelledPair(String query, String listName, boolean isMatch) {}

record Evaluation(double threshold, int tp, int fp, int fn, int tn) {
    double precision() { return tp + fp == 0 ? 1.0 : (double) tp / (tp + fp); }
    double recall() { return tp + fn == 0 ? 1.0 : (double) tp / (tp + fn); }
    double f1() {
        double p = precision(), r = recall();
        return p + r == 0 ? 0.0 : 2 * p * r / (p + r);
    }

    static Evaluation evaluate(NameMatcher matcher, List<LabelledPair> pairs, double threshold) {
        int tp = 0, fp = 0, fn = 0, tn = 0;
        for (LabelledPair pair : pairs) {
            boolean predicted = matcher.compare(pair.query(), pair.listName()).score() >= threshold;
            if (predicted && pair.isMatch()) tp++;
            else if (predicted) fp++;
            else if (pair.isMatch()) fn++;
            else tn++;
        }
        return new Evaluation(threshold, tp, fp, fn, tn);
    }

    static List<Evaluation> thresholdTable(NameMatcher matcher, List<LabelledPair> pairs, double[] thresholds) {
        List<Evaluation> out = new ArrayList<>();
        for (double t : thresholds) out.add(evaluate(matcher, pairs, t));
        return out;
    }
}

public final class Main {
    /** Every name here is invented for the lesson. */
    static Watchlist demoWatchlist() {
        return new Watchlist(List.of(
            new WatchlistEntry("FIC-0001", "Thaddeus Grimblewood", "FICTIONAL-SDN", List.of("Tadeusz Grimblewood", "T. Grimblewood")),
            new WatchlistEntry("FIC-0002", "Yevgenia Kraskovitch", "FICTIONAL-SDN", List.of("Evgenia Kraskovich")),
            new WatchlistEntry("FIC-0003", "Hamid al-Qasrani", "FICTIONAL-SDN", List.of("Hamid Al Qasrani", "Hameed Qasrani")),
            new WatchlistEntry("FIC-0004", "Ximena Fortaleza-Rúa", "FICTIONAL-SSI", List.of("Ximena Fortaleza Rua")),
            new WatchlistEntry("FIC-0005", "Octavian Blunderbuss", "FICTIONAL-SSI", List.of()),
            new WatchlistEntry("FIC-0006", "Perpetua Nightshade", "FICTIONAL-SDN", List.of("Perpetua Nightshade-Vale")),
            new WatchlistEntry("FIC-0007", "Bartholomew Quillfeather", "FICTIONAL-SSI", List.of("Bart Quillfeather"))
        ));
    }

    /** Synthetic ground truth: true variants of listed names and hard negatives that share tokens. */
    static List<LabelledPair> labelledPairs() {
        return List.of(
            new LabelledPair("Thaddeus Grimblewood", "Thaddeus Grimblewood", true),
            new LabelledPair("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood", true),
            new LabelledPair("Thadeus Grimblewod", "Thaddeus Grimblewood", true),
            new LabelledPair("T Grimblewood", "Thaddeus Grimblewood", true),
            new LabelledPair("Evgenia Kraskovich", "Yevgenia Kraskovitch", true),
            new LabelledPair("Yevgeniya Kraskovitch", "Yevgenia Kraskovitch", true),
            new LabelledPair("Hamid Al-Qasrani", "Hamid al-Qasrani", true),
            new LabelledPair("Hameed Qasrani", "Hamid al-Qasrani", true),
            new LabelledPair("Ximena Fortaleza Rua", "Ximena Fortaleza-Rúa", true),
            new LabelledPair("Octavian Blunderbus", "Octavian Blunderbuss", true),
            new LabelledPair("Perpetua Nitshade", "Perpetua Nightshade", true),
            new LabelledPair("Bart Quillfeather", "Bartholomew Quillfeather", true),
            new LabelledPair("Theodora Grimblewood", "Thaddeus Grimblewood", false),
            new LabelledPair("Thaddeus Grimble", "Thaddeus Grimblewood", false),
            new LabelledPair("Yevgenia Krasnova", "Yevgenia Kraskovitch", false),
            new LabelledPair("Hamid Qasim", "Hamid al-Qasrani", false),
            new LabelledPair("Ximena Fortes", "Ximena Fortaleza-Rúa", false),
            new LabelledPair("Octavia Blundell", "Octavian Blunderbuss", false),
            new LabelledPair("Perpetua Nightingale", "Perpetua Nightshade", false),
            new LabelledPair("Bartholomew Quince", "Bartholomew Quillfeather", false),
            new LabelledPair("Marisol Everly", "Thaddeus Grimblewood", false),
            new LabelledPair("Kwame Oduya-Brandt", "Hamid al-Qasrani", false),
            new LabelledPair("Ingrid Halvorsen", "Yevgenia Kraskovitch", false),
            new LabelledPair("Rafael Ocampo", "Octavian Blunderbuss", false)
        );
    }

    public static void main(String[] args) {
        System.out.println("== Normalization ==");
        for (String raw : List.of("GRIMBLEWOOD, Thaddeus", "Dr. Ximena Fortaleza-Rúa", "Hamid al-Qasrani", "Müller-Lüdenscheidt, Jörg")) {
            System.out.printf("  %-34s -> '%s'%n", "'" + raw + "'", Names.normalize(raw));
        }

        System.out.println("\n== Jaro-Winkler on classic pairs ==");
        String[][] classic = {{"martha", "marhta"}, {"dwayne", "duane"}, {"dixon", "dicksonx"}, {"grimblewood", "grimblewod"}, {"abc", "xyz"}};
        for (String[] p : classic) {
            System.out.printf("  %-12s %-12s jaro=%.4f  jaro_winkler=%.4f%n", p[0], p[1], Names.jaro(p[0], p[1]), Names.jaroWinkler(p[0], p[1]));
        }

        System.out.println("\n== Soundex ==");
        for (String t : List.of("robert", "rupert", "tymczak", "pfister", "ashcraft", "nightshade", "nitshade", "kraskovitch", "kraskovich")) {
            System.out.printf("  %-12s %s%n", t, Names.soundex(t));
        }

        Watchlist wl = demoWatchlist();
        System.out.println("\n== Screening at threshold 0.85 ==");
        for (String q : List.of("Thaddeus Grimblewood", "GRIMBLEWOOD, THADDEUS", "Hameed Al Qasrani", "Perpetua Nitshade",
                "Theodora Grimblewood", "Marisol Everly")) {
            System.out.println(wl.screen(q, 0.85).explain());
        }

        System.out.println("\n== Threshold tuning on the labelled synthetic set ==");
        System.out.printf("  %5s %3s %3s %3s %3s %9s %7s %6s%n", "thr", "tp", "fp", "fn", "tn", "precision", "recall", "f1");
        for (Evaluation ev : Evaluation.thresholdTable(wl.matcher, labelledPairs(), new double[]{0.70, 0.75, 0.80, 0.85, 0.90, 0.95, 1.00})) {
            System.out.printf("  %5.2f %3d %3d %3d %3d %9.3f %7.3f %6.3f%n", ev.threshold(), ev.tp(), ev.fp(), ev.fn(), ev.tn(),
                    ev.precision(), ev.recall(), ev.f1());
        }
    }
}
