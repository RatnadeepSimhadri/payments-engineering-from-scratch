// Lesson: phases/11-payment-rails/04-pan-luhn-and-bin-ranges/docs/en.md
// PANs, Luhn, and BIN Range Routing
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO/IEC 7812-1 (PAN structure, Luhn check digit, Annex B); PCI DSS v4.0 Req. 3.4.1 (masking);
//          network public BIN prefix rules (Visa 4; Mastercard 51-55, 2221-2720; Amex 34, 37; Discover 6011, 65, 644-649).
// Java 21, standard library only. Only published test PANs appear anywhere in this lesson.

import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Optional;

/** The Luhn (mod 10) check-digit algorithm from ISO/IEC 7812-1 Annex B. Works on any digit string. */
final class Luhn {
    private Luhn() {}

    private static void requireDigits(String s) {
        if (s == null || s.isEmpty()) throw new IllegalArgumentException("digit string must not be empty");
        for (int i = 0; i < s.length(); i++) {
            if (s.charAt(i) < '0' || s.charAt(i) > '9') throw new IllegalArgumentException("not a digit at position " + i);
        }
    }

    /**
     * Luhn sum of the digits when the rightmost digit is at position 0.
     * If {@code rightmostIsCheckDigit} is true, doubling starts at position 1; otherwise at position 0.
     */
    static int sum(String digits, boolean rightmostIsCheckDigit) {
        int total = 0;
        boolean dbl = !rightmostIsCheckDigit;
        for (int i = digits.length() - 1; i >= 0; i--) {
            int d = digits.charAt(i) - '0';
            if (dbl) { d *= 2; if (d > 9) d -= 9; }
            total += d;
            dbl = !dbl;
        }
        return total;
    }

    /** True when the full number (including its check digit) satisfies sum mod 10 == 0. */
    static boolean isValid(String digits) {
        requireDigits(digits);
        if (digits.length() < 2) return false;
        return sum(digits, true) % 10 == 0;
    }

    /** The check digit that makes {@code partial + digit} Luhn-valid. */
    static int checkDigit(String partial) {
        requireDigits(partial);
        return (10 - sum(partial, false) % 10) % 10;
    }

    /** {@code partial} with its check digit appended. */
    static String complete(String partial) {
        return partial + checkDigit(partial);
    }
}

/** The networks this lesson routes to. Real routing tables carry many more (JCB, UnionPay, RuPay, domestic debit schemes). */
enum Network { VISA, MASTERCARD, AMEX, DISCOVER, UNKNOWN }

/** A primary account number: 12 to 19 digits (ISO/IEC 7812-1) that pass the Luhn check. Never logged unmasked. */
record Pan(String digits) {
    static final int MIN_LENGTH = 12;
    static final int MAX_LENGTH = 19;

    Pan {
        Objects.requireNonNull(digits, "digits");
        if (digits.length() < MIN_LENGTH || digits.length() > MAX_LENGTH) {
            throw new IllegalArgumentException("PAN must be " + MIN_LENGTH + "-" + MAX_LENGTH + " digits, got " + digits.length());
        }
        if (!Luhn.isValid(digits)) throw new IllegalArgumentException("PAN fails the Luhn check");
    }

    /** Parses a PAN that may contain spaces or hyphens (as typed on a form). Rejects anything else. */
    static Pan parse(String text) {
        StringBuilder sb = new StringBuilder();
        for (char c : text.toCharArray()) {
            if (c >= '0' && c <= '9') sb.append(c);
            else if (c != ' ' && c != '-') throw new IllegalArgumentException("PAN contains a non-digit character");
        }
        return new Pan(sb.toString());
    }

    int length() { return digits.length(); }

    /** The Major Industry Identifier: first digit. 3 = travel/entertainment, 4 and 5 = banking, 6 = merchandising/banking. */
    int majorIndustryIdentifier() { return digits.charAt(0) - '0'; }

    /** Issuer identification number of the given length (6 or 8). */
    String bin(int length) {
        if (length != 6 && length != 8) throw new IllegalArgumentException("BIN length must be 6 or 8");
        return digits.substring(0, length);
    }

    String lastFour() { return digits.substring(digits.length() - 4); }

    /** PCI DSS masking: the first {@code prefix} digits and the last four, everything else '*'. prefix is at most 8. */
    String masked(int prefix) {
        if (prefix < 0 || prefix > 8) throw new IllegalArgumentException("may show at most 8 leading digits");
        if (prefix + 4 >= digits.length()) throw new IllegalArgumentException("mask would reveal the whole PAN");
        StringBuilder sb = new StringBuilder(digits.substring(0, prefix));
        for (int i = prefix; i < digits.length() - 4; i++) sb.append('*');
        return sb.append(lastFour()).toString();
    }

    /** Default mask: six leading digits plus the last four. */
    String masked() { return masked(6); }

    /** toString is the mask on purpose: a Pan that ends up in a log line is masked. */
    @Override public String toString() { return masked(); }
}

/**
 * One row of a BIN table: an inclusive range of account-number prefixes, normalized to WIDTH digits,
 * and what it maps to. A 6-digit BIN 411111 is stored as [41111100, 41111199]; an 8-digit BIN 41111120 as a single point.
 */
record BinRange(long low, long high, Network network, String issuer, String product) {
    static final int WIDTH = 8;
    static final long MIN_KEY = 0;
    static final long MAX_KEY = 99_999_999L;

    BinRange {
        if (low < MIN_KEY || high > MAX_KEY || low > high) throw new IllegalArgumentException("bad range " + low + "-" + high);
        Objects.requireNonNull(network); Objects.requireNonNull(issuer); Objects.requireNonNull(product);
    }

    /** Builds a range covering every account number that starts with {@code prefix}. */
    static BinRange ofPrefix(String prefix, Network network, String issuer, String product) {
        return ofPrefixRange(prefix, prefix, network, issuer, product);
    }

    /** Builds a range from {@code fromPrefix} through {@code toPrefix} inclusive, both of the same length (e.g. "2221" to "2720"). */
    static BinRange ofPrefixRange(String fromPrefix, String toPrefix, Network network, String issuer, String product) {
        if (fromPrefix.length() != toPrefix.length()) throw new IllegalArgumentException("prefix range bounds must have the same length");
        return new BinRange(normalizeLow(fromPrefix), normalizeHigh(toPrefix), network, issuer, product);
    }

    static long normalizeLow(String prefix) { return pad(prefix, '0'); }

    static long normalizeHigh(String prefix) { return pad(prefix, '9'); }

    private static long pad(String prefix, char fill) {
        if (prefix.isEmpty() || prefix.length() > WIDTH) throw new IllegalArgumentException("prefix must be 1-" + WIDTH + " digits");
        StringBuilder sb = new StringBuilder(prefix);
        while (sb.length() < WIDTH) sb.append(fill);
        return Long.parseLong(sb.toString());
    }

    /** The lookup key of a PAN: its first WIDTH digits as a number. */
    static long keyOf(Pan pan) { return Long.parseLong(pan.digits().substring(0, WIDTH)); }

    boolean contains(long key) { return low <= key && key <= high; }

    boolean overlaps(BinRange other) { return low <= other.high && other.low <= high; }
}

/** A sorted, non-overlapping set of BinRanges with O(log n) lookup by binary search. */
final class BinTable {
    private final List<BinRange> ranges;

    BinTable(List<BinRange> input) {
        List<BinRange> sorted = new ArrayList<>(input);
        sorted.sort(Comparator.comparingLong(BinRange::low).thenComparingLong(BinRange::high));
        for (int i = 1; i < sorted.size(); i++) {
            if (sorted.get(i - 1).overlaps(sorted.get(i))) {
                throw new IllegalArgumentException("overlapping BIN ranges: " + sorted.get(i - 1) + " and " + sorted.get(i));
            }
        }
        this.ranges = Collections.unmodifiableList(sorted);
    }

    int size() { return ranges.size(); }

    /** Binary search for the last range whose low <= key, then check key <= high. */
    Optional<BinRange> lookup(long key) {
        int lo = 0, hi = ranges.size() - 1, candidate = -1;
        while (lo <= hi) {
            int mid = (lo + hi) >>> 1;
            if (ranges.get(mid).low() <= key) { candidate = mid; lo = mid + 1; }
            else hi = mid - 1;
        }
        if (candidate >= 0 && ranges.get(candidate).contains(key)) return Optional.of(ranges.get(candidate));
        return Optional.empty();
    }

    Optional<BinRange> lookup(Pan pan) { return lookup(BinRange.keyOf(pan)); }

    /** The public network prefix rules, as a BinTable. Real tables are licensed from the networks and updated monthly. */
    static BinTable networkRanges() {
        return new BinTable(List.of(
                BinRange.ofPrefix("4", Network.VISA, "network", "Visa"),
                BinRange.ofPrefixRange("51", "55", Network.MASTERCARD, "network", "Mastercard 5-series"),
                BinRange.ofPrefixRange("2221", "2720", Network.MASTERCARD, "network", "Mastercard 2-series"),
                BinRange.ofPrefix("34", Network.AMEX, "network", "American Express"),
                BinRange.ofPrefix("37", Network.AMEX, "network", "American Express"),
                BinRange.ofPrefix("6011", Network.DISCOVER, "network", "Discover"),
                BinRange.ofPrefixRange("644", "649", Network.DISCOVER, "network", "Discover"),
                BinRange.ofPrefix("65", Network.DISCOVER, "network", "Discover")));
    }

    /** Network detection is a lookup in the network table. */
    static Network detectNetwork(Pan pan) {
        return networkRanges().lookup(pan).map(BinRange::network).orElse(Network.UNKNOWN);
    }

    /** Lengths each network issues. ISO 7812 allows 12-19; the networks are narrower. */
    static boolean lengthAllowed(Network network, int length) {
        return switch (network) {
            case VISA -> length == 13 || length == 16 || length == 19;
            case MASTERCARD -> length == 16;
            case AMEX -> length == 15;
            case DISCOVER -> length == 16 || length == 19;
            case UNKNOWN -> length >= Pan.MIN_LENGTH && length <= Pan.MAX_LENGTH;
        };
    }
}

/** What a card-processing front end does with a PAN before it touches an authorization: validate, detect, route, mask. */
record RoutingDecision(Pan pan, Network network, Optional<BinRange> issuerRange, boolean lengthOk) {
    String describe() {
        return pan.masked() + "  len=" + pan.length() + "  " + network
                + (lengthOk ? "" : " (length not issued by this network)")
                + issuerRange.map(r -> "  issuer=" + r.issuer() + " product=" + r.product()).orElse("  issuer=unknown BIN");
    }
}

final class CardRouter {
    private final BinTable issuerTable;

    CardRouter(BinTable issuerTable) { this.issuerTable = issuerTable; }

    RoutingDecision route(String rawPan) {
        Pan pan = Pan.parse(rawPan);
        Network network = BinTable.detectNetwork(pan);
        return new RoutingDecision(pan, network, issuerTable.lookup(pan), BinTable.lengthAllowed(network, pan.length()));
    }
}

public final class Main {
    /** A small issuer table made only of well-known test BINs. Issuer names are illustrative. */
    static BinTable sampleIssuerTable() {
        return new BinTable(List.of(
                BinRange.ofPrefix("411111", Network.VISA, "Test Bank A", "Visa Classic credit"),
                BinRange.ofPrefix("424242", Network.VISA, "Test Bank B", "Visa debit"),
                BinRange.ofPrefix("55555555", Network.MASTERCARD, "Test Bank C", "Mastercard World (8-digit BIN)"),
                BinRange.ofPrefix("222300", Network.MASTERCARD, "Test Bank D", "Mastercard 2-series credit"),
                BinRange.ofPrefix("378282", Network.AMEX, "Amex (test)", "Amex Green"),
                BinRange.ofPrefix("601111", Network.DISCOVER, "Discover (test)", "Discover it")));
    }

    public static void main(String[] args) {
        System.out.println("== Luhn by hand: 4111 1111 1111 111? ==");
        String partial = "411111111111111";
        System.out.println("sum of weighted digits (no check digit) = " + Luhn.sum(partial, false));
        System.out.println("check digit = (10 - " + Luhn.sum(partial, false) % 10 + ") mod 10 = " + Luhn.checkDigit(partial));
        System.out.println("complete PAN = " + Luhn.complete(partial) + "  valid=" + Luhn.isValid(Luhn.complete(partial)));
        System.out.println("single-digit typo 4111111111111112 valid=" + Luhn.isValid("4111111111111112"));
        System.out.println("adjacent transposition 4242424242424242 -> 4422424242424242 valid=" + Luhn.isValid("4422424242424242"));
        System.out.println("09 <-> 90 transposition is the one Luhn misses: 091 valid=" + Luhn.isValid("091") + ", 901 valid=" + Luhn.isValid("901"));
        System.out.println();

        System.out.println("== BIN table normalized to " + BinRange.WIDTH + " digits ==");
        BinTable issuers = sampleIssuerTable();
        System.out.println("411111 (6-digit)   -> [" + BinRange.normalizeLow("411111") + ", " + BinRange.normalizeHigh("411111") + "]");
        System.out.println("55555555 (8-digit) -> [" + BinRange.normalizeLow("55555555") + ", " + BinRange.normalizeHigh("55555555") + "]");
        System.out.println("2221-2720 (network) -> [" + BinRange.normalizeLow("2221") + ", " + BinRange.normalizeHigh("2720") + "]");
        System.out.println(issuers.size() + " issuer ranges, " + BinTable.networkRanges().size() + " network ranges, binary search on both");
        System.out.println();

        System.out.println("== Routing test PANs ==");
        CardRouter router = new CardRouter(issuers);
        for (String raw : List.of("4111 1111 1111 1111", "4242424242424242", "5555-5555-5555-4444", "2223003122003222",
                "378282246310005", "6011111111111117", "4000056655665556", "6304000000000000")) {
            System.out.println(router.route(raw).describe());
        }
        System.out.println();
        System.out.println("== Masking ==");
        Pan amex = Pan.parse("378282246310005");
        System.out.println("default (6+4): " + amex.masked() + "   8-digit BIN visible: " + amex.masked(8) + "   last four only: " + amex.masked(0));
        System.out.println("toString() is the mask, so a stray log line prints " + amex);
        System.out.println();
        for (String bad : List.of("4111111111111112", "1234", "4111 1111 1111 111a")) {
            try { Pan.parse(bad); System.out.println("accepted " + bad + " (should not happen)"); }
            catch (IllegalArgumentException e) { System.out.println("rejected: " + e.getMessage()); }
        }
    }
}
