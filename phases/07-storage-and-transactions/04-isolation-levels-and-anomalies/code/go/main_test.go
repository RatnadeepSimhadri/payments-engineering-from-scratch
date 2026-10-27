// Tests for phases/07-storage-and-transactions/04-isolation-levels-and-anomalies. Run: go test ./...
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

func readValue(t *testing.T, tx *Txn, key string) int64 {
	t.Helper()
	v, ok, err := tx.Read(key)
	if err != nil || !ok {
		t.Fatalf("T%d read %s: present=%v err=%v", tx.ID, key, ok, err)
	}
	return v
}

// The matrix is Berenson et al. (1995), Table 4.
func TestMatrix(t *testing.T) {
	m := AnomalyLab{}.Matrix()
	cases := []struct {
		name    string
		anomaly string
		allowed map[Isolation]bool
	}{
		{"dirty read: only READ_UNCOMMITTED allows it", "dirty read",
			map[Isolation]bool{ReadUncommitted: true, ReadCommitted: false, RepeatableRead: false, Snapshot: false, Serializable: false}},
		{"non-repeatable read: RU and RC allow it; RR, SNAPSHOT, SERIALIZABLE prevent it", "non-repeatable read",
			map[Isolation]bool{ReadUncommitted: true, ReadCommitted: true, RepeatableRead: false, Snapshot: false, Serializable: false}},
		{"phantom: lock-based RR still allows it; SNAPSHOT and SERIALIZABLE prevent it", "phantom",
			map[Isolation]bool{ReadUncommitted: true, ReadCommitted: true, RepeatableRead: true, Snapshot: false, Serializable: false}},
		{"lost update: RU and RC allow it; RR, SNAPSHOT, SERIALIZABLE prevent it", "lost update",
			map[Isolation]bool{ReadUncommitted: true, ReadCommitted: true, RepeatableRead: false, Snapshot: false, Serializable: false}},
		{"write skew: RU, RC and SNAPSHOT allow it; RR and SERIALIZABLE prevent it", "write skew",
			map[Isolation]bool{ReadUncommitted: true, ReadCommitted: true, RepeatableRead: false, Snapshot: true, Serializable: false}},
	}
	for _, c := range cases {
		t.Run(c.anomaly, func(t *testing.T) {
			for _, l := range Levels {
				if m[c.anomaly][l] != c.allowed[l] {
					t.Errorf("%s: at %s observed=%v, want %v", c.name, l, m[c.anomaly][l], c.allowed[l])
				}
			}
		})
	}
	noneSerializable, allRU := true, true
	for _, a := range Anomalies {
		if m[a][Serializable] {
			noneSerializable = false
		}
		if !m[a][ReadUncommitted] {
			allRU = false
		}
	}
	assert(t, "SERIALIZABLE allows no anomaly at all", noneSerializable)
	assert(t, "READ_UNCOMMITTED allows every anomaly", allRU)
	assert(t, "RR and SNAPSHOT are incomparable: each allows one the other prevents",
		m["phantom"][RepeatableRead] != m["phantom"][Snapshot] && m["write skew"][RepeatableRead] != m["write skew"][Snapshot])
	_, err := AnomalyLab{}.Run("time travel", Snapshot)
	assert(t, "unknown anomaly is an error", errors.Is(err, ErrUnknownAnomaly))
}

// The scenarios say WHY, not just whether.
func TestScenarios(t *testing.T) {
	skew := WriteSkew(Snapshot)
	assert(t, "write skew under SNAPSHOT: both commit and the combined balance is -800", skew.Observed && strings.HasPrefix(skew.Detail, "combined balance -800"))
	skewSer := WriteSkew(Serializable)
	assert(t, "write skew under SERIALIZABLE: exactly one transaction aborts", strings.Contains(skewSer.Detail, "T1 ABORTED") && strings.Contains(skewSer.Detail, "T2 COMMITTED"))
	lu := LostUpdate(Snapshot)
	assert(t, "lost update under SNAPSHOT: first committer wins, T2 aborted", strings.Contains(lu.Detail, "T1 COMMITTED, T2 ABORTED"))
	named := false
	for _, line := range lu.Trace {
		if strings.Contains(line, "first-committer-wins") {
			named = true
		}
	}
	assert(t, "lost update under SNAPSHOT: trace names the rule", named)
	luRc := LostUpdate(ReadCommitted)
	assert(t, "lost update under READ_COMMITTED: final balance 1200, one deposit vanished", strings.HasPrefix(luRc.Detail, "final balance 1200"))
	dr := DirtyRead(ReadUncommitted)
	assert(t, "dirty read under RU: T2 saw 900 though committed value is 1000", dr.Detail == "T2 saw 900; committed value is 1000")
	ph := Phantom(RepeatableRead)
	assert(t, "phantom under RR: 2 then 3 postings", ph.Detail == "T1 counted 2 then 3 postings")
	assert(t, "phantom trace prints the scan in sorted order", ph.Trace[2] == "  T1 scan posting:m42:* = {posting:m42:001=500, posting:m42:002=700}")
}

func TestReadUncommittedMechanics(t *testing.T) {
	s := NewStore()
	s.Load("a", 10)
	t1 := s.Begin(ReadUncommitted)
	if err := t1.Write("a", 5); err != nil {
		t.Fatal(err)
	}
	assert(t, "RU writes in place", s.Committed["a"] == 5)
	t1.Abort()
	assert(t, "RU abort restores the old value", s.Committed["a"] == 10)
	assert(t, "abort releases locks", len(s.Locks) == 0)
	assert(t, "abort twice is a no-op", func() bool { t1.Abort(); return t1.Status == Aborted && len(s.Trace) == 3 }())
}

func TestReadCommittedMechanics(t *testing.T) {
	s2 := NewStore()
	s2.Load("a", 10)
	w := s2.Begin(ReadCommitted)
	if err := w.Write("a", 7); err != nil {
		t.Fatal(err)
	}
	assert(t, "RC write is buffered, not visible", s2.Committed["a"] == 10 && readValue(t, w, "a") == 7)
	other := s2.Begin(ReadCommitted)
	err := other.Write("a", 99)
	assert(t, "no dirty writes at any level: second X lock conflicts", errors.Is(err, ErrConflict))
	var ce *ConflictError
	assert(t, "conflict error names the holder", errors.As(err, &ce) && ce.Msg == "T2 X-lock a blocked by X of T1")
	assert(t, "conflict aborted the requester", other.Status == Aborted)
	_, _, err = other.Read("a")
	assert(t, "an aborted transaction cannot be used", errors.Is(err, ErrNotActive))
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "RC commit publishes the write and bumps the clock", s2.Committed["a"] == 7 && s2.CommitClock == 1 && s2.VersionOf["a"] == 1)
	assert(t, "commit twice is an error", errors.Is(w.Commit(), ErrNotActive))
}

func TestRepeatableReadMechanics(t *testing.T) {
	s3 := NewStore()
	s3.Load("x", 1)
	r := s3.Begin(RepeatableRead)
	readValue(t, r, "x")
	r2 := s3.Begin(RepeatableRead)
	assert(t, "RR: two readers share the S lock", readValue(t, r2, "x") == 1 && len(s3.Locks["x"].Shared) == 2)
	assert(t, "RR: writer conflicts with another reader's S lock", errors.Is(r2.Write("x", 2), ErrConflict))
	if err := r.Write("x", 3); err != nil {
		t.Fatal(err)
	}
	assert(t, "RR: sole reader upgrades S to X", s3.Locks["x"].Exclusive == r.ID)
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "RR: commit applies buffered write", s3.Committed["x"] == 3)
}

func TestSerializableMechanics(t *testing.T) {
	s4 := NewStore()
	s4.Load("p:1", 1)
	scanner := s4.Begin(Serializable)
	rows, err := scanner.Scan("p:")
	assert(t, "SERIALIZABLE scan sees existing rows", err == nil && len(rows) == 1)
	inserter := s4.Begin(Serializable)
	assert(t, "SERIALIZABLE: insert under a predicate lock conflicts", errors.Is(inserter.Write("p:2", 2), ErrConflict))
	elsewhere := s4.Begin(Serializable)
	if err := elsewhere.Write("q:1", 5); err != nil {
		t.Fatal(err)
	}
	if err := elsewhere.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "SERIALIZABLE: write outside the predicate is allowed", s4.Committed["q:1"] == 5)
	if err := scanner.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "commit releases predicate locks", len(s4.PredicateLocks) == 0)
}

func TestSnapshotMechanics(t *testing.T) {
	s5 := NewStore()
	s5.Load("k", 1)
	snap := s5.Begin(Snapshot)
	writer := s5.Begin(ReadCommitted)
	if err := writer.Write("k", 2); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "SNAPSHOT reads the value as of begin", readValue(t, snap, "k") == 1)
	if err := snap.Write("k", 10); err != nil {
		t.Fatal(err)
	}
	assert(t, "SNAPSHOT: first-committer-wins rejects a write over a newer commit", errors.Is(snap.Commit(), ErrConflict))
	assert(t, "SNAPSHOT: the committed value is the first committer's", s5.Committed["k"] == 2)
	snapReadOnly := s5.Begin(Snapshot)
	w2 := s5.Begin(ReadCommitted)
	if err := w2.Write("k", 3); err != nil {
		t.Fatal(err)
	}
	if err := w2.Commit(); err != nil {
		t.Fatal(err)
	}
	readValue(t, snapReadOnly, "k")
	assert(t, "SNAPSHOT: a transaction with no writes always commits", snapReadOnly.Commit() == nil && snapReadOnly.Status == Committed)
	_, present, err := snapReadOnly.Read("missing")
	assert(t, "reading a missing key on a finished transaction is ErrNotActive", !present && errors.Is(err, ErrNotActive))
}
