// Lesson: phases/12-payment-systems-design/05-webhooks-and-callbacks/docs/en.md
// Webhooks: Signing, Delivery, Ordering, and Replay
// Run:  go run .
// Test: go test ./...
// Sources: RFC 2104 (HMAC); FIPS 180-4 (SHA-256); Stripe "Check the webhook signatures" docs for the
// t=...,v1=... header shape. Go 1.22+, standard library only (crypto/hmac, crypto/sha256, crypto/subtle).
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidArgument is the Go spelling of Java's IllegalArgumentException.
var ErrInvalidArgument = errors.New("invalid argument")

// ---------------------------------------------------------------------------------------------
// Time is injected. The demo and the tests drive a FakeClock in whole seconds; nothing sleeps.
// ---------------------------------------------------------------------------------------------
type Clock interface{ NowSeconds() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(startSeconds int64) *FakeClock { return &FakeClock{now: startSeconds} }
func (c *FakeClock) NowSeconds() int64           { return c.now }

func (c *FakeClock) Advance(seconds int64) error {
	if seconds < 0 {
		return fmt.Errorf("%w: clocks do not run backwards in this lesson", ErrInvalidArgument)
	}
	c.now += seconds
	return nil
}

// ---------------------------------------------------------------------------------------------
// Step 1: the event. The id is the dedupe key; the sequence is the per-object ordering key.
// ---------------------------------------------------------------------------------------------
type Event struct {
	ID        string
	Type      string
	ObjectID  string
	Sequence  int64
	CreatedAt int64
	Status    string
}

// ToJSON is a fixed-shape JSON document. Keys are always emitted in this order so the bytes are deterministic.
func (e Event) ToJSON() string {
	return fmt.Sprintf(`{"id":"%s","type":"%s","object":"%s","sequence":%d,"created":%d,"status":"%s"}`,
		e.ID, e.Type, e.ObjectID, e.Sequence, e.CreatedAt, e.Status)
}

// EventFromJSON parses exactly the shape ToJSON emits. Not a general JSON parser; the lesson is not about parsing.
func EventFromJSON(json string) (Event, error) {
	var e Event
	var err error
	if e.ID, err = field(json, "id"); err != nil {
		return Event{}, err
	}
	if e.Type, err = field(json, "type"); err != nil {
		return Event{}, err
	}
	if e.ObjectID, err = field(json, "object"); err != nil {
		return Event{}, err
	}
	seq, err := field(json, "sequence")
	if err != nil {
		return Event{}, err
	}
	if e.Sequence, err = strconv.ParseInt(seq, 10, 64); err != nil {
		return Event{}, fmt.Errorf("%w: bad sequence %q", ErrInvalidArgument, seq)
	}
	created, err := field(json, "created")
	if err != nil {
		return Event{}, err
	}
	if e.CreatedAt, err = strconv.ParseInt(created, 10, 64); err != nil {
		return Event{}, fmt.Errorf("%w: bad created %q", ErrInvalidArgument, created)
	}
	if e.Status, err = field(json, "status"); err != nil {
		return Event{}, err
	}
	return e, nil
}

func field(json, key string) (string, error) {
	needle := `"` + key + `":`
	at := strings.Index(json, needle)
	if at < 0 {
		return "", fmt.Errorf("%w: missing field %s", ErrInvalidArgument, key)
	}
	start := at + len(needle)
	if start < len(json) && json[start] == '"' {
		end := strings.IndexByte(json[start+1:], '"')
		if end < 0 {
			return "", fmt.Errorf("%w: unterminated field %s", ErrInvalidArgument, key)
		}
		return json[start+1 : start+1+end], nil
	}
	end := start
	for end < len(json) && (json[end] >= '0' && json[end] <= '9' || json[end] == '-') {
		end++
	}
	return json[start:end], nil
}

// ---------------------------------------------------------------------------------------------
// Step 2: signing. HMAC-SHA256 over "<timestamp>.<payload>" with a shared secret (RFC 2104).
// The header shape follows the widely copied Stripe scheme: t=<unix seconds>,v1=<hex mac>.
// ---------------------------------------------------------------------------------------------
const HEADER_NAME = "Webhook-Signature"

type WebhookSigner struct {
	secret []byte
	clock  Clock
}

func NewWebhookSigner(secret string, clock Clock) (*WebhookSigner, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("%w: secret must be at least 16 characters", ErrInvalidArgument)
	}
	return &WebhookSigner{secret: []byte(secret), clock: clock}, nil
}

// SignedPayload is the bytes that are authenticated. Binding the timestamp into the MAC is what makes replay detectable.
func SignedPayload(timestamp int64, payload string) string {
	return strconv.FormatInt(timestamp, 10) + "." + payload
}

func HmacSha256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

func Hex(bytes []byte) string { return hex.EncodeToString(bytes) }

func (s *WebhookSigner) Signature(timestamp int64, payload string) string {
	return Hex(HmacSha256(s.secret, []byte(SignedPayload(timestamp, payload))))
}

// Header is the header value for a payload signed now.
func (s *WebhookSigner) Header(payload string) string {
	ts := s.clock.NowSeconds()
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + s.Signature(ts, payload)
}

// ---------------------------------------------------------------------------------------------
// Step 3: verification. Parse the header, check freshness, recompute, compare in constant time.
// ---------------------------------------------------------------------------------------------
type Verification int

const (
	OK Verification = iota
	MALFORMED_HEADER
	STALE_TIMESTAMP
	BAD_SIGNATURE
)

func (v Verification) String() string {
	return [...]string{"OK", "MALFORMED_HEADER", "STALE_TIMESTAMP", "BAD_SIGNATURE"}[v]
}

const DEFAULT_TOLERANCE_SECONDS int64 = 300

type WebhookVerifier struct {
	secret           []byte
	clock            Clock
	toleranceSeconds int64
}

func NewWebhookVerifier(secret string, clock Clock, toleranceSeconds int64) *WebhookVerifier {
	return &WebhookVerifier{secret: []byte(secret), clock: clock, toleranceSeconds: toleranceSeconds}
}

func NewDefaultWebhookVerifier(secret string, clock Clock) *WebhookVerifier {
	return NewWebhookVerifier(secret, clock, DEFAULT_TOLERANCE_SECONDS)
}

type ParsedHeader struct {
	Timestamp  int64
	Signatures []string
}

var hexSig = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Parse reads "t=123,v1=abc,v1=def" into timestamp and candidate signatures. ok is false when malformed.
func Parse(header string) (ParsedHeader, bool) {
	if header == "" {
		return ParsedHeader{}, false
	}
	var ts *int64
	var sigs []string
	// Java's split drops trailing empty strings; strings.Split keeps them, so trim them first.
	parts := strings.Split(header, ",")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for _, part := range parts {
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			return ParsedHeader{}, false
		}
		k, v := strings.TrimSpace(part[:eq]), strings.TrimSpace(part[eq+1:])
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ParsedHeader{}, false
			}
			ts = &n
		case "v1":
			if !hexSig.MatchString(v) {
				return ParsedHeader{}, false
			}
			sigs = append(sigs, v)
		}
		// unknown schemes (v0, v2) are ignored so the producer can rotate schemes without breaking consumers
	}
	if ts == nil || len(sigs) == 0 {
		return ParsedHeader{}, false
	}
	return ParsedHeader{*ts, sigs}, true
}

// ConstantTimeEquals is a byte-wise comparison whose running time does not depend on where the first difference is.
func ConstantTimeEquals(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

func (v *WebhookVerifier) Verify(payload, header string) Verification {
	parsed, ok := Parse(header)
	if !ok {
		return MALFORMED_HEADER
	}
	age := v.clock.NowSeconds() - parsed.Timestamp
	if age > v.toleranceSeconds || -age > v.toleranceSeconds {
		return STALE_TIMESTAMP
	}
	expected := HmacSha256(v.secret, []byte(SignedPayload(parsed.Timestamp, payload)))
	for _, candidate := range parsed.Signatures {
		if ConstantTimeEquals(expected, Unhex(candidate)) {
			return OK
		}
	}
	return BAD_SIGNATURE
}

// Unhex decodes a lowercase hex string already validated by Parse; invalid input decodes to nil.
func Unhex(h string) []byte {
	out, err := hex.DecodeString(h)
	if err != nil {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Step 4: delivery. At-least-once with a backoff schedule and a dead letter after the last attempt.
// ---------------------------------------------------------------------------------------------

// Endpoint returns an HTTP-style status code. Anything outside 200-299 is a failure to be retried.
type Endpoint interface {
	Receive(payload string, headers map[string]string) int
}

// FakeEndpoint has its failures scripted per attempt, so a retry schedule can be tested exactly.
type FakeEndpoint struct {
	Received        []string
	ReceivedHeaders []map[string]string
	scriptedStatus  []int
	Calls           int
}

func NewFakeEndpoint() *FakeEndpoint { return &FakeEndpoint{} }

// FailWith: the next calls return these statuses in order; after the script is exhausted every call returns 200.
func (f *FakeEndpoint) FailWith(statuses ...int) *FakeEndpoint {
	f.scriptedStatus = append(f.scriptedStatus, statuses...)
	return f
}

func (f *FakeEndpoint) Receive(payload string, headers map[string]string) int {
	f.Calls++
	status := 200
	if len(f.scriptedStatus) > 0 {
		status, f.scriptedStatus = f.scriptedStatus[0], f.scriptedStatus[1:]
	}
	if status >= 200 && status < 300 {
		f.Received = append(f.Received, payload)
		f.ReceivedHeaders = append(f.ReceivedHeaders, headers)
	}
	return status
}

type DeliveryState int

const (
	PENDING DeliveryState = iota
	DELIVERED
	DEAD_LETTERED
)

func (s DeliveryState) String() string {
	return [...]string{"PENDING", "DELIVERED", "DEAD_LETTERED"}[s]
}

type Delivery struct {
	Event         Event
	Payload       string
	State         DeliveryState
	Attempts      int
	NextAttemptAt int64
	Statuses      []int
	AttemptTimes  []int64
}

func NewDelivery(event Event, firstAttemptAt int64) *Delivery {
	return &Delivery{Event: event, Payload: event.ToJSON(), State: PENDING, NextAttemptAt: firstAttemptAt}
}

// DefaultBackoffSeconds: seconds to wait after attempt n fails before attempt n+1. Length + 1 = max attempts.
var DefaultBackoffSeconds = []int64{30, 120, 600, 3600, 21600}

type WebhookDispatcher struct {
	clock       Clock
	signer      *WebhookSigner
	endpoint    Endpoint
	backoff     []int64
	Deliveries  []*Delivery
	DeadLetters []*Delivery
}

func NewWebhookDispatcher(clock Clock, signer *WebhookSigner, endpoint Endpoint, backoffSeconds []int64) *WebhookDispatcher {
	return &WebhookDispatcher{clock: clock, signer: signer, endpoint: endpoint, backoff: append([]int64(nil), backoffSeconds...)}
}

func NewDefaultWebhookDispatcher(clock Clock, signer *WebhookSigner, endpoint Endpoint) *WebhookDispatcher {
	return NewWebhookDispatcher(clock, signer, endpoint, DefaultBackoffSeconds)
}

func (w *WebhookDispatcher) MaxAttempts() int { return len(w.backoff) + 1 }

func (w *WebhookDispatcher) Enqueue(event Event) *Delivery {
	d := NewDelivery(event, w.clock.NowSeconds())
	w.Deliveries = append(w.Deliveries, d)
	return d
}

// Tick attempts every delivery that is due. Signs at send time so the timestamp reflects the retry, not the enqueue.
func (w *WebhookDispatcher) Tick() int {
	attempted := 0
	for _, d := range w.Deliveries {
		if d.State != PENDING || d.NextAttemptAt > w.clock.NowSeconds() {
			continue
		}
		attempted++
		headers := map[string]string{
			HEADER_NAME:       w.signer.Header(d.Payload),
			"Webhook-Id":      d.Event.ID,
			"Webhook-Attempt": strconv.Itoa(d.Attempts + 1),
		}
		status := w.endpoint.Receive(d.Payload, headers)
		d.Attempts++
		d.Statuses = append(d.Statuses, status)
		d.AttemptTimes = append(d.AttemptTimes, w.clock.NowSeconds())
		if status >= 200 && status < 300 {
			d.State = DELIVERED
		} else if d.Attempts >= w.MaxAttempts() {
			d.State = DEAD_LETTERED
			w.DeadLetters = append(w.DeadLetters, d)
		} else {
			d.NextAttemptAt = w.clock.NowSeconds() + w.backoff[d.Attempts-1]
		}
	}
	return attempted
}

// Drain drives the clock forward to each due time until nothing is pending. Returns the number of ticks that did work.
func (w *WebhookDispatcher) Drain(fake *FakeClock) int {
	ticks := 0
	for {
		next := int64(math.MaxInt64)
		for _, d := range w.Deliveries {
			if d.State == PENDING && d.NextAttemptAt < next {
				next = d.NextAttemptAt
			}
		}
		if next == math.MaxInt64 {
			return ticks
		}
		if next > fake.NowSeconds() {
			_ = fake.Advance(next - fake.NowSeconds()) // never negative here
		}
		w.Tick()
		ticks++
	}
}

// ---------------------------------------------------------------------------------------------
// Step 5: the consumer. Verify, dedupe by event id, and never let an older event overwrite a newer state.
// ---------------------------------------------------------------------------------------------
type Outcome int

const (
	ACCEPTED Outcome = iota
	DUPLICATE
	OUT_OF_ORDER
	REJECTED
)

func (o Outcome) String() string {
	return [...]string{"ACCEPTED", "DUPLICATE", "OUT_OF_ORDER", "REJECTED"}[o]
}

// Handled is the HTTP status the endpoint should answer with, alongside what happened.
type Handled struct {
	HTTPStatus   int
	Outcome      Outcome
	Verification Verification
}

type WebhookConsumer struct {
	verifier       *WebhookVerifier
	seenEventIDs   map[string]bool
	LatestSequence map[string]int64
	ObjectStatus   map[string]string
	Log            []string
}

func NewWebhookConsumer(verifier *WebhookVerifier) *WebhookConsumer {
	return &WebhookConsumer{verifier: verifier, seenEventIDs: map[string]bool{}, LatestSequence: map[string]int64{}, ObjectStatus: map[string]string{}}
}

func (c *WebhookConsumer) Handle(payload string, headers map[string]string) Handled {
	v := c.verifier.Verify(payload, headers[HEADER_NAME]) // a missing header reads as "", which Parse rejects
	if v != OK {
		c.Log = append(c.Log, "rejected "+v.String())
		return Handled{400, REJECTED, v} // 4xx: the producer should not retry a forgery
	}
	e, err := EventFromJSON(payload)
	if err != nil {
		c.Log = append(c.Log, "rejected malformed event: "+err.Error())
		return Handled{400, REJECTED, v}
	}
	if c.seenEventIDs[e.ID] {
		c.Log = append(c.Log, "duplicate "+e.ID)
		return Handled{200, DUPLICATE, v} // 200: tell the producer to stop retrying
	}
	c.seenEventIDs[e.ID] = true
	latest, ok := c.LatestSequence[e.ObjectID]
	if !ok {
		latest = -1
	}
	if e.Sequence <= latest {
		c.Log = append(c.Log, fmt.Sprintf("out-of-order %s seq %d <= %d", e.ID, e.Sequence, latest))
		return Handled{200, OUT_OF_ORDER, v}
	}
	c.LatestSequence[e.ObjectID] = e.Sequence
	c.ObjectStatus[e.ObjectID] = e.Status
	c.Log = append(c.Log, fmt.Sprintf("applied %s -> %s=%s", e.ID, e.ObjectID, e.Status))
	return Handled{200, ACCEPTED, v}
}

// ConsumerEndpoint is an adapter so a WebhookConsumer can sit behind the dispatcher as an Endpoint.
type ConsumerEndpoint struct{ Consumer *WebhookConsumer }

func (c ConsumerEndpoint) Receive(payload string, headers map[string]string) int {
	return c.Consumer.Handle(payload, headers).HTTPStatus
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------
const SECRET = "whsec_demo_secret_do_not_reuse_0001"

func joinInts[T int | int64](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	clock := NewFakeClock(1_700_000_000)
	signer, err := NewWebhookSigner(SECRET, clock)
	if err != nil {
		panic(err)
	}
	verifier := NewDefaultWebhookVerifier(SECRET, clock)

	fmt.Println("== Signing and verifying ==")
	authorized := Event{"evt_001", "payment.authorized", "pay_42", 1, clock.NowSeconds(), "authorized"}
	payload := authorized.ToJSON()
	header := signer.Header(payload)
	fmt.Println("payload: " + payload)
	fmt.Println("header:  " + header)
	fmt.Println("verify untouched        ->", verifier.Verify(payload, header))
	fmt.Println("verify tampered payload ->", verifier.Verify(strings.Replace(payload, `authorized"}`, `captured"}`, 1), header))
	fmt.Println("verify wrong secret     ->", NewDefaultWebhookVerifier("whsec_some_other_secret_9999", clock).Verify(payload, header))
	_ = clock.Advance(301)
	fmt.Printf("verify 301s later       -> %v (tolerance 300s: replay window closed)\n", verifier.Verify(payload, header))

	fmt.Println("\n== At-least-once delivery with backoff ==")
	flaky := NewFakeEndpoint().FailWith(503, 500)
	dispatcher := NewDefaultWebhookDispatcher(clock, signer, flaky)
	t0 := clock.NowSeconds()
	d := dispatcher.Enqueue(authorized)
	dispatcher.Drain(clock)
	fmt.Printf("attempts: %d, statuses: %s, state: %v\n", d.Attempts, joinInts(d.Statuses), d.State)
	offsets := make([]int64, 0, len(d.AttemptTimes))
	for _, t := range d.AttemptTimes {
		offsets = append(offsets, t-t0)
	}
	fmt.Printf("attempt offsets (s): %s  <- 0, +30, +150\n", joinInts(offsets))

	fmt.Printf("\n== Dead letter after %d attempts ==\n", dispatcher.MaxAttempts())
	dead := NewFakeEndpoint().FailWith(500, 500, 500, 500, 500, 500, 500)
	dispatcher2 := NewDefaultWebhookDispatcher(clock, signer, dead)
	t0 = clock.NowSeconds()
	dl := dispatcher2.Enqueue(Event{"evt_002", "payment.captured", "pay_42", 2, clock.NowSeconds(), "captured"})
	dispatcher2.Drain(clock)
	offsets = offsets[:0]
	for _, t := range dl.AttemptTimes {
		offsets = append(offsets, t-t0)
	}
	fmt.Printf("attempt offsets (s): %s\n", joinInts(offsets))
	fmt.Printf("state: %v, dead letters: %d, total wall time: %.2fh\n", dl.State, len(dispatcher2.DeadLetters),
		float64(clock.NowSeconds()-t0)/3600.0)

	fmt.Println("\n== Consumer: dedupe and ordering ==")
	consumer := NewWebhookConsumer(verifier)
	captured := Event{"evt_003", "payment.captured", "pay_77", 2, clock.NowSeconds(), "captured"}
	authorized77 := Event{"evt_004", "payment.authorized", "pay_77", 1, clock.NowSeconds(), "authorized"}
	for _, e := range []Event{captured, captured, authorized77} {
		h := map[string]string{HEADER_NAME: signer.Header(e.ToJSON())}
		r := consumer.Handle(e.ToJSON(), h)
		fmt.Printf("%-8s seq=%d -> %-12v http %d\n", e.ID, e.Sequence, r.Outcome, r.HTTPStatus)
	}
	fmt.Printf("pay_77 status: %s  (captured, not rolled back to authorized)\n", consumer.ObjectStatus["pay_77"])
	forged := consumer.Handle(captured.ToJSON(), map[string]string{HEADER_NAME: "t=" + strconv.FormatInt(clock.NowSeconds(), 10) + ",v1=" + strings.Repeat("0", 64)})
	fmt.Printf("forged signature -> %v (%v) http %d\n", forged.Outcome, forged.Verification, forged.HTTPStatus)
}
