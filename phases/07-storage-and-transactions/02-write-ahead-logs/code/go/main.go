// Lesson: phases/07-storage-and-transactions/02-write-ahead-logs/docs/en.md
// Write-Ahead Logs and Crash Recovery
// Run:  go run .
// Test: go test ./...
// Sources: Mohan et al., "ARIES: A Transaction Recovery Method" (ACM TODS 17(1), 1992); PostgreSQL manual ch. 28
//          "Reliability and the Write-Ahead Log"; Pillai et al., "All File Systems Are Not Created Equal" (OSDI 2014);
//          CRC-32 per ISO 3309 / ITU-T V.42 (the polynomial behind hash/crc32 IEEE, zlib.crc32 and java.util.zip.CRC32).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- money (trimmed copy of Phase 10, Lesson 01)

// Currency is an ISO 4217 currency code. The exponent is not needed in this lesson.
type Currency struct{ Code string }

var USD = Currency{"USD"}

// Money is an exact amount: signed integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minor int64, currency Currency) Money { return Money{minor, currency} }

func (m Money) Plus(other Money) Money {
	if m.Currency != other.Currency {
		panic("cannot combine " + m.Currency.Code + " with " + other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}
}

func (m Money) Minus(other Money) Money {
	if m.Currency != other.Currency {
		panic("cannot combine " + m.Currency.Code + " with " + other.Currency.Code)
	}
	return Money{m.MinorUnits - other.MinorUnits, m.Currency}
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

func (m Money) String() string {
	sign := ""
	mag := m.MinorUnits
	if mag < 0 {
		sign, mag = "-", -mag
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, mag/100, mag%100, m.Currency.Code)
}

// ---------------------------------------------------------------- storage

// Storage is the durability boundary. Write is buffered; bytes are durable only once Sync returns (fsync(2)).
type Storage interface {
	Size() int64
	Read(offset int64, length int) []byte
	Write(offset int64, data []byte)
	Truncate(size int64)
	Sync()
}

// MemoryStorage keeps two copies of the bytes: what the process sees, and what has reached the disk.
type MemoryStorage struct {
	visible   []byte
	durable   []byte
	SyncCount int
}

func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{} }

func (s *MemoryStorage) Size() int64        { return int64(len(s.visible)) }
func (s *MemoryStorage) DurableSize() int64 { return int64(len(s.durable)) }

func (s *MemoryStorage) Read(offset int64, length int) []byte {
	if offset < 0 || int(offset)+length > len(s.visible) {
		panic("read past end")
	}
	out := make([]byte, length)
	copy(out, s.visible[offset:int(offset)+length])
	return out
}

func (s *MemoryStorage) Write(offset int64, data []byte) {
	end := int(offset) + len(data)
	if end > len(s.visible) {
		s.visible = append(s.visible, make([]byte, end-len(s.visible))...)
	}
	copy(s.visible[offset:end], data)
}

func (s *MemoryStorage) Truncate(size int64) {
	if int(size) < len(s.visible) {
		s.visible = s.visible[:size]
	}
}

func (s *MemoryStorage) Sync() {
	s.durable = append([]byte(nil), s.visible...)
	s.SyncCount++
}

// Crash is kill -9 or power loss: everything that was not synced is gone.
func (s *MemoryStorage) Crash() { s.visible = append([]byte(nil), s.durable...) }

// CrashKeepingUnsynced is power loss during a write: the first keepBytes of the unsynced tail reached the disk.
func (s *MemoryStorage) CrashKeepingUnsynced(keepBytes int) {
	unsynced := len(s.visible) - len(s.durable)
	if unsynced < 0 {
		unsynced = 0
	}
	keep := min(keepBytes, unsynced)
	tail := append([]byte(nil), s.visible[len(s.durable):len(s.durable)+keep]...)
	s.visible = append(append([]byte(nil), s.durable...), tail...)
	s.durable = append([]byte(nil), s.visible...)
}

// FlipBit is bit rot: flip one bit in a byte that is already durable.
func (s *MemoryStorage) FlipBit(offset int64) {
	s.visible[offset] ^= 0x01
	s.durable = append([]byte(nil), s.visible...)
}

// ---------------------------------------------------------------- the log

const (
	HeaderBytes     = 16
	MaxPayloadBytes = 1 << 20 // 1 MiB: anything larger is a corrupt length, not a record
)

// Record is one record as replay hands it back: its sequence number, where it starts, and the caller's bytes.
type Record struct {
	Seq     uint64
	Offset  int64
	Payload []byte
}

// CorruptRecordError: a fully written record was damaged, or the sequence is not contiguous. Recovery must stop.
type CorruptRecordError struct {
	Offset int64
	Reason string
}

func (e *CorruptRecordError) Error() string {
	return fmt.Sprintf("corrupt record at offset %d: %s", e.Offset, e.Reason)
}

// ReplayResult: records delivered, where the valid prefix ends, torn bytes cut off, last sequence number seen.
type ReplayResult struct {
	RecordsReplayed int
	ValidLength     int64
	TruncatedBytes  int64
	LastSeq         uint64
}

func (r ReplayResult) Torn() bool { return r.TruncatedBytes > 0 }

// CRC32 covers the sequence number and the payload, so a frame copied to the wrong slot fails its checksum.
func CRC32(seq uint64, payload []byte) uint32 {
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], seq)
	return crc32.Update(crc32.ChecksumIEEE(seqBytes[:]), crc32.IEEETable, payload)
}

// Encode frames a payload: u32 length | u32 crc32(seq || payload) | u64 seq | payload, all big-endian.
func Encode(seq uint64, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("payload too large: %d", len(payload))
	}
	frame := make([]byte, HeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], CRC32(seq, payload))
	binary.BigEndian.PutUint64(frame[8:16], seq)
	copy(frame[HeaderBytes:], payload)
	return frame, nil
}

// WriteAheadLog is an append-only log of framed records on top of a Storage. Append never rewrites earlier
// bytes; Sync is the only durability point; sequence numbers are contiguous; Replay accepts the longest valid
// prefix, truncates a torn final frame, and refuses to continue past a damaged frame that has data after it.
type WriteAheadLog struct {
	storage Storage
	end     int64
	nextSeq uint64
}

func NewWriteAheadLog(storage Storage) *WriteAheadLog {
	return &WriteAheadLog{storage: storage, end: storage.Size(), nextSeq: 1}
}

func (w *WriteAheadLog) Size() int64     { return w.end }
func (w *WriteAheadLog) NextSeq() uint64 { return w.nextSeq }

// Append writes one frame at the end and returns its sequence number. Not durable until Sync.
func (w *WriteAheadLog) Append(payload []byte) (uint64, error) {
	seq := w.nextSeq
	frame, err := Encode(seq, payload)
	if err != nil {
		return 0, err
	}
	w.storage.Write(w.end, frame)
	w.end += int64(len(frame))
	w.nextSeq++
	return seq, nil
}

// Sync is the durability point. Everything appended before this call survives a crash after it returns.
func (w *WriteAheadLog) Sync() { w.storage.Sync() }

// Replay hands every frame to the consumer in order; truncates a torn tail; returns an error on corruption in the middle.
func (w *WriteAheadLog) Replay(consumer func(Record) error) (ReplayResult, error) {
	fileSize := w.storage.Size()
	replayed := 0
	var offset int64
	var lastSeq uint64
	first := true
	for offset < fileSize {
		remaining := fileSize - offset
		if remaining < HeaderBytes {
			break // torn: header cut off
		}
		header := w.storage.Read(offset, HeaderBytes)
		length := binary.BigEndian.Uint32(header[0:4])
		storedCRC := binary.BigEndian.Uint32(header[4:8])
		seq := binary.BigEndian.Uint64(header[8:16])
		if length > MaxPayloadBytes {
			return ReplayResult{}, &CorruptRecordError{offset, fmt.Sprintf("implausible length %d", length)}
		}
		if remaining-HeaderBytes < int64(length) {
			break // torn: payload cut off
		}
		payload := w.storage.Read(offset+HeaderBytes, int(length))
		actualCRC := CRC32(seq, payload)
		isLast := offset+HeaderBytes+int64(length) == fileSize
		if actualCRC != storedCRC {
			if isLast {
				break // torn: tail record half-written
			}
			after := fileSize - (offset + HeaderBytes + int64(length))
			return ReplayResult{}, &CorruptRecordError{offset, fmt.Sprintf(
				"crc mismatch (stored %08x, computed %08x) with %d bytes after it", storedCRC, actualCRC, after)}
		}
		if !first && seq != lastSeq+1 {
			return ReplayResult{}, &CorruptRecordError{offset, fmt.Sprintf("sequence gap: expected %d, found %d", lastSeq+1, seq)}
		}
		if err := consumer(Record{seq, offset, payload}); err != nil {
			return ReplayResult{}, err
		}
		replayed++
		lastSeq = seq
		first = false
		offset += HeaderBytes + int64(length)
	}
	truncated := fileSize - offset
	if truncated > 0 {
		w.storage.Truncate(offset)
		w.storage.Sync()
	}
	w.end = offset
	if replayed > 0 {
		w.nextSeq = lastSeq + 1
	}
	return ReplayResult{replayed, offset, truncated, lastSeq}, nil
}

// Reset throws away every frame after a checkpoint made them redundant; numbering continues from nextSeq.
func (w *WriteAheadLog) Reset(nextSeq uint64) {
	if w.end > 0 {
		w.storage.Truncate(0)
		w.storage.Sync()
		w.end = 0
	}
	w.nextSeq = nextSeq
}

// ---------------------------------------------------------------- the ledger on top

const (
	OpCredit   byte = 1
	OpDebit    byte = 2
	OpTransfer byte = 3
	opMinBytes      = 1 + 2 + 2 + 3 + 8
)

var ErrInsufficientFunds = errors.New("insufficient funds")

// EncodeOp payload layout: u8 op | u16 fromLen | from | u16 toLen | to | 3 bytes currency | i64 minorUnits.
func EncodeOp(op byte, from, to string, amount Money) []byte {
	buf := make([]byte, 0, opMinBytes+len(from)+len(to))
	buf = append(buf, op)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(from)))
	buf = append(buf, from...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(to)))
	buf = append(buf, to...)
	buf = append(buf, amount.Currency.Code...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(amount.MinorUnits))
	return buf
}

// Ledger holds balances whose only durable state is two logs: the WAL and a snapshot log. Every mutation is
// framed, appended, and synced BEFORE the in-memory map changes. Checkpoint appends the whole map as one
// snapshot record (tagged with the WAL sequence it covers) and then empties the WAL.
type Ledger struct {
	wal          *WriteAheadLog
	snapshots    *WriteAheadLog
	currency     Currency
	balances     map[string]Money
	appliedSeq   uint64
	LastRecovery ReplayResult
}

// OpenLedger recovers: load the last complete snapshot, then replay WAL records newer than it.
func OpenLedger(walStorage, snapshotStorage Storage, currency Currency) (*Ledger, error) {
	l := &Ledger{
		wal:       NewWriteAheadLog(walStorage),
		snapshots: NewWriteAheadLog(snapshotStorage),
		currency:  currency,
		balances:  map[string]Money{},
	}
	var latest *Record
	if _, err := l.snapshots.Replay(func(r Record) error { latest = &r; return nil }); err != nil {
		return nil, err
	}
	if latest != nil {
		if err := l.loadSnapshot(latest.Payload); err != nil {
			return nil, err
		}
	}
	covered := l.appliedSeq
	result, err := l.wal.Replay(func(r Record) error {
		if r.Seq <= covered {
			return nil // already inside the snapshot: skip, do not double-apply
		}
		return l.apply(r)
	})
	if err != nil {
		return nil, err
	}
	l.LastRecovery = result
	if result.LastSeq <= covered {
		l.wal.Reset(covered + 1) // every WAL record (if any) is inside the snapshot: finish the interrupted checkpoint
	}
	return l, nil
}

func (l *Ledger) WalSize() int64         { return l.wal.Size() }
func (l *Ledger) SnapshotCount() uint64  { return l.snapshots.NextSeq() - 1 }
func (l *Ledger) AppliedSeq() uint64     { return l.appliedSeq }

func (l *Ledger) Balance(account string) Money {
	if m, ok := l.balances[account]; ok {
		return m
	}
	return OfMinor(0, l.currency)
}

// View renders balances in account order, matching the Java and Python demos.
func (l *Ledger) View() string {
	keys := make([]string, 0, len(l.balances))
	for k := range l.balances {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+l.balances[k].String())
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (l *Ledger) Credit(account string, amount Money) error {
	if err := l.requirePositive(amount); err != nil {
		return err
	}
	return l.commit(EncodeOp(OpCredit, account, "", amount))
}

func (l *Ledger) Debit(account string, amount Money) error {
	if err := l.requirePositive(amount); err != nil {
		return err
	}
	if l.Balance(account).Minus(amount).IsNegative() {
		return fmt.Errorf("%w in %s: %s < %s", ErrInsufficientFunds, account, l.Balance(account), amount)
	}
	return l.commit(EncodeOp(OpDebit, account, "", amount))
}

// Transfer is one record with two balance changes: a crash can never leave the money in flight.
func (l *Ledger) Transfer(from, to string, amount Money) error {
	if err := l.requirePositive(amount); err != nil {
		return err
	}
	if l.Balance(from).Minus(amount).IsNegative() {
		return fmt.Errorf("%w in %s: %s < %s", ErrInsufficientFunds, from, l.Balance(from), amount)
	}
	return l.commit(EncodeOp(OpTransfer, from, to, amount))
}

func (l *Ledger) requirePositive(amount Money) error {
	if amount.Currency != l.currency {
		return fmt.Errorf("ledger is %s", l.currency.Code)
	}
	if amount.MinorUnits <= 0 {
		return fmt.Errorf("amount must be positive: %s", amount)
	}
	return nil
}

// commit is write-ahead: append, sync, and only then mutate memory.
func (l *Ledger) commit(payload []byte) error {
	seq, err := l.wal.Append(payload)
	if err != nil {
		return err
	}
	l.wal.Sync()
	return l.apply(Record{seq, -1, payload})
}

func (l *Ledger) apply(r Record) error {
	p := r.Payload
	if len(p) < opMinBytes {
		return &CorruptRecordError{r.Offset, "payload shorter than op header"}
	}
	op := p[0]
	pos := 1
	from, pos, err := readString(p, pos, r.Offset)
	if err != nil {
		return err
	}
	to, pos, err := readString(p, pos, r.Offset)
	if err != nil {
		return err
	}
	if pos+11 > len(p) {
		return &CorruptRecordError{r.Offset, "payload shorter than amount"}
	}
	amount := OfMinor(int64(binary.BigEndian.Uint64(p[pos+3:pos+11])), Currency{string(p[pos : pos+3])})
	switch op {
	case OpCredit:
		l.balances[from] = l.Balance(from).Plus(amount)
	case OpDebit:
		l.balances[from] = l.Balance(from).Minus(amount)
	case OpTransfer:
		l.balances[from] = l.Balance(from).Minus(amount)
		l.balances[to] = l.Balance(to).Plus(amount)
	default:
		return &CorruptRecordError{r.Offset, fmt.Sprintf("unknown op %d", op)}
	}
	l.appliedSeq = r.Seq
	return nil
}

// Checkpoint: snapshot payload is u64 coveredSeq | u32 count | repeated (u16 len | account | 3 bytes currency | i64 minor).
// Append it to the snapshot log, sync, then empty the WAL. Crash before the sync: the snapshot frame is torn and
// ignored, the WAL is intact. Crash after the sync but before the reset: the WAL still holds records with
// seq <= coveredSeq, and OpenLedger skips them. Either way the recovered state is identical.
func (l *Ledger) Checkpoint() error {
	keys := make([]string, 0, len(l.balances))
	for k := range l.balances {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := binary.BigEndian.AppendUint64(nil, l.appliedSeq)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(keys)))
	for _, k := range keys {
		m := l.balances[k]
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(k)))
		buf = append(buf, k...)
		buf = append(buf, m.Currency.Code...)
		buf = binary.BigEndian.AppendUint64(buf, uint64(m.MinorUnits))
	}
	if _, err := l.snapshots.Append(buf); err != nil {
		return err
	}
	l.snapshots.Sync()
	l.wal.Reset(l.appliedSeq + 1)
	return nil
}

func (l *Ledger) loadSnapshot(payload []byte) error {
	if len(payload) < 12 {
		return &CorruptRecordError{-1, "snapshot shorter than header"}
	}
	l.appliedSeq = binary.BigEndian.Uint64(payload[0:8])
	count := binary.BigEndian.Uint32(payload[8:12])
	pos := 12
	for i := uint32(0); i < count; i++ {
		account, next, err := readString(payload, pos, -1)
		if err != nil {
			return err
		}
		pos = next
		if pos+11 > len(payload) {
			return &CorruptRecordError{-1, "snapshot entry truncated"}
		}
		l.balances[account] = OfMinor(int64(binary.BigEndian.Uint64(payload[pos+3:pos+11])), Currency{string(payload[pos : pos+3])})
		pos += 11
	}
	return nil
}

func readString(buf []byte, pos int, offset int64) (string, int, error) {
	if pos+2 > len(buf) {
		return "", pos, &CorruptRecordError{offset, "string length missing"}
	}
	length := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
	pos += 2
	if pos+length > len(buf) {
		return "", pos, &CorruptRecordError{offset, fmt.Sprintf("string length %d exceeds payload", length)}
	}
	return string(buf[pos : pos+length]), pos + length, nil
}

// ---------------------------------------------------------------- demo

func usd(cents int64) Money { return OfMinor(cents, USD) }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustOpen(wal, snap Storage) *Ledger {
	l, err := OpenLedger(wal, snap, USD)
	must(err)
	return l
}

func main() {
	fmt.Println("== One frame on disk ==")
	payload := []byte("hello")
	fmt.Printf("crc32(seq=1, \"hello\") = %08x\n", CRC32(1, payload))
	frame, _ := Encode(1, payload)
	fmt.Printf("frame = %x  (len | crc | seq | payload)\n", frame)

	fmt.Println("\n== Balances survive kill -9 ==")
	walDisk := NewMemoryStorage()
	snapDisk := NewMemoryStorage()
	ledger := mustOpen(walDisk, snapDisk)
	must(ledger.Credit("merchant:acme", usd(10_000)))
	must(ledger.Credit("customer:ann", usd(5_000)))
	must(ledger.Transfer("customer:ann", "merchant:acme", usd(1_999)))
	fmt.Printf("before crash: %s, wal bytes = %d, syncs = %d\n", ledger.View(), ledger.WalSize(), walDisk.SyncCount)
	walDisk.Crash() // the process dies; only synced bytes remain
	ledger = mustOpen(walDisk, snapDisk)
	fmt.Printf("after restart: %s, replayed %d records\n", ledger.View(), ledger.LastRecovery.RecordsReplayed)

	fmt.Println("\n== A torn final record is dropped, not guessed ==")
	cleanSize := ledger.WalSize()
	raw := NewWriteAheadLog(walDisk)
	_, _ = raw.Replay(func(Record) error { return nil })
	_, _ = raw.Append(EncodeOp(OpCredit, "customer:bob", "", usd(700)))
	walDisk.CrashKeepingUnsynced(9) // power fails 9 bytes into the write
	ledger = mustOpen(walDisk, snapDisk)
	r := ledger.LastRecovery
	fmt.Printf("replayed %d, truncated %d torn bytes, wal back to %d (clean size was %d)\n", r.RecordsReplayed, r.TruncatedBytes, r.ValidLength, cleanSize)
	fmt.Printf("customer:bob = %s\n", ledger.Balance("customer:bob"))

	fmt.Println("\n== A corrupt middle record stops replay ==")
	walDisk.FlipBit(HeaderBytes + 5) // flip one bit inside the first payload
	if _, err := OpenLedger(walDisk, snapDisk, USD); err != nil {
		fmt.Println("refused:", err)
	} else {
		fmt.Println("unexpected: replay succeeded")
	}
	walDisk.FlipBit(HeaderBytes + 5) // repair it so the demo can continue

	fmt.Println("\n== Checkpoint compacts the log; sequence numbers make it safe ==")
	ledger = mustOpen(walDisk, snapDisk)
	for i := 0; i < 50; i++ {
		must(ledger.Credit("merchant:acme", usd(1)))
	}
	fmt.Printf("50 credits: wal bytes = %d, applied seq = %d\n", ledger.WalSize(), ledger.AppliedSeq())
	must(ledger.Checkpoint())
	fmt.Printf("after checkpoint: wal bytes = %d, snapshots = %d, snapshot bytes = %d\n", ledger.WalSize(), ledger.SnapshotCount(), snapDisk.Size())
	must(ledger.Credit("customer:ann", usd(100)))
	ledger = mustOpen(walDisk, snapDisk)
	fmt.Printf("after restart: %s, replayed %d wal record(s) on top of the snapshot, next seq = %d\n",
		ledger.View(), ledger.LastRecovery.RecordsReplayed, ledger.AppliedSeq()+1)
}
