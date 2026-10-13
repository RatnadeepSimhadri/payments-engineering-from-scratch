// Lesson: phases/00-setup-and-tooling/03-testing-from-the-standard-library/docs/en.md
// Testing from the Standard Library: go test, FakeClock, SeededRandom, and a jittered backoff to test with them
// Run:  go run .
// Test: go test ./...   (and go test -race ./... for the FakeClock concurrency test)
// Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
// Brooker, "Exponential Backoff And Jitter" (AWS Architecture Blog, 2015) for full-jitter backoff.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Clock is a source of time in epoch milliseconds. Production code takes a Clock; tests hand it a FakeClock.
type Clock interface {
	NowMillis() int64
}

// SystemClock is the real clock. It never appears in a test.
type SystemClock struct{}

func (SystemClock) NowMillis() int64 { return time.Now().UnixMilli() }

// ErrClockBackwards is returned when a FakeClock is asked to move into the past.
var ErrClockBackwards = errors.New("fake clock cannot move backwards")

// FakeClock moves only when told to. Thread-safe so code under test may read it from goroutines.
// Invariant: time never goes backwards; Advance rejects negatives and AdvanceTo rejects the past.
type FakeClock struct {
	mu  sync.Mutex
	now int64
}

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }

func (c *FakeClock) NowMillis() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Advance(millis int64) error {
	if millis < 0 {
		return fmt.Errorf("%w: cannot advance by a negative duration %d", ErrClockBackwards, millis)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += millis
	return nil
}

func (c *FakeClock) AdvanceTo(millis int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if millis < c.now {
		return fmt.Errorf("%w: from %d to %d", ErrClockBackwards, c.now, millis)
	}
	c.now = millis
	return nil
}

const goldenGamma uint64 = 0x9E3779B97F4A7C15

// SeededRandom is SplitMix64: a 64-bit generator with a 64-bit state. The same sequence for the same seed in
// Java, Python, and Go, which is why the course uses it instead of each language's own PRNG. Go's uint64
// wraps on overflow, which is exactly the arithmetic the algorithm wants.
// Not for cryptography or anything money-bearing; for simulations, shuffles, and jitter.
type SeededRandom struct {
	seed  int64
	state uint64
}

func NewSeededRandom(seed int64) *SeededRandom { return &SeededRandom{seed: seed, state: uint64(seed)} }

func (r *SeededRandom) Seed() int64 { return r.seed }

func (r *SeededRandom) nextUint64() uint64 {
	r.state += goldenGamma
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// NextInt64 returns the next 64 bits as a signed value, matching Java's long and Python's next_long exactly.
func (r *SeededRandom) NextInt64() int64 { return int64(r.nextUint64()) }

// NextInt is uniform in [0, bound). Uses the top 31 bits; the modulo bias is below 2^-31 for any realistic bound.
func (r *SeededRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic(fmt.Sprintf("bound must be positive: %d", bound))
	}
	return int((r.nextUint64() >> 33) % uint64(bound))
}

// NextFloat64 is uniform in [0.0, 1.0): the top 53 bits scaled by 2^-53, so every float is exactly representable.
func (r *SeededRandom) NextFloat64() float64 { return float64(r.nextUint64()>>11) * (1.0 / (1 << 53)) }

// Shuffle is Fisher-Yates, in place: for i from n-1 down to 1, swap a[i] with a[NextInt(i+1)].
func Shuffle[T any](r *SeededRandom, items []T) {
	for i := len(items) - 1; i > 0; i-- {
		j := r.NextInt(i + 1)
		items[i], items[j] = items[j], items[i]
	}
}

// Deadline is a point in time to compare the clock against. It shows why a fake clock exists: no sleep in the test.
type Deadline struct {
	Clock    Clock
	AtMillis int64
}

func (d Deadline) Expired() bool { return d.Clock.NowMillis() >= d.AtMillis }

func (d Deadline) RemainingMillis() int64 {
	if rem := d.AtMillis - d.Clock.NowMillis(); rem > 0 {
		return rem
	}
	return 0
}

// BackoffMillis is full-jitter exponential backoff: a uniform delay in [0, min(cap, base * 2^attempt)).
// Deterministic given the rng, which is what makes the retry scheduler in Phase 2 testable.
func BackoffMillis(attempt int, baseMillis, capMillis int64, rng *SeededRandom) (int64, error) {
	if attempt < 0 || baseMillis <= 0 || capMillis < baseMillis {
		return 0, errors.New("attempt >= 0, base > 0, cap >= base required")
	}
	shift := attempt
	if shift > 30 { // 2^30 * base already exceeds any sane cap
		shift = 30
	}
	window := baseMillis << shift
	if window > capMillis {
		window = capMillis
	}
	return int64(rng.NextInt(int(window))), nil
}

func main() {
	fmt.Println("== SeededRandom(42): the same five numbers in Java, Python, and Go ==")
	rng := NewSeededRandom(42)
	for i := 0; i < 5; i++ {
		fmt.Println(rng.NextInt64())
	}

	fmt.Println("\n== NextInt(100) x 8 and a shuffle of 0..9 with seed 7 ==")
	rng = NewSeededRandom(42)
	parts := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		parts = append(parts, fmt.Sprint(rng.NextInt(100)))
	}
	fmt.Println(strings.Join(parts, " "))
	deck := make([]int, 10)
	for i := range deck {
		deck[i] = i
	}
	Shuffle(NewSeededRandom(7), deck)
	fmt.Println(deck)

	fmt.Println("\n== FakeClock and a deadline: no time.Sleep anywhere ==")
	clock := NewFakeClock(1_700_000_000_000) // 2023-11-14T22:13:20Z
	deadline := Deadline{clock, clock.NowMillis() + 30_000}
	fmt.Printf("t+0s     expired=%v remaining=%dms\n", deadline.Expired(), deadline.RemainingMillis())
	_ = clock.Advance(29_999)
	fmt.Printf("t+29.999 expired=%v remaining=%dms\n", deadline.Expired(), deadline.RemainingMillis())
	_ = clock.Advance(1)
	fmt.Printf("t+30s    expired=%v remaining=%dms\n", deadline.Expired(), deadline.RemainingMillis())

	fmt.Println("\n== Full-jitter backoff for a PSP call, base 100ms cap 2000ms, seed 42 ==")
	rng = NewSeededRandom(42)
	for attempt := 0; attempt < 5; attempt++ {
		window := int64(100) << attempt
		if window > 2000 {
			window = 2000
		}
		wait, _ := BackoffMillis(attempt, 100, 2000, rng)
		fmt.Printf("attempt %d: window [0, %4d) -> wait %4d ms\n", attempt, window, wait)
	}
	if err := clock.Advance(-1); err != nil {
		fmt.Println("\nrejected:", err)
	}
}
