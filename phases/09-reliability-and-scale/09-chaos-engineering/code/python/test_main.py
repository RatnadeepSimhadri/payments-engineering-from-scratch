"""Tests for phases/09-reliability-and-scale/09-chaos-engineering. Run: python3 -m unittest -v"""
import unittest
from dataclasses import dataclass

from main import (ABORT, STEADY, USD, AuthRequest, AuthResult, BlastRadius, Experiment, ExperimentRunner, Fault,
                  FaultInjector, GameDay, Gateway, Metrics, Money, Outcome, Processor, SimClock, SimNetwork,
                  SteadyState, TrafficGenerator)

AMOUNT = Money.of_minor(1999, USD)


@dataclass
class World:
    """A fresh, fully seeded world. Same seeds => same results, which is what makes chaos replayable."""

    clock: SimClock
    injector: FaultInjector
    processor: Processor
    network: SimNetwork
    gateway: Gateway
    traffic: TrafficGenerator
    runner: ExperimentRunner

    @staticmethod
    def create() -> "World":
        clock = SimClock(1_700_000_000_000)
        injector = FaultInjector(7)
        processor = Processor(clock, 11)
        network = SimNetwork(clock, injector, 13)
        gateway = Gateway(clock, network, processor)
        traffic = TrafficGenerator(17, "pay")
        return World(clock, injector, processor, network, gateway, traffic,
                     ExperimentRunner(clock, injector, gateway, processor, traffic))


def exp(target: str, fault: Fault, radius: BlastRadius) -> Experiment:
    return Experiment("t", "h", target, fault, radius, STEADY, ABORT, 300)


class ValueTypesAndInjectorTest(unittest.TestCase):
    def test_values(self):
        self.assertEqual(str(AMOUNT), "19.99 USD")
        with self.assertRaises(ValueError):
            Fault.Latency(-1)
        with self.assertRaises(ValueError):
            BlastRadius.PercentOfRequests(101)
        with self.assertRaises(ValueError):
            SimClock(0).advance(-5)

    def test_injector(self):
        inj = FaultInjector(1)
        self.assertFalse(inj.is_armed())
        inj.inject("processor", Fault.Error("HTTP_500"), BlastRadius.Everyone())
        req = AuthRequest("p1", "m-1", AMOUNT, "idem-p1", 0)
        self.assertEqual(len(inj.faults_for("processor", req)), 1)
        self.assertEqual(inj.faults_for("gateway->processor", req), [])
        inj.clear()
        self.assertTrue(not inj.is_armed() and inj.faults_for("processor", req) == [])
        self.assertTrue(BlastRadius.Merchants(frozenset({"m-1"})).includes(req, None)
                        and not BlastRadius.Merchants(frozenset({"m-2"})).includes(req, None))


class GatewayTest(unittest.TestCase):
    def test_happy_path(self):
        w = World.create()
        ok = w.gateway.authorize("p-1", "m-0", AMOUNT)
        self.assertEqual(ok.attempts, 1)
        self.assertTrue(80 <= ok.latency_millis <= 120)
        self.assertTrue(ok.outcome.is_good())

    def test_latency_beyond_timeout(self):
        w = World.create()
        w.injector.inject(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(2_500), BlastRadius.Everyone())
        slow = w.gateway.authorize("p-2", "m-0", AMOUNT)
        self.assertTrue(slow.outcome == Outcome.TIMEOUT and slow.attempts == 2)
        self.assertEqual(slow.latency_millis, 2 * Gateway.TIMEOUT_MILLIS)
        self.assertEqual(w.processor.authorizations_for("p-2"), 0)

    def test_processor_idempotency(self):
        w = World.create()
        p = w.processor
        first = AuthRequest("p-3", "m-0", AMOUNT, "idem-p-3", w.clock.now_millis())
        o1 = p.handle(first)
        o2 = p.handle(first)
        self.assertEqual(o1, o2)
        self.assertTrue(p.authorizations_for("p-3") <= 1 and p.no_double_authorizations())

    def test_partition_looks_like_timeout(self):
        w = World.create()
        w.injector.inject(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Partition(), BlastRadius.Merchants(frozenset({"m-3"})))
        cut = w.gateway.authorize("p-4", "m-3", AMOUNT)
        fine = w.gateway.authorize("p-5", "m-4", AMOUNT)
        self.assertEqual(cut.outcome, Outcome.TIMEOUT)
        self.assertTrue(fine.outcome.is_good() and fine.attempts == 1)

    def test_clock_skew(self):
        w = World.create()
        w.injector.inject(SimNetwork.TARGET_PROCESSOR, Fault.ClockSkew(6 * 60 * 1000), BlastRadius.Everyone())
        self.assertEqual(w.gateway.authorize("p-6", "m-0", AMOUNT).outcome, Outcome.STALE_REQUEST)
        w.injector.clear()
        w.injector.inject(SimNetwork.TARGET_PROCESSOR, Fault.ClockSkew(4 * 60 * 1000), BlastRadius.Everyone())
        self.assertTrue(w.gateway.authorize("p-7", "m-0", AMOUNT).outcome.is_good())
        self.assertEqual(w.gateway.authorize("p-8", "m-0", AMOUNT).attempts, 1)


class MetricsTest(unittest.TestCase):
    def test_p99_is_a_quantile(self):
        m = Metrics()
        for i in range(1, 101):
            m.record(AuthResult(Outcome.AUTHORIZED, i, 1))
        self.assertEqual(m.p99_millis(), 99)
        m.record(AuthResult(Outcome.TIMEOUT, 4_000, 2))
        self.assertTrue(99.0 < m.good_rate_percent() < 100.0)
        self.assertEqual(Metrics().good_rate_percent(), 100.0)


class ExperimentTest(unittest.TestCase):
    def test_held_aborted_and_violated(self):
        w = World.create()
        held = w.runner.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(300), BlastRadius.PercentOfRequests(10)))
        self.assertTrue(held.baseline_held)
        self.assertTrue(held.hypothesis_held and not held.aborted)
        self.assertTrue(held.experimental.p99_millis() > held.baseline.p99_millis()
                        and held.experimental.p99_millis() <= STEADY.max_p99_millis)

        aborted = w.runner.run(exp(SimNetwork.TARGET_PROCESSOR, Fault.Error("HTTP_500"), BlastRadius.PercentOfRequests(70)))
        self.assertTrue(aborted.aborted)
        self.assertEqual(aborted.requests_before_abort, ABORT.check_every)
        self.assertFalse(w.injector.is_armed())
        self.assertTrue(aborted.recovered)
        self.assertTrue(aborted.verdict().startswith("ABORTED"))

        skewed = w.runner.run(exp(SimNetwork.TARGET_PROCESSOR, Fault.ClockSkew(6 * 60 * 1000), BlastRadius.Everyone()))
        self.assertTrue(skewed.aborted and skewed.experimental.count(Outcome.STALE_REQUEST) > 0)
        self.assertTrue(skewed.no_double_authorizations)

        partition = w.runner.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Partition(), BlastRadius.Merchants(frozenset({"m-3"}))))
        self.assertFalse(partition.aborted)
        self.assertTrue(not partition.hypothesis_held and partition.experimental.p99_millis() > STEADY.max_p99_millis)
        self.assertTrue(partition.no_double_authorizations)

    def test_impossible_steady_state_is_not_run(self):
        w = World.create()
        impossible = Experiment("x", "h", SimNetwork.TARGET_PROCESSOR, Fault.Error("E"), BlastRadius.Everyone(),
                                SteadyState(100.0, 50), ABORT, 100)
        not_run = w.runner.run(impossible)
        self.assertTrue(not not_run.baseline_held and not_run.verdict().startswith("NOT RUN"))
        self.assertTrue(not w.injector.is_armed() and w.injector.applied_count() == 0)

    def test_determinism(self):
        a = World.create().runner.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(2_500), BlastRadius.PercentOfRequests(10)))
        b = World.create().runner.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(2_500), BlastRadius.PercentOfRequests(10)))
        self.assertEqual(a.render(), b.render())

    def test_java_random_parity_with_the_docs(self):
        # Seeds 7/11/13/17 drive the same java.util.Random streams as the Java demo: 500-request baseline, 488 auth / 12 decl.
        w = World.create()
        report = w.runner.run(Experiment("t", "h", SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(300),
                                         BlastRadius.PercentOfRequests(10), STEADY, ABORT, 500))
        self.assertTrue(report.baseline.count(Outcome.AUTHORIZED) == 488 and report.baseline.count(Outcome.DECLINED) == 12)
        self.assertEqual(report.experimental.p99_millis(), 417)

    def test_gameday_collects_and_summarises(self):
        w = World.create()
        day = GameDay(w.runner)
        day.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(300), BlastRadius.PercentOfRequests(10)))
        day.run(exp(SimNetwork.TARGET_PROCESSOR, Fault.Error("HTTP_500"), BlastRadius.PercentOfRequests(70)))
        self.assertEqual(len(day.reports()), 2)
        self.assertTrue("HYPOTHESIS HELD" in day.summary() and "ABORTED" in day.summary())


if __name__ == "__main__":
    unittest.main()
