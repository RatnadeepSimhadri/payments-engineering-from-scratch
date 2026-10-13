// Tests for phases/10-money-and-ledgers/07-holds-and-available-balance. Run: go test -race ./...
package main

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

func funded(clock *ManualClock) *Account {
	a := NewAccount("acct", USD, clock)
	if err := a.Credit(usd("500.00")); err != nil {
		panic(err)
	}
	return a
}

func mustAuth(t *testing.T, a *Account, id string, amount Money, ttl int64) *Hold {
	t.Helper()
	h, err := a.Authorize(id, amount, ttl)
	if err != nil {
		t.Fatalf("Authorize(%s): %v", id, err)
	}
	return h
}

func TestCreditRaisesLedgerAndAvailableTogether(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	if a.LedgerBalance() != usd("500.00") || a.AvailableBalance() != a.LedgerBalance() {
		t.Fatalf("ledger=%s available=%s", a.LedgerBalance(), a.AvailableBalance())
	}
	if !a.CheckInvariant() {
		t.Fatal("invariant")
	}
}

func TestAuthorizeReducesAvailableNotLedger(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	h := mustAuth(t, a, "hotel", usd("300.00"), 7*Day)
	if a.LedgerBalance() != usd("500.00") {
		t.Errorf("ledger moved: %s", a.LedgerBalance())
	}
	if a.AvailableBalance() != usd("200.00") {
		t.Errorf("available = %s, want 200.00", a.AvailableBalance())
	}
	if !h.IsActive() || h.Status != Active {
		t.Errorf("hold status %v", h.Status)
	}
	if !a.CheckInvariant() {
		t.Error("invariant")
	}
}

func TestHoldsStackAndAvailableNeverGoesNegative(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	mustAuth(t, a, "hotel", usd("300.00"), 7*Day)
	mustAuth(t, a, "fuel", usd("125.00"), 2*Hour)
	if a.HeldTotal() != usd("425.00") || a.AvailableBalance() != usd("75.00") {
		t.Fatalf("held=%s available=%s", a.HeldTotal(), a.AvailableBalance())
	}
	if len(a.ActiveHolds()) != 2 {
		t.Errorf("active holds = %d", len(a.ActiveHolds()))
	}
	if _, err := a.Authorize("too-much", usd("75.01"), Day); !errors.Is(err, ErrInsufficientAvailable) {
		t.Errorf("expected ErrInsufficientAvailable, got %v", err)
	}
	mustAuth(t, a, "exact", usd("75.00"), Day)
	if !a.AvailableBalance().IsZero() || !a.CheckInvariant() {
		t.Errorf("available=%s", a.AvailableBalance())
	}
}

func TestVoidReturnsTheWholeAmount(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	mustAuth(t, a, "exact", usd("75.00"), Day)
	released, err := a.VoidHold("exact")
	if err != nil || released != usd("75.00") {
		t.Fatalf("void: %s %v", released, err)
	}
	if a.AvailableBalance() != usd("500.00") {
		t.Errorf("available = %s", a.AvailableBalance())
	}
	h, _ := a.Hold("exact")
	if h.Status != Voided {
		t.Errorf("status %v", h.Status)
	}
	if _, err := a.VoidHold("exact"); !errors.Is(err, ErrHoldNotActive) {
		t.Errorf("void twice: %v", err)
	}
}

func TestAvailableNotLedgerGatesASettledDebit(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	mustAuth(t, a, "hotel", usd("300.00"), 7*Day)
	mustAuth(t, a, "fuel", usd("125.00"), 2*Hour)
	if err := a.Debit(usd("80.00")); !errors.Is(err, ErrInsufficientAvailable) {
		t.Errorf("debit beyond available: %v", err)
	}
	if err := a.Debit(usd("5.00")); err != nil {
		t.Fatal(err)
	}
	if a.LedgerBalance() != usd("495.00") || a.AvailableBalance() != usd("70.00") {
		t.Errorf("ledger=%s available=%s", a.LedgerBalance(), a.AvailableBalance())
	}
}

func TestPartialCaptureReleasesTheRemainder(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	mustAuth(t, a, "fuel", usd("125.00"), 2*Hour)
	released, err := a.Capture("fuel", usd("42.17"))
	if err != nil || released != usd("82.83") {
		t.Fatalf("capture: %s %v", released, err)
	}
	if a.LedgerBalance() != usd("457.83") || a.AvailableBalance() != usd("457.83") {
		t.Errorf("ledger=%s available=%s", a.LedgerBalance(), a.AvailableBalance())
	}
	h, _ := a.Hold("fuel")
	if h.Status != Captured || h.Captured != usd("42.17") {
		t.Errorf("hold %v", h)
	}
	if !a.CheckInvariant() {
		t.Error("invariant")
	}
	if _, err := a.Capture("fuel", usd("1.00")); !errors.Is(err, ErrHoldNotActive) {
		t.Errorf("capture twice: %v", err)
	}
}

func TestOverCaptureIsRejectedAndChangesNothing(t *testing.T) {
	a := funded(NewManualClock(1_000_000))
	mustAuth(t, a, "hotel", usd("300.00"), 7*Day)
	if _, err := a.Capture("hotel", usd("300.01")); !errors.Is(err, ErrOverCapture) {
		t.Errorf("over-capture: %v", err)
	}
	h, _ := a.Hold("hotel")
	if a.LedgerBalance() != usd("500.00") || !h.IsActive() {
		t.Errorf("state changed after rejection")
	}
}

func TestExpiredHoldIsReleasedByTheClock(t *testing.T) {
	clock := NewManualClock(1_000_000)
	a := funded(clock)
	mustAuth(t, a, "hotel", usd("300.00"), 7*Day)
	_ = clock.Advance(7*Day - 1)
	if len(a.ActiveHolds()) != 1 || a.AvailableBalance() != usd("200.00") {
		t.Fatalf("one second early: active=%d available=%s", len(a.ActiveHolds()), a.AvailableBalance())
	}
	_ = clock.Advance(1)
	if len(a.ActiveHolds()) != 0 || a.AvailableBalance() != usd("500.00") {
		t.Fatalf("at expiry: active=%d available=%s", len(a.ActiveHolds()), a.AvailableBalance())
	}
	h, _ := a.Hold("hotel")
	if h.Status != Expired {
		t.Errorf("status %v", h.Status)
	}
	if _, err := a.Capture("hotel", usd("300.00")); !errors.Is(err, ErrHoldNotActive) {
		t.Errorf("capture expired: %v", err)
	}
	if !a.CheckInvariant() {
		t.Error("invariant")
	}
}

func TestIncrementalAuthorizationGrowsTheHold(t *testing.T) {
	b := funded(NewManualClock(0))
	mustAuth(t, b, "h", usd("310.00"), Day)
	if _, err := b.Increment("h", usd("45.00")); err != nil {
		t.Fatal(err)
	}
	h, _ := b.Hold("h")
	if h.Amount != usd("355.00") || b.AvailableBalance() != usd("145.00") {
		t.Errorf("amount=%s available=%s", h.Amount, b.AvailableBalance())
	}
	if _, err := b.Increment("h", usd("145.01")); !errors.Is(err, ErrInsufficientAvailable) {
		t.Errorf("increment beyond available: %v", err)
	}
	released, err := b.Capture("h", usd("355.00"))
	if err != nil || !released.IsZero() || b.LedgerBalance() != usd("145.00") || !b.CheckInvariant() {
		t.Errorf("full capture: released=%s err=%v ledger=%s", released, err, b.LedgerBalance())
	}
}

func TestFailuresTheLessonWarnsAbout(t *testing.T) {
	a := funded(NewManualClock(0))
	mustAuth(t, a, "h", usd("1.00"), Day)
	if _, err := a.Authorize("h", usd("1.00"), Day); !errors.Is(err, ErrDuplicateHold) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := a.Capture("nope", usd("1.00")); !errors.Is(err, ErrUnknownHold) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := a.Authorize("eur", must(Parse("1.00", EUR)), Day); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("currency: %v", err)
	}
	if _, err := a.Authorize("zero", usd("0.00"), Day); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("zero: %v", err)
	}
	if err := NewManualClock(5).Advance(-1); err == nil {
		t.Error("clock ran backwards")
	}
}

func TestSeededRandomOperationsKeepTheInvariant(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	clock := NewManualClock(0)
	r := NewAccount("r", USD, clock)
	_ = r.Credit(usd("1000.00"))
	n := 0
	for i := 0; i < 2000; i++ {
		amt := OfMinor(int64(1+rnd.Intn(30_000)), USD)
		switch rnd.Intn(5) {
		case 0:
			_, _ = r.Authorize(fmt.Sprintf("x%d", n), amt, int64(1+rnd.Intn(3600)))
			n++
		case 1:
			if n > 0 {
				_, _ = r.Capture(fmt.Sprintf("x%d", rnd.Intn(n)), amt)
			}
		case 2:
			if n > 0 {
				_, _ = r.VoidHold(fmt.Sprintf("x%d", rnd.Intn(n)))
			}
		case 3:
			_ = clock.Advance(int64(rnd.Intn(1800)))
		default:
			_ = r.Debit(amt)
		}
		if !r.CheckInvariant() || r.AvailableBalance().IsNegative() {
			t.Fatalf("invariant broken at step %d: ledger=%s held=%s available=%s", i, r.LedgerBalance(), r.HeldTotal(), r.AvailableBalance())
		}
	}
}

func TestConcurrentHoldsAgainstOneAccount(t *testing.T) {
	shared := NewAccount("shared", USD, NewManualClock(0))
	_ = shared.Credit(usd("500.00"))
	accepted, rejected := RaceAuthorizations(shared, 50, usd("20.00"))
	if accepted != 25 || rejected != 25 {
		t.Fatalf("accepted=%d rejected=%d, want 25/25", accepted, rejected)
	}
	if !shared.AvailableBalance().IsZero() || len(shared.ActiveHolds()) != 25 || !shared.CheckInvariant() {
		t.Errorf("after race: available=%s active=%d", shared.AvailableBalance(), len(shared.ActiveHolds()))
	}
}

func TestEightGoroutinesRacingForTheWholeBalanceOneWins(t *testing.T) {
	solo := NewAccount("solo", USD, NewManualClock(0))
	_ = solo.Credit(usd("100.00"))
	if accepted, rejected := RaceAuthorizations(solo, 8, usd("100.00")); accepted != 1 || rejected != 7 {
		t.Fatalf("accepted=%d rejected=%d, want 1/7", accepted, rejected)
	}
}
