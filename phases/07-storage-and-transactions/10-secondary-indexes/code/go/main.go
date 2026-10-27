// Lesson: phases/07-storage-and-transactions/10-secondary-indexes/docs/en.md
// Secondary Indexes: Local vs Global over a partitioned payment store.
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only. Hashing: FNV-1a 64-bit (Fowler/Noll/Vo, IETF draft-eastlake-fnv).
// Design references: DynamoDB LSI/GSI documentation; Kleppmann, Designing Data-Intensive Applications, ch. 6.
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
	ErrCurrencyMismatch = errors.New("cannot combine currencies")
	ErrOverflow         = errors.New("int64 overflow")
	ErrMissingField     = errors.New("payment field must not be empty")
	ErrNoShards         = errors.New("need at least one shard")
	ErrNoIndexPartition = errors.New("need at least one index partition")
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

// Payment is one row of the primary table. The primary key is ID; CustomerID is the indexed attribute.
type Payment struct {
	ID         string
	CustomerID string
	MerchantID string
	Amount     Money
	CreatedAt  int64
}

// NewPayment is the validating constructor (Java's record compact constructor): no field may be empty.
func NewPayment(id, customerID, merchantID string, amount Money, createdAt int64) (Payment, error) {
	for name, v := range map[string]string{"id": id, "customerId": customerID, "merchantId": merchantID} {
		if v == "" {
			return Payment{}, fmt.Errorf("%w: %s", ErrMissingField, name)
		}
	}
	return Payment{id, customerID, merchantID, amount, createdAt}, nil
}

// Locator says where a row lives: which shard, and its primary key there. A global index entry points at one of these.
type Locator struct {
	ShardID   int
	PaymentID string
}

func (l Locator) Less(o Locator) bool {
	if l.ShardID != o.ShardID {
		return l.ShardID < o.ShardID
	}
	return l.PaymentID < o.PaymentID
}

// IndexMode says how the global index is kept in step with the primary table.
type IndexMode int

const (
	// Sync: the index partition is written inside the same request as the row. Costs an extra remote write per put.
	Sync IndexMode = iota
	// Async: the index change is queued and applied later. Puts are cheap; the index lags by the queue depth.
	Async
)

func (m IndexMode) String() string { return [...]string{"SYNC", "ASYNC"}[m] }

// IndexUpdate is one change to the global index. Delete=false inserts the locator under the customer; Delete=true removes it.
type IndexUpdate struct {
	Seq        int64
	CustomerID string
	Locator    Locator
	Delete     bool
}

// QueryResult is the answer to a query plus what it cost: how many shard requests and index-partition requests were made.
type QueryResult struct {
	Payments      []Payment
	ShardRequests int
	IndexRequests int
}

func (r QueryResult) TotalRequests() int { return r.ShardRequests + r.IndexRequests }

// IndexAudit is the result of checking the global index against the primary table.
type IndexAudit struct {
	Entries  int
	Dangling []Locator
	Missing  []Locator
}

func (a IndexAudit) Consistent() bool { return len(a.Dangling) == 0 && len(a.Missing) == 0 }

// ---- Hashing: deterministic 64-bit FNV-1a over UTF-8 bytes, so every language port routes a key to the same shard ----

const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

// Fnv1a64 returns the hash as a signed int64, the same bits Java's long holds.
func Fnv1a64(key string) int64 {
	h := fnvOffset
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= fnvPrime
	}
	return int64(h)
}

// floorMod is Java's Math.floorMod: the result has the sign of the divisor, so a negative hash still lands in [0, n).
func floorMod(x int64, n int64) int {
	r := x % n
	if r < 0 {
		r += n
	}
	return int(r)
}

func PartitionFor(key string, partitions int) int {
	return floorMod(Fnv1a64(key), int64(partitions))
}

// Shard is one shard of the primary table. It owns its rows and a LOCAL secondary index (customerId -> ids) that is
// updated in the same write as the row, so within a shard the index is never stale. Every method call that a client
// would make over the network increments requests.
type Shard struct {
	ID         int
	rows       map[string]Payment
	byCustomer map[string]map[string]bool
	requests   int
}

func NewShard(id int) *Shard {
	return &Shard{ID: id, rows: map[string]Payment{}, byCustomer: map[string]map[string]bool{}}
}

func (s *Shard) Requests() int { return s.requests }
func (s *Shard) Size() int     { return len(s.rows) }

// Put inserts or replaces. Maintains the local index atomically with the row (single-threaded, same call).
// Returns the previous row and whether there was one.
func (s *Shard) Put(p Payment) (Payment, bool) {
	s.requests++
	previous, had := s.rows[p.ID]
	s.rows[p.ID] = p
	if had && previous.CustomerID != p.CustomerID {
		s.unindex(previous)
	}
	if s.byCustomer[p.CustomerID] == nil {
		s.byCustomer[p.CustomerID] = map[string]bool{}
	}
	s.byCustomer[p.CustomerID][p.ID] = true
	return previous, had
}

func (s *Shard) Get(paymentID string) (Payment, bool) {
	s.requests++
	p, ok := s.rows[paymentID]
	return p, ok
}

// GetMany fetches several rows in one request: the batch-get a client would issue to one node. Missing ids are skipped.
func (s *Shard) GetMany(ids []string) []Payment {
	s.requests++
	out := []Payment{}
	for _, id := range ids {
		if p, ok := s.rows[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *Shard) Delete(paymentID string) (Payment, bool) {
	s.requests++
	removed, ok := s.rows[paymentID]
	if ok {
		delete(s.rows, paymentID)
		s.unindex(removed)
	}
	return removed, ok
}

// LookupCustomer is the local index lookup: only rows that live on this shard, in id order.
func (s *Shard) LookupCustomer(customerID string) []Payment {
	s.requests++
	out := []Payment{}
	for _, id := range sortedKeys(s.byCustomer[customerID]) {
		out = append(out, s.rows[id])
	}
	return out
}

// Scan is a full scan in id order, used only to bootstrap a global index.
func (s *Shard) Scan() []Payment {
	s.requests++
	out := make([]Payment, 0, len(s.rows))
	for _, id := range sortedKeys(s.rows) {
		out = append(out, s.rows[id])
	}
	return out
}

func (s *Shard) unindex(p Payment) {
	if ids := s.byCustomer[p.CustomerID]; ids != nil {
		delete(ids, p.ID)
		if len(ids) == 0 {
			delete(s.byCustomer, p.CustomerID)
		}
	}
}

// GlobalIndex is a GLOBAL secondary index, partitioned by the indexed attribute (customerId), not by the primary key.
// All locators for one customer live in one index partition, so a customer query touches exactly one.
type GlobalIndex struct {
	partitions []map[string]map[Locator]bool
	requests   []int
}

func NewGlobalIndex(partitionCount int) (*GlobalIndex, error) {
	if partitionCount <= 0 {
		return nil, ErrNoIndexPartition
	}
	g := &GlobalIndex{requests: make([]int, partitionCount)}
	for i := 0; i < partitionCount; i++ {
		g.partitions = append(g.partitions, map[string]map[Locator]bool{})
	}
	return g, nil
}

func (g *GlobalIndex) PartitionCount() int { return len(g.partitions) }
func (g *GlobalIndex) PartitionFor(customerID string) int {
	return PartitionFor(customerID, len(g.partitions))
}
func (g *GlobalIndex) Requests(partition int) int { return g.requests[partition] }

func (g *GlobalIndex) TotalRequests() int {
	sum := 0
	for _, r := range g.requests {
		sum += r
	}
	return sum
}

func (g *GlobalIndex) Apply(u IndexUpdate) {
	p := g.PartitionFor(u.CustomerID)
	g.requests[p]++
	part := g.partitions[p]
	if u.Delete {
		if set := part[u.CustomerID]; set != nil {
			delete(set, u.Locator)
			if len(set) == 0 {
				delete(part, u.CustomerID)
			}
		}
	} else {
		if part[u.CustomerID] == nil {
			part[u.CustomerID] = map[Locator]bool{}
		}
		part[u.CustomerID][u.Locator] = true
	}
}

// Lookup is one request to one partition. Returns locators in (ShardID, PaymentID) order.
func (g *GlobalIndex) Lookup(customerID string) []Locator {
	p := g.PartitionFor(customerID)
	g.requests[p]++
	return sortedLocators(g.partitions[p][customerID])
}

func (g *GlobalIndex) EntryCount() int {
	n := 0
	for _, part := range g.partitions {
		for _, set := range part {
			n += len(set)
		}
	}
	return n
}

// AllEntries returns every (locator -> customer) pair in the index, for auditing.
func (g *GlobalIndex) AllEntries() map[Locator]string {
	out := map[Locator]string{}
	for _, part := range g.partitions {
		for customer, set := range part {
			for l := range set {
				out[l] = customer
			}
		}
	}
	return out
}

func (g *GlobalIndex) Clear() {
	for i := range g.partitions {
		g.partitions[i] = map[string]map[Locator]bool{}
	}
}

// PartitionedStore is the partitioned payment store: N shards keyed by payment id, each with a local index, plus one
// global index partitioned by customer id. Exposes both query strategies so their costs can be compared on the same data.
type PartitionedStore struct {
	shards      []*Shard
	globalIndex *GlobalIndex
	mode        IndexMode
	pending     []IndexUpdate
	nextSeq     int64
}

func NewPartitionedStore(shardCount, indexPartitions int, mode IndexMode) (*PartitionedStore, error) {
	if shardCount <= 0 {
		return nil, ErrNoShards
	}
	gi, err := NewGlobalIndex(indexPartitions)
	if err != nil {
		return nil, err
	}
	s := &PartitionedStore{globalIndex: gi, mode: mode, nextSeq: 1}
	for i := 0; i < shardCount; i++ {
		s.shards = append(s.shards, NewShard(i))
	}
	return s, nil
}

func (s *PartitionedStore) ShardCount() int           { return len(s.shards) }
func (s *PartitionedStore) Shard(id int) *Shard       { return s.shards[id] }
func (s *PartitionedStore) GlobalIndex() *GlobalIndex { return s.globalIndex }
func (s *PartitionedStore) Mode() IndexMode           { return s.mode }
func (s *PartitionedStore) PendingIndexUpdates() int  { return len(s.pending) }

func (s *PartitionedStore) ShardFor(paymentID string) int {
	return PartitionFor(paymentID, len(s.shards))
}

func (s *PartitionedStore) TotalShardRequests() int {
	n := 0
	for _, sh := range s.shards {
		n += sh.Requests()
	}
	return n
}

func (s *PartitionedStore) newUpdate(customerID string, l Locator, del bool) IndexUpdate {
	u := IndexUpdate{s.nextSeq, customerID, l, del}
	s.nextSeq++
	return u
}

// Put is the write path. One shard write always; the global index write is inline (Sync) or queued (Async).
func (s *PartitionedStore) Put(p Payment) {
	shardID := s.ShardFor(p.ID)
	previous, had := s.shards[shardID].Put(p)
	if had && previous.CustomerID != p.CustomerID {
		s.emit(s.newUpdate(previous.CustomerID, Locator{shardID, p.ID}, true))
	}
	if !had || previous.CustomerID != p.CustomerID {
		s.emit(s.newUpdate(p.CustomerID, Locator{shardID, p.ID}, false))
	}
}

func (s *PartitionedStore) Delete(paymentID string) bool {
	shardID := s.ShardFor(paymentID)
	removed, ok := s.shards[shardID].Delete(paymentID)
	if !ok {
		return false
	}
	s.emit(s.newUpdate(removed.CustomerID, Locator{shardID, paymentID}, true))
	return true
}

func (s *PartitionedStore) emit(u IndexUpdate) {
	if s.mode == Sync {
		s.globalIndex.Apply(u)
	} else {
		s.pending = append(s.pending, u)
	}
}

// DrainIndexUpdates applies up to max queued index updates in order. Returns how many were applied.
func (s *PartitionedStore) DrainIndexUpdates(max int) int {
	applied := 0
	for applied < max && len(s.pending) > 0 {
		s.globalIndex.Apply(s.pending[0])
		s.pending = s.pending[1:]
		applied++
	}
	return applied
}

func (s *PartitionedStore) DrainAll() int { return s.DrainIndexUpdates(math.MaxInt) }

// Get is a primary-key lookup: exactly one shard request.
func (s *PartitionedStore) Get(paymentID string) (Payment, bool) {
	return s.shards[s.ShardFor(paymentID)].Get(paymentID)
}

func sortPayments(ps []Payment) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].CreatedAt != ps[j].CreatedAt {
			return ps[i].CreatedAt < ps[j].CreatedAt
		}
		return ps[i].ID < ps[j].ID
	})
}

// FindByCustomerScatterGather is strategy A: ask every shard's local index and merge. Cost is always N shard requests.
func (s *PartitionedStore) FindByCustomerScatterGather(customerID string) QueryResult {
	merged := []Payment{}
	for _, sh := range s.shards {
		merged = append(merged, sh.LookupCustomer(customerID)...)
	}
	sortPayments(merged)
	return QueryResult{merged, len(s.shards), 0}
}

// FindByCustomerGlobal is strategy B: one index-partition request, then one batch request per distinct shard that holds
// a hit. Locators whose row has vanished (an index entry that outlived its row) are filtered out, never returned.
func (s *PartitionedStore) FindByCustomerGlobal(customerID string) QueryResult {
	locators := s.globalIndex.Lookup(customerID)
	shardOrder := []int{} // LinkedHashMap: first-seen order of shards (locators are sorted, so ascending)
	byShard := map[int][]string{}
	for _, l := range locators {
		if _, seen := byShard[l.ShardID]; !seen {
			shardOrder = append(shardOrder, l.ShardID)
		}
		byShard[l.ShardID] = append(byShard[l.ShardID], l.PaymentID)
	}
	merged := []Payment{}
	for _, shardID := range shardOrder {
		merged = append(merged, s.shards[shardID].GetMany(byShard[shardID])...)
	}
	sortPayments(merged)
	return QueryResult{merged, len(shardOrder), 1}
}

// RebuildGlobalIndex is bootstrap or repair: throw the index away and rebuild it from a scan of every shard.
func (s *PartitionedStore) RebuildGlobalIndex() int {
	s.globalIndex.Clear()
	s.pending = nil
	entries := 0
	for _, sh := range s.shards {
		for _, p := range sh.Scan() {
			s.globalIndex.Apply(s.newUpdate(p.CustomerID, Locator{sh.ID, p.ID}, false))
			entries++
		}
	}
	return entries
}

// AuditGlobalIndex compares index against primary table. Dangling = in index, row gone or re-attributed. Missing = row exists, no entry.
func (s *PartitionedStore) AuditGlobalIndex() IndexAudit {
	indexed := s.globalIndex.AllEntries()
	dangling, missing := []Locator{}, []Locator{}
	for _, l := range sortedLocatorKeys(indexed) {
		row, ok := s.shards[l.ShardID].Get(l.PaymentID)
		if !ok || row.CustomerID != indexed[l] {
			dangling = append(dangling, l)
		}
	}
	for _, sh := range s.shards {
		for _, p := range sh.Scan() {
			l := Locator{sh.ID, p.ID}
			if p.CustomerID != indexed[l] {
				missing = append(missing, l)
			}
		}
	}
	return IndexAudit{len(indexed), dangling, missing}
}

// ---- ordering helpers (TreeMap / TreeSet) ----

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedLocators(set map[Locator]bool) []Locator {
	out := make([]Locator, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

func sortedLocatorKeys[V any](m map[Locator]V) []Locator {
	out := make([]Locator, 0, len(m))
	for l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// ---- worked example ----

func SampleData() []Payment {
	spec := [][5]string{
		{"pay_001", "cust_alice", "m_books", "1999", "1"},
		{"pay_002", "cust_bob", "m_books", "4500", "2"},
		{"pay_003", "cust_alice", "m_coffee", "475", "3"},
		{"pay_004", "cust_carol", "m_coffee", "650", "4"},
		{"pay_005", "cust_alice", "m_rides", "1250", "5"},
		{"pay_006", "cust_bob", "m_rides", "2300", "6"},
		{"pay_007", "cust_dave", "m_books", "9999", "7"},
		{"pay_008", "cust_alice", "m_grocer", "8210", "8"},
		{"pay_009", "cust_carol", "m_books", "1500", "9"},
		{"pay_010", "cust_erin", "m_coffee", "525", "10"},
		{"pay_011", "cust_alice", "m_coffee", "480", "11"},
		{"pay_012", "cust_bob", "m_grocer", "6100", "12"},
	}
	rows := make([]Payment, 0, len(spec))
	for _, s := range spec {
		cents, _ := strconv.ParseInt(s[3], 10, 64)
		at, _ := strconv.ParseInt(s[4], 10, 64)
		p, err := NewPayment(s[0], s[1], s[2], OfMinor(cents, USD), at)
		if err != nil {
			panic(err)
		}
		rows = append(rows, p)
	}
	return rows
}

func Total(payments []Payment) (Money, error) {
	sum := OfMinor(0, USD)
	for _, p := range payments {
		var err error
		if sum, err = sum.Plus(p.Amount); err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

// placement prints {shard=[ids], ...} the way Java's TreeMap<Integer, List<String>> does.
func placement(store *PartitionedStore, customerID string) string {
	parts := []string{}
	for i := 0; i < store.ShardCount(); i++ {
		ids := []string{}
		for _, p := range store.Shard(i).LookupCustomer(customerID) {
			ids = append(ids, p.ID)
		}
		if len(ids) > 0 {
			parts = append(parts, fmt.Sprintf("%d=[%s]", i, strings.Join(ids, ", ")))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func paymentsEqual(a, b []Payment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func main() {
	fmt.Println("== Placement: payments hash to shards by payment id, not by customer ==")
	sync := must(NewPartitionedStore(4, 2, Sync))
	for _, p := range SampleData() {
		sync.Put(p)
	}
	syncShardWrites := sync.TotalShardRequests()
	syncIndexWrites := sync.GlobalIndex().TotalRequests()
	for i := 0; i < 4; i++ {
		fmt.Printf("shard %d holds %d rows\n", i, sync.Shard(i).Size())
	}
	fmt.Println("cust_alice rows by shard: " + placement(sync, "cust_alice"))
	fmt.Println("cust_erin rows by shard:  " + placement(sync, "cust_erin"))

	fmt.Println()
	fmt.Println("== Query: all payments for cust_alice ==")
	before := sync.TotalShardRequests()
	scatter := sync.FindByCustomerScatterGather("cust_alice")
	fmt.Printf("scatter-gather over local indexes: %d rows, %d shard requests, %d index requests, total %s\n",
		len(scatter.Payments), scatter.ShardRequests, scatter.IndexRequests, must(Total(scatter.Payments)))
	global := sync.FindByCustomerGlobal("cust_alice")
	fmt.Printf("global index then fetch:           %d rows, %d shard requests, %d index requests, total %s\n",
		len(global.Payments), global.ShardRequests, global.IndexRequests, must(Total(global.Payments)))
	fmt.Printf("same answer: %t; shard requests consumed by both queries: %d\n",
		paymentsEqual(scatter.Payments, global.Payments), sync.TotalShardRequests()-before)

	fmt.Println()
	fmt.Println("== Query: cust_erin, a customer with a single payment ==")
	scatter = sync.FindByCustomerScatterGather("cust_erin")
	global = sync.FindByCustomerGlobal("cust_erin")
	fmt.Printf("scatter-gather: %d requests; global: %d requests (1 index + %d shard)\n",
		scatter.TotalRequests(), global.TotalRequests(), global.ShardRequests)

	fmt.Println()
	fmt.Println("== Write cost: SYNC index pays on every put ==")
	fmt.Printf("SYNC store after 12 puts: shard requests %d, index-partition requests %d\n", syncShardWrites, syncIndexWrites)
	async := must(NewPartitionedStore(4, 2, Async))
	for _, p := range SampleData() {
		async.Put(p)
	}
	fmt.Printf("ASYNC store after 12 puts: shard requests %d, index-partition requests %d, queued updates %d\n",
		async.TotalShardRequests(), async.GlobalIndex().TotalRequests(), async.PendingIndexUpdates())

	fmt.Println()
	fmt.Println("== Staleness window: the row is durable, the index has not heard yet ==")
	fresh := must(NewPayment("pay_013", "cust_erin", "m_rides", OfMinor(1800, USD), 13))
	async.DrainAll()
	async.Put(fresh)
	found := "missing"
	if _, ok := async.Get("pay_013"); ok {
		found = "found"
	}
	fmt.Printf("get(pay_013) by primary key: %s\n", found)
	fmt.Printf("global index query for cust_erin: %d rows (expected 2)  <- stale, %d update(s) queued\n",
		len(async.FindByCustomerGlobal("cust_erin").Payments), async.PendingIndexUpdates())
	fmt.Printf("scatter-gather for cust_erin:     %d rows (local indexes are never stale)\n",
		len(async.FindByCustomerScatterGather("cust_erin").Payments))
	async.DrainAll()
	fmt.Printf("after drain: global index query for cust_erin: %d rows\n", len(async.FindByCustomerGlobal("cust_erin").Payments))

	fmt.Println()
	fmt.Println("== Dangling entry: delete the row, index update still queued ==")
	async.Delete("pay_013")
	fmt.Printf("index entries for cust_erin: %d, pending updates: %d\n",
		len(async.GlobalIndex().Lookup("cust_erin")), async.PendingIndexUpdates())
	audit := async.AuditGlobalIndex()
	fmt.Printf("audit: %d entries, %d dangling, %d missing\n", audit.Entries, len(audit.Dangling), len(audit.Missing))
	fmt.Printf("query filters the ghost: %d rows returned for cust_erin\n", len(async.FindByCustomerGlobal("cust_erin").Payments))
	async.DrainAll()
	audit = async.AuditGlobalIndex()
	fmt.Printf("after drain: %d dangling, %d missing, consistent=%t\n", len(audit.Dangling), len(audit.Missing), audit.Consistent())

	fmt.Println()
	fmt.Println("== Bootstrap: rebuild the global index from a scan of every shard ==")
	rebuilt := async.RebuildGlobalIndex()
	fmt.Printf("rebuilt %d entries from %d shards; consistent=%t\n", rebuilt, async.ShardCount(), async.AuditGlobalIndex().Consistent())
}
