// Lesson: phases/12-payment-systems-design/01-idempotent-payment-api/docs/en.md
// Designing an Idempotent Payment API
// Run:  go run .
// Test: go test ./...   (also passes under go test -race ./...)
// Sources: IETF draft-ietf-httpapi-idempotency-key-header; Stripe API reference (Idempotent requests);
//
//	RFC 9110 (HTTP semantics: 409, 422, Retry-After). Go 1.22+, standard library only.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	JPY = Currency{"JPY", 0}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var ErrCurrencyMismatch = errors.New("currency mismatch")
var ErrOverflow = errors.New("int64 overflow")

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

// Parse reads "12.34", "-0.5", "1200" exactly and rejects more decimals than the currency allows.
func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows (%d)", text, currency.Code, currency.Exponent)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	var minor int64
	for _, ch := range whole + frac {
		next := minor*10 + int64(ch-'0')
		if next < minor {
			return Money{}, ErrOverflow
		}
		minor = next
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum := m.MinorUnits + other.MinorUnits
	if (m.MinorUnits > 0 && other.MinorUnits > 0 && sum < 0) || (m.MinorUnits < 0 && other.MinorUnits < 0 && sum >= 0) {
		return Money{}, ErrOverflow
	}
	return Money{sum, m.Currency}, nil
}

func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Production passes a system clock; tests and the demo pass a FakeClock.
// ---------------------------------------------------------------------------------------------

type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now atomic.Int64 }

func NewFakeClock(startMillis int64) *FakeClock {
	c := &FakeClock{}
	c.now.Store(startMillis)
	return c
}

func (c *FakeClock) NowMillis() int64     { return c.now.Load() }
func (c *FakeClock) Advance(millis int64) { c.now.Add(millis) }

// ---------------------------------------------------------------------------------------------
// The request and its fingerprint: SHA-256 over a canonical encoding of the body, so two requests
// that mean the same thing hash the same regardless of field order or whitespace in the JSON.
// ---------------------------------------------------------------------------------------------

type PaymentRequest struct {
	Amount      Money
	Source      string
	Description string
}

func NewPaymentRequest(amount Money, source, description string) (PaymentRequest, error) {
	if !amount.IsPositive() {
		return PaymentRequest{}, fmt.Errorf("amount must be positive: %v", amount)
	}
	return PaymentRequest{amount, source, description}, nil
}

// Canonical renders fields sorted by name, one name=value per line, amount in minor units.
func (r PaymentRequest) Canonical() string {
	fields := map[string]string{
		"amount":      strconv.FormatInt(r.Amount.MinorUnits, 10),
		"currency":    r.Amount.Currency.Code,
		"description": r.Description,
		"source":      r.Source,
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(fields[k])
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (r PaymentRequest) Fingerprint() string { return sha256Hex(r.Canonical()) }

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// PaymentResponse is the HTTP-shaped response: status, body, and the two headers this lesson cares about.
type PaymentResponse struct {
	Status  int
	Body    string
	Headers map[string]string
}

const (
	HeaderReplayed   = "Idempotent-Replayed"
	HeaderRetryAfter = "Retry-After"
)

func respond(status int, body string) PaymentResponse {
	return PaymentResponse{status, body, map[string]string{}}
}

func (r PaymentResponse) WithHeader(name, value string) PaymentResponse {
	headers := make(map[string]string, len(r.Headers)+1)
	for k, v := range r.Headers {
		headers[k] = v
	}
	headers[name] = value
	return PaymentResponse{r.Status, r.Body, headers}
}

func (r PaymentResponse) Replayed() bool { return r.Headers[HeaderReplayed] == "true" }

func (r PaymentResponse) RetryAfterSeconds() int {
	n, _ := strconv.Atoi(r.Headers[HeaderRetryAfter])
	return n
}

// ---------------------------------------------------------------------------------------------
// The processor behind the API. FakeProcessor models approval, decline, a crash after the charge
// landed, and a hook that lets a test hold a charge in flight while a concurrent duplicate arrives.
// ---------------------------------------------------------------------------------------------

type ProcessorResult struct {
	ReferenceID string
	Approved    bool
	Code        string
	Amount      Money
}

type Processor interface {
	Charge(referenceID string, amount Money, source string) (ProcessorResult, error)
	Lookup(referenceID string) (ProcessorResult, bool)
}

// ErrSimulatedCrash stands in for the handler process dying after the processor committed.
var ErrSimulatedCrash = errors.New("process crashed after the processor committed the charge")

type FakeProcessor struct {
	mu                   sync.Mutex
	charges              map[string]ProcessorResult
	chargeCalls          atomic.Int32
	crashAfterNextCharge bool
	beforeCommit         func()
}

func NewFakeProcessor() *FakeProcessor {
	return &FakeProcessor{charges: map[string]ProcessorResult{}, beforeCommit: func() {}}
}

func (p *FakeProcessor) Charge(referenceID string, amount Money, source string) (ProcessorResult, error) {
	p.chargeCalls.Add(1)
	p.mu.Lock()
	hook := p.beforeCommit
	p.mu.Unlock()
	hook()
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.charges[referenceID]; ok {
		return existing, nil // a real processor answers "duplicate reference" with the original outcome
	}
	approved := !strings.HasPrefix(source, "tok_decline")
	code := "approved"
	if !approved {
		code = "card_declined"
	}
	result := ProcessorResult{referenceID, approved, code, amount}
	p.charges[referenceID] = result
	if p.crashAfterNextCharge {
		p.crashAfterNextCharge = false
		return ProcessorResult{}, ErrSimulatedCrash
	}
	return result, nil
}

func (p *FakeProcessor) Lookup(referenceID string) (ProcessorResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.charges[referenceID]
	return r, ok
}

func (p *FakeProcessor) ChargeCalls() int { return int(p.chargeCalls.Load()) }

func (p *FakeProcessor) ChargesStored() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.charges)
}

func (p *FakeProcessor) CrashAfterNextCharge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.crashAfterNextCharge = true
}

func (p *FakeProcessor) OnBeforeCommit(hook func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beforeCommit = hook
}

// ---------------------------------------------------------------------------------------------
// The idempotency store. One record per key, reserved atomically before any side effect.
// ---------------------------------------------------------------------------------------------

type KeyState int

const (
	InProgress KeyState = iota
	Completed
)

type IdempotencyRecord struct {
	Key             string
	Fingerprint     string
	ReferenceID     string
	State           KeyState
	Response        *PaymentResponse
	CreatedAtMillis int64
	UpdatedAtMillis int64
}

// Reservation is what Reserve decided. Exactly one caller per key gets Reserved or Recover.
type Reservation int

const (
	Reserved Reservation = iota
	Recover
	ReservationInProgress
	ReservationCompleted
	FingerprintMismatch
)

type ReserveOutcome struct {
	Decision Reservation
	Record   IdempotencyRecord
}

type IdempotencyStore struct {
	mu          sync.Mutex
	records     map[string]IdempotencyRecord
	clock       Clock
	ttlMillis   int64
	leaseMillis int64
}

func NewIdempotencyStore(clock Clock, ttlMillis, leaseMillis int64) *IdempotencyStore {
	return &IdempotencyStore{records: map[string]IdempotencyRecord{}, clock: clock, ttlMillis: ttlMillis, leaseMillis: leaseMillis}
}

// Reserve is an atomic check-and-reserve: the whole decision happens under one lock so two
// concurrent callers with the same key cannot both see "absent" and both proceed to charge.
func (s *IdempotencyStore) Reserve(key, fingerprint, referenceID string) ReserveOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.NowMillis()
	existing, ok := s.records[key]
	if ok && now-existing.CreatedAtMillis >= s.ttlMillis {
		delete(s.records, key) // expired: the key may be reused with any body
		ok = false
	}
	if !ok {
		fresh := IdempotencyRecord{key, fingerprint, referenceID, InProgress, nil, now, now}
		s.records[key] = fresh
		return ReserveOutcome{Reserved, fresh}
	}
	if existing.Fingerprint != fingerprint {
		return ReserveOutcome{FingerprintMismatch, existing}
	}
	if existing.State == Completed {
		return ReserveOutcome{ReservationCompleted, existing}
	}
	if now-existing.UpdatedAtMillis < s.leaseMillis {
		return ReserveOutcome{ReservationInProgress, existing} // a live owner is still working
	}
	renewed := existing
	renewed.UpdatedAtMillis = now // owner died: this caller takes over
	s.records[key] = renewed
	return ReserveOutcome{Recover, renewed}
}

func (s *IdempotencyStore) Complete(key string, response PaymentResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.records[key]
	if !ok {
		return fmt.Errorf("Complete() without Reserve(): %s", key)
	}
	existing.State = Completed
	existing.Response = &response
	existing.UpdatedAtMillis = s.clock.NowMillis()
	s.records[key] = existing
	return nil
}

func (s *IdempotencyStore) Get(key string) (IdempotencyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[key]
	return r, ok
}

func (s *IdempotencyStore) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// ---------------------------------------------------------------------------------------------
// The handler: POST /payments and GET /payments/{id} modelled in-process.
// ---------------------------------------------------------------------------------------------

const (
	KeyTTLMillis      = int64(24 * 60 * 60 * 1000) // 24h
	LeaseMillis       = int64(30_000)              // how long an IN_PROGRESS owner is trusted
	RetryAfterSeconds = 1
	KeySeparator      = "\u001f" // a control character the key grammar forbids
)

// ScopedKey joins the merchant (the authenticated caller) and the key: two merchants may both send
// "order-1001" and must never see each other's responses.
func ScopedKey(merchantID, idempotencyKey string) string {
	return merchantID + KeySeparator + idempotencyKey
}

// ReferenceIDFor is a pure function of the scoped key, so a retry can ask the processor about it.
func ReferenceIDFor(scoped string) string { return "ref_" + sha256Hex("ref:" + scoped)[:16] }

type PaymentsHandler struct {
	store     *IdempotencyStore
	processor Processor
}

func NewPaymentsHandler(store *IdempotencyStore, processor Processor) *PaymentsHandler {
	return &PaymentsHandler{store, processor}
}

// Post is POST /payments: the only idempotent operation. A returned error means the process died
// mid-flight (the simulated crash); every other outcome is an HTTP-shaped response.
func (h *PaymentsHandler) Post(merchantID, idempotencyKey string, request PaymentRequest) (PaymentResponse, error) {
	if strings.TrimSpace(merchantID) == "" {
		return respond(401, `{"error":"unauthenticated"}`), nil
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return respond(400, `{"error":"idempotency_key_required"}`), nil
	}
	if len(idempotencyKey) > 255 || strings.IndexFunc(idempotencyKey, unicode.IsControl) >= 0 {
		return respond(400, `{"error":"idempotency_key_invalid"}`), nil
	}
	key := ScopedKey(merchantID, idempotencyKey)
	fingerprint := request.Fingerprint()
	referenceID := ReferenceIDFor(key)
	outcome := h.store.Reserve(key, fingerprint, referenceID)
	switch outcome.Decision {
	case FingerprintMismatch:
		return respond(422, `{"error":"idempotency_key_reused_with_different_request"}`), nil
	case ReservationInProgress:
		return respond(409, `{"error":"request_in_progress"}`).WithHeader(HeaderRetryAfter, strconv.Itoa(RetryAfterSeconds)), nil
	case ReservationCompleted:
		return outcome.Record.Response.WithHeader(HeaderReplayed, "true"), nil
	case Recover:
		return h.recover(key, request, referenceID)
	default:
		return h.execute(key, request, referenceID)
	}
}

func (h *PaymentsHandler) execute(key string, request PaymentRequest, referenceID string) (PaymentResponse, error) {
	result, err := h.processor.Charge(referenceID, request.Amount, request.Source)
	if err != nil {
		return PaymentResponse{}, err // the process "died" here: the record stays IN_PROGRESS
	}
	response := render(result)
	if err := h.store.Complete(key, response); err != nil {
		return PaymentResponse{}, err
	}
	return response, nil
}

// recover runs when the previous owner died between Charge and Complete: ask before charging again.
func (h *PaymentsHandler) recover(key string, request PaymentRequest, referenceID string) (PaymentResponse, error) {
	result, found := h.processor.Lookup(referenceID)
	if !found {
		var err error
		if result, err = h.processor.Charge(referenceID, request.Amount, request.Source); err != nil {
			return PaymentResponse{}, err
		}
	}
	response := render(result)
	if err := h.store.Complete(key, response); err != nil {
		return PaymentResponse{}, err
	}
	return response, nil
}

// Get is GET /payments/{id}: a read. No key, no store record, always the *current* state.
// Replaying a stored response here would hide a capture, refund, or dispute that happened since.
func (h *PaymentsHandler) Get(paymentID string) PaymentResponse {
	current, ok := h.processor.Lookup(paymentID)
	if !ok {
		return respond(404, `{"error":"not_found"}`)
	}
	return respond(200, render(current).Body)
}

func render(result ProcessorResult) PaymentResponse {
	status, http := "failed", 402
	if result.Approved {
		status, http = "succeeded", 201
	}
	body := fmt.Sprintf(`{"id":"%s","status":"%s","code":"%s","amount":"%v"}`, result.ReferenceID, status, result.Code, result.Amount)
	return respond(http, body)
}

func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

func mustRequest(amount Money, source, description string) PaymentRequest {
	r, err := NewPaymentRequest(amount, source, description)
	if err != nil {
		panic(err)
	}
	return r
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	processor := NewFakeProcessor()
	store := NewIdempotencyStore(clock, KeyTTLMillis, LeaseMillis)
	api := NewPaymentsHandler(store, processor)
	acme := "merchant_acme"

	order := mustRequest(must(Parse("19.99", USD)), "tok_visa_4242", "Order 1001")
	fmt.Println("== Fingerprint: SHA-256 of the canonical body ==")
	fmt.Print(order.Canonical())
	fmt.Println("fingerprint =", order.Fingerprint())

	fmt.Println("\n== First call charges; the duplicate is replayed, not re-charged ==")
	first, _ := api.Post(acme, "key-1001", order)
	fmt.Printf("POST #1 -> %d %s %v\n", first.Status, first.Body, first.Headers)
	second, _ := api.Post(acme, "key-1001", order)
	fmt.Printf("POST #2 -> %d %s %v\n", second.Status, second.Body, second.Headers)
	fmt.Println("processor charge calls =", processor.ChargeCalls())

	fmt.Println("\n== Same key, different body: refuse, do not guess ==")
	changed := mustRequest(must(Parse("29.99", USD)), "tok_visa_4242", "Order 1001")
	mismatch, _ := api.Post(acme, "key-1001", changed)
	fmt.Printf("POST #3 -> %d %s\n", mismatch.Status, mismatch.Body)

	fmt.Println("\n== Same key, different merchant: a different payment ==")
	other, _ := api.Post("merchant_globex", "key-1001", order)
	fmt.Printf("POST by globex -> %d replayed=%v ref=%s\n", other.Status, other.Replayed(), ReferenceIDFor(ScopedKey("merchant_globex", "key-1001")))
	fmt.Println("processor charge calls =", processor.ChargeCalls())

	fmt.Println("\n== Crash after the processor committed, before we stored the response ==")
	order2 := mustRequest(must(Parse("45.00", USD)), "tok_visa_4242", "Order 1002")
	processor.CrashAfterNextCharge()
	if _, err := api.Post(acme, "key-1002", order2); err != nil {
		fmt.Println("POST #4 ->", err)
	}
	tooSoon, _ := api.Post(acme, "key-1002", order2)
	fmt.Printf("retry at +0s   -> %d Retry-After=%d (owner lease still live)\n", tooSoon.Status, tooSoon.RetryAfterSeconds())
	clock.Advance(LeaseMillis)
	recovered, _ := api.Post(acme, "key-1002", order2)
	fmt.Printf("retry at +30s  -> %d %s\n", recovered.Status, recovered.Body)
	fmt.Printf("processor charge calls = %d, charges stored = %d  (recovery looked up %s instead of charging again)\n",
		processor.ChargeCalls(), processor.ChargesStored(), ReferenceIDFor(ScopedKey(acme, "key-1002")))

	fmt.Println("\n== Keys expire after 24h ==")
	clock.Advance(KeyTTLMillis)
	afterTTL, _ := api.Post(acme, "key-1001", order)
	fmt.Printf("POST key-1001 at +24h -> %d replayed=%v (new charge: %d calls total)\n", afterTTL.Status, afterTTL.Replayed(), processor.ChargeCalls())

	fmt.Println("\n== Declines are stored and replayed too ==")
	declined := mustRequest(must(Parse("10.00", USD)), "tok_decline_0002", "Order 1003")
	d1, _ := api.Post(acme, "key-1003", declined)
	d2, _ := api.Post(acme, "key-1003", declined)
	fmt.Printf("POST #1 -> %d, POST #2 -> %d replayed=%v\n", d1.Status, d2.Status, d2.Replayed())

	fmt.Println("\n== Reads are not idempotent operations: no key, no stored response ==")
	id := ReferenceIDFor(ScopedKey(acme, "key-1002"))
	read := api.Get(id)
	fmt.Printf("GET /payments/%s -> %d %s\n", id, read.Status, read.Body)
	fmt.Printf("GET /payments/ref_missing -> %d; store records = %d (unchanged by reads)\n", api.Get("ref_missing").Status, store.Size())
}
