"""Tests for phases/03-systems-data-structures/12-time-wheels-and-timers. Run: python3 -m unittest -v"""
import unittest

from main import (Bucket, HeapTimer, HierarchicalTimer, JavaRandom, ManualClock, TimerEntry, TimingWheel,
                  same_set_fired)


def poll(timer, clock, advance):
    clock.advance(advance)
    timer.poll()
    return True


def levels_after(timer, delay):
    timer.schedule(delay, lambda: None)
    return timer.levels()


class BucketTest(unittest.TestCase):
    def test_link_unlink_flush(self):
        bucket = Bucket()
        e1, e2, e3 = (TimerEntry(i, 10, lambda: None) for i in (1, 2, 3))
        for e in (e1, e2, e3):
            bucket.add(e)
        self.assertEqual(bucket.size(), 3)
        bucket.remove(e2)
        self.assertTrue(bucket.size() == 2 and e2.bucket is None and e2.prev is None)
        flushed = []
        bucket.flush(lambda e: flushed.append(e.id))
        self.assertTrue(flushed == [1, 3] and bucket.size() == 0)
        bucket.remove(e1)
        self.assertEqual(bucket.size(), 0)


class SingleWheelTest(unittest.TestCase):
    def test_exact_firing_inline_and_validation(self):
        clock = ManualClock(0)
        timer = HierarchicalTimer(clock, 1, 8)
        log = []
        timer.schedule(3, lambda: log.append("a"))
        timer.schedule(5, lambda: log.append("b"))
        timer.schedule(5, lambda: log.append("c"))
        self.assertTrue(poll(timer, clock, 2) and log == [])
        self.assertTrue(poll(timer, clock, 1) and log == ["a"])
        self.assertTrue(poll(timer, clock, 2) and log == ["a", "b", "c"])
        self.assertTrue(timer.pending() == 0 and timer.fired() == 3)
        inline = []
        timer.schedule(0, lambda: inline.append("now"))
        self.assertEqual(inline, ["now"])
        with self.assertRaises(ValueError):
            timer.schedule(-1, lambda: None)
        with self.assertRaises(ValueError):
            TimingWheel(1, 1, 0)
        with self.assertRaises(ValueError):
            clock.advance(-1)


class CancellationTest(unittest.TestCase):
    def test_cancel_is_o1_and_frees_slot(self):
        c2 = ManualClock(0)
        t2 = HierarchicalTimer(c2, 1, 8)
        l2 = []
        hold = t2.schedule(4, lambda: l2.append("hold expired"))
        t2.schedule(4, lambda: l2.append("other"))
        self.assertTrue(hold.cancel())
        self.assertFalse(hold.cancel())
        self.assertTrue(t2.pending() == 1 and hold.bucket is None)
        poll(t2, c2, 4)
        self.assertEqual(l2, ["other"])
        past_hold = t2.schedule(2, lambda: l2.append("late cancel"))
        poll(t2, c2, 2)
        self.assertTrue(not past_hold.cancel() and l2 == ["other", "late cancel"])


class OverflowTest(unittest.TestCase):
    def test_levels(self):
        t3 = HierarchicalTimer(ManualClock(0), 1, 8)
        self.assertEqual(levels_after(t3, 7), 1)
        self.assertEqual(levels_after(t3, 8), 2)
        self.assertEqual(levels_after(t3, 63), 2)
        self.assertEqual(levels_after(t3, 64), 3)
        self.assertEqual(levels_after(t3, 511), 3)
        self.assertEqual(levels_after(t3, 512), 4)

    def test_four_levels_exact_deadlines(self):
        c4 = ManualClock(0)
        t4 = HierarchicalTimer(c4, 1, 8)
        fire_times = []
        delays = [1, 7, 8, 9, 63, 64, 65, 300, 511, 512, 4095]
        for d in delays:
            t4.schedule(d, lambda: fire_times.append(c4.now_millis()))
        for _ in range(4096):
            c4.advance(1)
            t4.poll()
        self.assertEqual(fire_times, delays)
        self.assertEqual(t4.cascades(), 13)

    def test_big_clock_jump(self):
        c5 = ManualClock(0)
        t5 = HierarchicalTimer(c5, 1, 8)
        jump_fires = []
        t5.schedule(70, lambda: jump_fires.append(c5.now_millis()))
        t5.schedule(9, lambda: jump_fires.append(c5.now_millis()))
        c5.advance(100)
        t5.poll()
        self.assertTrue(jump_fires == [100, 100] and t5.fired() == 2)


class CoarseTickTest(unittest.TestCase):
    def test_round_up_never_early(self):
        c6 = ManualClock(1_003)
        coarse = HierarchicalTimer(c6, 10, 16)
        order = []
        coarse.schedule(29, lambda: order.append("29"))  # deadline 1032 -> tick 1040
        coarse.schedule(23, lambda: order.append("23"))  # deadline 1026 -> tick 1030
        c6.advance(26)
        coarse.poll()
        self.assertEqual(order, [])
        c6.advance(1)
        coarse.poll()
        self.assertEqual(order, ["23"])
        c6.advance(10)
        coarse.poll()
        self.assertEqual(order, ["23", "29"])
        self.assertEqual(coarse.schedule(5, lambda: None).deadline_millis % 10, 0)


class WheelVersusHeapTest(unittest.TestCase):
    def test_same_set_fired(self):
        rng = JavaRandom(99)
        wc, hc = ManualClock(0), ManualClock(0)
        wheel = HierarchicalTimer(wc, 1, 16)
        heap = HeapTimer(hc)
        wheel_fired, heap_fired = [], []
        we, he = [], []
        for i in range(5_000):
            delay = rng.next_int(20_000)
            we.append(wheel.schedule(delay, lambda i=i: wheel_fired.append(i)))
            he.append(heap.schedule(delay, lambda i=i: heap_fired.append(i)))
        for i in range(0, 5_000, 4):
            we[i].cancel()
            he[i].cancel()
        for _ in range(25):
            wc.advance(1_000)
            hc.advance(1_000)
            wheel.poll()
            heap.poll()
        self.assertTrue(same_set_fired(wheel_fired, heap_fired) and len(wheel_fired) == 3_750)
        self.assertTrue(wheel.pending() == 0 and heap.pending() == 0 and wheel.fired() == heap.fired())


if __name__ == "__main__":
    unittest.main()
