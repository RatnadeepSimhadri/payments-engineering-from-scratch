// Tests for phases/08-messaging-and-streams/01-the-append-only-log. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

var big = LogConfig{SegmentBytes: 1 << 20, IndexIntervalBytes: 4096, RetentionBytes: 1 << 30}

func newPartition(t *testing.T, id int, config LogConfig) *Partition {
	t.Helper()
	p, err := NewPartition(id, config, NewInMemoryStorage())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func appendN(t *testing.T, p *Partition, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := p.Append([]byte("k"), []byte("0123456789"), int64(i)); err != nil { // 39-byte frames
			t.Fatal(err)
		}
	}
}

func filledPartition(t *testing.T) *Partition {
	t.Helper()
	p := newPartition(t, 0, big)
	for i := 0; i < 50; i++ {
		if _, err := p.Append([]byte(fmt.Sprintf("k%d", i%3)), []byte(fmt.Sprintf("v%d", i)), int64(1000+i)); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestCodecRoundTripAndLayout(t *testing.T) {
	frame := Encode(42, 1234, []byte("k"), []byte("value"))
	if len(frame) != 4+24+1+5 || FrameSize([]byte("k"), []byte("value")) != len(frame) {
		t.Fatalf("frame size %d", len(frame))
	}
	rec, err := Decode(frame, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Offset != 42 || rec.Timestamp != 1234 || rec.KeyString() != "k" || rec.ValueString() != "value" {
		t.Fatalf("decoded %+v", rec)
	}
	if FrameLengthAt(frame, 0) != len(frame) {
		t.Fatal("FrameLengthAt mismatch")
	}
	nullKey := Encode(1, 1, nil, []byte("v"))
	nk, _ := Decode(nullKey, 0)
	if nk.Key != nil || len(nullKey) != 4+24+1 {
		t.Fatal("nil key must encode as -1 and decode as nil")
	}
}

func TestFnv1a32KnownVectors(t *testing.T) {
	if Fnv1a32(nil) != 0x811C9DC5 {
		t.Fatal("empty input must be the offset basis")
	}
	if Fnv1a32([]byte("a")) != 0xE40C292C {
		t.Fatalf("fnv1a32('a') = %x", Fnv1a32([]byte("a")))
	}
}

func TestOffsetsMonotonicAndContiguous(t *testing.T) {
	p := newPartition(t, 0, big)
	for i := 0; i < 50; i++ {
		off, err := p.Append([]byte("k"), []byte("v"), int64(i))
		if err != nil || off != int64(i) {
			t.Fatalf("append %d gave offset %d err %v", i, off, err)
		}
	}
	if p.LogStartOffset() != 0 || p.LogEndOffset() != 50 || p.SegmentCount() != 1 {
		t.Fatalf("start %d end %d segments %d", p.LogStartOffset(), p.LogEndOffset(), p.SegmentCount())
	}
}

func TestReadFromArbitraryOffset(t *testing.T) {
	p := filledPartition(t)
	mid, err := p.Read(17, 5)
	if err != nil || len(mid) != 5 || mid[0].Offset != 17 || mid[4].Offset != 21 {
		t.Fatalf("read(17,5) = %v err %v", mid, err)
	}
	if mid[0].ValueString() != "v17" || mid[0].Timestamp != 1017 {
		t.Fatalf("wrong record %+v", mid[0])
	}
	end, err := p.Read(50, 10)
	if err != nil || len(end) != 0 {
		t.Fatal("read at LogEndOffset must be empty, not an error")
	}
	if _, err := p.Read(51, 1); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatal("read above LogEndOffset must be out of range")
	}
	if _, err := p.Read(-1, 1); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatal("read below LogStartOffset must be out of range")
	}
}

func TestSparseIndexAddressesEveryOffset(t *testing.T) {
	data, _ := NewInMemoryStorage().Create(0, 100)
	seg := NewSegment(100, data, 1<<20, 100)
	for i := 0; i < 40; i++ {
		if _, err := seg.Append([]byte("key"), []byte(fmt.Sprintf("payload-%d", i)), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	entries := seg.IndexEntries()
	if len(entries) < 2 || len(entries) >= 40 {
		t.Fatalf("index should be sparse, has %d entries", len(entries))
	}
	if entries[0] != (IndexEntry{0, 0}) {
		t.Fatalf("first entry %+v", entries[0])
	}
	for off := int64(100); off < 140; off++ {
		one, err := seg.Read(off, 1)
		if err != nil || len(one) != 1 || one[0].Offset != off || one[0].ValueString() != fmt.Sprintf("payload-%d", off-100) {
			t.Fatalf("offset %d not addressable: %v %v", off, one, err)
		}
	}
}

func TestSegmentsRollAtSizeLimit(t *testing.T) {
	p := newPartition(t, 1, LogConfig{120, 40, 10_000})
	appendN(t, p, 10)
	if p.SegmentCount() != 4 || !reflect.DeepEqual(p.SegmentBaseOffsets(), []int64{0, 3, 6, 9}) {
		t.Fatalf("segments %v", p.SegmentBaseOffsets())
	}
	across, err := p.Read(2, 4)
	if err != nil || len(across) != 4 || across[0].Offset != 2 || across[3].Offset != 5 {
		t.Fatalf("read across boundary = %v err %v", across, err)
	}
	last, _ := p.Read(9, 10)
	if len(last) != 1 {
		t.Fatal("read into the last partial segment")
	}
}

func TestOversizedRecordLandsInEmptySegment(t *testing.T) {
	p := newPartition(t, 2, LogConfig{50, 10, 10_000})
	off, err := p.Append([]byte("k"), []byte("this value is much longer than the fifty byte segment limit"), 1)
	if err != nil || off != 0 {
		t.Fatal(err)
	}
	if got, _ := p.Read(0, 1); len(got) != 1 {
		t.Fatal("oversized record must be readable")
	}
}

func TestStorageTracksSegmentsAndRetentionDeletesThroughIt(t *testing.T) {
	storage := NewInMemoryStorage()
	p, err := NewPartition(7, LogConfig{120, 40, 250}, storage)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, p, 10)
	if !reflect.DeepEqual(storage.BaseOffsets(7), []int64{0, 3, 6, 9}) {
		t.Fatalf("storage %v", storage.BaseOffsets(7))
	}
	if len(storage.BaseOffsets(8)) != 0 {
		t.Fatal("storage must be keyed by partition")
	}
	before := p.SizeBytes()
	dropped := p.EnforceRetention()
	if dropped != 2 || p.SegmentCount() != 2 || p.SizeBytes() >= before || p.SizeBytes() > 250 {
		t.Fatalf("dropped %d segments %d bytes %d", dropped, p.SegmentCount(), p.SizeBytes())
	}
	if p.LogStartOffset() != 6 || p.LogEndOffset() != 10 {
		t.Fatalf("start %d end %d", p.LogStartOffset(), p.LogEndOffset())
	}
	if !reflect.DeepEqual(storage.BaseOffsets(7), []int64{6, 9}) {
		t.Fatalf("retention must delete from storage: %v", storage.BaseOffsets(7))
	}
	if _, err := p.Read(2, 1); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatal("retired offset must be out of range")
	}
	if got, _ := p.Read(6, 1); got[0].Offset != 6 {
		t.Fatal("surviving records intact")
	}
	retired := NewOffsetStore()
	_ = retired.Commit("g4", 7, 2)
	if NewConsumer(p, "g4", retired).Position() != 6 {
		t.Fatal("a committed offset retired by retention must reset to LogStartOffset")
	}
}

func TestActiveSegmentNeverDropped(t *testing.T) {
	p := newPartition(t, 4, LogConfig{50, 10, 10})
	appendN(t, p, 2)
	p.EnforceRetention()
	if p.SegmentCount() != 1 || p.LogEndOffset() != 2 {
		t.Fatal("active segment must survive retention")
	}
}

func TestSameKeySamePartitionInOrder(t *testing.T) {
	topic, err := NewTopic("t", 4, big, NewInMemoryStorage())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"alice", "bob", "carol", "dave", "erin"}
	homes := map[string]map[int]bool{}
	for i := 0; i < 100; i++ {
		k := keys[i%len(keys)]
		meta, err := topic.Append([]byte(k), []byte(fmt.Sprintf("v%d", i)), int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if meta.Partition != topic.PartitionFor([]byte(k)) {
			t.Fatal("Append must use PartitionFor")
		}
		if homes[k] == nil {
			homes[k] = map[int]bool{}
		}
		homes[k][meta.Partition] = true
	}
	for k, set := range homes {
		if len(set) != 1 {
			t.Fatalf("key %s landed in %d partitions", k, len(set))
		}
	}
	all, _ := topic.Partition(topic.PartitionFor([]byte("alice"))).Read(0, 1000)
	last := -1
	for _, r := range all {
		if r.KeyString() == "alice" {
			n, _ := strconv.Atoi(r.ValueString()[1:])
			if n <= last {
				t.Fatal("per-key order violated")
			}
			last = n
		}
	}
	if last != 95 {
		t.Fatalf("last alice value %d", last)
	}
	if topic.PartitionFor([]byte("bob")) != topic.PartitionFor([]byte("bob")) {
		t.Fatal("PartitionFor must be pure")
	}
	if p := topic.PartitionFor([]byte("anything")); p < 0 || p >= 4 {
		t.Fatalf("partition %d out of range", p)
	}
}

func TestNilKeysRoundRobin(t *testing.T) {
	topic, _ := NewTopic("u", 3, big, NewInMemoryStorage())
	got := []int{topic.PartitionFor(nil), topic.PartitionFor(nil), topic.PartitionFor(nil), topic.PartitionFor(nil)}
	if !reflect.DeepEqual(got, []int{0, 1, 2, 0}) {
		t.Fatalf("round robin %v", got)
	}
}

func TestConsumerPositionSemantics(t *testing.T) {
	p := filledPartition(t)
	c := NewConsumer(p, "g1", NewOffsetStore())
	if c.Position() != 0 {
		t.Fatal("a group with no commit starts at LogStartOffset")
	}
	first, _ := c.Poll(10)
	if len(first) != 10 || c.Position() != 10 {
		t.Fatalf("poll advanced to %d", c.Position())
	}
	if err := c.Seek(45); err != nil {
		t.Fatal(err)
	}
	if rest, _ := c.Poll(10); len(rest) != 5 || c.Position() != 50 {
		t.Fatal("seek then poll must stop at the end")
	}
	if empty, _ := c.Poll(10); len(empty) != 0 || c.Position() != 50 {
		t.Fatal("poll at the end is empty and does not move")
	}
	if err := c.Seek(99); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatal("seek outside the log must be rejected")
	}
}

func TestCommitThenResumeRedeliversUncommitted(t *testing.T) {
	p := filledPartition(t)
	offsets := NewOffsetStore()
	worker := NewConsumer(p, "g2", offsets)
	worker.Poll(10)
	if err := worker.Commit(); err != nil {
		t.Fatal(err)
	}
	if offsets.Committed("g2", 0) != 10 {
		t.Fatal("commit stores the next offset to read")
	}
	worker.Poll(5)
	if worker.Position() != 15 || offsets.Committed("g2", 0) != 10 {
		t.Fatal("polling past the commit must not move the committed offset")
	}
	resumed := NewConsumer(p, "g2", offsets)
	batch, _ := resumed.Poll(5)
	if resumed.Position() != 15 || batch[0].Offset != 10 {
		t.Fatal("a restarted consumer resumes at the committed offset and redelivers 10..14")
	}
	if NewConsumer(p, "g3", offsets).Position() != 0 {
		t.Fatal("another group is unaffected")
	}
	if offsets.Committed("nobody", 0) != NoCommit {
		t.Fatal("unknown group has no commit")
	}
	if err := offsets.Commit("g", 0, -5); err == nil {
		t.Fatal("negative commit must be rejected")
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := NewLogConfig(0, 1, 1); err == nil {
		t.Fatal("segmentBytes 0 must be rejected")
	}
	if _, err := NewTopic("t", 0, big, NewInMemoryStorage()); err == nil {
		t.Fatal("zero partitions must be rejected")
	}
}
