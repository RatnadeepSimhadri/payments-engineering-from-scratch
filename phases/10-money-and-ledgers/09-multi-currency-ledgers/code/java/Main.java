// Lesson: phases/10-money-and-ledgers/09-multi-currency-ledgers/docs/en.md
// Multi-Currency Ledgers and Revaluation
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 4217 (codes, exponents); IAS 21 / ASC 830 (foreign-currency monetary items, exchange differences);
// Money from Lesson 01, FX rate shape from Lesson 02. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
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
// Per-currency sub-ledgers. An entry has exactly one currency. Cross-currency business events are
// transactions made of one entry per currency, joined through FX clearing accounts.
// ---------------------------------------------------------------------------------------------

enum Side { DEBIT, CREDIT }

enum AccountType {
    ASSET(Side.DEBIT), EXPENSE(Side.DEBIT), LIABILITY(Side.CREDIT), EQUITY(Side.CREDIT), REVENUE(Side.CREDIT);

    private final Side normalBalance;
    AccountType(Side normalBalance) { this.normalBalance = normalBalance; }
    Side normalBalance() { return normalBalance; }
}

/** An account holds exactly one currency for its whole life. {@code clearing} marks FX clearing accounts. */
record Account(String id, String name, AccountType type, Currency currency, boolean clearing) {}

record Posting(String accountId, Side side, Money amount) {
    Posting {
        Objects.requireNonNull(accountId, "accountId");
        if (!amount.isPositive()) throw new LedgerException("posting amount must be positive, got " + amount);
    }

    static Posting debit(String accountId, Money amount) { return new Posting(accountId, Side.DEBIT, amount); }
    static Posting credit(String accountId, Money amount) { return new Posting(accountId, Side.CREDIT, amount); }

    long signedMinor() { return side == Side.DEBIT ? amount.minorUnits() : -amount.minorUnits(); }
}

/** A single-currency balanced entry. Mixing currencies inside one entry is rejected, not netted. */
record JournalEntry(String id, Currency currency, List<Posting> postings) {
    JournalEntry {
        if (postings == null || postings.size() < 2) throw new LedgerException("entry " + id + " needs at least two postings");
        postings = List.copyOf(postings);
        long net = 0;
        for (Posting p : postings) {
            if (!p.amount().currency().equals(currency)) {
                throw new MixedCurrencyException(id, currency, p.amount().currency());
            }
            net = Math.addExact(net, p.signedMinor());
        }
        if (net != 0) throw new LedgerException("entry " + id + " unbalanced by " + Money.ofMinor(net, currency));
    }

    static JournalEntry of(String id, Posting... postings) {
        return new JournalEntry(id, postings[0].amount().currency(), List.of(postings));
    }
}

/**
 * An exchange rate as an exact rational: 1 unit of {@code base} = numerator/denominator units of {@code quote}.
 * Source and timestamp travel with it so every conversion can be audited (Lesson 02).
 */
record FxRate(Currency base, Currency quote, long numerator, long denominator, String source, long asOf) {
    FxRate {
        if (base.equals(quote)) throw new IllegalArgumentException("rate needs two different currencies");
        if (numerator <= 0 || denominator <= 0) throw new IllegalArgumentException("rate must be positive");
    }

    /** Convert an amount of base or quote into the other currency, in minor units, rounding once. */
    Money convert(Money amount, Rounding rounding) {
        Currency from = amount.currency();
        Currency to = from.equals(base) ? quote : from.equals(quote) ? base : null;
        if (to == null) throw new CurrencyMismatchException(amount.currency(), base);
        long num = from.equals(base) ? numerator : denominator;
        long den = from.equals(base) ? denominator : numerator;
        // minor(to) = minor(from) * rate * 10^(exp(to) - exp(from))
        int shift = to.exponent() - from.exponent();
        for (int i = 0; i < Math.abs(shift); i++) {
            if (shift > 0) num = Math.multiplyExact(num, 10); else den = Math.multiplyExact(den, 10);
        }
        return Money.ofMinor(Money.divideRounded(Math.multiplyExact(amount.minorUnits(), num), den, rounding), to);
    }

    @Override public String toString() {
        return "1 " + base.code() + " = " + numerator + "/" + denominator + " " + quote.code() + " (" + source + ")";
    }
}

/** A business event: one entry per currency, posted all-or-nothing. {@code rate} is null for single-currency events. */
record Transaction(String id, String description, List<JournalEntry> entries, FxRate rate) {
    Transaction { entries = List.copyOf(entries); }
}

class LedgerException extends IllegalArgumentException {
    LedgerException(String message) { super(message); }
}

final class MixedCurrencyException extends LedgerException {
    MixedCurrencyException(String id, Currency entryCurrency, Currency found) {
        super("entry " + id + " is " + entryCurrency.code() + " but has a " + found.code() + " posting; use one entry per currency");
    }
}

/** One currency's journal and balances. Knows nothing about other currencies. */
final class SubLedger {
    final Currency currency;
    private final List<JournalEntry> journal = new ArrayList<>();
    private final Map<String, Long> netDebits = new LinkedHashMap<>();

    SubLedger(Currency currency) { this.currency = currency; }

    void open(String accountId) { netDebits.put(accountId, 0L); }

    void append(JournalEntry e) {
        journal.add(e);
        for (Posting p : e.postings()) netDebits.merge(p.accountId(), p.signedMinor(), Math::addExact);
    }

    long netDebit(String accountId) { return netDebits.getOrDefault(accountId, 0L); }

    /** Sum of net debits over every account in this currency. Zero, always, or the sub-ledger is corrupt. */
    long trialBalanceNet() {
        long acc = 0;
        for (long v : netDebits.values()) acc = Math.addExact(acc, v);
        return acc;
    }

    int size() { return journal.size(); }

    Map<String, Long> netDebits() { return Collections.unmodifiableMap(netDebits); }
}

/** Per foreign currency: the net position outside clearing, what it cost in functional currency, and revaluations booked. */
record Position(Currency currency, Money position, Money basis, Money revalued) {
    Money carryingValue() { return basis.plus(revalued); }
}

/**
 * A ledger with one SubLedger per currency, a functional (reporting) currency, and per-currency FX clearing accounts.
 * exchange() posts a two-entry transaction through clearing; revalue() books unrealized gain/loss in functional
 * currency only; realize() books the realized result when a foreign position is flat.
 */
final class MultiCurrencyLedger {
    final Currency functional;
    private final Map<Currency, SubLedger> subLedgers = new LinkedHashMap<>();
    private final Map<String, Account> accounts = new LinkedHashMap<>();
    private final Map<String, Transaction> transactions = new LinkedHashMap<>();
    private final Map<Currency, Long> basis = new HashMap<>();      // functional minor units, signed like the position
    private final Map<Currency, Long> revalued = new HashMap<>();   // cumulative unrealized gain (+) / loss (-)
    private int adjustmentCounter = 0;

    static final String UNREALIZED = "4900";
    static final String REALIZED = "4910";

    MultiCurrencyLedger(Currency functional) {
        this.functional = functional;
        addCurrency(functional);
        openAccount(UNREALIZED, "Unrealized FX Gain/Loss", AccountType.REVENUE, functional);
        openAccount(REALIZED, "Realized FX Gain/Loss", AccountType.REVENUE, functional);
    }

    static String clearingId(Currency c) { return "1900-" + c.code(); }
    static String revaluationId(Currency c) { return "1950-" + c.code(); }

    /** Register a currency: its sub-ledger, its clearing account, and (if foreign) its revaluation adjustment account. */
    void addCurrency(Currency c) {
        if (subLedgers.containsKey(c)) return;
        subLedgers.put(c, new SubLedger(c));
        openAccountInternal(new Account(clearingId(c), "FX Clearing " + c.code(), AccountType.ASSET, c, true));
        if (!c.equals(functional)) {
            openAccountInternal(new Account(revaluationId(c), "FX Revaluation Adjustment " + c.code(), AccountType.ASSET, functional, false));
            basis.put(c, 0L);
            revalued.put(c, 0L);
        }
    }

    Account openAccount(String id, String name, AccountType type, Currency currency) {
        addCurrency(currency);
        return openAccountInternal(new Account(id, name, type, currency, false));
    }

    private Account openAccountInternal(Account a) {
        if (accounts.putIfAbsent(a.id(), a) != null) throw new LedgerException("account exists: " + a.id());
        subLedgers.get(a.currency()).open(a.id());
        return a;
    }

    Account account(String id) {
        Account a = accounts.get(id);
        if (a == null) throw new LedgerException("unknown account: " + id);
        return a;
    }

    SubLedger subLedger(Currency c) {
        SubLedger s = subLedgers.get(c);
        if (s == null) throw new LedgerException("no sub-ledger for " + c.code());
        return s;
    }

    Money balance(String accountId) {
        Account a = account(accountId);
        long net = subLedger(a.currency()).netDebit(accountId);
        return Money.ofMinor(a.type().normalBalance() == Side.DEBIT ? net : Math.negateExact(net), a.currency());
    }

    Money netDebit(String accountId) {
        Account a = account(accountId);
        return Money.ofMinor(subLedger(a.currency()).netDebit(accountId), a.currency());
    }

    /** Post a transaction: validate every entry against its sub-ledger, then append all. Idempotent on transaction id. */
    boolean post(Transaction tx) {
        Transaction existing = transactions.get(tx.id());
        if (existing != null) {
            if (existing.equals(tx)) return false;
            throw new LedgerException("transaction " + tx.id() + " already posted with different content");
        }
        if (tx.entries().isEmpty()) throw new LedgerException("transaction " + tx.id() + " has no entries");
        for (JournalEntry e : tx.entries()) {
            for (Posting p : e.postings()) {
                if (!account(p.accountId()).currency().equals(e.currency())) {
                    throw new LedgerException("entry " + e.id() + ": account " + p.accountId() + " is not a " + e.currency().code() + " account");
                }
            }
        }
        for (JournalEntry e : tx.entries()) subLedger(e.currency()).append(e);
        transactions.put(tx.id(), tx);
        return true;
    }

    /**
     * Cross-currency movement: debit {@code debitAccount} by {@code amount} (its currency A) and credit
     * {@code creditAccount} with the converted amount (currency B), as two single-currency entries joined by
     * the clearing accounts 1900-A and 1900-B. One of A, B must be the functional currency so the position's
     * functional cost (basis) is known exactly.
     */
    Transaction exchange(String txId, String description, String debitAccount, Money amount, String creditAccount, FxRate rate) {
        Currency a = account(debitAccount).currency(), b = account(creditAccount).currency();
        if (!amount.currency().equals(a)) throw new CurrencyMismatchException(amount.currency(), a);
        if (a.equals(b)) throw new LedgerException("exchange needs two currencies; post a single-currency entry instead");
        if (!a.equals(functional) && !b.equals(functional)) {
            throw new LedgerException("exchange " + a.code() + "->" + b.code() + " must route through " + functional.code());
        }
        Money converted = rate.convert(amount, Rounding.HALF_EVEN);
        if (!converted.currency().equals(b)) throw new CurrencyMismatchException(converted.currency(), b);
        if (!converted.isPositive()) throw new LedgerException("converted amount rounds to zero");
        Transaction tx = new Transaction(txId, description, List.of(
                JournalEntry.of(txId + "/" + a.code(), Posting.debit(debitAccount, amount), Posting.credit(clearingId(a), amount)),
                JournalEntry.of(txId + "/" + b.code(), Posting.debit(clearingId(b), converted), Posting.credit(creditAccount, converted))),
                rate);
        if (post(tx)) {
            // Position outside clearing: A gains +amount, B loses -converted. Basis follows at the functional value.
            if (b.equals(functional)) basis.merge(a, converted.minorUnits(), Math::addExact);
            else basis.merge(b, -amount.minorUnits(), Math::addExact);
        }
        return transactions.get(txId);
    }

    /** Net debit of every non-clearing account in a foreign currency: what we actually hold (+) or owe (-). */
    Money position(Currency c) {
        long acc = 0;
        for (Account a : accounts.values()) {
            if (a.currency().equals(c) && !a.clearing()) acc = Math.addExact(acc, subLedger(c).netDebit(a.id()));
        }
        return Money.ofMinor(acc, c);
    }

    Position positionReport(Currency c) {
        requireForeign(c);
        return new Position(c, position(c), Money.ofMinor(basis.get(c), functional), Money.ofMinor(revalued.get(c), functional));
    }

    /**
     * Mark the foreign position to the closing rate. The difference between its value at that rate and its
     * carrying value (basis + earlier revaluations) is booked as unrealized gain/loss, in functional currency only:
     * gain -> Dr 1950-c / Cr 4900; loss -> Dr 4900 / Cr 1950-c. Returns the gain (negative for a loss).
     */
    Money revalue(Currency c, FxRate closingRate) {
        requireForeign(c);
        Money fair = closingRate.convert(position(c), Rounding.HALF_EVEN);
        long gain = Math.subtractExact(fair.minorUnits(), Math.addExact(basis.get(c), revalued.get(c)));
        if (gain != 0) {
            Money g = Money.ofMinor(Math.abs(gain), functional);
            String id = "reval-" + (++adjustmentCounter) + "-" + c.code();
            JournalEntry e = gain > 0
                    ? JournalEntry.of(id, Posting.debit(revaluationId(c), g), Posting.credit(UNREALIZED, g))
                    : JournalEntry.of(id, Posting.debit(UNREALIZED, g), Posting.credit(revaluationId(c), g));
            post(new Transaction(id, "revalue " + c.code() + " at " + closingRate, List.of(e), closingRate));
            revalued.merge(c, gain, Math::addExact);
        }
        return Money.ofMinor(gain, functional);
    }

    /**
     * When a foreign position is flat (zero outside clearing), its remaining basis is the realized gain/loss.
     * Book it from functional clearing to 4910 and reverse the unrealized revaluations, which are no longer unrealized.
     * Returns the realized gain (negative for a loss).
     */
    Money realize(Currency c) {
        requireForeign(c);
        if (!position(c).isZero()) throw new LedgerException("position in " + c.code() + " is " + position(c) + "; realize only a flat position");
        long realizedGain = -basis.get(c);
        List<JournalEntry> entries = new ArrayList<>();
        String id = "realize-" + (++adjustmentCounter) + "-" + c.code();
        if (realizedGain != 0) {
            Money g = Money.ofMinor(Math.abs(realizedGain), functional);
            entries.add(realizedGain > 0
                    ? JournalEntry.of(id + "/pnl", Posting.debit(clearingId(functional), g), Posting.credit(REALIZED, g))
                    : JournalEntry.of(id + "/pnl", Posting.debit(REALIZED, g), Posting.credit(clearingId(functional), g)));
        }
        long rev = revalued.get(c);
        if (rev != 0) {
            Money r = Money.ofMinor(Math.abs(rev), functional);
            entries.add(rev > 0
                    ? JournalEntry.of(id + "/unwind", Posting.debit(UNREALIZED, r), Posting.credit(revaluationId(c), r))
                    : JournalEntry.of(id + "/unwind", Posting.debit(revaluationId(c), r), Posting.credit(UNREALIZED, r)));
        }
        if (!entries.isEmpty()) post(new Transaction(id, "realize " + c.code() + " position", entries, null));
        basis.put(c, 0L);
        revalued.put(c, 0L);
        return Money.ofMinor(realizedGain, functional);
    }

    /** Invariant: functional clearing net debit == sum of open foreign bases (each exchange moves both by the same amount). */
    boolean clearingMatchesBasis() {
        long sum = 0;
        for (long b : basis.values()) sum = Math.addExact(sum, b);
        return subLedger(functional).netDebit(clearingId(functional)) == sum;
    }

    /** Every sub-ledger nets to zero in its own currency. */
    boolean allSubLedgersBalance() {
        for (SubLedger s : subLedgers.values()) if (s.trialBalanceNet() != 0) return false;
        return true;
    }

    List<Currency> currencies() { return List.copyOf(subLedgers.keySet()); }

    List<Account> accounts() { return List.copyOf(accounts.values()); }

    private void requireForeign(Currency c) {
        if (c.equals(functional) || !basis.containsKey(c)) throw new LedgerException(c.code() + " is not a foreign currency of this ledger");
    }
}

public final class Main {
    static void printTransaction(Transaction tx) {
        System.out.println(tx.id() + "  " + tx.description() + (tx.rate() == null ? "" : "  @ " + tx.rate()));
        for (JournalEntry e : tx.entries()) {
            System.out.println("  [" + e.currency().code() + " sub-ledger] " + e.id());
            for (Posting p : e.postings()) System.out.println((p.side() == Side.DEBIT ? "    Dr " : "        Cr ") + p.accountId() + "  " + p.amount());
        }
    }

    static void printBalances(MultiCurrencyLedger l) {
        for (Account a : l.accounts()) {
            Money b = l.balance(a.id());
            if (!b.isZero()) System.out.printf("  %-9s %-34s %s%n", a.id(), a.name(), b);
        }
        for (Currency c : l.currencies()) {
            System.out.println("  " + c.code() + " sub-ledger nets to " + Money.ofMinor(l.subLedger(c).trialBalanceNet(), c));
        }
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD, EUR = Currency.EUR;
        long day = 1_767_225_600L;
        MultiCurrencyLedger ledger = new MultiCurrencyLedger(USD);
        ledger.openAccount("1200-USD", "Processor Receivable", AccountType.ASSET, USD);
        ledger.openAccount("1010-USD", "Operating Bank", AccountType.ASSET, USD);
        ledger.openAccount("1010-EUR", "Operating Bank", AccountType.ASSET, EUR);
        ledger.openAccount("2100-EUR", "Merchant Payable", AccountType.LIABILITY, EUR);

        System.out.println("== A US customer pays 110.00 USD; the German merchant is owed EUR at 1 EUR = 1.1000 USD ==");
        FxRate booked = new FxRate(EUR, USD, 11_000, 10_000, "rate-desk", day);
        printTransaction(ledger.exchange("fx-1", "cross-border card payment", "1200-USD", Money.parse("110.00", USD), "2100-EUR", booked));
        printBalances(ledger);
        Position open = ledger.positionReport(EUR);
        System.out.println("  EUR position " + open.position() + " (we owe EUR), basis " + open.basis() + " (its USD value when booked)");

        System.out.println("\n== Mixing currencies in one entry is rejected ==");
        try {
            new JournalEntry("bad", USD, List.of(Posting.debit("1200-USD", Money.parse("110.00", USD)),
                    Posting.credit("2100-EUR", Money.parse("100.00", EUR))));
        } catch (MixedCurrencyException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Month end: EUR strengthens to 1.1200 USD; revalue the open EUR position ==");
        FxRate close = new FxRate(EUR, USD, 11_200, 10_000, "ECB reference", day + 30 * 86_400);
        System.out.println("unrealized gain/loss booked: " + ledger.revalue(EUR, close));
        System.out.println("revalue again at the same rate: " + ledger.revalue(EUR, close) + " (idempotent: nothing left to book)");
        Position p = ledger.positionReport(EUR);
        System.out.println("  EUR position " + p.position() + ", basis " + p.basis() + ", revalued " + p.revalued() + ", carrying " + p.carryingValue());
        System.out.println("  4900 Unrealized FX Gain/Loss " + ledger.balance("4900"));

        System.out.println("\n== Processor settles 110.00 USD; treasury buys 100.00 EUR at 1.1150; merchant is paid ==");
        ledger.post(new Transaction("stl-1", "processor settlement", List.of(JournalEntry.of("stl-1/USD",
                Posting.debit("1010-USD", Money.parse("110.00", USD)), Posting.credit("1200-USD", Money.parse("110.00", USD)))), null));
        FxRate market = new FxRate(EUR, USD, 11_150, 10_000, "bank quote", day + 31 * 86_400);
        printTransaction(ledger.exchange("fx-2", "buy EUR for payout", "1010-EUR", Money.parse("100.00", EUR), "1010-USD", market));
        ledger.post(new Transaction("out-1", "pay merchant", List.of(JournalEntry.of("out-1/EUR",
                Posting.debit("2100-EUR", Money.parse("100.00", EUR)), Posting.credit("1010-EUR", Money.parse("100.00", EUR)))), null));
        System.out.println("EUR position now " + ledger.position(EUR) + "; functional clearing holds " + ledger.netDebit("1900-USD"));
        System.out.println("realized gain/loss: " + ledger.realize(EUR));
        System.out.println("  4900 Unrealized " + ledger.balance("4900") + ", 4910 Realized " + ledger.balance("4910")
                + ", 1950-EUR " + ledger.balance("1950-EUR") + ", 1900-USD " + ledger.netDebit("1900-USD"));
        System.out.println("  operating bank 1010-USD " + ledger.balance("1010-USD") + ": the 1.50 USD the round trip cost");
        System.out.println("  every sub-ledger balances: " + ledger.allSubLedgersBalance() + "; clearing matches basis: " + ledger.clearingMatchesBasis());

        System.out.println("\n== Exponents differ: 5,000 JPY to USD at 1 USD = 157.25 JPY ==");
        FxRate jpy = new FxRate(USD, Currency.JPY, 15_725, 100, "rate-desk", day);
        System.out.println("5000 JPY -> " + jpy.convert(Money.parse("5000", Currency.JPY), Rounding.HALF_EVEN)
                + "; 31.80 USD -> " + jpy.convert(Money.parse("31.80", USD), Rounding.HALF_EVEN));
    }
}
