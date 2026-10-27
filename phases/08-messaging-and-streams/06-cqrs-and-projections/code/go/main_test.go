// Tests for phases/08-messaging-and-streams/06-cqrs-and-projections. Run: go test ./...
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

func checkIs(t *testing.T, name string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: got %v, want %v", name, err, target)
	}
}

func handleOK(t *testing.T, h *CommandHandler, id string, c Command) int64 {
	t.Helper()
	pos, err := h.Handle(id, c)
	if err != nil {
		t.Fatalf("Handle(%s, %v): %v", id, c, err)
	}
	return pos
}

func runOnce(t *testing.T, p *Projector, now int64) int {
	t.Helper()
	n, err := p.RunOnce(now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

func catchUp(t *testing.T, p *Projector, now int64) int {
	t.Helper()
	n, err := p.CatchUp(now)
	if err != nil {
		t.Fatalf("CatchUp: %v", err)
	}
	return n
}

func projector(t *testing.T, store *EventStore, cp *CheckpointStore, p Projection, batch int) *Projector {
	t.Helper()
	pr, err := NewProjector(store, cp, p, batch)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func balance(v *BalanceView, id string) Money {
	m, _ := v.Balance(id)
	return m
}

// seeded builds a store with two accounts and nine events, the same shape as the demo.
func seeded(t *testing.T, clock *SimClock) *EventStore {
	t.Helper()
	store := NewEventStore()
	h := NewCommandHandler(store, clock)
	handleOK(t, h, "a", OpenAccount{"a", "Alice", USD})
	handleOK(t, h, "b", OpenAccount{"b", "Bob", USD})
	handleOK(t, h, "a", Deposit{usd(250_000), "d1"})
	handleOK(t, h, "b", Deposit{usd(80_000), "d2"})
	clock.Advance(86_400_000)
	handleOK(t, h, "a", Withdraw{usd(120_000), "w1"})
	handleOK(t, h, "b", Withdraw{usd(60_000), "w2"})
	handleOK(t, h, "a", Withdraw{usd(75_000), "w3"})
	handleOK(t, h, "b", Deposit{usd(15_000), "d3"})
	handleOK(t, h, "a", Deposit{usd(5_000), "d4"})
	return store
}

func TestWriteSide(t *testing.T) {
	clock := NewSimClock(1_700_000_000_000)
	store := seeded(t, clock)
	handler := NewCommandHandler(store, clock)
	first, _ := store.ReadAll(0, 1)
	check(t, "global positions are dense and 1-based", store.LastPosition() == 9 && first[0].Position == 1)
	check(t, "stream versions are independent of global position", store.Version("a") == 5 && store.Version("b") == 4)
	check(t, "stream read returns only that stream in version order", len(store.ReadStream("a")) == 5 && store.ReadStream("a")[4].Version == 5)
	_, err := handler.Handle("b", Withdraw{usd(999_999), "x"})
	checkIs(t, "overdraft rejected by the write model", err, ErrCommandRejected)
	_, err = handler.Handle("zz", Deposit{usd(1), "x"})
	checkIs(t, "deposit to unknown account rejected", err, ErrCommandRejected)
	_, err = handler.Handle("a", OpenAccount{"a", "A", USD})
	checkIs(t, "reopening an account rejected", err, ErrCommandRejected)
	_, err = handler.Handle("a", Deposit{usd(0), "x"})
	checkIs(t, "non-positive deposit rejected", err, ErrCommandRejected)
	_, err = store.Append("a", 2, []Event{Deposited{usd(1), "x"}}, clock.Now())
	checkIs(t, "stale expected version is a concurrency error", err, ErrConcurrency)
	check(t, "rejected commands append nothing", store.LastPosition() == 9)
	_, err = store.ReadAll(10, 1)
	check(t, "readAll beyond the log is an error", err != nil)
	end, _ := store.ReadAll(9, 5)
	check(t, "readAll at the end is empty", len(end) == 0)
	four, _ := store.ReadAll(0, 4)
	check(t, "readAll respects max", len(four) == 4 && four[3].Position == 4)
}

func TestProjectionsFoldTheLog(t *testing.T) {
	clock := NewSimClock(1_700_000_000_000)
	store := seeded(t, clock)
	checkpoints := NewCheckpointStore()
	balances := NewIdempotentBalanceView()
	balanceProjector := projector(t, store, checkpoints, balances, 3)
	check(t, "projector starts at position 0 with full lag", balanceProjector.Position() == 0 && balanceProjector.Lag() == 9)
	check(t, "one batch applies batchSize events and checkpoints", runOnce(t, balanceProjector, clock.Now()) == 3 && balanceProjector.Position() == 3)
	check(t, "catchUp drains the rest", catchUp(t, balanceProjector, clock.Now()) == 6 && balanceProjector.CaughtUp())
	check(t, "balances match the fold of the log", balance(balances, "a") == usd(60_000) && balance(balances, "b") == usd(35_000))
	check(t, "read model carries data the write model does not keep (owner)", balances.Owner("a") == "Alice")

	daily := NewDailyActivityView(USD)
	catchUp(t, projector(t, store, checkpoints, daily, 100), clock.Now())
	day0 := DayOf(1_700_000_000_000)
	d0, _ := daily.Day(day0)
	d1, _ := daily.Day(day0 + 1)
	check(t, "daily view groups by recordedAt day", d0.Deposits == 2 && d0.Withdrawals == 0 && d1.Withdrawals == 3)
	check(t, "daily net is money arithmetic, not floats", d1.Net == usd(-235_000))

	v1 := NewLargeWithdrawalsView(1, usd(100_000))
	catchUp(t, projector(t, store, checkpoints, v1, 100), clock.Now())
	check(t, "v1 alerts only at or above 1000.00 and has no owner", len(v1.Alerts()) == 1 && v1.Alerts()[0].Owner == "")
}

func TestVersioningAndPromotion(t *testing.T) {
	clock := NewSimClock(1_700_000_000_000)
	store := seeded(t, clock)
	checkpoints := NewCheckpointStore()
	v1 := NewLargeWithdrawalsView(1, usd(100_000))
	readSide := NewReadSide()
	liveLarge := projector(t, store, checkpoints, v1, 100)
	catchUp(t, liveLarge, clock.Now())
	readSide.Register(liveLarge)
	v2 := projector(t, store, checkpoints, NewLargeWithdrawalsView(2, usd(50_000)), 4)
	readSide.Register(v2)
	check(t, "v2 has its own checkpoint starting at 0", v2.Position() == 0 && readSide.Live("large-withdrawals") == liveLarge)
	runOnce(t, v2, clock.Now())
	_, err := readSide.Promote("large-withdrawals")
	checkIs(t, "promotion refused while behind", err, ErrNotCaughtUp)
	catchUp(t, v2, clock.Now())
	retired, err := readSide.Promote("large-withdrawals")
	check(t, "promotion swaps live and returns the retired projector", err == nil && readSide.Live("large-withdrawals") == v2 && retired == liveLarge)
	v2View := v2.Projection().(*LargeWithdrawalsView)
	check(t, "v2 sees three alerts with owners", len(v2View.Alerts()) == 3 && v2View.Alerts()[1].Owner == "Bob")
	check(t, "retired version's checkpoint is untouched", retired.Position() == 9)
	_, err = readSide.Promote("large-withdrawals")
	checkIs(t, "promote with nothing building", err, ErrNothingBuilding)
	checkIs(t, "checkpoint never moves backwards", checkpoints.Save(v1, 1), ErrCheckpointBackwards)
}

func TestEventualConsistencyAndReadYourWrites(t *testing.T) {
	clock := NewSimClock(1_700_000_000_000)
	store := seeded(t, clock)
	handler := NewCommandHandler(store, clock)
	balances := NewIdempotentBalanceView()
	balanceProjector := projector(t, store, NewCheckpointStore(), balances, 3)
	catchUp(t, balanceProjector, clock.Now())
	readSide := NewReadSide()
	readSide.Register(balanceProjector)
	pos := handleOK(t, handler, "a", Deposit{usd(1_000), "late"})
	check(t, "a new write leaves the projection behind", balanceProjector.Lag() == 1 && balance(balances, "a") == usd(60_000))
	ok, err := readSide.WaitFor("balances", pos, clock.Now(), 5)
	check(t, "waitFor catches the projection up to the write", err == nil && ok && balance(balances, "a") == usd(61_000))
	ok, _ = readSide.WaitFor("balances", pos+1, clock.Now(), 0)
	check(t, "waitFor with zero runs reports not caught up when behind", !ok)
}

func TestCrashBetweenApplyAndCheckpoint(t *testing.T) {
	clock := NewSimClock(1_700_000_000_000)
	store := seeded(t, clock)
	handleOK(t, NewCommandHandler(store, clock), "a", Deposit{usd(1_000), "late"})

	naive := NewBalanceView()
	naiveProjector := projector(t, store, NewCheckpointStore(), naive, 4)
	runOnce(t, naiveProjector, clock.Now())
	naiveProjector.CrashAfterApplies(2)
	_, err := naiveProjector.RunOnce(clock.Now())
	checkIs(t, "crash is surfaced", err, ErrCrash)
	check(t, "crash left the checkpoint where it was", naiveProjector.Position() == 4)
	catchUp(t, naiveProjector, clock.Now())
	check(t, "naive projection double-applies redelivered withdrawals", balance(naive, "a") == usd(61_000-120_000))

	safe := NewIdempotentBalanceView()
	safeProjector := projector(t, store, NewCheckpointStore(), safe, 4)
	runOnce(t, safeProjector, clock.Now())
	safeProjector.CrashAfterApplies(2)
	safeProjector.RunOnce(clock.Now())
	catchUp(t, safeProjector, clock.Now())
	check(t, "idempotent projection ignores redelivery", balance(safe, "a") == usd(61_000) && safe.Skipped() == 2)
	_, err = NewProjector(store, NewCheckpointStore(), naive, 0)
	check(t, "batchSize must be positive", err != nil)
}

func TestConsistencyWindow(t *testing.T) {
	cases := []struct {
		name       string
		poll       int64
		batch      int
		wantEvents int
	}{{"poll 50", 50, 100, 100}, {"poll 10", 10, 100, 100}}
	windows := map[string]Window{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Probe(100, 7, c.poll, c.batch)
			windows[c.name] = w
			check(t, "probe covers every event", w.Events == c.wantEvents)
			check(t, "staleness is bounded by the poll interval when the projector keeps up", w.Max <= c.poll)
		})
	}
	check(t, "shorter polling shrinks the window", windows["poll 10"].Mean < windows["poll 50"].Mean)
	overloaded := Probe(100, 7, 10, 1)
	check(t, "a projector slower than the producer falls behind without bound", overloaded.Max > 100)
}
