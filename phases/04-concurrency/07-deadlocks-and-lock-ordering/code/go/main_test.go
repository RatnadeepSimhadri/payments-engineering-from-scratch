// Tests for phases/04-concurrency/07-deadlocks-and-lock-ordering. Run: go test -race ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func usd(minor int64) Money { return OfMinor(minor, USD) }

func graph(edges ...[2]string) *WaitForGraph {
	g := NewWaitForGraph()
	for _, e := range edges {
		g.AddEdge(e[0], e[1])
	}
	return g
}

func TestTwoNodeCycle(t *testing.T) {
	if got := graph([2]string{"T1", "T2"}, [2]string{"T2", "T1"}).FindCycle(); !reflect.DeepEqual(got, []string{"T1", "T2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestThreeNodeCycleReportedFromSmallestID(t *testing.T) {
	got := graph([2]string{"T2", "T3"}, [2]string{"T3", "T1"}, [2]string{"T1", "T2"}).FindCycle()
	if !reflect.DeepEqual(got, []string{"T1", "T2", "T3"}) {
		t.Fatalf("got %v", got)
	}
}

func TestChainIsNotADeadlock(t *testing.T) {
	if got := graph([2]string{"T1", "T2"}, [2]string{"T2", "T3"}, [2]string{"T4", "T3"}).FindCycle(); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestWaiterBehindTheCycleIsNotOnIt(t *testing.T) {
	got := graph([2]string{"T4", "T1"}, [2]string{"T1", "T2"}, [2]string{"T2", "T1"}).FindCycle()
	if !reflect.DeepEqual(got, []string{"T1", "T2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestDiamondSelfLoopAndEmpty(t *testing.T) {
	if got := graph([2]string{"T1", "T2"}, [2]string{"T1", "T3"}, [2]string{"T2", "T4"}, [2]string{"T3", "T4"}).FindCycle(); got != nil {
		t.Fatalf("diamond: got %v", got)
	}
	if got := graph([2]string{"T1", "T1"}).FindCycle(); !reflect.DeepEqual(got, []string{"T1"}) {
		t.Fatalf("self-loop: got %v", got)
	}
	if got := NewWaitForGraph().FindCycle(); got != nil {
		t.Fatalf("empty: got %v", got)
	}
}

func TestScriptedTwoTransferDeadlock(t *testing.T) {
	tracker := NewLockTracker()
	tracker.Acquired("T1", "lock:A")
	tracker.Acquired("T2", "lock:B")
	if err := tracker.Requesting("T1", "lock:B"); err != nil || tracker.Detect() != nil {
		t.Fatalf("one waiter must not be a cycle: %v %v", err, tracker.Detect())
	}
	err := tracker.Requesting("T2", "lock:A")
	var dd *DeadlockDetectedError
	if !errors.As(err, &dd) || !reflect.DeepEqual(dd.Cycle, []string{"T1", "T2"}) {
		t.Fatalf("expected DeadlockDetectedError [T1 T2], got %v", err)
	}
	if len(tracker.Graph().Successors("T2")) != 0 {
		t.Fatal("refused request must not be left in the graph")
	}
	tracker.Released("T2", "lock:B")
	tracker.Acquired("T1", "lock:B")
	if tracker.Detect() != nil || len(tracker.Graph().Nodes()) != 0 {
		t.Fatal("after release and acquire nothing should wait")
	}
	events := tracker.Events()
	if events[0] != "T1 acquired lock:A" {
		t.Fatalf("events %v", events)
	}
	found := false
	for _, e := range events {
		if e == "T2 aborted: deadlock [T1 T2]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("abort event missing in %v", events)
	}
}

func TestReRequestingAHeldLockIsNotASelfDeadlock(t *testing.T) {
	tracker := NewLockTracker()
	tracker.Acquired("T1", "lock:A")
	if err := tracker.Requesting("T1", "lock:A"); err != nil || tracker.Detect() != nil {
		t.Fatalf("got %v %v", err, tracker.Detect())
	}
}

func TestTwoNaiveTransfersForcedToInterleaveExactlyOneAborted(t *testing.T) {
	tracker := NewLockTracker()
	a := NewAccount("A", USD, 10_000, tracker)
	b := NewAccount("B", USD, 10_000, tracker)
	naive := NewTransferService(NewBarrier(2).Await, NewBackoff(1), 0)
	results := make(chan error, 2)
	go func() { results <- naive.NaiveTransfer(a, b, usd(2_500), "T1") }()
	go func() { results <- naive.NaiveTransfer(b, a, usd(1_000), "T2") }()
	aborted, committed := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			var dd *DeadlockDetectedError
			if errors.As(err, &dd) {
				aborted++
			} else if err == nil {
				committed++
			} else {
				t.Fatalf("unexpected error %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a goroutine hung: real deadlock")
		}
	}
	if aborted != 1 || committed != 1 {
		t.Fatalf("aborted %d committed %d", aborted, committed)
	}
	if a.BalanceMinor()+b.BalanceMinor() != 20_000 {
		t.Fatal("money not conserved")
	}
	ab := [2]int64{a.BalanceMinor(), b.BalanceMinor()}
	if ab != [2]int64{7_500, 12_500} && ab != [2]int64{11_000, 9_000} {
		t.Fatalf("balances %v are not one whole transfer", ab)
	}
	if a.Lock.IsLocked() || b.Lock.IsLocked() {
		t.Fatal("a lock was left held")
	}
}

func contended(t *testing.T, x, y *Account, transfer func(from, to *Account, amount Money, owner string) error, perThread int) {
	t.Helper()
	err := RunAll(8, func(w int) error {
		for i := 0; i < perThread; i++ {
			var err error
			if w%2 == 0 {
				err = transfer(x, y, usd(1), fmt.Sprintf("w%d", w))
			} else {
				err = transfer(y, x, usd(1), fmt.Sprintf("w%d", w))
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if x.BalanceMinor()+y.BalanceMinor() != 200_000 || x.BalanceMinor() != 100_000 {
		t.Fatalf("balances %d %d", x.BalanceMinor(), y.BalanceMinor())
	}
	if x.Lock.IsLocked() || y.Lock.IsLocked() {
		t.Fatal("a lock was left held")
	}
}

func TestOrderedTransfersTerminateAndConserve(t *testing.T) {
	c := NewAccount("C", USD, 100_000, nil)
	d := NewAccount("D", USD, 100_000, nil)
	contended(t, c, d, NewTransferService(runtime.Gosched, NewBackoff(7), 0).OrderedTransfer, 500)
}

func TestOrderedTransferLocksSmallerIDFirst(t *testing.T) {
	tracker := NewLockTracker()
	e := NewAccount("E", USD, 1_000, tracker)
	f := NewAccount("F", USD, 1_000, tracker)
	if err := NewTransferService(nil, NewBackoff(3), 0).OrderedTransfer(f, e, usd(300), "T9"); err != nil {
		t.Fatal(err)
	}
	if tracker.Events()[0] != "T9 requests lock:E" || e.BalanceMinor() != 1_300 || f.BalanceMinor() != 700 {
		t.Fatalf("events %v, balances %d %d", tracker.Events(), e.BalanceMinor(), f.BalanceMinor())
	}
}

func TestTryLockTransfersTerminateAndConserve(t *testing.T) {
	g := NewAccount("G", USD, 100_000, nil)
	h := NewAccount("H", USD, 100_000, nil)
	contended(t, g, h, NewTransferService(runtime.Gosched, NewBackoff(7), 0).TryLockTransfer, 500)
}

func TestTryLockGivesUpAfterMaxAttemptsHoldingNothing(t *testing.T) {
	i := NewAccount("I", USD, 100, nil)
	j := NewAccount("J", USD, 100, nil)
	holding := make(chan struct{})
	release := make(chan struct{})
	var holder sync.WaitGroup
	holder.Add(1)
	go func() { // another goroutine keeps J busy
		defer holder.Done()
		_ = j.Lock.Lock("someone-else")
		close(holding)
		<-release
		j.Lock.Unlock("someone-else")
	}()
	<-holding
	counting := NewBackoff(1)
	err := NewTransferService(nil, counting, 5).TryLockTransfer(i, j, usd(1), "T5")
	if !errors.Is(err, ErrGaveUp) {
		t.Fatalf("expected ErrGaveUp, got %v", err)
	}
	if counting.Pauses() != 5 || i.Lock.IsLocked() {
		t.Fatalf("pauses %d, I locked %v", counting.Pauses(), i.Lock.IsLocked())
	}
	close(release)
	holder.Wait()
}

func TestBusinessRules(t *testing.T) {
	k := NewAccount("K", USD, 100, nil)
	l := NewAccount("L", USD, 100, nil)
	s := NewTransferService(nil, NewBackoff(1), 0)
	if err := s.OrderedTransfer(k, l, usd(101), "T6"); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if k.BalanceMinor() != 100 || l.BalanceMinor() != 100 || k.Lock.IsLocked() || l.Lock.IsLocked() {
		t.Fatal("refused transfer must change nothing and release both locks")
	}
	if err := s.OrderedTransfer(k, l, usd(-1), "T7"); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount, got %v", err)
	}
	if err := s.OrderedTransfer(k, k, usd(1), "T8"); !errors.Is(err, ErrSameAccount) {
		t.Fatalf("expected ErrSameAccount, got %v", err)
	}
	if usd(12_500).String() != "125.00 USD" {
		t.Fatalf("got %v", usd(12_500))
	}
}
