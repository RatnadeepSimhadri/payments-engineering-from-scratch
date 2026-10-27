// Lesson: phases/08-messaging-and-streams/03-delivery-guarantees/docs/en.md
// At-Most-Once, At-Least-Once, and Effectively-Once
// Run:  go run .
// Test: go test ./...
// Sources: KIP-98 (Exactly Once Delivery and Transactional Messaging in Kafka); Kafka docs section 4.6 "Message Delivery
// Semantics"; Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012). Money follows Phase 10, Lesson 01.
// Go 1.22+, standard library only. Same design as the Java and Python versions. Crashes are injected by a FaultInjector
// at a chosen offset; nothing is random.
package main

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------------------------------------------------
// The log and the records on it. One partition is enough; the delivery question is per partition anyway.
// ---------------------------------------------------------------------------------------------------------------------

// Record is one captured payment on the log. PaymentID is the business key that makes idempotent processing possible.
type Record struct {
	Offset    int64
	PaymentID string
	Amount    Money
}

// Log is a single-partition append-only log, the shape of Phase 8, Lesson 01 reduced to what this lesson needs.
type Log struct {
	records []Record
}

func (l *Log) Append(paymentID string, amount Money) int64 {
	offset := int64(len(l.records))
	l.records = append(l.records, Record{offset, paymentID, amount})
	return offset
}

func (l *Log) EndOffset() int64 { return int64(len(l.records)) }

// Read returns up to maxRecords from fromOffset; empty at the end offset.
func (l *Log) Read(fromOffset int64, maxRecords int) ([]Record, error) {
	if fromOffset < 0 || fromOffset > int64(len(l.records)) {
		return nil, fmt.Errorf("offset %d out of range", fromOffset)
	}
	to := min(int64(len(l.records)), fromOffset+int64(maxRecords))
	return append([]Record{}, l.records[fromOffset:to]...), nil
}

// ---------------------------------------------------------------------------------------------------------------------
// Faults. The process dies at a chosen point; the test chooses the point.
// ---------------------------------------------------------------------------------------------------------------------

// ErrCrash is returned when the simulated process dies. Everything not yet durably committed is gone.
var ErrCrash = errors.New("process crashed")

// FaultInjector fires exactly once: after the side effect for crashAtOffset has been performed and before the commit.
type FaultInjector struct {
	crashAtOffset int64
	armed         bool
}

func NewFaultInjector(crashAtOffset int64) *FaultInjector {
	return &FaultInjector{crashAtOffset: crashAtOffset, armed: true}
}

func NoFaults() *FaultInjector { return NewFaultInjector(-1) }

func (f *FaultInjector) AfterSideEffect(offset int64) error {
	if f.armed && offset == f.crashAtOffset {
		f.armed = false
		return fmt.Errorf("%w after the side effect for offset %d, before the commit", ErrCrash, offset)
	}
	return nil
}

func (f *FaultInjector) Fired() bool { return !f.armed }

// ---------------------------------------------------------------------------------------------------------------------
// The durable store: ledger postings, the processed-id set, and the committed offset, with optional atomicity.
// ---------------------------------------------------------------------------------------------------------------------

type Posting struct {
	PaymentID string
	Amount    Money
}

// Store holds everything the consumer persists, so a transaction can cover any combination of it. Writes made directly
// are durable immediately (one write, one fsync, one failure point each). Writes made through a Transaction become
// durable together or not at all.
type Store struct {
	postings        []Posting
	processed       map[string]bool
	committedOffset int64
}

func NewStore() *Store { return &Store{processed: map[string]bool{}} }

// --- direct, non-atomic writes -------------------------------------------------------------------------------------

func (s *Store) Post(posting Posting) { s.postings = append(s.postings, posting) }

// PostIfNew is insert-if-absent on the business key, in the same durable store as the postings. False on a duplicate.
func (s *Store) PostIfNew(posting Posting) bool {
	if s.processed[posting.PaymentID] {
		return false
	}
	s.processed[posting.PaymentID] = true
	s.postings = append(s.postings, posting)
	return true
}

func (s *Store) CommitOffset(nextOffset int64) error {
	if nextOffset < 0 {
		return errors.New("offset must be non-negative")
	}
	s.committedOffset = nextOffset
	return nil
}

// --- atomic writes ---------------------------------------------------------------------------------------------------

func (s *Store) Begin() *Transaction { return &Transaction{store: s, bufferedOffset: -1} }

// ErrTransactionDone is returned when a committed transaction is used again.
var ErrTransactionDone = errors.New("transaction already committed")

// Transaction buffers writes and applies them all in Commit(). A crash before Commit() leaves the store untouched.
type Transaction struct {
	store            *Store
	bufferedPostings []Posting
	bufferedOffset   int64
	done             bool
}

func (t *Transaction) Post(posting Posting) error {
	if t.done {
		return ErrTransactionDone
	}
	t.bufferedPostings = append(t.bufferedPostings, posting)
	return nil
}

func (t *Transaction) CommitOffset(nextOffset int64) error {
	if t.done {
		return ErrTransactionDone
	}
	t.bufferedOffset = nextOffset
	return nil
}

func (t *Transaction) Commit() error {
	if t.done {
		return ErrTransactionDone
	}
	t.store.postings = append(t.store.postings, t.bufferedPostings...)
	for _, p := range t.bufferedPostings {
		t.store.processed[p.PaymentID] = true
	}
	if t.bufferedOffset >= 0 {
		t.store.committedOffset = t.bufferedOffset
	}
	t.done = true
	return nil
}

// --- reads -----------------------------------------------------------------------------------------------------------

func (s *Store) CommittedOffset() int64 { return s.committedOffset }

func (s *Store) Postings() []Posting { return append([]Posting(nil), s.postings...) }

// Balance sums the postings; every posting carries the same currency in this lesson, so a mismatch is a bug.
func (s *Store) Balance(currency Currency) Money {
	total := Zero(currency)
	for _, p := range s.postings {
		sum, err := total.Plus(p.Amount)
		if err != nil {
			panic(err)
		}
		total = sum
	}
	return total
}

// ---------------------------------------------------------------------------------------------------------------------
// The four ways to order "process" and "commit".
// ---------------------------------------------------------------------------------------------------------------------

type Strategy int

const (
	// AtMostOnce: commit first, then process. A crash loses whatever was committed but not yet processed.
	AtMostOnce Strategy = iota
	// AtLeastOnce: process first, then commit. A crash re-delivers whatever was processed but not yet committed.
	AtLeastOnce
	// AtLeastOnceIdempotent: at-least-once offsets, but the side effect is an insert-if-absent on the business key.
	AtLeastOnceIdempotent
	// Transactional: side effect and offset commit in one atomic transaction per record.
	Transactional
)

// Strategies lists every strategy in declaration order, like Java's Strategy.values().
var Strategies = []Strategy{AtMostOnce, AtLeastOnce, AtLeastOnceIdempotent, Transactional}

func (s Strategy) String() string {
	switch s {
	case AtMostOnce:
		return "AT_MOST_ONCE"
	case AtLeastOnce:
		return "AT_LEAST_ONCE"
	case AtLeastOnceIdempotent:
		return "AT_LEAST_ONCE_IDEMPOTENT"
	default:
		return "TRANSACTIONAL"
	}
}

// Consumer is one consumer process. It starts from the store's committed offset and runs until the log is exhausted or
// it crashes.
type Consumer struct {
	strategy  Strategy
	log       *Log
	store     *Store
	faults    *FaultInjector
	batchSize int
}

func NewConsumer(strategy Strategy, log *Log, store *Store, faults *FaultInjector, batchSize int) (*Consumer, error) {
	if batchSize <= 0 {
		return nil, errors.New("batchSize must be positive")
	}
	return &Consumer{strategy, log, store, faults, batchSize}, nil
}

// RunToEnd processes the log; it returns ErrCrash when the injected fault fires (the process is gone at that point).
func (c *Consumer) RunToEnd() error {
	position := c.store.CommittedOffset()
	for {
		batch, err := c.log.Read(position, c.batchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		batchEnd := batch[len(batch)-1].Offset + 1
		switch c.strategy {
		case AtMostOnce:
			if err := c.store.CommitOffset(batchEnd); err != nil {
				return err
			}
			for _, r := range batch {
				c.store.Post(Posting{r.PaymentID, r.Amount})
				if err := c.faults.AfterSideEffect(r.Offset); err != nil {
					return err
				}
			}
		case AtLeastOnce:
			for _, r := range batch {
				c.store.Post(Posting{r.PaymentID, r.Amount})
				if err := c.faults.AfterSideEffect(r.Offset); err != nil {
					return err
				}
			}
			if err := c.store.CommitOffset(batchEnd); err != nil {
				return err
			}
		case AtLeastOnceIdempotent:
			for _, r := range batch {
				c.store.PostIfNew(Posting{r.PaymentID, r.Amount})
				if err := c.faults.AfterSideEffect(r.Offset); err != nil {
					return err
				}
			}
			if err := c.store.CommitOffset(batchEnd); err != nil {
				return err
			}
		case Transactional:
			for _, r := range batch {
				tx := c.store.Begin()
				if err := tx.Post(Posting{r.PaymentID, r.Amount}); err != nil {
					return err
				}
				if err := tx.CommitOffset(r.Offset + 1); err != nil {
					return err
				}
				if err := c.faults.AfterSideEffect(r.Offset); err != nil {
					return err // the buffered transaction dies with the process
				}
				if err := tx.Commit(); err != nil {
					return err
				}
			}
		}
		position = batchEnd
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// The experiment: run a strategy, crash it once, restart it, and count what the ledger ended up with.
// ---------------------------------------------------------------------------------------------------------------------

type Outcome struct {
	Strategy   Strategy
	Posted     int
	Duplicates int
	Lost       int
	Crashes    int
	Balance    Money
}

func (o Outcome) EffectivelyOnce() bool { return o.Duplicates == 0 && o.Lost == 0 }

// Run executes a strategy against the log, restarting a fresh process after every crash, and measures the ledger.
// Any error other than ErrCrash is a programming error in the simulation and panics.
func Run(strategy Strategy, log *Log, crashAtOffset int64, batchSize int) Outcome {
	store := NewStore()
	faults := NewFaultInjector(crashAtOffset)
	crashes := 0
	for {
		consumer, err := NewConsumer(strategy, log, store, faults, batchSize)
		if err != nil {
			panic(err)
		}
		err = consumer.RunToEnd()
		if err == nil {
			break
		}
		if !errors.Is(err, ErrCrash) {
			panic(err)
		}
		crashes++ // the process is gone; a fresh one starts from store.CommittedOffset()
	}
	return Measure(strategy, log, store, crashes)
}

// Measure counts, per payment id on the log, how many postings the store holds.
func Measure(strategy Strategy, log *Log, store *Store, crashes int) Outcome {
	all, _ := log.Read(0, int(log.EndOffset()))
	counts := map[string]int{}
	for _, r := range all {
		counts[r.PaymentID] = 0
	}
	for _, p := range store.Postings() {
		counts[p.PaymentID]++
	}
	duplicates, lost := 0, 0
	for _, c := range counts {
		if c == 0 {
			lost++
		}
		if c > 1 {
			duplicates += c - 1
		}
	}
	return Outcome{strategy, len(store.Postings()), duplicates, lost, crashes, store.Balance(USD)}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// SampleLog builds the ten payments the demo and the tests share.
func SampleLog(count int) *Log {
	log := &Log{}
	for i := 0; i < count; i++ {
		log.Append(fmt.Sprintf("pay-%d", 1000+i), must(Parse(fmt.Sprintf("%d.%02d", 10+i, (i*37)%100), USD)))
	}
	return log
}

func main() {
	log := SampleLog(10)
	expected := Zero(USD)
	all := must(log.Read(0, 10))
	for _, r := range all {
		expected = must(expected.Plus(r.Amount))
	}
	fmt.Printf("== 10 captured payments on the log, batch size 4, expected ledger total %s ==\n", expected)
	for _, r := range all {
		fmt.Printf("  offset %d  %s  %s\n", r.Offset, r.PaymentID, r.Amount)
	}

	fmt.Println("\n== Crash after the side effect for offset 5, before its commit; then restart ==")
	fmt.Printf("  %-26s %-7s %-11s %-5s %-8s %s\n", "strategy", "posted", "duplicates", "lost", "crashes", "ledger total")
	for _, s := range Strategies {
		o := Run(s, log, 5, 4)
		verdict := "  <- matches"
		if !o.EffectivelyOnce() {
			if o.Lost > 0 {
				verdict = "  <- money missing"
			} else {
				verdict = "  <- money counted twice"
			}
		}
		fmt.Printf("  %-26s %-7d %-11d %-5d %-8d %s%s\n", s, o.Posted, o.Duplicates, o.Lost, o.Crashes, o.Balance, verdict)
	}

	fmt.Println("\n== Why: the order of 'process' and 'commit' around the crash ==")
	fmt.Println("  AT_MOST_ONCE    commit(8) -> post 4 -> post 5 -> CRASH -> restart at 8          : offsets 6,7 never posted")
	fmt.Println("  AT_LEAST_ONCE   post 4 -> post 5 -> CRASH -> restart at 4 -> post 4,5 again      : offsets 4,5 posted twice")
	fmt.Println("  IDEMPOTENT      same as above, but postIfNew(pay-1004) and (pay-1005) return false : duplicates absorbed")
	fmt.Println("  TRANSACTIONAL   tx{post 5, commit 6} -> CRASH before tx.commit -> restart at 5     : nothing to absorb")

	fmt.Println("\n== No crash at all: every strategy agrees ==")
	for _, s := range Strategies {
		o := Run(s, log, -1, 4)
		fmt.Printf("  %-26s posted=%d duplicates=%d lost=%d\n", s, o.Posted, o.Duplicates, o.Lost)
	}

	fmt.Println("\n== Crash at every offset 0..9: how many runs end with the right ledger? ==")
	for _, s := range Strategies {
		correct := 0
		for crashAt := int64(0); crashAt < 10; crashAt++ {
			if Run(s, log, crashAt, 4).EffectivelyOnce() {
				correct++
			}
		}
		fmt.Printf("  %-26s %d of 10\n", s, correct)
	}
}
