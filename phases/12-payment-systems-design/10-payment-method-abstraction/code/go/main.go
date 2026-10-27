// Lesson: phases/12-payment-systems-design/10-payment-method-abstraction/docs/en.md
// Payment Method Abstraction and Processor Adapters
// Run:  go run .
// Test: go test ./...
// Sources: ISO 8583 field 39 response codes; NACHA Operating Rules (return reason codes R01-R85);
// ISO 4217 numeric currency codes; EMVCo 3-D Secure 2 (soft decline). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidArgument is Java's IllegalArgumentException: zero amounts, the wrong instrument for an adapter.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIllegalState is Java's IllegalStateException: registering two processors for one method type.
	ErrIllegalState = errors.New("illegal state")
	// ErrUnsupportedCapability is Java's UnsupportedCapabilityException: a flag or currency the adapter did not declare.
	ErrUnsupportedCapability = errors.New("unsupported capability")
)

// ---------------------------------------------------------------------------------------------
// The normalized vocabulary. Product code sees only these types, never a vendor's raw codes.
// ---------------------------------------------------------------------------------------------

type MethodType int

const (
	CARD MethodType = iota
	BANK_DEBIT
	WALLET
)

func (t MethodType) String() string { return [...]string{"CARD", "BANK_DEBIT", "WALLET"}[t] }

// PaymentMethod is the sealed interface: Go has no sealed types, so the unexported marker method
// keeps the set of instruments closed to this package.
type PaymentMethod interface {
	Type() MethodType
	Token() string
	paymentMethod()
}

type CardMethod struct{ CardToken, Network string }
type BankDebitMethod struct{ AccountToken, MandateID string }
type WalletMethod struct{ WalletToken, Provider string }

func (m CardMethod) Type() MethodType      { return CARD }
func (m CardMethod) Token() string         { return m.CardToken }
func (m CardMethod) paymentMethod()        {}
func (m BankDebitMethod) Type() MethodType { return BANK_DEBIT }
func (m BankDebitMethod) Token() string    { return m.AccountToken }
func (m BankDebitMethod) paymentMethod()   {}
func (m WalletMethod) Type() MethodType    { return WALLET }
func (m WalletMethod) Token() string       { return m.WalletToken }
func (m WalletMethod) paymentMethod()      {}

type Capability int

const (
	SEPARATE_CAPTURE Capability = iota
	PARTIAL_CAPTURE
	REFUND
	PARTIAL_REFUND
	ASYNC_CONFIRMATION
	CUSTOMER_ACTION
)

var capabilityNames = [...]string{"SEPARATE_CAPTURE", "PARTIAL_CAPTURE", "REFUND", "PARTIAL_REFUND", "ASYNC_CONFIRMATION", "CUSTOMER_ACTION"}

func (c Capability) String() string { return capabilityNames[c] }

type PaymentStatus int

const (
	AUTHORIZED PaymentStatus = iota
	SUCCEEDED
	PENDING
	REQUIRES_ACTION
	DECLINED
	FAILED
)

func (s PaymentStatus) String() string {
	return [...]string{"AUTHORIZED", "SUCCEEDED", "PENDING", "REQUIRES_ACTION", "DECLINED", "FAILED"}[s]
}

func (s PaymentStatus) IsFinal() bool { return s == SUCCEEDED || s == DECLINED || s == FAILED }

type RetryAdvice int

const (
	NOT_APPLICABLE RetryAdvice = iota
	RETRY_NOW
	RETRY_LATER
	RETRY_WITH_CUSTOMER
	DO_NOT_RETRY
)

func (a RetryAdvice) String() string {
	return [...]string{"NOT_APPLICABLE", "RETRY_NOW", "RETRY_LATER", "RETRY_WITH_CUSTOMER", "DO_NOT_RETRY"}[a]
}

type DeclineReason int

const (
	NONE DeclineReason = iota
	INSUFFICIENT_FUNDS
	DO_NOT_HONOR
	ISSUER_UNAVAILABLE
	AUTHENTICATION_REQUIRED
	EXPIRED_INSTRUMENT
	INVALID_ACCOUNT
	SUSPECTED_FRAUD
	UNAUTHORIZED_DEBIT
	INVALID_AMOUNT
	UNKNOWN
)

var declineReasonNames = [...]string{"NONE", "INSUFFICIENT_FUNDS", "DO_NOT_HONOR", "ISSUER_UNAVAILABLE", "AUTHENTICATION_REQUIRED",
	"EXPIRED_INSTRUMENT", "INVALID_ACCOUNT", "SUSPECTED_FRAUD", "UNAUTHORIZED_DEBIT", "INVALID_AMOUNT", "UNKNOWN"}

// declineAdvice is the data each Java enum constant carries: the retry advice travels with the reason.
var declineAdvice = [...]RetryAdvice{NOT_APPLICABLE, RETRY_LATER, RETRY_LATER, RETRY_NOW, RETRY_WITH_CUSTOMER,
	DO_NOT_RETRY, DO_NOT_RETRY, DO_NOT_RETRY, DO_NOT_RETRY, DO_NOT_RETRY, DO_NOT_RETRY}

func (r DeclineReason) String() string      { return declineReasonNames[r] }
func (r DeclineReason) Advice() RetryAdvice { return declineAdvice[r] }

type ChargeRequest struct {
	Reference string
	Amount    Money
	Method    PaymentMethod
	Capture   bool
}

func NewChargeRequest(reference string, amount Money, method PaymentMethod, capture bool) (ChargeRequest, error) {
	if method == nil {
		return ChargeRequest{}, fmt.Errorf("%w: method", ErrInvalidArgument)
	}
	if !amount.IsPositive() {
		return ChargeRequest{}, fmt.Errorf("%w: amount must be positive: %v", ErrInvalidArgument, amount)
	}
	return ChargeRequest{reference, amount, method, capture}, nil
}

// PaymentResult is what every adapter returns. RawCode is for logs and support, never for branching.
// Reference is empty on capture and refund results, where Java passed null.
type PaymentResult struct {
	Reference    string
	ProcessorRef string
	Status       PaymentStatus
	Amount       Money
	Reason       DeclineReason
	RawCode      string
}

func (r PaymentResult) Advice() RetryAdvice { return r.Reason.Advice() }

// PaymentEvent is an asynchronous outcome (bank return, wallet approval) translated into the same vocabulary.
type PaymentEvent struct {
	ProcessorRef string
	Status       PaymentStatus
	Reason       DeclineReason
	RawCode      string
}

// Capabilities is a defensive copy of the flags and currencies an adapter declares.
type Capabilities struct {
	flags      map[Capability]bool
	currencies map[Currency]bool
}

func NewCapabilities(flags []Capability, currencies []Currency) Capabilities {
	c := Capabilities{flags: map[Capability]bool{}, currencies: map[Currency]bool{}}
	for _, f := range flags {
		c.flags[f] = true
	}
	for _, cur := range currencies {
		c.currencies[cur] = true
	}
	return c
}

func (c Capabilities) Supports(cap Capability) bool { return c.flags[cap] }

// Flags lists the declared capabilities in declaration order, like Java's EnumSet iteration.
func (c Capabilities) Flags() []Capability {
	out := []Capability{}
	for i := range capabilityNames {
		if c.flags[Capability(i)] {
			out = append(out, Capability(i))
		}
	}
	return out
}

func (c Capabilities) Require(adapter string, cap Capability) error {
	if !c.Supports(cap) {
		return fmt.Errorf("%w: %s does not support %v", ErrUnsupportedCapability, adapter, cap)
	}
	return nil
}

func (c Capabilities) RequireCurrency(adapter string, cur Currency) error {
	if !c.currencies[cur] {
		return fmt.Errorf("%w: %s does not support currency %s", ErrUnsupportedCapability, adapter, cur.Code)
	}
	return nil
}

// formatFlags renders [A, B, C] the way Java prints an EnumSet.
func formatFlags(flags []Capability) string {
	names := make([]string, len(flags))
	for i, f := range flags {
		names[i] = f.String()
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// PaymentProcessor is the port. One interface for every rail; adapters implement it, product code depends on it.
type PaymentProcessor interface {
	Name() string
	MethodType() MethodType
	Capabilities() Capabilities
	Charge(request ChargeRequest) (PaymentResult, error)
	Capture(processorRef string, amount Money) (PaymentResult, error)
	Refund(processorRef string, amount Money) (PaymentResult, error)
	TranslateEvent(rawEvent string) (PaymentEvent, bool)
}

// ---------------------------------------------------------------------------------------------
// Fake vendors. Each has the quirks of its rail; none of them leak past its adapter.
// ---------------------------------------------------------------------------------------------

// vendorRow is {authorized, captured, refunded} in minor units: the Java long[3].
type vendorRow struct{ authorized, captured, refunded int64 }

type AuthResponse struct {
	ResponseCode    string
	AuthID          string
	AmountMinor     int64
	CurrencyNumeric string
}

// FakeCardNetworkApi: ISO 8583-style response codes, ISO 4217 numeric currency, native idempotency keys.
type FakeCardNetworkApi struct {
	byIdempotencyKey map[string]AuthResponse
	ledger           map[string]*vendorRow
	vendorCalls      int
	chargesCreated   int
	lastWire         string
}

func NewFakeCardNetworkApi() *FakeCardNetworkApi {
	return &FakeCardNetworkApi{byIdempotencyKey: map[string]AuthResponse{}, ledger: map[string]*vendorRow{}}
}

func (a *FakeCardNetworkApi) VendorCalls() int    { return a.vendorCalls }
func (a *FakeCardNetworkApi) ChargesCreated() int { return a.chargesCreated }

// LastWire is what went over the wire on the last authorize, for tests that pin the quirk translation.
func (a *FakeCardNetworkApi) LastWire() string { return a.lastWire }

var cardTokenCodes = map[string]string{
	"tok_insufficient": "51", "tok_do_not_honor": "05", "tok_expired": "54", "tok_fraud": "59",
	"tok_bad_number": "14", "tok_issuer_down": "91", "tok_sca_required": "1A", "tok_weird": "N7",
}

func (a *FakeCardNetworkApi) Authorize(idempotencyKey, panToken string, amountMinor int64, currencyNumeric string, capture bool) AuthResponse {
	a.vendorCalls++
	a.lastWire = fmt.Sprintf("amount=%d currency=%s", amountMinor, currencyNumeric)
	if prior, ok := a.byIdempotencyKey[idempotencyKey]; ok {
		return prior
	}
	a.chargesCreated++
	code, ok := cardTokenCodes[panToken]
	if !ok {
		code = "00"
	}
	authID := fmt.Sprintf("auth_%06d", a.chargesCreated)
	if code == "00" {
		captured := int64(0)
		if capture {
			captured = amountMinor
		}
		a.ledger[authID] = &vendorRow{amountMinor, captured, 0}
	}
	response := AuthResponse{code, authID, amountMinor, currencyNumeric}
	a.byIdempotencyKey[idempotencyKey] = response
	return response
}

func (a *FakeCardNetworkApi) Capture(authID string, amountMinor int64) string {
	a.vendorCalls++
	row, ok := a.ledger[authID]
	if !ok {
		return "25" // unable to locate record
	}
	if row.captured+amountMinor > row.authorized {
		return "13" // invalid amount
	}
	row.captured += amountMinor
	return "00"
}

func (a *FakeCardNetworkApi) Refund(authID string, amountMinor int64) string {
	a.vendorCalls++
	row, ok := a.ledger[authID]
	if !ok {
		return "25"
	}
	if row.refunded+amountMinor > row.captured {
		return "13"
	}
	row.refunded += amountMinor
	return "00"
}

// FakeBankDebitApi: ACH-style bank debit: decimal-string amounts, USD only, no idempotency, outcome arrives days later.
type FakeBankDebitApi struct {
	outbox         []string
	vendorCalls    int
	chargesCreated int
	traceSeq       int
	lastWire       string
}

func NewFakeBankDebitApi() *FakeBankDebitApi { return &FakeBankDebitApi{} }

func (a *FakeBankDebitApi) VendorCalls() int    { return a.vendorCalls }
func (a *FakeBankDebitApi) ChargesCreated() int { return a.chargesCreated }
func (a *FakeBankDebitApi) LastWire() string    { return a.lastWire }

var bankTokenReturns = map[string]string{
	"acct_nsf": "R01", "acct_closed": "R02", "acct_no_account": "R03", "acct_unauthorized": "R10", "acct_weird": "R99",
}

// SubmitDebit returns a trace number. Submitting the same debit twice creates two debits: the rail has no dedupe.
func (a *FakeBankDebitApi) SubmitDebit(accountToken, amountDecimal string) string {
	a.vendorCalls++
	a.lastWire = "amount=" + amountDecimal
	a.chargesCreated++
	a.traceSeq++
	trace := fmt.Sprintf("ach_%06d", a.traceSeq)
	if returnCode, returned := bankTokenReturns[accountToken]; returned {
		a.outbox = append(a.outbox, "ach|returned|"+trace+"|"+returnCode)
	} else {
		a.outbox = append(a.outbox, "ach|settled|"+trace)
	}
	return trace
}

func (a *FakeBankDebitApi) SubmitCredit(originalTrace, amountDecimal string) string {
	a.vendorCalls++
	a.traceSeq++
	return fmt.Sprintf("ach_%06d", a.traceSeq)
}

// DrainEvents is what the bank's file or webhook delivers later.
func (a *FakeBankDebitApi) DrainEvents() []string {
	out := append([]string{}, a.outbox...)
	a.outbox = a.outbox[:0]
	return out
}

type WalletOrder struct{ ID, Status, Issue string }

// FakeWalletApi: lowercase currency, status words instead of codes, payer approval out of band.
type FakeWalletApi struct {
	byRequestID    map[string]WalletOrder
	ledger         map[string]*vendorRow
	outbox         []string
	vendorCalls    int
	chargesCreated int
	lastWire       string
}

func NewFakeWalletApi() *FakeWalletApi {
	return &FakeWalletApi{byRequestID: map[string]WalletOrder{}, ledger: map[string]*vendorRow{}}
}

func (a *FakeWalletApi) VendorCalls() int    { return a.vendorCalls }
func (a *FakeWalletApi) ChargesCreated() int { return a.chargesCreated }
func (a *FakeWalletApi) LastWire() string    { return a.lastWire }

func (a *FakeWalletApi) CreateOrder(requestID, walletToken string, amountMinor int64, currencyLower, intent string) WalletOrder {
	a.vendorCalls++
	a.lastWire = fmt.Sprintf("amount=%d currency=%s intent=%s", amountMinor, currencyLower, intent)
	if prior, ok := a.byRequestID[requestID]; ok {
		return prior
	}
	a.chargesCreated++
	id := fmt.Sprintf("wal_%06d", a.chargesCreated)
	var order WalletOrder
	switch walletToken {
	case "wal_nsf":
		order = WalletOrder{id, "DECLINED", "INSUFFICIENT_FUNDS"}
	case "wal_declined":
		order = WalletOrder{id, "DECLINED", "INSTRUMENT_DECLINED"}
	case "wal_action":
		order = WalletOrder{id, "PAYER_ACTION_REQUIRED", ""}
	default:
		if intent == "CAPTURE" {
			order = WalletOrder{id, "COMPLETED", ""}
		} else {
			order = WalletOrder{id, "APPROVED", ""}
		}
	}
	if order.Status != "DECLINED" {
		captured := int64(0)
		if intent == "CAPTURE" && order.Status == "COMPLETED" {
			captured = amountMinor
		}
		a.ledger[id] = &vendorRow{amountMinor, captured, 0}
	}
	a.byRequestID[requestID] = order
	return order
}

// CaptureOrder: the wallet captures the full authorized amount or nothing.
func (a *FakeWalletApi) CaptureOrder(orderID string, amountMinor int64) string {
	a.vendorCalls++
	row, ok := a.ledger[orderID]
	if !ok || row.captured != 0 || amountMinor != row.authorized {
		return "UNPROCESSABLE_ENTITY"
	}
	row.captured = amountMinor
	return "COMPLETED"
}

func (a *FakeWalletApi) RefundCapture(orderID string, amountMinor int64) string {
	a.vendorCalls++
	row, ok := a.ledger[orderID]
	if !ok || row.refunded+amountMinor > row.captured {
		return "UNPROCESSABLE_ENTITY"
	}
	row.refunded += amountMinor
	return "COMPLETED"
}

// PayerApproves: the payer finishes the out-of-band step in the wallet's own UI.
func (a *FakeWalletApi) PayerApproves(orderID string) {
	row := a.ledger[orderID]
	row.captured = row.authorized
	a.outbox = append(a.outbox, "wallet:PAYMENT.CAPTURE.COMPLETED:"+orderID)
}

func (a *FakeWalletApi) PayerAbandons(orderID string) {
	a.outbox = append(a.outbox, "wallet:PAYMENT.CAPTURE.DENIED:"+orderID)
}

func (a *FakeWalletApi) DrainEvents() []string {
	out := append([]string{}, a.outbox...)
	a.outbox = a.outbox[:0]
	return out
}

// ---------------------------------------------------------------------------------------------
// Adapters: the only place a vendor's vocabulary is spoken.
// ---------------------------------------------------------------------------------------------

func lookupReason(table map[string]DeclineReason, code string) DeclineReason {
	if r, ok := table[code]; ok {
		return r
	}
	return UNKNOWN
}

var CardResponseCodes = map[string]DeclineReason{
	"51": INSUFFICIENT_FUNDS, "05": DO_NOT_HONOR, "54": EXPIRED_INSTRUMENT, "59": SUSPECTED_FRAUD,
	"14": INVALID_ACCOUNT, "91": ISSUER_UNAVAILABLE, "1A": AUTHENTICATION_REQUIRED, "13": INVALID_AMOUNT,
}

var cardCaps = NewCapabilities(
	[]Capability{SEPARATE_CAPTURE, PARTIAL_CAPTURE, REFUND, PARTIAL_REFUND},
	[]Currency{USD, EUR, GBP, JPY})

type CardAdapter struct{ api *FakeCardNetworkApi }

func NewCardAdapter(api *FakeCardNetworkApi) *CardAdapter { return &CardAdapter{api} }

func (c *CardAdapter) Name() string               { return "card" }
func (c *CardAdapter) MethodType() MethodType     { return CARD }
func (c *CardAdapter) Capabilities() Capabilities { return cardCaps }

func CardMapCode(code string) DeclineReason { return lookupReason(CardResponseCodes, code) }

func (c *CardAdapter) Charge(request ChargeRequest) (PaymentResult, error) {
	card, ok := request.Method.(CardMethod)
	if !ok {
		return PaymentResult{}, fmt.Errorf("%w: card adapter given %v", ErrInvalidArgument, request.Method.Type())
	}
	if err := cardCaps.RequireCurrency(c.Name(), request.Amount.Currency); err != nil {
		return PaymentResult{}, err
	}
	if !request.Capture {
		if err := cardCaps.Require(c.Name(), SEPARATE_CAPTURE); err != nil {
			return PaymentResult{}, err
		}
	}
	r := c.api.Authorize(request.Reference, card.Token(), request.Amount.MinorUnits, request.Amount.Currency.Numeric, request.Capture)
	if r.ResponseCode == "00" {
		status := AUTHORIZED
		if request.Capture {
			status = SUCCEEDED
		}
		return PaymentResult{request.Reference, r.AuthID, status, request.Amount, NONE, "00"}, nil
	}
	reason := CardMapCode(r.ResponseCode)
	status := DECLINED
	if reason == AUTHENTICATION_REQUIRED {
		status = REQUIRES_ACTION
	}
	return PaymentResult{request.Reference, r.AuthID, status, request.Amount, reason, r.ResponseCode}, nil
}

func (c *CardAdapter) Capture(processorRef string, amount Money) (PaymentResult, error) {
	return c.settle(processorRef, amount, c.api.Capture(processorRef, amount.MinorUnits)), nil
}

func (c *CardAdapter) Refund(processorRef string, amount Money) (PaymentResult, error) {
	return c.settle(processorRef, amount, c.api.Refund(processorRef, amount.MinorUnits)), nil
}

func (c *CardAdapter) settle(processorRef string, amount Money, code string) PaymentResult {
	if code == "00" {
		return PaymentResult{"", processorRef, SUCCEEDED, amount, NONE, code}
	}
	return PaymentResult{"", processorRef, DECLINED, amount, CardMapCode(code), code}
}

// TranslateEvent: card authorizations are synchronous; this rail has no confirmation events to translate.
func (c *CardAdapter) TranslateEvent(rawEvent string) (PaymentEvent, bool) {
	return PaymentEvent{}, false
}

var BankReturnCodes = map[string]DeclineReason{
	"R01": INSUFFICIENT_FUNDS, "R09": INSUFFICIENT_FUNDS, "R02": INVALID_ACCOUNT, "R03": INVALID_ACCOUNT,
	"R04": INVALID_ACCOUNT, "R16": DO_NOT_HONOR, "R10": UNAUTHORIZED_DEBIT, "R29": UNAUTHORIZED_DEBIT,
}

var bankCaps = NewCapabilities(
	[]Capability{REFUND, PARTIAL_REFUND, ASYNC_CONFIRMATION},
	[]Currency{USD})

type BankDebitAdapter struct {
	api         *FakeBankDebitApi
	byReference map[string]PaymentResult // the rail has no dedupe, so we do
	debited     map[string]Money
	refunded    map[string]Money
}

func NewBankDebitAdapter(api *FakeBankDebitApi) *BankDebitAdapter {
	return &BankDebitAdapter{api, map[string]PaymentResult{}, map[string]Money{}, map[string]Money{}}
}

func (b *BankDebitAdapter) Name() string               { return "bank_debit" }
func (b *BankDebitAdapter) MethodType() MethodType     { return BANK_DEBIT }
func (b *BankDebitAdapter) Capabilities() Capabilities { return bankCaps }

func BankMapCode(code string) DeclineReason { return lookupReason(BankReturnCodes, code) }

func (b *BankDebitAdapter) Charge(request ChargeRequest) (PaymentResult, error) {
	account, ok := request.Method.(BankDebitMethod)
	if !ok {
		return PaymentResult{}, fmt.Errorf("%w: bank adapter given %v", ErrInvalidArgument, request.Method.Type())
	}
	if err := bankCaps.RequireCurrency(b.Name(), request.Amount.Currency); err != nil {
		return PaymentResult{}, err
	}
	if !request.Capture {
		if err := bankCaps.Require(b.Name(), SEPARATE_CAPTURE); err != nil {
			return PaymentResult{}, err
		}
	}
	if prior, ok := b.byReference[request.Reference]; ok {
		return prior, nil
	}
	trace := b.api.SubmitDebit(account.Token(), request.Amount.PlainString())
	result := PaymentResult{request.Reference, trace, PENDING, request.Amount, NONE, ""}
	b.byReference[request.Reference] = result
	b.debited[trace] = request.Amount
	return result, nil
}

func (b *BankDebitAdapter) Capture(processorRef string, amount Money) (PaymentResult, error) {
	return PaymentResult{}, bankCaps.Require(b.Name(), SEPARATE_CAPTURE) // always refused: the flag is not declared
}

func (b *BankDebitAdapter) Refund(processorRef string, amount Money) (PaymentResult, error) {
	if err := bankCaps.Require(b.Name(), REFUND); err != nil {
		return PaymentResult{}, err
	}
	original, ok := b.debited[processorRef]
	if !ok {
		return PaymentResult{}, fmt.Errorf("%w: unknown debit %s", ErrInvalidArgument, processorRef)
	}
	already, ok := b.refunded[processorRef]
	if !ok {
		already = OfMinor(0, amount.Currency)
	}
	total, err := already.Plus(amount)
	if err != nil {
		return PaymentResult{}, err
	}
	if total.Compare(original) > 0 {
		return PaymentResult{"", processorRef, DECLINED, amount, INVALID_AMOUNT, "over_refund"}, nil
	}
	creditTrace := b.api.SubmitCredit(processorRef, amount.PlainString())
	b.refunded[processorRef] = total
	return PaymentResult{"", creditTrace, PENDING, amount, NONE, ""}, nil
}

func (b *BankDebitAdapter) TranslateEvent(rawEvent string) (PaymentEvent, bool) {
	parts := strings.Split(rawEvent, "|")
	if len(parts) < 3 || parts[0] != "ach" {
		return PaymentEvent{}, false
	}
	switch parts[1] {
	case "settled":
		return PaymentEvent{parts[2], SUCCEEDED, NONE, ""}, true
	case "returned":
		if len(parts) < 4 {
			return PaymentEvent{}, false
		}
		return PaymentEvent{parts[2], FAILED, BankMapCode(parts[3]), parts[3]}, true
	default:
		return PaymentEvent{}, false
	}
}

var WalletIssues = map[string]DeclineReason{
	"INSUFFICIENT_FUNDS": INSUFFICIENT_FUNDS, "INSTRUMENT_DECLINED": DO_NOT_HONOR, "PAYER_CANNOT_PAY": DO_NOT_HONOR,
}

var walletCaps = NewCapabilities(
	[]Capability{SEPARATE_CAPTURE, REFUND, PARTIAL_REFUND, CUSTOMER_ACTION},
	[]Currency{USD, EUR, GBP})

type WalletAdapter struct {
	api        *FakeWalletApi
	authorized map[string]Money
}

func NewWalletAdapter(api *FakeWalletApi) *WalletAdapter {
	return &WalletAdapter{api, map[string]Money{}}
}

func (w *WalletAdapter) Name() string               { return "wallet" }
func (w *WalletAdapter) MethodType() MethodType     { return WALLET }
func (w *WalletAdapter) Capabilities() Capabilities { return walletCaps }

func WalletMapIssue(issue string) DeclineReason { return lookupReason(WalletIssues, issue) }

func (w *WalletAdapter) Charge(request ChargeRequest) (PaymentResult, error) {
	wallet, ok := request.Method.(WalletMethod)
	if !ok {
		return PaymentResult{}, fmt.Errorf("%w: wallet adapter given %v", ErrInvalidArgument, request.Method.Type())
	}
	if err := walletCaps.RequireCurrency(w.Name(), request.Amount.Currency); err != nil {
		return PaymentResult{}, err
	}
	if !request.Capture {
		if err := walletCaps.Require(w.Name(), SEPARATE_CAPTURE); err != nil {
			return PaymentResult{}, err
		}
	}
	intent := "AUTHORIZE"
	if request.Capture {
		intent = "CAPTURE"
	}
	o := w.api.CreateOrder(request.Reference, wallet.Token(), request.Amount.MinorUnits, strings.ToLower(request.Amount.Currency.Code), intent)
	var status PaymentStatus
	switch o.Status {
	case "COMPLETED":
		status = SUCCEEDED
	case "APPROVED":
		status = AUTHORIZED
	case "PAYER_ACTION_REQUIRED":
		status = REQUIRES_ACTION
	default:
		status = DECLINED
	}
	if status == AUTHORIZED {
		w.authorized[o.ID] = request.Amount
	}
	reason := NONE
	if status == DECLINED {
		reason = WalletMapIssue(o.Issue)
	}
	raw := o.Status
	if o.Issue != "" {
		raw += "/" + o.Issue
	}
	return PaymentResult{request.Reference, o.ID, status, request.Amount, reason, raw}, nil
}

func (w *WalletAdapter) Capture(processorRef string, amount Money) (PaymentResult, error) {
	if err := walletCaps.Require(w.Name(), SEPARATE_CAPTURE); err != nil {
		return PaymentResult{}, err
	}
	if auth, ok := w.authorized[processorRef]; ok && amount.Compare(auth) != 0 {
		if err := walletCaps.Require(w.Name(), PARTIAL_CAPTURE); err != nil {
			return PaymentResult{}, err
		}
	}
	return w.outcome(processorRef, amount, w.api.CaptureOrder(processorRef, amount.MinorUnits)), nil
}

func (w *WalletAdapter) Refund(processorRef string, amount Money) (PaymentResult, error) {
	if err := walletCaps.Require(w.Name(), REFUND); err != nil {
		return PaymentResult{}, err
	}
	return w.outcome(processorRef, amount, w.api.RefundCapture(processorRef, amount.MinorUnits)), nil
}

func (w *WalletAdapter) outcome(processorRef string, amount Money, status string) PaymentResult {
	if status == "COMPLETED" {
		return PaymentResult{"", processorRef, SUCCEEDED, amount, NONE, status}
	}
	return PaymentResult{"", processorRef, DECLINED, amount, INVALID_AMOUNT, status}
}

func (w *WalletAdapter) TranslateEvent(rawEvent string) (PaymentEvent, bool) {
	parts := strings.Split(rawEvent, ":")
	if len(parts) != 3 || parts[0] != "wallet" {
		return PaymentEvent{}, false
	}
	switch parts[1] {
	case "PAYMENT.CAPTURE.COMPLETED":
		return PaymentEvent{parts[2], SUCCEEDED, NONE, parts[1]}, true
	case "PAYMENT.CAPTURE.DENIED":
		return PaymentEvent{parts[2], FAILED, DO_NOT_HONOR, parts[1]}, true
	default:
		return PaymentEvent{}, false
	}
}

// ---------------------------------------------------------------------------------------------
// The facade product code calls: route by method type, keep the latest normalized state.
// ---------------------------------------------------------------------------------------------

type PaymentService struct {
	processors     map[MethodType]PaymentProcessor
	byProcessorRef map[string]PaymentResult
}

func NewPaymentService() *PaymentService {
	return &PaymentService{map[MethodType]PaymentProcessor{}, map[string]PaymentResult{}}
}

func (s *PaymentService) Register(processor PaymentProcessor) error {
	if _, dup := s.processors[processor.MethodType()]; dup {
		return fmt.Errorf("%w: already registered: %v", ErrIllegalState, processor.MethodType())
	}
	s.processors[processor.MethodType()] = processor
	return nil
}

func (s *PaymentService) ProcessorFor(t MethodType) (PaymentProcessor, error) {
	p, ok := s.processors[t]
	if !ok {
		return nil, fmt.Errorf("%w: no processor for %v", ErrUnsupportedCapability, t)
	}
	return p, nil
}

func (s *PaymentService) Pay(request ChargeRequest) (PaymentResult, error) {
	p, err := s.ProcessorFor(request.Method.Type())
	if err != nil {
		return PaymentResult{}, err
	}
	result, err := p.Charge(request)
	if err != nil {
		return PaymentResult{}, err
	}
	s.byProcessorRef[result.ProcessorRef] = result
	return result, nil
}

// OnRawEvent feeds a raw notification to the adapter for its rail; applies it if the adapter recognizes it.
// The bool is false for an unrecognized event, an unknown reference, or a payment already decided.
func (s *PaymentService) OnRawEvent(t MethodType, rawEvent string) (PaymentResult, bool) {
	p, err := s.ProcessorFor(t)
	if err != nil {
		return PaymentResult{}, false
	}
	e, ok := p.TranslateEvent(rawEvent)
	if !ok {
		return PaymentResult{}, false
	}
	current, known := s.byProcessorRef[e.ProcessorRef]
	if !known || current.Status.IsFinal() {
		return PaymentResult{}, false
	}
	updated := PaymentResult{current.Reference, current.ProcessorRef, e.Status, current.Amount, e.Reason, e.RawCode}
	s.byProcessorRef[e.ProcessorRef] = updated
	return updated, true
}

func (s *PaymentService) Lookup(processorRef string) (PaymentResult, bool) {
	r, ok := s.byProcessorRef[processorRef]
	return r, ok
}

// ---------------------------------------------------------------------------------------------
// The conformance suite: one set of contract checks every adapter must pass.
// ---------------------------------------------------------------------------------------------

// ConformanceTarget is everything the suite needs to drive one adapter: its vendor's test tokens and counters.
type ConformanceTarget struct {
	Processor         PaymentProcessor
	Currency          Currency
	Approve           PaymentMethod
	InsufficientFunds PaymentMethod
	DrainEvents       func() []string
	VendorCalls       func() int
	ChargesCreated    func() int
}

type ConformanceReport struct {
	Adapter  string
	Checks   int
	Failures []string
}

func (r ConformanceReport) Passed() bool { return len(r.Failures) == 0 }

type ProcessorConformance struct {
	t        ConformanceTarget
	events   map[string]PaymentEvent
	failures []string
	checks   int
	seq      int
}

// RunConformance runs every contract check once against a fresh target. Empty failures means conformant.
func RunConformance(target ConformanceTarget) ConformanceReport {
	c := &ProcessorConformance{t: target, events: map[string]PaymentEvent{}, failures: []string{}}
	c.runAll()
	return ConformanceReport{target.Processor.Name(), c.checks, append([]string{}, c.failures...)}
}

func (c *ProcessorConformance) expect(name string, ok bool) {
	c.checks++
	if !ok {
		c.failures = append(c.failures, c.t.Processor.Name()+": "+name)
	}
}

func (c *ProcessorConformance) request(method PaymentMethod, amount string, capture bool) ChargeRequest {
	c.seq++
	return ChargeRequest{fmt.Sprintf("conf-%s-%d", c.t.Processor.Name(), c.seq), mustParse(amount, c.t.Currency), method, capture}
}

// finalStatus is the synchronous result, or the translated event if the adapter said PENDING.
func (c *ProcessorConformance) finalStatus(r PaymentResult) PaymentStatus {
	if r.Status != PENDING {
		return r.Status
	}
	for _, raw := range c.t.DrainEvents() {
		if e, ok := c.t.Processor.TranslateEvent(raw); ok {
			c.events[e.ProcessorRef] = e
		}
	}
	e, ok := c.events[r.ProcessorRef]
	if !ok {
		return PENDING
	}
	return e.Status
}

func (c *ProcessorConformance) finalReason(r PaymentResult) DeclineReason {
	if r.Status != PENDING {
		return r.Reason
	}
	c.finalStatus(r)
	e, ok := c.events[r.ProcessorRef]
	if !ok {
		return NONE
	}
	return e.Reason
}

// failsUnsupported runs the action and reports whether it returned ErrUnsupportedCapability and
// how many vendor calls it made on the way (the Java out-parameter int[]).
func (c *ProcessorConformance) failsUnsupported(action func() error) (bool, int) {
	before := c.t.VendorCalls()
	err := action()
	if !errors.Is(err, ErrUnsupportedCapability) {
		return false, -1
	}
	return true, c.t.VendorCalls() - before
}

// charge/capture/refund wrap the port for the suite. Java's checked calls could only fail by
// throwing out of the suite; here an unexpected error is recorded as a failure without counting
// as a check, so the check counts match the Java suite exactly.
func (c *ProcessorConformance) fail(step string, err error) {
	if err != nil {
		c.failures = append(c.failures, c.t.Processor.Name()+": "+step+" returned "+err.Error())
	}
}

func (c *ProcessorConformance) charge(r ChargeRequest) PaymentResult {
	res, err := c.t.Processor.Charge(r)
	c.fail("charge "+r.Reference, err)
	return res
}

func (c *ProcessorConformance) capture(ref string, amount Money) PaymentResult {
	res, err := c.t.Processor.Capture(ref, amount)
	c.fail("capture "+ref, err)
	return res
}

func (c *ProcessorConformance) refund(ref string, amount Money) PaymentResult {
	res, err := c.t.Processor.Refund(ref, amount)
	c.fail("refund "+ref, err)
	return res
}

func (c *ProcessorConformance) runAll() {
	p := c.t.Processor
	caps := p.Capabilities()
	async := caps.Supports(ASYNC_CONFIRMATION)

	// 1. Approval: synchronous success, or PENDING only if the adapter declares async confirmation.
	ok := c.request(c.t.Approve, "45.00", true)
	approved := c.charge(ok)
	c.expect("approve returns a processor reference", approved.ProcessorRef != "")
	c.expect("approve echoes the exact amount", approved.Amount == ok.Amount)
	if async {
		c.expect("approve status is SUCCEEDED, or PENDING iff ASYNC_CONFIRMATION", approved.Status == PENDING)
	} else {
		c.expect("approve status is SUCCEEDED, or PENDING iff ASYNC_CONFIRMATION", approved.Status == SUCCEEDED)
	}
	c.expect("approve eventually SUCCEEDED", c.finalStatus(approved) == SUCCEEDED)

	// 2. Insufficient funds normalizes to one reason and one retry advice, whatever the rail calls it.
	nsf := c.charge(c.request(c.t.InsufficientFunds, "45.00", true))
	nsfFinal := c.finalStatus(nsf)
	c.expect("insufficient funds ends DECLINED or FAILED", nsfFinal == DECLINED || nsfFinal == FAILED)
	c.expect("insufficient funds maps to INSUFFICIENT_FUNDS", c.finalReason(nsf) == INSUFFICIENT_FUNDS)
	c.expect("insufficient funds advises RETRY_LATER", c.finalReason(nsf).Advice() == RETRY_LATER)

	// 3. Idempotency: the same reference twice is one charge at the vendor, one processor reference.
	dup := c.request(c.t.Approve, "12.34", true)
	createdBefore := c.t.ChargesCreated()
	first := c.charge(dup)
	second := c.charge(dup)
	c.expect("same reference returns the same processor reference", first.ProcessorRef == second.ProcessorRef)
	c.expect("same reference creates exactly one vendor charge", c.t.ChargesCreated()-createdBefore == 1)

	// 4. Currency honesty: an undeclared currency is refused before the vendor is called.
	chf := ChargeRequest{"conf-chf-" + p.Name(), mustParse("10.00", CHF), c.t.Approve, true}
	refused, delta := c.failsUnsupported(func() error { _, err := p.Charge(chf); return err })
	c.expect("undeclared currency fails with ErrUnsupportedCapability", refused)
	c.expect("undeclared currency never reaches the vendor", delta == 0)

	// 5. Separate capture: works if declared, refused before the vendor if not.
	if caps.Supports(SEPARATE_CAPTURE) {
		auth := c.charge(c.request(c.t.Approve, "100.00", false))
		c.expect("authorize-only returns AUTHORIZED", auth.Status == AUTHORIZED)
		partial := mustParse("60.00", c.t.Currency)
		if caps.Supports(PARTIAL_CAPTURE) {
			cap := c.capture(auth.ProcessorRef, partial)
			c.expect("partial capture SUCCEEDED for the partial amount", cap.Status == SUCCEEDED && cap.Amount == partial)
		} else {
			refused, d := c.failsUnsupported(func() error { _, err := p.Capture(auth.ProcessorRef, partial); return err })
			c.expect("undeclared partial capture is refused", refused)
			c.expect("undeclared partial capture never reaches the vendor", d == 0)
			cap := c.capture(auth.ProcessorRef, auth.Amount)
			c.expect("full capture SUCCEEDED", cap.Status == SUCCEEDED)
		}
	} else {
		refused, d := c.failsUnsupported(func() error { _, err := p.Charge(c.request(c.t.Approve, "100.00", false)); return err })
		c.expect("undeclared separate capture is refused", refused)
		c.expect("undeclared separate capture never reaches the vendor", d == 0)
	}

	// 6. Refunds: a declared partial refund works for exactly the requested amount; over-refund is declined.
	if caps.Supports(PARTIAL_REFUND) {
		half := mustParse("22.50", c.t.Currency)
		refund := c.refund(approved.ProcessorRef, half)
		c.expect("partial refund is accepted", refund.Status == SUCCEEDED || refund.Status == PENDING)
		c.expect("partial refund is for the requested amount", refund.Amount == half)
		over := c.refund(approved.ProcessorRef, mustParse("30.00", c.t.Currency))
		c.expect("refund beyond the captured amount is DECLINED", over.Status == DECLINED)
	}

	// 7. Events: unknown or foreign notifications are ignored, never failed on.
	_, recognized := p.TranslateEvent("totally|unrelated|payload")
	c.expect("unknown event is ignored", !recognized)
	_, recognized = p.TranslateEvent("")
	c.expect("empty event is ignored", !recognized)

	// 8. Declines always carry a reason and the raw code, so support can trace them without product code branching on it.
	c.expect("decline carries a non-NONE reason", nsf.Status == PENDING || nsf.Reason != NONE)
}

// ---------------------------------------------------------------------------------------------
// Demo.
// ---------------------------------------------------------------------------------------------

type Rig struct {
	Service *PaymentService
	Card    *FakeCardNetworkApi
	Bank    *FakeBankDebitApi
	Wallet  *FakeWalletApi
}

func NewRig() *Rig {
	card, bank, wallet := NewFakeCardNetworkApi(), NewFakeBankDebitApi(), NewFakeWalletApi()
	service := NewPaymentService()
	mustOK(service.Register(NewCardAdapter(card)))
	mustOK(service.Register(NewBankDebitAdapter(bank)))
	mustOK(service.Register(NewWalletAdapter(wallet)))
	return &Rig{service, card, bank, wallet}
}

func targets() []ConformanceTarget {
	card, bank, wallet := NewFakeCardNetworkApi(), NewFakeBankDebitApi(), NewFakeWalletApi()
	return []ConformanceTarget{
		{NewCardAdapter(card), USD, CardMethod{"tok_visa_4242", "visa"}, CardMethod{"tok_insufficient", "visa"},
			func() []string { return nil }, card.VendorCalls, card.ChargesCreated},
		{NewBankDebitAdapter(bank), USD, BankDebitMethod{"acct_ok", "mandate_1"}, BankDebitMethod{"acct_nsf", "mandate_2"},
			bank.DrainEvents, bank.VendorCalls, bank.ChargesCreated},
		{NewWalletAdapter(wallet), USD, WalletMethod{"wal_ok", "wallet"}, WalletMethod{"wal_nsf", "wallet"},
			wallet.DrainEvents, wallet.VendorCalls, wallet.ChargesCreated},
	}
}

// must unwraps a (value, error) pair in demos, where an error is a bug.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func charge(reference, amount string, currency Currency, method PaymentMethod, capture bool) ChargeRequest {
	return must(NewChargeRequest(reference, mustParse(amount, currency), method, capture))
}

func show(label string, r PaymentResult) {
	raw := r.RawCode
	if raw == "" {
		raw = "-"
	}
	fmt.Printf("%-34s -> %-15v reason=%-24v advice=%-19v raw=%s\n", label, r.Status, r.Reason, r.Advice(), raw)
}

func main() {
	rig := NewRig()
	svc := rig.Service

	fmt.Println("== One interface, three rails: the same 45.00 USD charge ==")
	show("card  tok_visa_4242", must(svc.Pay(charge("order-1", "45.00", USD, CardMethod{"tok_visa_4242", "visa"}, true))))
	ach := must(svc.Pay(charge("order-2", "45.00", USD, BankDebitMethod{"acct_ok", "mandate_7"}, true)))
	show("bank  acct_ok", ach)
	show("wallet wal_ok", must(svc.Pay(charge("order-3", "45.00", USD, WalletMethod{"wal_ok", "wallet"}, true))))

	fmt.Println("\n== Three vendors say 'no money' three ways; product code sees one reason ==")
	show("card  ISO 8583 code 51", must(svc.Pay(charge("order-4", "45.00", USD, CardMethod{"tok_insufficient", "visa"}, true))))
	achNsf := must(svc.Pay(charge("order-5", "45.00", USD, BankDebitMethod{"acct_nsf", "mandate_8"}, true)))
	show("bank  submitted (outcome later)", achNsf)
	for _, raw := range rig.Bank.DrainEvents() {
		fmt.Println("  raw bank event: " + raw)
		svc.OnRawEvent(BANK_DEBIT, raw)
	}
	nsfNow, _ := svc.Lookup(achNsf.ProcessorRef)
	show("bank  after return file", nsfNow)
	achNow, _ := svc.Lookup(ach.ProcessorRef)
	show("bank  order-2 after settlement", achNow)
	show("wallet INSUFFICIENT_FUNDS", must(svc.Pay(charge("order-6", "45.00", USD, WalletMethod{"wal_nsf", "wallet"}, true))))

	fmt.Println("\n== Customer action: SCA soft decline and wallet approval, one status ==")
	show("card  ISO 8583 code 1A", must(svc.Pay(charge("order-7", "45.00", USD, CardMethod{"tok_sca_required", "visa"}, true))))
	action := must(svc.Pay(charge("order-8", "45.00", USD, WalletMethod{"wal_action", "wallet"}, true)))
	show("wallet PAYER_ACTION_REQUIRED", action)
	rig.Wallet.PayerApproves(action.ProcessorRef)
	for _, raw := range rig.Wallet.DrainEvents() {
		svc.OnRawEvent(WALLET, raw)
	}
	actionNow, _ := svc.Lookup(action.ProcessorRef)
	show("wallet after payer approves", actionNow)

	fmt.Println("\n== Capability flags refuse before the vendor is called ==")
	before := rig.Bank.VendorCalls()
	if _, err := svc.Pay(charge("order-9", "45.00", USD, BankDebitMethod{"acct_ok", "mandate_9"}, false)); errors.Is(err, ErrUnsupportedCapability) {
		fmt.Printf("authorize-only on bank debit: %s (vendor calls +%d)\n", errMessage(err), rig.Bank.VendorCalls()-before)
	}
	if _, err := svc.Pay(charge("order-10", "4500", JPY, WalletMethod{"wal_ok", "wallet"}, true)); errors.Is(err, ErrUnsupportedCapability) {
		fmt.Println("JPY on wallet: " + errMessage(err))
	}
	jpy := must(svc.Pay(charge("order-11", "4500", JPY, CardMethod{"tok_visa_4242", "visa"}, true)))
	fmt.Printf("JPY on card: %v %v (sent as minor=4500, currency=392)\n", jpy.Status, jpy.Amount)

	fmt.Println("\n== Conformance suite, run against every adapter ==")
	for _, target := range targets() {
		report := RunConformance(target)
		verdict := "PASS"
		if !report.Passed() {
			verdict = fmt.Sprintf("FAIL %v", report.Failures)
		}
		fmt.Printf("%-10s %2d checks  %-62s %s\n", report.Adapter, report.Checks, formatFlags(target.Processor.Capabilities().Flags()), verdict)
	}
}

// errMessage strips the sentinel prefix so the demo prints what Java's getMessage() printed.
func errMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrUnsupportedCapability.Error()+": ")
}
