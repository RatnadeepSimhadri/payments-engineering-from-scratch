// Tests for phases/08-messaging-and-streams/05-event-sourcing. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkRejected(t *testing.T, name string, s AccountState, c Command) {
	t.Helper()
	if _, err := s.Decide(c); !errors.Is(err, ErrCommandRejected) {
		t.Errorf("%s: got %v, want ErrCommandRejected", name, err)
	}
}

func isConcurrency(err error) bool {
	var ce *ConcurrencyError
	return errors.As(err, &ce)
}

func handle(t *testing.T, svc *AccountService, stream string, c Command) AccountState {
	t.Helper()
	s, err := svc.Handle(stream, c)
	if err != nil {
		t.Fatalf("Handle(%v): %v", c, err)
	}
	return s
}

var history = []Event{
	Opened{"a1", USD}, Deposited{usd(10_000)}, Held{"h1", usd(2_500)}, Withdrawn{usd(6_000)}, Released{"h1"},
}

func TestTheFold(t *testing.T) {
	s := Replay(history)
	check(t, "replay: version equals event count", s.Version == 5)
	check(t, "replay: balance = deposits - withdrawals", s.Balance == usd(4_000))
	check(t, "replay: released hold no longer reduces available", s.Available() == usd(4_000) && len(s.Holds) == 0)
	afterHold := Replay(history[:3])
	check(t, "hold reduces available but not balance", afterHold.Balance == usd(10_000) && afterHold.Available() == usd(7_500))
	check(t, "fold is incremental: replay(all) == replay(prefix).applyAll(suffix)", s.Equal(afterHold.ApplyAll(history[3:5])))
	check(t, "empty state is not open and has version 0", !EmptyState().IsOpen() && EmptyState().Version == 0)
	before := len(afterHold.Holds)
	afterHold.Apply(Held{"h9", usd(1)})
	check(t, "apply copies the holds map instead of sharing it", len(afterHold.Holds) == before)
}

func TestDecideInvariants(t *testing.T) {
	afterHold := Replay(history[:3])
	checkRejected(t, "withdraw beyond available is rejected", afterHold, Withdraw{usd(7_501)})
	events, err := afterHold.Decide(Withdraw{usd(7_500)})
	check(t, "withdraw exactly the available amount is allowed", err == nil && len(events) == 1)
	checkRejected(t, "hold beyond available is rejected", afterHold, PlaceHold{"h2", usd(7_501)})
	checkRejected(t, "duplicate hold id is rejected", afterHold, PlaceHold{"h1", usd(1)})
	checkRejected(t, "release of unknown hold is rejected", afterHold, ReleaseHold{"nope"})
	checkRejected(t, "deposit of zero is rejected", afterHold, Deposit{usd(0)})
	checkRejected(t, "deposit of a negative amount is rejected", afterHold, Deposit{usd(-1)})
	_, err = afterHold.Decide(Deposit{OfMinor(100, EUR)})
	check(t, "currency mismatch is rejected", errors.Is(err, ErrCurrencyMismatch))
	checkRejected(t, "command on an unopened account is rejected", EmptyState(), Deposit{usd(1)})
	checkRejected(t, "opening twice is rejected", afterHold, OpenAccount{"a1", USD})
	check(t, "decide does not mutate state", afterHold.Version == 3 && len(afterHold.Holds) == 1)
}

func TestEventStoreOptimisticConcurrency(t *testing.T) {
	store := NewEventStore()
	check(t, "new stream is at version 0", store.Version("x") == 0)
	v, err := store.Append("x", 0, []Event{Opened{"x", USD}, Deposited{usd(1)}})
	check(t, "append from 0 returns 2", err == nil && v == 2)
	_, err = store.Append("x", 1, []Event{Deposited{usd(1)}})
	check(t, "append with stale expected version fails", isConcurrency(err))
	check(t, "failed append wrote nothing", store.Version("x") == 2 && store.TotalEvents() == 2)
	tail := store.Read("x", 1)
	_, isDeposited := tail[0].(Deposited)
	check(t, "read after version 1 returns the tail", len(tail) == 1 && isDeposited)
	check(t, "read of unknown stream is empty", len(store.Read("nope", 0)) == 0)
}

func TestServiceHandleRetryAndSnapshots(t *testing.T) {
	store := NewEventStore()
	snaps := NewSnapshotStore()
	svc, err := NewAccountService(store, snaps, 3)
	if err != nil {
		t.Fatal(err)
	}
	handle(t, svc, "acct", OpenAccount{"acct", USD})
	handle(t, svc, "acct", Deposit{usd(5_000)})
	seen := store.Version("acct")
	if _, err := svc.HandleAt("acct", Withdraw{usd(1_000)}, seen); err != nil {
		t.Fatal(err)
	}
	_, err = svc.HandleAt("acct", Withdraw{usd(1_000)}, seen)
	check(t, "second writer at the same expected version loses", isConcurrency(err))
	var ce *ConcurrencyError
	errors.As(err, &ce)
	check(t, "concurrency error carries expected and actual versions", ce != nil && ce.Expected == 2 && ce.Actual == 3)
	check(t, "loser wrote no event", store.Version("acct") == 3)
	retried := handle(t, svc, "acct", Withdraw{usd(1_000)})
	check(t, "retry against the fresh version succeeds", retried.Version == 4 && retried.Balance == usd(3_000))
	snap, ok := snaps.Load("acct")
	check(t, "snapshot taken at version 3", snaps.Writes() == 1 && ok && snap.Version == 3)
	loaded := svc.Load("acct")
	check(t, "load replays only the tail after the snapshot", svc.EventsReplayedOnLastLoad() == 1 && loaded.Version == 4)
	check(t, "snapshot load equals full replay", loaded.Equal(svc.ReplayFromScratch("acct")))
	handle(t, svc, "acct", Deposit{usd(1)})
	handle(t, svc, "acct", Deposit{usd(1)})
	snap, ok = snaps.Load("acct")
	check(t, "snapshot taken again at version 6", snaps.Writes() == 2 && ok && snap.Version == 6)
	before := store.Version("acct")
	_, err = svc.Handle("acct", Withdraw{usd(999_999)})
	check(t, "rejected command appends nothing", errors.Is(err, ErrCommandRejected))
	check(t, "version unchanged after rejection", store.Version("acct") == before)
	_, err = NewAccountService(store, snaps, 0)
	check(t, "snapshotEvery must be positive", err != nil)
}

func TestMoneyDiscipline(t *testing.T) {
	_, err := usd(1).Plus(OfMinor(1, EUR))
	check(t, "plus rejects a currency mix", errors.Is(err, ErrCurrencyMismatch))
	check(t, "JPY prints without decimals", OfMinor(1200, JPY).String() == "1200 JPY")
	check(t, "negative USD prints with a sign", usd(-7).String() == "-0.07 USD")
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	check(t, "plus reports overflow", errors.Is(err, ErrOverflow))
}
