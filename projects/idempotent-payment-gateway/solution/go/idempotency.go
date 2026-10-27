// Project: projects/idempotent-payment-gateway — Stage 01: idempotency store (Go).
// Keyed by (merchantID, idempotencyKey); every record carries the SHA-256 fingerprint of the canonical
// request so a reused key with a different body is detected instead of silently replayed.
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Sha256Hex is SHA-256 over a canonical string, rendered as 64 lowercase hex characters.
func Sha256Hex(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// ChargeRequest is a merchant's request to charge a customer. The idempotency key identifies the attempt;
// the canonical form (amount, currency, source, description joined by newlines) identifies the intent.
type ChargeRequest struct {
	MerchantID     string
	IdempotencyKey string
	Amount         Money
	Source         string
	Description    string
}

// NewChargeRequest validates what the Java compact constructor validates.
func NewChargeRequest(merchantID, idempotencyKey string, amount Money, source, description string) (ChargeRequest, error) {
	for _, f := range []struct{ value, field string }{{merchantID, "merchantId"}, {idempotencyKey, "idempotencyKey"}, {source, "source"}} {
		if strings.TrimSpace(f.value) == "" {
			return ChargeRequest{}, fmt.Errorf("%s is required", f.field)
		}
	}
	if strings.Contains(source, "\n") || strings.Contains(description, "\n") {
		return ChargeRequest{}, errors.New("source and description must not contain newlines")
	}
	if !amount.IsPositive() {
		return ChargeRequest{}, fmt.Errorf("amount must be positive: %v", amount)
	}
	return ChargeRequest{merchantID, idempotencyKey, amount, source, description}, nil
}

// Canonical is "1999\nUSD\ntok_visa\norder-42": the fields that define the intent, in a fixed order. The key is not part of it.
func (r ChargeRequest) Canonical() string {
	return fmt.Sprintf("%d\n%s\n%s\n%s", r.Amount.MinorUnits, r.Amount.Currency.Code, r.Source, r.Description)
}

func (r ChargeRequest) Fingerprint() string { return Sha256Hex(r.Canonical()) }

// ApiResponse is what the gateway returned to the merchant; the store keeps it so a replay returns the same bytes.
type ApiResponse struct {
	Status   int
	Body     string
	Replayed bool
}

func (a ApiResponse) AsReplay() ApiResponse { return ApiResponse{a.Status, a.Body, true} }

type KeyState int

const (
	KeyInProgress KeyState = iota
	KeyCompleted
)

func (s KeyState) String() string { return [...]string{"IN_PROGRESS", "COMPLETED"}[s] }

type Decision int

const (
	DecisionNew Decision = iota
	DecisionReplay
	DecisionInProgress
	DecisionConflict
)

func (d Decision) String() string { return [...]string{"NEW", "REPLAY", "IN_PROGRESS", "CONFLICT"}[d] }

// BeginResult is the outcome of Begin(): Response is non-nil only for DecisionReplay.
type BeginResult struct {
	Decision Decision
	Response *ApiResponse
}

type IdempotencyRecord struct {
	MerchantID  string
	Key         string
	Fingerprint string
	State       KeyState
	Response    *ApiResponse
	CreatedAt   int64
	ExpiresAt   int64
}

func (r IdempotencyRecord) IsExpired(now int64) bool { return now >= r.ExpiresAt }

// ErrIllegalState is Java's IllegalStateException: the store was asked to do something its record does not allow.
var ErrIllegalState = errors.New("illegal state")

type storeKey struct{ merchantID, key string }

// IdempotencyStore is the in-memory store. Every method holds the mutex so that concurrent Begin() calls
// with the same key admit exactly one caller; the rest see DecisionInProgress.
type IdempotencyStore struct {
	mu        sync.Mutex
	clock     Clock
	ttlMillis int64
	records   map[storeKey]IdempotencyRecord
}

func NewIdempotencyStore(clock Clock, ttlMillis int64) (*IdempotencyStore, error) {
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	if ttlMillis <= 0 {
		return nil, fmt.Errorf("ttlMillis must be positive: %d", ttlMillis)
	}
	return &IdempotencyStore{clock: clock, ttlMillis: ttlMillis, records: map[storeKey]IdempotencyRecord{}}, nil
}

func (s *IdempotencyStore) TTLMillis() int64 { return s.ttlMillis }

// Begin claims the key for this request or reports why the caller must not proceed.
func (s *IdempotencyStore) Begin(merchantID, key, fingerprint string) BeginResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey{merchantID, key}
	now := s.clock.NowMillis()
	existing, ok := s.records[k]
	if ok && existing.IsExpired(now) {
		delete(s.records, k)
		ok = false
	}
	if !ok {
		s.records[k] = IdempotencyRecord{merchantID, key, fingerprint, KeyInProgress, nil, now, now + s.ttlMillis}
		return BeginResult{DecisionNew, nil}
	}
	if existing.Fingerprint != fingerprint {
		return BeginResult{DecisionConflict, nil}
	}
	if existing.State == KeyInProgress {
		return BeginResult{DecisionInProgress, nil}
	}
	response := *existing.Response
	return BeginResult{DecisionReplay, &response}
}

// Complete stores the response for an in-progress key. Completing twice, or a key never begun, is ErrIllegalState.
func (s *IdempotencyStore) Complete(merchantID, key string, response ApiResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey{merchantID, key}
	existing, ok := s.records[k]
	if !ok || existing.IsExpired(s.clock.NowMillis()) {
		return fmt.Errorf("%w: no in-progress record for %s/%s", ErrIllegalState, merchantID, key)
	}
	if existing.State == KeyCompleted {
		return fmt.Errorf("%w: record already completed for %s/%s", ErrIllegalState, merchantID, key)
	}
	stored := ApiResponse{response.Status, response.Body, false}
	s.records[k] = IdempotencyRecord{merchantID, key, existing.Fingerprint, KeyCompleted, &stored, existing.CreatedAt, existing.ExpiresAt}
	return nil
}

// Release gives the key back after a failure that must not be replayed (timeout, circuit open).
// Returns false when there was nothing to release; a completed key is kept and is ErrIllegalState.
func (s *IdempotencyStore) Release(merchantID, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey{merchantID, key}
	existing, ok := s.records[k]
	if !ok {
		return false, nil
	}
	if existing.State == KeyCompleted {
		return false, fmt.Errorf("%w: cannot release a completed record for %s/%s", ErrIllegalState, merchantID, key)
	}
	delete(s.records, k)
	return true, nil
}

// Get is the live record, if any; expired records read as absent (Optional -> value, ok).
func (s *IdempotencyStore) Get(merchantID, key string) (IdempotencyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.records[storeKey{merchantID, key}]
	if !ok || existing.IsExpired(s.clock.NowMillis()) {
		return IdempotencyRecord{}, false
	}
	return existing, true
}

// PurgeExpired drops every expired record and returns how many were removed.
func (s *IdempotencyStore) PurgeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.NowMillis()
	removed := 0
	for k, r := range s.records {
		if r.IsExpired(now) {
			delete(s.records, k)
			removed++
		}
	}
	return removed
}

// Size is the records held, expired or not; call PurgeExpired() first for the live count.
func (s *IdempotencyStore) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}
