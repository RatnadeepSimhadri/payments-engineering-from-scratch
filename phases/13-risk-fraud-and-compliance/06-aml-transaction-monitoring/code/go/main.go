// Lesson: phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring/docs/en.md
// AML Transaction Monitoring and Structuring Detection
// Run:  go run .
// Test: go test ./...
// Sources: 31 U.S.C. 5324 and 31 CFR 1010.311 (currency transaction reports, structuring); FATF Recommendations 10 and 20;
// FinCEN SAR filing instructions. Go 1.22+, standard library only. Every account and name is synthetic.
// Same design as the Java and Python versions; java.util.Random is ported so the seeded population matches.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01. Integer minor units; never a float.
// ---------------------------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("overflow")
)

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	p := a * b
	if p/b != a {
		return 0, ErrOverflow
	}
	return p, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

// BasisPoints is this * bps / 10000, rounded HALF_EVEN. Used for "at least 80% of the threshold" style bands.
func (m Money) BasisPoints(bps int64) (Money, error) {
	product, err := mulChecked(m.MinorUnits, bps)
	if err != nil {
		return Money{}, err
	}
	q, r := product/10000, product%10000 // Go's / and % truncate toward zero, exactly like Java's
	twice := r * 2
	if twice < 0 {
		twice = -twice
	}
	if twice > 10000 || (twice == 10000 && q%2 != 0) {
		if product < 0 {
			q--
		} else {
			q++
		}
	}
	return Money{q, m.Currency}, nil
}

// Compare panics on a currency mismatch: the scenarios only ever compare USD with USD, so a mismatch is a programming error.
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	default:
		return 0
	}
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// must unwraps a Money operation inside the detectors, where every operand is USD and the sums cannot overflow.
func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// ---------------------------------------------------------------------------------------------
// Step 1: the transaction. Direction and channel are what the scenarios filter on.
// ---------------------------------------------------------------------------------------------

type Direction int

const (
	In Direction = iota
	Out
)

func (d Direction) String() string { return [...]string{"IN", "OUT"}[d] }

type Channel int

const (
	Cash Channel = iota
	Wire
	ACH
	Card
)

func (c Channel) String() string { return [...]string{"CASH", "WIRE", "ACH", "CARD"}[c] }

// ErrNonPositiveAmount: a transaction amount must be positive.
var ErrNonPositiveAmount = errors.New("transaction amount must be positive")

type Transaction struct {
	ID             string
	AccountID      string
	CounterpartyID string
	Direction      Direction
	Channel        Channel
	Amount         Money
	Timestamp      int64
}

func NewTransaction(id, accountID, counterpartyID string, direction Direction, channel Channel, amount Money, timestamp int64) (Transaction, error) {
	if amount.MinorUnits <= 0 {
		return Transaction{}, fmt.Errorf("%w: %s", ErrNonPositiveAmount, amount)
	}
	return Transaction{id, accountID, counterpartyID, direction, channel, amount, timestamp}, nil
}

func (t Transaction) Brief() string {
	who := "to " + t.CounterpartyID
	if t.Direction == In {
		who = "from " + t.CounterpartyID
	}
	return fmt.Sprintf("%s %s %s %s %s @%s", t.ID, t.Direction, t.Channel, t.Amount, who, FormatHours(t.Timestamp))
}

const (
	Hour = int64(3600)
	Day  = 24 * Hour
)

// T0 is a fixed midnight epoch so output reads as "d2 13:00" rather than a Unix timestamp.
var T0 = int64(1_700_000_000) - (int64(1_700_000_000) % Day)

// FormatHours prints the day/hour offset from T0.
func FormatHours(ts int64) string {
	rel := ts - T0
	return fmt.Sprintf("d%d %02d:%02d", rel/Day, (rel%Day)/Hour, (rel%Hour)/60)
}

// ---------------------------------------------------------------------------------------------
// Step 2: scenarios as data. A scenario is a kind plus named parameters; the engine interprets them.
// ---------------------------------------------------------------------------------------------

type ScenarioKind int

const (
	Structuring ScenarioKind = iota
	RapidPassThrough
	Velocity
)

func (k ScenarioKind) String() string {
	return [...]string{"STRUCTURING", "RAPID_PASS_THROUGH", "VELOCITY"}[k]
}

type Severity int

const (
	Low Severity = iota
	Medium
	High
)

func (s Severity) String() string { return [...]string{"LOW", "MEDIUM", "HIGH"}[s] }

// ErrMissingParam is returned when a scenario lacks a parameter its detector needs.
var ErrMissingParam = errors.New("scenario missing parameter")

type Scenario struct {
	ID          string
	Kind        ScenarioKind
	Severity    Severity
	Params      map[string]int64
	paramOrder  []string // declaration order, for printing (Java's Map.of prints in an unspecified order)
	Description string
}

func NewScenario(id string, kind ScenarioKind, severity Severity, params map[string]int64, description string) Scenario {
	order := make([]string, 0, len(params))
	for k := range params {
		order = append(order, k)
	}
	sort.Strings(order)
	return Scenario{id, kind, severity, params, order, description}
}

func (s Scenario) Param(name string) (int64, error) {
	v, ok := s.Params[name]
	if !ok {
		return 0, fmt.Errorf("%w: scenario %s missing parameter %s", ErrMissingParam, s.ID, name)
	}
	return v, nil
}

// ParamString prints the parameters as Java's Map.toString does, in declaration order.
func (s Scenario) ParamString() string {
	parts := make([]string, 0, len(s.paramOrder))
	for _, k := range s.paramOrder {
		parts = append(parts, fmt.Sprintf("%s=%d", k, s.Params[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func withOrder(s Scenario, order ...string) Scenario {
	s.paramOrder = order
	return s
}

// StructuringScenario: cash deposits each under the reporting threshold but at least floorBps of it, summing to >= threshold inside windowSeconds.
func StructuringScenario(id string, thresholdMinor, floorBps, windowSeconds, minCount int64) Scenario {
	return withOrder(NewScenario(id, Structuring, High,
		map[string]int64{"thresholdMinor": thresholdMinor, "floorBps": floorBps, "windowSeconds": windowSeconds, "minCount": minCount},
		fmt.Sprintf("cash deposits just under the CTR threshold that aggregate above it within %d days", windowSeconds/Day)),
		"thresholdMinor", "floorBps", "windowSeconds", "minCount")
}

// RapidPassThroughScenario: inbound funds followed by outbound transfers of at least outBps of the inbound amount inside windowSeconds.
func RapidPassThroughScenario(id string, minInMinor, outBps, windowSeconds int64) Scenario {
	return withOrder(NewScenario(id, RapidPassThrough, High,
		map[string]int64{"minInMinor": minInMinor, "outBps": outBps, "windowSeconds": windowSeconds},
		fmt.Sprintf("funds received and at least %d%% sent onward within %d hours", outBps/100, windowSeconds/Hour)),
		"minInMinor", "outBps", "windowSeconds")
}

// VelocityScenario: too many transactions or too many distinct counterparties in a window; scored, alert at >= scoreThreshold.
func VelocityScenario(id string, windowSeconds, baselineCount, baselineCounterparties, scoreThreshold int64) Scenario {
	return withOrder(NewScenario(id, Velocity, Medium,
		map[string]int64{"windowSeconds": windowSeconds, "baselineCount": baselineCount, "baselineCounterparties": baselineCounterparties, "scoreThreshold": scoreThreshold},
		fmt.Sprintf("transaction count and counterparty spread far above baseline within %d hours", windowSeconds/Hour)),
		"windowSeconds", "baselineCount", "baselineCounterparties", "scoreThreshold")
}

// paramReader collects the first missing-parameter error so a detector can read several parameters in a row.
type paramReader struct {
	s   Scenario
	err error
}

func (p *paramReader) get(name string) int64 {
	v, err := p.s.Param(name)
	if err != nil && p.err == nil {
		p.err = err
	}
	return v
}

// ---------------------------------------------------------------------------------------------
// Step 3: alerts carry their evidence. An alert without the transactions behind it is an opinion.
// ---------------------------------------------------------------------------------------------

type Alert struct {
	ScenarioID string
	Kind       ScenarioKind
	Severity   Severity
	AccountID  string
	Score      int64
	Evidence   []Transaction
	Narrative  string
}

func (a Alert) WindowStart() int64 {
	if len(a.Evidence) == 0 {
		return 0
	}
	min := a.Evidence[0].Timestamp
	for _, tx := range a.Evidence {
		if tx.Timestamp < min {
			min = tx.Timestamp
		}
	}
	return min
}

func (a Alert) WindowEnd() int64 {
	if len(a.Evidence) == 0 {
		return 0
	}
	max := a.Evidence[0].Timestamp
	for _, tx := range a.Evidence {
		if tx.Timestamp > max {
			max = tx.Timestamp
		}
	}
	return max
}

func (a Alert) Total() Money {
	t := Zero(a.Evidence[0].Amount.Currency)
	for _, tx := range a.Evidence {
		t = must(t.Plus(tx.Amount))
	}
	return t
}

func (a Alert) Explain() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s %s] %s on %s score=%d %s..%s: %s", a.Severity, a.ScenarioID, a.Kind, a.AccountID, a.Score,
		FormatHours(a.WindowStart()), FormatHours(a.WindowEnd()), a.Narrative)
	for _, tx := range a.Evidence {
		sb.WriteString("\n    ")
		sb.WriteString(tx.Brief())
	}
	return sb.String()
}

// ---------------------------------------------------------------------------------------------
// Step 4: the engine. Groups by account, sorts by time, runs each scenario's detector, merges overlaps.
// ---------------------------------------------------------------------------------------------

// ErrDuplicateScenario is returned when two scenarios share an id.
var ErrDuplicateScenario = errors.New("duplicate scenario id")

type MonitoringEngine struct{ scenarios []Scenario }

func NewMonitoringEngine(scenarios []Scenario) (*MonitoringEngine, error) {
	ids := map[string]bool{}
	for _, s := range scenarios {
		if ids[s.ID] {
			return nil, fmt.Errorf("%w %s", ErrDuplicateScenario, s.ID)
		}
		ids[s.ID] = true
	}
	return &MonitoringEngine{append([]Scenario(nil), scenarios...)}, nil
}

func sortByTime(txs []Transaction) {
	sort.SliceStable(txs, func(i, j int) bool {
		if txs[i].Timestamp != txs[j].Timestamp {
			return txs[i].Timestamp < txs[j].Timestamp
		}
		return txs[i].ID < txs[j].ID
	})
}

// Evaluate runs every scenario over every account. The error is a scenario missing a parameter its detector needs.
func (e *MonitoringEngine) Evaluate(transactions []Transaction) ([]Alert, error) {
	byAccount := map[string][]Transaction{}
	for _, tx := range transactions {
		byAccount[tx.AccountID] = append(byAccount[tx.AccountID], tx)
	}
	accounts := make([]string, 0, len(byAccount))
	for a := range byAccount {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts) // Java's TreeMap order
	alerts := []Alert{}
	for _, account := range accounts {
		sorted := append([]Transaction(nil), byAccount[account]...)
		sortByTime(sorted)
		for _, s := range e.scenarios {
			var raw []Alert
			var err error
			switch s.Kind {
			case Structuring:
				raw, err = StructuringDetector(s, account, sorted)
			case RapidPassThrough:
				raw, err = RapidPassThroughDetector(s, account, sorted)
			default:
				raw, err = VelocityDetector(s, account, sorted)
			}
			if err != nil {
				return nil, err
			}
			alerts = append(alerts, MergeOverlapping(raw)...)
		}
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.ScenarioID < b.ScenarioID
	})
	return alerts, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// StructuringDetector is a rolling window over qualifying cash deposits: each in [floor, threshold), window sum >= threshold, count >= minCount.
func StructuringDetector(s Scenario, account string, sorted []Transaction) ([]Alert, error) {
	p := &paramReader{s: s}
	threshold := OfMinor(p.get("thresholdMinor"), USD)
	floorBps := p.get("floorBps")
	window := p.get("windowSeconds")
	minCount := int(p.get("minCount"))
	if p.err != nil {
		return nil, p.err
	}
	floor := must(threshold.BasisPoints(floorBps))
	qualifying := []Transaction{}
	for _, tx := range sorted {
		if tx.Direction == In && tx.Channel == Cash && tx.Amount.Compare(threshold) < 0 && tx.Amount.Compare(floor) >= 0 {
			qualifying = append(qualifying, tx)
		}
	}
	out := []Alert{}
	left := 0
	sum := Zero(USD)
	for right := 0; right < len(qualifying); right++ {
		sum = must(sum.Plus(qualifying[right].Amount))
		for qualifying[right].Timestamp-qualifying[left].Timestamp > window {
			sum = must(sum.Minus(qualifying[left].Amount))
			left++
		}
		count := right - left + 1
		if count >= minCount && sum.Compare(threshold) >= 0 {
			evidence := append([]Transaction(nil), qualifying[left:right+1]...)
			score := minInt64(100, 50+10*int64(count)+(sum.MinorUnits-threshold.MinorUnits)*10/threshold.MinorUnits)
			out = append(out, Alert{s.ID, s.Kind, s.Severity, account, score, evidence,
				fmt.Sprintf("%d cash deposits of %s to %s totalling %s within %d days", count, floor, threshold, sum, window/Day)})
		}
	}
	return out, nil
}

// RapidPassThroughDetector: for each inbound of at least minIn, outbound transfers inside the window totalling >= outBps of it.
func RapidPassThroughDetector(s Scenario, account string, sorted []Transaction) ([]Alert, error) {
	p := &paramReader{s: s}
	minIn := OfMinor(p.get("minInMinor"), USD)
	outBps, window := p.get("outBps"), p.get("windowSeconds")
	if p.err != nil {
		return nil, p.err
	}
	out := []Alert{}
	for i := 0; i < len(sorted); i++ {
		in := sorted[i]
		if in.Direction != In || in.Amount.Compare(minIn) < 0 {
			continue
		}
		required := must(in.Amount.BasisPoints(outBps))
		sentOn := Zero(USD)
		evidence := []Transaction{in}
		for j := i + 1; j < len(sorted) && sorted[j].Timestamp-in.Timestamp <= window; j++ {
			tx := sorted[j]
			if tx.Direction != Out || tx.Channel == Card {
				continue
			}
			sentOn = must(sentOn.Plus(tx.Amount))
			evidence = append(evidence, tx)
			if sentOn.Compare(required) >= 0 {
				break
			}
		}
		if sentOn.Compare(required) >= 0 {
			elapsedHours := (evidence[len(evidence)-1].Timestamp - in.Timestamp) / Hour
			score := minInt64(100, 60+(window/Hour-elapsedHours)*40/maxInt64(1, window/Hour))
			out = append(out, Alert{s.ID, s.Kind, s.Severity, account, score, evidence,
				fmt.Sprintf("%s in, %s out to %d counterparties within %dh", in.Amount, sentOn, len(evidence)-1, elapsedHours)})
		}
	}
	return out, nil
}

// VelocityDetector: score = 50 * count/baselineCount + 50 * counterparties/baselineCounterparties over the window; alert at >= scoreThreshold.
func VelocityDetector(s Scenario, account string, sorted []Transaction) ([]Alert, error) {
	p := &paramReader{s: s}
	window, baseCount, baseCp := p.get("windowSeconds"), p.get("baselineCount"), p.get("baselineCounterparties")
	scoreThreshold := p.get("scoreThreshold")
	if p.err != nil {
		return nil, p.err
	}
	out := []Alert{}
	left := 0
	for right := 0; right < len(sorted); right++ {
		for sorted[right].Timestamp-sorted[left].Timestamp > window {
			left++
		}
		win := sorted[left : right+1]
		cps := map[string]bool{}
		for _, tx := range win {
			cps[tx.CounterpartyID] = true
		}
		score := 50*int64(len(win))/baseCount + 50*int64(len(cps))/baseCp
		if score >= scoreThreshold {
			out = append(out, Alert{s.ID, s.Kind, s.Severity, account, minInt64(100, score), append([]Transaction(nil), win...),
				fmt.Sprintf("%d transactions with %d counterparties in %dh (baseline %d/%d)", len(win), len(cps), window/Hour, baseCount, baseCp)})
		}
	}
	return out, nil
}

// MergeOverlapping: alerts from the same scenario whose evidence overlaps are one pattern, not several. Keep the highest score.
func MergeOverlapping(raw []Alert) []Alert {
	merged := []Alert{}
	for _, a := range raw {
		if len(merged) > 0 {
			last := merged[len(merged)-1]
			lastIDs := map[string]bool{}
			for _, tx := range last.Evidence {
				lastIDs[tx.ID] = true
			}
			overlaps := false
			for _, tx := range a.Evidence {
				if lastIDs[tx.ID] {
					overlaps = true
					break
				}
			}
			if overlaps {
				seen := map[string]bool{}
				ev := []Transaction{}
				for _, tx := range append(append([]Transaction(nil), last.Evidence...), a.Evidence...) {
					if !seen[tx.ID] {
						seen[tx.ID] = true
						ev = append(ev, tx)
					}
				}
				sortByTime(ev)
				better := last
				if a.Score > last.Score {
					better = a
				}
				peak := better.Narrative
				if idx := strings.Index(peak, "; merged"); idx >= 0 {
					peak = peak[:idx]
				}
				mergedAlert := Alert{a.ScenarioID, a.Kind, a.Severity, a.AccountID, better.Score, ev, peak}
				merged[len(merged)-1] = Alert{a.ScenarioID, a.Kind, a.Severity, a.AccountID, better.Score, ev,
					fmt.Sprintf("%s; merged window holds %d transactions totalling %s", peak, len(ev), mergedAlert.Total())}
				continue
			}
		}
		merged = append(merged, a)
	}
	return merged
}

// ---------------------------------------------------------------------------------------------
// Step 5: a seeded synthetic population with planted patterns, so recall and false positives are measurable.
// ---------------------------------------------------------------------------------------------

// JavaRandom is java.util.Random's 48-bit LCG with nextInt(bound), so the seeded population is the Java one.
type JavaRandom struct{ seed int64 }

const (
	lcgMultiplier = 0x5DEECE66D
	lcgAddend     = 0xB
	lcgMask       = (int64(1) << 48) - 1
)

func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ lcgMultiplier) & lcgMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*lcgMultiplier + lcgAddend) & lcgMask
	return int32(r.seed >> (48 - bits))
}

func (r *JavaRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	if bound&(-bound) == bound {
		return int((int64(bound) * int64(r.next(31))) >> 31)
	}
	for {
		bits := r.next(31)
		val := bits % int32(bound)
		if bits-val+int32(bound-1) >= 0 {
			return int(val)
		}
	}
}

type SyntheticGenerator struct {
	rng     *JavaRandom
	seq     int
	Planted map[string]string // account -> pattern it carries
	planted []string          // insertion order
}

func NewSyntheticGenerator(seed int64) *SyntheticGenerator {
	return &SyntheticGenerator{rng: NewJavaRandom(seed), Planted: map[string]string{}}
}

func (g *SyntheticGenerator) plant(account, pattern string) {
	if _, ok := g.Planted[account]; !ok {
		g.planted = append(g.planted, account)
	}
	g.Planted[account] = pattern
}

// PlantedAccounts returns the planted accounts in insertion order (Java's LinkedHashMap keySet).
func (g *SyntheticGenerator) PlantedAccounts() []string { return append([]string(nil), g.planted...) }

func (g *SyntheticGenerator) nextID() string {
	g.seq++
	return fmt.Sprintf("tx%05d", g.seq)
}

func (g *SyntheticGenerator) tx(account, cp string, d Direction, ch Channel, minor, ts int64) Transaction {
	t, err := NewTransaction(g.nextID(), account, cp, d, ch, OfMinor(minor, USD), ts)
	if err != nil {
		panic(err) // the generator only produces positive amounts
	}
	return t
}

// NormalAccount is ordinary retail behaviour: payroll in, card spend out, the odd cash deposit well under the threshold.
func (g *SyntheticGenerator) NormalAccount(account string, t0 int64, days int) []Transaction {
	out := []Transaction{}
	for d := 0; d < days; d++ {
		day := t0 + int64(d)*Day
		if d%14 == 0 {
			out = append(out, g.tx(account, "employer-"+account, In, ACH, int64(180_000+g.rng.NextInt(60_000)), day+6*Hour))
		}
		spends := 1 + g.rng.NextInt(4)
		for i := 0; i < spends; i++ {
			// Java evaluates the call's arguments left to right: counterparty, amount, hour, minute.
			merchant := g.rng.NextInt(40)
			amount := int64(500 + g.rng.NextInt(12_000))
			hour := int64(8 + g.rng.NextInt(12))
			minute := int64(g.rng.NextInt(3600))
			out = append(out, g.tx(account, fmt.Sprintf("merchant-%d", merchant), Out, Card, amount, day+hour*Hour+minute))
		}
		if g.rng.NextInt(10) == 0 {
			out = append(out, g.tx(account, "branch", In, Cash, int64(2_000+g.rng.NextInt(300_000)), day+10*Hour))
		}
	}
	return out
}

// Structurer is the classic: N cash deposits each a little under the threshold, spread over a few days.
func (g *SyntheticGenerator) Structurer(account string, t0 int64, deposits int, thresholdMinor int64) []Transaction {
	g.plant(account, "STRUCTURING")
	out := g.NormalAccount(account, t0, 10)
	for i := 0; i < deposits; i++ {
		amount := thresholdMinor - 5_000 - int64(g.rng.NextInt(60_000)) // 9,400.00 to 9,950.00 on a 10,000.00 threshold
		branch := g.rng.NextInt(3)
		out = append(out, g.tx(account, fmt.Sprintf("branch-%d", branch), In, Cash, amount, t0+2*Day+int64(i)*7*Hour))
	}
	return out
}

// PassThrough is a mule: a large wire in, then almost all of it wired on to several counterparties within hours.
func (g *SyntheticGenerator) PassThrough(account string, t0, inMinor int64, legs int) []Transaction {
	g.plant(account, "RAPID_PASS_THROUGH")
	out := g.NormalAccount(account, t0, 10)
	in := t0 + 3*Day + 9*Hour
	out = append(out, g.tx(account, "shell-co", In, Wire, inMinor, in))
	remaining := inMinor * 9_600 / 10_000 // 96% sent on, integer arithmetic
	for i := 0; i < legs; i++ {
		leg := remaining / int64(legs-i)
		if i == legs-1 {
			leg = remaining
		}
		remaining -= leg
		out = append(out, g.tx(account, fmt.Sprintf("beneficiary-%d", i), Out, Wire, leg, in+int64(2+i*3)*Hour))
	}
	return out
}

// Burst: dozens of small transfers to many counterparties in one afternoon.
func (g *SyntheticGenerator) Burst(account string, t0 int64, count int) []Transaction {
	g.plant(account, "VELOCITY")
	out := g.NormalAccount(account, t0, 10)
	start := t0 + 5*Day + 13*Hour
	for i := 0; i < count; i++ {
		out = append(out, g.tx(account, fmt.Sprintf("peer-%d", i), Out, ACH, int64(2_000+g.rng.NextInt(20_000)), start+int64(i)*5*60))
	}
	return out
}

// CashBusiness is a legitimate cash-intensive business (a laundromat, a food truck): daily cash deposits that sit near the floor.
func (g *SyntheticGenerator) CashBusiness(account string, t0 int64, days int) []Transaction {
	out := []Transaction{}
	for d := 0; d < days; d++ {
		day := t0 + int64(d)*Day
		out = append(out, g.tx(account, "branch-0", In, Cash, int64(550_000+g.rng.NextInt(400_000)), day+18*Hour))
		if d%7 == 6 {
			supplier := g.rng.NextInt(5)
			amount := int64(1_500_000 + g.rng.NextInt(1_000_000))
			out = append(out, g.tx(account, fmt.Sprintf("supplier-%d", supplier), Out, ACH, amount, day+9*Hour))
		}
	}
	return out
}

func (g *SyntheticGenerator) Population(normalAccounts int, t0 int64) []Transaction {
	all := []Transaction{}
	for i := 0; i < normalAccounts; i++ {
		all = append(all, g.NormalAccount(fmt.Sprintf("acct-%03d", i), t0, 10)...)
	}
	all = append(all, g.CashBusiness("acct-laundromat", t0, 10)...)
	all = append(all, g.CashBusiness("acct-foodtruck", t0, 10)...)
	all = append(all, g.Structurer("acct-structurer", t0, 10, 1_000_000)...)
	all = append(all, g.PassThrough("acct-mule", t0, 4_850_000, 3)...)
	all = append(all, g.Burst("acct-burst", t0, 36)...)
	return all
}

func StandardScenarios() []Scenario {
	return []Scenario{
		StructuringScenario("STR-01", 1_000_000, 8_000, 3*Day, 3),
		RapidPassThroughScenario("RPT-01", 1_000_000, 9_000, 24*Hour),
		VelocityScenario("VEL-01", 6*Hour, 6, 4, 150),
	}
}

func mustEngine(scenarios []Scenario) *MonitoringEngine {
	e, err := NewMonitoringEngine(scenarios)
	if err != nil {
		panic(err)
	}
	return e
}

func mustEvaluate(e *MonitoringEngine, txs []Transaction) []Alert {
	alerts, err := e.Evaluate(txs)
	if err != nil {
		panic(err)
	}
	return alerts
}

func main() {
	fmt.Println("== Scenarios (data, not code) ==")
	for _, s := range StandardScenarios() {
		fmt.Printf("  %-7s %-18s %-6s %s\n    params %s\n", s.ID, s.Kind, s.Severity, s.Description, s.ParamString())
	}

	fmt.Println("\n== Hand-built case: ten deposits of 9,900.00 over three days ==")
	tenDeposits := []Transaction{}
	for i := 0; i < 10; i++ {
		t, _ := NewTransaction(fmt.Sprintf("dep%d", i), "acct-demo", "branch", In, Cash, OfMinor(990_000, USD), T0+int64(i)*7*Hour)
		tenDeposits = append(tenDeposits, t)
	}
	engine := mustEngine(StandardScenarios())
	for _, a := range mustEvaluate(engine, tenDeposits) {
		fmt.Println("  " + strings.ReplaceAll(a.Explain(), "\n", "\n  "))
	}

	fmt.Println("\n== Synthetic population: 60 ordinary accounts, two cash businesses, three planted patterns (seed 7) ==")
	gen := NewSyntheticGenerator(7)
	population := gen.Population(60, T0)
	alerts := mustEvaluate(engine, population)
	fmt.Printf("  transactions: %d, alerts: %d\n", len(population), len(alerts))
	for _, a := range alerts {
		planted, ok := gen.Planted[a.AccountID]
		if !ok {
			planted = "none"
		}
		fmt.Printf("  %-5s %-7s %-16s score=%3d planted=%-18s %s\n", a.Severity, a.ScenarioID, a.AccountID, a.Score, planted, a.Narrative)
	}
	truePositives, falsePositives := 0, 0
	found := map[string]bool{}
	for _, a := range alerts {
		if a.Kind.String() == gen.Planted[a.AccountID] {
			truePositives++
			found[a.AccountID] = true
		} else {
			falsePositives++
		}
	}
	fmt.Printf("  planted patterns found: %d of %d; true-positive alerts %d, false-positive alerts %d\n", len(found), len(gen.Planted), truePositives, falsePositives)

	fmt.Println("\n== The first alert in full ==")
	if len(alerts) > 0 {
		fmt.Println("  " + strings.ReplaceAll(alerts[0].Explain(), "\n", "\n  "))
	}
}
