// Lesson: phases/11-payment-rails/08-ach-and-nacha/docs/en.md
// ACH and the NACHA File Format
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Nacha Operating Rules & Guidelines, Appendix Three (ACH record formats); ABA Routing Number
// Policy (check-digit weights 3-7-1); Nacha Appendix Four (return reason codes). Java 21, standard library only.

import java.time.Clock;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;

/** ISO 4217 currency; copied from Phase 10, Lesson 01. ACH moves USD only, but the type stays honest. */
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

/** An exact amount in minor units of one currency; the subset of Phase 10 Lesson 01 that ACH needs. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

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
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    boolean isNegative() { return minorUnits < 0; }
    boolean isZero() { return minorUnits == 0; }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String whole = Long.toString(magnitude / scale);
        String result = whole;
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = whole + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

/** Thrown when a line, a field, or a control total does not satisfy the Nacha file format. */
final class NachaFormatException extends IllegalArgumentException {
    NachaFormatException(String message) { super(message); }
}

/** ABA routing transit number: 8 digits of identity plus a mod-10 check digit weighted 3-7-1. */
final class RoutingNumber {
    private static final int[] WEIGHTS = {3, 7, 1, 3, 7, 1, 3, 7};

    private RoutingNumber() {}

    /** The ninth digit that makes the weighted sum of all nine digits a multiple of ten. */
    static int checkDigit(String first8) {
        if (first8 == null || first8.length() != 8 || !first8.chars().allMatch(Character::isDigit)) {
            throw new NachaFormatException("routing prefix must be 8 digits: " + first8);
        }
        int sum = 0;
        for (int i = 0; i < 8; i++) sum += WEIGHTS[i] * (first8.charAt(i) - '0');
        return (10 - sum % 10) % 10;
    }

    static boolean isValid(String routing) {
        if (routing == null || routing.length() != 9 || !routing.chars().allMatch(Character::isDigit)) return false;
        return checkDigit(routing.substring(0, 8)) == routing.charAt(8) - '0';
    }

    static String require(String routing) {
        if (!isValid(routing)) throw new NachaFormatException("invalid routing number (check digit): " + routing);
        return routing;
    }
}

/** Standard Entry Class: which rulebook chapter governs the batch and what the receiver authorized. */
enum SecCode {
    PPD, // Prearranged Payment and Deposit: consumer accounts, written/standing authorization (payroll, bills)
    CCD, // Corporate Credit or Debit: business-to-business
    WEB  // Internet-Initiated: consumer debit authorized online; requires fraud detection by the Originator
}

/** Two-digit transaction code: account type (2x checking, 3x savings) and direction (credit, debit, prenote, return). */
enum TransactionCode {
    CHECKING_RETURN_CREDIT(21), CHECKING_CREDIT(22), CHECKING_PRENOTE_CREDIT(23),
    CHECKING_RETURN_DEBIT(26), CHECKING_DEBIT(27), CHECKING_PRENOTE_DEBIT(28),
    SAVINGS_RETURN_CREDIT(31), SAVINGS_CREDIT(32), SAVINGS_PRENOTE_CREDIT(33),
    SAVINGS_RETURN_DEBIT(36), SAVINGS_DEBIT(37), SAVINGS_PRENOTE_DEBIT(38);

    final int code;
    TransactionCode(int code) { this.code = code; }

    boolean isCredit() { int d = code % 10; return d >= 1 && d <= 4; }
    boolean isDebit() { int d = code % 10; return d >= 6 && d <= 9; }
    boolean isPrenote() { int d = code % 10; return d == 3 || d == 8; }
    boolean isReturn() { int d = code % 10; return d == 1 || d == 6; }

    static TransactionCode fromCode(int code) {
        for (TransactionCode t : values()) if (t.code == code) return t;
        throw new NachaFormatException("unknown transaction code: " + code);
    }
}

/** Fixed-width field helpers. Positions in Nacha documentation are 1-based and inclusive; so are these. */
final class Fixed {
    private Fixed() {}

    /** Alphameric: left-justify, space-fill, truncate, uppercase. */
    static String alpha(String value, int width) {
        String s = value == null ? "" : value.toUpperCase();
        if (s.length() > width) s = s.substring(0, width);
        return s + " ".repeat(width - s.length());
    }

    /** Numeric: right-justify, zero-fill; a value that does not fit is an error, never truncated. */
    static String numeric(long value, int width) {
        if (value < 0) throw new NachaFormatException("numeric field cannot be negative: " + value);
        String s = Long.toString(value);
        if (s.length() > width) throw new NachaFormatException("value " + value + " does not fit in " + width + " digits");
        return "0".repeat(width - s.length()) + s;
    }

    /** Numeric given as a digit string (account numbers keep leading zeros). */
    static String digits(String value, int width) {
        if (value == null || value.isEmpty() || !value.chars().allMatch(Character::isDigit)) {
            throw new NachaFormatException("expected digits: " + value);
        }
        if (value.length() > width) throw new NachaFormatException("'" + value + "' does not fit in " + width + " digits");
        return "0".repeat(width - value.length()) + value;
    }

    /** Characters start..end inclusive, 1-based, exactly as the Nacha layout tables number them. */
    static String field(String line, int start, int end) { return line.substring(start - 1, end); }

    static long number(String line, int start, int end) {
        String s = field(line, start, end);
        if (!s.chars().allMatch(Character::isDigit)) throw new NachaFormatException("expected digits at " + start + "-" + end + ": '" + s + "'");
        return Long.parseLong(s);
    }

    static void requireMatch(String what, Object declared, Object computed) {
        if (!declared.equals(computed)) throw new NachaFormatException(what + " mismatch: declared " + declared + ", computed " + computed);
    }

    static String require94(String line) {
        if (line.length() != 94) throw new NachaFormatException("record must be 94 characters, got " + line.length() + ": '" + line + "'");
        return line;
    }
}

/** Record type 1. One per file. Who is sending, who is receiving, when it was created. */
record FileHeader(String immediateDestination, String immediateOrigin, LocalDateTime created, char fileIdModifier,
                  String destinationName, String originName, String referenceCode) {
    static final DateTimeFormatter DATE = DateTimeFormatter.ofPattern("yyMMdd");
    static final DateTimeFormatter TIME = DateTimeFormatter.ofPattern("HHmm");

    FileHeader {
        RoutingNumber.require(immediateDestination);
        if (immediateOrigin.length() != 9 && immediateOrigin.length() != 10) throw new NachaFormatException("immediate origin must be 9 or 10 characters");
        if (!Character.isLetterOrDigit(fileIdModifier) || Character.isLowerCase(fileIdModifier)) throw new NachaFormatException("file ID modifier must be A-Z or 0-9");
    }

    static FileHeader of(String destination, String origin, Clock clock, String destinationName, String originName) {
        return new FileHeader(destination, origin, LocalDateTime.now(clock), 'A', destinationName, originName, "");
    }

    String toLine() {
        String line = "1" + "01"
                + Fixed.alpha(immediateDestination.length() == 9 ? " " + immediateDestination : immediateDestination, 10)
                + Fixed.alpha(immediateOrigin.length() == 9 ? " " + immediateOrigin : immediateOrigin, 10)
                + created.format(DATE) + created.format(TIME)
                + fileIdModifier + "094" + "10" + "1"
                + Fixed.alpha(destinationName, 23) + Fixed.alpha(originName, 23) + Fixed.alpha(referenceCode, 8);
        return Fixed.require94(line);
    }

    static FileHeader parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '1') throw new NachaFormatException("not a file header: " + line);
        if (!Fixed.field(line, 35, 37).equals("094") || !Fixed.field(line, 38, 39).equals("10") || line.charAt(39) != '1') {
            throw new NachaFormatException("file header must declare record size 094, blocking factor 10, format code 1");
        }
        String date = Fixed.field(line, 24, 29), time = Fixed.field(line, 30, 33);
        LocalDateTime created = LocalDateTime.parse(date + time, DateTimeFormatter.ofPattern("yyMMddHHmm"));
        return new FileHeader(Fixed.field(line, 4, 13).strip(), Fixed.field(line, 14, 23).strip(), created, line.charAt(33),
                Fixed.field(line, 41, 63).strip(), Fixed.field(line, 64, 86).strip(), Fixed.field(line, 87, 94).strip());
    }
}

/** Record type 5. One per batch. Who the company is, what SEC code governs, when entries should settle. */
record BatchHeader(int serviceClassCode, String companyName, String companyDiscretionaryData, String companyId, SecCode secCode,
                   String entryDescription, String descriptiveDate, String effectiveEntryDate, String odfiId, int batchNumber) {
    static final int MIXED = 200, CREDITS_ONLY = 220, DEBITS_ONLY = 225;

    BatchHeader {
        if (serviceClassCode != MIXED && serviceClassCode != CREDITS_ONLY && serviceClassCode != DEBITS_ONLY) {
            throw new NachaFormatException("service class code must be 200, 220, or 225: " + serviceClassCode);
        }
        if (odfiId.length() != 8 || !odfiId.chars().allMatch(Character::isDigit)) throw new NachaFormatException("ODFI id must be 8 digits: " + odfiId);
        if (effectiveEntryDate.length() != 6) throw new NachaFormatException("effective entry date must be yyMMdd");
        if (batchNumber < 1) throw new NachaFormatException("batch number starts at 1");
    }

    String toLine() {
        String line = "5" + Fixed.numeric(serviceClassCode, 3) + Fixed.alpha(companyName, 16) + Fixed.alpha(companyDiscretionaryData, 20)
                + Fixed.alpha(companyId, 10) + secCode.name() + Fixed.alpha(entryDescription, 10) + Fixed.alpha(descriptiveDate, 6)
                + effectiveEntryDate + "   " /* settlement date: filled by the ACH Operator */ + "1" /* originator status */
                + odfiId + Fixed.numeric(batchNumber, 7);
        return Fixed.require94(line);
    }

    static BatchHeader parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '5') throw new NachaFormatException("not a batch header: " + line);
        return new BatchHeader((int) Fixed.number(line, 2, 4), Fixed.field(line, 5, 20).strip(), Fixed.field(line, 21, 40).strip(),
                Fixed.field(line, 41, 50).strip(), SecCode.valueOf(Fixed.field(line, 51, 53)), Fixed.field(line, 54, 63).strip(),
                Fixed.field(line, 64, 69).strip(), Fixed.field(line, 70, 75), Fixed.field(line, 80, 87), (int) Fixed.number(line, 88, 94));
    }
}

/**
 * Return reason codes from Nacha Appendix Four, the subset a payments team sees most. The window is how long the
 * RDFI has after settlement to return the entry: two banking days for most reasons, sixty calendar days for an
 * unauthorized consumer debit. Re-presentment is allowed only for R01 and R09, at most twice.
 */
record ReturnReason(String code, String description, boolean extendedWindow, boolean representable) {
    static final List<ReturnReason> TABLE = List.of(
            new ReturnReason("R01", "Insufficient funds", false, true),
            new ReturnReason("R02", "Account closed", false, false),
            new ReturnReason("R03", "No account / unable to locate account", false, false),
            new ReturnReason("R04", "Invalid account number structure", false, false),
            new ReturnReason("R05", "Unauthorized debit to consumer account using corporate SEC code", true, false),
            new ReturnReason("R07", "Authorization revoked by customer", true, false),
            new ReturnReason("R08", "Payment stopped", false, false),
            new ReturnReason("R09", "Uncollected funds", false, true),
            new ReturnReason("R10", "Customer advises originator is not known to receiver and/or not authorized", true, false),
            new ReturnReason("R11", "Customer advises entry not in accordance with the terms of the authorization", true, false),
            new ReturnReason("R16", "Account frozen", false, false),
            new ReturnReason("R20", "Non-transaction account", false, false),
            new ReturnReason("R29", "Corporate customer advises not authorized", false, false));

    static ReturnReason lookup(String code) {
        for (ReturnReason r : TABLE) if (r.code.equals(code)) return r;
        throw new NachaFormatException("unknown return reason code: " + code);
    }

    static boolean isKnown(String code) {
        for (ReturnReason r : TABLE) if (r.code.equals(code)) return true;
        return false;
    }

    /** Number of banking days (Mon-Fri; holidays ignored here) or calendar days the RDFI has. */
    int windowDays() { return extendedWindow ? 60 : 2; }

    /**
     * The last date on which the RDFI may transmit the return. Two-day reasons count banking days after
     * settlement; extended reasons count sixty calendar days. Federal Reserve holidays are out of scope.
     */
    java.time.LocalDate deadline(java.time.LocalDate settlementDate) {
        if (extendedWindow) return settlementDate.plusDays(60);
        java.time.LocalDate d = settlementDate;
        int remaining = 2;
        while (remaining > 0) {
            d = d.plusDays(1);
            if (d.getDayOfWeek() != java.time.DayOfWeek.SATURDAY && d.getDayOfWeek() != java.time.DayOfWeek.SUNDAY) remaining--;
        }
        return d;
    }

    /** True when the RDFI returned the entry on or before its deadline; a late return is itself dishonourable. */
    boolean isTimely(java.time.LocalDate settlementDate, java.time.LocalDate returnDate) {
        return !returnDate.isAfter(deadline(settlementDate));
    }
}

/**
 * Record type 7. Either a payment-related addenda (type code 05, free text) or a return addenda (type code 99,
 * carrying the reason code and the trace number of the entry being returned).
 */
record Addenda(String typeCode, String reasonCode, String originalTrace, String originalDfi, String info) {
    Addenda {
        if (!typeCode.equals("05") && !typeCode.equals("99")) throw new NachaFormatException("addenda type must be 05 or 99: " + typeCode);
        if (typeCode.equals("99")) {
            if (reasonCode.length() != 3) throw new NachaFormatException("return reason code must be 3 characters: " + reasonCode);
            if (originalTrace.length() != 15) throw new NachaFormatException("original trace must be 15 digits");
            if (originalDfi.length() != 8) throw new NachaFormatException("original DFI must be 8 digits");
        }
    }

    static Addenda payment(String info) { return new Addenda("05", "", "", "", info); }

    static Addenda returned(String reasonCode, String originalTrace, String originalDfi, String info) {
        return new Addenda("99", reasonCode, originalTrace, originalDfi, info);
    }

    boolean isReturn() { return typeCode.equals("99"); }

    /** The Appendix Four entry for this return; only meaningful on a type-99 addenda. */
    ReturnReason reason() {
        if (!isReturn()) throw new NachaFormatException("not a return addenda");
        return ReturnReason.lookup(reasonCode);
    }

    String toLine(int entrySequence, String traceNumber) {
        String line;
        if (typeCode.equals("05")) {
            line = "7" + "05" + Fixed.alpha(info, 80) + Fixed.numeric(1, 4) + Fixed.numeric(entrySequence, 7);
        } else {
            line = "7" + "99" + Fixed.alpha(reasonCode, 3) + originalTrace + Fixed.alpha("", 6) /* date of death */
                    + originalDfi + Fixed.alpha(info, 44) + traceNumber;
        }
        return Fixed.require94(line);
    }

    static Addenda parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '7') throw new NachaFormatException("not an addenda record: " + line);
        String type = Fixed.field(line, 2, 3);
        if (type.equals("05")) return payment(Fixed.field(line, 4, 83).strip());
        return returned(Fixed.field(line, 4, 6), Fixed.field(line, 7, 21), Fixed.field(line, 28, 35), Fixed.field(line, 36, 79).strip());
    }
}

/** Record type 6. One per payment. The receiver's bank, account, amount, and a trace number the ODFI assigns. */
record EntryDetail(TransactionCode transactionCode, String receivingDfi, String accountNumber, Money amount, String individualId,
                   String individualName, String discretionaryData, Addenda addenda, String traceNumber) {
    EntryDetail {
        RoutingNumber.require(receivingDfi);
        if (accountNumber.isBlank() || accountNumber.length() > 17) throw new NachaFormatException("account number must be 1-17 characters");
        if (!amount.currency().equals(Currency.USD)) throw new NachaFormatException("ACH carries USD only");
        if (amount.isNegative()) throw new NachaFormatException("amount must not be negative; direction is the transaction code");
        if (amount.minorUnits() > 9_999_999_999L) throw new NachaFormatException("amount exceeds 10 digits of cents");
        if (transactionCode.isPrenote() && !amount.isZero()) throw new NachaFormatException("prenote entries must carry a zero amount");
        if (!traceNumber.isEmpty() && traceNumber.length() != 15) throw new NachaFormatException("trace number must be 15 digits");
    }

    static EntryDetail of(TransactionCode code, String receivingDfi, String account, Money amount, String individualId, String name) {
        return new EntryDetail(code, receivingDfi, account, amount, individualId, name, "", null, "");
    }

    EntryDetail withAddenda(Addenda a) {
        return new EntryDetail(transactionCode, receivingDfi, accountNumber, amount, individualId, individualName, discretionaryData, a, traceNumber);
    }

    EntryDetail withTrace(String odfiId, int sequence) {
        return new EntryDetail(transactionCode, receivingDfi, accountNumber, amount, individualId, individualName, discretionaryData, addenda,
                odfiId + Fixed.numeric(sequence, 7));
    }

    /** The first eight digits: what the entry hash sums. The check digit is excluded on purpose. */
    String receivingDfiId() { return receivingDfi.substring(0, 8); }

    int recordCount() { return addenda == null ? 1 : 2; }

    String toLine() {
        String line = "6" + Fixed.numeric(transactionCode.code, 2) + receivingDfi + Fixed.alpha(accountNumber, 17)
                + Fixed.numeric(amount.minorUnits(), 10) + Fixed.alpha(individualId, 15) + Fixed.alpha(individualName, 22)
                + Fixed.alpha(discretionaryData, 2) + (addenda == null ? "0" : "1") + traceNumber;
        return Fixed.require94(line);
    }

    static EntryDetail parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '6') throw new NachaFormatException("not an entry detail: " + line);
        boolean hasAddenda = line.charAt(78) == '1';
        EntryDetail e = new EntryDetail(TransactionCode.fromCode((int) Fixed.number(line, 2, 3)), Fixed.field(line, 4, 12),
                Fixed.field(line, 13, 29).strip(), Money.ofMinor(Fixed.number(line, 30, 39), Currency.USD), Fixed.field(line, 40, 54).strip(),
                Fixed.field(line, 55, 76).strip(), Fixed.field(line, 77, 78).strip(), null, Fixed.field(line, 80, 94));
        return hasAddenda ? e.withAddenda(Addenda.payment("")) : e; // placeholder until the 7 record arrives
    }
}

/** Record type 8: the totals the receiver recomputes. Equality of parsed vs computed is the integrity check. */
record BatchControl(int serviceClassCode, int entryAddendaCount, long entryHash, Money totalDebit, Money totalCredit, String companyId,
                    String odfiId, int batchNumber) {
    /** Compare the declared control (from the file) against the computed one (from the entries), naming the first difference. */
    void verifyAgainst(BatchControl computed) {
        Fixed.requireMatch("batch " + batchNumber + " entry/addenda count", entryAddendaCount, computed.entryAddendaCount);
        Fixed.requireMatch("batch " + batchNumber + " entry hash", entryHash, computed.entryHash);
        Fixed.requireMatch("batch " + batchNumber + " total debit", totalDebit, computed.totalDebit);
        Fixed.requireMatch("batch " + batchNumber + " total credit", totalCredit, computed.totalCredit);
        Fixed.requireMatch("batch " + batchNumber + " control record", this, computed);
    }

    String toLine() {
        String line = "8" + Fixed.numeric(serviceClassCode, 3) + Fixed.numeric(entryAddendaCount, 6) + Fixed.numeric(entryHash, 10)
                + Fixed.numeric(totalDebit.minorUnits(), 12) + Fixed.numeric(totalCredit.minorUnits(), 12) + Fixed.alpha(companyId, 10)
                + Fixed.alpha("", 19) /* message authentication code */ + Fixed.alpha("", 6) + odfiId + Fixed.numeric(batchNumber, 7);
        return Fixed.require94(line);
    }

    static BatchControl parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '8') throw new NachaFormatException("not a batch control: " + line);
        return new BatchControl((int) Fixed.number(line, 2, 4), (int) Fixed.number(line, 5, 10), Fixed.number(line, 11, 20),
                Money.ofMinor(Fixed.number(line, 21, 32), Currency.USD), Money.ofMinor(Fixed.number(line, 33, 44), Currency.USD),
                Fixed.field(line, 45, 54).strip(), Fixed.field(line, 80, 87), (int) Fixed.number(line, 88, 94));
    }
}

/** Record type 9: file-level totals plus the block count that explains the 9-filler lines. */
record FileControl(int batchCount, int blockCount, int entryAddendaCount, long entryHash, Money totalDebit, Money totalCredit) {
    void verifyAgainst(FileControl computed) {
        Fixed.requireMatch("file batch count", batchCount, computed.batchCount);
        Fixed.requireMatch("file block count", blockCount, computed.blockCount);
        Fixed.requireMatch("file entry/addenda count", entryAddendaCount, computed.entryAddendaCount);
        Fixed.requireMatch("file entry hash", entryHash, computed.entryHash);
        Fixed.requireMatch("file total debit", totalDebit, computed.totalDebit);
        Fixed.requireMatch("file total credit", totalCredit, computed.totalCredit);
    }

    String toLine() {
        String line = "9" + Fixed.numeric(batchCount, 6) + Fixed.numeric(blockCount, 6) + Fixed.numeric(entryAddendaCount, 8)
                + Fixed.numeric(entryHash, 10) + Fixed.numeric(totalDebit.minorUnits(), 12) + Fixed.numeric(totalCredit.minorUnits(), 12)
                + Fixed.alpha("", 39);
        return Fixed.require94(line);
    }

    static FileControl parse(String line) {
        Fixed.require94(line);
        if (line.charAt(0) != '9') throw new NachaFormatException("not a file control: " + line);
        return new FileControl((int) Fixed.number(line, 2, 7), (int) Fixed.number(line, 8, 13), (int) Fixed.number(line, 14, 21),
                Fixed.number(line, 22, 31), Money.ofMinor(Fixed.number(line, 32, 43), Currency.USD), Money.ofMinor(Fixed.number(line, 44, 55), Currency.USD));
    }
}

/** A batch header, its entries (trace numbers assigned here), and the control record computed from them. */
record Batch(BatchHeader header, List<EntryDetail> entries) {
    static final long HASH_MODULUS = 10_000_000_000L;

    Batch {
        if (entries.isEmpty()) throw new NachaFormatException("a batch needs at least one entry");
        List<EntryDetail> traced = new ArrayList<>(entries.size());
        for (int i = 0; i < entries.size(); i++) {
            EntryDetail e = entries.get(i);
            traced.add(e.traceNumber().isEmpty() ? e.withTrace(header.odfiId(), i + 1) : e);
        }
        for (EntryDetail e : traced) {
            if (header.serviceClassCode() == BatchHeader.CREDITS_ONLY && e.transactionCode().isDebit()) throw new NachaFormatException("debit entry in a 220 credits-only batch");
            if (header.serviceClassCode() == BatchHeader.DEBITS_ONLY && e.transactionCode().isCredit()) throw new NachaFormatException("credit entry in a 225 debits-only batch");
        }
        entries = List.copyOf(traced);
    }

    /** Sum of the receiving DFI identifiers (8 digits each), keeping only the rightmost 10 digits. */
    static long entryHash(List<String> receivingDfiIds) {
        long sum = 0;
        for (String id : receivingDfiIds) sum = Math.addExact(sum, Long.parseLong(id));
        return sum % HASH_MODULUS;
    }

    long entryHash() { return entryHash(entries.stream().map(EntryDetail::receivingDfiId).toList()); }

    int entryAddendaCount() { return entries.stream().mapToInt(EntryDetail::recordCount).sum(); }

    Money totalDebit() {
        Money total = Money.ofMinor(0, Currency.USD);
        for (EntryDetail e : entries) if (e.transactionCode().isDebit()) total = total.plus(e.amount());
        return total;
    }

    Money totalCredit() {
        Money total = Money.ofMinor(0, Currency.USD);
        for (EntryDetail e : entries) if (e.transactionCode().isCredit()) total = total.plus(e.amount());
        return total;
    }

    BatchControl control() {
        return new BatchControl(header.serviceClassCode(), entryAddendaCount(), entryHash(), totalDebit(), totalCredit(),
                header.companyId(), header.odfiId(), header.batchNumber());
    }

    List<String> toLines() {
        List<String> lines = new ArrayList<>();
        lines.add(header.toLine());
        for (int i = 0; i < entries.size(); i++) {
            EntryDetail e = entries.get(i);
            lines.add(e.toLine());
            if (e.addenda() != null) lines.add(e.addenda().toLine(i + 1, e.traceNumber()));
        }
        lines.add(control().toLine());
        return lines;
    }
}

/** The whole file: header, batches, control, and 9-filler to a multiple of ten records. */
record AchFile(FileHeader header, List<Batch> batches) {
    static final int BLOCKING_FACTOR = 10;
    static final String FILLER = "9".repeat(94);

    AchFile {
        if (batches.isEmpty()) throw new NachaFormatException("a file needs at least one batch");
        batches = List.copyOf(batches);
    }

    int recordCount() { return 2 + batches.stream().mapToInt(b -> 2 + b.entryAddendaCount()).sum(); }

    int blockCount() { return (recordCount() + BLOCKING_FACTOR - 1) / BLOCKING_FACTOR; }

    FileControl control() {
        Money debit = Money.ofMinor(0, Currency.USD), credit = Money.ofMinor(0, Currency.USD);
        long hash = 0;
        int count = 0;
        for (Batch b : batches) {
            debit = debit.plus(b.totalDebit());
            credit = credit.plus(b.totalCredit());
            hash = (hash + b.entryHash()) % Batch.HASH_MODULUS;
            count += b.entryAddendaCount();
        }
        return new FileControl(batches.size(), blockCount(), count, hash, debit, credit);
    }

    List<String> toLines() {
        List<String> lines = new ArrayList<>();
        lines.add(header.toLine());
        for (Batch b : batches) lines.addAll(b.toLines());
        lines.add(control().toLine());
        while (lines.size() % BLOCKING_FACTOR != 0) lines.add(FILLER);
        return lines;
    }

    String write() { return String.join("\n", toLines()) + "\n"; }

    /** Parse and verify: every line 94 characters, every routing number valid, every control total recomputed and matched. */
    static AchFile parse(String text) {
        FileHeader fileHeader = null;
        List<Batch> batches = new ArrayList<>();
        BatchHeader batchHeader = null;
        List<EntryDetail> entries = null;
        FileControl fileControl = null;
        for (String raw : text.split("\r?\n")) {
            if (raw.isEmpty()) continue;
            String line = Fixed.require94(raw);
            if (line.equals(FILLER)) continue;
            switch (line.charAt(0)) {
                case '1' -> { if (fileHeader != null) throw new NachaFormatException("duplicate file header"); fileHeader = FileHeader.parse(line); }
                case '5' -> {
                    if (fileHeader == null) throw new NachaFormatException("batch header before file header");
                    if (batchHeader != null) throw new NachaFormatException("batch header before previous batch control");
                    batchHeader = BatchHeader.parse(line);
                    entries = new ArrayList<>();
                }
                case '6' -> { if (batchHeader == null) throw new NachaFormatException("entry outside a batch"); entries.add(EntryDetail.parse(line)); }
                case '7' -> {
                    if (entries == null || entries.isEmpty()) throw new NachaFormatException("addenda without an entry");
                    EntryDetail last = entries.get(entries.size() - 1);
                    if (last.addenda() == null) throw new NachaFormatException("addenda follows an entry whose addenda indicator is 0");
                    entries.set(entries.size() - 1, last.withAddenda(Addenda.parse(line)));
                }
                case '8' -> {
                    if (batchHeader == null) throw new NachaFormatException("batch control without a batch header");
                    Batch batch = new Batch(batchHeader, entries);
                    BatchControl.parse(line).verifyAgainst(batch.control());
                    batches.add(batch);
                    batchHeader = null;
                    entries = null;
                }
                case '9' -> { if (fileControl != null) throw new NachaFormatException("duplicate file control"); fileControl = FileControl.parse(line); }
                default -> throw new NachaFormatException("unknown record type " + line.charAt(0));
            }
        }
        if (fileHeader == null || fileControl == null) throw new NachaFormatException("file header and file control are both required");
        if (batchHeader != null) throw new NachaFormatException("file ended inside a batch");
        AchFile file = new AchFile(fileHeader, batches);
        fileControl.verifyAgainst(file.control());
        return file;
    }
}

public final class Main {
    static final String ODFI = "021999996";   // the originating bank; its first 8 digits prefix every trace number
    static final Clock CLOCK = Clock.fixed(Instant.parse("2026-10-11T09:30:00Z"), ZoneOffset.UTC);

    static AchFile payrollFile() {
        FileHeader header = FileHeader.of(ODFI, "1234567890", CLOCK, "FIRST FICTIONAL BANK", "ACME PAYROLL INC");
        BatchHeader batchHeader = new BatchHeader(BatchHeader.CREDITS_ONLY, "ACME PAYROLL", "", "1234567890", SecCode.PPD,
                "PAYROLL", "OCT 15", "261013", ODFI.substring(0, 8), 1);
        List<EntryDetail> entries = List.of(
                EntryDetail.of(TransactionCode.CHECKING_CREDIT, "011999993", "000123456789", Money.parse("2512.37", Currency.USD), "EMP0001", "ADA LOVELACE"),
                EntryDetail.of(TransactionCode.SAVINGS_CREDIT, "061999998", "9876543210", Money.parse("1800.00", Currency.USD), "EMP0002", "GRACE HOPPER"),
                EntryDetail.of(TransactionCode.CHECKING_CREDIT, "121999993", "55501234", Money.parse("3075.50", Currency.USD), "EMP0003", "EDSGER DIJKSTRA")
                        .withAddenda(Addenda.payment("BONUS INCLUDED Q3")));
        return new AchFile(header, List.of(new Batch(batchHeader, entries)));
    }

    public static void main(String[] args) {
        System.out.println("== Routing number check digit (ABA mod-10, weights 3-7-1) ==");
        System.out.println("02199999 -> check digit " + RoutingNumber.checkDigit("02199999") + "  (021999996 valid: " + RoutingNumber.isValid("021999996") + ")");
        System.out.println("021999969 valid? " + RoutingNumber.isValid("021999969") + "  (two digits transposed)");

        System.out.println("\n== A payroll file: 1 batch, 3 PPD credits, 1 addenda ==");
        AchFile file = payrollFile();
        String text = file.write();
        System.out.print(text);
        Batch batch = file.batches().get(0);
        System.out.println("entry hash   = " + batch.entryHash() + "  (01199999 + 06199999 + 12199999)");
        System.out.println("total credit = " + batch.totalCredit() + ", total debit = " + batch.totalDebit());
        System.out.println("records = " + file.recordCount() + ", blocks = " + file.blockCount() + ", lines written = " + file.toLines().size());

        System.out.println("\n== Round trip ==");
        AchFile parsed = AchFile.parse(text);
        System.out.println("parse(write(file)) == file ? " + parsed.equals(file));

        System.out.println("\n== A return: R01 Insufficient Funds on a WEB debit ==");
        EntryDetail original = EntryDetail.of(TransactionCode.CHECKING_DEBIT, "011999993", "000123456789", Money.parse("49.99", Currency.USD), "ORD-7781", "ADA LOVELACE")
                .withTrace(ODFI.substring(0, 8), 1);
        EntryDetail returned = EntryDetail.of(TransactionCode.CHECKING_RETURN_DEBIT, "011999993", "000123456789", original.amount(), "ORD-7781", "ADA LOVELACE")
                .withAddenda(Addenda.returned("R01", original.traceNumber(), ODFI.substring(0, 8), ""));
        BatchHeader returnHeader = new BatchHeader(BatchHeader.DEBITS_ONLY, "ACME STORE", "", "1234567890", SecCode.WEB, "RETURN", "", "261015", "01199999", 1);
        for (String line : new Batch(returnHeader, List.of(returned)).toLines()) System.out.println(line);
        ReturnReason reason = returned.addenda().reason();
        java.time.LocalDate settled = java.time.LocalDate.of(2026, 10, 15);   // a Thursday
        System.out.println(reason.code() + " " + reason.description() + ": window " + reason.windowDays()
                + (reason.extendedWindow() ? " calendar" : " banking") + " days, deadline " + reason.deadline(settled)
                + ", re-presentable " + reason.representable());
        ReturnReason r10 = ReturnReason.lookup("R10");
        System.out.println(r10.code() + " " + r10.description() + ": deadline " + r10.deadline(settled) + ", re-presentable " + r10.representable());

        System.out.println("\n== Rejections ==");
        try {
            EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999969", "1", Money.parse("1.00", Currency.USD), "", "X");
        } catch (NachaFormatException e) { System.out.println("rejected: " + e.getMessage()); }
        try {
            String ctl = batch.control().toLine();                       // bump the last digit of the credit total (position 44)
            char bumped = ctl.charAt(43) == '9' ? '0' : (char) (ctl.charAt(43) + 1);
            AchFile.parse(text.replace(ctl, ctl.substring(0, 43) + bumped + ctl.substring(44)));
        } catch (NachaFormatException e) { System.out.println("rejected: " + e.getMessage()); }
    }
}
