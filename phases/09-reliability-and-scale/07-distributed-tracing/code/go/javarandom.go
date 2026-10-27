package main

// JavaRandom reproduces java.util.Random bit for bit (48-bit LCG) so the seeded trace and span ids are
// identical to the Java demo: seed 42 yields trace 359d41ba... in every language.
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

// NextBytes is Random.nextBytes: each 32-bit draw is consumed least-significant byte first.
func (r *JavaRandom) NextBytes(buf []byte) {
	for i := 0; i < len(buf); {
		rnd := r.next(32)
		for n := min(len(buf)-i, 4); n > 0; n-- {
			buf[i] = byte(rnd)
			rnd >>= 8
			i++
		}
	}
}
