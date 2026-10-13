// Tests for phases/14-payments-at-scale/08-cost-and-authorization-optimization. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.Set;

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

    static final Currency USD = Currency.USD;
    static final Set<Lever> NONE = EnumSet.noneOf(Lever.class);

    static Txn txn(String id, long minor, CardType type, boolean token, ResponseCode base, int dToken, int dRetry, int dUpdater) {
        return new Txn(id, Money.ofMinor(minor, USD), type, token, base, dToken, dRetry, dUpdater);
    }

    public static void main(String[] args) {
        AuthExperiment exp = new AuthExperiment(PriceList.standard(USD), USD);

        // interchange and network fees, exact
        check("L1 commercial on 1000.00 is 26.60", exp.interchange(Money.ofMinor(100_000, USD), CardType.COMMERCIAL, false).minorUnits() == 2_660);
        check("L3 commercial on 1000.00 is 18.60", exp.interchange(Money.ofMinor(100_000, USD), CardType.COMMERCIAL, true).minorUnits() == 1_860);
        check("L3 data does not change consumer credit", exp.interchange(Money.ofMinor(5_000, USD), CardType.CONSUMER_CREDIT, true)
                .equals(exp.interchange(Money.ofMinor(5_000, USD), CardType.CONSUMER_CREDIT, false)));
        check("regulated debit on 50.00 is 0.05% + 0.22 = 0.245 -> 0.24 (half-even)",
                exp.interchange(Money.ofMinor(5_000, USD), CardType.REGULATED_DEBIT, false).minorUnits() == 24);
        check("least-cost debit network fee on 100.00 is 0.06 vs 0.13",
                exp.networkFee(Money.ofMinor(10_000, USD), CardType.REGULATED_DEBIT, true).minorUnits() == 6
                        && exp.networkFee(Money.ofMinor(10_000, USD), CardType.REGULATED_DEBIT, false).minorUnits() == 13);

        // per-transaction paths
        Txn expired = txn("e", 4_999, CardType.CONSUMER_CREDIT, true, ResponseCode.EXPIRED_CARD, 9_000, 9_000, 1_000);
        check("expired card is not retried unchanged", exp.simulate(expired, EnumSet.of(Lever.SOFT_DECLINE_RETRY)).attempts() == 1);
        TxnTrace upd = exp.simulate(expired, EnumSet.of(Lever.ACCOUNT_UPDATER));
        check("updater match approves and charges the hit fee", upd.finalOutcome().approved() && upd.updaterHit() && upd.fees().minorUnits() == 20);
        Txn expiredMiss = txn("m", 4_999, CardType.CONSUMER_CREDIT, false, ResponseCode.EXPIRED_CARD, 0, 0, 8_000);
        check("updater miss (draw 8000 >= 8000) stays declined, no hit fee", !exp.simulate(expiredMiss, EnumSet.of(Lever.ACCOUNT_UPDATER)).updaterHit());
        TxnTrace tok = exp.simulate(expired, EnumSet.of(Lever.NETWORK_TOKEN, Lever.ACCOUNT_UPDATER));
        check("token fixes the expired card first, so no updater fee", tok.finalOutcome().approved() && !tok.updaterHit() && tok.fees().minorUnits() == 6);
        Txn notEligible = txn("n", 4_999, CardType.CONSUMER_CREDIT, false, ResponseCode.EXPIRED_CARD, 0, 0, 9_999);
        check("token lever does nothing for an ineligible card", !exp.simulate(notEligible, EnumSet.of(Lever.NETWORK_TOKEN)).tokenUsed());

        Txn dnh = txn("d", 2_000, CardType.CONSUMER_CREDIT, true, ResponseCode.DO_NOT_HONOR, 2_499, 9_999, 0);
        check("token recovers 05 when draw < 2500", exp.simulate(dnh, EnumSet.of(Lever.NETWORK_TOKEN)).finalOutcome().approved());
        Txn dnh2 = txn("d2", 2_000, CardType.CONSUMER_CREDIT, true, ResponseCode.DO_NOT_HONOR, 2_500, 999, 0);
        TxnTrace r05 = exp.simulate(dnh2, EnumSet.of(Lever.SOFT_DECLINE_RETRY));
        check("05 gets exactly one retry, approved when draw < 1000", r05.attempts() == 2 && r05.retried() && r05.finalOutcome().approved());

        for (ResponseCode hard : List.of(ResponseCode.LOST_OR_STOLEN, ResponseCode.NOT_PERMITTED)) {
            TxnTrace h = exp.simulate(txn("h", 1_000, CardType.CONSUMER_CREDIT, true, hard, 0, 0, 0), EnumSet.allOf(Lever.class));
            check("never retry " + hard.code, h.attempts() == 1 && !h.finalOutcome().approved());
        }

        Txn timeout = txn("t", 3_000, CardType.CONSUMER_CREDIT, false, ResponseCode.PROCESSOR_TIMEOUT, 0, 8_999, 0);
        TxnTrace fo = exp.simulate(timeout, EnumSet.of(Lever.ROUTING, Lever.SOFT_DECLINE_RETRY));
        check("timeout fails over once and is not also retried", fo.failedOver() && !fo.retried() && fo.attempts() == 2 && fo.finalOutcome().approved());
        Txn timeout2 = txn("t2", 3_000, CardType.CONSUMER_CREDIT, false, ResponseCode.PROCESSOR_TIMEOUT, 0, 9_000, 0);
        check("failover with draw 9000 ends as issuer unavailable", exp.simulate(timeout2, EnumSet.of(Lever.ROUTING)).finalOutcome() == ResponseCode.ISSUER_UNAVAILABLE);
        check("same txn, same levers, same trace", exp.simulate(timeout, EnumSet.allOf(Lever.class)).equals(exp.simulate(timeout, EnumSet.allOf(Lever.class))));
        checkThrows("draws must be basis points", IllegalArgumentException.class,
                () -> txn("x", 1, CardType.CONSUMER_CREDIT, false, ResponseCode.APPROVED, 10_000, 0, 0));

        // the experiment: common random numbers make every delta exact and reproducible
        List<Txn> pop = AuthExperiment.population(20_000, 7, USD);
        ArmResult control = exp.run(pop, NONE);
        ArmResult control2 = exp.run(AuthExperiment.population(20_000, 7, USD), NONE);
        check("control is reproducible", control.equals(control2));
        check("control makes one attempt per transaction", control.attempts() == pop.size());
        long baseApproved = pop.stream().filter(t -> t.baseOutcome().approved()).count();
        check("control approvals equal base approvals", control.approvals() == baseApproved);

        int[] singles = new int[Lever.values().length];
        for (Lever l : Lever.values()) {
            ArmResult arm = exp.run(pop, EnumSet.of(l));
            singles[l.ordinal()] = arm.approvals() - control.approvals();
            check(l + " never lowers approvals", arm.approvals() >= control.approvals());
        }
        ArmResult all = exp.run(pop, EnumSet.allOf(Lever.class));
        int allUplift = all.approvals() - control.approvals();
        int sum = 0, max = 0;
        for (int s : singles) { sum += s; max = Math.max(max, s); }
        check("all levers beat the best single lever", allUplift > max);
        check("uplifts are not additive (overlap)", allUplift < sum);
        check("LEVEL3_DATA changes cost, not approvals", singles[Lever.LEVEL3_DATA.ordinal()] == 0
                && exp.run(pop, EnumSet.of(Lever.LEVEL3_DATA)).interchange().compareTo(control.interchange()) < 0);
        long expiredEligible = pop.stream().filter(t -> t.baseOutcome() == ResponseCode.EXPIRED_CARD && t.tokenEligible()).count();
        long dnhRecovered = pop.stream().filter(t -> t.baseOutcome() == ResponseCode.DO_NOT_HONOR && t.tokenEligible() && t.drawToken() < 2_500).count();
        check("token uplift is exactly eligible expired + recovered 05", singles[Lever.NETWORK_TOKEN.ordinal()] == expiredEligible + dnhRecovered);
        long hardResent = pop.stream().filter(t -> t.baseOutcome().declineClass == DeclineClass.NEVER_RETRY)
                .filter(t -> exp.simulate(t, EnumSet.allOf(Lever.class)).attempts() > 1).count();
        check("no hard decline is ever re-sent", hardResent == 0);
        Uplift u = Uplift.of("ALL", control, all, 3_000);
        check("net value = 30% of extra volume - extra cost",
                u.netValue().equals(u.extraVolume().times(3_000, 10_000, Rounding.HALF_EVEN).minus(u.extraCost())));
        check("rate delta in bps matches approvals delta", u.rateDeltaBps() == (long) all.approvals() * 10_000 / pop.size() - (long) control.approvals() * 10_000 / pop.size());

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
