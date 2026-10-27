// Lesson: phases/05-distributed-foundations/12-leases-and-fencing-tokens/docs/en.md
// Leases, Fencing Tokens, and Distributed Locks
// Run:  go run .
// Test: go test ./...   (also: go test -race ./...)
// Sources: Gray & Cheriton, "Leases" (SOSP 1989); Burrows, "The Chubby lock service" (OSDI 2006);
//
//	Kleppmann, "How to do distributed locking" (2016); Redis "Distributed Locks with Redis" (Redlock) page.
//
// Go 1.22+, standard library only. Time is injected through Clock; process pauses are clock.Advance(), never sleep.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Clock is the source of time. The lock service and every client read the same simulated clock in this lesson.
type Clock interface {
	NowMs() int64
}

type FakeClock struct{ now int64 }

func NewFakeClock(startMs int64) *FakeClock { return &FakeClock{startMs} }

func (c *FakeClock) NowMs() int64 { return c.now }

// Advance is a pause: GC stop-the-world, a VM migration, a swapped-out page, or a long network stall.
// Moving time backwards is a programming error and panics.
func (c *FakeClock) Advance(ms int64) {
	if ms < 0 {
		panic(fmt.Sprintf("time only moves forward: %d", ms))
	}
	c.now += ms
}

var (
	// ErrLeaseExpired: a client tried to renew or release a lease it no longer holds.
	ErrLeaseExpired = errors.New("lease expired")
	// ErrStaleToken: fenced storage saw a writer present a token older than one it has already accepted.
	ErrStaleToken = errors.New("stale fencing token")
)

// LeaseExpiredError carries the human-readable reason and matches ErrLeaseExpired under errors.Is.
type LeaseExpiredError struct{ Message string }

func (e *LeaseExpiredError) Error() string        { return e.Message }
func (e *LeaseExpiredError) Is(target error) bool { return target == ErrLeaseExpired }

// StaleTokenError records which token was presented and which the storage had already seen.
type StaleTokenError struct {
	Key         string
	Presented   int64
	HighestSeen int64
}

func (e *StaleTokenError) Error() string {
	return fmt.Sprintf("stale fencing token %d for %s; storage has already seen %d", e.Presented, e.Key, e.HighestSeen)
}

func (e *StaleTokenError) Is(target error) bool { return target == ErrStaleToken }

// Lease is what a client holds after acquiring the lock: who, until when, and a token that is strictly larger than
// every token the lock has ever issued before. The token is the only part a remote resource can trust.
type Lease struct {
	Resource    string
	Owner       string
	Token       int64
	GrantedAtMs int64
	ExpiresAtMs int64
}

// IsExpired is expiry according to the lock service's clock.
func (l Lease) IsExpired(clock Clock) bool { return clock.NowMs() >= l.ExpiresAtMs }

// IsSafeToUse is the client-side question: is it still safe to act? Chubby-style, a client should stop trusting a
// lease a safety margin before its nominal expiry, because its own clock may run slow relative to the service's.
func (l Lease) IsSafeToUse(clock Clock, marginMs int64) bool {
	return clock.NowMs()+marginMs < l.ExpiresAtMs
}

func (l Lease) TTLMs() int64 { return l.ExpiresAtMs - l.GrantedAtMs }

// LeaseLock is a lease-based lock on one named resource. The holder gets exclusive access until the lease expires or
// is released. The lock service is the only place tokens are minted, and it mints them from a counter that never
// goes backwards. A mutex guards the fields: the lock service is shared state even in a simulation.
type LeaseLock struct {
	mu        sync.Mutex
	clock     Clock
	resource  string
	nextToken int64
	current   *Lease // nil when free
	history   []string
}

func NewLeaseLock(clock Clock, resource string) *LeaseLock {
	return &LeaseLock{clock: clock, resource: resource, nextToken: 1}
}

// TryAcquire grants the lock if it is free or the previous lease has expired. Expiry is decided here, by this clock.
// A non-positive ttl is a programming error and panics.
func (l *LeaseLock) TryAcquire(owner string, ttlMs int64) (Lease, bool) {
	if ttlMs <= 0 {
		panic(fmt.Sprintf("ttl must be positive: %d", ttlMs))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.NowMs()
	if l.current != nil && !l.current.IsExpired(l.clock) {
		l.history = append(l.history, fmt.Sprintf("t=%d %s denied (held by %s token %d)", now, owner, l.current.Owner, l.current.Token))
		return Lease{}, false
	}
	if l.current != nil {
		l.history = append(l.history, fmt.Sprintf("t=%d lease of %s (token %d) expired at t=%d", now, l.current.Owner, l.current.Token, l.current.ExpiresAtMs))
	}
	lease := Lease{l.resource, owner, l.nextToken, now, now + ttlMs}
	l.nextToken++
	l.current = &lease
	l.history = append(l.history, fmt.Sprintf("t=%d %s acquired token %d until t=%d", now, owner, lease.Token, lease.ExpiresAtMs))
	return lease, true
}

// Renew extends a lease the caller still holds. Renewing an expired or superseded lease is an error, not a re-acquire.
func (l *LeaseLock) Renew(lease Lease, ttlMs int64) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireHeld(lease, "renew"); err != nil {
		return Lease{}, err
	}
	now := l.clock.NowMs()
	renewed := Lease{l.resource, lease.Owner, lease.Token, lease.GrantedAtMs, now + ttlMs}
	l.current = &renewed
	l.history = append(l.history, fmt.Sprintf("t=%d %s renewed token %d until t=%d", now, lease.Owner, lease.Token, renewed.ExpiresAtMs))
	return renewed, nil
}

func (l *LeaseLock) Release(lease Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireHeld(lease, "release"); err != nil {
		return err
	}
	l.history = append(l.history, fmt.Sprintf("t=%d %s released token %d", l.clock.NowMs(), lease.Owner, lease.Token))
	l.current = nil
	return nil
}

func (l *LeaseLock) requireHeld(lease Lease, action string) error {
	if l.current == nil || l.current.Token != lease.Token || l.current.IsExpired(l.clock) {
		return &LeaseExpiredError{fmt.Sprintf("%s cannot %s: token %d is no longer the live lease at t=%d",
			lease.Owner, action, lease.Token, l.clock.NowMs())}
	}
	return nil
}

// Holder is the live lease, if any.
func (l *LeaseLock) Holder() (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil || l.current.IsExpired(l.clock) {
		return Lease{}, false
	}
	return *l.current, true
}

func (l *LeaseLock) HighestTokenIssued() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextToken - 1
}

func (l *LeaseLock) History() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.history...)
}

// Storage is the protected resource: a key-value store that writers reach after taking the lock.
type Storage interface {
	Read(key string) int64
	Write(key string, value, token int64) error
	WriteLog() []string
}

// UnfencedStorage trusts every writer. The lock is the only protection, and the lock cannot see a paused client.
type UnfencedStorage struct {
	data map[string]int64
	log  []string
}

func NewUnfencedStorage() *UnfencedStorage { return &UnfencedStorage{data: map[string]int64{}} }

func (s *UnfencedStorage) Read(key string) int64 { return s.data[key] }

func (s *UnfencedStorage) Write(key string, value, token int64) error {
	s.data[key] = value
	s.log = append(s.log, fmt.Sprintf("%s=%d (token %d)", key, value, token))
	return nil
}

func (s *UnfencedStorage) WriteLog() []string { return append([]string(nil), s.log...) }

// FencedStorage checks the fencing token on every write and rejects anything older than the newest token it has seen
// for that key. The check and the write are one atomic step at the resource, which is the property the lock alone
// cannot provide.
type FencedStorage struct {
	data         map[string]int64
	highestToken map[string]int64
	log          []string
}

func NewFencedStorage() *FencedStorage {
	return &FencedStorage{data: map[string]int64{}, highestToken: map[string]int64{}}
}

func (s *FencedStorage) Read(key string) int64 { return s.data[key] }

func (s *FencedStorage) Write(key string, value, token int64) error {
	seen := s.highestToken[key]
	if token < seen {
		s.log = append(s.log, fmt.Sprintf("REJECTED %s=%d (token %d < %d)", key, value, token, seen))
		return &StaleTokenError{key, token, seen}
	}
	s.highestToken[key] = token
	s.data[key] = value
	s.log = append(s.log, fmt.Sprintf("%s=%d (token %d)", key, value, token))
	return nil
}

func (s *FencedStorage) HighestTokenSeen(key string) int64 { return s.highestToken[key] }

func (s *FencedStorage) WriteLog() []string { return append([]string(nil), s.log...) }

// Result is how a payout attempt ended.
type Result int

const (
	Applied Result = iota
	LockBusy
	FencedOff
)

func (r Result) String() string { return [...]string{"APPLIED", "LOCK_BUSY", "FENCED_OFF"}[r] }

// PayoutWorker applies a payout: lock, read the pending balance, subtract the payout, write it back.
type PayoutWorker struct {
	Name    string
	clock   *FakeClock
	lock    *LeaseLock
	storage Storage
}

func NewPayoutWorker(name string, clock *FakeClock, lock *LeaseLock, storage Storage) *PayoutWorker {
	return &PayoutWorker{name, clock, lock, storage}
}

// Payout: pauseAfterReadMs stalls between the read and the client-side lease check; pauseAfterCheckMs stalls between
// the check and the write. The second pause is the one a client-side check cannot defend against.
func (w *PayoutWorker) Payout(account string, amountMinor, ttlMs, pauseAfterReadMs, pauseAfterCheckMs int64) Result {
	lease, ok := w.lock.TryAcquire(w.Name, ttlMs)
	if !ok {
		return LockBusy
	}
	balance := w.storage.Read(account)
	w.clock.Advance(pauseAfterReadMs) // the pause nobody planned for
	if !lease.IsSafeToUse(w.clock, 0) {
		return FencedOff // client-side check: necessary, not sufficient
	}
	w.clock.Advance(pauseAfterCheckMs) // the check is already stale by the time the write lands
	if err := w.storage.Write(account, balance-amountMinor, lease.Token); errors.Is(err, ErrStaleToken) {
		return FencedOff
	}
	// The lease may already be gone; then there is nothing to release. The write above is what mattered.
	_ = w.lock.Release(lease)
	return Applied
}

// Report counts outcomes over many replays of the two-worker race.
type Report struct {
	Runs, LostUpdates, FencedOff, BothApplied int
}

// RunRaceStudy replays the two-worker race many times with random pause lengths and counts lost updates. The
// invariant: after both payouts, the balance equals start - a - b exactly when both were applied, and start - (the
// one applied) when one was fenced off. Any other value is a lost update.
func RunRaceStudy(seed int64, runs int, fenced bool) Report {
	rng := NewJavaRandom(seed)
	lost, fencedOff, both := 0, 0, 0
	for i := 0; i < runs; i++ {
		clock := NewFakeClock(0)
		lock := NewLeaseLock(clock, "payout:acct-7")
		var storage Storage
		if fenced {
			storage = NewFencedStorage()
		} else {
			storage = NewUnfencedStorage()
		}
		_ = storage.Write("acct-7", 100_000, 0)
		ttl := int64(1_000)
		pauseA := int64(rng.NextInt(2_000)) // sometimes shorter than the ttl, sometimes longer
		b := NewPayoutWorker("B", clock, lock, storage)

		// A acquires, reads, then stalls. B arrives while A is stalled and retries until the lock frees up.
		// Modelled as: A's pause runs, then B tries, then A's write lands.
		leaseA, _ := lock.TryAcquire("A", ttl)
		balanceA := storage.Read("acct-7")
		clock.Advance(pauseA)
		rb := b.Payout("acct-7", 30_000, ttl, 0, 0)
		ra := Applied
		if err := storage.Write("acct-7", balanceA-20_000, leaseA.Token); errors.Is(err, ErrStaleToken) {
			ra = FencedOff
		}
		expected := int64(100_000)
		if ra == Applied {
			expected -= 20_000
		}
		if rb == Applied {
			expected -= 30_000
		}
		if storage.Read("acct-7") != expected {
			lost++
		}
		if ra == FencedOff {
			fencedOff++
		}
		if ra == Applied && rb == Applied {
			both++
		}
	}
	return Report{runs, lost, fencedOff, both}
}

func mustAcquire(lock *LeaseLock, owner string, ttlMs int64) Lease {
	lease, ok := lock.TryAcquire(owner, ttlMs)
	if !ok {
		panic("lock is held")
	}
	return lease
}

func scenario(title string, storage Storage) {
	fmt.Println("== " + title + " ==")
	clock := NewFakeClock(0)
	lock := NewLeaseLock(clock, "payout:acct-7")
	_ = storage.Write("acct-7", 100_000, 0)
	fmt.Printf("acct-7 starts at %d minor units (1000.00 USD)\n", storage.Read("acct-7"))

	// Worker A takes the lock with a 1 s lease and reads the balance.
	a := mustAcquire(lock, "A", 1_000)
	balanceSeenByA := storage.Read("acct-7")
	fmt.Printf("t=%d A holds token %d, reads %d, plans payout of 20000\n", clock.NowMs(), a.Token, balanceSeenByA)

	// A stalls for 1.5 s: a stop-the-world GC. The lease expires at t=1000 while A is frozen.
	clock.Advance(1_500)
	fmt.Printf("t=%d A has been paused 1500 ms; lease expired at t=%d\n", clock.NowMs(), a.ExpiresAtMs)

	// Worker B acquires the now-free lock, applies its own payout of 30000, releases.
	b := NewPayoutWorker("B", clock, lock, storage)
	rb := b.Payout("acct-7", 30_000, 1_000, 0, 0)
	fmt.Printf("t=%d B: %s, balance now %d (B held token %d)\n", clock.NowMs(), rb, storage.Read("acct-7"), lock.HighestTokenIssued())

	// A wakes up and writes what it computed before the pause, still believing it holds the lock.
	if err := storage.Write("acct-7", balanceSeenByA-20_000, a.Token); err != nil {
		fmt.Printf("t=%d A writes with token %d: %s\n", clock.NowMs(), a.Token, err.Error())
	} else {
		fmt.Printf("t=%d A writes %d with token %d: ACCEPTED\n", clock.NowMs(), balanceSeenByA-20_000, a.Token)
	}
	finalBalance := storage.Read("acct-7")
	correct := int64(100_000 - 20_000 - 30_000)
	fmt.Printf("final balance %d; both payouts applied would be %d; only B applied would be %d\n", finalBalance, correct, 100_000-30_000)
	if finalBalance != correct && finalBalance != 100_000-30_000 {
		fmt.Println("LOST UPDATE: B's payout of 30000 vanished from the balance but the money left the building.")
	} else if finalBalance == 100_000-30_000 {
		fmt.Println("A's stale write was fenced off. A must re-acquire, re-read, and recompute.")
	}
	fmt.Println("storage write log: [" + strings.Join(storage.WriteLog(), ", ") + "]")
	fmt.Println()
}

func main() {
	fmt.Println("== Leases expire on the service's clock; tokens never go backwards ==")
	clock := NewFakeClock(0)
	lock := NewLeaseLock(clock, "demo")
	l1 := mustAcquire(lock, "A", 500)
	fmt.Printf("A acquired token %d, expires t=%d\n", l1.Token, l1.ExpiresAtMs)
	clock.Advance(300)
	verdict := "denied"
	if _, ok := lock.TryAcquire("B", 500); ok {
		verdict = "granted"
	}
	fmt.Println("t=300 B tryAcquire -> " + verdict)
	l1r, err := lock.Renew(l1, 500)
	if err != nil {
		panic(err)
	}
	fmt.Printf("t=300 A renews -> same token %d, expires t=%d\n", l1r.Token, l1r.ExpiresAtMs)
	clock.Advance(900)
	l2 := mustAcquire(lock, "B", 500)
	fmt.Printf("t=1200 B tryAcquire -> granted token %d (A's lease expired at t=800)\n", l2.Token)
	if _, err := lock.Renew(l1r, 500); errors.Is(err, ErrLeaseExpired) {
		fmt.Println("t=1200 A renews -> " + err.Error())
	}
	fmt.Printf("safe-to-use with 100 ms margin at t=1200 for B: %t; at t=1650: %t\n",
		l2.IsSafeToUse(clock, 100), l2.IsSafeToUse(NewFakeClock(1_650), 100))
	fmt.Println()

	scenario("The bug: lock with a timeout, storage that trusts the lock", NewUnfencedStorage())
	scenario("The fix: the same lock, storage that checks the fencing token", NewFencedStorage())

	fmt.Println("== 2000 randomized races, seed 7 ==")
	plain := RunRaceStudy(7, 2_000, false)
	fenced := RunRaceStudy(7, 2_000, true)
	fmt.Printf("unfenced: lost updates=%d fencedOff=%d bothApplied=%d\n", plain.LostUpdates, plain.FencedOff, plain.BothApplied)
	fmt.Printf("fenced  : lost updates=%d fencedOff=%d bothApplied=%d\n", fenced.LostUpdates, fenced.FencedOff, fenced.BothApplied)
	fmt.Println("every run where A's pause outlived its lease is a lost update without fencing and a rejected write with it.")
}
