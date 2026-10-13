// Lesson: phases/12-payment-systems-design/07-disputes-and-chargebacks/docs/en.md
// Disputes and Chargebacks: The Lifecycle and the Evidence
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Visa Core Rules (Dispute Resolution: Allocation and Collaboration flows, reason codes 10-13);
//          Mastercard Chargeback Guide (reason codes 4834, 4837, 4853; second presentment; pre-arbitration). Java 21, stdlib only.

import java.time.LocalDate;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

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
    boolean isPositive() { return minorUnits > 0; }
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
// A dispute lives for months. The clock is a calendar date that tests move forward by days.
// ---------------------------------------------------------------------------------------------

interface Clock { LocalDate today(); }

final class FakeClock implements Clock {
    private LocalDate today;
    FakeClock(LocalDate start) { today = start; }
    @Override public LocalDate today() { return today; }
    void advanceDays(long days) { today = today.plusDays(days); }
}

// ---------------------------------------------------------------------------------------------
// Minimal double-entry ledger (Phase 10, Lesson 03).
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
    @Override public String toString() { return (side == Side.DEBIT ? "Dr " : "   Cr ") + account + " " + amount; }
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

    Ledger(Currency currency) { this.currency = currency; }

    void open(String account, AccountType type) { accounts.put(account, type); balances.put(account, 0L); }

    void post(JournalEntry e) {
        for (Posting p : e.postings()) if (!accounts.containsKey(p.account())) throw new IllegalArgumentException("unknown account " + p.account());
        journal.add(e);
        for (Posting p : e.postings()) {
            long signed = p.side() == accounts.get(p.account()).normal ? p.amount().minorUnits() : -p.amount().minorUnits();
            balances.merge(p.account(), signed, Math::addExact);
        }
    }

    Money balance(String account) { return Money.ofMinor(balances.get(account), currency); }

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
// Reason codes and evidence. A reason code says what the cardholder claims; it decides which
// evidence can answer the claim. Each requirement is "at least one of" a set of evidence types.
// ---------------------------------------------------------------------------------------------

enum ReasonCategory { FRAUD, AUTHORIZATION, PROCESSING_ERROR, CONSUMER_DISPUTE }

enum EvidenceType {
    RECEIPT, PROOF_OF_DELIVERY, SIGNED_DELIVERY, PRODUCT_DESCRIPTION, CUSTOMER_COMMUNICATION, REFUND_POLICY,
    THREE_DS_AUTHENTICATION, PRIOR_UNDISPUTED_TRANSACTIONS, AVS_CVV_MATCH, AUTHORIZATION_RECORD,
    DUPLICATE_EXPLANATION, REFUND_ISSUED
}

record ReasonCode(String network, String code, String title, ReasonCategory category, List<Set<EvidenceType>> requirements) {
    ReasonCode {
        // EnumSet iterates in declaration order, so messages and the printed table are deterministic (Set.copyOf is not).
        requirements = requirements.stream().map(r -> Collections.unmodifiableSet(EnumSet.copyOf(r))).toList();
    }
    String key() { return network + " " + code; }
}

final class ReasonCodes {
    static final Map<String, ReasonCode> TABLE = new LinkedHashMap<>();

    @SafeVarargs
    private static void add(String network, String code, String title, ReasonCategory cat, Set<EvidenceType>... reqs) {
        ReasonCode rc = new ReasonCode(network, code, title, cat, List.of(reqs));
        TABLE.put(rc.key(), rc);
    }

    static {
        add("VISA", "10.4", "Other Fraud: Card-Absent Environment", ReasonCategory.FRAUD,
                EnumSet.of(EvidenceType.THREE_DS_AUTHENTICATION, EvidenceType.PRIOR_UNDISPUTED_TRANSACTIONS), EnumSet.of(EvidenceType.RECEIPT));
        add("VISA", "11.3", "No Authorization", ReasonCategory.AUTHORIZATION, EnumSet.of(EvidenceType.AUTHORIZATION_RECORD));
        add("VISA", "12.6.1", "Duplicate Processing", ReasonCategory.PROCESSING_ERROR, EnumSet.of(EvidenceType.DUPLICATE_EXPLANATION));
        add("VISA", "13.1", "Merchandise/Services Not Received", ReasonCategory.CONSUMER_DISPUTE,
                EnumSet.of(EvidenceType.PROOF_OF_DELIVERY, EvidenceType.SIGNED_DELIVERY), EnumSet.of(EvidenceType.RECEIPT));
        add("VISA", "13.3", "Not as Described or Defective", ReasonCategory.CONSUMER_DISPUTE,
                EnumSet.of(EvidenceType.PRODUCT_DESCRIPTION), EnumSet.of(EvidenceType.CUSTOMER_COMMUNICATION, EvidenceType.REFUND_POLICY));
        add("VISA", "13.6", "Credit Not Processed", ReasonCategory.CONSUMER_DISPUTE,
                EnumSet.of(EvidenceType.REFUND_ISSUED, EvidenceType.REFUND_POLICY));
        add("MASTERCARD", "4837", "No Cardholder Authorization", ReasonCategory.FRAUD,
                EnumSet.of(EvidenceType.THREE_DS_AUTHENTICATION, EvidenceType.PRIOR_UNDISPUTED_TRANSACTIONS));
        add("MASTERCARD", "4834", "Point-of-Interaction Error", ReasonCategory.PROCESSING_ERROR,
                EnumSet.of(EvidenceType.DUPLICATE_EXPLANATION, EvidenceType.RECEIPT));
        add("MASTERCARD", "4853", "Cardholder Dispute", ReasonCategory.CONSUMER_DISPUTE,
                EnumSet.of(EvidenceType.PROOF_OF_DELIVERY, EvidenceType.PRODUCT_DESCRIPTION), EnumSet.of(EvidenceType.CUSTOMER_COMMUNICATION));
    }

    static ReasonCode get(String network, String code) {
        ReasonCode rc = TABLE.get(network + " " + code);
        if (rc == null) throw new IllegalArgumentException("unknown reason code " + network + " " + code);
        return rc;
    }
}

record EvidenceItem(EvidenceType type, String reference) {}

record EvidenceBundle(List<EvidenceItem> items) {
    EvidenceBundle { items = List.copyOf(items); }

    static EvidenceBundle of(EvidenceItem... items) { return new EvidenceBundle(List.of(items)); }

    boolean has(EvidenceType t) { return items.stream().anyMatch(i -> i.type() == t); }

    /** Requirements not met by any item: each entry is a set of which at least one must be present. */
    List<Set<EvidenceType>> missingFor(ReasonCode reason) {
        List<Set<EvidenceType>> missing = new ArrayList<>();
        for (Set<EvidenceType> req : reason.requirements()) if (req.stream().noneMatch(this::has)) missing.add(req);
        return missing;
    }

    /** A fraud claim against a transaction the issuer itself authenticated with 3-D Secure shifts liability to the issuer. */
    boolean liabilityShift(ReasonCode reason) {
        return reason.category() == ReasonCategory.FRAUD && has(EvidenceType.THREE_DS_AUTHENTICATION);
    }
}

// ---------------------------------------------------------------------------------------------
// The dispute state machine (the Lesson 02 design: a table of rules, guards, a transition log,
// event-id deduplication), with one addition: most states carry a deadline, and when the clock
// passes it the system itself applies DEADLINE_PASSED.
// ---------------------------------------------------------------------------------------------

enum DisputeState {
    INQUIRY, INQUIRY_RESPONDED, CHARGEBACK, REPRESENTED, PRE_ARBITRATION, PRE_ARB_RESPONDED, ARBITRATION,
    CLOSED_NO_CHARGEBACK, WON, LOST;

    boolean isTerminal() { return this == CLOSED_NO_CHARGEBACK || this == WON || this == LOST; }
}

enum DisputeEventKind {
    RESPOND_INQUIRY, INQUIRY_CLOSED, CHARGEBACK_RECEIVED, ACCEPT, SUBMIT_REPRESENTMENT, ISSUER_ACCEPTED,
    PRE_ARB_RECEIVED, RESPOND_PRE_ARB, ISSUER_WITHDREW, ARBITRATION_FILED, RULING_FOR_MERCHANT, RULING_FOR_ISSUER,
    DEADLINE_PASSED
}

enum DisputeGuard { ALWAYS, EVIDENCE_COMPLETE_IN_TIME, DEADLINE_ELAPSED }

record DisputeRule(DisputeState from, DisputeEventKind kind, DisputeGuard guard, DisputeState to) {}

record DisputeEvent(String eventId, DisputeEventKind kind, EvidenceBundle evidence, String note) {
    static DisputeEvent of(String id, DisputeEventKind kind, String note) { return new DisputeEvent(id, kind, null, note); }
    static DisputeEvent withEvidence(String id, DisputeEventKind kind, EvidenceBundle evidence, String note) {
        return new DisputeEvent(id, kind, Objects.requireNonNull(evidence, "evidence"), note);
    }
}

record DisputeTransition(int version, LocalDate date, String eventId, DisputeEventKind kind, DisputeState from, DisputeState to, String note) {
    @Override public String toString() {
        return String.format("v%-2d %s  %-20s %-17s -> %-17s %s", version, date, kind == null ? "OPENED" : kind,
                from == null ? "-" : from, to, note);
    }
}

final class IllegalDisputeTransitionException extends IllegalStateException {
    IllegalDisputeTransitionException(String m) { super(m); }
}

enum ApplyOutcome { APPLIED, DUPLICATE }

/** Days allowed in each state before DEADLINE_PASSED fires. A state absent from the map has no automatic deadline. */
record DeadlinePolicy(Map<DisputeState, Integer> days) {
    DeadlinePolicy { days = Collections.unmodifiableMap(new EnumMap<>(days)); }

    static DeadlinePolicy standard() {
        Map<DisputeState, Integer> d = new EnumMap<>(DisputeState.class);
        d.put(DisputeState.INQUIRY, 20);            // respond to a retrieval request (shown as overdue only)
        d.put(DisputeState.CHARGEBACK, 20);         // merchant response; the network gives the acquirer 30
        d.put(DisputeState.REPRESENTED, 30);        // issuer may answer with pre-arbitration
        d.put(DisputeState.PRE_ARBITRATION, 30);    // merchant accepts or responds
        d.put(DisputeState.PRE_ARB_RESPONDED, 10);  // issuer may escalate to arbitration
        return new DeadlinePolicy(d);
    }
}

final class Dispute {
    final String id;
    final String paymentId;
    final Money amount;
    final ReasonCode reason;
    DisputeState state;
    LocalDate stateEnteredOn;
    LocalDate deadline;                         // null when the current state has no deadline
    EvidenceBundle evidence;
    final List<DisputeTransition> history = new ArrayList<>();
    final Set<String> appliedEventIds = new HashSet<>();

    Dispute(String id, String paymentId, Money amount, ReasonCode reason, DisputeState initial, LocalDate today) {
        this.id = id;
        this.paymentId = paymentId;
        this.amount = amount;
        this.reason = reason;
        this.state = initial;
        this.stateEnteredOn = today;
    }

    DisputeState state() { return state; }
    LocalDate deadline() { return deadline; }
    List<DisputeTransition> history() { return Collections.unmodifiableList(history); }
}

final class DisputeService {
    static final String MERCHANT = "merchant_payable";       // liability: what we owe the merchant
    static final String SCHEME = "scheme_settlement";         // asset: net position with the card scheme
    static final String HOLD = "dispute_hold";                // liability: funds re-credited by the scheme, held until the outcome
    static final String FEES = "fee_revenue";                 // revenue: chargeback fees

    static final List<DisputeRule> RULES = List.of(
            new DisputeRule(DisputeState.INQUIRY, DisputeEventKind.RESPOND_INQUIRY, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.INQUIRY_RESPONDED),
            new DisputeRule(DisputeState.INQUIRY, DisputeEventKind.CHARGEBACK_RECEIVED, DisputeGuard.ALWAYS, DisputeState.CHARGEBACK),
            new DisputeRule(DisputeState.INQUIRY_RESPONDED, DisputeEventKind.INQUIRY_CLOSED, DisputeGuard.ALWAYS, DisputeState.CLOSED_NO_CHARGEBACK),
            new DisputeRule(DisputeState.INQUIRY_RESPONDED, DisputeEventKind.CHARGEBACK_RECEIVED, DisputeGuard.ALWAYS, DisputeState.CHARGEBACK),
            new DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.ACCEPT, DisputeGuard.ALWAYS, DisputeState.LOST),
            new DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.REPRESENTED),
            new DisputeRule(DisputeState.CHARGEBACK, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.LOST),
            new DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.ISSUER_ACCEPTED, DisputeGuard.ALWAYS, DisputeState.WON),
            new DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.PRE_ARB_RECEIVED, DisputeGuard.ALWAYS, DisputeState.PRE_ARBITRATION),
            new DisputeRule(DisputeState.REPRESENTED, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.WON),
            new DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.ACCEPT, DisputeGuard.ALWAYS, DisputeState.LOST),
            new DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.RESPOND_PRE_ARB, DisputeGuard.EVIDENCE_COMPLETE_IN_TIME, DisputeState.PRE_ARB_RESPONDED),
            new DisputeRule(DisputeState.PRE_ARBITRATION, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.LOST),
            new DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.ISSUER_WITHDREW, DisputeGuard.ALWAYS, DisputeState.WON),
            new DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.ARBITRATION_FILED, DisputeGuard.ALWAYS, DisputeState.ARBITRATION),
            new DisputeRule(DisputeState.PRE_ARB_RESPONDED, DisputeEventKind.DEADLINE_PASSED, DisputeGuard.DEADLINE_ELAPSED, DisputeState.WON),
            new DisputeRule(DisputeState.ARBITRATION, DisputeEventKind.RULING_FOR_MERCHANT, DisputeGuard.ALWAYS, DisputeState.WON),
            new DisputeRule(DisputeState.ARBITRATION, DisputeEventKind.RULING_FOR_ISSUER, DisputeGuard.ALWAYS, DisputeState.LOST));

    private final Ledger ledger;
    private final Clock clock;
    private final DeadlinePolicy deadlines;
    private final Money chargebackFee;
    private final Money arbitrationFee;
    private final Map<String, Dispute> disputes = new LinkedHashMap<>();
    private int entrySeq = 0;

    DisputeService(Ledger ledger, Clock clock, DeadlinePolicy deadlines, Money chargebackFee, Money arbitrationFee) {
        this.ledger = ledger;
        this.clock = clock;
        this.deadlines = deadlines;
        this.chargebackFee = chargebackFee;
        this.arbitrationFee = arbitrationFee;
        ledger.open(SCHEME, AccountType.ASSET);
        ledger.open(MERCHANT, AccountType.LIABILITY);
        ledger.open(HOLD, AccountType.LIABILITY);
        ledger.open(FEES, AccountType.REVENUE);
    }

    Ledger ledger() { return ledger; }

    Dispute dispute(String id) {
        Dispute d = disputes.get(id);
        if (d == null) throw new IllegalArgumentException("unknown dispute " + id);
        return d;
    }

    /** A retrieval request: the issuer asks for information. No money moves. */
    Dispute openInquiry(String id, String paymentId, Money amount, ReasonCode reason) {
        return open(id, paymentId, amount, reason, DisputeState.INQUIRY);
    }

    /** A first chargeback: the scheme has already taken the money back from us. */
    Dispute openChargeback(String id, String paymentId, Money amount, ReasonCode reason) {
        Dispute d = open(id, paymentId, amount, reason, DisputeState.CHARGEBACK);
        postMoney(d, DisputeState.INQUIRY, DisputeState.CHARGEBACK);
        return d;
    }

    private Dispute open(String id, String paymentId, Money amount, ReasonCode reason, DisputeState initial) {
        if (disputes.containsKey(id)) throw new IllegalStateException("dispute exists: " + id);
        if (!amount.isPositive()) throw new IllegalArgumentException("dispute amount must be positive");
        Dispute d = new Dispute(id, paymentId, amount, reason, initial, clock.today());
        d.deadline = deadlineFor(initial, clock.today());
        d.history.add(new DisputeTransition(0, clock.today(), "open:" + id, null, null, initial, "opened: " + reason.key() + " " + reason.title()));
        disputes.put(id, d);
        return d;
    }

    private LocalDate deadlineFor(DisputeState s, LocalDate entered) {
        Integer days = deadlines.days().get(s);
        return days == null ? null : entered.plusDays(days);
    }

    /** Pick the rule, check its guard, or explain precisely why not. Pure: never mutates. */
    DisputeRule resolve(Dispute d, DisputeEvent e) {
        if (d.state.isTerminal()) throw new IllegalDisputeTransitionException(d.id + " is " + d.state + " (terminal); " + e.kind() + " refused");
        DisputeRule rule = RULES.stream().filter(r -> r.from() == d.state && r.kind() == e.kind()).findFirst()
                .orElseThrow(() -> new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " is not legal from " + d.state));
        if (rule.guard() == DisputeGuard.DEADLINE_ELAPSED && (d.deadline == null || !clock.today().isAfter(d.deadline))) {
            throw new IllegalDisputeTransitionException(d.id + ": DEADLINE_PASSED on " + clock.today() + " but the deadline is " + d.deadline);
        }
        if (rule.guard() == DisputeGuard.EVIDENCE_COMPLETE_IN_TIME) {
            if (d.deadline != null && clock.today().isAfter(d.deadline)) {
                throw new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " on " + clock.today() + " is after the deadline " + d.deadline);
            }
            if (e.evidence() == null) throw new IllegalDisputeTransitionException(d.id + ": " + e.kind() + " needs an evidence bundle");
            List<Set<EvidenceType>> missing = e.evidence().missingFor(d.reason);
            if (!missing.isEmpty()) {
                throw new IllegalDisputeTransitionException(d.id + ": evidence for " + d.reason.key() + " is incomplete; missing one of " + missing);
            }
        }
        return rule;
    }

    ApplyOutcome apply(String disputeId, DisputeEvent e) {
        Dispute d = dispute(disputeId);
        if (d.appliedEventIds.contains(e.eventId())) return ApplyOutcome.DUPLICATE;
        DisputeRule rule = resolve(d, e);                       // throws before any mutation
        DisputeState from = d.state;
        postMoney(d, from, rule.to());
        d.state = rule.to();
        d.stateEnteredOn = clock.today();
        d.deadline = rule.to().isTerminal() ? null : deadlineFor(rule.to(), clock.today());
        if (e.evidence() != null) d.evidence = e.evidence();
        d.appliedEventIds.add(e.eventId());
        d.history.add(new DisputeTransition(d.history.size(), clock.today(), e.eventId(), e.kind(), from, rule.to(), e.note()));
        return ApplyOutcome.APPLIED;
    }

    /**
     * Apply DEADLINE_PASSED to every open dispute whose deadline is before today and whose state has a
     * deadline rule. The event id names the state and the deadline, so running the sweep twice is harmless.
     * INQUIRY has a deadline but no rule: it is reported overdue, and only the issuer can move it.
     */
    List<String> sweepDeadlines() {
        List<String> fired = new ArrayList<>();
        for (Dispute d : disputes.values()) {
            if (d.state.isTerminal() || d.deadline == null || !clock.today().isAfter(d.deadline)) continue;
            boolean hasRule = RULES.stream().anyMatch(r -> r.from() == d.state && r.kind() == DisputeEventKind.DEADLINE_PASSED);
            if (!hasRule) { fired.add(d.id + " overdue in " + d.state + " (no automatic outcome)"); continue; }
            DisputeState before = d.state;
            apply(d.id, DisputeEvent.of("deadline:" + d.id + ":" + d.state + ":" + d.deadline, DisputeEventKind.DEADLINE_PASSED,
                    "deadline " + d.deadline + " passed"));
            fired.add(d.id + " " + before + " -> " + d.state);
        }
        return fired;
    }

    /**
     * The money that moves on each transition. The first chargeback debits the merchant (plus a fee);
     * a representment brings the funds back from the scheme into a hold, not to the merchant; the
     * outcome either releases the hold to the merchant or returns it to the scheme.
     */
    private void postMoney(Dispute d, DisputeState from, DisputeState to) {
        List<Posting> p = new ArrayList<>();
        String what;
        if (to == DisputeState.CHARGEBACK) {
            what = "chargeback debited";
            p.add(Posting.debit(MERCHANT, d.amount));
            p.add(Posting.credit(SCHEME, d.amount));
            p.add(Posting.debit(MERCHANT, chargebackFee));
            p.add(Posting.credit(FEES, chargebackFee));
        } else if (to == DisputeState.REPRESENTED) {
            what = "representment: funds re-credited, held";
            p.add(Posting.debit(SCHEME, d.amount));
            p.add(Posting.credit(HOLD, d.amount));
        } else if (to == DisputeState.WON && from != DisputeState.CHARGEBACK) {
            what = "won: hold released to merchant";
            p.add(Posting.debit(HOLD, d.amount));
            p.add(Posting.credit(MERCHANT, d.amount));
        } else if (to == DisputeState.LOST && from != DisputeState.CHARGEBACK) {
            what = "lost after representment: hold returned to scheme";
            p.add(Posting.debit(HOLD, d.amount));
            p.add(Posting.credit(SCHEME, d.amount));
            if (from == DisputeState.ARBITRATION) {
                p.add(Posting.debit(MERCHANT, arbitrationFee));
                p.add(Posting.credit(SCHEME, arbitrationFee));
            }
        } else {
            return;   // inquiry steps, pre-arbitration, arbitration filing, and LOST from CHARGEBACK move no money
        }
        ledger.post(new JournalEntry(String.format("je_%03d", ++entrySeq), clock.today(), d.id + " " + what, p));
    }

    /** What this dispute has cost the merchant so far: the net of every merchant_payable posting tagged with its id. */
    Money merchantImpact(String disputeId) {
        Money net = Money.zero(dispute(disputeId).amount.currency());
        for (JournalEntry e : ledger.journal()) {
            if (!e.description().startsWith(disputeId + " ")) continue;
            for (Posting p : e.postings()) {
                if (!p.account().equals(MERCHANT)) continue;
                net = p.side() == Side.CREDIT ? net.plus(p.amount()) : net.minus(p.amount());
            }
        }
        return net;
    }

    static long daysBetween(LocalDate a, LocalDate b) { return ChronoUnit.DAYS.between(a, b); }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    static DisputeService service(FakeClock clock) {
        return new DisputeService(new Ledger(Currency.USD), clock, DeadlinePolicy.standard(), usd(1_500), usd(50_000));
    }

    static void step(DisputeService svc, FakeClock clock, LocalDate start, String id, String label) {
        Dispute d = svc.dispute(id);
        System.out.printf("day %3d  %-40s state %-17s deadline %-10s hold %-11s merchant %s%n",
                DisputeService.daysBetween(start, clock.today()), label, d.state(), d.deadline() == null ? "-" : d.deadline(),
                svc.ledger().balance(DisputeService.HOLD), svc.merchantImpact(id));
    }

    public static void main(String[] args) {
        System.out.println("== Reason codes and the evidence that answers them (each group: at least one of) ==");
        for (ReasonCode rc : ReasonCodes.TABLE.values()) {
            System.out.printf("%-17s %-17s %-38s %s%n", rc.key(), rc.category(), rc.title(), rc.requirements());
        }

        System.out.println();
        System.out.println("== A 'not received' dispute that runs the whole course: 249.00 USD, Visa 13.1 ==");
        LocalDate start = LocalDate.of(2026, 2, 2);
        FakeClock clock = new FakeClock(start);
        DisputeService svc = service(clock);
        ReasonCode notReceived = ReasonCodes.get("VISA", "13.1");
        svc.openChargeback("dp_1", "pay_77", usd(24_900), notReceived);
        step(svc, clock, start, "dp_1", "first chargeback received");
        clock.advanceDays(9);
        EvidenceBundle thin = EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"));
        try {
            svc.apply("dp_1", DisputeEvent.withEvidence("ev_1", DisputeEventKind.SUBMIT_REPRESENTMENT, thin, "receipt only"));
        } catch (IllegalDisputeTransitionException e) {
            System.out.println("         refused: " + e.getMessage());
        }
        clock.advanceDays(3);
        EvidenceBundle full = EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"),
                new EvidenceItem(EvidenceType.SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"));
        svc.apply("dp_1", DisputeEvent.withEvidence("ev_2", DisputeEventKind.SUBMIT_REPRESENTMENT, full, "receipt + signed delivery"));
        step(svc, clock, start, "dp_1", "representment submitted");
        clock.advanceDays(29);
        svc.apply("dp_1", DisputeEvent.of("iss_3", DisputeEventKind.PRE_ARB_RECEIVED, "issuer: cardholder says signature not theirs"));
        step(svc, clock, start, "dp_1", "pre-arbitration from issuer");
        clock.advanceDays(15);
        EvidenceBundle more = EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "inv_77.pdf"),
                new EvidenceItem(EvidenceType.SIGNED_DELIVERY, "carrier POD 1Z999 signed J. DOE"),
                new EvidenceItem(EvidenceType.CUSTOMER_COMMUNICATION, "email 2026-01-30 confirming arrival"));
        svc.apply("dp_1", DisputeEvent.withEvidence("ev_4", DisputeEventKind.RESPOND_PRE_ARB, more, "pre-arb declined with new evidence"));
        step(svc, clock, start, "dp_1", "pre-arbitration response");
        clock.advanceDays(7);
        svc.apply("dp_1", DisputeEvent.of("iss_5", DisputeEventKind.ARBITRATION_FILED, "issuer filed with Visa"));
        step(svc, clock, start, "dp_1", "issuer files arbitration");
        clock.advanceDays(55);
        svc.apply("dp_1", DisputeEvent.of("net_6", DisputeEventKind.RULING_FOR_MERCHANT, "Visa ruling: merchant"));
        step(svc, clock, start, "dp_1", "network ruling for the merchant");
        System.out.println("history:");
        for (DisputeTransition t : svc.dispute("dp_1").history()) System.out.println("   " + t);
        System.out.println("duplicate ruling delivery -> " + svc.apply("dp_1", DisputeEvent.of("net_6", DisputeEventKind.RULING_FOR_MERCHANT, "again")));

        System.out.println();
        System.out.println("== Missed deadline: a fraud chargeback nobody answers ==");
        LocalDate s2 = clock.today();
        svc.openChargeback("dp_2", "pay_88", usd(8_000), ReasonCodes.get("VISA", "10.4"));
        clock.advanceDays(20);
        System.out.println("day 20 sweep: " + svc.sweepDeadlines());
        clock.advanceDays(1);
        System.out.println("day 21 sweep: " + svc.sweepDeadlines());
        step(svc, clock, s2, "dp_2", "after the sweep");

        System.out.println();
        System.out.println("== 3-D Secure: a fraud claim on an authenticated payment ==");
        LocalDate s3 = clock.today();
        ReasonCode fraud = ReasonCodes.get("MASTERCARD", "4837");
        svc.openChargeback("dp_3", "pay_99", usd(12_000), fraud);
        EvidenceBundle threeDs = EvidenceBundle.of(new EvidenceItem(EvidenceType.THREE_DS_AUTHENTICATION, "ECI 02, authentication value present"));
        System.out.println("liability shift: " + threeDs.liabilityShift(fraud));
        clock.advanceDays(4);
        svc.apply("dp_3", DisputeEvent.withEvidence("ev_31", DisputeEventKind.SUBMIT_REPRESENTMENT, threeDs, "3DS authenticated"));
        clock.advanceDays(31);
        System.out.println("day 35 sweep: " + svc.sweepDeadlines());
        step(svc, clock, s3, "dp_3", "issuer silent past its deadline");

        System.out.println();
        System.out.println("== Inquiry answered, issuer closes it: no money moves ==");
        LocalDate s4 = clock.today();
        svc.openInquiry("dp_4", "pay_11", usd(4_500), ReasonCodes.get("VISA", "13.3"));
        svc.apply("dp_4", DisputeEvent.withEvidence("ev_41", DisputeEventKind.RESPOND_INQUIRY,
                EvidenceBundle.of(new EvidenceItem(EvidenceType.PRODUCT_DESCRIPTION, "listing snapshot"),
                        new EvidenceItem(EvidenceType.REFUND_POLICY, "returns page")), "answered"));
        clock.advanceDays(6);
        svc.apply("dp_4", DisputeEvent.of("iss_42", DisputeEventKind.INQUIRY_CLOSED, "issuer satisfied"));
        step(svc, clock, s4, "dp_4", "inquiry closed");

        System.out.println();
        System.out.println("== Arbitration lost: amount, chargeback fee, and arbitration fee ==");
        LocalDate s5 = clock.today();
        svc.openChargeback("dp_5", "pay_55", usd(30_000), ReasonCodes.get("MASTERCARD", "4853"));
        svc.apply("dp_5", DisputeEvent.withEvidence("ev_51", DisputeEventKind.SUBMIT_REPRESENTMENT,
                EvidenceBundle.of(new EvidenceItem(EvidenceType.PRODUCT_DESCRIPTION, "spec sheet"),
                        new EvidenceItem(EvidenceType.CUSTOMER_COMMUNICATION, "chat log")), "represented"));
        svc.apply("dp_5", DisputeEvent.of("iss_52", DisputeEventKind.PRE_ARB_RECEIVED, "issuer disagrees"));
        svc.apply("dp_5", DisputeEvent.withEvidence("ev_53", DisputeEventKind.RESPOND_PRE_ARB,
                EvidenceBundle.of(new EvidenceItem(EvidenceType.PRODUCT_DESCRIPTION, "spec sheet"),
                        new EvidenceItem(EvidenceType.CUSTOMER_COMMUNICATION, "chat log")), "declined pre-arb"));
        svc.apply("dp_5", DisputeEvent.of("iss_54", DisputeEventKind.ARBITRATION_FILED, "filed"));
        svc.apply("dp_5", DisputeEvent.of("net_55", DisputeEventKind.RULING_FOR_ISSUER, "ruling: issuer"));
        step(svc, clock, s5, "dp_5", "arbitration lost");

        System.out.println();
        System.out.println("== Ledger ==");
        for (String a : List.of(DisputeService.SCHEME, DisputeService.MERCHANT, DisputeService.HOLD, DisputeService.FEES)) {
            System.out.printf("   %-18s %12s%n", a, svc.ledger().balance(a));
        }
        System.out.println("   ledger balances: " + svc.ledger().balances() + ", journal entries: " + svc.ledger().journal().size());
    }
}
