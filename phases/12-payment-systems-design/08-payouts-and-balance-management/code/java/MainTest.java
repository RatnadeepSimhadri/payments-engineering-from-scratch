// Tests for phases/12-payment-systems-design/08-payouts-and-balance-management. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.DayOfWeek;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static final Currency USD = Currency.USD;
    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    record Fixture(FakeClock clock, InMemoryPayoutFileSink files, BalanceService svc) {}

    /** Monday 2026-03-02; merchant "m" pays out daily with a 10.00 minimum and a 10% / 90-day reserve. */
    static Fixture fixture(long reserveBps) {
        FakeClock clock = new FakeClock(LocalDate.of(2026, 3, 2));
        InMemoryPayoutFileSink files = new InMemoryPayoutFileSink();
        BalanceService svc = new BalanceService(new Ledger(USD), clock, files, 7, USD);
        svc.onboard(new Merchant("m", "ba_tok_1", new PayoutSchedule(PayoutFrequency.DAILY, null, usd(1_000)), new ReservePolicy(reserveBps, 90)));
        return new Fixture(clock, files, svc);
    }

    static boolean consistent(BalanceService svc, String m) {
        return svc.ledger().balances() && svc.scheduledReserve(m).equals(svc.balance(m).reserve());
    }

    public static void main(String[] args) {
        // --- pending is not payable -----------------------------------------------------------------
        Fixture f = fixture(1_000);
        BalanceService svc = f.svc();
        CaptureRecord c1 = svc.capture("c1", "m", usd(10_000), 290, 30);
        check("net = 100.00 - 3.20", c1.net().equals(usd(9_680)));
        check("capture lands in pending", svc.balance("m").pending().equals(usd(9_680)) && svc.balance("m").available().isZero());
        check("no payout from pending funds", svc.runPayouts().isEmpty() && f.files().files.isEmpty());
        f.clock().advanceDays(10);
        check("time alone does not settle anything", svc.runPayouts().isEmpty() && !svc.isSettled("c1"));

        // --- settlement reconciles before it moves money -------------------------------------------------
        checkThrows("report total mismatch refused", IllegalStateException.class,
                () -> svc.settle(new SettlementReport("bad", f.clock().today(), List.of("c1"), usd(9_999))));
        checkThrows("unknown capture in report refused", IllegalStateException.class,
                () -> svc.settle(new SettlementReport("bad2", f.clock().today(), List.of("c1", "nope"), usd(10_000))));
        check("refused reports changed nothing", svc.balance("m").pending().equals(usd(9_680)) && svc.ledger().balance(BalanceService.BANK).isZero());
        svc.settle(new SettlementReport("s1", f.clock().today(), List.of("c1"), usd(10_000)));
        check("settled: reserve = floor(9680 x 10%) = 9.68", svc.balance("m").reserve().equals(usd(968)));
        check("settled: available = net - reserve = 87.12", svc.balance("m").available().equals(usd(8_712)) && svc.balance("m").pending().isZero());
        check("cash arrived in bank", svc.ledger().balance(BalanceService.BANK).equals(usd(10_000)));
        checkThrows("settling the same capture twice refused", IllegalStateException.class,
                () -> svc.settle(new SettlementReport("s1b", f.clock().today(), List.of("c1"), usd(10_000))));
        check("tranches match reserve account", consistent(svc, "m"));

        // --- payout and its file ----------------------------------------------------------------------------
        List<Payout> batch = svc.runPayouts();   // 2026-03-12, a Thursday
        check("one payout of all of available", batch.size() == 1 && batch.get(0).amount().equals(usd(8_712)));
        check("available moved to in transit", svc.balance("m").available().isZero() && svc.balance("m").inTransit().equals(usd(8_712)));
        List<String> file = f.files().files.values().iterator().next();
        check("file has header, one detail, trailer", file.size() == 3 && file.get(0).startsWith("H,") && file.get(2).startsWith("T,"));
        check("trailer control totals: 1 line, 8712 credits, 0 debits", file.get(2).equals("T,1,8712,0"));
        check("detail line names payout, merchant, bank token, direction, minor units",
                file.get(1).equals("D," + batch.get(0).id + ",m,ba_tok_1,CREDIT,8712"));
        check("total owed unchanged by initiating a payout", svc.balance("m").total().equals(usd(9_680)));
        svc.confirm(batch.get(0).id);
        check("confirmed: cash left, in transit cleared", svc.balance("m").inTransit().isZero()
                && svc.ledger().balance(BalanceService.BANK).equals(usd(1_288)) && batch.get(0).state() == PayoutState.PAID);
        svc.confirm(batch.get(0).id);
        check("duplicate confirmation is a no-op", svc.ledger().balance(BalanceService.BANK).equals(usd(1_288)));
        check("a second run the same day pays nothing", svc.runPayouts().isEmpty());

        // --- minimums and schedules ----------------------------------------------------------------------------
        Fixture g = fixture(0);
        g.svc().capture("g1", "m", usd(1_000), 290, 30);   // net 9.41 < 10.00 minimum
        g.svc().settle(new SettlementReport("gs", g.clock().today(), List.of("g1"), usd(1_000)));
        check("below minimum: no payout", g.svc().runPayouts().isEmpty() && g.svc().balance("m").available().equals(usd(941)));
        g.svc().capture("g2", "m", usd(100), 0, 0);
        g.svc().settle(new SettlementReport("gs2", g.clock().today(), List.of("g2"), usd(100)));
        check("at or above minimum: paid", g.svc().runPayouts().size() == 1);
        PayoutSchedule weekly = new PayoutSchedule(PayoutFrequency.WEEKLY, DayOfWeek.FRIDAY, usd(0));
        check("weekly schedule due only on its day", weekly.dueOn(LocalDate.of(2026, 3, 6)) && !weekly.dueOn(LocalDate.of(2026, 3, 5)));
        PayoutSchedule daily = new PayoutSchedule(PayoutFrequency.DAILY, null, usd(0));
        check("daily schedule skips weekends", daily.dueOn(LocalDate.of(2026, 3, 2)) && !daily.dueOn(LocalDate.of(2026, 3, 7)) && !daily.dueOn(LocalDate.of(2026, 3, 8)));

        // --- reserve release -------------------------------------------------------------------------------------
        Fixture r = fixture(1_000);
        r.svc().capture("r1", "m", usd(10_000), 290, 30);
        r.svc().settle(new SettlementReport("rs", r.clock().today(), List.of("r1"), usd(10_000)));
        r.clock().advanceDays(89);
        check("reserve not released on day 89", r.svc().releaseReserves().isZero() && r.svc().balance("m").reserve().equals(usd(968)));
        r.clock().advanceDays(1);
        check("reserve released on day 90", r.svc().releaseReserves().equals(usd(968)) && r.svc().balance("m").reserve().isZero()
                && r.svc().balance("m").available().equals(usd(9_680)));
        check("release is not repeated", r.svc().releaseReserves().isZero() && consistent(r.svc(), "m"));

        // --- negative balances --------------------------------------------------------------------------------------
        Fixture n = fixture(1_000);
        BalanceService ns = n.svc();
        ns.capture("n1", "m", usd(10_000), 290, 30);
        ns.settle(new SettlementReport("ns1", n.clock().today(), List.of("n1"), usd(10_000)));
        ns.runPayouts();
        ns.debitMerchant("m", usd(20_000), "chargeback");
        check("chargeback drives available negative", ns.balance("m").available().equals(usd(-20_000)));
        check("negative since today", n.clock().today().equals(ns.negativeSince("m")));
        Money used = ns.coverFromReserve("m");
        check("reserve covers what it can", used.equals(usd(968)) && ns.balance("m").reserve().isZero()
                && ns.balance("m").available().equals(usd(-19_032)) && consistent(ns, "m"));
        check("no credit payout while negative", ns.runPayouts().isEmpty());
        ns.capture("n2", "m", usd(5_000), 0, 0);
        ns.settle(new SettlementReport("ns2", n.clock().today(), List.of("n2"), usd(5_000)));
        ns.runPayouts();
        check("settlement nets against the deficit (reserve share applied too)", ns.balance("m").available().equals(usd(-14_032)));
        n.clock().advanceDays(6);
        check("day 6 negative: no collection yet", ns.runPayouts().isEmpty());
        n.clock().advanceDays(1);
        List<Payout> coll = ns.runPayouts();
        check("day 7 negative: one DEBIT for the deficit", coll.size() == 1 && coll.get(0).direction == PayoutDirection.DEBIT
                && coll.get(0).amount().equals(usd(14_032)));
        check("collection zeroes available and clears negativeSince", ns.balance("m").available().isZero() && ns.negativeSince("m") == null);
        List<String> collFile = new ArrayList<>(n.files().files.values()).get(n.files().files.size() - 1);
        check("collection trailer counts the debit", collFile.get(collFile.size() - 1).equals("T,1,0,14032"));
        ns.returned(coll.get(0).id, "R01");
        check("returned collection (R01) restores the deficit", ns.balance("m").available().equals(usd(-14_032))
                && ns.negativeSince("m") != null && coll.get(0).state() == PayoutState.RETURNED);
        checkThrows("cannot confirm a returned line", IllegalStateException.class, () -> ns.confirm(coll.get(0).id));
        check("ledger still balances", ns.ledger().balances());

        // --- returned credit pauses payouts ---------------------------------------------------------------------------
        Fixture p = fixture(0);
        p.svc().capture("p1", "m", usd(5_000), 0, 0);
        p.svc().settle(new SettlementReport("ps", p.clock().today(), List.of("p1"), usd(5_000)));
        Payout po = p.svc().runPayouts().get(0);
        p.svc().returned(po.id, "R03");
        check("returned payout goes back to available", p.svc().balance("m").available().equals(usd(5_000)) && p.svc().balance("m").inTransit().isZero());
        check("merchant paused after a return", p.svc().isPaused("m"));
        p.clock().advanceDays(1);
        check("paused merchant is not paid", p.svc().runPayouts().isEmpty());
        p.svc().resume("m", "fixed");
        check("resumed merchant is paid", p.svc().runPayouts().size() == 1);

        // --- conservation ---------------------------------------------------------------------------------------------
        Fixture k = fixture(1_000);
        BalanceService ks = k.svc();
        long gross = 0;
        for (int i = 0; i < 20; i++) {
            ks.capture("k" + i, "m", usd(1_000 + 37L * i), 290, 30);
            gross += 1_000 + 37L * i;
        }
        List<String> ids = new ArrayList<>();
        for (int i = 0; i < 20; i++) ids.add("k" + i);
        ks.settle(new SettlementReport("ks", k.clock().today(), ids, usd(gross)));
        Payout kp = ks.runPayouts().get(0);
        ks.confirm(kp.id);
        Money owed = ks.balance("m").total();
        Money fees = ks.ledger().balance(BalanceService.FEES);
        check("gross = paid out + still owed + fees", usd(gross).equals(kp.amount().plus(owed).plus(fees)));
        check("bank = settled - paid", ks.ledger().balance(BalanceService.BANK).equals(usd(gross).minus(kp.amount())));

        // --- validation -------------------------------------------------------------------------------------------------
        checkThrows("unknown merchant", IllegalArgumentException.class, () -> ks.balance("nobody"));
        checkThrows("reserve over 100% refused", IllegalArgumentException.class, () -> new ReservePolicy(10_001, 1));
        checkThrows("non-positive debit refused", IllegalArgumentException.class, () -> ks.debitMerchant("m", usd(0), "x"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String fl : failures) System.out.println("FAIL: " + fl);
        if (!failures.isEmpty()) System.exit(1);
    }
}
