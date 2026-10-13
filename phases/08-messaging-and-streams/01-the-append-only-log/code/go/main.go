// Lesson: phases/08-messaging-and-streams/01-the-append-only-log/docs/en.md
// The Append-Only Log: Partitions, Offsets, and Segments
// Run:  go run .
// Test: go test ./...
// Sources: Apache Kafka documentation, section 5.4 "Log" (segment files, sparse offset index, retention);
//          Kreps, Narkhede, Rao, "Kafka: a Distributed Messaging System for Log Processing" (NetDB 2011).
// Go 1.22+, standard library only. Same design as the Java and Python versions. Segments live behind an injected
// SegmentStorage; the in-memory one holds byte slices with the exact frame layout a file would hold.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// Clock is an injected time source so demos and tests are deterministic.
type Clock interface {
	Now() int64
}

// TickClock advances one millisecond per call, starting at a fixed epoch.
type TickClock struct{ next int64 }

func NewTickClock(startMillis int64) *TickClock { return &TickClock{next: startMillis} }

func (c *TickClock) Now() int64 {
	v := c.next
	c.next++
	return v
}

// LogConfig holds the sizing rules for every partition of a topic.
type LogConfig struct {
	SegmentBytes       int
	IndexIntervalBytes int
	RetentionBytes     int64
}

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
// A nil Key means the record was produced without a key.
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

// IndexEntry is one entry of the sparse offset index: offset base+RelativeOffset starts at byte Position.
type IndexEntry struct {
	RelativeOffset int
	Position       int
}

// ErrOffsetOutOfRange is returned for reads below the log start or above the log end.
var ErrOffsetOutOfRange = errors.New("offset out of range")

// ErrSegmentFull is returned when a non-empty segment cannot take another frame.
var ErrSegmentFull = errors.New("segment is full")

func outOfRange(offset, start, end int64) error {
	return fmt.Errorf("%w: offset %d is outside [%d, %d]", ErrOffsetOutOfRange, offset, start, end)
}

// SegmentData is the bytes of one segment, as a file would present them: append at the end, read any range.
// The Segment never touches a byte slice directly, so the same logic works over a file or a block store.
type SegmentData interface {
	Append(bytes []byte)
	Size() int
	Read(position, length int) []byte
}

// SegmentStorage creates, lists, and deletes segment data by (partition, base offset). Retention goes through here.
type SegmentStorage interface {
	Create(partition int, baseOffset int64) (SegmentData, error)
	Delete(partition int, baseOffset int64) error
	BaseOffsets(partition int) []int64
}

// InMemorySegmentData is one growable byte slice.
type InMemorySegmentData struct{ data []byte }

func (d *InMemorySegmentData) Append(bytes []byte) { d.data = append(d.data, bytes...) }
func (d *InMemorySegmentData) Size() int           { return len(d.data) }
func (d *InMemorySegmentData) Read(position, length int) []byte {
	if position < 0 || length < 0 || position+length > len(d.data) {
		panic(fmt.Sprintf("read [%d, %d) of %d", position, position+length, len(d.data)))
	}
	return append([]byte{}, d.data[position:position+length]...)
}

// InMemoryStorage is partition -> baseOffset -> bytes; the same shape as Kafka's "<topic>-<partition>/<baseOffset>.log".
type InMemoryStorage struct {
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

func (s *InMemoryStorage) Create(partition int, baseOffset int64) (SegmentData, error) {
	d := s.dir(partition)
	if _, exists := d[baseOffset]; exists {
		return nil, fmt.Errorf("segment %d/%d already exists", partition, baseOffset)
	}
	data := &InMemorySegmentData{}
	d[baseOffset] = data
	return data, nil
}

func (s *InMemoryStorage) Delete(partition int, baseOffset int64) error {
	d := s.dir(partition)
	if _, exists := d[baseOffset]; !exists {
		return fmt.Errorf("no segment %d/%d", partition, baseOffset)
	}
	delete(d, baseOffset)
	return nil
}

func (s *InMemoryStorage) BaseOffsets(partition int) []int64 {
	out := []int64{}
	for base := range s.dir(partition) {
		out = append(out, base)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Frame layout, big-endian, exactly as it would sit in a segment file:
//
//	int32 frameLength   bytes after this field
//	int64 offset
//	int64 timestamp
//	int32 keyLength     -1 when the key is nil
//	bytes key
//	int32 valueLength
//	bytes value
const (
	lengthPrefix = 4
	header       = 8 + 8 + 4 + 4
)

func FrameSize(key, value []byte) int { return lengthPrefix + header + len(key) + len(value) }

func Encode(offset, timestamp int64, key, value []byte) []byte {
	buf := make([]byte, 0, FrameSize(key, value))
	buf = binary.BigEndian.AppendUint32(buf, uint32(FrameSize(key, value)-lengthPrefix))
	buf = binary.BigEndian.AppendUint64(buf, uint64(offset))
	buf = binary.BigEndian.AppendUint64(buf, uint64(timestamp))
	if key == nil {
		buf = binary.BigEndian.AppendUint32(buf, uint32(0xFFFFFFFF)) // -1
	} else {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(key)))
		buf = append(buf, key...)
	}
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(value)))
	buf = append(buf, value...)
	return buf
}

// Decode reads the frame at position; the caller advances by FrameLengthAt.
func Decode(data []byte, position int) (StoredRecord, error) {
	frameLength := int(int32(binary.BigEndian.Uint32(data[position:])))
	if frameLength < header {
		return StoredRecord{}, fmt.Errorf("corrupt frame at %d", position)
	}
	pos := position + lengthPrefix
	offset := int64(binary.BigEndian.Uint64(data[pos:]))
	timestamp := int64(binary.BigEndian.Uint64(data[pos+8:]))
	keyLength := int(int32(binary.BigEndian.Uint32(data[pos+16:])))
	pos += 20
	var key []byte
	if keyLength >= 0 {
		key = append([]byte{}, data[pos:pos+keyLength]...)
		pos += keyLength
	}
	valueLength := int(int32(binary.BigEndian.Uint32(data[pos:])))
	pos += 4
	value := append([]byte{}, data[pos:pos+valueLength]...)
	return StoredRecord{offset, timestamp, key, value}, nil
}

// FrameLengthAt is the total bytes occupied by the frame at position, including its own length prefix.
func FrameLengthAt(data []byte, position int) int {
	return lengthPrefix + int(int32(binary.BigEndian.Uint32(data[position:])))
}

// Fnv1a32 is FNV-1a 32-bit; five lines and identical in all three languages (Kafka itself uses murmur2).
func Fnv1a32(data []byte) uint32 {
	h := uint32(0x811C9DC5)
	for _, b := range data {
		h ^= uint32(b)
		h *= 0x01000193
	}
	return h
}

// Segment is a contiguous run of records with offsets [BaseOffset, nextOffset). Bytes are appended, never modified.
type Segment struct {
	BaseOffset          int64
	data                SegmentData
	maxBytes            int
	indexIntervalBytes  int
	nextOffset          int64
	index               []IndexEntry
	bytesSinceIndexEntr int
}

func NewSegment(baseOffset int64, data SegmentData, maxBytes, indexIntervalBytes int) *Segment {
	return &Segment{
		BaseOffset:          baseOffset,
		data:                data,
		maxBytes:            maxBytes,
		indexIntervalBytes:  indexIntervalBytes,
		nextOffset:          baseOffset,
		bytesSinceIndexEntr: 1 << 30, // force an entry for the first record
	}
}

func (s *Segment) SizeBytes() int             { return s.data.Size() }
func (s *Segment) NextOffset() int64          { return s.nextOffset }
func (s *Segment) RecordCount() int           { return int(s.nextOffset - s.BaseOffset) }
func (s *Segment) IsEmpty() bool              { return s.data.Size() == 0 }
func (s *Segment) IndexEntries() []IndexEntry { return append([]IndexEntry{}, s.index...) }
func (s *Segment) HasRoomFor(frameBytes int) bool {
	return s.data.Size()+frameBytes <= s.maxBytes
}

// frameLengthAt is the bytes occupied by the frame at position, learned by reading only its 4-byte length prefix.
func (s *Segment) frameLengthAt(position int) int {
	return FrameLengthAt(s.data.Read(position, lengthPrefix), 0)
}

// Append stores one record and returns its offset. An empty segment accepts any size so oversized records still land.
func (s *Segment) Append(key, value []byte, timestamp int64) (int64, error) {
	frame := FrameSize(key, value)
	if !s.IsEmpty() && !s.HasRoomFor(frame) {
		return 0, fmt.Errorf("%w: base offset %d", ErrSegmentFull, s.BaseOffset)
	}
	offset := s.nextOffset
	encoded := Encode(offset, timestamp, key, value)
	if s.bytesSinceIndexEntr >= s.indexIntervalBytes {
		s.index = append(s.index, IndexEntry{int(offset - s.BaseOffset), s.data.Size()})
		s.bytesSinceIndexEntr = 0
	}
	s.data.Append(encoded)
	s.bytesSinceIndexEntr += len(encoded)
	s.nextOffset++
	return offset, nil
}

// PositionOf binary-searches the sparse index for the last entry at or before offset, then scans forward frame by frame.
func (s *Segment) PositionOf(offset int64) (int, error) {
	if offset < s.BaseOffset || offset >= s.nextOffset {
		return 0, outOfRange(offset, s.BaseOffset, s.nextOffset-1)
	}
	relative := int(offset - s.BaseOffset)
	lo, hi, best := 0, len(s.index)-1, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if s.index[mid].RelativeOffset <= relative {
			best, lo = mid, mid+1
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

// Read returns up to maxRecords starting at offset, stopping at the end of this segment.
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

// Partition is an ordered list of segments. The last is active; older ones are immutable and eligible for retention.
type Partition struct {
	ID       int
	config   LogConfig
	storage  SegmentStorage
	segments []*Segment
}

func NewPartition(id int, config LogConfig, storage SegmentStorage) (*Partition, error) {
	p := &Partition{ID: id, config: config, storage: storage}
	first, err := p.newSegment(0)
	if err != nil {
		return nil, err
	}
	p.segments = []*Segment{first}
	return p, nil
}

func (p *Partition) newSegment(baseOffset int64) (*Segment, error) {
	data, err := p.storage.Create(p.ID, baseOffset)
	if err != nil {
		return nil, err
	}
	return NewSegment(baseOffset, data, p.config.SegmentBytes, p.config.IndexIntervalBytes), nil
}

func (p *Partition) active() *Segment    { return p.segments[len(p.segments)-1] }
func (p *Partition) LogStartOffset() int64 { return p.segments[0].BaseOffset }

// LogEndOffset is the offset the next record will receive.
func (p *Partition) LogEndOffset() int64 { return p.active().NextOffset() }
func (p *Partition) SegmentCount() int   { return len(p.segments) }

func (p *Partition) SegmentBaseOffsets() []int64 {
	out := make([]int64, len(p.segments))
	for i, s := range p.segments {
		out[i] = s.BaseOffset
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

// Append stores the record and returns its offset, rolling to a new segment when the active one cannot take the frame.
func (p *Partition) Append(key, value []byte, timestamp int64) (int64, error) {
	active := p.active()
	if !active.IsEmpty() && !active.HasRoomFor(FrameSize(key, value)) {
		next, err := p.newSegment(active.NextOffset())
		if err != nil {
			return 0, err
		}
		active = next
		p.segments = append(p.segments, active)
	}
	return active.Append(key, value, timestamp)
}

func (p *Partition) segmentIndexFor(offset int64) int {
	lo, hi, best := 0, len(p.segments)-1, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if p.segments[mid].BaseOffset <= offset {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// Read returns up to maxRecords from fromOffset, crossing segment boundaries. Reading at LogEndOffset is empty;
// below LogStartOffset or above LogEndOffset is ErrOffsetOutOfRange, as in Kafka.
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

// EnforceRetention drops whole segments, oldest first, until the partition fits in RetentionBytes. Never drops the active one.
func (p *Partition) EnforceRetention() int {
	removed := 0
	for len(p.segments) > 1 && p.SizeBytes() > p.config.RetentionBytes {
		oldest := p.segments[0]
		p.segments = p.segments[1:]
		if err := p.storage.Delete(p.ID, oldest.BaseOffset); err != nil {
			panic(err) // the segment was created through the same storage; a failed delete is a storage bug
		}
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
		p, err := NewPartition(i, config, storage)
		if err != nil {
			return nil, err
		}
		t.partitions = append(t.partitions, p)
	}
	return t, nil
}

func (t *Topic) PartitionCount() int          { return len(t.partitions) }
func (t *Topic) Partition(id int) *Partition { return t.partitions[id] }

// PartitionFor hashes keyed records and rotates unkeyed ones. The hash is masked to 31 bits before the modulo.
func (t *Topic) PartitionFor(key []byte) int {
	if key == nil {
		p := int(t.roundRobin % int64(len(t.partitions)))
		t.roundRobin++
		return p
	}
	return int((Fnv1a32(key) & 0x7FFFFFFF) % uint32(len(t.partitions)))
}

func (t *Topic) Append(key, value []byte, timestamp int64) (RecordMetadata, error) {
	p := t.PartitionFor(key)
	offset, err := t.partitions[p].Append(key, value, timestamp)
	return RecordMetadata{p, offset}, err
}

// NoCommit is what OffsetStore.Committed returns for a group that has never committed on a partition.
const NoCommit int64 = -1

type groupPartition struct {
	group     string
	partition int
}

// OffsetStore holds committed consumer positions keyed by (group, partition). In Kafka this is the __consumer_offsets
// topic; here it is a map, but the contract is the same: the committed value is the NEXT offset to read.
type OffsetStore struct{ committed map[groupPartition]int64 }

func NewOffsetStore() *OffsetStore { return &OffsetStore{committed: map[groupPartition]int64{}} }

func (o *OffsetStore) Commit(group string, partition int, nextOffset int64) error {
	if nextOffset < 0 {
		return errors.New("offset must be non-negative")
	}
	o.committed[groupPartition{group, partition}] = nextOffset
	return nil
}

func (o *OffsetStore) Committed(group string, partition int) int64 {
	if v, ok := o.committed[groupPartition{group, partition}]; ok {
		return v
	}
	return NoCommit
}

// Consumer is a single-partition reader that belongs to a consumer group. Position is the next offset to read.
// A new Consumer resumes from the group's committed offset; if there is none, or retention has retired it, it starts
// at LogStartOffset (Kafka's auto.offset.reset=earliest).
type Consumer struct {
	partition *Partition
	group     string
	offsets   *OffsetStore
	position  int64
}

func NewConsumer(partition *Partition, group string, offsets *OffsetStore) *Consumer {
	c := &Consumer{partition: partition, group: group, offsets: offsets}
	committed := offsets.Committed(group, partition.ID)
	if committed == NoCommit || committed < partition.LogStartOffset() || committed > partition.LogEndOffset() {
		c.position = partition.LogStartOffset()
	} else {
		c.position = committed
	}
	return c
}

func (c *Consumer) Seek(offset int64) error {
	start, end := c.partition.LogStartOffset(), c.partition.LogEndOffset()
	if offset < start || offset > end {
		return outOfRange(offset, start, end)
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

// Commit records that everything below position has been processed. Polled-but-uncommitted records are redelivered after a restart.
func (c *Consumer) Commit() error { return c.offsets.Commit(c.group, c.partition.ID, c.position) }

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
		meta := must(topic.Append([]byte(key), []byte(value), clock.Now()))
		fmt.Printf("%-9s -> partition %d offset %d\n", key, meta.Partition, meta.Offset)
	}

	fmt.Println("\n== Partition layout after segment rolls (segmentBytes=160) ==")
	for p := 0; p < topic.PartitionCount(); p++ {
		part := topic.Partition(p)
		fmt.Printf("partition %d: logStart=%d logEnd=%d segments(base offsets)=%v bytes=%d\n",
			p, part.LogStartOffset(), part.LogEndOffset(), part.SegmentBaseOffsets(), part.SizeBytes())
	}

	p0 := topic.Partition(0)
	fmt.Println("\n== Reading partition 0 from offset 1, three at a time ==")
	offsets := NewOffsetStore()
	consumer := NewConsumer(p0, "ledger-poster", offsets)
	if err := consumer.Seek(1); err != nil {
		panic(err)
	}
	for {
		batch := must(consumer.Poll(3))
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			fmt.Printf("offset %d ts %d key %s value %s\n", r.Offset, r.Timestamp, r.KeyString(), r.ValueString())
		}
		fmt.Printf("  position is now %d\n", consumer.Position())
	}

	fmt.Println("\n== Commit, crash, resume (partition 1, six records) ==")
	p1 := topic.Partition(1)
	worker := NewConsumer(p1, "ledger-poster", offsets)
	fmt.Printf("fresh consumer with no commit starts at logStart: position %d\n", worker.Position())
	must(worker.Poll(2))
	if err := worker.Commit(); err != nil {
		panic(err)
	}
	fmt.Printf("processed 2 and committed: committed offset = %d\n", offsets.Committed("ledger-poster", 1))
	must(worker.Poll(2))
	fmt.Printf("processed 2 more without committing: position %d (then the process dies)\n", worker.Position())
	restarted := NewConsumer(p1, "ledger-poster", offsets)
	fmt.Printf("restart resumes at the committed offset: position %d -> offsets 2 and 3 are delivered again (at-least-once)\n", restarted.Position())

	fmt.Println("\n== Retention: keep at most 400 bytes per partition, whole segments only ==")
	archiver := NewConsumer(p0, "archiver", offsets)
	must(archiver.Poll(2))
	if err := archiver.Commit(); err != nil {
		panic(err)
	}
	for i := 0; i < 20; i++ {
		must(p0.Append([]byte("m-coffee"), []byte(fmt.Sprintf("auth-%d 5.00 USD", 2000+i)), clock.Now()))
	}
	fmt.Printf("before: segments=%v bytes=%d\n", p0.SegmentBaseOffsets(), p0.SizeBytes())
	dropped := p0.EnforceRetention()
	fmt.Printf("after : dropped %d segment(s), segments=%v bytes=%d logStart=%d\n",
		dropped, p0.SegmentBaseOffsets(), p0.SizeBytes(), p0.LogStartOffset())
	if _, err := p0.Read(0, 1); err != nil {
		fmt.Println("read(0) rejected:", err)
	}
	first := must(p0.Read(p0.LogStartOffset(), 1))[0]
	fmt.Printf("earliest readable record: offset %d %s\n", first.Offset, first.ValueString())
	lagging := NewConsumer(p0, "archiver", offsets)
	fmt.Printf("archiver committed %d, which retention retired: it resets to logStart, position %d\n",
		offsets.Committed("archiver", 0), lagging.Position())

	fmt.Println("\n== The frame on the wire ==")
	frame := Encode(7, 1_700_000_000_000, []byte("m-fuel"), []byte("ok"))
	fmt.Printf("%d bytes: %s\n", len(frame), hex.EncodeToString(frame))
}
