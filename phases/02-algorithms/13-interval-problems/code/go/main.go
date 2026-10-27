// Lesson: phases/02-algorithms/13-interval-problems/docs/en.md
// Interval Problems: Merging, Scheduling, and Sweep Lines
// Run:  go run .
// Test: go test ./...
// Sources: Kleinberg & Tardos, Algorithm Design (2005), ch. 4 (interval scheduling exchange argument);
// Shamos & Hoey, "Geometric intersection problems" (FOCS 1976) for the sweep line; ISO 4217 for Money.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Sentinel errors: Go reports what Java throws as IllegalArgumentException.
var (
	ErrEndBeforeStart = errors.New("end is before start")
	ErrNonPositive    = errors.New("hold amount must be positive")
	ErrWrongBook      = errors.New("hold currency does not match the book")
)

// Interval is a half-open interval [start, end) on an integer timeline (seconds, minutes, epoch millis: the unit is
// the caller's). Half-open means [0, 10) and [10, 20) touch but do not overlap, which is what a timeline needs:
// every instant belongs to exactly one of two adjacent intervals.
type Interval struct {
	Start, End int64
}

// NewInterval validates end >= start.
func NewInterval(start, end int64) (Interval, error) {
	if end < start {
		return Interval{}, fmt.Errorf("%w: end %d, start %d", ErrEndBeforeStart, end, start)
	}
	return Interval{start, end}, nil
}

// Length is end - start.
func (iv Interval) Length() int64 { return iv.End - iv.Start }

// IsEmpty is true when the interval holds no instant.
func (iv Interval) IsEmpty() bool { return iv.End == iv.Start }

// Overlaps is true if the two intervals share at least one instant. Touching intervals ([0,10) and [10,20)) do not.
func (iv Interval) Overlaps(o Interval) bool { return iv.Start < o.End && o.Start < iv.End }

// OverlapsOrTouches is true if they overlap or touch, i.e. their union is a single interval.
func (iv Interval) OverlapsOrTouches(o Interval) bool { return iv.Start <= o.End && o.Start <= iv.End }

// Contains is the half-open membership test.
func (iv Interval) Contains(t int64) bool { return iv.Start <= t && t < iv.End }

// Intersect is the overlapping part, or an empty interval at the later start if there is none.
func (iv Interval) Intersect(o Interval) Interval {
	s, e := max(iv.Start, o.Start), min(iv.End, o.End)
	if e < s {
		return Interval{s, s}
	}
	return Interval{s, e}
}

// Compare sorts by start, then by end. The order every algorithm below relies on.
func (iv Interval) Compare(o Interval) int {
	return cmp.Or(cmp.Compare(iv.Start, o.Start), cmp.Compare(iv.End, o.End))
}

func (iv Interval) String() string { return fmt.Sprintf("[%d, %d)", iv.Start, iv.End) }

// Peak is the result of a maximum-overlap sweep: how many intervals were open at once, and the first instant it happened.
type Peak struct {
	Count int
	At    int64
}

// event is one endpoint of an interval in a sweep: +1 at a start, -1 at an end; idx names the source interval.
type event struct {
	time  int64
	delta int
	idx   int
}

// byTimeThenDelta sorts events by time with -1 before +1 at equal times (half-open: an interval ending at t does not
// overlap one starting at t).
func byTimeThenDelta(a, b event) int {
	return cmp.Or(cmp.Compare(a.time, b.time), cmp.Compare(a.delta, b.delta))
}

// --- Interval algorithms on half-open integer intervals. Every function is pure and leaves its input untouched. ---

// Merge combines overlapping or touching intervals into a disjoint, sorted list. Sort by start; then each interval
// either extends the last merged one or starts a new one. O(n log n) for the sort, O(n) after.
// Invariant of the output: strictly increasing starts, and merged[i].End < merged[i+1].Start.
func Merge(input []Interval) []Interval {
	sorted := slices.Clone(input)
	slices.SortFunc(sorted, Interval.Compare)
	out := []Interval{}
	for _, iv := range sorted {
		if iv.IsEmpty() {
			continue
		}
		if len(out) == 0 || !out[len(out)-1].OverlapsOrTouches(iv) {
			out = append(out, iv)
		} else {
			last := out[len(out)-1]
			out[len(out)-1] = Interval{last.Start, max(last.End, iv.End)}
		}
	}
	return out
}

// Coverage is the total length covered by the union of the intervals: the sum of the merged lengths.
func Coverage(input []Interval) (int64, error) {
	var total int64
	for _, iv := range Merge(input) {
		var err error
		if total, err = addChecked(total, iv.Length()); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// Gaps returns the parts of `rng` not covered by any input interval, in order.
func Gaps(input []Interval, rng Interval) []Interval {
	out := []Interval{}
	cursor := rng.Start
	for _, iv := range Merge(input) {
		if iv.End <= rng.Start {
			continue
		}
		if iv.Start >= rng.End {
			break
		}
		if iv.Start > cursor {
			out = append(out, Interval{cursor, iv.Start})
		}
		cursor = max(cursor, iv.End)
	}
	if cursor < rng.End {
		out = append(out, Interval{cursor, rng.End})
	}
	return out
}

// MaxOverlap is the sweep line: turn every interval into a +1 event at its start and a -1 event at its end, sort events
// by time with -1 before +1 at equal times, and walk them keeping a running count. The maximum count is the maximum
// overlap. O(n log n).
func MaxOverlap(input []Interval) Peak {
	events := make([]event, 0, 2*len(input))
	for i, iv := range input {
		if iv.IsEmpty() {
			continue
		}
		events = append(events, event{iv.Start, +1, i}, event{iv.End, -1, i})
	}
	slices.SortStableFunc(events, byTimeThenDelta) // -1 sorts before +1
	open, best := 0, 0
	bestAt := int64(math.MinInt64)
	if len(input) == 0 {
		bestAt = 0
	}
	for _, e := range events {
		open += e.delta
		if open > best {
			best = open
			bestAt = e.time
		}
	}
	if best == 0 {
		bestAt = 0
	}
	return Peak{best, bestAt}
}

// MaxNonOverlapping is interval scheduling: the largest set of mutually non-overlapping intervals. Greedy by earliest
// end time, proved optimal by an exchange argument (Phase 2, Lesson 07). O(n log n). Ties on end time are broken by
// start time via the secondary sort key, so the result is deterministic.
func MaxNonOverlapping(input []Interval) []Interval {
	byEnd := slices.Clone(input)
	slices.SortStableFunc(byEnd, func(a, b Interval) int {
		return cmp.Or(cmp.Compare(a.End, b.End), cmp.Compare(a.Start, b.Start))
	})
	chosen := []Interval{}
	lastEnd := int64(math.MinInt64)
	for _, iv := range byEnd {
		if iv.IsEmpty() {
			continue
		}
		if iv.Start >= lastEnd {
			chosen = append(chosen, iv)
			lastEnd = iv.End
		}
	}
	return chosen
}

// Stabbing returns all intervals containing instant t, in sorted order. Linear scan; a segment or interval tree does
// this in O(log n + k).
func Stabbing(input []Interval, t int64) []Interval {
	out := []Interval{}
	for _, iv := range input {
		if iv.Contains(t) {
			out = append(out, iv)
		}
	}
	slices.SortFunc(out, Interval.Compare)
	return out
}

// Hold is a card authorization hold: an amount reserved on an account for a window of time.
type Hold struct {
	ID     string
	Amount Money
	Window Interval
}

// NewHold validates that the amount is positive.
func NewHold(id string, amount Money, window Interval) (Hold, error) {
	if amount.MinorUnits <= 0 {
		return Hold{}, fmt.Errorf("%w: %s", ErrNonPositive, amount)
	}
	return Hold{id, amount, window}, nil
}

// PeakExposure is the maximum simultaneous held amount, the first instant it was reached, and how many holds were open.
type PeakExposure struct {
	Amount    Money
	At        int64
	HoldsOpen int
}

// HoldBook holds all holds against one account. Answers the two questions a risk engine asks: how much is held right
// now, and what was the worst-case simultaneous exposure over a period. The second is a weighted sweep line.
type HoldBook struct {
	currency Currency
	holds    []Hold
}

// NewHoldBook opens an empty book in one currency.
func NewHoldBook(currency Currency) *HoldBook { return &HoldBook{currency: currency} }

// Add records a hold; a hold in another currency is ErrWrongBook.
func (b *HoldBook) Add(h Hold) error {
	if h.Amount.Currency != b.currency {
		return fmt.Errorf("%w: hold in %s on a %s book", ErrWrongBook, h.Amount.Currency.Code, b.currency.Code)
	}
	b.holds = append(b.holds, h)
	return nil
}

// Holds returns the holds in insertion order (a copy).
func (b *HoldBook) Holds() []Hold { return slices.Clone(b.holds) }

// ExposureAt is the sum of holds whose window contains t.
func (b *HoldBook) ExposureAt(t int64) (Money, error) {
	total := Zero(b.currency)
	for _, h := range b.holds {
		if h.Window.Contains(t) {
			var err error
			if total, err = total.Plus(h.Amount); err != nil {
				return Money{}, err
			}
		}
	}
	return total, nil
}

// ExpiredBy lists holds that have expired by t (window ended at or before t) without being captured: candidates to release.
func (b *HoldBook) ExpiredBy(t int64) []Hold {
	out := []Hold{}
	for _, h := range b.holds {
		if h.Window.End <= t {
			out = append(out, h)
		}
	}
	return out
}

// PeakExposure is the weighted sweep: +amount at each start, -amount at each end, releases before reservations at equal
// instants. Returns the maximum simultaneous held amount and the first instant it was reached.
func (b *HoldBook) PeakExposure() (PeakExposure, error) {
	events := make([]event, 0, 2*len(b.holds))
	for i, h := range b.holds {
		if h.Window.IsEmpty() {
			continue
		}
		events = append(events, event{h.Window.Start, +1, i}, event{h.Window.End, -1, i})
	}
	slices.SortStableFunc(events, byTimeThenDelta)
	open, best := Zero(b.currency), Zero(b.currency)
	var bestAt int64
	openCount, bestCount := 0, 0
	for _, e := range events {
		amt := b.holds[e.idx].Amount
		var err error
		if e.delta > 0 {
			open, err = open.Plus(amt)
		} else {
			open, err = open.Minus(amt)
		}
		if err != nil {
			return PeakExposure{}, err
		}
		openCount += e.delta
		if open.Compare(best) > 0 {
			best = open
			bestAt = e.time
			bestCount = openCount
		}
	}
	return PeakExposure{best, bestAt, bestCount}, nil
}

// BusyPeriods is the union of all hold windows: when was anything at all held.
func (b *HoldBook) BusyPeriods() []Interval {
	windows := make([]Interval, len(b.holds))
	for i, h := range b.holds {
		windows[i] = h.Window
	}
	return Merge(windows)
}

// T converts a clock time to minutes after midnight, so the numbers stay readable.
func T(hh, mm int) int64 { return int64(hh)*60 + int64(mm) }

func iv(s, e int64) Interval { return must(NewInterval(s, e)) }

// SampleHolds is the demo's card: six holds over one day.
func SampleHolds() *HoldBook {
	usd := USD
	book := NewHoldBook(usd)
	for _, h := range []Hold{
		must(NewHold("hotel", OfMinor(25_000, usd), iv(T(9, 0), T(17, 0)))),
		must(NewHold("fuel", OfMinor(12_500, usd), iv(T(10, 30), T(11, 30)))),
		must(NewHold("restaurant", OfMinor(8_000, usd), iv(T(12, 0), T(13, 0)))),
		must(NewHold("car-rental", OfMinor(50_000, usd), iv(T(11, 0), T(15, 0)))),
		must(NewHold("parking", OfMinor(1_500, usd), iv(T(13, 0), T(14, 0)))),
		must(NewHold("coffee", OfMinor(600, usd), iv(T(17, 0), T(17, 15)))),
	} {
		if err := book.Add(h); err != nil {
			panic(err)
		}
	}
	return book
}

// jstr formats a slice as Java's List.toString does: [a, b, c].
func jstr[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func ids(hs []Hold) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.ID
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Settlement windows: merge overlapping cutoffs ==")
	windows := []Interval{
		iv(T(14, 0), T(15, 30)), // scheme A cutoff window
		iv(T(15, 0), T(16, 0)),  // scheme B
		iv(T(16, 0), T(16, 45)), // scheme C, touches B
		iv(T(9, 0), T(10, 0)),   // morning batch
		iv(T(18, 0), T(18, 0))}  // an empty placeholder
	fmt.Println("input:  " + jstr(windows))
	fmt.Println("merged: " + jstr(Merge(windows)))
	fmt.Printf("coverage: %d minutes; gaps in 08:00-19:00: %s\n", must(Coverage(windows)), jstr(Gaps(windows, iv(T(8, 0), T(19, 0)))))

	fmt.Println()
	fmt.Println("== Max overlap: how many windows are open at once ==")
	p := MaxOverlap(windows)
	fmt.Printf("peak %d windows open, first reached at minute %d (%d:%02d)\n", p.Count, p.At, p.At/60, p.At%60)

	fmt.Println()
	fmt.Println("== Interval scheduling: most non-overlapping maintenance slots ==")
	slots := []Interval{iv(1, 4), iv(3, 5), iv(0, 6), iv(5, 7), iv(3, 9), iv(5, 9), iv(6, 10), iv(8, 11), iv(8, 12), iv(2, 14), iv(12, 16)}
	fmt.Println("slots: " + jstr(slots))
	fmt.Println("chosen by earliest end: " + jstr(MaxNonOverlapping(slots)))

	fmt.Println()
	fmt.Println("== Authorization holds on one card: exposure over the day ==")
	book := SampleHolds()
	for _, h := range book.Holds() {
		fmt.Printf("  %-11s %12s  %s\n", h.ID, h.Amount, h.Window)
	}
	fmt.Println("exposure at 12:30 =", must(book.ExposureAt(T(12, 30))))
	fmt.Println("exposure at 17:00 =", must(book.ExposureAt(T(17, 0))), " (hotel released at 17:00, coffee starts: half-open)")
	peak := must(book.PeakExposure())
	fmt.Printf("peak exposure %s with %d holds open, first at minute %d\n", peak.Amount, peak.HoldsOpen, peak.At)
	fmt.Println("busy periods: " + jstr(book.BusyPeriods()))
	fmt.Println("expired by 14:00: " + jstr(ids(book.ExpiredBy(T(14, 0)))))
}
