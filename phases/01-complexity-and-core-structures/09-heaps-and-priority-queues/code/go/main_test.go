// Tests for phases/01-complexity-and-core-structures/09-heaps-and-priority-queues. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"testing"
)

func drain(t *testing.T, h *MinHeap[int]) []int {
	t.Helper()
	var out []int
	for !h.IsEmpty() {
		v, err := h.Pop()
		if err != nil {
			t.Fatalf("Pop on non-empty heap: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestEmptyHeap(t *testing.T) {
	h := NewMinHeap(NaturalOrder)
	if h.Size() != 0 || !h.IsEmpty() {
		t.Fatal("new heap must be empty")
	}
	if _, err := h.Peek(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Peek on empty: err = %v, want ErrEmpty", err)
	}
	if _, err := h.Pop(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Pop on empty: err = %v, want ErrEmpty", err)
	}
}

func TestPushPeekPop(t *testing.T) {
	h := NewMinHeap(NaturalOrder)
	for _, v := range []int{5, 3, 8, 1} {
		h.Push(v)
	}
	if top, _ := h.Peek(); top != 1 {
		t.Errorf("Peek = %d, want 1", top)
	}
	if !h.IsHeap() {
		t.Error("heap property violated after pushes")
	}
	if got := drain(t, h); !slices.Equal(got, []int{1, 3, 5, 8}) {
		t.Errorf("pop order = %v", got)
	}
}

func TestDuplicatesAllowed(t *testing.T) {
	h := NewMinHeap(NaturalOrder)
	h.Push(7)
	h.Push(7)
	h.Push(7)
	if v, _ := h.Pop(); h.Size() != 2 || v != 7 {
		t.Errorf("duplicates mishandled: popped %d, size %d", v, h.Size())
	}
}

func TestHeapifyBuildsValidHeapAndPopsSorted(t *testing.T) {
	raw := []int{9, 4, 7, 1, 8, 2, 6, 3, 5, 0, 11, 10}
	built := Heapify(raw, NaturalOrder)
	if built.Size() != len(raw) || !built.IsHeap() {
		t.Fatalf("heapify: size=%d isHeap=%v", built.Size(), built.IsHeap())
	}
	want := slices.Clone(raw)
	slices.Sort(want)
	if got := drain(t, built); !slices.Equal(got, want) {
		t.Errorf("drained = %v, want %v", got, want)
	}
	if raw[0] != 9 {
		t.Error("Heapify must copy, not reorder the caller's slice")
	}
}

func TestHeapifyEdgeCases(t *testing.T) {
	if !Heapify([]int{}, NaturalOrder).IsEmpty() {
		t.Error("heapify of empty must be empty")
	}
	if v, _ := Heapify([]int{42}, NaturalOrder).Peek(); v != 42 {
		t.Errorf("heapify of one element: peek = %d", v)
	}
}

func TestComparatorControlsOrder(t *testing.T) {
	rev := Heapify([]int{9, 4, 7, 1, 8, 2, 6, 3, 5, 0, 11, 10}, ReverseOrder)
	if v, _ := rev.Peek(); v != 11 || !rev.IsHeap() {
		t.Errorf("reverse order: peek = %d isHeap=%v", v, rev.IsHeap())
	}
}

func TestSeededRandomOperationsKeepHeapProperty(t *testing.T) {
	rng := NewSeededRandom(42)
	stress := NewMinHeap(NaturalOrder)
	var shadow []int
	for i := 0; i < 2000; i++ {
		if stress.IsEmpty() || rng.NextInt(3) != 0 {
			v := rng.NextInt(500)
			stress.Push(v)
			shadow = append(shadow, v)
		} else {
			popped, _ := stress.Pop()
			min := slices.Min(shadow)
			if popped != min {
				t.Fatalf("op %d: popped %d but shadow min is %d", i, popped, min)
			}
			shadow = slices.Delete(shadow, slices.Index(shadow, min), slices.Index(shadow, min)+1)
		}
		if !stress.IsHeap() {
			t.Fatalf("heap property violated after op %d", i)
		}
	}
	if stress.Size() != len(shadow) {
		t.Errorf("size %d != shadow %d", stress.Size(), len(shadow))
	}
}

func popKeys(t *testing.T, h *IndexedMinHeap[string], n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		e, err := h.Pop()
		if err != nil {
			t.Fatalf("Pop: %v", err)
		}
		out = append(out, e.Key)
	}
	return out
}

func TestIndexedHeapStableTies(t *testing.T) {
	ih := NewIndexedMinHeap[string]()
	for _, k := range []string{"A", "B", "C"} {
		if err := ih.Push(k, 50); err != nil {
			t.Fatal(err)
		}
	}
	_ = ih.Push("D", 10)
	if !ih.IsConsistent() || !ih.Contains("B") || ih.Contains("Z") {
		t.Error("state after pushes is wrong")
	}
	if p, _ := ih.PriorityOf("B"); p != 50 {
		t.Errorf("PriorityOf(B) = %d", p)
	}
	if err := ih.Push("A", 1); err == nil {
		t.Error("duplicate key must be rejected")
	}
	if got := popKeys(t, ih, 4); !slices.Equal(got, []string{"D", "A", "B", "C"}) {
		t.Errorf("pop order = %v, want D then A B C in insertion order", got)
	}
	if !ih.IsEmpty() || !ih.IsConsistent() {
		t.Error("heap should be empty and consistent after draining")
	}
}

func TestDecreaseKey(t *testing.T) {
	dk := NewIndexedMinHeap[string]()
	for i := 0; i < 20; i++ {
		_ = dk.Push("k"+string(rune('0'+i/10))+string(rune('0'+i%10)), int64(1000+i*10))
	}
	if err := dk.DecreaseKey("k17", 5); err != nil {
		t.Fatal(err)
	}
	if top, _ := dk.Peek(); top.Key != "k17" || top.Priority != 5 {
		t.Errorf("after DecreaseKey top = %+v", top)
	}
	if !dk.IsConsistent() {
		t.Error("index map inconsistent after DecreaseKey")
	}
	_ = dk.DecreaseKey("k03", 1000) // a new tie with k00, which was inserted earlier
	if got := popKeys(t, dk, 3); !slices.Equal(got, []string{"k17", "k00", "k03"}) {
		t.Errorf("pop order = %v, want k17 k00 k03", got)
	}
	if err := dk.DecreaseKey("k05", 999_999); err == nil {
		t.Error("raising a priority must be rejected")
	}
	if err := dk.DecreaseKey("nope", 1); err == nil {
		t.Error("unknown key must be rejected")
	}
	if !dk.IsConsistent() {
		t.Error("inconsistent after mixed operations")
	}
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    int64
	}{{0, 1000}, {3, 8000}, {6, 60000}, {40, 60000}}
	for _, c := range cases {
		if got, err := BackoffMillis(c.attempt); err != nil || got != c.want {
			t.Errorf("BackoffMillis(%d) = %d, %v; want %d", c.attempt, got, err, c.want)
		}
	}
	if _, err := BackoffMillis(-1); err == nil {
		t.Error("negative attempt must be rejected")
	}
}

func ids(jobs []RetryJob) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.PaymentID
	}
	return out
}

func TestRetrySchedulerOrderAndExpedite(t *testing.T) {
	clock := NewFakeClock(1_000_000)
	s := NewRetryScheduler(clock)
	must(s.ScheduleRetry("A", 2)) // +4000
	must(s.ScheduleRetry("B", 0)) // +1000
	must(s.ScheduleRetry("C", 0)) // +1000, tie with B
	must(s.ScheduleRetry("D", 6)) // +60000
	if len(s.PollDue()) != 0 || s.Pending() != 4 {
		t.Fatal("nothing should be due yet")
	}
	if err := s.Expedite("D", clock.NowMillis()+500); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.NextDueAtMillis(); next != clock.NowMillis()+500 {
		t.Errorf("next due = %d, want now+500", next)
	}
	if err := s.Expedite("A", clock.NowMillis()+999_999); err == nil {
		t.Error("expedite must not push a retry later")
	}
	_ = clock.Advance(1000)
	if got := ids(s.PollDue()); !slices.Equal(got, []string{"D", "B", "C"}) {
		t.Errorf("due at t+1000 = %v, want D B C", got)
	}
	if s.Pending() != 1 || s.IsScheduled("B") || !s.IsScheduled("A") {
		t.Error("polled jobs must be removed, unpolled ones kept")
	}
	_ = clock.Advance(2999)
	if len(s.PollDue()) != 0 {
		t.Error("one millisecond early must not be due")
	}
	_ = clock.Advance(1)
	if len(s.PollDue()) != 1 || s.Pending() != 0 {
		t.Error("job due exactly at the boundary must be returned")
	}
}

func TestClockNeverGoesBackwards(t *testing.T) {
	if err := NewFakeClock(0).Advance(-1); err == nil {
		t.Error("negative advance must be rejected")
	}
}
