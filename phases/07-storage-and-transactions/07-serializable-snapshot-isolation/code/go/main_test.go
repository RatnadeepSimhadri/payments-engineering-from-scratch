// Tests for phases/07-storage-and-transactions/07-serializable-snapshot-isolation. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func seeded(t *testing.T) *SsiStore[int64] {
	t.Helper()
	store := NewSsiStore[int64](&TimestampOracle{})
	tx := store.Begin()
	write(t, store, tx, "A", 100)
	write(t, store, tx, "B", 100)
	commit(t, store, tx)
	return store
}

func write(t *testing.T, s *SsiStore[int64], tx *Transaction[int64], key string, v int64) {
	t.Helper()
	if err := s.Write(tx, key, v); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

func commit(t *testing.T, s *SsiStore[int64], tx *Transaction[int64]) int64 {
	t.Helper()
	ts, err := s.Commit(tx)
	if err != nil {
		t.Fatalf("commit %s: %v", tx, err)
	}
	return ts
}

func get(t *testing.T, s *SsiStore[int64], tx *Transaction[int64], key string) int64 {
	t.Helper()
	v, ok, err := s.Read(tx, key)
	if err != nil || !ok {
		t.Fatalf("read %s: present=%v err=%v", key, ok, err)
	}
	return v
}

func readIs(t *testing.T, s *SsiStore[int64], tx *Transaction[int64], key string, want int64) bool {
	t.Helper()
	v, ok, err := s.Read(tx, key)
	return err == nil && ok && v == want
}

func readEmpty(t *testing.T, s *SsiStore[int64], tx *Transaction[int64], key string) bool {
	t.Helper()
	_, ok, err := s.Read(tx, key)
	return err == nil && !ok
}

// withdraw is Lesson 06's write-skew program: check A+B >= amount, then withdraw from one account.
func withdraw(t *testing.T, s *SsiStore[int64], tx *Transaction[int64], from, other string, amount int64) bool {
	t.Helper()
	total := get(t, s, tx, from) + get(t, s, tx, other)
	if total < amount {
		return false
	}
	write(t, s, tx, from, get(t, s, tx, from)-amount)
	return true
}

func TestSIBehaviourUnchanged(t *testing.T) {
	si := seeded(t)
	r := si.Begin()
	w := si.Begin()
	write(t, si, w, "A", 40)
	assert(t, "buffered write invisible", readIs(t, si, r, "A", 100))
	commit(t, si, w)
	assert(t, "snapshot is stable after a concurrent commit", readIs(t, si, r, "A", 100))
	assert(t, "one rw edge: reader commits fine", commit(t, si, r) == r.StartTs && r.Status == Committed)
	x1 := si.Begin()
	x2 := si.Begin()
	write(t, si, x1, "A", 1)
	write(t, si, x2, "A", 2)
	commit(t, si, x1)
	_, err := si.Commit(x2)
	assert(t, "first committer still wins", errors.Is(err, ErrWriteConflict))
	assert(t, "write conflict carries the reason verbatim", err != nil && err.Error() == "first committer wins on A")
	assert(t, "a write conflict is not a serialization failure", !errors.Is(err, ErrSerializationFailure))
}

func TestWriteSkewAborted(t *testing.T) {
	skew := seeded(t)
	t1 := skew.Begin()
	t2 := skew.Begin()
	assert(t, "t1 withdraws on its snapshot", withdraw(t, skew, t1, "A", "B", 150))
	assert(t, "t2 withdraws on its snapshot", withdraw(t, skew, t2, "B", "A", 150))
	assert(t, "both are pivots before either commits", t1.IsPivot() && t2.IsPivot())
	assert(t, "edges recorded in both directions", t1.InConflicts.Size() == 1 && t1.InConflicts.Contains(t2.ID) &&
		t1.OutConflicts.Size() == 1 && t1.OutConflicts.Contains(t2.ID))
	_, err := skew.Commit(t1)
	assert(t, "first to commit is the pivot and aborts", errors.Is(err, ErrSerializationFailure))
	assert(t, "abort removed its edges from the survivor", t2.InConflicts.IsEmpty() && t2.OutConflicts.IsEmpty())
	assert(t, "survivor commits", commit(t, skew, t2) > 0 && t2.Status == Committed)
	audit := skew.Begin()
	assert(t, "invariant A+B >= 0 holds", get(t, skew, audit, "A")+get(t, skew, audit, "B") == 50)
	commit(t, skew, audit)
	assert(t, "exactly one abort", skew.Aborts() == 1 && skew.Commits() == 3)
	_, _, err = skew.Read(t1, "A")
	assert(t, "aborted transaction is unusable", errors.Is(err, ErrNotActive))
	assert(t, "the not-active error carries the abort reason", err != nil && strings.Contains(err.Error(), "is the pivot"))
}

func TestCommittedPivotAbortsNewcomer(t *testing.T) {
	skew2 := seeded(t)
	p := skew2.Begin()
	q := skew2.Begin()
	withdraw(t, skew2, p, "A", "B", 150)
	commit(t, skew2, p)
	assert(t, "pivot-to-be committed with one edge at most", p.Status == Committed)
	qSum := get(t, skew2, q, "A") + get(t, skew2, q, "B")
	assert(t, "q reads the old snapshot and records q -rw-> p", qSum == 200 && q.OutConflicts.Contains(p.ID) && p.InConflicts.Contains(q.ID))
	err := skew2.Write(q, "B", -50)
	assert(t, "q's write of B closes the structure: aborted immediately", errors.Is(err, ErrSerializationFailure))
	assert(t, "q is aborted with a reason naming the pivot", q.Status == Aborted && strings.Contains(q.AbortReason, "T2"))
}

func TestDisjointTransactions(t *testing.T) {
	dj := seeded(t)
	d1 := dj.Begin()
	d2 := dj.Begin()
	write(t, dj, d1, "A", get(t, dj, d1, "A")+1)
	write(t, dj, d2, "B", get(t, dj, d2, "B")+1)
	commit(t, dj, d1)
	commit(t, dj, d2)
	assert(t, "disjoint read/write sets both commit", dj.Commits() == 3 && dj.Aborts() == 0)
}

func TestSireadLocksRetained(t *testing.T) {
	ret := seeded(t)
	early := ret.Begin()
	late := ret.Begin()
	get(t, ret, early, "B")
	commit(t, ret, early)
	assert(t, "read lock retained after commit", containsID(ret.ReadersOf("B"), early.ID))
	assert(t, "only the setup txn is releasable while late is active", ret.ReleaseFinished() == 1 && containsID(ret.ReadersOf("B"), early.ID))
	write(t, ret, late, "B", 1)
	assert(t, "late writer gets an incoming edge from the committed reader", late.InConflicts.Contains(early.ID))
	commit(t, ret, late)
	assert(t, "single edge: late commits", late.Status == Committed)
	assert(t, "finished transactions released once nothing is concurrent", ret.ReleaseFinished() == 2 && ret.RetainedReadLocks() == 0)
}

func TestSerializableHistoryHasNoSpuriousAborts(t *testing.T) {
	ro := seeded(t)
	tA := ro.Begin() // will write A after reading B
	tB := ro.Begin() // will write B
	get(t, ro, tA, "B")
	write(t, ro, tB, "B", 1) // tA -rw-> tB
	commit(t, ro, tB)
	write(t, ro, tA, "A", 1)
	commit(t, ro, tA) // tA has only an out edge: fine
	tR := ro.Begin()  // started after tA's commit, so it is not concurrent with tA
	assert(t, "later read-only txn sees both commits and is not aborted", readIs(t, ro, tR, "A", 1) && readIs(t, ro, tR, "B", 1))
	commit(t, ro, tR)
	assert(t, "no spurious aborts in a serializable history", ro.Aborts() == 0)
}

func TestDeletesParticipate(t *testing.T) {
	del := seeded(t)
	rd := del.Begin()
	dl := del.Begin()
	get(t, del, rd, "A")
	if err := del.Delete(dl, "A"); err != nil {
		t.Fatal(err)
	}
	assert(t, "delete records rd -rw-> dl", rd.OutConflicts.Contains(dl.ID))
	commit(t, del, dl)
	assert(t, "reader keeps seeing the deleted key", readIs(t, del, rd, "A", 100))
	commit(t, del, rd)
	assert(t, "new snapshot sees the delete", readEmpty(t, del, del.Begin(), "A"))
}

func TestHelpers(t *testing.T) {
	s := newIDSet()
	s.Add(3)
	s.Add(1)
	s.Add(3)
	assert(t, "idSet keeps insertion order without duplicates", s.String() == "[3, 1]" && s.First() == 3)
	s.Remove(3)
	assert(t, "idSet remove", s.String() == "[1]" && !s.Contains(3))
	assert(t, "usd parses cents", usd("19.99").MinorUnits == 1999 && usd("-50.00").String() == "-50.00 USD")
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
