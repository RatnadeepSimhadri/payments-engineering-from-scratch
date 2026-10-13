// Lesson: phases/02-algorithms/06-dynamic-programming/docs/en.md
// Dynamic Programming: From Recurrence to Table
// Run:  go run .
// Test: go test ./...
// Sources: Bellman, "Dynamic Programming" (1957); CLRS ch. 15 (LCS); Kellerer, Pferschy, Pisinger,
// "Knapsack Problems" (2004). Money design copied from phases/10-money-and-ledgers/01-representing-money.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
)

// INF marks an unreachable amount in the recurrence forms; half of MaxInt32 so INF+1 cannot overflow.
const INF = 1 << 30

// Unreachable is the coin count reported when no combination of denominations sums to the amount.
const Unreachable = -1

// Currency is an ISO 4217 currency: code and minor-unit exponent. Trimmed copy of Phase 10 Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	INR = Currency{"INR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// Money is an exact amount of one currency as a signed count of minor units. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

// Compare returns -1, 0, or 1; it panics on a currency mismatch (a programming error).
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	}
	return 0
}

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// ChangeResult is how many instruments and how many of each denomination (parallel to the input).
type ChangeResult struct {
	CoinCount int
	Used      []int
}

func (r ChangeResult) Reachable() bool { return r.CoinCount != Unreachable }

// Coins expands the counts into face values, largest first, for display.
func (r ChangeResult) Coins(denominations []int) []int {
	out := []int{}
	for i, d := range denominations {
		for k := 0; k < r.Used[i]; k++ {
			out = append(out, d)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

func validate(denominations []int, amount int) error {
	if amount < 0 {
		return fmt.Errorf("amount must be non-negative: %d", amount)
	}
	if len(denominations) == 0 {
		return errors.New("need at least one denomination")
	}
	for _, d := range denominations {
		if d <= 0 {
			return fmt.Errorf("denominations must be positive: %d", d)
		}
	}
	return nil
}

// MinCoinsRecursive is the recurrence as written: f(0)=0, f(a)=1+min over d<=a of f(a-d). Exponential.
func MinCoinsRecursive(denominations []int, amount int, calls *int64) (int, error) {
	if err := validate(denominations, amount); err != nil {
		return 0, err
	}
	var recurse func(a int) int
	recurse = func(a int) int {
		*calls++
		if a == 0 {
			return 0
		}
		best := INF
		for _, d := range denominations {
			if d <= a {
				if sub := recurse(a - d); sub != INF && sub+1 < best {
					best = sub + 1
				}
			}
		}
		return best
	}
	return recurse(amount), nil
}

// MinCoinsMemo is the same recurrence with every subproblem solved once. O(amount * len(denominations)).
func MinCoinsMemo(denominations []int, amount int, calls *int64) (int, error) {
	if err := validate(denominations, amount); err != nil {
		return 0, err
	}
	memo := map[int]int{}
	var memoised func(a int) int
	memoised = func(a int) int {
		*calls++
		if a == 0 {
			return 0
		}
		if known, ok := memo[a]; ok {
			return known
		}
		best := INF
		for _, d := range denominations {
			if d <= a {
				if sub := memoised(a - d); sub != INF && sub+1 < best {
					best = sub + 1
				}
			}
		}
		memo[a] = best
		return best
	}
	return memoised(amount), nil
}

// MinCoinsTable is the bottom-up table plus a choice array for reconstruction. Ties go to the lowest index.
func MinCoinsTable(denominations []int, amount int) (ChangeResult, error) {
	if err := validate(denominations, amount); err != nil {
		return ChangeResult{}, err
	}
	best := make([]int, amount+1)
	choice := make([]int, amount+1)
	for a := range best {
		best[a], choice[a] = INF, -1
	}
	best[0] = 0
	for a := 1; a <= amount; a++ {
		for i, d := range denominations {
			if d <= a && best[a-d] != INF && best[a-d]+1 < best[a] {
				best[a] = best[a-d] + 1
				choice[a] = i
			}
		}
	}
	used := make([]int, len(denominations))
	if best[amount] == INF {
		return ChangeResult{Unreachable, used}, nil
	}
	for a := amount; a > 0; a -= denominations[choice[a]] {
		used[choice[a]]++
	}
	return ChangeResult{best[amount], used}, nil
}

// CountWays counts distinct multisets summing to amount. Denominations OUTER so {1,2} and {2,1} count once.
func CountWays(denominations []int, amount int) (int64, error) {
	if err := validate(denominations, amount); err != nil {
		return 0, err
	}
	ways := make([]int64, amount+1)
	ways[0] = 1
	for _, d := range denominations {
		for a := d; a <= amount; a++ {
			next := ways[a] + ways[a-d]
			if next < ways[a] {
				return 0, errors.New("int64 overflow in count of ways")
			}
			ways[a] = next
		}
	}
	return ways[amount], nil
}

// GreedyCoins is largest-first greedy, for comparison. Correct for canonical systems, wrong in general.
func GreedyCoins(denominations []int, amount int) (int, error) {
	if err := validate(denominations, amount); err != nil {
		return 0, err
	}
	sorted := append([]int(nil), denominations...)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
	count := 0
	for _, d := range sorted {
		count += amount / d
		amount %= d
	}
	if amount != 0 {
		return Unreachable, nil
	}
	return count, nil
}

// Payout is a pending payout: amount (knapsack weight) and priority score (knapsack value).
type Payout struct {
	ID       string
	Amount   Money
	Priority int64
}

// Batch is the chosen payouts, their total, and the priority achieved.
type Batch struct {
	Chosen   []Payout
	Total    Money
	Priority int64
}

// BestBatch is 0/1 knapsack over payout amounts in minor units. O(n * limit) time and space: pseudo-polynomial.
func BestBatch(payouts []Payout, limit Money) (Batch, error) {
	if limit.MinorUnits < 0 {
		return Batch{}, errors.New("limit must be non-negative")
	}
	for _, p := range payouts {
		if p.Amount.Currency != limit.Currency {
			return Batch{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, limit.Currency.Code, p.Amount.Currency.Code)
		}
		if p.Amount.MinorUnits <= 0 {
			return Batch{}, fmt.Errorf("payout amounts must be positive: %s", p.ID)
		}
		if p.Priority < 0 {
			return Batch{}, fmt.Errorf("priority must be non-negative: %s", p.ID)
		}
	}
	n, w := len(payouts), int(limit.MinorUnits)
	best := make([][]int64, n+1) // best[i][c]: max priority using the first i payouts under capacity c
	for i := range best {
		best[i] = make([]int64, w+1)
	}
	for i := 1; i <= n; i++ {
		p := payouts[i-1]
		weight := int(p.Amount.MinorUnits)
		for c := 0; c <= w; c++ {
			skip := best[i-1][c]
			take := int64(-1)
			if weight <= c {
				take = best[i-1][c-weight] + p.Priority
			}
			if take > skip {
				best[i][c] = take
			} else {
				best[i][c] = skip
			}
		}
	}
	chosen := []Payout{}
	total := OfMinor(0, limit.Currency)
	c := w
	for i := n; i >= 1; i-- {
		if best[i][c] != best[i-1][c] { // this row's value changed: the item was taken
			p := payouts[i-1]
			chosen = append([]Payout{p}, chosen...)
			total, _ = total.Plus(p.Amount)
			c -= int(p.Amount.MinorUnits)
		}
	}
	return Batch{chosen, total, best[n][w]}, nil
}

// GreedyBatch is highest-priority-first greedy, for comparison.
func GreedyBatch(payouts []Payout, limit Money) Batch {
	order := append([]Payout(nil), payouts...)
	sort.SliceStable(order, func(a, b int) bool { return order[a].Priority > order[b].Priority })
	chosen := []Payout{}
	total := OfMinor(0, limit.Currency)
	var priority int64
	for _, p := range order {
		if next, _ := total.Plus(p.Amount); next.Compare(limit) <= 0 {
			chosen = append(chosen, p)
			total = next
			priority += p.Priority
		}
	}
	return Batch{chosen, total, priority}
}

// Alignment is which lines two statements share in order, and which are left over on each side.
type Alignment struct {
	Common    []string
	LeftOnly  []string
	RightOnly []string
}

// LcsTable: length[i][j] = LCS of a[:i] and b[:j]. O(|a| * |b|) time and space.
func LcsTable(a, b []string) [][]int {
	length := make([][]int, len(a)+1)
	for i := range length {
		length[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				length[i][j] = length[i-1][j-1] + 1
			} else if length[i-1][j] >= length[i][j-1] {
				length[i][j] = length[i-1][j]
			} else {
				length[i][j] = length[i][j-1]
			}
		}
	}
	return length
}

func LcsLength(a, b []string) int { return LcsTable(a, b)[len(a)][len(b)] }

// LcsAlign walks the table back from the corner. Ties prefer consuming from a, so the result is deterministic.
func LcsAlign(a, b []string) Alignment {
	length := LcsTable(a, b)
	var common, leftOnly, rightOnly []string
	i, j := len(a), len(b)
	for i > 0 && j > 0 {
		switch {
		case a[i-1] == b[j-1]:
			common = append(common, a[i-1])
			i--
			j--
		case length[i-1][j] >= length[i][j-1]:
			leftOnly = append(leftOnly, a[i-1])
			i--
		default:
			rightOnly = append(rightOnly, b[j-1])
			j--
		}
	}
	for i > 0 {
		i--
		leftOnly = append(leftOnly, a[i])
	}
	for j > 0 {
		j--
		rightOnly = append(rightOnly, b[j])
	}
	return Alignment{reversed(common), reversed(leftOnly), reversed(rightOnly)}
}

func reversed(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[len(xs)-1-i] = x
	}
	return out
}

func ids(payouts []Payout) []string {
	out := make([]string, 0, len(payouts))
	for _, p := range payouts {
		out = append(out, p.ID)
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Recurrence -> memo -> table: fewest coins for 20 with {1, 3, 4} ==")
	odd := []int{1, 3, 4}
	var naive, memo int64
	r1 := must(MinCoinsRecursive(odd, 20, &naive))
	r2 := must(MinCoinsMemo(odd, 20, &memo))
	r3 := must(MinCoinsTable(odd, 20))
	fmt.Printf("recursive: %d coins after %d calls\n", r1, naive)
	fmt.Printf("memoised : %d coins after %d calls\n", r2, memo)
	fmt.Printf("table    : %d coins = %v\n", r3.CoinCount, r3.Coins(odd))

	fmt.Println("\n== Greedy is wrong for non-canonical denominations ==")
	fmt.Printf("{1,3,4} for 6: greedy=%d (4+1+1), optimal=%d (3+3)\n",
		must(GreedyCoins(odd, 6)), must(MinCoinsTable(odd, 6)).CoinCount)
	vouchers := []int{100, 1500, 2500} // 1.00, 15.00, 25.00 USD gift instruments, in cents
	plan := must(MinCoinsTable(vouchers, 3000))
	fmt.Printf("30.00 USD in {1, 15, 25} vouchers: greedy=%d instruments, table=%d -> %v cents\n",
		must(GreedyCoins(vouchers, 3000)), plan.CoinCount, plan.Coins(vouchers))
	us := []int{1, 5, 10, 25}
	sixtyThree := must(MinCoinsTable(us, 63))
	fmt.Printf("0.63 USD in US coins: %d coins = %v (greedy agrees: %d)\n",
		sixtyThree.CoinCount, sixtyThree.Coins(us), must(GreedyCoins(us, 63)))
	fmt.Printf("unreachable: 0.03 with only {2} -> CoinCount=%d\n", must(MinCoinsTable([]int{2}, 3)).CoinCount)

	fmt.Println("\n== Number of ways ==")
	fmt.Printf("ways to make 5 from {1,2,5} = %d  (5, 2+2+1, 2+1+1+1, 1+1+1+1+1)\n", must(CountWays([]int{1, 2, 5}, 5)))
	fmt.Printf("ways to make 1.00 USD from US coins = %d\n", must(CountWays(us, 100)))

	fmt.Println("\n== 0/1 knapsack: payout batch under a 100.00 USD settlement limit ==")
	payouts := []Payout{
		{"P-A", OfMinor(6000, USD), 100},
		{"P-B", OfMinor(5000, USD), 90},
		{"P-C", OfMinor(5000, USD), 90},
		{"P-D", OfMinor(3000, USD), 10},
		{"P-E", OfMinor(1000, USD), 5},
		{"P-F", OfMinor(12000, USD), 999},
	}
	limit := OfMinor(10000, USD)
	greedy := GreedyBatch(payouts, limit)
	best := must(BestBatch(payouts, limit))
	fmt.Printf("greedy by priority: %v total=%v priority=%d\n", ids(greedy.Chosen), greedy.Total, greedy.Priority)
	fmt.Printf("knapsack table   : %v total=%v priority=%d\n", ids(best.Chosen), best.Total, best.Priority)

	fmt.Println("\n== LCS: statement lines in order ==")
	bank := []string{"DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00"}
	ledger := []string{"DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99"}
	al := LcsAlign(bank, ledger)
	fmt.Printf("bank   : %q\n", bank)
	fmt.Printf("ledger : %q\n", ledger)
	fmt.Printf("matched in order (%d): %q\n", len(al.Common), al.Common)
	fmt.Printf("bank only   : %q\n", al.LeftOnly)
	fmt.Printf("ledger only : %q\n", al.RightOnly)
}
