// Tests for phases/14-payments-at-scale/10-simulation-testing-of-payment-systems. Run: go test ./...
// Everything is deterministic: no wall clock, no goroutines, no network. A failing check names the invariant it broke.
package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// panics reports whether action panics (the Java throws IllegalArgumentException for these programming errors).
func panics(action func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	action()
	return false
}

var ten = OfMinor(1000, USD)

func TestDeterministicPrimitives(t *testing.T) {
	check(t, "splitmix64 seed 0 first draw matches the reference vector", NewRng(0).NextLong() == 0xe220a8397b1dcdaf)
	r0 := NewRng(0)
	r0.NextLong()
	check(t, "splitmix64 seed 0 second draw matches the reference vector", r0.NextLong() == 0x6e789e6aa1b965f4)
	bounded := NewRng(42)
	inRange := true
	for i := 0; i < 10_000; i++ {
		if x := bounded.NextInt(7); x < 0 || x >= 7 {
			inRange = false
		}
	}
	check(t, "nextInt stays inside [0, bound)", inRange)
	check(t, "chance(0) never fires and chance(10000) always fires", !NewRng(1).Chance(0) && NewRng(1).Chance(10_000))
	check(t, "nextLongBounded handles bounds beyond 32 bits", NewRng(3).NextLongBounded(10_000_000_000) < 10_000_000_000)
	check(t, "nextInt rejects a non-positive bound", panics(func() { NewRng(1).NextInt(0) }))
	s1, s2 := NewRng(7), NewRng(7)
	check(t, "two Rngs with one seed produce the same stream", s1.NextLong() == s2.NextLong() && s1.NextInt(100) == s2.NextInt(100))
	check(t, "split draws once from the parent", func() bool { p := NewRng(9); p.Split(); return p.Draws == 1 }())
}

func TestVirtualClock(t *testing.T) {
	sim := NewSimulator()
	order := []string{}
	sim.Schedule(50, "b", func() { order = append(order, "b") })
	sim.Schedule(10, "a", func() { order = append(order, "a") })
	sim.Schedule(50, "c", func() { order = append(order, "c") })
	check(t, "drain ok", sim.RunUntilIdle(100) == nil)
	check(t, "events run in time order, ties in scheduling order", strings.Join(order, "") == "abc" && sim.Now == 50)
	idle := NewSimulator()
	idle.RunUntil(5_000)
	check(t, "runUntil advances the clock even with nothing scheduled", idle.Now == 5_000)
	check(t, "schedule rejects negative delays", panics(func() { NewSimulator().Schedule(-1, "x", func() {}) }))
	loop := NewSimulator()
	var self func()
	self = func() { loop.Schedule(1, "again", self) }
	loop.Schedule(0, "again", self)
	check(t, "runUntilIdle turns an infinite loop into a failure", errors.Is(loop.RunUntilIdle(1_000), ErrNoQuiescence))
	check(t, "the trace counts every processed event", sim.Trace.Count == 3 && sim.Processed == 3 && sim.Pending() == 0)
}

func TestSimulatedNetwork(t *testing.T) {
	nsim := NewSimulator()
	reliable := NewNetwork(nsim, NewRng(1), NetworkConfig{20, 200, 0})
	got := false
	reliable.Send("client-0", "gateway", "ping", func() { got = true })
	need(0, nsim.RunUntilIdle(10))
	check(t, "a message on a lossless link is delivered within the latency window", got && nsim.Now >= 20 && nsim.Now <= 200)
	reliable.Partition("client-0", "gateway")
	got2 := false
	reliable.Send("gateway", "client-0", "pong", func() { got2 = true })
	need(0, nsim.RunUntilIdle(10))
	check(t, "a partition blocks both directions and counts the message as blocked", !got2 && reliable.Blocked == 1)
	reliable.Heal("client-0", "gateway")
	check(t, "heal removes the partition", !reliable.IsPartitioned("client-0", "gateway") && !reliable.IsPartitioned("gateway", "client-0"))
	lossy := NewNetwork(NewSimulator(), NewRng(5), NetworkConfig{1, 1, 10_000})
	lossy.Send("a", "b", "x", func() {})
	check(t, "a 100% drop rate drops every message", lossy.Dropped == 1 && lossy.Delivered == 0)
}

func TestMoneyAndLedgerDiscipline(t *testing.T) {
	_, err := ten.Plus(OfMinor(1, Currency{"EUR", 2}))
	check(t, "money refuses to mix currencies", errors.Is(err, ErrCurrencyMismatch))
	_, err = Transfer("e1", "a", "b", Zero(USD))
	check(t, "transfer rejects a non-positive amount", errors.Is(err, ErrNonPositiveTransfer))
	ledger := NewLedger(USD)
	check(t, "post ok", ledger.Post(need(Transfer("pay-1", "asset:receivable:c1", "liability:clearing", ten))) == nil)
	check(t, "a transfer leaves the trial balance at zero", ledger.TrialBalance().IsZero() && ledger.Balance("asset:receivable:c1") == ten)
	check(t, "ledger rejects an unbalanced entry", errors.Is(ledger.Post(JournalEntry{"bad", []Posting{{"a", ten}}}), ErrUnbalancedEntry))
	accounts, _ := ledger.Balances()
	check(t, "balances are listed in account order", strings.Join(accounts, ",") == "asset:receivable:c1,liability:clearing")
}

func TestProcessorIdempotencyOnAuthKey(t *testing.T) {
	psim := NewSimulator()
	pnet := NewNetwork(psim, NewRng(2), NetworkConfig{1, 1, 0})
	processor := NewProcessor(psim, pnet, NewRng(3), 0)
	gw := NewGateway(psim, pnet, processor, NoDefect, 3_000, 2_000)
	processor.Receive(AuthRequest{"auth-pay-1", "pay-1", "c1", ten, 1}, gw)
	processor.Receive(AuthRequest{"auth-pay-1", "pay-1", "c1", ten, 2}, gw)
	check(t, "the processor decides once per authKey and replays the retry", len(processor.Approvals) == 1 && processor.Replayed == 1)
}

func TestGatewayIdempotencyOnClientKey(t *testing.T) {
	gsim := NewSimulator()
	gnet := NewNetwork(gsim, NewRng(4), NetworkConfig{1, 1, 0})
	proc2 := NewProcessor(gsim, gnet, NewRng(5), 0)
	gw2 := NewGateway(gsim, gnet, proc2, NoDefect, 3_000, 2_000)
	replies := []Status{}
	gw2.Handle(PaymentRequest{"key-1", "c1", ten}, func(s Status) { replies = append(replies, s) })
	gw2.Handle(PaymentRequest{"key-1", "c1", ten}, func(s Status) { replies = append(replies, s) })
	need(0, gsim.RunUntilIdle(1_000))
	check(t, "one key makes one payment; the duplicate waits and gets the same answer",
		len(gw2.Payments) == 1 && gw2.Replays == 1 && len(replies) == 2 && replies[0] == Approved && replies[1] == Approved)
	check(t, "the approval was posted exactly once", gw2.Ledger.CountEntries("pay-000001") == 1 && gw2.Ledger.TrialBalance().IsZero())
}

func TestCrashWhenBusyFiresInTheDangerousWindow(t *testing.T) {
	csim := NewSimulator()
	cnet := NewNetwork(csim, NewRng(6), NetworkConfig{1, 1, 0})
	proc3 := NewProcessor(csim, cnet, NewRng(7), 0)
	gw3 := NewGateway(csim, cnet, proc3, NoDefect, 3_000, 2_000)
	gw3.CrashWhenBusy()
	check(t, "an armed crash does not fire while nothing is pending", gw3.Crashes == 0)
	proc3.Down = true // the first auth attempt is swallowed
	csim.Schedule(1_000, "processor-up", func() { proc3.Down = false })
	late := []Status{}
	gw3.Handle(PaymentRequest{"key-9", "c9", ten}, func(s Status) { late = append(late, s) })
	check(t, "the armed crash fires once a payment is in flight", gw3.Crashes == 1 && len(late) == 0)
	need(0, csim.RunUntilIdle(1_000))
	check(t, "recovery re-drives the in-flight payment and posts it once",
		gw3.Payments["pay-000001"].Status == Approved && gw3.Recovered == 1 && gw3.Ledger.CountEntries("pay-000001") == 1)
	check(t, "the timer armed before the crash was invalidated by the epoch", gw3.AuthTimeouts == 0 && gw3.AuthSends == 2)
}

func TestFaultSchedule(t *testing.T) {
	schedule := GenerateFaultSchedule(NewRng(11), 3000*Hour, 100)
	sorted, late2, inside := true, false, true
	for i := 1; i < len(schedule.Faults); i++ {
		if schedule.Faults[i-1].StartMs > schedule.Faults[i].StartMs {
			sorted = false
		}
	}
	for _, f := range schedule.Faults {
		if f.StartMs > 1<<31-1 {
			late2 = true
		}
		if f.StartMs < 0 || f.EndMs > 3000*Hour || f.EndMs < f.StartMs {
			inside = false
		}
	}
	check(t, "faults are sorted, inside the horizon, and reach beyond 2^31 ms on long horizons", sorted && late2 && inside)
	check(t, "the schedule has the requested number of faults", len(schedule.Faults) == 100)
}

func TestWholeSystem(t *testing.T) {
	a := Harness.Run(StandardConfig(1, 48))
	b := Harness.Run(StandardConfig(1, 48))
	check(t, "same seed gives the same fingerprint and the same counts", a.Fingerprint == b.Fingerprint && a.Events == b.Events && a.Payments == b.Payments)
	check(t, "a different seed gives a different fingerprint", a.Fingerprint != Harness.Run(StandardConfig(2, 48)).Fingerprint)
	check(t, "the correct gateway survives 48 hours of faults with no violation", a.Passed() && a.Pending == 0 && a.Payments > 500)
	check(t, "the run actually exercised faults", a.Crashes > 0 && a.NetDropped > 0 && a.AuthTimeouts > 0)
	check(t, "trial balance is zero at the end", a.TrialBalance.IsZero())
	check(t, "seed 1 over 48 hours replays the Java trace", a.Fingerprint == "ff8801a677b7abd9" && a.Events == 9435 && a.Payments == 1147)
	fresh, found := Harness.FindFailingSeed(StandardConfig(1, 24).With(FreshAuthKeyOnRetry), 1, 20)
	check(t, "a fresh auth key per retry is caught as a double authorization", found && fresh.FirstBrokenInvariant() == "no-double-authorization")
	skip, found := Harness.FindFailingSeed(StandardConfig(1, 24).With(RecoverySkipsPending), 1, 20)
	check(t, "skipping recovery is caught as a stuck or unposted payment",
		found && (skip.FirstBrokenInvariant() == "no-stuck-payments" || skip.FirstBrokenInvariant() == "approval-posted-once"))
	check(t, "the failing report carries the seed to replay it", found && strings.Contains(skip.Reproduction(), "SimConfig.standard("+strconv.FormatUint(skip.Seed, 10)+", 24)"))
	_, found = Harness.FindFailingSeed(StandardConfig(1, 12), 1, 2)
	check(t, "a passing search returns nothing", !found)
}
