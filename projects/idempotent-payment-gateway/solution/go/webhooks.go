// Project: projects/idempotent-payment-gateway — Stage 04: webhook dispatch (Go).
// Events are signed with HMAC-SHA256 over "<timestamp>.<body>" (RFC 2104) and carried in a header
// "t=<millis>,v1=<hex>". Delivery is at-least-once: a failed attempt is retried on a fixed schedule from the
// injected clock, then dead-lettered. The consumer verifies the signature inside a tolerance window and
// dedupes by event id, which is what turns at-least-once delivery into exactly-once processing.
// Go 1.22+, standard library only (crypto/hmac, crypto/sha256). Same design as the Java and Python solutions.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// WebhookEvent is one lifecycle event. The body is a fixed-order JSON object so that the same event always produces the same bytes.
type WebhookEvent struct {
	ID        string
	Type      string
	PaymentID string
	Data      string
	CreatedAt int64
}

// ErrMalformedBody is returned by ParseWebhookEvent for anything that is not the fixed format ToBody produces.
var ErrMalformedBody = errors.New("malformed webhook body")

func NewWebhookEvent(id, eventType, paymentID, data string, createdAt int64) (WebhookEvent, error) {
	for _, s := range []string{id, eventType, paymentID, data} {
		if strings.ContainsAny(s, "\"\\") {
			return WebhookEvent{}, errors.New("fields must not contain quotes or backslashes")
		}
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(eventType) == "" {
		return WebhookEvent{}, errors.New("id and type are required")
	}
	return WebhookEvent{id, eventType, paymentID, data, createdAt}, nil
}

func (e WebhookEvent) ToBody() string {
	return fmt.Sprintf(`{"id":"%s","type":"%s","payment_id":"%s","data":"%s","created_at":%d}`, e.ID, e.Type, e.PaymentID, e.Data, e.CreatedAt)
}

// ParseWebhookEvent is the inverse of ToBody() for the fixed format above; anything else is ErrMalformedBody.
func ParseWebhookEvent(body string) (WebhookEvent, error) {
	id, err := field(body, "id")
	if err != nil {
		return WebhookEvent{}, err
	}
	eventType, err := field(body, "type")
	if err != nil {
		return WebhookEvent{}, err
	}
	paymentID, err := field(body, "payment_id")
	if err != nil {
		return WebhookEvent{}, err
	}
	data, err := field(body, "data")
	if err != nil {
		return WebhookEvent{}, err
	}
	marker := `"created_at":`
	at := strings.Index(body, marker)
	end := strings.LastIndex(body, "}")
	if at < 0 || end < at+len(marker) {
		return WebhookEvent{}, fmt.Errorf("%w: missing created_at", ErrMalformedBody)
	}
	createdAt, err := strconv.ParseInt(body[at+len(marker):end], 10, 64)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: %v", ErrMalformedBody, err)
	}
	event, err := NewWebhookEvent(id, eventType, paymentID, data, createdAt)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: %v", ErrMalformedBody, err)
	}
	return event, nil
}

func field(body, key string) (string, error) {
	marker := `"` + key + `":"`
	start := strings.Index(body, marker)
	if start < 0 {
		return "", fmt.Errorf("%w: missing %s", ErrMalformedBody, key)
	}
	start += len(marker)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return "", fmt.Errorf("%w: unterminated %s", ErrMalformedBody, key)
	}
	return body[start : start+end], nil
}

// WebhookSigner produces the signature header the producer sends: "t=<millis>,v1=<hmac-sha256 hex>".
type WebhookSigner struct {
	secret []byte
}

func NewWebhookSigner(secret string) (*WebhookSigner, error) {
	if secret == "" {
		return nil, errors.New("secret is required")
	}
	return &WebhookSigner{[]byte(secret)}, nil
}

func HmacSha256Hex(secret []byte, message string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// Signature signs "<timestamp>.<body>": binding the timestamp defeats replay of an old signature.
func (s *WebhookSigner) Signature(timestampMillis int64, body string) string {
	return HmacSha256Hex(s.secret, fmt.Sprintf("%d.%s", timestampMillis, body))
}

func (s *WebhookSigner) Header(timestampMillis int64, body string) string {
	return fmt.Sprintf("t=%d,v1=%s", timestampMillis, s.Signature(timestampMillis, body))
}

type VerifyResult int

const (
	VerifyOK VerifyResult = iota
	MalformedHeader
	StaleTimestamp
	BadSignature
)

func (v VerifyResult) String() string {
	return [...]string{"OK", "MALFORMED_HEADER", "STALE_TIMESTAMP", "BAD_SIGNATURE"}[v]
}

// WebhookVerifier is the consumer side of the signature: same secret, same string, constant-time compare, bounded clock skew.
type WebhookVerifier struct {
	secret          []byte
	toleranceMillis int64
	clock           Clock
}

func NewWebhookVerifier(secret string, toleranceMillis int64, clock Clock) (*WebhookVerifier, error) {
	if secret == "" {
		return nil, errors.New("secret is required")
	}
	if toleranceMillis < 0 {
		return nil, errors.New("tolerance must be non-negative")
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	return &WebhookVerifier{[]byte(secret), toleranceMillis, clock}, nil
}

func (v *WebhookVerifier) Verify(body, header string) VerifyResult {
	parts := strings.Split(header, ",")
	if header == "" || len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
		return MalformedHeader
	}
	timestamp, err := strconv.ParseInt(parts[0][2:], 10, 64)
	if err != nil {
		return MalformedHeader
	}
	provided := parts[1][3:]
	if abs64(v.clock.NowMillis()-timestamp) > v.toleranceMillis {
		return StaleTimestamp
	}
	expected := HmacSha256Hex(v.secret, fmt.Sprintf("%d.%s", timestamp, body))
	if hmac.Equal([]byte(expected), []byte(provided)) {
		return VerifyOK
	}
	return BadSignature
}

type ReceiveStatus int

const (
	Accepted ReceiveStatus = iota
	Duplicate
	Rejected
)

func (s ReceiveStatus) String() string { return [...]string{"ACCEPTED", "DUPLICATE", "REJECTED"}[s] }

// ReceiveResult: Event is nil when the delivery was rejected.
type ReceiveResult struct {
	Status       ReceiveStatus
	Verification VerifyResult
	Event        *WebhookEvent
}

// WebhookConsumer is a merchant's webhook handler: verify, dedupe by event id, then process exactly once.
type WebhookConsumer struct {
	verifier  *WebhookVerifier
	seenIDs   map[string]bool
	processed []WebhookEvent
}

func NewWebhookConsumer(verifier *WebhookVerifier) *WebhookConsumer {
	if verifier == nil {
		panic("verifier is required")
	}
	return &WebhookConsumer{verifier: verifier, seenIDs: map[string]bool{}}
}

func (c *WebhookConsumer) Receive(body, header string) ReceiveResult {
	verification := c.verifier.Verify(body, header)
	if verification != VerifyOK {
		return ReceiveResult{Rejected, verification, nil}
	}
	event, err := ParseWebhookEvent(body)
	if err != nil {
		return ReceiveResult{Rejected, VerifyOK, nil}
	}
	if c.seenIDs[event.ID] {
		return ReceiveResult{Duplicate, verification, &event}
	}
	c.seenIDs[event.ID] = true
	c.processed = append(c.processed, event)
	return ReceiveResult{Accepted, verification, &event}
}

func (c *WebhookConsumer) Processed() []WebhookEvent { return slices.Clone(c.processed) }

// WebhookEndpoint is where the dispatcher posts to. Deliver returns true for a 2xx.
type WebhookEndpoint interface {
	Deliver(body, header string) bool
}

// Delivery is one call a ScriptedEndpoint saw.
type Delivery struct {
	Body   string
	Header string
}

// ScriptedEndpoint: 'S' succeed, 'F' fail; the last character repeats. Records every delivery it saw.
type ScriptedEndpoint struct {
	script     string
	deliveries []Delivery
}

func NewScriptedEndpoint(script string) (*ScriptedEndpoint, error) {
	if script == "" || strings.Trim(script, "SF") != "" {
		return nil, fmt.Errorf("script must be a non-empty string of S and F: %q", script)
	}
	return &ScriptedEndpoint{script: script}, nil
}

func (e *ScriptedEndpoint) Deliver(body, header string) bool {
	step := e.script[min(len(e.deliveries), len(e.script)-1)]
	e.deliveries = append(e.deliveries, Delivery{body, header})
	return step == 'S'
}

func (e *ScriptedEndpoint) Deliveries() []Delivery { return slices.Clone(e.deliveries) }

// ConsumerEndpoint adapts a WebhookConsumer as an endpoint: Accepted and Duplicate are both 2xx, Rejected is not.
type ConsumerEndpoint struct {
	consumer *WebhookConsumer
}

func NewConsumerEndpoint(consumer *WebhookConsumer) *ConsumerEndpoint {
	if consumer == nil {
		panic("consumer is required")
	}
	return &ConsumerEndpoint{consumer}
}

func (e *ConsumerEndpoint) Deliver(body, header string) bool {
	return e.consumer.Receive(body, header).Status != Rejected
}

// DeliveryJob is a queued delivery: the event, how many attempts have been made, and when the next one is due.
type DeliveryJob struct {
	Event         WebhookEvent
	Attempts      int
	NextAttemptAt int64
}

// WebhookDispatcher is at-least-once delivery. Enqueue() schedules the first attempt now; RunDue() performs every
// attempt whose time has come. A failure schedules the next attempt retryDelays[attempts-1] later; when the
// schedule is used up the job goes to the dead-letter list for a human.
type WebhookDispatcher struct {
	signer      *WebhookSigner
	endpoint    WebhookEndpoint
	clock       Clock
	retryDelays []int64
	pending     []DeliveryJob
	delivered   []WebhookEvent
	deadLetters []DeliveryJob
}

func NewWebhookDispatcher(signer *WebhookSigner, endpoint WebhookEndpoint, clock Clock, retryDelaysMillis []int64) (*WebhookDispatcher, error) {
	if signer == nil || endpoint == nil || clock == nil {
		return nil, errors.New("dispatcher needs a signer, an endpoint and a clock")
	}
	for _, d := range retryDelaysMillis {
		if d < 0 {
			return nil, errors.New("retry delays must be non-negative")
		}
	}
	return &WebhookDispatcher{signer: signer, endpoint: endpoint, clock: clock, retryDelays: slices.Clone(retryDelaysMillis)}, nil
}

// MaxAttempts is the total attempts a job may receive: the first one plus one per retry delay.
func (d *WebhookDispatcher) MaxAttempts() int { return len(d.retryDelays) + 1 }

func (d *WebhookDispatcher) Enqueue(event WebhookEvent) {
	d.pending = append(d.pending, DeliveryJob{event, 0, d.clock.NowMillis()})
}

// RunDue attempts every due job once and returns the number of attempts made. Jobs that are not yet due keep
// their order; retries are appended after them, as in the Java version.
func (d *WebhookDispatcher) RunDue() int {
	now := d.clock.NowMillis()
	attemptsMade := 0
	var remaining, retries []DeliveryJob
	for _, job := range d.pending {
		if job.NextAttemptAt > now {
			remaining = append(remaining, job)
			continue
		}
		attemptsMade++
		body := job.Event.ToBody()
		ok := d.endpoint.Deliver(body, d.signer.Header(now, body))
		attempts := job.Attempts + 1
		switch {
		case ok:
			d.delivered = append(d.delivered, job.Event)
		case attempts >= d.MaxAttempts():
			d.deadLetters = append(d.deadLetters, DeliveryJob{job.Event, attempts, now})
		default:
			retries = append(retries, DeliveryJob{job.Event, attempts, now + d.retryDelays[attempts-1]})
		}
	}
	d.pending = append(remaining, retries...)
	return attemptsMade
}

func (d *WebhookDispatcher) Pending() []DeliveryJob     { return slices.Clone(d.pending) }
func (d *WebhookDispatcher) Delivered() []WebhookEvent  { return slices.Clone(d.delivered) }
func (d *WebhookDispatcher) DeadLetters() []DeliveryJob { return slices.Clone(d.deadLetters) }
