// Tests for phases/01-complexity-and-core-structures/03-dynamic-arrays. Run: go test ./...
package main

import (
	"errors"
	"math/bits"
	"reflect"
	"testing"
)

func get(t *testing.T, a *DynamicArray[int], i int) int {
	t.Helper()
	v, err := a.Get(i)
	if err != nil {
		t.Fatalf("Get(%d): %v", i, err)
	}
	return v
}

// invariantHolds is the structural invariant: size within capacity, live prefix readable.
func invariantHolds(t *testing.T, a *DynamicArray[int], expectedSize int) bool {
	t.Helper()
	if a.Size() != expectedSize || a.Size() > a.Capacity() {
		return false
	}
	for i := 0; i < a.Size(); i++ {
		if _, err := a.Get(i); err != nil {
			return false
		}
	}
	return true
}

func TestGrowthPolicies(t *testing.T) {
	cases := []struct {
		name   string
		policy GrowthPolicy
		from   int
		want   int
	}{
		{"1.5x: 8 -> 12", Grow1_5X, 8, 12},
		{"1.5x: 1 -> 2 (never stalls)", Grow1_5X, 1, 2},
		{"1.5x: 2 -> 3", Grow1_5X, 2, 3},
		{"2x: 8 -> 16", Grow2X, 8, 16},
		{"2x: 1 -> 2", Grow2X, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.policy.Next(c.from); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestAddPathAndBounds(t *testing.T) {
	a := mustArray[int](4, Grow2X, true)
	for i := 0; i < 4; i++ {
		a.Add(i)
	}
	if a.Capacity() != 4 || a.Stats().Reallocations != 0 {
		t.Error("4 adds fill capacity 4 without realloc")
	}
	a.Add(4)
	if a.Capacity() != 8 || a.Size() != 5 {
		t.Error("5th add doubles capacity")
	}
	if a.Stats().ElementsCopied != 4 || a.Stats().Reallocations != 1 {
		t.Error("5th add copied the 4 existing elements")
	}
	if get(t, a, 0) != 0 || get(t, a, 3) != 3 || get(t, a, 4) != 4 {
		t.Error("elements preserved across grow")
	}
	if !invariantHolds(t, a, 5) {
		t.Error("invariant after grow")
	}

	// bounds are checked against size, not capacity
	if _, err := a.Get(5); !errors.Is(err, ErrIndexOutOfBounds) {
		t.Error("get(size) rejected even though slot exists")
	}
	if _, err := a.Get(-1); !errors.Is(err, ErrIndexOutOfBounds) {
		t.Error("get(-1) rejected")
	}
	if _, err := a.Set(7, 1); !errors.Is(err, ErrIndexOutOfBounds) {
		t.Error("set past size rejected")
	}
	if err := a.Insert(6, 1); !errors.Is(err, ErrIndexOutOfBounds) {
		t.Error("insert past size rejected")
	}
	if err := a.Insert(a.Size(), 99); err != nil || get(t, a, 5) != 99 {
		t.Error("insert at size is allowed (append)")
	}
}

func TestInsertRemoveShift(t *testing.T) {
	b := mustArray[int](2, Grow1_5X, false)
	b.Add(10)
	b.Add(30)
	if err := b.Insert(1, 20); err != nil {
		t.Fatal(err)
	}
	if get(t, b, 0) != 10 || get(t, b, 1) != 20 || get(t, b, 2) != 30 || b.Size() != 3 {
		t.Error("insert in the middle shifts tail")
	}
	if b.Capacity() != 3 {
		t.Error("insert into full array grew by 1.5x rule (2 -> 3)")
	}
	if v, err := b.Remove(1); err != nil || v != 20 || get(t, b, 1) != 30 || b.Size() != 2 {
		t.Error("remove middle returns and shifts")
	}
	if old, err := b.Set(0, 11); err != nil || old != 10 || get(t, b, 0) != 11 {
		t.Error("set returns old")
	}
	if v, err := b.RemoveLast(); err != nil || v != 30 || b.Size() != 1 {
		t.Error("removeLast")
	}
	_, _ = b.RemoveLast()
	if _, err := b.RemoveLast(); !errors.Is(err, ErrEmpty) {
		t.Error("removeLast on empty")
	}
}

func TestAmortizedCopies(t *testing.T) {
	n := 100_000
	two := MeasureAppends(Grow2X, n, 8)
	onePointFive := MeasureAppends(Grow1_5X, n, 8)
	checks := []struct {
		name string
		ok   bool
	}{
		{"2x copies fewer than 2n", two.ElementsCopied < 2*int64(n)},
		{"1.5x copies fewer than 3n", onePointFive.ElementsCopied < 3*int64(n)},
		{"1.5x copies more than 2x", onePointFive.ElementsCopied > two.ElementsCopied},
		{"1.5x reallocates more often", onePointFive.Reallocations > two.Reallocations},
		{"1.5x slack is below 50%", float64(onePointFive.PeakCapacity) < 1.5*float64(n)},
		{"2x slack is below 100%", int64(two.PeakCapacity) < 2*int64(n)},
		{"2x peak capacity is a power of two times 8", bits.OnesCount(uint(two.PeakCapacity/8)) == 1},
	}
	for _, c := range checks {
		if !c.ok {
			t.Error(c.name)
		}
	}
	if got := CapacitySequence(Grow1_5X, 8, 40)[:5]; !reflect.DeepEqual(got, []int{8, 12, 18, 27, 40}) {
		t.Errorf("sequence 1.5x from 8: 8,12,18,27,40, got %v", got)
	}
}

func TestShrinkHysteresis(t *testing.T) {
	c := mustArray[int](4, Grow2X, true)
	for i := 0; i < 64; i++ {
		c.Add(i)
	}
	if c.Capacity() != 64 {
		t.Error("capacity 64 after 64 adds")
	}
	for c.Size() > 16 {
		_, _ = c.RemoveLast()
	}
	if c.Capacity() != 64 || c.Stats().Shrinks != 0 {
		t.Error("no shrink at exactly one quarter full")
	}
	_, _ = c.RemoveLast()
	if c.Capacity() != 32 || c.Size() != 15 || c.Stats().Shrinks != 1 {
		t.Error("shrink to half when below one quarter")
	}
	if !invariantHolds(t, c, 15) {
		t.Error("invariant after shrink")
	}
	reallocs := c.Stats().Reallocations
	for i := 0; i < 500; i++ {
		c.Add(i)
		_, _ = c.RemoveLast()
	}
	if c.Stats().Reallocations != reallocs {
		t.Error("add/remove at boundary does not thrash")
	}
	for c.Size() > 0 {
		_, _ = c.RemoveLast()
	}
	if c.Capacity() != MinCapacity {
		t.Error("never shrinks below MinCapacity")
	}
	d := mustArray[int](4, Grow2X, false)
	for i := 0; i < 64; i++ {
		d.Add(i)
	}
	d.Clear()
	if d.Capacity() != 64 || !d.IsEmpty() {
		t.Error("shrink disabled keeps capacity")
	}
}

func TestIterationAndConstruction(t *testing.T) {
	e := mustArray[string](1, Grow1_5X, true)
	e.Add("a")
	e.Add("b")
	e.Add("c")
	joined := ""
	e.Each(func(s string) { joined += s })
	if joined != "abc" {
		t.Errorf("iterator visits live prefix only: %q", joined)
	}
	if _, err := NewDynamicArray[int](0, Grow2X); err == nil {
		t.Error("initial capacity must be positive")
	}
	if e.String() != "[a, b, c] size=3 capacity=3" {
		t.Errorf("String(): %q", e.String())
	}
}
