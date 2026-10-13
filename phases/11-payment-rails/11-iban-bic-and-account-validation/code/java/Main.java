// Lesson: phases/11-payment-rails/11-iban-bic-and-account-validation/docs/en.md
// IBAN, BIC, and Account Number Validation
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 13616-1 (IBAN) and ISO/IEC 7064 MOD 97-10; ISO 9362 (BIC); SWIFT IBAN Registry for the
// country table; ABA routing transit number 3-7-1 check digit. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

enum Severity { ERROR, WARNING }

/** Why a validation failed. Codes are stable identifiers a caller can branch on; messages are for humans. */
enum Reason {
    EMPTY, INVALID_CHARACTERS, UNKNOWN_COUNTRY, WRONG_LENGTH, INVALID_CHECK_DIGITS,
    BBAN_STRUCTURE, CHECKSUM_FAILED, INVALID_PREFIX, TEST_BIC, COUNTRY_MISMATCH
}

record Issue(Reason reason, Severity severity, String message) {
    @Override public String toString() { return severity + " " + reason + ": " + message; }
}

/** The outcome of validating one identifier. valid() is true when no ERROR-severity issue exists. */
record ValidationResult(String kind, String normalized, List<Issue> issues) {
    ValidationResult {
        issues = List.copyOf(issues);
    }

    boolean valid() {
        return issues.stream().noneMatch(i -> i.severity() == Severity.ERROR);
    }

    boolean has(Reason reason) {
        return issues.stream().anyMatch(i -> i.reason() == reason);
    }
}

/** One run of characters in a BBAN: count and character class n (digits), a (upper letters), c (either). */
record Group(int count, char charClass) {
    boolean matches(String segment) {
        if (segment.length() != count) return false;
        for (int i = 0; i < segment.length(); i++) {
            char ch = segment.charAt(i);
            boolean digit = ch >= '0' && ch <= '9';
            boolean upper = ch >= 'A' && ch <= 'Z';
            switch (charClass) {
                case 'n' -> { if (!digit) return false; }
                case 'a' -> { if (!upper) return false; }
                default -> { if (!digit && !upper) return false; }
            }
        }
        return true;
    }
}

record CountryFormat(String country, int length, List<Group> groups, int bankIdStart, int bankIdLength,
                     int branchIdStart, int branchIdLength) {
    int bbanLength() {
        return groups.stream().mapToInt(Group::count).sum();
    }

    boolean hasBranch() { return branchIdLength > 0; }

    static CountryFormat of(String country, int length, String pattern, int bankStart, int bankLen) {
        return of(country, length, pattern, bankStart, bankLen, 0, 0);
    }

    static CountryFormat of(String country, int length, String pattern, int bankStart, int bankLen,
                            int branchStart, int branchLen) {
        List<Group> groups = new ArrayList<>();
        for (String token : pattern.split(" ")) {
            groups.add(new Group(Integer.parseInt(token.substring(0, token.length() - 1)),
                    token.charAt(token.length() - 1)));
        }
        CountryFormat f = new CountryFormat(country, length, groups, bankStart, bankLen, branchStart, branchLen);
        if (f.bbanLength() + 4 != length) throw new IllegalStateException("bad table entry " + country);
        return f;
    }
}

final class Registry {
    /** Subset of the SWIFT IBAN Registry, keyed by ISO 3166-1 alpha-2 country. */
    static final Map<String, CountryFormat> COUNTRY_FORMATS;
    /** ISO 3166-1 codes a BIC may carry: the IBAN countries plus common non-IBAN jurisdictions. */
    static final Set<String> KNOWN_COUNTRIES;

    static {
        Map<String, CountryFormat> m = new LinkedHashMap<>();
        for (CountryFormat f : List.of(
                CountryFormat.of("AE", 23, "3n 16n", 0, 3),
                CountryFormat.of("AT", 20, "5n 11n", 0, 5),
                CountryFormat.of("BE", 16, "3n 7n 2n", 0, 3),
                CountryFormat.of("CH", 21, "5n 12c", 0, 5),
                CountryFormat.of("DE", 22, "8n 10n", 0, 8),
                CountryFormat.of("DK", 18, "4n 9n 1n", 0, 4),
                CountryFormat.of("ES", 24, "4n 4n 1n 1n 10n", 0, 4, 4, 4),
                CountryFormat.of("FI", 18, "3n 11n", 0, 3),
                CountryFormat.of("FR", 27, "5n 5n 11c 2n", 0, 5, 5, 5),
                CountryFormat.of("GB", 22, "4a 6n 8n", 0, 4, 4, 6),
                CountryFormat.of("IE", 22, "4a 6n 8n", 0, 4, 4, 6),
                CountryFormat.of("IT", 27, "1a 5n 5n 12c", 1, 5, 6, 5),
                CountryFormat.of("LU", 20, "3n 13c", 0, 3),
                CountryFormat.of("NL", 18, "4a 10n", 0, 4),
                CountryFormat.of("NO", 15, "4n 6n 1n", 0, 4),
                CountryFormat.of("PL", 28, "8n 16n", 0, 8),
                CountryFormat.of("PT", 25, "4n 4n 11n 2n", 0, 4, 4, 4),
                CountryFormat.of("SA", 24, "2n 18c", 0, 2),
                CountryFormat.of("SE", 24, "3n 16n 1n", 0, 3))) {
            m.put(f.country(), f);
        }
        COUNTRY_FORMATS = Collections.unmodifiableMap(m);
        Set<String> known = new java.util.HashSet<>(m.keySet());
        known.addAll(List.of("US", "CA", "JP", "AU", "SG", "HK", "IN", "CN", "BR", "MX", "ZA", "NZ", "KR"));
        KNOWN_COUNTRIES = Collections.unmodifiableSet(known);
    }

    /** Strip whitespace and hyphens, upper-case. The paper form "GB82 WEST 1234 ..." becomes machine form. */
    static String normalize(String text) {
        StringBuilder sb = new StringBuilder();
        for (char ch : text.toCharArray()) {
            if (ch == ' ' || ch == '-' || ch == '\t' || ch == '\r' || ch == '\n') continue;
            sb.append(Character.toUpperCase(ch));
        }
        return sb.toString();
    }

    static boolean isAlnumUpper(String s) {
        for (int i = 0; i < s.length(); i++) {
            char ch = s.charAt(i);
            if (!((ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z'))) return false;
        }
        return true;
    }

    static boolean isDigits(String s) {
        if (s.isEmpty()) return false;
        for (int i = 0; i < s.length(); i++) if (s.charAt(i) < '0' || s.charAt(i) > '9') return false;
        return true;
    }

    static boolean isLetters(String s) {
        if (s.isEmpty()) return false;
        for (int i = 0; i < s.length(); i++) if (s.charAt(i) < 'A' || s.charAt(i) > 'Z') return false;
        return true;
    }

    /**
     * ISO 7064 MOD 97-10: interpret letters as two-digit numbers (A=10 .. Z=35) and reduce the whole digit
     * string modulo 97. One digit at a time, so the running value never exceeds 97*10+9 and a long suffices.
     */
    static int mod97(String text) {
        long remainder = 0;
        for (int i = 0; i < text.length(); i++) {
            char ch = text.charAt(i);
            if (ch >= '0' && ch <= '9') {
                remainder = (remainder * 10 + (ch - '0')) % 97;
            } else if (ch >= 'A' && ch <= 'Z') {
                int value = ch - 'A' + 10;
                remainder = (remainder * 10 + value / 10) % 97;
                remainder = (remainder * 10 + value % 10) % 97;
            } else {
                throw new IllegalArgumentException("mod97 input must be A-Z or 0-9, got '" + ch + "'");
            }
        }
        return (int) remainder;
    }
}

/** A validated IBAN decomposed into its parts. Construct through Iban.parse, never directly. */
record Iban(String country, String checkDigits, String bban, String bankIdentifier, String branchIdentifier) {

    String value() { return country + checkDigits + bban; }

    /** Paper form: groups of four separated by spaces (ISO 13616-1, section 6). */
    String formatted() {
        String v = value();
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < v.length(); i += 4) {
            if (i > 0) sb.append(' ');
            sb.append(v, i, Math.min(i + 4, v.length()));
        }
        return sb.toString();
    }

    static ValidationResult validate(String text) {
        List<Issue> issues = new ArrayList<>();
        String s = Registry.normalize(text);
        if (s.isEmpty()) {
            issues.add(new Issue(Reason.EMPTY, Severity.ERROR, "empty IBAN"));
            return new ValidationResult("IBAN", s, issues);
        }
        if (!Registry.isAlnumUpper(s)) {
            issues.add(new Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "IBAN may contain only A-Z and 0-9"));
            return new ValidationResult("IBAN", s, issues);
        }
        if (s.length() < 4 || !Registry.isLetters(s.substring(0, 2))) {
            issues.add(new Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, "no country code in '" + s + "'"));
            return new ValidationResult("IBAN", s, issues);
        }
        String country = s.substring(0, 2);
        CountryFormat fmt = Registry.COUNTRY_FORMATS.get(country);
        if (fmt == null) {
            issues.add(new Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, country + " is not in the IBAN registry subset"));
            return new ValidationResult("IBAN", s, issues);
        }
        if (s.length() != fmt.length()) {
            issues.add(new Issue(Reason.WRONG_LENGTH, Severity.ERROR,
                    country + " IBAN must be " + fmt.length() + " characters, got " + s.length()));
            return new ValidationResult("IBAN", s, issues);
        }
        String check = s.substring(2, 4);
        boolean checkIsDigits = Registry.isDigits(check);
        if (!checkIsDigits || check.equals("00") || check.equals("01") || check.equals("99")) {
            issues.add(new Issue(Reason.INVALID_CHECK_DIGITS, Severity.ERROR,
                    "check digits '" + check + "' are structurally invalid"));
        }
        String bban = s.substring(4);
        int pos = 0;
        for (Group g : fmt.groups()) {
            String segment = bban.substring(pos, Math.min(pos + g.count(), bban.length()));
            if (!g.matches(segment)) {
                issues.add(new Issue(Reason.BBAN_STRUCTURE, Severity.ERROR, "BBAN positions " + (pos + 1) + "-"
                        + (pos + g.count()) + " must be " + g.count() + g.charClass() + ", got '" + segment + "'"));
                break;
            }
            pos += g.count();
        }
        if (checkIsDigits && Registry.mod97(bban + country + check) != 1) {
            issues.add(new Issue(Reason.CHECKSUM_FAILED, Severity.ERROR,
                    "mod-97 remainder is not 1; a digit is wrong or transposed"));
        }
        return new ValidationResult("IBAN", s, issues);
    }

    static Iban parse(String text) {
        ValidationResult result = validate(text);
        if (!result.valid()) throw new IllegalArgumentException(String.join("; ", result.issues().stream().map(Issue::toString).toList()));
        String s = result.normalized();
        CountryFormat fmt = Registry.COUNTRY_FORMATS.get(s.substring(0, 2));
        String bban = s.substring(4);
        String bank = bban.substring(fmt.bankIdStart(), fmt.bankIdStart() + fmt.bankIdLength());
        String branch = fmt.hasBranch() ? bban.substring(fmt.branchIdStart(), fmt.branchIdStart() + fmt.branchIdLength()) : null;
        return new Iban(s.substring(0, 2), s.substring(2, 4), bban, bank, branch);
    }

    /** ISO 13616-1 generation: set the check digits to 00, compute 98 - mod97, zero-pad to two digits. */
    static String checkDigitsFor(String country, String bban) {
        country = country.toUpperCase();
        bban = Registry.normalize(bban);
        CountryFormat fmt = Registry.COUNTRY_FORMATS.get(country);
        if (fmt == null) throw new IllegalArgumentException("unknown country " + country);
        if (bban.length() != fmt.bbanLength()) {
            throw new IllegalArgumentException(country + " BBAN must be " + fmt.bbanLength() + " characters, got " + bban.length());
        }
        int remainder = Registry.mod97(bban + country + "00");
        return String.format("%02d", 98 - remainder);
    }

    static Iban generate(String country, String bban) {
        String check = checkDigitsFor(country, bban);
        return parse(country.toUpperCase() + check + Registry.normalize(bban));
    }
}

/** ISO 9362 Business Identifier Code: 4 party prefix, 2 country, 2 location, optional 3 branch. */
record Bic(String institution, String country, String location, String branch) {

    String value() { return institution + country + location + branch; }

    String bic8() { return institution + country + location; }

    boolean isTest() { return location.charAt(1) == '0'; }

    static ValidationResult validate(String text) {
        List<Issue> issues = new ArrayList<>();
        String s = Registry.normalize(text);
        if (s.isEmpty()) {
            issues.add(new Issue(Reason.EMPTY, Severity.ERROR, "empty BIC"));
            return new ValidationResult("BIC", s, issues);
        }
        if (s.length() != 8 && s.length() != 11) {
            issues.add(new Issue(Reason.WRONG_LENGTH, Severity.ERROR, "BIC must be 8 or 11 characters, got " + s.length()));
            return new ValidationResult("BIC", s, issues);
        }
        if (!Registry.isAlnumUpper(s)) {
            issues.add(new Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "BIC may contain only A-Z and 0-9"));
            return new ValidationResult("BIC", s, issues);
        }
        String country = s.substring(4, 6);
        if (!Registry.isLetters(country)) {
            issues.add(new Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "country code '" + country + "' must be two letters"));
        } else if (!Registry.KNOWN_COUNTRIES.contains(country)) {
            issues.add(new Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, country + " is not a known ISO 3166-1 country"));
        }
        if (s.charAt(7) == '0') {
            issues.add(new Issue(Reason.TEST_BIC, Severity.WARNING, "location code ending in 0 denotes a test BIC"));
        }
        return new ValidationResult("BIC", s, issues);
    }

    static Bic parse(String text) {
        ValidationResult result = validate(text);
        if (!result.valid()) throw new IllegalArgumentException(String.join("; ", result.issues().stream().map(Issue::toString).toList()));
        String s = result.normalized();
        return new Bic(s.substring(0, 4), s.substring(4, 6), s.substring(6, 8), s.length() == 11 ? s.substring(8) : "XXX");
    }
}

/** ABA routing transit number: nine digits, 3-7-1 weighted mod-10 check digit, Federal Reserve prefix ranges. */
final class RoutingNumber {
    static final int[] WEIGHTS = {3, 7, 1, 3, 7, 1, 3, 7, 1};

    static char checkDigit(String firstEight) {
        if (firstEight.length() != 8 || !Registry.isDigits(firstEight)) throw new IllegalArgumentException("need exactly eight digits");
        int total = 0;
        for (int i = 0; i < 8; i++) total += WEIGHTS[i] * (firstEight.charAt(i) - '0');
        return (char) ('0' + (10 - total % 10) % 10);
    }

    /** Federal Reserve routing symbol ranges: 00-12 banks, 21-32 thrifts, 61-72 electronic, 80 traveler's cheques. */
    static boolean prefixIsValid(int prefix) {
        return (prefix >= 0 && prefix <= 12) || (prefix >= 21 && prefix <= 32) || (prefix >= 61 && prefix <= 72) || prefix == 80;
    }

    static ValidationResult validate(String text) {
        List<Issue> issues = new ArrayList<>();
        String s = Registry.normalize(text);
        if (s.isEmpty()) {
            issues.add(new Issue(Reason.EMPTY, Severity.ERROR, "empty routing number"));
            return new ValidationResult("ABA", s, issues);
        }
        if (!Registry.isDigits(s)) {
            issues.add(new Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "routing number must be digits"));
            return new ValidationResult("ABA", s, issues);
        }
        if (s.length() != 9) {
            issues.add(new Issue(Reason.WRONG_LENGTH, Severity.ERROR, "routing number must be 9 digits, got " + s.length()));
            return new ValidationResult("ABA", s, issues);
        }
        if (!prefixIsValid(Integer.parseInt(s.substring(0, 2)))) {
            issues.add(new Issue(Reason.INVALID_PREFIX, Severity.ERROR, "prefix " + s.substring(0, 2) + " is not a Federal Reserve routing symbol"));
        }
        int total = 0;
        for (int i = 0; i < 9; i++) total += WEIGHTS[i] * (s.charAt(i) - '0');
        if (total % 10 != 0) {
            issues.add(new Issue(Reason.CHECKSUM_FAILED, Severity.ERROR, "3-7-1 weighted sum " + total + " is not a multiple of 10"));
        }
        return new ValidationResult("ABA", s, issues);
    }
}

/** Facade: validate an IBAN and BIC pair as a payment system receives them on a payout instruction. */
final class AccountValidator {
    static ValidationResult validatePair(String ibanText, String bicText) {
        ValidationResult iban = Iban.validate(ibanText);
        ValidationResult bic = Bic.validate(bicText);
        List<Issue> issues = new ArrayList<>(iban.issues());
        issues.addAll(bic.issues());
        if (iban.valid() && bic.valid() && !iban.normalized().substring(0, 2).equals(bic.normalized().substring(4, 6))) {
            issues.add(new Issue(Reason.COUNTRY_MISMATCH, Severity.WARNING, "IBAN country " + iban.normalized().substring(0, 2)
                    + " differs from BIC country " + bic.normalized().substring(4, 6)));
        }
        return new ValidationResult("IBAN+BIC", iban.normalized() + "/" + bic.normalized(), issues);
    }
}

public final class Main {
    static void show(ValidationResult result) {
        System.out.printf("  %-8s %-32s %s%n", result.kind(), result.normalized(), result.valid() ? "valid" : "INVALID");
        for (Issue issue : result.issues()) System.out.println("           - " + issue);
    }

    public static void main(String[] args) {
        System.out.println("== Mod-97 by hand: GB82 WEST 1234 5698 7654 32 ==");
        Iban iban = Iban.parse("GB82 WEST 1234 5698 7654 32");
        String rearranged = iban.bban() + iban.country() + iban.checkDigits();
        System.out.println("  rearranged      = " + rearranged);
        System.out.println("  mod 97          = " + Registry.mod97(rearranged) + " (must be 1)");
        System.out.println("  bank identifier = " + iban.bankIdentifier() + ", branch (sort code) = " + iban.branchIdentifier());
        System.out.println("  check digits regenerated from BBAN = " + Iban.checkDigitsFor("GB", iban.bban()));

        System.out.println();
        System.out.println("== What mod-97 catches ==");
        String[][] mutations = {
                {"one digit changed  ", "GB82WEST12345698765431"},
                {"adjacent transposed", "GB82WEST12345698765423"},
                {"two groups swapped ", "GB82WEST56981234765432"}};
        for (String[] m : mutations) {
            System.out.println("  " + m[0] + " " + m[1] + ": " + (Iban.validate(m[1]).valid() ? "valid" : "rejected"));
        }

        System.out.println();
        System.out.println("== Structured failures ==");
        for (String sample : List.of(
                "DE89 3704 0044 0532 0130 00",
                "FR14 2004 1010 0505 0001 3M02 606",
                "DE89 3704 0044 0532 0130 0",
                "GB82 WEST 1234 5698 7654 3X",
                "GB00 WEST 1234 5698 7654 32",
                "XX82 WEST 1234 5698 7654 32",
                "gb82-west-1234-5698-7654-32")) {
            show(Iban.validate(sample));
        }

        System.out.println();
        System.out.println("== BIC (ISO 9362) ==");
        for (String sample : List.of("DEUTDEFF", "DEUTDEFF500", "NWBKGB2L", "DEUTDEF0", "DEUTDE", "DEUTZZFF")) {
            show(Bic.validate(sample));
        }

        System.out.println();
        System.out.println("== ABA routing numbers (fictitious prefixes, real check digits) ==");
        for (String sample : List.of("021999996", "011999993", "021999969", "991999990", "02199999")) {
            show(RoutingNumber.validate(sample));
        }

        System.out.println();
        System.out.println("== Payout instruction: IBAN + BIC together ==");
        show(AccountValidator.validatePair("DE89 3704 0044 0532 0130 00", "DEUTDEFF"));
        show(AccountValidator.validatePair("DE89 3704 0044 0532 0130 00", "NWBKGB2L"));

        System.out.println();
        System.out.println("== Generating an IBAN from a BBAN ==");
        Iban generated = Iban.generate("NL", "ABNA0417164300");
        System.out.println("  NL + ABNA0417164300 -> " + generated.formatted());
    }
}
