// Tests for phases/08-messaging-and-streams/08-watermarks-and-late-data. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static final long MIN = Main.MINUTE;

    static Event ev(String eventTime, String arrival, long cents) {
        return Main.ev("k", eventTime, arrival, cents);
    }

    static EventTimeWindowOperator hourly(long oooMin, long latenessMin) {
        return new EventTimeWindowOperator(60 * MIN, oooMin * MIN, latenessMin * MIN, Currency.USD);
    }

    public static void main(String[] args) {
        // window assignment
        var assigner = new TumblingWindowAssigner(60 * MIN);
        check("11:59 belongs to [11:00, 12:00)", assigner.assign(Main.t("11:59")).equals(new Window(Main.t("11:00"), Main.t("12:00"))));
        check("12:00 belongs to [12:00, 13:00): half-open", assigner.assign(Main.t("12:00")).start() == Main.t("12:00"));
        check("negative timestamps floor correctly", assigner.assign(-1).start() == -60 * MIN);
        checkThrows("zero window size rejected", IllegalArgumentException.class, () -> new TumblingWindowAssigner(0));

        // watermark generator
        var wm = new BoundedOutOfOrdernessWatermark(5 * MIN);
        check("watermark unknown before any event", wm.current() == Long.MIN_VALUE);
        check("watermark = max event time - bound", wm.observe(Main.t("12:09")) == Main.t("12:04"));
        check("watermark never moves backwards", wm.observe(Main.t("11:30")) == Main.t("12:04"));
        check("watermark advances on a newer event", wm.observe(Main.t("12:20")) == Main.t("12:15"));
        checkThrows("negative out-of-orderness rejected", IllegalArgumentException.class, () -> new BoundedOutOfOrdernessWatermark(-1));

        // the 11:59 event arriving at 12:03 is caught by a 5-minute watermark
        var op = hourly(5, 10);
        op.process(ev("11:40", "11:40", 1999));
        op.process(ev("12:02", "12:02", 3000));
        List<WindowResult> r1 = op.process(ev("11:59", "12:03", 7700));
        check("11:59 arriving at 12:03 fires nothing yet", r1.isEmpty());
        check("watermark is 11:57 so the 11:00 window is still open", op.currentWatermark() == Main.t("11:57"));
        List<WindowResult> r2 = op.process(ev("12:09", "12:09", 2000));
        check("12:09 advances watermark to 12:04 and fires exactly one window", r2.size() == 1);
        check("fired window is [11:00, 12:00)", r2.get(0).windowStart() == Main.t("11:00") && r2.get(0).windowEnd() == Main.t("12:00"));
        check("fired window counts the 11:59 event", r2.get(0).count() == 2);
        check("fired window sums with Money, 19.99 + 77.00", r2.get(0).sum().equals(Money.ofMinor(9699, Currency.USD)));
        check("first firing is ON_TIME", r2.get(0).kind() == FiringKind.ON_TIME);

        // allowed lateness: a late event within the bound re-fires the window as a correction
        List<WindowResult> r3 = op.process(ev("11:57", "12:10", 999));
        check("late-but-allowed event re-fires the window", r3.size() == 1 && r3.get(0).kind() == FiringKind.LATE_UPDATE);
        check("late update carries the corrected count", r3.get(0).count() == 3);
        check("late update carries the corrected sum", r3.get(0).sum().equals(Money.ofMinor(10698, Currency.USD)));
        check("no side output yet", op.sideOutput().isEmpty());

        // beyond allowed lateness: side output, never silent drop
        op.process(ev("12:16", "12:16", 600));          // watermark 12:11 >= 12:00 + 10 min: window GC'd
        List<WindowResult> r4 = op.process(ev("11:50", "12:20", 2500));
        check("too-late event fires nothing", r4.isEmpty());
        check("too-late event lands in side output", op.sideOutput().size() == 1);
        LateEvent late = op.sideOutput().get(0);
        check("side output records the target window", late.windowStart() == Main.t("11:00") && late.windowEnd() == Main.t("12:00"));
        check("side output records the watermark at arrival", late.watermarkAtArrival() == Main.t("12:11"));
        check("side output records the reason", late.reason() == LateReason.WINDOW_CLOSED);
        check("the 11:00 window is no longer held in memory", op.openWindowCount() == 1);

        // the next window fires when the watermark passes 13:00
        List<WindowResult> r5 = op.process(ev("13:06", "13:06", 100));
        check("12:00 window fires when watermark reaches 13:01", r5.size() == 1 && r5.get(0).windowStart() == Main.t("12:00"));
        check("12:00 window count is 3 (12:02, 12:09, 12:16)", r5.get(0).count() == 3);

        // invariant: every event accounted for exactly once
        check("events = open + closed + side output", op.accountsForEveryEvent());
        check("ON_TIME fires exactly once per window",
                op.allEmitted().stream().filter(x -> x.kind() == FiringKind.ON_TIME && x.windowStart() == Main.t("11:00")).count() == 1);

        // zero out-of-orderness: the 11:59 event arriving after 12:02 is late
        var strict = hourly(0, 0);
        strict.process(ev("11:40", "11:40", 100));
        List<WindowResult> s1 = strict.process(ev("12:02", "12:02", 100));
        check("zero bound: 12:02 fires the 11:00 window immediately", s1.size() == 1 && s1.get(0).count() == 1);
        strict.process(ev("11:59", "12:03", 100));
        check("zero bound and zero lateness: 11:59 goes straight to the side output", strict.sideOutput().size() == 1);
        check("strict operator still accounts for every event", strict.accountsForEveryEvent());

        // processing-time operator never sees late data because it never asks when things happened
        var pt = new ProcessingTimeWindowOperator(60 * MIN, Currency.USD);
        pt.process(ev("11:59", "12:03", 7700));
        check("processing time puts the 11:59 payment in the 12:00 hour", pt.results().get(0).windowStart() == Main.t("12:00"));

        // money discipline
        checkThrows("mixing currencies in a window sum is an error", IllegalArgumentException.class,
                () -> Money.ofMinor(1, Currency.USD).plus(Money.ofMinor(1, new Currency("EUR", 2))));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
