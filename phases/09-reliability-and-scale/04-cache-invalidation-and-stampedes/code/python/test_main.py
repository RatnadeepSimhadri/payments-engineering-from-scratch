"""Tests for phases/09-reliability-and-scale/04-cache-invalidation-and-stampedes. Run: python3 -m unittest -v"""
import unittest

from main import (USD, BackingStore, ClientStats, EarlyExpirationStrategy, Entry, LockingStrategy, Money,
                  NaiveStrategy, Scenario, Simulator, SingleFlightStrategy, StaleWhileRevalidateStrategy, Strategy)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def burst(factory, clients: int, store_latency: int, received: list) -> Strategy:
    """N clients ask for the same cold key at the same instant. Returns the strategy after the simulation drains."""
    sim = Simulator()
    store = BackingStore(sim, store_latency)
    store.write("k", usd(100))
    s = factory(sim, store)
    for _ in range(clients):
        sim.schedule(0, lambda: s.get("k", received.append))
    sim.run()
    return s


class SimulatorTest(unittest.TestCase):
    def test_ordering_and_no_time_travel(self):
        sim = Simulator()
        order = []
        sim.schedule(50, lambda: order.append("b"))
        sim.schedule(10, lambda: order.append("a"))
        sim.schedule(50, lambda: order.append("c"))
        sim.schedule(10, lambda: sim.after(5, lambda: order.append("a2")))
        sim.run()
        self.assertEqual(order, ["a", "a2", "b", "c"])
        self.assertEqual(sim.now(), 50)
        with self.assertRaises(ValueError):
            sim.schedule(1, lambda: None)

    def test_store_load_latency_and_concurrency(self):
        s2 = Simulator()
        st = BackingStore(s2, 100)
        st.write("k", usd(5))
        completed = []
        s2.schedule(0, lambda: st.load("k", lambda v: completed.append(s2.now())))
        s2.schedule(30, lambda: st.load("k", lambda v: completed.append(s2.now())))
        s2.run()
        self.assertEqual(completed, [100, 130])
        self.assertTrue(st.loads == 2 and st.peak_in_flight == 2 and st.in_flight == 0)

    def test_entry_horizons(self):
        e = Entry(usd(1), 1_000, 1_500, 100)
        self.assertTrue(e.is_fresh(999) and not e.is_fresh(1_000))
        self.assertTrue(e.is_usable(1_499) and not e.is_usable(1_500))


class StampedeTest(unittest.TestCase):
    def test_naive_every_cold_client_loads(self):
        got = []
        naive = burst(lambda sm, store: NaiveStrategy(sm, store, 5_000), 100, 100, got)
        self.assertTrue(naive.store.loads == 100 and naive.store.peak_in_flight == 100)
        self.assertTrue(len(got) == 100 and all(v == usd(100) for v in got))
        self.assertTrue(naive.stats.percentile(0.5) == 100 and naive.stats.max() == 100)

    def test_single_flight_one_load_everyone_served(self):
        got = []
        sf = burst(lambda sm, store: SingleFlightStrategy(sm, store, 5_000), 100, 100, got)
        self.assertTrue(sf.store.loads == 1 and sf.store.peak_in_flight == 1)
        self.assertTrue(len(got) == 100 and all(v == usd(100) for v in got))
        self.assertTrue(sf.stats.max() == 100 and sf.stats.percentile(0.5) == 100)
        self.assertTrue(sf.stats.misses == 100 and sf.waiting_for("k") == 0)

    def test_single_flight_hit_after_warm_up_miss_after_ttl(self):
        s3 = Simulator()
        st3 = BackingStore(s3, 100)
        st3.write("k", usd(100))
        sf3 = SingleFlightStrategy(s3, st3, 5_000)
        s3.schedule(0, lambda: sf3.get("k", lambda v: None))
        s3.schedule(200, lambda: sf3.get("k", lambda v: None))
        s3.schedule(5_100 + 50, lambda: sf3.get("k", lambda v: None))  # past the TTL: a second load
        s3.run()
        self.assertTrue(sf3.stats.hits == 1 and sf3.stats.misses == 2 and st3.loads == 2)

    def test_locking_one_load_waiters_retry(self):
        got = []
        lock = burst(lambda sm, store: LockingStrategy(sm, store, 5_000, 30), 50, 100, got)
        self.assertEqual(lock.store.loads, 1)
        self.assertEqual(len(got), 50)
        self.assertEqual(lock.stats.lock_retries, 49 * 4)  # each waiter failed at t=0,30,60,90 then hit at t=120
        self.assertTrue(lock.stats.max() == 120 and lock.stats.percentile(0.01) == 100)


class EarlyExpirationTest(unittest.TestCase):
    def test_recompute_probability_shape(self):
        s4 = Simulator()
        st4 = BackingStore(s4, 100)
        early = EarlyExpirationStrategy(s4, st4, 5_000, 1.0, 1)
        far_away = Entry(usd(1), 1_000_000, 1_000_000, 100)
        any_early = False
        for _ in range(1_000):
            any_early |= early.should_recompute(far_away, 0)
        self.assertFalse(any_early)
        at_boundary = Entry(usd(1), 1_000, 1_000, 100)
        self.assertTrue(early.should_recompute(at_boundary, 1_000))
        near = Entry(usd(1), 1_050, 1_050, 100)  # 50 ms before expiry, load takes 100 ms
        near_count = sum(1 for _ in range(10_000) if early.should_recompute(near, 1_000))
        self.assertTrue(5_500 < near_count < 6_600)  # about e^-0.5

    def test_cliff_is_avoided_on_the_real_scenario(self):
        quiet = Scenario(1_000, 15_000, 100, 5_000, -1, 7)
        xfetch = quiet.run(lambda sm, store: EarlyExpirationStrategy(sm, store, 5_000, 1.0, 11))
        naive_quiet = quiet.run(lambda sm, store: NaiveStrategy(sm, store, 5_000))
        self.assertLess(xfetch.store.loads, naive_quiet.store.loads // 5)
        self.assertTrue(xfetch.store.peak_in_flight < 10 and naive_quiet.store.peak_in_flight > 90)
        self.assertTrue(xfetch.stats.early_refreshes > 0 and xfetch.stats.average_lead_ms() > 0)
        self.assertEqual(xfetch.stats.percentile(0.99), 0)


class StaleWhileRevalidateTest(unittest.TestCase):
    def test_serve_stale_within_grace_one_refresh_no_waiting(self):
        s5 = Simulator()
        st5 = BackingStore(s5, 100)
        st5.write("k", usd(100))
        swr = StaleWhileRevalidateStrategy(s5, st5, 1_000, 500)
        got = []
        s5.schedule(0, lambda: swr.get("k", got.append))       # cold: waits 100 ms
        s5.schedule(900, lambda: st5.write("k", usd(20)))     # store changes
        s5.schedule(1_200, lambda: swr.get("k", got.append))   # stale, within grace: served old value now
        s5.schedule(1_210, lambda: swr.get("k", got.append))   # still refreshing: no second load
        s5.schedule(1_400, lambda: swr.get("k", got.append))   # refresh landed at 1300: fresh new value
        s5.run()
        self.assertTrue(got[1] == usd(100) and got[2] == usd(100))
        self.assertEqual(st5.loads, 2)
        self.assertEqual(got[3], usd(20))
        self.assertEqual(swr.stats.stale_serves, 2)
        self.assertTrue(swr.stats.max() == 100 and swr.stats.percentile(0.5) == 0)
        self.assertFalse(swr.is_refreshing("k"))

    def test_past_grace_behaves_like_single_flight(self):
        s6 = Simulator()
        st6 = BackingStore(s6, 100)
        st6.write("k", usd(100))
        swr2 = StaleWhileRevalidateStrategy(s6, st6, 1_000, 500)
        s6.schedule(0, lambda: swr2.get("k", lambda v: None))
        for i in range(20):
            s6.schedule(2_000 + i, lambda: swr2.get("k", lambda v: None))
        s6.run()
        self.assertTrue(st6.loads == 2 and swr2.stats.misses == 21)


class ScenarioTest(unittest.TestCase):
    def test_invalidation_is_a_cliff(self):
        sc = Scenario(1_000, 4_000, 100, 5_000, -1, 7)
        n_inv = sc.run_with_invalidation(lambda sm, store: NaiveStrategy(sm, store, 5_000), 2_500)
        f_inv = sc.run_with_invalidation(lambda sm, store: SingleFlightStrategy(sm, store, 5_000), 2_500)
        self.assertTrue(n_inv.store.loads > 90 and n_inv.store.peak_in_flight > 90)
        self.assertTrue(f_inv.store.loads == 2 and f_inv.store.peak_in_flight == 1)

    def test_scenario_is_deterministic(self):
        sc = Scenario(1_000, 4_000, 100, 5_000, -1, 7)
        r1 = sc.run(lambda sm, store: EarlyExpirationStrategy(sm, store, 5_000, 1.0, 3))
        r2 = sc.run(lambda sm, store: EarlyExpirationStrategy(sm, store, 5_000, 1.0, 3))
        self.assertTrue(r1.store.loads == r2.store.loads and r1.stats.latencies == r2.stats.latencies)

    def test_java_random_parity_with_the_docs_table(self):
        # The demo numbers come from java.util.Random(7) jitter and Random(11) XFetch draws.
        sc = Scenario(1_000, 10_000, 100, 5_000, 4_000, 7)
        naive = sc.run(lambda sim, st: NaiveStrategy(sim, st, 5_000))
        early = sc.run(lambda sim, st: EarlyExpirationStrategy(sim, st, 5_000, 1.0, 11))
        self.assertTrue(naive.store.loads == 103 and naive.store.peak_in_flight == 102 and naive.stats.stale_serves == 1099)
        self.assertTrue(early.store.loads == 5 and early.stats.early_refreshes == 4 and early.stats.stale_serves == 761)


class PercentileTest(unittest.TestCase):
    def test_nearest_rank(self):
        cs = ClientStats()
        for v in (5, 1, 4, 2, 3):
            cs.record(v, False)
        self.assertTrue(cs.percentile(0.5) == 3 and cs.percentile(0.99) == 5 and cs.max() == 5)


if __name__ == "__main__":
    unittest.main()
