// Tests for phases/03-systems-data-structures/07-skip-lists. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"testing"
)

func newList[K int, V any](t *testing.T, maxLevel int, p float64, seed int64) *SkipList[K, V] {
	t.Helper()
	l, err := NewSkipList[K, V](maxLevel, p, NewJavaRandom(seed))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestConstruction(t *testing.T) {
	if _, err := NewSkipList[int, string](16, 1.0, NewJavaRandom(1)); !errors.Is(err, ErrInvalidArgument) {
		t.Error("p must be in (0,1)")
	}
	if _, err := NewSkipList[int, string](0, 0.5, NewJavaRandom(1)); !errors.Is(err, ErrInvalidArgument) {
		t.Error("maxLevel must be positive")
	}
	if DefaultMaxLevel(100_000, 0.5) != 17 {
		t.Error("defaultMaxLevel for 100k keys at p=0.5 is 17")
	}
	if DefaultMaxLevel(100_000, 0.25) != 9 {
		t.Error("defaultMaxLevel for 100k keys at p=0.25 is 9")
	}
}

func sampleList(t *testing.T) *SkipList[int, string] {
	list := newList[int, string](t, 16, 0.5, 42)
	for _, k := range []int{742, 118, 905, 367, 512, 288, 674, 59, 831, 456} {
		list.Put(k, "m"+strconv.Itoa(k))
	}
	return list
}

func TestBasicMapBehaviour(t *testing.T) {
	list := newList[int, string](t, 16, 0.5, 42)
	if _, ok := list.Get(5); list.Size() != 0 || list.Level() != 1 || ok {
		t.Error("empty list: size 0, level 1, get absent")
	}
	for _, k := range []int{742, 118, 905, 367, 512, 288, 674, 59, 831, 456} {
		list.Put(k, "m"+strconv.Itoa(k))
	}
	if !slices.Equal(list.Keys(), []int{59, 118, 288, 367, 456, 512, 674, 742, 831, 905}) {
		t.Errorf("keys come out sorted: %v", list.Keys())
	}
	v512, _ := list.Get(512)
	v59, _ := list.Get(59)
	v905, _ := list.Get(905)
	if v512 != "m512" || v59 != "m59" || v905 != "m905" {
		t.Error("get finds present keys")
	}
	_, ok1 := list.Get(1)
	_, ok500 := list.Get(500)
	_, ok1000 := list.Get(1000)
	if ok1 || ok500 || ok1000 {
		t.Error("get misses absent keys, including beyond both ends")
	}
	list.Put(512, "updated")
	if v, _ := list.Get(512); v != "updated" || list.Size() != 10 {
		t.Error("put on an existing key replaces the value without growing")
	}
	if err := list.CheckInvariants(); err != nil {
		t.Errorf("invariants hold after inserts: %v", err)
	}
}

func TestRankAndByRank(t *testing.T) {
	list := sampleList(t)
	if list.Rank(59) != 1 || list.Rank(512) != 6 || list.Rank(905) != 10 {
		t.Error("rank is 1-based position in sorted order")
	}
	if list.Rank(500) != 0 || list.Rank(0) != 0 {
		t.Error("rank of absent key is 0")
	}
	k1, _ := list.ByRank(1)
	k6, _ := list.ByRank(6)
	k10, _ := list.ByRank(10)
	if k1 != 59 || k6 != 512 || k10 != 905 {
		t.Error("byRank inverts rank")
	}
	_, ok0 := list.ByRank(0)
	_, ok11 := list.ByRank(11)
	if ok0 || ok11 {
		t.Error("byRank out of range is absent")
	}
	for r := 1; r <= 10; r++ {
		k, _ := list.ByRank(r)
		if list.Rank(k) != r {
			t.Errorf("rank(byRank(%d)) == %d", r, list.Rank(k))
		}
	}
}

func TestRange(t *testing.T) {
	list := sampleList(t)
	if !slices.Equal(list.Range(300, 700), []int{367, 456, 512, 674}) {
		t.Error("range is inclusive and ordered")
	}
	if !slices.Equal(list.Range(118, 288), []int{118, 288}) {
		t.Error("range with bounds on keys includes them")
	}
	if len(list.Range(900, 904)) != 0 || len(list.Range(1000, 2000)) != 0 {
		t.Error("range with no hits is empty")
	}
}

func TestRemove(t *testing.T) {
	list := sampleList(t)
	removed := list.Remove(512)
	if _, ok := list.Get(512); !removed || ok || list.Size() != 9 {
		t.Error("remove returns true and drops the key")
	}
	if list.Remove(512) || list.Remove(12345) || list.Size() != 9 {
		t.Error("remove of absent key returns false")
	}
	if k, _ := list.ByRank(6); list.Rank(674) != 6 || k != 674 {
		t.Error("ranks shift down after removal")
	}
	if err := list.CheckInvariants(); err != nil {
		t.Errorf("invariants hold after removal: %v", err)
	}
	one := newList[int, string](t, 16, 0.5, 3)
	one.Put(1, "a")
	one.Remove(1)
	if one.Size() != 0 || one.Level() != 1 || len(one.Keys()) != 0 || one.CheckInvariants() != nil {
		t.Error("removing the last key returns to the empty shape")
	}
}

// sortedKeys stands in for java.util.TreeMap's key set.
func sortedKeys(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func TestLargeRandomizedAgainstModel(t *testing.T) {
	n := 50_000
	rng := NewJavaRandom(7)
	big, err := NewSkipList[int, int](DefaultMaxLevel(n, 0.5), 0.5, rng)
	if err != nil {
		t.Fatal(err)
	}
	model := map[int]int{}
	keys := make([]int, n)
	for i := range keys {
		keys[i] = i * 3
	}
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for _, k := range keys {
		big.Put(k, k)
		model[k] = k
	}
	ordered := sortedKeys(model)
	if !slices.Equal(big.Keys(), ordered) || big.Size() != n {
		t.Error("large insert agrees with the sorted model")
	}
	if err := big.CheckInvariants(); err != nil {
		t.Errorf("large insert keeps invariants: %v", err)
	}
	for i := 0; i < 1_000; i++ {
		k := keys[i]
		if headMapSize := sort.SearchInts(ordered, k) + 1; big.Rank(k) != headMapSize { // headMap(k, inclusive).size()
			t.Fatalf("rank agrees with headMap size: rank(%d) = %d, want %d", k, big.Rank(k), headMapSize)
		}
	}
	wantRange := []int{}
	for _, k := range ordered {
		if k >= 3_000 && k <= 3_300 {
			wantRange = append(wantRange, k)
		}
	}
	if !slices.Equal(big.Range(3_000, 3_300), wantRange) {
		t.Error("range agrees with subMap")
	}
	for i := 0; i < n; i += 2 {
		big.Remove(keys[i])
		delete(model, keys[i])
	}
	ordered = sortedKeys(model)
	if !slices.Equal(big.Keys(), ordered) || big.CheckInvariants() != nil {
		t.Error("mixed removes agree with the model")
	}
	first, _ := big.ByRank(1)
	last, _ := big.ByRank(big.Size())
	if first != ordered[0] || last != ordered[len(ordered)-1] {
		t.Error("byRank after removes matches model order")
	}

	// ----- the probabilistic shape -----
	hist := big.LevelHistogram()
	size := float64(big.Size())
	if !(float64(hist[1]) > 0.45*size && float64(hist[1]) < 0.55*size) {
		t.Errorf("level 1 holds about half the nodes: %d", hist[1])
	}
	if !(float64(hist[2]) > 0.20*size && float64(hist[2]) < 0.30*size) {
		t.Errorf("level 2 holds about a quarter: %d", hist[2])
	}
	if big.Level() > big.MaxLevel || big.Level() < 10 {
		t.Error("levels in use are within maxLevel")
	}

	// ----- expected O(log n): measured comparisons vs Pugh -----
	big.SearchSteps = 0
	for i := 0; i < 5_000; i++ {
		big.Get(int(rng.NextIntN(int32(3 * n))))
	}
	avg := float64(big.SearchSteps) / 5_000.0
	expected := ExpectedSteps(big.Size(), 0.5)
	if !(avg < 1.5*expected && avg > 0.5*expected) {
		t.Errorf("average comparisons within 1.5x of log_(1/p)(n)/p: %.1f vs %.1f", avg, expected)
	}

	// sorted input does not degrade: shape depends on coins, not keys
	sortedIn := newList[int, int](t, 17, 0.5, 11)
	for i := 0; i < n; i++ {
		sortedIn.Put(i, i)
	}
	sortedIn.SearchSteps = 0
	for i := 0; i < 5_000; i++ {
		sortedIn.Get(i * 7 % n)
	}
	if float64(sortedIn.SearchSteps)/5_000.0 >= 1.5*ExpectedSteps(n, 0.5) {
		t.Error("sorted insertion order gives the same logarithmic cost")
	}
}

func TestBiasedCoinIsALinkedList(t *testing.T) {
	flat := NewSkipListWithLevelSource[int, int](16, 0.5, func() int { return 1 })
	for i := 0; i < 2_000; i++ {
		flat.Put(i, i)
	}
	flat.SearchSteps = 0
	for i := 0; i < 100; i++ {
		flat.Get(1_999)
	}
	if flat.Level() != 1 || float64(flat.SearchSteps)/100.0 <= 1_900 {
		t.Error("a coin that never promotes yields a linked list: O(n) search")
	}
	if v, _ := flat.Get(1_234); v != 1_234 || flat.Rank(1_234) != 1_235 || flat.CheckInvariants() != nil {
		t.Error("the degenerate list is still a correct map")
	}
}

func TestDeterminism(t *testing.T) {
	a := newList[int, int](t, 16, 0.5, 99)
	b := newList[int, int](t, 16, 0.5, 99)
	for i := 0; i < 1_000; i++ {
		a.Put(i, i)
		b.Put(i, i)
	}
	if !slices.Equal(a.LevelHistogram(), b.LevelHistogram()) {
		t.Error("identical seeds produce identical level histograms")
	}
}
