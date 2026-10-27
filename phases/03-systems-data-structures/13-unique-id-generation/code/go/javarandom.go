package main

import "math"

// JavaRandom is java.util.Random bit for bit: a 48-bit linear congruential generator, so the same
// seed produces the same stream of ints, longs, doubles, and Gaussians as the Java demo and tests.
type JavaRandom struct {
	seed             int64
	nextNextGaussian float64
	haveNextGaussian bool
}

const (
	javaRandomMultiplier = 0x5DEECE66D
	javaRandomAddend     = 0xB
	javaRandomMask       = (int64(1) << 48) - 1
)

func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaRandomMultiplier) & javaRandomMask}
}

// next returns the top `bits` bits of the next state as a signed 32-bit int, like Java's (int) cast.
func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaRandomMultiplier + javaRandomAddend) & javaRandomMask
	return int32(r.seed >> (48 - bits))
}

func (r *JavaRandom) NextInt() int32 { return r.next(32) }

// NextIntN is Random.nextInt(bound): the power-of-two shortcut and the rejection loop in 32-bit arithmetic.
func (r *JavaRandom) NextIntN(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	u := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(u)) >> 31)
	}
	for {
		v := u % bound
		if u-v+m >= 0 { // int32 wraparound makes this negative on the rejected tail
			return v
		}
		u = r.next(31)
	}
}

func (r *JavaRandom) NextLong() int64 {
	return (int64(r.next(32)) << 32) + int64(r.next(32))
}

func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }

func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}

// NextGaussian is the polar method exactly as Random implements it, including the cached second value.
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

// Shuffle is Collections.shuffle(list, rnd): swap from the back using nextInt(i).
func (r *JavaRandom) Shuffle(n int, swap func(i, j int)) {
	for i := n; i > 1; i-- {
		swap(i-1, int(r.NextIntN(int32(i))))
	}
}
