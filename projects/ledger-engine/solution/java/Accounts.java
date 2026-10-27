// Project: projects/ledger-engine — stage 1: account types, normal balance side, account registry.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Test: python3 scripts/project_test.py ledger-engine --lang java --all --solution
// Java 21, standard library only. Java first; Python port in solution/python; Go port follows later.

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** Which column a posting lands in. Debit and credit are directions, not "minus" and "plus". */
enum Side {
    DEBIT, CREDIT;

    Side opposite() { return this == DEBIT ? CREDIT : DEBIT; }
}

/** The five account classes of double-entry bookkeeping. The class decides which side increases the account. */
enum AccountType {
    ASSET, LIABILITY, EQUITY, REVENUE, EXPENSE;

    /** Assets and expenses grow on the debit side; liabilities, equity, and revenue grow on the credit side. */
    Side normalSide() {
        return (this == ASSET || this == EXPENSE) ? Side.DEBIT : Side.CREDIT;
    }
}

/** An account's identity never changes: id, class, and currency are fixed when it is opened. */
record Account(String id, AccountType type, Currency currency) {
    Account {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("account id must not be blank");
        Objects.requireNonNull(type, "type");
        Objects.requireNonNull(currency, "currency");
    }

    /** The side on which this account's balance grows. */
    Side normalSide() { return type.normalSide(); }
}

/** Base type for every rule the ledger enforces, so callers can catch "the ledger said no" in one place. */
class LedgerException extends IllegalArgumentException {
    LedgerException(String message) { super(message); }
}

final class DuplicateAccountException extends LedgerException {
    DuplicateAccountException(String id) { super("account already exists: " + id); }
}

final class UnknownAccountException extends LedgerException {
    UnknownAccountException(String id) { super("unknown account: " + id); }
}

/** The chart of accounts: every account the ledger knows, in opening order. Ids are unique. Not thread-safe. */
final class AccountRegistry {
    private final Map<String, Account> accounts = new LinkedHashMap<>();

    /** Open an account. The same id can never be opened twice, even with identical attributes. */
    Account open(String id, AccountType type, Currency currency) {
        Account account = new Account(id, type, currency);
        if (accounts.containsKey(account.id())) throw new DuplicateAccountException(account.id());
        accounts.put(account.id(), account);
        return account;
    }

    /** Look an account up; an unknown id is an error, never null. */
    Account get(String id) {
        Account account = accounts.get(id);
        if (account == null) throw new UnknownAccountException(id);
        return account;
    }

    boolean contains(String id) { return accounts.containsKey(id); }

    int size() { return accounts.size(); }

    /** Accounts in opening order; a copy, so the chart cannot be edited from outside. */
    List<Account> all() { return List.copyOf(accounts.values()); }
}
