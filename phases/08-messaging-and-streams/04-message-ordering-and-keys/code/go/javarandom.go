package main

// JavaRandom is java.util.Random: the 48-bit linear congruential generator, so a seed yields the same sequence as
// the Java demo. Only the methods this lesson needs are ported.
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

// NextDouble is ((next(26) << 27) + next(27)) * 2^-53.
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}
