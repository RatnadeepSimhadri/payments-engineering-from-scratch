// Tests for phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog. Run: go test ./...
package main

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func checkInvalid(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("%s: want ErrInvalidArgument, got %v", name, err)
	}
}

func TestHashing(t *testing.T) {
	if Fnv1a64("tok_1") != Fnv1a64("tok_1") {
		t.Error("fnv1a64 is deterministic")
	}
	if Hash64("tok_1") == Hash64("tok_2") {
		t.Error("hash64 separates neighbours")
	}
	if Mix64(0) != 0 {
		t.Error("mix64 is a bijection on zero")
	}
	// Same bits as the Java long 0xcbf29ce484222325L read as unsigned: the empty key is the offset basis.
	if Fnv1a64("") != 0xcbf29ce484222325 {
		t.Errorf("fnv1a64(\"\") = %x", Fnv1a64(""))
	}
}

func TestCountMinSizing(t *testing.T) {
	sized, err := ForErrorBounds(0.01, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if sized.Width != 272 {
		t.Errorf("width = ceil(e / epsilon) = 272, got %d", sized.Width)
	}
	if sized.Depth != 5 {
		t.Errorf("depth = ceil(ln(1/delta)) = 5, got %d", sized.Depth)
	}
	if sized.Bytes() != 272*5*8 {
		t.Errorf("bytes = width * depth * 8, got %d", sized.Bytes())
	}
	_, err = ForErrorBounds(1.5, 0.1)
	checkInvalid(t, "epsilon must be in (0,1)", err)
	_, err = ForErrorBounds(0.1, 0.0)
	checkInvalid(t, "delta must be in (0,1)", err)
	_, err = NewCountMinSketch(0, 3)
	checkInvalid(t, "width must be positive", err)
}

func TestCountMinExactOnSmallInputs(t *testing.T) {
	cms, _ := NewCountMinSketch(1024, 4)
	_ = cms.AddCount("m1", 5)
	_ = cms.AddCount("m2", 3)
	cms.Add("m1")
	if cms.Estimate("m1") != 6 {
		t.Errorf("estimate counts increments: %d", cms.Estimate("m1"))
	}
	if cms.Estimate("m2") != 3 {
		t.Errorf("estimate second key: %d", cms.Estimate("m2"))
	}
	if cms.Total() != 9 {
		t.Errorf("total tracks every add: %d", cms.Total())
	}
	if cms.Estimate("never") != 0 {
		t.Errorf("unseen key with no collisions is zero: %d", cms.Estimate("never"))
	}
	checkInvalid(t, "negative counts rejected", cms.AddCount("m1", -1))
}

func TestCountMinNeverUnderestimates(t *testing.T) {
	rng := NewJavaRandom(7)
	busy, _ := ForErrorBounds(0.005, 0.001)
	truth := map[string]int64{}
	for i := 0; i < 100_000; i++ {
		key := "merchant_" + strconv.Itoa(int(math.Abs(rng.NextGaussian())*300))
		busy.Add(key)
		truth[key]++
	}
	neverUnder := true
	violations := 0
	for key, want := range truth {
		est := busy.Estimate(key)
		if est < want {
			neverUnder = false
		}
		if float64(est) > float64(want)+busy.ErrorBound() {
			violations++
		}
	}
	if !neverUnder {
		t.Error("count-min never underestimates (invariant)")
	}
	if allowed := max(1, int(float64(len(truth))*busy.FailureProbability()*10)); violations > allowed {
		t.Errorf("overestimate within epsilon*N for (almost) every key: %d violations", violations)
	}
	if math.Abs(busy.ErrorBound()-math.E/float64(busy.Width)*100_000) >= 1e-6 {
		t.Error("errorBound is e/width * N")
	}
	if math.Abs(busy.FailureProbability()-math.Exp(-float64(busy.Depth))) >= 1e-12 {
		t.Error("failure probability is e^-depth")
	}
}

func TestCountMinMerge(t *testing.T) {
	rng := NewJavaRandom(7)
	left, _ := NewCountMinSketch(256, 4)
	right, _ := NewCountMinSketch(256, 4)
	both, _ := NewCountMinSketch(256, 4)
	for i := 0; i < 2_000; i++ {
		k := "k" + strconv.Itoa(int(rng.NextIntN(50)))
		left.Add(k)
		both.Add(k)
		j := "k" + strconv.Itoa(int(rng.NextIntN(50)))
		right.Add(j)
		both.Add(j)
	}
	merged, err := left.Merge(right)
	if err != nil {
		t.Fatal(err)
	}
	sameAsBoth := merged.Total() == both.Total()
	for i := 0; i < 50; i++ {
		k := "k" + strconv.Itoa(i)
		sameAsBoth = sameAsBoth && merged.Estimate(k) == both.Estimate(k)
	}
	if !sameAsBoth {
		t.Error("merge equals sketch of the concatenated stream")
	}
	other, _ := NewCountMinSketch(128, 4)
	_, err = left.Merge(other)
	checkInvalid(t, "merge rejects different shapes", err)
}

func TestRhoAndRegisters(t *testing.T) {
	cases := []struct {
		name string
		hash uint64
		want int
	}{
		{"first rest bit set -> 1", 0x0008000000000000, 1},
		{"eighth rest bit set -> 8", 0x0000100000000000, 8},
		{"all rest bits zero -> 65 - p", 0, 53},
		{"ignores the index bits", 0xFFF0000000000000, 53},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Rho(c.hash, 12); got != c.want {
				t.Errorf("rho = %d, want %d", got, c.want)
			}
		})
	}
	_, err := NewHyperLogLog(3)
	checkInvalid(t, "precision below 4 rejected", err)
	_, err = NewHyperLogLog(17)
	checkInvalid(t, "precision above 16 rejected", err)
	if Alpha(16) != 0.673 {
		t.Error("alpha for m=16 is 0.673")
	}
	if math.Abs(Alpha(4096)-0.7213/(1+1.079/4096)) >= 1e-12 {
		t.Error("alpha for large m approaches 0.7213")
	}
}

func TestHyperLogLogCardinality(t *testing.T) {
	empty, _ := NewHyperLogLog(10)
	if empty.Cardinality() != 0 {
		t.Errorf("empty sketch has cardinality 0: %d", empty.Cardinality())
	}
	one, _ := NewHyperLogLog(10)
	one.Add("tok_a")
	one.Add("tok_a")
	one.Add("tok_a")
	if one.Cardinality() != 1 {
		t.Errorf("repeats of one key count once: %d", one.Cardinality())
	}
	small, _ := NewHyperLogLog(12)
	for i := 0; i < 100; i++ {
		small.Add("card_" + strconv.Itoa(i))
	}
	if d := small.Cardinality() - 100; d < -2 || d > 2 {
		t.Errorf("linear counting is near-exact on 100 items: %d", small.Cardinality())
	}

	rng := NewJavaRandom(7)
	hll, _ := NewHyperLogLog(12)
	distinct := map[string]bool{}
	for i := 0; i < 200_000; i++ {
		card := "tok_" + strconv.Itoa(int(rng.NextIntN(60_000)))
		hll.Add(card)
		distinct[card] = true
	}
	relErr := math.Abs(float64(hll.Cardinality())-float64(len(distinct))) / float64(len(distinct))
	if math.Abs(hll.StandardError()-0.01625) >= 1e-9 {
		t.Error("standard error is 1.04/sqrt(m) = 1.625% at p=12")
	}
	if relErr >= 3*hll.StandardError() {
		t.Errorf("estimate within 3 standard errors of the truth: relErr=%f", relErr)
	}
	if hll.Bytes() != 4096 {
		t.Error("sketch uses exactly m bytes")
	}
}

func TestHyperLogLogMergeIsUnion(t *testing.T) {
	a, _ := NewHyperLogLog(12)
	b, _ := NewHyperLogLog(12)
	union, _ := NewHyperLogLog(12)
	for i := 0; i < 20_000; i++ {
		a.Add("c" + strconv.Itoa(i))
		union.Add("c" + strconv.Itoa(i))
	}
	for i := 10_000; i < 30_000; i++ {
		b.Add("c" + strconv.Itoa(i))
		union.Add("c" + strconv.Itoa(i))
	}
	ab, err := a.Merge(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ab.Registers; i++ {
		if ab.RegisterAt(i) != union.RegisterAt(i) {
			t.Fatalf("merged registers equal the union's registers exactly: register %d", i)
		}
	}
	if ab.Cardinality() != union.Cardinality() {
		t.Error("merged cardinality equals the union's")
	}
	ba, _ := b.Merge(a)
	if ba.Cardinality() != ab.Cardinality() {
		t.Error("merge is commutative")
	}
	other, _ := NewHyperLogLog(10)
	_, err = a.Merge(other)
	checkInvalid(t, "merge rejects different precision", err)
}

func TestJavaRandomParity(t *testing.T) {
	// Known values from java.util.Random(42): nextInt(100) sequence and the first nextGaussian.
	rng := NewJavaRandom(42)
	want := []int32{30, 63, 48, 84, 70}
	for i, w := range want {
		if got := rng.NextIntN(100); got != w {
			t.Errorf("nextInt(100)[%d] = %d, want %d", i, got, w)
		}
	}
	g := NewJavaRandom(42)
	if got := g.NextGaussian(); math.Abs(got-1.1419053154730547) > 1e-15 {
		t.Errorf("nextGaussian = %v", got)
	}
}
