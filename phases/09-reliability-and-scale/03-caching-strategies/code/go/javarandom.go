package main

// JavaRandom reproduces java.util.Random bit for bit (48-bit LCG) so the seeded workload is
// identical to the Java demo: same accounts, same debits, same hit rates.
type JavaRandom struct{ seed int64 }

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (int64(1) << 48) - 1
)

// NewJavaRandom scrambles the seed exactly as java.util.Random(long) does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaMultiplier) & javaMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bits))
}

// NextInt is Random.nextInt(bound), including the power-of-two fast path and the rejection loop.
func (r *JavaRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	b := int32(bound)
	if b&(-b) == b {
		return int((int64(b) * int64(r.next(31))) >> 31)
	}
	for {
		bits := r.next(31)
		val := bits % b
		if bits-val+(b-1) >= 0 {
			return int(val)
		}
	}
}

// NextDouble is Random.nextDouble(): 53 random bits scaled into [0, 1).
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}
