// Tests for phases/07-storage-and-transactions/02-write-ahead-logs. Run: go test ./...
package main

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func replayStrings(t *testing.T, log *WriteAheadLog) []string {
	t.Helper()
	seen := []string{}
	if _, err := log.Replay(func(r Record) error { seen = append(seen, string(r.Payload)); return nil }); err != nil {
		t.Fatalf("unexpected replay error: %v", err)
	}
	return seen
}

func mustAppend(t *testing.T, log *WriteAheadLog, payload string) uint64 {
	t.Helper()
	seq, err := log.Append([]byte(payload))
	if err != nil {
		t.Fatalf("append %q: %v", payload, err)
	}
	return seq
}

func TestCRC32CoversSeqAndPayload(t *testing.T) {
	if CRC32(1, []byte("hello")) == CRC32(2, []byte("hello")) {
		t.Fatal("same payload under different seq must have different crc")
	}
	if got := CRC32(1, []byte("hello")); got != 0x341ac4bc {
		t.Fatalf("crc32(1, hello) = %08x, want 341ac4bc (same as Java and Python)", got)
	}
}

func TestFrameLayout(t *testing.T) {
	frame, err := Encode(1, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != HeaderBytes+5 {
		t.Fatalf("frame length %d, want %d", len(frame), HeaderBytes+5)
	}
	if binary.BigEndian.Uint32(frame[0:4]) != 5 {
		t.Fatal("length prefix must be big-endian 5")
	}
	if binary.BigEndian.Uint64(frame[8:16]) != 1 {
		t.Fatal("seq must be stored at bytes 8..15")
	}
	if _, err := Encode(1, make([]byte, MaxPayloadBytes+1)); err == nil {
		t.Fatal("oversized payload must be rejected at encode")
	}
}

func TestRoundTripWithContiguousSequenceNumbers(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	s1, s2, s3 := mustAppend(t, log, "a"), mustAppend(t, log, "bb"), mustAppend(t, log, "")
	if s1 != 1 || s2 != 2 || s3 != 3 {
		t.Fatalf("seqs = %d %d %d, want 1 2 3", s1, s2, s3)
	}
	if log.Size() != 17+18+16 {
		t.Fatalf("size = %d, want 51", log.Size())
	}
	log.Sync()
	reopened := NewWriteAheadLog(disk)
	seen := []string{}
	result, err := reopened.Replay(func(r Record) error {
		seen = append(seen, string(rune('0'+r.Seq))+":"+string(r.Payload))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"1:a", "2:bb", "3:"}) {
		t.Fatalf("replay saw %v", seen)
	}
	if result.Torn() || result.RecordsReplayed != 3 || result.ValidLength != 51 || result.LastSeq != 3 {
		t.Fatalf("unexpected result %+v", result)
	}
	if reopened.NextSeq() != 4 {
		t.Fatalf("next seq = %d, want 4", reopened.NextSeq())
	}
}

func TestEmptyLogReplaysNothing(t *testing.T) {
	result, err := NewWriteAheadLog(NewMemoryStorage()).Replay(func(Record) error { return nil })
	if err != nil || result.RecordsReplayed != 0 || result.ValidLength != 0 || result.Torn() {
		t.Fatalf("empty log: %+v, %v", result, err)
	}
}

func TestUnsyncedAppendIsLostOnCrash(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	mustAppend(t, log, "synced")
	log.Sync()
	mustAppend(t, log, "not synced")
	disk.Crash()
	if got := replayStrings(t, NewWriteAheadLog(disk)); !reflect.DeepEqual(got, []string{"synced"}) {
		t.Fatalf("after crash saw %v", got)
	}
}

func TestTornHeaderIsDroppedAndStorageTruncated(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	mustAppend(t, log, "one")
	mustAppend(t, log, "two")
	log.Sync()
	mustAppend(t, log, "three")
	disk.CrashKeepingUnsynced(7) // header cut off mid-way
	reopened := NewWriteAheadLog(disk)
	if got := replayStrings(t, reopened); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("saw %v", got)
	}
	if disk.Size() != 19+19 {
		t.Fatalf("storage size %d, want 38 (truncated back to the valid prefix)", disk.Size())
	}
	if seq := mustAppend(t, reopened, "x"); seq != 3 {
		t.Fatalf("next append got seq %d, want the torn seq 3 reused", seq)
	}
}

func TestTornPayloadIsDroppedAndReported(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	mustAppend(t, log, "first")
	log.Sync()
	mustAppend(t, log, "second")
	disk.CrashKeepingUnsynced(HeaderBytes + 3) // payload cut off
	seen := []string{}
	result, err := NewWriteAheadLog(disk).Replay(func(r Record) error { seen = append(seen, string(r.Payload)); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"first"}) || !result.Torn() || result.TruncatedBytes != 19 {
		t.Fatalf("seen %v, result %+v", seen, result)
	}
}

func TestCorruptMiddleRecordStopsReplayWithOffset(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	for _, p := range []string{"one", "two", "three"} {
		mustAppend(t, log, p)
	}
	log.Sync()
	disk.FlipBit(19 + HeaderBytes) // first payload byte of record 2
	seen := []string{}
	_, err := NewWriteAheadLog(disk).Replay(func(r Record) error { seen = append(seen, string(r.Payload)); return nil })
	var corrupt *CorruptRecordError
	if !errors.As(err, &corrupt) {
		t.Fatalf("expected CorruptRecordError, got %v", err)
	}
	if corrupt.Offset != 19 || !strings.Contains(corrupt.Error(), "crc mismatch") {
		t.Fatalf("wrong error: %v", corrupt)
	}
	if !reflect.DeepEqual(seen, []string{"one"}) {
		t.Fatalf("records before the corruption should be delivered; saw %v", seen)
	}
	if disk.Size() != 19+19+21 {
		t.Fatal("a corrupt log must never be truncated")
	}
}

func TestChecksumFailedFinalFrameIsTreatedAsTorn(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	mustAppend(t, log, "first")
	mustAppend(t, log, "last")
	log.Sync()
	disk.FlipBit(disk.Size() - 1) // damage the final payload byte
	seen := []string{}
	result, err := NewWriteAheadLog(disk).Replay(func(r Record) error { seen = append(seen, string(r.Payload)); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"first"}) || !result.Torn() {
		t.Fatalf("seen %v, result %+v", seen, result)
	}
}

func TestSequenceGapIsCorruption(t *testing.T) {
	disk := NewMemoryStorage()
	f1, _ := Encode(1, []byte("one"))
	f3, _ := Encode(3, []byte("three")) // seq 2 is missing
	disk.Write(0, f1)
	disk.Write(19, f3)
	disk.Sync()
	_, err := NewWriteAheadLog(disk).Replay(func(Record) error { return nil })
	var corrupt *CorruptRecordError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("expected sequence gap error, got %v", err)
	}
}

func TestImplausibleLengthIsCorruption(t *testing.T) {
	disk := NewMemoryStorage()
	header := make([]byte, HeaderBytes)
	binary.BigEndian.PutUint32(header[0:4], 0xFFFFFFFF)
	binary.BigEndian.PutUint64(header[8:16], 1)
	disk.Write(0, header)
	disk.Write(HeaderBytes, make([]byte, 20))
	disk.Sync()
	_, err := NewWriteAheadLog(disk).Replay(func(Record) error { return nil })
	var corrupt *CorruptRecordError
	if !errors.As(err, &corrupt) {
		t.Fatalf("expected CorruptRecordError, got %v", err)
	}
}

func TestReplayIsIdempotent(t *testing.T) {
	disk := NewMemoryStorage()
	log := NewWriteAheadLog(disk)
	for _, p := range []string{"a", "b", "c"} {
		mustAppend(t, log, p)
	}
	log.Sync()
	first := replayStrings(t, NewWriteAheadLog(disk))
	second := replayStrings(t, NewWriteAheadLog(disk))
	if !reflect.DeepEqual(first, second) || len(first) != 3 {
		t.Fatalf("replay twice: %v vs %v", first, second)
	}
}

func TestLedgerCommitSyncsBeforeMemoryChanges(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, err := OpenLedger(wal, snap, USD)
	if err != nil {
		t.Fatal(err)
	}
	before := wal.SyncCount
	if err := ledger.Credit("acme", usd(10_000)); err != nil {
		t.Fatal(err)
	}
	if wal.SyncCount != before+1 {
		t.Fatalf("every commit syncs exactly once; syncs went %d -> %d", before, wal.SyncCount)
	}
	if wal.DurableSize() != wal.Size() || ledger.Balance("acme") != usd(10_000) {
		t.Fatal("commit must be durable before memory changes")
	}
}

func TestLedgerBalancesSurviveCrash(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("acme", usd(10_000)))
	must(ledger.Credit("ann", usd(5_000)))
	must(ledger.Transfer("ann", "acme", usd(1_999)))
	must(ledger.Debit("acme", usd(1)))
	wal.Crash()
	recovered, err := OpenLedger(wal, snap, USD)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Balance("acme") != usd(11_998) || recovered.Balance("ann") != usd(3_001) {
		t.Fatalf("balances after crash: %s", recovered.View())
	}
	if recovered.LastRecovery.RecordsReplayed != 4 || recovered.AppliedSeq() != 4 {
		t.Fatalf("recovery %+v, applied %d", recovered.LastRecovery, recovered.AppliedSeq())
	}
}

func TestLedgerRejectedOperationsAppendNothing(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("ann", usd(100)))
	size := wal.Size()
	if err := ledger.Debit("ann", usd(101)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if err := ledger.Credit("ann", usd(0)); err == nil {
		t.Fatal("zero amount must be rejected")
	}
	if wal.Size() != size {
		t.Fatal("rejected operations must append nothing")
	}
}

func TestLedgerTornTransferChangesNeitherBalance(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("acme", usd(1_000)))
	clean := wal.Size()
	raw := NewWriteAheadLog(wal)
	_, _ = raw.Replay(func(Record) error { return nil })
	_, _ = raw.Append(EncodeOp(OpTransfer, "acme", "ann", usd(500)))
	wal.CrashKeepingUnsynced(20)
	recovered, err := OpenLedger(wal, snap, USD)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Balance("acme") != usd(1_000) || recovered.Balance("ann") != usd(0) {
		t.Fatalf("torn transfer moved money: %s", recovered.View())
	}
	if !recovered.LastRecovery.Torn() || recovered.WalSize() != clean {
		t.Fatalf("torn transfer should be truncated from the wal: %+v", recovered.LastRecovery)
	}
}

func TestLedgerCheckpointTruncatesWalAndRecoversFromSnapshot(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("acme", usd(10_000)))
	for i := 0; i < 20; i++ {
		must(ledger.Credit("acme", usd(1)))
	}
	bulky := ledger.WalSize()
	must(ledger.Checkpoint())
	if ledger.WalSize() != 0 || bulky == 0 {
		t.Fatalf("checkpoint should empty the wal (was %d, now %d)", bulky, ledger.WalSize())
	}
	if ledger.SnapshotCount() != 1 || snap.Size() == 0 {
		t.Fatal("checkpoint writes one snapshot record")
	}
	must(ledger.Credit("bob", usd(700)))
	recovered, err := OpenLedger(wal, snap, USD)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Balance("acme") != usd(10_020) || recovered.Balance("bob") != usd(700) {
		t.Fatalf("snapshot + wal: %s", recovered.View())
	}
	if recovered.LastRecovery.RecordsReplayed != 1 {
		t.Fatalf("only post-checkpoint records replay from the wal; got %d", recovered.LastRecovery.RecordsReplayed)
	}
	if recovered.AppliedSeq() != 22 {
		t.Fatalf("sequence numbers continue across the checkpoint; applied = %d", recovered.AppliedSeq())
	}
}

func TestLedgerRecordsCoveredBySnapshotAreSkippedNotDoubleApplied(t *testing.T) {
	// Simulate a crash after the snapshot was synced but before the WAL was truncated.
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("a", usd(100)))
	must(ledger.Credit("a", usd(100)))
	walBytes := wal.Size()
	probe, _ := OpenLedger(wal, snap, USD)
	must(probe.Checkpoint()) // snapshot durable, wal reset...
	stale := NewMemoryStorage() // ...but on this disk the truncate never landed
	rebuild := NewWriteAheadLog(stale)
	_, _ = rebuild.Append(EncodeOp(OpCredit, "a", "", usd(100)))
	_, _ = rebuild.Append(EncodeOp(OpCredit, "a", "", usd(100)))
	rebuild.Sync()
	if stale.Size() != walBytes {
		t.Fatalf("rebuilt wal %d bytes, want %d", stale.Size(), walBytes)
	}
	recovered, err := OpenLedger(stale, snap, USD)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Balance("a") != usd(200) {
		t.Fatalf("covered records double-applied: %s", recovered.Balance("a"))
	}
	if recovered.WalSize() != 0 {
		t.Fatal("interrupted checkpoint should be finished on open")
	}
	must(recovered.Credit("a", usd(1)))
	if recovered.AppliedSeq() != 3 {
		t.Fatalf("numbering should resume after the covered seq; applied = %d", recovered.AppliedSeq())
	}
}

func TestLedgerOpeningTwiceYieldsSameState(t *testing.T) {
	wal, snap := NewMemoryStorage(), NewMemoryStorage()
	ledger, _ := OpenLedger(wal, snap, USD)
	must(ledger.Credit("k", usd(1)))
	must(ledger.Credit("k", usd(2)))
	first, _ := OpenLedger(wal, snap, USD)
	second, _ := OpenLedger(wal, snap, USD)
	if first.View() != second.View() || first.Balance("k") != usd(3) {
		t.Fatalf("replay not idempotent: %s vs %s", first.View(), second.View())
	}
}
