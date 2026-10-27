// Tests for phases/10-money-and-ledgers/10-fees-and-splits. Run: go test ./...
package main

import (
	"fmt"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func policy(t *testing.T) SplitPolicy {
	t.Helper()
	commission := ok[FeeSchedule](t)(ok[FeeSchedule](t)(PercentPlusFixed("c", 1_200, eur("0.30"))).WithBounds(eur("0.50"), eur("25.00")))
	return ok[SplitPolicy](t)(NewSplitPolicy(commission, 1_900, 2_000, map[string]string{"seller-B": "partner-P"},
		ok[FeeSchedule](t)(PercentPlusFixed("p", 140, eur("0.25")))))
}

func fee(t *testing.T, f FeeSchedule, amount string) Money {
	t.Helper()
	return ok[Money](t)(f.FeeFor(eur(amount)))
}

func workedOrder(t *testing.T) (Order, OrderSplit) {
	t.Helper()
	order := ok[Order](t)(NewOrder("o-1", []LineItem{
		{"lamp", "seller-A", eur("59.99")}, {"bulb", "seller-A", eur("4.99")}, {"rug", "seller-B", eur("119.00")},
	}, eur("10.00")))
	return order, ok[OrderSplit](t)(NewSplitter(policy(t)).Split(order))
}

func partsTotal(s OrderSplit) Money {
	acc := s.Total(func(l LineSplit) Money { return l.Tax })
	for _, part := range []func(LineSplit) Money{
		func(l LineSplit) Money { return l.PlatformFee }, func(l LineSplit) Money { return l.ReferralFee }, func(l LineSplit) Money { return l.SellerNet },
	} {
		acc, _ = acc.Plus(s.Total(part))
	}
	return acc
}

func TestFeeSchedules(t *testing.T) {
	c := policy(t).Commission
	cases := []struct{ name, amount, want string }{
		{"percent + fixed (50.00 -> 6.30)", "50.00", "6.30"},
		{"minimum applies (1.00 -> 0.50)", "1.00", "0.50"},
		{"maximum applies (300.00 -> 25.00)", "300.00", "25.00"},
		{"fee never exceeds the amount (0.40 -> 0.40)", "0.40", "0.40"},
		{"fee on zero is zero", "0.00", "0.00"},
		{"percent rounds half even (0.125 -> 0.12 + 0.30, then the minimum)", "1.04", "0.50"},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			if got := fee(t, c, cs.amount); got != eur(cs.want) {
				t.Errorf("fee on %s = %v, want %s", cs.amount, got, cs.want)
			}
		})
	}
	if got := fee(t, ok[FeeSchedule](t)(PercentPlusFixed("x", 1_250, eur("0.00"))), "1.00"); got != eur("0.12") {
		t.Errorf("12.5 bps of 1.00 rounds half even to 0.12, got %v", got)
	}
	if _, err := ok[FeeSchedule](t)(PercentPlusFixed("x", 100, eur("0.00"))).WithBounds(eur("2.00"), eur("1.00")); err == nil {
		t.Error("min above max rejected")
	}
	if _, err := PercentPlusFixed("x", 10_001, eur("0.00")); err == nil {
		t.Error("bps above 100% rejected")
	}
	if _, err := c.FeeFor(eur("-1.00")); err == nil {
		t.Error("negative fee base rejected")
	}
}

func TestWorkedOrder(t *testing.T) {
	order, s := workedOrder(t)
	if s.Total(func(l LineSplit) Money { return l.DiscountShare }) != eur("10.00") {
		t.Error("discount shares sum to discount")
	}
	if s.Lines[0].DiscountShare != eur("3.26") || s.Lines[1].DiscountShare != eur("0.27") || s.Lines[2].DiscountShare != eur("6.47") {
		t.Error("discount allocated by price (3.26, 0.27, 6.47)")
	}
	for _, l := range s.Lines {
		if l.SumOfParts() != l.Charged {
			t.Errorf("every line's parts sum to its charge: %s", l.Line.SKU)
		}
	}
	if s.Lines[0].Tax != eur("9.06") || s.Lines[0].PlatformFee != eur("6.02") || s.Lines[0].SellerNet != eur("41.65") {
		t.Error("lamp VAT 9.06, platform 6.02, seller 41.65")
	}
	if s.Lines[2].PlatformFee != eur("9.32") || s.Lines[2].ReferralFee != eur("2.33") {
		t.Error("rug commission split 80/20 (9.32 + 2.33)")
	}
	if !s.Lines[0].ReferralFee.IsZero() || s.Lines[0].Referrer != "" {
		t.Error("no referral for seller without a referrer")
	}
	nets := s.SellerNets()
	a, _ := nets.Get("seller-A")
	b, _ := nets.Get("seller-B")
	if len(nets.Keys) != 2 || a != eur("44.84") || b != eur("82.91") {
		t.Errorf("seller totals aggregate by seller: %v", nets)
	}
	if charged := ok[Money](t)(order.Charged()); partsTotal(s) != charged || charged != eur("173.98") {
		t.Error("order parts sum to order charged")
	}
	if s.ProcessorFee != eur("2.69") {
		t.Error("processor fee 1.4% + 0.25 on 173.98 = 2.69")
	}
}

func TestLedgerAndPayouts(t *testing.T) {
	_, s := workedOrder(t)
	ledger := NewLedger()
	ok[struct{}](t)(struct{}{}, ledger.Post(ok[JournalEntry](t)(CaptureEntry(s))))
	if ledger.TrialBalanceNet() != 0 {
		t.Error("capture entry balances")
	}
	if ledger.NetDebit("1200 Receivable") != 17_398-269 {
		t.Error("receivable net of processor fee")
	}
	if ledger.NetDebit("2300 VAT Payable") != -2_778 {
		t.Error("VAT payable is the sum of line VAT")
	}

	pp := PayoutPolicy{1_000, ok[FeeSchedule](t)(PercentPlusFixed("payout", 0, eur("0.25"))), eur("20.00")}
	a := ok[Payout](t)(ComputePayout("seller-A", eur("44.84"), pp))
	if a.Reserve != eur("4.49") {
		t.Error("reserve rounds up (4.484 -> 4.49)")
	}
	sum := a.Reserve
	for _, part := range []Money{a.Fee, a.Net, a.CarriedOver} {
		sum = ok[Money](t)(sum.Plus(part))
	}
	if sum != a.Available {
		t.Error("payout parts sum to available")
	}
	if a.Net != eur("40.10") || !a.Paid() {
		t.Error("payout net 40.10")
	}
	small := ok[Payout](t)(ComputePayout("seller-C", eur("12.40"), pp))
	if small.Paid() || small.CarriedOver != eur("12.40") || !small.Reserve.IsZero() {
		t.Error("below minimum carries over everything")
	}
	negative := ok[Payout](t)(ComputePayout("seller-D", eur("-5.00"), pp))
	if negative.Paid() || negative.CarriedOver != eur("-5.00") {
		t.Error("negative balance is never paid out")
	}
	ok[struct{}](t)(struct{}{}, ledger.Post(ok[JournalEntry](t)(PayoutEntry(a))))
	if ledger.TrialBalanceNet() != 0 || ledger.NetDebit("2100 Seller Payable seller-A") != 0 {
		t.Error("payout entry balances and empties payable")
	}
	if ledger.NetDebit("2150 Seller Reserve seller-A") != -449 {
		t.Error("reserve moved to reserve account")
	}
}

func TestBoundariesAndErrors(t *testing.T) {
	sp := NewSplitter(policy(t))
	if _, err := sp.Split(ok[Order](t)(NewOrder("o", []LineItem{{"x", "seller-A", eur("1.00")}}, eur("2.00")))); err == nil {
		t.Error("discount above subtotal rejected")
	}
	free := ok[OrderSplit](t)(sp.Split(ok[Order](t)(NewOrder("o-free", []LineItem{{"x", "seller-A", eur("5.00")}}, eur("5.00")))))
	if !free.Lines[0].SumOfParts().IsZero() || !free.Lines[0].SellerNet.IsZero() {
		t.Error("fully discounted line: every part zero")
	}
	tiny := ok[OrderSplit](t)(sp.Split(ok[Order](t)(NewOrder("o-tiny", []LineItem{{"x", "seller-B", eur("0.01")}}, eur("0.00")))))
	if tiny.Lines[0].SumOfParts() != eur("0.01") || tiny.Lines[0].SellerNet.IsNegative() {
		t.Error("one-cent line still sums")
	}
	if _, err := sp.Split(ok[Order](t)(NewOrder("o", []LineItem{{"x", "seller-A", eur("0.00")}}, eur("0.00")))); err == nil {
		t.Error("zero-price line rejected")
	}
	if _, err := NewOrder("o", nil, eur("0.00")); err == nil {
		t.Error("empty order rejected")
	}
}

func TestPropertyRandomOrders(t *testing.T) {
	r := NewJavaRandom(23)
	// java.util.Random(23).nextLong(bound) reference values from the JDK, so the property test sees the same orders
	ref := NewJavaRandom(23)
	if a, b, c := ref.NextLongBounded(1000), ref.NextLongBounded(1<<20), ref.NextLongBounded(123456789); a != 988 || b != 949759 || c != 104581275 {
		t.Fatalf("NextLongBounded diverges from the JDK: %d %d %d", a, b, c)
	}
	sp := NewSplitter(policy(t))
	for i := range 2_000 {
		n := 1 + int(r.NextInt(5))
		var lines []LineItem
		var subtotal int64
		for k := range n {
			bound := int32(50_000)
			if r.NextBoolean() {
				bound = 500
			}
			price := int64(1 + r.NextInt(bound))
			subtotal += price
			seller := "seller-B"
			if r.NextBoolean() {
				seller = "seller-A"
			}
			lines = append(lines, LineItem{fmt.Sprintf("s%d", k), seller, OfMinor(price, EUR)})
		}
		var discountMinor int64
		if r.NextInt(4) == 0 {
			discountMinor = r.NextLongBounded(subtotal + 1)
		}
		discount := OfMinor(discountMinor, EUR)
		os := ok[OrderSplit](t)(sp.Split(ok[Order](t)(NewOrder(fmt.Sprintf("o%d", i), lines, discount))))
		charged := ok[Money](t)(os.Order.Charged())
		if partsTotal(os) != charged || os.Total(func(l LineSplit) Money { return l.DiscountShare }) != discount {
			t.Fatalf("order %d: parts do not conserve", i)
		}
		for _, l := range os.Lines {
			if l.Tax.IsNegative() || l.PlatformFee.IsNegative() || l.ReferralFee.IsNegative() || l.SellerNet.IsNegative() {
				t.Fatalf("order %d: a part is negative", i)
			}
			if commission := ok[Money](t)(l.PlatformFee.Plus(l.ReferralFee)); commission.Compare(eur("25.00")) > 0 {
				t.Fatalf("order %d: commission exceeds the maximum", i)
			}
		}
		if charged.IsPositive() {
			lg := NewLedger()
			ok[struct{}](t)(struct{}{}, lg.Post(ok[JournalEntry](t)(CaptureEntry(os))))
			if lg.TrialBalanceNet() != 0 {
				t.Fatalf("order %d: capture entry does not balance", i)
			}
		}
	}
}
