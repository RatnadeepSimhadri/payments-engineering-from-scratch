// Lesson: phases/14-payments-at-scale/08-cost-and-authorization-optimization/docs/en.md
// Cost and Authorization-Rate Optimization
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Visa decline-code categories and retry limits; EMVCo Payment Tokenisation Specification;
// Federal Reserve Regulation II (12 CFR 235, debit interchange cap). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.Set;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    Money times(long numerator, long denominator, Rounding rounding) {
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twice = Math.abs(remainder) * 2, abs = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twice >= abs ? quotient + sign : quotient;
            case HALF_EVEN -> twice > abs ? quotient + sign : twice < abs ? quotient : (quotient % 2 == 0 ? quotient : quotient + sign);
        };
    }

    @Override
    public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Response codes, card types, and the price list
// ---------------------------------------------------------------------------------------------

/** How a decline may be treated. Mirrors the card networks' grouping of response codes. */
enum DeclineClass {
    NONE,        // approved
    NEVER_RETRY, // the issuer will never approve this card for this merchant: lost, stolen, closed
    RETRY_LATER, // the issuer cannot approve now: funds, issuer down, system error
    FIX_DATA,    // the credential is wrong or stale: retry only with new data
    GENERIC,     // no reason given; one retry is tolerated, more is abuse
}

/** ISO 8583 field 39 response codes used in this lesson. */
enum ResponseCode {
    APPROVED("00", DeclineClass.NONE),
    DO_NOT_HONOR("05", DeclineClass.GENERIC),
    LOST_OR_STOLEN("43", DeclineClass.NEVER_RETRY),
    INSUFFICIENT_FUNDS("51", DeclineClass.RETRY_LATER),
    EXPIRED_CARD("54", DeclineClass.FIX_DATA),
    NOT_PERMITTED("57", DeclineClass.NEVER_RETRY),
    ISSUER_UNAVAILABLE("91", DeclineClass.RETRY_LATER),
    PROCESSOR_TIMEOUT("96", DeclineClass.RETRY_LATER);

    final String code;
    final DeclineClass declineClass;

    ResponseCode(String code, DeclineClass declineClass) {
        this.code = code;
        this.declineClass = declineClass;
    }

    boolean approved() { return this == APPROVED; }
}

enum CardType { CONSUMER_CREDIT, REGULATED_DEBIT, COMMERCIAL }

enum Lever { NETWORK_TOKEN, ACCOUNT_UPDATER, SOFT_DECLINE_RETRY, ROUTING, LEVEL3_DATA }

/**
 * One transaction in the experiment. Its base outcome and three uniform draws in [0, 10000) are fixed
 * when the population is generated, so every arm of the experiment sees the same customer, the same
 * card, and the same luck. This is the common-random-numbers technique: differences between arms are
 * caused by the levers and nothing else.
 */
record Txn(String id, Money amount, CardType cardType, boolean tokenEligible, ResponseCode baseOutcome,
           int drawToken, int drawRetry, int drawUpdater) {
    Txn {
        for (int d : new int[] {drawToken, drawRetry, drawUpdater}) {
            if (d < 0 || d >= 10_000) throw new IllegalArgumentException("draws are basis points in [0, 10000)");
        }
    }
}

/**
 * Prices and probabilities. Probabilities are in basis points. Fees marked illustrative are
 * plausible orders of magnitude, not any provider's price list; the regulated-debit cap is
 * Regulation II's 0.21 USD + 5 bps + 0.01 fraud-prevention adjustment.
 */
record PriceList(
        Money authFee,                 // per attempt, charged by the processor (illustrative)
        Money tokenFee,                // per tokenized attempt (illustrative)
        Money updaterHitFee,           // per successful account-updater match (illustrative)
        long tokenRecoverDoNotHonorBps,// share of 05 declines approved when the token is used
        long updaterMatchBps,          // share of stale credentials the updater can refresh
        long retryInsufficientFundsBps,
        long retryIssuerUnavailableBps,
        long retryDoNotHonorBps,
        long retryTimeoutSameProcessorBps,
        long failoverTimeoutBps,       // approval when a timeout is re-sent via the secondary processor
        long premiumNetworkBps,        // network fee on the default debit network (illustrative)
        long leastCostNetworkBps,      // network fee on the cheaper debit network (illustrative)
        Map<CardType, long[]> interchange,  // {bps, fixed minor}
        long[] commercialLevel3) {     // {bps, fixed minor} with full line-item data

    static PriceList standard(Currency c) {
        Map<CardType, long[]> ic = new EnumMap<>(CardType.class);
        ic.put(CardType.CONSUMER_CREDIT, new long[] {180, 10});   // 1.80% + 0.10 (illustrative)
        ic.put(CardType.REGULATED_DEBIT, new long[] {5, 22});     // 0.05% + 0.22 (Regulation II cap with fraud adjustment)
        ic.put(CardType.COMMERCIAL, new long[] {265, 10});        // 2.65% + 0.10, level 1 data only (illustrative)
        return new PriceList(Money.ofMinor(5, c), Money.ofMinor(1, c), Money.ofMinor(15, c),
                2_500, 8_000, 1_500, 7_000, 1_000, 5_000, 9_000, 13, 6, Collections.unmodifiableMap(ic), new long[] {185, 10});
    }
}

/** The outcome of running one arm of the experiment over the whole population. */
record ArmResult(Set<Lever> levers, int transactions, int attempts, int approvals, Money approvedVolume,
                 Money authFees, Money tokenFees, Money updaterFees, Money networkFees, Money interchange) {

    Money totalCost() { return authFees.plus(tokenFees).plus(updaterFees).plus(networkFees).plus(interchange); }

    /** Approval rate per transaction (not per attempt), in basis points. */
    long approvalRateBps() { return transactions == 0 ? 0 : (long) approvals * 10_000 / transactions; }
}

/** Per-transaction trace: what happened, how many attempts, what it cost. Used by the tests and the demo. */
record TxnTrace(ResponseCode finalOutcome, int attempts, boolean tokenUsed, boolean updaterHit, boolean failedOver,
                boolean retried, Money fees) {}

final class AuthExperiment {
    final PriceList prices;
    final Currency currency;

    AuthExperiment(PriceList prices, Currency currency) {
        this.prices = prices;
        this.currency = currency;
    }

    /** Decide one transaction's path under a set of levers. Pure: same txn, same levers, same trace. */
    TxnTrace simulate(Txn t, Set<Lever> levers) {
        ResponseCode outcome = t.baseOutcome();
        boolean useToken = levers.contains(Lever.NETWORK_TOKEN) && t.tokenEligible();
        boolean updaterHit = false;
        Money fees = Money.zero(currency);

        // 1. Credential levers run before the first attempt.
        if (outcome == ResponseCode.EXPIRED_CARD) {
            if (useToken) {
                outcome = ResponseCode.APPROVED;          // the token's lifecycle management already holds the new card
            } else if (levers.contains(Lever.ACCOUNT_UPDATER) && t.drawUpdater() < prices.updaterMatchBps()) {
                outcome = ResponseCode.APPROVED;
                updaterHit = true;
                fees = fees.plus(prices.updaterHitFee());
            }
        }
        if (useToken && outcome == ResponseCode.DO_NOT_HONOR && t.drawToken() < prices.tokenRecoverDoNotHonorBps()) {
            outcome = ResponseCode.APPROVED;              // issuers approve more tokenized card-not-present traffic
        }

        // 2. First attempt.
        int attempts = 1;
        fees = fees.plus(prices.authFee());
        if (useToken) fees = fees.plus(prices.tokenFee());

        // 3. Timeout at the primary: fail over to the secondary processor.
        boolean failedOver = false;
        if (outcome == ResponseCode.PROCESSOR_TIMEOUT && levers.contains(Lever.ROUTING)) {
            attempts++;
            failedOver = true;
            fees = fees.plus(prices.authFee());
            if (useToken) fees = fees.plus(prices.tokenFee());
            outcome = t.drawRetry() < prices.failoverTimeoutBps() ? ResponseCode.APPROVED : ResponseCode.ISSUER_UNAVAILABLE;
        }

        // 4. One retry for soft declines only. NEVER_RETRY and FIX_DATA are never re-sent unchanged.
        boolean retried = false;
        if (!failedOver && levers.contains(Lever.SOFT_DECLINE_RETRY) && retryable(outcome)) {
            attempts++;
            retried = true;
            fees = fees.plus(prices.authFee());
            if (useToken) fees = fees.plus(prices.tokenFee());
            if (t.drawRetry() < retrySuccessBps(outcome)) outcome = ResponseCode.APPROVED;
        }
        return new TxnTrace(outcome, attempts, useToken, updaterHit, failedOver, retried, fees);
    }

    static boolean retryable(ResponseCode r) {
        return r.declineClass == DeclineClass.RETRY_LATER || r.declineClass == DeclineClass.GENERIC;
    }

    long retrySuccessBps(ResponseCode r) {
        return switch (r) {
            case INSUFFICIENT_FUNDS -> prices.retryInsufficientFundsBps();
            case ISSUER_UNAVAILABLE -> prices.retryIssuerUnavailableBps();
            case DO_NOT_HONOR -> prices.retryDoNotHonorBps();
            case PROCESSOR_TIMEOUT -> prices.retryTimeoutSameProcessorBps();
            default -> 0;
        };
    }

    /** Interchange on an approved transaction, given whether level 3 data was sent. */
    Money interchange(Money amount, CardType type, boolean level3) {
        long[] rate = type == CardType.COMMERCIAL && level3 ? prices.commercialLevel3() : prices.interchange().get(type);
        return amount.times(rate[0], 10_000, Rounding.HALF_EVEN).plus(Money.ofMinor(rate[1], amount.currency()));
    }

    /** Network fee on an approved transaction; regulated debit can be routed to the cheaper network. */
    Money networkFee(Money amount, CardType type, boolean leastCost) {
        long bps = type == CardType.REGULATED_DEBIT && leastCost ? prices.leastCostNetworkBps() : prices.premiumNetworkBps();
        return amount.times(bps, 10_000, Rounding.HALF_EVEN);
    }

    ArmResult run(List<Txn> population, Set<Lever> levers) {
        int attempts = 0, approvals = 0;
        Money volume = Money.zero(currency), auth = Money.zero(currency), token = Money.zero(currency);
        Money updater = Money.zero(currency), network = Money.zero(currency), ic = Money.zero(currency);
        boolean level3 = levers.contains(Lever.LEVEL3_DATA), leastCost = levers.contains(Lever.ROUTING);
        for (Txn t : population) {
            TxnTrace tr = simulate(t, levers);
            attempts += tr.attempts();
            int tokenAttempts = tr.tokenUsed() ? tr.attempts() : 0;
            Money tokenCost = Money.ofMinor(Math.multiplyExact(prices.tokenFee().minorUnits(), tokenAttempts), currency);
            token = token.plus(tokenCost);
            if (tr.updaterHit()) updater = updater.plus(prices.updaterHitFee());
            auth = auth.plus(Money.ofMinor(Math.multiplyExact(prices.authFee().minorUnits(), tr.attempts()), currency));
            if (tr.finalOutcome().approved()) {
                approvals++;
                volume = volume.plus(t.amount());
                network = network.plus(networkFee(t.amount(), t.cardType(), leastCost));
                ic = ic.plus(interchange(t.amount(), t.cardType(), level3));
            }
        }
        return new ArmResult(Collections.unmodifiableSet(levers.isEmpty() ? EnumSet.noneOf(Lever.class) : EnumSet.copyOf(levers)),
                population.size(), attempts, approvals, volume, auth, token, updater, network, ic);
    }

    /** Seeded population. The base outcome mix is fixed by the seed; see the lesson for the shares. */
    static List<Txn> population(int n, long seed, Currency c) {
        Random rnd = new Random(seed);
        List<Txn> out = new ArrayList<>(n);
        for (int i = 0; i < n; i++) {
            int typeRoll = rnd.nextInt(100);
            CardType type = typeRoll < 60 ? CardType.CONSUMER_CREDIT : typeRoll < 90 ? CardType.REGULATED_DEBIT : CardType.COMMERCIAL;
            long minor = type == CardType.COMMERCIAL ? 20_000 + rnd.nextInt(480_000) : 1_000 + rnd.nextInt(19_000);
            boolean tokenEligible = rnd.nextInt(100) < 70;
            int u = rnd.nextInt(10_000);
            ResponseCode base;
            if (u < 400) base = ResponseCode.EXPIRED_CARD;               // 4.0% stale credentials on file
            else if (u < 800) base = ResponseCode.DO_NOT_HONOR;          // 4.0%
            else if (u < 1100) base = ResponseCode.INSUFFICIENT_FUNDS;   // 3.0%
            else if (u < 1250) base = ResponseCode.ISSUER_UNAVAILABLE;   // 1.5%
            else if (u < 1400) base = ResponseCode.PROCESSOR_TIMEOUT;    // 1.5%
            else if (u < 1500) base = ResponseCode.LOST_OR_STOLEN;       // 1.0%
            else if (u < 1600) base = ResponseCode.NOT_PERMITTED;        // 1.0%
            else base = ResponseCode.APPROVED;                           // 84.0%
            out.add(new Txn(String.format("t%06d", i), Money.ofMinor(minor, c), type, tokenEligible, base,
                    rnd.nextInt(10_000), rnd.nextInt(10_000), rnd.nextInt(10_000)));
        }
        return out;
    }
}

/** Uplift of one arm over control: exact integer deltas, plus net value at a stated contribution margin. */
record Uplift(String arm, int extraApprovals, long rateDeltaBps, Money extraVolume, Money extraCost, Money netValue) {
    static Uplift of(String arm, ArmResult control, ArmResult treatment, long marginBps) {
        Money extraVolume = treatment.approvedVolume().minus(control.approvedVolume());
        Money extraCost = treatment.totalCost().minus(control.totalCost());
        Money value = extraVolume.times(marginBps, 10_000, Rounding.HALF_EVEN).minus(extraCost);
        return new Uplift(arm, treatment.approvals() - control.approvals(),
                treatment.approvalRateBps() - control.approvalRateBps(), extraVolume, extraCost, value);
    }
}

public final class Main {
    static final Currency USD = Currency.USD;
    static final long MARGIN_BPS = 3_000; // the merchant's contribution margin on recovered sales: 30%

    static String pct(long bps) {
        String sign = bps < 0 ? "-" : "";
        bps = Math.abs(bps);
        return String.format("%s%d.%02d%%", sign, bps / 100, bps % 100);
    }

    public static void main(String[] args) {
        AuthExperiment exp = new AuthExperiment(PriceList.standard(USD), USD);

        System.out.println("== Level 2/3 data on one 1,000.00 USD commercial card purchase ==");
        Money ticket = Money.ofMinor(100_000, USD);
        Money l1 = exp.interchange(ticket, CardType.COMMERCIAL, false);
        Money l3 = exp.interchange(ticket, CardType.COMMERCIAL, true);
        System.out.println("level 1 interchange " + l1 + ", level 3 " + l3 + ", saved " + l1.minus(l3));

        System.out.println("\n== One expired card, three ways ==");
        Txn stale = new Txn("demo", Money.ofMinor(4_999, USD), CardType.CONSUMER_CREDIT, true, ResponseCode.EXPIRED_CARD, 9_000, 9_000, 1_000);
        for (Set<Lever> levers : List.of(EnumSet.noneOf(Lever.class), EnumSet.of(Lever.SOFT_DECLINE_RETRY),
                EnumSet.of(Lever.ACCOUNT_UPDATER), EnumSet.of(Lever.NETWORK_TOKEN))) {
            TxnTrace tr = exp.simulate(stale, levers);
            System.out.printf("  %-22s -> %-14s attempts %d fees %s%n", levers.isEmpty() ? "control" : levers, tr.finalOutcome(), tr.attempts(), tr.fees());
        }

        System.out.println("\n== 100,000 transactions, seed 2026, same draws in every arm ==");
        List<Txn> pop = AuthExperiment.population(100_000, 2026, USD);
        ArmResult control = exp.run(pop, EnumSet.noneOf(Lever.class));
        System.out.printf("control: %d approvals of %d (%s), %d attempts, volume %s, cost %s%n", control.approvals(), control.transactions(),
                pct(control.approvalRateBps()), control.attempts(), control.approvedVolume(), control.totalCost());
        System.out.printf("%n  %-20s %8s %8s %18s %14s %16s%n", "arm", "+approve", "+rate", "+volume", "+cost", "net @30% margin");
        List<Uplift> rows = new ArrayList<>();
        for (Lever l : Lever.values()) rows.add(Uplift.of(l.name(), control, exp.run(pop, EnumSet.of(l)), MARGIN_BPS));
        rows.add(Uplift.of("ALL", control, exp.run(pop, EnumSet.allOf(Lever.class)), MARGIN_BPS));
        for (Uplift u : rows) {
            System.out.printf("  %-20s %8d %8s %18s %14s %16s%n", u.arm(), u.extraApprovals(), pct(u.rateDeltaBps()), u.extraVolume(), u.extraCost(), u.netValue());
        }
        int sumOfSingles = rows.subList(0, Lever.values().length).stream().mapToInt(Uplift::extraApprovals).sum();
        System.out.println("\nsum of single-lever uplifts " + sumOfSingles + " vs all levers together " + rows.get(rows.size() - 1).extraApprovals()
                + "\n(token and updater recover the same expired cards; routing and retry the same timeouts)");

        ArmResult all = exp.run(pop, EnumSet.allOf(Lever.class));
        System.out.printf("cost per approval: control %s, all levers %s%n",
                Money.ofMinor(control.totalCost().minorUnits() / control.approvals(), USD),
                Money.ofMinor(all.totalCost().minorUnits() / all.approvals(), USD));
        System.out.println("hard declines re-sent under ALL: " + pop.stream()
                .filter(t -> t.baseOutcome().declineClass == DeclineClass.NEVER_RETRY)
                .filter(t -> exp.simulate(t, EnumSet.allOf(Lever.class)).attempts() > 1).count());
    }
}
