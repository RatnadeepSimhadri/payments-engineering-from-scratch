"""Tests for phases/04-concurrency/04-concurrent-hash-maps. Run: python3 -m unittest -v"""
import threading
import unittest

from main import (AtomicInteger, GlobalLockMap, Hashing, LockFreeReadMap, StripedHashMap, race_get_then_put,
                  race_put_if_absent)


def java_string_hash(s):
    """Reference String.hashCode computed independently of Hashing.stable_hash."""
    h = 0
    for ch in s:
        h = (31 * h + ord(ch)) % (1 << 32)
    return h - (1 << 32) if h >= (1 << 31) else h


class HashingTest(unittest.TestCase):
    def test_hashing(self):
        self.assertEqual(Hashing.stable_hash("acct-1"), java_string_hash("acct-1"))
        self.assertEqual(Hashing.stable_hash("acct-1"), -1423448841)  # the value Java prints
        self.assertEqual(Hashing.stable_hash("acct-2") - Hashing.stable_hash("acct-1"), 1)  # low bits only
        m = StripedHashMap(16)
        self.assertGreaterEqual(len({m.segment_index(f"acct-{i}") for i in range(1, 9)}), 4)
        self.assertTrue(all(0 <= Hashing.bucket_index(f"k{i}", 64) < 64 for i in range(1000)))
        self.assertEqual(Hashing.spread(12345), Hashing.spread(12345))
        # Same placements as the Java demo: 32-bit wraparound in the multiply and an unsigned >>> for the stripe.
        self.assertEqual([m.segment_index(f"acct-{i}") for i in range(1, 9)], [8, 4, 13, 7, 0, 1, 7, 5])
        self.assertEqual(Hashing.spread(Hashing.stable_hash("acct-1")), -2101873864)


class StoreContract:
    """Run against every implementation, like the Java test's loop over three factories."""

    def make(self):
        raise NotImplementedError

    def test_single_threaded_contract(self):
        m = self.make()
        self.assertTrue(m.get("a") is None and m.size() == 0)
        self.assertIsNone(m.put("a", 1))
        self.assertTrue(m.get("a") == 1 and m.size() == 1)
        self.assertTrue(m.put("a", 2) == 1 and m.get("a") == 2 and m.size() == 1)
        self.assertTrue(m.put_if_absent("a", 9) == 2 and m.get("a") == 2)
        self.assertTrue(m.put_if_absent("b", 5) is None and m.get("b") == 5 and m.size() == 2)
        self.assertTrue(m.compute("a", lambda k, v: v + 10) == 12 and m.get("a") == 12)
        self.assertTrue(m.compute("c", lambda k, v: 1 if v is None else v + 1) == 1 and m.size() == 3)
        self.assertTrue(m.compute("c", lambda k, v: None) is None and m.get("c") is None and m.size() == 2)
        self.assertTrue(m.remove("b") == 5 and m.get("b") is None and m.size() == 1)
        self.assertIsNone(m.remove("zzz"))
        with self.assertRaises(TypeError):
            m.get(None)
        with self.assertRaises(TypeError):
            m.put("x", None)
        for i in range(500):
            m.put(f"fill-{i}", i)
        self.assertTrue(all(m.get(f"fill-{i}") == i for i in range(500)) and m.size() == 501)

    def test_concurrent_with_barriers(self):
        """Every phase starts behind a barrier, so the assertions are about end states every interleaving produces."""
        m = self.make()
        threads, per_thread, shared, bumps = 4, 5_000, 100, 1_000
        barrier = threading.Barrier(threads)
        errors = AtomicInteger()

        def work(id_):
            try:
                barrier.wait()  # phase 1: disjoint inserts
                for i in range(per_thread):
                    m.put(f"t{id_}-{i}", i)
                barrier.wait()  # phase 2: everyone reads everyone's keys
                for o in range(threads):
                    for i in range(0, per_thread, 7):
                        if m.get(f"t{o}-{i}") != i:
                            errors.increment_and_get()
                barrier.wait()  # phase 3: contended increments on shared keys
                for _ in range(bumps):
                    for k in range(shared):
                        m.compute(f"shared-{k}", lambda key, v: 1 if v is None else v + 1)
                barrier.wait()  # phase 4: each thread removes its own keys
                for i in range(per_thread):
                    if m.remove(f"t{id_}-{i}") is None:
                        errors.increment_and_get()
            except Exception:
                errors.increment_and_get()

        workers = [threading.Thread(target=work, args=(t,)) for t in range(threads)]
        for w in workers:
            w.start()
        for w in workers:
            w.join()
        self.assertEqual(errors.get(), 0)
        self.assertTrue(all(m.get(f"shared-{k}") == threads * bumps for k in range(shared)))
        self.assertEqual(m.size(), shared)


class GlobalLockMapTest(StoreContract, unittest.TestCase):
    def make(self):
        return GlobalLockMap()


class StripedHashMapTest(StoreContract, unittest.TestCase):
    def make(self):
        return StripedHashMap(16)

    def test_striped_specifics(self):
        one = StripedHashMap(1)
        for i in range(200):
            one.put(f"k{i}", i)
        self.assertGreaterEqual(one.total_resizes(), 5)
        self.assertTrue(all(one.get(f"k{i}") == i for i in range(200)) and one.size() == 200)
        with self.assertRaises(ValueError):
            StripedHashMap(12)
        sixteen = StripedHashMap(16)
        self.assertTrue(all(0 <= sixteen.segment_index(f"acct-{i}") < 16 for i in range(1000))
                        and sixteen.stripes() == 16)


class LockFreeReadMapTest(StoreContract, unittest.TestCase):
    def make(self):
        return LockFreeReadMap(16, 64)

    def test_lock_free_read_specifics(self):
        m = LockFreeReadMap(1, 2)  # two buckets: every chain is long, every remove hits the middle
        for i in range(6):
            m.put(f"k{i}", i)
        self.assertTrue(m.buckets() > 2 and m.resizes() >= 1)
        m.resize_to(64)
        self.assertTrue(m.buckets() == 64 and m.size() == 6 and m.get("k3") == 3 and m.get("k5") == 5)
        m.resize_to(8)
        self.assertEqual(m.buckets(), 64)  # shrinking is ignored
        chain = LockFreeReadMap(1, 1)
        chain.put("a", 1)
        chain.put("b", 2)
        chain.put("c", 3)
        chain.resize_to(1)  # ignored, but harmless
        self.assertTrue(chain.remove("b") == 2 and chain.get("a") == 1 and chain.get("c") == 3
                        and chain.get("b") is None)
        self.assertTrue(chain.compute("a", lambda k, v: v + 100) == 101 and chain.get("c") == 3 and chain.size() == 2)
        with self.assertRaises(ValueError):
            LockFreeReadMap(16, 8)

    def test_readers_never_see_torn_state(self):
        """Readers run without locks while writers rebuild chains and resize."""
        m = LockFreeReadMap(4, 4)
        stable_keys = 64
        for i in range(stable_keys):
            m.put(f"stable-{i}", i)
        stop = threading.Event()
        anomalies = AtomicInteger()
        reads = AtomicInteger()

        def reader():
            while not stop.is_set():
                for i in range(stable_keys):
                    if m.get(f"stable-{i}") != i:
                        anomalies.increment_and_get()
                    reads.increment_and_get()

        def writer(id_):
            for i in range(20_000):
                k = f"churn-{id_}-{i % 500}"
                if i % 3 == 2:
                    m.remove(k)
                else:
                    m.compute(k, lambda key, v: 1 if v is None else v + 1)

        readers = [threading.Thread(target=reader) for _ in range(2)]
        writers = [threading.Thread(target=writer, args=(w,)) for w in range(2)]
        for t in readers + writers:
            t.start()
        for w in writers:
            w.join()
        stop.set()
        for r in readers:
            r.join()
        self.assertEqual(anomalies.get(), 0)
        self.assertGreater(reads.get(), stable_keys)
        self.assertGreater(m.buckets(), 4)
        self.assertTrue(all(m.get(f"stable-{i}") == i for i in range(stable_keys)))

    def test_nodes_are_immutable(self):
        m = LockFreeReadMap(1, 1)
        m.put("a", 1)
        with self.assertRaises(AttributeError):
            m._table[0].value = 2  # a reader can rely on a chain never changing under it


class CheckThenActTest(unittest.TestCase):
    def test_barrier_forced_race(self):
        self.assertEqual(race_get_then_put(StripedHashMap(), 8), 8)
        self.assertEqual(race_get_then_put(LockFreeReadMap(), 8), 8)
        self.assertEqual(race_put_if_absent(StripedHashMap(), 8), 1)
        self.assertEqual(race_put_if_absent(LockFreeReadMap(), 8), 1)
        self.assertEqual(race_put_if_absent(GlobalLockMap(), 8), 1)


if __name__ == "__main__":
    unittest.main()
