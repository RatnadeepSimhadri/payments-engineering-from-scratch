// Lesson: phases/14-payments-at-scale/08-cost-and-authorization-optimization/docs/en.md
// Cost and Authorization-Rate Optimization
// Run:  go run .
// Test: go test ./...
// Sources: Visa decline-code categories and retry limits; EMVCo Payment Tokenisation Specification;
// Federal Reserve Regulation II (12 CFR 235, debit interchange cap). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Response codes, card types, and the price list
// ---------------------------------------------------------------------------------------------

// DeclineClass says how a decline may be treated. Mirrors the card networks' grouping of response codes.
type DeclineClass int

const (
	None       DeclineClass = iota // approved
	NeverRetry                     // the issuer will never approve this card for this merchant: lost, stolen, closed
	RetryLater                     // the issuer cannot approve now: funds, issuer down, system error
	FixData                        // the credential is wrong or stale: retry only with new data
	Generic                        // no reason given; one retry is tolerated, more is abuse
)

// ResponseCode is an ISO 8583 field 39 response code used in this lesson.
type ResponseCode int

const (
	Approved ResponseCode = iota
	DoNotHonor
	LostOrStolen
	InsufficientFunds
	ExpiredCard
	NotPermitted
	IssuerUnavailable
	ProcessorTimeout
)

var responseCodes = [...]struct {
	name         string
	code         string
	declineClass DeclineClass
}{
	{"APPROVED", "00", None},
	{"DO_NOT_HONOR", "05", Generic},
	{"LOST_OR_STOLEN", "43", NeverRetry},
	{"INSUFFICIENT_FUNDS", "51", RetryLater},
	{"EXPIRED_CARD", "54", FixData},
	{"NOT_PERMITTED", "57", NeverRetry},
	{"ISSUER_UNAVAILABLE", "91", RetryLater},
	{"PROCESSOR_TIMEOUT", "96", RetryLater},
}

func (r ResponseCode) String() string             { return responseCodes[r].name }
func (r ResponseCode) Code() string               { return responseCodes[r].code }
func (r ResponseCode) DeclineClass() DeclineClass { return responseCodes[r].declineClass }
func (r ResponseCode) Approved() bool             { return r == Approved }

type CardType int

const (
	ConsumerCredit CardType = iota
	RegulatedDebit
	Commercial
)

type Lever int

const (
	NetworkToken Lever = iota
	AccountUpdater
	SoftDeclineRetry
	Routing
	Level3Data
)

var leverNames = [...]string{"NETWORK_TOKEN", "ACCOUNT_UPDATER", "SOFT_DECLINE_RETRY", "ROUTING", "LEVEL3_DATA"}

// Levers lists every lever in declaration order (Lever.values()).
var Levers = []Lever{NetworkToken, AccountUpdater, SoftDeclineRetry, Routing, Level3Data}

func (l Lever) String() string { return leverNames[l] }

// LeverSet is an EnumSet<Lever>: a bitmask that prints in declaration order as "[A, B]".
type LeverSet uint8

func Of(levers ...Lever) LeverSet {
	var s LeverSet
	for _, l := range levers {
		s |= 1 << l
	}
	return s
}

func AllLevers() LeverSet { return Of(Levers...) }

func (s LeverSet) Has(l Lever) bool { return s&(1<<l) != 0 }
func (s LeverSet) IsEmpty() bool    { return s == 0 }

func (s LeverSet) String() string {
	names := []string{}
	for _, l := range Levers {
		if s.Has(l) {
			names = append(names, l.String())
		}
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// Txn is one transaction in the experiment. Its base outcome and three uniform draws in [0, 10000) are fixed
// when the population is generated, so every arm of the experiment sees the same customer, the same card, and
// the same luck. This is the common-random-numbers technique: differences between arms are caused by the levers
// and nothing else.
type Txn struct {
	Id            string
	Amount        Money
	CardType      CardType
	TokenEligible bool
	BaseOutcome   ResponseCode
	DrawToken     int
	DrawRetry     int
	DrawUpdater   int
}

// ErrBadDraw is returned for a draw outside [0, 10000).
var ErrBadDraw = errors.New("draws are basis points in [0, 10000)")

func NewTxn(id string, amount Money, cardType CardType, tokenEligible bool, baseOutcome ResponseCode,
	drawToken, drawRetry, drawUpdater int) (Txn, error) {
	for _, d := range []int{drawToken, drawRetry, drawUpdater} {
		if d < 0 || d >= 10_000 {
			return Txn{}, ErrBadDraw
		}
	}
	return Txn{id, amount, cardType, tokenEligible, baseOutcome, drawToken, drawRetry, drawUpdater}, nil
}

// Rate is an interchange rate: basis points plus a fixed amount in minor units.
type Rate struct {
	Bps   int64
	Fixed int64
}

// PriceList holds prices and probabilities. Probabilities are in basis points. Fees marked illustrative are
// plausible orders of magnitude, not any provider's price list; the regulated-debit cap is Regulation II's
// 0.21 USD + 5 bps + 0.01 fraud-prevention adjustment.
type PriceList struct {
	AuthFee                      Money // per attempt, charged by the processor (illustrative)
	TokenFee                     Money // per tokenized attempt (illustrative)
	UpdaterHitFee                Money // per successful account-updater match (illustrative)
	TokenRecoverDoNotHonorBps    int64 // share of 05 declines approved when the token is used
	UpdaterMatchBps              int64 // share of stale credentials the updater can refresh
	RetryInsufficientFundsBps    int64
	RetryIssuerUnavailableBps    int64
	RetryDoNotHonorBps           int64
	RetryTimeoutSameProcessorBps int64
	FailoverTimeoutBps           int64 // approval when a timeout is re-sent via the secondary processor
	PremiumNetworkBps            int64 // network fee on the default debit network (illustrative)
	LeastCostNetworkBps          int64 // network fee on the cheaper debit network (illustrative)
	Interchange                  map[CardType]Rate
	CommercialLevel3             Rate // with full line-item data
}

func StandardPriceList(c Currency) PriceList {
	return PriceList{
		AuthFee: OfMinor(5, c), TokenFee: OfMinor(1, c), UpdaterHitFee: OfMinor(15, c),
		TokenRecoverDoNotHonorBps: 2_500, UpdaterMatchBps: 8_000, RetryInsufficientFundsBps: 1_500,
		RetryIssuerUnavailableBps: 7_000, RetryDoNotHonorBps: 1_000, RetryTimeoutSameProcessorBps: 5_000,
		FailoverTimeoutBps: 9_000, PremiumNetworkBps: 13, LeastCostNetworkBps: 6,
		Interchange: map[CardType]Rate{
			ConsumerCredit: {180, 10}, // 1.80% + 0.10 (illustrative)
			RegulatedDebit: {5, 22},   // 0.05% + 0.22 (Regulation II cap with fraud adjustment)
			Commercial:     {265, 10}, // 2.65% + 0.10, level 1 data only (illustrative)
		},
		CommercialLevel3: Rate{185, 10},
	}
}

// ArmResult is the outcome of running one arm of the experiment over the whole population.
type ArmResult struct {
	Levers         LeverSet
	Transactions   int
	Attempts       int
	Approvals      int
	ApprovedVolume Money
	AuthFees       Money
	TokenFees      Money
	UpdaterFees    Money
	NetworkFees    Money
	Interchange    Money
}

func (a ArmResult) TotalCost() Money {
	total := need(a.AuthFees.Plus(a.TokenFees))
	total = need(total.Plus(a.UpdaterFees))
	total = need(total.Plus(a.NetworkFees))
	return need(total.Plus(a.Interchange))
}

// ApprovalRateBps is the approval rate per transaction (not per attempt), in basis points.
func (a ArmResult) ApprovalRateBps() int64 {
	if a.Transactions == 0 {
		return 0
	}
	return int64(a.Approvals) * 10_000 / int64(a.Transactions)
}

// TxnTrace is the per-transaction trace: what happened, how many attempts, what it cost.
type TxnTrace struct {
	FinalOutcome ResponseCode
	Attempts     int
	TokenUsed    bool
	UpdaterHit   bool
	FailedOver   bool
	Retried      bool
	Fees         Money
}

type AuthExperiment struct {
	Prices   PriceList
	Currency Currency
}

func NewAuthExperiment(prices PriceList, currency Currency) *AuthExperiment {
	return &AuthExperiment{prices, currency}
}

// Simulate decides one transaction's path under a set of levers. Pure: same txn, same levers, same trace.
func (e *AuthExperiment) Simulate(t Txn, levers LeverSet) TxnTrace {
	outcome := t.BaseOutcome
	useToken := levers.Has(NetworkToken) && t.TokenEligible
	updaterHit := false
	fees := Zero(e.Currency)

	// 1. Credential levers run before the first attempt.
	if outcome == ExpiredCard {
		if useToken {
			outcome = Approved // the token's lifecycle management already holds the new card
		} else if levers.Has(AccountUpdater) && int64(t.DrawUpdater) < e.Prices.UpdaterMatchBps {
			outcome = Approved
			updaterHit = true
			fees = need(fees.Plus(e.Prices.UpdaterHitFee))
		}
	}
	if useToken && outcome == DoNotHonor && int64(t.DrawToken) < e.Prices.TokenRecoverDoNotHonorBps {
		outcome = Approved // issuers approve more tokenized card-not-present traffic
	}

	// 2. First attempt.
	attempts := 1
	fees = need(fees.Plus(e.Prices.AuthFee))
	if useToken {
		fees = need(fees.Plus(e.Prices.TokenFee))
	}

	// 3. Timeout at the primary: fail over to the secondary processor.
	failedOver := false
	if outcome == ProcessorTimeout && levers.Has(Routing) {
		attempts++
		failedOver = true
		fees = need(fees.Plus(e.Prices.AuthFee))
		if useToken {
			fees = need(fees.Plus(e.Prices.TokenFee))
		}
		if int64(t.DrawRetry) < e.Prices.FailoverTimeoutBps {
			outcome = Approved
		} else {
			outcome = IssuerUnavailable
		}
	}

	// 4. One retry for soft declines only. NeverRetry and FixData are never re-sent unchanged.
	retried := false
	if !failedOver && levers.Has(SoftDeclineRetry) && Retryable(outcome) {
		attempts++
		retried = true
		fees = need(fees.Plus(e.Prices.AuthFee))
		if useToken {
			fees = need(fees.Plus(e.Prices.TokenFee))
		}
		if int64(t.DrawRetry) < e.RetrySuccessBps(outcome) {
			outcome = Approved
		}
	}
	return TxnTrace{outcome, attempts, useToken, updaterHit, failedOver, retried, fees}
}

func Retryable(r ResponseCode) bool {
	return r.DeclineClass() == RetryLater || r.DeclineClass() == Generic
}

func (e *AuthExperiment) RetrySuccessBps(r ResponseCode) int64 {
	switch r {
	case InsufficientFunds:
		return e.Prices.RetryInsufficientFundsBps
	case IssuerUnavailable:
		return e.Prices.RetryIssuerUnavailableBps
	case DoNotHonor:
		return e.Prices.RetryDoNotHonorBps
	case ProcessorTimeout:
		return e.Prices.RetryTimeoutSameProcessorBps
	}
	return 0
}

// Interchange on an approved transaction, given whether level 3 data was sent.
func (e *AuthExperiment) Interchange(amount Money, cardType CardType, level3 bool) Money {
	rate := e.Prices.Interchange[cardType]
	if cardType == Commercial && level3 {
		rate = e.Prices.CommercialLevel3
	}
	pct := need(amount.Times(rate.Bps, 10_000, HalfEven))
	return need(pct.Plus(OfMinor(rate.Fixed, amount.Currency)))
}

// NetworkFee on an approved transaction; regulated debit can be routed to the cheaper network.
func (e *AuthExperiment) NetworkFee(amount Money, cardType CardType, leastCost bool) Money {
	bps := e.Prices.PremiumNetworkBps
	if cardType == RegulatedDebit && leastCost {
		bps = e.Prices.LeastCostNetworkBps
	}
	return need(amount.Times(bps, 10_000, HalfEven))
}

func (e *AuthExperiment) Run(population []Txn, levers LeverSet) ArmResult {
	attempts, approvals := 0, 0
	volume, auth, token := Zero(e.Currency), Zero(e.Currency), Zero(e.Currency)
	updater, network, ic := Zero(e.Currency), Zero(e.Currency), Zero(e.Currency)
	level3, leastCost := levers.Has(Level3Data), levers.Has(Routing)
	for _, t := range population {
		tr := e.Simulate(t, levers)
		attempts += tr.Attempts
		tokenAttempts := 0
		if tr.TokenUsed {
			tokenAttempts = tr.Attempts
		}
		tokenCost := OfMinor(need(mulChecked(e.Prices.TokenFee.MinorUnits, int64(tokenAttempts))), e.Currency)
		token = need(token.Plus(tokenCost))
		if tr.UpdaterHit {
			updater = need(updater.Plus(e.Prices.UpdaterHitFee))
		}
		auth = need(auth.Plus(OfMinor(need(mulChecked(e.Prices.AuthFee.MinorUnits, int64(tr.Attempts))), e.Currency)))
		if tr.FinalOutcome.Approved() {
			approvals++
			volume = need(volume.Plus(t.Amount))
			network = need(network.Plus(e.NetworkFee(t.Amount, t.CardType, leastCost)))
			ic = need(ic.Plus(e.Interchange(t.Amount, t.CardType, level3)))
		}
	}
	return ArmResult{levers, len(population), attempts, approvals, volume, auth, token, updater, network, ic}
}

// Population is the seeded population. The base outcome mix is fixed by the seed; see the lesson for the shares.
func Population(n int, seed int64, c Currency) []Txn {
	rnd := NewJavaRandom(seed)
	out := make([]Txn, 0, n)
	for i := 0; i < n; i++ {
		typeRoll := rnd.NextIntBound(100)
		cardType := Commercial
		switch {
		case typeRoll < 60:
			cardType = ConsumerCredit
		case typeRoll < 90:
			cardType = RegulatedDebit
		}
		var minor int64
		if cardType == Commercial {
			minor = 20_000 + int64(rnd.NextIntBound(480_000))
		} else {
			minor = 1_000 + int64(rnd.NextIntBound(19_000))
		}
		tokenEligible := rnd.NextIntBound(100) < 70
		u := rnd.NextIntBound(10_000)
		var base ResponseCode
		switch {
		case u < 400:
			base = ExpiredCard // 4.0% stale credentials on file
		case u < 800:
			base = DoNotHonor // 4.0%
		case u < 1100:
			base = InsufficientFunds // 3.0%
		case u < 1250:
			base = IssuerUnavailable // 1.5%
		case u < 1400:
			base = ProcessorTimeout // 1.5%
		case u < 1500:
			base = LostOrStolen // 1.0%
		case u < 1600:
			base = NotPermitted // 1.0%
		default:
			base = Approved // 84.0%
		}
		out = append(out, need(NewTxn(fmt.Sprintf("t%06d", i), OfMinor(minor, c), cardType, tokenEligible, base,
			int(rnd.NextIntBound(10_000)), int(rnd.NextIntBound(10_000)), int(rnd.NextIntBound(10_000)))))
	}
	return out
}

// Uplift of one arm over control: exact integer deltas, plus net value at a stated contribution margin.
type Uplift struct {
	Arm            string
	ExtraApprovals int
	RateDeltaBps   int64
	ExtraVolume    Money
	ExtraCost      Money
	NetValue       Money
}

func UpliftOf(arm string, control, treatment ArmResult, marginBps int64) Uplift {
	extraVolume := need(treatment.ApprovedVolume.Minus(control.ApprovedVolume))
	extraCost := need(treatment.TotalCost().Minus(control.TotalCost()))
	value := need(need(extraVolume.Times(marginBps, 10_000, HalfEven)).Minus(extraCost))
	return Uplift{arm, treatment.Approvals - control.Approvals,
		treatment.ApprovalRateBps() - control.ApprovalRateBps(), extraVolume, extraCost, value}
}

const MarginBps = 3_000 // the merchant's contribution margin on recovered sales: 30%

func Pct(bps int64) string {
	sign := ""
	if bps < 0 {
		sign = "-"
		bps = -bps
	}
	return fmt.Sprintf("%s%d.%02d%%", sign, bps/100, bps%100)
}

func main() {
	exp := NewAuthExperiment(StandardPriceList(USD), USD)

	fmt.Println("== Level 2/3 data on one 1,000.00 USD commercial card purchase ==")
	ticket := OfMinor(100_000, USD)
	l1 := exp.Interchange(ticket, Commercial, false)
	l3 := exp.Interchange(ticket, Commercial, true)
	fmt.Printf("level 1 interchange %v, level 3 %v, saved %v\n", l1, l3, need(l1.Minus(l3)))

	fmt.Println("\n== One expired card, three ways ==")
	stale := need(NewTxn("demo", OfMinor(4_999, USD), ConsumerCredit, true, ExpiredCard, 9_000, 9_000, 1_000))
	for _, levers := range []LeverSet{Of(), Of(SoftDeclineRetry), Of(AccountUpdater), Of(NetworkToken)} {
		tr := exp.Simulate(stale, levers)
		label := levers.String()
		if levers.IsEmpty() {
			label = "control"
		}
		fmt.Printf("  %-22s -> %-14v attempts %d fees %v\n", label, tr.FinalOutcome, tr.Attempts, tr.Fees)
	}

	fmt.Println("\n== 100,000 transactions, seed 2026, same draws in every arm ==")
	pop := Population(100_000, 2026, USD)
	control := exp.Run(pop, Of())
	fmt.Printf("control: %d approvals of %d (%s), %d attempts, volume %v, cost %v\n", control.Approvals, control.Transactions,
		Pct(control.ApprovalRateBps()), control.Attempts, control.ApprovedVolume, control.TotalCost())
	fmt.Printf("\n  %-20s %8s %8s %18s %14s %16s\n", "arm", "+approve", "+rate", "+volume", "+cost", "net @30% margin")
	rows := []Uplift{}
	for _, l := range Levers {
		rows = append(rows, UpliftOf(l.String(), control, exp.Run(pop, Of(l)), MarginBps))
	}
	rows = append(rows, UpliftOf("ALL", control, exp.Run(pop, AllLevers()), MarginBps))
	for _, u := range rows {
		fmt.Printf("  %-20s %8d %8s %18v %14v %16v\n", u.Arm, u.ExtraApprovals, Pct(u.RateDeltaBps), u.ExtraVolume, u.ExtraCost, u.NetValue)
	}
	sumOfSingles := 0
	for _, u := range rows[:len(Levers)] {
		sumOfSingles += u.ExtraApprovals
	}
	fmt.Printf("\nsum of single-lever uplifts %d vs all levers together %d\n(token and updater recover the same expired cards; routing and retry the same timeouts)\n",
		sumOfSingles, rows[len(rows)-1].ExtraApprovals)

	all := exp.Run(pop, AllLevers())
	fmt.Printf("cost per approval: control %v, all levers %v\n",
		OfMinor(control.TotalCost().MinorUnits/int64(control.Approvals), USD),
		OfMinor(all.TotalCost().MinorUnits/int64(all.Approvals), USD))
	hardResent := 0
	for _, t := range pop {
		if t.BaseOutcome.DeclineClass() == NeverRetry && exp.Simulate(t, AllLevers()).Attempts > 1 {
			hardResent++
		}
	}
	fmt.Printf("hard declines re-sent under ALL: %d\n", hardResent)
}
