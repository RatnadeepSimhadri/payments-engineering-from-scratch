// Project: projects/ledger-engine — stage 1 starter: account types, normal balance side, account registry.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py ledger-engine --lang java --stage 1 --path <workspace>
// Every method below throws until you implement it. Keep the names: the graders of every stage use them.
// Java 21, standard library only.

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Which column a posting lands in. Debit and credit are directions, not "minus" and "plus". */
enum Side {
    DEBIT, CREDIT;

    /** DEBIT <-> CREDIT. */
    Side opposite() {
        throw new UnsupportedOperationException("stage 1: implement Side.opposite");
    }
}

/** The five account classes of double-entry bookkeeping. The class decides which side increases the account. */
enum AccountType {
    ASSET, LIABILITY, EQUITY, REVENUE, EXPENSE;

    /** Assets and expenses grow on the debit side; liabilities, equity, and revenue grow on the credit side. */
    Side normalSide() {
        throw new UnsupportedOperationException("stage 1: implement AccountType.normalSide");
    }
}

/** An account's identity never changes: id, class, and currency are fixed when it is opened. */
record Account(String id, AccountType type, Currency currency) {
    Account {
        // TODO stage 1: reject a null or blank id, a null type, a null currency (IllegalArgumentException / NullPointerException).
    }

    /** The side on which this account's balance grows. */
    Side normalSide() {
        throw new UnsupportedOperationException("stage 1: implement Account.normalSide");
    }
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

    /** Open an account. The same id can never be opened twice, even with identical attributes -> DuplicateAccountException. */
    Account open(String id, AccountType type, Currency currency) {
        throw new UnsupportedOperationException("stage 1: implement AccountRegistry.open");
    }

    /** Look an account up; an unknown id is an UnknownAccountException, never null. */
    Account get(String id) {
        throw new UnsupportedOperationException("stage 1: implement AccountRegistry.get");
    }

    boolean contains(String id) {
        throw new UnsupportedOperationException("stage 1: implement AccountRegistry.contains");
    }

    int size() {
        throw new UnsupportedOperationException("stage 1: implement AccountRegistry.size");
    }

    /** Accounts in opening order; an unmodifiable copy, so the chart cannot be edited from outside. */
    List<Account> all() {
        throw new UnsupportedOperationException("stage 1: implement AccountRegistry.all");
    }
}
