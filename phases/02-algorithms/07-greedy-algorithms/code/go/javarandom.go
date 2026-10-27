package main

import "math"

// JavaRandom is a bit-for-bit port of java.util.Random (the 48-bit linear congruential generator),
// so a seed yields the same sequence as the Java demo and tests. Go's math/rand is a different
// generator and would give different numbers for the same seed.
type JavaRandom struct {
	seed                 int64
	haveNextNextGaussian bool
	nextNextGaussian     float64
}

const (
	javaRandomMultiplier = 0x5DEECE66D
	javaRandomAddend     = 0xB
	javaRandomMask       = (int64(1) << 48) - 1
)

// NewJavaRandom scrambles the seed exactly as java.util.Random(long seed) does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaRandomMultiplier) & javaRandomMask}
}

// next advances the LCG and returns the top `bits` bits as a Java int (so next(32) can be negative).
func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaRandomMultiplier + javaRandomAddend) & javaRandomMask
	return int32(r.seed >> (48 - bits))
}

// NextInt returns a uniformly distributed int32 (Java nextInt()).
func (r *JavaRandom) NextInt() int32 { return r.next(32) }

// NextIntN returns a uniformly distributed value in [0, bound) with Java's rejection loop (nextInt(int bound)).
func (r *JavaRandom) NextIntN(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	if bound&(bound-1) == 0 { // power of two
		return int((int64(bound) * int64(r.next(31))) >> 31)
	}
	for {
		bits := int32(r.next(31))
		val := bits % int32(bound)
		if bits-val+int32(bound-1) >= 0 { // Java's int-overflow rejection test
			return int(val)
		}
	}
}

// NextLong returns a Java long (two next(32) calls).
func (r *JavaRandom) NextLong() int64 { return (int64(r.next(32)) << 32) + int64(r.next(32)) }

// NextBoolean is Java nextBoolean().
func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }

// NextDouble is Java nextDouble(): 53 random bits scaled into [0, 1).
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}

// NextGaussian is Java nextGaussian(): the polar Box-Muller method, caching the second value.
func (r *JavaRandom) NextGaussian() float64 {
	if r.haveNextNextGaussian {
		r.haveNextNextGaussian = false
		return r.nextNextGaussian
	}
	for {
		v1 := 2*r.NextDouble() - 1
		v2 := 2*r.NextDouble() - 1
		s := v1*v1 + v2*v2
		if s < 1 && s != 0 {
			multiplier := math.Sqrt(-2 * math.Log(s) / s)
			r.nextNextGaussian = v2 * multiplier
			r.haveNextNextGaussian = true
			return v1 * multiplier
		}
	}
}

// Shuffle is Collections.shuffle(list, rnd): for i from n-1 down to 1, swap(i, nextInt(i+1)).
func (r *JavaRandom) Shuffle(n int, swap func(i, j int)) {
	for i := n; i > 1; i-- {
		swap(i-1, r.NextIntN(i))
	}
}
