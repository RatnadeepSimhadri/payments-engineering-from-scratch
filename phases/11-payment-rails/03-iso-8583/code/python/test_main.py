"""Tests for phases/11-payment-rails/03-iso-8583. Run: python3 -m unittest -v"""
import unittest

from main import INR, JPY, TEST_PAN, USD, Bitmap, Iso8583Error, Iso8583Message, Iso8583Spec, Money

EXPECTED_WIRE = (
    "0200" + "7238040008C08000"
    + "16" + "4111111111111111" + "000000" + "000000001999" + "1012143015" + "000123" + "143015" + "1012"
    + "051" + "123456789012" + "TERM0001" + "MERCHANT0000001" + "840"
)


def purchase(spec: Iso8583Spec) -> Iso8583Message:
    return (
        Iso8583Message("0200", spec)
        .set(2, TEST_PAN).set(3, "000000").set_amount(Money.of_minor(1999, USD))
        .set(7, "1012143015").set(11, "000123").set(12, "143015").set(13, "1012").set(22, "051")
        .set(37, "123456789012").set(41, "TERM0001").set(42, "MERCHANT0000001")
    )


class BitmapTest(unittest.TestCase):
    def test_hex_for_worked_example(self):
        bm = Bitmap()
        for n in (2, 3, 4, 7, 11, 12, 13, 22, 37, 41, 42, 49):
            bm.set(n)
        self.assertEqual(bm.to_hex(), "7238040008C08000")

    def test_round_trip_and_bit_39(self):
        bm = Bitmap.from_hex("7238040008C08000")
        self.assertTrue(bm.is_set(49))
        self.assertFalse(bm.is_set(39))
        bm.set(39)
        self.assertEqual(bm.to_hex(), "723804000AC08000")

    def test_rejects_non_hex_and_wrong_length(self):
        with self.assertRaises(Iso8583Error):
            Bitmap.from_hex("ZZ38040008C08000")
        with self.assertRaises(Iso8583Error):
            Bitmap.from_hex("7238")


class MtiTest(unittest.TestCase):
    def setUp(self):
        self.spec = Iso8583Spec.standard_1987()

    def test_mti_digits(self):
        req = purchase(self.spec)
        self.assertEqual((req.mti_version(), req.mti_class(), req.mti_function(), req.mti_origin()), (0, 2, 0, 0))

    def test_response_mti(self):
        self.assertEqual(purchase(self.spec).response_mti(), "0210")
        self.assertEqual(Iso8583Message("0420", self.spec).response_mti(), "0430")
        with self.assertRaises(Iso8583Error):
            Iso8583Message("0210", self.spec).response_mti()

    def test_mti_must_be_four_digits(self):
        with self.assertRaises(Iso8583Error):
            Iso8583Message("02A0", self.spec)
        with self.assertRaises(Iso8583Error):
            Iso8583Message("200", self.spec)


class EncodeDecodeTest(unittest.TestCase):
    def setUp(self):
        self.spec = Iso8583Spec.standard_1987()
        self.req = purchase(self.spec)
        self.wire = self.req.encode()

    def test_encode_matches_hand_built_message(self):
        self.assertEqual(len(self.wire), 123)
        self.assertEqual(self.wire.decode("ascii"), EXPECTED_WIRE)
        self.assertEqual(self.wire[20:22], b"16")

    def test_decode_fields(self):
        back = Iso8583Message.decode(self.wire, self.spec)
        self.assertEqual(back.mti, "0200")
        self.assertEqual(back.get(2), TEST_PAN)
        self.assertEqual(back.amount(), Money.of_minor(1999, USD))
        self.assertEqual((back.get(11), back.get(37)), ("000123", "123456789012"))
        self.assertEqual(back.fields().keys(), self.req.fields().keys())

    def test_round_trip_is_byte_identical(self):
        self.assertEqual(Iso8583Message.decode(self.wire, self.spec).encode(), self.wire)

    def test_absent_field(self):
        back = Iso8583Message.decode(self.wire, self.spec)
        self.assertFalse(back.has(39))
        with self.assertRaises(Iso8583Error):
            back.get(39)

    def test_secondary_bitmap(self):
        msg = (
            Iso8583Message("0200", self.spec)
            .set(2, TEST_PAN).set(3, "000000").set_amount(Money.of_minor(500000, INR))
            .set(11, "000124").set(102, "12345678901")
        )
        text = msg.encode().decode("ascii")
        self.assertEqual(text[4:20], "F020000000008000")
        self.assertEqual(text[20:36], "0000000004000000")
        back = Iso8583Message.decode(msg.encode(), self.spec)
        self.assertEqual(back.get(102), "12345678901")
        self.assertEqual(back.amount(), Money.of_minor(500000, INR))
        self.assertEqual(self.wire.decode("ascii")[4], "7")  # no secondary bitmap below DE65

    def test_lllvar(self):
        data = "A" * 250
        msg = Iso8583Message("0800", self.spec).set(11, "000001").set(123, data)
        self.assertTrue(msg.encode().decode("ascii").endswith("250" + data))
        self.assertEqual(Iso8583Message.decode(msg.encode(), self.spec).get(123), data)


class FieldValidationTest(unittest.TestCase):
    def setUp(self):
        self.spec = Iso8583Spec.standard_1987()
        self.msg = Iso8583Message("0200", self.spec)

    def test_fixed_wrong_length(self):
        with self.assertRaises(Iso8583Error):
            self.msg.set(3, "00000")

    def test_numeric_with_dot(self):
        with self.assertRaises(Iso8583Error):
            self.msg.set(4, "000000019.99")

    def test_llvar_over_max(self):
        with self.assertRaises(Iso8583Error):
            self.msg.set(2, "4" * 20)

    def test_undefined_and_empty(self):
        with self.assertRaises(Iso8583Error):
            self.msg.set(64, "DEADBEEF")
        with self.assertRaises(Iso8583Error):
            self.msg.set(41, "")

    def test_amount_helpers(self):
        with self.assertRaises(Iso8583Error):
            self.msg.set_amount(Money.of_minor(-1, USD))
        self.msg.set_amount(Money.of_minor(5, JPY))
        self.assertEqual((self.msg.get(4), self.msg.get(49)), ("000000000005", "392"))
        self.assertEqual(str(Iso8583Message("0200", self.spec).set_amount(Money.of_minor(1200, JPY)).amount()), "1200 JPY")

    def test_money_never_from_float(self):
        with self.assertRaises(TypeError):
            Money(19.99, USD)  # type: ignore[arg-type]


class MalformedDecodeTest(unittest.TestCase):
    def setUp(self):
        self.spec = Iso8583Spec.standard_1987()

    def decode(self, text: str) -> Iso8583Message:
        return Iso8583Message.decode(text.encode("ascii"), self.spec)

    def test_truncated(self):
        with self.assertRaises(Iso8583Error):
            self.decode(EXPECTED_WIRE[:60])
        with self.assertRaises(Iso8583Error):
            self.decode("0200723804")

    def test_trailing_bytes(self):
        with self.assertRaises(Iso8583Error):
            self.decode(EXPECTED_WIRE + "XYZ")

    def test_bad_length_prefix(self):
        with self.assertRaises(Iso8583Error):
            self.decode("0200" + "7238040008C08000" + "99" + EXPECTED_WIRE[22:])
        with self.assertRaises(Iso8583Error):
            self.decode("0200" + "7238040008C08000" + "1A" + EXPECTED_WIRE[22:])

    def test_bit_for_undefined_de(self):
        with self.assertRaises(Iso8583Error):
            self.decode("0200" + "7238040008C08001" + EXPECTED_WIRE[20:] + "X")

    def test_bitmap_problems(self):
        with self.assertRaises(Iso8583Error):
            self.decode("0200" + "F000000000000000")
        with self.assertRaises(Iso8583Error):
            self.decode("0200" + "8000000000000000" + "8000000000000000")


class MaskingTest(unittest.TestCase):
    def test_masked_pan_and_dump(self):
        req = purchase(Iso8583Spec.standard_1987())
        self.assertEqual(req.masked_pan(), "411111******1111")
        self.assertNotIn(TEST_PAN, req.dump())
        self.assertIn("411111******1111", req.dump())


if __name__ == "__main__":
    unittest.main()
