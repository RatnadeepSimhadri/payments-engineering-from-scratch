"""Tests for phases/01-complexity-and-core-structures/11-bit-manipulation. Run: python3 -m unittest -v"""
import unittest

from main import (LONG_MAX, LONG_MIN, AccountFlags, Bitmap, Bits, Iso8583Bitmap, SnowflakeId, java_div, java_rem,
                  to_int8, to_int32, to_int64, urshift32)


class JavaRandom:
    """java.util.Random(seed) bit for bit: next(bits), nextInt(bound), nextLong(), nextBoolean()."""

    def __init__(self, seed):
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits):
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound):
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r

    def next_long(self):
        return to_int64((self.next(32) << 32) + self.next(32))

    def next_boolean(self):
        return self.next(1) != 0


class MaskTest(unittest.TestCase):
    def test_masks_and_single_bits(self):
        self.assertEqual(Bits.mask(0), 0)
        self.assertEqual(Bits.mask(3), 0b111)
        self.assertEqual(Bits.mask(63), LONG_MAX)
        self.assertEqual(Bits.mask(64), -1)
        with self.assertRaises(ValueError):
            Bits.mask(65)
        self.assertTrue(Bits.set(0, 5) == 32 and Bits.clear(32, 5) == 0 and Bits.toggle(Bits.toggle(7, 1), 1) == 7)
        self.assertTrue(Bits.is_set(LONG_MIN, 63) and not Bits.is_set(LONG_MIN, 62))  # unsigned view of the sign bit
        self.assertEqual(Bits.set(0, 63), LONG_MIN)  # setting bit 63 gives a negative long

    def test_extract_and_insert(self):
        self.assertEqual(Bits.extract(0b1011_0100, 4, 3), 0b011)
        self.assertEqual(Bits.extract(-1, 60, 4), 15)  # top field of a negative value is unsigned
        self.assertEqual(Bits.insert(0b1011_0100, 4, 3, 0b110), 0b1110_0100)
        self.assertEqual(Bits.extract(Bits.insert(0, 22, 41, 123_456_789), 22, 41), 123_456_789)
        with self.assertRaises(ValueError):
            Bits.insert(0, 0, 3, 8)
        with self.assertRaises(ValueError):
            Bits.extract(0, 60, 5)


class PopcountTest(unittest.TestCase):
    def test_implementations_agree_on_random_longs(self):
        rnd = JavaRandom(5)
        pop_agree = True
        for _ in range(10_000):
            v = rnd.next_long()
            expected = (v & ((1 << 64) - 1)).bit_count()  # Long.bitCount
            pop_agree = Bits.popcount_kernighan(v) == expected and Bits.popcount_parallel(v) == expected
            if not pop_agree:
                break
        self.assertTrue(pop_agree)
        self.assertTrue(Bits.popcount_parallel(0) == 0 and Bits.popcount_parallel(-1) == 64
                        and Bits.popcount_kernighan(LONG_MIN) == 1)

    def test_powers_of_two_and_rendering(self):
        self.assertTrue(Bits.lowest_set_bit(0b1011_0100) == 0b100 and Bits.lowest_set_bit(0) == 0
                        and Bits.lowest_set_bit(LONG_MIN) == LONG_MIN)
        self.assertTrue(Bits.is_power_of_two(1) and Bits.is_power_of_two(1024) and not Bits.is_power_of_two(0)
                        and not Bits.is_power_of_two(1000) and not Bits.is_power_of_two(LONG_MIN))
        self.assertTrue(Bits.round_up_to_power_of_two(1) == 1 and Bits.round_up_to_power_of_two(1000) == 1024
                        and Bits.round_up_to_power_of_two(1024) == 1024 and Bits.round_up_to_power_of_two(1025) == 2048)
        self.assertTrue(Bits.to_binary(5, 8) == "00000101" and Bits.to_hex(255, 4) == "00FF"
                        and Bits.to_hex(-1, 16) == "FFFFFFFFFFFFFFFF")


class AccountFlagsTest(unittest.TestCase):
    def test_flags(self):
        f = AccountFlags.none().with_(AccountFlags.KYC_VERIFIED).with_(AccountFlags.PAYOUTS_ENABLED)
        self.assertTrue(f.has(AccountFlags.KYC_VERIFIED)
                        and f.has(AccountFlags.KYC_VERIFIED | AccountFlags.PAYOUTS_ENABLED)
                        and not f.has(AccountFlags.FROZEN))
        self.assertTrue(f.can_pay_out())
        self.assertFalse(f.with_(AccountFlags.CHARGEBACK_HOLD).can_pay_out())
        self.assertFalse(f.with_(AccountFlags.FROZEN).can_pay_out())
        self.assertEqual(f.with_(AccountFlags.FROZEN).without(AccountFlags.FROZEN).bits(), f.bits())
        self.assertEqual(f.without(AccountFlags.HIGH_RISK).bits(), f.bits())
        self.assertTrue(str(f) == "{KYC_VERIFIED, PAYOUTS_ENABLED}" and str(AccountFlags.none()) == "{}")
        with self.assertRaises(ValueError):
            AccountFlags(1 << 7)


class BitmapTest(unittest.TestCase):
    def test_against_reference_set(self):
        # Seed 5 continues after the popcount test in Java; replay those 10,000 nextLong calls first.
        rnd = JavaRandom(5)
        for _ in range(10_000):
            rnd.next_long()
        bm = Bitmap(200)
        ref = set()  # plays java.util.BitSet
        for _ in range(2_000):
            idx = rnd.next_int(200)
            if rnd.next_boolean():
                bm.set(idx)
                ref.add(idx)
            else:
                bm.clear(idx)
                ref.discard(idx)

        def ref_next(i):
            return min((x for x in ref if x >= i), default=-1)

        agree = bm.cardinality() == len(ref)
        for i in range(200):
            if not agree:
                break
            agree = bm.get(i) == (i in ref) and bm.next_set_bit(i) == ref_next(i)
        self.assertTrue(agree)
        with self.assertRaises(IndexError):
            bm.set(200)
        a = Bitmap(130)
        with self.assertRaises(ValueError):
            a.and_(bm)

    def test_words_and_next_set_bit(self):
        self.assertTrue(Bitmap(0).words_allocated() == 0 and Bitmap(1).words_allocated() == 1
                        and Bitmap(64).words_allocated() == 1 and Bitmap(65).words_allocated() == 2)
        sparse = Bitmap(1_000)
        sparse.set(999)
        self.assertTrue(sparse.next_set_bit(0) == 999 and sparse.next_set_bit(999) == 999
                        and sparse.next_set_bit(1_000) == -1)
        self.assertEqual(Bitmap(1_000).next_set_bit(0), -1)
        a, b = Bitmap(130), Bitmap(130)
        a.set(0)
        a.set(129)
        b.set(129)
        b.set(64)
        self.assertTrue(a.and_(b).cardinality() == 1 and a.and_(b).get(129) and a.or_(b).cardinality() == 3)


class Iso8583Test(unittest.TestCase):
    FIELDS = [2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49]

    def test_primary_bitmap(self):
        hex_str = Iso8583Bitmap.encode(self.FIELDS)
        self.assertEqual(len(hex_str), 16)
        self.assertTrue(hex_str.startswith("7"))  # fields 2,3,4 are bits 62..60
        self.assertEqual(hex_str, "7238058020C08000")
        self.assertEqual(Iso8583Bitmap.decode(hex_str), self.FIELDS)

    def test_secondary_bitmap(self):
        both = Iso8583Bitmap.encode(sorted(set(self.FIELDS) | {102}))
        self.assertTrue(len(both) == 32 and both.startswith("F"))
        self.assertEqual(both[16:], "0000000004000000")  # field 102 is bit 26 of the secondary bitmap
        decoded = Iso8583Bitmap.decode(both)
        self.assertTrue(102 in decoded and len(decoded) == 15)
        self.assertEqual(Iso8583Bitmap.encode({64}), "0000000000000001")

    def test_rejections(self):
        with self.assertRaises(ValueError):
            Iso8583Bitmap.encode({1})
        with self.assertRaises(ValueError):
            Iso8583Bitmap.encode({129})
        with self.assertRaises(ValueError):
            Iso8583Bitmap.decode("F000000000000000")  # field 1 set but only 16 chars
        with self.assertRaises(ValueError):
            Iso8583Bitmap.decode("7234")


class SnowflakeTest(unittest.TestCase):
    def test_compose_decompose(self):
        ts = SnowflakeId.EPOCH_MILLIS + 123_456_789
        id_ = SnowflakeId.compose(ts, 7, 42)
        parts = SnowflakeId.decompose(id_)
        self.assertTrue(parts.timestamp_millis == ts and parts.worker == 7 and parts.sequence == 42)
        self.assertGreater(id_, 0)
        self.assertGreater(SnowflakeId.compose(ts + 1, 0, 0), SnowflakeId.compose(ts, 1023, 4095))
        self.assertEqual(SnowflakeId.compose(SnowflakeId.EPOCH_MILLIS + SnowflakeId.MAX_TIMESTAMP, 1023, 4095), LONG_MAX)
        with self.assertRaises(ValueError):
            SnowflakeId.compose(SnowflakeId.EPOCH_MILLIS - 1, 0, 0)
        with self.assertRaises(ValueError):
            SnowflakeId.compose(ts, 1024, 0)
        with self.assertRaises(ValueError):
            SnowflakeId.decompose(-1)

    def test_generator(self):
        now = [SnowflakeId.EPOCH_MILLIS + 1_000]
        gen = SnowflakeId.Generator(3, lambda: now[0])
        first, second = gen.next(), gen.next()
        self.assertTrue(SnowflakeId.decompose(first).sequence == 0 and SnowflakeId.decompose(second).sequence == 1
                        and second > first)
        now[0] += 1
        self.assertEqual(SnowflakeId.decompose(gen.next()).sequence, 0)
        now[0] -= 5
        with self.assertRaises(RuntimeError):
            gen.next()  # clock moving backwards is refused

    def test_sequence_overflow_waits(self):
        reads = [0]
        slow = [SnowflakeId.EPOCH_MILLIS + 5_000]

        def clock():
            reads[0] += 1
            if reads[0] % 10_000 == 0:
                slow[0] += 1
            return slow[0]

        busy = SnowflakeId.Generator(1, clock)
        prev = -1
        increasing = True
        for _ in range(9_000):
            x = busy.next()
            if x <= prev:
                increasing = False
            prev = x
        self.assertTrue(increasing and busy.waits() >= 1)


class TwosComplementTrapsTest(unittest.TestCase):
    def test_java_fixed_width_semantics(self):
        self.assertLess(to_int64(abs(LONG_MIN)), 0)  # Math.abs(Long.MIN_VALUE) is still negative
        self.assertTrue(to_int8(0x80) == -128 and (to_int8(0x80) & 0xFF) == 128)
        self.assertTrue((-8 >> 1) == -4 and urshift32(-8, 1) == 2_147_483_644)
        self.assertTrue(to_int32(1 << (32 & 31)) == 1 and to_int32(1 << 31) < 0)  # shift count mod 32
        self.assertTrue(2_000_000_000 + urshift32(2_100_000_000 - 2_000_000_000, 1) == 2_050_000_000
                        and java_div(to_int32(2_000_000_000 + 2_100_000_000), 2) < 0)
        self.assertTrue(java_rem(-7, 2) == -1 and (-7 & 1) == 1)

    def test_python_differs_from_java(self):
        # The reasons the helpers exist: Python floors, takes the divisor's sign, and never wraps.
        self.assertEqual(-7 // 2, -4)
        self.assertEqual(java_div(-7, 2), -3)
        self.assertEqual(-7 % 2, 1)
        self.assertEqual(LONG_MAX + 1, 1 << 63)
        self.assertEqual(to_int64(LONG_MAX + 1), LONG_MIN)


if __name__ == "__main__":
    unittest.main()
