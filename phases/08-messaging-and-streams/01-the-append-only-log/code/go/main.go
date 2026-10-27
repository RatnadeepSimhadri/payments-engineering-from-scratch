// Lesson: phases/08-messaging-and-streams/01-the-append-only-log/docs/en.md
// The Append-Only Log: Partitions, Offsets, and Segments
// Run:  go run .
// Test: go test ./...
// Sources: Apache Kafka documentation, section 5.4 "Log" (segment files, sparse offset index, retention);
// Kreps, Narkhede, Rao, "Kafka: a Distributed Messaging System for Log Processing" (NetDB 2011).
// Go 1.22+, standard library only. Same design as the Java and Python versions: segments live behind an injected
// SegmentStorage; the in-memory one holds byte slices with the exact frame layout a file would hold.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Clock is the injected time source so demos and tests are deterministic.
type Clock interface {
	Now() int64
}

// TickClock advances one millisecond per call, starting at a fixed epoch.
type TickClock struct {
	next int64
}

func NewTickClock(startMillis int64) *TickClock { return &TickClock{next: startMillis} }

func (c *TickClock) Now() int64 {
	n := c.next
	c.next++
	return n
}

// LogConfig holds the sizing rules for every partition of a topic.
type LogConfig struct {
	SegmentBytes       int
	IndexIntervalBytes int
	RetentionBytes     int64
}

// NewLogConfig validates the sizing rules; Java's compact constructor throws where this returns an error.
func NewLogConfig(segmentBytes, indexIntervalBytes int, retentionBytes int64) (LogConfig, error) {
	if segmentBytes <= 0 {
		return LogConfig{}, errors.New("segmentBytes must be positive")
	}
	if indexIntervalBytes <= 0 {
		return LogConfig{}, errors.New("indexIntervalBytes must be positive")
	}
	if retentionBytes <= 0 {
		return LogConfig{}, errors.New("retentionBytes must be positive")
	}
	return LogConfig{segmentBytes, indexIntervalBytes, retentionBytes}, nil
}

// StoredRecord is what a consumer receives: the record plus the coordinates the log assigned to it.
// A nil Key means "no key"; an empty non-nil key is a zero-length key.
type StoredRecord struct {
	Offset    int64
	Timestamp int64
	Key       []byte
	Value     []byte
}

func (r StoredRecord) KeyString() string {
	if r.Key == nil {
		return "null"
	}
	return string(r.Key)
}

func (r StoredRecord) ValueString() string { return string(r.Value) }

// RecordMetadata says where a produced record landed.
type RecordMetadata struct {
	Partition int
	Offset    int64
}

// IndexEntry is one entry of the sparse offset index: "offset baseOffset+RelativeOffset starts at byte Position".
type IndexEntry struct {
	RelativeOffset int
	Position       int
}

var (
	// ErrOffsetOutOfRange is returned for reads and seeks outside [logStartOffset, logEndOffset].
	ErrOffsetOutOfRange = errors.New("offset out of range")
	// ErrSegmentFull is returned when a non-empty segment cannot take another frame.
	ErrSegmentFull = errors.New("segment is full")
	// ErrCorruptFrame is returned when a frame's length prefix is smaller than the fixed header.
	ErrCorruptFrame = errors.New("corrupt frame")
)

func outOfRange(offset, start, end int64) error {
	return fmt.Errorf("%w: offset %d is outside [%d, %d]", ErrOffsetOutOfRange, offset, start, end)
}

// SegmentData is the bytes of one segment, as a file would present them: append at the end, read any range.
// The Segment never touches a byte slice directly, so the same Segment logic works over a file, a block store,
// or the in-memory version.
type SegmentData interface {
	Append(bytes []byte)
	Size() int
	Read(position, length int) []byte
}

// SegmentStorage creates, lists, and deletes segment data by base offset. Retention goes through here so a
// file-backed store can unlink.
type SegmentStorage interface {
	Create(partition int, baseOffset int64) SegmentData
	Delete(partition int, baseOffset int64)
	BaseOffsets(partition int) []int64
}

// InMemoryStorage is the storage used by the demo and the tests. One growable byte slice per segment.
type InMemoryStorage struct {
	// partition -> baseOffset -> bytes; the same shape as Kafka's "<topic>-<partition>/<baseOffset>.log" directories.
	partitions map[int]map[int64]*InMemorySegmentData
}

func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{partitions: map[int]map[int64]*InMemorySegmentData{}}
}

func (s *InMemoryStorage) dir(partition int) map[int64]*InMemorySegmentData {
	d, ok := s.partitions[partition]
	if !ok {
		d = map[int64]*InMemorySegmentData{}
		s.partitions[partition] = d
	}
	return d
}

// Create panics on a duplicate base offset: that is a programming error, as Java's IllegalStateException says.
func (s *InMemoryStorage) Create(partition int, baseOffset int64) SegmentData {
	if _, exists := s.dir(partition)[baseOffset]; exists {
		panic(fmt.Sprintf("segment %d/%d already exists", partition, baseOffset))
	}
	data := &InMemorySegmentData{data: make([]byte, 0, 256)}
	s.dir(partition)[baseOffset] = data
	return data
}

func (s *InMemoryStorage) Delete(partition int, baseOffset int64) {
	if _, exists := s.dir(partition)[baseOffset]; !exists {
		panic(fmt.Sprintf("no segment %d/%d", partition, baseOffset))
	}
	delete(s.dir(partition), baseOffset)
}

// BaseOffsets returns the segments of a partition in ascending order (Java's TreeMap order, sorted explicitly here).
func (s *InMemoryStorage) BaseOffsets(partition int) []int64 {
	out := make([]int64, 0, len(s.dir(partition)))
	for base := range s.dir(partition) {
		out = append(out, base)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// InMemorySegmentData is one segment's bytes.
type InMemorySegmentData struct {
	data []byte
}

func (d *InMemorySegmentData) Append(bytes []byte) { d.data = append(d.data, bytes...) }

func (d *InMemorySegmentData) Size() int { return len(d.data) }

// Read copies [position, position+length). A range outside the segment is a programming error and panics,
// as Java's IndexOutOfBoundsException does.
func (d *InMemorySegmentData) Read(position, length int) []byte {
	if position < 0 || length < 0 || position+length > len(d.data) {
		panic(fmt.Sprintf("read [%d, %d) of %d", position, position+length, len(d.data)))
	}
	out := make([]byte, length)
	copy(out, d.data[position:position+length])
	return out
}

// The byte layout of one record ("frame"), big-endian, exactly as it would sit in a segment file:
//
//	int32 frameLength   bytes after this field
//	int64 offset
//	int64 timestamp
//	int32 keyLength     -1 when the key is nil
//	bytes key
//	int32 valueLength
//	bytes value
//
// The length prefix is what lets a reader skip a record it does not care about without parsing it.
const (
	lengthPrefix = 4
	header       = 8 + 8 + 4 + 4 // offset, timestamp, keyLength, valueLength
)

// FrameSize is the number of bytes Encode will produce, including the length prefix.
func FrameSize(key, value []byte) int { return lengthPrefix + header + len(key) + len(value) }

// Encode writes one frame. A nil key is encoded as keyLength -1.
func Encode(offset, timestamp int64, key, value []byte) []byte {
	if value == nil {
		panic("value must not be nil")
	}
	buf := make([]byte, 0, FrameSize(key, value))
	buf = binary.BigEndian.AppendUint32(buf, uint32(FrameSize(key, value)-lengthPrefix))
	buf = binary.BigEndian.AppendUint64(buf, uint64(offset))
	buf = binary.BigEndian.AppendUint64(buf, uint64(timestamp))
	if key == nil {
		buf = binary.BigEndian.AppendUint32(buf, uint32(0xFFFFFFFF))
	} else {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(key)))
		buf = append(buf, key...)
	}
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(value)))
	buf = append(buf, value...)
	return buf
}

// Decode reads the frame starting at position; the caller advances by FrameLengthAt.
func Decode(data []byte, position int) (StoredRecord, error) {
	p := position
	frameLength := int(int32(binary.BigEndian.Uint32(data[p:])))
	p += 4
	if frameLength < header {
		return StoredRecord{}, fmt.Errorf("%w at %d", ErrCorruptFrame, position)
	}
	offset := int64(binary.BigEndian.Uint64(data[p:]))
	p += 8
	timestamp := int64(binary.BigEndian.Uint64(data[p:]))
	p += 8
	keyLength := int(int32(binary.BigEndian.Uint32(data[p:])))
	p += 4
	var key []byte
	if keyLength >= 0 {
		key = make([]byte, keyLength)
		copy(key, data[p:p+keyLength])
		p += keyLength
	}
	valueLength := int(int32(binary.BigEndian.Uint32(data[p:])))
	p += 4
	value := make([]byte, valueLength)
	copy(value, data[p:p+valueLength])
	return StoredRecord{offset, timestamp, key, value}, nil
}

// FrameLengthAt is the total bytes occupied by the frame at position, including its own length prefix.
func FrameLengthAt(data []byte, position int) int {
	return lengthPrefix + int(int32(binary.BigEndian.Uint32(data[position:])))
}

// Fnv1a32 is FNV-1a 32-bit. Chosen over Kafka's murmur2 because it is five lines and identical in all three languages.
func Fnv1a32(bytes []byte) uint32 {
	hash := uint32(0x811C9DC5)
	for _, b := range bytes {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return hash
}

// Segment is a contiguous run of records with offsets [BaseOffset, nextOffset). Bytes are appended and never
// modified. The sparse index records one (relativeOffset, position) pair roughly every indexIntervalBytes.
type Segment struct {
	BaseOffset           int64
	data                 SegmentData
	maxBytes             int
	indexIntervalBytes   int
	nextOffset           int64
	index                []IndexEntry
	bytesSinceIndexEntry int
}

func NewSegment(baseOffset int64, data SegmentData, maxBytes, indexIntervalBytes int) *Segment {
	return &Segment{
		BaseOffset:           baseOffset,
		data:                 data,
		maxBytes:             maxBytes,
		indexIntervalBytes:   indexIntervalBytes,
		nextOffset:           baseOffset,
		bytesSinceIndexEntry: int(^uint(0) >> 1), // force an entry for the first record
	}
}

func (s *Segment) SizeBytes() int { return s.data.Size() }

func (s *Segment) NextOffset() int64 { return s.nextOffset }

func (s *Segment) RecordCount() int { return int(s.nextOffset - s.BaseOffset) }

func (s *Segment) IsEmpty() bool { return s.data.Size() == 0 }

// IndexEntries returns a copy of the sparse index.
func (s *Segment) IndexEntries() []IndexEntry { return append([]IndexEntry(nil), s.index...) }

func (s *Segment) HasRoomFor(frameBytes int) bool { return s.data.Size()+frameBytes <= s.maxBytes }

// Append one record and return its offset. An empty segment accepts any size so oversized records still land.
func (s *Segment) Append(key, value []byte, timestamp int64) (int64, error) {
	frame := FrameSize(key, value)
	if !s.IsEmpty() && !s.HasRoomFor(frame) {
		return 0, fmt.Errorf("%w: segment %d", ErrSegmentFull, s.BaseOffset)
	}
	offset := s.nextOffset
	encoded := Encode(offset, timestamp, key, value)
	if s.bytesSinceIndexEntry >= s.indexIntervalBytes {
		s.index = append(s.index, IndexEntry{int(offset - s.BaseOffset), s.data.Size()})
		s.bytesSinceIndexEntry = 0
	}
	s.data.Append(encoded)
	s.bytesSinceIndexEntry += len(encoded)
	s.nextOffset++
	return offset, nil
}

// frameLengthAt learns the bytes occupied by the frame at position by reading only its 4-byte length prefix.
func (s *Segment) frameLengthAt(position int) int {
	return FrameLengthAt(s.data.Read(position, lengthPrefix), 0)
}

// PositionOf is the byte position of the record at offset: binary-search the sparse index for the last entry at or
// before the target, then scan forward frame by frame. The scan is bounded by indexIntervalBytes.
func (s *Segment) PositionOf(offset int64) (int, error) {
	if offset < s.BaseOffset || offset >= s.nextOffset {
		return 0, outOfRange(offset, s.BaseOffset, s.nextOffset-1)
	}
	relative := int(offset - s.BaseOffset)
	lo, hi, best := 0, len(s.index)-1, 0
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		if s.index[mid].RelativeOffset <= relative {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	entry := s.index[best]
	position := entry.Position
	current := s.BaseOffset + int64(entry.RelativeOffset)
	for current < offset {
		position += s.frameLengthAt(position)
		current++
	}
	return position, nil
}

// Read up to maxRecords starting at offset, stopping at the end of this segment.
func (s *Segment) Read(offset int64, maxRecords int) ([]StoredRecord, error) {
	out := []StoredRecord{}
	if offset >= s.nextOffset || maxRecords <= 0 {
		return out, nil
	}
	position, err := s.PositionOf(offset)
	if err != nil {
		return nil, err
	}
	for position < s.data.Size() && len(out) < maxRecords {
		length := s.frameLengthAt(position)
		rec, err := Decode(s.data.Read(position, length), 0)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
		position += length
	}
	return out, nil
}

// Partition is an ordered list of segments. The last segment is active; older ones are immutable and eligible
// for retention.
type Partition struct {
	ID       int
	config   LogConfig
	storage  SegmentStorage
	segments []*Segment
}

func NewPartition(id int, config LogConfig, storage SegmentStorage) *Partition {
	p := &Partition{ID: id, config: config, storage: storage}
	p.segments = append(p.segments, p.newSegment(0))
	return p
}

func (p *Partition) newSegment(baseOffset int64) *Segment {
	return NewSegment(baseOffset, p.storage.Create(p.ID, baseOffset), p.config.SegmentBytes, p.config.IndexIntervalBytes)
}

func (p *Partition) active() *Segment { return p.segments[len(p.segments)-1] }

func (p *Partition) LogStartOffset() int64 { return p.segments[0].BaseOffset }

// LogEndOffset is the offset the next record will receive; also "high watermark" in a single-replica log.
func (p *Partition) LogEndOffset() int64 { return p.active().NextOffset() }

func (p *Partition) SegmentCount() int { return len(p.segments) }

func (p *Partition) SegmentBaseOffsets() []int64 {
	out := make([]int64, 0, len(p.segments))
	for _, s := range p.segments {
		out = append(out, s.BaseOffset)
	}
	return out
}

func (p *Partition) SizeBytes() int64 {
	var total int64
	for _, s := range p.segments {
		total += int64(s.SizeBytes())
	}
	return total
}

// Append and return the assigned offset. Rolls to a new segment when the active one cannot take the frame.
// It cannot fail: an empty segment accepts any frame, so the roll always leaves room.
func (p *Partition) Append(key, value []byte, timestamp int64) int64 {
	active := p.active()
	frame := FrameSize(key, value)
	if !active.IsEmpty() && !active.HasRoomFor(frame) {
		active = p.newSegment(active.NextOffset())
		p.segments = append(p.segments, active)
	}
	offset, err := active.Append(key, value, timestamp)
	if err != nil {
		panic(err) // unreachable: a fresh segment accepts any size
	}
	return offset
}

// segmentIndexFor is the index of the segment whose offset range contains offset: the last segment with
// BaseOffset <= offset.
func (p *Partition) segmentIndexFor(offset int64) int {
	lo, hi, best := 0, len(p.segments)-1, 0
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		if p.segments[mid].BaseOffset <= offset {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// Read up to maxRecords from fromOffset, crossing segment boundaries. Reading at LogEndOffset returns an empty
// slice (nothing yet); reading below LogStartOffset or above LogEndOffset is ErrOffsetOutOfRange, as in Kafka.
func (p *Partition) Read(fromOffset int64, maxRecords int) ([]StoredRecord, error) {
	start, end := p.LogStartOffset(), p.LogEndOffset()
	if fromOffset < start || fromOffset > end {
		return nil, outOfRange(fromOffset, start, end)
	}
	out := []StoredRecord{}
	if fromOffset == end {
		return out, nil
	}
	next := fromOffset
	for i := p.segmentIndexFor(fromOffset); i < len(p.segments) && len(out) < maxRecords; i++ {
		chunk, err := p.segments[i].Read(next, maxRecords-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(chunk) > 0 {
			next = chunk[len(chunk)-1].Offset + 1
		}
	}
	return out, nil
}

// EnforceRetention drops whole segments, oldest first, until the partition fits in RetentionBytes. The active
// segment is never dropped.
func (p *Partition) EnforceRetention() int {
	removed := 0
	for len(p.segments) > 1 && p.SizeBytes() > p.config.RetentionBytes {
		oldest := p.segments[0]
		p.segments = p.segments[1:]
		p.storage.Delete(p.ID, oldest.BaseOffset)
		removed++
	}
	return removed
}

// Topic is a named set of partitions. Records with the same non-nil key always land in the same partition.
type Topic struct {
	Name       string
	partitions []*Partition
	roundRobin int64
}

func NewTopic(name string, partitionCount int, config LogConfig, storage SegmentStorage) (*Topic, error) {
	if partitionCount <= 0 {
		return nil, errors.New("need at least one partition")
	}
	t := &Topic{Name: name}
	for i := 0; i < partitionCount; i++ {
		t.partitions = append(t.partitions, NewPartition(i, config, storage))
	}
	return t, nil
}

func (t *Topic) PartitionCount() int { return len(t.partitions) }

func (t *Topic) Partition(id int) *Partition { return t.partitions[id] }

// PartitionFor: keyed records hash; unkeyed records rotate. The hash is masked to non-negative before the modulo.
func (t *Topic) PartitionFor(key []byte) int {
	if key == nil {
		p := int(t.roundRobin % int64(len(t.partitions)))
		t.roundRobin++
		return p
	}
	return int(Fnv1a32(key)&0x7FFFFFFF) % len(t.partitions)
}

func (t *Topic) Append(key, value []byte, timestamp int64) RecordMetadata {
	p := t.PartitionFor(key)
	return RecordMetadata{p, t.partitions[p].Append(key, value, timestamp)}
}

// OffsetStore holds committed consumer positions, keyed by (group, partition). In Kafka this is the
// __consumer_offsets topic; here it is a map, but the contract is the same: the committed value is the NEXT
// offset to read, not the last one processed.
type OffsetStore struct {
	committed map[string]int64
}

// None is the committed value for a group that has never committed.
const None int64 = -1

func NewOffsetStore() *OffsetStore { return &OffsetStore{committed: map[string]int64{}} }

func offsetKey(group string, partition int) string { return group + "/" + fmt.Sprint(partition) }

func (o *OffsetStore) Commit(group string, partition int, nextOffset int64) error {
	if nextOffset < 0 {
		return errors.New("offset must be non-negative")
	}
	o.committed[offsetKey(group, partition)] = nextOffset
	return nil
}

func (o *OffsetStore) Committed(group string, partition int) int64 {
	if v, ok := o.committed[offsetKey(group, partition)]; ok {
		return v
	}
	return None
}

// Consumer is a single-partition reader that belongs to a consumer group. Position is the next offset to read.
// A new Consumer resumes from the group's committed offset; if there is none, or it has been retired by
// retention, it starts at LogStartOffset (Kafka's auto.offset.reset=earliest).
type Consumer struct {
	partition *Partition
	group     string
	offsets   *OffsetStore
	position  int64
}

func NewConsumer(partition *Partition, group string, offsets *OffsetStore) *Consumer {
	c := &Consumer{partition: partition, group: group, offsets: offsets}
	committed := offsets.Committed(group, partition.ID)
	if committed == None || committed < partition.LogStartOffset() || committed > partition.LogEndOffset() {
		c.position = partition.LogStartOffset()
	} else {
		c.position = committed
	}
	return c
}

func (c *Consumer) SeekTo(offset int64) error {
	if offset < c.partition.LogStartOffset() || offset > c.partition.LogEndOffset() {
		return outOfRange(offset, c.partition.LogStartOffset(), c.partition.LogEndOffset())
	}
	c.position = offset
	return nil
}

func (c *Consumer) Position() int64 { return c.position }

func (c *Consumer) Poll(maxRecords int) ([]StoredRecord, error) {
	batch, err := c.partition.Read(c.position, maxRecords)
	if err != nil {
		return nil, err
	}
	if len(batch) > 0 {
		c.position = batch[len(batch)-1].Offset + 1
	}
	return batch, nil
}

// SeekTo is Java's seek; renamed because go vet reserves the Seek name for io.Seeker's signature.
// Commit records that everything below position has been processed. Anything polled but not committed is
// redelivered after a restart.
func (c *Consumer) Commit() error { return c.offsets.Commit(c.group, c.partition.ID, c.position) }

func utf8(s string) []byte { return []byte(s) }

func formatOffsets(offsets []int64) string {
	parts := make([]string, len(offsets))
	for i, o := range offsets {
		parts[i] = fmt.Sprint(o)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	clock := NewTickClock(1_700_000_000_000)
	config := must(NewLogConfig(160, 64, 400))
	storage := NewInMemoryStorage()
	topic := must(NewTopic("payments.authorized", 3, config, storage))

	fmt.Println("== Producing 12 keyed records into 3 partitions ==")
	merchants := []string{"m-coffee", "m-books", "m-grocer", "m-fuel"}
	for i := 0; i < 12; i++ {
		key := merchants[i%len(merchants)]
		value := fmt.Sprintf("auth-%d 19.99 USD", 1000+i)
		meta := topic.Append(utf8(key), utf8(value), clock.Now())
		fmt.Printf("%-9s -> partition %d offset %d\n", key, meta.Partition, meta.Offset)
	}

	fmt.Println("\n== Partition layout after segment rolls (segmentBytes=160) ==")
	for p := 0; p < topic.PartitionCount(); p++ {
		part := topic.Partition(p)
		fmt.Printf("partition %d: logStart=%d logEnd=%d segments(base offsets)=%s bytes=%d\n",
			p, part.LogStartOffset(), part.LogEndOffset(), formatOffsets(part.SegmentBaseOffsets()), part.SizeBytes())
	}

	p0 := topic.Partition(0)
	fmt.Println("\n== Reading partition 0 from offset 1, three at a time ==")
	offsets := NewOffsetStore()
	consumer := NewConsumer(p0, "ledger-poster", offsets)
	must(0, consumer.SeekTo(1))
	for {
		batch := must(consumer.Poll(3))
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			fmt.Printf("offset %d ts %d key %s value %s\n", r.Offset, r.Timestamp, r.KeyString(), r.ValueString())
		}
		fmt.Println("  position is now", consumer.Position())
	}

	fmt.Println("\n== Commit, crash, resume (partition 1, six records) ==")
	p1 := topic.Partition(1)
	worker := NewConsumer(p1, "ledger-poster", offsets)
	fmt.Println("fresh consumer with no commit starts at logStart: position", worker.Position())
	must(worker.Poll(2))
	must(0, worker.Commit())
	fmt.Println("processed 2 and committed: committed offset =", offsets.Committed("ledger-poster", 1))
	must(worker.Poll(2))
	fmt.Printf("processed 2 more without committing: position %d (then the process dies)\n", worker.Position())
	restarted := NewConsumer(p1, "ledger-poster", offsets)
	fmt.Printf("restart resumes at the committed offset: position %d -> offsets 2 and 3 are delivered again (at-least-once)\n",
		restarted.Position())

	fmt.Println("\n== Retention: keep at most 400 bytes per partition, whole segments only ==")
	archiver := NewConsumer(p0, "archiver", offsets)
	must(archiver.Poll(2))
	must(0, archiver.Commit())
	for i := 0; i < 20; i++ {
		p0.Append(utf8("m-coffee"), utf8(fmt.Sprintf("auth-%d 5.00 USD", 2000+i)), clock.Now())
	}
	fmt.Printf("before: segments=%s bytes=%d\n", formatOffsets(p0.SegmentBaseOffsets()), p0.SizeBytes())
	dropped := p0.EnforceRetention()
	fmt.Printf("after : dropped %d segment(s), segments=%s bytes=%d logStart=%d\n",
		dropped, formatOffsets(p0.SegmentBaseOffsets()), p0.SizeBytes(), p0.LogStartOffset())
	if _, err := p0.Read(0, 1); err != nil {
		fmt.Println("read(0) rejected:", err)
	}
	first := must(p0.Read(p0.LogStartOffset(), 1))[0]
	fmt.Printf("earliest readable record: offset %d %s\n", first.Offset, first.ValueString())
	lagging := NewConsumer(p0, "archiver", offsets)
	fmt.Printf("archiver committed %d, which retention retired: it resets to logStart, position %d\n",
		offsets.Committed("archiver", 0), lagging.Position())

	fmt.Println("\n== The frame on the wire ==")
	frame := Encode(7, 1_700_000_000_000, utf8("m-fuel"), utf8("ok"))
	fmt.Printf("%d bytes: %x\n", len(frame), frame)
}
