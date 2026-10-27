// Lesson: phases/12-payment-systems-design/09-limits-and-velocity-controls/docs/en.md
// Limits, Velocity Controls, and Exposure
// Run:  go run .
// Test: go test ./...
// Sources: PCI DSS v4.0 (card testing guidance); Visa Core Rules (authorization velocity and
// excessive-decline programs); RFC 9110 (429 and Retry-After). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrInvalidArgument is the Go form of Java's IllegalArgumentException: bad limits, non-positive
// amounts, a clock that goes backwards, duplicate or unknown limit ids.
var ErrInvalidArgument = errors.New("invalid argument")

// ---------------------------------------------------------------------------------------------
// Time is injected. Every window boundary in this lesson is computed from clock.NowMillis().
// ---------------------------------------------------------------------------------------------

type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now atomic.Int64 }

func NewFakeClock(startMillis int64) *FakeClock {
	c := &FakeClock{}
	c.now.Store(startMillis)
	return c
}
func (c *FakeClock) NowMillis() int64     { return c.now.Load() }
func (c *FakeClock) Advance(millis int64) { c.now.Add(millis) }

// ---------------------------------------------------------------------------------------------
// The attempt being judged, and the dimensions a limit can key on.
// ---------------------------------------------------------------------------------------------

// Attempt is one authorization attempt as the limit engine sees it. The card is identified by a fingerprint, never a PAN.
type Attempt struct {
	AttemptID       string
	MerchantID      string
	CardFingerprint string
	Amount          Money
}

func NewAttempt(attemptID, merchantID, cardFingerprint string, amount Money) (Attempt, error) {
	if !amount.IsPositive() {
		return Attempt{}, fmt.Errorf("%w: amount must be positive: %v", ErrInvalidArgument, amount)
	}
	return Attempt{attemptID, merchantID, cardFingerprint, amount}, nil
}

// LimitScope is what a limit counts against. The scope key is the string the window is bucketed by.
type LimitScope int

const (
	CARD LimitScope = iota
	MERCHANT
	CARD_AT_MERCHANT
	GLOBAL
)

func (s LimitScope) String() string {
	return [...]string{"CARD", "MERCHANT", "CARD_AT_MERCHANT", "GLOBAL"}[s]
}

func (s LimitScope) KeyFor(a Attempt) string {
	switch s {
	case CARD:
		return "card:" + a.CardFingerprint
	case MERCHANT:
		return "merchant:" + a.MerchantID
	case CARD_AT_MERCHANT:
		return "merchant:" + a.MerchantID + "|card:" + a.CardFingerprint
	default:
		return "global"
	}
}

// LimitKind is what a limit measures over its window. EXPOSURE has no window: it is the sum of currently open authorizations.
type LimitKind int

const (
	COUNT LimitKind = iota
	AMOUNT
	EXPOSURE
)

func (k LimitKind) String() string { return [...]string{"COUNT", "AMOUNT", "EXPOSURE"}[k] }

// Severity: HARD breaches deny. SOFT breaches allow but flag for review (step-up, manual review, delayed capture).
type Severity int

const (
	HARD Severity = iota
	SOFT
)

func (s Severity) String() string { return [...]string{"HARD", "SOFT"}[s] }

// Limit: at most threshold units (attempts, or minor units of money) per scope key within WindowMillis.
// MaxAmount is meaningful for AMOUNT and EXPOSURE limits, and the currency it carries is the only
// currency the limit can judge: a limit in USD does not see EUR attempts.
type Limit struct {
	ID           string
	Scope        LimitScope
	Kind         LimitKind
	WindowMillis int64
	MaxCount     int64
	MaxAmount    Money
	Severity     Severity
}

func NewLimit(id string, scope LimitScope, kind LimitKind, windowMillis, maxCount int64, maxAmount Money, severity Severity) (Limit, error) {
	if kind == COUNT && maxCount <= 0 {
		return Limit{}, fmt.Errorf("%w: COUNT limit needs maxCount > 0", ErrInvalidArgument)
	}
	if kind != COUNT && !maxAmount.IsPositive() {
		return Limit{}, fmt.Errorf("%w: %v limit needs a positive maxAmount", ErrInvalidArgument, kind)
	}
	if kind != EXPOSURE && windowMillis <= 0 {
		return Limit{}, fmt.Errorf("%w: window must be positive for %v", ErrInvalidArgument, kind)
	}
	return Limit{id, scope, kind, windowMillis, maxCount, maxAmount, severity}, nil
}

func CountLimit(id string, scope LimitScope, windowMillis, maxCount int64, severity Severity) (Limit, error) {
	return NewLimit(id, scope, COUNT, windowMillis, maxCount, Money{}, severity)
}
func AmountLimit(id string, scope LimitScope, windowMillis int64, maxAmount Money, severity Severity) (Limit, error) {
	return NewLimit(id, scope, AMOUNT, windowMillis, 0, maxAmount, severity)
}
func ExposureLimit(id string, scope LimitScope, maxAmount Money, severity Severity) (Limit, error) {
	return NewLimit(id, scope, EXPOSURE, 0, 0, maxAmount, severity)
}

// Applies: does this limit apply to the attempt at all? Money limits only judge their own currency.
func (l Limit) Applies(a Attempt) bool {
	return l.Kind == COUNT || l.MaxAmount.Currency == a.Amount.Currency
}

// ---------------------------------------------------------------------------------------------
// The sliding window: a queue of (timestamp, amount) entries, evicted lazily. The window is the
// half-open interval (now - W, now]: an entry exactly W old has left the window.
// ---------------------------------------------------------------------------------------------

type Entry struct {
	AtMillis    int64
	AmountMinor int64
}

type SlidingWindow struct {
	windowMillis int64
	entries      []Entry // head at entries[0]; evicted entries are sliced off the front
	sumMinor     int64   // maintained incrementally so Sum is O(evicted), not O(n)
}

func NewSlidingWindow(windowMillis int64) (*SlidingWindow, error) {
	if windowMillis <= 0 {
		return nil, fmt.Errorf("%w: window must be positive", ErrInvalidArgument)
	}
	return &SlidingWindow{windowMillis: windowMillis}, nil
}

// Evict drops every entry that has fallen out of (now - W, now]. Amortized O(1) per entry ever added.
func (w *SlidingWindow) Evict(now int64) {
	cutoff := now - w.windowMillis
	for len(w.entries) > 0 && w.entries[0].AtMillis <= cutoff {
		w.sumMinor -= w.entries[0].AmountMinor
		w.entries = w.entries[1:]
	}
}

func (w *SlidingWindow) Add(now, amountMinor int64) error {
	if len(w.entries) > 0 && now < w.entries[len(w.entries)-1].AtMillis {
		return fmt.Errorf("%w: entries must be appended in time order; clock went backwards", ErrInvalidArgument)
	}
	w.Evict(now)
	w.entries = append(w.entries, Entry{now, amountMinor})
	sum, err := addChecked(w.sumMinor, amountMinor)
	if err != nil {
		return err
	}
	w.sumMinor = sum
	return nil
}

func (w *SlidingWindow) Count(now int64) int      { w.Evict(now); return len(w.entries) }
func (w *SlidingWindow) SumMinor(now int64) int64 { w.Evict(now); return w.sumMinor }

// NextExpiryMillis is when the oldest entry leaves the window, i.e. the earliest instant a COUNT
// breach can clear. The bool is false if the window is empty (Java returns null).
func (w *SlidingWindow) NextExpiryMillis(now int64) (int64, bool) {
	w.Evict(now)
	if len(w.entries) == 0 {
		return 0, false
	}
	return w.entries[0].AtMillis + w.windowMillis, true
}

// ---------------------------------------------------------------------------------------------
// Exposure: money authorized and not yet captured, voided, or expired. No window: it is a balance.
// ---------------------------------------------------------------------------------------------

type openSet struct {
	order   []string // insertion order, like Java's LinkedHashMap
	amounts map[string]Money
}

type ExposureTracker struct {
	openByScopeKey map[string]*openSet // scopeKey -> attemptId -> amount
}

func NewExposureTracker() *ExposureTracker {
	return &ExposureTracker{openByScopeKey: map[string]*openSet{}}
}

func (e *ExposureTracker) Open(scopeKey string, currency Currency) Money {
	total := Zero(currency)
	if set, ok := e.openByScopeKey[scopeKey]; ok {
		for _, id := range set.order {
			if m := set.amounts[id]; m.Currency == currency {
				total = mustPlus(total, m)
			}
		}
	}
	return total
}

func (e *ExposureTracker) Add(scopeKey, attemptID string, amount Money) {
	set, ok := e.openByScopeKey[scopeKey]
	if !ok {
		set = &openSet{amounts: map[string]Money{}}
		e.openByScopeKey[scopeKey] = set
	}
	if _, present := set.amounts[attemptID]; !present {
		set.order = append(set.order, attemptID)
	}
	set.amounts[attemptID] = amount
}

// Release: capture, void, or expiry releases the exposure. Unknown ids are ignored: release is idempotent.
func (e *ExposureTracker) Release(scopeKey, attemptID string) {
	set, ok := e.openByScopeKey[scopeKey]
	if !ok {
		return
	}
	if _, present := set.amounts[attemptID]; !present {
		return
	}
	delete(set.amounts, attemptID)
	for i, id := range set.order {
		if id == attemptID {
			set.order = append(set.order[:i], set.order[i+1:]...)
			break
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Decisions carry their reasons. A denial you cannot explain is a support ticket you cannot close.
// ---------------------------------------------------------------------------------------------

type Outcome int

const (
	ALLOW Outcome = iota
	REVIEW
	DENY
)

func (o Outcome) String() string { return [...]string{"ALLOW", "REVIEW", "DENY"}[o] }

// Reason is one limit's verdict on one attempt: what was observed, what the attempt would make it, and the threshold.
// RetryAfterMillis is nil when the breach is not time-bound (EXPOSURE, or an empty window).
type Reason struct {
	LimitID          string
	Scope            LimitScope
	ScopeKey         string
	Kind             LimitKind
	Severity         Severity
	Observed         string
	WouldBe          string
	Threshold        string
	WindowMillis     int64
	RetryAfterMillis *int64
}

func (r Reason) Explain() string {
	window := "open"
	if r.Kind != EXPOSURE {
		window = fmt.Sprintf("in %ds", r.WindowMillis/1000)
	}
	retry := ""
	if r.RetryAfterMillis != nil {
		retry = fmt.Sprintf(", clears in %ds", *r.RetryAfterMillis/1000)
	}
	return fmt.Sprintf("%s [%v] %s: %s -> %s exceeds %s %s%s", r.LimitID, r.Severity, r.ScopeKey, r.Observed, r.WouldBe, r.Threshold, window, retry)
}

type Decision struct {
	Outcome Outcome
	Reasons []Reason
}

func (d Decision) Allowed() bool { return d.Outcome != DENY }

// Explanations: what a support engineer, a merchant dashboard, or an audit log needs. One line per breached limit.
func (d Decision) Explanations() []string {
	out := make([]string, 0, len(d.Reasons))
	for _, r := range d.Reasons {
		out = append(out, r.Explain())
	}
	return out
}

// RetryAfterSeconds: seconds until the earliest HARD breach clears, for a Retry-After header. 0 if nothing is time-bound.
func (d Decision) RetryAfterSeconds() int64 {
	best, found := int64(0), false
	for _, r := range d.Reasons {
		if r.Severity == HARD && r.RetryAfterMillis != nil && (!found || *r.RetryAfterMillis < best) {
			best, found = *r.RetryAfterMillis, true
		}
	}
	if !found {
		return 0
	}
	return (best + 999) / 1000
}

// ---------------------------------------------------------------------------------------------
// The limit hierarchy: platform defaults, overridden per merchant by limit id.
// ---------------------------------------------------------------------------------------------

type LimitPolicy struct {
	defaultOrder      []string // declaration order, like Java's LinkedHashMap
	defaults          map[string]Limit
	merchantOverrides map[string]map[string]Limit
}

func NewLimitPolicy() *LimitPolicy {
	return &LimitPolicy{defaults: map[string]Limit{}, merchantOverrides: map[string]map[string]Limit{}}
}

func (p *LimitPolicy) AddDefault(limit Limit) error {
	if _, dup := p.defaults[limit.ID]; dup {
		return fmt.Errorf("%w: duplicate limit id %s", ErrInvalidArgument, limit.ID)
	}
	p.defaults[limit.ID] = limit
	p.defaultOrder = append(p.defaultOrder, limit.ID)
	return nil
}

// Override replaces the default with the same id for one merchant; it never relaxes a limit the default does not have.
func (p *LimitPolicy) Override(merchantID string, limit Limit) error {
	if _, ok := p.defaults[limit.ID]; !ok {
		return fmt.Errorf("%w: no default limit to override: %s", ErrInvalidArgument, limit.ID)
	}
	overrides, ok := p.merchantOverrides[merchantID]
	if !ok {
		overrides = map[string]Limit{}
		p.merchantOverrides[merchantID] = overrides
	}
	overrides[limit.ID] = limit
	return nil
}

// LimitsFor returns the effective limits for a merchant, in the order the defaults were declared.
func (p *LimitPolicy) LimitsFor(merchantID string) []Limit {
	overrides := p.merchantOverrides[merchantID]
	effective := make([]Limit, 0, len(p.defaultOrder))
	for _, id := range p.defaultOrder {
		if o, ok := overrides[id]; ok {
			effective = append(effective, o)
		} else {
			effective = append(effective, p.defaults[id])
		}
	}
	return effective
}

// ---------------------------------------------------------------------------------------------
// The engine: evaluate every applicable limit, then record the attempt, under one lock.
// ---------------------------------------------------------------------------------------------

type LimitEngine struct {
	mu           sync.Mutex
	policy       *LimitPolicy
	clock        Clock
	windows      map[string]*SlidingWindow // limitId + "@" + scopeKey -> window
	exposure     *ExposureTracker
	openAttempts map[string]Attempt // attemptId -> attempt, for Release()
}

func NewLimitEngine(policy *LimitPolicy, clock Clock) *LimitEngine {
	return &LimitEngine{policy: policy, clock: clock, windows: map[string]*SlidingWindow{},
		exposure: NewExposureTracker(), openAttempts: map[string]Attempt{}}
}

func (e *LimitEngine) windowFor(limit Limit, scopeKey string) *SlidingWindow {
	key := limit.ID + "@" + scopeKey
	w, ok := e.windows[key]
	if !ok {
		w, _ = NewSlidingWindow(limit.WindowMillis) // the Limit constructor already validated the window
		e.windows[key] = w
	}
	return w
}

// Check judges one attempt against every effective limit and, if not denied, records it in every
// window and in exposure. Check-and-record is one critical section so two concurrent attempts cannot
// both see "4 of 5" and both pass. All breaches are collected, not just the first: a denial
// must name every limit it tripped so the explanation is complete.
func (e *LimitEngine) Check(attempt Attempt) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock.NowMillis()
	reasons := []Reason{}
	outcome := ALLOW
	for _, limit := range e.policy.LimitsFor(attempt.MerchantID) {
		if !limit.Applies(attempt) {
			continue
		}
		r, breached := e.evaluate(limit, attempt, now)
		if !breached {
			continue
		}
		reasons = append(reasons, r)
		if limit.Severity == HARD {
			outcome = DENY
		} else if outcome == ALLOW {
			outcome = REVIEW
		}
	}
	if outcome != DENY {
		e.record(attempt, now)
	}
	return Decision{outcome, reasons}
}

func retryAfter(w *SlidingWindow, now int64) *int64 {
	expiry, ok := w.NextExpiryMillis(now)
	if !ok {
		return nil
	}
	remaining := expiry - now
	return &remaining
}

func (e *LimitEngine) evaluate(limit Limit, attempt Attempt, now int64) (Reason, bool) {
	scopeKey := limit.Scope.KeyFor(attempt)
	switch limit.Kind {
	case COUNT:
		w := e.windowFor(limit, scopeKey)
		observed := int64(w.Count(now))
		if observed+1 <= limit.MaxCount {
			return Reason{}, false
		}
		return Reason{limit.ID, limit.Scope, scopeKey, limit.Kind, limit.Severity,
			fmt.Sprintf("%d attempts", observed), fmt.Sprintf("%d attempts", observed+1), fmt.Sprintf("%d attempts", limit.MaxCount),
			limit.WindowMillis, retryAfter(w, now)}, true
	case AMOUNT:
		w := e.windowFor(limit, scopeKey)
		observed := OfMinor(w.SumMinor(now), attempt.Amount.Currency)
		wouldBe := mustPlus(observed, attempt.Amount)
		if !wouldBe.IsGreaterThan(limit.MaxAmount) {
			return Reason{}, false
		}
		return Reason{limit.ID, limit.Scope, scopeKey, limit.Kind, limit.Severity,
			observed.String(), wouldBe.String(), limit.MaxAmount.String(),
			limit.WindowMillis, retryAfter(w, now)}, true
	default: // EXPOSURE
		observed := e.exposure.Open(scopeKey, attempt.Amount.Currency)
		wouldBe := mustPlus(observed, attempt.Amount)
		if !wouldBe.IsGreaterThan(limit.MaxAmount) {
			return Reason{}, false
		}
		return Reason{limit.ID, limit.Scope, scopeKey, limit.Kind, limit.Severity,
			observed.String(), wouldBe.String(), limit.MaxAmount.String(), 0, nil}, true
	}
}

func (e *LimitEngine) record(attempt Attempt, now int64) {
	for _, limit := range e.policy.LimitsFor(attempt.MerchantID) {
		if !limit.Applies(attempt) {
			continue
		}
		scopeKey := limit.Scope.KeyFor(attempt)
		switch limit.Kind {
		case COUNT, AMOUNT:
			if err := e.windowFor(limit, scopeKey).Add(now, attempt.Amount.MinorUnits); err != nil {
				panic(err) // the clock is monotonic under the lock; going backwards is a programming error
			}
		case EXPOSURE:
			e.exposure.Add(scopeKey, attempt.AttemptID, attempt.Amount)
		}
	}
	e.openAttempts[attempt.AttemptID] = attempt
}

// Release: capture, void, or authorization expiry: the attempt no longer counts toward exposure. Velocity history is untouched.
func (e *LimitEngine) Release(attemptID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	attempt, ok := e.openAttempts[attemptID]
	if !ok {
		return
	}
	delete(e.openAttempts, attemptID)
	for _, limit := range e.policy.LimitsFor(attempt.MerchantID) {
		if limit.Kind == EXPOSURE {
			e.exposure.Release(limit.Scope.KeyFor(attempt), attemptID)
		}
	}
}

func (e *LimitEngine) OpenExposure(limit Limit, sample Attempt) Money {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exposure.Open(limit.Scope.KeyFor(sample), sample.Amount.Currency)
}

func (e *LimitEngine) WindowCount(limit Limit, sample Attempt) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.windowFor(limit, limit.Scope.KeyFor(sample)).Count(e.clock.NowMillis())
}

// ---------------------------------------------------------------------------------------------
// Demo.
// ---------------------------------------------------------------------------------------------

const (
	SECOND int64 = 1000
	MINUTE       = 60 * SECOND
	HOUR         = 60 * MINUTE
	DAY          = 24 * HOUR
)

// must unwraps a (value, error) pair for literal policies and demos, where an error is a bug.
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

func standardPolicy() *LimitPolicy {
	p := NewLimitPolicy()
	mustOK(p.AddDefault(must(CountLimit("card-attempts-1h", CARD, HOUR, 5, HARD))))
	mustOK(p.AddDefault(must(AmountLimit("card-amount-24h", CARD, DAY, mustParse("500.00", USD), SOFT))))
	mustOK(p.AddDefault(must(AmountLimit("merchant-amount-24h", MERCHANT, DAY, mustParse("10000.00", USD), HARD))))
	mustOK(p.AddDefault(must(ExposureLimit("merchant-exposure", MERCHANT, mustParse("2000.00", USD), HARD))))
	mustOK(p.Override("merchant_bigbox", must(AmountLimit("merchant-amount-24h", MERCHANT, DAY, mustParse("250000.00", USD), HARD))))
	mustOK(p.Override("merchant_bigbox", must(ExposureLimit("merchant-exposure", MERCHANT, mustParse("50000.00", USD), HARD))))
	return p
}

func show(label string, d Decision) {
	fmt.Printf("%s -> %v\n", label, d.Outcome)
	for _, line := range d.Explanations() {
		fmt.Println("    " + line)
	}
}

func attempt(id, merchant, card, amount string) Attempt {
	return must(NewAttempt(id, merchant, card, mustParse(amount, USD)))
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	engine := NewLimitEngine(standardPolicy(), clock)
	acme, card := "merchant_acme", "fp_4242"

	fmt.Println("== Card testing: 5 attempts per card per hour, hard ==")
	for i := 1; i <= 6; i++ {
		d := engine.Check(attempt(fmt.Sprintf("att-%d", i), acme, card, "1.00"))
		show(fmt.Sprintf("attempt %d (1.00 USD)", i), d)
		if d.Outcome == DENY {
			fmt.Printf("    Retry-After: %ds\n", d.RetryAfterSeconds())
		}
		clock.Advance(10 * SECOND)
	}

	fmt.Println("\n== The window slides: 1h after the first attempt, one slot frees ==")
	clock.Advance(HOUR - 60*SECOND) // now exactly 1h after attempt 1
	show("attempt 7 at t0+1h", engine.Check(attempt("att-7", acme, card, "1.00")))
	show("attempt 8 at t0+1h", engine.Check(attempt("att-8", acme, card, "1.00")))

	fmt.Println("\n== Soft limit: a big ticket is allowed but flagged for review ==")
	clock.Advance(2 * HOUR)
	card2 := "fp_1881"
	show("480.00 USD", engine.Check(attempt("att-9", acme, card2, "480.00")))
	show("30.00 USD (would be 510.00 of 500.00)", engine.Check(attempt("att-10", acme, card2, "30.00")))

	fmt.Println("\n== Exposure: open authorizations count until captured or voided ==")
	card3 := "fp_7777"
	show("1,400.00 USD auth", engine.Check(attempt("att-11", acme, card3, "1400.00")))
	show("200.00 USD auth (open exposure 1,916.00)", engine.Check(attempt("att-12", acme, "fp_7778", "200.00")))
	engine.Release("att-11")
	fmt.Println("captured att-11: exposure back to " + engine.OpenExposure(
		standardPolicy().LimitsFor(acme)[3], attempt("x", acme, card3, "1.00")).String())
	show("200.00 USD auth after capture", engine.Check(attempt("att-13", acme, "fp_7779", "200.00")))

	fmt.Println("\n== Hierarchy: bigbox overrides the merchant limits, keeps the card limits ==")
	show("bigbox 15,000.00 USD", engine.Check(attempt("att-14", "merchant_bigbox", "fp_9001", "15000.00")))
	show("acme   15,000.00 USD", engine.Check(attempt("att-15", acme, "fp_9002", "15000.00")))

	fmt.Println("\n== A denial names every limit it tripped ==")
	multi := engine.Check(attempt("att-16", acme, card, "1400.00"))
	show("card fp_4242, 1,400.00 USD", multi)
	fmt.Printf("breached limits: %d\n", len(multi.Reasons))
}
