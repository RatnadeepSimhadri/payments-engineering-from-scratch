// Stage 1 tests for projects/idempotent-payment-gateway (Go): SHA-256 fingerprints, canonical form, and the
// New / InProgress / Replay / Conflict decisions of the idempotency store with TTL on an injected clock.
// Port of Stage01Test.java: same checks, same names, same order. Helpers prefixed s01 because every stage's
// test file compiles into the same package in the grader's sandbox.
package main

import (
	"errors"
	"testing"
)

func s01check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// s01checkErr expects err to be (or wrap) want; a nil want accepts any non-nil error.
func s01checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s01request(t *testing.T, key, amount string) ChargeRequest {
	t.Helper()
	req, err := NewChargeRequest("m_1", key, must(Parse(amount, USD)), "tok_visa", "order-42")
	if err != nil {
		t.Fatalf("NewChargeRequest(%s, %s): %v", key, amount, err)
	}
	return req
}

func TestStage01Fingerprints(t *testing.T) {
	// Fingerprints: SHA-256 (FIPS 180-4 test vector) over a canonical string that excludes the key
	s01check(t, "sha256 of abc", Sha256Hex("abc") == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	s01check(t, "sha256 of empty string", Sha256Hex("") == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	req := s01request(t, "key-1", "19.99")
	s01check(t, "canonical form", req.Canonical() == "1999\nUSD\ntok_visa\norder-42")
	s01check(t, "fingerprint is sha256 of canonical", req.Fingerprint() == Sha256Hex(req.Canonical()))
	s01check(t, "fingerprint known value", req.Fingerprint() == "485e792d29b86326c3335f0b5e84d3a948d47121f7e4c4b57f889d7287e90b91")
	s01check(t, "fingerprint ignores the key", s01request(t, "key-2", "19.99").Fingerprint() == req.Fingerprint())
	s01check(t, "fingerprint changes with amount", s01request(t, "key-1", "29.99").Fingerprint() != req.Fingerprint())
	eur, _ := NewChargeRequest("m_1", "key-1", must(Parse("19.99", EUR)), "tok_visa", "order-42")
	s01check(t, "fingerprint changes with currency", eur.Fingerprint() != req.Fingerprint())
	desc, _ := NewChargeRequest("m_1", "key-1", must(Parse("19.99", USD)), "tok_visa", "order-43")
	s01check(t, "fingerprint changes with description", desc.Fingerprint() != req.Fingerprint())

	// Request validation
	_, err := NewChargeRequest("m_1", "key-1", must(Parse("0.00", USD)), "tok_visa", "order-42")
	s01checkErr(t, "zero amount rejected", err, nil)
	_, err = NewChargeRequest("m_1", "key-1", must(Parse("-1.00", USD)), "tok_visa", "order-42")
	s01checkErr(t, "negative amount rejected", err, nil)
	_, err = NewChargeRequest("m_1", " ", must(Parse("1.00", USD)), "tok_visa", "x")
	s01checkErr(t, "blank key rejected", err, nil)
	_, err = NewChargeRequest("m_1", "k", must(Parse("1.00", USD)), "tok_visa", "a\nb")
	s01checkErr(t, "newline in description rejected", err, nil)
}

func TestStage01Store(t *testing.T) {
	req := s01request(t, "key-1", "19.99")
	other := s01request(t, "key-1", "29.99")

	// Store decisions
	clock := NewFakeClock(1_000_000)
	store, err := NewIdempotencyStore(clock, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewIdempotencyStore(clock, 0)
	s01checkErr(t, "ttl must be positive", err, nil)
	s01check(t, "first begin is NEW", store.Begin("m_1", "key-1", req.Fingerprint()).Decision == DecisionNew)
	s01check(t, "NEW carries no response", store.Begin("m_1", "key-9", req.Fingerprint()).Response == nil)
	s01check(t, "second begin while open is IN_PROGRESS", store.Begin("m_1", "key-1", req.Fingerprint()).Decision == DecisionInProgress)
	rec, ok := store.Get("m_1", "key-1")
	s01check(t, "record is IN_PROGRESS", ok && rec.State == KeyInProgress)
	s01check(t, "record expiry is created + ttl", ok && rec.ExpiresAt == 1_060_000 && rec.CreatedAt == 1_000_000)
	s01check(t, "different merchant, same key is NEW", store.Begin("m_2", "key-1", req.Fingerprint()).Decision == DecisionNew)
	s01check(t, "different fingerprint while open is CONFLICT", store.Begin("m_1", "key-1", other.Fingerprint()).Decision == DecisionConflict)

	stored := ApiResponse{201, `{"payment_id":"pay_1"}`, false}
	if err := store.Complete("m_1", "key-1", stored); err != nil {
		t.Fatalf("complete: %v", err)
	}
	replay := store.Begin("m_1", "key-1", req.Fingerprint())
	s01check(t, "completed key replays", replay.Decision == DecisionReplay)
	s01check(t, "replay returns the stored status and body", replay.Response != nil && replay.Response.Status == 201 && replay.Response.Body == stored.Body)
	s01check(t, "stored response is not marked replayed", replay.Response != nil && !replay.Response.Replayed)
	s01check(t, "AsReplay flags the copy", replay.Response != nil && replay.Response.AsReplay().Replayed && replay.Response.AsReplay().Body == stored.Body)
	s01check(t, "replay is stable", store.Begin("m_1", "key-1", req.Fingerprint()).Decision == DecisionReplay)
	s01check(t, "different fingerprint after completion is CONFLICT", store.Begin("m_1", "key-1", other.Fingerprint()).Decision == DecisionConflict)
	rec, ok = store.Get("m_1", "key-1")
	s01check(t, "record is COMPLETED", ok && rec.State == KeyCompleted)

	// complete / release misuse
	s01checkErr(t, "complete twice", store.Complete("m_1", "key-1", stored), ErrIllegalState)
	s01checkErr(t, "complete unknown key", store.Complete("m_1", "nope", stored), ErrIllegalState)
	_, err = store.Release("m_1", "key-1")
	s01checkErr(t, "release completed key", err, ErrIllegalState)
	released, err := store.Release("m_1", "nope")
	s01check(t, "release unknown key is false", err == nil && !released)
	released, err = store.Release("m_1", "key-9")
	s01check(t, "release in-progress key is true", err == nil && released)
	_, ok = store.Get("m_1", "key-9")
	s01check(t, "released key is absent", !ok)
	s01check(t, "released key can be claimed again", store.Begin("m_1", "key-9", req.Fingerprint()).Decision == DecisionNew)

	// TTL on the injected clock
	clock.Advance(59_999)
	s01check(t, "one ms before expiry still replays", store.Begin("m_1", "key-1", req.Fingerprint()).Decision == DecisionReplay)
	clock.Advance(1)
	_, ok = store.Get("m_1", "key-1")
	s01check(t, "expired record reads as absent", !ok)
	s01check(t, "expired key begins fresh as NEW", store.Begin("m_1", "key-1", other.Fingerprint()).Decision == DecisionNew)
	rec, ok = store.Get("m_1", "key-1")
	s01check(t, "fresh record has the new fingerprint", ok && rec.Fingerprint == other.Fingerprint())
	s01check(t, "size counts stale records", store.Size() == 3)
	purged := store.PurgeExpired()
	s01check(t, "purge removes exactly the expired records", purged == 2)
	s01check(t, "size after purge", store.Size() == 1)
	clock.Advance(60_000)
	s01check(t, "purge removes everything once all expire", store.PurgeExpired() == 1 && store.Size() == 0)
}
