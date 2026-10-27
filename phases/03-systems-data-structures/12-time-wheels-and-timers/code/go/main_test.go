// Tests for phases/03-systems-data-structures/12-time-wheels-and-timers. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"testing"
)

func newTimer(t *testing.T, clock Clock, tick int64, size int) *HierarchicalTimer {
	t.Helper()
	timer, err := NewHierarchicalTimer(clock, tick, size)
	if err != nil {
		t.Fatal(err)
	}
	return timer
}

func schedule(t *testing.T, timer *HierarchicalTimer, delay int64, task func()) *TimerEntry {
	t.Helper()
	e, err := timer.Schedule(delay, task)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func poll(t *testing.T, timer *HierarchicalTimer, clock *ManualClock, advance int64) {
	t.Helper()
	if err := clock.Advance(advance); err != nil {
		t.Fatal(err)
	}
	timer.Poll()
}

func levelsAfter(t *testing.T, timer *HierarchicalTimer, delay int64) int {
	t.Helper()
	schedule(t, timer, delay, func() {})
	return timer.Levels()
}

func TestBucket(t *testing.T) {
	bucket := NewBucket()
	e1, e2, e3 := NewTimerEntry(1, 10, func() {}), NewTimerEntry(2, 10, func() {}), NewTimerEntry(3, 10, func() {})
	bucket.Add(e1)
	bucket.Add(e2)
	bucket.Add(e3)
	if bucket.Size() != 3 {
		t.Error("bucket size after three adds")
	}
	bucket.Remove(e2)
	if bucket.Size() != 2 || e2.Bucket != nil || e2.Prev != nil {
		t.Error("bucket remove middle entry")
	}
	flushed := []int64{}
	bucket.Flush(func(e *TimerEntry) { flushed = append(flushed, e.ID) })
	if !slices.Equal(flushed, []int64{1, 3}) || bucket.Size() != 0 {
		t.Error("bucket flush is FIFO and empties the bucket")
	}
	bucket.Remove(e1)
	if bucket.Size() != 0 {
		t.Error("bucket remove of a non-member is a no-op")
	}
}

func TestSingleWheelFiresOnTickBoundaries(t *testing.T) {
	clock := NewManualClock(0)
	timer := newTimer(t, clock, 1, 8)
	log := []string{}
	schedule(t, timer, 3, func() { log = append(log, "a") })
	schedule(t, timer, 5, func() { log = append(log, "b") })
	schedule(t, timer, 5, func() { log = append(log, "c") })
	poll(t, timer, clock, 2)
	if len(log) != 0 {
		t.Error("nothing fires before the deadline")
	}
	poll(t, timer, clock, 1)
	if !slices.Equal(log, []string{"a"}) {
		t.Error("fires exactly at the deadline")
	}
	poll(t, timer, clock, 2)
	if !slices.Equal(log, []string{"a", "b", "c"}) {
		t.Error("same-tick timers fire in insertion order")
	}
	if timer.Pending() != 0 || timer.Fired() != 3 {
		t.Error("pending drops to zero")
	}

	// Zero delay runs inline; negative delay rejected
	inline := []string{}
	schedule(t, timer, 0, func() { inline = append(inline, "now") })
	if !slices.Equal(inline, []string{"now"}) {
		t.Error("zero delay on a tick boundary runs inline")
	}
	if _, err := timer.Schedule(-1, func() {}); !errors.Is(err, ErrInvalidArgument) {
		t.Error("negative delay is rejected")
	}
	if _, err := NewTimingWheel(1, 1, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Error("wheel size 1 is rejected")
	}
	if err := clock.Advance(-1); !errors.Is(err, ErrInvalidArgument) {
		t.Error("clock does not go backwards")
	}
}

func TestCancellation(t *testing.T) {
	c2 := NewManualClock(0)
	t2 := newTimer(t, c2, 1, 8)
	l2 := []string{}
	hold := schedule(t, t2, 4, func() { l2 = append(l2, "hold expired") })
	schedule(t, t2, 4, func() { l2 = append(l2, "other") })
	if !hold.Cancel() {
		t.Error("cancel returns true the first time")
	}
	if hold.Cancel() {
		t.Error("cancel returns false the second time")
	}
	if t2.Pending() != 1 || hold.Bucket != nil {
		t.Error("cancel frees the bucket slot immediately")
	}
	poll(t, t2, c2, 4)
	if !slices.Equal(l2, []string{"other"}) {
		t.Error("cancelled timer never fires, neighbour does")
	}
	pastHold := schedule(t, t2, 2, func() { l2 = append(l2, "late cancel") })
	poll(t, t2, c2, 2)
	if pastHold.Cancel() || !slices.Equal(l2, []string{"other", "late cancel"}) {
		t.Error("cancel after firing is false and harmless")
	}
}

func TestOverflowLevels(t *testing.T) {
	c3 := NewManualClock(0)
	t3 := newTimer(t, c3, 1, 8)
	cases := []struct {
		name  string
		delay int64
		want  int
	}{
		{"one level for a delay inside the interval", 7, 1},
		{"delay == interval opens an overflow wheel", 8, 2},
		{"delay 63 stays at two levels", 63, 2},
		{"delay 64 opens a third level", 64, 3},
		{"delay 511 stays at three levels", 511, 3},
		{"delay 512 opens a fourth level", 512, 4},
	}
	for _, c := range cases {
		if got := levelsAfter(t, t3, c.delay); got != c.want {
			t.Errorf("%s: levels = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCascadesAcrossFourLevels(t *testing.T) {
	c4 := NewManualClock(0)
	t4 := newTimer(t, c4, 1, 8)
	fireTimes := []int64{}
	delays := []int64{1, 7, 8, 9, 63, 64, 65, 300, 511, 512, 4095}
	for _, d := range delays {
		schedule(t, t4, d, func() { fireTimes = append(fireTimes, c4.NowMillis()) })
	}
	for i := 0; i < 4096; i++ {
		poll(t, t4, c4, 1)
	}
	if !slices.Equal(fireTimes, delays) {
		t.Errorf("every delay fires at exactly its deadline across four levels: %v", fireTimes)
	}
	// 1,7: level 0 (0). 8,9,63: level 1 (1 each). 64,65: level 2 but land in level 0 directly (1 each). 300,511: level 2 via level 1 (2 each).
	// 512: level 3, due at its boundary (1). 4095: level 3 -> 2 -> 1 -> 0 (3). Total 13; never more than levels - 1 per entry.
	if t4.Cascades() != 13 {
		t.Errorf("cascades: one per level an entry descends, 13 for this workload: %d", t4.Cascades())
	}

	// Cascade correctness when the clock is not advanced one tick at a time
	c5 := NewManualClock(0)
	t5 := newTimer(t, c5, 1, 8)
	jumpFires := []int64{}
	schedule(t, t5, 70, func() { jumpFires = append(jumpFires, c5.NowMillis()) })
	schedule(t, t5, 9, func() { jumpFires = append(jumpFires, c5.NowMillis()) })
	poll(t, t5, c5, 100)
	if !slices.Equal(jumpFires, []int64{100, 100}) || t5.Fired() != 2 {
		t.Error("a big clock jump still fires everything due, in deadline order")
	}
}

func TestNonAlignedClockRoundsUp(t *testing.T) {
	c6 := NewManualClock(1_003)
	coarse := newTimer(t, c6, 10, 16)
	order := []string{}
	schedule(t, coarse, 29, func() { order = append(order, "29") }) // deadline 1032 -> tick 1040
	schedule(t, coarse, 23, func() { order = append(order, "23") }) // deadline 1026 -> tick 1030
	poll(t, coarse, c6, 26)                                         // now 1029
	if len(order) != 0 {
		t.Error("coarse tick: nothing fires before its rounded-up tick")
	}
	poll(t, coarse, c6, 1) // 1030
	if !slices.Equal(order, []string{"23"}) {
		t.Error("coarse tick: 23 ms fires at the 1030 boundary")
	}
	poll(t, coarse, c6, 10) // 1040
	if !slices.Equal(order, []string{"23", "29"}) {
		t.Error("coarse tick: 29 ms fires at the 1040 boundary")
	}
	if schedule(t, coarse, 5, func() {}).DeadlineMillis%10 != 0 {
		t.Error("rounded deadline is a tick multiple")
	}
}

func TestWheelAndHeapAgree(t *testing.T) {
	rng := NewJavaRandom(99)
	wc, hc := NewManualClock(0), NewManualClock(0)
	wheel := newTimer(t, wc, 1, 16)
	hp := NewHeapTimer(hc)
	wheelFired, heapFired := []int64{}, []int64{}
	we, he := []*TimerEntry{}, []*TimerEntry{}
	for i := 0; i < 5_000; i++ {
		delay := int64(rng.NextIntN(20_000))
		id := int64(i)
		we = append(we, schedule(t, wheel, delay, func() { wheelFired = append(wheelFired, id) }))
		h, err := hp.Schedule(delay, func() { heapFired = append(heapFired, id) })
		if err != nil {
			t.Fatal(err)
		}
		he = append(he, h)
	}
	for i := 0; i < 5_000; i += 4 {
		we[i].Cancel()
		he[i].Cancel()
	}
	for step := 0; step < 25; step++ {
		poll(t, wheel, wc, 1_000)
		if err := hc.Advance(1_000); err != nil {
			t.Fatal(err)
		}
		hp.Poll()
	}
	if !SameSetFired(wheelFired, heapFired) || len(wheelFired) != 3_750 {
		t.Errorf("wheel and heap fire the same set: %d fired", len(wheelFired))
	}
	if wheel.Pending() != 0 || hp.Pending() != 0 || wheel.Fired() != hp.Fired() {
		t.Error("wheel frees cancelled entries eagerly, heap lazily")
	}
}

func TestFloorArithmetic(t *testing.T) {
	// Go's / and % truncate toward zero; the wheel needs Java's floorDiv/floorMod for negative start times.
	if floorDiv(-7, 2) != -4 || floorMod(-7, 2) != 1 || ceilDiv(7, 2) != 4 || ceilDiv(-7, 2) != -3 {
		t.Error("floorDiv, floorMod, and ceilDiv match java.lang.Math")
	}
	w, err := NewTimingWheel(10, 4, -25)
	if err != nil || w.CurrentTime() != -30 {
		t.Errorf("a negative start time aligns down to the tick: %d", w.CurrentTime())
	}
}
