"""Tests for phases/03-systems-data-structures/15-serialization-formats. Run: python3 -m unittest -v"""
import unittest

from main import (JPY, LONG_MAX, LONG_MIN, USD, Audit, ByteSink, ByteSource, Decoder, Encoder, JavaRandom, Json,
                  MalformedMessageException, Money, PostingV1, PostingV2, Varint, WireType, random_posting)


def hx(s: str) -> bytes:
    return bytes.fromhex(s.replace(" ", ""))


def varint(v: int) -> bytes:
    s = ByteSink()
    Varint.write_unsigned(s, v)
    return s.to_byte_array()


def read_varint(b: bytes) -> int:
    return Varint.read_unsigned(ByteSource(b))


def size_signed(v: int) -> int:
    s = ByteSink()
    Varint.write_signed(s, v)
    return s.size()


SAMPLE = PostingV2(1_048_577, Money.of_minor(-1999, USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
                   Audit("svc-refunds", 1_760_000_000_000), True)


class VarintTest(unittest.TestCase):
    def test_spec_vectors(self):
        self.assertEqual(varint(0), hx("00"))
        self.assertEqual(varint(1), hx("01"))
        self.assertEqual(varint(127), hx("7f"))
        self.assertEqual(varint(128), hx("80 01"))
        self.assertEqual(varint(150), hx("96 01"))
        self.assertEqual(varint(300), hx("ac 02"))
        self.assertEqual(len(varint(LONG_MAX)), 9)
        self.assertEqual(varint(-1), hx("ff ff ff ff ff ff ff ff ff 01"))
        self.assertTrue(Varint.encoded_size(127) == 1 and Varint.encoded_size(128) == 2 and Varint.encoded_size(-1) == 10)

    def test_round_trips_and_errors(self):
        rng = JavaRandom(3)
        ok = True
        for _ in range(10_000):
            v = rng.next_long()
            shift = rng.next_int(64)
            v = (v & 0xFFFFFFFFFFFFFFFF) >> shift  # nextLong() >>> nextInt(64), evaluated left to right
            if v >= 1 << 63:
                v -= 1 << 64
            ok &= read_varint(varint(v)) == v
        self.assertTrue(ok)
        self.assertEqual(read_varint(varint(-1)), -1)  # Java long semantics: all ones reads back as -1
        with self.assertRaises(MalformedMessageException):
            read_varint(hx("80"))
        with self.assertRaises(MalformedMessageException):
            read_varint(hx("80 80 80 80 80 80 80 80 80 80 80 00"))
        with self.assertRaises(MalformedMessageException):
            read_varint(hx("ff ff ff ff ff ff ff ff ff 7f"))


class ZigZagTest(unittest.TestCase):
    def test_spec_table(self):
        self.assertEqual(Varint.zigzag_encode(0), 0)
        self.assertEqual(Varint.zigzag_encode(-1), 1)
        self.assertEqual(Varint.zigzag_encode(1), 2)
        self.assertEqual(Varint.zigzag_encode(-2), 3)
        self.assertEqual(Varint.zigzag_encode(2_147_483_647), 4_294_967_294)
        self.assertEqual(Varint.zigzag_encode(-2_147_483_648), 4_294_967_295)
        # Without the 64-bit mask Python would return 2^64 - 1 here; Java's long is all ones, i.e. -1.
        self.assertEqual(Varint.zigzag_encode(LONG_MIN), -1)
        self.assertEqual(Varint.zigzag_encode(LONG_MAX), -2)

    def test_round_trip_and_sizes(self):
        rng = JavaRandom(3)
        for _ in range(10_000):  # advance past the varint test's draws so the streams match Java's single rng
            rng.next_long()
            rng.next_int(64)
        self.assertTrue(all(Varint.zigzag_decode(Varint.zigzag_encode(v)) == v
                            for v in (rng.next_long() for _ in range(10_000))))
        refund = ByteSink()
        Varint.write_signed(refund, -1999)
        self.assertEqual(refund.to_byte_array(), hx("9d 1f"))
        self.assertTrue(size_signed(-64) == 1 and size_signed(-65) == 2 and size_signed(63) == 1 and size_signed(64) == 2)


class TagTest(unittest.TestCase):
    def test_spec_message_vectors(self):
        self.assertEqual(Encoder().uint64(1, 150).to_byte_array(), hx("08 96 01"))
        self.assertEqual(Encoder().string(2, "testing").to_byte_array(), hx("12 07 74 65 73 74 69 6e 67"))
        self.assertEqual(Encoder().message(3, Encoder().uint64(1, 150)).to_byte_array(), hx("1a 03 08 96 01"))
        self.assertEqual(Encoder().fixed64(4, 0x0102030405060708).to_byte_array(), hx("21 08 07 06 05 04 03 02 01"))
        self.assertTrue(len(Encoder().uint64(16, 1).to_byte_array()) == 3 and len(Encoder().uint64(15, 1).to_byte_array()) == 2)

    def test_bad_tags(self):
        with self.assertRaises(ValueError):
            Encoder().uint64(0, 1)
        with self.assertRaises(MalformedMessageException):
            Decoder(hx("00 01")).next()
        with self.assertRaises(MalformedMessageException):
            Decoder(hx("0b")).next()

    def test_decoder_walk_and_type_check(self):
        d = Decoder(hx("08 96 01 12 07 74 65 73 74 69 6e 67"))
        self.assertTrue(d.next() and d.field() == 1 and d.type() is WireType.VARINT and d.uint64() == 150
                        and d.next() and d.field() == 2 and d.type() is WireType.LENGTH_DELIMITED
                        and d.string() == "testing" and not d.next())
        x = Decoder(hx("08 96 01"))
        x.next()
        with self.assertRaises(MalformedMessageException):
            x.string()


class SkipTest(unittest.TestCase):
    def test_every_wire_type_is_skipped(self):
        mixed = (Encoder().uint64(9, 300).fixed64(10, 7).string(11, "skip me")
                 .message(12, Encoder().uint64(1, 1)).uint64(1, 42).to_byte_array())
        sk = Decoder(mixed)
        found = -1
        while sk.next():
            if sk.field() == 1:
                found = sk.uint64()
            else:
                sk.skip()
        self.assertTrue(found == 42 and sk.skipped == 4)
        f32 = Decoder(hx("2d 01 02 03 04 08 05"))  # field 5 FIXED32, then field 1 = 5
        got = -1
        while f32.next():
            if f32.field() == 1:
                got = f32.uint64()
            else:
                f32.skip()
        self.assertEqual(got, 5)


class PostingTest(unittest.TestCase):
    def test_round_trip_and_evolution(self):
        bin_ = SAMPLE.encode()
        self.assertEqual(PostingV2.decode(bin_), SAMPLE)
        self.assertEqual(len(bin_), 94)
        v1_view = Decoder(bin_)
        as_v1 = PostingV1.decode(v1_view)
        self.assertEqual(as_v1, SAMPLE.as_v1())
        self.assertEqual(v1_view.skipped, 3)
        old = PostingV1(7, Money.of_minor(1250, USD), "acct:a", "acct:b")
        up = PostingV2.decode(old.encode())
        self.assertTrue(up.memo == "" and up.audit is None and not up.reversal and up.id == 7 and up.amount == old.amount)
        self.assertEqual(PostingV2(7, old.amount, "acct:a", "acct:b", "", None, False).encode(), old.encode())
        self.assertEqual(PostingV2.decode(bin_).amount.minor_units, -1999)
        self.assertEqual(str(PostingV1.decode(PostingV1(1, Money.of_minor(1200, JPY), "a", "b").encode()).amount), "1200 JPY")
        self.assertEqual(PostingV1.decode(PostingV1(1, Money.of_minor(1, USD), "acct:éè€", "b").encode()).debit_account,
                         "acct:éè€")

    def test_malformed(self):
        bin_ = SAMPLE.encode()
        with self.assertRaises(MalformedMessageException):
            PostingV1.decode(Encoder().uint64(1, 1).to_byte_array())
        with self.assertRaises(MalformedMessageException):
            PostingV2.decode(bin_[:-3])
        with self.assertRaises(MalformedMessageException):
            PostingV2.decode(Encoder().string(PostingV1.F_AMOUNT, "19.99").string(3, "USD").to_byte_array())

    def test_random_postings_round_trip(self):
        rng = JavaRandom(11)
        self.assertTrue(all(PostingV2.decode(q.encode()) == q for q in (random_posting(rng) for _ in range(2_000))))


class JsonTest(unittest.TestCase):
    def test_json(self):
        self.assertEqual(Json.quote('a"b\\c\nd\u0001'), '"a\\"b\\\\c\\nd\\u0001"')
        self.assertEqual(Json.posting(PostingV2(5, Money.of_minor(-1, USD), "a", "b", "", None, False), True),
                         '{"id":"5","amountMinor":"-1","currency":"USD","debitAccount":"a","creditAccount":"b"}')
        self.assertEqual(int(float(9_007_199_254_740_993)), 9_007_199_254_740_992)
        self.assertEqual(int(float(9_007_199_254_740_992)), 9_007_199_254_740_992)
        self.assertLess(len(SAMPLE.encode()), len(Json.posting(SAMPLE, False).encode("utf-8")))


if __name__ == "__main__":
    unittest.main()
