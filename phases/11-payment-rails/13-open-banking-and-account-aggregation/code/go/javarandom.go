// JavaRandom is java.util.Random, bit for bit (the 48-bit LCG of the JDK), so the Go demo and tests draw the same
// numbers as the Java lesson. Only the methods this lesson uses are ported.

package main

import "math"

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

// NextInt is Random.nextInt(): 32 random bits as a signed int.
func (r *JavaRandom) NextInt() int32 { return r.next(32) }

// NextIntBound is Random.nextInt(bound), including the power-of-two shortcut and the rejection loop.
func (r *JavaRandom) NextIntBound(bound int32) int32 {
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

// NextLong is Random.nextLong(): two 32-bit draws, high word first.
func (r *JavaRandom) NextLong() int64 {
	return int64(r.next(32))<<32 + int64(r.next(32))
}

// NextBoolean is Random.nextBoolean().
func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }

// NextDouble is Random.nextDouble(): 53 random bits scaled into [0, 1).
func (r *JavaRandom) NextDouble() float64 {
	return float64(int64(r.next(26))<<27+int64(r.next(27))) * (1.0 / float64(int64(1)<<53))
}

// NextGaussian is Random.nextGaussian(): the polar method with the cached second value.
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

// NextBytes is Random.nextBytes(): fills the slice four bytes per int draw, least significant byte first.
func (r *JavaRandom) NextBytes(bytes []byte) {
	for i := 0; i < len(bytes); {
		rnd := r.NextInt()
		for n := min(len(bytes)-i, 4); n > 0; n-- {
			bytes[i] = byte(rnd)
			i++
			rnd >>= 8
		}
	}
}

// Shuffle is Collections.shuffle(list, random): a Fisher-Yates pass from the end, same draws as the JDK.
func Shuffle[T any](items []T, r *JavaRandom) {
	for i := len(items); i > 1; i-- {
		j := r.NextIntBound(int32(i))
		items[i-1], items[j] = items[j], items[i-1]
	}
}
