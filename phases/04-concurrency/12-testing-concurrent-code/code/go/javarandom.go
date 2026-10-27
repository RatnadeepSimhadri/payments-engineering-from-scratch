package main

// JavaRandom is java.util.Random's 48-bit linear congruential generator, ported exactly so that a seed
// samples the same schedules in Java, Python, and Go and every number in the demo matches.
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

// NextInt reproduces Random.nextInt(bound), including the power-of-two shortcut and the rejection loop.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	u := r.Next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(u)) >> 31)
	}
	for {
		v := u % bound
		if u-v+m >= 0 { // the int32 subtraction wraps exactly like Java's int
			return v
		}
		u = r.Next(31)
	}
}
