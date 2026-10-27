"""Tests for phases/08-messaging-and-streams/08-watermarks-and-late-data. Run: python3 -m unittest -v"""
import unittest

from main import (
    MINUTE,
    USD,
    BoundedOutOfOrdernessWatermark,
    Currency,
    EventTimeWindowOperator,
    FiringKind,
    LateReason,
    Money,
    ProcessingTimeWindowOperator,
    TumblingWindowAssigner,
    Window,
    ev as _ev,
    t,
)

MIN = MINUTE


def ev(event_time, arrival, cents):
    return _ev("k", event_time, arrival, cents)


def hourly(ooo_min, lateness_min):
    return EventTimeWindowOperator(60 * MIN, ooo_min * MIN, lateness_min * MIN, USD)


class AssignerAndWatermarkTest(unittest.TestCase):
    def test_window_assignment(self):
        assigner = TumblingWindowAssigner(60 * MIN)
        self.assertEqual(assigner.assign(t("11:59")), Window(t("11:00"), t("12:00")))
        self.assertEqual(assigner.assign(t("12:00")).start, t("12:00"))
        self.assertEqual(assigner.assign(-1).start, -60 * MIN)
        with self.assertRaises(ValueError):
            TumblingWindowAssigner(0)

    def test_watermark_generator(self):
        wm = BoundedOutOfOrdernessWatermark(5 * MIN)
        self.assertIsNone(wm.current())
        self.assertEqual(wm.observe(t("12:09")), t("12:04"))
        self.assertEqual(wm.observe(t("11:30")), t("12:04"))
        self.assertEqual(wm.observe(t("12:20")), t("12:15"))
        with self.assertRaises(ValueError):
            BoundedOutOfOrdernessWatermark(-1)


class EventTimeOperatorTest(unittest.TestCase):
    def test_late_data_lifecycle(self):
        op = hourly(5, 10)
        op.process(ev("11:40", "11:40", 1999))
        op.process(ev("12:02", "12:02", 3000))
        r1 = op.process(ev("11:59", "12:03", 7700))
        self.assertEqual(r1, [])
        self.assertEqual(op.current_watermark(), t("11:57"))
        r2 = op.process(ev("12:09", "12:09", 2000))
        self.assertEqual(len(r2), 1)
        self.assertEqual((r2[0].window_start, r2[0].window_end), (t("11:00"), t("12:00")))
        self.assertEqual(r2[0].count, 2)
        self.assertEqual(r2[0].sum, Money.of_minor(9699, USD))
        self.assertEqual(r2[0].kind, FiringKind.ON_TIME)

        # allowed lateness: a late event within the bound re-fires the window as a correction
        r3 = op.process(ev("11:57", "12:10", 999))
        self.assertEqual(len(r3), 1)
        self.assertEqual(r3[0].kind, FiringKind.LATE_UPDATE)
        self.assertEqual(r3[0].count, 3)
        self.assertEqual(r3[0].sum, Money.of_minor(10698, USD))
        self.assertEqual(op.side_output(), [])

        # beyond allowed lateness: side output, never silent drop
        op.process(ev("12:16", "12:16", 600))  # watermark 12:11 >= 12:00 + 10 min: window GC'd
        r4 = op.process(ev("11:50", "12:20", 2500))
        self.assertEqual(r4, [])
        self.assertEqual(len(op.side_output()), 1)
        late = op.side_output()[0]
        self.assertEqual((late.window_start, late.window_end), (t("11:00"), t("12:00")))
        self.assertEqual(late.watermark_at_arrival, t("12:11"))
        self.assertEqual(late.reason, LateReason.WINDOW_CLOSED)
        self.assertEqual(op.open_window_count(), 1)

        # the next window fires when the watermark passes 13:00
        r5 = op.process(ev("13:06", "13:06", 100))
        self.assertEqual(len(r5), 1)
        self.assertEqual(r5[0].window_start, t("12:00"))
        self.assertEqual(r5[0].count, 3)

        # invariant: every event accounted for exactly once
        self.assertTrue(op.accounts_for_every_event())
        on_time_11 = [x for x in op.all_emitted() if x.kind == FiringKind.ON_TIME and x.window_start == t("11:00")]
        self.assertEqual(len(on_time_11), 1)

    def test_zero_out_of_orderness(self):
        strict = hourly(0, 0)
        strict.process(ev("11:40", "11:40", 100))
        s1 = strict.process(ev("12:02", "12:02", 100))
        self.assertEqual(len(s1), 1)
        self.assertEqual(s1[0].count, 1)
        strict.process(ev("11:59", "12:03", 100))
        self.assertEqual(len(strict.side_output()), 1)
        self.assertTrue(strict.accounts_for_every_event())

    def test_negative_lateness_rejected(self):
        with self.assertRaises(ValueError):
            EventTimeWindowOperator(60 * MIN, 0, -1, USD)


class ProcessingTimeAndMoneyTest(unittest.TestCase):
    def test_processing_time_never_sees_late_data(self):
        pt = ProcessingTimeWindowOperator(60 * MIN, USD)
        pt.process(ev("11:59", "12:03", 7700))
        self.assertEqual(pt.results()[0].window_start, t("12:00"))

    def test_money_discipline(self):
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).plus(Money.of_minor(1, Currency("EUR", 2)))
        with self.assertRaises(TypeError):
            Money(1.5, USD)


if __name__ == "__main__":
    unittest.main()
