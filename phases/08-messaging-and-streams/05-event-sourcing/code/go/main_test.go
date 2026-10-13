// Tests for phases/08-messaging-and-streams/05-event-sourcing. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

var history = []Event{
	Opened{"a1", USD}, Deposited{usd(10_000)}, Held{"h1", usd(2_500)}, Withdrawn{usd(6_000)}, Released{"h1"},
}

func TestReplayFold(t *testing.T) {
	s := Replay(history)
	if s.Version != 5 {
		t.Fatalf("version = %d, want 5", s.Version)
	}
	if s.Balance != usd(4_000) || s.Available() != usd(4_000) || len(s.Holds) != 0 {
		t.Fatalf("state = %+v", s)
	}
}

func TestHoldReducesAvailableNotBalance(t *testing.T) {
	s := Replay(history[:3])
	if s.Balance != usd(10_000) || s.Available() != usd(7_500) {
		t.Fatalf("balance %s available %s", s.Balance, s.Available())
	}
}

func TestFoldIsIncremental(t *testing.T) {
	prefix := Replay(history[:3])
	if !prefix.ApplyAll(history[3:]).Equal(Replay(history)) {
		t.Fatal("replay(all) != replay(prefix).applyAll(suffix)")
	}
}

func TestEmptyState(t *testing.T) {
	if EmptyState().IsOpen() || EmptyState().Version != 0 {
		t.Fatal("empty state should be closed at version 0")
	}
}

func expectRejected(t *testing.T, s AccountState, c Command) {
	t.Helper()
	if _, err := s.Decide(c); !errors.Is(err, ErrCommandRejected) {
		t.Fatalf("%T should be rejected, got %v", c, err)
	}
}

func TestDecideInvariants(t *testing.T) {
	s := Replay(history[:3]) // balance 100.00, hold 25.00, available 75.00
	expectRejected(t, s, Withdraw{usd(7_501)})
	if evs, err := s.Decide(Withdraw{usd(7_500)}); err != nil || len(evs) != 1 {
		t.Fatalf("withdraw exactly available: %v %v", evs, err)
	}
	expectRejected(t, s, PlaceHold{"h2", usd(7_501)})
	expectRejected(t, s, PlaceHold{"h1", usd(1)})
	expectRejected(t, s, ReleaseHold{"nope"})
	expectRejected(t, s, Deposit{usd(0)})
	expectRejected(t, s, Deposit{usd(-1)})
	expectRejected(t, s, OpenAccount{"a1", USD})
	expectRejected(t, EmptyState(), Deposit{usd(1)})
	if _, err := s.Decide(Deposit{OfMinor(100, EUR)}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch: %v", err)
	}
	if s.Version != 3 || len(s.Holds) != 1 {
		t.Fatal("decide mutated state")
	}
}

func TestEventStoreOptimisticConcurrency(t *testing.T) {
	store := NewEventStore()
	if store.Version("x") != 0 {
		t.Fatal("new stream should be at version 0")
	}
	if v, err := store.Append("x", 0, []Event{Opened{"x", USD}, Deposited{usd(1)}}); err != nil || v != 2 {
		t.Fatalf("append: %d %v", v, err)
	}
	_, err := store.Append("x", 1, []Event{Deposited{usd(1)}})
	var ce *ConcurrencyError
	if !errors.As(err, &ce) || ce.Expected != 1 || ce.Actual != 2 {
		t.Fatalf("want ConcurrencyError, got %v", err)
	}
	if store.Version("x") != 2 || store.TotalEvents() != 2 {
		t.Fatal("failed append must write nothing")
	}
	tail := store.Read("x", 1)
	if len(tail) != 1 {
		t.Fatalf("tail = %v", tail)
	}
	if _, ok := tail[0].(Deposited); !ok {
		t.Fatalf("tail[0] = %T", tail[0])
	}
	if len(store.Read("nope", 0)) != 0 {
		t.Fatal("unknown stream should read empty")
	}
}

func newService(t *testing.T) (*EventStore, *SnapshotStore, *AccountService) {
	t.Helper()
	store, snaps := NewEventStore(), NewSnapshotStore()
	svc, err := NewAccountService(store, snaps, 3)
	if err != nil {
		t.Fatal(err)
	}
	must(svc.Handle("acct", OpenAccount{"acct", USD}))
	must(svc.Handle("acct", Deposit{usd(5_000)}))
	return store, snaps, svc
}

func TestSecondWriterLosesAndRetries(t *testing.T) {
	store, _, svc := newService(t)
	seen := store.Version("acct")
	must(svc.HandleAt("acct", Withdraw{usd(1_000)}, seen))
	_, err := svc.HandleAt("acct", Withdraw{usd(1_000)}, seen)
	var ce *ConcurrencyError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConcurrencyError, got %v", err)
	}
	if store.Version("acct") != 3 {
		t.Fatal("loser must write nothing")
	}
	retried := must(svc.Handle("acct", Withdraw{usd(1_000)}))
	if retried.Version != 4 || retried.Balance != usd(3_000) {
		t.Fatalf("retry: %+v", retried)
	}
}

func TestSnapshotsAndTailReplay(t *testing.T) {
	_, snaps, svc := newService(t)
	must(svc.Handle("acct", Withdraw{usd(1_000)}))
	if snap, ok := snaps.Load("acct"); !ok || snaps.Writes() != 1 || snap.Version != 3 {
		t.Fatalf("snapshot at 3: %v %d", ok, snaps.Writes())
	}
	must(svc.Handle("acct", Deposit{usd(1)}))
	loaded := svc.Load("acct")
	if svc.EventsReplayedOnLastLoad() != 1 || loaded.Version != 4 {
		t.Fatalf("replayed %d at version %d", svc.EventsReplayedOnLastLoad(), loaded.Version)
	}
	if !loaded.Equal(svc.ReplayFromScratch("acct")) {
		t.Fatal("snapshot load must equal full replay")
	}
	must(svc.Handle("acct", Deposit{usd(1)}))
	must(svc.Handle("acct", Deposit{usd(1)}))
	if snap, _ := snaps.Load("acct"); snaps.Writes() != 2 || snap.Version != 6 {
		t.Fatalf("snapshot at 6: writes %d version %d", snaps.Writes(), snap.Version)
	}
}

func TestRejectedCommandAppendsNothing(t *testing.T) {
	store, _, svc := newService(t)
	before := store.Version("acct")
	if _, err := svc.Handle("acct", Withdraw{usd(999_999)}); !errors.Is(err, ErrCommandRejected) {
		t.Fatalf("want rejection, got %v", err)
	}
	if store.Version("acct") != before {
		t.Fatal("rejected command must append nothing")
	}
}

func TestSnapshotEveryMustBePositive(t *testing.T) {
	if _, err := NewAccountService(NewEventStore(), NewSnapshotStore(), 0); err == nil {
		t.Fatal("snapshotEvery 0 should be rejected")
	}
}

func TestMoneyOverflowIsAnError(t *testing.T) {
	if _, err := OfMinor(1<<63-1, USD).Plus(usd(1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
	if OfMinor(-7, USD).String() != "-0.07 USD" {
		t.Fatalf("String = %s", OfMinor(-7, USD))
	}
}
