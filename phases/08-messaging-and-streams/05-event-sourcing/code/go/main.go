// Lesson: phases/08-messaging-and-streams/05-event-sourcing/docs/en.md
// Event Sourcing: State as a Fold Over Events
// Run:  go run .
// Test: go test ./...
// Sources: Fowler, "Event Sourcing" (martinfowler.com, 2005); Young, "CQRS Documents" (2010) on expected-version
//
//	concurrency; EventStoreDB docs on optimistic concurrency and snapshots. Money follows Phase 10, Lesson 01.
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------------------------------
// Money: copied from Phase 10, Lesson 01. Lessons are self-contained; there is no shared library.
// ---------------------------------------------------------------------------------------------------------------------

// Currency is an ISO 4217 code and minor-unit exponent.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	JPY = Currency{"JPY", 0}
)

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrOverflow is returned when an int64 would wrap.
var ErrOverflow = errors.New("int64 overflow")

// Money is integer minor units plus a currency. Addition is checked; mixing currencies is an error.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func Zero(currency Currency) Money { return Money{0, currency} }

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
	if other.MinorUnits == -1<<63 {
		return Money{}, ErrOverflow
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

// Compare panics on a currency mismatch: it is a programming error, and a comparison cannot return an error.
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
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	sign := ""
	if abs < 0 {
		abs = -abs
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// ---------------------------------------------------------------------------------------------------------------------
// Events: facts that happened. Past tense, immutable, never validated (validation happened before they were written).
// ---------------------------------------------------------------------------------------------------------------------

// Event is a closed set: the five types below. isEvent() keeps the set closed to this package.
type Event interface{ isEvent() }

type Opened struct {
	AccountID string
	Currency  Currency
}
type Deposited struct{ Amount Money }
type Withdrawn struct{ Amount Money }
type Held struct {
	HoldID string
	Amount Money
}
type Released struct{ HoldID string }

func (Opened) isEvent()    {}
func (Deposited) isEvent() {}
func (Withdrawn) isEvent() {}
func (Held) isEvent()      {}
func (Released) isEvent()  {}

func (e Opened) String() string    { return fmt.Sprintf("Opened{%s %s}", e.AccountID, e.Currency.Code) }
func (e Deposited) String() string { return fmt.Sprintf("Deposited{%s}", e.Amount) }
func (e Withdrawn) String() string { return fmt.Sprintf("Withdrawn{%s}", e.Amount) }
func (e Held) String() string      { return fmt.Sprintf("Held{%s %s}", e.HoldID, e.Amount) }
func (e Released) String() string  { return fmt.Sprintf("Released{%s}", e.HoldID) }

// ---------------------------------------------------------------------------------------------------------------------
// Commands: requests that may be refused. Imperative mood.
// ---------------------------------------------------------------------------------------------------------------------

type Command interface{ isCommand() }

type OpenAccount struct {
	AccountID string
	Currency  Currency
}
type Deposit struct{ Amount Money }
type Withdraw struct{ Amount Money }
type PlaceHold struct {
	HoldID string
	Amount Money
}
type ReleaseHold struct{ HoldID string }

func (OpenAccount) isCommand() {}
func (Deposit) isCommand()     {}
func (Withdraw) isCommand()    {}
func (PlaceHold) isCommand()   {}
func (ReleaseHold) isCommand() {}

// ErrCommandRejected wraps every invariant violation. No event is produced.
var ErrCommandRejected = errors.New("command rejected")

// ConcurrencyError reports that another writer appended between load and append.
type ConcurrencyError struct {
	StreamID string
	Expected int64
	Actual   int64
}

func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf("stream %s: expected version %d but found %d", e.StreamID, e.Expected, e.Actual)
}

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCommandRejected, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------------------------------------------------
// The aggregate: state is a pure function of the event sequence. Apply has no branches that can fail.
// ---------------------------------------------------------------------------------------------------------------------

// AccountState is immutable by convention: every method returns a new value and copies the holds map.
// Version is the number of events applied so far.
type AccountState struct {
	AccountID string
	Currency  Currency
	Balance   Money
	Holds     map[string]Money
	Version   int64
}

func EmptyState() AccountState { return AccountState{Holds: map[string]Money{}} }

func (s AccountState) IsOpen() bool { return s.AccountID != "" }

// Available is balance minus every active hold: what a withdrawal or new hold may use.
func (s AccountState) Available() Money {
	held := Zero(s.Currency)
	for _, m := range s.Holds {
		held, _ = held.Plus(m)
	}
	avail, _ := s.Balance.Minus(held)
	return avail
}

// HoldIDs returns the active hold ids in sorted order (maps have no order; output must be deterministic).
func (s AccountState) HoldIDs() []string {
	ids := make([]string, 0, len(s.Holds))
	for id := range s.Holds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func copyHolds(h map[string]Money) map[string]Money {
	out := make(map[string]Money, len(h)+1)
	for k, v := range h {
		out[k] = v
	}
	return out
}

// Apply is the fold step. Total: every event type is handled, every result is a new state with Version + 1.
// Events were validated before they were written, so the arithmetic here cannot fail on a well-formed stream.
func (s AccountState) Apply(event Event) AccountState {
	next := s
	next.Version = s.Version + 1
	switch e := event.(type) {
	case Opened:
		next.AccountID, next.Currency, next.Balance, next.Holds = e.AccountID, e.Currency, Zero(e.Currency), map[string]Money{}
	case Deposited:
		next.Balance, _ = s.Balance.Plus(e.Amount)
	case Withdrawn:
		next.Balance, _ = s.Balance.Minus(e.Amount)
	case Held:
		next.Holds = copyHolds(s.Holds)
		next.Holds[e.HoldID] = e.Amount
	case Released:
		next.Holds = copyHolds(s.Holds)
		delete(next.Holds, e.HoldID) // the balance never moved; only the available amount changes
	default:
		panic(fmt.Sprintf("unknown event %T", event))
	}
	return next
}

func (s AccountState) ApplyAll(events []Event) AccountState {
	for _, e := range events {
		s = s.Apply(e)
	}
	return s
}

// Replay folds a whole history from the empty state. Replay(all) == Replay(prefix).ApplyAll(suffix).
func Replay(events []Event) AccountState { return EmptyState().ApplyAll(events) }

// Equal compares two states field by field (maps are not comparable with ==).
func (s AccountState) Equal(o AccountState) bool {
	if s.AccountID != o.AccountID || s.Currency != o.Currency || s.Balance != o.Balance || s.Version != o.Version || len(s.Holds) != len(o.Holds) {
		return false
	}
	for k, v := range s.Holds {
		if ov, ok := o.Holds[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

// Decide validates a command against the current state and returns the events it produces. The only place business
// rules live. It never mutates anything; the caller appends the events and folds them.
func (s AccountState) Decide(command Command) ([]Event, error) {
	switch c := command.(type) {
	case OpenAccount:
		if s.IsOpen() {
			return nil, rejected("account %s is already open", s.AccountID)
		}
		return []Event{Opened{c.AccountID, c.Currency}}, nil
	case Deposit:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if err := s.requirePositiveSameCurrency(c.Amount); err != nil {
			return nil, err
		}
		return []Event{Deposited{c.Amount}}, nil
	case Withdraw:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if err := s.requirePositiveSameCurrency(c.Amount); err != nil {
			return nil, err
		}
		if c.Amount.Compare(s.Available()) > 0 {
			return nil, rejected("insufficient available funds: %s < %s", s.Available(), c.Amount)
		}
		return []Event{Withdrawn{c.Amount}}, nil
	case PlaceHold:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if err := s.requirePositiveSameCurrency(c.Amount); err != nil {
			return nil, err
		}
		if _, exists := s.Holds[c.HoldID]; exists {
			return nil, rejected("hold %s already exists", c.HoldID)
		}
		if c.Amount.Compare(s.Available()) > 0 {
			return nil, rejected("insufficient available funds for hold: %s < %s", s.Available(), c.Amount)
		}
		return []Event{Held{c.HoldID, c.Amount}}, nil
	case ReleaseHold:
		if err := s.requireOpen(); err != nil {
			return nil, err
		}
		if _, exists := s.Holds[c.HoldID]; !exists {
			return nil, rejected("no such hold: %s", c.HoldID)
		}
		return []Event{Released{c.HoldID}}, nil
	}
	return nil, fmt.Errorf("unknown command %T", command)
}

func (s AccountState) requireOpen() error {
	if !s.IsOpen() {
		return rejected("account is not open")
	}
	return nil
}

func (s AccountState) requirePositiveSameCurrency(amount Money) error {
	if !amount.IsPositive() {
		return rejected("amount must be positive: %s", amount)
	}
	if amount.Currency != s.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, s.Currency.Code, amount.Currency.Code)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------------------------
// Storage: an append-only stream per aggregate with optimistic concurrency, plus a snapshot store.
// ---------------------------------------------------------------------------------------------------------------------

// StoredEvent is an event with the version it occupies in its stream (1-based).
type StoredEvent struct {
	Version int64
	Event   Event
}

// EventStore is an in-memory event store. One slice per stream id; Append checks the expected version.
type EventStore struct {
	streams map[string][]StoredEvent
	all     []StoredEvent
}

func NewEventStore() *EventStore { return &EventStore{streams: map[string][]StoredEvent{}} }

// Version is the current version of a stream (= number of events). 0 for a stream that does not exist.
func (s *EventStore) Version(streamID string) int64 { return int64(len(s.streams[streamID])) }

// Append writes events iff the stream is at expectedVersion and returns the new version. Check-and-append is one step.
func (s *EventStore) Append(streamID string, expectedVersion int64, events []Event) (int64, error) {
	stream := s.streams[streamID]
	if int64(len(stream)) != expectedVersion {
		return int64(len(stream)), &ConcurrencyError{streamID, expectedVersion, int64(len(stream))}
	}
	for _, e := range events {
		stored := StoredEvent{int64(len(stream)) + 1, e}
		stream = append(stream, stored)
		s.all = append(s.all, stored)
	}
	s.streams[streamID] = stream
	return int64(len(stream)), nil
}

// Read returns events with version > afterVersion, in order. Read(id, 0) is the whole stream.
func (s *EventStore) Read(streamID string, afterVersion int64) []Event {
	var out []Event
	for _, se := range s.streams[streamID] {
		if se.Version > afterVersion {
			out = append(out, se.Event)
		}
	}
	return out
}

func (s *EventStore) TotalEvents() int { return len(s.all) }

type Snapshot struct {
	State   AccountState
	Version int64
}

// SnapshotStore keeps the latest snapshot per stream. A snapshot is a cache of a fold prefix; losing it costs time,
// never correctness.
type SnapshotStore struct {
	latest map[string]Snapshot
	writes int
}

func NewSnapshotStore() *SnapshotStore { return &SnapshotStore{latest: map[string]Snapshot{}} }

func (s *SnapshotStore) Save(streamID string, state AccountState) {
	s.latest[streamID] = Snapshot{state, state.Version}
	s.writes++
}

func (s *SnapshotStore) Load(streamID string) (Snapshot, bool) {
	snap, ok := s.latest[streamID]
	return snap, ok
}

func (s *SnapshotStore) Writes() int { return s.writes }

// AccountService is the command side: load (snapshot + tail), decide, append with expected version, snapshot every N.
type AccountService struct {
	events                   *EventStore
	snapshots                *SnapshotStore
	snapshotEvery            int64
	eventsReplayedOnLastLoad int
}

func NewAccountService(events *EventStore, snapshots *SnapshotStore, snapshotEvery int64) (*AccountService, error) {
	if snapshotEvery <= 0 {
		return nil, errors.New("snapshotEvery must be positive")
	}
	return &AccountService{events: events, snapshots: snapshots, snapshotEvery: snapshotEvery}, nil
}

// Load returns the current state: start from the latest snapshot (or empty) and fold the events after it.
func (svc *AccountService) Load(streamID string) AccountState {
	base := EmptyState()
	if snap, ok := svc.snapshots.Load(streamID); ok {
		base = snap.State
	}
	tail := svc.events.Read(streamID, base.Version)
	svc.eventsReplayedOnLastLoad = len(tail)
	return base.ApplyAll(tail)
}

// ReplayFromScratch folds the whole stream from version 0, ignoring snapshots.
func (svc *AccountService) ReplayFromScratch(streamID string) AccountState {
	return Replay(svc.events.Read(streamID, 0))
}

func (svc *AccountService) EventsReplayedOnLastLoad() int { return svc.eventsReplayedOnLastLoad }

// HandleAt handles a command against the version the caller saw. On *ConcurrencyError the caller reloads,
// re-decides against the new state, and retries or gives up.
func (svc *AccountService) HandleAt(streamID string, command Command, expectedVersion int64) (AccountState, error) {
	current := svc.Load(streamID)
	if current.Version != expectedVersion {
		return current, &ConcurrencyError{streamID, expectedVersion, current.Version}
	}
	produced, err := current.Decide(command)
	if err != nil {
		return current, err
	}
	newVersion, err := svc.events.Append(streamID, expectedVersion, produced)
	if err != nil {
		return current, err
	}
	next := current.ApplyAll(produced)
	if newVersion/svc.snapshotEvery > expectedVersion/svc.snapshotEvery {
		svc.snapshots.Save(streamID, next)
	}
	return next, nil
}

// Handle is HandleAt against the latest version.
func (svc *AccountService) Handle(streamID string, command Command) (AccountState, error) {
	return svc.HandleAt(streamID, command, svc.Load(streamID).Version)
}

func usd(cents int64) Money { return OfMinor(cents, USD) }

func printState(s AccountState, what string) {
	fmt.Printf("%-22s v%d balance %s available %s holds [%s]\n", what, s.Version, s.Balance, s.Available(), strings.Join(s.HoldIDs(), ", "))
}

func must(s AccountState, err error) AccountState {
	if err != nil {
		panic(err)
	}
	return s
}

func main() {
	store := NewEventStore()
	snapshots := NewSnapshotStore()
	service, _ := NewAccountService(store, snapshots, 4)
	acct := "acct-42"

	fmt.Println("== Commands become events; state is the fold ==")
	printState(must(service.Handle(acct, OpenAccount{acct, USD})), "opened")
	printState(must(service.Handle(acct, Deposit{usd(10_000)})), "deposited 100.00")
	printState(must(service.Handle(acct, PlaceHold{"auth-1", usd(2_500)})), "held 25.00 for auth-1")
	printState(must(service.Handle(acct, Withdraw{usd(6_000)})), "withdrew 60.00")

	fmt.Println("\n== Rejected commands produce no events ==")
	if _, err := service.Handle(acct, Withdraw{usd(2_000)}); err != nil {
		fmt.Printf("withdraw 20.00 rejected: %v (version still %d)\n", err, store.Version(acct))
	}
	if _, err := service.Handle(acct, ReleaseHold{"auth-9"}); err != nil {
		fmt.Printf("release auth-9 rejected: %v\n", err)
	}
	if _, err := service.Handle(acct, Deposit{OfMinor(500, EUR)}); err != nil {
		fmt.Printf("deposit 5.00 EUR rejected: %v\n", err)
	}

	fmt.Println("\n== Optimistic concurrency: two writers load version 4 ==")
	seen := store.Version(acct)
	a := must(service.HandleAt(acct, ReleaseHold{"auth-1"}, seen))
	fmt.Printf("writer A released auth-1 at expected version %d -> version %d, available %s\n", seen, a.Version, a.Available())
	if _, err := service.HandleAt(acct, Withdraw{usd(4_000)}, seen); err != nil {
		fmt.Printf("writer B withdraw 40.00 at expected version %d rejected: %v\n", seen, err)
	}
	b := must(service.Handle(acct, Withdraw{usd(4_000)}))
	fmt.Printf("writer B reloaded and retried -> version %d, balance %s\n", b.Version, b.Balance)

	fmt.Println("\n== Snapshots: every 4 events, load folds only the tail ==")
	fmt.Printf("snapshot writes so far: %d (versions 4)\n", snapshots.Writes())
	must(service.Handle(acct, Deposit{usd(1_000)}))
	must(service.Handle(acct, Deposit{usd(1_000)}))
	latest, _ := snapshots.Load(acct)
	fmt.Printf("after 8 events: snapshot writes = %d, latest snapshot version = %d\n", snapshots.Writes(), latest.Version)
	must(service.Handle(acct, Deposit{usd(1_000)}))
	loaded := service.Load(acct)
	fmt.Printf("load at version %d replayed %d event(s) on top of the snapshot; balance %s\n", loaded.Version, service.EventsReplayedOnLastLoad(), loaded.Balance)
	full := service.ReplayFromScratch(acct)
	fmt.Printf("full replay from version 0 gives the same state: %v\n", loaded.Equal(full))

	fmt.Println("\n== The stream as stored ==")
	for i, e := range store.Read(acct, 0) {
		fmt.Printf("v%-2d %v\n", i+1, e)
	}
	fmt.Printf("balance is derived: %s = sum of deposits - sum of withdrawals; available = %s\n", full.Balance, full.Available())
}
