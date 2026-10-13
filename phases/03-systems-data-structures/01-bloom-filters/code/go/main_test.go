// Tests for phases/03-systems-data-structures/01-bloom-filters. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestFnv1a64KnownVectors(t *testing.T) {
	cases := map[string]uint64{"": 0xcbf29ce484222325, "a": 0xaf63dc4c8601ec8c, "foobar": 0x85944171f73967e8}
	for in, want := range cases {
		if got := Fnv1a64([]byte(in)); got != want {
			t.Errorf("Fnv1a64(%q) = %#x, want %#x", in, got, want)
		}
	}
}

func TestMix64(t *testing.T) {
	if Mix64(0) != 0 {
		t.Errorf("Mix64(0) should be 0")
	}
	if got := Mix64(1); got != 0xb456bcfc34c2cb2c {
		t.Errorf("Mix64(1) = %#x", got)
	}
	seen := make(map[uint64]bool, 10_000)
	for i := uint64(0); i < 10_000; i++ {
		seen[Mix64(i)] = true
	}
	if len(seen) != 10_000 {
		t.Errorf("Mix64 collided on a sample of 10000 inputs")
	}
}

func TestIndexesMatchJavaAndPython(t *testing.T) {
	bf := mustNew(1000, 3)
	if got := bf.Indexes([]byte("idem-key-1")); !reflect.DeepEqual(got, []uint64{994, 570, 146}) {
		t.Errorf("Indexes(idem-key-1) = %v", got)
	}
	if got := bf.Indexes([]byte("card-4111")); !reflect.DeepEqual(got, []uint64{343, 535, 727}) {
		t.Errorf("Indexes(card-4111) = %v", got)
	}
}

func TestIndexesInRange(t *testing.T) {
	bf := mustNew(97, 5)
	for i := 0; i < 500; i++ {
		idx := bf.Indexes([]byte(fmt.Sprintf("key-%d", i)))
		if len(idx) != 5 {
			t.Fatalf("expected 5 indexes, got %d", len(idx))
		}
		for _, x := range idx {
			if x >= 97 {
				t.Fatalf("index %d out of range", x)
			}
		}
	}
}

func TestSizingFormulas(t *testing.T) {
	if m, _ := OptimalBits(10_000, 0.01); m != 95_851 {
		t.Errorf("OptimalBits(10000, 0.01) = %d", m)
	}
	if k := OptimalHashes(95_851, 10_000); k != 7 {
		t.Errorf("OptimalHashes(95851, 10000) = %d", k)
	}
	if m, _ := OptimalBits(1_000, 0.001); m != 14_378 {
		t.Errorf("OptimalBits(1000, 0.001) = %d", m)
	}
	if k := OptimalHashes(14_378, 1_000); k != 10 {
		t.Errorf("OptimalHashes(14378, 1000) = %d", k)
	}
	if p := ExpectedFalsePositiveRate(95_851, 7, 10_000); math.Abs(p-0.01004) > 0.00005 {
		t.Errorf("ExpectedFalsePositiveRate = %f", p)
	}
	if ExpectedFalsePositiveRate(100, 3, 0) != 0 {
		t.Errorf("zero items must give zero rate")
	}
	bf, err := Create(10_000, 0.01)
	if err != nil || bf.M != 95_851 || bf.K != 7 {
		t.Errorf("Create = (%v, %v)", bf, err)
	}
}

func TestRejectsBadParameters(t *testing.T) {
	if _, err := NewBloomFilter(0, 3); err == nil {
		t.Errorf("zero bits accepted")
	}
	if _, err := NewBloomFilter(100, 0); err == nil {
		t.Errorf("zero hashes accepted")
	}
	if _, err := OptimalBits(100, 1.5); err == nil {
		t.Errorf("p outside (0,1) accepted")
	}
}

func TestEmptyFilterContainsNothing(t *testing.T) {
	bf := mustNew(1024, 3)
	if bf.MightContainString("anything") || bf.BitCount() != 0 {
		t.Errorf("empty filter reported a member")
	}
}

func TestNoFalseNegatives(t *testing.T) {
	bf, _ := Create(1_000, 0.01)
	for i := 0; i < 1_000; i++ {
		bf.AddString(fmt.Sprintf("pay_%d", i))
	}
	for i := 0; i < 1_000; i++ {
		if !bf.MightContainString(fmt.Sprintf("pay_%d", i)) {
			t.Fatalf("false negative for pay_%d", i)
		}
	}
}

func TestAddReportsWhetherBitsChanged(t *testing.T) {
	bf := mustNew(4096, 4)
	if !bf.AddString("pay_7f3a") {
		t.Errorf("first add should change bits")
	}
	if bf.AddString("pay_7f3a") {
		t.Errorf("second add of the same key should change nothing")
	}
	if bf.Insertions != 2 || bf.BitCount() != 4 {
		t.Errorf("insertions=%d bits=%d", bf.Insertions, bf.BitCount())
	}
	if !bf.MightContain([]byte("pay_7f3a")) {
		t.Errorf("string and byte keys must agree")
	}
}

func TestMeasuredRateMatchesFormula(t *testing.T) {
	bf, _ := Create(10_000, 0.01)
	measured := MeasureFalsePositiveRate(bf, 10_000, 20_000)
	predicted := bf.EstimatedFalsePositiveRate()
	if measured < 0.5*predicted || measured > 1.5*predicted {
		t.Errorf("measured %f vs predicted %f", measured, predicted)
	}
	if math.Abs(bf.FillRatio()-(1-math.Exp(-7.0*10_000/95_851))) > 0.01 {
		t.Errorf("fill ratio %f off theory", bf.FillRatio())
	}
	if c := bf.ApproximateCount(); c < 9_700 || c > 10_300 {
		t.Errorf("ApproximateCount = %d", c)
	}
	if mustNew(64, 2).ApproximateCount() != 0 {
		t.Errorf("empty filter should estimate 0")
	}
}

func TestOverfillingDestroysTheGuarantee(t *testing.T) {
	bf := mustNew(95_851, 7)
	for i := 0; i < 100_000; i++ {
		bf.AddString(fmt.Sprintf("k-%d", i))
	}
	if bf.EstimatedFalsePositiveRate() < 0.9 || bf.FillRatio() < 0.99 {
		t.Errorf("10x overfill: p=%f fill=%f", bf.EstimatedFalsePositiveRate(), bf.FillRatio())
	}
}

func TestUnion(t *testing.T) {
	a, b := mustNew(4096, 4), mustNew(4096, 4)
	a.AddString("shard-a-key")
	b.AddString("shard-b-key")
	both, err := a.Union(b)
	if err != nil {
		t.Fatal(err)
	}
	if !both.MightContainString("shard-a-key") || !both.MightContainString("shard-b-key") {
		t.Errorf("union lost a key")
	}
	if both.MightContainString("shard-c-key") {
		t.Errorf("union reported an absent key")
	}
	if both.Insertions != 2 {
		t.Errorf("union insertions = %d", both.Insertions)
	}
	if _, err := a.Union(mustNew(2048, 4)); !errors.Is(err, ErrParameterMismatch) {
		t.Errorf("different m accepted: %v", err)
	}
	if _, err := a.Union(mustNew(4096, 3)); !errors.Is(err, ErrParameterMismatch) {
		t.Errorf("different k accepted: %v", err)
	}
}
