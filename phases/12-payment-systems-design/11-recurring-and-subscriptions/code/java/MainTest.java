// Tests for phases/12-payment-systems-design/11-recurring-and-subscriptions. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
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

    static LocalDate d(int y, int m, int day) { return LocalDate.of(y, m, day); }

    record Rig(FakeClock clock, ScriptedGateway gateway, MapCardUpdater updater, BillingEngine engine) {
        static Rig create(LocalDate start, DunningPolicy policy) {
            FakeClock clock = new FakeClock(start);
            ScriptedGateway gateway = new ScriptedGateway();
            MapCardUpdater updater = new MapCardUpdater();
            return new Rig(clock, gateway, updater, new BillingEngine(clock, gateway, updater, policy));
        }
        void runDays(int days) {
            for (int i = 0; i < days; i++) { engine.runDay(); clock.advanceDays(1); }
        }
    }

    public static void main(String[] args) {
        // Billing cycles are computed from the anchor.
        LocalDate jan31 = d(2026, 1, 31);
        check("anchor period 1 clamps to Feb 28", BillingCycle.periodStart(jan31, Interval.MONTHLY, 1).equals(d(2026, 2, 28)));
        check("anchor period 2 returns to Mar 31", BillingCycle.periodStart(jan31, Interval.MONTHLY, 2).equals(d(2026, 3, 31)));
        check("leap year: Jan 31 anchor -> Feb 29 2028", BillingCycle.periodStart(d(2028, 1, 31), Interval.MONTHLY, 1).equals(d(2028, 2, 29)));
        check("chained dates drift to Mar 28", BillingCycle.chainedStarts(jan31, Interval.MONTHLY, 3).get(2).equals(d(2026, 3, 28)));
        check("yearly Feb 29 anchor -> Feb 28 in a common year", BillingCycle.periodStart(d(2028, 2, 29), Interval.YEARLY, 1).equals(d(2029, 2, 28)));
        check("yearly Feb 29 anchor -> Feb 29 again four years on", BillingCycle.periodStart(d(2028, 2, 29), Interval.YEARLY, 4).equals(d(2032, 2, 29)));
        check("quarterly from Nov 30 -> Feb 28", BillingCycle.periodStart(d(2025, 11, 30), Interval.QUARTERLY, 1).equals(d(2026, 2, 28)));
        check("period end is the next period's start", BillingCycle.periodEnd(jan31, Interval.MONTHLY, 0).equals(d(2026, 2, 28)));
        checkThrows("negative period index rejected", IllegalArgumentException.class, () -> BillingCycle.periodStart(jan31, Interval.MONTHLY, -1));

        // Proration: the worked example, to the cent.
        ProrationResult p = Proration.compute(Main.BASIC, Main.PRO, d(2026, 3, 1), d(2026, 4, 1), d(2026, 3, 11));
        check("used 10 days", p.usedDays() == 10);
        check("remaining 21 of 31 days", p.remainingDays() == 21 && p.periodDays() == 31);
        check("credit 20.32", p.credit().equals(Money.parse("20.32", Currency.USD)));
        check("charge 33.87", p.charge().equals(Money.parse("33.87", Currency.USD)));
        check("net 13.55", p.net().equals(Money.parse("13.55", Currency.USD)));
        ProrationResult down = Proration.compute(Main.PRO, Main.BASIC, d(2026, 3, 1), d(2026, 4, 1), d(2026, 3, 11));
        check("downgrade net is negative 13.55", down.net().equals(Money.parse("-13.55", Currency.USD)));
        check("change on the first day credits the whole old price",
                Proration.compute(Main.BASIC, Main.PRO, d(2026, 3, 1), d(2026, 4, 1), d(2026, 3, 1)).credit().equals(Main.BASIC.price()));

        // The invariant: used portion + credit == price, for every day of every month length.
        boolean conserved = true;
        Plan odd = new Plan("odd", Money.parse("9.99", Currency.USD), Interval.MONTHLY);
        for (LocalDate start : List.of(d(2026, 2, 1), d(2026, 4, 1), d(2026, 1, 1), d(2028, 2, 1))) {
            LocalDate end = start.plusMonths(1);
            for (LocalDate day = start; day.isBefore(end); day = day.plusDays(1)) {
                ProrationResult r = Proration.compute(odd, Main.PRO, start, end, day);
                Money[] split = odd.price().allocate(r.usedDays(), r.remainingDays());
                conserved &= split[0].plus(split[1]).equals(odd.price()) && split[1].equals(r.credit());
            }
        }
        check("allocation conserves the price for every change day", conserved);
        Money[] halves = odd.price().allocate(15, 15);
        check("9.99 split 15:15 is 5.00 + 4.99", halves[0].minorUnits() == 500 && halves[1].minorUnits() == 499);
        checkThrows("change date outside the period rejected", IllegalArgumentException.class,
                () -> Proration.compute(Main.BASIC, Main.PRO, d(2026, 3, 1), d(2026, 4, 1), d(2026, 4, 1)));
        checkThrows("cross-currency plan change rejected", CurrencyMismatchException.class,
                () -> Proration.compute(Main.BASIC, new Plan("eu", Money.parse("30.00", Currency.EUR), Interval.MONTHLY),
                        d(2026, 3, 1), d(2026, 4, 1), d(2026, 3, 11)));

        // State machine.
        check("PAST_DUE -> ACTIVE allowed", SubscriptionTransitions.allowed(SubscriptionStatus.PAST_DUE, SubscriptionStatus.ACTIVE));
        check("ACTIVE -> UNPAID not allowed (must go through PAST_DUE)", !SubscriptionTransitions.allowed(SubscriptionStatus.ACTIVE, SubscriptionStatus.UNPAID));
        check("CANCELED is terminal", SubscriptionTransitions.ALLOWED.get(SubscriptionStatus.CANCELED).isEmpty());
        checkThrows("illegal transition throws", IllegalTransitionException.class, () -> {
            Rig r = Rig.create(jan31, DunningPolicy.standard());
            Subscription s = r.engine().subscribe("s", "c", Main.BASIC, "tok", 0);
            r.engine().cancel("s", false);
            s.transition(SubscriptionStatus.ACTIVE);
        });

        // Dunning schedule: offsets from the first failure, then exhaustion.
        {
            Rig r = Rig.create(jan31, DunningPolicy.standard());
            r.gateway().script("tok_broke", ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS,
                    ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS);
            r.engine().subscribe("sub_trial", "cus", Main.PRO, "tok_broke", 14);
            check("trial starts TRIALING", r.engine().subscription("sub_trial").status == SubscriptionStatus.TRIALING);
            r.runDays(14);
            check("no charge during the trial", r.gateway().charges().isEmpty());
            r.runDays(1);   // Feb 14
            Invoice inv = r.engine().invoice("sub_trial:2026-02-14");
            check("first failure moves TRIALING -> PAST_DUE", r.engine().subscription("sub_trial").status == SubscriptionStatus.PAST_DUE);
            check("first retry is +1 day", inv.nextAttempt.equals(d(2026, 2, 15)));
            r.runDays(1);
            check("second retry is +3 days", inv.nextAttempt.equals(d(2026, 2, 17)));
            r.runDays(13);  // Feb 16 .. Feb 28: attempts on Feb 17, Feb 21, Feb 28
            check("five attempts in total", inv.attempts == DunningPolicy.standard().maxAttempts());
            check("invoice UNCOLLECTIBLE after exhaustion", inv.status == InvoiceStatus.UNCOLLECTIBLE);
            check("subscription CANCELED after exhaustion", r.engine().subscription("sub_trial").status == SubscriptionStatus.CANCELED);
            r.runDays(60);
            check("a canceled subscription is never billed again", r.gateway().charges().size() == 5);
        }

        // Recovery, idempotent days, and catch-up.
        {
            Rig r = Rig.create(jan31, DunningPolicy.standard());
            r.gateway().script("tok_flaky", ChargeOutcome.APPROVED, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.APPROVED);
            r.engine().subscribe("s", "c", Main.BASIC, "tok_flaky", 0);
            r.engine().runDay();
            r.engine().runDay();
            check("same-day rerun does not charge twice", r.gateway().charges().size() == 1);
            r.clock().set(d(2026, 2, 28));
            r.engine().runDay();
            check("renewal failure -> PAST_DUE", r.engine().subscription("s").status == SubscriptionStatus.PAST_DUE);
            r.clock().set(d(2026, 3, 1));
            r.engine().runDay();
            check("retry success -> ACTIVE", r.engine().subscription("s").status == SubscriptionStatus.ACTIVE);
            check("retry used a new reference", r.gateway().charges().get(2).startsWith("s:2026-02-28#2 "));
            r.clock().set(d(2026, 6, 15));
            r.engine().runDay();
            check("catch-up bills each missed period exactly once",
                    r.engine().invoice("s:2026-03-31") != null && r.engine().invoice("s:2026-04-30") != null
                            && r.engine().invoice("s:2026-05-31") != null && r.engine().invoices().size() == 5);
            check("after catch-up the next bill is the anchored Jun 30", r.engine().subscription("s").currentPeriodEnd.equals(d(2026, 6, 30)));
        }

        // Hard declines and the card updater.
        {
            Rig r = Rig.create(jan31, DunningPolicy.standard());
            r.gateway().script("tok_stolen", ChargeOutcome.DO_NOT_RETRY);
            r.gateway().script("tok_expired", ChargeOutcome.EXPIRED_CARD);
            r.gateway().script("tok_old", ChargeOutcome.EXPIRED_CARD);
            r.updater().replace("tok_old", "tok_new");
            r.engine().subscribe("stolen", "c1", Main.BASIC, "tok_stolen", 0);
            r.engine().subscribe("expired", "c2", Main.BASIC, "tok_expired", 0);
            r.engine().subscribe("updated", "c3", Main.BASIC, "tok_old", 0);
            r.engine().runDay();
            check("DO_NOT_RETRY exhausts after one attempt", r.engine().invoice("stolen:2026-01-31").attempts == 1
                    && r.engine().subscription("stolen").status == SubscriptionStatus.CANCELED);
            check("expired card with no update is not retried", r.engine().invoice("expired:2026-01-31").status == InvoiceStatus.UNCOLLECTIBLE);
            check("card updater replaces the token", r.engine().subscription("updated").cardToken.equals("tok_new"));
            check("updated card charged the same day", r.engine().invoice("updated:2026-01-31").status == InvoiceStatus.PAID);
            check("updater was consulted for both expired cards", r.updater().calls() == 2);
        }

        // UNPAID policy keeps the subscription but stops billing.
        {
            Rig r = Rig.create(jan31, new DunningPolicy(List.of(2), SubscriptionStatus.UNPAID));
            r.gateway().script("tok_x", ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS);
            r.engine().subscribe("u", "c", Main.BASIC, "tok_x", 0);
            r.runDays(40);
            check("exhausted with UNPAID policy -> UNPAID", r.engine().subscription("u").status == SubscriptionStatus.UNPAID);
            check("UNPAID creates no further invoices", r.engine().invoices().size() == 1);
        }

        // Cancel at period end, and plan changes through the engine.
        {
            Rig r = Rig.create(d(2026, 3, 1), DunningPolicy.standard());
            r.engine().subscribe("up", "c", Main.BASIC, "tok", 0);
            r.engine().subscribe("dn", "c", Main.PRO, "tok", 0);
            r.engine().subscribe("bye", "c", Main.BASIC, "tok", 0);
            r.engine().runDay();
            r.clock().set(d(2026, 3, 11));
            r.engine().changePlan("up", Main.PRO);
            check("upgrade invoices the net now", r.engine().invoice("up:proration:2026-03-11").amount.equals(Money.parse("13.55", Currency.USD))
                    && r.engine().invoice("up:proration:2026-03-11").status == InvoiceStatus.PAID);
            r.engine().changePlan("dn", Main.BASIC);
            check("downgrade becomes credit", r.engine().subscription("dn").creditBalance.equals(Money.parse("13.55", Currency.USD)));
            r.engine().cancel("bye", true);
            r.clock().set(d(2026, 4, 1));
            r.engine().runDay();
            check("credit reduces the next invoice", r.engine().invoice("dn:2026-04-01").amount.equals(Money.parse("16.45", Currency.USD)));
            check("upgraded plan bills the new price at renewal", r.engine().invoice("up:2026-04-01").amount.equals(Main.PRO.price()));
            check("cancel at period end -> CANCELED, no new invoice",
                    r.engine().subscription("bye").status == SubscriptionStatus.CANCELED && r.engine().invoice("bye:2026-04-01") == null);
        }

        // Policy validation and Money discipline.
        checkThrows("non-increasing retry offsets rejected", IllegalArgumentException.class,
                () -> new DunningPolicy(List.of(3, 3), SubscriptionStatus.CANCELED));
        checkThrows("exhaustion must end CANCELED or UNPAID", IllegalArgumentException.class,
                () -> new DunningPolicy(List.of(1), SubscriptionStatus.ACTIVE));
        checkThrows("Money rejects currency mix", CurrencyMismatchException.class,
                () -> Main.BASIC.price().plus(Money.parse("1.00", Currency.EUR)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
