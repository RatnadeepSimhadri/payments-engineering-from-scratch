// Tests for phases/07-storage-and-transactions/08-two-phase-commit. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

type world struct {
	clock *SimClock
	net   *SimNetwork
	a, b  *Ledger
	coord *Coordinator
}

// newWorld: alice holds 500.00 at bank-a, bob holds 100.00 at bank-b.
func newWorld() *world {
	w := &world{clock: &SimClock{}}
	w.net = NewSimNetwork(w.clock, 5, 1000)
	w.a = NewLedger("bank-a", map[string]Money{"alice": USDOf("500.00")})
	w.b = NewLedger("bank-b", map[string]Money{"bob": USDOf("100.00")})
	w.net.Register(w.a)
	w.net.Register(w.b)
	w.coord = NewCoordinator(w.net)
	return w
}

func (w *world) total() Money { return mustPlus(w.a.Total(USD), w.b.Total(USD)) }

func TestHappyPathCommitsAtomically(t *testing.T) {
	w := newWorld()
	out, err := w.coord.Transfer("tx-1", w.a, "alice", w.b, "bob", USDOf("150.00"))
	if err != nil || out.Decision != Commit || !out.Complete {
		t.Fatalf("expected complete COMMIT, got %v %v", out, err)
	}
	if w.a.Balance("alice") != USDOf("350.00") || w.b.Balance("bob") != USDOf("250.00") {
		t.Fatalf("balances wrong: %v %v", w.a.Balance("alice"), w.b.Balance("bob"))
	}
	if w.total() != USDOf("600.00") {
		t.Fatalf("money not conserved: %v", w.total())
	}
	if w.a.IsLocked("alice") || w.b.IsLocked("bob") {
		t.Fatal("locks should be released after commit")
	}
}

func TestLogsRecordTheProtocol(t *testing.T) {
	w := newWorld()
	w.coord.Transfer("tx-1", w.a, "alice", w.b, "bob", USDOf("150.00"))
	recs := w.coord.LogRecords()
	if len(recs) != 3 || recs[0].Kind != "BEGIN" || recs[1].Kind != "DECISION" || recs[2].Kind != "END" {
		t.Fatalf("coordinator log wrong: %v", recs)
	}
	pr := w.a.LogRecords()
	if len(pr) != 2 || pr[0].Kind != "PREPARED" || pr[1].Kind != "COMMITTED" {
		t.Fatalf("participant log wrong: %v", pr)
	}
}

func TestParticipantVotesNoOnInsufficientFunds(t *testing.T) {
	w := newWorld()
	out, _ := w.coord.Transfer("tx-2", w.a, "alice", w.b, "bob", USDOf("900.00"))
	if out.Decision != Abort || out.Reason != "bank-a voted NO" {
		t.Fatalf("expected abort on NO vote, got %v", out)
	}
	if w.a.Balance("alice") != USDOf("500.00") || w.b.Balance("bob") != USDOf("100.00") {
		t.Fatal("abort must leave balances untouched")
	}
	if w.a.IsLocked("alice") || w.b.IsLocked("bob") {
		t.Fatal("abort must leave no locks")
	}
	if d, ok := w.coord.DecisionOf("tx-2"); !ok || d != Abort {
		t.Fatal("abort must be logged at the coordinator")
	}
}

func TestPrepareTimeoutIsANoVote(t *testing.T) {
	w := newWorld()
	w.net.Partition("bank-b")
	out, _ := w.coord.Transfer("tx-3", w.a, "alice", w.b, "bob", USDOf("10.00"))
	if out.Decision != Abort || out.Complete {
		t.Fatalf("expected incomplete abort, got %v", out)
	}
	if w.clock.Now() < 1000 {
		t.Fatalf("clock should have advanced by the timeout, now=%d", w.clock.Now())
	}
	if w.a.StateOf("tx-3") != StateAborted || w.a.IsLocked("alice") {
		t.Fatal("bank-a must be released after abort")
	}
	if len(w.coord.PendingTransactions()) != 1 {
		t.Fatal("abort delivery to the partitioned node should be pending")
	}
	w.net.Heal("bank-b")
	if w.coord.RetryPending() != 1 || len(w.coord.PendingTransactions()) != 0 {
		t.Fatal("retry should finish delivery")
	}
}

func TestCoordinatorCrashBeforeDecisionBlocksParticipants(t *testing.T) {
	w := newWorld()
	w.coord.CrashPoint = AfterVotesBeforeDecisionLog
	_, err := w.coord.Transfer("tx-4", w.a, "alice", w.b, "bob", USDOf("25.00"))
	if !errors.Is(err, ErrCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	if len(w.a.InDoubt()) != 1 || len(w.b.InDoubt()) != 1 {
		t.Fatal("both participants should be in doubt")
	}
	if !w.a.IsLocked("alice") || !w.b.IsLocked("bob") {
		t.Fatal("in-doubt accounts must stay locked")
	}
	if w.a.Prepare("tx-5", Posting{"alice", USDOf("-1.00")}) != VoteNo {
		t.Fatal("a second transaction on a locked account must be refused")
	}
}

func TestParticipantRecoveryKeepsInDoubtLock(t *testing.T) {
	w := newWorld()
	w.coord.CrashPoint = AfterVotesBeforeDecisionLog
	w.coord.Transfer("tx-4", w.a, "alice", w.b, "bob", USDOf("25.00"))
	recovered := w.b.CrashAndRecover()
	if got := recovered.InDoubt(); len(got) != 1 || got[0] != "tx-4" || !recovered.IsLocked("bob") {
		t.Fatalf("recovered participant should still be in doubt, got %v", got)
	}
	if recovered.Balance("bob") != USDOf("100.00") {
		t.Fatal("recovery must not apply an undecided posting")
	}
}

func TestCoordinatorRecoveryPresumesAbort(t *testing.T) {
	w := newWorld()
	w.coord.CrashPoint = AfterVotesBeforeDecisionLog
	w.coord.Transfer("tx-4", w.a, "alice", w.b, "bob", USDOf("25.00"))
	w.coord = RecoverCoordinator(w.coord, w.net)
	if d, ok := w.coord.DecisionOf("tx-4"); !ok || d != Abort {
		t.Fatal("recovery without a decision record must presume abort")
	}
	if w.a.StateOf("tx-4") != StateAborted || w.b.StateOf("tx-4") != StateAborted {
		t.Fatal("recovery must release the participants")
	}
	if w.a.IsLocked("alice") || w.b.IsLocked("bob") || w.total() != USDOf("600.00") {
		t.Fatal("locks released and money conserved")
	}
}

func TestCoordinatorRecoveryRedeliversCommit(t *testing.T) {
	w := newWorld()
	w.coord.CrashPoint = AfterDecisionLogBeforeSend
	if _, err := w.coord.Transfer("tx-6", w.a, "alice", w.b, "bob", USDOf("40.00")); !errors.Is(err, ErrCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	if w.a.StateOf("tx-6") != StatePrepared || w.b.StateOf("tx-6") != StatePrepared {
		t.Fatal("participants should still be prepared")
	}
	w.coord = RecoverCoordinator(w.coord, w.net)
	if w.a.StateOf("tx-6") != StateCommitted || w.b.StateOf("tx-6") != StateCommitted {
		t.Fatal("recovery must redeliver COMMIT")
	}
	if w.a.Balance("alice") != USDOf("460.00") || w.b.Balance("bob") != USDOf("140.00") {
		t.Fatalf("balances after recovery wrong: %v %v", w.a.Balance("alice"), w.b.Balance("bob"))
	}
	recs := w.coord.LogRecords()
	if recs[len(recs)-1].Kind != "END" {
		t.Fatal("recovered log must end with END")
	}
	again := RecoverCoordinator(w.coord, w.net)
	if len(again.PendingTransactions()) != 0 || w.a.Balance("alice") != USDOf("460.00") {
		t.Fatal("recovery must be idempotent")
	}
}

func TestParticipantCrashAfterVoteThenRetry(t *testing.T) {
	w := newWorld()
	w.net.CrashAfterVote("bank-b")
	out, _ := w.coord.Transfer("tx-7", w.a, "alice", w.b, "bob", USDOf("5.00"))
	if out.Decision != Commit || out.Complete {
		t.Fatalf("expected incomplete COMMIT, got %v", out)
	}
	if w.a.StateOf("tx-7") != StateCommitted || w.b.StateOf("tx-7") != StatePrepared {
		t.Fatal("reachable participant committed, crashed one still prepared")
	}
	if w.coord.RetryPending() != 0 {
		t.Fatal("retry while partitioned makes no progress")
	}
	recovered := w.b.CrashAndRecover()
	if len(recovered.InDoubt()) != 1 {
		t.Fatal("recovered participant should be in doubt")
	}
	w.net.Replace(recovered)
	w.net.Heal("bank-b")
	if w.coord.RetryPending() != 1 || recovered.StateOf("tx-7") != StateCommitted {
		t.Fatal("retry after heal must complete")
	}
	if recovered.Balance("bob") != USDOf("105.00") {
		t.Fatalf("posting applied once, got %v", recovered.Balance("bob"))
	}
}

func TestCommitAndAbortAreIdempotentAndGuarded(t *testing.T) {
	w := newWorld()
	w.coord.Transfer("tx-1", w.a, "alice", w.b, "bob", USDOf("5.00"))
	if err := w.b.Commit("tx-1"); err != nil || w.b.Balance("bob") != USDOf("105.00") {
		t.Fatal("duplicate commit must be a no-op")
	}
	if err := w.b.Abort("tx-1"); !errors.Is(err, ErrIllegalState) {
		t.Fatal("abort after commit must be refused")
	}
	if err := w.a.Commit("never-prepared"); !errors.Is(err, ErrIllegalState) {
		t.Fatal("commit without prepare must be refused")
	}
	if err := w.a.Abort("never-prepared"); err != nil || w.a.StateOf("never-prepared") != StateAborted {
		t.Fatal("abort from NONE is legal")
	}
}

func TestThreeParticipantsOneNoReleasesEarlierYes(t *testing.T) {
	w := newWorld()
	c := NewLedger("bank-c", map[string]Money{"carol": USDOf("0.00")})
	w.net.Register(c)
	out, _ := w.coord.Run("tx-8", []string{"bank-a", "bank-c", "bank-b"}, map[string]Posting{
		"bank-a": {"alice", USDOf("-10.00")},
		"bank-c": {"carol", USDOf("-1.00")},
		"bank-b": {"bob", USDOf("11.00")},
	})
	if out.Decision != Abort {
		t.Fatal("one NO must abort the whole transaction")
	}
	if w.a.StateOf("tx-8") != StateAborted || w.a.IsLocked("alice") {
		t.Fatal("earlier YES voter must be released")
	}
	if w.b.StateOf("tx-8") != StateAborted {
		t.Fatal("later participant never prepared but is told to abort")
	}
}

func TestMoneyRejectsCurrencyMix(t *testing.T) {
	_, err := USDOf("1.00").Plus(Money{100, Currency{"EUR", 2}})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("expected currency mismatch")
	}
}
