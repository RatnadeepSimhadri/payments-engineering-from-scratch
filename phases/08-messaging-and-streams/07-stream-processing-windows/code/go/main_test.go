// Tests for phases/08-messaging-and-streams/07-stream-processing-windows. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func find(results []WindowResult, key string, start, end int64) (WindowResult, bool) {
	for _, r := range results {
		if r.Key == key && r.Window.Start == start && r.Window.End == end {
			return r, true
		}
	}
	return WindowResult{}, false
}

func findOK(t *testing.T, results []WindowResult, key string, start, end int64) WindowResult {
	t.Helper()
	r, ok := find(results, key, start, end)
	if !ok {
		t.Fatalf("no result for %s [%d, %d)", key, start, end)
	}
	return r
}

func run(t *testing.T, op WindowOperator, interval int64, txns []Txn, end int64) ([]WindowResult, *StreamRunner) {
	t.Helper()
	runner, err := NewStreamRunner(op, interval)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(txns, end)
	if err != nil {
		t.Fatal(err)
	}
	return out, runner
}

func process(t *testing.T, op WindowOperator, txns ...Txn) {
	t.Helper()
	for _, x := range txns {
		if err := op.Process(x); err != nil {
			t.Fatal(err)
		}
	}
}

func tumblingMin(t *testing.T) *TumblingWindows {
	t.Helper()
	tw, err := NewTumblingWindows(MIN)
	if err != nil {
		t.Fatal(err)
	}
	return tw
}

func slidingMin(t *testing.T) *SlidingWindows {
	t.Helper()
	sw, err := NewSlidingWindows(MIN, 10*SEC)
	if err != nil {
		t.Fatal(err)
	}
	return sw
}

var burst = []Txn{txn("A", 100, 50*SEC), txn("A", 100, 56*SEC), txn("A", 100, 63*SEC), txn("A", 100, 69*SEC)}

func TestAssigners(t *testing.T) {
	tumbling := tumblingMin(t)
	check(t, "tumbling: timestamp at window start belongs to that window", reflect.DeepEqual(tumbling.Assign(MIN), []Window{{MIN, 2 * MIN}}))
	check(t, "tumbling: last millisecond belongs to the same window as the start", reflect.DeepEqual(tumbling.Assign(2*MIN-1), []Window{{MIN, 2 * MIN}}))
	check(t, "tumbling: exactly one window per timestamp", len(tumbling.Assign(12_345)) == 1 && tumbling.Assign(12_345)[0].Contains(12_345))
	sliding := slidingMin(t)
	at63 := sliding.Assign(63 * SEC)
	check(t, "sliding: size/slide windows per timestamp", len(at63) == 6)
	all := true
	for _, w := range at63 {
		all = all && w.Contains(63*SEC)
	}
	check(t, "sliding: every assigned window contains the timestamp", all)
	check(t, "sliding: windows are aligned to the slide and ordered", at63[0] == Window{10 * SEC, 70 * SEC} && at63[5] == Window{60 * SEC, 120 * SEC})
	check(t, "sliding: a timestamp exactly on a slide boundary is in 6 windows too", len(sliding.Assign(60*SEC)) == 6 && sliding.Assign(60 * SEC)[5].Start == 60*SEC)
	check(t, "floorMod aligns negative timestamps like Java", floorMod(-1, MIN) == MIN-1 && floorMod(7, 5) == 2)
	t.Run("validation", func(t *testing.T) {
		_, err := NewSlidingWindows(MIN, 2*MIN)
		check(t, "sliding: slide larger than size rejected", err != nil)
		_, err = NewSlidingWindows(MIN, 7*SEC)
		check(t, "sliding: size must be a multiple of slide", err != nil)
		_, err = NewTumblingWindows(0)
		check(t, "tumbling: size must be positive", err != nil)
		_, err = NewWindow(5, 5)
		check(t, "window: zero length rejected", err != nil)
		_, err = NewTxn("A", OfMinor(1, USD), -1)
		check(t, "txn: negative time rejected", err != nil)
	})
}

func TestAggregateArithmetic(t *testing.T) {
	a := EmptyAggregate(USD).Add(OfMinor(1_299, USD)).Add(OfMinor(2_450, USD))
	check(t, "aggregate: count, sum, max", a.Count == 2 && a.Sum.MinorUnits == 3_749 && a.Max.MinorUnits == 2_450)
	check(t, "aggregate: merge is associative in effect", a.Merge(EmptyAggregate(USD).Add(OfMinor(1, USD))).Count == 3)
}

func TestTumblingOperator(t *testing.T) {
	tumblingAgg := NewWindowedAggregator(tumblingMin(t), USD)
	tumblingOut, _ := run(t, tumblingAgg, 10*SEC, burst, 3*MIN)
	check(t, "tumbling: a 19-second burst across a boundary is split 2 and 2", findOK(t, tumblingOut, "A", 0, MIN).Aggregate.Count == 2 && findOK(t, tumblingOut, "A", MIN, 2*MIN).Aggregate.Count == 2)
	check(t, "tumbling: windows are emitted when the timer passes their end", findOK(t, tumblingOut, "A", 0, MIN).EmittedAt == MIN && findOK(t, tumblingOut, "A", MIN, 2*MIN).EmittedAt == 2*MIN)
	check(t, "tumbling: emitted windows are dropped from state", tumblingAgg.StateSize() == 0)
	check(t, "tumbling: velocity rule (> 3) misses the burst", len(VelocityRule{3}.Breaches(tumblingOut)) == 0)
	total := 0
	for _, r := range tumblingOut {
		total += r.Aggregate.Count
	}
	check(t, "runner: results cover every record exactly once under tumbling", total == 4)

	direct := NewWindowedAggregator(tumblingMin(t), USD)
	process(t, direct, txn("A", 100, 50*SEC))
	peeked, ok := direct.Peek("A", Window{0, MIN})
	check(t, "not emitted before the window end", len(direct.OnTimer(MIN-1)) == 0 && ok && peeked.Count == 1)
	check(t, "emitted exactly at the window end", len(direct.OnTimer(MIN)) == 1)
	check(t, "a window is emitted once", len(direct.OnTimer(MIN)) == 0 && len(direct.OnTimer(10*MIN)) == 0)
	check(t, "processing time cannot go backwards", errors.Is(direct.Process(txn("A", 1, 30*SEC)), ErrTimeWentBackwards))
}

func TestSlidingOperatorCatchesTheBurst(t *testing.T) {
	slidingOut, slidingRunner := run(t, NewWindowedAggregator(slidingMin(t), USD), 10*SEC, burst, 3*MIN)
	breaches := VelocityRule{3}.Breaches(slidingOut)
	allFour, sum400 := len(breaches) > 0, true
	for _, r := range breaches {
		allFour = allFour && r.Aggregate.Count == 4
		sum400 = sum400 && r.Aggregate.Sum.MinorUnits == 400
	}
	check(t, "sliding: the burst is caught", allFour)
	check(t, "sliding: every window that fully contains 0:50..1:09 reports 4", findOK(t, slidingOut, "A", 10*SEC, 70*SEC).Aggregate.Count == 4 && findOK(t, slidingOut, "A", 50*SEC, 110*SEC).Aggregate.Count == 4)
	check(t, "sliding: the edge windows that hold only half the burst report 2", findOK(t, slidingOut, "A", 0, MIN).Aggregate.Count == 2 && findOK(t, slidingOut, "A", 60*SEC, 120*SEC).Aggregate.Count == 2)
	check(t, "sliding: the same burst is reported in 5 windows", len(breaches) == 5)
	check(t, "sliding: peak state is larger than tumbling's", slidingRunner.MaxStateSize() > 1 && slidingRunner.MaxStateSize() <= 6+6)
	check(t, "sliding: sum is identical in every full window", sum400)
}

func TestSessionOperator(t *testing.T) {
	session, err := NewSessionAggregator(30*SEC, USD)
	if err != nil {
		t.Fatal(err)
	}
	process(t, session, txn("C", 500, 0))
	check(t, "session: first record opens [t, t+gap)", reflect.DeepEqual(session.OpenSessions("C"), []Window{{0, 30 * SEC}}))
	process(t, session, txn("C", 500, 25*SEC))
	check(t, "session: a record inside the gap extends the session", reflect.DeepEqual(session.OpenSessions("C"), []Window{{0, 55 * SEC}}))
	process(t, session, txn("C", 500, 55*SEC))
	check(t, "session: a record exactly at the end starts a new session (half-open)", len(session.OpenSessions("C")) == 2)
	check(t, "session: nothing closes before last+gap", len(session.OnTimer(54*SEC)) == 0)
	closed := session.OnTimer(55 * SEC)
	check(t, "session: closes when now >= last record + gap, with the gap included in the window", len(closed) == 1 && closed[0].Window == Window{0, 55 * SEC} && closed[0].Aggregate.Count == 2)
	check(t, "session: the open one remains", session.StateSize() == 1)
	_, err = NewSessionAggregator(0, USD)
	check(t, "session: gap must be positive", err != nil)

	bridge, _ := NewSessionAggregator(30*SEC, USD)
	process(t, bridge, txn("C", 1, 0), txn("C", 1, 50*SEC))
	check(t, "session: two sessions before a bridging record", len(bridge.OpenSessions("C")) == 2)
	process(t, bridge, txn("C", 1, 50*SEC)) // same time as the second; overlaps only it
	check(t, "session: a record overlapping one session merges only that one", len(bridge.OpenSessions("C")) == 2)

	mixed := append([]Txn{}, burst...)
	for m := int64(0); m < 3; m++ {
		mixed = append(mixed, txn("B", 4_000, m*MIN+30*SEC))
	}
	sort.SliceStable(mixed, func(i, j int) bool { return mixed[i].Time < mixed[j].Time })
	sessionOp, _ := NewSessionAggregator(30*SEC, USD)
	sessionOut, _ := run(t, sessionOp, 10*SEC, mixed, 5*MIN)
	var aResults, bResults []WindowResult
	for _, r := range sessionOut {
		if r.Key == "A" {
			aResults = append(aResults, r)
		} else if r.Key == "B" {
			bResults = append(bResults, r)
		}
	}
	check(t, "session: the burst is one session of 4", len(aResults) == 1 && aResults[0].Aggregate.Count == 4)
	check(t, "session: steady one-a-minute traffic is one session per record", len(bResults) == 3)
	check(t, "session: a session window spans first record to last record + gap", aResults[0].Window == Window{50 * SEC, 99 * SEC})
}

func TestRunnerInvariants(t *testing.T) {
	_, err := NewStreamRunner(NewWindowedAggregator(tumblingMin(t), USD), 0)
	check(t, "runner: timer interval must be positive", err != nil)
	coarse, _ := run(t, NewWindowedAggregator(tumblingMin(t), USD), 45*SEC, burst, 3*MIN)
	check(t, "runner: a coarse timer delays emission to the next tick after the window end", findOK(t, coarse, "A", 0, MIN).EmittedAt == 90*SEC)
	backwards := []Txn{txn("A", 1, MIN), txn("A", 1, 0)}
	runner, _ := NewStreamRunner(NewWindowedAggregator(tumblingMin(t), USD), 10*SEC)
	_, err = runner.Run(backwards, 2*MIN)
	check(t, "runner: an out-of-order stream surfaces the operator's error", errors.Is(err, ErrTimeWentBackwards))
}
