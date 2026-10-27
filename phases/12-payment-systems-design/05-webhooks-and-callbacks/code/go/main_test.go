// Tests for phases/12-payment-systems-design/05-webhooks-and-callbacks. Run: go test ./...
package main

import (
	"bytes"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const testSecret = "whsec_test_secret_0123456789abcdef"

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

type fixture struct {
	clock    *FakeClock
	signer   *WebhookSigner
	verifier *WebhookVerifier
	ev       Event
	payload  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := NewFakeClock(1_700_000_000)
	signer, err := NewWebhookSigner(testSecret, clock)
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{"evt_1", "payment.captured", "pay_1", 1, clock.NowSeconds(), "captured"}
	return &fixture{clock, signer, NewDefaultWebhookVerifier(testSecret, clock), ev, ev.ToJSON()}
}

func countPrefix(log []string, prefix string) int {
	n := 0
	for _, s := range log {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func TestHmacVector(t *testing.T) {
	f := newFixture(t)
	// RFC 2104 / RFC 4231 test vector: key = 0x0b * 20, data = "Hi There"
	key := bytes.Repeat([]byte{0x0b}, 20)
	check(t, "HMAC-SHA256 matches RFC 4231 test case 1",
		Hex(HmacSha256(key, []byte("Hi There"))) == "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7")
	check(t, "hex is lowercase and 64 chars", regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(f.signer.Signature(1, "x")))
}

func TestSigningAndVerification(t *testing.T) {
	f := newFixture(t)
	header := f.signer.Header(f.payload)
	now := strconv.FormatInt(f.clock.NowSeconds(), 10)
	check(t, "header shape t=...,v1=...", regexp.MustCompile(`^t=\d+,v1=[0-9a-f]{64}$`).MatchString(header))
	check(t, "verify ok", f.verifier.Verify(f.payload, header) == OK)
	check(t, "signature deterministic for same ts+payload", f.signer.Header(f.payload) == header)
	check(t, "different payload, different signature", f.signer.Signature(f.clock.NowSeconds(), f.payload) != f.signer.Signature(f.clock.NowSeconds(), f.payload+" "))
	cases := []struct {
		name    string
		payload string
		header  string
		want    Verification
	}{
		{"tampered payload rejected", strings.Replace(f.payload, "captured", "refunded", 1), header, BAD_SIGNATURE},
		{"timestamp is bound into the MAC", f.payload, strings.Replace(header, "t="+now, "t="+strconv.FormatInt(f.clock.NowSeconds()+1, 10), 1), BAD_SIGNATURE},
		{"malformed header: empty", f.payload, "", MALFORMED_HEADER},
		{"malformed header: no v1", f.payload, "t=123", MALFORMED_HEADER},
		{"malformed header: bad ts", f.payload, "t=abc,v1=" + strings.Repeat("0", 64), MALFORMED_HEADER},
		{"malformed header: short sig", f.payload, "t=1,v1=abcd", MALFORMED_HEADER},
		{"unknown scheme ignored", f.payload, header + ",v0=deadbeef", OK},
		{"rotation: any one v1 matching passes", f.payload, header + ",v1=" + strings.Repeat("f", 64), OK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := f.verifier.Verify(c.payload, c.header); got != c.want {
				t.Errorf("Verify = %v, want %v", got, c.want)
			}
		})
	}
	check(t, "wrong secret rejected", NewDefaultWebhookVerifier("whsec_other_secret_0000000000", f.clock).Verify(f.payload, header) == BAD_SIGNATURE)
	// Java's null header arrives in Go as a missing map key, which reads as "".
	check(t, "malformed header: missing (null)", f.verifier.Verify(f.payload, map[string]string{}[HEADER_NAME]) == MALFORMED_HEADER)
}

func TestToleranceWindow(t *testing.T) {
	f := newFixture(t)
	header := f.signer.Header(f.payload)
	_ = f.clock.Advance(300)
	check(t, "exactly at tolerance still ok", f.verifier.Verify(f.payload, header) == OK)
	_ = f.clock.Advance(1)
	check(t, "one second past tolerance is stale", f.verifier.Verify(f.payload, header) == STALE_TIMESTAMP)
	check(t, "stale beats bad signature (checked first)", f.verifier.Verify(f.payload+"x", header) == STALE_TIMESTAMP)
	futureTs := f.clock.NowSeconds() + 400
	future := "t=" + strconv.FormatInt(futureTs, 10) + ",v1=" + f.signer.Signature(futureTs, f.payload)
	check(t, "timestamp from the future is also stale", f.verifier.Verify(f.payload, future) == STALE_TIMESTAMP)
	check(t, "custom tolerance honoured", NewWebhookVerifier(testSecret, f.clock, 3600).Verify(f.payload, header) == OK)
}

func TestConstantTimeCompareAndHelpers(t *testing.T) {
	f := newFixture(t)
	check(t, "ct equal", ConstantTimeEquals([]byte{1, 2, 3}, []byte{1, 2, 3}))
	check(t, "ct differs at end", !ConstantTimeEquals([]byte{1, 2, 3}, []byte{1, 2, 4}))
	check(t, "ct length mismatch", !ConstantTimeEquals([]byte{1, 2}, []byte{1, 2, 3}))
	check(t, "unhex round trip", Hex(Unhex("00ff7a")) == "00ff7a")
	_, err := NewWebhookSigner("short", f.clock)
	check(t, "short secret rejected", errors.Is(err, ErrInvalidArgument))
}

func TestEventJSONRoundTrip(t *testing.T) {
	f := newFixture(t)
	back, err := EventFromJSON(f.payload)
	check(t, "event json round trip", err == nil && back == f.ev)
	_, err = EventFromJSON(`{"id":"x"}`)
	check(t, "missing field rejected", errors.Is(err, ErrInvalidArgument))
}

func TestDeliveryHappyPath(t *testing.T) {
	f := newFixture(t)
	ok := NewFakeEndpoint()
	dsp := NewDefaultWebhookDispatcher(f.clock, f.signer, ok)
	d := dsp.Enqueue(f.ev)
	check(t, "tick attempts a due delivery", dsp.Tick() == 1)
	check(t, "delivered first try", d.State == DELIVERED && d.Attempts == 1)
	_, hasSig := ok.ReceivedHeaders[0][HEADER_NAME]
	check(t, "endpoint received signed header", hasSig)
	check(t, "attempt header is 1", ok.ReceivedHeaders[0]["Webhook-Attempt"] == "1")
	check(t, "second tick does nothing", dsp.Tick() == 0)
}

func TestBackoffScheduleIsExact(t *testing.T) {
	f := newFixture(t)
	flaky := NewFakeEndpoint().FailWith(503, 500, 429)
	dsp2 := NewWebhookDispatcher(f.clock, f.signer, flaky, []int64{10, 20, 40})
	t0 := f.clock.NowSeconds()
	d2 := dsp2.Enqueue(f.ev)
	dsp2.Tick()
	check(t, "not retried before backoff elapses", d2.NextAttemptAt == t0+10 && dsp2.Tick() == 0)
	_ = f.clock.Advance(9)
	check(t, "still not due at 9s", dsp2.Tick() == 0)
	_ = f.clock.Advance(1)
	check(t, "due at exactly 10s", dsp2.Tick() == 1 && d2.Attempts == 2)
	dsp2.Drain(f.clock)
	check(t, "delivered on fourth attempt", d2.State == DELIVERED && d2.Attempts == 4)
	check(t, "attempt offsets 0,10,30,70", reflect.DeepEqual(d2.AttemptTimes, []int64{t0, t0 + 10, t0 + 30, t0 + 70}))
	check(t, "statuses recorded", reflect.DeepEqual(d2.Statuses, []int{503, 500, 429, 200}))
	check(t, "retry is re-signed with a fresh timestamp", strings.HasPrefix(flaky.ReceivedHeaders[0][HEADER_NAME], "t="+strconv.FormatInt(t0+70, 10)))
	check(t, "at-least-once: payload delivered exactly once here", len(flaky.Received) == 1)
}

func TestDeadLetter(t *testing.T) {
	f := newFixture(t)
	down := NewFakeEndpoint().FailWith(500, 500, 500, 500, 500, 500, 500, 500)
	dsp3 := NewWebhookDispatcher(f.clock, f.signer, down, []int64{1, 2, 4})
	d3 := dsp3.Enqueue(f.ev)
	dsp3.Drain(f.clock)
	check(t, "max attempts = backoff length + 1", dsp3.MaxAttempts() == 4 && d3.Attempts == 4)
	inDead := false
	for _, x := range dsp3.DeadLetters {
		if x == d3 {
			inDead = true
		}
	}
	check(t, "dead lettered", d3.State == DEAD_LETTERED && inDead)
	check(t, "no further attempts after dead letter", dsp3.Tick() == 0 && down.Calls == 4)
	check(t, "default schedule has 6 attempts", NewDefaultWebhookDispatcher(f.clock, f.signer, down).MaxAttempts() == 6)
}

func TestConsumerDedupeOrderingRejection(t *testing.T) {
	f := newFixture(t)
	consumer := NewWebhookConsumer(f.verifier)
	a1 := Event{"evt_a1", "payment.authorized", "pay_9", 1, f.clock.NowSeconds(), "authorized"}
	a2 := Event{"evt_a2", "payment.captured", "pay_9", 2, f.clock.NowSeconds(), "captured"}
	h2 := map[string]string{HEADER_NAME: f.signer.Header(a2.ToJSON())}
	h1 := map[string]string{HEADER_NAME: f.signer.Header(a1.ToJSON())}
	check(t, "first event accepted", consumer.Handle(a2.ToJSON(), h2).Outcome == ACCEPTED)
	check(t, "duplicate id returns DUPLICATE with 200", consumer.Handle(a2.ToJSON(), h2) == Handled{200, DUPLICATE, OK})
	check(t, "older sequence does not roll state back", consumer.Handle(a1.ToJSON(), h1).Outcome == OUT_OF_ORDER && consumer.ObjectStatus["pay_9"] == "captured")
	forged := map[string]string{HEADER_NAME: "t=" + strconv.FormatInt(f.clock.NowSeconds(), 10) + ",v1=" + strings.Repeat("0", 64)}
	check(t, "forged signature rejected with 400", consumer.Handle(a2.ToJSON(), forged).HTTPStatus == 400)
	check(t, "missing header rejected", consumer.Handle(a2.ToJSON(), map[string]string{}).Verification == MALFORMED_HEADER)
	check(t, "rejected events are not marked seen", consumer.LatestSequence["pay_9"] == 2 && countPrefix(consumer.Log, "applied") == 1)
}

// flakyConsumerEndpoint processes the payload but reports a timeout on the first call, as a gateway in front of it might.
type flakyConsumerEndpoint struct {
	consumer *WebhookConsumer
	n        int
}

func (e *flakyConsumerEndpoint) Receive(p string, h map[string]string) int {
	e.n++
	status := e.consumer.Handle(p, h).HTTPStatus
	if e.n == 1 {
		return 504 // consumer processed it, but the producer saw a timeout
	}
	return status
}

func TestEndToEndRedeliveryIsAbsorbed(t *testing.T) {
	f := newFixture(t)
	c2 := NewWebhookConsumer(f.verifier)
	a2 := Event{"evt_a2", "payment.captured", "pay_9", 2, f.clock.NowSeconds(), "captured"}
	dsp4 := NewWebhookDispatcher(f.clock, f.signer, &flakyConsumerEndpoint{consumer: c2}, []int64{5})
	d4 := dsp4.Enqueue(a2)
	dsp4.Drain(f.clock)
	check(t, "timeout after processing causes a redelivery", d4.Attempts == 2 && d4.State == DELIVERED)
	check(t, "consumer saw the duplicate and applied once", countPrefix(c2.Log, "duplicate") == 1 && countPrefix(c2.Log, "applied") == 1)
	check(t, "fake clock refuses to go backwards", errors.Is(f.clock.Advance(-1), ErrInvalidArgument))
}
