package main

import "math"

// JavaRandom reproduces java.util.Random (a 48-bit linear congruential generator) so the simulated
// gateway and the exploration coin flips roll the same numbers as the Java demo for the same seed.
type JavaRandom struct {
	seed             int64
	nextNextGaussian float64
	haveNextGaussian bool
}

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (1 << 48) - 1
)

// NewJavaRandom scrambles the seed as java.util.Random(long) does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaMultiplier) & javaMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bits))
}

// NextInt is java.util.Random.nextInt(bound), including the rejection loop.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 {
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

// NextLong is java.util.Random.nextLong(): two 32-bit draws, the first shifted.
func (r *JavaRandom) NextLong() int64 {
	return (int64(r.next(32)) << 32) + int64(r.next(32))
}

// NextDouble is java.util.Random.nextDouble(): 53 random bits scaled by 2^-53.
func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / (1 << 53))
}

// NextBoolean is java.util.Random.nextBoolean().
func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }

// NextGaussian is java.util.Random.nextGaussian(): the polar method with one cached value.
func (r *JavaRandom) NextGaussian() float64 {
	if r.haveNextGaussian {
		r.haveNextGaussian = false
		return r.nextNextGaussian
	}
	for {
		v1 := 2*r.NextDouble() - 1
		v2 := 2*r.NextDouble() - 1
		s := v1*v1 + v2*v2
		if s < 1 && s != 0 {
			multiplier := math.Sqrt(-2 * math.Log(s) / s)
			r.nextNextGaussian = v2 * multiplier
			r.haveNextGaussian = true
			return v1 * multiplier
		}
	}
}
