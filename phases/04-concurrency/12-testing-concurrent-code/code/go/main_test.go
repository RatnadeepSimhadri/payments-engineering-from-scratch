// Tests for phases/04-concurrency/12-testing-concurrent-code. Run: go test ./...  (and go test -race ./...)
package main

import (
	"errors"
	"reflect"
	"testing"
)

func exhaustive(t *testing.T, p Program, inv Invariant) ExplorationResult {
	t.Helper()
	r, err := Exhaustive(p, inv)
	if err != nil {
		t.Fatalf("Exhaustive: %v", err)
	}
	return r
}

func random(t *testing.T, p Program, inv Invariant, seed int64, n int) ExplorationResult {
	t.Helper()
	r, err := Random(p, inv, seed, n)
	if err != nil {
		t.Fatalf("Random: %v", err)
	}
	return r
}

func step(t *testing.T, m *Machine, threads ...int) {
	t.Helper()
	for _, th := range threads {
		if err := m.Step(th); err != nil {
			t.Fatalf("Step(%d): %v", th, err)
		}
	}
}

func bound(t *testing.T, p Program) int64 {
	t.Helper()
	n, err := p.InterleavingsUpperBound()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMachineStepsOneInstructionAtATime(t *testing.T) {
	racy := RacyIncrement(10_000, 2_500)
	m := NewMachine(racy)
	if !reflect.DeepEqual(m.EnabledThreads(), []int{0, 1}) {
		t.Error("both threads enabled at start")
	}
	step(t, m, 0, 0, 0)
	if !m.Finished(0) || m.Finished(1) {
		t.Error("thread 0 finished after three steps")
	}
	if m.Memory["balance"] != 12_500 {
		t.Error("memory updated by the store")
	}
	if err := m.Step(0); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("stepping a finished thread is an error: %v", err)
	}
	step(t, m, 1, 1, 1)
	if !m.AllFinished() || m.Memory["balance"] != 15_000 {
		t.Error("sequential schedule is correct")
	}
	trace := m.Trace()
	if len(trace) != 6 || trace[0].Thread != "T1" || trace[5].Thread != "T2" {
		t.Error("trace records six steps in order")
	}
	if trace[0].String() != "T1: r = load balance" || trace[2].String() != "T1: store balance = r" {
		t.Errorf("trace steps print like the Java demo: %v", trace)
	}
}

func TestCountingInterleavings(t *testing.T) {
	cases := []struct {
		name string
		p    Program
		want int64
	}{
		{"2 threads x 3 steps = C(6,3) = 20", RacyIncrement(10_000, 2_500), 20},
		{"2 threads x 5 steps = C(10,5) = 252", LockedIncrement(0, 1), 252},
		{"multinomial 4!/(2!1!1!) = 12", NewProgram(map[string]int64{"x": 0}, []ThreadSpec{
			{"A", []Instruction{AtomicAdd{"x", 1}, AtomicAdd{"x", 1}}},
			{"B", []Instruction{AtomicAdd{"x", 1}}},
			{"C", []Instruction{AtomicAdd{"x", 1}}},
		}), 12},
		{"two transfers of 10 steps = C(20,10) = 184756", TransferUnordered(), 184756},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bound(t, c.p); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestExhaustiveFindsLostUpdateAndProvesLock(t *testing.T) {
	racy := RacyIncrement(10_000, 2_500)
	r := exhaustive(t, racy, BalanceEquals(15_000))
	if r.SchedulesExplored != 20 {
		t.Error("explores every interleaving")
	}
	if r.Violations != 18 || r.Deadlocks != 0 {
		t.Error("finds the lost update in 18 of 20 schedules")
	}
	if r.FinalMemory["balance"] != 12_500 {
		t.Error("counterexample ends with balance 12500")
	}
	if len(r.Counterexample) != 6 {
		t.Error("counterexample is a full six-step trace")
	}
	t1Store, t2Load := -1, -1
	for i, s := range r.Counterexample {
		if _, ok := s.Instruction.(Store); ok && s.Thread == "T1" {
			t1Store = i
		}
		if _, ok := s.Instruction.(Load); ok && s.Thread == "T2" {
			t2Load = i
		}
	}
	if !(t2Load < t1Store) {
		t.Error("in the counterexample T2 loads before T1 stores")
	}
	if FormatMemory(r.FinalMemory) != "{balance=12500}" {
		t.Error("final memory prints like Java's map")
	}

	locked := exhaustive(t, LockedIncrement(10_000, 2_500), BalanceEquals(15_000))
	if locked.SchedulesExplored != 2 {
		t.Error("the lock leaves exactly two schedules")
	}
	if !locked.Clean() || locked.Counterexample != nil || locked.FinalMemory != nil {
		t.Error("locked increment never violates")
	}
}

func TestExhaustiveFindsOverdraftDespiteAtomicSubtract(t *testing.T) {
	debit := exhaustive(t, CheckThenActDebit(150, 100), BalanceNonNegative())
	if debit.Violations != 18 {
		t.Error("check-then-act overdraws in 18 of 20 schedules")
	}
	if debit.FinalMemory["balance"] != -50 {
		t.Error("overdraft counterexample ends at -50")
	}
	if !exhaustive(t, CheckThenActDebit(250, 100), BalanceNonNegative()).Clean() {
		t.Error("with enough funds no schedule overdraws")
	}
}

func TestDeadlockDetection(t *testing.T) {
	dl := exhaustive(t, TransferUnordered(), Conserved(200))
	if dl.Deadlocks != 2 || dl.Violations != 0 {
		t.Error("unordered transfer deadlocks in two schedules")
	}
	allLocks := len(dl.Counterexample) == 2
	for _, s := range dl.Counterexample {
		if _, ok := s.Instruction.(Lock); !ok {
			allLocks = false
		}
	}
	if !allLocks {
		t.Error("deadlock trace is the two lock acquisitions")
	}
	ok := exhaustive(t, TransferOrdered(), Conserved(200))
	if ok.SchedulesExplored != 2 || !ok.Clean() {
		t.Error("ordered locks: two schedules, no deadlock, money conserved")
	}

	dead := NewMachine(TransferUnordered())
	step(t, dead, 0, 1)
	if !dead.Deadlocked() || len(dead.EnabledThreads()) != 0 {
		t.Error("machine reports deadlock when nobody is enabled")
	}
	bad := NewMachine(NewProgram(map[string]int64{}, []ThreadSpec{{"X", []Instruction{Unlock{"L"}}}}))
	if err := bad.Step(0); !errors.Is(err, ErrUnlockNoHold) {
		t.Errorf("unlock of a lock you do not hold is an error: %v", err)
	}
}

func TestOverflowIsReported(t *testing.T) {
	p := NewProgram(map[string]int64{"x": 1<<63 - 1}, []ThreadSpec{{"X", []Instruction{AtomicAdd{"x", 1}}}})
	if _, err := Exhaustive(p, BalanceNonNegative()); !errors.Is(err, ErrOverflow) {
		t.Errorf("an int64 overflow in the model is an error, not a wrap: %v", err)
	}
}

func TestRandomExplorationIsSeeded(t *testing.T) {
	racy := RacyIncrement(10_000, 2_500)
	s1 := random(t, racy, BalanceEquals(15_000), 42, 500)
	s2 := random(t, racy, BalanceEquals(15_000), 42, 500)
	if !sameResult(s1, s2) || !reflect.DeepEqual(s1, s2) || s1.SchedulesExplored != 500 {
		t.Error("same seed, same result")
	}
	if s1.Violations <= 300 {
		t.Errorf("random schedules find the bug in most samples: %d", s1.Violations)
	}
	s3 := random(t, racy, BalanceEquals(15_000), 7, 500)
	if s3.Violations == s1.Violations && reflect.DeepEqual(s3.Counterexample, s1.Counterexample) {
		t.Error("different seed, different sample")
	}
	if !random(t, LockedIncrement(0, 1), BalanceEquals(2), 1, 200).Clean() {
		t.Error("random exploration of the locked program is clean")
	}
	// Parity with java.util.Random(42): 1,000 samples find 735 violations in Java, Python, and Go.
	if got := random(t, racy, BalanceEquals(15_000), 42, 1_000).Violations; got != 735 {
		t.Errorf("seed 42 x 1000 schedules: violations = %d, want 735", got)
	}
}

func TestStressHarness(t *testing.T) {
	lockedStress := StressRun(func() BalanceCounter { return &LockedCounter{} }, 4, 5_000, 1, 5)
	if lockedStress.RoundsWithLostUpdates != 0 || lockedStress.ExpectedPerRound != 20_000 {
		t.Error("locked counter is exact in every round")
	}
	if raceDetectorEnabled {
		t.Skip("RacyCounter is a deliberate data race: the race detector would (correctly) fail the run; the model checker above is the proof")
	}
	racyStress := StressRun(func() BalanceCounter { return &RacyCounter{} }, 4, 5_000, 1, 5)
	if racyStress.TotalLost < 0 {
		t.Error("racy counter never gains updates")
	}
	if racyStress.Rounds != 5 {
		t.Error("stress harness reports every round")
	}
}
