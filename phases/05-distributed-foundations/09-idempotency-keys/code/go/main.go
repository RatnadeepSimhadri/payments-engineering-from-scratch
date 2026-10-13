// Lesson: phases/05-distributed-foundations/09-idempotency-keys/docs/en.md
// Idempotency Keys: Exactly-Once Effects on At-Least-Once Networks
// Run:  go run .
// Test: go test -race ./...
// Sources: Stripe API reference, "Idempotent requests"; IETF draft-ietf-httpapi-idempotency-key-header;
// RFC 9110 section 9.2.2 (idempotent methods). Go 1.22+, standard library only.
// Same design as the Java and Python versions: same fingerprint, same outcomes, same status codes.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const DayMs int64 = 24 * 60 * 60 * 1000

// Clock is what the store reads for TTL and lease decisions. Tests use FakeClock so expiry is replayable.
type Clock interface {
	NowMs() int64
}

type FakeClock struct{ now int64 }

func NewFakeClock(startMs int64) *FakeClock { return &FakeClock{now: startMs} }

func (c *FakeClock) NowMs() int64 { return c.now }

func (c *FakeClock) Advance(ms int64) {
	if ms < 0 {
		panic("cannot advance a negative duration")
	}
	c.now += ms
}

// Currency is an ISO 4217 currency, copied from Phase 10 Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%d %s", m.MinorUnits, m.Currency.Code)
	}
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	sign := ""
	if abs < 0 {
		abs, sign = -abs, "-"
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

var ErrInvalidRequest = errors.New("invalid request")

// ChargeRequest is one POST /v1/charges as the server sees it. The key is chosen by the client (nil means
// none was sent); the fingerprint is computed by the server from the fields that have an effect.
type ChargeRequest struct {
	AccountID      string
	IdempotencyKey *string
	Amount         Money
	CardToken      string
	Description    string
}

func NewChargeRequest(accountID string, idempotencyKey *string, amount Money, cardToken, description string) (ChargeRequest, error) {
	if idempotencyKey != nil && (strings.TrimSpace(*idempotencyKey) == "" || len(*idempotencyKey) > 255) {
		return ChargeRequest{}, fmt.Errorf("%w: Idempotency-Key must be 1..255 characters", ErrInvalidRequest)
	}
	if amount.MinorUnits <= 0 {
		return ChargeRequest{}, fmt.Errorf("%w: amount must be positive", ErrInvalidRequest)
	}
	return ChargeRequest{accountID, idempotencyKey, amount, cardToken, description}, nil
}

// Canonical is the fixed-order, one-field-per-line form, so a reordered JSON body hashes the same.
func (r ChargeRequest) Canonical() string {
	return fmt.Sprintf("POST /v1/charges\n%d\n%s\n%s\n%s", r.Amount.MinorUnits, r.Amount.Currency.Code, r.CardToken, r.Description)
}

func (r ChargeRequest) Fingerprint() string { return Sha256Hex(r.Canonical()) }

func Sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Response is what the client receives. Replayed is the Idempotent-Replayed: true header a replay carries.
type Response struct {
	Status   int
	Body     string
	Replayed bool
}

func (r Response) AsReplay() Response { return Response{r.Status, r.Body, true} }

// Outcome is one of the four answers the store can give when a request arrives.
type Outcome int

const (
	New        Outcome = iota // no usable entry: execute, then Complete() or Abandon()
	Replay                    // completed entry, same fingerprint: return its response
	InProgress                // another request with this key is executing: 409
	Conflict                  // same key, different fingerprint: 422
)

func (o Outcome) String() string {
	return [...]string{"NEW", "REPLAY", "IN_PROGRESS", "CONFLICT"}[o]
}

type Begin struct {
	Outcome  Outcome
	Response *Response // set only for Replay
}

// entry is one row of the store. It is in progress until Complete stores a response.
type entry struct {
	fingerprint string
	startedAtMs int64
	response    *Response
	expiresAtMs int64
}

func (e *entry) inProgress() bool { return e.response == nil }

type scopedKey struct{ account, key string }

var ErrNoReservation = errors.New("complete without a live reservation")

// IdempotencyStore is keyed by (account, idempotency key); it remembers the fingerprint and, once done, the
// response to replay. One mutex: Begin is the atomic check-and-reserve that makes concurrent duplicates safe.
type IdempotencyStore struct {
	mu                sync.Mutex
	entries           map[scopedKey]*entry
	clock             Clock
	ttlMs             int64
	inProgressLeaseMs int64
}

func NewIdempotencyStore(clock Clock, ttlMs, inProgressLeaseMs int64) (*IdempotencyStore, error) {
	if ttlMs <= 0 || inProgressLeaseMs <= 0 {
		return nil, fmt.Errorf("%w: ttl and lease must be positive", ErrInvalidRequest)
	}
	return &IdempotencyStore{entries: map[scopedKey]*entry{}, clock: clock, ttlMs: ttlMs, inProgressLeaseMs: inProgressLeaseMs}, nil
}

// Begin atomically looks up the key, decides Replay / InProgress / Conflict for a live entry, and otherwise
// reserves the key for this caller and answers New.
func (s *IdempotencyStore) Begin(accountID, key, fingerprint string) Begin {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.NowMs()
	k := scopedKey{accountID, key}
	e := s.entries[k]
	if e != nil {
		if !e.inProgress() && now >= e.expiresAtMs {
			delete(s.entries, k) // TTL passed: forget it
			e = nil
		} else if e.inProgress() && now-e.startedAtMs >= s.inProgressLeaseMs {
			delete(s.entries, k) // the first attempt died: let a retry take over
			e = nil
		}
	}
	if e != nil {
		if e.fingerprint != fingerprint {
			return Begin{Outcome: Conflict}
		}
		if e.inProgress() {
			return Begin{Outcome: InProgress}
		}
		r := e.response.AsReplay()
		return Begin{Outcome: Replay, Response: &r}
	}
	s.entries[k] = &entry{fingerprint: fingerprint, startedAtMs: now}
	return Begin{Outcome: New}
}

// Complete stores the handler's response, whatever its status, and starts the TTL.
func (s *IdempotencyStore) Complete(accountID, key string, response Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[scopedKey{accountID, key}]
	if e == nil || !e.inProgress() {
		return fmt.Errorf("%w: %s", ErrNoReservation, key)
	}
	r := response
	e.response = &r
	e.expiresAtMs = s.clock.NowMs() + s.ttlMs
	return nil
}

// Abandon releases a reservation whose handler crashed before producing a response, so a retry can execute.
func (s *IdempotencyStore) Abandon(accountID, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scopedKey{accountID, key}
	if e := s.entries[k]; e != nil && e.inProgress() {
		delete(s.entries, k)
	}
}

// PurgeExpired removes every completed entry whose TTL has passed and returns how many were removed.
func (s *IdempotencyStore) PurgeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.NowMs()
	removed := 0
	for k, e := range s.entries {
		if !e.inProgress() && now >= e.expiresAtMs {
			delete(s.entries, k)
			removed++
		}
	}
	return removed
}

func (s *IdempotencyStore) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Processor is the thing with side effects: the card network. One call here is one movement of money.
// A non-nil error means the call crashed before producing any response.
type Processor interface {
	Charge(request ChargeRequest) (Response, error)
}

var ErrProcessorCrash = errors.New("processor connection reset")

// FakeProcessor succeeds with ch_1, ch_2, ... and can be told to decline, fail, or crash.
type FakeProcessor struct {
	mu              sync.Mutex
	executions      int
	declineNext     int
	serverErrorNext int
	crashNext       int
}

func (p *FakeProcessor) Charge(request ChargeRequest) (Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.executions++
	if p.crashNext > 0 {
		p.crashNext--
		return Response{}, ErrProcessorCrash
	}
	if p.serverErrorNext > 0 {
		p.serverErrorNext--
		return Response{502, `{"error":"processor_unavailable"}`, false}, nil
	}
	if p.declineNext > 0 {
		p.declineNext--
		return Response{402, `{"error":"card_declined"}`, false}, nil
	}
	return Response{200, fmt.Sprintf(`{"id":"ch_%d","amount":"%s","status":"succeeded"}`, p.executions, request.Amount), false}, nil
}

func (p *FakeProcessor) Executions() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executions
}

func (p *FakeProcessor) DeclineNext() { p.mu.Lock(); p.declineNext++; p.mu.Unlock() }

func (p *FakeProcessor) ServerErrorNext() { p.mu.Lock(); p.serverErrorNext++; p.mu.Unlock() }

func (p *FakeProcessor) CrashNext() { p.mu.Lock(); p.crashNext++; p.mu.Unlock() }

// PaymentsApi is the endpoint. Without a key every call executes. With a key: reserve, execute, store;
// replay on retry; 409 while another attempt is running; 422 when the key is reused with a different body.
type PaymentsApi struct {
	Store     *IdempotencyStore
	Processor Processor
}

func (a *PaymentsApi) Charge(request ChargeRequest) (Response, error) {
	if request.IdempotencyKey == nil {
		return a.Processor.Charge(request)
	}
	key := *request.IdempotencyKey
	b := a.Store.Begin(request.AccountID, key, request.Fingerprint())
	switch b.Outcome {
	case Replay:
		return *b.Response, nil
	case InProgress:
		return Response{409, `{"error":"idempotency_key_in_progress"}`, false}, nil
	case Conflict:
		return Response{422, `{"error":"idempotency_key_reused_with_different_request"}`, false}, nil
	}
	response, err := a.Processor.Charge(request)
	if err != nil {
		a.Store.Abandon(request.AccountID, key)
		return Response{}, err
	}
	if err := a.Store.Complete(request.AccountID, key, response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func strPtr(s string) *string { return &s }

func mustRequest(accountID string, key *string, amount Money, cardToken, description string) ChargeRequest {
	r, err := NewChargeRequest(accountID, key, amount, cardToken, description)
	if err != nil {
		panic(err)
	}
	return r
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	processor := &FakeProcessor{}
	store, _ := NewIdempotencyStore(clock, DayMs, 30_000)
	api := &PaymentsApi{store, processor}
	req := mustRequest("acct_1", strPtr("order-8812-attempt"), OfMinor(1999, USD), "tok_visa_4242", "Order 8812")

	fmt.Println("== Without a key: a retry is a second charge ==")
	noKey := mustRequest("acct_1", nil, OfMinor(1999, USD), "tok_visa_4242", "Order 8812")
	api.Charge(noKey)
	api.Charge(noKey)
	fmt.Printf("two sends of the same request executed %d charges\n", processor.Executions())

	fmt.Println("\n== With a key: the retry replays the stored response ==")
	fmt.Printf("fingerprint = %s...\n", req.Fingerprint()[:16])
	first, _ := api.Charge(req)
	fmt.Printf("first:  %d %s replayed=%v\n", first.Status, first.Body, first.Replayed)
	retry, _ := api.Charge(req)
	fmt.Printf("retry:  %d %s replayed=%v\n", retry.Status, retry.Body, retry.Replayed)
	fmt.Printf("processor executions so far: %d (the keyed pair added exactly one)\n", processor.Executions())

	fmt.Println("\n== Same key, different amount: 422, nothing executes ==")
	tampered := mustRequest("acct_1", strPtr("order-8812-attempt"), OfMinor(2999, USD), "tok_visa_4242", "Order 8812")
	conflict, _ := api.Charge(tampered)
	fmt.Printf("conflict: %d %s\n", conflict.Status, conflict.Body)

	fmt.Println("\n== Concurrent duplicates: one wins, the other sees 409 until it is done ==")
	fp := req.Fingerprint()
	a := store.Begin("acct_1", "order-9001", fp)
	b := store.Begin("acct_1", "order-9001", fp)
	fmt.Printf("request A begin -> %s ; request B begin -> %s\n", a.Outcome, b.Outcome)
	_ = store.Complete("acct_1", "order-9001", Response{200, `{"id":"ch_9001"}`, false})
	bRetry := store.Begin("acct_1", "order-9001", fp)
	fmt.Printf("A completes; B retries -> %s with body %s\n", bRetry.Outcome, bRetry.Response.Body)

	fmt.Println("\n== Declines are responses too: they replay ==")
	processor.DeclineNext()
	declined := mustRequest("acct_1", strPtr("order-7001"), OfMinor(5000, USD), "tok_visa_0002", "")
	d1, _ := api.Charge(declined)
	d2, _ := api.Charge(declined)
	fmt.Printf("first %d, retry %d replayed=%v, executions for this key: 1\n", d1.Status, d2.Status, d2.Replayed)

	fmt.Println("\n== A crash mid-execution releases the reservation; the retry executes ==")
	processor.CrashNext()
	crashy := mustRequest("acct_1", strPtr("order-7002"), OfMinor(1200, USD), "tok_visa_4242", "")
	before := processor.Executions()
	if _, err := api.Charge(crashy); err != nil {
		fmt.Println("first attempt: " + err.Error())
	}
	recovered, _ := api.Charge(crashy)
	fmt.Printf("retry: %d, executions for this key: %d (the hazard: did the first attempt move money?)\n", recovered.Status, processor.Executions()-before)

	fmt.Println("\n== TTL: after 24 h the key is forgotten and a retry executes again ==")
	fmt.Printf("entries before purge: %d\n", store.Size())
	clock.Advance(DayMs)
	fmt.Printf("purged %d expired entries; entries now: %d\n", store.PurgeExpired(), store.Size())
	late, _ := api.Charge(req)
	fmt.Printf("same request after TTL: %d replayed=%v -> a new charge %s\n", late.Status, late.Replayed, late.Body)
}
