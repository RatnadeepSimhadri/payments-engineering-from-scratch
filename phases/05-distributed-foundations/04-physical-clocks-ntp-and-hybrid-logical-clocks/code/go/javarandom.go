package main

// JavaRandom is java.util.Random's 48-bit linear congruential generator plus the JDK 17+ RandomGenerator.nextLong(bound)
// it inherits, ported exactly so the same seed draws the same latencies in Java, Python, and Go.
type JavaRandom struct {
	seed int64
}

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (1 << 48) - 1
)

// NewJavaRandom scrambles the seed the way the JDK constructor does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaMultiplier) & javaMask}
}

// Next returns the top `bits` bits of the next state as a signed 32-bit value, like Random.next(int).
func (r *JavaRandom) Next(bits uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bits))
}

// NextLong is Random.nextLong(): two 32-bit draws, the first shifted left.
func (r *JavaRandom) NextLong() int64 {
	return int64(r.Next(32))<<32 + int64(r.Next(32))
}

// NextLongBounded is RandomGenerator.nextLong(bound): the power-of-two mask shortcut or the rejection loop.
func (r *JavaRandom) NextLongBounded(bound int64) int64 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	m := bound - 1
	v := r.NextLong()
	if bound&m == 0 {
		return v & m
	}
	u := int64(uint64(v) >> 1) // v >>> 1
	for {
		v = u % bound
		if u+m-v >= 0 { // reject over-represented candidates; the int64 addition wraps like Java's long
			return v
		}
		u = int64(uint64(r.NextLong()) >> 1)
	}
}
