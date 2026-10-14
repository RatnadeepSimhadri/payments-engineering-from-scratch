"""Tests for phases/04-concurrency/08-the-actor-model. Run: python3 -m unittest -v"""
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor

from main import (
    EUR, AccountActor, ActorSystem, BalanceIs, CountIs, CounterActor, CurrencyMismatchError, Deposit, Deposited,
    DeterministicScheduler, GetBalance, Increment, InsufficientFunds, JavaRandom, Money, ProbeActor,
    SupervisorStrategy, ThreadedScheduler, Transfer, TransferCoordinator, Withdraw, Withdrawn, Crash, actor_of,
    bank_with, checked_int64, scripted_transfers, total, usd,
)

RESUME, RESTART, STOP = SupervisorStrategy.RESUME, SupervisorStrategy.RESTART, SupervisorStrategy.STOP


def run_script(seed: int) -> list[str]:
    sched = DeterministicScheduler(seed)
    s = bank_with(sched, True)
    probe = s.spawn("probe", ProbeActor, RESUME)
    for t in scripted_transfers(probe):
        s.ref("transfers").tell(t)
    sched.run_until_idle()
    return list(s.trace())


class MoneyTest(unittest.TestCase):
    def test_money(self):
        self.assertEqual(str(usd(12_345)), "123.45 USD")
        self.assertTrue(usd(100).minus(usd(150)).is_negative())
        with self.assertRaises(CurrencyMismatchError):
            usd(1).plus(Money.of_minor(1, EUR))

    def test_int64_overflow_is_reproduced(self):
        with self.assertRaises(OverflowError):
            checked_int64((1 << 63))

    def test_java_random_matches_jdk(self):
        # new java.util.Random(42).nextInt(10) x5 == 0, 3, 8, 4, 0 in the JDK
        r = JavaRandom(42)
        self.assertEqual([r.next_int(10) for _ in range(5)], [0, 3, 8, 4, 0])


class SingleAccountTest(unittest.TestCase):
    def test_single_account(self):
        sched = DeterministicScheduler(0)
        s = ActorSystem(sched, True)
        probe = s.spawn("probe", ProbeActor, RESUME)
        acct = s.spawn("acct-1", lambda: AccountActor("acct-1", usd(10_000)), RESUME)
        acct.tell(Deposit("d1", usd(2_500), probe))
        acct.tell(Withdraw("w1", usd(1_999), probe))
        acct.tell(Withdraw("w2", usd(50_000), probe))
        acct.tell(GetBalance(probe))
        self.assertEqual(actor_of(s, "acct-1").balance(), usd(10_000), "nothing happens until the scheduler runs")
        self.assertEqual(actor_of(s, "probe").received, [])
        delivered = sched.run_until_idle()
        self.assertEqual(delivered, 8, "every message delivered exactly once")
        self.assertEqual(sched.steps(), 8)
        got = actor_of(s, "probe").received
        self.assertIsInstance(got[0], Deposited)
        self.assertEqual(got[0].balance, usd(12_500))
        self.assertIsInstance(got[1], Withdrawn)
        self.assertEqual((got[1].amount, got[1].balance), (usd(1_999), usd(10_501)))
        self.assertIsInstance(got[2], InsufficientFunds)
        self.assertEqual((got[2].requested, got[2].balance), (usd(50_000), usd(10_501)))
        self.assertIsInstance(got[3], BalanceIs)
        self.assertEqual(got[3].balance, usd(10_501))
        self.assertEqual(actor_of(s, "acct-1").balance(), usd(10_501), "refused withdrawal left the balance untouched")
        acct_trace = [line for line in s.trace() if line.startswith("acct-1 <- ")]
        self.assertTrue(acct_trace[0].startswith("acct-1 <- Deposit"))
        self.assertTrue(acct_trace[1].startswith("acct-1 <- Withdraw(w1"))
        self.assertTrue(acct_trace[2].startswith("acct-1 <- Withdraw(w2"))
        self.assertEqual(acct_trace[3], "acct-1 <- GetBalance")
        with self.assertRaises(ValueError):
            AccountActor("x", usd(-1))


class TransfersTest(unittest.TestCase):
    def test_transfers(self):
        sched = DeterministicScheduler(0)
        s = bank_with(sched, True)
        probe = s.spawn("probe", ProbeActor, RESUME)
        s.ref("transfers").tell(Transfer("ok", "acct-A", "acct-B", usd(7_500), probe))
        s.ref("transfers").tell(Transfer("bad", "acct-C", "acct-A", usd(99_999), probe))
        sched.run_until_idle()
        results = actor_of(s, "probe").transfer_results()
        self.assertEqual(len(results), 2)
        ok = next(r for r in results if r.transfer_id == "ok")
        bad = next(r for r in results if r.transfer_id == "bad")
        self.assertTrue(ok.ok)
        self.assertEqual(actor_of(s, "acct-A").balance(), usd(2_500))
        self.assertEqual(actor_of(s, "acct-B").balance(), usd(17_500))
        self.assertFalse(bad.ok)
        self.assertEqual(bad.reason, "acct-C has 100.00 USD, needs 999.99 USD")
        self.assertEqual(actor_of(s, "acct-C").balance(), usd(10_000), "failed transfer moved nothing")
        coordinator = actor_of(s, "transfers")
        self.assertIsInstance(coordinator, TransferCoordinator)
        self.assertEqual((coordinator.completed(), coordinator.rejected(), coordinator.in_flight()), (1, 1, 0))
        self.assertIn("acct-A <- Withdraw(ok, 75.00 USD)", s.trace())
        self.assertIn("acct-B <- Deposit(ok, 75.00 USD)", s.trace())


class DeterminismTest(unittest.TestCase):
    def test_determinism(self):
        self.assertEqual(run_script(42), run_script(42), "same seed, same trace")
        self.assertEqual(run_script(0), run_script(0), "round-robin seed 0 is reproducible too")
        base = run_script(1)
        self.assertTrue(any(run_script(seed) != base for seed in range(2, 11)), "different seeds differ")
        self.assertEqual(len(run_script(3)), 32)
        self.assertEqual(len(run_script(9)), 32)

    def test_conservation_across_seeds(self):
        conserved = never_negative = all_answered = nothing_in_flight = True
        for seed in range(25):
            sched = DeterministicScheduler(seed)
            s = bank_with(sched, False)
            probe = s.spawn("probe", ProbeActor, RESUME)
            for t in scripted_transfers(probe):
                s.ref("transfers").tell(t)
            sched.run_until_idle()
            conserved &= total(s, "acct-A", "acct-B", "acct-C") == usd(30_000)
            for a in ("acct-A", "acct-B", "acct-C"):
                never_negative &= not actor_of(s, a).balance().is_negative()
            all_answered &= len(actor_of(s, "probe").transfer_results()) == 6
            nothing_in_flight &= actor_of(s, "transfers").in_flight() == 0
        self.assertTrue(conserved, "money is conserved under 25 interleavings")
        self.assertTrue(never_negative, "no balance ever goes negative")
        self.assertTrue(all_answered, "every transfer gets exactly one result")
        self.assertTrue(nothing_in_flight, "coordinator finishes with nothing in flight")


class SupervisionTest(unittest.TestCase):
    def test_supervision(self):
        sched = DeterministicScheduler(0)
        s = ActorSystem(sched, False)
        probe = s.spawn("probe", ProbeActor, RESUME)
        acct = s.spawn("acct-X", lambda: AccountActor("acct-X", usd(5_000)), RESUME)
        resumed = s.spawn("c-resume", CounterActor, RESUME)
        restarted = s.spawn("c-restart", CounterActor, RESTART)
        stopped = s.spawn("c-stop", CounterActor, STOP)
        acct.tell(Deposit("eur", Money.of_minor(100, EUR), probe))
        acct.tell(Deposit("usd", usd(100), probe))
        for c in (resumed, restarted, stopped):
            c.tell(Increment())
            c.tell(Increment())
            c.tell(Crash("boom"))
            c.tell(Increment())
            c.tell(GetBalance(probe))
        sched.run_until_idle()
        received = actor_of(s, "probe").received
        self.assertTrue(any(isinstance(m, Deposited) and m.correlation_id == "usd" and m.balance == usd(5_100)
                            for m in received), "a throwing message is dropped and the next one is processed")
        self.assertEqual(actor_of(s, "acct-X").balance(), usd(5_100))
        self.assertEqual(s.cell("acct-X").failures.get(), 1)
        self.assertEqual(s.cell("acct-X").restarts.get(), 0)
        counts = [m.count for m in received if isinstance(m, CountIs)]
        self.assertEqual(len(counts), 2)
        self.assertEqual(counts[0], 3, "RESUME counter keeps 2 and adds 1")
        self.assertEqual(counts[1], 1, "RESTART counter counted only the post-restart increment")
        self.assertEqual(s.cell("c-restart").restarts.get(), 1)
        self.assertTrue(s.cell("c-stop").stopped)
        self.assertEqual(s.cell("c-stop").dead_letters.get(), 2)
        self.assertEqual(s.dead_letters(), 2)
        stopped.tell(Increment())
        sched.run_until_idle()
        self.assertEqual(s.cell("c-stop").dead_letters.get(), 3, "messages to a stopped actor are dead letters too")
        self.assertEqual(len(s.failures()), 4)
        self.assertIn("acct-X failed on Deposit(eur", s.failures()[0])
        self.assertFalse(any(isinstance(m, Deposited) and m.correlation_id == "eur" for m in received))


class SystemRulesTest(unittest.TestCase):
    def test_system_rules(self):
        sched = DeterministicScheduler(0)
        s = ActorSystem(sched, False)
        a = s.spawn("a", CounterActor, RESUME)
        with self.assertRaises(ValueError):
            s.spawn("a", CounterActor, RESUME)
        with self.assertRaises(ValueError):
            s.ref("nobody")
        with self.assertRaises(TypeError):
            a.tell(None)
        self.assertFalse(sched.step(), "an idle scheduler reports no step")
        a.tell(Increment())
        self.assertTrue(sched.step())
        self.assertFalse(sched.step(), "one message, one step")


class ThreadedTest(unittest.TestCase):
    def test_threaded_hot_account(self):
        with ThreadPoolExecutor(max_workers=4) as pool:
            s = ActorSystem(ThreadedScheduler(pool, 32), False)
            acct = s.spawn("hot", lambda: AccountActor("hot", usd(0)), RESUME)
            threads, per_thread = 4, 10_000

            def produce() -> None:
                for _ in range(per_thread):
                    acct.tell(Deposit("x", usd(1), None))

            producers = [threading.Thread(target=produce) for _ in range(threads)]
            for p in producers:
                p.start()
            for p in producers:
                p.join()
            probe = s.spawn("probe", lambda: ProbeActor(1), RESUME)
            acct.tell(GetBalance(probe))
            self.assertTrue(actor_of(s, "probe").latch.await_(10), "probe answered in time")
            self.assertEqual(actor_of(s, "probe").balances()["hot"], usd(threads * per_thread))
            self.assertEqual(s.cell("hot").max_concurrent_receives.get(), 1, "receive() never ran on two threads")
            self.assertEqual(s.cell("hot").processed.get(), threads * per_thread + 1)

    def test_threaded_transfers(self):
        with ThreadPoolExecutor(max_workers=4) as pool:
            s = bank_with(ThreadedScheduler(pool, 16), False)
            transfers = 3_000
            probe = s.spawn("probe", lambda: ProbeActor(transfers), RESUME)
            names = ["acct-A", "acct-B", "acct-C"]

            def produce(id_: int) -> None:
                for i in range(transfers // 3):
                    frm, to = names[(id_ + i) % 3], names[(id_ + i + 1) % 3]
                    s.ref("transfers").tell(Transfer(f"t{id_}-{i}", frm, to, usd(1 + (i % 700)), probe))

            producers = [threading.Thread(target=produce, args=(t,)) for t in range(3)]
            for p in producers:
                p.start()
            for p in producers:
                p.join()
            self.assertTrue(actor_of(s, "probe").latch.await_(10), "all transfer results arrived")
            probe2 = s.spawn("probe2", lambda: ProbeActor(3), RESUME)
            for n in names:
                s.ref(n).tell(GetBalance(probe2))
            p2 = actor_of(s, "probe2")
            self.assertTrue(p2.latch.await_(10), "balances reported")
            balances = p2.balances()
            t = usd(0)
            for m in balances.values():
                t = t.plus(m)
            self.assertEqual(t, usd(30_000), "money conserved across 3,000 concurrent transfers")
            self.assertFalse(any(m.is_negative() for m in balances.values()))
            self.assertEqual(s.cell("acct-A").max_concurrent_receives.get(), 1)
            self.assertEqual(s.cell("transfers").max_concurrent_receives.get(), 1)


if __name__ == "__main__":
    unittest.main()
