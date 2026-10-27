"""Stage 4 tests for projects/idempotent-payment-gateway (Python): HMAC-SHA256 signatures over "<timestamp>.<body>",
verification inside a tolerance window with a constant-time compare, at-least-once delivery on a retry schedule with
dead-lettering, consumer dedupe by event id, and the Gateway emitting lifecycle events.
Port of Stage04Test.java: same checks, same names, same order."""

import re
import unittest

from main import (
    USD,
    BackoffPolicy,
    ChargeRequest,
    CircuitBreaker,
    ConsumerEndpoint,
    FakeClock,
    Gateway,
    IdempotencyStore,
    Money,
    ReceiveStatus,
    RetryBudget,
    RetryingClient,
    Rng,
    ScriptedEndpoint,
    ScriptedProcessor,
    VerifyResult,
    WebhookConsumer,
    WebhookDispatcher,
    WebhookEvent,
    WebhookSigner,
    WebhookVerifier,
)

S4_T0 = 1_700_000_000_000


def s4_event(id: str) -> WebhookEvent:
    return WebhookEvent(id, "payment.succeeded", "pay_1", "amount=19.99 USD", S4_T0)


class Stage04WebhookTest(unittest.TestCase):
    def test_event_body_is_fixed_order_json_and_round_trips(self):
        evt = s4_event("evt_1")
        body = evt.to_body()
        self.assertEqual(body, '{"id":"evt_1","type":"payment.succeeded","payment_id":"pay_1","data":"amount=19.99 USD","created_at":1700000000000}', "body is fixed-order JSON")
        self.assertEqual(WebhookEvent.parse(body), evt, "parse inverts to_body")
        with self.assertRaises(ValueError, msg="malformed body rejected"):
            WebhookEvent.parse('{"id":"evt_1"}')
        with self.assertRaises(ValueError, msg="quotes in fields rejected"):
            WebhookEvent('evt"1', "t", "p", "d", 0)
        with self.assertRaises(ValueError, msg="blank id rejected"):
            WebhookEvent(" ", "t", "p", "d", 0)

    def test_hmac_sha256_and_the_signature_header(self):
        body = s4_event("evt_1").to_body()
        self.assertEqual(WebhookSigner.hmac_sha256_hex(b"key", "The quick brown fox jumps over the lazy dog"),
                         "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8", "hmac known vector")
        signer = WebhookSigner("whsec_test")
        sig = signer.signature(S4_T0, body)
        self.assertTrue(len(sig) == 64 and re.fullmatch(r"[0-9a-f]{64}", sig) is not None, "signature is 64 lowercase hex chars")
        self.assertEqual(sig, "c189aab766a9d122c3d7fed8734fab41c76bd4290e69b3639d4e841f309816fd", "signature known value")
        self.assertEqual(signer.header(S4_T0, body), "t=1700000000000,v1=" + sig, "header format")
        self.assertNotEqual(signer.signature(S4_T0 + 1, body), sig, "signature binds the timestamp")
        self.assertNotEqual(signer.signature(S4_T0, body.replace("19.99", "190.99")), sig, "signature binds the body")
        self.assertNotEqual(WebhookSigner("whsec_other").signature(S4_T0, body), sig, "signature binds the secret")
        with self.assertRaises(ValueError, msg="empty secret rejected"):
            WebhookSigner("")

    def test_verification_window_constant_time_compare_malformed_headers(self):
        body = s4_event("evt_1").to_body()
        signer = WebhookSigner("whsec_test")
        sig = signer.signature(S4_T0, body)
        clock = FakeClock(S4_T0)
        verifier = WebhookVerifier("whsec_test", 5 * 60_000, clock)
        header = signer.header(S4_T0, body)
        self.assertIs(verifier.verify(body, header), VerifyResult.OK, "valid header verifies")
        self.assertIs(verifier.verify(body.replace("pay_1", "pay_2"), header), VerifyResult.BAD_SIGNATURE, "tampered body fails")
        self.assertIs(WebhookVerifier("whsec_other", 5 * 60_000, clock).verify(body, header), VerifyResult.BAD_SIGNATURE, "wrong secret fails")
        self.assertIs(verifier.verify(body, f"t={S4_T0 + 1},v1={sig}"), VerifyResult.BAD_SIGNATURE, "forged timestamp fails the signature")
        self.assertIs(verifier.verify(body, "t=1700000000000"), VerifyResult.MALFORMED_HEADER, "missing v1 is malformed")
        self.assertIs(verifier.verify(body, "t=now,v1=" + sig), VerifyResult.MALFORMED_HEADER, "non-numeric timestamp is malformed")
        self.assertIs(verifier.verify(body, None), VerifyResult.MALFORMED_HEADER, "None header is malformed")
        clock.advance(5 * 60_000)
        self.assertIs(verifier.verify(body, header), VerifyResult.OK, "exactly at the tolerance edge still OK")
        clock.advance(1)
        self.assertIs(verifier.verify(body, header), VerifyResult.STALE_TIMESTAMP, "one ms past tolerance is STALE")
        clock.set(S4_T0 - 5 * 60_000 - 1)
        self.assertIs(verifier.verify(body, header), VerifyResult.STALE_TIMESTAMP, "a signature from the future is also STALE")
        clock.set(S4_T0)
        self.assertTrue(WebhookVerifier("whsec_test", 0, clock).verify(body, header) is VerifyResult.OK
                        and WebhookVerifier("whsec_test", 0, FakeClock(S4_T0 + 1)).verify(body, header) is VerifyResult.STALE_TIMESTAMP,
                        "zero tolerance accepts only the exact instant")

    def test_consumer_verify_dedupe_by_id_process_once(self):
        evt = s4_event("evt_1")
        body = evt.to_body()
        signer = WebhookSigner("whsec_test")
        clock = FakeClock(S4_T0)
        verifier = WebhookVerifier("whsec_test", 5 * 60_000, clock)
        header = signer.header(S4_T0, body)
        consumer = WebhookConsumer(verifier)
        r1 = consumer.receive(body, header)
        self.assertTrue(r1.status is ReceiveStatus.ACCEPTED and r1.verification is VerifyResult.OK and r1.event == evt, "first delivery ACCEPTED")
        r2 = consumer.receive(body, header)
        self.assertTrue(r2.status is ReceiveStatus.DUPLICATE and len(consumer.processed()) == 1, "redelivery is DUPLICATE, not processed twice")
        self.assertIs(consumer.receive(body, signer.header(S4_T0 + 1000, body)).status, ReceiveStatus.DUPLICATE, "redelivery with a fresh signature is still DUPLICATE")
        r3 = consumer.receive(body, f"t={S4_T0},v1=" + "0" * 64)
        self.assertTrue(r3.status is ReceiveStatus.REJECTED and r3.verification is VerifyResult.BAD_SIGNATURE and r3.event is None, "bad signature REJECTED with the reason")
        other = s4_event("evt_2").to_body()
        self.assertTrue(consumer.receive(other, signer.header(S4_T0, other)).status is ReceiveStatus.ACCEPTED and len(consumer.processed()) == 2, "a different event id is ACCEPTED")
        garbage = '{"nope":1}'
        self.assertIs(consumer.receive(garbage, signer.header(S4_T0, garbage)).status, ReceiveStatus.REJECTED, "verified but unparsable body is REJECTED")

    def test_dispatcher_at_least_once_on_a_schedule_then_dead_letter(self):
        signer = WebhookSigner("whsec_test")
        dc = FakeClock(S4_T0)
        flaky = ScriptedEndpoint("FFS")
        dispatcher = WebhookDispatcher(signer, flaky, dc, [60_000, 300_000])
        self.assertEqual(dispatcher.max_attempts(), 3, "max_attempts is retries + 1")
        dispatcher.enqueue(s4_event("evt_10"))
        self.assertTrue(len(dispatcher.pending()) == 1 and dispatcher.pending()[0].attempts == 0 and dispatcher.pending()[0].next_attempt_at == S4_T0, "enqueued job is pending with 0 attempts")
        self.assertTrue(dispatcher.run_due() == 1 and len(flaky.deliveries()) == 1 and len(dispatcher.delivered()) == 0, "first run attempts once and fails")
        self.assertTrue(len(dispatcher.pending()) == 1 and dispatcher.pending()[0].attempts == 1 and dispatcher.pending()[0].next_attempt_at == S4_T0 + 60_000, "retry scheduled 60s later")
        self.assertTrue(dispatcher.run_due() == 0 and len(flaky.deliveries()) == 1, "nothing due before the schedule")
        dc.advance(60_000)
        self.assertTrue(dispatcher.run_due() == 1 and dispatcher.pending()[0].attempts == 2 and dispatcher.pending()[0].next_attempt_at == S4_T0 + 360_000, "second attempt at +60s fails")
        dc.advance(300_000)
        self.assertTrue(dispatcher.run_due() == 1 and len(dispatcher.delivered()) == 1 and len(dispatcher.pending()) == 0 and len(dispatcher.dead_letters()) == 0, "third attempt at +360s succeeds")
        self.assertTrue(flaky.deliveries()[0].header.startswith(f"t={S4_T0},")
                        and flaky.deliveries()[2].header.startswith(f"t={S4_T0 + 360_000},"), "every attempt was signed with the clock at send time")
        self.assertEqual(flaky.deliveries()[0].body, flaky.deliveries()[2].body, "every attempt carried the same body")
        self.assertIs(WebhookVerifier("whsec_test", 1000, FakeClock(S4_T0 + 360_000)).verify(flaky.deliveries()[2].body, flaky.deliveries()[2].header),
                      VerifyResult.OK, "each delivery verifies against a clock at send time")

        down = ScriptedEndpoint("F")
        dead = WebhookDispatcher(signer, down, dc, [1_000])
        dead.enqueue(s4_event("evt_20"))
        dead.enqueue(s4_event("evt_21"))
        self.assertEqual(dead.run_due(), 2, "two due jobs run in one pass")
        dc.advance(1_000)
        self.assertTrue(dead.run_due() == 2 and len(dead.dead_letters()) == 2 and len(dead.pending()) == 0, "after the schedule both are dead-lettered")
        self.assertTrue(dead.dead_letters()[0].event.id == "evt_20" and dead.dead_letters()[0].attempts == 2, "dead letter keeps the event and attempt count")
        self.assertTrue(dead.run_due() == 0 and len(down.deliveries()) == 4, "no further attempts once dead")
        with self.assertRaises(ValueError, msg="negative retry delay rejected"):
            WebhookDispatcher(signer, down, dc, [-1])

    def test_end_to_end_gateway_emits_dispatcher_delivers_consumer_processes_once(self):
        gc = FakeClock(S4_T0)
        store = IdempotencyStore(gc, 60_000)
        client = RetryingClient(ScriptedProcessor("ADU"), BackoffPolicy(100, 2_000, 1), RetryBudget(10, 10), CircuitBreaker(gc, 10, 30_000, 1), Rng(42), gc)
        gateway = Gateway(store, client, gc)
        merchant = WebhookConsumer(WebhookVerifier("whsec_live", 5 * 60_000, gc))
        live = WebhookDispatcher(WebhookSigner("whsec_live"), ConsumerEndpoint(merchant), gc, [60_000])
        gateway.attach_webhooks(live)
        gateway.charge(ChargeRequest("m_1", "k1", Money.parse("19.99", USD), "tok_visa", "order-1"))
        gateway.charge(ChargeRequest("m_1", "k1", Money.parse("19.99", USD), "tok_visa", "order-1"))
        gateway.charge(ChargeRequest("m_1", "k2", Money.parse("5.00", USD), "tok_visa", "order-2"))
        gateway.charge(ChargeRequest("m_1", "k3", Money.parse("7.00", USD), "tok_visa", "order-3"))
        self.assertTrue(len(live.pending()) == 2
                        and live.pending()[0].event.type == "payment.succeeded" and live.pending()[0].event.payment_id == "pay_1"
                        and live.pending()[1].event.type == "payment.failed" and live.pending()[1].event.payment_id == "pay_2",
                        "succeeded and failed emit, replay and unavailable do not")
        self.assertNotEqual(live.pending()[0].event.id, live.pending()[1].event.id, "event ids are unique")
        self.assertEqual(live.pending()[0].event.data, "amount=19.99 USD", "event data carries the amount")
        live.run_due()
        self.assertTrue(len(live.delivered()) == 2 and len(merchant.processed()) == 2, "both events delivered and processed once")
        delivered = merchant.processed()[0]
        self.assertTrue(merchant.receive(delivered.to_body(), WebhookSigner("whsec_live").header(gc.now_millis(), delivered.to_body())).status is ReceiveStatus.DUPLICATE
                        and len(merchant.processed()) == 2, "redelivering a processed event is a 2xx DUPLICATE")


if __name__ == "__main__":
    unittest.main()
