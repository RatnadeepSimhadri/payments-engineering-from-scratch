// Lesson: phases/03-systems-data-structures/05-lsm-trees/docs/en.md
// LSM Trees: Memtables, SSTables, and Compaction
// Run:  go run .
// Test: go test ./...
// Sources: O'Neil, Cheng, Gawlick, O'Neil (1996), "The Log-Structured Merge-Tree"; Chang et al. (2006), "Bigtable";
// RocksDB wiki (Compaction, Bloom Filter); Apache Cassandra docs (SizeTieredCompactionStrategy). Bloom (1970).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIllegalState is returned where the Java throws IllegalStateException (immutable files, broken invariants).
	ErrIllegalState = errors.New("illegal state")
)

// Entry is a value or a tombstone, stamped with the sequence number of the write that produced it. Higher seq wins.
type Entry struct {
	Value     string
	Tombstone bool
	Seq       int64
}

func EntryValue(v string, seq int64) Entry { return Entry{Value: v, Seq: seq} }
func EntryTombstone(seq int64) Entry       { return Entry{Tombstone: true, Seq: seq} }

// ---------------------------------------------------------------- Hashing ----

// FNV-1a and a 64-bit mixer, as in Lessons 01 and 02. uint64 so the multiplies wrap like a Java long.
func Fnv1a64(key string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, b := range []byte(key) {
		h ^= uint64(b)
		h *= 0x100000001b3
	}
	return h
}

func Mix64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// ---------------------------------------------------------------- BloomFilter ----

// BloomFilter sized for ~1% false positives: 10 bits per key, 7 hashes, double hashing.
type BloomFilter struct {
	words  []uint64
	bits   uint64
	hashes int
}

func NewBloomFilter(expectedKeys int) *BloomFilter {
	bits := max(uint64(64), uint64(expectedKeys)*10)
	return &BloomFilter{words: make([]uint64, (bits+63)/64), bits: bits, hashes: 7}
}

func (b *BloomFilter) index(h1, h2 uint64, i int) uint64 { return (h1 + uint64(i)*h2) % b.bits }

func (b *BloomFilter) Add(key string) {
	h1 := Mix64(Fnv1a64(key))
	h2 := Mix64(h1)
	for i := 0; i < b.hashes; i++ {
		bit := b.index(h1, h2, i)
		b.words[bit>>6] |= 1 << (bit & 63)
	}
}

func (b *BloomFilter) MightContain(key string) bool {
	h1 := Mix64(Fnv1a64(key))
	h2 := Mix64(h1)
	for i := 0; i < b.hashes; i++ {
		bit := b.index(h1, h2, i)
		if b.words[bit>>6]&(1<<(bit&63)) == 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- SimulatedDisk ----

// SimulatedDisk is a directory of immutable byte-array files with I/O counters. Stands in for the file system.
type SimulatedDisk struct {
	names                                               []string // insertion order, like the Java LinkedHashMap
	files                                               map[string][]byte
	BytesWritten, BytesRead, FilesWritten, FilesDeleted int64
}

func NewSimulatedDisk() *SimulatedDisk { return &SimulatedDisk{files: map[string][]byte{}} }

func (d *SimulatedDisk) Write(name string, data []byte) error {
	if _, exists := d.files[name]; exists {
		return fmt.Errorf("%w: files are immutable: %s", ErrIllegalState, name)
	}
	d.names = append(d.names, name)
	d.files[name] = slices.Clone(data)
	d.BytesWritten += int64(len(data))
	d.FilesWritten++
	return nil
}

func (d *SimulatedDisk) Read(name string) ([]byte, error) {
	data, ok := d.files[name]
	if !ok {
		return nil, fmt.Errorf("%w: no such file: %s", ErrIllegalState, name)
	}
	d.BytesRead += int64(len(data))
	return slices.Clone(data), nil
}

func (d *SimulatedDisk) Delete(name string) {
	if _, ok := d.files[name]; ok {
		delete(d.files, name)
		d.names = slices.DeleteFunc(d.names, func(n string) bool { return n == name })
		d.FilesDeleted++
	}
}

func (d *SimulatedDisk) FileCount() int { return len(d.files) }

func (d *SimulatedDisk) TotalBytes() int64 {
	t := int64(0)
	for _, f := range d.files {
		t += int64(len(f))
	}
	return t
}

// ---------------------------------------------------------------- Memtable ----

// Memtable is the in-memory write buffer: a sorted map, so a flush is a sequential scan that produces a sorted run.
type Memtable struct {
	m                *SortedMap
	approximateBytes int64
}

func NewMemtable() *Memtable { return &Memtable{m: NewSortedMap()} }

func (mt *Memtable) Put(key string, entry Entry) {
	old, had := mt.m.Put(key, entry)
	mt.approximateBytes += int64(len(key) + len(entry.Value) + 16)
	if had {
		mt.approximateBytes -= int64(len(key) + len(old.Value) + 16)
	}
}

func (mt *Memtable) Get(key string) (Entry, bool) { return mt.m.Get(key) }
func (mt *Memtable) Size() int                    { return mt.m.Len() }
func (mt *Memtable) ApproximateBytes() int64      { return mt.approximateBytes }
func (mt *Memtable) IsEmpty() bool                { return mt.m.IsEmpty() }
func (mt *Memtable) Entries() *SortedMap          { return mt.m }

// ---------------------------------------------------------------- DataOutput encoding ----

// writeUTF is DataOutputStream.writeUTF: a 2-byte big-endian length followed by *modified* UTF-8
// (NUL as C0 80, characters outside the BMP as two 3-byte surrogates), so the files are byte-identical to Java's.
func writeUTF(buf *bytes.Buffer, s string) error {
	var enc []byte
	for _, c := range utf16.Encode([]rune(s)) {
		switch {
		case c >= 0x0001 && c <= 0x007F:
			enc = append(enc, byte(c))
		case c <= 0x07FF:
			enc = append(enc, byte(0xC0|(c>>6)), byte(0x80|(c&0x3F)))
		default:
			enc = append(enc, byte(0xE0|(c>>12)), byte(0x80|((c>>6)&0x3F)), byte(0x80|(c&0x3F)))
		}
	}
	if len(enc) > 0xFFFF {
		return fmt.Errorf("%w: encoded string too long: %d bytes", ErrIllegalState, len(enc))
	}
	buf.Write(binary.BigEndian.AppendUint16(nil, uint16(len(enc))))
	buf.Write(enc)
	return nil
}

func readUTF(r *bytes.Reader) (string, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "", err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	enc := make([]byte, n)
	if _, err := io.ReadFull(r, enc); err != nil { // a zero-length read at EOF is fine: the empty string
		return "", err
	}
	var units []uint16
	for i := 0; i < n; {
		c := enc[i]
		switch {
		case c>>4 <= 7:
			units = append(units, uint16(c))
			i++
		case c>>4 == 12 || c>>4 == 13:
			units = append(units, uint16(c&0x1F)<<6|uint16(enc[i+1]&0x3F))
			i += 2
		default:
			units = append(units, uint16(c&0x0F)<<12|uint16(enc[i+1]&0x3F)<<6|uint16(enc[i+2]&0x3F))
			i += 3
		}
	}
	return string(utf16.Decode(units)), nil
}

// ---------------------------------------------------------------- SSTable ----

const BlockEntries = 16

// SSTable: an immutable sorted run on disk with a Bloom filter and a sparse index in memory.
// Lookup: Bloom says no -> skip the file; else binary search the sparse index, then scan one block.
type SSTable struct {
	FileName                                   string
	EntryCount                                 int
	Bytes                                      int64
	MinKey, MaxKey                             string
	hasKeys                                    bool
	bloom                                      *BloomFilter
	sparseKeys                                 []string // first key of every block
	sparseOffsets                              []int    // byte offset of every block
	disk                                       *SimulatedDisk
	BloomNegatives, RangeNegatives, BlockReads int64
}

// WriteSSTable writes a sorted sequence of entries as one file and returns the handle.
func WriteSSTable(disk *SimulatedDisk, fileName string, sorted *SortedMap) (*SSTable, error) {
	var buf bytes.Buffer
	bloom := NewBloomFilter(sorted.Len())
	var sparseKeys []string
	var sparseOffsets []int
	buf.Write(binary.BigEndian.AppendUint32(nil, uint32(sorted.Len())))
	for i, k := range sorted.Keys() {
		e, _ := sorted.Get(k)
		if i%BlockEntries == 0 {
			sparseKeys = append(sparseKeys, k)
			sparseOffsets = append(sparseOffsets, buf.Len())
		}
		if err := writeUTF(&buf, k); err != nil {
			return nil, err
		}
		buf.Write(binary.BigEndian.AppendUint64(nil, uint64(e.Seq)))
		if e.Tombstone {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
		value := e.Value
		if e.Tombstone {
			value = ""
		}
		if err := writeUTF(&buf, value); err != nil {
			return nil, err
		}
		bloom.Add(k)
	}
	data := buf.Bytes()
	if err := disk.Write(fileName, data); err != nil {
		return nil, err
	}
	t := &SSTable{FileName: fileName, disk: disk, EntryCount: sorted.Len(), Bytes: int64(len(data)), bloom: bloom,
		sparseKeys: sparseKeys, sparseOffsets: sparseOffsets}
	if !sorted.IsEmpty() {
		t.MinKey, t.MaxKey, t.hasKeys = sorted.FirstKey(), sorted.LastKey(), true
	}
	return t, nil
}

// Get is the point lookup: Bloom, then key range, then one block. ok is false when the key is not in this table.
func (t *SSTable) Get(key string) (Entry, bool) {
	if !t.hasKeys || key < t.MinKey || key > t.MaxKey {
		t.RangeNegatives++
		return Entry{}, false
	}
	if !t.bloom.MightContain(key) {
		t.BloomNegatives++
		return Entry{}, false
	}
	block := t.ReadBlock(t.BlockFor(key))
	for _, k := range block.Keys() {
		if k == key {
			e, _ := block.Get(k)
			return e, true
		}
		if k > key {
			break
		}
	}
	return Entry{}, false
}

// BlockFor is the index of the last sparse key <= key (binary search over the in-memory index).
func (t *SSTable) BlockFor(key string) int {
	lo, hi := 0, len(t.sparseKeys)-1
	for lo < hi {
		mid := int(uint(lo+hi+1) >> 1)
		if t.sparseKeys[mid] <= key {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// ReadBlock decodes one block of up to BlockEntries entries starting at the block's offset.
// A missing or truncated file is a broken invariant of the simulated disk, so it panics as the Java throws.
func (t *SSTable) ReadBlock(block int) *SortedMap {
	t.BlockReads++
	data, err := t.disk.Read(t.FileName)
	if err != nil {
		panic(err)
	}
	r := bytes.NewReader(data[t.sparseOffsets[block]:])
	remaining := min(BlockEntries, t.EntryCount-block*BlockEntries)
	out := NewSortedMap()
	for i := 0; i < remaining; i++ {
		k, err := readUTF(r)
		if err != nil {
			panic(err)
		}
		var fixed [9]byte
		if _, err := io.ReadFull(r, fixed[:]); err != nil {
			panic(err)
		}
		seq := int64(binary.BigEndian.Uint64(fixed[:8]))
		tomb := fixed[8] != 0
		v, err := readUTF(r)
		if err != nil {
			panic(err)
		}
		if tomb {
			out.Put(k, EntryTombstone(seq))
		} else {
			out.Put(k, EntryValue(v, seq))
		}
	}
	return out
}

// ReadAll returns every entry, in key order. Used by compaction and range scans.
func (t *SSTable) ReadAll() *SortedMap {
	out := NewSortedMap()
	for b := range t.sparseKeys {
		out.PutAll(t.ReadBlock(b))
	}
	return out
}

func (t *SSTable) BlockCount() int { return len(t.sparseKeys) }

// ---------------------------------------------------------------- LsmTree ----

// LsmTree: one mutable memtable, a list of immutable SSTables, and size-tiered compaction.
// Writes are O(log n) in memory and sequential on disk; reads consult the memtable then each table until a hit.
type LsmTree struct {
	Disk                                                                          *SimulatedDisk
	MemtableLimit                                                                 int // entries before a flush
	TierThreshold                                                                 int // tables of similar size that trigger a compaction
	memtable                                                                      *Memtable
	tables                                                                        []*SSTable // every on-disk run; seq numbers, not position, decide recency
	seq, nextFileID                                                               int64
	Flushes, Compactions, BytesCompacted, UserBytesWritten, TablesConsulted, Gets int64
}

func NewLsmTree(disk *SimulatedDisk, memtableLimit, tierThreshold int) (*LsmTree, error) {
	if memtableLimit <= 0 || tierThreshold < 2 {
		return nil, fmt.Errorf("%w: bad limits", ErrInvalidArgument)
	}
	return &LsmTree{Disk: disk, MemtableLimit: memtableLimit, TierThreshold: tierThreshold, memtable: NewMemtable()}, nil
}

// Put writes a value. Go strings cannot be null, so there is no "null value" path: Delete is the only way to write a tombstone.
func (l *LsmTree) Put(key, value string) {
	l.UserBytesWritten += int64(len(key) + len(value))
	l.seq++
	l.memtable.Put(key, EntryValue(value, l.seq))
	l.maybeFlush()
}

func (l *LsmTree) Delete(key string) {
	l.UserBytesWritten += int64(len(key))
	l.seq++
	l.memtable.Put(key, EntryTombstone(l.seq)) // a delete is a write
	l.maybeFlush()
}

func (l *LsmTree) maybeFlush() {
	if l.memtable.Size() >= l.MemtableLimit {
		l.Flush()
	}
}

func (l *LsmTree) nextFileName() string {
	name := "sst-" + strconv.FormatInt(l.nextFileID, 10) + ".db"
	l.nextFileID++
	return name
}

// Flush freezes the memtable, writes it as a new SSTable, starts an empty memtable, then checks for compaction.
func (l *LsmTree) Flush() {
	if l.memtable.IsEmpty() {
		return
	}
	t, err := WriteSSTable(l.Disk, l.nextFileName(), l.memtable.Entries())
	if err != nil {
		panic(err) // the Java wraps the IOException in IllegalStateException: a disk that refuses a fresh file name is broken
	}
	l.tables = append(l.tables, t)
	l.memtable = NewMemtable()
	l.Flushes++
	l.maybeCompact()
}

// Get is the read path: the memtable answers outright. Otherwise every table whose Bloom filter and key range admit
// the key is consulted and the entry with the highest seq wins. Size-tiered compaction merges tables of
// similar size regardless of age, so file order alone cannot say which copy is newest; seq can.
func (l *LsmTree) Get(key string) (string, bool) {
	l.Gets++
	if e, ok := l.memtable.Get(key); ok {
		if e.Tombstone {
			return "", false
		}
		return e.Value, true
	}
	var best Entry
	found := false
	for _, t := range l.tables {
		l.TablesConsulted++
		if e, ok := t.Get(key); ok && (!found || e.Seq > best.Seq) {
			best, found = e, true
		}
	}
	if !found || best.Tombstone {
		return "", false
	}
	return best.Value, true
}

// maybeCompact is size-tiered: group tables whose entry counts are less than twice the smallest in the group.
// When any tier has TierThreshold tables, merge them into one. Repeat until no tier is full.
func (l *LsmTree) maybeCompact() {
	for {
		tier := l.fullTier()
		if tier == nil {
			return
		}
		l.compact(tier)
	}
}

func (l *LsmTree) fullTier() []*SSTable {
	sorted := slices.Clone(l.tables)
	slices.SortStableFunc(sorted, func(a, b *SSTable) int { return a.EntryCount - b.EntryCount })
	for i := range sorted {
		var tier []*SSTable
		base := max(1, sorted[i].EntryCount)
		for j := i; j < len(sorted) && int64(sorted[j].EntryCount) < 2*int64(base); j++ {
			tier = append(tier, sorted[j])
		}
		if len(tier) >= l.TierThreshold {
			return tier
		}
	}
	return nil
}

// cursor is one table's position in the k-way merge.
type cursor struct {
	keys []string
	src  *SortedMap
	pos  int
}

func (c *cursor) key() string  { return c.keys[c.pos] }
func (c *cursor) entry() Entry { e, _ := c.src.Get(c.keys[c.pos]); return e }

// cursorHeap orders by key, then newest seq first on ties: the PriorityQueue comparator from the Java.
type cursorHeap []*cursor

func (h cursorHeap) Len() int { return len(h) }
func (h cursorHeap) Less(i, j int) bool {
	if h[i].key() != h[j].key() {
		return h[i].key() < h[j].key()
	}
	return h[i].entry().Seq > h[j].entry().Seq
}
func (h cursorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *cursorHeap) Push(x any)   { *h = append(*h, x.(*cursor)) }
func (h *cursorHeap) Pop() any     { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

// compact is the k-way merge of the chosen tables. For each key the highest seq wins. A tombstone can be dropped only
// when no table outside this compaction could still hold an older value for its key; the simple safe
// rule used here is: drop tombstones only when every table is being compacted.
func (l *LsmTree) compact(chosen []*SSTable) {
	everything := len(chosen) == len(l.tables)
	h := &cursorHeap{}
	for _, t := range chosen {
		l.BytesCompacted += t.Bytes
		all := t.ReadAll()
		if !all.IsEmpty() {
			heap.Push(h, &cursor{keys: all.Keys(), src: all})
		}
	}
	merged := NewSortedMap()
	lastKey, haveLast := "", false
	for h.Len() > 0 {
		c := heap.Pop(h).(*cursor)
		k := c.key()
		if !haveLast || k != lastKey { // first occurrence of k is the newest
			lastKey, haveLast = k, true
			e := c.entry()
			if !(e.Tombstone && everything) {
				merged.Put(k, e)
			}
		}
		c.pos++
		if c.pos < len(c.keys) {
			heap.Push(h, c)
		}
	}
	for _, t := range chosen {
		l.tables = slices.DeleteFunc(l.tables, func(x *SSTable) bool { return x == t })
		l.Disk.Delete(t.FileName)
	}
	if !merged.IsEmpty() {
		t, err := WriteSSTable(l.Disk, l.nextFileName(), merged)
		if err != nil {
			panic(err)
		}
		l.tables = append(l.tables, t)
	}
	l.Compactions++
}

// KV is one live key/value pair of a range scan (the Java returns a NavigableMap; Go returns the pairs in key order).
type KV struct{ Key, Value string }

// Range returns keys in [lo, hi] with their live values: merge of memtable and every table, highest seq wins, tombstones hidden.
func (l *LsmTree) Range(lo, hi string) []KV {
	merged := NewSortedMap()
	newest := func(k string, e Entry) {
		if old, had := merged.Get(k); !had || e.Seq >= old.Seq {
			merged.Put(k, e)
		}
	}
	for _, t := range l.tables {
		sub := t.ReadAll().SubMap(lo, hi)
		for _, k := range sub.Keys() {
			e, _ := sub.Get(k)
			newest(k, e)
		}
	}
	sub := l.memtable.Entries().SubMap(lo, hi)
	for _, k := range sub.Keys() {
		e, _ := sub.Get(k)
		newest(k, e)
	}
	out := []KV{}
	for _, k := range merged.Keys() {
		e, _ := merged.Get(k)
		if !e.Tombstone {
			out = append(out, KV{k, e.Value})
		}
	}
	return out
}

func (l *LsmTree) TableCount() int    { return len(l.tables) }
func (l *LsmTree) Tables() []*SSTable { return slices.Clone(l.tables) }
func (l *LsmTree) MemtableSize() int  { return l.memtable.Size() }

// WriteAmplification is bytes written to disk divided by bytes the user wrote: the cost of compaction.
func (l *LsmTree) WriteAmplification() float64 {
	if l.UserBytesWritten == 0 {
		return 0
	}
	return float64(l.Disk.BytesWritten) / float64(l.UserBytesWritten)
}

func (l *LsmTree) BloomNegatives() int64 {
	n := int64(0)
	for _, t := range l.tables {
		n += t.BloomNegatives
	}
	return n
}

func (l *LsmTree) RangeNegatives() int64 {
	n := int64(0)
	for _, t := range l.tables {
		n += t.RangeNegatives
	}
	return n
}

func (l *LsmTree) BlockReads() int64 {
	n := int64(0)
	for _, t := range l.tables {
		n += t.BlockReads
	}
	return n
}

// CheckInvariants: no empty tables; no tier at or above the threshold after compaction; disk holds exactly the live tables.
func (l *LsmTree) CheckInvariants() error {
	for _, t := range l.tables {
		if t.EntryCount == 0 {
			return fmt.Errorf("%w: empty table %s", ErrIllegalState, t.FileName)
		}
	}
	if l.fullTier() != nil {
		return fmt.Errorf("%w: a tier is still full after compaction", ErrIllegalState)
	}
	if l.Disk.FileCount() != len(l.tables) {
		return fmt.Errorf("%w: disk files %d != tables %d", ErrIllegalState, l.Disk.FileCount(), len(l.tables))
	}
	return nil
}

// ---------------------------------------------------------------- Demo ----

// commas formats like Java's %,d.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(ch)
	}
	return out.String()
}

func keysOf(kvs []KV) string {
	parts := make([]string, len(kvs))
	for i, kv := range kvs {
		parts[i] = kv.Key
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// orNull prints a missing value the way Java prints null.
func orNull(v string, ok bool) string {
	if !ok {
		return "null"
	}
	return v
}

func main() {
	rng := NewJavaRandom(42)
	disk := NewSimulatedDisk()
	lsm, _ := NewLsmTree(disk, 1_000, 4)

	fmt.Println("== Payment status store: 20,000 payments, each updated three times ==")
	for _, state := range []string{"created", "authorized", "captured"} {
		for i := 0; i < 20_000; i++ {
			lsm.Put(fmt.Sprintf("pay_%06d", i), state+"@"+strconv.Itoa(int(rng.NextIntN(1_000_000))))
		}
	}
	fmt.Printf("flushes=%d compactions=%d tables=%d memtable=%d entries\n",
		lsm.Flushes, lsm.Compactions, lsm.TableCount(), lsm.MemtableSize())
	fmt.Printf("disk: %d files, %s bytes; user wrote %s bytes; write amplification %.2fx\n",
		disk.FileCount(), commas(disk.TotalBytes()), commas(lsm.UserBytesWritten), lsm.WriteAmplification())
	sizes := []string{}
	for _, t := range lsm.Tables() {
		sizes = append(sizes, strconv.Itoa(t.EntryCount))
	}
	fmt.Println("table sizes (entries): [" + strings.Join(sizes, ", ") + "]")

	fmt.Println()
	fmt.Println("== Read path: highest seq wins, Bloom filters skip the rest ==")
	v, ok := lsm.Get("pay_000042")
	fmt.Println("pay_000042 -> " + orNull(v, ok) + "  (the third write; earlier versions are shadowed)")
	before, blocksBefore := lsm.TablesConsulted, lsm.BlockReads()
	for i := 0; i < 1_000; i++ {
		lsm.Get(fmt.Sprintf("pay_%06dx", i)) // never written
	}
	fmt.Printf("1000 misses consulted %d tables: %d rejected by key range, %d by Bloom, %d block reads (false positives)\n",
		lsm.TablesConsulted-before, lsm.RangeNegatives(), lsm.BloomNegatives(), lsm.BlockReads()-blocksBefore)

	fmt.Println()
	fmt.Println("== Deletes are writes: tombstones ==")
	lsm.Delete("pay_000042")
	v, ok = lsm.Get("pay_000042")
	fmt.Println("after delete, pay_000042 -> " + orNull(v, ok) + " (tombstone in the memtable shadows the table)")
	lsm.Flush()
	v, ok = lsm.Get("pay_000042")
	fmt.Println("after flush, pay_000042 -> " + orNull(v, ok) + " (tombstone now in the newest table)")

	fmt.Println()
	fmt.Println("== Range scan merges every level ==")
	fmt.Println("pay_000040..pay_000044 -> " + keysOf(lsm.Range("pay_000040", "pay_000044")) + "  (42 is gone)")

	fmt.Println()
	fmt.Println("== Forcing compaction down to one table drops the tombstone ==")
	disk2 := NewSimulatedDisk()
	small, _ := NewLsmTree(disk2, 4, 2)
	small.Put("a", "1")
	small.Put("b", "2")
	small.Put("c", "3")
	small.Put("d", "4") // flush 1
	small.Delete("b")
	small.Put("e", "5")
	small.Put("f", "6")
	small.Put("g", "7") // flush 2 -> compaction
	b, okB := small.Get("b")
	fmt.Printf("tables=%d, keys=%s, b -> %s\n", small.TableCount(), keysOf(small.Range("a", "z")), orNull(b, okB))
	fmt.Printf("files written=%d deleted=%d (compaction rewrote, then removed the inputs)\n", disk2.FilesWritten, disk2.FilesDeleted)
}
