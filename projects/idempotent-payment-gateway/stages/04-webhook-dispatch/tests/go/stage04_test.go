// Stage 4 tests for projects/idempotent-payment-gateway (Go): HMAC-SHA256 signatures over "<timestamp>.<body>",
// verification inside a tolerance window with a constant-time compare, at-least-once delivery on a retry
// schedule with dead-lettering, consumer dedupe by event id, and the Gateway emitting lifecycle events.
// Port of Stage04Test.java: same checks, same names, same order.
package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func s04check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s04checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

const s04T0 int64 = 1_700_000_000_000

func s04event(id string) WebhookEvent {
	return must1(NewWebhookEvent(id, "payment.succeeded", "pay_1", "amount=19.99 USD", s04T0))
}

func s04signer(secret string) *WebhookSigner { return must1(NewWebhookSigner(secret)) }

func s04verifier(secret string, tolerance int64, clock Clock) *WebhookVerifier {
	return must1(NewWebhookVerifier(secret, tolerance, clock))
}

func s04endpoint(script string) *ScriptedEndpoint { return must1(NewScriptedEndpoint(script)) }

func s04dispatcher(signer *WebhookSigner, endpoint WebhookEndpoint, clock Clock, delays []int64) *WebhookDispatcher {
	return must1(NewWebhookDispatcher(signer, endpoint, clock, delays))
}

func TestStage04SignaturesAndConsumer(t *testing.T) {
	// Event body is a fixed-order JSON object and round-trips through parse
	evt := s04event("evt_1")
	body := evt.ToBody()
	s04check(t, "body is fixed-order JSON", body == `{"id":"evt_1","type":"payment.succeeded","payment_id":"pay_1","data":"amount=19.99 USD","created_at":1700000000000}`)
	parsed, err := ParseWebhookEvent(body)
	s04check(t, "parse inverts ToBody", err == nil && parsed == evt)
	_, err = ParseWebhookEvent(`{"id":"evt_1"}`)
	s04checkErr(t, "malformed body rejected", err, ErrMalformedBody)
	_, err = NewWebhookEvent(`evt"1`, "t", "p", "d", 0)
	s04checkErr(t, "quotes in fields rejected", err, nil)
	_, err = NewWebhookEvent(" ", "t", "p", "d", 0)
	s04checkErr(t, "blank id rejected", err, nil)

	// HMAC-SHA256 (RFC 2104 / RFC 4231 style vector) and the signature header
	s04check(t, "hmac known vector", HmacSha256Hex([]byte("key"), "The quick brown fox jumps over the lazy dog") == "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8")
	signer := s04signer("whsec_test")
	sig := signer.Signature(s04T0, body)
	s04check(t, "signature is 64 lowercase hex chars", len(sig) == 64 && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sig))
	s04check(t, "signature known value", sig == "c189aab766a9d122c3d7fed8734fab41c76bd4290e69b3639d4e841f309816fd")
	s04check(t, "header format", signer.Header(s04T0, body) == "t=1700000000000,v1="+sig)
	s04check(t, "signature binds the timestamp", signer.Signature(s04T0+1, body) != sig)
	s04check(t, "signature binds the body", signer.Signature(s04T0, strings.Replace(body, "19.99", "190.99", 1)) != sig)
	s04check(t, "signature binds the secret", s04signer("whsec_other").Signature(s04T0, body) != sig)
	_, err = NewWebhookSigner("")
	s04checkErr(t, "empty secret rejected", err, nil)

	// Verification: window, constant-time compare, malformed headers
	clock := NewFakeClock(s04T0)
	verifier := s04verifier("whsec_test", 5*60_000, clock)
	header := signer.Header(s04T0, body)
	s04check(t, "valid header verifies", verifier.Verify(body, header) == VerifyOK)
	s04check(t, "tampered body fails", verifier.Verify(strings.Replace(body, "pay_1", "pay_2", 1), header) == BadSignature)
	s04check(t, "wrong secret fails", s04verifier("whsec_other", 5*60_000, clock).Verify(body, header) == BadSignature)
	s04check(t, "forged timestamp fails the signature", verifier.Verify(body, "t=1700000000001,v1="+sig) == BadSignature)
	s04check(t, "missing v1 is malformed", verifier.Verify(body, "t=1700000000000") == MalformedHeader)
	s04check(t, "non-numeric timestamp is malformed", verifier.Verify(body, "t=now,v1="+sig) == MalformedHeader)
	s04check(t, "empty header is malformed", verifier.Verify(body, "") == MalformedHeader)
	clock.Advance(5 * 60_000)
	s04check(t, "exactly at the tolerance edge still OK", verifier.Verify(body, header) == VerifyOK)
	clock.Advance(1)
	s04check(t, "one ms past tolerance is STALE", verifier.Verify(body, header) == StaleTimestamp)
	clock.Set(s04T0 - 5*60_000 - 1)
	s04check(t, "a signature from the future is also STALE", verifier.Verify(body, header) == StaleTimestamp)
	clock.Set(s04T0)
	s04check(t, "zero tolerance accepts only the exact instant", s04verifier("whsec_test", 0, clock).Verify(body, header) == VerifyOK &&
		s04verifier("whsec_test", 0, NewFakeClock(s04T0+1)).Verify(body, header) == StaleTimestamp)
	_, err = NewWebhookVerifier("whsec_test", -1, clock)
	s04checkErr(t, "negative tolerance rejected", err, nil)

	// Consumer: verify, dedupe by id, process once
	consumer := NewWebhookConsumer(verifier)
	r1 := consumer.Receive(body, header)
	s04check(t, "first delivery ACCEPTED", r1.Status == Accepted && r1.Verification == VerifyOK && r1.Event != nil && *r1.Event == evt)
	r2 := consumer.Receive(body, header)
	s04check(t, "redelivery is DUPLICATE, not processed twice", r2.Status == Duplicate && len(consumer.Processed()) == 1)
	s04check(t, "redelivery with a fresh signature is still DUPLICATE", consumer.Receive(body, signer.Header(s04T0+1000, body)).Status == Duplicate)
	r3 := consumer.Receive(body, "t=1700000000000,v1="+strings.Repeat("0", 64))
	s04check(t, "bad signature REJECTED with the reason", r3.Status == Rejected && r3.Verification == BadSignature && r3.Event == nil)
	other := s04event("evt_2").ToBody()
	s04check(t, "a different event id is ACCEPTED", consumer.Receive(other, signer.Header(s04T0, other)).Status == Accepted && len(consumer.Processed()) == 2)
	garbage := `{"nope":1}`
	s04check(t, "verified but unparsable body is REJECTED", consumer.Receive(garbage, signer.Header(s04T0, garbage)).Status == Rejected)
}

func TestStage04DispatcherAndGateway(t *testing.T) {
	signer := s04signer("whsec_test")

	// Dispatcher: at-least-once on a schedule, then dead-letter
	dc := NewFakeClock(s04T0)
	flaky := s04endpoint("FFS")
	dispatcher := s04dispatcher(signer, flaky, dc, []int64{60_000, 300_000})
	s04check(t, "MaxAttempts is retries + 1", dispatcher.MaxAttempts() == 3)
	dispatcher.Enqueue(s04event("evt_10"))
	pending := dispatcher.Pending()
	s04check(t, "enqueued job is pending with 0 attempts", len(pending) == 1 && pending[0].Attempts == 0 && pending[0].NextAttemptAt == s04T0)
	s04check(t, "first run attempts once and fails", dispatcher.RunDue() == 1 && len(flaky.Deliveries()) == 1 && len(dispatcher.Delivered()) == 0)
	pending = dispatcher.Pending()
	s04check(t, "retry scheduled 60s later", len(pending) == 1 && pending[0].Attempts == 1 && pending[0].NextAttemptAt == s04T0+60_000)
	s04check(t, "nothing due before the schedule", dispatcher.RunDue() == 0 && len(flaky.Deliveries()) == 1)
	dc.Advance(60_000)
	ran := dispatcher.RunDue()
	pending = dispatcher.Pending()
	s04check(t, "second attempt at +60s fails", ran == 1 && len(pending) == 1 && pending[0].Attempts == 2 && pending[0].NextAttemptAt == s04T0+360_000)
	dc.Advance(300_000)
	s04check(t, "third attempt at +360s succeeds", dispatcher.RunDue() == 1 && len(dispatcher.Delivered()) == 1 && len(dispatcher.Pending()) == 0 && len(dispatcher.DeadLetters()) == 0)
	deliveries := flaky.Deliveries()
	if len(deliveries) != 3 {
		t.Fatalf("expected 3 deliveries, got %d", len(deliveries))
	}
	s04check(t, "every attempt was signed with the clock at send time", strings.HasPrefix(deliveries[0].Header, "t=1700000000000,") &&
		strings.HasPrefix(deliveries[2].Header, "t=1700000360000,"))
	s04check(t, "every attempt carried the same body", deliveries[0].Body == deliveries[2].Body)
	s04check(t, "each delivery verifies against a clock at send time", s04verifier("whsec_test", 1000, NewFakeClock(s04T0+360_000)).Verify(deliveries[2].Body, deliveries[2].Header) == VerifyOK)

	down := s04endpoint("F")
	dead := s04dispatcher(signer, down, dc, []int64{1_000})
	dead.Enqueue(s04event("evt_20"))
	dead.Enqueue(s04event("evt_21"))
	s04check(t, "two due jobs run in one pass", dead.RunDue() == 2)
	dc.Advance(1_000)
	s04check(t, "after the schedule both are dead-lettered", dead.RunDue() == 2 && len(dead.DeadLetters()) == 2 && len(dead.Pending()) == 0)
	s04check(t, "dead letter keeps the event and attempt count", dead.DeadLetters()[0].Event.ID == "evt_20" && dead.DeadLetters()[0].Attempts == 2)
	s04check(t, "no further attempts once dead", dead.RunDue() == 0 && len(down.Deliveries()) == 4)
	_, err := NewWebhookDispatcher(signer, down, dc, []int64{-1})
	s04checkErr(t, "negative retry delay rejected", err, nil)
	_, err = NewScriptedEndpoint("SX")
	s04checkErr(t, "endpoint script must be S/F", err, nil)

	// End to end: the gateway emits events, the dispatcher delivers them, the consumer processes each once
	gc := NewFakeClock(s04T0)
	store := must1(NewIdempotencyStore(gc, 60_000))
	client := NewRetryingClient(must1(NewScriptedProcessor("ADU")), must1(NewBackoffPolicy(100, 2_000, 1)), must1(NewRetryBudget(10, 10)), must1(NewCircuitBreaker(gc, 10, 30_000, 1)), NewRng(42), gc)
	gateway := NewGateway(store, client, gc)
	merchant := NewWebhookConsumer(s04verifier("whsec_live", 5*60_000, gc))
	live := s04dispatcher(s04signer("whsec_live"), NewConsumerEndpoint(merchant), gc, []int64{60_000})
	gateway.AttachWebhooks(live)
	charge := func(key, amount, order string) {
		gateway.Charge(must1(NewChargeRequest("m_1", key, must(Parse(amount, USD)), "tok_visa", order)))
	}
	charge("k1", "19.99", "order-1")
	charge("k1", "19.99", "order-1")
	charge("k2", "5.00", "order-2")
	charge("k3", "7.00", "order-3")
	pending = live.Pending()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending events, got %d", len(pending))
	}
	s04check(t, "succeeded and failed emit, replay and unavailable do not", pending[0].Event.Type == "payment.succeeded" && pending[0].Event.PaymentID == "pay_1" &&
		pending[1].Event.Type == "payment.failed" && pending[1].Event.PaymentID == "pay_2")
	s04check(t, "event ids are unique", pending[0].Event.ID != pending[1].Event.ID)
	s04check(t, "event data carries the amount", pending[0].Event.Data == "amount=19.99 USD")
	live.RunDue()
	s04check(t, "both events delivered and processed once", len(live.Delivered()) == 2 && len(merchant.Processed()) == 2)
	delivered := merchant.Processed()[0]
	s04check(t, "redelivering a processed event is a 2xx DUPLICATE", merchant.Receive(delivered.ToBody(), s04signer("whsec_live").Header(gc.NowMillis(), delivered.ToBody())).Status == Duplicate &&
		len(merchant.Processed()) == 2)
}
