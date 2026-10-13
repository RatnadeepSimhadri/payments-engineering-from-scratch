// Lesson: phases/10-money-and-ledgers/08-reconciliation/docs/en.md
// Reconciliation: Matching Your Ledger Against Theirs
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 4217 (minor units); ISO 20022 camt.053 (bank statement structure the "theirs" side models).
// Java 21, standard library only. Money is integer minor units, copied from Phase 10, Lesson 01.

import java.time.LocalDate;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Same design as Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency JPY = new Currency("JPY", 0);

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
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

/** Exact amount of one currency as a signed count of minor units. Subset of the Lesson 01 type. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {

    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(
                Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }

    Money abs() { return minorUnits < 0 ? negate() : this; }

    boolean isZero() { return minorUnits == 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long magnitude = Math.abs(minorUnits);
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + magnitude + " " + currency.code();
        String frac = Long.toString(magnitude % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return sign + (magnitude / scale) + "." + frac + " " + currency.code();
    }
}

/** One line from OUR ledger: a posting against the settlement account that we expect the processor to confirm. */
record LedgerPosting(String reference, LocalDate date, Money amount, String account) {
    LedgerPosting {
        Objects.requireNonNull(reference, "reference");
        Objects.requireNonNull(date, "date");
        Objects.requireNonNull(amount, "amount");
        Objects.requireNonNull(account, "account");
    }
}

/** One line from THEIR statement: what the processor or bank says happened. */
record StatementLine(String reference, LocalDate date, Money amount, String description) {
    StatementLine {
        Objects.requireNonNull(reference, "reference");
        Objects.requireNonNull(date, "date");
        Objects.requireNonNull(amount, "amount");
        Objects.requireNonNull(description, "description");
    }
}

/** Which pass produced a match. EXACT_REFERENCE is strong evidence; AMOUNT_DATE is a heuristic that a human may review. */
enum MatchRule { EXACT_REFERENCE, AMOUNT_DATE }

/** A posting of ours paired with a line of theirs. Amounts are equal by construction. */
record Match(LedgerPosting ours, StatementLine theirs, MatchRule rule) {
    Match {
        if (!ours.amount().equals(theirs.amount())) {
            throw new IllegalArgumentException("a Match must have equal amounts; got " + ours.amount() + " vs " + theirs.amount());
        }
    }
}

/**
 * The five ways two books disagree. The order of the constants is the order breaks are reported in,
 * which is also the order an operations analyst works them: money that is missing first, duplicates last.
 */
enum BreakKind {
    MISSING_OURS,     // on their statement, nowhere in our ledger
    MISSING_THEIRS,   // in our ledger, nowhere on their statement
    AMOUNT_MISMATCH,  // same reference on both sides, different amount
    DUPLICATE_OURS,   // our ledger has more copies of a reference than their statement
    DUPLICATE_THEIRS  // their statement has more copies of a reference than our ledger
}

/** One unexplained item. Exactly one of ours/theirs is null for MISSING_* and DUPLICATE_*; both are set for AMOUNT_MISMATCH. */
record Break(BreakKind kind, LedgerPosting ours, StatementLine theirs) {
    Break {
        Objects.requireNonNull(kind, "kind");
        switch (kind) {
            case MISSING_OURS, DUPLICATE_THEIRS -> { if (ours != null || theirs == null) throw new IllegalArgumentException(kind + " needs theirs only"); }
            case MISSING_THEIRS, DUPLICATE_OURS -> { if (ours == null || theirs != null) throw new IllegalArgumentException(kind + " needs ours only"); }
            case AMOUNT_MISMATCH -> { if (ours == null || theirs == null) throw new IllegalArgumentException(kind + " needs both sides"); }
        }
    }

    /** Our side's amount in this break, or zero. */
    Money oursAmount(Currency currency) { return ours == null ? Money.zero(currency) : ours.amount(); }

    /** Their side's amount in this break, or zero. */
    Money theirsAmount(Currency currency) { return theirs == null ? Money.zero(currency) : theirs.amount(); }

    /** ours - theirs: positive means we recorded more than they did. */
    Money difference(Currency currency) { return oursAmount(currency).minus(theirsAmount(currency)); }

    String reference() { return ours != null ? ours.reference() : theirs.reference(); }

    LocalDate date() { return ours != null ? ours.date() : theirs.date(); }
}

/**
 * The result of one reconciliation run. Every input record appears in exactly one Match or exactly one Break,
 * which is what makes the totals tie out: ledgerTotal == matchedTotal + oursUnmatched and
 * statementTotal == matchedTotal + theirsUnmatched.
 */
record ReconciliationReport(Currency currency, List<Match> matches, List<Break> breaks,
                            Money ledgerTotal, Money statementTotal) {

    ReconciliationReport {
        matches = List.copyOf(matches);
        breaks = List.copyOf(breaks);
    }

    Money matchedTotal() {
        Money total = Money.zero(currency);
        for (Match m : matches) total = total.plus(m.ours().amount());
        return total;
    }

    Money oursUnmatched() {
        Money total = Money.zero(currency);
        for (Break b : breaks) total = total.plus(b.oursAmount(currency));
        return total;
    }

    Money theirsUnmatched() {
        Money total = Money.zero(currency);
        for (Break b : breaks) total = total.plus(b.theirsAmount(currency));
        return total;
    }

    /** ledgerTotal - statementTotal, which must equal oursUnmatched - theirsUnmatched. */
    Money netDifference() { return ledgerTotal.minus(statementTotal); }

    /** The tie-out: the only number a controller signs. */
    boolean tiesOut() {
        return ledgerTotal.equals(matchedTotal().plus(oursUnmatched()))
                && statementTotal.equals(matchedTotal().plus(theirsUnmatched()))
                && netDifference().equals(oursUnmatched().minus(theirsUnmatched()));
    }

    long countBreaks(BreakKind kind) { return breaks.stream().filter(b -> b.kind() == kind).count(); }

    boolean isClean() { return breaks.isEmpty(); }

    String render() {
        StringBuilder sb = new StringBuilder();
        sb.append("MATCHES (").append(matches.size()).append(")\n");
        for (Match m : matches) {
            sb.append(String.format("  %-16s %s %12s  %s%n", m.rule(), m.ours().date(), m.ours().amount(),
                    m.ours().reference().equals(m.theirs().reference()) ? m.ours().reference()
                            : m.ours().reference() + " <-> " + m.theirs().reference()));
        }
        sb.append("BREAKS (").append(breaks.size()).append(")\n");
        for (Break b : breaks) {
            sb.append(String.format("  %-16s %s ours=%12s theirs=%12s diff=%12s  %s%n", b.kind(), b.date(),
                    b.oursAmount(currency), b.theirsAmount(currency), b.difference(currency), b.reference()));
        }
        sb.append("TOTALS\n");
        sb.append(String.format("  ledger total      %14s%n", ledgerTotal));
        sb.append(String.format("  statement total   %14s%n", statementTotal));
        sb.append(String.format("  matched           %14s%n", matchedTotal()));
        sb.append(String.format("  ours unmatched    %14s%n", oursUnmatched()));
        sb.append(String.format("  theirs unmatched  %14s%n", theirsUnmatched()));
        sb.append(String.format("  net difference    %14s  (ours unmatched - theirs unmatched = %s)%n",
                netDifference(), oursUnmatched().minus(theirsUnmatched())));
        sb.append("  ties out          ").append(tiesOut() ? "YES" : "NO").append('\n');
        return sb.toString();
    }
}

/**
 * Three-pass matcher. Pass 1 pairs by reference id (and finds amount mismatches and duplicates).
 * Pass 2 pairs the leftovers by equal amount and a date within toleranceDays, nearest date first.
 * Pass 3 labels whatever is still unmatched as missing on one side. Every step is deterministic:
 * inputs are sorted by (date, reference, amount, input position) before any decision is made.
 */
final class Reconciler {
    static final Comparator<LedgerPosting> OURS_ORDER = Comparator
            .comparing(LedgerPosting::date)
            .thenComparing(LedgerPosting::reference)
            .thenComparingLong(p -> p.amount().minorUnits());
    static final Comparator<StatementLine> THEIRS_ORDER = Comparator
            .comparing(StatementLine::date)
            .thenComparing(StatementLine::reference)
            .thenComparingLong(l -> l.amount().minorUnits());

    private final Currency currency;
    private final int toleranceDays;

    Reconciler(Currency currency, int toleranceDays) {
        if (toleranceDays < 0) throw new IllegalArgumentException("toleranceDays must be >= 0");
        this.currency = Objects.requireNonNull(currency);
        this.toleranceDays = toleranceDays;
    }

    ReconciliationReport reconcile(List<LedgerPosting> ledger, List<StatementLine> statement) {
        List<LedgerPosting> ours = new ArrayList<>(ledger);
        List<StatementLine> theirs = new ArrayList<>(statement);
        Money ledgerTotal = Money.zero(currency);
        for (LedgerPosting p : ours) ledgerTotal = ledgerTotal.plus(p.amount());      // throws on a foreign currency
        Money statementTotal = Money.zero(currency);
        for (StatementLine l : theirs) statementTotal = statementTotal.plus(l.amount());
        Collections.sort(ours, OURS_ORDER);      // stable: equal keys keep input order
        Collections.sort(theirs, THEIRS_ORDER);

        List<Match> matches = new ArrayList<>();
        List<Break> breaks = new ArrayList<>();
        List<LedgerPosting> oursLeft = new ArrayList<>();
        List<StatementLine> theirsLeft = new ArrayList<>();

        matchByReference(ours, theirs, matches, breaks, oursLeft, theirsLeft);
        matchByAmountAndDate(oursLeft, theirsLeft, matches);
        for (LedgerPosting p : oursLeft) breaks.add(new Break(BreakKind.MISSING_THEIRS, p, null));
        for (StatementLine l : theirsLeft) breaks.add(new Break(BreakKind.MISSING_OURS, null, l));

        matches.sort(Comparator.comparing((Match m) -> m.ours().date())
                .thenComparing(m -> m.ours().reference())
                .thenComparingLong(m -> m.ours().amount().minorUnits()));
        breaks.sort(Comparator.comparing(Break::kind)
                .thenComparing(Break::date)
                .thenComparing(Break::reference));
        ReconciliationReport report = new ReconciliationReport(currency, matches, breaks, ledgerTotal, statementTotal);
        if (!report.tiesOut()) throw new IllegalStateException("reconciliation report does not tie out; this is a bug in the matcher");
        return report;
    }

    /** Pass 1: group both sides by reference. TreeMap keeps reference order deterministic. */
    private void matchByReference(List<LedgerPosting> ours, List<StatementLine> theirs,
                                  List<Match> matches, List<Break> breaks,
                                  List<LedgerPosting> oursLeft, List<StatementLine> theirsLeft) {
        Map<String, List<LedgerPosting>> oursByRef = new TreeMap<>();
        Map<String, List<StatementLine>> theirsByRef = new TreeMap<>();
        for (LedgerPosting p : ours) oursByRef.computeIfAbsent(p.reference(), k -> new ArrayList<>()).add(p);
        for (StatementLine l : theirs) theirsByRef.computeIfAbsent(l.reference(), k -> new ArrayList<>()).add(l);

        for (Map.Entry<String, List<LedgerPosting>> e : oursByRef.entrySet()) {
            List<LedgerPosting> o = e.getValue();
            List<StatementLine> t = theirsByRef.getOrDefault(e.getKey(), List.of());
            if (t.isEmpty()) { oursLeft.addAll(o); continue; }          // nothing to pair with: try pass 2
            int pairs = Math.min(o.size(), t.size());
            for (int i = 0; i < pairs; i++) {
                if (o.get(i).amount().equals(t.get(i).amount())) matches.add(new Match(o.get(i), t.get(i), MatchRule.EXACT_REFERENCE));
                else breaks.add(new Break(BreakKind.AMOUNT_MISMATCH, o.get(i), t.get(i)));
            }
            for (int i = pairs; i < o.size(); i++) breaks.add(new Break(BreakKind.DUPLICATE_OURS, o.get(i), null));
            for (int i = pairs; i < t.size(); i++) breaks.add(new Break(BreakKind.DUPLICATE_THEIRS, null, t.get(i)));
        }
        for (Map.Entry<String, List<StatementLine>> e : theirsByRef.entrySet()) {
            if (!oursByRef.containsKey(e.getKey())) theirsLeft.addAll(e.getValue());
        }
        oursLeft.sort(OURS_ORDER);
        theirsLeft.sort(THEIRS_ORDER);
    }

    /** Pass 2: greedy nearest-date match on equal amounts. Removes matched items from both leftover lists. */
    private void matchByAmountAndDate(List<LedgerPosting> oursLeft, List<StatementLine> theirsLeft, List<Match> matches) {
        List<LedgerPosting> stillOurs = new ArrayList<>();
        for (LedgerPosting p : oursLeft) {
            int best = -1;
            long bestDistance = Long.MAX_VALUE;
            for (int j = 0; j < theirsLeft.size(); j++) {
                StatementLine l = theirsLeft.get(j);
                if (!l.amount().equals(p.amount())) continue;
                long distance = Math.abs(ChronoUnit.DAYS.between(p.date(), l.date()));
                if (distance > toleranceDays) continue;
                if (distance < bestDistance) { best = j; bestDistance = distance; }   // strict <: first in sorted order wins ties
            }
            if (best >= 0) matches.add(new Match(p, theirsLeft.remove(best), MatchRule.AMOUNT_DATE));
            else stillOurs.add(p);
        }
        oursLeft.clear();
        oursLeft.addAll(stillOurs);
    }
}

public final class Main {
    static LedgerPosting ours(String ref, String date, String amount) {
        return new LedgerPosting(ref, LocalDate.parse(date), Money.parse(amount, Currency.USD), "1010 Settlement Bank");
    }

    static StatementLine theirs(String ref, String date, String amount) {
        return new StatementLine(ref, LocalDate.parse(date), Money.parse(amount, Currency.USD), "CARD SETTLEMENT");
    }

    public static void main(String[] args) {
        List<LedgerPosting> ledger = List.of(
                ours("ch_1001", "2026-03-02", "19.99"),
                ours("ch_1002", "2026-03-02", "250.00"),
                ours("ch_1003", "2026-03-02", "7.50"),
                ours("ch_1004", "2026-03-03", "120.00"),
                ours("ch_1005", "2026-03-03", "49.95"),
                ours("ch_1006", "2026-03-03", "1500.00"),
                ours("ch_1007", "2026-03-04", "33.10"),
                ours("int_8841", "2026-03-04", "64.00"),     // our reference; processor used its own id
                ours("ch_1009", "2026-03-04", "88.88"),      // we have it, they do not
                ours("ch_1010", "2026-03-05", "12.00"));     // amount disagrees with theirs
        List<StatementLine> statement = List.of(
                theirs("ch_1001", "2026-03-02", "19.99"),
                theirs("ch_1002", "2026-03-02", "250.00"),
                theirs("ch_1003", "2026-03-02", "7.50"),
                theirs("ch_1004", "2026-03-03", "120.00"),
                theirs("ch_1005", "2026-03-03", "49.95"),
                theirs("ch_1006", "2026-03-03", "1500.00"),
                theirs("ch_1007", "2026-03-04", "33.10"),
                theirs("ch_1007", "2026-03-04", "33.10"),    // they posted it twice
                theirs("txn_77f2", "2026-03-05", "64.00"),   // their id for our int_8841, settled a day later
                theirs("ch_1010", "2026-03-05", "21.00"),    // we say 12.00
                theirs("ch_1011", "2026-03-05", "5.00"));    // they have it, we do not

        Reconciler reconciler = new Reconciler(Currency.USD, 1);
        ReconciliationReport report = reconciler.reconcile(ledger, statement);

        System.out.println("== Reconciling 10 ledger postings against 11 statement lines (tolerance 1 day) ==");
        System.out.print(report.render());
        System.out.println();
        System.out.println("== Reading the breaks ==");
        System.out.println("MISSING_OURS ch_1011 5.00: they settled something we never recorded; look for a lost webhook.");
        System.out.println("MISSING_THEIRS ch_1009 88.88: we recorded a settlement they never sent; still in transit or declined.");
        System.out.println("AMOUNT_MISMATCH ch_1010 12.00 vs 21.00: a transposed digit on one side; the diff of -9.00 is the classic sign.");
        System.out.println("DUPLICATE_THEIRS ch_1007: their file double-posted; dispute it, do not book it.");
        System.out.println("net difference " + report.netDifference() + " = " + report.oursUnmatched() + " - " + report.theirsUnmatched());
    }
}
