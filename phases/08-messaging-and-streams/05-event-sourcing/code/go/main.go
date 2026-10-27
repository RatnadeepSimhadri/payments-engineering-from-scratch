// Lesson: phases/08-messaging-and-streams/05-event-sourcing/docs/en.md
// Event Sourcing: State as a Fold Over Events
// Run:  go run .
// Test: go test ./...
// Sources: Fowler, "Event Sourcing" (martinfowler.com, 2005); Young, "CQRS Documents" (2010) on expected-version
// concurrency; EventStoreDB docs on optimistic concurrency and snapshots. Money follows Phase 10, Lesson 01.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------------------------------
// Events: facts that happened. Past tense, immutable, never validated (validation happened before they were written).
// The unexported marker method closes the set: only types in this package can be events.
// ---------------------------------------------------------------------------------------------------------------------

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

func (e Opened) String() string {
	return fmt.Sprintf("Opened[accountId=%s, currency=%s]", e.AccountID, e.Currency)
}
func (e Deposited) String() string { return fmt.Sprintf("Deposited[amount=%s]", e.Amount) }
func (e Withdrawn) String() string { return fmt.Sprintf("Withdrawn[amount=%s]", e.Amount) }
func (e Held) String() string      { return fmt.Sprintf("Held[holdId=%s, amount=%s]", e.HoldID, e.Amount) }
func (e Released) String() string  { return fmt.Sprintf("Released[holdId=%s]", e.HoldID) }

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

// ErrCommandRejected is the sentinel every business-rule rejection wraps; callers use errors.Is.
var ErrCommandRejected = errors.New("command rejected")

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCommandRejected, fmt.Sprintf(format, args...))
}

// ConcurrencyError reports an expected-version mismatch; callers match it with errors.As.
type ConcurrencyError struct {
	StreamID string
	Expected int64
	Actual   int64
}

func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf("stream %s: expected version %d but found %d", e.StreamID, e.Expected, e.Actual)
}

// ---------------------------------------------------------------------------------------------------------------------
// The aggregate: state is a pure function of the event sequence. Apply() has no branches that can fail.
// ---------------------------------------------------------------------------------------------------------------------

// AccountState is the immutable account state. Version = number of events applied so far. It is passed by value;
// the holds map is copied before every change so two states never share one.
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
		held, _ = held.Plus(m) // same currency by construction; see requirePositiveSameCurrency
	}
	avail, _ := s.Balance.Minus(held)
	return avail
}

// HoldIDs returns the hold ids in sorted order, so printing is deterministic.
func (s AccountState) HoldIDs() []string {
	ids := make([]string, 0, len(s.Holds))
	for id := range s.Holds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func copyHolds(holds map[string]Money) map[string]Money {
	out := make(map[string]Money, len(holds)+1)
	for k, v := range holds {
		out[k] = v
	}
	return out
}

// Apply is the fold step. Total: every event type is handled, every result is a new state with Version + 1.
// The Money errors are discarded because events were validated before being written; a well-formed stream cannot
// mix currencies or overflow.
func (s AccountState) Apply(event Event) AccountState {
	next := s.Version + 1
	switch e := event.(type) {
	case Opened:
		return AccountState{e.AccountID, e.Currency, Zero(e.Currency), map[string]Money{}, next}
	case Deposited:
		balance, _ := s.Balance.Plus(e.Amount)
		return AccountState{s.AccountID, s.Currency, balance, s.Holds, next}
	case Withdrawn:
		balance, _ := s.Balance.Minus(e.Amount)
		return AccountState{s.AccountID, s.Currency, balance, s.Holds, next}
	case Held:
		h := copyHolds(s.Holds)
		h[e.HoldID] = e.Amount
		return AccountState{s.AccountID, s.Currency, s.Balance, h, next}
	case Released:
		h := copyHolds(s.Holds)
		delete(h, e.HoldID) // the balance never moved; only the available amount changes
		return AccountState{s.AccountID, s.Currency, s.Balance, h, next}
	default:
		panic(fmt.Sprintf("unknown event %T", event))
	}
}

// Replay folds a whole history from the empty state. Replay(events) == Replay(prefix).ApplyAll(suffix).
func Replay(events []Event) AccountState { return EmptyState().ApplyAll(events) }

func (s AccountState) ApplyAll(events []Event) AccountState {
	for _, e := range events {
		s = s.Apply(e)
	}
	return s
}

// Equal compares states field by field; a struct containing a map is not comparable with ==.
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

// Decide validates a command against the current state and returns the events it produces. This is the only place
// business rules live. It never mutates anything; the caller appends the events and folds them.
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
	default:
		panic(fmt.Sprintf("unknown command %T", command))
	}
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

// StoredEvent is an event with the version it occupies in its stream (1-based: the first event is version 1).
type StoredEvent struct {
	Version int64
	Event   Event
}

// EventStore is the in-memory event store. One slice per stream id; Append checks the expected version.
type EventStore struct {
	streams map[string][]StoredEvent
	all     []StoredEvent
}

func NewEventStore() *EventStore { return &EventStore{streams: map[string][]StoredEvent{}} }

// Version is the current version of a stream (= number of events). 0 for a stream that does not exist.
func (s *EventStore) Version(streamID string) int64 { return int64(len(s.streams[streamID])) }

// Append writes events if and only if the stream is at expectedVersion. Returns the new version. The
// check-and-append is one step, so two writers that both loaded version 3 cannot both succeed.
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
	out := []Event{}
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

// Load returns the latest snapshot and whether one exists (Java's Optional).
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

func NewAccountService(events *EventStore, snapshots *SnapshotStore, snapshotEvery int) (*AccountService, error) {
	if snapshotEvery <= 0 {
		return nil, errors.New("snapshotEvery must be positive")
	}
	return &AccountService{events: events, snapshots: snapshots, snapshotEvery: int64(snapshotEvery)}, nil
}

// Load returns the current state: start from the latest snapshot (or empty) and fold the events after it.
func (a *AccountService) Load(streamID string) AccountState {
	base := EmptyState()
	if snap, ok := a.snapshots.Load(streamID); ok {
		base = snap.State
	}
	tail := a.events.Read(streamID, base.Version)
	a.eventsReplayedOnLastLoad = len(tail)
	return base.ApplyAll(tail)
}

// ReplayFromScratch is a full replay from version 0, ignoring snapshots. Used by tests and "rebuild everything" tooling.
func (a *AccountService) ReplayFromScratch(streamID string) AccountState {
	return Replay(a.events.Read(streamID, 0))
}

func (a *AccountService) EventsReplayedOnLastLoad() int { return a.eventsReplayedOnLastLoad }

// HandleAt handles a command against the version the caller saw. If another writer got in first, a
// *ConcurrencyError: the caller reloads, re-decides against the new state, and retries (or gives up).
func (a *AccountService) HandleAt(streamID string, command Command, expectedVersion int64) (AccountState, error) {
	current := a.Load(streamID)
	if current.Version != expectedVersion {
		return current, &ConcurrencyError{streamID, expectedVersion, current.Version}
	}
	produced, err := current.Decide(command)
	if err != nil {
		return current, err
	}
	newVersion, err := a.events.Append(streamID, expectedVersion, produced)
	if err != nil {
		return current, err
	}
	next := current.ApplyAll(produced)
	if newVersion/a.snapshotEvery > expectedVersion/a.snapshotEvery {
		a.snapshots.Save(streamID, next)
	}
	return next, nil
}

// Handle is the convenience for callers that always want the latest state: load, then handle at that version.
func (a *AccountService) Handle(streamID string, command Command) (AccountState, error) {
	return a.HandleAt(streamID, command, a.Load(streamID).Version)
}

func usd(cents int64) Money { return OfMinor(cents, USD) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func printState(s AccountState, what string) {
	fmt.Printf("%-22s v%d balance %s available %s holds [%s]\n", what, s.Version, s.Balance, s.Available(), strings.Join(s.HoldIDs(), ", "))
}

func main() {
	store := NewEventStore()
	snapshots := NewSnapshotStore()
	service := must(NewAccountService(store, snapshots, 4))
	acct := "acct-42"

	fmt.Println("== Commands become events; state is the fold ==")
	s := must(service.Handle(acct, OpenAccount{acct, USD}))
	printState(s, "opened")
	s = must(service.Handle(acct, Deposit{usd(10_000)}))
	printState(s, "deposited 100.00")
	s = must(service.Handle(acct, PlaceHold{"auth-1", usd(2_500)}))
	printState(s, "held 25.00 for auth-1")
	s = must(service.Handle(acct, Withdraw{usd(6_000)}))
	printState(s, "withdrew 60.00")

	fmt.Println("\n== Rejected commands produce no events ==")
	if _, err := service.Handle(acct, Withdraw{usd(2_000)}); errors.Is(err, ErrCommandRejected) {
		fmt.Printf("withdraw 20.00 rejected: %s (version still %d)\n", err, store.Version(acct))
	}
	if _, err := service.Handle(acct, ReleaseHold{"auth-9"}); errors.Is(err, ErrCommandRejected) {
		fmt.Println("release auth-9 rejected:", err)
	}
	if _, err := service.Handle(acct, Deposit{OfMinor(500, EUR)}); errors.Is(err, ErrCurrencyMismatch) {
		fmt.Println("deposit 5.00 EUR rejected:", err)
	}

	fmt.Println("\n== Optimistic concurrency: two writers load version 4 ==")
	seen := store.Version(acct)
	a := must(service.HandleAt(acct, ReleaseHold{"auth-1"}, seen))
	fmt.Printf("writer A released auth-1 at expected version %d -> version %d, available %s\n", seen, a.Version, a.Available())
	var conflict *ConcurrencyError
	if _, err := service.HandleAt(acct, Withdraw{usd(4_000)}, seen); errors.As(err, &conflict) {
		fmt.Printf("writer B withdraw 40.00 at expected version %d rejected: %s\n", seen, conflict)
	}
	b := must(service.Handle(acct, Withdraw{usd(4_000)}))
	fmt.Printf("writer B reloaded and retried -> version %d, balance %s\n", b.Version, b.Balance)

	fmt.Println("\n== Snapshots: every 4 events, load folds only the tail ==")
	fmt.Printf("snapshot writes so far: %d (versions 4)\n", snapshots.Writes())
	must(service.Handle(acct, Deposit{usd(1_000)}))
	must(service.Handle(acct, Deposit{usd(1_000)}))
	latest := int64(-1)
	if snap, ok := snapshots.Load(acct); ok {
		latest = snap.Version
	}
	fmt.Printf("after 8 events: snapshot writes = %d, latest snapshot version = %d\n", snapshots.Writes(), latest)
	must(service.Handle(acct, Deposit{usd(1_000)}))
	loaded := service.Load(acct)
	fmt.Printf("load at version %d replayed %d event(s) on top of the snapshot; balance %s\n",
		loaded.Version, service.EventsReplayedOnLastLoad(), loaded.Balance)
	full := service.ReplayFromScratch(acct)
	fmt.Printf("full replay from version 0 gives the same state: %t\n", loaded.Equal(full))

	fmt.Println("\n== The stream as stored ==")
	v := 1
	for _, e := range store.Read(acct, 0) {
		fmt.Printf("v%-2d %s\n", v, e)
		v++
	}
	fmt.Printf("balance is derived: %s = sum of deposits - sum of withdrawals; available = %s\n", full.Balance, full.Available())
}
