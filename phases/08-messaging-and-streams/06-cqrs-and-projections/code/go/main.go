// Lesson: phases/08-messaging-and-streams/06-cqrs-and-projections/docs/en.md
// CQRS and Projections
// Run:  go run .
// Test: go test ./...
// Sources: Greg Young, "CQRS Documents" (2010); Fowler, "CQRS" (martinfowler.com, 2011); Kleppmann, "Designing
// Data-Intensive Applications", ch. 11 (deriving state from an event log); EventStoreDB docs on $all and checkpoints.
// Go 1.22+, standard library only. Same design as the Java and Python versions. Time comes from an injected
// SimClock; nothing sleeps.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// SimClock is the simulated clock: the demo advances it explicitly. Milliseconds since an arbitrary epoch.
type SimClock struct {
	now int64
}

func NewSimClock(start int64) *SimClock { return &SimClock{now: start} }

func (c *SimClock) Now() int64 { return c.now }

func (c *SimClock) Advance(millis int64) {
	if millis < 0 {
		panic("cannot go backwards")
	}
	c.now += millis
}

// ---------------------------------------------------------------------------------------------------------------------
// Write side: commands are validated against the aggregate's current state; the result is events appended to the store.
// ---------------------------------------------------------------------------------------------------------------------

type Event interface{ isEvent() }

type AccountOpened struct {
	AccountID string
	Owner     string
	Currency  Currency
}

type Deposited struct {
	Amount    Money
	Reference string
}

type Withdrawn struct {
	Amount    Money
	Reference string
}

func (AccountOpened) isEvent() {}
func (Deposited) isEvent()     {}
func (Withdrawn) isEvent()     {}

func (e AccountOpened) String() string {
	return fmt.Sprintf("AccountOpened[accountId=%s, owner=%s, currency=%s]", e.AccountID, e.Owner, e.Currency)
}
func (e Deposited) String() string {
	return fmt.Sprintf("Deposited[amount=%s, reference=%s]", e.Amount, e.Reference)
}
func (e Withdrawn) String() string {
	return fmt.Sprintf("Withdrawn[amount=%s, reference=%s]", e.Amount, e.Reference)
}

type Command interface{ isCommand() }

type OpenAccount struct {
	AccountID string
	Owner     string
	Currency  Currency
}

type Deposit struct {
	Amount    Money
	Reference string
}

type Withdraw struct {
	Amount    Money
	Reference string
}

func (OpenAccount) isCommand() {}
func (Deposit) isCommand()     {}
func (Withdraw) isCommand()    {}

var (
	// ErrCommandRejected wraps every business-rule rejection.
	ErrCommandRejected = errors.New("command rejected")
	// ErrConcurrency wraps an expected-version mismatch on append.
	ErrConcurrency = errors.New("concurrency conflict")
	// ErrCrash is returned by the fault injector to simulate a process dying between apply and checkpoint.
	ErrCrash = errors.New("crash")
	// ErrNotCaughtUp: promotion refused because the building projection is behind the log.
	ErrNotCaughtUp = errors.New("not caught up")
	// ErrNothingBuilding: promotion requested for a name with no building version.
	ErrNothingBuilding = errors.New("nothing is being built")
	// ErrCheckpointBackwards: a checkpoint save would move a position backwards.
	ErrCheckpointBackwards = errors.New("checkpoint would move backwards")
)

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCommandRejected, fmt.Sprintf(format, args...))
}

// AccountState is the write model: only what is needed to validate commands. No owner name, no history, no daily totals.
type AccountState struct {
	AccountID string
	Currency  Currency
	Balance   Money
	Version   int64
}

var EmptyState = AccountState{}

func (s AccountState) Exists() bool { return s.AccountID != "" }

// Apply folds one event. Money errors are discarded: events were validated before being written.
func (s AccountState) Apply(event Event) AccountState {
	switch e := event.(type) {
	case AccountOpened:
		return AccountState{e.AccountID, e.Currency, Zero(e.Currency), s.Version + 1}
	case Deposited:
		balance, _ := s.Balance.Plus(e.Amount)
		return AccountState{s.AccountID, s.Currency, balance, s.Version + 1}
	case Withdrawn:
		balance, _ := s.Balance.Minus(e.Amount)
		return AccountState{s.AccountID, s.Currency, balance, s.Version + 1}
	default:
		panic(fmt.Sprintf("unknown event %T", event))
	}
}

// Decide validates the command against current state and returns the events it produces, or rejects it.
func (s AccountState) Decide(command Command) ([]Event, error) {
	switch c := command.(type) {
	case OpenAccount:
		if s.Exists() {
			return nil, rejected("account %s already open", c.AccountID)
		}
		return []Event{AccountOpened{c.AccountID, c.Owner, c.Currency}}, nil
	case Deposit:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if !c.Amount.IsPositive() {
			return nil, rejected("deposit must be positive")
		}
		return []Event{Deposited{c.Amount, c.Reference}}, nil
	case Withdraw:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if !c.Amount.IsPositive() {
			return nil, rejected("withdrawal must be positive")
		}
		if s.Balance.Compare(c.Amount) < 0 {
			return nil, rejected("insufficient funds: balance %s, requested %s", s.Balance, c.Amount)
		}
		return []Event{Withdrawn{c.Amount, c.Reference}}, nil
	default:
		panic(fmt.Sprintf("unknown command %T", command))
	}
}

func (s AccountState) requireOpen() error {
	if !s.Exists() {
		return rejected("account does not exist")
	}
	return nil
}

// EventEnvelope is an event as stored: its position in the global log (dense, 1-based, across all streams), its
// stream and version within that stream, and when the store recorded it. Projections consume the global log in
// position order.
type EventEnvelope struct {
	Position   int64
	StreamID   string
	Version    int64
	RecordedAt int64
	Event      Event
}

// EventStore is the in-memory event store with per-stream optimistic concurrency and a single global log that
// projections read.
type EventStore struct {
	streams map[string][]EventEnvelope
	all     []EventEnvelope
}

func NewEventStore() *EventStore { return &EventStore{streams: map[string][]EventEnvelope{}} }

func (s *EventStore) Version(streamID string) int64 { return int64(len(s.streams[streamID])) }

// LastPosition is the position of the newest event in the global log; 0 when empty.
func (s *EventStore) LastPosition() int64 { return int64(len(s.all)) }

// Append is check-and-append in one step: the stream must be at expectedVersion. Returns the position of the last
// event written.
func (s *EventStore) Append(streamID string, expectedVersion int64, events []Event, recordedAt int64) (int64, error) {
	stream := s.streams[streamID]
	if int64(len(stream)) != expectedVersion {
		return s.LastPosition(), fmt.Errorf("%w: stream %s expected version %d but is at %d", ErrConcurrency, streamID, expectedVersion, len(stream))
	}
	for _, e := range events {
		env := EventEnvelope{int64(len(s.all)) + 1, streamID, int64(len(stream)) + 1, recordedAt, e}
		stream = append(stream, env)
		s.all = append(s.all, env)
	}
	s.streams[streamID] = stream
	return s.LastPosition(), nil
}

func (s *EventStore) ReadStream(streamID string) []EventEnvelope { return s.streams[streamID] }

// ReadAll returns up to max events with position > afterPosition, in position order. The read side's only entry point.
func (s *EventStore) ReadAll(afterPosition int64, max int) ([]EventEnvelope, error) {
	if afterPosition < 0 || afterPosition > int64(len(s.all)) {
		return nil, fmt.Errorf("position %d not in [0, %d]", afterPosition, len(s.all))
	}
	from := int(afterPosition)
	to := min(len(s.all), from+maxInt(0, max))
	return append([]EventEnvelope{}, s.all[from:to]...), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// CommandHandler loads the stream, folds it into state, decides, appends with the loaded version. Returns the global
// position written.
type CommandHandler struct {
	store *EventStore
	clock *SimClock
}

func NewCommandHandler(store *EventStore, clock *SimClock) *CommandHandler {
	return &CommandHandler{store, clock}
}

func (h *CommandHandler) Handle(accountID string, command Command) (int64, error) {
	state := EmptyState
	for _, env := range h.store.ReadStream(accountID) {
		state = state.Apply(env.Event)
	}
	produced, err := state.Decide(command)
	if err != nil {
		return h.store.LastPosition(), err
	}
	return h.store.Append(accountID, state.Version, produced, h.clock.Now())
}

// ---------------------------------------------------------------------------------------------------------------------
// Read side: projections fold the global log into query-shaped state. Each has a name and a version; a checkpoint per
// (name, version) records how far into the log it has read.
// ---------------------------------------------------------------------------------------------------------------------

type Projection interface {
	Name() string
	Version() int
	// Apply one event. Must be safe to call twice with the same envelope (the projector may redeliver after a crash).
	Apply(envelope EventEnvelope)
}

// CheckpointStore is the durable "how far has this projection read": one position per (name, version). In production
// a row next to the view's tables.
type CheckpointStore struct {
	positions map[string]int64
}

func NewCheckpointStore() *CheckpointStore { return &CheckpointStore{positions: map[string]int64{}} }

func CheckpointKey(p Projection) string { return fmt.Sprintf("%s@v%d", p.Name(), p.Version()) }

func (c *CheckpointStore) Position(p Projection) int64 { return c.positions[CheckpointKey(p)] }

func (c *CheckpointStore) Save(p Projection, position int64) error {
	if position < c.Position(p) {
		return fmt.Errorf("%w: checkpoint for %s", ErrCheckpointBackwards, CheckpointKey(p))
	}
	c.positions[CheckpointKey(p)] = position
	return nil
}

func (c *CheckpointStore) Reset(p Projection) { delete(c.positions, CheckpointKey(p)) }

// Projector drives one projection: read from its checkpoint, apply, save the checkpoint. If the process dies after
// apply and before save (CrashAfterApplies), the next run re-applies the same events, so Apply must be idempotent.
type Projector struct {
	store             *EventStore
	checkpoints       *CheckpointStore
	projection        Projection
	batchSize         int
	crashAfterApplies int
	lastRunAt         int64
}

func NewProjector(store *EventStore, checkpoints *CheckpointStore, projection Projection, batchSize int) (*Projector, error) {
	if batchSize <= 0 {
		return nil, errors.New("batchSize must be positive")
	}
	return &Projector{store, checkpoints, projection, batchSize, -1, math.MinInt64}, nil
}

func (p *Projector) Projection() Projection { return p.projection }

func (p *Projector) Position() int64 { return p.checkpoints.Position(p.projection) }

func (p *Projector) Lag() int64 { return p.store.LastPosition() - p.Position() }

func (p *Projector) CaughtUp() bool { return p.Lag() == 0 }

func (p *Projector) CrashAfterApplies(n int) { p.crashAfterApplies = n }

// RunOnce processes one batch and returns how many events were applied. Call repeatedly (or use CatchUp) to drain
// the log. The injected crash comes back as ErrCrash with the checkpoint unsaved.
func (p *Projector) RunOnce(now int64) (int, error) {
	p.lastRunAt = now
	batch, err := p.store.ReadAll(p.Position(), p.batchSize)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, env := range batch {
		p.projection.Apply(env)
		applied++
		if p.crashAfterApplies >= 0 && applied >= p.crashAfterApplies {
			p.crashAfterApplies = -1
			return applied, fmt.Errorf("%w: projector for %s died after applying %d events, before saving the checkpoint", ErrCrash, CheckpointKey(p.projection), applied)
		}
	}
	if len(batch) > 0 {
		if err := p.checkpoints.Save(p.projection, batch[len(batch)-1].Position); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// CatchUp runs batches until the projection has read everything. Returns total events applied.
func (p *Projector) CatchUp(now int64) (int, error) {
	total := 0
	for !p.CaughtUp() {
		n, err := p.RunOnce(now)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (p *Projector) LastRunAt() int64 { return p.lastRunAt }

// BalanceView is read model 1: current balance per account. Naive version: applies whatever it is given.
// Idempotent, when set, makes each row remember the last position applied to it and ignore anything at or below it;
// this is what makes redelivery after a crash harmless (Java's IdempotentBalanceView subclass).
type BalanceView struct {
	balances       map[string]Money
	owners         map[string]string
	idempotent     bool
	appliedThrough map[string]int64
	skipped        int
}

func NewBalanceView() *BalanceView {
	return &BalanceView{balances: map[string]Money{}, owners: map[string]string{}, appliedThrough: map[string]int64{}}
}

func NewIdempotentBalanceView() *BalanceView {
	v := NewBalanceView()
	v.idempotent = true
	return v
}

func (v *BalanceView) Name() string { return "balances" }

func (v *BalanceView) Version() int {
	if v.idempotent {
		return 2
	}
	return 1
}

// TypeName mirrors Java's getClass().getSimpleName() for the demo.
func (v *BalanceView) TypeName() string {
	if v.idempotent {
		return "IdempotentBalanceView"
	}
	return "BalanceView"
}

func (v *BalanceView) Apply(env EventEnvelope) {
	if v.idempotent {
		if env.Position <= v.appliedThrough[env.StreamID] {
			v.skipped++
			return
		}
		defer func() { v.appliedThrough[env.StreamID] = env.Position }()
	}
	switch e := env.Event.(type) {
	case AccountOpened:
		v.balances[e.AccountID] = Zero(e.Currency)
		v.owners[e.AccountID] = e.Owner
	case Deposited:
		if current, ok := v.balances[env.StreamID]; ok {
			v.balances[env.StreamID], _ = current.Plus(e.Amount)
		} else {
			v.balances[env.StreamID] = e.Amount
		}
	case Withdrawn:
		v.balances[env.StreamID], _ = v.balances[env.StreamID].Minus(e.Amount)
	}
}

// Balance returns the account's balance and whether the account is known.
func (v *BalanceView) Balance(accountID string) (Money, bool) {
	m, ok := v.balances[accountID]
	return m, ok
}

func (v *BalanceView) Owner(accountID string) string { return v.owners[accountID] }

func (v *BalanceView) Skipped() int { return v.skipped }

// All formats the balances in account-id order, like Java's TreeMap toString.
func (v *BalanceView) All() string {
	ids := make([]string, 0, len(v.balances))
	for id := range v.balances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id + "=" + v.balances[id].String()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Day is one day's activity.
type Day struct {
	Deposits    int
	Withdrawals int
	Net         Money
}

func (d Day) String() string {
	return fmt.Sprintf("Day[deposits=%d, withdrawals=%d, net=%s]", d.Deposits, d.Withdrawals, d.Net)
}

// DailyActivityView is read model 2: activity per day, which the write model has no reason to know about.
type DailyActivityView struct {
	days           map[int64]Day
	appliedThrough int64
	currency       Currency
}

func NewDailyActivityView(currency Currency) *DailyActivityView {
	return &DailyActivityView{days: map[int64]Day{}, currency: currency}
}

func DayOf(millis int64) int64 { return millis / 86_400_000 }

func (v *DailyActivityView) Name() string { return "daily-activity" }

func (v *DailyActivityView) Version() int { return 1 }

func (v *DailyActivityView) Apply(env EventEnvelope) {
	if env.Position <= v.appliedThrough {
		return
	}
	v.appliedThrough = env.Position
	day := DayOf(env.RecordedAt)
	current, ok := v.days[day]
	if !ok {
		current = Day{0, 0, Zero(v.currency)}
	}
	switch e := env.Event.(type) {
	case Deposited:
		net, _ := current.Net.Plus(e.Amount)
		v.days[day] = Day{current.Deposits + 1, current.Withdrawals, net}
	case Withdrawn:
		net, _ := current.Net.Minus(e.Amount)
		v.days[day] = Day{current.Deposits, current.Withdrawals + 1, net}
	case AccountOpened:
	}
}

// Day returns the activity for a day and whether anything happened on it.
func (v *DailyActivityView) Day(day int64) (Day, bool) {
	d, ok := v.days[day]
	return d, ok
}

// Days returns the day numbers in ascending order (Java's TreeMap order).
func (v *DailyActivityView) Days() []int64 {
	out := make([]int64, 0, len(v.days))
	for d := range v.days {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Alert is one large withdrawal. Owner is "" in version 1, which never saw the AccountOpened event's owner.
type Alert struct {
	Position  int64
	AccountID string
	Owner     string
	Amount    Money
	Reference string
}

func (a Alert) String() string {
	owner := a.Owner
	if owner == "" {
		owner = "null"
	}
	return fmt.Sprintf("Alert[position=%d, accountId=%s, owner=%s, amount=%s, reference=%s]", a.Position, a.AccountID, owner, a.Amount, a.Reference)
}

// LargeWithdrawalsView is read model 3: withdrawals at or above a threshold, for a compliance screen. Version 1
// reports the account id and amount above 1000.00; version 2 lowers the threshold to 500.00 and adds the owner name,
// which requires having seen the AccountOpened event. Changing either is a new version, rebuilt from position 0.
type LargeWithdrawalsView struct {
	version        int
	threshold      Money
	owners         map[string]string
	alerts         []Alert
	appliedThrough int64
}

func NewLargeWithdrawalsView(version int, threshold Money) *LargeWithdrawalsView {
	return &LargeWithdrawalsView{version: version, threshold: threshold, owners: map[string]string{}}
}

func (v *LargeWithdrawalsView) Name() string { return "large-withdrawals" }

func (v *LargeWithdrawalsView) Version() int { return v.version }

func (v *LargeWithdrawalsView) Apply(env EventEnvelope) {
	if env.Position <= v.appliedThrough {
		return
	}
	v.appliedThrough = env.Position
	switch e := env.Event.(type) {
	case AccountOpened:
		v.owners[e.AccountID] = e.Owner
	case Withdrawn:
		if e.Amount.Compare(v.threshold) >= 0 {
			owner := ""
			if v.version >= 2 {
				owner = "?"
				if o, ok := v.owners[env.StreamID]; ok {
					owner = o
				}
			}
			v.alerts = append(v.alerts, Alert{env.Position, env.StreamID, owner, e.Amount, e.Reference})
		}
	case Deposited:
	}
}

func (v *LargeWithdrawalsView) Alerts() []Alert { return append([]Alert(nil), v.alerts...) }

// ReadSide is the query side's front door: by projection name, which version is live. A new version is registered,
// caught up in the background, and promoted only once it has read the whole log; queries never see a half-built view.
type ReadSide struct {
	live     map[string]*Projector
	building map[string]*Projector
}

func NewReadSide() *ReadSide {
	return &ReadSide{live: map[string]*Projector{}, building: map[string]*Projector{}}
}

func (r *ReadSide) Register(projector *Projector) {
	name := projector.Projection().Name()
	if _, exists := r.live[name]; exists {
		r.building[name] = projector
	} else {
		r.live[name] = projector
	}
}

// Live returns the live projector for a name; an unknown name is a programming error and panics.
func (r *ReadSide) Live(name string) *Projector {
	p, ok := r.live[name]
	if !ok {
		panic("no live projection named " + name)
	}
	return p
}

func (r *ReadSide) Building(name string) *Projector { return r.building[name] }

// Promote swaps the building version to live. Refuses while it is behind the log. Returns the retired projector.
func (r *ReadSide) Promote(name string) (*Projector, error) {
	next, ok := r.building[name]
	if !ok {
		return nil, fmt.Errorf("%w for %s", ErrNothingBuilding, name)
	}
	if !next.CaughtUp() {
		return nil, fmt.Errorf("%w: %s is %d events behind; not promoting", ErrNotCaughtUp, CheckpointKey(next.Projection()), next.Lag())
	}
	old := r.live[name]
	r.live[name] = next
	delete(r.building, name)
	return old, nil
}

// WaitFor runs the live view until it has read at least position: read-your-own-writes for one request.
func (r *ReadSide) WaitFor(name string, position, now int64, maxRuns int) (bool, error) {
	p := r.Live(name)
	for i := 0; i < maxRuns && p.Position() < position; i++ {
		if _, err := p.RunOnce(now); err != nil {
			return false, err
		}
	}
	return p.Position() >= position, nil
}

// Window is the staleness distribution measured by the ConsistencyProbe.
type Window struct {
	Events int
	Min    int64
	Mean   int64
	Max    int64
}

// Probe simulates commands issued one every commandGapMillis, with a projector that runs every pollIntervalMillis,
// and returns the staleness distribution (projectedAt - recordedAt per event). Deterministic: driven by the SimClock only.
func Probe(commands int, commandGapMillis, pollIntervalMillis int64, batchSize int) Window {
	clock := NewSimClock(1_700_000_000_000)
	store := NewEventStore()
	handler := NewCommandHandler(store, clock)
	projector := must(NewProjector(store, NewCheckpointStore(), NewIdempotentBalanceView(), batchSize))
	must(handler.Handle("acct-probe", OpenAccount{"acct-probe", "Probe", USD}))
	must(projector.CatchUp(clock.Now()))
	recordedAt := map[int64]int64{}
	staleness := []int64{}
	nextCommand, nextPoll := clock.Now(), clock.Now()+pollIntervalMillis
	issued := 0
	for issued < commands || !projector.CaughtUp() {
		if issued < commands && clock.Now() >= nextCommand {
			pos := must(handler.Handle("acct-probe", Deposit{OfMinor(int64(100+issued), USD), fmt.Sprintf("d-%d", issued)}))
			recordedAt[pos] = clock.Now()
			issued++
			nextCommand += commandGapMillis
		}
		if clock.Now() >= nextPoll {
			before := projector.Position()
			must(projector.RunOnce(clock.Now()))
			for p := before + 1; p <= projector.Position(); p++ {
				staleness = append(staleness, clock.Now()-recordedAt[p])
			}
			nextPoll += pollIntervalMillis
		}
		clock.Advance(1)
	}
	if len(staleness) == 0 {
		return Window{}
	}
	lo, hi, sum := int64(math.MaxInt64), int64(math.MinInt64), int64(0)
	for _, s := range staleness {
		lo, hi, sum = min(lo, s), max(hi, s), sum+s
	}
	return Window{len(staleness), lo, sum / int64(len(staleness)), hi}
}

func usd(minor int64) Money { return OfMinor(minor, USD) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func balanceOf(v *BalanceView, id string) string {
	m, ok := v.Balance(id)
	if !ok {
		return "null"
	}
	return m.String()
}

func main() {
	clock := NewSimClock(1_700_000_000_000)
	store := NewEventStore()
	commands := NewCommandHandler(store, clock)
	checkpoints := NewCheckpointStore()

	fmt.Println("== 1. Command side: validate against the write model, append events ==")
	must(commands.Handle("acct-alice", OpenAccount{"acct-alice", "Alice Ng", USD}))
	must(commands.Handle("acct-bob", OpenAccount{"acct-bob", "Bob Osei", USD}))
	must(commands.Handle("acct-alice", Deposit{usd(250_000), "payroll-oct"}))
	must(commands.Handle("acct-bob", Deposit{usd(80_000), "refund-4471"}))
	clock.Advance(3_600_000)
	must(commands.Handle("acct-alice", Withdraw{usd(120_000), "rent"}))
	must(commands.Handle("acct-bob", Withdraw{usd(60_000), "card-9921"}))
	clock.Advance(86_400_000)
	must(commands.Handle("acct-alice", Withdraw{usd(75_000), "tuition"}))
	lastWrite := must(commands.Handle("acct-bob", Deposit{usd(15_000), "cashback"}))
	if _, err := commands.Handle("acct-bob", Withdraw{usd(99_999), "overdraft-attempt"}); errors.Is(err, ErrCommandRejected) {
		fmt.Println("rejected:", err)
	}
	fmt.Printf("global log has %d events; acct-alice at version %d, acct-bob at version %d\n", store.LastPosition(), store.Version("acct-alice"), store.Version("acct-bob"))
	for _, env := range must(store.ReadAll(0, 100)) {
		fmt.Printf("  pos %d  %-10s v%d  %s\n", env.Position, env.StreamID, env.Version, env.Event)
	}

	fmt.Println("\n== 2. Read side: three projections fold the same log into three shapes ==")
	readSide := NewReadSide()
	balances := must(NewProjector(store, checkpoints, NewIdempotentBalanceView(), 3))
	daily := must(NewProjector(store, checkpoints, NewDailyActivityView(USD), 3))
	large := must(NewProjector(store, checkpoints, NewLargeWithdrawalsView(1, usd(100_000)), 3))
	for _, p := range []*Projector{balances, daily, large} {
		readSide.Register(p)
		applied := must(p.CatchUp(clock.Now()))
		fmt.Printf("%-22s applied %d events, checkpoint at position %d\n", CheckpointKey(p.Projection()), applied, p.Position())
	}
	balanceView := readSide.Live("balances").Projection().(*BalanceView)
	fmt.Println("balances: " + balanceView.All())
	dailyView := readSide.Live("daily-activity").Projection().(*DailyActivityView)
	for _, d := range dailyView.Days() {
		day, _ := dailyView.Day(d)
		fmt.Printf("day %d: %s\n", d, day)
	}
	largeV1 := readSide.Live("large-withdrawals").Projection().(*LargeWithdrawalsView)
	fmt.Printf("large withdrawals v1 (>= 1000.00): %s\n", formatAlerts(largeV1.Alerts()))

	fmt.Println("\n== 3. New events arrive; projections are behind until they poll ==")
	clock.Advance(500)
	pos := must(commands.Handle("acct-alice", Deposit{usd(5_000), "interest"}))
	fmt.Printf("wrote position %d; balances projection is at %d (lag %d): a read now returns %s\n",
		pos, balances.Position(), balances.Lag(), balanceOf(balanceView, "acct-alice"))
	ok := must(readSide.WaitFor("balances", pos, clock.Now(), 10))
	fmt.Printf("waitFor(position %d) -> %t; a read now returns %s\n", pos, ok, balanceOf(balanceView, "acct-alice"))

	fmt.Println("\n== 4. Projection versioning: rebuild v2 from position 0, promote when caught up ==")
	largeV2 := must(NewProjector(store, checkpoints, NewLargeWithdrawalsView(2, usd(50_000)), 4))
	readSide.Register(largeV2)
	fmt.Printf("v2 registered; checkpoint %s = %d, live is still v%d\n", CheckpointKey(largeV2.Projection()), largeV2.Position(), readSide.Live("large-withdrawals").Projection().Version())
	must(largeV2.RunOnce(clock.Now()))
	if _, err := readSide.Promote("large-withdrawals"); errors.Is(err, ErrNotCaughtUp) {
		fmt.Println("promote refused:", err)
	}
	must(largeV2.CatchUp(clock.Now()))
	retired := must(readSide.Promote("large-withdrawals"))
	fmt.Printf("promoted v%d; retired v%d (its checkpoint stays at %d until it is deleted)\n",
		readSide.Live("large-withdrawals").Projection().Version(), retired.Projection().Version(), retired.Position())
	for _, a := range largeV2.Projection().(*LargeWithdrawalsView).Alerts() {
		fmt.Println("  " + a.String())
	}

	fmt.Println("\n== 5. Crash between apply and checkpoint: naive vs idempotent projection ==")
	for _, view := range []*BalanceView{NewBalanceView(), NewIdempotentBalanceView()} {
		p := must(NewProjector(store, NewCheckpointStore(), view, 4))
		must(p.RunOnce(clock.Now()))
		p.CrashAfterApplies(2)
		if _, err := p.RunOnce(clock.Now()); errors.Is(err, ErrCrash) {
			fmt.Printf("%s: %s\n", view.TypeName(), err)
		}
		must(p.CatchUp(clock.Now()))
		fmt.Printf("  after restart: alice=%s bob=%s (truth: alice=%s bob=%s)\n",
			balanceOf(view, "acct-alice"), balanceOf(view, "acct-bob"), balanceOf(balanceView, "acct-alice"), balanceOf(balanceView, "acct-bob"))
	}

	fmt.Println("\n== 6. The eventual-consistency window, measured ==")
	for _, poll := range []int64{50, 10, 1} {
		w := Probe(200, 7, poll, 100)
		fmt.Printf("200 commands every 7 ms, projector polls every %2d ms: staleness min=%d mean=%d max=%d ms over %d events\n",
			poll, w.Min, w.Mean, w.Max, w.Events)
	}
	small := Probe(200, 7, 10, 1)
	fmt.Printf("same, polling every 10 ms but batchSize=1 (projector slower than the producer): staleness mean=%d max=%d ms\n", small.Mean, small.Max)
	fmt.Printf("\nlast write position in the main store: %d; every live projection is at %d\n", store.LastPosition(), readSide.Live("balances").Position())
	if lastWrite <= 0 {
		panic("unreachable")
	}
}

func formatAlerts(alerts []Alert) string {
	parts := make([]string, len(alerts))
	for i, a := range alerts {
		parts[i] = a.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
