// Lesson: phases/08-messaging-and-streams/07-stream-processing-windows/docs/en.md
// Stream Processing: Tumbling, Sliding, and Session Windows
// Run:  go run .
// Test: go test ./...
// Sources: Akidau et al., "The Dataflow Model" (VLDB 2015), sections 2.2-2.3 (windowing, triggers);
// Apache Flink documentation, "Windows" (assigners, triggers, state); Kafka Streams "Windowing" docs.
// Go 1.22+, standard library only. Same design as the Java and Python versions. Processing time is the only clock
// in this lesson and it is injected; nothing sleeps.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------------------------------
// Records, windows, aggregates.
// ---------------------------------------------------------------------------------------------------------------------

// Txn is one card transaction as the stream processor sees it. Time is the processing time: the clock when the record
// arrived. This lesson uses processing time only; Lesson 08 separates event time from it.
type Txn struct {
	CardID string
	Amount Money
	Time   int64
}

// NewTxn validates what Java's compact constructor validates.
func NewTxn(cardID string, amount Money, time int64) (Txn, error) {
	if time < 0 {
		return Txn{}, errors.New("time must be non-negative")
	}
	return Txn{cardID, amount, time}, nil
}

// Window is a half-open time interval [Start, End). It is a comparable struct, so it works as a map key; ordering is
// by Start, then End (see Less).
type Window struct {
	Start int64
	End   int64
}

// NewWindow rejects an empty or inverted interval.
func NewWindow(start, end int64) (Window, error) {
	if end <= start {
		return Window{}, fmt.Errorf("window must have positive length: [%d, %d)", start, end)
	}
	return Window{start, end}, nil
}

func (w Window) Contains(t int64) bool { return t >= w.Start && t < w.End }

func (w Window) Length() int64 { return w.End - w.Start }

// Less is Java's compareTo: by start, then end.
func (w Window) Less(o Window) bool {
	if w.Start != o.Start {
		return w.Start < o.Start
	}
	return w.End < o.End
}

func (w Window) String() string { return fmt.Sprintf("[%d, %d)", w.Start, w.End) }

// Aggregate is what is kept per (key, window): count, sum, max. Updated in O(1) per record; the records themselves
// are not kept.
type Aggregate struct {
	Count int
	Sum   Money
	Max   Money
}

func EmptyAggregate(currency Currency) Aggregate { return Aggregate{0, Zero(currency), Zero(currency)} }

// Add folds one amount in. Every record in a stream shares the operator's currency, so the Plus error cannot occur.
func (a Aggregate) Add(amount Money) Aggregate {
	sum, err := a.Sum.Plus(amount)
	if err != nil {
		panic(err)
	}
	return Aggregate{a.Count + 1, sum, a.Max.Max(amount)}
}

func (a Aggregate) Merge(other Aggregate) Aggregate {
	sum, err := a.Sum.Plus(other.Sum)
	if err != nil {
		panic(err)
	}
	return Aggregate{a.Count + other.Count, sum, a.Max.Max(other.Max)}
}

func (a Aggregate) String() string {
	return fmt.Sprintf("Aggregate[count=%d, sum=%s, max=%s]", a.Count, a.Sum, a.Max)
}

// WindowResult is an emitted result: one key, one closed window, its aggregate, and the processing time at which it
// was emitted.
type WindowResult struct {
	Key       string
	Window    Window
	Aggregate Aggregate
	EmittedAt int64
}

// ---------------------------------------------------------------------------------------------------------------------
// Assigners: a pure function from a timestamp to the windows it belongs to.
// ---------------------------------------------------------------------------------------------------------------------

type WindowAssigner interface {
	Assign(time int64) []Window
	Describe() string
}

// floorMod is Java's Math.floorMod: the remainder takes the divisor's sign, so negative timestamps still align.
func floorMod(x, m int64) int64 {
	r := x % m
	if r != 0 && (r < 0) != (m < 0) {
		r += m
	}
	return r
}

// TumblingWindows are fixed-size, non-overlapping windows aligned to the epoch: every timestamp is in exactly one.
type TumblingWindows struct {
	Size int64
}

func NewTumblingWindows(size int64) (*TumblingWindows, error) {
	if size <= 0 {
		return nil, errors.New("size must be positive")
	}
	return &TumblingWindows{size}, nil
}

func (t *TumblingWindows) Assign(time int64) []Window {
	start := time - floorMod(time, t.Size)
	return []Window{{start, start + t.Size}}
}

func (t *TumblingWindows) Describe() string { return fmt.Sprintf("tumbling(size=%d)", t.Size) }

// SlidingWindows are fixed-size windows that start every Slide: every timestamp is in Size/Slide windows.
type SlidingWindows struct {
	Size  int64
	Slide int64
}

func NewSlidingWindows(size, slide int64) (*SlidingWindows, error) {
	if size <= 0 || slide <= 0 {
		return nil, errors.New("size and slide must be positive")
	}
	if slide > size {
		return nil, errors.New("slide larger than size leaves gaps; use tumbling windows")
	}
	if size%slide != 0 {
		return nil, errors.New("size must be a multiple of slide")
	}
	return &SlidingWindows{size, slide}, nil
}

func (s *SlidingWindows) Assign(time int64) []Window {
	out := []Window{}
	lastStart := time - floorMod(time, s.Slide)
	for start := lastStart; start > time-s.Size; start -= s.Slide {
		out = append(out, Window{start, start + s.Size})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *SlidingWindows) Describe() string {
	return fmt.Sprintf("sliding(size=%d, slide=%d)", s.Size, s.Slide)
}

// ---------------------------------------------------------------------------------------------------------------------
// Operators. Each keeps per-key, per-window state, updates it on Process(), and emits on OnTimer(now) when a window's
// trigger condition is met in processing time.
// ---------------------------------------------------------------------------------------------------------------------

type WindowOperator interface {
	Process(txn Txn) error
	OnTimer(now int64) []WindowResult
	StateSize() int
	Describe() string
}

// ErrTimeWentBackwards: a record arrived with a processing time earlier than one already seen.
var ErrTimeWentBackwards = errors.New("processing time went backwards")

// sortedWindows returns a key's windows in (start, end) order: Java's TreeMap iteration order.
func sortedWindows(m map[Window]Aggregate) []Window {
	out := make([]Window, 0, len(m))
	for w := range m {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

func sortResults(out []WindowResult) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Window != out[j].Window {
			return out[i].Window.Less(out[j].Window)
		}
		return out[i].Key < out[j].Key
	})
}

func sortedKeys(state map[string]map[Window]Aggregate) []string {
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func totalState(state map[string]map[Window]Aggregate) int {
	n := 0
	for _, m := range state {
		n += len(m)
	}
	return n
}

// WindowedAggregator handles tumbling or sliding windows: the assigner decides membership, the trigger is "processing
// time has passed the window end".
type WindowedAggregator struct {
	assigner    WindowAssigner
	currency    Currency
	state       map[string]map[Window]Aggregate
	maxTimeSeen int64
}

func NewWindowedAggregator(assigner WindowAssigner, currency Currency) *WindowedAggregator {
	return &WindowedAggregator{assigner, currency, map[string]map[Window]Aggregate{}, -1}
}

func (a *WindowedAggregator) Process(txn Txn) error {
	if txn.Time < a.maxTimeSeen {
		return fmt.Errorf("%w: %d < %d", ErrTimeWentBackwards, txn.Time, a.maxTimeSeen)
	}
	a.maxTimeSeen = txn.Time
	perKey, ok := a.state[txn.CardID]
	if !ok {
		perKey = map[Window]Aggregate{}
		a.state[txn.CardID] = perKey
	}
	fresh := EmptyAggregate(a.currency).Add(txn.Amount)
	for _, w := range a.assigner.Assign(txn.Time) {
		if existing, found := perKey[w]; found {
			perKey[w] = existing.Merge(fresh)
		} else {
			perKey[w] = fresh
		}
	}
	return nil
}

// OnTimer emits and drops every window whose end <= now. Windows are emitted once; later records cannot reach them.
func (a *WindowedAggregator) OnTimer(now int64) []WindowResult {
	out := []WindowResult{}
	for _, key := range sortedKeys(a.state) {
		windows := a.state[key]
		for _, w := range sortedWindows(windows) {
			if w.End > now {
				break
			}
			out = append(out, WindowResult{key, w, windows[w], now})
			delete(windows, w)
		}
		if len(windows) == 0 {
			delete(a.state, key)
		}
	}
	sortResults(out)
	return out
}

// Peek returns the current (key, window) aggregate and whether one exists. Used by tests and by the demo to show state
// before a trigger.
func (a *WindowedAggregator) Peek(key string, window Window) (Aggregate, bool) {
	agg, ok := a.state[key][window]
	return agg, ok
}

func (a *WindowedAggregator) StateSize() int { return totalState(a.state) }

func (a *WindowedAggregator) Describe() string { return a.assigner.Describe() }

// SessionAggregator handles session windows: per key, a record extends the session it falls within Gap of, or starts
// a new one. Two sessions that a record bridges are merged. A session closes when processing time passes its last
// record + gap.
type SessionAggregator struct {
	Gap         int64
	currency    Currency
	state       map[string]map[Window]Aggregate
	maxTimeSeen int64
}

func NewSessionAggregator(gap int64, currency Currency) (*SessionAggregator, error) {
	if gap <= 0 {
		return nil, errors.New("gap must be positive")
	}
	return &SessionAggregator{gap, currency, map[string]map[Window]Aggregate{}, -1}, nil
}

func (s *SessionAggregator) Process(txn Txn) error {
	if txn.Time < s.maxTimeSeen {
		return fmt.Errorf("%w: %d < %d", ErrTimeWentBackwards, txn.Time, s.maxTimeSeen)
	}
	s.maxTimeSeen = txn.Time
	sessions, ok := s.state[txn.CardID]
	if !ok {
		sessions = map[Window]Aggregate{}
		s.state[txn.CardID] = sessions
	}
	// A record's own session is [t, t + gap). Merge with every existing session it overlaps.
	mine := Window{txn.Time, txn.Time + s.Gap}
	agg := EmptyAggregate(s.currency).Add(txn.Amount)
	for _, w := range sortedWindows(sessions) {
		overlaps := w.Start < mine.End && mine.Start < w.End
		if overlaps {
			mine = Window{min(w.Start, mine.Start), max(w.End, mine.End)}
			agg = agg.Merge(sessions[w])
			delete(sessions, w)
		}
	}
	sessions[mine] = agg
	return nil
}

// OnTimer: a session [start, end) with end = lastRecord + gap closes when now >= end: the gap has elapsed with
// nothing new.
func (s *SessionAggregator) OnTimer(now int64) []WindowResult {
	out := []WindowResult{}
	for _, key := range sortedKeys(s.state) {
		sessions := s.state[key]
		for _, w := range sortedWindows(sessions) {
			if w.End <= now {
				// Report the session as [first record, last record + gap); the gap is part of the window by definition.
				out = append(out, WindowResult{key, w, sessions[w], now})
				delete(sessions, w)
			}
		}
		if len(sessions) == 0 {
			delete(s.state, key)
		}
	}
	sortResults(out)
	return out
}

func (s *SessionAggregator) OpenSessions(key string) []Window { return sortedWindows(s.state[key]) }

func (s *SessionAggregator) StateSize() int { return totalState(s.state) }

func (s *SessionAggregator) Describe() string { return fmt.Sprintf("session(gap=%d)", s.Gap) }

// ---------------------------------------------------------------------------------------------------------------------
// Driver and the velocity rule.
// ---------------------------------------------------------------------------------------------------------------------

// StreamRunner feeds records to an operator in time order and fires the timer every timerInterval of processing time.
type StreamRunner struct {
	operator      WindowOperator
	timerInterval int64
	nextTimer     int64
	maxStateSize  int
	results       []WindowResult
}

func NewStreamRunner(operator WindowOperator, timerInterval int64) (*StreamRunner, error) {
	if timerInterval <= 0 {
		return nil, errors.New("timerInterval must be positive")
	}
	return &StreamRunner{operator: operator, timerInterval: timerInterval, nextTimer: timerInterval}, nil
}

// Run processes all records (must be in non-decreasing time), then fires the timer up to endOfStream.
func (r *StreamRunner) Run(txns []Txn, endOfStream int64) ([]WindowResult, error) {
	for _, t := range txns {
		for r.nextTimer <= t.Time {
			r.results = append(r.results, r.operator.OnTimer(r.nextTimer)...)
			r.nextTimer += r.timerInterval
		}
		if err := r.operator.Process(t); err != nil {
			return r.results, err
		}
		r.maxStateSize = max(r.maxStateSize, r.operator.StateSize())
	}
	for r.nextTimer <= endOfStream {
		r.results = append(r.results, r.operator.OnTimer(r.nextTimer)...)
		r.nextTimer += r.timerInterval
	}
	return append([]WindowResult(nil), r.results...), nil
}

func (r *StreamRunner) MaxStateSize() int { return r.maxStateSize }

// VelocityRule is the fraud rule: more than MaxPerWindow transactions for one card in one window is a velocity breach.
type VelocityRule struct {
	MaxPerWindow int
}

func (v VelocityRule) Breaches(results []WindowResult) []WindowResult {
	out := []WindowResult{}
	for _, r := range results {
		if r.Aggregate.Count > v.MaxPerWindow {
			out = append(out, r)
		}
	}
	return out
}

const (
	SEC int64 = 1_000
	MIN int64 = 60 * SEC
)

func txn(card string, cents int64, time int64) Txn { return Txn{card, OfMinor(cents, USD), time} }

// DemoStream is the demo stream: three cards over about four minutes of processing time.
func DemoStream() []Txn {
	s := []Txn{}
	// card-A: four quick transactions straddling the 1:00 boundary, then quiet
	s = append(s, txn("card-A", 1_299, 0*MIN+50*SEC))
	s = append(s, txn("card-A", 2_450, 0*MIN+56*SEC))
	s = append(s, txn("card-A", 899, 1*MIN+3*SEC))
	s = append(s, txn("card-A", 15_000, 1*MIN+9*SEC))
	// card-B: one transaction a minute, steady
	for m := int64(0); m < 4; m++ {
		s = append(s, txn("card-B", 4_000+m, m*MIN+30*SEC))
	}
	// card-C: a burst at 2:00-2:10, a pause, another burst at 3:00
	s = append(s, txn("card-C", 500, 2*MIN+0*SEC))
	s = append(s, txn("card-C", 500, 2*MIN+4*SEC))
	s = append(s, txn("card-C", 500, 2*MIN+10*SEC))
	s = append(s, txn("card-C", 9_900, 3*MIN+0*SEC))
	s = append(s, txn("card-C", 9_900, 3*MIN+5*SEC))
	sort.SliceStable(s, func(i, j int) bool { return s[i].Time < s[j].Time })
	return s
}

func mmss(millis int64) string { return fmt.Sprintf("%d:%02d", millis/MIN, (millis%MIN)/SEC) }

func printResults(results []WindowResult) {
	for _, r := range results {
		fmt.Printf("  %-7s %s-%s  count=%d sum=%s max=%s  (emitted at %s)\n", r.Key, mmss(r.Window.Start), mmss(r.Window.End),
			r.Aggregate.Count, r.Aggregate.Sum, r.Aggregate.Max, mmss(r.EmittedAt))
	}
}

func formatWindows(ws []Window) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatSessions(ws []Window) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = mmss(w.Start) + "-" + mmss(w.End)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func countKey(results []WindowResult, key string) int {
	n := 0
	for _, r := range results {
		if r.Key == key {
			n++
		}
	}
	return n
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	stream := DemoStream()
	end := 5 * MIN
	rule := VelocityRule{3}

	fmt.Println("== 1. Window assignment is a pure function of the timestamp ==")
	t := 1*MIN + 3*SEC
	fmt.Printf("t=%s  tumbling(60s): %s\n", mmss(t), formatWindows(must(NewTumblingWindows(MIN)).Assign(t)))
	fmt.Printf("t=%s  sliding(60s, 10s): %s\n", mmss(t), formatWindows(must(NewSlidingWindows(MIN, 10*SEC)).Assign(t)))

	fmt.Println("\n== 2. Tumbling 1-minute windows: transactions per card per minute ==")
	tumbling := must(NewStreamRunner(NewWindowedAggregator(must(NewTumblingWindows(MIN)), USD), 10*SEC))
	tumblingResults := must(tumbling.Run(stream, end))
	printResults(tumblingResults)
	fmt.Printf("velocity breaches (> 3 per window): %d   max state entries: %d\n", len(rule.Breaches(tumblingResults)), tumbling.MaxStateSize())
	fmt.Println("card-A made 4 transactions in 19 seconds (0:50 to 1:09) and no tumbling window saw more than 2.")

	fmt.Println("\n== 3. Sliding 1-minute windows every 10 seconds: the boundary problem goes away ==")
	sliding := must(NewStreamRunner(NewWindowedAggregator(must(NewSlidingWindows(MIN, 10*SEC)), USD), 10*SEC))
	slidingResults := must(sliding.Run(stream, end))
	breaches := rule.Breaches(slidingResults)
	fmt.Printf("%d window results emitted (%d for card-A); breaches:\n", len(slidingResults), countKey(slidingResults, "card-A"))
	printResults(breaches)
	fmt.Printf("max state entries: %d (each record lives in size/slide = 6 windows)\n", sliding.MaxStateSize())

	fmt.Println("\n== 4. Session windows with a 30-second gap: activity bursts, not clock minutes ==")
	sessions := must(NewSessionAggregator(30*SEC, USD))
	sessionRunner := must(NewStreamRunner(sessions, 10*SEC))
	sessionResults := must(sessionRunner.Run(stream, end))
	printResults(sessionResults)
	fmt.Println("card-B never pauses less than 30 s between transactions, so each transaction is its own session;")
	fmt.Println("card-C's two bursts are two sessions; card-A's four transactions are one.")

	fmt.Println("\n== 5. Trigger timing: a window closes only when the timer passes its end ==")
	agg := NewWindowedAggregator(must(NewTumblingWindows(MIN)), USD)
	must(0, agg.Process(txn("card-A", 1_299, 50*SEC)))
	must(0, agg.Process(txn("card-A", 2_450, 56*SEC)))
	first := Window{0, MIN}
	peeked, _ := agg.Peek("card-A", first)
	fmt.Printf("state for card-A %s before the timer: %s\n", first, peeked)
	fmt.Printf("onTimer(0:59) emits %d windows; onTimer(1:00) emits %d\n", len(agg.OnTimer(59*SEC)), len(agg.OnTimer(MIN)))
	if err := agg.Process(txn("card-A", 1, 30*SEC)); errors.Is(err, ErrTimeWentBackwards) {
		fmt.Println("a record with an earlier processing time is rejected:", err)
	}

	fmt.Println("\n== 6. Session extension in detail ==")
	s2 := must(NewSessionAggregator(30*SEC, USD))
	must(0, s2.Process(txn("card-C", 500, 0)))
	fmt.Println("record at 0:00, gap 30 s -> open session " + formatSessions(s2.OpenSessions("card-C")))
	must(0, s2.Process(txn("card-C", 500, 25*SEC)))
	must(0, s2.Process(txn("card-C", 500, 50*SEC)))
	fmt.Println("records at 0:25 and 0:50 each arrive before the gap elapses -> extended to " + formatSessions(s2.OpenSessions("card-C")))
	must(0, s2.Process(txn("card-C", 500, 2*MIN)))
	fmt.Println("record at 2:00 arrives after the gap -> two sessions: " + formatSessions(s2.OpenSessions("card-C")))
	closed := []string{}
	for _, r := range s2.OnTimer(80 * SEC) {
		closed = append(closed, fmt.Sprintf("%s-%s count=%d", mmss(r.Window.Start), mmss(r.Window.End), r.Aggregate.Count))
	}
	fmt.Println("onTimer(1:20) closes the first: [" + strings.Join(closed, ", ") + "]")
	fmt.Printf("state entries: tumbling=%d sliding=%d session=%d (peak during the run)\n", tumbling.MaxStateSize(), sliding.MaxStateSize(), sessionRunner.MaxStateSize())
}
