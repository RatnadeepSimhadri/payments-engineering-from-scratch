// Tests for phases/07-storage-and-transactions/11-change-data-capture. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func seeded(t *testing.T) *Database {
	t.Helper()
	db := NewDatabase()
	if _, err := db.Begin().Put("accounts", "acc_alice", Account(10_000, 1)).Put("accounts", "acc_bob", Account(5_000, 1)).Commit(); err != nil {
		t.Fatal(err)
	}
	transfer(t, db, "pay_001", "acc_alice", "acc_bob", 2_500)
	return db
}

func transfer(t *testing.T, db *Database, id, from, to string, amount int64) {
	t.Helper()
	if _, err := Transfer(db, id, from, to, amount); err != nil {
		t.Fatalf("transfer %s: %v", id, err)
	}
}

func readFrom(t *testing.T, log *CommitLog, after int64, max int) []LogRecord {
	t.Helper()
	out, err := log.ReadFrom(after, max)
	if err != nil {
		t.Fatalf("readFrom(%d): %v", after, err)
	}
	return out
}

func poll(t *testing.T, c *CdcConnector, max int) int {
	t.Helper()
	n, err := c.Poll(max)
	if err != nil {
		t.Fatalf("poll %s: %v", c.Name, err)
	}
	return n
}

func pollCrash(t *testing.T, c *CdcConnector, max int) int {
	t.Helper()
	n, err := c.PollWithCrash(max, true)
	if err != nil {
		t.Fatalf("poll(crash) %s: %v", c.Name, err)
	}
	return n
}

func snapshot(t *testing.T, c *CdcConnector, table string) int64 {
	t.Helper()
	lsn, err := c.Snapshot(table)
	if err != nil {
		t.Fatalf("snapshot %s: %v", c.Name, err)
	}
	return lsn
}

func lsns(events []ChangeEvent) []int64 {
	out := []int64{}
	for _, e := range events {
		out = append(out, e.Lsn)
	}
	return out
}

func balanceOf(t *testing.T, v *BalanceViewSink, id string) int64 {
	t.Helper()
	m, ok := v.Balance(id)
	if !ok {
		t.Fatalf("no balance for %s", id)
	}
	return m.MinorUnits
}

func col(row *Row, c string) string {
	v, _ := row.Get(c)
	return v
}

func TestCommitLog(t *testing.T) {
	db := seeded(t)
	all := readFrom(t, db.Log(), 0, 100)
	assert(t, "two transactions produce five records", len(all) == 5)
	assert(t, "LSNs are 1..5 in order", all[0].Lsn == 1 && all[4].Lsn == 5)
	increasing := true
	for i := 1; i < len(all); i++ {
		increasing = increasing && all[i].Lsn == all[i-1].Lsn+1
	}
	assert(t, "LSNs strictly increase by one", increasing)
	assert(t, "records of one transaction share a txId", all[2].TxID == all[3].TxID && all[3].TxID == all[4].TxID)
	assert(t, "insert has null before", all[0].Op == Insert && all[0].Before == nil)
	assert(t, "update carries before and after images",
		all[3].Op == Update && col(all[3].Before, "balance_minor") == "10000" && col(all[3].After, "balance_minor") == "7500")
	assert(t, "headLsn is the last record", db.Log().HeadLsn() == 5)
	assert(t, "readFrom is exclusive of the offset", readFrom(t, db.Log(), 3, 100)[0].Lsn == 4)
	assert(t, "readFrom honours max", len(readFrom(t, db.Log(), 0, 2)) == 2)
	assert(t, "readFrom past head is empty", len(readFrom(t, db.Log(), 5, 100)) == 0)

	rolled := db.Begin().Put("accounts", "acc_carol", Account(1, 1))
	if err := rolled.Rollback(); err != nil {
		t.Fatal(err)
	}
	assert(t, "rollback writes nothing to the log", db.Log().HeadLsn() == 5 && db.Read("accounts", "acc_carol") == nil)
	_, err := rolled.Commit()
	assert(t, "finished transaction cannot commit", errors.Is(err, ErrTxnFinished))
	_, err = rolled.Put("accounts", "x", Account(1, 1)).Commit()
	assert(t, "put after finish is reported by commit", errors.Is(err, ErrTxnFinished))
	none, err := db.Begin().Delete("accounts", "acc_nobody").Commit()
	assert(t, "deleting a missing row logs nothing", err == nil && none == 0 && db.Log().HeadLsn() == 5)
	if _, err := db.Begin().Delete("accounts", "acc_bob").Commit(); err != nil {
		t.Fatal(err)
	}
	del := readFrom(t, db.Log(), 5, 1)[0]
	assert(t, "delete has before image and null after", del.Op == Delete && del.After == nil && col(del.Before, "balance_minor") == "7500")
	// log records are immutable: the row handed to Put is copied, so mutating the caller's Row changes nothing
	original := Account(42, 1)
	if _, err := db.Begin().Put("accounts", "acc_dan", original).Commit(); err != nil {
		t.Fatal(err)
	}
	original.set("balance_minor", "tampered")
	rec := readFrom(t, db.Log(), 6, 1)[0]
	assert(t, "log records are immutable", col(rec.After, "balance_minor") == "42" && col(db.Read("accounts", "acc_dan"), "balance_minor") == "42")
}

func TestEnvelopeMapping(t *testing.T) {
	db := seeded(t)
	all := readFrom(t, db.Log(), 0, 100)
	if _, err := db.Begin().Delete("accounts", "acc_bob").Commit(); err != nil {
		t.Fatal(err)
	}
	del := readFrom(t, db.Log(), 5, 1)[0]
	c := FromLog(all[0], 7)
	u := FromLog(all[3], 7)
	d := FromLog(del, 7)
	assert(t, "INSERT maps to op c", c.Op == "c" && !c.Snapshot)
	assert(t, "UPDATE maps to op u", u.Op == "u")
	assert(t, "DELETE maps to op d", d.Op == "d" && d.After == nil)
	assert(t, "event keeps lsn, txId and timestamp", u.Lsn == 4 && u.TxID == 2 && u.TsMillis == 7)
	r := SnapshotRow("accounts", "acc_x", Account(1, 1), 42, 7)
	assert(t, "snapshot row is op r at the snapshot lsn", r.Op == "r" && r.Snapshot && r.Lsn == 42 && r.Before == nil)
	assert(t, "describe prints the Debezium-shaped line", Describe(u) == "lsn=4 tx=2 op=u accounts/acc_alice before=10000 after=7500")
	assert(t, "describe prints a full row when there is no balance column",
		Describe(FromLog(all[2], 7)) == "lsn=3 tx=2 op=c payments/pay_001 before=null after={from=acc_alice, to=acc_bob, amount_minor=2500, currency=USD}")
}

func TestConnectorOffsets(t *testing.T) {
	clock := NewFakeClock(1_000)
	db2 := seeded(t)
	offsets := NewOffsetStore()
	rec := &RecordingSink{}
	conn := NewCdcConnector("rec", db2, offsets, rec, clock)
	assert(t, "fresh consumer starts at offset 0", conn.Offset() == 0)
	assert(t, "poll delivers in LSN order", poll(t, conn, 3) == 3 && slices.Equal(lsns(rec.Events), []int64{1, 2, 3}))
	assert(t, "offset advances to last delivered lsn", conn.Offset() == 3 && offsets.Commits == 1)
	assert(t, "second poll continues from the offset", poll(t, conn, 100) == 2 && slices.Equal(lsns(rec.Events), []int64{1, 2, 3, 4, 5}))
	assert(t, "poll with nothing new delivers nothing and commits nothing", poll(t, conn, 100) == 0 && offsets.Commits == 2)
	assert(t, "events carry the injected clock", rec.Events[0].TsMillis == 1_000)
}

func TestAtLeastOnce(t *testing.T) {
	clock := NewFakeClock(1_000)
	db3 := seeded(t)
	off3 := NewOffsetStore()
	rec3 := &RecordingSink{}
	crashy := NewCdcConnector("crashy", db3, off3, rec3, clock)
	pollCrash(t, crashy, 3)
	assert(t, "crash before commit leaves offset at 0", crashy.Offset() == 0 && len(rec3.Events) == 3)
	poll(t, crashy, 100)
	assert(t, "restart redelivers the uncommitted batch", slices.Equal(lsns(rec3.Events), []int64{1, 2, 3, 1, 2, 3, 4, 5}))

	naive := NewNaiveTotalsSink()
	exact := NewIdempotentTotalsSink()
	naiveConn := NewCdcConnector("naive", db3, NewOffsetStore(), naive, clock)
	exactConn := NewCdcConnector("exact", db3, NewOffsetStore(), exact, clock)
	pollCrash(t, naiveConn, 5)
	poll(t, naiveConn, 100)
	pollCrash(t, exactConn, 5)
	poll(t, exactConn, 100)
	assert(t, "naive sink double counts after redelivery", naive.PaymentsSeen == 2 && naive.Total.MinorUnits == 5_000)
	assert(t, "idempotent sink counts each payment once", exact.PaymentsSeen() == 1 && exact.Total.MinorUnits == 2_500)
}

func TestSnapshotThenStream(t *testing.T) {
	clock := NewFakeClock(1_000)
	db4 := seeded(t)
	off4 := NewOffsetStore()
	view := NewBalanceViewSink()
	boot := NewCdcConnector("view", db4, off4, view, clock)
	snap := snapshot(t, boot, "accounts")
	assert(t, "snapshot lsn is the log head at snapshot time", snap == 5)
	assert(t, "snapshot delivers one r event per row", view.Applied == 2 && balanceOf(t, view, "acc_alice") == 7_500)
	assert(t, "snapshot stores the snapshot lsn as the offset", boot.Offset() == 5)
	transfer(t, db4, "pay_002", "acc_bob", "acc_alice", 1_000)
	assert(t, "stream resumes exactly after the snapshot lsn", poll(t, boot, 100) == 3 && boot.Offset() == 8)
	assert(t, "view equals the database after catch-up", balanceOf(t, view, "acc_alice") == 8_500 && balanceOf(t, view, "acc_bob") == 6_500)

	transfer(t, db4, "pay_003", "acc_alice", "acc_bob", 300)
	pollCrash(t, boot, 3)
	appliedBefore := view.Applied
	poll(t, boot, 100)
	assert(t, "replayed account events are skipped by lsn", view.Applied == appliedBefore && view.Skipped == 2)
	assert(t, "view still correct after replay", balanceOf(t, view, "acc_alice") == 8_200 && balanceOf(t, view, "acc_bob") == 6_800)
	if err := view.Apply(SnapshotRow("accounts", "acc_alice", Account(1, 1), 5, 0)); err != nil {
		t.Fatal(err)
	}
	assert(t, "a stale snapshot row for an already-advanced key is ignored", balanceOf(t, view, "acc_alice") == 8_200)
	if err := view.Apply(FromLog(NewLogRecord(999, 50, Delete, "accounts", "acc_bob", Account(1, 1), nil), 0)); err != nil {
		t.Fatal(err)
	}
	_, bobPresent := view.Balance("acc_bob")
	assert(t, "delete removes the key from the view", !bobPresent && view.Size() == 1)
	if err := view.Apply(FromLog(NewLogRecord(1, 1, Insert, "payments", "pay_x", nil, Payment("a", "b", 1)), 0)); err != nil {
		t.Fatal(err)
	}
	assert(t, "view ignores other tables", view.Size() == 1)
	err := view.Apply(FromLog(NewLogRecord(1000, 51, Insert, "accounts", "acc_bad", nil, NewRow("balance_minor", "12.34")), 0))
	assert(t, "a non-integer amount is an error, never a float", err != nil && view.Size() == 1)
}

func TestRetention(t *testing.T) {
	clock := NewFakeClock(1_000)
	db5 := seeded(t)
	assert(t, "truncateBefore removes old records", db5.Log().TruncateBefore(4) == 3 && db5.Log().OldestLsn() == 4 && db5.Log().Size() == 2)
	assert(t, "reading at the retention boundary works", len(readFrom(t, db5.Log(), 3, 100)) == 2)
	_, err := db5.Log().ReadFrom(0, 100)
	var truncated *LogTruncatedError
	assert(t, "reading before the retention boundary fails", errors.As(err, &truncated) && truncated.Requested == 1 && truncated.OldestAvailable == 4)
	assert(t, "the truncation error matches its sentinel", errors.Is(err, ErrLogTruncated))
	lateConn := NewCdcConnector("late", db5, NewOffsetStore(), &RecordingSink{}, clock)
	_, err = lateConn.Poll(100)
	assert(t, "a consumer whose offset was truncated cannot poll", errors.As(err, &truncated))
	lateView := NewBalanceViewSink()
	recovered := NewCdcConnector("late2", db5, NewOffsetStore(), lateView, clock)
	snapshot(t, recovered, "accounts")
	assert(t, "re-snapshot recovers the full state", poll(t, recovered, 100) == 0 && balanceOf(t, lateView, "acc_alice") == 7_500 && balanceOf(t, lateView, "acc_bob") == 7_500)
}

func TestMoney(t *testing.T) {
	assert(t, "Money renders minor units", OfMinor(8_200, USD).String() == "82.00 USD")
	_, err := OfMinor(1, USD).Plus(OfMinor(1, Currency{"EUR", 2}))
	assert(t, "Money refuses mixed currencies", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "Money refuses to overflow", errors.Is(err, ErrOverflow))
	diff, err := OfMinor(500, USD).Minus(OfMinor(700, USD))
	assert(t, "Money minus", err == nil && diff.String() == "-2.00 USD")
}
