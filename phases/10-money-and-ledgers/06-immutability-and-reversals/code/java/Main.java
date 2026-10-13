// Lesson: phases/10-money-and-ledgers/06-immutability-and-reversals/docs/en.md
// Immutability, Reversals, and Corrections
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Pacioli (1494) on reversing entries; FIPS 180-4 (SHA-256) for the hash chain; Money from Lesson 01.
// Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
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
    Money abs() { return minorUnits < 0 ? negate() : this; }
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

    /** Largest-remainder split (Lesson 01): the parts always sum exactly to the whole; ties go to the lower index. */
    Money[] allocate(long... ratios) {
        if (ratios.length == 0) throw new IllegalArgumentException("need at least one ratio");
        long total = 0;
        for (long r : ratios) {
            if (r < 0) throw new IllegalArgumentException("ratios must be non-negative");
            total = Math.addExact(total, r);
        }
        if (total == 0) throw new IllegalArgumentException("ratios must not all be zero");
        long[] shares = new long[ratios.length];
        long[] remainders = new long[ratios.length];
        long distributed = 0;
        long magnitude = Math.abs(minorUnits);
        for (int i = 0; i < ratios.length; i++) {
            long scaled = Math.multiplyExact(magnitude, ratios[i]);
            shares[i] = scaled / total;
            remainders[i] = scaled % total;
            distributed += shares[i];
        }
        long leftover = magnitude - distributed;
        Integer[] order = new Integer[ratios.length];
        for (int i = 0; i < order.length; i++) order[i] = i;
        Arrays.sort(order, (a, b) -> Long.compare(remainders[b], remainders[a]));
        for (int k = 0; k < leftover; k++) shares[order[k]]++;
        Money[] result = new Money[ratios.length];
        for (int i = 0; i < ratios.length; i++) result[i] = new Money(minorUnits < 0 ? -shares[i] : shares[i], currency);
        return result;
    }

    static Money sum(Currency currency, Money... amounts) {
        Money acc = zero(currency);
        for (Money m : amounts) acc = acc.plus(m);
        return acc;
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
// Entries that can never change, and the three ways to undo one: full reversal, partial reversal,
// and correction (reverse + replace). Every undo is a new entry linked to the one it undoes.
// ---------------------------------------------------------------------------------------------

enum Side {
    DEBIT, CREDIT;

    Side opposite() { return this == DEBIT ? CREDIT : DEBIT; }
}

enum AccountType {
    ASSET(Side.DEBIT), EXPENSE(Side.DEBIT), LIABILITY(Side.CREDIT), EQUITY(Side.CREDIT), REVENUE(Side.CREDIT);

    private final Side normalBalance;
    AccountType(Side normalBalance) { this.normalBalance = normalBalance; }
    Side normalBalance() { return normalBalance; }
}

record Account(String id, String name, AccountType type, Currency currency) {}

record Posting(String accountId, Side side, Money amount) {
    Posting {
        Objects.requireNonNull(accountId, "accountId");
        Objects.requireNonNull(side, "side");
        if (!amount.isPositive()) throw new LedgerException("posting amount must be positive, got " + amount);
    }

    static Posting debit(String accountId, Money amount) { return new Posting(accountId, Side.DEBIT, amount); }
    static Posting credit(String accountId, Money amount) { return new Posting(accountId, Side.CREDIT, amount); }

    long signedMinor() { return side == Side.DEBIT ? amount.minorUnits() : -amount.minorUnits(); }

    String canonical() { return accountId + ":" + side + ":" + amount.minorUnits() + ":" + amount.currency().code(); }
}

/** ORIGINAL records a business event; REVERSAL undoes (part of) one; CORRECTION replaces one that was reversed. */
enum EntryKind { ORIGINAL, REVERSAL, CORRECTION }

/**
 * Immutable and balanced by construction. A REVERSAL or CORRECTION must name the entry it relates to and a reason:
 * an undo without a link or a why is indistinguishable from fraud in an audit.
 */
record JournalEntry(String id, String description, List<Posting> postings, EntryKind kind, String relatesTo, String reason) {
    JournalEntry {
        if (id == null || id.isBlank()) throw new LedgerException("entry id required");
        Objects.requireNonNull(description, "description");
        Objects.requireNonNull(kind, "kind");
        if (postings == null || postings.size() < 2) throw new LedgerException("entry " + id + " needs at least two postings");
        postings = List.copyOf(postings);
        if (kind == EntryKind.ORIGINAL && relatesTo != null) throw new LedgerException("an original entry relates to nothing");
        if (kind != EntryKind.ORIGINAL && (relatesTo == null || reason == null || reason.isBlank())) {
            throw new LedgerException(kind + " " + id + " must name the entry it relates to and a reason");
        }
        Map<Currency, Long> net = new LinkedHashMap<>();
        for (Posting p : postings) net.merge(p.amount().currency(), p.signedMinor(), Math::addExact);
        for (Map.Entry<Currency, Long> e : net.entrySet()) {
            if (e.getValue() != 0) {
                throw new LedgerException("entry " + id + " unbalanced: debits minus credits in " + e.getKey().code() + " = "
                        + Money.ofMinor(e.getValue(), e.getKey()));
            }
        }
    }

    static JournalEntry original(String id, String description, List<Posting> postings) {
        return new JournalEntry(id, description, postings, EntryKind.ORIGINAL, null, null);
    }

    Currency currency() { return postings.get(0).amount().currency(); }

    /** Sum of the debit legs: the "size" of the entry, used to bound partial reversals. */
    long debitTotal() {
        long acc = 0;
        for (Posting p : postings) if (p.side() == Side.DEBIT) acc = Math.addExact(acc, p.amount().minorUnits());
        return acc;
    }

    String canonical() {
        StringBuilder sb = new StringBuilder();
        sb.append(id).append('|').append(kind).append('|').append(relatesTo).append('|').append(reason).append('|').append(description);
        for (Posting p : postings) sb.append('|').append(p.canonical());
        return sb.toString();
    }
}

/** A journal slot: the entry, its position, and a SHA-256 link to the previous slot. Tampering breaks the chain. */
record JournalRecord(long seq, JournalEntry entry, String prevHash, String hash) {
    static final String GENESIS = "0".repeat(64);

    static String hashOf(long seq, JournalEntry entry, String prevHash) {
        try {
            MessageDigest sha = MessageDigest.getInstance("SHA-256");
            byte[] digest = sha.digest((prevHash + "|" + seq + "|" + entry.canonical()).getBytes(StandardCharsets.UTF_8));
            return HexFormat.of().formatHex(digest);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is required by every Java platform", e);
        }
    }
}

class LedgerException extends IllegalArgumentException {
    LedgerException(String message) { super(message); }
}

final class IdempotencyConflictException extends LedgerException {
    IdempotencyConflictException(String id) { super("entry id " + id + " already posted with different content"); }
}

final class AlreadyReversedException extends LedgerException {
    AlreadyReversedException(String id) { super("entry " + id + " is already fully reversed"); }
}

final class ExcessiveReversalException extends LedgerException {
    ExcessiveReversalException(String id, Money requested, Money remaining) {
        super("cannot reverse " + requested + " of " + id + ": only " + remaining + " remains");
    }
}

/**
 * Append-only journal. The public write methods are post (originals), reverse, reversePartially, and correct;
 * none of them modifies or removes an existing entry. Every write is idempotent on its entry id.
 */
final class Ledger {
    private final Map<String, Account> accounts = new LinkedHashMap<>();
    private final List<JournalRecord> journal = new ArrayList<>();
    private final Map<String, Integer> indexById = new HashMap<>();
    private final Map<String, Long> netDebits = new HashMap<>();
    /** For each reversible entry: how much of each posting (by index) has been reversed so far, in minor units. */
    private final Map<String, long[]> reversedSoFar = new HashMap<>();
    private final Map<String, List<String>> children = new HashMap<>();

    Account openAccount(String id, String name, AccountType type, Currency currency) {
        Account account = new Account(id, name, type, currency);
        if (accounts.putIfAbsent(id, account) != null) throw new LedgerException("account exists: " + id);
        netDebits.put(id, 0L);
        return account;
    }

    Account account(String id) {
        Account a = accounts.get(id);
        if (a == null) throw new LedgerException("unknown account: " + id);
        return a;
    }

    Money balance(String accountId) {
        Account a = account(accountId);
        long net = netDebits.get(accountId);
        return Money.ofMinor(a.type().normalBalance() == Side.DEBIT ? net : Math.negateExact(net), a.currency());
    }

    JournalEntry entry(String id) {
        Integer i = indexById.get(id);
        if (i == null) throw new LedgerException("unknown entry: " + id);
        return journal.get(i).entry();
    }

    List<JournalRecord> records() { return Collections.unmodifiableList(journal); }

    int size() { return journal.size(); }

    /** Post a new business event. Same id + same content is a no-op (returns false); same id + different content is a conflict. */
    boolean post(JournalEntry entry) {
        if (entry.kind() != EntryKind.ORIGINAL) throw new LedgerException("use reverse, reversePartially, or correct for " + entry.kind());
        if (replayed(entry)) return false;
        validateAccounts(entry);
        append(entry);
        return true;
    }

    /** Reverse whatever remains of an entry. Idempotent on reversalId. */
    JournalEntry reverse(String originalId, String reversalId, String reason) {
        JournalEntry existing = existingChild(reversalId, originalId, EntryKind.REVERSAL);
        if (existing != null) return existing;
        JournalEntry original = reversible(originalId);
        long[] done = reversedSoFar.get(originalId);
        List<Posting> flipped = new ArrayList<>();
        for (int i = 0; i < original.postings().size(); i++) {
            Posting p = original.postings().get(i);
            long remaining = p.amount().minorUnits() - done[i];
            if (remaining > 0) flipped.add(new Posting(p.accountId(), p.side().opposite(), Money.ofMinor(remaining, p.amount().currency())));
        }
        if (flipped.isEmpty()) throw new AlreadyReversedException(originalId);
        JournalEntry reversal = new JournalEntry(reversalId, "reversal of " + originalId, flipped, EntryKind.REVERSAL, originalId, reason);
        append(reversal);
        return reversal;
    }

    /**
     * Reverse {@code amount} of an entry (a partial refund). Each side's legs are allocated in proportion to what
     * remains unreversed on each leg (largest remainder, Lesson 01), so no leg is ever over-reversed and the
     * reversal is balanced. Idempotent on reversalId.
     */
    JournalEntry reversePartially(String originalId, String reversalId, Money amount, String reason) {
        JournalEntry existing = existingChild(reversalId, originalId, EntryKind.REVERSAL);
        if (existing != null) {
            if (existing.debitTotal() != amount.minorUnits()) throw new IdempotencyConflictException(reversalId);
            return existing;
        }
        JournalEntry original = reversible(originalId);
        if (!amount.currency().equals(original.currency())) throw new CurrencyMismatchException(amount.currency(), original.currency());
        if (!amount.isPositive()) throw new LedgerException("partial reversal amount must be positive");
        long[] done = reversedSoFar.get(originalId);
        long remainingDebits = 0;
        for (int i = 0; i < original.postings().size(); i++) {
            Posting p = original.postings().get(i);
            if (p.side() == Side.DEBIT) remainingDebits += p.amount().minorUnits() - done[i];
        }
        if (remainingDebits == 0) throw new AlreadyReversedException(originalId);
        if (amount.minorUnits() > remainingDebits) {
            throw new ExcessiveReversalException(originalId, amount, Money.ofMinor(remainingDebits, amount.currency()));
        }
        List<Posting> flipped = new ArrayList<>();
        for (Side side : Side.values()) {
            List<Integer> legs = new ArrayList<>();
            for (int i = 0; i < original.postings().size(); i++) if (original.postings().get(i).side() == side) legs.add(i);
            long[] ratios = new long[legs.size()];
            for (int k = 0; k < legs.size(); k++) {
                int i = legs.get(k);
                ratios[k] = original.postings().get(i).amount().minorUnits() - done[i];
            }
            Money[] parts = amount.allocate(ratios);
            for (int k = 0; k < legs.size(); k++) {
                if (parts[k].isZero()) continue;
                Posting p = original.postings().get(legs.get(k));
                flipped.add(new Posting(p.accountId(), side.opposite(), parts[k]));
            }
        }
        JournalEntry reversal = new JournalEntry(reversalId, "partial reversal of " + originalId, flipped, EntryKind.REVERSAL, originalId, reason);
        append(reversal);
        return reversal;
    }

    /**
     * Correct an entry that was wrong when posted: fully reverse it and post the replacement, linked to the original,
     * as one atomic step. Both are validated before either is appended. Idempotent on (reversalId, replacementId).
     */
    JournalEntry correct(String originalId, String reversalId, String replacementId, List<Posting> replacementPostings, String reason) {
        JournalEntry replacement = new JournalEntry(replacementId, "correction of " + originalId, replacementPostings,
                EntryKind.CORRECTION, originalId, reason);
        JournalEntry priorReversal = existingChild(reversalId, originalId, EntryKind.REVERSAL);
        if (priorReversal != null && indexById.containsKey(replacementId)) {
            if (!entry(replacementId).equals(replacement)) throw new IdempotencyConflictException(replacementId);
            return replacement;
        }
        if (priorReversal != null || indexById.containsKey(replacementId)) {
            throw new LedgerException("correction of " + originalId + " was half-applied; ids already in use");
        }
        JournalEntry original = reversible(originalId);
        long[] done = reversedSoFar.get(originalId);
        if (Arrays.stream(done).anyMatch(v -> v != 0)) {
            throw new LedgerException("entry " + originalId + " already has reversals; correct the remaining amount explicitly");
        }
        validateAccounts(replacement);
        List<Posting> flipped = new ArrayList<>();
        for (Posting p : original.postings()) flipped.add(new Posting(p.accountId(), p.side().opposite(), p.amount()));
        JournalEntry reversal = new JournalEntry(reversalId, "reversal of " + originalId + " for correction", flipped,
                EntryKind.REVERSAL, originalId, reason);
        append(reversal);
        append(replacement);
        return replacement;
    }

    boolean isFullyReversed(String id) {
        JournalEntry e = entry(id);
        long[] done = reversedSoFar.get(id);
        if (done == null) return false;
        for (int i = 0; i < e.postings().size(); i++) if (done[i] != e.postings().get(i).amount().minorUnits()) return false;
        return true;
    }

    /** Amount of the entry not yet reversed (debit side), e.g. what can still be refunded. */
    Money remaining(String id) {
        JournalEntry e = entry(id);
        long[] done = reversedSoFar.get(id);
        long rem = 0;
        for (int i = 0; i < e.postings().size(); i++) {
            if (e.postings().get(i).side() == Side.DEBIT) rem += e.postings().get(i).amount().minorUnits() - (done == null ? 0 : done[i]);
        }
        return Money.ofMinor(rem, e.currency());
    }

    /** The entry and everything that relates to it, transitively, in journal order: the audit story of one event. */
    List<JournalRecord> history(String id) {
        List<String> ids = new ArrayList<>();
        collect(id, ids);
        List<JournalRecord> out = new ArrayList<>();
        for (String x : ids) out.add(journal.get(indexById.get(x)));
        out.sort((a, b) -> Long.compare(a.seq(), b.seq()));
        return out;
    }

    /** One line per record: seq, kind, id, link, reason, net effect. What an auditor reads. */
    List<String> auditView() {
        List<String> lines = new ArrayList<>();
        for (JournalRecord r : journal) {
            JournalEntry e = r.entry();
            lines.add(String.format("#%d %-10s %-9s %-13s %-31s debits %-11s hash %s",
                    r.seq(), e.kind(), e.id(), e.relatesTo() == null ? "" : "-> " + e.relatesTo(),
                    e.reason() == null ? "" : e.reason(), Money.ofMinor(e.debitTotal(), e.currency()), r.hash().substring(0, 12)));
        }
        return lines;
    }

    /** Recompute every hash from the genesis value. Returns the seq of the first broken record, or -1 if intact. */
    static long verifyChain(List<JournalRecord> records) {
        String prev = JournalRecord.GENESIS;
        for (int i = 0; i < records.size(); i++) {
            JournalRecord r = records.get(i);
            if (r.seq() != i + 1 || !r.prevHash().equals(prev) || !r.hash().equals(JournalRecord.hashOf(r.seq(), r.entry(), prev))) {
                return i + 1;
            }
            prev = r.hash();
        }
        return -1;
    }

    // ---- internals ----

    private boolean replayed(JournalEntry entry) {
        Integer i = indexById.get(entry.id());
        if (i == null) return false;
        if (journal.get(i).entry().equals(entry)) return true;
        throw new IdempotencyConflictException(entry.id());
    }

    private JournalEntry existingChild(String childId, String originalId, EntryKind kind) {
        Integer i = indexById.get(childId);
        if (i == null) return null;
        JournalEntry e = journal.get(i).entry();
        if (e.kind() != kind || !originalId.equals(e.relatesTo())) throw new IdempotencyConflictException(childId);
        return e;
    }

    private JournalEntry reversible(String id) {
        JournalEntry e = entry(id);
        if (e.kind() == EntryKind.REVERSAL) {
            throw new LedgerException("entry " + id + " is a reversal; post a new original instead of reversing a reversal");
        }
        return e;
    }

    private void validateAccounts(JournalEntry entry) {
        for (Posting p : entry.postings()) {
            if (!account(p.accountId()).currency().equals(p.amount().currency())) {
                throw new LedgerException("entry " + entry.id() + ": account " + p.accountId() + " is not " + p.amount().currency().code());
            }
        }
    }

    private void append(JournalEntry entry) {
        long seq = journal.size() + 1;
        String prev = journal.isEmpty() ? JournalRecord.GENESIS : journal.get(journal.size() - 1).hash();
        journal.add(new JournalRecord(seq, entry, prev, JournalRecord.hashOf(seq, entry, prev)));
        indexById.put(entry.id(), (int) seq - 1);
        for (Posting p : entry.postings()) netDebits.merge(p.accountId(), p.signedMinor(), Math::addExact);
        if (entry.kind() != EntryKind.REVERSAL) reversedSoFar.put(entry.id(), new long[entry.postings().size()]);
        if (entry.relatesTo() != null) {
            children.computeIfAbsent(entry.relatesTo(), k -> new ArrayList<>()).add(entry.id());
            if (entry.kind() == EntryKind.REVERSAL) applyReversal(entry);
        }
    }

    /** Attribute each reversal leg to the original's legs (same account, opposite side), never exceeding a leg. */
    private void applyReversal(JournalEntry reversal) {
        JournalEntry original = entry(reversal.relatesTo());
        long[] done = reversedSoFar.get(original.id());
        for (Posting r : reversal.postings()) {
            long left = r.amount().minorUnits();
            for (int i = 0; i < original.postings().size() && left > 0; i++) {
                Posting o = original.postings().get(i);
                if (!o.accountId().equals(r.accountId()) || o.side() != r.side().opposite()) continue;
                long take = Math.min(left, o.amount().minorUnits() - done[i]);
                done[i] += take;
                left -= take;
            }
            if (left != 0) throw new IllegalStateException("reversal " + reversal.id() + " exceeds original " + original.id());
        }
    }

    private void collect(String id, List<String> out) {
        out.add(id);
        for (String child : children.getOrDefault(id, List.of())) collect(child, out);
    }
}

public final class Main {
    static void printEntry(JournalEntry e) {
        System.out.println(e.kind() + " " + e.id() + (e.relatesTo() == null ? "" : " -> " + e.relatesTo() + " (" + e.reason() + ")"));
        for (Posting p : e.postings()) {
            System.out.println((p.side() == Side.DEBIT ? "  Dr " : "      Cr ") + p.accountId() + "  " + p.amount());
        }
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;
        Ledger ledger = new Ledger();
        ledger.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        ledger.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, USD);
        ledger.openAccount("4100", "Fee Revenue", AccountType.REVENUE, USD);

        System.out.println("== A 120.00 USD sale with a 2.9% fee ==");
        Money gross = Money.parse("120.00", USD);
        Money fee = gross.times(29, 1000, Rounding.HALF_EVEN);
        JournalEntry sale = JournalEntry.original("pay-100", "card payment captured", List.of(
                Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee)));
        ledger.post(sale);
        printEntry(sale);

        System.out.println("\n== Partial refund of 50.00: fee and payable reversed in proportion ==");
        JournalEntry r1 = ledger.reversePartially("pay-100", "ref-100-1", Money.parse("50.00", USD), "customer returned 1 of 2 items");
        printEntry(r1);
        JournalEntry again = ledger.reversePartially("pay-100", "ref-100-1", Money.parse("50.00", USD), "retry of the same refund");
        System.out.println("retry of ref-100-1 returns the same entry: " + (again == r1) + ", journal size " + ledger.size());
        System.out.println("remaining refundable: " + ledger.remaining("pay-100"));
        try {
            ledger.reversePartially("pay-100", "ref-100-2", Money.parse("80.00", USD), "refund too large");
        } catch (ExcessiveReversalException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Full reversal of what remains (chargeback) ==");
        JournalEntry r2 = ledger.reverse("pay-100", "cb-100", "chargeback 10.4 fraud");
        printEntry(r2);
        System.out.println("fully reversed: " + ledger.isFullyReversed("pay-100") + "; balances 1200 " + ledger.balance("1200")
                + ", 2100 " + ledger.balance("2100") + ", 4100 " + ledger.balance("4100"));
        try {
            ledger.reverse("pay-100", "cb-100b", "duplicate chargeback notice");
        } catch (AlreadyReversedException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Correction: a fee booked at 3.9% instead of 2.9% ==");
        Money gross2 = Money.parse("80.00", USD);
        Money wrongFee = gross2.times(39, 1000, Rounding.HALF_EVEN);
        ledger.post(JournalEntry.original("pay-200", "card payment captured", List.of(
                Posting.debit("1200", gross2), Posting.credit("2100", gross2.minus(wrongFee)), Posting.credit("4100", wrongFee))));
        Money rightFee = gross2.times(29, 1000, Rounding.HALF_EVEN);
        JournalEntry fixed = ledger.correct("pay-200", "rev-200", "pay-200c", List.of(
                Posting.debit("1200", gross2), Posting.credit("2100", gross2.minus(rightFee)), Posting.credit("4100", rightFee)),
                "fee rate config error INC-4471");
        printEntry(ledger.entry("rev-200"));
        printEntry(fixed);
        System.out.println("fee revenue now " + ledger.balance("4100") + " (2.32 on pay-200, 0.00 net on pay-100)");

        System.out.println("\n== Audit view: nothing was edited, everything is linked ==");
        ledger.auditView().forEach(System.out::println);
        System.out.println("history of pay-100: " + ledger.history("pay-100").stream().map(r -> r.entry().id()).toList());
        System.out.println("hash chain intact: " + (Ledger.verifyChain(ledger.records()) == -1));

        List<JournalRecord> tampered = new ArrayList<>(ledger.records());
        JournalRecord victim = tampered.get(2);
        JournalEntry edited = JournalEntry.original("cb-100", "quietly edited", victim.entry().postings());
        tampered.set(2, new JournalRecord(victim.seq(), edited, victim.prevHash(), victim.hash()));
        System.out.println("after editing record #3 in a copy, first broken seq = " + Ledger.verifyChain(tampered));
    }
}
