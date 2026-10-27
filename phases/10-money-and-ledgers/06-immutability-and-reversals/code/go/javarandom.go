// JavaRandom reproduces java.util.Random's 48-bit linear congruential generator, so a seed produces the
// same stream here as in the Java version of this lesson (the demo numbers depend on it).
package main

const (
	javaRandomMultiplier = 0x5DEECE66D
	javaRandomAddend     = 0xB
	javaRandomMask       = (1 << 48) - 1
)

type JavaRandom struct {
	seed int64
}

func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaRandomMultiplier) & javaRandomMask}
}

// next returns the top `bits` bits of the next state as Java's (int) cast would: a signed 32-bit value.
func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaRandomMultiplier + javaRandomAddend) & javaRandomMask
	return int32(r.seed >> (48 - bits))
}

// NextInt is Random.nextInt(bound): power-of-two fast path, otherwise the rejection loop on int overflow.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 { // power of two
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 { // Java: the int sum overflowed to negative -> reject and redraw
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

// NextInt32 is Random.nextInt() with no bound.
func (r *JavaRandom) NextInt32() int32 { return r.next(32) }

// NextLong is Random.nextLong(): two 32-bit draws, high word first.
func (r *JavaRandom) NextLong() int64 {
	return (int64(r.next(32)) << 32) + int64(r.next(32))
}

// NextDouble is Random.nextDouble(): 53 random bits over 2^53.
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}

// NextBoolean is Random.nextBoolean().
func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }
