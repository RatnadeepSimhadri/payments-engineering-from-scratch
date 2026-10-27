package main

// JavaRandom is java.util.Random: the 48-bit linear congruential generator, so a seed yields the same jitter, amounts,
// and stalls as the Java demo. Only the methods this lesson needs are ported.
type JavaRandom struct {
	seed int64
}

const (
	javaRandomMultiplier = 0x5DEECE66D
	javaRandomAddend     = 0xB
	javaRandomMask       = (int64(1) << 48) - 1
)

func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaRandomMultiplier) & javaRandomMask}
}

// next returns the top bits of the next LCG state as a signed 32-bit value, exactly as the JDK's protected next(int).
func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaRandomMultiplier + javaRandomAddend) & javaRandomMask
	return int32(r.seed >> (48 - bits))
}

// NextInt is nextInt(bound): the power-of-two fast path, then the rejection loop. bound must be positive.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	if bound&(-bound) == bound { // power of two
		return int32((int64(bound) * int64(r.next(31))) >> 31)
	}
	var bits, val int32
	for {
		bits = r.next(31)
		val = bits % bound
		if bits-val+(bound-1) >= 0 { // the Java test is "< 0" to detect int overflow; int32 wraps the same way
			return val
		}
	}
}

// NextDouble is ((next(26) << 27) + next(27)) * 2^-53.
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}
