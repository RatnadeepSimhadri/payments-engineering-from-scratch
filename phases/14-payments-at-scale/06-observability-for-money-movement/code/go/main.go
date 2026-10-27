// Lesson: phases/14-payments-at-scale/06-observability-for-money-movement/docs/en.md
// Observability for Money Movement
// Run:  go run .
// Test: go test ./...
// Sources: Google SRE Workbook ch. 5 "Alerting on SLOs" (multiwindow, multi-burn-rate alerts);
// Phase 9 Lessons 01 and 06 (SLOs, metrics). Go 1.22+, standard library only. Money is integer minor units.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Currency is an ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

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
	product := a * b
	if product/b != a {
		return 0, ErrOverflow
	}
	return product, nil
}

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

// Compare panics on a currency mismatch, as the Java compareTo throws: it is a programming error.
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
	text := strconv.FormatInt(magnitude/scale, 10)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// need panics on an error where the Java threw an unchecked exception from arithmetic that cannot fail on
// the lesson's inputs (int64 overflow of a sum of minor units).
func need[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Nothing in this file reads the wall clock.
// ---------------------------------------------------------------------------------------------

type Clock interface{ Now() time.Time }

type ManualClock struct{ now time.Time }

func NewManualClock(start time.Time) *ManualClock { return &ManualClock{start} }

func (c *ManualClock) Now() time.Time { return c.now }

// ErrTimeBackwards is returned when a test tries to advance the clock by a negative duration.
var ErrTimeBackwards = errors.New("time does not go backwards")

func (c *ManualClock) Advance(d time.Duration) error {
	if d < 0 {
		return ErrTimeBackwards
	}
	c.now = c.now.Add(d)
	return nil
}

type PaymentState int

const (
	Created PaymentState = iota
	Authorized
	CapturePending
	Captured
	Settled
	Failed
)

var paymentStateNames = [...]string{"CREATED", "AUTHORIZED", "CAPTURE_PENDING", "CAPTURED", "SETTLED", "FAILED"}

func (s PaymentState) String() string { return paymentStateNames[s] }
func (s PaymentState) Terminal() bool { return s == Settled || s == Failed }

// AuthOutcome: the three outcomes an availability SLI must tell apart: only ProcessorError is our failure.
type AuthOutcome int

const (
	Approved AuthOutcome = iota
	IssuerDecline
	ProcessorError
)

const authOutcomes = 3

type Severity int

const (
	Page Severity = iota
	Ticket
)

func (s Severity) String() string { return [...]string{"PAGE", "TICKET"}[s] }

type Alert struct {
	Severity Severity
	Name     string
	Detail   string
}

type StuckPayment struct {
	PaymentId string
	Processor string
	State     PaymentState
	Age       time.Duration
	Amount    Money
}

func minutesOf(d time.Duration) int64 { return int64(d / time.Minute) }

func epochMinute(at time.Time) int64 {
	s := at.Unix()
	return (s - ((s%60)+60)%60) / 60 // Math.floorDiv(epochSecond, 60)
}

// MinuteCounter counts per minute bucket, keyed by epoch minute. A window query sums the buckets whose minute is
// in (now - window, now]. Old buckets are dropped once they fall outside the longest window we query.
type MinuteCounter struct {
	buckets map[int64][]int64
	width   int
}

func NewMinuteCounter(width int) *MinuteCounter { return &MinuteCounter{map[int64][]int64{}, width} }

func (c *MinuteCounter) Add(at time.Time, slot int, n int64) {
	minute := epochMinute(at)
	counts, ok := c.buckets[minute]
	if !ok {
		counts = make([]int64, c.width)
		c.buckets[minute] = counts
	}
	counts[slot] += n
}

func (c *MinuteCounter) Sum(now time.Time, window time.Duration) []int64 {
	nowMinute := epochMinute(now)
	from := nowMinute - minutesOf(window) + 1
	total := make([]int64, c.width)
	for minute, counts := range c.buckets {
		if minute >= from && minute <= nowMinute {
			for i := 0; i < c.width; i++ {
				total[i] += counts[i]
			}
		}
	}
	return total
}

// Covers is true when the counter has data reaching back at least the whole window.
func (c *MinuteCounter) Covers(now time.Time, window time.Duration) bool {
	if len(c.buckets) == 0 {
		return false
	}
	first := int64(0)
	started := false
	for minute := range c.buckets {
		if !started || minute < first {
			first, started = minute, true
		}
	}
	return first <= epochMinute(now)-minutesOf(window)+1
}

func (c *MinuteCounter) Prune(now time.Time, keep time.Duration) {
	cutoff := epochMinute(now) - minutesOf(keep)
	for minute := range c.buckets {
		if minute < cutoff {
			delete(c.buckets, minute)
		}
	}
}

// Ratio holds integer basis-point helpers: 10,000 * num / den, rounded down. -1 when there is no data.
var Ratio ratio

type ratio struct{}

func (ratio) Bps(numerator, denominator int64) int64 {
	if denominator == 0 {
		return -1
	}
	return need(mulChecked(numerator, 10_000)) / denominator
}

func (ratio) Pct(bps int64) string {
	if bps < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d.%02d%%", bps/100, bps%100)
}

// Slo is an SLO with its error budget in parts per million (99.9% SLO = 1,000 ppm budget). The burn rate is
// returned x100 so that 14.4 is 1440 and every comparison is an exact integer comparison.
type Slo struct {
	Name      string
	BudgetPpm int64
}

func (s Slo) BurnRateX100(bad, total int64) int64 {
	if total == 0 {
		return 0
	}
	// (bad / total) / (budgetPpm / 1e6) * 100 = bad * 1e8 / (total * budgetPpm)
	return need(mulChecked(bad, 100_000_000)) / need(mulChecked(total, s.BudgetPpm))
}

// BurnRule is one burn-rate rule from the SRE Workbook table: both windows must exceed the threshold.
type BurnRule struct {
	Severity      Severity
	LongWindow    time.Duration
	ShortWindow   time.Duration
	ThresholdX100 int64
}

type tracked struct {
	processor string
	amount    Money
	state     PaymentState
	enteredAt time.Time
}

var DefaultBurnRules = []BurnRule{
	{Page, time.Hour, 5 * time.Minute, 1440},     // 2% of 30-day budget in 1h
	{Page, 6 * time.Hour, 30 * time.Minute, 600}, // 5% in 6h
	{Ticket, 24 * time.Hour, 2 * time.Hour, 300}, // 10% in 1d
	{Ticket, 72 * time.Hour, 6 * time.Hour, 100}, // 10% in 3d
}

func DefaultStuckAfter() map[PaymentState]time.Duration {
	return map[PaymentState]time.Duration{
		Created:        30 * time.Minute,   // abandoned checkout or lost auth response
		Authorized:     6 * 24 * time.Hour, // capture before the auth expires (~7 days)
		CapturePending: 15 * time.Minute,   // capture sent, no answer
		Captured:       3 * 24 * time.Hour, // should be in a settlement file by now
	}
}

type MoneyMovementMonitor struct {
	clock               Clock
	currency            Currency
	stuckAfter          map[PaymentState]time.Duration
	slo                 Slo
	burnRules           []BurnRule
	stuckPageThreshold  Money
	authDropBps         int64
	minAttemptsForRate  int64
	reconBreakTicketBps int64

	payments        map[string]tracked
	authByProcessor map[string]*MinuteCounter
	authAll         *MinuteCounter
	reconRuns       [][2]int64 // {matched, breaks}
}

func NewMoneyMovementMonitor(clock Clock, currency Currency) *MoneyMovementMonitor {
	return NewMoneyMovementMonitorWith(clock, currency, DefaultStuckAfter(), Slo{"auth availability 99.9%", 1_000}, DefaultBurnRules,
		OfMinor(100_000, currency), 500, 100, 50)
}

func NewMoneyMovementMonitorWith(clock Clock, currency Currency, stuckAfter map[PaymentState]time.Duration, slo Slo, burnRules []BurnRule,
	stuckPageThreshold Money, authDropBps, minAttemptsForRate, reconBreakTicketBps int64) *MoneyMovementMonitor {
	return &MoneyMovementMonitor{clock: clock, currency: currency, stuckAfter: stuckAfter, slo: slo, burnRules: burnRules,
		stuckPageThreshold: stuckPageThreshold, authDropBps: authDropBps, minAttemptsForRate: minAttemptsForRate,
		reconBreakTicketBps: reconBreakTicketBps, payments: map[string]tracked{}, authByProcessor: map[string]*MinuteCounter{},
		authAll: NewMinuteCounter(authOutcomes)}
}

// ---- inputs -------------------------------------------------------------------------------

// Transition records that a payment entered a state now. Terminal states stop tracking the payment.
func (m *MoneyMovementMonitor) Transition(paymentId, processor string, amount Money, to PaymentState) error {
	if amount.Currency != m.currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.currency.Code, amount.Currency.Code)
	}
	if to.Terminal() {
		delete(m.payments, paymentId)
	} else {
		m.payments[paymentId] = tracked{processor, amount, to, m.clock.Now()}
	}
	return nil
}

func (m *MoneyMovementMonitor) AuthResult(processor string, outcome AuthOutcome) {
	mc, ok := m.authByProcessor[processor]
	if !ok {
		mc = NewMinuteCounter(authOutcomes)
		m.authByProcessor[processor] = mc
	}
	mc.Add(m.clock.Now(), int(outcome), 1)
	m.authAll.Add(m.clock.Now(), int(outcome), 1)
}

func (m *MoneyMovementMonitor) ReconRun(matched, breaks int64) {
	m.reconRuns = append(m.reconRuns, [2]int64{matched, breaks})
}

// ---- metrics ------------------------------------------------------------------------------

// AgeOf is how long the payment has been in its current state; false when it is not tracked.
func (m *MoneyMovementMonitor) AgeOf(paymentId string) (time.Duration, bool) {
	t, ok := m.payments[paymentId]
	if !ok {
		return 0, false
	}
	return m.clock.Now().Sub(t.enteredAt), true
}

// Stuck lists payments whose time in their current state exceeds that state's limit, oldest first.
func (m *MoneyMovementMonitor) Stuck() []StuckPayment {
	out := []StuckPayment{}
	for id, t := range m.payments {
		age := m.clock.Now().Sub(t.enteredAt)
		if limit, ok := m.stuckAfter[t.state]; ok && age > limit {
			out = append(out, StuckPayment{id, t.processor, t.state, age, t.amount})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Age != out[j].Age {
			return out[i].Age > out[j].Age
		}
		return out[i].PaymentId < out[j].PaymentId
	})
	return out
}

func (m *MoneyMovementMonitor) StuckExposure() Money {
	total := Zero(m.currency)
	for _, s := range m.Stuck() {
		total = need(total.Plus(s.Amount))
	}
	return total
}

// nonTerminalStates is the EnumMap key order the Java iterates.
var nonTerminalStates = []PaymentState{Created, Authorized, CapturePending, Captured}

// InFlightExposure is the money in every non-terminal state: what is committed but not yet final.
func (m *MoneyMovementMonitor) InFlightExposure() map[PaymentState]Money {
	out := map[PaymentState]Money{}
	for _, s := range nonTerminalStates {
		out[s] = Zero(m.currency)
	}
	for _, t := range m.payments {
		out[t.state] = need(out[t.state].Plus(t.amount))
	}
	return out
}

func (m *MoneyMovementMonitor) TotalInFlight() Money {
	total := Zero(m.currency)
	for _, v := range m.InFlightExposure() {
		total = need(total.Plus(v))
	}
	return total
}

// AgeBandsMinutes are the histogram edges: the shape of the queue, not just its length.
var AgeBandsMinutes = []int64{1, 5, 15, 60, 24 * 60}

func (m *MoneyMovementMonitor) StateAgeHistogram() map[PaymentState][]int64 {
	out := map[PaymentState][]int64{}
	for _, s := range nonTerminalStates {
		out[s] = make([]int64, len(AgeBandsMinutes)+1)
	}
	for _, t := range m.payments {
		minutes := minutesOf(m.clock.Now().Sub(t.enteredAt))
		band := 0
		for band < len(AgeBandsMinutes) && minutes >= AgeBandsMinutes[band] {
			band++
		}
		out[t.state][band]++
	}
	return out
}

// AuthRateBps is approvals over all attempts, in basis points; -1 if fewer than minAttemptsForRate attempts.
// processor "" means the whole fleet.
func (m *MoneyMovementMonitor) AuthRateBps(processor string, window time.Duration) int64 {
	c := m.counts(processor, window)
	total := c[0] + c[1] + c[2]
	if total < m.minAttemptsForRate {
		return -1
	}
	return Ratio.Bps(c[Approved], total)
}

func (m *MoneyMovementMonitor) Attempts(processor string, window time.Duration) int64 {
	c := m.counts(processor, window)
	return c[0] + c[1] + c[2]
}

func (m *MoneyMovementMonitor) counts(processor string, window time.Duration) []int64 {
	if processor == "" {
		return m.authAll.Sum(m.clock.Now(), window)
	}
	mc, ok := m.authByProcessor[processor]
	if !ok {
		return make([]int64, authOutcomes)
	}
	return mc.Sum(m.clock.Now(), window)
}

// BurnRateX100 is the availability SLI burn rate: processor errors are bad, issuer declines are good events.
func (m *MoneyMovementMonitor) BurnRateX100(window time.Duration) int64 {
	c := m.authAll.Sum(m.clock.Now(), window)
	return m.slo.BurnRateX100(c[ProcessorError], c[0]+c[1]+c[2])
}

// ReconBreakRateBps is breaks over records across the last n reconciliation runs, in basis points.
func (m *MoneyMovementMonitor) ReconBreakRateBps(lastRuns int) int64 {
	var matched, breaks int64
	for i := max(0, len(m.reconRuns)-lastRuns); i < len(m.reconRuns); i++ {
		matched += m.reconRuns[i][0]
		breaks += m.reconRuns[i][1]
	}
	return Ratio.Bps(breaks, matched+breaks)
}

func (m *MoneyMovementMonitor) BurnRuleArmed(r BurnRule) bool {
	return m.authAll.Covers(m.clock.Now(), r.LongWindow)
}

// Processors lists the processors seen so far in sorted order (the Java TreeMap key order).
func (m *MoneyMovementMonitor) Processors() []string {
	out := make([]string, 0, len(m.authByProcessor))
	for p := range m.authByProcessor {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ---- alerts -------------------------------------------------------------------------------

func (m *MoneyMovementMonitor) EvaluateAlerts() []Alert {
	out := []Alert{}
	for _, r := range m.burnRules {
		if !m.authAll.Covers(m.clock.Now(), r.LongWindow) {
			continue // a half-empty long window is a short window
		}
		lng, shrt := m.BurnRateX100(r.LongWindow), m.BurnRateX100(r.ShortWindow)
		if lng >= r.ThresholdX100 && shrt >= r.ThresholdX100 {
			out = append(out, Alert{r.Severity, fmt.Sprintf("slo-burn-%dm", minutesOf(r.LongWindow)),
				fmt.Sprintf("%s burning %s over %dm and %s over %dm (threshold %s)", m.slo.Name, X100(lng),
					minutesOf(r.LongWindow), X100(shrt), minutesOf(r.ShortWindow), X100(r.ThresholdX100))})
		}
	}
	if stuck := m.Stuck(); len(stuck) > 0 {
		exposure := m.StuckExposure()
		sev := Ticket
		if exposure.Compare(m.stuckPageThreshold) >= 0 {
			sev = Page
		}
		oldest := stuck[0]
		out = append(out, Alert{sev, "stuck-payments", fmt.Sprintf("%d stuck, %v; oldest %s in %v for %dm",
			len(stuck), exposure, oldest.PaymentId, oldest.State, minutesOf(oldest.Age))})
	}
	fleet := m.AuthRateBps("", 15*time.Minute)
	for _, p := range m.Processors() {
		rate := m.AuthRateBps(p, 15*time.Minute)
		if rate >= 0 && fleet >= 0 && fleet-rate >= m.authDropBps {
			out = append(out, Alert{Page, "auth-rate-drop:" + p,
				fmt.Sprintf("%s approves %s vs fleet %s over 15m", p, Ratio.Pct(rate), Ratio.Pct(fleet))})
		}
	}
	if recon := m.ReconBreakRateBps(4); recon >= m.reconBreakTicketBps {
		out = append(out, Alert{Ticket, "recon-break-rate", fmt.Sprintf("break rate %s over last 4 runs", Ratio.Pct(recon))})
	}
	return out
}

func X100(v int64) string { return fmt.Sprintf("%d.%02d", v/100, v%100) }

// ---- dashboard ----------------------------------------------------------------------------

func (m *MoneyMovementMonitor) Dashboard() string {
	var sb strings.Builder
	sb.WriteString("MONEY MOVEMENT  " + m.clock.Now().UTC().Format(time.RFC3339) + "\n")
	fmt.Fprintf(&sb, "in flight %v\n", m.TotalInFlight())
	hist := m.StateAgeHistogram()
	exposure := m.InFlightExposure()
	fmt.Fprintf(&sb, "  %-16s %-14s %6s %6s %6s %6s %6s %6s\n", "state", "amount", "<1m", "1-5m", "5-15m", "15-60m", "1-24h", ">24h")
	for _, s := range nonTerminalStates {
		h := hist[s]
		fmt.Fprintf(&sb, "  %-16s %-14s %6d %6d %6d %6d %6d %6d\n", s, exposure[s], h[0], h[1], h[2], h[3], h[4], h[5])
	}
	fmt.Fprintf(&sb, "auth rate        %-10s %-10s %-10s\n", "5m", "1h", "attempts 1h")
	for _, p := range m.Processors() {
		fmt.Fprintf(&sb, "  %-14s %-10s %-10s %d\n", p, Ratio.Pct(m.AuthRateBps(p, 5*time.Minute)),
			Ratio.Pct(m.AuthRateBps(p, time.Hour)), m.Attempts(p, time.Hour))
	}
	fmt.Fprintf(&sb, "slo burn         5m %s  1h %s  6h %s\n", X100(m.BurnRateX100(5*time.Minute)),
		X100(m.BurnRateX100(time.Hour)), X100(m.BurnRateX100(6*time.Hour)))
	sb.WriteString("recon breaks     " + Ratio.Pct(m.ReconBreakRateBps(4)) + " (last 4 runs)\n")
	alerts := m.EvaluateAlerts()
	if len(alerts) == 0 {
		sb.WriteString("alerts           none\n")
	}
	for _, a := range alerts {
		fmt.Fprintf(&sb, "ALERT %v %s: %s\n", a.Severity, a.Name, a.Detail)
	}
	return sb.String()
}

// Simulation is a seeded eight-hour day from 06:00: two processors, 40 attempts a minute, an hourly settlement
// run, three lost capture webhooks at 12:00, and a partial outage on proc_b from 13:00. Records the first minute
// each alert fires. Six quiet hours come first so the 6h burn window has full coverage.
type Simulation struct {
	Clock        *ManualClock
	Monitor      *MoneyMovementMonitor
	FirstFired   map[string]int
	FiredOrder   []string // insertion order of FirstFired (the Java LinkedHashMap)
	Snapshots    map[int]string
	rnd          *JavaRandom
	scheduled    map[int][]func()
	reconMatched int64
	reconBreaks  int64
	seq          int
	captured     map[string]Money
	capturedIds  []string
}

var SimulationStart = time.Date(2026, 3, 31, 6, 0, 0, 0, time.UTC)

const (
	SimulationMinutes = 480
	LostWebhookMinute = 360
	OutageMinute      = 420
)

func NewSimulation(seed int64) *Simulation {
	clock := NewManualClock(SimulationStart)
	return &Simulation{Clock: clock, Monitor: NewMoneyMovementMonitor(clock, USD), FirstFired: map[string]int{},
		Snapshots: map[int]string{}, rnd: NewJavaRandom(seed), scheduled: map[int][]func(){}, captured: map[string]Money{}}
}

func (s *Simulation) at(minute int, r func()) { s.scheduled[minute] = append(s.scheduled[minute], r) }

func (s *Simulation) Run(minutes int, snapshotMinutes map[int]bool) {
	for minute := 0; minute < minutes; minute++ {
		for _, processor := range []string{"proc_a", "proc_b"} {
			for i := 0; i < 20; i++ {
				s.attempt(minute, processor)
			}
		}
		if minute == LostWebhookMinute {
			for _, amount := range []int64{40_000, 35_000, 30_000} {
				id := fmt.Sprintf("pay_lost_%d", amount)
				need(0, s.Monitor.Transition(id, "proc_a", OfMinor(amount, USD), CapturePending))
			}
		}
		if due, ok := s.scheduled[minute]; ok {
			delete(s.scheduled, minute)
			for _, r := range due {
				r()
			}
		}
		if minute%60 == 59 {
			// hourly settlement file in this simulation: everything captured so far settles
			for _, id := range s.capturedIds {
				need(0, s.Monitor.Transition(id, "settle", s.captured[id], Settled))
			}
			s.captured, s.capturedIds = map[string]Money{}, nil
		}
		if minute%30 == 29 {
			s.Monitor.ReconRun(s.reconMatched, s.reconBreaks)
			s.reconMatched, s.reconBreaks = 0, 0
		}
		for _, a := range s.Monitor.EvaluateAlerts() {
			if _, seen := s.FirstFired[a.Name]; !seen {
				s.FirstFired[a.Name] = minute
				s.FiredOrder = append(s.FiredOrder, a.Name)
			}
		}
		if snapshotMinutes[minute] {
			s.Snapshots[minute] = s.Monitor.Dashboard()
		}
		need(0, s.Clock.Advance(time.Minute))
	}
}

func (s *Simulation) attempt(minute int, processor string) {
	id := fmt.Sprintf("pay_%06d", s.seq)
	s.seq++
	amount := OfMinor(500+int64(s.rnd.NextIntBound(49_500)), USD)
	u := s.rnd.NextDouble()
	degraded := processor == "proc_b" && minute >= OutageMinute
	errorP := 0.0005
	if degraded {
		errorP = 0.25
	}
	outcome := Approved
	switch {
	case u < errorP:
		outcome = ProcessorError
	case u < errorP+0.08:
		outcome = IssuerDecline
	}
	need(0, s.Monitor.Transition(id, processor, amount, Created))
	s.Monitor.AuthResult(processor, outcome)
	switch outcome {
	case Approved:
		need(0, s.Monitor.Transition(id, processor, amount, Authorized))
		s.at(minute+1, func() { need(0, s.Monitor.Transition(id, processor, amount, CapturePending)) })
		s.at(minute+2, func() {
			need(0, s.Monitor.Transition(id, processor, amount, Captured))
			if _, dup := s.captured[id]; !dup {
				s.capturedIds = append(s.capturedIds, id)
			}
			s.captured[id] = amount
			s.reconMatched++
		})
	case IssuerDecline:
		need(0, s.Monitor.Transition(id, processor, amount, Failed))
	case ProcessorError:
		need(0, s.Monitor.Transition(id, processor, amount, Failed))
		// A timeout is not a decline: one in five was actually approved at the processor,
		// which reconciliation later reports as a capture we do not have.
		if s.rnd.NextIntBound(5) == 0 {
			s.reconBreaks++
		}
	}
}

func main() {
	fmt.Println("== Burn rate by hand: 99.9% SLO, budget 1,000 ppm ==")
	slo := Slo{"auth availability 99.9%", 1_000}
	fmt.Println("5m: 6 errors / 200 attempts   -> burn " + X100(slo.BurnRateX100(6, 200)))
	fmt.Println("1h: 40 errors / 2,400 attempts -> burn " + X100(slo.BurnRateX100(40, 2_400)))
	fmt.Println("page rule needs both >= 14.40: fires")

	fmt.Println("\n== Eight hours of traffic, seed 42 ==")
	sim := NewSimulation(42)
	sim.Run(SimulationMinutes, map[int]bool{359: true, 380: true, 430: true})
	fmt.Printf("lost capture webhooks injected at minute %d; proc_b outage starts at minute %d\n", LostWebhookMinute, OutageMinute)
	for _, name := range sim.FiredOrder {
		minute := sim.FirstFired[name]
		fmt.Printf("  first fired %-22s minute %3d (%s)\n", name, minute, SimulationStart.Add(time.Duration(minute)*time.Minute).Format("15:04"))
	}
	minutes := make([]int, 0, len(sim.Snapshots))
	for m := range sim.Snapshots {
		minutes = append(minutes, m)
	}
	sort.Ints(minutes)
	for _, m := range minutes {
		fmt.Printf("\n-- dashboard at minute %d --\n", m)
		fmt.Print(sim.Snapshots[m])
	}
}
