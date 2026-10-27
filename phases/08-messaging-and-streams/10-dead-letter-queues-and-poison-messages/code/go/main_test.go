// Tests for phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkIs(t *testing.T, name string, err, target error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s (no error)", name)
	} else if !errors.Is(err, target) {
		t.Errorf("%s (wrong error %v)", name, err)
	}
}

// CountingHandler fails transiently a fixed number of times per offset, then succeeds. Fully deterministic.
type CountingHandler struct {
	failuresLeft map[int64]int
	handled      []int64
}

func newCountingHandler(failuresLeft map[int64]int) *CountingHandler {
	copied := make(map[int64]int, len(failuresLeft))
	for k, v := range failuresLeft {
		copied[k] = v
	}
	return &CountingHandler{failuresLeft: copied}
}

func (h *CountingHandler) Handle(e Event) error {
	if left := h.failuresLeft[e.Offset]; left > 0 {
		h.failuresLeft[e.Offset] = left - 1
		return &HandlerError{Kind: Transient, Msg: fmt.Sprintf("transient #%d", left)}
	}
	h.handled = append(h.handled, e.Offset)
	return nil
}

func ev(offset int64, key, amount, ccy string) Event { return Event{offset, key, amount, ccy} }

func anyLetter(letters []DeadLetter, pred func(DeadLetter) bool) bool {
	for _, d := range letters {
		if pred(d) {
			return true
		}
	}
	return false
}

func minor(t *testing.T, h *LedgerPostingHandler, key string) int64 {
	t.Helper()
	m, ok := h.Balance(key)
	if !ok {
		t.Fatalf("no balance for %s", key)
	}
	return m.MinorUnits
}

func TestStopTheWorldHeadOfLineBlocking(t *testing.T) {
	naive := NewStopTheWorldConsumer(NewLedgerPostingHandler(NewFaultInjector(1, 0.0)), 3)
	naive.Consume(partition())
	check(t, "stop-the-world stalls at the poison offset", naive.StuckOn() != nil && naive.StuckOn().Offset == 2)
	check(t, "stop-the-world processes nothing past the poison", naive.Processed() == 2 && naive.CommittedOffset() == 2)
}

// resilientRun is the Java test's second block: a consumer with no transient faults over the demo partition.
func resilientRun(t *testing.T) (*ResilientConsumer, *LedgerPostingHandler, *DeadLetterQueue, *SimClock) {
	t.Helper()
	clock := &SimClock{}
	handler := NewLedgerPostingHandler(NewFaultInjector(1, 0.0))
	dlq := NewDeadLetterQueue()
	consumer, err := NewResilientConsumer(handler, dlq, clock, 3, 100)
	if err != nil {
		t.Fatalf("NewResilientConsumer: %v", err)
	}
	consumer.Consume(partition())
	return consumer, handler, dlq, clock
}

func TestPoisonAndQuarantine(t *testing.T) {
	consumer, handler, dlq, clock := resilientRun(t)
	s := consumer.Stats()
	check(t, "offset commits past the poison message", s.CommittedOffset == 10)
	check(t, "every event is processed or parked", s.Processed+s.Parked == int64(len(partition())))
	check(t, "poison message parked as PERMANENT_FAILURE on first attempt", anyLetter(dlq.All(), func(d DeadLetter) bool {
		return d.Event.Offset == 2 && d.Reason == PermanentFailure && d.Attempts == 1
	}))
	check(t, "unknown currency parked as PERMANENT_FAILURE", anyLetter(dlq.All(), func(d DeadLetter) bool {
		return d.Event.Offset == 6 && d.Reason == PermanentFailure
	}))
	check(t, "permanent failures are never retried", s.Retries == 0 && clock.Now() == 0)
	quarantinedA := 0
	for _, d := range dlq.All() {
		if d.Event.AccountKey == "acct-A" && d.Reason == KeyQuarantined {
			quarantinedA++
		}
	}
	check(t, "later events for the poisoned key are quarantined, not applied", quarantinedA == 2)
	check(t, "quarantined followers never reach the handler: acct-A balance is only offset 0", minor(t, handler, "acct-A") == 10000)
	check(t, "other keys keep flowing: acct-B fully posted", minor(t, handler, "acct-B") == 3975)
	balanceC, _ := handler.Balance("acct-C")
	check(t, "other keys keep flowing: acct-C fully posted in JPY", balanceC == Money{1500, JPY})
	check(t, "quarantine is per key", consumer.IsQuarantined("acct-A") && consumer.IsQuarantined("acct-D") && !consumer.IsQuarantined("acct-B"))
	check(t, "dead letter records the cause offset", strings.Contains(dlq.ForKey("acct-A")[1].LastError, "offset 2"))
	check(t, "poison dead letter carries the Java-shaped parse message",
		dlq.ForKey("acct-A")[0].LastError == "12.345 has more decimals than USD allows (2)")
	check(t, "unknown-currency dead letter carries the Java-shaped message", dlq.ForKey("acct-D")[0].LastError == "unknown currency: US$")
	check(t, "demo dead-letter table has four rows", dlq.Size() == 4)
}

func TestReplayWithCorrection(t *testing.T) {
	consumer, handler, dlq, _ := resilientRun(t)

	replayed := consumer.Replay("acct-A", map[int64]Event{2: ev(2, "acct-A", "12.34", "USD")})
	check(t, "replay applies all three parked acct-A events", replayed == 3)
	check(t, "replay restores per-key order: 100.00 + 12.34 - 40.00 + 5.00", minor(t, handler, "acct-A") == 7734)
	check(t, "replayed key leaves quarantine", !consumer.IsQuarantined("acct-A"))
	check(t, "DLQ keeps only the unfixed key", dlq.Size() == 1 && dlq.All()[0].Event.AccountKey == "acct-D")
	check(t, "stats reflect the replay", consumer.Stats().Processed == 9 && consumer.Stats().Parked == 1)

	// Replay without a correction fails again and re-parks the followers in order
	replayedD := consumer.Replay("acct-D", map[int64]Event{})
	check(t, "replay of an unfixed poison message replays nothing", replayedD == 0)
	check(t, "unfixed key is quarantined again", consumer.IsQuarantined("acct-D") && len(dlq.ForKey("acct-D")) == 1)

	// Idempotent handler: replaying an applied offset does not double-post
	check(t, "idempotent replay returns no error", handler.Handle(ev(0, "acct-A", "100.00", "USD")) == nil)
	check(t, "handler is idempotent per offset", minor(t, handler, "acct-A") == 7734)
}

func TestTransientRetriesWithBackoff(t *testing.T) {
	clock := &SimClock{}
	counting := newCountingHandler(map[int64]int{1: 2}) // offset 1 fails twice, then succeeds
	dlq := NewDeadLetterQueue()
	c, err := NewResilientConsumer(counting, dlq, clock, 3, 100)
	if err != nil {
		t.Fatalf("NewResilientConsumer: %v", err)
	}
	c.Consume([]Event{ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k1", "3.00", "USD")})
	check(t, "transient failure within maxAttempts eventually succeeds", reflect.DeepEqual(counting.handled, []int64{0, 1, 2}))
	check(t, "two retries recorded", c.Stats().Retries == 2)
	check(t, "backoff is exponential: 100 + 200 ms on the simulated clock", clock.Now() == 300)
	check(t, "nothing parked when retries succeed", dlq.Size() == 0 && !c.IsQuarantined("k1"))
}

func TestMaxAttemptsExceededThenReplay(t *testing.T) {
	clock := &SimClock{}
	counting := newCountingHandler(map[int64]int{1: 5}) // offset 1 fails five times: more than maxAttempts
	dlq := NewDeadLetterQueue()
	c, err := NewResilientConsumer(counting, dlq, clock, 3, 100)
	if err != nil {
		t.Fatalf("NewResilientConsumer: %v", err)
	}
	c.Consume([]Event{ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k2", "3.00", "USD"), ev(3, "k1", "4.00", "USD")})
	k1 := dlq.ForKey("k1")
	if len(k1) != 2 {
		t.Fatalf("expected two k1 dead letters, got %d", len(k1))
	}
	check(t, "exhausted retries park the event", k1[0].Reason == MaxAttemptsExceeded)
	check(t, "attempt count equals maxAttempts", k1[0].Attempts == 3)
	check(t, "parkedAt is the simulated clock after the backoffs", k1[0].ParkedAt == 300)
	check(t, "follower on the same key is quarantined", k1[1].Reason == KeyQuarantined)
	handledK2 := false
	for _, o := range counting.handled {
		if o == 2 {
			handledK2 = true
		}
	}
	check(t, "a different key is unaffected", handledK2)
	check(t, "offset still commits to the end", c.Stats().CommittedOffset == 4)

	// Replay after the transient cause clears: the remaining failures are consumed by retries
	counting.failuresLeft[1] = 1
	replayed := c.Replay("k1", map[int64]Event{})
	check(t, "replay after recovery applies both parked events in order", replayed == 2 && reflect.DeepEqual(counting.handled, []int64{0, 2, 1, 3}))
	check(t, "k1 leaves quarantine after a clean replay", !c.IsQuarantined("k1") && dlq.Size() == 0)
}

func TestBoundariesAndMoney(t *testing.T) {
	_, err := NewResilientConsumer(newCountingHandler(nil), NewDeadLetterQueue(), &SimClock{}, 0, 100)
	checkIs(t, "maxAttempts must be at least one", err, ErrMaxAttempts)
	checkIs(t, "clock cannot go backwards", (&SimClock{}).Advance(-1), ErrNegativeAdvance)
	_, err = ParseMoney("12.345", USD)
	checkIs(t, "Money.parse rejects excess precision rather than rounding", err, ErrExcessPrecision)
	_, err = Money{1, USD}.Plus(Money{1, JPY})
	checkIs(t, "Money.plus rejects mixed currencies", err, ErrCurrencyMismatch)
	_, err = CurrencyOf("US$")
	checkIs(t, "unknown currency code is an error value", err, ErrUnknownCurrency)
	_, err = ParseMoney("12x", USD)
	checkIs(t, "garbage amount is not a decimal", err, ErrNotDecimal)
	_, err = ParseMoney("", USD)
	checkIs(t, "empty amount is not a decimal", err, ErrNotDecimal)
	_, err = ParseMoney("99999999999999999999", USD)
	check(t, "overflowing amount is rejected, never wrapped", err != nil)

	cases := []struct {
		text string
		ccy  Currency
		want Money
	}{
		{"100.00", USD, Money{10000, USD}},
		{"-40.00", USD, Money{-4000, USD}},
		{"12.3", USD, Money{1230, USD}},
		{".5", USD, Money{50, USD}},
		{"1200", JPY, Money{1200, JPY}},
		{" +7 ", USD, Money{700, USD}},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			got, err := ParseMoney(c.text, c.ccy)
			if err != nil || got != c.want {
				t.Errorf("ParseMoney(%q) = %v, %v; want %v", c.text, got, err, c.want)
			}
		})
	}
	check(t, "Money prints like the Java record", Money{7734, USD}.String() == "77.34 USD" && Money{1500, JPY}.String() == "1500 JPY" && Money{-5, USD}.String() == "-0.05 USD")
	check(t, "ConsumerStats prints the Java record shape",
		ConsumerStats{9, 1, 3, 2, 10}.String() == "ConsumerStats[processed=9, parked=1, quarantined=3, retries=2, committedOffset=10]")
}

func TestSeededFaultInjectorMatchesJava(t *testing.T) {
	// java.util.Random(11) with rate 0.25: the demo's second run retries exactly twice and parks offset 5
	faults := NewFaultInjector(11, 0.25)
	pattern := make([]bool, 12)
	fails := 0
	for i := range pattern {
		pattern[i] = faults.ShouldFail()
		if pattern[i] {
			fails++
		}
	}
	check(t, "java.util.Random(11) fails 4 of the first 12 draws at rate 0.25", fails == 4)
	check(t, "the first six draws are F F F T T T", reflect.DeepEqual(pattern[:6], []bool{false, false, false, true, true, true}))
	faults.Disable()
	check(t, "a disabled injector never fails", !faults.ShouldFail())

	// The demo's second run, end to end, pinned to the Java numbers.
	clock := &SimClock{}
	handler := NewLedgerPostingHandler(NewFaultInjector(11, 0.25))
	dlq := NewDeadLetterQueue()
	consumer, err := NewResilientConsumer(handler, dlq, clock, 3, 100)
	if err != nil {
		t.Fatalf("NewResilientConsumer: %v", err)
	}
	consumer.Consume(partition())
	check(t, "demo run: processed=4 parked=6 quarantined=3 retries=2 committed=10",
		consumer.Stats() == ConsumerStats{4, 6, 3, 2, 10})
	check(t, "demo run: simulated clock is 300ms", clock.Now() == 300)
	check(t, "demo run: offset 5 is parked as MAX_ATTEMPTS_EXCEEDED after 3 attempts", anyLetter(dlq.All(), func(d DeadLetter) bool {
		return d.Event.Offset == 5 && d.Reason == MaxAttemptsExceeded && d.Attempts == 3 && d.LastError == "ledger db timeout"
	}))
}
