// Project: projects/idempotent-payment-gateway — seeded randomness (Go).
// xorshift64* (Vigna, 2016). The same algorithm and seed produce the same sequence in Java, Python, and Go,
// so a retry schedule computed in one language is byte-for-byte the schedule in the others.
// Never use this for anything security-sensitive; it exists so backoff jitter is replayable in tests.
package main

import "fmt"

// Rng is a xorshift64* generator. The state is uint64 so the shifts are logical, like Java's >>>.
type Rng struct {
	state uint64
}

func NewRng(seed int64) *Rng {
	if seed == 0 {
		return &Rng{state: 0x9E3779B97F4A7C15}
	}
	return &Rng{state: uint64(seed)}
}

// NextLong is the next 64-bit output, as a signed value exactly like Java's long.
func (r *Rng) NextLong() int64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return int64(x * 0x2545F4914F6CDD1D)
}

// NextBelow is a uniform integer in [0, bound), using the unsigned interpretation of NextLong()
// (Long.remainderUnsigned in Java). A non-positive bound is a programming error and panics.
func (r *Rng) NextBelow(bound int64) int64 {
	if bound <= 0 {
		panic(fmt.Sprintf("bound must be positive: %d", bound))
	}
	return int64(uint64(r.NextLong()) % uint64(bound))
}
