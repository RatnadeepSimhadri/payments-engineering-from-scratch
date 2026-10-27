// Lesson: phases/10-money-and-ledgers/10-fees-and-splits/docs/en.md
// Fees, Splits, and Platform Payments
// Run:  go run .
// Test: go test ./...
// Sources: largest-remainder apportionment (Hamilton); EU VAT Directive 2006/112/EC (tax-inclusive prices);
// Money and Allocate from Lesson 01 (money.go), ledger shape from Lesson 03. Go 1.22, standard library only.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// Zero is the zero amount of a currency.
func Zero(currency Currency) Money { return Money{0, currency} }

// ---------------------------------------------------------------------------------------------
// Fee schedules, the split of every line into parts that sum to it, and payouts.
// ---------------------------------------------------------------------------------------------

// FeeSchedule is percent + fixed, clamped to [Minimum, Maximum], and never more than the amount it is charged on.
// Percent is in basis points (1 bp = 0.01%), an exact rational bps/10000. Nil bounds mean "no bound" (Java: null).
type FeeSchedule struct {
	Name     string
	Bps      int64
	Fixed    Money
	Minimum  *Money
	Maximum  *Money
	Rounding Rounding
}

func NewFeeSchedule(name string, bps int64, fixed Money, minimum, maximum *Money, rounding Rounding) (FeeSchedule, error) {
	if bps < 0 || bps > 10_000 {
		return FeeSchedule{}, fmt.Errorf("bps must be 0..10000: %d", bps)
	}
	if fixed.IsNegative() {
		return FeeSchedule{}, errors.New("fixed fee must not be negative")
	}
	if minimum != nil && maximum != nil && minimum.Compare(*maximum) > 0 {
		return FeeSchedule{}, fmt.Errorf("minimum %v exceeds maximum %v", *minimum, *maximum)
	}
	return FeeSchedule{name, bps, fixed, minimum, maximum, rounding}, nil
}

func PercentPlusFixed(name string, bps int64, fixed Money) (FeeSchedule, error) {
	return NewFeeSchedule(name, bps, fixed, nil, nil, HalfEven)
}

func (f FeeSchedule) WithBounds(minimum, maximum Money) (FeeSchedule, error) {
	return NewFeeSchedule(f.Name, f.Bps, f.Fixed, &minimum, &maximum, f.Rounding)
}

func (f FeeSchedule) FeeFor(amount Money) (Money, error) {
	if amount.IsNegative() {
		return Money{}, fmt.Errorf("fee base must not be negative: %v", amount)
	}
	percent, err := amount.Times(f.Bps, 10_000, f.Rounding)
	if err != nil {
		return Money{}, err
	}
	fee, err := percent.Plus(f.Fixed)
	if err != nil {
		return Money{}, err
	}
	if f.Minimum != nil && fee.Compare(*f.Minimum) < 0 {
		fee = *f.Minimum
	}
	if f.Maximum != nil && fee.Compare(*f.Maximum) > 0 {
		fee = *f.Maximum
	}
	if fee.Compare(amount) > 0 { // a fee never exceeds what it is charged on
		fee = amount
	}
	return fee, nil
}

type LineItem struct {
	SKU      string
	SellerID string
	Price    Money
}

type Order struct {
	ID       string
	Lines    []LineItem
	Discount Money
}

func NewOrder(id string, lines []LineItem, discount Money) (Order, error) {
	if len(lines) == 0 {
		return Order{}, errors.New("order needs at least one line")
	}
	if discount.IsNegative() {
		return Order{}, errors.New("discount must not be negative")
	}
	return Order{id, append([]LineItem(nil), lines...), discount}, nil
}

func (o Order) Subtotal() (Money, error) {
	acc := Zero(o.Discount.Currency)
	for _, l := range o.Lines {
		var err error
		if acc, err = acc.Plus(l.Price); err != nil {
			return Money{}, err
		}
	}
	return acc, nil
}

func (o Order) Charged() (Money, error) {
	sub, err := o.Subtotal()
	if err != nil {
		return Money{}, err
	}
	return sub.Minus(o.Discount)
}

// SplitPolicy: prices are tax-inclusive (VAT-style). The platform commission is charged on the amount net of tax.
// A referral partner, if a seller has one, receives ReferralShareBps of the commission.
type SplitPolicy struct {
	Commission       FeeSchedule
	TaxBps           int64
	ReferralShareBps int64
	ReferrerBySeller map[string]string
	ProcessorFee     FeeSchedule
}

func NewSplitPolicy(commission FeeSchedule, taxBps, referralShareBps int64, referrerBySeller map[string]string, processorFee FeeSchedule) (SplitPolicy, error) {
	if taxBps < 0 || referralShareBps < 0 || referralShareBps > 10_000 {
		return SplitPolicy{}, errors.New("bad bps")
	}
	copied := make(map[string]string, len(referrerBySeller))
	for k, v := range referrerBySeller {
		copied[k] = v
	}
	return SplitPolicy{commission, taxBps, referralShareBps, copied, processorFee}, nil
}

// LineSplit is one line's money, in parts. Invariant: Tax + PlatformFee + ReferralFee + SellerNet == Charged.
// Referrer is "" when the seller has none (Java: null).
type LineSplit struct {
	Line          LineItem
	DiscountShare Money
	Charged       Money
	Tax           Money
	PlatformFee   Money
	ReferralFee   Money
	Referrer      string
	SellerNet     Money
}

func (l LineSplit) SumOfParts() Money {
	acc := l.Tax
	for _, part := range []Money{l.PlatformFee, l.ReferralFee, l.SellerNet} {
		var err error
		if acc, err = acc.Plus(part); err != nil {
			panic(err) // all parts came from the same charge
		}
	}
	return acc
}

// MoneyByParty is an insertion-ordered map of party id to amount (Java's LinkedHashMap<String, Money>).
type MoneyByParty struct {
	Keys   []string
	Values map[string]Money
}

func newMoneyByParty() *MoneyByParty { return &MoneyByParty{Values: map[string]Money{}} }

func (m *MoneyByParty) add(key string, amount Money) error {
	if existing, seen := m.Values[key]; seen {
		sum, err := existing.Plus(amount)
		if err != nil {
			return err
		}
		m.Values[key] = sum
		return nil
	}
	m.Keys = append(m.Keys, key)
	m.Values[key] = amount
	return nil
}

func (m *MoneyByParty) Get(key string) (Money, bool) { v, ok := m.Values[key]; return v, ok }

// String renders like Java's Map.toString: {k=v, k=v}.
func (m *MoneyByParty) String() string {
	parts := make([]string, 0, len(m.Keys))
	for _, k := range m.Keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m.Values[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

type OrderSplit struct {
	Order        Order
	Lines        []LineSplit
	ProcessorFee Money
}

// Total sums one part over every line.
func (s OrderSplit) Total(part func(LineSplit) Money) Money {
	acc := Zero(s.Order.Discount.Currency)
	for _, l := range s.Lines {
		var err error
		if acc, err = acc.Plus(part(l)); err != nil {
			panic(err)
		}
	}
	return acc
}

func (s OrderSplit) SellerNets() *MoneyByParty {
	out := newMoneyByParty()
	for _, l := range s.Lines {
		if err := out.add(l.Line.SellerID, l.SellerNet); err != nil {
			panic(err)
		}
	}
	return out
}

func (s OrderSplit) ReferralFees() *MoneyByParty {
	out := newMoneyByParty()
	for _, l := range s.Lines {
		if l.Referrer != "" && !l.ReferralFee.IsZero() {
			if err := out.add(l.Referrer, l.ReferralFee); err != nil {
				panic(err)
			}
		}
	}
	return out
}

type Splitter struct {
	policy SplitPolicy
}

func NewSplitter(policy SplitPolicy) *Splitter { return &Splitter{policy} }

// Split:
//  1. Allocate the order discount across lines in proportion to price.
//  2. Per line: allocate charged into [net of tax, tax] by ratio 10000 : taxBps.
//  3. Commission = schedule(net of tax). Allocate it into [platform, referral] if the seller has a referrer.
//  4. Seller net = net of tax - commission: the remainder, so the parts sum to the line exactly.
func (sp *Splitter) Split(order Order) (OrderSplit, error) {
	cur := order.Discount.Currency
	subtotal, err := order.Subtotal()
	if err != nil {
		return OrderSplit{}, err
	}
	if order.Discount.Compare(subtotal) > 0 {
		return OrderSplit{}, errors.New("discount exceeds subtotal")
	}
	weights := make([]int64, len(order.Lines))
	for i, l := range order.Lines {
		if !l.Price.IsPositive() {
			return OrderSplit{}, fmt.Errorf("line price must be positive: %v", l.Price)
		}
		weights[i] = l.Price.MinorUnits
	}
	discountShares := zeros(cur, len(weights))
	if !order.Discount.IsZero() {
		if discountShares, err = order.Discount.Allocate(weights...); err != nil {
			return OrderSplit{}, err
		}
	}
	out := make([]LineSplit, 0, len(weights))
	for i, line := range order.Lines {
		charged, err := line.Price.Minus(discountShares[i])
		if err != nil {
			return OrderSplit{}, err
		}
		taxParts := zeros(cur, 2)
		if !charged.IsZero() {
			if taxParts, err = charged.Allocate(10_000, sp.policy.TaxBps); err != nil {
				return OrderSplit{}, err
			}
		}
		netOfTax, tax := taxParts[0], taxParts[1]
		commission, err := sp.policy.Commission.FeeFor(netOfTax)
		if err != nil {
			return OrderSplit{}, err
		}
		referrer := sp.policy.ReferrerBySeller[line.SellerID]
		platform, referral := commission, Zero(cur)
		if referrer != "" && !commission.IsZero() {
			parts, err := commission.Allocate(10_000-sp.policy.ReferralShareBps, sp.policy.ReferralShareBps)
			if err != nil {
				return OrderSplit{}, err
			}
			platform, referral = parts[0], parts[1]
		}
		sellerNet, err := netOfTax.Minus(commission)
		if err != nil {
			return OrderSplit{}, err
		}
		out = append(out, LineSplit{line, discountShares[i], charged, tax, platform, referral, referrer, sellerNet})
	}
	charged, err := order.Charged()
	if err != nil {
		return OrderSplit{}, err
	}
	processorFee, err := sp.policy.ProcessorFee.FeeFor(charged)
	if err != nil {
		return OrderSplit{}, err
	}
	return OrderSplit{order, out, processorFee}, nil
}

func zeros(c Currency, n int) []Money {
	z := make([]Money, n)
	for i := range z {
		z[i] = Zero(c)
	}
	return z
}

// PayoutPolicy: hold a rolling reserve, charge a payout fee, and skip payouts below a minimum (carry them over).
type PayoutPolicy struct {
	ReserveBps    int64
	PayoutFee     FeeSchedule
	MinimumPayout Money
}

// Payout: Available == Reserve + Fee + Net + CarriedOver, always.
type Payout struct {
	SellerID    string
	Available   Money
	Reserve     Money
	Fee         Money
	Net         Money
	CarriedOver Money
}

func (p Payout) Paid() bool { return p.Net.IsPositive() }

func ComputePayout(sellerID string, available Money, policy PayoutPolicy) (Payout, error) {
	zero := Zero(available.Currency)
	if !available.IsPositive() {
		return Payout{sellerID, available, zero, zero, zero, available}, nil
	}
	reserve, err := available.Times(policy.ReserveBps, 10_000, Up) // round the hold in the platform's favour
	if err != nil {
		return Payout{}, err
	}
	afterReserve, err := available.Minus(reserve)
	if err != nil {
		return Payout{}, err
	}
	fee, err := policy.PayoutFee.FeeFor(afterReserve)
	if err != nil {
		return Payout{}, err
	}
	net, err := afterReserve.Minus(fee)
	if err != nil {
		return Payout{}, err
	}
	if net.Compare(policy.MinimumPayout) < 0 {
		return Payout{sellerID, available, zero, zero, zero, available}, nil // nothing moves; try again next cycle
	}
	return Payout{sellerID, available, reserve, fee, net, zero}, nil
}

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger (Lesson 03) to show that the split posts as one balanced entry.
// ---------------------------------------------------------------------------------------------

type Side int

const (
	Debit Side = iota
	Credit
)

type Posting struct {
	AccountID string
	Side      Side
	Amount    Money
}

func NewPosting(accountID string, side Side, amount Money) (Posting, error) {
	if !amount.IsPositive() {
		return Posting{}, fmt.Errorf("posting amount must be positive, got %v", amount)
	}
	return Posting{accountID, side, amount}, nil
}

func (p Posting) SignedMinor() int64 {
	if p.Side == Debit {
		return p.Amount.MinorUnits
	}
	return -p.Amount.MinorUnits
}

type JournalEntry struct {
	ID       string
	Postings []Posting
}

func NewJournalEntry(id string, postings []Posting) (JournalEntry, error) {
	var net int64
	for _, p := range postings {
		var err error
		if net, err = addChecked(net, p.SignedMinor()); err != nil {
			return JournalEntry{}, err
		}
	}
	if len(postings) < 2 || net != 0 {
		return JournalEntry{}, fmt.Errorf("entry %s unbalanced by %d", id, net)
	}
	return JournalEntry{id, append([]Posting(nil), postings...)}, nil
}

type Ledger struct {
	accountOrder []string
	netDebits    map[string]int64
	journal      []JournalEntry
}

func NewLedger() *Ledger { return &Ledger{netDebits: map[string]int64{}} }

func (l *Ledger) Post(e JournalEntry) error {
	for _, p := range e.Postings {
		if _, err := addChecked(l.netDebits[p.AccountID], p.SignedMinor()); err != nil {
			return err
		}
	}
	l.journal = append(l.journal, e)
	for _, p := range e.Postings {
		if _, seen := l.netDebits[p.AccountID]; !seen {
			l.accountOrder = append(l.accountOrder, p.AccountID)
		}
		l.netDebits[p.AccountID] += p.SignedMinor()
	}
	return nil
}

func (l *Ledger) NetDebit(accountID string) int64 { return l.netDebits[accountID] }

func (l *Ledger) TrialBalanceNet() int64 {
	var sum int64
	for _, id := range l.accountOrder {
		sum += l.netDebits[id]
	}
	return sum
}

// CaptureEntry builds the capture entry for a split: one debit to the receivable, one credit per party, nothing dropped.
// Parties are credited in first-seen order (the LinkedHashMap order of the Java version), never map order.
func CaptureEntry(s OrderSplit) (JournalEntry, error) {
	charged, err := s.Order.Charged()
	if err != nil {
		return JournalEntry{}, err
	}
	receivable, err := NewPosting("1200 Receivable", Debit, charged)
	if err != nil {
		return JournalEntry{}, err
	}
	ps := []Posting{receivable}
	ps = addCredit(ps, "2300 VAT Payable", s.Total(func(l LineSplit) Money { return l.Tax }))
	ps = addCredit(ps, "4100 Commission Revenue", s.Total(func(l LineSplit) Money { return l.PlatformFee }))
	referrals := s.ReferralFees()
	for _, partner := range referrals.Keys {
		ps = addCredit(ps, "2400 Referral Payable "+partner, referrals.Values[partner])
	}
	sellers := s.SellerNets()
	for _, seller := range sellers.Keys {
		ps = addCredit(ps, "2100 Seller Payable "+seller, sellers.Values[seller])
	}
	if s.ProcessorFee.IsPositive() {
		ps = append(ps, Posting{"5100 Processing Expense", Debit, s.ProcessorFee}, Posting{"1200 Receivable", Credit, s.ProcessorFee})
	}
	return NewJournalEntry("cap-"+s.Order.ID, ps)
}

func PayoutEntry(p Payout) (JournalEntry, error) {
	payable := "2100 Seller Payable " + p.SellerID
	moved, err := p.Reserve.Plus(p.Fee)
	if err != nil {
		return JournalEntry{}, err
	}
	if moved, err = moved.Plus(p.Net); err != nil {
		return JournalEntry{}, err
	}
	debit, err := NewPosting(payable, Debit, moved)
	if err != nil {
		return JournalEntry{}, err
	}
	ps := []Posting{debit}
	ps = addCredit(ps, "2150 Seller Reserve "+p.SellerID, p.Reserve)
	ps = addCredit(ps, "4200 Payout Fee Revenue", p.Fee)
	ps = addCredit(ps, "1010 Bank", p.Net)
	return NewJournalEntry("out-"+p.SellerID, ps)
}

func addCredit(ps []Posting, account string, amount Money) []Posting {
	if amount.IsPositive() {
		ps = append(ps, Posting{account, Credit, amount})
	}
	return ps
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func eur(s string) Money { return must(Parse(s, EUR)) }

// amt renders an amount without its currency code, for the table.
func amt(m Money) string {
	s := m.String()
	return s[:len(s)-4]
}

func main() {
	minFee, maxFee := eur("0.50"), eur("25.00")
	commission := must(must(PercentPlusFixed("commission 12% + 0.30", 1_200, eur("0.30"))).WithBounds(minFee, maxFee))
	policy := must(NewSplitPolicy(
		commission,
		1_900, // German VAT 19%, included in prices
		2_000, // referral partner gets 20% of commission
		map[string]string{"seller-B": "partner-P"},
		must(PercentPlusFixed("processor 1.4% + 0.25", 140, eur("0.25")))))

	fmt.Println("== Fee schedule: 12% + 0.30, min 0.50, max 25.00 ==")
	for _, amount := range []string{"1.00", "2.00", "50.00", "300.00", "0.40"} {
		a := eur(amount)
		fmt.Printf("  fee on %-11v = %v\n", a, must(policy.Commission.FeeFor(a)))
	}

	fmt.Println("\n== Order o-1: three lines, two sellers, 10.00 coupon, prices include 19% VAT ==")
	order := must(NewOrder("o-1", []LineItem{
		{"lamp", "seller-A", eur("59.99")},
		{"bulb", "seller-A", eur("4.99")},
		{"rug", "seller-B", eur("119.00")},
	}, eur("10.00")))
	split := must(NewSplitter(policy).Split(order))
	fmt.Printf("  %-5s %-9s %8s %9s %9s %8s %9s %9s %10s %6s\n", "sku", "seller", "price", "discount", "charged", "VAT", "platform", "referral", "seller net", "sums")
	for _, l := range split.Lines {
		fmt.Printf("  %-5s %-9s %8s %9s %9s %8s %9s %9s %10s %6v\n", l.Line.SKU, l.Line.SellerID,
			amt(l.Line.Price), amt(l.DiscountShare), amt(l.Charged), amt(l.Tax), amt(l.PlatformFee),
			amt(l.ReferralFee), amt(l.SellerNet), l.SumOfParts() == l.Charged)
	}
	fmt.Printf("  order charged %v; VAT %v; platform %v; referral %v; sellers %v\n", must(order.Charged()),
		split.Total(func(l LineSplit) Money { return l.Tax }), split.Total(func(l LineSplit) Money { return l.PlatformFee }),
		split.ReferralFees(), split.SellerNets())
	fmt.Printf("  processor fee on the charge: %v\n", split.ProcessorFee)

	fmt.Println("\n== Posted as one balanced entry ==")
	ledger := NewLedger()
	cap := must(CaptureEntry(split))
	must(struct{}{}, ledger.Post(cap))
	for _, p := range cap.Postings {
		prefix := "      Cr "
		if p.Side == Debit {
			prefix = "  Dr "
		}
		fmt.Printf("%s%s  %v\n", prefix, p.AccountID, p.Amount)
	}
	fmt.Printf("  trial balance nets to %d\n", ledger.TrialBalanceNet())

	fmt.Println("\n== Payouts: 10% rolling reserve (rounded up), payout fee 0.25, minimum payout 20.00 ==")
	payoutPolicy := PayoutPolicy{1_000, must(PercentPlusFixed("payout", 0, eur("0.25"))), eur("20.00")}
	for _, seller := range split.SellerNets().Keys {
		available := OfMinor(-ledger.NetDebit("2100 Seller Payable "+seller), EUR)
		p := must(ComputePayout(seller, available, payoutPolicy))
		fmt.Printf("  %s: available %v -> reserve %v, fee %v, to bank %v, carried %v\n", seller, p.Available, p.Reserve, p.Fee, p.Net, p.CarriedOver)
		if p.Paid() {
			must(struct{}{}, ledger.Post(must(PayoutEntry(p))))
		}
	}
	small := must(ComputePayout("seller-C", eur("12.40"), payoutPolicy))
	fmt.Printf("  seller-C: available %v -> below minimum, carried %v\n", small.Available, small.CarriedOver)
	fmt.Printf("  trial balance after payouts nets to %d\n", ledger.TrialBalanceNet())

	fmt.Println("\n== Why allocate, not round each part: 0.10 split three ways ==")
	dime := eur("0.10")
	naive := must(dime.Times(1, 3, HalfEven))
	fmt.Printf("  rounding each third: %v x 3 = %v (a cent lost)\n", naive, must(naive.Times(3, 1, HalfEven)))
	thirds := must(dime.Allocate(1, 1, 1))
	fmt.Printf("  allocate(1,1,1): %v sum %v\n", thirds, must(Sum(EUR, thirds...)))
}
