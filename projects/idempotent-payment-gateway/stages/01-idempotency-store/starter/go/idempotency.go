// Project: projects/idempotent-payment-gateway — Stage 01 starter (Go): idempotency store.
// Docs: projects/idempotent-payment-gateway/stages/01-idempotency-store/docs/en.md
// Run:  go run .
// Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 1 --path <workspace>
// The data types, NewChargeRequest, ApiResponse, the enums, IdempotencyRecord and the store's constructor are
// given. Every function that panics "not implemented" is yours; keep the names, the graders of every stage use them.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Sha256Hex is SHA-256 over a canonical string, rendered as 64 lowercase hex characters.
func Sha256Hex(canonical string) string {
	panic("not implemented: TODO stage 01 Sha256Hex: crypto/sha256 over the UTF-8 bytes, lowercase hex")
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
	panic("not implemented: TODO stage 01 ChargeRequest.Canonical: minor units, currency code, source, description joined by newlines; the key is NOT part of it")
}

func (r ChargeRequest) Fingerprint() string {
	panic("not implemented: TODO stage 01 ChargeRequest.Fingerprint: Sha256Hex(Canonical())")
}

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
	panic("not implemented: TODO stage 01 IdempotencyStore.Begin: expired counts as absent; NEW / CONFLICT (checked first) / IN_PROGRESS / REPLAY with the stored response")
}

// Complete stores the response for an in-progress key. Completing twice, or a key never begun, is ErrIllegalState.
func (s *IdempotencyStore) Complete(merchantID, key string, response ApiResponse) error {
	panic("not implemented: TODO stage 01 IdempotencyStore.Complete: store the response (Replayed=false); unknown, expired or completed -> ErrIllegalState")
}

// Release gives the key back after a failure that must not be replayed (timeout, circuit open).
// Returns false when there was nothing to release; a completed key is kept and is ErrIllegalState.
func (s *IdempotencyStore) Release(merchantID, key string) (bool, error) {
	panic("not implemented: TODO stage 01 IdempotencyStore.Release: drop an in-progress record (true); absent -> false; completed -> ErrIllegalState")
}

// Get is the live record, if any; expired records read as absent (Optional -> value, ok).
func (s *IdempotencyStore) Get(merchantID, key string) (IdempotencyRecord, bool) {
	panic("not implemented: TODO stage 01 IdempotencyStore.Get: the live record, if any; expired reads as absent")
}

// PurgeExpired drops every expired record and returns how many were removed.
func (s *IdempotencyStore) PurgeExpired() int {
	panic("not implemented: TODO stage 01 IdempotencyStore.PurgeExpired: drop every expired record, return how many")
}

// Size is the records held, expired or not; call PurgeExpired() first for the live count.
func (s *IdempotencyStore) Size() int {
	panic("not implemented: TODO stage 01 IdempotencyStore.Size: records held, expired or not")
}
