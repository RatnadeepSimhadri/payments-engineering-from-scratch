// Tests for phases/00-setup-and-tooling/03-testing-from-the-standard-library. Run: go test ./...  and  go test -race ./...
//
// This file is the lesson's Go harness: Test* functions taking *testing.T, table-driven cases with t.Run,
// a helper marked with t.Helper() so failures point at the caller, and a concurrency test that only means
// something under -race. `go test` compiles this package together with main.go and exits 1 on any failure.
package main

import (
	"errors"
	"math"
	"sort"
	"sync"
	"testing"
)

// mustEqual reports a mismatch at the caller's line, not here, because of t.Helper().
func mustEqual[T comparable](t *testing.T, name string, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", name, want, got)
	}
}

func TestSeededRandomSequenceMatchesJavaAndPython(t *testing.T) {
	rng := NewSeededRandom(42)
	want := []int64{-4767286540954276203, 2949826092126892291, 5139283748462763858, 6349198060258255764, 701532786141963250}
	for _, w := range want {
		mustEqual(t, "seed 42 value", w, rng.NextInt64())
	}
}

func TestPublishedSplitMix64ReferenceVector(t *testing.T) {
	rng := NewSeededRandom(1234567)
	mustEqual(t, "ref #1", int64(6457827717110365317), rng.NextInt64())
	mustEqual(t, "ref #2", int64(3203168211198807973), rng.NextInt64())
}

func TestSeedSemantics(t *testing.T) {
	mustEqual(t, "same seed same sequence", NewSeededRandom(99).NextInt64(), NewSeededRandom(99).NextInt64())
	if NewSeededRandom(1).NextInt64() == NewSeededRandom(2).NextInt64() {
		t.Error("different seeds should differ")
	}
	mustEqual(t, "seed remembered", int64(42), NewSeededRandom(42).Seed())
}

func TestNextInt(t *testing.T) {
	rng := NewSeededRandom(42)
	want := []int{51, 53, 71, 35, 65, 97, 82, 63}
	for _, w := range want {
		mustEqual(t, "NextInt(100)", w, rng.NextInt(100))
	}
	rng = NewSeededRandom(42)
	for i := 0; i < 10_000; i++ {
		if v := rng.NextInt(7); v < 0 || v >= 7 {
			t.Fatalf("NextInt(7) = %d out of range", v)
		}
	}
}

func TestNextIntRejectsNonPositiveBound(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NextInt(0) should panic")
		}
	}()
	NewSeededRandom(1).NextInt(0)
}

func TestNextFloat64(t *testing.T) {
	rng := NewSeededRandom(42)
	if got := rng.NextFloat64(); math.Abs(got-0.7415648787718233) > 1e-15 {
		t.Errorf("first float for seed 42 = %v", got)
	}
	for i := 0; i < 10_000; i++ {
		if d := rng.NextFloat64(); d < 0 || d >= 1 {
			t.Fatalf("NextFloat64 = %v out of [0,1)", d)
		}
	}
}

func TestShuffleDeterministicPermutation(t *testing.T) {
	deck := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	Shuffle(NewSeededRandom(7), deck)
	want := []int{3, 2, 1, 6, 7, 5, 4, 8, 9, 0}
	for i := range want {
		mustEqual(t, "shuffled position", want[i], deck[i])
	}
	sorted := append([]int{}, deck...)
	sort.Ints(sorted)
	for i := range sorted {
		mustEqual(t, "permutation", i, sorted[i])
	}
}

func TestFakeClock(t *testing.T) {
	cases := []struct {
		name string
		step func(c *FakeClock) error
		want int64
	}{
		{"starts where told", func(c *FakeClock) error { return nil }, 1_000},
		{"advance adds", func(c *FakeClock) error { return c.Advance(500) }, 1_500},
		{"advance by zero is allowed", func(c *FakeClock) error { return c.Advance(0) }, 1_500},
		{"advanceTo sets", func(c *FakeClock) error { return c.AdvanceTo(2_000) }, 2_000},
		{"advanceTo same instant is allowed", func(c *FakeClock) error { return c.AdvanceTo(2_000) }, 2_000},
	}
	clock := NewFakeClock(1_000)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.step(clock); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			mustEqual(t, "now", tc.want, clock.NowMillis())
		})
	}
}

func TestFakeClockNeverGoesBackwards(t *testing.T) {
	clock := NewFakeClock(2_000)
	if err := clock.Advance(-1); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("Advance(-1) err = %v", err)
	}
	if err := clock.AdvanceTo(1_999); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("AdvanceTo(1999) err = %v", err)
	}
	mustEqual(t, "rejected moves leave the clock untouched", int64(2_000), clock.NowMillis())
	mustEqual(t, "clocks are independent", int64(5), NewFakeClock(5).NowMillis())
}

// Run with -race: eight goroutines advance a shared clock; without the mutex the detector reports the race
// and, often, the total is short.
func TestFakeClockConcurrentAdvancesAreNotLost(t *testing.T) {
	shared := NewFakeClock(0)
	const goroutines, perGoroutine = 8, 1_000
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = shared.Advance(1)
				_ = shared.NowMillis()
			}
		}()
	}
	wg.Wait()
	mustEqual(t, "total", int64(goroutines*perGoroutine), shared.NowMillis())
}

func TestDeadlineWithoutSleeping(t *testing.T) {
	clock := NewFakeClock(10_000)
	d := Deadline{clock, 10_000 + 30_000}
	if d.Expired() {
		t.Error("expired at start")
	}
	mustEqual(t, "remaining at start", int64(30_000), d.RemainingMillis())
	_ = clock.Advance(29_999)
	if d.Expired() {
		t.Error("expired one ms early")
	}
	_ = clock.Advance(1)
	if !d.Expired() {
		t.Error("should expire exactly at the deadline (>=)")
	}
	_ = clock.Advance(5_000)
	mustEqual(t, "remaining never negative", int64(0), d.RemainingMillis())
}

func TestBackoff(t *testing.T) {
	rng := NewSeededRandom(42)
	want := []int64{51, 153, 171, 735, 365}
	for attempt, w := range want {
		got, err := BackoffMillis(attempt, 100, 2000, rng)
		if err != nil {
			t.Fatal(err)
		}
		mustEqual(t, "backoff seed 42", w, got)
	}
	rng = NewSeededRandom(3)
	for attempt := 0; attempt < 200; attempt++ {
		if got, _ := BackoffMillis(attempt, 100, 2000, rng); got >= 2000 {
			t.Fatalf("attempt %d exceeded cap: %d", attempt, got)
		}
	}
	rng = NewSeededRandom(5)
	for i := 0; i < 1_000; i++ {
		if got, _ := BackoffMillis(0, 100, 2000, rng); got >= 100 {
			t.Fatalf("attempt 0 escaped [0, base): %d", got)
		}
	}
}

func TestBackoffRejectsBadArguments(t *testing.T) {
	if _, err := BackoffMillis(-1, 100, 2000, NewSeededRandom(1)); err == nil {
		t.Error("negative attempt should error")
	}
	if _, err := BackoffMillis(0, 100, 50, NewSeededRandom(1)); err == nil {
		t.Error("cap below base should error")
	}
}
