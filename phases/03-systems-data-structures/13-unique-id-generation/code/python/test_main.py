"""Tests for phases/03-systems-data-structures/13-unique-id-generation. Run: python3 -m unittest -v"""
import unittest

from main import (ClockMovedBackwardsException, IllegalStateError, JavaRandom, ManualClock, SnowflakeGenerator, Ulid,
                  UlidGenerator, Uuid, UuidV4Generator, UuidV7Generator, collision_probability,
                  ids_for_collision_probability, is_lexically_increasing, is_strictly_increasing)

BASE = 1_760_000_000_000


class UuidTextTest(unittest.TestCase):
    def test_text_form_and_parse(self):
        known = Uuid(0x0199C82CC00072C9, 0xBD984A5CE5B3F441)
        self.assertEqual(str(known), "0199c82c-c000-72c9-bd98-4a5ce5b3f441")
        self.assertEqual(Uuid.parse(str(known)), known)
        self.assertEqual(Uuid.parse(str(known).upper()), known)
        self.assertTrue(known.version() == 7 and known.variant() == 2)
        self.assertEqual(known.timestamp_millis(), 0x0199C82CC000)
        self.assertEqual(str(Uuid(0, 0)), "00000000-0000-0000-0000-000000000000")
        # Java longs are signed; the comparison must be unsigned so 0x8000... sorts after 0x7FFF...
        self.assertGreater(Uuid(-(1 << 63), 0).compare_to(Uuid(0x7FFFFFFFFFFFFFFF, 0)), 0)
        with self.assertRaises(ValueError):
            Uuid.parse("0199c82cc00072c9bd984a5ce5b3f441")
        with self.assertRaises(ValueError):
            Uuid.parse("0199c82c-c000-72c9-bd98-4a5ce5b3f4_1")  # int(..., 16) would accept '_'

    def test_generators_match_java_bits(self):
        # The demo's first ids, pinned from the Java run: same seed, same 128 bits.
        clock = ManualClock(BASE)
        rng = JavaRandom(7)
        self.assertEqual(str(UuidV4Generator(rng).next()), "bb0f1798-a377-4418-bfc9-945a02770b39")
        self.assertEqual(str(UuidV7Generator(clock, rng).next()), "0199c82c-c000-72c9-bd98-4a5ce5b3f441")
        self.assertEqual(str(UlidGenerator(clock, rng).next()), "01K742SG00QRDBAJRS0J8V7JJX")


class UuidV4Test(unittest.TestCase):
    def test_v4(self):
        v4 = UuidV4Generator(JavaRandom(1))
        seen = set()
        bits_ok = True
        for _ in range(10_000):
            u = v4.next()
            bits_ok &= u.version() == 4 and u.variant() == 2
            seen.add(u)
        self.assertTrue(bits_ok)
        self.assertEqual(len(seen), 10_000)
        self.assertEqual(UuidV4Generator(JavaRandom(1)).next(), UuidV4Generator(JavaRandom(1)).next())


class UuidV7Test(unittest.TestCase):
    def test_v7(self):
        c7 = ManualClock(BASE)
        v7 = UuidV7Generator(c7, JavaRandom(2))
        first = v7.next()
        self.assertTrue(first.timestamp_millis() == BASE and first.version() == 7 and first.variant() == 2)
        burst = [v7.next() for _ in range(5_000)]
        self.assertTrue(is_strictly_increasing(burst))
        self.assertGreaterEqual(burst[4_999].timestamp_millis(), BASE + 1)
        self.assertTrue(burst[0].timestamp_millis() == burst[1].timestamp_millis()
                        and (burst[1].msb & 0xFFF) == (burst[0].msb & 0xFFF) + 1)
        c7.advance(10)
        later = v7.next()
        self.assertTrue(later.timestamp_millis() == BASE + 10 and later.compare_to(burst[4_999]) > 0)
        c7.set(BASE - 60_000)
        after_step_back = v7.next()
        self.assertTrue(after_step_back.timestamp_millis() == BASE + 10 and after_step_back.compare_to(later) > 0)
        self.assertGreater(str(later), str(burst[0]))


class UlidTest(unittest.TestCase):
    def test_encoding(self):
        self.assertEqual(Ulid.parse("01ARZ3NDEKTSV4RRFFQ69G5FAV").timestamp_millis, 1_469_922_850_259)
        self.assertTrue(str(Ulid((1 << 48) - 1, 0, 0)).startswith("7ZZZZZZZZZ"))
        self.assertEqual(str(Ulid(0, 0, 0)), "0" * 26)
        vec = Ulid(1_469_922_850_259, 0xABCD, 0x0123456789ABCDEF)
        self.assertEqual(Ulid.parse(str(vec)), vec)
        self.assertEqual(Ulid.parse(str(vec).lower().replace("1", "l").replace("0", "o")), vec)
        self.assertTrue(len(str(vec)) == 26 and all(ch in Ulid.ALPHABET for ch in str(vec)))
        with self.assertRaises(ValueError):
            Ulid.parse("01ARZ3NDEKTSV4RRFFQ69G5FAU")
        with self.assertRaises(ValueError):
            Ulid.parse("8ZZZZZZZZZZZZZZZZZZZZZZZZZ")
        with self.assertRaises(ValueError):
            Ulid.parse("01ARZ3NDEK")

    def test_monotonic_generator(self):
        ug = UlidGenerator(ManualClock(BASE), JavaRandom(3))
        uburst = [ug.next() for _ in range(3_000)]
        self.assertTrue(is_strictly_increasing(uburst) and uburst[1].random_lo == uburst[0].random_lo + 1)
        self.assertTrue(is_lexically_increasing([str(u) for u in uburst]))
        carry = UlidGenerator(ManualClock(BASE), JavaRandom(4))
        carry.resume(Ulid(BASE, 5, -1))  # -1 is Java's all-ones long
        carried = carry.next()
        self.assertTrue(carried.random_hi == 6 and carried.random_lo == 0)
        full = UlidGenerator(ManualClock(BASE), JavaRandom(5))
        full.resume(Ulid(BASE, 0xFFFF, -1))
        with self.assertRaises(IllegalStateError):
            full.next()
        resumed = UlidGenerator(ManualClock(BASE + 1), JavaRandom(6))
        resumed.resume(Ulid(BASE, 0xFFFF, -1))
        self.assertEqual(resumed.next().timestamp_millis, BASE + 1)


class SnowflakeTest(unittest.TestCase):
    def test_layout_monotonicity_and_drift(self):
        cs = ManualClock(BASE)
        sf = SnowflakeGenerator(cs, 17, 5)
        id0 = sf.next()
        self.assertTrue(SnowflakeGenerator.timestamp_millis_of(id0) == BASE and SnowflakeGenerator.worker_id_of(id0) == 17
                        and SnowflakeGenerator.sequence_of(id0) == 0)
        self.assertTrue(0 < id0 < (1 << 63))
        fburst = [sf.next() for _ in range(5_000)]
        self.assertTrue(is_strictly_increasing(fburst))
        self.assertTrue(sf.borrowed_millis == 1 and SnowflakeGenerator.timestamp_millis_of(fburst[4_999]) == BASE + 1)
        cs.advance(1)
        same_as_borrowed = sf.next()
        self.assertTrue(SnowflakeGenerator.timestamp_millis_of(same_as_borrowed) == BASE + 1
                        and same_as_borrowed > fburst[4_999])
        cs.set(BASE - 2)
        small_drift = sf.next()
        self.assertTrue(small_drift > same_as_borrowed and SnowflakeGenerator.timestamp_millis_of(small_drift) == BASE + 1)
        cs.set(BASE - 10_000)
        with self.assertRaises(ClockMovedBackwardsException):
            sf.next()
        with self.assertRaises(ValueError):
            SnowflakeGenerator(cs, 1024, 5)
        w1, w2 = SnowflakeGenerator(ManualClock(BASE), 1, 5), SnowflakeGenerator(ManualClock(BASE), 2, 5)
        self.assertNotEqual(w1.next(), w2.next())
        self.assertGreater(SnowflakeGenerator(ManualClock(BASE + 1), 0, 5).next(),
                           SnowflakeGenerator(ManualClock(BASE), 1023, 5).next())


class CollisionMathTest(unittest.TestCase):
    def test_birthday_bound(self):
        self.assertLess(abs(collision_probability(77_163, 32) - 0.5), 0.01)
        self.assertLess(collision_probability(1e12, 122), 1e-12)
        self.assertLess(abs(ids_for_collision_probability(0.5, 32) - 77_163), 10)
        self.assertLess(collision_probability(1e6, 62), collision_probability(1e7, 62))


if __name__ == "__main__":
    unittest.main()
