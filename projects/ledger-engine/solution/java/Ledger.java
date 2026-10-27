// Project: projects/ledger-engine — the ledger: append-only journal, derived balances, reconciliation.
// Stage 2: Ledger.post.  Stage 3: balance, balanceAt, snapshots, trialBalance, reverse.  Stage 4: lines, reconcile.
// Docs: projects/ledger-engine/stages/*/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Test: python3 scripts/project_test.py ledger-engine --lang java --all --solution
// Java 21, standard library only. Java first; Python port in solution/python; Go port follows later.

import java.time.LocalDate;
import java.time.format.DateTimeParseException;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/** Signed balances of every account after the entry with this sequence number was applied. */
record BalanceSnapshot(long sequence, Map<String, Money> balances) {
    BalanceSnapshot {
        balances = Map.copyOf(balances);
    }
}

/** One row of a trial balance: the account's net balance placed in the debit or the credit column. */
record TrialBalanceLine(Account account, Money debit, Money credit) {}

/** All accounts of one currency with their column totals. The two totals agree, or an entry got in unbalanced. */
record TrialBalance(Currency currency, List<TrialBalanceLine> lines, Money totalDebits, Money totalCredits) {
    TrialBalance {
        lines = List.copyOf(lines);
    }

    Money difference() { return totalDebits.minus(totalCredits); }

    boolean isBalanced() { return difference().isZero(); }
}

/** An in-memory double-entry ledger: a chart of accounts and an append-only journal. Not thread-safe. */
final class Ledger {
    static final int DEFAULT_SNAPSHOT_EVERY = 100;

    private final AccountRegistry accounts;
    private final int snapshotEvery;
    private final List<PostedEntry> journal = new ArrayList<>();
    private final Map<String, PostedEntry> byId = new HashMap<>();
    private final List<BalanceSnapshot> snapshots = new ArrayList<>();

    Ledger(AccountRegistry accounts) { this(accounts, DEFAULT_SNAPSHOT_EVERY); }

    /** {@code snapshotEvery} is K: a running-balance snapshot is taken after every K-th posted entry. */
    Ledger(AccountRegistry accounts, int snapshotEvery) {
        if (accounts == null) throw new IllegalArgumentException("ledger needs an account registry");
        if (snapshotEvery < 1) throw new IllegalArgumentException("snapshotEvery must be >= 1");
        this.accounts = accounts;
        this.snapshotEvery = snapshotEvery;
    }

    AccountRegistry accounts() { return accounts; }

    int snapshotEvery() { return snapshotEvery; }

    // ---------------------------------------------------------------- stage 2: posting

    /**
     * Append an entry to the journal and give it the next sequence number. Posting an id that is already
     * in the journal is a no-op that returns the record already there. Every rule is checked before
     * anything is written, so a rejected entry leaves the journal exactly as it was.
     */
    PostedEntry post(JournalEntry entry) {
        PostedEntry existing = byId.get(entry.id());
        if (existing != null) return existing;
        validate(entry);
        PostedEntry posted = new PostedEntry(journal.size() + 1, entry);
        journal.add(posted);
        byId.put(entry.id(), posted);
        if (posted.sequence() % snapshotEvery == 0) {
            snapshots.add(new BalanceSnapshot(posted.sequence(), balancesAt(posted.sequence())));
        }
        return posted;
    }

    /** Order of checks: posting count, unknown account, mixed currencies, account currency, balance. */
    private void validate(JournalEntry entry) {
        if (entry.postings().size() < 2) {
            throw new InvalidEntryException(entry.id(), "needs at least two postings, got " + entry.postings().size());
        }
        Currency currency = entry.currency();
        Money debits = Money.ofMinor(0, currency);
        Money credits = Money.ofMinor(0, currency);
        for (Posting posting : entry.postings()) {
            Account account = accounts.get(posting.accountId());
            if (!posting.amount().currency().equals(currency)) {
                throw new CurrencyMismatchException(currency, posting.amount().currency());
            }
            if (!account.currency().equals(currency)) {
                throw new CurrencyMismatchException(account.currency(), currency);
            }
            if (posting.side() == Side.DEBIT) debits = debits.plus(posting.amount());
            else credits = credits.plus(posting.amount());
        }
        if (!debits.equals(credits)) throw new UnbalancedEntryException(entry.id(), debits, credits);
    }

    /** The journal in sequence order. A copy: entries are never edited or removed. */
    List<PostedEntry> journal() { return List.copyOf(journal); }

    PostedEntry entry(String id) {
        PostedEntry posted = byId.get(id);
        if (posted == null) throw new UnknownEntryException(id);
        return posted;
    }

    boolean hasEntry(String id) { return byId.containsKey(id); }

    int size() { return journal.size(); }

    // ---------------------------------------------------------------- stage 3: balances, trial balance, reversals

    /** Signed effect of one posting on its account: positive on the account's normal side. */
    private Money signed(Posting posting, Account account) {
        return posting.side() == account.normalSide() ? posting.amount() : posting.amount().negate();
    }

    /** Derived, never stored directly: the latest snapshot plus a replay of everything after it. */
    Money balance(String accountId) {
        Account account = accounts.get(accountId);
        Money zero = Money.ofMinor(0, account.currency());
        BalanceSnapshot base = snapshots.isEmpty() ? null : snapshots.get(snapshots.size() - 1);
        Money balance = base == null ? zero : base.balances().getOrDefault(accountId, zero);
        long from = base == null ? 0 : base.sequence();
        for (int i = (int) from; i < journal.size(); i++) {
            for (Posting posting : journal.get(i).entry().postings()) {
                if (posting.accountId().equals(accountId)) balance = balance.plus(signed(posting, account));
            }
        }
        return balance;
    }

    /** Full recomputation from the start of the journal through {@code sequence} inclusive. Ignores snapshots. */
    Money balanceAt(String accountId, long sequence) {
        Account account = accounts.get(accountId);
        Money balance = Money.ofMinor(0, account.currency());
        for (PostedEntry posted : journal) {
            if (posted.sequence() > sequence) break;
            for (Posting posting : posted.entry().postings()) {
                if (posting.accountId().equals(accountId)) balance = balance.plus(signed(posting, account));
            }
        }
        return balance;
    }

    /** Signed balance of every known account after {@code sequence}, by full replay. */
    private Map<String, Money> balancesAt(long sequence) {
        Map<String, Money> balances = new LinkedHashMap<>();
        for (Account account : accounts.all()) balances.put(account.id(), balanceAt(account.id(), sequence));
        return balances;
    }

    List<BalanceSnapshot> snapshots() { return List.copyOf(snapshots); }

    /**
     * Per currency: each account's net (debits minus credits) goes in the debit column if positive, the
     * credit column if negative. Column totals must agree because every posted entry was balanced.
     */
    List<TrialBalance> trialBalance() {
        Map<String, Money> net = new LinkedHashMap<>();                 // account id -> debits minus credits
        for (Account account : accounts.all()) net.put(account.id(), Money.ofMinor(0, account.currency()));
        for (PostedEntry posted : journal) {
            for (Posting posting : posted.entry().postings()) {
                Money current = net.get(posting.accountId());
                net.put(posting.accountId(), posting.side() == Side.DEBIT
                        ? current.plus(posting.amount()) : current.minus(posting.amount()));
            }
        }
        Map<String, List<TrialBalanceLine>> lines = new TreeMap<>();   // currency code -> lines
        Map<String, Currency> currencies = new HashMap<>();
        for (Account account : accounts.all()) {
            Money balance = net.get(account.id());
            Money zero = Money.ofMinor(0, account.currency());
            TrialBalanceLine line = balance.isNegative()
                    ? new TrialBalanceLine(account, zero, balance.negate())
                    : new TrialBalanceLine(account, balance, zero);
            lines.computeIfAbsent(account.currency().code(), k -> new ArrayList<>()).add(line);
            currencies.put(account.currency().code(), account.currency());
        }
        List<TrialBalance> result = new ArrayList<>();
        for (Map.Entry<String, List<TrialBalanceLine>> group : lines.entrySet()) {
            Currency currency = currencies.get(group.getKey());
            Money debits = Money.ofMinor(0, currency);
            Money credits = Money.ofMinor(0, currency);
            for (TrialBalanceLine line : group.getValue()) {
                debits = debits.plus(line.debit());
                credits = credits.plus(line.credit());
            }
            result.add(new TrialBalance(currency, group.getValue(), debits, credits));
        }
        return result;
    }

    /** The id a reversal of {@code entryId} always gets, so reversing twice finds the first reversal. */
    static String reversalId(String entryId) { return "rev:" + entryId; }

    /**
     * Post the mirror of an entry: same postings, every side flipped, same date as the original, id
     * {@code rev:<entryId>}. The original stays in the journal untouched. Reversing an already-reversed
     * entry returns the existing reversal (post() de-duplicates on id), so the operation is idempotent.
     */
    PostedEntry reverse(String entryId) {
        PostedEntry original = entry(entryId);
        List<Posting> mirrored = new ArrayList<>();
        for (Posting posting : original.entry().postings()) mirrored.add(posting.mirrored());
        return post(new JournalEntry(reversalId(entryId), original.entry().date(), mirrored));
    }

    boolean isReversed(String entryId) { return byId.containsKey(reversalId(entryId)); }

    // ---------------------------------------------------------------- stage 4: reconciliation

    /**
     * Our side of a statement: one line per journal entry that touches the account, in journal order,
     * with the entry's net effect on the account in the account's normal direction.
     */
    List<LedgerLine> lines(String accountId) {
        Account account = accounts.get(accountId);
        List<LedgerLine> out = new ArrayList<>();
        for (PostedEntry posted : journal) {
            Money net = Money.ofMinor(0, account.currency());
            boolean touches = false;
            for (Posting posting : posted.entry().postings()) {
                if (!posting.accountId().equals(accountId)) continue;
                touches = true;
                net = net.plus(signed(posting, account));
            }
            if (touches) out.add(new LedgerLine(posted.id(), net, posted.entry().date()));
        }
        return out;
    }

    static LocalDate parseDate(String text) {
        try {
            return LocalDate.parse(text);
        } catch (DateTimeParseException e) {
            throw new LedgerException("not an ISO-8601 date: " + text);
        }
    }

    static boolean withinWindow(String a, String b, int windowDays) {
        return Math.abs(ChronoUnit.DAYS.between(parseDate(a), parseDate(b))) <= windowDays;
    }

    /**
     * Match the account's journal lines against an external statement. Pass 1 matches by reference
     * (equal amounts match; unequal amounts are AMOUNT_MISMATCH; a repeated statement reference is a
     * DUPLICATE). Pass 2 matches what is left by equal amount within {@code windowDays} of each other,
     * first unmatched ledger line in journal order wins. Whatever remains is missing on one side.
     * The report's totals tie out by construction: every line lands in exactly one match or one break.
     */
    ReconciliationReport reconcile(String accountId, List<StatementLine> statement, int windowDays) {
        Account account = accounts.get(accountId);
        if (windowDays < 0) throw new IllegalArgumentException("windowDays must be >= 0");
        Currency currency = account.currency();
        Money zero = Money.ofMinor(0, currency);

        List<LedgerLine> ledgerLines = lines(accountId);
        Map<String, LedgerLine> ledgerByRef = new LinkedHashMap<>();
        Money ledgerTotal = zero;
        for (LedgerLine line : ledgerLines) {
            ledgerByRef.put(line.ref(), line);
            ledgerTotal = ledgerTotal.plus(line.amount());
        }
        Money statementTotal = zero;
        for (StatementLine line : statement) {
            if (!line.amount().currency().equals(currency)) throw new CurrencyMismatchException(currency, line.amount().currency());
            parseDate(line.date());
            statementTotal = statementTotal.plus(line.amount());
        }

        List<ReconciliationMatch> matches = new ArrayList<>();
        List<ReconciliationBreak> breaks = new ArrayList<>();
        Set<String> consumedLedger = new HashSet<>();
        Set<String> seenStatementRefs = new HashSet<>();
        List<StatementLine> unmatchedStatement = new ArrayList<>();

        // pass 1: by reference
        for (StatementLine line : statement) {
            if (!seenStatementRefs.add(line.ref())) {
                breaks.add(new ReconciliationBreak(BreakType.DUPLICATE, line.ref(), zero, line.amount()));
                continue;
            }
            LedgerLine ours = ledgerByRef.get(line.ref());
            if (ours == null) {
                unmatchedStatement.add(line);
            } else if (ours.amount().equals(line.amount())) {
                matches.add(new ReconciliationMatch(ours.ref(), line.ref(), line.amount(), MatchedBy.REF));
                consumedLedger.add(ours.ref());
            } else {
                breaks.add(new ReconciliationBreak(BreakType.AMOUNT_MISMATCH, line.ref(), ours.amount(), line.amount()));
                consumedLedger.add(ours.ref());
            }
        }

        // pass 2: by amount within the date window
        for (StatementLine line : unmatchedStatement) {
            LedgerLine found = null;
            for (LedgerLine ours : ledgerLines) {
                if (consumedLedger.contains(ours.ref())) continue;
                if (ours.amount().equals(line.amount()) && withinWindow(ours.date(), line.date(), windowDays)) {
                    found = ours;
                    break;
                }
            }
            if (found == null) {
                breaks.add(new ReconciliationBreak(BreakType.MISSING_IN_LEDGER, line.ref(), zero, line.amount()));
            } else {
                matches.add(new ReconciliationMatch(found.ref(), line.ref(), line.amount(), MatchedBy.AMOUNT_AND_DATE));
                consumedLedger.add(found.ref());
            }
        }

        // whatever the ledger still holds, the statement never showed
        for (LedgerLine ours : ledgerLines) {
            if (!consumedLedger.contains(ours.ref())) {
                breaks.add(new ReconciliationBreak(BreakType.MISSING_IN_STATEMENT, ours.ref(), ours.amount(), zero));
            }
        }
        return new ReconciliationReport(accountId, currency, matches, breaks, ledgerTotal, statementTotal);
    }
}
