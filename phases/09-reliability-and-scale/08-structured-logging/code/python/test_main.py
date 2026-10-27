"""Tests for phases/09-reliability-and-scale/08-structured-logging. Run: python3 -m unittest -v"""
import unittest

from main import FakeClock, Json, Level, Logger, LogRecord, LogStore, PaymentFlow, Redactor, Sampler

TRACE = "4bf92f3577b34da6a3ce929d0e0e4736"


class JsonTest(unittest.TestCase):
    def test_serialization(self):
        rec = LogRecord(1700000000000, Level.INFO, "payments", 'he said "hi"\n', {"k": "v\\w"})
        json = rec.to_json()
        self.assertTrue(json.startswith('{"ts":1700000000000,'))
        self.assertIn('"msg":"he said \\"hi\\"\\n"', json)
        self.assertIn('"k":"v\\\\w"', json)
        self.assertNotIn("\n", json)
        self.assertEqual(Json.escape("a\u0001b"), "a\\u0001b")
        self.assertEqual("".join(Json.pair([], "x", None, True)), '"x":null')


class LevelTest(unittest.TestCase):
    def test_levels(self):
        self.assertTrue(Level.ERROR.at_least(Level.WARN))
        self.assertFalse(Level.DEBUG.at_least(Level.INFO))
        self.assertTrue(Level.ERROR.syslog == 3 and Level.DEBUG.syslog == 7)


class RedactorTest(unittest.TestCase):
    def test_redaction(self):
        red = Redactor()
        self.assertTrue(Redactor.luhn_valid("4111111111111111"))
        self.assertFalse(Redactor.luhn_valid("4111111111111112"))
        self.assertEqual(Redactor.mask("4111111111111111"), "411111******1111")
        self.assertEqual(red.mask_pans("card 4111 1111 1111 1111 ok"), "card 411111******1111 ok")
        self.assertEqual(red.mask_pans("5555-5555-5555-4444"), "555555******4444")
        self.assertEqual(red.mask_pans("4222222222222"), "422222***2222")
        self.assertEqual(red.mask_pans("order 1234567890123 shipped"), "order 1234567890123 shipped")
        self.assertEqual(red.mask_pans("ref 411111111111"), "ref 411111111111")
        self.assertEqual(red.mask_pans("id 94111111111111111"), "id 94111111111111111")
        self.assertEqual(red.mask_pans("4111111111111111 and 5555555555554444"), "411111******1111 and 555555******4444")
        self.assertEqual(red.redact_value("api_key", "sk_live_x"), "[REDACTED]")
        self.assertEqual(red.redact_value("Authorization", "Bearer abc"), "[REDACTED]")
        self.assertEqual(red.redact_value("merchant_id", "acme"), "acme")
        self.assertEqual(red.redact_value("note", "pan 4111111111111111"), "pan 411111******1111")
        self.assertEqual(red.redactions, 8)


class SamplerTest(unittest.TestCase):
    def test_sampling(self):
        s = Sampler(4, 4)
        self.assertTrue(s.keep(Level.ERROR, "0af7651916cd43dd8448eb211c80319c", "x"))
        self.assertTrue(s.keep(Level.WARN, "0af7651916cd43dd8448eb211c80319c", "x"))
        self.assertTrue(s.keep(Level.INFO, "1af7651916cd43dd8448eb211c80319c", "x"))
        self.assertFalse(s.keep(Level.INFO, "0af7651916cd43dd8448eb211c80319c", "x"))
        self.assertTrue(s.keep(Level.DEBUG, "1af7651916cd43dd8448eb211c80319c", "other")
                        and not s.keep(Level.DEBUG, "0af7651916cd43dd8448eb211c80319c", "other"))
        self.assertTrue(Sampler(1, 1).keep(Level.DEBUG, "0af7651916cd43dd8448eb211c80319c", "x"))
        self.assertTrue(Sampler.fnv1a("payments") == Sampler.fnv1a("payments") and Sampler.fnv1a("a") != Sampler.fnv1a("b"))
        with self.assertRaises(ValueError):
            Sampler(0, 1)

    def test_fnv1a_matches_java_int_arithmetic(self):
        # Java: int h = 0x811c9dc5; h ^= 'a'; h *= 0x01000193  ->  wrapped to a signed int
        self.assertEqual(Sampler.fnv1a("a"), -468965076)
        self.assertEqual(Sampler.fnv1a(""), -2128831035)  # 0x811c9dc5 wrapped


class LoggerTest(unittest.TestCase):
    def test_bound_context_minimum_level_and_index(self):
        clock = FakeClock(5)
        store = LogStore({"payment_id", "trace_id"})
        base = Logger("ledger", clock, Level.INFO, Redactor(), Sampler(1, 1), store)
        scoped = base.with_trace(TRACE, "00f067aa0ba902b7").with_("payment_id", "pay_1")
        scoped.debug("not written: below minimum")
        scoped.info("hold posted", "amount_minor", "1999", "currency", "USD")
        base.info("no context here")
        self.assertEqual(store.size(), 2)
        posted = store.query("payment_id", "pay_1")[0]
        self.assertTrue(posted.field("trace_id") == TRACE and posted.field("span_id") == "00f067aa0ba902b7")
        self.assertLess(posted.to_json().index('"payment_id"'), posted.to_json().index('"amount_minor"'))
        self.assertTrue(len(store.query("payment_id", "pay_1")) == 1 and store.all()[1].field("payment_id") is None)
        self.assertIs(store.query("trace_id", TRACE)[0], posted)
        self.assertEqual(store.query("payment_id", "nope"), [])
        self.assertEqual(posted.timestamp_millis, 5)
        with self.assertRaises(ValueError):
            scoped.info("bad", "only_key")


class EndToEndTest(unittest.TestCase):
    def test_six_services_one_payment_id(self):
        flow = PaymentFlow(Sampler(1, 1), Level.DEBUG)
        flow.charge(TRACE, "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", False)
        by_payment = flow.store.query("payment_id", "pay_8f3k2")
        self.assertTrue(len(by_payment) == flow.store.size() == 9)
        self.assertEqual([r.timestamp_millis for r in by_payment], sorted(r.timestamp_millis for r in by_payment))
        self.assertEqual(len({r.service for r in by_payment}), 6)
        self.assertTrue(all(r.field("trace_id") == TRACE for r in by_payment))
        everything = "\n".join(r.to_json() for r in by_payment)
        self.assertTrue("4111111111111111" not in everything and "4111 1111 1111 1111" not in everything)
        self.assertIn("411111******1111", everything)
        self.assertTrue(all(r.field("pan") == "[REDACTED]" for r in by_payment if r.field("pan") is not None))
        self.assertTrue('"secret":"[REDACTED]"' in everything and "whsec_live" not in everything)
        self.assertTrue('"amount_minor":"1999"' in everything and "19.99" not in everything)

    def test_sampling_preserves_failures(self):
        sampled = PaymentFlow(Sampler(4, 4), Level.DEBUG)
        sampled.charge("0af7651916cd43dd8448eb211c80319c", "pay_s0", "acme", 500, "EUR", "5555555555554444", False)
        sampled.charge("2af7651916cd43dd8448eb211c80319c", "pay_s2", "acme", 502, "EUR", "5555555555554444", True)
        self.assertEqual(sampled.store.query("trace_id", "0af7651916cd43dd8448eb211c80319c"), [])
        failed = sampled.store.query("payment_id", "pay_s2")
        self.assertTrue(len(failed) == 2 and failed[0].level == Level.ERROR and failed[1].level == Level.WARN)
        self.assertEqual(sampled.sampler.dropped + sampled.store.size(), 19)


if __name__ == "__main__":
    unittest.main()
