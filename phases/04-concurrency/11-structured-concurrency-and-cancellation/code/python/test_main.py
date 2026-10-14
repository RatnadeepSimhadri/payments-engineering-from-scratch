"""Tests for phases/04-concurrency/11-structured-concurrency-and-cancellation. Run: python3 -m unittest -v"""
import unittest

from main import (
    NANOS_PER_MILLI, USD, AtomicInteger, CancellationToken, CancelledException, CountDownLatch, Currency, Deadline,
    ExecutionException, ManualClock, Money, StructuredScope, Subtask, SystemClock, ThreadPerTaskExecutor, add_exact,
    millis, wait_for_signal_or_cancel,
)

State = Subtask.State


class DeadlineTest(unittest.TestCase):
    def test_composition_and_expiry(self):
        clock = ManualClock()
        d200 = Deadline.after(clock, millis(200))
        d800 = Deadline.after(clock, millis(800))
        self.assertEqual(d800.earliest(d200), d200, "earliest picks the tighter deadline")
        self.assertEqual(d200.earliest(d800), d200)
        self.assertEqual(Deadline.NONE.earliest(d200), d200, "NONE never wins over a real deadline")
        self.assertEqual(d200.remaining(clock) // NANOS_PER_MILLI, 200)
        self.assertFalse(d200.is_expired(clock))
        clock.advance(millis(200))
        self.assertTrue(d200.is_expired(clock), "expired exactly at the instant")
        self.assertEqual(d200.remaining(clock), 0)
        self.assertFalse(Deadline.NONE.is_expired(clock))

    def test_after_overflow_is_reproduced(self):
        with self.assertRaises(OverflowError):
            add_exact((1 << 63) - 1, 1)


class TokenTest(unittest.TestCase):
    def test_tree_idempotence_callbacks(self):
        c2 = ManualClock()
        root = CancellationToken.root(c2)
        child = root.child(Deadline.after(c2, millis(100)))
        grandchild = child.child(Deadline.after(c2, millis(900)))
        self.assertEqual(grandchild.deadline(), child.deadline(), "child deadline is inherited by grandchild")
        callbacks = AtomicInteger()
        grandchild.on_cancel(callbacks.increment_and_get)
        self.assertFalse(root.is_cancelled() or child.is_cancelled() or grandchild.is_cancelled())
        self.assertTrue(root.cancel("operator abort"), "first cancel wins")
        self.assertFalse(root.cancel("second reason"))
        self.assertEqual(root.reason(), "operator abort")
        self.assertTrue(child.is_cancelled() and grandchild.is_cancelled(), "cancel propagates down two levels")
        self.assertEqual(callbacks.get(), 1, "callback ran exactly once")
        grandchild.on_cancel(callbacks.increment_and_get)
        self.assertEqual(callbacks.get(), 2, "late registration runs immediately")
        with self.assertRaises(CancelledException):
            grandchild.throw_if_cancelled()

    def test_child_never_cancels_parent(self):
        c2 = ManualClock()
        parent2 = CancellationToken.root(c2)
        kid = parent2.child()
        kid.cancel("child only")
        self.assertTrue(kid.is_cancelled())
        self.assertFalse(parent2.is_cancelled(), "cancelling a child never cancels the parent")
        late_kid = kid.child()
        self.assertTrue(late_kid.is_cancelled(), "child of a cancelled token is born cancelled")
        self.assertEqual(late_kid.reason(), "child only")

    def test_lazy_deadline(self):
        c3 = ManualClock()
        timed = CancellationToken.root(c3).child(Deadline.after(c3, millis(50)))
        c3.advance(millis(49))
        self.assertFalse(timed.is_cancelled(), "deadline not yet reached")
        c3.advance(millis(1))
        self.assertTrue(timed.is_cancelled(), "deadline expiry becomes a cancellation lazily")
        self.assertEqual(timed.reason(), "deadline exceeded")


class ScopeTest(unittest.TestCase):
    def setUp(self):
        self.pool = ThreadPerTaskExecutor()
        self.sys = SystemClock()

    def tearDown(self):
        self.pool.shutdown()
        self.assertTrue(self.pool.await_termination(5), "no thread outlives the test")

    def test_all_succeed(self):
        with StructuredScope("ok", self.pool, CancellationToken.root(self.sys), clock=self.sys) as scope:
            a = scope.fork("a", lambda t: 1)
            b = scope.fork("b", lambda t: 2)
            scope.join()
            scope.throw_if_failed()
            self.assertIs(a.state(), State.SUCCESS)
            self.assertIs(b.state(), State.SUCCESS)
            self.assertEqual(a.get() + b.get(), 3, "values available after join")
            self.assertEqual(scope.active(), 0, "nothing active after join")
            with self.assertRaises(RuntimeError):
                scope.fork("late", lambda t: 3)

    def test_one_failure_cancels_siblings(self):
        cleanups = AtomicInteger()
        sibling_ready = CountDownLatch(1)

        def slow_body(t):
            t.on_cancel(cleanups.increment_and_get)
            sibling_ready.count_down()
            wait_for_signal_or_cancel(t)
            return 1

        def boom_body(t):
            sibling_ready.await_()
            raise RuntimeError("fraud rule tripped")

        with StructuredScope("fail", self.pool, CancellationToken.root(self.sys), clock=self.sys) as scope:
            slow = scope.fork("slow", slow_body)
            boom = scope.fork("boom", boom_body)
            scope.join()
            self.assertIs(boom.state(), State.FAILED)
            self.assertIsInstance(boom.exception(), RuntimeError)
            self.assertIs(slow.state(), State.CANCELLED, "sibling is CANCELLED, not FAILED")
            self.assertEqual(cleanups.get(), 1, "sibling cleanup ran once")
            self.assertIn("boom", scope.cancellation_reason())
            with self.assertRaises(RuntimeError):
                slow.get()
            with self.assertRaises(ExecutionException) as ctx:
                scope.throw_if_failed()
            self.assertEqual(str(ctx.exception.__cause__), "fraud rule tripped")
            self.assertEqual(scope.active(), 0, "nothing active after a failed join")

    def test_deadline_on_manual_clock(self):
        mc = ManualClock()
        started = CountDownLatch(1)

        def straggler_body(t):
            started.count_down()
            wait_for_signal_or_cancel(t)
            return 1

        with StructuredScope("deadline", self.pool, CancellationToken.root(mc), Deadline.after(mc, millis(300)),
                             mc) as scope:
            straggler = scope.fork("straggler", straggler_body)
            started.await_()
            mc.advance(millis(301))             # the clock moves; nobody sleeps
            scope.join()
            self.assertIs(straggler.state(), State.CANCELLED)
            self.assertEqual(scope.cancellation_reason(), "deadline exceeded")
            with self.assertRaises(TimeoutError):
                scope.throw_if_failed()

    def test_nested_scope_inherits_tighter_budget(self):
        nc = ManualClock()
        req_root = CancellationToken.root(nc)
        with StructuredScope("request", self.pool, req_root, Deadline.after(nc, millis(100)), nc) as outer:
            with StructuredScope("bank-call", self.pool, outer.token(), Deadline.after(nc, millis(5000)),
                                 nc) as inner:
                self.assertEqual(inner.deadline(), outer.deadline())
                inner.join()
            outer.join()

    def test_external_cancel(self):
        external = CancellationToken.root(self.sys)
        running = CountDownLatch(1)

        def body(t):
            running.count_down()
            wait_for_signal_or_cancel(t)
            return 1

        with StructuredScope("external", self.pool, external, clock=self.sys) as scope:
            worker = scope.fork("worker", body)
            running.await_()
            external.cancel("client disconnected")
            scope.join()
            self.assertIs(worker.state(), State.CANCELLED)
            self.assertEqual(scope.cancellation_reason(), "client disconnected")

    def test_close_without_join(self):
        completed_normally = AtomicInteger()
        all_started = CountDownLatch(3)

        def body(t):
            all_started.count_down()
            wait_for_signal_or_cancel(t)
            completed_normally.increment_and_get()
            return 1

        with StructuredScope("leaky", self.pool, CancellationToken.root(self.sys), clock=self.sys) as scope:
            leaked = scope
            for i in range(3):
                scope.fork(f"w{i}", body)
            all_started.await_()
        self.assertTrue(leaked.is_cancelled())
        self.assertIn("closed without join", leaked.cancellation_reason())
        self.assertEqual(leaked.active(), 0, "no subtask is still running after close")
        self.assertTrue(all(s.state() is State.CANCELLED for s in leaked.subtasks()))
        self.assertEqual(completed_normally.get(), 0)
        with self.assertRaises(RuntimeError):
            leaked.fork("late", lambda t: 1)

    def test_fork_under_cancelled_token_never_runs(self):
        dead = CancellationToken.root(self.sys)
        dead.cancel("already dead")
        body_runs = AtomicInteger()

        def body(t):
            body_runs.increment_and_get()
            return 1

        with StructuredScope("dead", self.pool, dead, clock=self.sys) as scope:
            never = scope.fork("never", body)
            scope.join()
            self.assertEqual(body_runs.get(), 0)
            self.assertIs(never.state(), State.CANCELLED)


class MoneyTest(unittest.TestCase):
    def test_money_guard(self):
        self.assertTrue(Money.of_minor(10_000, USD).covers(Money.of_minor(2_500, USD)))
        self.assertEqual(str(Money.of_minor(2_505, USD)), "25.05 USD")
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).covers(Money.of_minor(1, Currency("EUR", 2)))


if __name__ == "__main__":
    unittest.main()
