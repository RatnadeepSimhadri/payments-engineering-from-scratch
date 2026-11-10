// Lesson: phases/10-money-and-ledgers/03-double-entry-ledger/docs/en.md
// The Double-Entry Ledger from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Pacioli, Summa de arithmetica (1494) for the debit=credit rule; ISO 4217 for currency exponents;
// Money type copied from Phase 10, Lesson 01 (lessons are self-contained). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

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

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

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
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isPositive() { return minorUnits > 0; }

    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twiceRemainder = Math.abs(remainder) * 2;
        long absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twiceRemainder >= absDivisor ? quotient + sign : quotient;
            case HALF_EVEN -> {
                if (twiceRemainder > absDivisor) yield quotient + sign;
                if (twiceRemainder < absDivisor) yield quotient;
                yield (quotient % 2 == 0) ? quotient : quotient + sign;
            }
        };
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = result + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// The ledger.
// ---------------------------------------------------------------------------------------------

/** Which side of an entry a posting sits on. A posting is never signed; it is a positive amount on a side. */
enum Side {
    DEBIT, CREDIT;

    Side opposite() { return this == DEBIT ? CREDIT : DEBIT; }
}

/** The five account types and the side on which each one normally carries its balance. */
enum AccountType {
    ASSET(Side.DEBIT), EXPENSE(Side.DEBIT), LIABILITY(Side.CREDIT), EQUITY(Side.CREDIT), REVENUE(Side.CREDIT);

    private final Side normalBalance;
    AccountType(Side normalBalance) { this.normalBalance = normalBalance; }

    /** The side that increases this account. A debit increases an asset; a credit increases a liability. */
    Side normalBalance() { return normalBalance; }
}

record Account(String id, String name, AccountType type, Currency currency) {
    Account {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("account id required");
        Objects.requireNonNull(type, "type");
        Objects.requireNonNull(currency, "currency");
    }
}

/** One line of a journal entry: a positive amount of one currency on one side of one account. */
record Posting(String accountId, Side side, Money amount) {
    Posting {
        Objects.requireNonNull(accountId, "accountId");
        Objects.requireNonNull(side, "side");
        if (!amount.isPositive()) throw new InvalidPostingException("posting amount must be positive, got " + amount);
    }

    static Posting debit(String accountId, Money amount) { return new Posting(accountId, Side.DEBIT, amount); }
    static Posting credit(String accountId, Money amount) { return new Posting(accountId, Side.CREDIT, amount); }

    Posting flipped() { return new Posting(accountId, side.opposite(), amount); }

    @Override public String toString() {
        return (side == Side.DEBIT ? "  Dr " : "      Cr ") + accountId + "  " + amount;
    }
}

/**
 * An immutable, balanced set of postings. Construction validates the balance rule, so a JournalEntry
 * that exists is balanced by definition: for every currency, sum(debits) == sum(credits).
 */
record JournalEntry(String id, String description, List<Posting> postings, String reversalOf) {
    JournalEntry {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("entry id required");
        Objects.requireNonNull(description, "description");
        if (postings == null || postings.size() < 2) {
            throw new UnbalancedEntryException(id, "an entry needs at least two postings");
        }
        postings = List.copyOf(postings);
        Map<Currency, Long> net = new LinkedHashMap<>();
        for (Posting p : postings) {
            long signed = p.side() == Side.DEBIT ? p.amount().minorUnits() : -p.amount().minorUnits();
            net.merge(p.amount().currency(), signed, Math::addExact);
        }
        StringBuilder problems = new StringBuilder();
        for (Map.Entry<Currency, Long> e : net.entrySet()) {
            if (e.getValue() != 0) {
                if (problems.length() > 0) problems.append("; ");
                problems.append("debits minus credits in ").append(e.getKey().code()).append(" = ")
                        .append(Money.ofMinor(e.getValue(), e.getKey()));
            }
        }
        if (problems.length() > 0) throw new UnbalancedEntryException(id, problems + " (must be zero per currency)");
    }

    JournalEntry(String id, String description, List<Posting> postings) { this(id, description, postings, null); }

    /** The entry that undoes this one: same postings with every side flipped. Nothing is ever edited. */
    JournalEntry reversal(String newId, String description) {
        List<Posting> flipped = new ArrayList<>(postings.size());
        for (Posting p : postings) flipped.add(p.flipped());
        return new JournalEntry(newId, description, flipped, id);
    }

    Money totalDebits(Currency currency) {
        Money acc = Money.zero(currency);
        for (Posting p : postings) if (p.side() == Side.DEBIT && p.amount().currency().equals(currency)) acc = acc.plus(p.amount());
        return acc;
    }
}

class LedgerException extends IllegalArgumentException {
    LedgerException(String message) { super(message); }
}

final class UnbalancedEntryException extends LedgerException {
    UnbalancedEntryException(String entryId, String detail) { super("entry " + entryId + " rejected: " + detail); }
}

final class InvalidPostingException extends LedgerException {
    InvalidPostingException(String detail) { super(detail); }
}

final class UnknownAccountException extends LedgerException {
    UnknownAccountException(String accountId) { super("unknown account: " + accountId); }
}

final class IdempotencyConflictException extends LedgerException {
    IdempotencyConflictException(String entryId) { super("entry id " + entryId + " already posted with different content"); }
}

final class AlreadyReversedException extends LedgerException {
    AlreadyReversedException(String entryId) { super("entry " + entryId + " has already been reversed"); }
}

/** One row of a trial balance: an account and its net balance placed in the debit or the credit column. */
record TrialBalanceRow(Account account, Money debit, Money credit) {}

/** The trial balance for one currency. Its two column totals are equal or the ledger is corrupt. */
record TrialBalance(Currency currency, List<TrialBalanceRow> rows, Money totalDebits, Money totalCredits) {
    boolean balances() { return totalDebits.equals(totalCredits); }
}

/**
 * An append-only journal plus a chart of accounts. Balances are derived from postings and cached as
 * two running totals per account (debits, credits), which the tests prove equal a fresh recomputation.
 */
final class Ledger {
    private final Map<String, Account> accounts = new LinkedHashMap<>();
    private final List<JournalEntry> journal = new ArrayList<>();
    private final Map<String, Integer> indexById = new HashMap<>();
    private final Map<String, String> reversedBy = new HashMap<>();
    private final Map<String, Long> debitTotals = new HashMap<>();
    private final Map<String, Long> creditTotals = new HashMap<>();

    Account openAccount(String id, String name, AccountType type, Currency currency) {
        Account account = new Account(id, name, type, currency);
        if (accounts.putIfAbsent(id, account) != null) throw new IllegalArgumentException("account exists: " + id);
        debitTotals.put(id, 0L);
        creditTotals.put(id, 0L);
        return account;
    }

    Account account(String id) {
        Account a = accounts.get(id);
        if (a == null) throw new UnknownAccountException(id);
        return a;
    }

    /**
     * Append an entry. Returns true if it was appended, false if an identical entry with the same id
     * was already in the journal (idempotent replay). A different entry under the same id is a conflict.
     * Validation happens entirely before the first mutation, so a rejected entry leaves no trace.
     */
    boolean post(JournalEntry entry) {
        Integer existing = indexById.get(entry.id());
        if (existing != null) {
            if (journal.get(existing).equals(entry)) return false;
            throw new IdempotencyConflictException(entry.id());
        }
        for (Posting p : entry.postings()) {
            Account account = account(p.accountId());
            if (!account.currency().equals(p.amount().currency())) {
                throw new InvalidPostingException("entry " + entry.id() + " rejected: account " + account.id() + " is "
                        + account.currency().code() + " but posting is " + p.amount().currency().code());
            }
        }
        if (entry.reversalOf() != null) {
            if (!indexById.containsKey(entry.reversalOf())) throw new UnknownEntryException(entry.reversalOf());
            if (reversedBy.containsKey(entry.reversalOf())) throw new AlreadyReversedException(entry.reversalOf());
        }
        // Validation complete: now mutate, and mutate everything.
        journal.add(entry);
        indexById.put(entry.id(), journal.size() - 1);
        if (entry.reversalOf() != null) reversedBy.put(entry.reversalOf(), entry.id());
        for (Posting p : entry.postings()) {
            Map<String, Long> totals = p.side() == Side.DEBIT ? debitTotals : creditTotals;
            totals.merge(p.accountId(), p.amount().minorUnits(), Math::addExact);
        }
        return true;
    }

    /** Post the reversal of an existing entry. The original stays in the journal forever. */
    JournalEntry reverse(String entryId, String newEntryId, String description) {
        JournalEntry original = entry(entryId);
        JournalEntry reversal = original.reversal(newEntryId, description);
        post(reversal);
        return reversal;
    }

    JournalEntry entry(String id) {
        Integer index = indexById.get(id);
        if (index == null) throw new UnknownEntryException(id);
        return journal.get(index);
    }

    List<JournalEntry> entries() { return Collections.unmodifiableList(journal); }

    int size() { return journal.size(); }

    /** Balance in the account's normal direction: positive means "what you expect" for that type. */
    Money balance(String accountId) {
        Account account = account(accountId);
        long debits = debitTotals.get(accountId), credits = creditTotals.get(accountId);
        long signed = account.type().normalBalance() == Side.DEBIT
                ? Math.subtractExact(debits, credits)
                : Math.subtractExact(credits, debits);
        return Money.ofMinor(signed, account.currency());
    }

    /** Debits minus credits, regardless of account type. Sums to zero across all accounts of a currency. */
    Money netDebit(String accountId) {
        Account account = account(accountId);
        return Money.ofMinor(Math.subtractExact(debitTotals.get(accountId), creditTotals.get(accountId)), account.currency());
    }

    /** Recompute an account's net debit by scanning every posting. The cache must always agree with this. */
    Money netDebitFromPostings(String accountId) {
        Account account = account(accountId);
        long acc = 0;
        for (JournalEntry e : journal) {
            for (Posting p : e.postings()) {
                if (!p.accountId().equals(accountId)) continue;
                acc = Math.addExact(acc, p.side() == Side.DEBIT ? p.amount().minorUnits() : -p.amount().minorUnits());
            }
        }
        return Money.ofMinor(acc, account.currency());
    }

    TrialBalance trialBalance(Currency currency) {
        List<TrialBalanceRow> rows = new ArrayList<>();
        Money totalDebits = Money.zero(currency), totalCredits = Money.zero(currency);
        for (Account a : accounts.values()) {
            if (!a.currency().equals(currency)) continue;
            Money net = netDebit(a.id());
            Money debit = net.isNegative() ? Money.zero(currency) : net;
            Money credit = net.isNegative() ? net.negate() : Money.zero(currency);
            rows.add(new TrialBalanceRow(a, debit, credit));
            totalDebits = totalDebits.plus(debit);
            totalCredits = totalCredits.plus(credit);
        }
        return new TrialBalance(currency, rows, totalDebits, totalCredits);
    }
}

final class UnknownEntryException extends LedgerException {
    UnknownEntryException(String entryId) { super("unknown entry: " + entryId); }
}

/** Deterministic generator of balanced entries for stress tests: seeded Random, never the wall clock. */
final class RandomEntries {
    private final Random random;
    private final List<String> accountIds;
    private final Currency currency;
    private int counter = 0;

    RandomEntries(long seed, List<String> accountIds, Currency currency) {
        this.random = new Random(seed);
        this.accountIds = accountIds;
        this.currency = currency;
    }

    JournalEntry next() {
        int legs = 2 + random.nextInt(3);                  // 2..4 postings
        List<Posting> postings = new ArrayList<>();
        long debitSum = 0;
        for (int i = 0; i < legs - 1; i++) {
            long amount = 1 + random.nextInt(100_000);     // 0.01 .. 1000.00
            postings.add(Posting.debit(accountIds.get(random.nextInt(accountIds.size())), Money.ofMinor(amount, currency)));
            debitSum += amount;
        }
        postings.add(Posting.credit(accountIds.get(random.nextInt(accountIds.size())), Money.ofMinor(debitSum, currency)));
        return new JournalEntry("rnd-" + (counter++), "random balanced entry", postings);
    }
}

public final class Main {
    static void printEntry(JournalEntry e) {
        System.out.println(e.id() + "  " + e.description() + (e.reversalOf() == null ? "" : "  (reverses " + e.reversalOf() + ")"));
        for (Posting p : e.postings()) System.out.println(p);
    }

    static void printTrialBalance(Ledger ledger, Currency currency) {
        TrialBalance tb = ledger.trialBalance(currency);
        System.out.printf("%-28s %14s %14s%n", "account", "debit", "credit");
        for (TrialBalanceRow row : tb.rows()) {
            System.out.printf("%-28s %14s %14s%n", row.account().id() + " " + row.account().name(),
                    row.debit().isZero() ? "" : row.debit(), row.credit().isZero() ? "" : row.credit());
        }
        System.out.printf("%-28s %14s %14s   balances=%s%n", "TOTAL", tb.totalDebits(), tb.totalCredits(), tb.balances());
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;
        Ledger ledger = new Ledger();
        ledger.openAccount("1010", "Settlement Bank", AccountType.ASSET, USD);
        ledger.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        ledger.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, USD);
        ledger.openAccount("4100", "Fee Revenue", AccountType.REVENUE, USD);

        System.out.println("== A customer pays a merchant 100.00 USD; we keep a 2.9% fee ==");
        Money gross = Money.parse("100.00", USD);
        Money fee = gross.times(29, 1000, Rounding.HALF_EVEN);
        Money net = gross.minus(fee);
        JournalEntry sale = new JournalEntry("pay-001", "card payment captured", List.of(
                Posting.debit("1200", gross),
                Posting.credit("2100", net),
                Posting.credit("4100", fee)));
        System.out.println("posted=" + ledger.post(sale));
        printEntry(sale);
        System.out.println("replay posted=" + ledger.post(sale) + "  (same id, same content: no-op)");

        JournalEntry settle = new JournalEntry("stl-001", "processor settles to our bank", List.of(
                Posting.debit("1010", gross), Posting.credit("1200", gross)));
        ledger.post(settle);
        printEntry(settle);
        JournalEntry payout = new JournalEntry("out-001", "payout to merchant", List.of(
                Posting.debit("2100", net), Posting.credit("1010", net)));
        ledger.post(payout);
        printEntry(payout);

        System.out.println("\n== Balances (in each account's normal direction) ==");
        for (String id : List.of("1010", "1200", "2100", "4100")) {
            Account a = ledger.account(id);
            System.out.printf("%s %-22s %-9s %s%n", id, a.name(), a.type(), ledger.balance(id));
        }
        System.out.println("\n== Trial balance ==");
        printTrialBalance(ledger, USD);

        System.out.println("\n== Entries that cannot exist ==");
        try {
            new JournalEntry("bad-1", "unbalanced", List.of(Posting.debit("1200", gross), Posting.credit("2100", net)));
        } catch (UnbalancedEntryException e) { System.out.println("rejected: " + e.getMessage()); }
        try {
            new JournalEntry("bad-2", "mixed currency", List.of(
                    Posting.debit("1200", gross), Posting.credit("2100", Money.parse("100.00", Currency.EUR))));
        } catch (UnbalancedEntryException e) { System.out.println("rejected: " + e.getMessage()); }
        try {
            ledger.post(new JournalEntry("pay-001", "same id, different content", List.of(
                    Posting.debit("1200", net), Posting.credit("2100", net))));
        } catch (IdempotencyConflictException e) { System.out.println("rejected: " + e.getMessage()); }

        System.out.println("\n== Reversal: the capture is voided; nothing is edited ==");
        Money before = ledger.balance("4100");
        JournalEntry reversal = ledger.reverse("pay-001", "rev-001", "reversal: void pay-001");
        printEntry(reversal);
        System.out.println("fee revenue before reversal " + before + ", after " + ledger.balance("4100"));
        System.out.println("journal now holds " + ledger.size() + " entries; pay-001 is still entry #1");

        System.out.println("\n== 10,000 seeded random balanced entries: trial balance still sums to zero ==");
        RandomEntries gen = new RandomEntries(42, List.of("1010", "1200", "2100", "4100"), USD);
        for (int i = 0; i < 10_000; i++) ledger.post(gen.next());
        TrialBalance tb = ledger.trialBalance(USD);
        System.out.println("entries=" + ledger.size() + " totalDebits=" + tb.totalDebits() + " totalCredits=" + tb.totalCredits()
                + " balances=" + tb.balances());
    }
}
