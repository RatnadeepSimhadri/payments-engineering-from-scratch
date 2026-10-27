// Tests for phases/03-systems-data-structures/05-lsm-trees. Run: go test ./...
package main

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"testing"
)

func newLsm(t *testing.T, disk *SimulatedDisk, limit, threshold int) *LsmTree {
	t.Helper()
	l, err := NewLsmTree(disk, limit, threshold)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// present is "get returned a value" and absent is Java's null.
func present(t *testing.T, l *LsmTree, key, want string) {
	t.Helper()
	if got, ok := l.Get(key); !ok || got != want {
		t.Errorf("get(%q) = %q, %v; want %q", key, got, ok, want)
	}
}

func absent(t *testing.T, l *LsmTree, key string) {
	t.Helper()
	if got, ok := l.Get(key); ok {
		t.Errorf("get(%q) = %q; want absent", key, got)
	}
}

func rangeKeys(kvs []KV) []string {
	out := []string{}
	for _, kv := range kvs {
		out = append(out, kv.Key)
	}
	return out
}

func TestMemtable(t *testing.T) {
	mem := NewMemtable()
	mem.Put("b", EntryValue("2", 1))
	mem.Put("a", EntryValue("1", 2))
	mem.Put("b", EntryValue("22", 3))
	if !slices.Equal(mem.Entries().Keys(), []string{"a", "b"}) {
		t.Error("memtable keeps keys sorted")
	}
	if e, ok := mem.Get("b"); mem.Size() != 2 || !ok || e.Value != "22" || e.Seq != 3 {
		t.Error("memtable overwrite keeps latest and size")
	}
}

func TestModifiedUTF8(t *testing.T) {
	var out bytes.Buffer
	if err := writeUTF(&out, "ké\u0000"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), []byte{0x00, 0x05, 'k', 0xc3, 0xa9, 0xc0, 0x80}) { // NUL is two bytes, as in DataOutputStream
		t.Errorf("writeUTF bytes = % x", out.Bytes())
	}
	back, err := readUTF(bytes.NewReader(out.Bytes()))
	if err != nil || back != "ké\u0000" {
		t.Errorf("readUTF round trip = %q, %v", back, err)
	}
}

func TestSSTable(t *testing.T) {
	disk := NewSimulatedDisk()
	run := NewSortedMap()
	for i := 0; i < 100; i++ {
		run.Put(fmt.Sprintf("k%03d", i), EntryValue("v"+strconv.Itoa(i), int64(i)))
	}
	run.Put("k050", EntryTombstone(500))
	sst, err := WriteSSTable(disk, "t0.db", run)
	if err != nil {
		t.Fatal(err)
	}
	if sst.EntryCount != 100 || sst.MinKey != "k000" || sst.MaxKey != "k099" || sst.BlockCount() != 7 {
		t.Error("sstable records count, range, and block count")
	}
	if e, ok := sst.Get("k007"); !ok || e.Value != "v7" || e.Seq != 7 {
		t.Error("sstable get decodes a value")
	}
	if e, ok := sst.Get("k050"); !ok || !e.Tombstone || e.Seq != 500 {
		t.Error("sstable get decodes a tombstone")
	}
	if e, ok := sst.Get("k099"); !ok || e.Value != "v99" {
		t.Error("sstable get on last key in last block")
	}
	blocksBefore := sst.BlockReads
	_, okZ := sst.Get("z")
	_, okA := sst.Get("a")
	if okZ || okA || sst.BlockReads != blocksBefore || sst.RangeNegatives != 2 {
		t.Error("sstable get outside range is absent without I/O")
	}
	for _, c := range []struct {
		key  string
		want int
	}{{"k000", 0}, {"k015", 0}, {"k016", 1}, {"k099", 6}} {
		if got := sst.BlockFor(c.key); got != c.want {
			t.Errorf("blockFor(%s) = %d, want %d (block whose first key <= key)", c.key, got, c.want)
		}
	}
	negatives, falsePositives := 0, 0
	for i := 0; i < 2_000; i++ {
		missing := fmt.Sprintf("k%03dz", i%100) // inside range, never written
		before := sst.BloomNegatives
		_, ok := sst.Get(missing)
		if sst.BloomNegatives > before {
			negatives++
		} else if !ok {
			falsePositives++
		}
	}
	if negatives <= 1_900 {
		t.Errorf("bloom filter rejects most absent keys without reading a block: %d", negatives)
	}
	if falsePositives >= 100 {
		t.Errorf("bloom false positives are rare (~1%%): %d", falsePositives)
	}
	if !sst.ReadAll().Equal(run) {
		t.Error("readAll round-trips every entry")
	}
	if err := disk.Write("t0.db", []byte{1}); !errors.Is(err, ErrIllegalState) {
		t.Errorf("sstable files are immutable: %v", err)
	}
}

func TestPutGetDeleteThroughFlushes(t *testing.T) {
	lsm := newLsm(t, NewSimulatedDisk(), 10, 3)
	for i := 0; i < 25; i++ {
		lsm.Put("key"+strconv.Itoa(i), "v"+strconv.Itoa(i))
	}
	if lsm.Flushes != 2 || lsm.MemtableSize() != 5 {
		t.Error("25 puts with limit 10 flush twice and leave 5 in the memtable")
	}
	present(t, lsm, "key24", "v24")
	present(t, lsm, "key3", "v3")
	present(t, lsm, "key12", "v12")
	absent(t, lsm, "nope")
	lsm.Put("key3", "new3")
	present(t, lsm, "key3", "new3") // newer write in memtable shadows older table value
	lsm.Delete("key12")
	absent(t, lsm, "key12") // tombstone in memtable hides table value
	lsm.Flush()
	absent(t, lsm, "key12") // tombstone persists across flush
	present(t, lsm, "key3", "new3")
	// Go has no null string, so the Java "null value is not a delete" check becomes: a Put never produces a tombstone.
	lsm.Put("k", "")
	if v, ok := lsm.Get("k"); !ok || v != "" {
		t.Error("an empty value is a value, not a delete")
	}
}

func TestSizeTieredCompaction(t *testing.T) {
	d3 := NewSimulatedDisk()
	tiered := newLsm(t, d3, 100, 4)
	rng := NewJavaRandom(5)
	model := map[string]string{}
	for i := 0; i < 5_000; i++ {
		k := "acct_" + strconv.Itoa(int(rng.NextIntN(800)))
		if rng.NextIntN(10) == 0 {
			tiered.Delete(k)
			delete(model, k)
		} else {
			v := "bal_" + strconv.Itoa(i)
			tiered.Put(k, v)
			model[k] = v
		}
	}
	tiered.Flush()
	if tiered.Compactions == 0 || tiered.CheckInvariants() != nil {
		t.Errorf("compaction ran and no tier is full afterwards: %v", tiered.CheckInvariants())
	}
	if int64(tiered.TableCount()) >= tiered.Flushes {
		t.Error("fewer tables than flushes after compaction")
	}
	for i := 0; i < 800; i++ {
		k := "acct_" + strconv.Itoa(i)
		expected, inModel := model[k]
		actual, ok := tiered.Get(k)
		if inModel != ok || expected != actual {
			t.Fatalf("every key agrees with the model after flushes and compactions (highest seq wins): %s", k)
		}
	}
	scanned := tiered.Range("acct_10", "acct_19")
	expectedScan := []KV{}
	for k, v := range model {
		if k >= "acct_10" && k <= "acct_19" {
			expectedScan = append(expectedScan, KV{k, v})
		}
	}
	sort.Slice(expectedScan, func(i, j int) bool { return expectedScan[i].Key < expectedScan[j].Key })
	if !slices.Equal(scanned, expectedScan) {
		t.Error("range scan merges levels and hides tombstones")
	}
	if tiered.WriteAmplification() <= 1.0 {
		t.Error("write amplification is above 1 (compaction rewrites data)")
	}
	if d3.FileCount() != tiered.TableCount() || d3.FilesDeleted == 0 {
		t.Error("disk holds exactly the live tables (inputs deleted)")
	}
}

func TestRecencyAcrossTiers(t *testing.T) {
	trap := newLsm(t, NewSimulatedDisk(), 3, 2)
	trap.Put("x", "old")
	trap.Put("p", "1")
	trap.Put("o", "1") // table A (3 entries)
	trap.Put("q", "1")
	trap.Put("r", "1")
	trap.Put("s", "1") // table B (3) -> same tier -> AB (6)
	if x, _ := trap.Get("x"); trap.TableCount() != 1 || x != "old" || trap.Tables()[0].EntryCount != 6 {
		t.Error("two same-size tables compacted into one")
	}
	trap.Put("x", "new")
	trap.Put("y", "1")
	trap.Put("z", "1") // table C (3): 6 is not < 2*3, so AB stays apart
	if trap.TableCount() != 2 || trap.CheckInvariants() != nil {
		t.Error("tables of different sizes are left in separate tiers")
	}
	present(t, trap, "x", "new") // newer value in the small table beats older value in the big table
	trap.Put("x", "newest")
	trap.Put("u", "1")
	trap.Put("v", "1") // table D (3) -> CD (5) -> joins AB's tier -> one table
	if x, _ := trap.Get("x"); trap.TableCount() != 1 || x != "newest" || trap.Tables()[0].EntryCount != 10 || trap.CheckInvariants() != nil {
		t.Error("cascading compaction keeps the highest-seq value")
	}
}

func TestTombstoneLifecycle(t *testing.T) {
	tomb := newLsm(t, NewSimulatedDisk(), 2, 2)
	tomb.Put("a", "1")
	tomb.Put("b", "2") // table 1
	tomb.Delete("a")
	tomb.Put("c", "3") // table 2 -> compaction of everything
	if _, ok := tomb.Get("a"); tomb.TableCount() != 1 || ok || tomb.Tables()[0].EntryCount != 2 {
		t.Error("full compaction drops the tombstone and the deleted key")
	}

	keep := newLsm(t, NewSimulatedDisk(), 2, 3)
	keep.Put("a", "1")
	keep.Put("b", "2") // table 1
	keep.Put("c", "3")
	keep.Put("d", "4") // table 2
	keep.Put("e", "5")
	keep.Put("f", "6") // table 3 -> compaction (all three)
	keep.Put("g", "7")
	keep.Put("h", "8") // table 4 (2 entries); the merged one has 6
	keep.Delete("a")
	keep.Put("i", "9") // table 5 (2 entries) -> tier of two 2-entry tables, threshold 3: no compaction
	if _, ok := keep.Get("a"); keep.TableCount() != 3 || ok {
		t.Error("partial state: tombstone lives in a small table while value lives in the big one")
	}
	keep.Put("j", "1")
	keep.Put("k", "1") // third small table -> compaction of the three small ones, not the big one
	tombstoneKept := false
	for _, tbl := range keep.Tables() {
		if e, ok := tbl.Get("a"); ok && e.Tombstone {
			tombstoneKept = true
		}
	}
	if _, ok := keep.Get("a"); !tombstoneKept || ok {
		t.Error("tombstone survives a compaction that excludes the table holding the old value")
	}
}

func TestLimits(t *testing.T) {
	if _, err := NewLsmTree(NewSimulatedDisk(), 0, 4); !errors.Is(err, ErrInvalidArgument) {
		t.Error("memtable limit must be positive")
	}
	if _, err := NewLsmTree(NewSimulatedDisk(), 10, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Error("tier threshold must be at least 2")
	}
}
