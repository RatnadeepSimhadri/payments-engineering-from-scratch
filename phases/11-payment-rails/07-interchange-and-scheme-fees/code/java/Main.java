// Lesson: phases/11-payment-rails/07-interchange-and-scheme-fees/docs/en.md
// Interchange, Scheme Fees, and Merchant Pricing: a fee engine that splits one card payment to the cent.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Regulation II (12 CFR 235.3-235.4, US debit cap); EU Regulation 2015/751 (IFR caps); scheme fee schedules.
// Java 21, standard library only. Rates in the demo table are illustrative, shaped like published schedules.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** The interchange category of the card, as the issuer's BIN declares it (Lesson 04 routes on the same BIN). */
enum CardType {
    CONSUMER_DEBIT_REGULATED, // US: issuer >= $10bn assets, Regulation II cap. EEA: all consumer debit (IFR).
    CONSUMER_DEBIT_EXEMPT,    // US debit from an issuer under $10bn: exempt from the cap
    CONSUMER_CREDIT,
    PREMIUM_CREDIT,           // rewards tiers: the issuer funds points from interchange
    COMMERCIAL_CREDIT;        // corporate and purchasing cards: capped neither in the US nor the EEA

    boolean isConsumer() { return this != COMMERCIAL_CREDIT; }
    boolean isDebit() { return this == CONSUMER_DEBIT_REGULATED || this == CONSUMER_DEBIT_EXEMPT; }
}

/** Where the issuer is relative to the merchant's acquirer. */
enum Region {
    US_DOMESTIC,     // US card, US acquirer
    EEA_DOMESTIC,    // EEA card, EEA acquirer: IFR caps apply to consumer cards
    INTER_REGIONAL   // issuer and acquirer in different regions: cross-border scheme fee applies
}

enum Channel { CARD_PRESENT, CARD_NOT_PRESENT }

/**
 * A price with a proportional part and a fixed part, such as 1.80% + 0.10.
 * The proportional part is in parts per million (1.80% = 18_000 ppm, 0.05% = 500 ppm) so every rate a
 * scheme publishes is an exact integer. The fixed part is in minor units of the transaction's currency.
 */
record Rate(long ppm, long fixedMinor) {
    static final long PPM = 1_000_000;

    Rate {
        if (ppm < 0 || fixedMinor < 0) throw new IllegalArgumentException("rates are non-negative");
    }

    /** Rate.of("1.80", 10) is 1.80% + 10 minor units. Parses the percent exactly; never via a double. */
    static Rate of(String percent, long fixedMinor) {
        int dot = percent.indexOf('.');
        String whole = dot < 0 ? percent : percent.substring(0, dot);
        StringBuilder frac = new StringBuilder(dot < 0 ? "" : percent.substring(dot + 1));
        if (frac.length() > 4) throw new IllegalArgumentException("at most 4 decimals of a percent: " + percent);
        while (frac.length() < 4) frac.append('0');
        return new Rate(Long.parseLong(whole) * 10_000 + Long.parseLong(frac.toString()), fixedMinor);
    }

    /** amount x ppm / 1e6 rounded HALF_EVEN, plus the fixed part. Exactly one rounding per fee component. */
    Money applyTo(Money amount) {
        Money proportional = amount.times(ppm, PPM, Rounding.HALF_EVEN);
        return proportional.plus(Money.ofMinor(fixedMinor, amount.currency()));
    }

    @Override
    public String toString() {
        String frac = String.format("%04d", ppm % 10_000);
        while (frac.length() > 2 && frac.endsWith("0")) frac = frac.substring(0, frac.length() - 1);
        return (ppm / 10_000) + "." + frac + "% + " + fixedMinor;
    }
}

record RateKey(CardType cardType, Region region, Channel channel) {}

final class NoRateException extends RuntimeException {
    NoRateException(RateKey key) { super("no interchange rate for " + key); }
}

/**
 * The interchange table: what the acquirer pays the issuer, published by the scheme, looked up per transaction.
 * Regulatory caps are checked when a rate is loaded, so a bad table fails at startup rather than at month end.
 */
final class InterchangeTable {
    /** EU Regulation 2015/751, Articles 3 and 4: consumer debit 0.2%, consumer credit 0.3%, no fixed part. */
    static final long IFR_DEBIT_CAP_PPM = 2_000;
    static final long IFR_CREDIT_CAP_PPM = 3_000;
    /** Regulation II, 12 CFR 235.3 and 235.4: 0.05% + 21 cents, plus 1 cent fraud-prevention adjustment. */
    static final Rate REG_II_CAP = new Rate(500, 22);

    private final Map<RateKey, Rate> rates = new HashMap<>();

    InterchangeTable put(CardType type, Region region, Channel channel, Rate rate) {
        RateKey key = new RateKey(type, region, channel);
        checkCaps(key, rate);
        rates.put(key, rate);
        return this;
    }

    /** Put the same rate for both channels. */
    InterchangeTable putBoth(CardType type, Region region, Rate rate) {
        for (Channel ch : Channel.values()) put(type, region, ch, rate);
        return this;
    }

    Rate lookup(CardType type, Region region, Channel channel) {
        RateKey key = new RateKey(type, region, channel);
        Rate rate = rates.get(key);
        if (rate == null) throw new NoRateException(key);
        return rate;
    }

    int size() { return rates.size(); }

    static void checkCaps(RateKey key, Rate rate) {
        if (key.region() == Region.EEA_DOMESTIC && key.cardType().isConsumer()) {
            long cap = key.cardType().isDebit() ? IFR_DEBIT_CAP_PPM : IFR_CREDIT_CAP_PPM;
            if (rate.ppm() > cap || rate.fixedMinor() > 0) {
                throw new IllegalArgumentException("IFR cap exceeded for " + key + ": " + rate);
            }
        }
        if (key.region() == Region.US_DOMESTIC && key.cardType() == CardType.CONSUMER_DEBIT_REGULATED) {
            if (rate.ppm() > REG_II_CAP.ppm() || rate.fixedMinor() > REG_II_CAP.fixedMinor()) {
                throw new IllegalArgumentException("Regulation II cap exceeded for " + key + ": " + rate);
            }
        }
    }

    /** Illustrative table, shaped like published US and EEA schedules. There is no EEA "exempt debit" row. */
    static InterchangeTable demo() {
        InterchangeTable t = new InterchangeTable();
        Region us = Region.US_DOMESTIC, eea = Region.EEA_DOMESTIC, xb = Region.INTER_REGIONAL;
        Channel cp = Channel.CARD_PRESENT, cnp = Channel.CARD_NOT_PRESENT;

        t.putBoth(CardType.CONSUMER_DEBIT_REGULATED, us, Rate.of("0.05", 22));
        t.put(CardType.CONSUMER_DEBIT_EXEMPT, us, cp, Rate.of("0.80", 15));
        t.put(CardType.CONSUMER_DEBIT_EXEMPT, us, cnp, Rate.of("1.65", 15));
        t.put(CardType.CONSUMER_CREDIT, us, cp, Rate.of("1.51", 10));
        t.put(CardType.CONSUMER_CREDIT, us, cnp, Rate.of("1.80", 10));
        t.put(CardType.PREMIUM_CREDIT, us, cp, Rate.of("2.10", 10));
        t.put(CardType.PREMIUM_CREDIT, us, cnp, Rate.of("2.40", 10));
        t.put(CardType.COMMERCIAL_CREDIT, us, cp, Rate.of("2.50", 10));
        t.put(CardType.COMMERCIAL_CREDIT, us, cnp, Rate.of("2.70", 10));

        t.putBoth(CardType.CONSUMER_DEBIT_REGULATED, eea, Rate.of("0.20", 0));
        t.putBoth(CardType.CONSUMER_CREDIT, eea, Rate.of("0.30", 0));
        t.putBoth(CardType.PREMIUM_CREDIT, eea, Rate.of("0.30", 0));
        t.putBoth(CardType.COMMERCIAL_CREDIT, eea, Rate.of("1.50", 5));

        for (CardType debit : new CardType[] {CardType.CONSUMER_DEBIT_REGULATED, CardType.CONSUMER_DEBIT_EXEMPT}) {
            t.put(debit, xb, cp, Rate.of("0.20", 0));
            t.put(debit, xb, cnp, Rate.of("1.15", 0));
        }
        for (CardType credit : new CardType[] {CardType.CONSUMER_CREDIT, CardType.PREMIUM_CREDIT}) {
            t.put(credit, xb, cp, Rate.of("0.30", 0));
            t.put(credit, xb, cnp, Rate.of("1.50", 0));
        }
        t.putBoth(CardType.COMMERCIAL_CREDIT, xb, Rate.of("2.00", 0));
        return t;
    }
}

/**
 * What the network charges the acquirer per transaction: an assessment on volume, a fixed per-transaction
 * processing fee, and a cross-border assessment when issuer and acquirer are in different regions.
 */
record SchemeFees(Rate assessment, long crossBorderPpm) {
    Money feeFor(Money amount, Region region) {
        Money fee = assessment.applyTo(amount);
        if (region == Region.INTER_REGIONAL) {
            fee = fee.plus(amount.times(crossBorderPpm, Rate.PPM, Rounding.HALF_EVEN));
        }
        return fee;
    }

    static SchemeFees demo() { return new SchemeFees(Rate.of("0.14", 2), 6_000); }
}

/** How the acquirer prices the merchant. */
sealed interface Pricing permits Pricing.IcPlusPlus, Pricing.Blended {
    /** Interchange ++: pass interchange and scheme fees through at cost, add a disclosed markup. */
    record IcPlusPlus(Rate markup) implements Pricing {}
    /** Blended: one rate for every card; the acquirer keeps whatever is left after interchange and scheme. */
    record Blended(Rate merchantRate) implements Pricing {}
}

record Transaction(String id, Money amount, CardType cardType, Region region, Channel channel) {
    Transaction {
        Objects.requireNonNull(amount, "amount");
        if (!amount.isPositive()) throw new IllegalArgumentException("a purchase amount is positive: " + amount);
    }
}

/**
 * Who gets what from one transaction. The invariant, checked on construction:
 * interchange + schemeFees + acquirerMargin + merchantNet == gross, to the minor unit.
 */
record FeeBreakdown(Money gross, Rate interchangeRate, Money interchange, Money schemeFees,
                    Money acquirerMargin, Money merchantNet) {
    FeeBreakdown {
        Money total = Money.sum(gross.currency(), interchange, schemeFees, acquirerMargin, merchantNet);
        if (!total.equals(gross)) {
            throw new IllegalStateException("fee split does not sum to gross: " + total + " != " + gross);
        }
    }

    /** The merchant discount: everything the merchant does not receive. */
    Money merchantDiscount() { return gross.minus(merchantNet); }

    /** Basis points of gross, rounded HALF_EVEN, for reporting only. */
    long effectiveRateBps() { return Money.divideRounded(merchantDiscount().minorUnits() * 10_000, gross.minorUnits(), Rounding.HALF_EVEN); }
}

final class FeeEngine {
    private final InterchangeTable table;
    private final SchemeFees scheme;

    FeeEngine(InterchangeTable table, SchemeFees scheme) {
        this.table = table;
        this.scheme = scheme;
    }

    FeeBreakdown quote(Transaction tx, Pricing pricing) {
        Money gross = tx.amount();
        Rate icRate = table.lookup(tx.cardType(), tx.region(), tx.channel());
        Money interchange = icRate.applyTo(gross);
        Money schemeFees = scheme.feeFor(gross, tx.region());
        Money merchantDiscount = switch (pricing) {
            case Pricing.IcPlusPlus p -> interchange.plus(schemeFees).plus(p.markup().applyTo(gross));
            case Pricing.Blended b -> b.merchantRate().applyTo(gross);
        };
        Money acquirerMargin = merchantDiscount.minus(interchange).minus(schemeFees);
        Money merchantNet = gross.minus(merchantDiscount);
        return new FeeBreakdown(gross, icRate, interchange, schemeFees, acquirerMargin, merchantNet);
    }

    /**
     * Attribute a fee billed in aggregate (a monthly scheme fee, a statement fee) back to the transactions that
     * caused it, in proportion to their amounts. Largest-remainder allocation: the parts sum exactly to the fee.
     */
    static Money[] attribute(Money aggregateFee, List<Transaction> txs) {
        long[] weights = new long[txs.size()];
        for (int i = 0; i < weights.length; i++) {
            Money amount = txs.get(i).amount();
            if (!amount.currency().equals(aggregateFee.currency())) throw new CurrencyMismatchException(amount.currency(), aggregateFee.currency());
            weights[i] = amount.minorUnits();
        }
        return aggregateFee.allocate(weights);
    }

    /** Sum one month of quotes into the totals an IC++ merchant statement shows. */
    static FeeBreakdown total(List<FeeBreakdown> quotes) {
        Currency c = quotes.get(0).gross().currency();
        Money g = Money.ofMinor(0, c), ic = g, sf = g, am = g, net = g;
        for (FeeBreakdown q : quotes) {
            g = g.plus(q.gross()); ic = ic.plus(q.interchange()); sf = sf.plus(q.schemeFees());
            am = am.plus(q.acquirerMargin()); net = net.plus(q.merchantNet());
        }
        return new FeeBreakdown(g, new Rate(0, 0), ic, sf, am, net);
    }
}

public final class Main {
    static void print(String label, FeeBreakdown f) {
        System.out.printf("%-34s gross %-11s IC %-9s scheme %-8s acquirer %-9s merchant %-11s (%d bps, IC rate %s)%n",
                label, f.gross(), f.interchange(), f.schemeFees(), f.acquirerMargin(), f.merchantNet(),
                f.effectiveRateBps(), f.interchangeRate());
    }

    public static void main(String[] args) {
        FeeEngine engine = new FeeEngine(InterchangeTable.demo(), SchemeFees.demo());
        Pricing icpp = new Pricing.IcPlusPlus(Rate.of("0.20", 5));
        Pricing blended = new Pricing.Blended(Rate.of("2.90", 30));
        Money hundred = Money.parse("100.00", Currency.USD);

        System.out.println("== Who gets paid on 100.00 USD (US consumer credit, card-not-present, IC++) ==");
        FeeBreakdown f = engine.quote(new Transaction("t1", hundred, CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp);
        System.out.println("issuer   (interchange 1.80% + 0.10)   " + f.interchange());
        System.out.println("network  (assessment 0.14% + 0.02)    " + f.schemeFees());
        System.out.println("acquirer (markup 0.20% + 0.05)        " + f.acquirerMargin());
        System.out.println("merchant (net settlement)             " + f.merchantNet());
        System.out.println("sum                                   " + Money.sum(Currency.USD, f.interchange(), f.schemeFees(), f.acquirerMargin(), f.merchantNet()));

        System.out.println("\n== The same 100.00 USD across card types, CNP ==");
        for (CardType type : CardType.values()) {
            print(type + " IC++", engine.quote(new Transaction("t", hundred, type, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp));
        }

        System.out.println("\n== Blended 2.90% + 0.30: the merchant pays the same, the acquirer's margin moves ==");
        for (CardType type : CardType.values()) {
            print(type + " blended", engine.quote(new Transaction("t", hundred, type, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), blended));
        }

        System.out.println("\n== Small tickets: 2.00 USD regulated debit ==");
        Money two = Money.parse("2.00", Currency.USD);
        print("regulated debit IC++", engine.quote(new Transaction("t", two, CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT), icpp));
        print("regulated debit blended", engine.quote(new Transaction("t", two, CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT), blended));

        System.out.println("\n== EEA: IFR caps and cross-border ==");
        Money eur = Money.parse("100.00", Currency.EUR);
        print("EEA premium credit CP", engine.quote(new Transaction("t", eur, CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT), icpp));
        print("inter-regional credit CNP", engine.quote(new Transaction("t", eur, CardType.CONSUMER_CREDIT, Region.INTER_REGIONAL, Channel.CARD_NOT_PRESENT), icpp));
        try {
            new InterchangeTable().put(CardType.CONSUMER_CREDIT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT, Rate.of("0.35", 0));
        } catch (IllegalArgumentException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        try {
            engine.quote(new Transaction("t", eur, CardType.CONSUMER_DEBIT_EXEMPT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT), icpp);
        } catch (NoRateException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Rounding: 37.77 USD consumer credit CNP ==");
        FeeBreakdown odd = engine.quote(new Transaction("t", Money.parse("37.77", Currency.USD), CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp);
        System.out.println("interchange = 37.77 x 1.80% = 0.67986 -> 0.68, + 0.10 = " + odd.interchange());
        print("37.77", odd);

        System.out.println("\n== Attributing a 25.00 USD monthly scheme fee to three transactions ==");
        List<Transaction> month = new ArrayList<>(List.of(
                new Transaction("a", Money.parse("100.00", Currency.USD), CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_PRESENT),
                new Transaction("b", Money.parse("37.77", Currency.USD), CardType.PREMIUM_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT),
                new Transaction("c", Money.parse("2.00", Currency.USD), CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT)));
        Money[] parts = FeeEngine.attribute(Money.parse("25.00", Currency.USD), month);
        for (int i = 0; i < parts.length; i++) System.out.println(month.get(i).id() + " " + month.get(i).amount() + " -> " + parts[i]);
        System.out.println("sum = " + Money.sum(Currency.USD, parts));
        List<FeeBreakdown> quotes = new ArrayList<>();
        for (Transaction tx : month) quotes.add(engine.quote(tx, icpp));
        print("month total IC++", FeeEngine.total(quotes));
    }
}
