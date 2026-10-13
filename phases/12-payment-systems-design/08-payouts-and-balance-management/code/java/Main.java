// Lesson: phases/12-payment-systems-design/08-payouts-and-balance-management/docs/en.md
// Payouts, Balances, and Negative Balance Handling
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Nacha Operating Rules (batch/entry/control records, return reason codes R01-R03);
//          Visa and Mastercard settlement (clearing vs settlement). Java 21, stdlib only.

import java.time.DayOfWeek;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    Money min(Money other) { return compareTo(other) <= 0 ? this : other; }
    boolean isPositive() { return minorUnits > 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isZero() { return minorUnits == 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
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
// Calendar clock: settlement, reserves and payout schedules are all counted in days.
// ---------------------------------------------------------------------------------------------

interface Clock { LocalDate today(); }

final class FakeClock implements Clock {
    private LocalDate today;
    FakeClock(LocalDate start) { today = start; }
    @Override public LocalDate today() { return today; }
    void advanceDays(long days) { today = today.plusDays(days); }
}

// ---------------------------------------------------------------------------------------------
// Minimal double-entry ledger (Phase 10, Lesson 03). Each merchant has four sub-accounts.
// ---------------------------------------------------------------------------------------------

enum Side { DEBIT, CREDIT }

enum AccountType {
    ASSET(Side.DEBIT), LIABILITY(Side.CREDIT), REVENUE(Side.CREDIT);
    final Side normal;
    AccountType(Side normal) { this.normal = normal; }
}

record Posting(String account, Side side, Money amount) {
    Posting { if (!amount.isPositive()) throw new IllegalArgumentException("posting amount must be positive: " + amount); }
    static Posting debit(String account, Money amount) { return new Posting(account, Side.DEBIT, amount); }
    static Posting credit(String account, Money amount) { return new Posting(account, Side.CREDIT, amount); }
}

record JournalEntry(String id, LocalDate date, String description, List<Posting> postings) {
    JournalEntry {
        postings = List.copyOf(postings);
        long net = 0;
        for (Posting p : postings) net += p.side() == Side.DEBIT ? p.amount().minorUnits() : -p.amount().minorUnits();
        if (postings.size() < 2 || net != 0) throw new IllegalArgumentException("entry " + id + " is unbalanced or too short");
    }
}

final class Ledger {
    private final Map<String, AccountType> accounts = new LinkedHashMap<>();
    private final Map<String, Long> balances = new HashMap<>();
    private final List<JournalEntry> journal = new ArrayList<>();
    private final Currency currency;
    private int seq = 0;

    Ledger(Currency currency) { this.currency = currency; }

    void open(String account, AccountType type) {
        if (accounts.putIfAbsent(account, type) != null) throw new IllegalArgumentException("account exists: " + account);
        balances.put(account, 0L);
    }

    /** Post one balanced entry; every posting amount is positive (a zero line is refused by Posting itself). */
    void post(LocalDate date, String description, List<Posting> postings) {
        for (Posting p : postings) if (!accounts.containsKey(p.account())) throw new IllegalArgumentException("unknown account " + p.account());
        JournalEntry e = new JournalEntry(String.format("je_%04d", ++seq), date, description, postings);
        journal.add(e);
        for (Posting p : e.postings()) {
            long signed = p.side() == accounts.get(p.account()).normal ? p.amount().minorUnits() : -p.amount().minorUnits();
            balances.merge(p.account(), signed, Math::addExact);
        }
    }

    Money balance(String account) {
        Long b = balances.get(account);
        if (b == null) throw new IllegalArgumentException("unknown account " + account);
        return Money.ofMinor(b, currency);
    }

    boolean balances() {
        long debitSide = 0, creditSide = 0;
        for (Map.Entry<String, AccountType> a : accounts.entrySet()) {
            if (a.getValue().normal == Side.DEBIT) debitSide += balances.get(a.getKey()); else creditSide += balances.get(a.getKey());
        }
        return debitSide == creditSide;
    }

    List<JournalEntry> journal() { return Collections.unmodifiableList(journal); }
}

// ---------------------------------------------------------------------------------------------
// Merchants, captures, settlement reports, reserves, payouts.
// ---------------------------------------------------------------------------------------------

enum PayoutFrequency { DAILY, WEEKLY }

/** When a merchant is paid: every business day, or one weekday; never less than the minimum. */
record PayoutSchedule(PayoutFrequency frequency, DayOfWeek weeklyDay, Money minimumPayout) {
    boolean dueOn(LocalDate d) {
        boolean businessDay = d.getDayOfWeek() != DayOfWeek.SATURDAY && d.getDayOfWeek() != DayOfWeek.SUNDAY;
        return businessDay && (frequency == PayoutFrequency.DAILY || d.getDayOfWeek() == weeklyDay);
    }
}

/** Rolling reserve: a share of each settlement is held back for a number of days before it becomes available. */
record ReservePolicy(long basisPoints, int holdDays) {
    ReservePolicy {
        if (basisPoints < 0 || basisPoints > 10_000) throw new IllegalArgumentException("reserve bps must be 0..10000");
        if (holdDays < 0) throw new IllegalArgumentException("holdDays must be >= 0");
    }
}

record Merchant(String id, String bankAccountToken, PayoutSchedule schedule, ReservePolicy reserve) {}

/** A capture's net is owed to the merchant, but only becomes payable once the scheme has actually paid us for it. */
record CaptureRecord(String id, String merchantId, Money gross, Money fee, LocalDate capturedOn) {
    Money net() { return gross.minus(fee); }
}

/** The scheme's statement that it paid us for these captures. The authority on "settled". */
record SettlementReport(String id, LocalDate valueDate, List<String> captureIds, Money totalGross) {
    SettlementReport { captureIds = List.copyOf(captureIds); }
}

record ReserveTranche(String merchantId, Money amount, LocalDate releaseOn) {}

enum PayoutDirection { CREDIT, DEBIT }

enum PayoutState { IN_TRANSIT, PAID, RETURNED }

final class Payout {
    final String id;
    final String merchantId;
    final Money amount;
    final PayoutDirection direction;
    final LocalDate initiatedOn;
    PayoutState state = PayoutState.IN_TRANSIT;
    String returnCode;

    Payout(String id, String merchantId, Money amount, PayoutDirection direction, LocalDate initiatedOn) {
        this.id = id;
        this.merchantId = merchantId;
        this.amount = amount;
        this.direction = direction;
        this.initiatedOn = initiatedOn;
    }

    PayoutState state() { return state; }
    Money amount() { return amount; }

    @Override public String toString() { return id + " " + direction + " " + merchantId + " " + amount + " " + state + (returnCode == null ? "" : " " + returnCode); }
}

/** The hook to the bank: a payout file per run. The real one writes NACHA or ISO 20022 pain.001; this one keeps lines in memory. */
interface PayoutFileSink { void write(String fileName, List<String> lines); }

final class InMemoryPayoutFileSink implements PayoutFileSink {
    final Map<String, List<String>> files = new LinkedHashMap<>();
    @Override public void write(String fileName, List<String> lines) { files.put(fileName, List.copyOf(lines)); }
}

record Balance(Money pending, Money available, Money reserve, Money inTransit) {
    /** Everything we owe the merchant, in any stage. */
    Money total() { return pending.plus(available).plus(reserve).plus(inTransit); }
}

final class BalanceService {
    static final String SCHEME = "scheme_receivable";     // asset: captured, not yet paid to us by the scheme
    static final String BANK = "bank_cash";                // asset: our operating bank account
    static final String COLLECTIONS = "collections_receivable"; // asset: debits we have asked merchants' banks for
    static final String FEES = "fee_revenue";

    private final Ledger ledger;
    private final Clock clock;
    private final PayoutFileSink sink;
    private final int collectAfterNegativeDays;
    private final Currency currency;
    private final Map<String, Merchant> merchants = new LinkedHashMap<>();
    private final Map<String, CaptureRecord> captures = new LinkedHashMap<>();
    private final Map<String, String> settledBy = new HashMap<>();
    private final List<ReserveTranche> tranches = new ArrayList<>();
    private final Map<String, Payout> payouts = new LinkedHashMap<>();
    private final Map<String, LocalDate> negativeSince = new HashMap<>();
    private final Map<String, String> paused = new HashMap<>();
    private int payoutSeq = 0;
    private int fileSeq = 0;

    BalanceService(Ledger ledger, Clock clock, PayoutFileSink sink, int collectAfterNegativeDays, Currency currency) {
        this.ledger = ledger;
        this.clock = clock;
        this.sink = sink;
        this.collectAfterNegativeDays = collectAfterNegativeDays;
        this.currency = currency;
        ledger.open(SCHEME, AccountType.ASSET);
        ledger.open(BANK, AccountType.ASSET);
        ledger.open(COLLECTIONS, AccountType.ASSET);
        ledger.open(FEES, AccountType.REVENUE);
    }

    static String pendingAcct(String m) { return m + ":pending"; }
    static String availableAcct(String m) { return m + ":available"; }
    static String reserveAcct(String m) { return m + ":reserve"; }
    static String inTransitAcct(String m) { return m + ":in_transit"; }

    Ledger ledger() { return ledger; }

    void onboard(Merchant m) {
        merchants.put(m.id(), m);
        ledger.open(pendingAcct(m.id()), AccountType.LIABILITY);
        ledger.open(availableAcct(m.id()), AccountType.LIABILITY);
        ledger.open(reserveAcct(m.id()), AccountType.LIABILITY);
        ledger.open(inTransitAcct(m.id()), AccountType.LIABILITY);
    }

    Merchant merchant(String id) {
        Merchant m = merchants.get(id);
        if (m == null) throw new IllegalArgumentException("unknown merchant " + id);
        return m;
    }

    Balance balance(String merchantId) {
        merchant(merchantId);
        return new Balance(ledger.balance(pendingAcct(merchantId)), ledger.balance(availableAcct(merchantId)),
                ledger.balance(reserveAcct(merchantId)), ledger.balance(inTransitAcct(merchantId)));
    }

    /** A capture: the scheme owes us the gross; the merchant is owed the net, but it is PENDING until settlement. */
    CaptureRecord capture(String captureId, String merchantId, Money gross, long feeBps, long fixedFeeMinor) {
        merchant(merchantId);
        if (captures.containsKey(captureId)) throw new IllegalStateException("capture exists: " + captureId);
        long variable = Math.floorDiv(Math.multiplyExact(gross.minorUnits(), feeBps) + 5_000, 10_000);
        CaptureRecord c = new CaptureRecord(captureId, merchantId, gross, Money.ofMinor(variable + fixedFeeMinor, currency), clock.today());
        List<Posting> postings = new ArrayList<>(List.of(Posting.debit(SCHEME, gross), Posting.credit(pendingAcct(merchantId), c.net())));
        if (c.fee().isPositive()) postings.add(Posting.credit(FEES, c.fee()));
        ledger.post(clock.today(), "capture " + captureId, postings);
        captures.put(captureId, c);
        return c;
    }

    boolean isSettled(String captureId) { return settledBy.containsKey(captureId); }

    /**
     * Apply a settlement report. It must reconcile first: every capture known and unsettled, and the report's
     * total equal to the sum of their gross amounts. Only then does cash arrive and pending become available,
     * minus the merchant's rolling reserve, which is held as a tranche with its own release date.
     */
    void settle(SettlementReport report) {
        Money sum = Money.zero(currency);
        for (String id : report.captureIds()) {
            CaptureRecord c = captures.get(id);
            if (c == null) throw new IllegalStateException("report " + report.id() + " names unknown capture " + id);
            if (isSettled(id)) throw new IllegalStateException("report " + report.id() + ": " + id + " already settled by " + settledBy.get(id));
            sum = sum.plus(c.gross());
        }
        if (!sum.equals(report.totalGross())) {
            throw new IllegalStateException("report " + report.id() + " does not reconcile: total " + report.totalGross() + " but captures sum to " + sum);
        }
        List<Posting> postings = new ArrayList<>(List.of(Posting.debit(BANK, sum), Posting.credit(SCHEME, sum)));
        Map<String, Money> netByMerchant = new LinkedHashMap<>();
        for (String id : report.captureIds()) {
            CaptureRecord c = captures.get(id);
            netByMerchant.merge(c.merchantId(), c.net(), Money::plus);
            settledBy.put(id, report.id());
        }
        for (Map.Entry<String, Money> e : netByMerchant.entrySet()) {
            Merchant m = merchant(e.getKey());
            Money net = e.getValue();
            Money reserve = Money.ofMinor(Math.floorDiv(Math.multiplyExact(net.minorUnits(), m.reserve().basisPoints()), 10_000), currency);
            postings.add(Posting.debit(pendingAcct(m.id()), net));
            postings.add(Posting.credit(availableAcct(m.id()), net.minus(reserve)));
            if (reserve.isPositive()) {
                postings.add(Posting.credit(reserveAcct(m.id()), reserve));
                tranches.add(new ReserveTranche(m.id(), reserve, report.valueDate().plusDays(m.reserve().holdDays())));
            }
        }
        ledger.post(clock.today(), "settlement " + report.id(), postings);
    }

    /** A refund, chargeback, or fee: it comes out of available, which may go negative. The scheme nets it from what it owes us. */
    void debitMerchant(String merchantId, Money amount, String reason) {
        merchant(merchantId);
        if (!amount.isPositive()) throw new IllegalArgumentException("debit must be positive");
        ledger.post(clock.today(), reason + " " + merchantId, List.of(
                Posting.debit(availableAcct(merchantId), amount), Posting.credit(SCHEME, amount)));
        trackNegative(merchantId);
    }

    /** Release every tranche due today or earlier into available. */
    Money releaseReserves() {
        Money released = Money.zero(currency);
        List<ReserveTranche> due = tranches.stream().filter(t -> !t.releaseOn().isAfter(clock.today())).toList();
        for (ReserveTranche t : due) {
            ledger.post(clock.today(), "reserve release " + t.merchantId() + " due " + t.releaseOn(), List.of(
                    Posting.debit(reserveAcct(t.merchantId()), t.amount()), Posting.credit(availableAcct(t.merchantId()), t.amount())));
            released = released.plus(t.amount());
            trackNegative(t.merchantId());
        }
        tranches.removeAll(due);
        return released;
    }

    /**
     * Negative balance, step 1: use the merchant's own reserve, earliest-releasing tranches first, before
     * asking anyone for money. That is what the reserve is for.
     */
    Money coverFromReserve(String merchantId) {
        Money deficit = ledger.balance(availableAcct(merchantId)).negate();
        Money used = Money.zero(currency);
        if (!deficit.isPositive()) return used;
        List<ReserveTranche> mine = new ArrayList<>(tranches.stream().filter(t -> t.merchantId().equals(merchantId))
                .sorted((a, b) -> a.releaseOn().compareTo(b.releaseOn())).toList());
        for (ReserveTranche t : mine) {
            if (!deficit.isPositive()) break;
            Money take = t.amount().min(deficit);
            tranches.remove(t);
            if (take.compareTo(t.amount()) < 0) tranches.add(new ReserveTranche(merchantId, t.amount().minus(take), t.releaseOn()));
            used = used.plus(take);
            deficit = deficit.minus(take);
        }
        if (used.isPositive()) {
            ledger.post(clock.today(), "reserve applied to negative balance " + merchantId, List.of(
                    Posting.debit(reserveAcct(merchantId), used), Posting.credit(availableAcct(merchantId), used)));
        }
        trackNegative(merchantId);
        return used;
    }

    private void trackNegative(String merchantId) {
        if (ledger.balance(availableAcct(merchantId)).isNegative()) negativeSince.putIfAbsent(merchantId, clock.today());
        else negativeSince.remove(merchantId);
    }

    LocalDate negativeSince(String merchantId) { return negativeSince.get(merchantId); }

    /** Sum of the merchant's unreleased tranches. Must always equal the reserve account's balance. */
    Money scheduledReserve(String merchantId) {
        Money total = Money.zero(currency);
        for (ReserveTranche t : tranches) if (t.merchantId().equals(merchantId)) total = total.plus(t.amount());
        return total;
    }

    boolean isPaused(String merchantId) { return paused.containsKey(merchantId); }

    void resume(String merchantId, String why) { paused.remove(merchantId); }

    Payout payout(String id) {
        Payout p = payouts.get(id);
        if (p == null) throw new IllegalArgumentException("unknown payout " + id);
        return p;
    }

    /**
     * The daily payout run. For each merchant: cover a negative balance from reserve; if it has been negative
     * for collectAfterNegativeDays, ask its bank for the deficit (a DEBIT line); otherwise, if its schedule is
     * due, it is not paused, and available meets the minimum, pay out all of available (a CREDIT line). One
     * file per run, with a trailer carrying the counts and totals the bank checks.
     */
    List<Payout> runPayouts() {
        LocalDate today = clock.today();
        List<Payout> batch = new ArrayList<>();
        for (Merchant m : merchants.values()) {
            coverFromReserve(m.id());
            Money available = ledger.balance(availableAcct(m.id()));
            LocalDate since = negativeSince.get(m.id());
            if (available.isNegative() && since != null && !today.isBefore(since.plusDays(collectAfterNegativeDays))) {
                Payout debit = new Payout(String.format("co_%04d", ++payoutSeq), m.id(), available.negate(), PayoutDirection.DEBIT, today);
                ledger.post(today, "collection " + debit.id, List.of(
                        Posting.debit(COLLECTIONS, debit.amount), Posting.credit(availableAcct(m.id()), debit.amount)));
                trackNegative(m.id());
                payouts.put(debit.id, debit);
                batch.add(debit);
                continue;
            }
            if (!m.schedule().dueOn(today) || isPaused(m.id())) continue;
            if (available.compareTo(m.schedule().minimumPayout()) < 0 || !available.isPositive()) continue;
            Payout credit = new Payout(String.format("po_%04d", ++payoutSeq), m.id(), available, PayoutDirection.CREDIT, today);
            ledger.post(today, "payout " + credit.id, List.of(
                    Posting.debit(availableAcct(m.id()), available), Posting.credit(inTransitAcct(m.id()), available)));
            payouts.put(credit.id, credit);
            batch.add(credit);
        }
        if (!batch.isEmpty()) writeFile(today, batch);
        return batch;
    }

    private void writeFile(LocalDate today, List<Payout> batch) {
        String name = String.format("payouts_%s_%03d.csv", today, ++fileSeq);
        List<String> lines = new ArrayList<>();
        lines.add("H," + today + "," + name + "," + currency.code());
        long credits = 0, debits = 0;
        for (Payout p : batch) {
            lines.add("D," + p.id + "," + p.merchantId + "," + merchant(p.merchantId).bankAccountToken() + "," + p.direction + "," + p.amount.minorUnits());
            if (p.direction == PayoutDirection.CREDIT) credits += p.amount.minorUnits(); else debits += p.amount.minorUnits();
        }
        lines.add("T," + batch.size() + "," + credits + "," + debits);
        sink.write(name, lines);
    }

    /** The bank confirms a line. A credit leaves our cash; a debit (collection) arrives in it. */
    void confirm(String payoutId) {
        Payout p = payout(payoutId);
        if (p.state == PayoutState.PAID) return;
        if (p.state != PayoutState.IN_TRANSIT) throw new IllegalStateException(payoutId + " is " + p.state);
        if (p.direction == PayoutDirection.CREDIT) {
            ledger.post(clock.today(), "payout paid " + p.id, List.of(Posting.debit(inTransitAcct(p.merchantId), p.amount), Posting.credit(BANK, p.amount)));
        } else {
            ledger.post(clock.today(), "collection received " + p.id, List.of(Posting.debit(BANK, p.amount), Posting.credit(COLLECTIONS, p.amount)));
        }
        p.state = PayoutState.PAID;
    }

    /**
     * The bank returns a line (NACHA return codes: R01 insufficient funds, R02 account closed, R03 no account).
     * A returned credit goes back to available and payouts pause until the bank details are fixed; a returned
     * debit puts the deficit back on the merchant's balance.
     */
    void returned(String payoutId, String returnCode) {
        Payout p = payout(payoutId);
        if (p.state != PayoutState.IN_TRANSIT) throw new IllegalStateException(payoutId + " is " + p.state + "; cannot return");
        if (p.direction == PayoutDirection.CREDIT) {
            ledger.post(clock.today(), "payout returned " + p.id + " " + returnCode, List.of(
                    Posting.debit(inTransitAcct(p.merchantId), p.amount), Posting.credit(availableAcct(p.merchantId), p.amount)));
            paused.put(p.merchantId, "payout " + p.id + " returned " + returnCode);
        } else {
            ledger.post(clock.today(), "collection returned " + p.id + " " + returnCode, List.of(
                    Posting.debit(availableAcct(p.merchantId), p.amount), Posting.credit(COLLECTIONS, p.amount)));
            negativeSince.remove(p.merchantId);
            trackNegative(p.merchantId);
        }
        p.state = PayoutState.RETURNED;
        p.returnCode = returnCode;
    }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    static final Currency USD = Currency.USD;
    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    static void show(BalanceService svc, FakeClock clock, String m, String label) {
        Balance b = svc.balance(m);
        System.out.printf("%s %-3s %-38s pending %-11s available %-12s reserve %-10s in transit %s%n",
                clock.today(), clock.today().getDayOfWeek().toString().substring(0, 3), label, b.pending(), b.available(), b.reserve(), b.inTransit());
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(LocalDate.of(2026, 3, 2));   // a Monday
        InMemoryPayoutFileSink files = new InMemoryPayoutFileSink();
        BalanceService svc = new BalanceService(new Ledger(USD), clock, files, 7, USD);
        svc.onboard(new Merchant("m_shoe", "ba_tok_7Q2", new PayoutSchedule(PayoutFrequency.DAILY, null, usd(1_000)), new ReservePolicy(1_000, 90)));
        svc.onboard(new Merchant("m_art", "ba_tok_X91", new PayoutSchedule(PayoutFrequency.WEEKLY, DayOfWeek.FRIDAY, usd(2_500)), new ReservePolicy(0, 0)));

        System.out.println("== Captures are owed to the merchant but not payable: they are PENDING until the scheme pays us ==");
        svc.capture("cap_1", "m_shoe", usd(10_000), 290, 30);
        svc.capture("cap_2", "m_shoe", usd(25_000), 290, 30);
        svc.capture("cap_3", "m_shoe", usd(5_000), 290, 30);
        svc.capture("cap_4", "m_art", usd(18_000), 290, 30);
        show(svc, clock, "m_shoe", "after 3 captures (400.00 gross)");
        System.out.println("payout run Monday -> " + svc.runPayouts() + "  (nothing settled, nothing to pay)");
        clock.advanceDays(1);
        System.out.println("payout run Tuesday -> " + svc.runPayouts() + "  (the expected T+1 report did not arrive)");

        System.out.println();
        System.out.println("== Wednesday: the settlement report arrives and reconciles; 10% of m_shoe's net goes to a 90-day reserve ==");
        clock.advanceDays(1);
        try {
            svc.settle(new SettlementReport("st_bad", clock.today(), List.of("cap_1", "cap_2", "cap_3"), usd(39_999)));
        } catch (IllegalStateException e) {
            System.out.println("refused: " + e.getMessage());
        }
        svc.settle(new SettlementReport("st_0302", clock.today(), List.of("cap_1", "cap_2", "cap_3", "cap_4"), usd(58_000)));
        show(svc, clock, "m_shoe", "after settlement st_0302");
        List<Payout> wed = svc.runPayouts();
        System.out.println("payout run Wednesday -> " + wed);
        show(svc, clock, "m_shoe", "after the payout run");
        files.files.forEach((name, lines) -> { System.out.println("file " + name); lines.forEach(l -> System.out.println("   " + l)); });
        clock.advanceDays(1);
        svc.confirm(wed.get(0).id);
        show(svc, clock, "m_shoe", "bank confirmed the payout");

        System.out.println();
        System.out.println("== Friday: m_art's weekly payout is returned R03 (no account); payouts pause ==");
        clock.advanceDays(1);
        List<Payout> fri = svc.runPayouts();
        System.out.println("payout run Friday -> " + fri);
        svc.returned(fri.get(0).id, "R03");
        show(svc, clock, "m_art", "after the return");
        clock.advanceDays(7);
        System.out.println("next Friday run -> " + svc.runPayouts() + "  (m_art paused: " + svc.isPaused("m_art") + ")");
        svc.resume("m_art", "bank details re-verified");
        System.out.println("after resume, Friday run would pay " + svc.balance("m_art").available());

        System.out.println();
        System.out.println("== A 600.00 chargeback drives m_shoe negative; the reserve covers what it can ==");
        clock.advanceDays(3);   // Monday 2026-03-16
        svc.debitMerchant("m_shoe", usd(60_000), "chargeback dp_9");
        show(svc, clock, "m_shoe", "after the chargeback");
        svc.runPayouts();
        show(svc, clock, "m_shoe", "reserve applied, still negative");
        System.out.println("negative since " + svc.negativeSince("m_shoe"));

        System.out.println();
        System.out.println("== New settlements net against the deficit; after 7 negative days the rest is collected ==");
        svc.capture("cap_5", "m_shoe", usd(8_000), 290, 30);
        clock.advanceDays(2);
        svc.settle(new SettlementReport("st_0316", clock.today(), List.of("cap_5"), usd(8_000)));
        svc.runPayouts();
        show(svc, clock, "m_shoe", "netted by st_0316, reserve applied");
        clock.advanceDays(5);   // Monday 2026-03-23, 7 days negative
        List<Payout> collect = svc.runPayouts();
        System.out.println("payout run -> " + collect);
        show(svc, clock, "m_shoe", "collection initiated");
        clock.advanceDays(2);
        svc.confirm(collect.get(0).id);
        show(svc, clock, "m_shoe", "collection received");

        System.out.println();
        Ledger l = svc.ledger();
        System.out.println("== Ledger ==");
        for (String a : List.of(BalanceService.SCHEME, BalanceService.BANK, BalanceService.COLLECTIONS, BalanceService.FEES)) {
            System.out.printf("   %-24s %12s%n", a, l.balance(a));
        }
        for (String m : List.of("m_shoe", "m_art")) System.out.printf("   %-24s %12s owed in total%n", m, svc.balance(m).total());
        System.out.println("   ledger balances: " + l.balances() + ", journal entries: " + l.journal().size());
    }
}
