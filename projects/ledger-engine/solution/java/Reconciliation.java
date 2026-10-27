// Project: projects/ledger-engine — stage 4: statement lines, breaks, and the reconciliation report.
// Docs: projects/ledger-engine/stages/04-reconcile-against-statement/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Test: python3 scripts/project_test.py ledger-engine --lang java --all --solution
// Java 21, standard library only. Java first; Python port in solution/python; Go port follows later.

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;

/**
 * One line of an external statement (a processor settlement file, a bank statement). The reference is
 * what we match on first; the amount is signed in the account's normal direction (money in is positive
 * for a bank asset account); the date is ISO-8601 ("2026-10-01").
 */
record StatementLine(String ref, Money amount, String date) {
    StatementLine {
        if (ref == null || ref.isBlank()) throw new IllegalArgumentException("statement line needs a reference");
        Objects.requireNonNull(amount, "amount");
        if (date == null || date.isBlank()) throw new IllegalArgumentException("statement line needs a date");
    }
}

/** Our side of the same shape: one journal entry's net effect on one account, keyed by the entry id. */
record LedgerLine(String ref, Money amount, String date) {}

/** The four ways a statement line and a journal entry can fail to agree. */
enum BreakType {
    MISSING_IN_LEDGER,    // the statement has it, our journal does not
    MISSING_IN_STATEMENT, // our journal has it, the statement does not
    AMOUNT_MISMATCH,      // same reference on both sides, different amounts
    DUPLICATE,            // the statement listed the same reference twice
}

/** How a match was found: by shared reference, or by equal amount within the date window. */
enum MatchedBy { REF, AMOUNT_AND_DATE }

record ReconciliationMatch(String ledgerRef, String statementRef, Money amount, MatchedBy by) {}

/** A break carries both sides; the absent side is the currency's zero, and the type says which side is absent. */
record ReconciliationBreak(BreakType type, String ref, Money ledgerAmount, Money statementAmount) {}

/**
 * Everything the reconciliation found, with totals that prove nothing was dropped:
 * ledgerTotal == matchedTotal + ledgerBreakTotal and statementTotal == matchedTotal + statementBreakTotal.
 */
record ReconciliationReport(String accountId, Currency currency, List<ReconciliationMatch> matches,
                            List<ReconciliationBreak> breaks, Money ledgerTotal, Money statementTotal) {
    ReconciliationReport {
        matches = List.copyOf(matches);
        breaks = List.copyOf(breaks);
    }

    Money matchedTotal() {
        Money acc = Money.ofMinor(0, currency);
        for (ReconciliationMatch m : matches) acc = acc.plus(m.amount());
        return acc;
    }

    /** The ledger-side amounts of every break (zero for breaks the ledger never saw). */
    Money ledgerBreakTotal() {
        Money acc = Money.ofMinor(0, currency);
        for (ReconciliationBreak b : breaks) acc = acc.plus(b.ledgerAmount());
        return acc;
    }

    /** The statement-side amounts of every break (zero for breaks the statement never saw). */
    Money statementBreakTotal() {
        Money acc = Money.ofMinor(0, currency);
        for (ReconciliationBreak b : breaks) acc = acc.plus(b.statementAmount());
        return acc;
    }

    List<ReconciliationBreak> breaksOf(BreakType type) {
        List<ReconciliationBreak> out = new ArrayList<>();
        for (ReconciliationBreak b : breaks) if (b.type() == type) out.add(b);
        return out;
    }

    /** True when every minor unit on both sides is accounted for by exactly one match or one break. */
    boolean tiesOut() {
        return ledgerTotal.equals(matchedTotal().plus(ledgerBreakTotal()))
                && statementTotal.equals(matchedTotal().plus(statementBreakTotal()));
    }

    boolean isClean() { return breaks.isEmpty(); }
}
