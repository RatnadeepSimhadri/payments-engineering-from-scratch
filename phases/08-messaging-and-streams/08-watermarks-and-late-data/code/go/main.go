// Lesson: phases/08-messaging-and-streams/08-watermarks-and-late-data/docs/en.md
// Watermarks, Event Time, and Late Data
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only. Same design as the Java and Python versions. Semantics follow the Dataflow model
// (Akidau et al., VLDB 2015) and Apache Flink's bounded-out-of-orderness watermark strategy.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Event is a payment event. EventTime is when the payment happened (stamped by the producer); ArrivalTime is when
// the stream processor saw it (stamped by the processor's clock). Both are epoch-style milliseconds; the demo uses
// minutes-since-midnight for readability.
type Event struct {
	Key         string
	EventTime   int64
	ArrivalTime int64
	Amount      Money
}

// LateReason says why an event landed in the side output.
type LateReason int

const WindowClosed LateReason = iota

func (r LateReason) String() string { return "WINDOW_CLOSED" }

// LateEvent is a late event parked in the side output, with the window it would have belonged to.
type LateEvent struct {
	Event              Event
	WindowStart        int64
	WindowEnd          int64
	WatermarkAtArrival int64
	Reason             LateReason
}

// FiringKind says whether a window result is its first (on-time) firing or a corrected re-firing caused by an
// allowed-late event.
type FiringKind int

const (
	OnTime FiringKind = iota
	LateUpdate
)

func (k FiringKind) String() string {
	if k == OnTime {
		return "ON_TIME"
	}
	return "LATE_UPDATE"
}

// WindowResult is a window result: [WindowStart, WindowEnd) is half-open.
type WindowResult struct {
	WindowStart int64
	WindowEnd   int64
	Count       int
	Sum         Money
	Kind        FiringKind
}

// Window is a half-open interval [Start, End). Tumbling windows of a fixed size tile the time axis.
type Window struct {
	Start int64
	End   int64
}

// floorDiv is Java's Math.floorDiv: rounds toward negative infinity, so negative timestamps land in the right window.
func floorDiv(x, y int64) int64 {
	q := x / y
	if (x%y != 0) && ((x < 0) != (y < 0)) {
		q--
	}
	return q
}

// TumblingWindowAssigner assigns each event time to exactly one tumbling window: start = floor(t / size) * size.
type TumblingWindowAssigner struct {
	sizeMillis int64
}

func NewTumblingWindowAssigner(sizeMillis int64) (*TumblingWindowAssigner, error) {
	if sizeMillis <= 0 {
		return nil, errors.New("window size must be positive")
	}
	return &TumblingWindowAssigner{sizeMillis}, nil
}

func (a *TumblingWindowAssigner) Assign(timestamp int64) Window {
	start := floorDiv(timestamp, a.sizeMillis) * a.sizeMillis
	return Window{start, start + a.sizeMillis}
}

// NoWatermark is the watermark before any event has been seen: nothing is known to be complete (Java's Long.MIN_VALUE).
const NoWatermark int64 = math.MinInt64

// BoundedOutOfOrdernessWatermark: after seeing a maximum event time T, the generator asserts that no further event with
// eventTime < T - maxOutOfOrderness will arrive. The watermark never moves backwards: a late event does not pull it down.
type BoundedOutOfOrdernessWatermark struct {
	maxOutOfOrdernessMillis int64
	maxEventTime            int64
}

func NewBoundedOutOfOrdernessWatermark(maxOutOfOrdernessMillis int64) (*BoundedOutOfOrdernessWatermark, error) {
	if maxOutOfOrdernessMillis < 0 {
		return nil, errors.New("out-of-orderness must be >= 0")
	}
	return &BoundedOutOfOrdernessWatermark{maxOutOfOrdernessMillis, NoWatermark}, nil
}

// Observe records an event time; returns the (possibly unchanged) current watermark.
func (w *BoundedOutOfOrdernessWatermark) Observe(eventTime int64) int64 {
	if eventTime > w.maxEventTime {
		w.maxEventTime = eventTime
	}
	return w.Current()
}

// Current is NoWatermark before any event has been seen.
func (w *BoundedOutOfOrdernessWatermark) Current() int64 {
	if w.maxEventTime == NoWatermark {
		return NoWatermark
	}
	return w.maxEventTime - w.maxOutOfOrdernessMillis
}

// WindowState is the mutable per-window accumulator. Kept until the window is garbage-collected after allowed lateness.
type WindowState struct {
	Count int
	Sum   Money
	Fired bool
}

func NewWindowState(currency Currency) *WindowState { return &WindowState{Sum: Zero(currency)} }

func (s *WindowState) Add(e Event) error {
	sum, err := s.Sum.Plus(e.Amount)
	if err != nil {
		return err
	}
	s.Count++
	s.Sum = sum
	return nil
}

// EventTimeWindowOperator is the event-time tumbling-window operator with a watermark, allowed lateness, and a
// late-data side output.
//
// Invariants:
//  1. A window [s, e) fires OnTime exactly once, when the watermark first reaches e.
//  2. An event with eventTime < watermark whose window has already fired is either folded in as a LateUpdate
//     (if watermark < e + allowedLateness) or sent to the side output, never dropped silently.
//  3. Every event is accounted for exactly once: sum over all final window counts + side output size == events seen.
type EventTimeWindowOperator struct {
	assigner              *TumblingWindowAssigner
	watermark             *BoundedOutOfOrdernessWatermark
	allowedLatenessMillis int64
	currency              Currency
	open                  map[Window]*WindowState
	sideOutput            []LateEvent
	emitted               []WindowResult
	eventsSeen            int64
	eventsInClosedWindows int64
}

func NewEventTimeWindowOperator(windowSizeMillis, maxOutOfOrdernessMillis, allowedLatenessMillis int64, currency Currency) (*EventTimeWindowOperator, error) {
	if allowedLatenessMillis < 0 {
		return nil, errors.New("allowed lateness must be >= 0")
	}
	assigner, err := NewTumblingWindowAssigner(windowSizeMillis)
	if err != nil {
		return nil, err
	}
	watermark, err := NewBoundedOutOfOrdernessWatermark(maxOutOfOrdernessMillis)
	if err != nil {
		return nil, err
	}
	return &EventTimeWindowOperator{
		assigner:              assigner,
		watermark:             watermark,
		allowedLatenessMillis: allowedLatenessMillis,
		currency:              currency,
		open:                  map[Window]*WindowState{},
	}, nil
}

// sortedOpen returns the open windows in start order: Java's TreeMap iteration order.
func (o *EventTimeWindowOperator) sortedOpen() []Window {
	out := make([]Window, 0, len(o.open))
	for w := range o.open {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// Process handles one event; returns the results emitted as a consequence (zero, one, or several). The only error is
// an event whose amount is in a different currency than the operator's.
func (o *EventTimeWindowOperator) Process(e Event) ([]WindowResult, error) {
	o.eventsSeen++
	out := []WindowResult{}
	w := o.assigner.Assign(e.EventTime)
	wmBefore := o.watermark.Current()

	if wmBefore != NoWatermark && wmBefore >= w.End+o.allowedLatenessMillis {
		// The window was fired and then garbage-collected. Side output, never silent drop.
		o.sideOutput = append(o.sideOutput, LateEvent{e, w.Start, w.End, wmBefore, WindowClosed})
		o.watermark.Observe(e.EventTime)
		return out, nil
	}

	state, ok := o.open[w]
	if !ok {
		state = NewWindowState(o.currency)
		o.open[w] = state
	}
	if err := state.Add(e); err != nil {
		o.eventsSeen--
		if state.Count == 0 {
			delete(o.open, w)
		}
		return nil, err
	}
	if state.Fired {
		// Window already fired on time; this is an allowed-late event. Re-emit a corrected result.
		out = append(out, WindowResult{w.Start, w.End, state.Count, state.Sum, LateUpdate})
	}

	wmAfter := o.watermark.Observe(e.EventTime)
	out = append(out, o.advance(wmAfter)...)
	return out, nil
}

// advance fires every unfired window whose end <= watermark, then GCs windows past end + allowed lateness.
func (o *EventTimeWindowOperator) advance(wm int64) []WindowResult {
	out := []WindowResult{}
	if wm == NoWatermark {
		return out
	}
	for _, w := range o.sortedOpen() {
		if w.End > wm {
			break // ordered by start; later windows are not ready
		}
		s := o.open[w]
		if !s.Fired {
			s.Fired = true
			out = append(out, WindowResult{w.Start, w.End, s.Count, s.Sum, OnTime})
		}
	}
	for _, w := range o.sortedOpen() {
		if wm >= w.End+o.allowedLatenessMillis {
			o.eventsInClosedWindows += int64(o.open[w].Count)
			delete(o.open, w)
		} else {
			break
		}
	}
	o.emitted = append(o.emitted, out...)
	return out
}

func (o *EventTimeWindowOperator) CurrentWatermark() int64 { return o.watermark.Current() }

func (o *EventTimeWindowOperator) SideOutput() []LateEvent {
	return append([]LateEvent(nil), o.sideOutput...)
}

func (o *EventTimeWindowOperator) AllEmitted() []WindowResult {
	return append([]WindowResult(nil), o.emitted...)
}

func (o *EventTimeWindowOperator) OpenWindowCount() int { return len(o.open) }

// AccountsForEveryEvent is invariant 3: nothing lost, nothing double-counted.
func (o *EventTimeWindowOperator) AccountsForEveryEvent() bool {
	var inOpen int64
	for _, s := range o.open {
		inOpen += int64(s.Count)
	}
	return o.eventsSeen == inOpen+o.eventsInClosedWindows+int64(len(o.sideOutput))
}

// ProcessingTimeWindowOperator is the naive alternative: windows keyed by arrival (processing) time. Deterministic
// here because the processor's clock is injected via Event.ArrivalTime. It never has late data, because it never asks
// when anything actually happened.
type ProcessingTimeWindowOperator struct {
	assigner *TumblingWindowAssigner
	windows  map[Window]*WindowState
	currency Currency
}

func NewProcessingTimeWindowOperator(windowSizeMillis int64, currency Currency) (*ProcessingTimeWindowOperator, error) {
	assigner, err := NewTumblingWindowAssigner(windowSizeMillis)
	if err != nil {
		return nil, err
	}
	return &ProcessingTimeWindowOperator{assigner, map[Window]*WindowState{}, currency}, nil
}

func (p *ProcessingTimeWindowOperator) Process(e Event) error {
	w := p.assigner.Assign(e.ArrivalTime)
	state, ok := p.windows[w]
	if !ok {
		state = NewWindowState(p.currency)
		p.windows[w] = state
	}
	return state.Add(e)
}

// Results lists every window in start order.
func (p *ProcessingTimeWindowOperator) Results() []WindowResult {
	windows := make([]Window, 0, len(p.windows))
	for w := range p.windows {
		windows = append(windows, w)
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start < windows[j].Start })
	out := make([]WindowResult, 0, len(windows))
	for _, w := range windows {
		s := p.windows[w]
		out = append(out, WindowResult{w.Start, w.End, s.Count, s.Sum, OnTime})
	}
	return out
}

const Minute int64 = 60_000

// T turns "11:59" into milliseconds since midnight. Keeps the demo readable.
func T(hhmm string) int64 {
	h, m, _ := strings.Cut(hhmm, ":")
	hours, _ := strconv.ParseInt(h, 10, 64)
	minutes, _ := strconv.ParseInt(m, 10, 64)
	return (hours*60 + minutes) * Minute
}

func hhmm(millis int64) string {
	if millis == NoWatermark {
		return "-inf"
	}
	minutes := floorDiv(millis, Minute)
	return fmt.Sprintf("%02d:%02d", floorDiv(minutes, 60), minutes-floorDiv(minutes, 60)*60)
}

func show(r WindowResult) string {
	return fmt.Sprintf("[%s, %s) count=%d sum=%s %s", hhmm(r.WindowStart), hhmm(r.WindowEnd), r.Count, r.Sum, r.Kind)
}

// Ev builds a demo event from "hh:mm" strings and cents.
func Ev(key, eventTime, arrivalTime string, cents int64) Event {
	return Event{key, T(eventTime), T(arrivalTime), OfMinor(cents, USD)}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	// Hourly settlement totals per merchant. The 11:59 authorization from a store with a flaky
	// uplink arrives at 12:03, after the 12:00-12:05 traffic has already been seen.
	events := []Event{
		Ev("m-1", "11:40", "11:40", 1999),
		Ev("m-1", "11:52", "11:52", 4500),
		Ev("m-2", "11:58", "11:58", 1250),
		Ev("m-1", "12:01", "12:01", 800),
		Ev("m-2", "12:02", "12:02", 3000),
		Ev("m-1", "11:59", "12:03", 7700), // happened in the 11:00 hour, arrived in the 12:00 hour
		Ev("m-2", "12:04", "12:04", 1500),
		Ev("m-1", "12:09", "12:09", 2000), // watermark reaches 12:04: the 11:00 window fires
		Ev("m-2", "11:57", "12:10", 999),  // late but within allowed lateness: corrected re-firing
		Ev("m-1", "12:16", "12:16", 600),  // watermark 12:11 passes 12:00 + 10 min: 11:00 window is GC'd
		Ev("m-2", "11:50", "12:20", 2500), // too late: side output
		Ev("m-1", "13:06", "13:06", 100),  // watermark 13:01 fires the 12:00 window
	}

	fmt.Println("== Processing-time windows: the 11:59 payment lands in the 12:00 hour ==")
	pt := must(NewProcessingTimeWindowOperator(60*Minute, USD))
	for _, e := range events {
		must(0, pt.Process(e))
	}
	for _, r := range pt.Results() {
		fmt.Println("  " + show(r))
	}

	fmt.Println()
	fmt.Println("== Event-time windows, watermark = max event time - 5 min, allowed lateness 10 min ==")
	op := must(NewEventTimeWindowOperator(60*Minute, 5*Minute, 10*Minute, USD))
	for _, e := range events {
		fired := must(op.Process(e))
		fmt.Printf("  event %s @%s (arrived %s) %-12s watermark=%s\n",
			e.Key, hhmm(e.EventTime), hhmm(e.ArrivalTime), e.Amount, hhmm(op.CurrentWatermark()))
		for _, r := range fired {
			fmt.Println("    -> fired " + show(r))
		}
	}

	fmt.Println()
	fmt.Println("== Side output (late data that missed the window entirely) ==")
	for _, le := range op.SideOutput() {
		fmt.Printf("  %s @%s arrived %s amount=%s target=[%s, %s) watermark-at-arrival=%s reason=%s\n",
			le.Event.Key, hhmm(le.Event.EventTime), hhmm(le.Event.ArrivalTime), le.Event.Amount,
			hhmm(le.WindowStart), hhmm(le.WindowEnd), hhmm(le.WatermarkAtArrival), le.Reason)
	}
	fmt.Println()
	fmt.Println("every event accounted for exactly once:", op.AccountsForEveryEvent())
	fmt.Println("open windows still holding state:", op.OpenWindowCount())
}
