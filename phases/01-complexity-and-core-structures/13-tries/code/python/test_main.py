"""Tests for phases/01-complexity-and-core-structures/13-tries. Run: python3 -m unittest -v"""
import unittest

from main import BinRouter, Match, NoRouteError, Route, Trie, mask, range_to_prefixes, sample_router


class TrieBasicsTest(unittest.TestCase):
    def test_empty_trie(self):
        t: Trie[str] = Trie()
        self.assertEqual((t.size(), t.node_count()), (0, 1))
        self.assertIsNone(t.search("4"))
        self.assertFalse(t.contains("4"))
        self.assertTrue(t.starts_with(""))  # the root exists
        self.assertFalse(t.starts_with("4"))

    def test_insert_search_starts_with(self):
        t: Trie[str] = Trie()
        self.assertIsNone(t.insert("4111", "A"))
        self.assertEqual(t.insert("4111", "B"), "A")
        self.assertEqual(t.size(), 1)
        t.insert("411", "C")
        t.insert("51", "D")
        self.assertEqual(t.size(), 3)
        self.assertEqual(t.node_count(), 1 + 4 + 2)
        self.assertEqual([t.search(k) for k in ("4111", "411", "51")], ["B", "C", "D"])
        self.assertIsNone(t.search("41"))  # a bare path is not a key
        self.assertIsNone(t.search("4"))
        self.assertIsNone(t.search("41111"))  # an extension is not a key either
        self.assertTrue(t.starts_with("4") and t.starts_with("41") and t.starts_with("4111"))
        self.assertFalse(t.starts_with("6") or t.starts_with("4112"))

    def test_keys_with_prefix_sorted(self):
        t: Trie[str] = Trie()
        for k in ("4111", "411", "51", "4"):
            t.insert(k, k)
        self.assertEqual(t.keys_with_prefix("4"), ["4", "411", "4111"])
        self.assertEqual(t.keys_with_prefix("9"), [])

    def test_none_value_rejected(self):
        with self.assertRaises(TypeError):
            Trie().insert("1", None)


class LongestPrefixMatchTest(unittest.TestCase):
    def setUp(self):
        self.t: Trie[str] = Trie()
        self.t.insert("4", "VISA")
        self.t.insert("4111", "VISA-SANDBOX")
        self.t.insert("51", "MC")

    def test_longest_wins(self):
        self.assertEqual(self.t.longest_prefix_match("4111111111111111"), Match("4111", "VISA-SANDBOX"))

    def test_falls_back_to_shorter(self):
        self.assertEqual(self.t.longest_prefix_match("4000000000000002"), Match("4", "VISA"))
        self.assertEqual(self.t.longest_prefix_match("411"), Match("4", "VISA"))  # bare path, shorter key wins

    def test_no_match(self):
        self.assertIsNone(self.t.longest_prefix_match("6011000000000004"))
        self.assertIsNone(self.t.longest_prefix_match(""))

    def test_exact_key_matches_itself(self):
        self.assertEqual(self.t.longest_prefix_match("4").prefix, "4")

    def test_empty_key_is_a_default(self):
        self.t.insert("", "DEFAULT")
        self.assertEqual(self.t.longest_prefix_match("9999").value, "DEFAULT")


class DeleteTest(unittest.TestCase):
    def setUp(self):
        self.d: Trie[str] = Trie()
        self.d.insert("4111", "A")
        self.d.insert("411", "B")
        self.d.insert("4", "C")
        self.before = self.d.node_count()

    def test_delete_of_path_or_absent_key_changes_nothing(self):
        self.assertFalse(self.d.delete("41"))
        self.assertFalse(self.d.delete("5"))
        self.assertEqual((self.d.node_count(), self.d.size()), (self.before, 3))

    def test_delete_leaf_keeps_shared_prefix(self):
        self.assertTrue(self.d.delete("4111"))
        self.assertEqual(self.d.node_count(), self.before - 1)
        self.assertEqual(self.d.size(), 2)
        self.assertEqual(self.d.search("411"), "B")
        self.assertEqual(self.d.search("4"), "C")
        self.assertTrue(self.d.starts_with("41"))
        self.assertIsNone(self.d.search("4111"))
        self.assertFalse(self.d.starts_with("4111"))

    def test_delete_interior_key_keeps_descendants(self):
        self.assertTrue(self.d.delete("4"))
        self.assertIsNone(self.d.search("4"))
        self.assertEqual(self.d.search("411"), "B")
        self.assertEqual(self.d.search("4111"), "A")
        self.assertEqual(self.d.node_count(), self.before)  # nothing to prune: the node still has children

    def test_delete_everything_prunes_to_root(self):
        for k in ("4111", "411", "4"):
            self.assertTrue(self.d.delete(k))
        self.assertEqual((self.d.size(), self.d.node_count()), (0, 1))
        self.assertIsNone(self.d.insert("51", "X"))
        self.assertEqual(self.d.search("51"), "X")


class RangeToPrefixesTest(unittest.TestCase):
    def test_simple_ranges(self):
        self.assertEqual(range_to_prefixes("51", "55"), ["51", "52", "53", "54", "55"])
        self.assertEqual(range_to_prefixes("4", "4"), ["4"])
        self.assertEqual(range_to_prefixes("00", "99"), [""])
        self.assertEqual(range_to_prefixes("3528", "3599"),
                         ["3528", "3529", "353", "354", "355", "356", "357", "358", "359"])

    def test_mastercard_two_series(self):
        ms = range_to_prefixes("2221", "2720")
        self.assertEqual(len(ms), 23)
        for p in ("23", "26", "270", "2720"):
            self.assertIn(p, ms)
        for p in ("222", "27", "2721"):
            self.assertNotIn(p, ms)
        for n in range(2000, 3000):
            s = str(n)
            covering = sum(1 for p in ms if s.startswith(p))
            self.assertEqual(covering, 1 if 2221 <= n <= 2720 else 0, s)

    def test_invalid_ranges(self):
        with self.assertRaises(ValueError):
            range_to_prefixes("51", "555")
        with self.assertRaises(ValueError):
            range_to_prefixes("56", "55")
        with self.assertRaises(ValueError):
            range_to_prefixes("5a", "55")


class BinRouterTest(unittest.TestCase):
    def setUp(self):
        self.r: BinRouter = sample_router()

    def test_sample_pans_route(self):
        self.assertEqual(self.r.route("4111111111111111"), Route("VISA", "Visa sandbox issuer", "4"))
        self.assertEqual(self.r.route("5555555555554444"), Route("MASTERCARD", "Mastercard sandbox issuer", "55"))
        self.assertEqual(self.r.route("2221000000000009").network, "MASTERCARD")
        self.assertEqual(self.r.route("2720999999999996").network, "MASTERCARD")
        self.assertEqual(self.r.route("378282246310005"), Route("AMEX", "Amex sandbox issuer", "37"))
        discover = self.r.route("6011111111111117")
        self.assertEqual((discover.network, discover.issuer), ("DISCOVER", "UNKNOWN"))
        self.assertEqual(self.r.route("6451111111111110").matched_prefix, "645")
        self.assertEqual(self.r.route("3530111333300000").network, "JCB")

    def test_no_route(self):
        with self.assertRaises(NoRouteError):
            self.r.route("2721000000000008")  # just past the 2-series range
        with self.assertRaises(NoRouteError):
            self.r.route("9999000000000000")

    def test_invalid_pans(self):
        for pan in ("41111111111", "41111111111111111111", "4111-1111-1111-1111"):
            with self.assertRaises(ValueError):
                self.r.route(pan)

    def test_mask(self):
        self.assertEqual(mask("4111111111111111"), "411111******1111")
        self.assertEqual(mask("12345"), "*****")


if __name__ == "__main__":
    unittest.main()
