// Tests for phases/12-payment-systems-design/10-payment-method-abstraction. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

var USD45 = mustParse("45.00", USD)

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

func pay(t *testing.T, svc *PaymentService, r ChargeRequest) PaymentResult {
	t.Helper()
	res, err := svc.Pay(r)
	if err != nil {
		t.Fatalf("pay %s: %v", r.Reference, err)
	}
	return res
}

func lookup(t *testing.T, svc *PaymentService, ref string) PaymentResult {
	t.Helper()
	r, ok := svc.Lookup(ref)
	if !ok {
		t.Fatalf("lookup %s: not found", ref)
	}
	return r
}

// LossyCardAdapter collapses every decline to DO_NOT_HONOR: the lossy mapping the suite must catch.
type LossyCardAdapter struct{ real *CardAdapter }

func (l *LossyCardAdapter) Name() string               { return "lossy_card" }
func (l *LossyCardAdapter) MethodType() MethodType     { return CARD }
func (l *LossyCardAdapter) Capabilities() Capabilities { return l.real.Capabilities() }
func (l *LossyCardAdapter) Charge(request ChargeRequest) (PaymentResult, error) {
	r, err := l.real.Charge(request)
	if err != nil || r.Status != DECLINED {
		return r, err
	}
	return PaymentResult{r.Reference, r.ProcessorRef, r.Status, r.Amount, DO_NOT_HONOR, r.RawCode}, nil
}
func (l *LossyCardAdapter) Capture(ref string, amount Money) (PaymentResult, error) {
	return l.real.Capture(ref, amount)
}
func (l *LossyCardAdapter) Refund(ref string, amount Money) (PaymentResult, error) {
	return l.real.Refund(ref, amount)
}
func (l *LossyCardAdapter) TranslateEvent(raw string) (PaymentEvent, bool) {
	return l.real.TranslateEvent(raw)
}

// NoDedupeBankAdapter forgets the rail has no idempotency and submits every call.
type NoDedupeBankAdapter struct {
	api        *FakeBankDebitApi
	translator *BankDebitAdapter
	debited    map[string]Money
}

func newNoDedupeBankAdapter(api *FakeBankDebitApi) *NoDedupeBankAdapter {
	return &NoDedupeBankAdapter{api, NewBankDebitAdapter(api), map[string]Money{}}
}
func (n *NoDedupeBankAdapter) Name() string               { return "no_dedupe_bank" }
func (n *NoDedupeBankAdapter) MethodType() MethodType     { return BANK_DEBIT }
func (n *NoDedupeBankAdapter) Capabilities() Capabilities { return n.translator.Capabilities() }
func (n *NoDedupeBankAdapter) Charge(request ChargeRequest) (PaymentResult, error) {
	if err := n.Capabilities().RequireCurrency(n.Name(), request.Amount.Currency); err != nil {
		return PaymentResult{}, err
	}
	if !request.Capture {
		if err := n.Capabilities().Require(n.Name(), SEPARATE_CAPTURE); err != nil {
			return PaymentResult{}, err
		}
	}
	trace := n.api.SubmitDebit(request.Method.Token(), request.Amount.PlainString())
	n.debited[trace] = request.Amount
	return PaymentResult{request.Reference, trace, PENDING, request.Amount, NONE, ""}, nil
}
func (n *NoDedupeBankAdapter) Capture(ref string, amount Money) (PaymentResult, error) {
	return n.translator.Capture(ref, amount)
}
func (n *NoDedupeBankAdapter) Refund(ref string, amount Money) (PaymentResult, error) {
	if amount.Compare(n.debited[ref]) > 0 {
		return PaymentResult{"", ref, DECLINED, amount, INVALID_AMOUNT, ""}, nil
	}
	return PaymentResult{"", ref, PENDING, amount, NONE, ""}, nil
}
func (n *NoDedupeBankAdapter) TranslateEvent(raw string) (PaymentEvent, bool) {
	return n.translator.TranslateEvent(raw)
}

func TestConformancePassesForEveryRealAdapter(t *testing.T) {
	for _, target := range targets() {
		report := RunConformance(target)
		check(t, fmt.Sprintf("conformance passes: %s %v", report.Adapter, report.Failures), report.Passed())
		check(t, "conformance runs 19+ checks: "+report.Adapter, report.Checks >= 19)
	}
}

func TestConformanceHasTeeth(t *testing.T) {
	t.Run("lossy mapping", func(t *testing.T) {
		api := NewFakeCardNetworkApi()
		lossy := RunConformance(ConformanceTarget{&LossyCardAdapter{NewCardAdapter(api)}, USD,
			CardMethod{"tok_visa_4242", "visa"}, CardMethod{"tok_insufficient", "visa"},
			func() []string { return nil }, api.VendorCalls, api.ChargesCreated})
		check(t, "lossy mapping fails conformance", !lossy.Passed())
		check(t, "lossy mapping fails on the reason check",
			slices.Contains(lossy.Failures, "lossy_card: insufficient funds maps to INSUFFICIENT_FUNDS"))
	})
	t.Run("missing dedupe", func(t *testing.T) {
		api := NewFakeBankDebitApi()
		noDedupe := RunConformance(ConformanceTarget{newNoDedupeBankAdapter(api), USD,
			BankDebitMethod{"acct_ok", "m1"}, BankDebitMethod{"acct_nsf", "m2"},
			api.DrainEvents, api.VendorCalls, api.ChargesCreated})
		check(t, "missing dedupe fails conformance", !noDedupe.Passed())
		check(t, "missing dedupe fails on the one-vendor-charge check",
			slices.Contains(noDedupe.Failures, "no_dedupe_bank: same reference creates exactly one vendor charge"))
	})
}

func TestCodeMappings(t *testing.T) {
	cases := []struct {
		name string
		got  DeclineReason
		want DeclineReason
	}{
		{"card 51 -> INSUFFICIENT_FUNDS", CardMapCode("51"), INSUFFICIENT_FUNDS},
		{"card 05 -> DO_NOT_HONOR", CardMapCode("05"), DO_NOT_HONOR},
		{"card 54 -> EXPIRED_INSTRUMENT", CardMapCode("54"), EXPIRED_INSTRUMENT},
		{"card 59 -> SUSPECTED_FRAUD", CardMapCode("59"), SUSPECTED_FRAUD},
		{"card 91 -> ISSUER_UNAVAILABLE", CardMapCode("91"), ISSUER_UNAVAILABLE},
		{"card 1A -> AUTHENTICATION_REQUIRED", CardMapCode("1A"), AUTHENTICATION_REQUIRED},
		{"unmapped card code -> UNKNOWN, never NONE", CardMapCode("N7"), UNKNOWN},
		{"R01 -> INSUFFICIENT_FUNDS", BankMapCode("R01"), INSUFFICIENT_FUNDS},
		{"R02 -> INVALID_ACCOUNT", BankMapCode("R02"), INVALID_ACCOUNT},
		{"R10 -> UNAUTHORIZED_DEBIT", BankMapCode("R10"), UNAUTHORIZED_DEBIT},
		{"unmapped return code -> UNKNOWN", BankMapCode("R99"), UNKNOWN},
		{"wallet INSTRUMENT_DECLINED -> DO_NOT_HONOR", WalletMapIssue("INSTRUMENT_DECLINED"), DO_NOT_HONOR},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { check(t, c.name, c.got == c.want) })
	}
}

func TestRetryAdviceTravelsWithTheReason(t *testing.T) {
	check(t, "fraud is DO_NOT_RETRY", SUSPECTED_FRAUD.Advice() == DO_NOT_RETRY)
	check(t, "issuer unavailable is RETRY_NOW", ISSUER_UNAVAILABLE.Advice() == RETRY_NOW)
	check(t, "unknown is DO_NOT_RETRY (fail safe)", UNKNOWN.Advice() == DO_NOT_RETRY)
	check(t, "SCA soft decline is RETRY_WITH_CUSTOMER", AUTHENTICATION_REQUIRED.Advice() == RETRY_WITH_CUSTOMER)
}

func TestWireFormatQuirksStayInsideAdapters(t *testing.T) {
	rig := NewRig()
	pay(t, rig.Service, charge("w1", "4500", JPY, CardMethod{"tok_visa_4242", "visa"}, true))
	check(t, "card sends JPY as minor units with ISO numeric 392", rig.Card.LastWire() == "amount=4500 currency=392")
	pay(t, rig.Service, ChargeRequest{"w2", USD45, BankDebitMethod{"acct_ok", "m"}, true})
	check(t, "bank sends a decimal string", rig.Bank.LastWire() == "amount=45.00")
	pay(t, rig.Service, charge("w3", "19.99", EUR, WalletMethod{"wal_ok", "wallet"}, false))
	check(t, "wallet sends lowercase currency and AUTHORIZE intent", rig.Wallet.LastWire() == "amount=1999 currency=eur intent=AUTHORIZE")
}

func TestOneVocabularyForNeedsTheCustomer(t *testing.T) {
	rig := NewRig()
	sca := pay(t, rig.Service, ChargeRequest{"s1", USD45, CardMethod{"tok_sca_required", "visa"}, true})
	wal := pay(t, rig.Service, ChargeRequest{"s2", USD45, WalletMethod{"wal_action", "wallet"}, true})
	check(t, "card 1A is REQUIRES_ACTION", sca.Status == REQUIRES_ACTION)
	check(t, "wallet payer action is REQUIRES_ACTION", wal.Status == REQUIRES_ACTION)
	rig.Wallet.PayerAbandons(wal.ProcessorRef)
	for _, raw := range rig.Wallet.DrainEvents() {
		rig.Service.OnRawEvent(WALLET, raw)
	}
	check(t, "abandoned wallet approval ends FAILED", lookup(t, rig.Service, wal.ProcessorRef).Status == FAILED)
}

func TestAsyncConfirmationThroughTheService(t *testing.T) {
	rig := NewRig()
	debit := pay(t, rig.Service, ChargeRequest{"a1", USD45, BankDebitMethod{"acct_closed", "m"}, true})
	check(t, "bank debit starts PENDING", debit.Status == PENDING)
	raw := rig.Bank.DrainEvents()
	check(t, "bank emits one return event", len(raw) == 1 && raw[0] == "ach|returned|ach_000001|R02")
	applied, ok := rig.Service.OnRawEvent(BANK_DEBIT, raw[0])
	check(t, "return event applied -> FAILED INVALID_ACCOUNT", ok && applied.Status == FAILED && applied.Reason == INVALID_ACCOUNT)
	_, again := rig.Service.OnRawEvent(BANK_DEBIT, raw[0])
	check(t, "redelivered event on a final payment is ignored", !again)
	_, unknown := rig.Service.OnRawEvent(BANK_DEBIT, "ach|settled|ach_999999")
	check(t, "event for an unknown reference is ignored", !unknown)
	_, cardEvent := NewCardAdapter(NewFakeCardNetworkApi()).TranslateEvent("ach|settled|x")
	check(t, "card adapter has no events to translate", !cardEvent)
}

func TestAdapterSuppliesTheDedupeTheRailLacks(t *testing.T) {
	api := NewFakeBankDebitApi()
	api.SubmitDebit("acct_ok", "45.00")
	api.SubmitDebit("acct_ok", "45.00")
	check(t, "raw rail double-debits on resubmit", api.ChargesCreated() == 2)
	adapter := NewBankDebitAdapter(NewFakeBankDebitApi())
	r := ChargeRequest{"d1", USD45, BankDebitMethod{"acct_ok", "m"}, true}
	first, err1 := adapter.Charge(r)
	second, err2 := adapter.Charge(r)
	check(t, "adapter returns the same trace on resubmit", err1 == nil && err2 == nil && first.ProcessorRef == second.ProcessorRef)
}

func TestCapabilityRefusals(t *testing.T) {
	rig := NewRig()
	_, err := NewBankDebitAdapter(rig.Bank).Capture("ach_000001", USD45)
	checkErr(t, "bank capture is unsupported", err, ErrUnsupportedCapability)
	_, err = rig.Service.Pay(charge("c1", "100", JPY, WalletMethod{"wal_ok", "w"}, true))
	checkErr(t, "JPY on wallet is unsupported", err, ErrUnsupportedCapability)
	check(t, "refused JPY wallet charge never reached the vendor", rig.Wallet.VendorCalls() == 0)
	auth := pay(t, rig.Service, charge("c2", "100.00", USD, CardMethod{"tok_visa_4242", "visa"}, false))
	card, _ := rig.Service.ProcessorFor(CARD)
	cap, err := card.Capture(auth.ProcessorRef, mustParse("120.00", USD))
	check(t, "card over-capture is DECLINED INVALID_AMOUNT", err == nil && cap.Status == DECLINED && cap.Reason == INVALID_AMOUNT)
	flags := []Capability{REFUND}
	caps := NewCapabilities(flags, []Currency{USD})
	flags[0] = SEPARATE_CAPTURE
	check(t, "capabilities is a defensive copy", caps.Supports(REFUND) && !caps.Supports(SEPARATE_CAPTURE) && len(NewCapabilities(nil, []Currency{USD}).Flags()) == 0)
}

func TestConstructionRules(t *testing.T) {
	_, err := NewChargeRequest("z", OfMinor(0, USD), CardMethod{"t", "visa"}, true)
	checkErr(t, "zero amount rejected", err, ErrInvalidArgument)
	_, err = NewCardAdapter(NewFakeCardNetworkApi()).Charge(ChargeRequest{"x", USD45, WalletMethod{"wal_ok", "w"}, true})
	checkErr(t, "wrong method type rejected by adapter", err, ErrInvalidArgument)
	s := NewPaymentService()
	mustOK(s.Register(NewCardAdapter(NewFakeCardNetworkApi())))
	checkErr(t, "one processor per method type", s.Register(NewCardAdapter(NewFakeCardNetworkApi())), ErrIllegalState)
	check(t, "Money plain string USD", USD45.PlainString() == "45.00")
	check(t, "Money plain string JPY", mustParse("4500", JPY).PlainString() == "4500")
	_, err = USD45.Plus(mustParse("1.00", EUR))
	checkErr(t, "Money rejects currency mix", err, ErrCurrencyMismatch)
}
