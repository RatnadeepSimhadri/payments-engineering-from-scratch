"""Tests for phases/11-payment-rails/04-pan-luhn-and-bin-ranges. Run: python3 -m unittest -v"""
import unittest

from main import BinRange, BinTable, CardRouter, Luhn, Network, Pan, sample_issuer_table


class JavaRandom:
    """java.util.Random (48-bit LCG) with nextInt(bound) and nextLong(bound), so the seeded checks replay MainTest.java."""

    MASK64 = (1 << 64) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r

    def next_long(self) -> int:
        v = ((self.next(32) << 32) + self.next(32)) & self.MASK64
        return v - (1 << 64) if v >= (1 << 63) else v

    def next_long_bounded(self, bound: int) -> int:
        m = bound - 1
        r = self.next_long()
        if (bound & m) == 0:
            return r & m
        u = (r & self.MASK64) >> 1
        while True:
            r = u % bound
            if ((u + m - r) & self.MASK64) < (1 << 63):
                return r
            u = (self.next_long() & self.MASK64) >> 1


VISA = Pan.parse("4111 1111 1111 1111")
ISSUERS = sample_issuer_table()


class LuhnTest(unittest.TestCase):
    def test_published_test_numbers_are_valid(self):
        for pan in ("4111111111111111", "4242424242424242", "5555555555554444", "2223003122003222",
                    "378282246310005", "6011111111111117", "79927398713"):
            self.assertTrue(Luhn.is_valid(pan), pan)

    def test_what_luhn_catches_and_misses(self):
        self.assertFalse(Luhn.is_valid("4111111111111112"))
        self.assertFalse(Luhn.is_valid("4422424242424242"))
        self.assertTrue(Luhn.is_valid("091"))
        self.assertTrue(Luhn.is_valid("901"))
        self.assertEqual(Luhn.check_digit("7992739871"), 3)
        self.assertEqual(Luhn.check_digit("411111111111111"), 1)
        self.assertEqual(Luhn.complete("411111111111111"), "4111111111111111")
        self.assertEqual(Luhn.check_digit("0"), 0)
        with self.assertRaises(ValueError):
            Luhn.is_valid("4111-1111")
        with self.assertRaises(ValueError):
            Luhn.is_valid("")

    def test_complete_is_always_valid_on_seeded_random_partials(self):
        rnd = JavaRandom(42)
        for _ in range(1000):
            length = 11 + rnd.next_int(8)
            partial = "".join(chr(48 + rnd.next_int(10)) for _ in range(length))
            self.assertTrue(Luhn.is_valid(Luhn.complete(partial)))


class PanTest(unittest.TestCase):
    def test_parse_and_mask(self):
        self.assertEqual(VISA.digits, "4111111111111111")
        self.assertEqual(Pan.parse("5555-5555-5555-4444").length(), 16)
        self.assertEqual(VISA.masked(), "411111******1111")
        self.assertEqual(VISA.masked(8), "41111111****1111")
        self.assertEqual(VISA.masked(0), "************1111")
        self.assertEqual(str(VISA), "411111******1111")
        self.assertEqual(repr(VISA), "411111******1111")
        self.assertEqual(f"{VISA!r} {[VISA]}", "411111******1111 [411111******1111]")
        self.assertEqual(len(Pan.parse("378282246310005").masked()), 15)
        self.assertEqual((VISA.bin(6), VISA.bin(8)), ("411111", "41111111"))
        self.assertEqual(Pan.parse("378282246310005").major_industry_identifier(), 3)

    def test_rejections(self):
        with self.assertRaises(ValueError):
            Pan.parse("4111111111111112")
        with self.assertRaises(ValueError):
            Pan.parse("79927398713")
        with self.assertRaises(ValueError):
            Pan.parse(Luhn.complete("4000000000000000000"))
        with self.assertRaises(ValueError):
            Pan.parse("4111 1111 1111 111a")
        with self.assertRaises(ValueError):
            VISA.masked(9)
        with self.assertRaises(ValueError):
            Pan(Luhn.complete("40000000000")).masked(8)


class BinRangeTest(unittest.TestCase):
    def test_normalization(self):
        self.assertEqual(BinRange.normalize_low("411111"), 41111100)
        self.assertEqual(BinRange.normalize_high("411111"), 41111199)
        self.assertEqual(BinRange.normalize_low("55555555"), BinRange.normalize_high("55555555"))
        r = BinRange.of_prefix_range("2221", "2720", Network.MASTERCARD, "n", "p")
        self.assertEqual((r.low, r.high), (22210000, 27209999))
        with self.assertRaises(ValueError):
            BinRange.of_prefix_range("51", "555", Network.MASTERCARD, "n", "p")
        with self.assertRaises(ValueError):
            BinTable([BinRange.of_prefix("4111", Network.VISA, "a", "p"), BinRange.of_prefix("411111", Network.VISA, "b", "p")])


class BinTableLookupTest(unittest.TestCase):
    def test_binary_search_lookup(self):
        self.assertEqual(ISSUERS.lookup(VISA).issuer, "Test Bank A")
        self.assertIn("8-digit", ISSUERS.lookup(Pan.parse("5555555555554444")).product)
        self.assertIsNone(ISSUERS.lookup(5555555600))
        self.assertIsNone(ISSUERS.lookup(55555554 * 100 + 99))
        self.assertIsNone(ISSUERS.lookup(0))
        self.assertIsNone(ISSUERS.lookup(BinRange.MAX_KEY))
        self.assertIsNone(ISSUERS.lookup(41111200))
        self.assertIsNotNone(ISSUERS.lookup(41111100))
        self.assertIsNotNone(ISSUERS.lookup(41111199))

    def test_binary_search_agrees_with_linear_scan(self):
        rnd = JavaRandom(7)
        every = [BinRange.of_prefix("411111", Network.VISA, "Test Bank A", "x"),
                 BinRange.of_prefix("424242", Network.VISA, "Test Bank B", "x"),
                 BinRange.of_prefix("55555555", Network.MASTERCARD, "Test Bank C", "x"),
                 BinRange.of_prefix("222300", Network.MASTERCARD, "Test Bank D", "x"),
                 BinRange.of_prefix("378282", Network.AMEX, "Amex (test)", "x"),
                 BinRange.of_prefix("601111", Network.DISCOVER, "Discover (test)", "x")]
        for i in range(10_000):
            key = rnd.next_long_bounded(BinRange.MAX_KEY + 1) if i % 2 == 0 else 41111000 + rnd.next_int(400)
            linear = next((r.issuer for r in every if r.contains(key)), None)
            hit = ISSUERS.lookup(key)
            self.assertEqual(linear, hit.issuer if hit is not None else None)


class NetworkTest(unittest.TestCase):
    def test_detection(self):
        self.assertIs(BinTable.detect_network(VISA), Network.VISA)
        self.assertIs(BinTable.detect_network(Pan.parse("5555555555554444")), Network.MASTERCARD)
        self.assertIs(BinTable.detect_network(Pan.parse("2223003122003222")), Network.MASTERCARD)
        self.assertIs(BinTable.network_ranges().lookup(27209999).network, Network.MASTERCARD)
        self.assertIsNone(BinTable.network_ranges().lookup(27210000))
        self.assertIsNone(BinTable.network_ranges().lookup(22209999))
        self.assertIs(BinTable.detect_network(Pan.parse("378282246310005")), Network.AMEX)
        self.assertIs(BinTable.detect_network(Pan.parse("340000000000009")), Network.AMEX)
        self.assertIs(BinTable.detect_network(Pan.parse("6011111111111117")), Network.DISCOVER)
        self.assertIs(BinTable.detect_network(Pan.parse("6500000000000002")), Network.DISCOVER)
        self.assertIs(BinTable.detect_network(Pan(Luhn.complete("630400000000000"))), Network.UNKNOWN)

    def test_length_rules(self):
        self.assertTrue(BinTable.length_allowed(Network.VISA, 16))
        self.assertTrue(BinTable.length_allowed(Network.VISA, 13))
        self.assertFalse(BinTable.length_allowed(Network.VISA, 15))
        self.assertTrue(BinTable.length_allowed(Network.AMEX, 15))
        self.assertFalse(BinTable.length_allowed(Network.AMEX, 16))
        self.assertFalse(BinTable.length_allowed(Network.MASTERCARD, 19))


class RoutingTest(unittest.TestCase):
    def test_end_to_end(self):
        router = CardRouter(ISSUERS)
        d = router.route("2223 0031 2200 3222")
        self.assertIs(d.network, Network.MASTERCARD)
        self.assertIsNotNone(d.issuer_range)
        self.assertTrue(d.length_ok)
        unknown = router.route("4000056655665556")
        self.assertIsNone(unknown.issuer_range)
        self.assertIs(unknown.network, Network.VISA)
        self.assertNotIn("2223003122003222", d.describe())
        self.assertTrue(d.describe().startswith("222300******3222"))


if __name__ == "__main__":
    unittest.main()
