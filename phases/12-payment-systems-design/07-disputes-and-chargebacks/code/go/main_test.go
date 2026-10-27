// Tests for phases/12-payment-systems-design/07-disputes-and-chargebacks. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

var seq = 0

func eid() string {
	seq++
	return fmt.Sprintf("e%d", seq)
}

var notReceived = reason("VISA", "13.1")
var delivery = EvidenceOf(EvidenceItem{RECEIPT, "r"}, EvidenceItem{PROOF_OF_DELIVERY, "pod"})

func ev(k DisputeEventKind) DisputeEvent {
	switch k {
	case SUBMIT_REPRESENTMENT, RESPOND_PRE_ARB, RESPOND_INQUIRY:
		return WithEvidence(eid(), k, delivery, "t")
	default:
		return EventOf(eid(), k, "t")
	}
}

func apply(t *testing.T, svc *DisputeService, id string, e DisputeEvent) ApplyOutcome {
	t.Helper()
	out, err := svc.Apply(id, e)
	if err != nil {
		t.Fatalf("apply %v to %s: %v", e.Kind, id, err)
	}
	return out
}

func dispute(t *testing.T, svc *DisputeService, id string) *Dispute {
	t.Helper()
	d, err := svc.Dispute(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func impact(t *testing.T, svc *DisputeService, id string) Money {
	t.Helper()
	m, err := svc.MerchantImpact(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sweep(t *testing.T, svc *DisputeService) []string {
	t.Helper()
	fired, err := svc.SweepDeadlines()
	if err != nil {
		t.Fatal(err)
	}
	return fired
}

// disputeIn drives a fresh 100.00 USD 13.1 dispute into the requested state along the canonical path.
func disputeIn(t *testing.T, svc *DisputeService, target DisputeState) string {
	t.Helper()
	seq++
	id := fmt.Sprintf("d%d", seq)
	viaInquiry := target == INQUIRY || target == INQUIRY_RESPONDED || target == CLOSED_NO_CHARGEBACK
	if viaInquiry {
		if _, err := svc.OpenInquiry(id, "p", usd(10_000), notReceived); err != nil {
			t.Fatal(err)
		}
		if target != INQUIRY {
			apply(t, svc, id, ev(RESPOND_INQUIRY))
		}
		if target == CLOSED_NO_CHARGEBACK {
			apply(t, svc, id, ev(INQUIRY_CLOSED))
		}
		return id
	}
	if _, err := svc.OpenChargeback(id, "p", usd(10_000), notReceived); err != nil {
		t.Fatal(err)
	}
	var path []DisputeEventKind
	switch target {
	case CHARGEBACK:
	case REPRESENTED:
		path = []DisputeEventKind{SUBMIT_REPRESENTMENT}
	case PRE_ARBITRATION:
		path = []DisputeEventKind{SUBMIT_REPRESENTMENT, PRE_ARB_RECEIVED}
	case PRE_ARB_RESPONDED:
		path = []DisputeEventKind{SUBMIT_REPRESENTMENT, PRE_ARB_RECEIVED, RESPOND_PRE_ARB}
	case ARBITRATION:
		path = []DisputeEventKind{SUBMIT_REPRESENTMENT, PRE_ARB_RECEIVED, RESPOND_PRE_ARB, ARBITRATION_FILED}
	case WON:
		path = []DisputeEventKind{SUBMIT_REPRESENTMENT, ISSUER_ACCEPTED}
	case LOST:
		path = []DisputeEventKind{ACCEPT}
	default:
		t.Fatalf("no path to %v", target)
	}
	for _, k := range path {
		apply(t, svc, id, ev(k))
	}
	return id
}

func TestTableShape(t *testing.T) {
	check(t, "18 rules", len(RULES) == 18)
	for _, r := range RULES {
		check(t, "terminal states have no outgoing rules", !r.From.IsTerminal())
		check(t, "ARBITRATION and INQUIRY have no deadline rule", !(r.Kind == DEADLINE_PASSED && (r.From == ARBITRATION || r.From == INQUIRY)))
	}
}

func TestEveryRuleFiresAsWritten(t *testing.T) {
	for _, r := range RULES {
		t.Run(fmt.Sprintf("%v+%v", r.From, r.Kind), func(t *testing.T) {
			rc := NewFakeClock(Date(2026, time.February, 2))
			svc := Service(rc)
			id := disputeIn(t, svc, r.From)
			if r.Kind == DEADLINE_PASSED {
				rc.AdvanceDays(31)
				sweep(t, svc)
			} else {
				apply(t, svc, id, ev(r.Kind))
			}
			check(t, fmt.Sprintf("rule %v + %v -> %v", r.From, r.Kind, r.To), dispute(t, svc, id).State == r.To)
			check(t, fmt.Sprintf("ledger balances after %v + %v", r.From, r.Kind), svc.Ledger().Balances())
		})
	}
}

func TestEveryIllegalPairIsRefusedAndChangesNothing(t *testing.T) {
	illegalPairs := 0
	for _, s := range DisputeStates {
		for _, k := range DisputeEventKinds {
			if _, legal := findRule(s, k); legal {
				continue
			}
			illegalPairs++
			svc := Service(NewFakeClock(Date(2026, time.February, 2)))
			id := disputeIn(t, svc, s)
			entries := len(svc.Ledger().Journal())
			versions := len(dispute(t, svc, id).History)
			_, err := svc.Apply(id, ev(k))
			check(t, fmt.Sprintf("illegal %v + %v refused", s, k), errors.Is(err, ErrIllegalTransition))
			check(t, fmt.Sprintf("untouched %v + %v", s, k), dispute(t, svc, id).State == s && len(svc.Ledger().Journal()) == entries && len(dispute(t, svc, id).History) == versions)
		}
	}
	check(t, "112 illegal pairs (10 states x 13 events - 18 rules)", illegalPairs == 112)
}

func TestEvidence(t *testing.T) {
	receiptOnly := EvidenceOf(EvidenceItem{RECEIPT, "r"})
	missing := receiptOnly.MissingFor(notReceived)
	check(t, "13.1 with receipt only misses the delivery group", len(missing) == 1 && missing[0].Equal(OneOf(PROOF_OF_DELIVERY, SIGNED_DELIVERY)))
	check(t, "signed delivery satisfies the 'one of' group", len(EvidenceOf(EvidenceItem{RECEIPT, "r"}, EvidenceItem{SIGNED_DELIVERY, "s"}).MissingFor(notReceived)) == 0)
	check(t, "empty bundle misses both groups", len(EvidenceOf().MissingFor(notReceived)) == 2)
	fraud := reason("VISA", "10.4")
	tds := EvidenceOf(EvidenceItem{THREE_DS_AUTHENTICATION, "eci05"})
	check(t, "3DS shifts liability on a fraud reason", tds.LiabilityShift(fraud))
	check(t, "3DS does not shift liability on a consumer dispute", !tds.LiabilityShift(notReceived))
	check(t, "AVS/CVV match alone does not answer 10.4", len(EvidenceOf(EvidenceItem{AVS_CVV_MATCH, "Y/M"}, EvidenceItem{RECEIPT, "r"}).MissingFor(fraud)) == 1)
	_, err := GetReasonCode("VISA", "99.9")
	check(t, "unknown reason code refused", errors.Is(err, ErrInvalidArgument))
	check(t, "requirements print in declaration order", reason("VISA", "13.6").Requirements[0].String() == "[REFUND_POLICY, REFUND_ISSUED]")
}

func TestMoneyOnTheFullPath(t *testing.T) {
	clock := NewFakeClock(Date(2026, time.February, 2))
	svc := Service(clock)
	receiptOnly := EvidenceOf(EvidenceItem{RECEIPT, "r"})
	if _, err := svc.OpenChargeback("dp", "pay", usd(24_900), notReceived); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Apply("dp", WithEvidence("x1", SUBMIT_REPRESENTMENT, receiptOnly, "thin"))
	check(t, "representment with incomplete evidence refused", errors.Is(err, ErrIllegalTransition) && strings.Contains(err.Error(), "missing one of [[PROOF_OF_DELIVERY, SIGNED_DELIVERY]]"))
	_, err = svc.Apply("dp", EventOf("x2", SUBMIT_REPRESENTMENT, "none"))
	check(t, "representment without a bundle refused", errors.Is(err, ErrIllegalTransition))
	check(t, "refusals left CHARGEBACK", dispute(t, svc, "dp").State == CHARGEBACK)

	check(t, "chargeback debits amount + 15.00 fee", impact(t, svc, "dp") == usd(-26_400) && svc.Ledger().Balance(FEES) == usd(1_500))
	check(t, "deadline is 20 days after the chargeback", dispute(t, svc, "dp").Deadline.Equal(Date(2026, time.February, 22)))
	clock.AdvanceDays(12)
	apply(t, svc, "dp", WithEvidence("x3", SUBMIT_REPRESENTMENT, delivery, "ok"))
	check(t, "representment re-credits into the hold, not to the merchant", svc.Ledger().Balance(HOLD) == usd(24_900) && impact(t, svc, "dp") == usd(-26_400))
	clock.AdvanceDays(29)
	apply(t, svc, "dp", EventOf("x4", PRE_ARB_RECEIVED, "pre-arb"))
	check(t, "pre-arbitration moves no money", svc.Ledger().Balance(HOLD) == usd(24_900))
	clock.AdvanceDays(15)
	apply(t, svc, "dp", WithEvidence("x5", RESPOND_PRE_ARB, delivery, "respond"))
	clock.AdvanceDays(7)
	apply(t, svc, "dp", EventOf("x6", ARBITRATION_FILED, "filed"))
	check(t, "arbitration has no deadline", dispute(t, svc, "dp").Deadline == nil)
	clock.AdvanceDays(55)
	sweep(t, svc)
	check(t, "sweep does not decide arbitration", dispute(t, svc, "dp").State == ARBITRATION)
	apply(t, svc, "dp", EventOf("x7", RULING_FOR_MERCHANT, "won"))
	check(t, "win releases the hold; merchant is out only the fee", dispute(t, svc, "dp").State == WON && svc.Ledger().Balance(HOLD).IsZero() && impact(t, svc, "dp") == usd(-1_500))
	check(t, "the whole course took 118 days", DaysBetween(Date(2026, time.February, 2), clock.Today()) == 118)
	h := dispute(t, svc, "dp").History
	check(t, "history has open + 5 transitions, versions contiguous", len(h) == 6 && h[5].Version == 5)
	check(t, "redelivered ruling is DUPLICATE", apply(t, svc, "dp", EventOf("x7", RULING_FOR_MERCHANT, "again")) == DUPLICATE)
	_, err = svc.Apply("dp", EventOf("x8", RULING_FOR_ISSUER, "late"))
	check(t, "terminal WON refuses a new ruling", errors.Is(err, ErrIllegalTransition))
}

func TestLosingAtEachStageCostsDifferentAmounts(t *testing.T) {
	l := Service(NewFakeClock(Date(2026, time.February, 2)))
	accepted := disputeIn(t, l, LOST)
	check(t, "accepting the chargeback: amount + fee", impact(t, l, accepted) == usd(-11_500))
	preArb := disputeIn(t, l, PRE_ARBITRATION)
	apply(t, l, preArb, ev(ACCEPT))
	check(t, "accepting pre-arb returns the hold to the scheme: amount + fee", impact(t, l, preArb) == usd(-11_500) && dispute(t, l, preArb).State == LOST)
	arb := disputeIn(t, l, ARBITRATION)
	apply(t, l, arb, ev(RULING_FOR_ISSUER))
	check(t, "losing arbitration adds the 500.00 filing fee", impact(t, l, arb) == usd(-61_500))
	check(t, "hold is empty when nothing is in flight", l.Ledger().Balance(HOLD).IsZero() && l.Ledger().Balances())
}

func TestDeadlines(t *testing.T) {
	fraud := reason("VISA", "10.4")
	dc := NewFakeClock(Date(2026, time.February, 2))
	d := Service(dc)
	if _, err := d.OpenChargeback("late", "p", usd(5_000), fraud); err != nil {
		t.Fatal(err)
	}
	dc.AdvanceDays(20)
	check(t, "on the deadline day nothing fires", len(sweep(t, d)) == 0 && dispute(t, d, "late").State == CHARGEBACK)
	dc.AdvanceDays(1)
	fired := sweep(t, d)
	check(t, "the day after, CHARGEBACK -> LOST", len(fired) == 1 && dispute(t, d, "late").State == LOST)
	if _, err := d.OpenChargeback("early", "p", usd(5_000), fraud); err != nil {
		t.Fatal(err)
	}
	_, err := d.Apply("early", EventOf("y0", DEADLINE_PASSED, "forged"))
	check(t, "a hand-made DEADLINE_PASSED before the deadline is refused", errors.Is(err, ErrIllegalTransition))
	check(t, "second sweep is harmless", len(sweep(t, d)) == 0 && len(dispute(t, d, "late").History) == 2)
	if _, err := d.OpenChargeback("tooLate", "p", usd(5_000), notReceived); err != nil {
		t.Fatal(err)
	}
	dc.AdvanceDays(21)
	_, err = d.Apply("tooLate", WithEvidence("y1", SUBMIT_REPRESENTMENT, delivery, "late"))
	check(t, "representment after the deadline refused even with full evidence", errors.Is(err, ErrIllegalTransition))
	if _, err := d.OpenInquiry("inq", "p", usd(5_000), notReceived); err != nil {
		t.Fatal(err)
	}
	dc.AdvanceDays(21)
	inq := sweep(t, d)
	overdue := false
	for _, s := range inq {
		if strings.HasPrefix(s, "inq overdue") {
			overdue = true
		}
	}
	check(t, "an overdue inquiry is reported, not moved", overdue && dispute(t, d, "inq").State == INQUIRY)
	check(t, "inquiry moved no money", impact(t, d, "inq").IsZero())
	apply(t, d, "inq", EventOf("y2", CHARGEBACK_RECEIVED, "issuer escalated"))
	check(t, "inquiry escalated to chargeback debits the merchant", impact(t, d, "inq") == usd(-6_500))

	_, err = d.OpenChargeback("late", "p", usd(1), fraud)
	check(t, "duplicate dispute id refused", errors.Is(err, ErrIllegalState))
	_, err = d.OpenChargeback("z", "p", usd(0), fraud)
	check(t, "non-positive amount refused", errors.Is(err, ErrInvalidArgument))
}
