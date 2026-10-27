// Tests for phases/12-payment-systems-design/14-testing-payment-systems. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func checkErr(t *testing.T, name string, err error, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
	} else if !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error %v)", name, err)
	}
}

func usd(minor int64) Money { return OfMinor(minor, USD) }

func cmd(k CommandKind, amount int64) Command { return Command{k, amount, APPROVE} }

// call returns an unwrapper for (bool, error) service calls the test expects to return no error.
// Go only forwards a multi-value call as the sole argument, hence the closure over t.
func call(t *testing.T) func(result bool, err error) bool {
	return func(result bool, err error) bool {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return result
	}
}

func TestMoneyFeeRoundingIsHalfEven(t *testing.T) {
	check(t, "2.9% of 100.00 = 2.90", must(usd(10_000).BasisPoints(290)).MinorUnits == 290)
	check(t, "2.9% of 0.18 rounds 0.522 up to 1", must(usd(18).BasisPoints(290)).MinorUnits == 1)
	check(t, "2.9% of 0.17 rounds 0.493 down to 0", must(usd(17).BasisPoints(290)).MinorUnits == 0)
	check(t, "tie 12.5 -> 12 (HALF_EVEN)", must(usd(125).BasisPoints(1000)).MinorUnits == 12)
	check(t, "tie 13.5 -> 14 (HALF_EVEN)", must(usd(135).BasisPoints(1000)).MinorUnits == 14)
	check(t, "String", usd(-37_50).String() == "-37.50 USD")
}

func TestLedgerDoubleEntry(t *testing.T) {
	ledger := NewLedger(USD)
	mustOK(ledger.Post(CUSTOMER, MERCHANT, usd(1000), "capture"))
	check(t, "posting moves money", ledger.Balance(CUSTOMER).MinorUnits == -1000 && ledger.Balance(MERCHANT).MinorUnits == 1000)
	check(t, "ledger total is zero", ledger.Total().IsZero())
	checkErr(t, "negative posting rejected", ledger.Post(CUSTOMER, MERCHANT, usd(-1), "x"), ErrInvalidArgument)
}

func TestServiceHappyPathAndStateGuards(t *testing.T) {
	ok := call(t)
	proc := NewFakeProcessor(USD)
	svc := NewPaymentService(USD, proc, NoBugs)
	check(t, "authorize", ok(svc.Authorize(usd(5000))) && svc.State == AUTHORIZED)
	check(t, "partial capture", ok(svc.Capture(usd(2000))) && svc.State == PARTIALLY_CAPTURED)
	_, err := svc.Capture(usd(3001))
	checkErr(t, "capture beyond authorization rejected", err, ErrInvalidArgument)
	check(t, "capture remainder", ok(svc.Capture(usd(3000))) && svc.State == CAPTURED)
	_, err = svc.Refund(usd(5001))
	checkErr(t, "refund beyond captured rejected", err, ErrInvalidArgument)
	check(t, "refund", ok(svc.Refund(usd(1250))) && svc.Refunded.MinorUnits == 1250)
	_, err = svc.VoidAuthorization()
	checkErr(t, "void after capture rejected", err, ErrIllegalState)
	_, err = svc.Refund(usd(0))
	checkErr(t, "zero amount rejected", err, ErrInvalidArgument)
	check(t, "fee posted to platform", svc.Ledger.Balance(PLATFORM_FEES).MinorUnits == 58+87)
	check(t, "invariants hold after happy path", CheckInvariants(svc) == "")
	check(t, "customer balance equals -(captured - refunded)", svc.Ledger.Balance(CUSTOMER).MinorUnits == -(5000-1250))
}

func TestServiceProcessorBehaviours(t *testing.T) {
	ok := call(t)
	declining := NewFakeProcessor(USD).Script(DECLINE)
	s2 := NewPaymentService(USD, declining, NoBugs)
	check(t, "decline leaves state NEW", !ok(s2.Authorize(usd(100))) && s2.State == NEW)
	check(t, "script exhausted means approve", ok(s2.Authorize(usd(100))) && declining.Calls == 2)
	erroring := NewFakeProcessor(USD).Script(APPROVE, ERROR)
	s3 := NewPaymentService(USD, erroring, NoBugs)
	ok(s3.Authorize(usd(100)))
	_, err := s3.Capture(usd(100))
	checkErr(t, "processor 5xx propagates", err, ErrIllegalState)
	checkErr(t, "processor 5xx is identifiable", err, ErrProcessorError)
	check(t, "5xx posts nothing", s3.Captured.IsZero() && s3.Ledger.Total().IsZero())
}

func TestTimeoutsWaitForReconcile(t *testing.T) {
	ok := call(t)
	lost := NewFakeProcessor(USD).Script(APPROVE, TIMEOUT_AFTER_APPLY)
	s4 := NewPaymentService(USD, lost, NoBugs)
	ok(s4.Authorize(usd(5000)))
	check(t, "timeout returns false and marks UNKNOWN", !ok(s4.Capture(usd(5000))) && s4.State == UNKNOWN)
	check(t, "nothing posted on timeout", s4.Captured.IsZero() && len(s4.Ledger.Postings) == 0)
	check(t, "processor did apply it", lost.Record.Captured == usd(5000))
	_, err := s4.Capture(usd(1))
	checkErr(t, "no capture while UNKNOWN", err, ErrIllegalState)
	s4.Reconcile()
	check(t, "reconcile posts the missing capture", s4.Captured == usd(5000) && s4.State == CAPTURED)
	check(t, "invariants hold after reconcile", CheckInvariants(s4) == "")
	never := NewFakeProcessor(USD).Script(APPROVE, TIMEOUT_BEFORE_APPLY)
	s5 := NewPaymentService(USD, never, NoBugs)
	ok(s5.Authorize(usd(5000)))
	ok(s5.Capture(usd(5000)))
	s5.Reconcile()
	check(t, "timeout before apply reconciles back to AUTHORIZED", s5.State == AUTHORIZED && s5.Captured.IsZero())
	s5.Reconcile()
	check(t, "reconcile is a no-op when not UNKNOWN", CheckInvariants(s5) == "" && s5.State == AUTHORIZED)
}

func TestInvariantsCatchEachInjectedBug(t *testing.T) {
	check(t, "over-capture bug caught", strings.HasPrefix(RunScenario(Scenario{"oc", []Command{cmd(AUTHORIZE, 1), cmd(CAPTURE, 2)}},
		Bugs{AllowOverCapture: true}).Violation, "captured"))
	check(t, "over-refund bug caught", strings.HasPrefix(RunScenario(Scenario{"or", []Command{cmd(AUTHORIZE, 1), cmd(CAPTURE, 1), cmd(REFUND, 2)}},
		Bugs{AllowOverRefund: true}).Violation, "refunded"))
	check(t, "unbalanced fee bug caught", strings.HasPrefix(RunScenario(Scenario{"uf", []Command{cmd(AUTHORIZE, 18), cmd(CAPTURE, 18)}},
		Bugs{UnbalancedFee: true}).Violation, "money not conserved"))
	check(t, "post-on-timeout bug caught", strings.Contains(RunScenario(Scenario{"pt", []Command{cmd(AUTHORIZE, 1),
		{CAPTURE, 1, TIMEOUT_BEFORE_APPLY}, cmd(RECONCILE, 0)}}, Bugs{PostOnTimeout: true}).Violation, "!= processor captured"))
	check(t, "same scenario, no bugs, passes", RunScenario(Scenario{"ok", []Command{cmd(AUTHORIZE, 18), cmd(CAPTURE, 18)}}, NoBugs).Passed())
	check(t, "illegal commands are skipped, not failures", RunScenario(Scenario{"skip", []Command{cmd(REFUND, 5), cmd(VOID, 0)}}, NoBugs).Passed())
}

func TestGenerators(t *testing.T) {
	rng := NewJavaRandom(1)
	longs := Longs(1, 100)
	inRange := true
	for i := 0; i < 1000; i++ {
		if v := longs.Generate(rng); v < 1 || v > 100 {
			inRange = false
		}
	}
	check(t, "longs stay in range", inRange)
	check(t, "longs shrink toward lo, lo first", longs.Shrink(100)[0] == 1 && slices.Contains(longs.Shrink(100), 99))
	check(t, "lo does not shrink", len(longs.Shrink(1)) == 0)
	check(t, "oneOf shrinks to earlier entries", slices.Equal(OneOf([]string{"a", "b", "c"}).Shrink("c"), []string{"a", "b"}))
	lists := Lists(longs, 5)
	sample := lists.Generate(NewJavaRandom(3))
	check(t, "lists are 1..maxLen", len(sample) >= 1 && len(sample) <= 5)
	shrunk := lists.Shrink([]int64{10, 20, 30, 40})
	check(t, "list shrink drops halves first", slices.Equal(shrunk[0], []int64{30, 40}) && slices.Equal(shrunk[1], []int64{10, 20}))
	noneEmpty := true
	for _, c := range shrunk {
		noneEmpty = noneEmpty && len(c) > 0
	}
	check(t, "list shrink never yields empty", noneEmpty)
	check(t, "same seed, same sequence", slices.Equal(lists.Generate(NewJavaRandom(9)), lists.Generate(NewJavaRandom(9))))
	check(t, "java.util.Random parity: new Random(42).nextInt(10) sequence", func() bool {
		r := NewJavaRandom(42)
		got := []int32{r.NextInt(10), r.NextInt(10), r.NextInt(10), r.NextInt(10), r.NextInt(10)}
		return slices.Equal(got, []int32{0, 3, 8, 4, 0})
	}())
}

func TestPropertyHarness(t *testing.T) {
	okResult := ForAll("n >= 1", Longs(1, 1000), func(n int64) error {
		if n >= 1 {
			return nil
		}
		return errors.New("negative")
	}, 5, 200)
	check(t, "passing property reports trials", okResult.Passed && okResult.TrialsRun == 200 && okResult.Counterexample == nil)
	big := ForAll("n < 500", Longs(1, 100_000), func(n int64) error {
		if n < 500 {
			return nil
		}
		return errors.New("n=" + strconv.FormatInt(n, 10))
	}, 5, 200)
	check(t, "failing property shrinks to boundary", !big.Passed && big.Counterexample.Shrunk == 500)
	check(t, "original is at least as large as shrunk", big.Counterexample.Original >= 500)
	crash := ForAll("crash", Longs(1, 10), func(n int64) error { panic(errors.New("boom")) }, 5, 10)
	check(t, "unexpected panic is a failure", !crash.Passed && strings.HasPrefix(crash.Counterexample.Reason, "crash"))
	rejected := ForAll("rejects", Longs(1, 10), func(n int64) error { return errors.New("not accepted: " + ErrInvalidArgument.Error()) }, 5, 10)
	check(t, "a plain error is a failure, not a rejection", !rejected.Passed)
	rejectedProperly := ForAll("rejects", Longs(1, 10), func(n int64) error { return errors.Join(ErrInvalidArgument, errors.New("not accepted")) }, 5, 10)
	check(t, "ErrInvalidArgument counts as rejected input, not failure", rejectedProperly.Passed)
}

func TestHeadlineProperty(t *testing.T) {
	check(t, "invariants hold for 300 random sequences", InvariantsHold(NoBugs, 42, 300, 12).Passed)
	check(t, "invariants hold under a second seed", InvariantsHold(NoBugs, 2024, 300, 12).Passed)
	oc := InvariantsHold(Bugs{AllowOverCapture: true}, 42, 500, 12)
	check(t, "over-capture found and shrunk to two commands", !oc.Passed && len(oc.Counterexample.Shrunk) == 2)
	check(t, "shrunk amounts are minimal", oc.Counterexample.Shrunk[0].AmountMinor == 1 && oc.Counterexample.Shrunk[1].AmountMinor == 2)
	check(t, "same seed finds the same trial as the Java harness", oc.TrialsRun == 12 && oc.Counterexample.ShrinkSteps == 67)
	pt := InvariantsHold(Bugs{PostOnTimeout: true}, 42, 2000, 12)
	check(t, "timeout bug found with enough trials", !pt.Passed && len(pt.Counterexample.Shrunk) == 3)
	check(t, "counterexample is reproducible", RunScenario(Scenario{"replay", pt.Counterexample.Shrunk}, Bugs{PostOnTimeout: true}).Violation != "")
}
