"""Tests for phases/12-payment-systems-design/05-webhooks-and-callbacks. Run: python3 -m unittest -v"""

import re
import unittest

from main import (HEADER_NAME, Delivery, DeliveryState, Event, FakeClock, FakeEndpoint, Handled, Outcome,
                  Verification, WebhookConsumer, WebhookDispatcher, WebhookSigner, WebhookVerifier)

SECRET = "whsec_test_secret_0123456789abcdef"


class WebhookTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(1_700_000_000)
        self.signer = WebhookSigner(SECRET, self.clock)
        self.verifier = WebhookVerifier(SECRET, self.clock)
        self.ev = Event("evt_1", "payment.captured", "pay_1", 1, self.clock.now_seconds(), "captured")
        self.payload = self.ev.to_json()
        self.header = self.signer.header(self.payload)

    def test_hmac_vectors(self) -> None:
        # RFC 2104 / RFC 4231 test vector: key = 0x0b * 20, data = "Hi There"
        key = bytes([0x0B] * 20)
        self.assertEqual(WebhookSigner.hex(WebhookSigner.hmac_sha256(key, b"Hi There")),
                         "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7")
        self.assertRegex(self.signer.signature(1, "x"), r"^[0-9a-f]{64}$")

    def test_signing_and_verification(self) -> None:
        now = self.clock.now_seconds()
        self.assertRegex(self.header, r"^t=\d+,v1=[0-9a-f]{64}$")
        self.assertIs(self.verifier.verify(self.payload, self.header), Verification.OK)
        self.assertEqual(self.signer.header(self.payload), self.header, "deterministic for same ts+payload")
        self.assertNotEqual(self.signer.signature(now, self.payload), self.signer.signature(now, self.payload + " "))
        self.assertIs(self.verifier.verify(self.payload.replace("captured", "refunded"), self.header), Verification.BAD_SIGNATURE)
        self.assertIs(WebhookVerifier("whsec_other_secret_0000000000", self.clock).verify(self.payload, self.header),
                      Verification.BAD_SIGNATURE)
        shifted = self.header.replace(f"t={now}", f"t={now + 1}")
        self.assertIs(self.verifier.verify(self.payload, shifted), Verification.BAD_SIGNATURE, "timestamp is bound into the MAC")

    def test_malformed_headers(self) -> None:
        self.assertIs(self.verifier.verify(self.payload, ""), Verification.MALFORMED_HEADER)
        self.assertIs(self.verifier.verify(self.payload, None), Verification.MALFORMED_HEADER)
        self.assertIs(self.verifier.verify(self.payload, "t=123"), Verification.MALFORMED_HEADER)
        self.assertIs(self.verifier.verify(self.payload, "t=abc,v1=" + "0" * 64), Verification.MALFORMED_HEADER)
        self.assertIs(self.verifier.verify(self.payload, "t=1,v1=abcd"), Verification.MALFORMED_HEADER)
        self.assertIs(self.verifier.verify(self.payload, self.header + ",v0=deadbeef"), Verification.OK, "unknown scheme ignored")
        self.assertIs(self.verifier.verify(self.payload, self.header + ",v1=" + "f" * 64), Verification.OK, "any one v1 matching passes")

    def test_tolerance_window(self) -> None:
        self.clock.advance(300)
        self.assertIs(self.verifier.verify(self.payload, self.header), Verification.OK, "exactly at tolerance still ok")
        self.clock.advance(1)
        self.assertIs(self.verifier.verify(self.payload, self.header), Verification.STALE_TIMESTAMP)
        self.assertIs(self.verifier.verify(self.payload + "x", self.header), Verification.STALE_TIMESTAMP, "stale checked first")
        future_ts = self.clock.now_seconds() + 400
        future = f"t={future_ts},v1={self.signer.signature(future_ts, self.payload)}"
        self.assertIs(self.verifier.verify(self.payload, future), Verification.STALE_TIMESTAMP)
        self.assertIs(WebhookVerifier(SECRET, self.clock, 3600).verify(self.payload, self.header), Verification.OK)

    def test_constant_time_compare_and_hex(self) -> None:
        self.assertTrue(WebhookVerifier.constant_time_equals(bytes([1, 2, 3]), bytes([1, 2, 3])))
        self.assertFalse(WebhookVerifier.constant_time_equals(bytes([1, 2, 3]), bytes([1, 2, 4])))
        self.assertFalse(WebhookVerifier.constant_time_equals(bytes([1, 2]), bytes([1, 2, 3])))
        self.assertEqual(WebhookSigner.hex(WebhookVerifier.unhex("00ff7a")), "00ff7a")
        with self.assertRaises(ValueError):
            WebhookSigner("short", self.clock)

    def test_event_json_round_trip(self) -> None:
        self.assertEqual(Event.from_json(self.payload), self.ev)
        with self.assertRaises(ValueError):
            Event.from_json('{"id":"x"}')

    def test_delivery_happy_path(self) -> None:
        ok = FakeEndpoint()
        dsp = WebhookDispatcher(self.clock, self.signer, ok)
        d = dsp.enqueue(self.ev)
        self.assertEqual(dsp.tick(), 1)
        self.assertIs(d.state, DeliveryState.DELIVERED)
        self.assertEqual(d.attempts, 1)
        self.assertIn(HEADER_NAME, ok.received_headers[0])
        self.assertEqual(ok.received_headers[0]["Webhook-Attempt"], "1")
        self.assertEqual(dsp.tick(), 0, "second tick does nothing")

    def test_backoff_schedule_is_exact(self) -> None:
        flaky = FakeEndpoint().fail_with(503, 500, 429)
        dsp2 = WebhookDispatcher(self.clock, self.signer, flaky, (10, 20, 40))
        t0 = self.clock.now_seconds()
        d2 = dsp2.enqueue(self.ev)
        dsp2.tick()
        self.assertEqual(d2.next_attempt_at, t0 + 10)
        self.assertEqual(dsp2.tick(), 0, "not retried before backoff elapses")
        self.clock.advance(9)
        self.assertEqual(dsp2.tick(), 0, "still not due at 9s")
        self.clock.advance(1)
        self.assertEqual(dsp2.tick(), 1, "due at exactly 10s")
        self.assertEqual(d2.attempts, 2)
        dsp2.drain(self.clock)
        self.assertIs(d2.state, DeliveryState.DELIVERED)
        self.assertEqual(d2.attempts, 4)
        self.assertEqual(d2.attempt_times, [t0, t0 + 10, t0 + 30, t0 + 70])
        self.assertEqual(d2.statuses, [503, 500, 429, 200])
        self.assertTrue(flaky.received_headers[0][HEADER_NAME].startswith(f"t={t0 + 70}"), "retry is re-signed")
        self.assertEqual(len(flaky.received), 1, "payload delivered exactly once here")

    def test_dead_letter(self) -> None:
        down = FakeEndpoint().fail_with(500, 500, 500, 500, 500, 500, 500, 500)
        dsp3 = WebhookDispatcher(self.clock, self.signer, down, (1, 2, 4))
        d3 = dsp3.enqueue(self.ev)
        dsp3.drain(self.clock)
        self.assertEqual(dsp3.max_attempts(), 4)
        self.assertEqual(d3.attempts, 4)
        self.assertIs(d3.state, DeliveryState.DEAD_LETTERED)
        self.assertIn(d3, dsp3.dead_letters)
        self.assertEqual(dsp3.tick(), 0)
        self.assertEqual(down.calls, 4, "no further attempts after dead letter")
        self.assertEqual(WebhookDispatcher(self.clock, self.signer, down).max_attempts(), 6)

    def test_consumer_dedupe_ordering_rejection(self) -> None:
        consumer = WebhookConsumer(self.verifier)
        now = self.clock.now_seconds()
        a1 = Event("evt_a1", "payment.authorized", "pay_9", 1, now, "authorized")
        a2 = Event("evt_a2", "payment.captured", "pay_9", 2, now, "captured")
        h2 = {HEADER_NAME: self.signer.header(a2.to_json())}
        h1 = {HEADER_NAME: self.signer.header(a1.to_json())}
        self.assertIs(consumer.handle(a2.to_json(), h2).outcome, Outcome.ACCEPTED)
        self.assertEqual(consumer.handle(a2.to_json(), h2), Handled(200, Outcome.DUPLICATE, Verification.OK))
        self.assertIs(consumer.handle(a1.to_json(), h1).outcome, Outcome.OUT_OF_ORDER)
        self.assertEqual(consumer.object_status["pay_9"], "captured", "older sequence does not roll state back")
        forged = {HEADER_NAME: f"t={now},v1=" + "0" * 64}
        self.assertEqual(consumer.handle(a2.to_json(), forged).http_status, 400)
        self.assertIs(consumer.handle(a2.to_json(), {}).verification, Verification.MALFORMED_HEADER)
        self.assertEqual(consumer.latest_sequence["pay_9"], 2)
        self.assertEqual(sum(1 for s in consumer.log if s.startswith("applied")), 1, "rejected events are not marked seen")

    def test_end_to_end_redelivery_is_absorbed(self) -> None:
        c2 = WebhookConsumer(self.verifier)
        a2 = Event("evt_a2", "payment.captured", "pay_9", 2, self.clock.now_seconds(), "captured")

        class FlakyConsumer:
            n = 0

            def receive(self, p: str, h: dict[str, str]) -> int:
                self.n += 1
                status = c2.handle(p, h).http_status
                return 504 if self.n == 1 else status  # consumer processed it, but the producer saw a timeout

        dsp4 = WebhookDispatcher(self.clock, self.signer, FlakyConsumer(), (5,))
        d4 = dsp4.enqueue(a2)
        dsp4.drain(self.clock)
        self.assertEqual(d4.attempts, 2)
        self.assertIs(d4.state, DeliveryState.DELIVERED)
        self.assertEqual(sum(1 for s in c2.log if s.startswith("duplicate")), 1)
        self.assertEqual(sum(1 for s in c2.log if s.startswith("applied")), 1)

    def test_fake_clock_refuses_to_go_backwards(self) -> None:
        with self.assertRaises(ValueError):
            self.clock.advance(-1)


if __name__ == "__main__":
    unittest.main()
