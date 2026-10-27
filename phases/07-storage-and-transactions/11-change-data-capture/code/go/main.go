// Lesson: phases/07-storage-and-transactions/11-change-data-capture/docs/en.md
// Change Data Capture: turn a database commit log into an ordered change stream with LSNs, consumer offsets,
// snapshot-then-stream bootstrap, and an idempotent sink.
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only. Event envelope follows the Debezium change-event shape (op c/u/d/r, before, after, source).
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("cannot combine currencies")
	ErrOverflow         = errors.New("int64 overflow")
	ErrTxnFinished      = errors.New("transaction is finished")
	ErrLogTruncated     = errors.New("log truncated")
)

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if other.MinorUnits == math.MinInt64 {
		return Money{}, ErrOverflow // Math.negateExact
	}
	return m.Plus(Money{-other.MinorUnits, m.Currency})
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs/scale, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// Clock is the injected time source so demos and tests are deterministic.
type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(start int64) *FakeClock { return &FakeClock{now: start} }
func (c *FakeClock) NowMillis() int64     { return c.now }
func (c *FakeClock) Advance(millis int64) { c.now += millis }

// Row is an insertion-ordered set of string columns (Java's LinkedHashMap<String, String>). A nil *Row is Java's null.
// Rows stored in log records are private copies: a Row has no setter once built, so records cannot be mutated.
type Row struct {
	keys []string
	vals map[string]string
}

// NewRow builds a row from alternating column, value pairs.
func NewRow(kv ...string) *Row {
	r := &Row{vals: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.set(kv[i], kv[i+1])
	}
	return r
}

func (r *Row) set(col, val string) {
	if _, ok := r.vals[col]; !ok {
		r.keys = append(r.keys, col)
	}
	r.vals[col] = val
}

// Get returns a column's value and whether the column exists.
func (r *Row) Get(col string) (string, bool) {
	if r == nil {
		return "", false
	}
	v, ok := r.vals[col]
	return v, ok
}

// Copy returns an independent copy; Copy of nil is nil.
func (r *Row) Copy() *Row {
	if r == nil {
		return nil
	}
	out := &Row{vals: make(map[string]string, len(r.vals))}
	for _, k := range r.keys {
		out.set(k, r.vals[k])
	}
	return out
}

// Equal compares columns and values, ignoring order.
func (r *Row) Equal(o *Row) bool {
	if r == nil || o == nil {
		return r == nil && o == nil
	}
	if len(r.vals) != len(o.vals) {
		return false
	}
	for k, v := range r.vals {
		if ov, ok := o.vals[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

func (r *Row) String() string {
	if r == nil {
		return "null"
	}
	parts := make([]string, len(r.keys))
	for i, k := range r.keys {
		parts[i] = k + "=" + r.vals[k]
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Op is the kind of row change a log record describes.
type Op int

const (
	Insert Op = iota
	Update
	Delete
)

func (o Op) String() string { return [...]string{"INSERT", "UPDATE", "DELETE"}[o] }

// LogRecord is one entry in the commit log. The LSN (log sequence number) is assigned at commit, is strictly increasing
// across the whole log, and is the only ordering CDC relies on. Before is nil for Insert, After is nil for Delete.
// Column values are strings; an amount column carries integer minor units, never a decimal.
type LogRecord struct {
	Lsn    int64
	TxID   int64
	Op     Op
	Table  string
	Key    string
	Before *Row
	After  *Row
}

func NewLogRecord(lsn, txID int64, op Op, table, key string, before, after *Row) LogRecord {
	return LogRecord{lsn, txID, op, table, key, before.Copy(), after.Copy()}
}

// LogTruncatedError is returned when a reader asks for an LSN the log has already truncated: the reader must re-snapshot.
type LogTruncatedError struct {
	Requested       int64
	OldestAvailable int64
}

func (e *LogTruncatedError) Error() string {
	return fmt.Sprintf("LSN %d has been truncated; oldest available is %d", e.Requested, e.OldestAvailable)
}

func (e *LogTruncatedError) Is(target error) bool { return target == ErrLogTruncated }

// CommitLog is an append-only commit log with retention. Records are appended in LSN order; ReadFrom returns records
// with LSN strictly greater than the argument, which is the "offset" contract: an offset names the last LSN consumed.
type CommitLog struct {
	records        []LogRecord
	truncatedBelow int64 // smallest LSN still present
	nextLsn        int64
}

func NewCommitLog() *CommitLog { return &CommitLog{truncatedBelow: 1, nextLsn: 1} }

func (l *CommitLog) Append(txID int64, op Op, table, key string, before, after *Row) int64 {
	r := NewLogRecord(l.nextLsn, txID, op, table, key, before, after)
	l.nextLsn++
	l.records = append(l.records, r)
	return r.Lsn
}

// HeadLsn is the LSN of the newest record, or 0 if the log is empty.
func (l *CommitLog) HeadLsn() int64   { return l.nextLsn - 1 }
func (l *CommitLog) OldestLsn() int64 { return l.truncatedBelow }
func (l *CommitLog) Size() int        { return len(l.records) }

// ReadFrom returns records with lsn > afterLsn, oldest first, at most max.
func (l *CommitLog) ReadFrom(afterLsn int64, max int) ([]LogRecord, error) {
	if afterLsn+1 < l.truncatedBelow {
		return nil, &LogTruncatedError{afterLsn + 1, l.truncatedBelow}
	}
	out := []LogRecord{}
	for _, r := range l.records {
		if r.Lsn > afterLsn {
			out = append(out, r)
			if len(out) >= max {
				break
			}
		}
	}
	return out, nil
}

// TruncateBefore drops every record with lsn < keepFromLsn. Models WAL recycling / binlog purge.
func (l *CommitLog) TruncateBefore(keepFromLsn int64) int {
	before := len(l.records)
	kept := l.records[:0]
	for _, r := range l.records {
		if r.Lsn >= keepFromLsn {
			kept = append(kept, r)
		}
	}
	l.records = kept
	if keepFromLsn > l.truncatedBelow {
		l.truncatedBelow = keepFromLsn
	}
	return before - len(l.records)
}

// KeyedRow is one (key, row) pair of a table scan.
type KeyedRow struct {
	Key string
	Row *Row
}

// Database is a toy transactional database: tables of string-column rows, a commit log, and transactions whose writes
// become visible and logged atomically at commit. Single-threaded, so "atomically" means "within one method call".
type Database struct {
	tables   map[string]map[string]*Row
	log      *CommitLog
	nextTxID int64
}

func NewDatabase() *Database {
	return &Database{tables: map[string]map[string]*Row{}, log: NewCommitLog(), nextTxID: 1}
}

func (db *Database) Log() *CommitLog { return db.log }

// Read returns the live row (nil when absent). Callers must not mutate it; Row has no setter.
func (db *Database) Read(table, key string) *Row {
	t, ok := db.tables[table]
	if !ok {
		return nil
	}
	return t[key]
}

// Scan is a snapshot of one table: a copy of every row, in key order.
func (db *Database) Scan(table string) []KeyedRow {
	t := db.tables[table]
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KeyedRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, KeyedRow{k, t[k].Copy()})
	}
	return out
}

func (db *Database) Begin() *Transaction {
	t := &Transaction{db: db, TxID: db.nextTxID}
	db.nextTxID++
	return t
}

// commit is called by Transaction.Commit: applies the buffered writes to the tables and appends them to the log.
func (db *Database) commit(tx *Transaction) int64 {
	var lastLsn int64
	for _, w := range tx.Writes {
		t, ok := db.tables[w.Table]
		if !ok {
			t = map[string]*Row{}
			db.tables[w.Table] = t
		}
		before := t[w.Key]
		var op Op
		if w.After == nil {
			if before == nil {
				continue // deleting a missing row logs nothing
			}
			delete(t, w.Key)
			op = Delete
		} else {
			t[w.Key] = w.After.Copy()
			if before == nil {
				op = Insert
			} else {
				op = Update
			}
		}
		lastLsn = db.log.Append(tx.TxID, op, w.Table, w.Key, before, w.After)
	}
	return lastLsn
}

// Write is one buffered change; After nil means delete.
type Write struct {
	Table string
	Key   string
	After *Row
}

// Transaction buffers writes until commit. Reads inside the transaction see the database state, not the buffer (kept simple).
// Put and Delete chain; a use after Commit/Rollback is remembered and reported by the next Commit or Rollback.
type Transaction struct {
	db     *Database
	TxID   int64
	Writes []Write
	done   bool
	err    error
}

func (t *Transaction) requireOpen() error {
	if t.done {
		return fmt.Errorf("%w: transaction %d", ErrTxnFinished, t.TxID)
	}
	return nil
}

func (t *Transaction) Put(table, key string, columns *Row) *Transaction {
	if err := t.requireOpen(); err != nil {
		t.err = err
		return t
	}
	t.Writes = append(t.Writes, Write{table, key, columns.Copy()})
	return t
}

func (t *Transaction) Delete(table, key string) *Transaction {
	if err := t.requireOpen(); err != nil {
		t.err = err
		return t
	}
	t.Writes = append(t.Writes, Write{table, key, nil})
	return t
}

// Commit returns the LSN of the last record this transaction wrote, or 0 if it wrote nothing.
func (t *Transaction) Commit() (int64, error) {
	if err := t.requireOpen(); err != nil {
		return 0, err
	}
	if t.err != nil {
		return 0, t.err
	}
	t.done = true
	return t.db.commit(t), nil
}

func (t *Transaction) Rollback() error {
	if err := t.requireOpen(); err != nil {
		return err
	}
	t.done = true
	t.Writes = nil
	return nil
}

// ChangeEvent is a Debezium-shaped change event. Op is "c" (create), "u" (update), "d" (delete) or "r" (read, emitted
// by a snapshot). Lsn is the position in the source log; snapshot events carry the snapshot LSN.
type ChangeEvent struct {
	Op       string
	Table    string
	Key      string
	Before   *Row
	After    *Row
	Lsn      int64
	TxID     int64
	TsMillis int64
	Snapshot bool
}

func FromLog(r LogRecord, tsMillis int64) ChangeEvent {
	op := [...]string{"c", "u", "d"}[r.Op]
	return ChangeEvent{op, r.Table, r.Key, r.Before, r.After, r.Lsn, r.TxID, tsMillis, false}
}

func SnapshotRow(table, key string, row *Row, snapshotLsn, tsMillis int64) ChangeEvent {
	return ChangeEvent{"r", table, key, nil, row, snapshotLsn, 0, tsMillis, true}
}

// OffsetStore is the durable store of consumer offsets, one per consumer name. In production: Kafka Connect offsets topic, a slot, a file.
type OffsetStore struct {
	offsets map[string]int64
	Commits int
}

func NewOffsetStore() *OffsetStore { return &OffsetStore{offsets: map[string]int64{}} }

func (o *OffsetStore) Load(consumer string) int64 { return o.offsets[consumer] }

func (o *OffsetStore) Save(consumer string, lsn int64) {
	o.offsets[consumer] = lsn
	o.Commits++
}

// Sink is something that receives change events. Must tolerate seeing the same event more than once.
type Sink interface {
	Apply(event ChangeEvent) error
}

// CdcConnector reads the commit log from a stored offset, wraps records as change events, hands them to the sink, and
// commits the offset only after the sink has applied the batch. A crash between apply and commit means the batch is
// delivered again on restart: at-least-once.
type CdcConnector struct {
	Name            string
	db              *Database
	offsets         *OffsetStore
	sink            Sink
	clock           Clock
	EventsDelivered int64
}

func NewCdcConnector(name string, db *Database, offsets *OffsetStore, sink Sink, clock Clock) *CdcConnector {
	return &CdcConnector{Name: name, db: db, offsets: offsets, sink: sink, clock: clock}
}

func (c *CdcConnector) Offset() int64 { return c.offsets.Load(c.Name) }

// Snapshot is the initial load: record the log head as the snapshot LSN, emit every current row as an "r" event, then
// store the snapshot LSN as the offset so streaming resumes exactly where the snapshot was taken. Rows changed after
// the snapshot LSN arrive again through the stream, which is why the sink must be idempotent.
func (c *CdcConnector) Snapshot(table string) (int64, error) {
	snapshotLsn := c.db.Log().HeadLsn()
	for _, kr := range c.db.Scan(table) {
		if err := c.deliver(SnapshotRow(table, kr.Key, kr.Row, snapshotLsn, c.clock.NowMillis())); err != nil {
			return 0, err
		}
	}
	c.offsets.Save(c.Name, snapshotLsn)
	return snapshotLsn, nil
}

// Poll streams one batch. Returns the number of events delivered. Commits the offset after the whole batch is applied.
func (c *CdcConnector) Poll(maxEvents int) (int, error) { return c.PollWithCrash(maxEvents, false) }

// PollWithCrash is Poll, but if crashBeforeCommit the offset is not saved: simulates dying after apply.
func (c *CdcConnector) PollWithCrash(maxEvents int, crashBeforeCommit bool) (int, error) {
	from := c.offsets.Load(c.Name)
	batch, err := c.db.Log().ReadFrom(from, maxEvents)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	last := from
	for _, r := range batch {
		if err := c.deliver(FromLog(r, c.clock.NowMillis())); err != nil {
			return 0, err
		}
		last = r.Lsn
	}
	if !crashBeforeCommit {
		c.offsets.Save(c.Name, last)
	}
	return len(batch), nil
}

func (c *CdcConnector) deliver(e ChangeEvent) error {
	if err := c.sink.Apply(e); err != nil {
		return err
	}
	c.EventsDelivered++
	return nil
}

func minorUnits(row *Row, col string) (Money, error) {
	v, ok := row.Get(col)
	if !ok {
		return Money{}, fmt.Errorf("missing column %s", col)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(n, USD), nil
}

// BalanceViewSink is an idempotent materialized view of the accounts table: balance per account, keyed by account id.
// It remembers the LSN last applied per key and ignores any event at or below it, so redelivery and snapshot/stream
// overlap are harmless.
type BalanceViewSink struct {
	balances   map[string]Money
	appliedLsn map[string]int64
	Applied    int
	Skipped    int
}

func NewBalanceViewSink() *BalanceViewSink {
	return &BalanceViewSink{balances: map[string]Money{}, appliedLsn: map[string]int64{}}
}

func (s *BalanceViewSink) Apply(e ChangeEvent) error {
	if e.Table != "accounts" {
		return nil
	}
	if e.Lsn <= s.appliedLsn[e.Key] {
		s.Skipped++
		return nil
	}
	if e.Op == "d" {
		delete(s.balances, e.Key)
	} else {
		m, err := minorUnits(e.After, "balance_minor")
		if err != nil {
			return err
		}
		s.balances[e.Key] = m
	}
	s.appliedLsn[e.Key] = e.Lsn
	s.Applied++
	return nil
}

func (s *BalanceViewSink) Balance(accountID string) (Money, bool) {
	m, ok := s.balances[accountID]
	return m, ok
}

func (s *BalanceViewSink) Size() int { return len(s.balances) }

// All returns a copy of the view.
func (s *BalanceViewSink) All() map[string]Money {
	out := make(map[string]Money, len(s.balances))
	for k, v := range s.balances {
		out[k] = v
	}
	return out
}

// formatBalances prints a view the way Java's TreeMap<String, Money> does: {acc_alice=75.00 USD, acc_bob=75.00 USD}.
func formatBalances(m map[string]Money) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k].String()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func balancesEqual(a, b map[string]Money) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// NaiveTotalsSink counts and sums payments as they arrive. Deliberately NOT idempotent: redelivery double-counts.
type NaiveTotalsSink struct {
	PaymentsSeen int
	Total        Money
}

func NewNaiveTotalsSink() *NaiveTotalsSink { return &NaiveTotalsSink{Total: OfMinor(0, USD)} }

func (s *NaiveTotalsSink) Apply(e ChangeEvent) error {
	if e.Table != "payments" || e.Op != "c" {
		return nil
	}
	amount, err := minorUnits(e.After, "amount_minor")
	if err != nil {
		return err
	}
	s.PaymentsSeen++
	s.Total, err = s.Total.Plus(amount)
	return err
}

// IdempotentTotalsSink is the same totals, made idempotent by remembering which payment keys have been counted.
type IdempotentTotalsSink struct {
	counted map[string]bool
	Total   Money
}

func NewIdempotentTotalsSink() *IdempotentTotalsSink {
	return &IdempotentTotalsSink{counted: map[string]bool{}, Total: OfMinor(0, USD)}
}

func (s *IdempotentTotalsSink) Apply(e ChangeEvent) error {
	if e.Table != "payments" || !(e.Op == "c" || e.Op == "r") {
		return nil
	}
	if s.counted[e.Key] {
		return nil
	}
	s.counted[e.Key] = true
	amount, err := minorUnits(e.After, "amount_minor")
	if err != nil {
		return err
	}
	s.Total, err = s.Total.Plus(amount)
	return err
}

func (s *IdempotentTotalsSink) PaymentsSeen() int { return len(s.counted) }

// RecordingSink records every event it sees, for assertions on order and envelope content.
type RecordingSink struct{ Events []ChangeEvent }

func (s *RecordingSink) Apply(e ChangeEvent) error {
	s.Events = append(s.Events, e)
	return nil
}

// ---- worked example ----

func Account(balanceMinor int64, version int) *Row {
	return NewRow("balance_minor", strconv.FormatInt(balanceMinor, 10), "currency", "USD", "version", strconv.Itoa(version))
}

func Payment(from, to string, amountMinor int64) *Row {
	return NewRow("from", from, "to", to, "amount_minor", strconv.FormatInt(amountMinor, 10), "currency", "USD")
}

func column(row *Row, col string) string {
	v, _ := row.Get(col)
	return v
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(err)
	}
	return n
}

// Transfer moves money between two accounts in one transaction: one payments row, two account updates, one commit.
func Transfer(db *Database, paymentID, from, to string, amountMinor int64) (int64, error) {
	fromBal := parseInt(column(db.Read("accounts", from), "balance_minor"))
	toBal := parseInt(column(db.Read("accounts", to), "balance_minor"))
	fromVer := int(parseInt(column(db.Read("accounts", from), "version")))
	toVer := int(parseInt(column(db.Read("accounts", to), "version")))
	return db.Begin().
		Put("payments", paymentID, Payment(from, to, amountMinor)).
		Put("accounts", from, Account(fromBal-amountMinor, fromVer+1)).
		Put("accounts", to, Account(toBal+amountMinor, toVer+1)).
		Commit()
}

func describeImage(row *Row) string {
	if row == nil {
		return "null"
	}
	if v, ok := row.Get("balance_minor"); ok {
		return v
	}
	return row.String()
}

func Describe(e ChangeEvent) string {
	return fmt.Sprintf("lsn=%d tx=%d op=%s %s/%s before=%s after=%s", e.Lsn, e.TxID, e.Op, e.Table, e.Key, describeImage(e.Before), describeImage(e.After))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	db := NewDatabase()

	fmt.Println("== 1. Transactions become log records with strictly increasing LSNs ==")
	must(db.Begin().Put("accounts", "acc_alice", Account(10_000, 1)).Put("accounts", "acc_bob", Account(5_000, 1)).Commit())
	must(Transfer(db, "pay_001", "acc_alice", "acc_bob", 2_500))
	for _, r := range must(db.Log().ReadFrom(0, 100)) {
		fmt.Println("  " + Describe(FromLog(r, clock.NowMillis())))
	}

	fmt.Println()
	fmt.Println("== 2. Snapshot, then stream from the snapshot LSN ==")
	offsets := NewOffsetStore()
	view := NewBalanceViewSink()
	connector := NewCdcConnector("balance-view", db, offsets, view, clock)
	snapLsn := must(connector.Snapshot("accounts"))
	fmt.Printf("  snapshot at lsn=%d delivered %d rows; view: %s\n", snapLsn, view.Applied, formatBalances(view.All()))
	clock.Advance(10)
	must(Transfer(db, "pay_002", "acc_bob", "acc_alice", 1_000)) // committed after the snapshot
	n := must(connector.Poll(100))
	fmt.Printf("  streamed %d events after lsn=%d; offset now %d; view: %s\n", n, snapLsn, connector.Offset(), formatBalances(view.All()))
	fmt.Printf("  database says alice=%s bob=%s\n", column(db.Read("accounts", "acc_alice"), "balance_minor"), column(db.Read("accounts", "acc_bob"), "balance_minor"))

	fmt.Println()
	fmt.Println("== 3. Crash after apply, before offset commit: at-least-once redelivery ==")
	clock.Advance(10)
	must(Transfer(db, "pay_003", "acc_alice", "acc_bob", 300))
	must(Transfer(db, "pay_004", "acc_alice", "acc_bob", 700))
	naive := NewNaiveTotalsSink()
	exact := NewIdempotentTotalsSink()
	offsets2 := NewOffsetStore()
	naiveConn := NewCdcConnector("naive-totals", db, offsets2, naive, clock)
	exactConn := NewCdcConnector("exact-totals", db, offsets2, exact, clock)
	must(naiveConn.PollWithCrash(6, true)) // applies lsn 1..6, dies before saving the offset
	must(exactConn.PollWithCrash(6, true))
	fmt.Printf("  after crash: naive total=%s (%d payments), exact total=%s (%d payments), offsets still %d/%d\n",
		naive.Total, naive.PaymentsSeen, exact.Total, exact.PaymentsSeen(), naiveConn.Offset(), exactConn.Offset())
	must(naiveConn.Poll(100)) // restart: replays 1..6 then continues
	must(exactConn.Poll(100))
	fmt.Printf("  after restart: naive total=%s (%d payments)  <- double counted\n", naive.Total, naive.PaymentsSeen)
	fmt.Printf("                 exact total=%s (%d payments)  <- idempotent by payment key\n", exact.Total, exact.PaymentsSeen())
	fmt.Printf("  true total of pay_001..pay_004 = %s\n", OfMinor(2_500+1_000+300+700, USD))

	fmt.Println()
	fmt.Println("== 4. The view catches up and the redelivered events are skipped by LSN ==")
	n = must(connector.Poll(100))
	fmt.Printf("  streamed %d events; view applied=%d skipped=%d; view: %s\n", n, view.Applied, view.Skipped, formatBalances(view.All()))
	must(Transfer(db, "pay_005", "acc_bob", "acc_alice", 1_500))
	must(connector.PollWithCrash(3, true)) // applies the three records of pay_005, dies before committing the offset
	redelivered := must(connector.Poll(100))
	fmt.Printf("  crash/replay: %d events redelivered; view applied=%d skipped=%d; view: %s\n",
		redelivered, view.Applied, view.Skipped, formatBalances(view.All()))

	fmt.Println()
	fmt.Println("== 5. Log retention: an offset older than the log forces a re-snapshot ==")
	lateOffsets := NewOffsetStore()
	lateView := NewBalanceViewSink()
	late := NewCdcConnector("late-consumer", db, lateOffsets, lateView, clock)
	dropped := db.Log().TruncateBefore(8)
	fmt.Printf("  truncated %d records; oldest lsn now %d; late consumer offset is %d\n", dropped, db.Log().OldestLsn(), late.Offset())
	if _, err := late.Poll(100); err != nil {
		var truncated *LogTruncatedError
		if errors.As(err, &truncated) {
			fmt.Println("  poll failed: " + truncated.Error())
		}
	}
	resnap := must(late.Snapshot("accounts"))
	must(late.Poll(100))
	fmt.Printf("  re-snapshot at lsn=%d then stream; late view: %s\n", resnap, formatBalances(lateView.All()))
	fmt.Printf("  both views agree: %t\n", balancesEqual(lateView.All(), view.All()))
}
