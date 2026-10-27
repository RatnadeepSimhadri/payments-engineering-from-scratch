// Lesson: phases/14-payments-at-scale/04-reconciliation-at-scale/docs/en.md
// Reconciliation at Scale: Streams, Buckets, and Breaks
// Run:  go run .
// Test: go test ./...
// Sources: ISO 20022 camt.053 (statement structure); Merkle, "A Digital Signature Based on a
// Conventional Encryption Function" (hash-based set comparison); Phase 10 Lesson 08 (Reconciliation).
// Go 1.22+, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).
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

func (m Money) IsZero() bool { return m.MinorUnits == 0 }

// Compare panics on a currency mismatch: comparing USD with EUR is a programming error, as in the Java version.
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

// Side says which feed a record came from. Internal is our ledger; Processor is the acquirer's settlement file.
type Side int

const (
	Internal Side = iota
	Processor
)

func (s Side) String() string { return [...]string{"INTERNAL", "PROCESSOR"}[s] }

// Dates are UTC midnights; EpochDay is LocalDate.toEpochDay.
func Date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
func EpochDay(t time.Time) int64                { return t.Unix() / 86_400 }
func OfEpochDay(day int64) time.Time            { return time.Unix(day*86_400, 0).UTC() }
func isoDate(t time.Time) string                { return t.Format("2006-01-02") }

// Rec is one line of a feed. Ref is the shared identifier both sides carry (the processor's transaction
// reference, which the gateway stored at capture time). TxDate is the date of the underlying transaction,
// which both sides know and which is therefore safe to join on. ValueDate is the date this side booked the
// record: capture date for us, settlement date for the processor. The two value dates legitimately differ
// by a day or two.
type Rec struct {
	Ref       string
	TxDate    time.Time
	ValueDate time.Time
	Amount    Money
	Side      Side
}

func (r Rec) Bucket() BucketKey {
	return BucketKey{EpochDay(r.TxDate), r.Amount.Currency.Code, r.Amount.MinorUnits}
}

// LagDays is the days between the transaction and this side booking it; a processor lag beyond tolerance is a TIMING break.
func (r Rec) LagDays() int64 { return EpochDay(r.ValueDate) - EpochDay(r.TxDate) }

// BucketKey is the join key of the streaming merge: (txDate, currency, amount). Both feeds must be sorted by
// this key; the reconciler verifies the ordering and refuses to proceed on a violation, because a merge join
// over an unsorted feed silently reports every record as a break.
type BucketKey struct {
	EpochDay    int64
	Currency    string
	AmountMinor int64
}

// Compare orders by epoch day, then currency, then amount.
func (k BucketKey) Compare(other BucketKey) int {
	switch {
	case k.EpochDay != other.EpochDay:
		return cmpInt64(k.EpochDay, other.EpochDay)
	case k.Currency != other.Currency:
		return strings.Compare(k.Currency, other.Currency)
	default:
		return cmpInt64(k.AmountMinor, other.AmountMinor)
	}
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (k BucketKey) String() string {
	return isoDate(OfEpochDay(k.EpochDay)) + "/" + k.Currency + "/" + strconv.FormatInt(k.AmountMinor, 10)
}

// BucketDigest is an order-independent digest of a set of references. Each reference is mixed through a 64-bit
// finaliser and the results are summed; equal sets give equal digests regardless of order, and the count
// travels alongside so that a digest collision also has to match the count.
type BucketDigest struct {
	Hash       uint64
	Count      int
	TotalMinor int64
}

var EmptyDigest = BucketDigest{}

// Mix is the SplitMix64 finaliser over the bytes of the ref; uint64 wraps exactly like Java's long.
func Mix(ref string) uint64 {
	h := uint64(0x9E3779B97F4A7C15)
	for i := 0; i < len(ref); i++ {
		h = (h ^ uint64(ref[i])) * 0xBF58476D1CE4E5B9
	}
	h ^= h >> 31
	h *= 0x94D049BB133111EB
	h ^= h >> 29
	return h
}

func (d BucketDigest) Add(r Rec) (BucketDigest, error) {
	total, err := addChecked(d.TotalMinor, r.Amount.MinorUnits)
	if err != nil {
		return BucketDigest{}, err
	}
	return BucketDigest{d.Hash + Mix(r.Ref), d.Count + 1, total}, nil
}

// BreakType says how a break is explained. The order matters: the classifier tries them from most to least specific.
type BreakType int

const (
	Duplicate          BreakType = iota // the same ref appears twice on one side
	AmountMismatch                      // same ref on both sides, different amount
	Timing                              // same ref and amount, dates differ within the tolerance window
	MissingAtProcessor                  // we have it, they do not (unsettled capture, lost file line)
	MissingInternal                     // they have it, we do not (unknown settlement, lost ledger write)
)

var breakTypeNames = [...]string{"DUPLICATE", "AMOUNT_MISMATCH", "TIMING", "MISSING_AT_PROCESSOR", "MISSING_INTERNAL"}

func (t BreakType) String() string { return breakTypeNames[t] }

// AgeBand is the age band for open breaks: how long since the record's value date, as of the run date.
type AgeBand int

const (
	D0_1 AgeBand = iota
	D2_7
	D8_30
	D30Plus
)

func (b AgeBand) String() string { return [...]string{"D0_1", "D2_7", "D8_30", "D30_PLUS"}[b] }

func AgeBandOf(days int64) AgeBand {
	switch {
	case days <= 1:
		return D0_1
	case days <= 7:
		return D2_7
	case days <= 30:
		return D8_30
	}
	return D30Plus
}

// Break is one unexplained difference. Internal or Processor is nil when that side is absent.
type Break struct {
	Type      BreakType
	Ref       string
	Internal  *Rec
	Processor *Rec
	AgeDays   int64
}

// InternalAmount is the amount a side contributed to this break; used by the tie-out, so each side counts its own.
func (b Break) InternalAmount(c Currency) Money {
	if b.Internal == nil {
		return Zero(c)
	}
	return b.Internal.Amount
}

func (b Break) ProcessorAmount(c Currency) Money {
	if b.Processor == nil {
		return Zero(c)
	}
	return b.Processor.Amount
}

func (b Break) Band() AgeBand { return AgeBandOf(b.AgeDays) }

func (b Break) String() string {
	s := b.Type.String() + " ref=" + b.Ref
	if b.Internal != nil {
		s += " internal=" + b.Internal.Amount.String() + "@" + isoDate(b.Internal.ValueDate)
	}
	if b.Processor != nil {
		s += " processor=" + b.Processor.Amount.String() + "@" + isoDate(b.Processor.ValueDate)
	}
	return s + fmt.Sprintf(" age=%dd", b.AgeDays)
}

// ReconciliationReport is the result of one reconciliation run. Totals are kept per side so the tie-out can be checked.
type ReconciliationReport struct {
	Currency                                          Currency
	AsOf                                              time.Time
	BucketsSeen, BucketsFastMatched, BucketsInspected int64
	MatchedCount                                      int64
	MatchedTotal                                      Money
	InternalCount, ProcessorCount                     int64
	InternalTotal, ProcessorTotal                     Money
	Breaks                                            []Break
}

func NewReconciliationReport(currency Currency, asOf time.Time) *ReconciliationReport {
	return &ReconciliationReport{Currency: currency, AsOf: asOf, MatchedTotal: Zero(currency),
		InternalTotal: Zero(currency), ProcessorTotal: Zero(currency), Breaks: []Break{}}
}

func (r *ReconciliationReport) CountByType() map[BreakType]int64 {
	m := map[BreakType]int64{}
	for _, b := range r.Breaks {
		m[b.Type]++
	}
	return m
}

func (r *ReconciliationReport) CountByAge() map[AgeBand]int64 {
	m := map[AgeBand]int64{}
	for _, b := range r.Breaks {
		m[b.Band()]++
	}
	return m
}

// InternalInBreaks is the sum of internal-side amounts sitting in breaks (the "unexplained" column of our ledger).
func (r *ReconciliationReport) InternalInBreaks() Money {
	m := Zero(r.Currency)
	for _, b := range r.Breaks {
		m = need(m.Plus(b.InternalAmount(r.Currency)))
	}
	return m
}

func (r *ReconciliationReport) ProcessorInBreaks() Money {
	m := Zero(r.Currency)
	for _, b := range r.Breaks {
		m = need(m.Plus(b.ProcessorAmount(r.Currency)))
	}
	return m
}

// TiesOut is the tie-out: every record on each side is either matched or inside exactly one break.
// internalTotal == matchedTotal + internalInBreaks and processorTotal == matchedTotal + processorInBreaks.
// A report that does not tie out has lost or double-counted a record and must not be published.
func (r *ReconciliationReport) TiesOut() bool {
	return r.InternalTotal == need(r.MatchedTotal.Plus(r.InternalInBreaks())) &&
		r.ProcessorTotal == need(r.MatchedTotal.Plus(r.ProcessorInBreaks()))
}

// commas renders 1234567 as "1,234,567", the way Java's %,d does.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var sb strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(ch)
	}
	if neg {
		return "-" + sb.String()
	}
	return sb.String()
}

func (r *ReconciliationReport) Summary() string {
	var sb strings.Builder
	sb.WriteString("as of " + isoDate(r.AsOf) + "\n")
	fmt.Fprintf(&sb, "internal:  %s records  %v\n", commas(r.InternalCount), r.InternalTotal)
	fmt.Fprintf(&sb, "processor: %s records  %v\n", commas(r.ProcessorCount), r.ProcessorTotal)
	fmt.Fprintf(&sb, "matched:   %s records  %v\n", commas(r.MatchedCount), r.MatchedTotal)
	fmt.Fprintf(&sb, "buckets: %s seen, %s fast-matched by digest, %s inspected record by record\n",
		commas(r.BucketsSeen), commas(r.BucketsFastMatched), commas(r.BucketsInspected))
	fmt.Fprintf(&sb, "breaks: %s  (internal side %v, processor side %v)\n",
		commas(int64(len(r.Breaks))), r.InternalInBreaks(), r.ProcessorInBreaks())
	byType := r.CountByType()
	for t := Duplicate; t <= MissingInternal; t++ { // EnumMap order: declaration order, present keys only
		if n, ok := byType[t]; ok {
			fmt.Fprintf(&sb, "  %-22s %s\n", t, commas(n))
		}
	}
	sb.WriteString("aging: ")
	byAge := r.CountByAge()
	for b := D0_1; b <= D30Plus; b++ {
		if n, ok := byAge[b]; ok {
			fmt.Fprintf(&sb, "%s=%d ", b, n)
		}
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "ties out: %v\n", r.TiesOut())
	return sb.String()
}

// RecIterator yields the next record of a feed and false when the feed is exhausted.
type RecIterator func() (Rec, bool)

// SliceIterator streams a slice; a production feed would wrap a bufio.Scanner over a sorted file.
func SliceIterator(recs []Rec) RecIterator {
	i := 0
	return func() (Rec, bool) {
		if i >= len(recs) {
			return Rec{}, false
		}
		r := recs[i]
		i++
		return r, true
	}
}

var (
	// ErrUnsortedFeed replaces Java's IllegalStateException: the merge join refuses an unsorted feed.
	ErrUnsortedFeed = errors.New("feed is not sorted")
	// ErrWrongFeed replaces Java's IllegalArgumentException: a record on the wrong side.
	ErrWrongFeed = errors.New("record is on the wrong feed")
)

// StreamingReconciler is a streaming merge-join reconciler. It consumes two feeds sorted by BucketKey, matches
// whole buckets by digest when they agree, inspects a bucket record by record only when the digests differ,
// and holds in memory only the current bucket plus the unmatched leftovers. Memory is proportional to the
// number of breaks, not the number of records.
type StreamingReconciler struct {
	currency            Currency
	timingToleranceDays int64
}

func NewStreamingReconciler(currency Currency, timingToleranceDays int) (*StreamingReconciler, error) {
	if timingToleranceDays < 0 {
		return nil, errors.New("tolerance must be >= 0")
	}
	return &StreamingReconciler{currency, int64(timingToleranceDays)}, nil
}

// cursor is a peekable, order-checking cursor over one sorted feed.
type cursor struct {
	next    RecIterator
	side    Side
	head    *Rec
	headKey BucketKey
	last    *BucketKey
}

func newCursor(it RecIterator, side Side) (*cursor, error) {
	c := &cursor{next: it, side: side}
	return c, c.advance()
}

func (c *cursor) hasNext() bool  { return c.head != nil }
func (c *cursor) key() BucketKey { return c.headKey }

// drainBucket drains every record of the current bucket into out and folds it into the digest, except records
// the defer test selects, which go to deferred undigested.
func (c *cursor) drainBucket(key BucketKey, out *[]Rec, deferTest func(Rec) bool, deferred *[]Rec) (BucketDigest, error) {
	d := EmptyDigest
	for c.head != nil && c.headKey == key {
		if deferTest(*c.head) {
			*deferred = append(*deferred, *c.head)
		} else {
			*out = append(*out, *c.head)
			var err error
			if d, err = d.Add(*c.head); err != nil {
				return BucketDigest{}, err
			}
		}
		if err := c.advance(); err != nil {
			return BucketDigest{}, err
		}
	}
	return d, nil
}

func (c *cursor) advance() error {
	next, ok := c.next()
	if !ok {
		c.head = nil
		return nil
	}
	if next.Side != c.side {
		return fmt.Errorf("%w: record %s", ErrWrongFeed, next.Ref)
	}
	k := next.Bucket()
	if c.last != nil && k.Compare(*c.last) < 0 {
		return fmt.Errorf("%w: %v feed is not sorted: %v after %v", ErrUnsortedFeed, c.side, k, *c.last)
	}
	c.last = &k
	c.head = &next
	c.headKey = k
	return nil
}

// catch converts an error a helper panicked with (through need) back into the return value at the API boundary.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(error)
		if !ok {
			panic(r)
		}
		*err = e
	}
}

func need[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (s *StreamingReconciler) Reconcile(internal, processor RecIterator, asOf time.Time) (r *ReconciliationReport, err error) {
	defer catch(&err)
	r = NewReconciliationReport(s.currency, asOf)
	a := need(newCursor(internal, Internal))
	b := need(newCursor(processor, Processor))
	// Unmatched records, keyed by ref, waiting for a partner from a different bucket.
	leftoverInternal := map[string][]Rec{}
	leftoverProcessor := map[string][]Rec{}
	var bufA, bufB, late []Rec
	never := func(Rec) bool { return false }
	settledLate := func(x Rec) bool { return x.LagDays() > s.timingToleranceDays }

	for a.hasNext() || b.hasNext() {
		var key BucketKey
		switch {
		case !a.hasNext():
			key = b.key()
		case !b.hasNext():
			key = a.key()
		case a.key().Compare(b.key()) <= 0:
			key = a.key()
		default:
			key = b.key()
		}
		r.BucketsSeen++

		bufA, bufB, late = bufA[:0], bufB[:0], late[:0]
		da, db := EmptyDigest, EmptyDigest
		if a.hasNext() && a.key() == key {
			da = need(a.drainBucket(key, &bufA, never, &late))
		}
		if b.hasNext() && b.key() == key {
			db = need(b.drainBucket(key, &bufB, settledLate, &late))
		}
		r.InternalCount += int64(da.Count)
		r.ProcessorCount += int64(db.Count + len(late))
		r.InternalTotal = need(r.InternalTotal.Plus(OfMinor(da.TotalMinor, s.currency)))
		r.ProcessorTotal = need(r.ProcessorTotal.Plus(OfMinor(db.TotalMinor, s.currency)))
		for _, y := range late {
			r.ProcessorTotal = need(r.ProcessorTotal.Plus(y.Amount))
			leftoverProcessor[y.Ref] = append(leftoverProcessor[y.Ref], y)
		}

		if da == db && len(late) == 0 {
			// Fast path: same count, same digest, same total -> the bucket matches as a whole.
			r.BucketsFastMatched++
			r.MatchedCount += int64(da.Count)
			r.MatchedTotal = need(r.MatchedTotal.Plus(OfMinor(da.TotalMinor, s.currency)))
			continue
		}
		r.BucketsInspected++
		s.inspectBucket(bufA, bufB, r, leftoverInternal, leftoverProcessor, asOf)
	}
	s.classifyLeftovers(leftoverInternal, leftoverProcessor, r, asOf)
	sort.SliceStable(r.Breaks, func(i, j int) bool {
		if r.Breaks[i].Type != r.Breaks[j].Type {
			return r.Breaks[i].Type < r.Breaks[j].Type
		}
		return r.Breaks[i].Ref < r.Breaks[j].Ref
	})
	return r, nil
}

func ptr(r Rec) *Rec { return &r }

// inspectBucket pairs records by ref within one bucket. A second copy of a ref already seen in this bucket is a
// DUPLICATE on that side; anything unpaired is parked as a leftover for the cross-bucket pass.
func (s *StreamingReconciler) inspectBucket(bufA, bufB []Rec, r *ReconciliationReport,
	leftoverInternal, leftoverProcessor map[string][]Rec, asOf time.Time) {
	byRef := map[string]Rec{}
	order := []string{} // insertion order of byRef, so leftovers are parked in feed order like the Java
	matchedHere := map[string]bool{}
	for _, x := range bufA {
		if _, dup := byRef[x.Ref]; dup {
			r.Breaks = append(r.Breaks, Break{Duplicate, x.Ref, ptr(x), nil, AgeDays(x.TxDate, asOf)})
		} else {
			byRef[x.Ref] = x
			order = append(order, x.Ref)
		}
	}
	for _, y := range bufB {
		if x, ok := byRef[y.Ref]; ok {
			delete(byRef, y.Ref)
			r.MatchedCount++                                     // count the pair once
			r.MatchedTotal = need(r.MatchedTotal.Plus(x.Amount)) // one amount, matched on both sides
			matchedHere[y.Ref] = true
		} else if matchedHere[y.Ref] {
			r.Breaks = append(r.Breaks, Break{Duplicate, y.Ref, nil, ptr(y), AgeDays(y.TxDate, asOf)})
		} else {
			leftoverProcessor[y.Ref] = append(leftoverProcessor[y.Ref], y)
		}
	}
	for _, ref := range order {
		if x, ok := byRef[ref]; ok {
			leftoverInternal[ref] = append(leftoverInternal[ref], x)
		}
	}
}

// classifyLeftovers is the cross-bucket pass: pair leftovers by ref and name the break. Runs once, over breaks only.
func (s *StreamingReconciler) classifyLeftovers(li, lp map[string][]Rec, r *ReconciliationReport, asOf time.Time) {
	refs := make([]string, 0, len(li)+len(lp))
	for ref := range li {
		refs = append(refs, ref)
	}
	for ref := range lp {
		if _, both := li[ref]; !both {
			refs = append(refs, ref)
		}
	}
	for _, ref := range refs {
		xs, ys := li[ref], lp[ref]
		pairs := min(len(xs), len(ys))
		for i := 0; i < pairs; i++ {
			x, y := xs[i], ys[i]
			age := AgeDays(x.TxDate, asOf)
			lag := EpochDay(x.ValueDate) - EpochDay(y.ValueDate)
			if lag < 0 {
				lag = -lag
			}
			switch {
			case x.Amount != y.Amount:
				r.Breaks = append(r.Breaks, Break{AmountMismatch, ref, ptr(x), ptr(y), age})
			case lag <= s.timingToleranceDays:
				// Same ref, same amount, dates within tolerance: matched, but recorded for visibility.
				r.MatchedCount++
				r.MatchedTotal = need(r.MatchedTotal.Plus(x.Amount))
			default:
				r.Breaks = append(r.Breaks, Break{Timing, ref, ptr(x), ptr(y), age})
			}
		}
		for i := pairs; i < len(xs); i++ {
			x := xs[i]
			t := MissingAtProcessor
			if i > 0 || pairs > 0 {
				t = Duplicate
			}
			r.Breaks = append(r.Breaks, Break{t, ref, ptr(x), nil, AgeDays(x.TxDate, asOf)})
		}
		for i := pairs; i < len(ys); i++ {
			y := ys[i]
			t := MissingInternal
			if i > 0 || pairs > 0 {
				t = Duplicate
			}
			r.Breaks = append(r.Breaks, Break{t, ref, nil, ptr(y), AgeDays(y.TxDate, asOf)})
		}
	}
}

func AgeDays(recordDate, asOf time.Time) int64 {
	return max(0, EpochDay(asOf)-EpochDay(recordDate))
}

// Feeds is two sorted feeds with a known number of injected breaks.
type Feeds struct {
	Internal       []Rec
	Processor      []Rec
	InjectedBreaks int
}

// FeedGenerator is a seeded generator of two feeds with a known number of injected breaks, so the demo and the
// tests can assert exactly what the reconciler must find. Each feed is returned sorted by BucketKey.
var FeedGenerator feedGenerator

type feedGenerator struct{}

func (feedGenerator) Generate(seed int64, records int, start time.Time, days int, c Currency, breakRate float64) Feeds {
	rnd := NewJavaRandom(seed)
	internal := make([]Rec, 0, records)
	processor := make([]Rec, 0, records)
	injected := 0
	for i := 0; i < records; i++ {
		ref := "TX" + strings.ToUpper(strconv.FormatInt(1_000_000_000+int64(i), 36))
		d := start.AddDate(0, 0, int(rnd.NextIntBound(int32(days))))
		cents := 100 + int64(rnd.NextIntBound(50_000)) // 1.00 .. 500.99
		if rnd.NextIntBound(5) == 0 {
			cents = (cents / 100) * 100 // round prices collide in buckets
		}
		amt := OfMinor(cents, c)
		internal = append(internal, Rec{ref, d, d, amt, Internal})
		if rnd.NextDouble() < breakRate {
			injected++
			switch rnd.NextIntBound(4) {
			case 0: // missing at processor: emit nothing
			case 1: // off by a cent
				processor = append(processor, Rec{ref, d, d.AddDate(0, 0, 1), OfMinor(cents+1, c), Processor})
			case 2: // settled late
				processor = append(processor, Rec{ref, d, d.AddDate(0, 0, 5), amt, Processor})
			default: // file line twice
				processor = append(processor, Rec{ref, d, d.AddDate(0, 0, 1), amt, Processor})
				processor = append(processor, Rec{ref, d, d.AddDate(0, 0, 1), amt, Processor})
			}
		} else {
			// Normal case: processor settles same day or next day (within tolerance).
			processor = append(processor, Rec{ref, d, d.AddDate(0, 0, int(rnd.NextIntBound(2))), amt, Processor})
		}
	}
	// A handful of records only the processor knows about.
	for i := 0; i < max(1, records/200_000); i++ {
		injected++
		d := start.AddDate(0, 0, int(rnd.NextIntBound(int32(days))))
		processor = append(processor, Rec{"UNKNOWN" + strconv.Itoa(i), d, d.AddDate(0, 0, 1), OfMinor(999, c), Processor})
	}
	SortFeed(internal)
	SortFeed(processor)
	return Feeds{internal, processor, injected}
}

// SortFeed orders a feed by BucketKey, then ref (stable, like List.sort).
func SortFeed(feed []Rec) {
	keys := make([]BucketKey, len(feed))
	for i, r := range feed {
		keys[i] = r.Bucket()
	}
	idx := make([]int, len(feed))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		if c := keys[idx[a]].Compare(keys[idx[b]]); c != 0 {
			return c < 0
		}
		return feed[idx[a]].Ref < feed[idx[b]].Ref
	})
	sorted := make([]Rec, len(feed))
	for i, j := range idx {
		sorted[i] = feed[j]
	}
	copy(feed, sorted)
}

func mustReport(r *ReconciliationReport, err error) *ReconciliationReport {
	if err != nil {
		panic(err)
	}
	return r
}

func main() {
	asOf := Date(2026, 3, 31)

	fmt.Println("== A small run you can read ==")
	m28, m29, m1 := Date(2026, 3, 28), Date(2026, 3, 29), Date(2026, 3, 1)
	internal := []Rec{
		{"A1", m28, m28, OfMinor(1999, USD), Internal},
		{"A2", m28, m28, OfMinor(1999, USD), Internal},
		{"B1", m28, m28, OfMinor(5000, USD), Internal},
		{"C1", m29, m29, OfMinor(1250, USD), Internal},
		{"D1", m1, m1, OfMinor(700, USD), Internal},
	}
	processor := []Rec{
		{"A1", m28, m29, OfMinor(1999, USD), Processor}, // settled T+1: fine
		{"A2", m28, m29, OfMinor(1999, USD), Processor},
		{"B1", m28, Date(2026, 4, 3), OfMinor(5000, USD), Processor}, // settled T+6: TIMING
		{"C1", m29, m29, OfMinor(1200, USD), Processor},              // 12.00 vs 12.50: AMOUNT_MISMATCH
		{"E9", m29, m29, OfMinor(4200, USD), Processor},              // we never saw it: MISSING_INTERNAL
	}
	SortFeed(internal)
	SortFeed(processor)
	rec, _ := NewStreamingReconciler(USD, 2)
	small := mustReport(rec.Reconcile(SliceIterator(internal), SliceIterator(processor), asOf))
	fmt.Print(small.Summary())
	for _, b := range small.Breaks {
		fmt.Println("  " + b.String())
	}

	fmt.Println("\n== 1,000,000 records, seeded, 0.1% injected breaks ==")
	t0 := time.Now()
	feeds := FeedGenerator.Generate(42, 500_000, Date(2026, 3, 1), 30, USD, 0.001)
	t1 := time.Now()
	big := mustReport(rec.Reconcile(SliceIterator(feeds.Internal), SliceIterator(feeds.Processor), asOf))
	t2 := time.Now()
	fmt.Printf("generated %s + %s records in %d ms (%d break situations injected)\n",
		commas(int64(len(feeds.Internal))), commas(int64(len(feeds.Processor))), t1.Sub(t0).Milliseconds(), feeds.InjectedBreaks)
	fmt.Printf("reconciled in %d ms\n", t2.Sub(t1).Milliseconds())
	fmt.Print(big.Summary())
	fmt.Println("first three breaks: ")
	for _, b := range big.Breaks[:min(3, len(big.Breaks))] {
		fmt.Println("  " + b.String())
	}

	fmt.Println("\n== An unsorted feed is refused, not silently mis-reconciled ==")
	shuffled := append([]Rec{}, internal...)
	shuffled[0], shuffled[len(shuffled)-1] = shuffled[len(shuffled)-1], shuffled[0]
	if _, err := rec.Reconcile(SliceIterator(shuffled), SliceIterator(processor), asOf); errors.Is(err, ErrUnsortedFeed) {
		fmt.Println("rejected: " + strings.TrimPrefix(err.Error(), ErrUnsortedFeed.Error()+": "))
	}
	if !big.TiesOut() || !small.TiesOut() {
		fmt.Println("TIE-OUT FAILED")
		panic("tie-out failed")
	}
}
