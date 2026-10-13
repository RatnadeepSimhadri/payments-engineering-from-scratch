"""Tests for phases/13-risk-fraud-and-compliance/04-sanctions-screening. Run: python3 -m unittest -v"""
import unittest

from main import (
    Evaluation,
    NameMatcher,
    ReasonKind,
    Watchlist,
    WatchlistEntry,
    _pair_score,
    demo_watchlist,
    evaluate,
    jaro,
    jaro_winkler,
    labelled_pairs,
    normalize,
    soundex,
    threshold_table,
    tokenize,
)


class NormalizeTest(unittest.TestCase):
    def test_case_and_punctuation(self):
        self.assertEqual(normalize("GRIMBLEWOOD, Thaddeus"), "grimblewood thaddeus")

    def test_diacritics_and_hyphen(self):
        self.assertEqual(normalize("Ximena Fortaleza-Rúa"), "ximena fortaleza rua")
        self.assertEqual(normalize("Straße"), "strasse")

    def test_honorific_dropped(self):
        self.assertEqual(normalize("Dr. Perpetua Nightshade"), "perpetua nightshade")

    def test_whitespace_collapsed(self):
        self.assertEqual(normalize("  Hamid   al-Qasrani "), "hamid al qasrani")

    def test_empty(self):
        self.assertEqual(normalize("..."), "")
        self.assertEqual(tokenize(""), [])


class JaroWinklerTest(unittest.TestCase):
    def test_winkler_published_examples(self):
        self.assertAlmostEqual(jaro("martha", "marhta"), 0.9444444444444445)
        self.assertAlmostEqual(jaro_winkler("martha", "marhta"), 0.9611111111111111)
        self.assertAlmostEqual(jaro_winkler("dwayne", "duane"), 0.84)
        self.assertAlmostEqual(jaro_winkler("dixon", "dicksonx"), 0.8133333333333332)

    def test_boundaries(self):
        self.assertEqual(jaro("abc", "abc"), 1.0)
        self.assertEqual(jaro("abc", "xyz"), 0.0)
        self.assertEqual(jaro("", "abc"), 0.0)

    def test_symmetric_and_bounded(self):
        self.assertEqual(jaro_winkler("kraskovitch", "kraskovich"), jaro_winkler("kraskovich", "kraskovitch"))
        jw = jaro_winkler("grimblewood", "grimblewod")
        self.assertTrue(0.98 < jw <= 1.0)

    def test_prefix_bonus_capped(self):
        self.assertLessEqual(jaro_winkler("abcdefgh", "abcdexyz"), jaro_winkler("abcd", "abcd"))


class SoundexTest(unittest.TestCase):
    def test_national_archives_examples(self):
        self.assertEqual(soundex("Robert"), "R163")
        self.assertEqual(soundex("Rupert"), "R163")
        self.assertEqual(soundex("Tymczak"), "T522")
        self.assertEqual(soundex("Pfister"), "P236")
        self.assertEqual(soundex("Ashcraft"), "A261")

    def test_padding_and_empty(self):
        self.assertEqual(soundex("Lee"), "L000")
        self.assertEqual(soundex(""), "")


class MatcherTest(unittest.TestCase):
    def setUp(self):
        self.m = NameMatcher()

    def test_pair_kinds(self):
        self.assertIs(_pair_score("hamid", "hamid")[0], ReasonKind.EXACT)
        self.assertIs(_pair_score("t", "thaddeus")[0], ReasonKind.INITIAL)
        self.assertIs(_pair_score("nitshade", "nightshade")[0], ReasonKind.FUZZY)
        self.assertIs(_pair_score("marisol", "thaddeus")[0], ReasonKind.UNMATCHED)

    def test_reordered_tokens(self):
        r = self.m.compare("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood")
        self.assertEqual(r.score, 1.0)
        self.assertTrue(r.reordered)

    def test_symmetric(self):
        a = self.m.compare("Hameed Qasrani", "Hamid al-Qasrani").score
        b = self.m.compare("Hamid al-Qasrani", "Hameed Qasrani").score
        self.assertAlmostEqual(a, b)

    def test_unmatched_particle_not_penalised(self):
        s = self.m.compare("Hameed Qasrani", "Hamid al-Qasrani").score
        self.assertAlmostEqual(s, (1.0 + jaro_winkler("hameed", "hamid")) / 2)

    def test_unmatched_real_token_dilutes(self):
        self.assertEqual(self.m.compare("Theodora Grimblewood", "Thaddeus Grimblewood").score, 0.5)
        self.assertLess(self.m.compare("Rafael Ocampo", "Octavian Blunderbuss").score, 0.5)

    def test_empty_query(self):
        self.assertEqual(self.m.compare("", "Thaddeus Grimblewood").score, 0.0)

    def test_explain_names_evidence(self):
        self.assertIn("fuzzy 'nitshade'~'nightshade'", self.m.compare("Perpetua Nitshade", "Perpetua Nightshade").explain())


class ScreeningTest(unittest.TestCase):
    def setUp(self):
        self.wl = demo_watchlist()

    def test_exact_hit_once_per_entry(self):
        r = self.wl.screen("Thaddeus Grimblewood", 0.85)
        self.assertTrue(r.is_hit())
        self.assertEqual(r.hits[0].entry_id, "FIC-0001")
        self.assertEqual(sum(1 for h in r.hits if h.entry_id == "FIC-0001"), 1)

    def test_best_alias_reported(self):
        self.assertEqual(self.wl.screen("Hameed Al Qasrani", 0.85).hits[0].matched_name, "Hameed Qasrani")

    def test_clear(self):
        self.assertFalse(self.wl.screen("Marisol Everly", 0.85).is_hit())

    def test_initial_alias_false_positive_and_threshold(self):
        self.assertTrue(self.wl.screen("Theodora Grimblewood", 0.85).is_hit())
        self.assertFalse(self.wl.screen("Theodora Grimblewood", 0.95).is_hit())

    def test_hits_sorted_best_first(self):
        self.assertEqual(self.wl.screen("Perpetua Nightshade", 0.5).hits[0].entry_id, "FIC-0006")

    def test_threshold_validation(self):
        with self.assertRaises(ValueError):
            self.wl.screen("x", 0.0)
        with self.assertRaises(ValueError):
            self.wl.screen("x", 1.5)

    def test_duplicate_ids_rejected(self):
        with self.assertRaises(ValueError):
            Watchlist([WatchlistEntry("A", "X Y", "P"), WatchlistEntry("A", "Z W", "P")])


class ThresholdTuningTest(unittest.TestCase):
    def setUp(self):
        self.m = NameMatcher()
        self.pairs = labelled_pairs()

    def test_threshold_one_is_precise_but_blind(self):
        ev = evaluate(self.m, self.pairs, 1.0)
        self.assertEqual(ev.fp, 0)
        self.assertEqual(ev.precision(), 1.0)
        self.assertEqual(ev.fn, 8)
        self.assertAlmostEqual(ev.recall(), 1 / 3)

    def test_threshold_085_full_recall(self):
        ev = evaluate(self.m, self.pairs, 0.85)
        self.assertEqual(ev.recall(), 1.0)
        self.assertEqual(ev.fp, 6)
        self.assertEqual(ev.tp + ev.fp + ev.fn + ev.tn, len(self.pairs))

    def test_monotone_in_threshold(self):
        table = threshold_table(self.m, self.pairs, [0.7, 0.9, 1.0])
        for prev, cur in zip(table, table[1:]):
            self.assertLessEqual(cur.recall(), prev.recall())
            self.assertLessEqual(cur.fp, prev.fp)

    def test_f1_of_nothing(self):
        self.assertEqual(Evaluation(0.5, 0, 0, 5, 0).f1(), 0.0)


if __name__ == "__main__":
    unittest.main()
