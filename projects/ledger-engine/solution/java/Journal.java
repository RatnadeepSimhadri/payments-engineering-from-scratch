// Project: projects/ledger-engine — stage 2: postings, journal entries, and the posted record.
// Docs: projects/ledger-engine/stages/02-balanced-journal-entries/docs/en.md
// Run:  javac -d .out *.java && java -cp .out Main
// Test: python3 scripts/project_test.py ledger-engine --lang java --all --solution
// Java 21, standard library only. Java first; Python and Go ports follow later.

import java.util.List;
import java.util.Objects;

/** One leg of an entry: a positive amount on one side of one account. */
record Posting(String accountId, Side side, Money amount) {
    Posting {
        if (accountId == null || accountId.isBlank()) throw new IllegalArgumentException("posting needs an account id");
        Objects.requireNonNull(side, "side");
        Objects.requireNonNull(amount, "amount");
        if (!amount.isPositive()) throw new IllegalArgumentException("posting amount must be positive: " + amount);
    }

    /** The posting with its side flipped: the building block of a reversal (stage 3). */
    Posting mirrored() { return new Posting(accountId, side.opposite(), amount); }
}

/**
 * An atomic group of postings identified by a caller-chosen id (the idempotency key) and an
 * ISO-8601 booking date ("2026-10-01"). Immutable: the postings list is defensively copied.
 * Construction does not check balance; {@link Ledger#post} does, so a bad entry can be built,
 * offered, and refused without ever touching the journal.
 */
record JournalEntry(String id, String date, List<Posting> postings) {
    JournalEntry {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("entry id must not be blank");
        if (date == null || date.isBlank()) throw new IllegalArgumentException("entry date must not be blank");
        Objects.requireNonNull(postings, "postings");
        postings = List.copyOf(postings);
    }

    JournalEntry(String id, String date, Posting... postings) { this(id, date, List.of(postings)); }

    /** The currency of the first posting; {@link Ledger#post} guarantees every posting shares it. */
    Currency currency() {
        if (postings.isEmpty()) throw new IllegalStateException("entry has no postings");
        return postings.get(0).amount().currency();
    }
}

/** What the journal actually stores: the entry plus the sequence number it was given on arrival (1, 2, 3, ...). */
record PostedEntry(long sequence, JournalEntry entry) {
    String id() { return entry.id(); }
}

final class InvalidEntryException extends LedgerException {
    InvalidEntryException(String id, String why) { super("entry " + id + " rejected: " + why); }
}

final class UnbalancedEntryException extends LedgerException {
    UnbalancedEntryException(String id, Money debits, Money credits) {
        super("entry " + id + " is unbalanced: debits " + debits + " != credits " + credits);
    }
}

final class UnknownEntryException extends LedgerException {
    UnknownEntryException(String id) { super("unknown entry: " + id); }
}
