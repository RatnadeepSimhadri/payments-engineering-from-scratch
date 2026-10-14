"""Tests for phases/02-algorithms/12-string-matching. Run: python3 -m unittest -v"""

import unittest

from main import AhoCorasick, KmpMatcher, Match, NaiveMatcher, RabinKarpMatcher, SanctionsScreener, sample_list


class JavaRandom:
    """java.util.Random (48-bit LCG), so the random strings are the same as the Java test's seed 7."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        return self._seed >> (48 - bits)

    def next_int(self, bound: int) -> int:
        if bound & (bound - 1) == 0:
            return (bound * self._next(31)) >> 31
        while True:
            bits = self._next(31)
            val = bits % bound
            if bits - val + (bound - 1) < (1 << 31):
                return val


def random_string(rng, length, alphabet):
    return "".join(chr(ord("a") + rng.next_int(alphabet)) for _ in range(length))


def roll_matches_rehash(text, m):
    rk = RabinKarpMatcher(text[:m])
    h = RabinKarpMatcher.hash(text[:m])
    for i in range(len(text) - m):
        h = rk.roll(h, text[i], text[i + m])
        if h != RabinKarpMatcher.hash(text[i + 1:i + 1 + m]):
            return False
    return True


def names(matches):
    return [m.pattern for m in matches]


class NaiveTest(unittest.TestCase):
    def test_naive(self):
        self.assertEqual(NaiveMatcher.search("abracadabra", "abra"), [0, 7])
        self.assertEqual(NaiveMatcher.search("aaaa", "aa"), [0, 1, 2])
        self.assertEqual(NaiveMatcher.search("abc", "d"), [])
        self.assertEqual(NaiveMatcher.search("ab", "abc"), [])
        with self.assertRaises(ValueError):
            NaiveMatcher.search("abc", "")


class KmpTest(unittest.TestCase):
    def test_failure_function(self):
        self.assertEqual(KmpMatcher.failure_function("ABABAC"), [0, 0, 1, 2, 3, 0])
        self.assertEqual(KmpMatcher.failure_function("aaaab"), [0, 1, 2, 3, 0])
        self.assertEqual(KmpMatcher.failure_function("abcabcabd"), [0, 0, 0, 1, 2, 3, 4, 5, 0])
        self.assertEqual(KmpMatcher.failure_function("a"), [0])
        self.assertEqual(KmpMatcher.failure_function("abcd"), [0, 0, 0, 0])

    def test_search(self):
        self.assertEqual(KmpMatcher("abra").search("abracadabra"), [0, 7])
        self.assertEqual(KmpMatcher("aa").search("aaaa"), [0, 1, 2])
        self.assertEqual(KmpMatcher("aaaab").search("a" * 30 + "b"), [26])
        self.assertEqual(KmpMatcher("xyz").search("abcxyz"), [3])
        self.assertEqual(KmpMatcher("abc").search("abc"), [0])
        self.assertEqual(KmpMatcher("abc").search(""), [])
        m = KmpMatcher("ab")
        copy = m.failure()
        copy[0] = 99
        self.assertEqual(m.failure()[0], 0)  # failure() returns a copy


class RabinKarpTest(unittest.TestCase):
    def test_rabin_karp(self):
        rk = RabinKarpMatcher("ACQ")
        self.assertEqual(rk.search("ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003"), [0, 18, 36])
        self.assertTrue(rk.hash_matches() == 3 and rk.false_positives() == 0)
        self.assertTrue(roll_matches_rehash("the quick brown fox", 5))
        self.assertEqual(RabinKarpMatcher("aa").search("aaaa"), [0, 1, 2])
        self.assertEqual(RabinKarpMatcher("abcd").search("abc"), [])
        self.assertEqual(RabinKarpMatcher("ü").search("müller müller"), [1, 8])


class RandomizedTest(unittest.TestCase):
    """One generator, drawn in the Java test's order: 200 single-pattern cases, then 100 automaton texts."""

    def test_matchers_agree_with_oracles(self):
        rng = JavaRandom(7)
        agree = True
        for _ in range(200):
            text = random_string(rng, 1 + rng.next_int(60), 2)
            pat = random_string(rng, 1 + rng.next_int(4), 2)
            expect = NaiveMatcher.search(text, pat)
            agree &= KmpMatcher(pat).search(text) == expect and RabinKarpMatcher(pat).search(text) == expect
        self.assertTrue(agree, "kmp and rabin-karp agree with naive on 200 random inputs")

        pats = ["ab", "ba", "aab", "bb", "abab"]
        ac_random = AhoCorasick(pats)
        ac_agree = True
        for _ in range(100):
            text = random_string(rng, 1 + rng.next_int(80), 2)
            expect = [Match(p, s) for p in pats for s in KmpMatcher(p).search(text)]
            got = ac_random.search(text)
            ac_agree &= len(got) == len(expect) and all(e in got for e in expect)
        self.assertTrue(ac_agree, "aho-corasick agrees with per-pattern KMP on 100 random texts")


class AhoCorasickTest(unittest.TestCase):
    def test_textbook_example(self):
        ac = AhoCorasick(["he", "she", "his", "hers"])
        self.assertEqual(ac.node_count(), 10)
        self.assertEqual(ac.search("ushers"), [Match("she", 1), Match("he", 2), Match("hers", 2)])
        self.assertEqual(ac.search("she"), [Match("she", 0), Match("he", 1)])  # a suffix of another pattern
        self.assertEqual(ac.search("xyz"), [])
        self.assertEqual(len(AhoCorasick(["ab", "ab"]).patterns()), 1)  # duplicate inserted once
        with self.assertRaises(ValueError):
            AhoCorasick(["a", ""])


class SanctionsScreenerTest(unittest.TestCase):
    def test_screening(self):
        screener = SanctionsScreener(sample_list())
        self.assertEqual(SanctionsScreener.normalize("  Al-Rashid   Exchange, Ltd. "), "AL RASHID EXCHANGE LTD")
        self.assertEqual(names(screener.screen("IVANOV TRADING, LLC")), ["IVANOV", "IVANOV TRADING LLC"])
        self.assertEqual(names(screener.screen("Northern Star Shipping Co.")), ["NORTHERN STAR SHIPPING", "STAR SHIPPING"])
        self.assertEqual(screener.screen("Dmitri Ivanova"), [])  # partial token does not hit
        self.assertEqual(screener.screen("Rashida Patel"), [])
        self.assertEqual(screener.screen("Acme Widgets Inc"), [])
        self.assertEqual(screener.screen("al rashid exchange house")[0].start, 3)  # relative to the normalized name


if __name__ == "__main__":
    unittest.main()
