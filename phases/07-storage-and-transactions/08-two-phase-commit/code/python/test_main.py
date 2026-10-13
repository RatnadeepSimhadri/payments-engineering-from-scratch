"""Tests for phases/07-storage-and-transactions/08-two-phase-commit. Run: python3 -m unittest -v"""

import unittest

from main import (
    USD,
    Coordinator,
    CrashError,
    CrashPoint,
    Decision,
    Ledger,
    Posting,
    SimClock,
    SimNetwork,
    TxState,
    Vote,
    usd,
)


class World:
    """A fresh two-ledger world: alice holds 500.00 at bank-a, bob holds 100.00 at bank-b."""

    def __init__(self) -> None:
        self.clock = SimClock(0)
        self.net = SimNetwork(self.clock, 5, 1_000)
        self.a = Ledger("bank-a", {"alice": usd("500.00")})
        self.b = Ledger("bank-b", {"bob": usd("100.00")})
        self.net.register(self.a)
        self.net.register(self.b)
        self.coord = Coordinator(self.net)

    def total(self):
        return self.a.total(USD).plus(self.b.total(USD))


class TwoPhaseCommitTest(unittest.TestCase):
    def test_happy_path_commits_atomically(self) -> None:
        w = World()
        out = w.coord.transfer("tx-1", w.a, "alice", w.b, "bob", usd("150.00"))
        self.assertEqual(out.decision, Decision.COMMIT)
        self.assertTrue(out.complete)
        self.assertEqual(w.a.balance("alice"), usd("350.00"))
        self.assertEqual(w.b.balance("bob"), usd("250.00"))
        self.assertEqual(w.total(), usd("600.00"))
        self.assertFalse(w.a.is_locked("alice") or w.b.is_locked("bob"))

    def test_logs_record_the_protocol(self) -> None:
        w = World()
        w.coord.transfer("tx-1", w.a, "alice", w.b, "bob", usd("150.00"))
        self.assertEqual([r.kind for r in w.coord.log_records()], ["BEGIN", "DECISION", "END"])
        self.assertEqual([r.kind for r in w.a.log_records()], ["PREPARED", "COMMITTED"])

    def test_participant_votes_no_on_insufficient_funds(self) -> None:
        w = World()
        out = w.coord.transfer("tx-2", w.a, "alice", w.b, "bob", usd("900.00"))
        self.assertEqual(out.decision, Decision.ABORT)
        self.assertIn("voted NO", out.reason)
        self.assertEqual(w.a.balance("alice"), usd("500.00"))
        self.assertEqual(w.b.balance("bob"), usd("100.00"))
        self.assertFalse(w.a.is_locked("alice") or w.b.is_locked("bob"))
        self.assertEqual(w.coord.decision_of("tx-2"), Decision.ABORT)

    def test_prepare_timeout_is_a_no_vote(self) -> None:
        w = World()
        w.net.partition("bank-b")
        out = w.coord.transfer("tx-3", w.a, "alice", w.b, "bob", usd("10.00"))
        self.assertEqual(out.decision, Decision.ABORT)
        self.assertIn("timed out", out.reason)
        self.assertGreaterEqual(w.clock.now(), 1_000)
        self.assertEqual(w.a.state_of("tx-3"), TxState.ABORTED)
        self.assertFalse(w.a.is_locked("alice"))
        self.assertIn("tx-3", w.coord.pending_transactions())
        w.net.heal("bank-b")
        self.assertEqual(w.coord.retry_pending(), 1)
        self.assertEqual(w.coord.pending_transactions(), set())

    def test_coordinator_crash_before_decision_blocks_participants(self) -> None:
        w = World()
        w.coord.crash_point = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG
        with self.assertRaises(CrashError):
            w.coord.transfer("tx-4", w.a, "alice", w.b, "bob", usd("25.00"))
        self.assertEqual(w.a.in_doubt(), {"tx-4"})
        self.assertEqual(w.b.in_doubt(), {"tx-4"})
        self.assertTrue(w.a.is_locked("alice") and w.b.is_locked("bob"))
        self.assertEqual(w.a.prepare("tx-5", Posting("alice", usd("-1.00"))), Vote.NO)

    def test_participant_recovery_keeps_in_doubt_lock(self) -> None:
        w = World()
        w.coord.crash_point = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG
        with self.assertRaises(CrashError):
            w.coord.transfer("tx-4", w.a, "alice", w.b, "bob", usd("25.00"))
        recovered = w.b.crash_and_recover()
        self.assertEqual(recovered.in_doubt(), {"tx-4"})
        self.assertTrue(recovered.is_locked("bob"))
        self.assertEqual(recovered.balance("bob"), usd("100.00"))

    def test_coordinator_recovery_presumes_abort(self) -> None:
        w = World()
        w.coord.crash_point = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG
        with self.assertRaises(CrashError):
            w.coord.transfer("tx-4", w.a, "alice", w.b, "bob", usd("25.00"))
        w.coord = Coordinator.recover(w.coord, w.net)
        self.assertEqual(w.coord.decision_of("tx-4"), Decision.ABORT)
        self.assertEqual(w.a.state_of("tx-4"), TxState.ABORTED)
        self.assertEqual(w.b.state_of("tx-4"), TxState.ABORTED)
        self.assertFalse(w.a.is_locked("alice") or w.b.is_locked("bob"))
        self.assertEqual(w.total(), usd("600.00"))

    def test_coordinator_recovery_redelivers_commit(self) -> None:
        w = World()
        w.coord.crash_point = CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND
        with self.assertRaises(CrashError):
            w.coord.transfer("tx-6", w.a, "alice", w.b, "bob", usd("40.00"))
        self.assertEqual((w.a.state_of("tx-6"), w.b.state_of("tx-6")), (TxState.PREPARED, TxState.PREPARED))
        w.coord = Coordinator.recover(w.coord, w.net)
        self.assertEqual((w.a.state_of("tx-6"), w.b.state_of("tx-6")), (TxState.COMMITTED, TxState.COMMITTED))
        self.assertEqual(w.a.balance("alice"), usd("460.00"))
        self.assertEqual(w.b.balance("bob"), usd("140.00"))
        self.assertEqual(w.coord.log_records()[-1].kind, "END")
        # recovery is idempotent
        again = Coordinator.recover(w.coord, w.net)
        self.assertEqual(again.pending_transactions(), set())
        self.assertEqual(w.a.balance("alice"), usd("460.00"))

    def test_participant_crash_after_vote_then_retry(self) -> None:
        w = World()
        w.net.crash_after_vote("bank-b")
        out = w.coord.transfer("tx-7", w.a, "alice", w.b, "bob", usd("5.00"))
        self.assertEqual(out.decision, Decision.COMMIT)
        self.assertFalse(out.complete)
        self.assertEqual(w.a.state_of("tx-7"), TxState.COMMITTED)
        self.assertEqual(w.b.state_of("tx-7"), TxState.PREPARED)
        self.assertEqual(w.coord.retry_pending(), 0)
        recovered = w.b.crash_and_recover()
        self.assertEqual(recovered.in_doubt(), {"tx-7"})
        w.net.replace(recovered)
        w.net.heal("bank-b")
        self.assertEqual(w.coord.retry_pending(), 1)
        self.assertEqual(recovered.state_of("tx-7"), TxState.COMMITTED)
        self.assertEqual(recovered.balance("bob"), usd("105.00"))

    def test_commit_and_abort_are_idempotent_and_guarded(self) -> None:
        w = World()
        w.coord.transfer("tx-1", w.a, "alice", w.b, "bob", usd("5.00"))
        w.b.commit("tx-1")  # duplicate commit: no-op
        self.assertEqual(w.b.balance("bob"), usd("105.00"))
        with self.assertRaises(RuntimeError):
            w.b.abort("tx-1")
        with self.assertRaises(RuntimeError):
            w.a.commit("never-prepared")
        w.a.abort("never-prepared")  # abort from NONE is legal
        self.assertEqual(w.a.state_of("never-prepared"), TxState.ABORTED)

    def test_three_participants_one_no_releases_earlier_yes(self) -> None:
        w = World()
        c = Ledger("bank-c", {"carol": usd("0.00")})
        w.net.register(c)
        postings = {
            "bank-a": Posting("alice", usd("-10.00")),
            "bank-c": Posting("carol", usd("-1.00")),
            "bank-b": Posting("bob", usd("11.00")),
        }
        out = w.coord.run("tx-8", postings)
        self.assertEqual(out.decision, Decision.ABORT)
        self.assertEqual(w.a.state_of("tx-8"), TxState.ABORTED)
        self.assertFalse(w.a.is_locked("alice"))
        self.assertEqual(w.b.state_of("tx-8"), TxState.ABORTED)

    def test_money_is_never_a_float(self) -> None:
        with self.assertRaises(TypeError):
            Ledger("x", {"a": __import__("main").Money(1.0, USD)})


if __name__ == "__main__":
    unittest.main()
