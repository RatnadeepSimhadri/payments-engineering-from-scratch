// Tests for phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func seeded(t *testing.T) *MvccStore[int64] {
	t.Helper()
	store := NewMvccStore[int64](&TimestampOracle{})
	tx := store.Begin()
	write(t, store, tx, "A", 100)
	write(t, store, tx, "B", 100)
	commit(t, store, tx)
	return store
}

func write(t *testing.T, s *MvccStore[int64], tx *Transaction[int64], key string, v int64) {
	t.Helper()
	if err := s.Write(tx, key, v); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

func commit(t *testing.T, s *MvccStore[int64], tx *Transaction[int64]) int64 {
	t.Helper()
	ts, err := s.Commit(tx)
	if err != nil {
		t.Fatalf("commit %s: %v", tx, err)
	}
	return ts
}

// readIs reports whether Read returns exactly (want, true) with no error.
func readIs(t *testing.T, s *MvccStore[int64], tx *Transaction[int64], key string, want int64) bool {
	t.Helper()
	v, ok, err := s.Read(tx, key)
	return err == nil && ok && v == want
}

// readEmpty reports whether Read returns absent with no error.
func readEmpty(t *testing.T, s *MvccStore[int64], tx *Transaction[int64], key string) bool {
	t.Helper()
	_, ok, err := s.Read(tx, key)
	return err == nil && !ok
}

func get(t *testing.T, s *MvccStore[int64], tx *Transaction[int64], key string) int64 {
	t.Helper()
	v, ok, err := s.Read(tx, key)
	if err != nil || !ok {
		t.Fatalf("read %s: present=%v err=%v", key, ok, err)
	}
	return v
}

func TestSnapshotReads(t *testing.T) {
	s := seeded(t)
	r := s.Begin()
	assert(t, "snapshot sees committed setup", readIs(t, s, r, "A", 100))
	w := s.Begin()
	write(t, s, w, "A", 40)
	assert(t, "buffered write invisible to other txn", readIs(t, s, r, "A", 100))
	assert(t, "buffered write visible to itself", readIs(t, s, w, "A", 40))
	ts := commit(t, s, w)
	assert(t, "commit returns a fresh timestamp", ts == 2)
	assert(t, "reader keeps its snapshot after a concurrent commit", readIs(t, s, r, "A", 100))
	assert(t, "new snapshot sees the commit", readIs(t, s, s.Begin(), "A", 40))
	assert(t, "missing key reads empty", readEmpty(t, s, r, "Z"))
	assert(t, "read-only commit consumes no timestamp", commit(t, s, r) == r.StartTs)
	assert(t, "two versions of A exist", s.VersionCount("A") == 2)
	assert(t, "transaction prints as T<id>@<snapshot>", r.String() == "T2@1")
}

func TestFirstCommitterWins(t *testing.T) {
	f := seeded(t)
	t1 := f.Begin()
	t2 := f.Begin()
	write(t, f, t1, "A", 90)
	write(t, f, t2, "A", 75)
	commit(t, f, t1)
	_, err := f.Commit(t2)
	assert(t, "second committer on the same key aborts", errors.Is(err, ErrWriteConflict))
	assert(t, "conflict message names the winner", err != nil && err.Error() == "write conflict: T3@1 lost A to T2 (committed at 2 > snapshot 1)")
	assert(t, "loser is marked ABORTED", t2.Status == Aborted)
	assert(t, "loser's write never became a version", readIs(t, f, f.Begin(), "A", 90))
	assert(t, "abort counted", f.Aborts() == 1 && f.Commits() == 2)
	_, _, err = f.Read(t2, "A")
	assert(t, "aborted txn cannot be used", errors.Is(err, ErrNotActive))
	assert(t, "aborted txn cannot write either", errors.Is(f.Write(t2, "A", 1), ErrNotActive))

	// a later writer that started after the commit is not a conflict
	t3 := f.Begin()
	write(t, f, t3, "A", 80)
	commit(t, f, t3)
	assert(t, "writer with a fresh snapshot commits", readIs(t, f, f.Begin(), "A", 80))
}

func TestWriteSkewAllowed(t *testing.T) {
	skew := seeded(t)
	w1 := skew.Begin()
	w2 := skew.Begin()
	sum1 := get(t, skew, w1, "A") + get(t, skew, w1, "B")
	sum2 := get(t, skew, w2, "A") + get(t, skew, w2, "B")
	if sum1 >= 150 {
		write(t, skew, w1, "A", get(t, skew, w1, "A")-150)
	}
	if sum2 >= 150 {
		write(t, skew, w2, "B", get(t, skew, w2, "B")-150)
	}
	commit(t, skew, w1)
	commit(t, skew, w2)
	audit := skew.Begin()
	assert(t, "write skew: both commit under SI", skew.Commits() == 3)
	assert(t, "write skew: invariant A+B >= 0 is violated", get(t, skew, audit, "A")+get(t, skew, audit, "B") == -100)
}

func TestDeletesAndTombstones(t *testing.T) {
	d := seeded(t)
	old := d.Begin()
	del := d.Begin()
	if err := d.Delete(del, "B"); err != nil {
		t.Fatal(err)
	}
	assert(t, "own delete reads as absent", readEmpty(t, d, del, "B"))
	commit(t, d, del)
	assert(t, "older snapshot still sees deleted key", readIs(t, d, old, "B", 100))
	assert(t, "new snapshot does not see deleted key", readEmpty(t, d, d.Begin(), "B"))
	assert(t, "tombstone is a version", d.VersionCount("B") == 2 && d.History("B")[1].IsTombstone())
}

func TestVacuumRespectsOldestSnapshot(t *testing.T) {
	g := seeded(t)
	pin := g.Begin()
	for i := 0; i < 5; i++ {
		tx := g.Begin()
		write(t, g, tx, "A", get(t, g, tx, "A")+1)
		commit(t, g, tx)
	}
	assert(t, "six versions of A before vacuum", g.VersionCount("A") == 6)
	assert(t, "vacuum with pinned snapshot removes nothing visible to it", g.Vacuum() == 0 && g.VersionCount("A") == 6)
	assert(t, "pinned reader still sees its version", readIs(t, g, pin, "A", 100))
	commit(t, g, pin)
	assert(t, "vacuum after pin released removes dead versions", g.Vacuum() == 5 && g.VersionCount("A") == 1)
	after := g.Begin()
	assert(t, "surviving version is the newest", readIs(t, g, after, "A", 105))
	commit(t, g, after)
	del2 := g.Begin()
	if err := g.Delete(del2, "B"); err != nil {
		t.Fatal(err)
	}
	commit(t, g, del2)
	assert(t, "vacuum removes a dead tombstone and its key", g.Vacuum() == 2 && g.VersionCount("B") == 0)
	assert(t, "no active transactions left", g.ActiveCount() == 0)
	assert(t, "total versions after vacuum", g.TotalVersions() == 1)
}

func TestMoneyHelpers(t *testing.T) {
	assert(t, "usd parses cents", usd("19.99").MinorUnits == 1999 && usd("5").MinorUnits == 500)
	diff, err := usd("-50.00").Minus(usd("0.05"))
	assert(t, "money toString", err == nil && diff.String() == "-50.05 USD")
	_, err = usd("1.00").Plus(OfMinor(100, Currency{"EUR", 2}))
	assert(t, "plus rejects a currency mismatch", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "plus reports overflow instead of wrapping", errors.Is(err, ErrOverflow))
}
