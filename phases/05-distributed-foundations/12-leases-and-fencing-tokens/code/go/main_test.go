// Tests for phases/05-distributed-foundations/12-leases-and-fencing-tokens. Run: go test -race ./...
package main

import (
	"errors"
	"sync"
	"testing"
)

func acquire(t *testing.T, lock *LeaseLock, owner string, ttl int64) Lease {
	t.Helper()
	lease, ok := lock.TryAcquire(owner, ttl)
	if !ok {
		t.Fatalf("TryAcquire(%s) denied", owner)
	}
	return lease
}

func write(t *testing.T, s Storage, key string, value, token int64) {
	t.Helper()
	if err := s.Write(key, value, token); err != nil {
		t.Fatalf("Write(%s, %d, token %d): %v", key, value, token, err)
	}
}

func TestLeaseMechanics(t *testing.T) {
	clock := NewFakeClock(0)
	lock := NewLeaseLock(clock, "r")
	a := acquire(t, lock, "A", 1_000)
	if a.Token != 1 {
		t.Error("first lease gets token 1")
	}
	if a.ExpiresAtMs != 1_000 || a.TTLMs() != 1_000 {
		t.Error("lease expires at grant + ttl")
	}
	if h, ok := lock.Holder(); !ok || h.Owner != "A" {
		t.Error("holder is A while live")
	}
	if _, ok := lock.TryAcquire("B", 1_000); ok {
		t.Error("second acquirer is denied while lease is live")
	}
	clock.Advance(999)
	if a.IsExpired(clock) {
		t.Error("not expired one ms before expiry")
	}
	clock.Advance(1)
	if !a.IsExpired(clock) {
		t.Error("expired exactly at expiry (>=)")
	}
	if _, ok := lock.Holder(); ok {
		t.Error("holder is empty once expired")
	}
	b := acquire(t, lock, "B", 1_000)
	if b.Token != 2 || b.Token <= a.Token {
		t.Error("next lease gets a strictly larger token")
	}
	var le *LeaseExpiredError
	if _, err := lock.Renew(a, 1_000); !errors.Is(err, ErrLeaseExpired) || !errors.As(err, &le) {
		t.Errorf("renewing an expired lease fails: %v", err)
	}
	if err := lock.Release(a); !errors.Is(err, ErrLeaseExpired) {
		t.Error("releasing a superseded lease fails")
	}
	bRenewed, err := lock.Renew(b, 500)
	if err != nil {
		t.Fatal(err)
	}
	if bRenewed.Token != 2 || bRenewed.ExpiresAtMs != clock.NowMs()+500 {
		t.Error("renew keeps the token and moves expiry")
	}
	if err := lock.Release(bRenewed); err != nil {
		t.Fatal(err)
	}
	if c, ok := lock.TryAcquire("C", 10); !ok || c.Token != 3 {
		t.Error("released lock is free immediately")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("ttl must be positive")
			}
		}()
		lock.TryAcquire("D", 0)
	}()
	if lock.HighestTokenIssued() != 3 {
		t.Error("tokens are monotonic across release and expiry")
	}
	if h := lock.History(); len(h) != 7 || h[1] != "t=0 B denied (held by A token 1)" {
		t.Errorf("history records every decision: %v", h)
	}
}

func TestSafetyMargin(t *testing.T) {
	c2 := NewFakeClock(0)
	m := acquire(t, NewLeaseLock(c2, "m"), "A", 1_000)
	c2.Advance(850)
	if !m.IsSafeToUse(c2, 0) {
		t.Error("safe to use with no margin at 850/1000")
	}
	if m.IsSafeToUse(c2, 200) {
		t.Error("not safe with a 200 ms margin at 850/1000")
	}
}

func TestFencedStorageRejectsStaleTokens(t *testing.T) {
	fs := NewFencedStorage()
	write(t, fs, "k", 1, 5)
	write(t, fs, "k", 2, 5)
	if fs.Read("k") != 2 {
		t.Error("same token may write again (holder's own retries)")
	}
	write(t, fs, "k", 3, 7)
	if fs.Read("k") != 3 || fs.HighestTokenSeen("k") != 7 {
		t.Error("newer token accepted and recorded")
	}
	err := fs.Write("k", 99, 6)
	var stale *StaleTokenError
	if !errors.Is(err, ErrStaleToken) || !errors.As(err, &stale) || stale.Presented != 6 || stale.HighestSeen != 7 {
		t.Errorf("older token rejected with both tokens in the error: %v", err)
	}
	if fs.Read("k") != 3 {
		t.Error("rejected write did not change the value")
	}
	write(t, fs, "other", 1, 1)
	if fs.HighestTokenSeen("other") != 1 || fs.HighestTokenSeen("k") != 7 {
		t.Error("tokens are tracked per key")
	}
	if log := fs.WriteLog(); len(log) != 5 || log[3] != "REJECTED k=99 (token 6 < 7)" {
		t.Errorf("write log records the rejection: %v", log)
	}
}

func TestPausedHolderWritesAfterExpiry(t *testing.T) {
	t.Run("unfenced storage loses the update", func(t *testing.T) {
		c3 := NewFakeClock(0)
		l3 := NewLeaseLock(c3, "payout")
		us := NewUnfencedStorage()
		write(t, us, "acct", 100_000, 0)
		la := acquire(t, l3, "A", 1_000)
		seenByA := us.Read("acct")
		c3.Advance(1_500)
		rb := NewPayoutWorker("B", c3, l3, us).Payout("acct", 30_000, 1_000, 0, 0)
		write(t, us, "acct", seenByA-20_000, la.Token)
		if rb != Applied {
			t.Error("B applied its payout after A's lease expired")
		}
		if us.Read("acct") != 80_000 {
			t.Error("unfenced storage accepts A's stale write and loses B's update")
		}
	})
	t.Run("fenced storage rejects the stale write", func(t *testing.T) {
		c4 := NewFakeClock(0)
		l4 := NewLeaseLock(c4, "payout")
		fs4 := NewFencedStorage()
		write(t, fs4, "acct", 100_000, 0)
		la4 := acquire(t, l4, "A", 1_000)
		seenByA4 := fs4.Read("acct")
		c4.Advance(1_500)
		rb4 := NewPayoutWorker("B", c4, l4, fs4).Payout("acct", 30_000, 1_000, 0, 0)
		if err := fs4.Write("acct", seenByA4-20_000, la4.Token); !errors.Is(err, ErrStaleToken) {
			t.Error("fenced storage rejects A's stale write")
		}
		if rb4 != Applied || fs4.Read("acct") != 70_000 {
			t.Error("balance reflects only B's payout")
		}
	})
}

func TestClientSideCheckIsNecessaryNotSufficient(t *testing.T) {
	c5 := NewFakeClock(0)
	us5 := NewUnfencedStorage()
	write(t, us5, "acct", 100_000, 0)
	r5 := NewPayoutWorker("A", c5, NewLeaseLock(c5, "payout"), us5).Payout("acct", 20_000, 1_000, 1_500, 0)
	if r5 != FencedOff || us5.Read("acct") != 100_000 {
		t.Error("pause before the check: client-side check catches it")
	}
	c6 := NewFakeClock(0)
	us6 := NewUnfencedStorage()
	write(t, us6, "acct", 100_000, 0)
	r6 := NewPayoutWorker("A", c6, NewLeaseLock(c6, "payout"), us6).Payout("acct", 20_000, 1_000, 0, 1_500)
	if r6 != Applied || c6.NowMs() != 1_500 {
		t.Error("pause after the check: the stale write lands anyway on unfenced storage")
	}
	c7 := NewFakeClock(0)
	l7 := NewLeaseLock(c7, "payout")
	fs7 := NewFencedStorage()
	write(t, fs7, "acct", 100_000, 0)
	blocker := acquire(t, l7, "A", 1_000)
	seen7 := fs7.Read("acct")
	c7.Advance(1_500)
	NewPayoutWorker("B", c7, l7, fs7).Payout("acct", 30_000, 1_000, 0, 0)
	if err := fs7.Write("acct", seen7-20_000, blocker.Token); !errors.Is(err, ErrStaleToken) {
		t.Error("pause after the check: fenced storage still rejects")
	}
	c8 := NewFakeClock(0)
	l8 := NewLeaseLock(c8, "payout")
	acquire(t, l8, "holder", 1_000)
	if r := NewPayoutWorker("A", c8, l8, NewUnfencedStorage()).Payout("acct", 1, 1_000, 0, 0); r != LockBusy {
		t.Error("a held lock reports LOCK_BUSY")
	}
	if Applied.String() != "APPLIED" || FencedOff.String() != "FENCED_OFF" {
		t.Error("results print by name")
	}
}

func TestRandomizedRaces(t *testing.T) {
	plain := RunRaceStudy(7, 2_000, false)
	fenced := RunRaceStudy(7, 2_000, true)
	if plain.LostUpdates <= 0 || plain.LostUpdates != plain.BothApplied {
		t.Error("unfenced storage loses updates whenever the pause outlives the lease")
	}
	if fenced.LostUpdates != 0 {
		t.Error("fenced storage never loses an update")
	}
	if fenced.FencedOff != plain.LostUpdates {
		t.Error("fenced storage rejected exactly the runs that would have lost")
	}
	if RunRaceStudy(7, 2_000, true) != fenced {
		t.Error("study is deterministic under the seed")
	}
	// java.util.Random parity with the Java and Python demos.
	if plain.LostUpdates != 995 {
		t.Errorf("seed 7 loses 995 updates in Java; Go lost %d", plain.LostUpdates)
	}
}

func TestLockIsSafeUnderConcurrentAcquirers(t *testing.T) {
	// The lock service is shared state: 50 goroutines race for one lease and exactly one wins.
	clock := NewFakeClock(0)
	lock := NewLeaseLock(clock, "shared")
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := lock.TryAcquire("w", 1_000); ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 || lock.HighestTokenIssued() != 1 {
		t.Errorf("exactly one acquirer wins: winners=%d tokens=%d", winners, lock.HighestTokenIssued())
	}
}
