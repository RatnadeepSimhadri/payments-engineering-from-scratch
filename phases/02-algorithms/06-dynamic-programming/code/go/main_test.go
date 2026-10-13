// Tests for phases/02-algorithms/06-dynamic-programming. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"testing"
)

var (
	us      = []int{1, 5, 10, 25}
	odd     = []int{1, 3, 4}
	payouts = []Payout{
		{"P-A", OfMinor(6000, USD), 100},
		{"P-B", OfMinor(5000, USD), 90},
		{"P-C", OfMinor(5000, USD), 90},
		{"P-D", OfMinor(3000, USD), 10},
		{"P-E", OfMinor(1000, USD), 5},
		{"P-F", OfMinor(12000, USD), 999},
	}
	limit = OfMinor(10000, USD)
)

func isSubsequence(sub, seq []string) bool {
	i := 0
	for _, s := range seq {
		if i < len(sub) && sub[i] == s {
			i++
		}
	}
	return i == len(sub)
}

func TestUSCoins63(t *testing.T) {
	r := must(MinCoinsTable(us, 63))
	if r.CoinCount != 6 {
		t.Fatalf("want 6 coins, got %d", r.CoinCount)
	}
	if !reflect.DeepEqual(r.Coins(us), []int{25, 25, 10, 1, 1, 1}) {
		t.Fatalf("reconstruction wrong: %v", r.Coins(us))
	}
	if g := must(GreedyCoins(us, 63)); g != 6 {
		t.Fatalf("greedy on canonical coins should agree, got %d", g)
	}
}

func TestNonCanonicalGreedyIsWrong(t *testing.T) {
	r := must(MinCoinsTable(odd, 6))
	if r.CoinCount != 2 || !reflect.DeepEqual(r.Coins(odd), []int{3, 3}) {
		t.Fatalf("want 2 coins (3+3), got %d %v", r.CoinCount, r.Coins(odd))
	}
	if g := must(GreedyCoins(odd, 6)); g != 3 {
		t.Fatalf("greedy should give 3, got %d", g)
	}
}

func TestCoinBoundaries(t *testing.T) {
	if must(MinCoinsTable(us, 0)).CoinCount != 0 {
		t.Fatal("amount 0 needs 0 coins")
	}
	r := must(MinCoinsTable([]int{2}, 3))
	if r.CoinCount != Unreachable || r.Reachable() {
		t.Fatal("3 from {2} must be unreachable")
	}
	if must(MinCoinsTable([]int{25, 1, 10, 5}, 63)).CoinCount != 6 {
		t.Fatal("denomination order must not change the count")
	}
}

func TestCoinValidation(t *testing.T) {
	if _, err := MinCoinsTable(us, -1); err == nil {
		t.Fatal("negative amount must be rejected")
	}
	if _, err := MinCoinsTable([]int{0, 1}, 5); err == nil {
		t.Fatal("zero denomination must be rejected")
	}
	if _, err := MinCoinsTable(nil, 5); err == nil {
		t.Fatal("empty denominations must be rejected")
	}
}

func TestThreeFormsAgree(t *testing.T) {
	for a := 0; a <= 20; a++ {
		var c1, c2 int64
		r := must(MinCoinsRecursive(odd, a, &c1))
		m := must(MinCoinsMemo(odd, a, &c2))
		tb := must(MinCoinsTable(odd, a)).CoinCount
		if r != m || m != tb {
			t.Fatalf("amount %d: recursive=%d memo=%d table=%d", a, r, m, tb)
		}
	}
}

func TestCallCounts(t *testing.T) {
	var naive, memo int64
	MinCoinsRecursive(odd, 20, &naive)
	MinCoinsMemo(odd, 20, &memo)
	if naive != 20736 || memo != 56 {
		t.Fatalf("want 20736 and 56 calls, got %d and %d", naive, memo)
	}
	var c int64
	if must(MinCoinsMemo([]int{2}, 3, &c)) != INF {
		t.Fatal("memo must return INF for unreachable")
	}
}

func TestCountWays(t *testing.T) {
	cases := []struct {
		denoms []int
		amount int
		want   int64
	}{
		{[]int{1, 2, 5}, 5, 4}, {us, 0, 1}, {[]int{2}, 3, 0}, {us, 100, 242}, {[]int{5, 2, 1}, 5, 4},
	}
	for _, c := range cases {
		if got := must(CountWays(c.denoms, c.amount)); got != c.want {
			t.Fatalf("CountWays(%v, %d) = %d, want %d", c.denoms, c.amount, got, c.want)
		}
	}
}

func TestBestBatch(t *testing.T) {
	best := must(BestBatch(payouts, limit))
	if best.Priority != 180 {
		t.Fatalf("want priority 180, got %d", best.Priority)
	}
	if !reflect.DeepEqual(ids(best.Chosen), []string{"P-B", "P-C"}) {
		t.Fatalf("want B and C, got %v", ids(best.Chosen))
	}
	if best.Total.Compare(limit) > 0 || best.Total.MinorUnits != 10000 {
		t.Fatalf("total %v violates limit or sum", best.Total)
	}
	var sum int64
	for _, p := range best.Chosen {
		sum += p.Priority
		if p.ID == "P-F" {
			t.Fatal("oversize payout must never be chosen")
		}
	}
	if sum != best.Priority {
		t.Fatal("reconstructed priority must equal table value")
	}
}

func TestGreedyBatchIsWorse(t *testing.T) {
	greedy := GreedyBatch(payouts, limit)
	if greedy.Priority != 115 || greedy.Priority >= must(BestBatch(payouts, limit)).Priority {
		t.Fatalf("greedy priority %d", greedy.Priority)
	}
}

func TestBatchBoundaries(t *testing.T) {
	if len(must(BestBatch(nil, limit)).Chosen) != 0 {
		t.Fatal("empty payouts give empty batch")
	}
	if must(BestBatch(payouts, OfMinor(0, USD))).Priority != 0 {
		t.Fatal("zero limit chooses nothing")
	}
	single := []Payout{{"X", OfMinor(700, USD), 7}}
	if must(BestBatch(single, OfMinor(700, USD))).Priority != 7 {
		t.Fatal("exact fit of a single item")
	}
}

func TestBatchValidation(t *testing.T) {
	_, err := BestBatch([]Payout{{"E", OfMinor(100, EUR), 1}}, limit)
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("want ErrCurrencyMismatch, got %v", err)
	}
	if _, err := BestBatch([]Payout{{"Z", OfMinor(0, USD), 1}}, limit); err == nil {
		t.Fatal("non-positive amount must be rejected")
	}
}

func TestLcsClrsExample(t *testing.T) {
	x := []string{"A", "B", "C", "B", "D", "A", "B"}
	y := []string{"B", "D", "C", "A", "B", "A"}
	if LcsLength(x, y) != 4 {
		t.Fatalf("want 4, got %d", LcsLength(x, y))
	}
	al := LcsAlign(x, y)
	if len(al.Common) != 4 || !isSubsequence(al.Common, x) || !isSubsequence(al.Common, y) {
		t.Fatalf("bad alignment %v", al)
	}
	if len(al.LeftOnly)+len(al.Common) != len(x) || len(al.RightOnly)+len(al.Common) != len(y) {
		t.Fatal("leftovers plus common must cover each side")
	}
}

func TestLcsBoundaries(t *testing.T) {
	x := []string{"A", "B", "C"}
	same := LcsAlign(x, x)
	if !reflect.DeepEqual(same.Common, x) || len(same.LeftOnly) != 0 {
		t.Fatal("LCS of identical lists is the list")
	}
	if LcsLength([]string{"A", "B"}, []string{"C", "D"}) != 0 || LcsLength(nil, x) != 0 {
		t.Fatal("disjoint or empty gives 0")
	}
}

func TestStatementAlignment(t *testing.T) {
	bank := []string{"DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00"}
	ledger := []string{"DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99"}
	want := Alignment{
		Common:    []string{"DEP 500.00", "CARD 19.99", "CARD 7.00"},
		LeftOnly:  []string{"CARD 42.10", "FEE 2.50"},
		RightOnly: []string{"CARD 42.10", "REFUND 19.99"},
	}
	if got := LcsAlign(bank, ledger); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
