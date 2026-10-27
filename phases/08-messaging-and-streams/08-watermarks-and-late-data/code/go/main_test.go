// Tests for phases/08-messaging-and-streams/08-watermarks-and-late-data. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

const MIN = Minute

func ev(eventTime, arrival string, cents int64) Event { return Ev("k", eventTime, arrival, cents) }

func hourly(t *testing.T, oooMin, latenessMin int64) *EventTimeWindowOperator {
	t.Helper()
	op, err := NewEventTimeWindowOperator(60*MIN, oooMin*MIN, latenessMin*MIN, USD)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func process(t *testing.T, op *EventTimeWindowOperator, e Event) []WindowResult {
	t.Helper()
	out, err := op.Process(e)
	if err != nil {
		t.Fatalf("Process(%v): %v", e, err)
	}
	return out
}

func TestWindowAssignment(t *testing.T) {
	assigner, err := NewTumblingWindowAssigner(60 * MIN)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "11:59 belongs to [11:00, 12:00)", assigner.Assign(T("11:59")) == Window{T("11:00"), T("12:00")})
	check(t, "12:00 belongs to [12:00, 13:00): half-open", assigner.Assign(T("12:00")).Start == T("12:00"))
	check(t, "negative timestamps floor correctly", assigner.Assign(-1).Start == -60*MIN)
	_, err = NewTumblingWindowAssigner(0)
	check(t, "zero window size rejected", err != nil)
	check(t, "floorDiv matches Java for mixed signs", floorDiv(-1, 60) == -1 && floorDiv(59, 60) == 0 && floorDiv(-60, 60) == -1)
}

func TestWatermarkGenerator(t *testing.T) {
	wm, err := NewBoundedOutOfOrdernessWatermark(5 * MIN)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "watermark unknown before any event", wm.Current() == NoWatermark)
	check(t, "watermark = max event time - bound", wm.Observe(T("12:09")) == T("12:04"))
	check(t, "watermark never moves backwards", wm.Observe(T("11:30")) == T("12:04"))
	check(t, "watermark advances on a newer event", wm.Observe(T("12:20")) == T("12:15"))
	_, err = NewBoundedOutOfOrdernessWatermark(-1)
	check(t, "negative out-of-orderness rejected", err != nil)
	_, err = NewEventTimeWindowOperator(60*MIN, 0, -1, USD)
	check(t, "negative allowed lateness rejected", err != nil)
}

func TestEventTimeOperatorLifecycle(t *testing.T) {
	op := hourly(t, 5, 10)
	process(t, op, ev("11:40", "11:40", 1999))
	process(t, op, ev("12:02", "12:02", 3000))
	r1 := process(t, op, ev("11:59", "12:03", 7700))
	check(t, "11:59 arriving at 12:03 fires nothing yet", len(r1) == 0)
	check(t, "watermark is 11:57 so the 11:00 window is still open", op.CurrentWatermark() == T("11:57"))
	r2 := process(t, op, ev("12:09", "12:09", 2000))
	check(t, "12:09 advances watermark to 12:04 and fires exactly one window", len(r2) == 1)
	check(t, "fired window is [11:00, 12:00)", r2[0].WindowStart == T("11:00") && r2[0].WindowEnd == T("12:00"))
	check(t, "fired window counts the 11:59 event", r2[0].Count == 2)
	check(t, "fired window sums with Money, 19.99 + 77.00", r2[0].Sum == OfMinor(9699, USD))
	check(t, "first firing is ON_TIME", r2[0].Kind == OnTime)

	t.Run("allowed lateness", func(t *testing.T) {
		r3 := process(t, op, ev("11:57", "12:10", 999))
		check(t, "late-but-allowed event re-fires the window", len(r3) == 1 && r3[0].Kind == LateUpdate)
		check(t, "late update carries the corrected count", r3[0].Count == 3)
		check(t, "late update carries the corrected sum", r3[0].Sum == OfMinor(10698, USD))
		check(t, "no side output yet", len(op.SideOutput()) == 0)
	})

	t.Run("beyond allowed lateness", func(t *testing.T) {
		process(t, op, ev("12:16", "12:16", 600)) // watermark 12:11 >= 12:00 + 10 min: window GC'd
		r4 := process(t, op, ev("11:50", "12:20", 2500))
		check(t, "too-late event fires nothing", len(r4) == 0)
		check(t, "too-late event lands in side output", len(op.SideOutput()) == 1)
		late := op.SideOutput()[0]
		check(t, "side output records the target window", late.WindowStart == T("11:00") && late.WindowEnd == T("12:00"))
		check(t, "side output records the watermark at arrival", late.WatermarkAtArrival == T("12:11"))
		check(t, "side output records the reason", late.Reason == WindowClosed)
		check(t, "the 11:00 window is no longer held in memory", op.OpenWindowCount() == 1)
	})

	t.Run("next window", func(t *testing.T) {
		r5 := process(t, op, ev("13:06", "13:06", 100))
		check(t, "12:00 window fires when watermark reaches 13:01", len(r5) == 1 && r5[0].WindowStart == T("12:00"))
		check(t, "12:00 window count is 3 (12:02, 12:09, 12:16)", r5[0].Count == 3)
		check(t, "events = open + closed + side output", op.AccountsForEveryEvent())
		onTime := 0
		for _, x := range op.AllEmitted() {
			if x.Kind == OnTime && x.WindowStart == T("11:00") {
				onTime++
			}
		}
		check(t, "ON_TIME fires exactly once per window", onTime == 1)
	})
}

func TestZeroOutOfOrderness(t *testing.T) {
	strict := hourly(t, 0, 0)
	process(t, strict, ev("11:40", "11:40", 100))
	s1 := process(t, strict, ev("12:02", "12:02", 100))
	check(t, "zero bound: 12:02 fires the 11:00 window immediately", len(s1) == 1 && s1[0].Count == 1)
	process(t, strict, ev("11:59", "12:03", 100))
	check(t, "zero bound and zero lateness: 11:59 goes straight to the side output", len(strict.SideOutput()) == 1)
	check(t, "strict operator still accounts for every event", strict.AccountsForEveryEvent())
}

func TestProcessingTimeOperator(t *testing.T) {
	pt, err := NewProcessingTimeWindowOperator(60*MIN, USD)
	if err != nil {
		t.Fatal(err)
	}
	if err := pt.Process(ev("11:59", "12:03", 7700)); err != nil {
		t.Fatal(err)
	}
	check(t, "processing time puts the 11:59 payment in the 12:00 hour", pt.Results()[0].WindowStart == T("12:00"))
}

func TestMoneyDiscipline(t *testing.T) {
	eur, _ := NewCurrency("EUR", 2)
	_, err := OfMinor(1, USD).Plus(OfMinor(1, eur))
	check(t, "mixing currencies in a window sum is an error", errors.Is(err, ErrCurrencyMismatch))
	op := hourly(t, 5, 10)
	process(t, op, ev("11:40", "11:40", 100))
	_, err = op.Process(Event{"k", T("11:41"), T("11:41"), OfMinor(1, eur)})
	check(t, "operator surfaces a foreign-currency event as an error", errors.Is(err, ErrCurrencyMismatch))
	check(t, "a rejected event is not counted", op.AccountsForEveryEvent() && op.OpenWindowCount() == 1)
}
