// Lesson: phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks/docs/en.md
// A Rules Engine with Velocity Checks
// Run:  go run .
// Test: go test ./...
// Sources: ISO 4217 (currency exponents); ISO/IEC 7812 (issuer identification numbers / BINs);
//
//	PCI DSS v4.0 Requirement 3 for why a card is identified by a token, never a PAN.
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01: integer minor units paired with an ISO 4217 currency.
// ---------------------------------------------------------------------------------------------

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

// MinorUnitsPerMajor is 10^Exponent.
func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("integer overflow")
	ErrConfiguration    = errors.New("configuration error")
)

// Money is an exact integer count of minor units of exactly one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minor int64, c Currency) Money { return Money{minor, c} }

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	p := a * b
	if p/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return p, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Parse reads "12.34" exactly; more decimals than the currency allows is an error, never a rounding.
func Parse(text string, c Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, fmt.Errorf("empty amount")
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(frac) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > c.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows (%d)", text, c.Code, c.Exponent)
	}
	for len(frac) < c.Exponent {
		frac += "0"
	}
	var w, f int64
	fmt.Sscan(whole, &w)
	if frac != "" {
		fmt.Sscan(frac, &f)
	}
	scaled, err := mulChecked(w, c.MinorUnitsPerMajor())
	if err != nil {
		return Money{}, err
	}
	minor, err := addChecked(scaled, f)
	if err != nil {
		return Money{}, err
	}
	if negative {
		minor = -minor
	}
	return Money{minor, c}, nil
}

// MustParse is for literals in demos and tests, where a typo should stop the program.
func MustParse(text string, c Currency) Money {
	m, err := Parse(text, c)
	if err != nil {
		panic(err)
	}
	return m
}

func (m Money) requireSameCurrency(o Money) error {
	if m.Currency != o.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, o.Currency.Code)
	}
	return nil
}

func (m Money) Plus(o Money) (Money, error) {
	if err := m.requireSameCurrency(o); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, o.MinorUnits)
	return Money{sum, m.Currency}, err
}

// Compare orders two amounts of the same currency; a mismatch is an error, not a silent false.
func (m Money) Compare(o Money) (int, error) {
	if err := m.requireSameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.MinorUnits < o.MinorUnits:
		return -1, nil
	case m.MinorUnits > o.MinorUnits:
		return 1, nil
	}
	return 0, nil
}

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	result := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		result += "." + fmt.Sprintf("%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		result = "-" + result
	}
	return result + " " + m.Currency.Code
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Nothing in this file calls time.Now().
// ---------------------------------------------------------------------------------------------

// Clock is the only source of time the engine knows.
type Clock interface {
	NowMillis() int64
}

// ManualClock is moved by hand. Monotone: Advance never goes backwards.
type ManualClock struct{ now int64 }

func NewManualClock(start int64) *ManualClock { return &ManualClock{now: start} }

func (c *ManualClock) NowMillis() int64 { return c.now }

func (c *ManualClock) Advance(millis int64) {
	if millis < 0 {
		panic("clock never goes backwards")
	}
	c.now += millis
}

// ---------------------------------------------------------------------------------------------
// The input: one authorization attempt. The card is a token (PCI DSS), never a PAN.
// ---------------------------------------------------------------------------------------------

type Transaction struct {
	ID          string
	AccountID   string
	CardToken   string
	BIN         string
	Country     string
	CardAgeDays int64
	Amount      Money
}

// Facts are the values a rule can see before velocity enrichment. Keys are the rule Field names.
// Ordered keys are kept in FactOrder so explanations print deterministically.
type Facts struct {
	values map[string]any
	order  []string
}

func NewFacts() *Facts { return &Facts{values: map[string]any{}} }

func (f *Facts) Put(name string, v any) {
	if _, ok := f.values[name]; !ok {
		f.order = append(f.order, name)
	}
	f.values[name] = v
}

func (f *Facts) Get(name string) (any, bool) {
	v, ok := f.values[name]
	return v, ok
}

func (f *Facts) Remove(name string) { delete(f.values, name) }

func (tx Transaction) Facts() *Facts {
	f := NewFacts()
	f.Put("amount", tx.Amount)
	f.Put("bin", tx.BIN)
	f.Put("country", tx.Country)
	f.Put("card_age_days", tx.CardAgeDays)
	f.Put("account_id", tx.AccountID)
	return f
}

// ---------------------------------------------------------------------------------------------
// Velocity: a sliding window approximated by a ring of time buckets.
// ---------------------------------------------------------------------------------------------

type ring struct {
	starts []int64 // math.MinInt64 marks a never-used slot
	counts []int64
	sums   []int64
}

func newRing(n int) *ring {
	r := &ring{make([]int64, n), make([]int64, n), make([]int64, n)}
	for i := range r.starts {
		r.starts[i] = math.MinInt64
	}
	return r
}

// SlidingWindowCounter counts and sums events per key over a window of W ms split into n buckets
// of width b = W / n. Recording lands in bucket floor(now / b); a slot holding an older lap is
// reset first. Queries add every slot whose start is newer than now - W. Memory per key is O(n)
// and the effective window is between W - b and W.
type SlidingWindowCounter struct {
	WindowMillis int64
	BucketMillis int64
	Buckets      int
	Currency     Currency
	rings        map[string]*ring
}

func NewSlidingWindowCounter(windowMillis int64, buckets int, c Currency) (*SlidingWindowCounter, error) {
	if buckets <= 0 {
		return nil, fmt.Errorf("%w: buckets must be positive", ErrConfiguration)
	}
	if windowMillis <= 0 || windowMillis%int64(buckets) != 0 {
		return nil, fmt.Errorf("%w: window must be a positive multiple of the bucket count", ErrConfiguration)
	}
	return &SlidingWindowCounter{windowMillis, windowMillis / int64(buckets), buckets, c, map[string]*ring{}}, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Record one event for the key at time now. One counter is one currency: others are a bug.
func (c *SlidingWindowCounter) Record(key string, amount Money, now int64) error {
	if amount.Currency != c.Currency {
		return fmt.Errorf("%w: counter is %s, amount is %s", ErrCurrencyMismatch, c.Currency.Code, amount.Currency.Code)
	}
	r, ok := c.rings[key]
	if !ok {
		r = newRing(c.Buckets)
		c.rings[key] = r
	}
	lap := floorDiv(now, c.BucketMillis)
	bucketStart := lap * c.BucketMillis
	idx := int(((lap % int64(c.Buckets)) + int64(c.Buckets)) % int64(c.Buckets))
	if r.starts[idx] != bucketStart { // slot holds a previous lap: reset it
		r.starts[idx], r.counts[idx], r.sums[idx] = bucketStart, 0, 0
	}
	r.counts[idx]++
	sum, err := addChecked(r.sums[idx], amount.MinorUnits)
	if err != nil {
		return err
	}
	r.sums[idx] = sum
	return nil
}

func (c *SlidingWindowCounter) live(r *ring, now int64, i int) bool {
	return r.starts[i] > now-c.WindowMillis
}

// Count returns the events for the key inside the window ending at now.
func (c *SlidingWindowCounter) Count(key string, now int64) int64 {
	r, ok := c.rings[key]
	if !ok {
		return 0
	}
	var n int64
	for i := 0; i < c.Buckets; i++ {
		if c.live(r, now, i) {
			n += r.counts[i]
		}
	}
	return n
}

// Sum returns the amounts for the key inside the window ending at now.
func (c *SlidingWindowCounter) Sum(key string, now int64) Money {
	var total int64
	if r, ok := c.rings[key]; ok {
		for i := 0; i < c.Buckets; i++ {
			if c.live(r, now, i) {
				total += r.sums[i]
			}
		}
	}
	return OfMinor(total, c.Currency)
}

// EvictIdle drops keys with no live bucket. For a housekeeping job, never the hot path.
func (c *SlidingWindowCounter) EvictIdle(now int64) int {
	before := len(c.rings)
	for key, r := range c.rings {
		alive := false
		for i := 0; i < c.Buckets && !alive; i++ {
			alive = c.live(r, now, i)
		}
		if !alive {
			delete(c.rings, key)
		}
	}
	return before - len(c.rings)
}

func (c *SlidingWindowCounter) TrackedKeys() int { return len(c.rings) }

// Dimension picks which transaction field keys a counter.
type Dimension int

const (
	DimensionCard Dimension = iota
	DimensionAccount
)

func (d Dimension) KeyFor(tx Transaction) string {
	if d == DimensionCard {
		return "card:" + tx.CardToken
	}
	return "account:" + tx.AccountID
}

type Metric int

const (
	MetricCount Metric = iota
	MetricSum
)

type Definition struct {
	FactName  string
	Dimension Dimension
	Metric    Metric
	Counter   *SlidingWindowCounter
}

// VelocityService holds named counters; Enrich records the attempt and adds one fact per counter
// so the engine sees velocity as just another field.
type VelocityService struct {
	definitions []Definition
}

func NewVelocityService() *VelocityService { return &VelocityService{} }

func (v *VelocityService) Define(factName string, d Dimension, m Metric, counter *SlidingWindowCounter) error {
	for _, def := range v.definitions {
		if def.FactName == factName {
			return fmt.Errorf("%w: duplicate fact %s", ErrConfiguration, factName)
		}
	}
	v.definitions = append(v.definitions, Definition{factName, d, m, counter})
	return nil
}

// Enrich records the attempt, then returns the transaction facts plus one velocity fact per counter.
// The attempt counts itself; declined attempts are recorded too (a burst of declines is the card-testing signal).
func (v *VelocityService) Enrich(tx Transaction, now int64) (*Facts, error) {
	facts := tx.Facts()
	for _, d := range v.definitions {
		key := d.Dimension.KeyFor(tx)
		if err := d.Counter.Record(key, tx.Amount, now); err != nil {
			return nil, err
		}
		if d.Metric == MetricCount {
			facts.Put(d.FactName, d.Counter.Count(key, now))
		} else {
			facts.Put(d.FactName, d.Counter.Sum(key, now))
		}
	}
	return facts, nil
}

// ---------------------------------------------------------------------------------------------
// Rules as data: field, operator, value, action, priority, score, explanation.
// ---------------------------------------------------------------------------------------------

// Action is ordered by severity so "the harsher of two" is a max().
type Action int

const (
	Allow Action = iota
	Review
	Decline
)

func (a Action) String() string { return [...]string{"ALLOW", "REVIEW", "DECLINE"}[a] }

type Operator int

const (
	EQ Operator = iota
	NE
	GT
	GTE
	LT
	LTE
	IN
	NotIn
)

func (o Operator) String() string {
	return [...]string{"EQ", "NE", "GT", "GTE", "LT", "LTE", "IN", "NOT_IN"}[o]
}

// StringSet is the value type for IN / NOT_IN. It prints sorted, so explanations are deterministic.
type StringSet map[string]struct{}

func SetOf(items ...string) StringSet {
	s := StringSet{}
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

func (s StringSet) String() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ", ") + "]"
}

func typeName(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func compareFacts(a, b any) (int, error) {
	switch x := a.(type) {
	case int64:
		if y, ok := b.(int64); ok {
			switch {
			case x < y:
				return -1, nil
			case x > y:
				return 1, nil
			}
			return 0, nil
		}
	case Money:
		if y, ok := b.(Money); ok {
			return x.Compare(y) // currency mismatch surfaces as an error
		}
	}
	return 0, fmt.Errorf("%w: cannot order %s against %s", ErrConfiguration, typeName(a), typeName(b))
}

// Apply evaluates observed <op> expected. Types must agree; a mismatch is a configuration bug.
func (o Operator) Apply(observed, expected any) (bool, error) {
	switch o {
	case EQ:
		return observed == expected, nil
	case NE:
		return observed != expected, nil
	case IN, NotIn:
		set, ok := expected.(StringSet)
		if !ok {
			return false, fmt.Errorf("%w: IN / NOT_IN need a set, got %s", ErrConfiguration, typeName(expected))
		}
		s, ok := observed.(string)
		if !ok {
			return false, fmt.Errorf("%w: IN / NOT_IN need a string fact, got %s", ErrConfiguration, typeName(observed))
		}
		_, member := set[s]
		if o == IN {
			return member, nil
		}
		return !member, nil
	}
	cmp, err := compareFacts(observed, expected)
	if err != nil {
		return false, err
	}
	switch o {
	case GT:
		return cmp > 0, nil
	case GTE:
		return cmp >= 0, nil
	case LT:
		return cmp < 0, nil
	default:
		return cmp <= 0, nil
	}
}

// Rule is one declarative rule. Lower Priority is evaluated and, under FirstMatch, decided first.
// Score is only used under ScoreStrategy. Explanation is what an analyst or a customer reads.
type Rule struct {
	Name        string
	Field       string
	Operator    Operator
	Value       any
	Action      Action
	Priority    int
	Score       int
	Explanation string
	Shadow      bool // evaluated and traced, but never decides and never scores
}

func NewRule(name, field string, op Operator, value any, action Action, priority, score int, explanation string) (Rule, error) {
	switch {
	case strings.TrimSpace(name) == "":
		return Rule{}, fmt.Errorf("%w: rule needs a name", ErrConfiguration)
	case strings.TrimSpace(field) == "":
		return Rule{}, fmt.Errorf("%w: rule needs a field", ErrConfiguration)
	case value == nil:
		return Rule{}, fmt.Errorf("%w: rule needs a value", ErrConfiguration)
	case score < 0:
		return Rule{}, fmt.Errorf("%w: score must be non-negative", ErrConfiguration)
	case strings.TrimSpace(explanation) == "":
		return Rule{}, fmt.Errorf("%w: rule needs an explanation", ErrConfiguration)
	}
	return Rule{name, field, op, value, action, priority, score, explanation, false}, nil
}

// AsShadow returns the same rule, evaluated and traced but never acting: it cannot decide and
// its score is not counted.
func (r Rule) AsShadow() Rule {
	r.Shadow = true
	return r
}

// MustRule is for rule literals in demos and tests.
func MustRule(name, field string, op Operator, value any, action Action, priority, score int, explanation string) Rule {
	r, err := NewRule(name, field, op, value, action, priority, score, explanation)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rule) Describe() string {
	text := fmt.Sprintf("IF %s %s %v THEN %s (p%d, +%d)", r.Field, r.Operator, r.Value, r.Action, r.Priority, r.Score)
	if r.Shadow {
		text += " [shadow]"
	}
	return text
}

// RuleTrace is one line of the decision record: what the rule compared and whether it fired.
type RuleTrace struct {
	Rule        string
	Priority    int
	Field       string
	Observed    any // nil when the fact was absent
	Operator    Operator
	Expected    any
	Action      Action
	Score       int
	Fired       bool
	Shadow      bool
	Explanation string
}

type Strategy int

const (
	FirstMatch Strategy = iota
	ScoreStrategy
)

func (s Strategy) String() string { return [...]string{"FIRST_MATCH", "SCORE"}[s] }

// Decision is the outcome and its evidence: Traces is every rule evaluated, in priority order.
type Decision struct {
	TransactionID   string
	Action          Action
	Strategy        Strategy
	Score           int
	DecidingRule    string // "" under SCORE or when nothing matched
	Traces          []RuleTrace
	DecidedAtMillis int64
}

// Fired is every live rule whose condition held. Shadow rules are excluded: they did not act.
func (d Decision) Fired() []RuleTrace {
	var out []RuleTrace
	for _, t := range d.Traces {
		if t.Fired && !t.Shadow {
			out = append(out, t)
		}
	}
	return out
}

// ShadowFired is every shadow rule whose condition held: what a candidate rule would have done,
// measured without acting.
func (d Decision) ShadowFired() []RuleTrace {
	var out []RuleTrace
	for _, t := range d.Traces {
		if t.Fired && t.Shadow {
			out = append(out, t)
		}
	}
	return out
}

func (d Decision) FiredRuleNames() []string {
	out := []string{}
	for _, t := range d.Fired() {
		out = append(out, t.Rule)
	}
	return out
}

func fmtValue(v any) string {
	if v == nil {
		return "<absent>"
	}
	return fmt.Sprintf("%v", v)
}

func (d Decision) Explain() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "decision for %s: %s [%s, score=%d]", d.TransactionID, d.Action, d.Strategy, d.Score)
	switch {
	case d.Strategy == ScoreStrategy:
		sb.WriteString(" by accumulated score")
	case d.DecidingRule == "":
		sb.WriteString(" default: no rule matched")
	default:
		fmt.Fprintf(&sb, " by %q", d.DecidingRule)
	}
	fmt.Fprintf(&sb, " at t=%d\n", d.DecidedAtMillis)
	for _, t := range d.Traces {
		mark := "pass"
		if t.Fired && t.Shadow {
			mark = "shadow"
		} else if t.Fired {
			mark = "fire"
		}
		fmt.Fprintf(&sb, "  [%s] p%d %s: %s=%s %s %s", mark, t.Priority, t.Rule, t.Field, fmtValue(t.Observed), t.Operator, fmtValue(t.Expected))
		if t.Fired {
			fmt.Fprintf(&sb, " -> %s (+%d): %s", t.Action, t.Score, t.Explanation)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// RulesEngine holds rules sorted by priority and decides with one of two strategies.
type RulesEngine struct {
	Rules     []Rule
	Strategy  Strategy
	ReviewAt  int
	DeclineAt int
}

func NewRulesEngine(rules []Rule, strategy Strategy, reviewAt, declineAt int) (*RulesEngine, error) {
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.Name] {
			return nil, fmt.Errorf("%w: duplicate rule name %s", ErrConfiguration, r.Name)
		}
		seen[r.Name] = true
	}
	if reviewAt <= 0 || declineAt <= reviewAt {
		return nil, fmt.Errorf("%w: need 0 < reviewAt < declineAt", ErrConfiguration)
	}
	sorted := append([]Rule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority }) // ties keep declaration order
	return &RulesEngine{sorted, strategy, reviewAt, declineAt}, nil
}

// Decide evaluates every rule against the facts (the full trace is the audit record), then decides:
// FirstMatch takes the first fired rule's action; ScoreStrategy sums fired scores. An absent fact
// never fires a rule. A type mismatch is an error: a rule that can never evaluate is a configuration bug.
func (e *RulesEngine) Decide(transactionID string, facts *Facts, now int64) (Decision, error) {
	traces := make([]RuleTrace, 0, len(e.Rules))
	total := 0
	var first *Rule
	for i := range e.Rules {
		rule := &e.Rules[i]
		observed, present := facts.Get(rule.Field)
		fired := false
		if present {
			var err error
			fired, err = rule.Operator.Apply(observed, rule.Value)
			if err != nil {
				return Decision{}, fmt.Errorf("rule %s: %w", rule.Name, err)
			}
		}
		traces = append(traces, RuleTrace{rule.Name, rule.Priority, rule.Field, observed, rule.Operator, rule.Value, rule.Action, rule.Score, fired, rule.Shadow, rule.Explanation})
		if fired && !rule.Shadow { // a shadow rule is observed, never obeyed
			total += rule.Score
			if first == nil {
				first = rule
			}
		}
	}
	if e.Strategy == FirstMatch {
		action, name := Allow, ""
		if first != nil {
			action, name = first.Action, first.Name
		}
		return Decision{transactionID, action, e.Strategy, total, name, traces, now}, nil
	}
	action := Allow
	switch {
	case total >= e.DeclineAt:
		action = Decline
	case total >= e.ReviewAt:
		action = Review
	}
	return Decision{transactionID, action, e.Strategy, total, "", traces, now}, nil
}

// Screener is the glue: record velocity, enrich facts, decide. One call per authorization attempt.
type Screener struct {
	Engine   *RulesEngine
	Velocity *VelocityService
	Clock    Clock
}

func (s *Screener) Screen(tx Transaction) (Decision, error) {
	now := s.Clock.NowMillis()
	facts, err := s.Velocity.Enrich(tx, now)
	if err != nil {
		return Decision{}, err
	}
	return s.Engine.Decide(tx.ID, facts, now)
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

const (
	Second int64 = 1_000
	Minute       = 60 * Second
	Hour         = 60 * Minute
	Day          = 24 * Hour
)

func usd(s string) Money { return MustParse(s, USD) }

func tx(id, account, card, bin, country string, cardAge int64, amount string) Transaction {
	return Transaction{id, account, card, bin, country, cardAge, usd(amount)}
}

// CardTestingRules is the rule set the demo and the tests share. Rules are data: this could be a JSON file.
func CardTestingRules() []Rule {
	return []Rule{
		MustRule("bin-blocklist", "bin", IN, SetOf("999999", "888888"), Decline, 5, 100,
			"issuer range is on the internal blocklist"),
		MustRule("card-testing-burst", "attempts_per_card_10m", GT, int64(5), Decline, 10, 60,
			"more than five authorizations on one card in ten minutes is the card-testing signature"),
		MustRule("fresh-card-micro-auth", "card_age_days", LT, int64(1), Review, 20, 25,
			"card was added less than a day ago"),
		MustRule("high-risk-country", "country", IN, SetOf("NG", "RU", "VE"), Review, 30, 30,
			"issuing country is on the elevated-risk list"),
		MustRule("daily-spend", "amount_per_account_1d", GT, usd("2000.00"), Review, 40, 40,
			"account spent more than 2000.00 USD in 24 hours"),
		MustRule("large-ticket", "amount", GTE, usd("5000.00"), Review, 50, 30,
			"single authorization at or above 5000.00 USD"),
	}
}

func mustCounter(window int64, buckets int) *SlidingWindowCounter {
	c, err := NewSlidingWindowCounter(window, buckets, USD)
	if err != nil {
		panic(err)
	}
	return c
}

// Velocity builds the two counters the card-testing rules read.
func Velocity() *VelocityService {
	v := NewVelocityService()
	_ = v.Define("attempts_per_card_10m", DimensionCard, MetricCount, mustCounter(10*Minute, 10))
	_ = v.Define("amount_per_account_1d", DimensionAccount, MetricSum, mustCounter(Day, 24))
	return v
}

func must(d Decision, err error) Decision {
	if err != nil {
		panic(err)
	}
	return d
}

func main() {
	clock := NewManualClock(1_700_000_040_000)
	engine, _ := NewRulesEngine(CardTestingRules(), FirstMatch, 50, 80)
	screener := &Screener{engine, Velocity(), clock}

	fmt.Println("== Rules in priority order ==")
	for _, r := range engine.Rules {
		fmt.Printf("  %s: %s\n", r.Name, r.Describe())
	}

	fmt.Println("\n== A normal purchase ==")
	fmt.Print(must(screener.Screen(tx("tx-001", "acct_alice", "tok_alice", "411111", "US", 400, "42.50"))).Explain())

	fmt.Println("\n== Card testing: six $1 attempts on a fresh card in two minutes ==")
	var last Decision
	for i := 1; i <= 6; i++ {
		clock.Advance(20 * Second)
		last = must(screener.Screen(tx(fmt.Sprintf("tx-10%d", i), "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")))
		fmt.Printf("  attempt %d -> %s fired=%v\n", i, last.Action, last.FiredRuleNames())
	}
	fmt.Print(last.Explain())

	fmt.Println("\n== Ten minutes later the window has slid past the burst ==")
	clock.Advance(10 * Minute)
	fmt.Print(must(screener.Screen(tx("tx-107", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))).Explain())

	fmt.Println("\n== Priority: a blocked BIN decides before anything else, but every rule is still traced ==")
	fmt.Print(must(screener.Screen(tx("tx-200", "acct_bob", "tok_bob", "999999", "NG", 0, "900.00"))).Explain())

	fmt.Println("\n== Velocity on money, not count: three $700 purchases trip the daily sum ==")
	for i := 1; i <= 3; i++ {
		clock.Advance(5 * Minute)
		last = must(screener.Screen(tx(fmt.Sprintf("tx-30%d", i), "acct_dave", "tok_dave", "411111", "US", 90, "700.00")))
	}
	fmt.Print(last.Explain())

	fmt.Println("\n== Same rules, SCORE strategy: four REVIEW signals add up to a DECLINE ==")
	scored, _ := NewRulesEngine(CardTestingRules(), ScoreStrategy, 50, 80)
	fmt.Print(must((&Screener{scored, Velocity(), clock}).Screen(tx("tx-400", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00"))).Explain())
	fm := must((&Screener{engine, Velocity(), clock}).Screen(tx("tx-401", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00")))
	fmt.Printf("FIRST_MATCH would have said: %s\n", fm.Action)

	fmt.Println("\n== Shadow mode: a candidate rule is traced but does not act ==")
	withCandidate := append(CardTestingRules(), MustRule("mid-ticket-candidate", "amount", GTE, usd("500.00"), Review, 45, 20,
		"candidate: single authorization at or above 500.00 USD").AsShadow())
	shadowEngine, _ := NewRulesEngine(withCandidate, FirstMatch, 50, 80)
	sd := must((&Screener{shadowEngine, Velocity(), clock}).Screen(tx("tx-500", "acct_frank", "tok_frank", "411111", "US", 200, "750.00")))
	fmt.Print(sd.Explain())
	wouldHave := []string{}
	for _, t := range sd.ShadowFired() {
		wouldHave = append(wouldHave, t.Rule)
	}
	fmt.Printf("acted on: %v  would have fired: %v\n", sd.FiredRuleNames(), wouldHave)
}
