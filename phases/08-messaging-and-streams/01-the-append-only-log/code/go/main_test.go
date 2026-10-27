// Tests for phases/08-messaging-and-streams/01-the-append-only-log. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

func b(s string) []byte { return []byte(s) }

func cfg(t *testing.T, segmentBytes, indexIntervalBytes int, retentionBytes int64) LogConfig {
	t.Helper()
	c, err := NewLogConfig(segmentBytes, indexIntervalBytes, retentionBytes)
	if err != nil {
		t.Fatalf("NewLogConfig: %v", err)
	}
	return c
}

func partition(t *testing.T, id int, config LogConfig) *Partition {
	t.Helper()
	return NewPartition(id, config, NewInMemoryStorage())
}

func readOK(t *testing.T, p *Partition, from int64, max int) []StoredRecord {
	t.Helper()
	out, err := p.Read(from, max)
	if err != nil {
		t.Fatalf("Read(%d, %d): %v", from, max, err)
	}
	return out
}

func pollOK(t *testing.T, c *Consumer, max int) []StoredRecord {
	t.Helper()
	out, err := c.Poll(max)
	if err != nil {
		t.Fatalf("Poll(%d): %v", max, err)
	}
	return out
}

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkOutOfRange(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrOffsetOutOfRange) {
		t.Errorf("%s: got %v, want ErrOffsetOutOfRange", name, err)
	}
}

func bigConfig(t *testing.T) LogConfig { return cfg(t, 1<<20, 4096, 1<<30) }

func fiftyRecords(t *testing.T) *Partition {
	t.Helper()
	p := partition(t, 0, bigConfig(t))
	for i := 0; i < 50; i++ {
		p.Append(b("k"+strconv.Itoa(i%3)), b("v"+strconv.Itoa(i)), int64(1000+i))
	}
	return p
}

func TestCodecRoundTripAndFrameLayout(t *testing.T) {
	frame := Encode(42, 1234, b("k"), b("value"))
	check(t, "frame size = 4 + 24 + key + value", len(frame) == 4+24+1+5 && FrameSize(b("k"), b("value")) == len(frame))
	decoded, err := Decode(frame, 0)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "decode offset", decoded.Offset == 42)
	check(t, "decode timestamp", decoded.Timestamp == 1234)
	check(t, "decode key and value", decoded.KeyString() == "k" && decoded.ValueString() == "value")
	nullKey := Encode(1, 1, nil, b("v"))
	nk, _ := Decode(nullKey, 0)
	check(t, "null key encodes as -1 and decodes as nil", nk.Key == nil && len(nullKey) == 4+24+1)
	check(t, "frameLengthAt matches encoded length", FrameLengthAt(frame, 0) == len(frame))
	check(t, "fnv1a32 of empty is offset basis", Fnv1a32([]byte{}) == 0x811C9DC5)
	check(t, "fnv1a32('a') known vector", Fnv1a32(b("a")) == 0xE40C292C)
	if _, err := Decode([]byte{0, 0, 0, 1, 0, 0, 0, 0}, 0); !errors.Is(err, ErrCorruptFrame) {
		t.Errorf("short frame should be corrupt, got %v", err)
	}
}

func TestOffsetsMonotonicAndContiguous(t *testing.T) {
	p := partition(t, 0, bigConfig(t))
	previous := int64(-1)
	contiguous := true
	for i := 0; i < 50; i++ {
		off := p.Append(b("k"+strconv.Itoa(i%3)), b("v"+strconv.Itoa(i)), int64(1000+i))
		if off != previous+1 {
			contiguous = false
		}
		previous = off
	}
	check(t, "offsets start at 0 and increase by exactly one", contiguous && previous == 49)
	check(t, "logEndOffset is next offset to assign", p.LogEndOffset() == 50 && p.LogStartOffset() == 0)
	check(t, "single large segment, no roll", p.SegmentCount() == 1)
}

func TestReadFromArbitraryOffset(t *testing.T) {
	p := fiftyRecords(t)
	mid := readOK(t, p, 17, 5)
	check(t, "read(17, 5) returns offsets 17..21", len(mid) == 5 && mid[0].Offset == 17 && mid[4].Offset == 21)
	check(t, "read returns the stored value", mid[0].ValueString() == "v17" && mid[0].Timestamp == 1017)
	check(t, "read at logEndOffset is empty", len(readOK(t, p, 50, 10)) == 0)
	_, err := p.Read(51, 1)
	checkOutOfRange(t, "read above logEndOffset is out of range", err)
	_, err = p.Read(-1, 1)
	checkOutOfRange(t, "read below logStartOffset is out of range", err)
}

func TestSparseIndex(t *testing.T) {
	seg := NewSegment(100, NewInMemoryStorage().Create(0, 100), 1<<20, 100)
	for i := 0; i < 40; i++ {
		if _, err := seg.Append(b("key"), b("payload-"+strconv.Itoa(i)), int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "sparse index has fewer entries than records", len(seg.IndexEntries()) < 40 && len(seg.IndexEntries()) >= 2)
	check(t, "first index entry is relative offset 0 at position 0", seg.IndexEntries()[0] == IndexEntry{0, 0})
	everyOffsetReadable := true
	for off := int64(100); off < 140; off++ {
		one, err := seg.Read(off, 1)
		if err != nil || len(one) != 1 || one[0].Offset != off || one[0].ValueString() != "payload-"+strconv.FormatInt(off-100, 10) {
			everyOffsetReadable = false
		}
	}
	check(t, "every offset is found through the sparse index", everyOffsetReadable)
	_, err := seg.PositionOf(140)
	checkOutOfRange(t, "positionOf past the segment end is out of range", err)
}

func TestSegmentRoll(t *testing.T) {
	rolled := partition(t, 1, cfg(t, 120, 40, 10_000))
	for i := 0; i < 10; i++ {
		rolled.Append(b("k"), b("0123456789"), int64(i)) // frame = 4+24+1+10 = 39 bytes; 3 per segment
	}
	check(t, "segments roll at the size limit", rolled.SegmentCount() == 4)
	check(t, "segment base offsets are 0,3,6,9", reflect.DeepEqual(rolled.SegmentBaseOffsets(), []int64{0, 3, 6, 9}))
	across := readOK(t, rolled, 2, 4)
	check(t, "read crosses segment boundaries", len(across) == 4 && across[0].Offset == 2 && across[3].Offset == 5)
	check(t, "read into the last partial segment", len(readOK(t, rolled, 9, 10)) == 1)
	oversized := partition(t, 2, cfg(t, 50, 10, 10_000))
	bigOffset := oversized.Append(b("k"), b("this value is much longer than the fifty byte segment limit"), 1)
	check(t, "an oversized record still lands in an empty segment", bigOffset == 0 && len(readOK(t, oversized, 0, 1)) == 1)
	full := NewSegment(0, NewInMemoryStorage().Create(0, 0), 50, 10)
	full.Append(b("k"), b("0123456789"), 1)
	_, err := full.Append(b("k"), b("0123456789"), 2)
	check(t, "a non-empty segment without room reports ErrSegmentFull", errors.Is(err, ErrSegmentFull))
}

func TestStorageAndRetention(t *testing.T) {
	storage := NewInMemoryStorage()
	stored := NewPartition(7, cfg(t, 120, 40, 250), storage)
	for i := 0; i < 10; i++ {
		stored.Append(b("k"), b("0123456789"), int64(i))
	}
	check(t, "storage holds one entry per segment", reflect.DeepEqual(storage.BaseOffsets(7), []int64{0, 3, 6, 9}))
	check(t, "storage is keyed by partition", len(storage.BaseOffsets(8)) == 0)

	bytesBefore := stored.SizeBytes()
	dropped := stored.EnforceRetention()
	check(t, "retention removed oldest segments", dropped == 2 && stored.SegmentCount() == 2 && stored.SizeBytes() < bytesBefore && stored.SizeBytes() <= 250)
	check(t, "logStartOffset advanced to the surviving segment", stored.LogStartOffset() == 6 && stored.LogEndOffset() == 10)
	check(t, "retention deleted the segments from storage", reflect.DeepEqual(storage.BaseOffsets(7), []int64{6, 9}))
	_, err := stored.Read(2, 1)
	checkOutOfRange(t, "reading a retired offset is out of range", err)
	check(t, "surviving records are intact", readOK(t, stored, 6, 1)[0].Offset == 6)
	tiny := partition(t, 4, cfg(t, 50, 10, 10))
	tiny.Append(b("k"), b("0123456789"), 1)
	tiny.Append(b("k"), b("0123456789"), 2)
	tiny.EnforceRetention()
	check(t, "active segment is never dropped", tiny.SegmentCount() == 1 && tiny.LogEndOffset() == 2)

	retired := NewOffsetStore()
	if err := retired.Commit("g4", 7, 2); err != nil {
		t.Fatal(err)
	}
	check(t, "a committed offset retired by retention resets to logStartOffset", NewConsumer(stored, "g4", retired).Position() == 6)
}

func TestKeyBasedPartitioning(t *testing.T) {
	topic, err := NewTopic("t", 4, bigConfig(t), NewInMemoryStorage())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"alice", "bob", "carol", "dave", "erin"}
	seen := map[string][]int{}
	stable := true
	for i := 0; i < 100; i++ {
		k := keys[i%len(keys)]
		m := topic.Append(b(k), b("v"+strconv.Itoa(i)), int64(i))
		if m.Partition != topic.PartitionFor(b(k)) {
			stable = false
		}
		seen[k] = append(seen[k], m.Partition)
	}
	oneHomeEachKey := true
	for _, list := range seen {
		for _, p := range list {
			if p != list[0] {
				oneHomeEachKey = false
			}
		}
	}
	check(t, "every key maps to exactly one partition", stable && oneHomeEachKey)
	home := topic.PartitionFor(b("alice"))
	all := readOK(t, topic.Partition(home), 0, 1000)
	ordered := true
	lastAlice := -1
	for _, rec := range all {
		if rec.KeyString() == "alice" {
			n, _ := strconv.Atoi(rec.ValueString()[1:])
			if n <= lastAlice {
				ordered = false
			}
			lastAlice = n
		}
	}
	check(t, "records for one key are in production order within its partition", ordered && lastAlice == 95)
	check(t, "partitionFor is a pure function of the key", topic.PartitionFor(b("bob")) == topic.PartitionFor(b("bob")))
	check(t, "partitionFor is within range", topic.PartitionFor(b("anything")) >= 0 && topic.PartitionFor(b("anything")) < 4)
	unkeyed, _ := NewTopic("u", 3, bigConfig(t), NewInMemoryStorage())
	rr := []int{unkeyed.PartitionFor(nil), unkeyed.PartitionFor(nil), unkeyed.PartitionFor(nil), unkeyed.PartitionFor(nil)}
	check(t, "null keys rotate round-robin", reflect.DeepEqual(rr, []int{0, 1, 2, 0}))
	if _, err := NewTopic("bad", 0, bigConfig(t), NewInMemoryStorage()); err == nil {
		t.Error("a topic needs at least one partition")
	}
}

func TestConsumerPositionSemantics(t *testing.T) {
	p := fiftyRecords(t)
	offsets := NewOffsetStore()
	c := NewConsumer(p, "g1", offsets)
	check(t, "a group with no commit starts at logStartOffset", c.Position() == 0)
	first := pollOK(t, c, 10)
	check(t, "poll advances position past the last delivered record", len(first) == 10 && c.Position() == 10)
	if err := c.SeekTo(45); err != nil {
		t.Fatal(err)
	}
	check(t, "seek then poll reads from the new position and stops at the end", len(pollOK(t, c, 10)) == 5 && c.Position() == 50)
	check(t, "poll at the end is empty and does not move", len(pollOK(t, c, 10)) == 0 && c.Position() == 50)
	checkOutOfRange(t, "seek outside the log is rejected", c.SeekTo(99))
}

func TestCommitAndResume(t *testing.T) {
	p := fiftyRecords(t)
	offsets := NewOffsetStore()
	worker := NewConsumer(p, "g2", offsets)
	pollOK(t, worker, 10)
	if err := worker.Commit(); err != nil {
		t.Fatal(err)
	}
	check(t, "commit stores the next offset to read", offsets.Committed("g2", 0) == 10)
	pollOK(t, worker, 5)
	check(t, "polling past the commit does not move the committed offset", worker.Position() == 15 && offsets.Committed("g2", 0) == 10)
	resumed := NewConsumer(p, "g2", offsets)
	check(t, "a restarted consumer resumes at the committed offset, redelivering 10..14", resumed.Position() == 10 && pollOK(t, resumed, 5)[0].Offset == 10)
	check(t, "another group is unaffected", NewConsumer(p, "g3", offsets).Position() == 0)
	check(t, "no commit for an unknown group", offsets.Committed("nobody", 0) == None)
	check(t, "negative commits are rejected", offsets.Commit("g2", 0, -1) != nil)
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		seg, idx int
		ret      int64
	}{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d_%d_%d", c.seg, c.idx, c.ret), func(t *testing.T) {
			if _, err := NewLogConfig(c.seg, c.idx, c.ret); err == nil {
				t.Error("config rejects non-positive sizes")
			}
		})
	}
}
