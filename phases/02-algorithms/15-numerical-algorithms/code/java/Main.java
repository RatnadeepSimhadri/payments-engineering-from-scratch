// Lesson: phases/02-algorithms/15-numerical-algorithms/docs/en.md
// Numerical Algorithms: GCD, Modular Arithmetic, and Check Digits
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO/IEC 7812-1 (Luhn, Annex B); ISO 7064:2003 (MOD 97-10); ISO 13616-1 (IBAN); Damm, "Totally
// anti-symmetric quasigroups" (2004); Knuth TAOCP vol. 2 sec. 4.5.2 (Euclid). Java 21, standard library only.

import java.math.BigInteger;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Euclid's algorithm and its extension: the gcd, the Bezout coefficients, and the modular inverse. */
final class Euclid {
    /** gcd(a, b) with gcd(0, 0) = 0 and the result always non-negative. O(log min(a, b)) divisions. */
    static long gcd(long a, long b) {
        a = Math.abs(a);
        b = Math.abs(b);
        while (b != 0) {
            long r = a % b;
            a = b;
            b = r;
        }
        return a;
    }

    static long lcm(long a, long b) {
        if (a == 0 || b == 0) return 0;
        return Math.abs(Math.multiplyExact(a / gcd(a, b), b));
    }

    /** g = gcd(a, b) together with x, y such that a*x + b*y = g. */
    record Bezout(long g, long x, long y) {}

    /** Extended Euclid: carries the coefficient pairs alongside the remainders. */
    static Bezout extendedGcd(long a, long b) {
        long oldR = a, r = b;
        long oldX = 1, x = 0;
        long oldY = 0, y = 1;
        while (r != 0) {
            long q = oldR / r;
            long tmp;
            tmp = oldR - q * r; oldR = r; r = tmp;
            tmp = oldX - q * x; oldX = x; x = tmp;
            tmp = oldY - q * y; oldY = y; y = tmp;
        }
        if (oldR < 0) return new Bezout(-oldR, -oldX, -oldY);
        return new Bezout(oldR, oldX, oldY);
    }

    /** The x in 0..m-1 with a*x = 1 (mod m); exists iff gcd(a, m) = 1. */
    static long modInverse(long a, long m) {
        if (m <= 1) throw new IllegalArgumentException("modulus must be > 1");
        Bezout b = extendedGcd(Modular.mod(a, m), m);
        if (b.g() != 1) throw new ArithmeticException(a + " has no inverse modulo " + m + " (gcd = " + b.g() + ")");
        return Modular.mod(b.x(), m);
    }
}

/** Modular arithmetic helpers. All results are in 0..m-1 regardless of the sign of the input. */
final class Modular {
    static long mod(long a, long m) {
        if (m <= 0) throw new IllegalArgumentException("modulus must be positive");
        long r = a % m;                 // Java's % keeps the dividend's sign
        return r < 0 ? r + m : r;
    }

    /** base^exp mod m by repeated squaring, O(log exp) multiplications; exp must be non-negative. */
    static long modPow(long base, long exp, long m) {
        if (exp < 0) throw new IllegalArgumentException("exponent must be non-negative");
        long result = 1 % m;
        long b = mod(base, m);
        while (exp > 0) {
            if ((exp & 1) == 1) result = Math.multiplyExact(result, b) % m;
            b = Math.multiplyExact(b, b) % m;
            exp >>= 1;
        }
        return result;
    }

    /**
     * Remainder of an arbitrarily long decimal string by Horner's rule: r = (r * 10 + digit) mod m. Never
     * forms the big number, so a 30-digit IBAN numeric needs no BigInteger. Requires m < 2^63 / 10.
     */
    static long modOfDecimalString(String digits, long m) {
        if (digits.isEmpty()) throw new IllegalArgumentException("empty digit string");
        long r = 0;
        for (int i = 0; i < digits.length(); i++) {
            char c = digits.charAt(i);
            if (c < '0' || c > '9') throw new IllegalArgumentException("not a digit at " + i + ": " + c);
            r = (r * 10 + (c - '0')) % m;
        }
        return r;
    }
}

/** Something that accepts or rejects a full identifier (payload plus check). */
interface Validator {
    boolean validate(String identifier);
}

/** Luhn (ISO/IEC 7812-1 Annex B): the check digit on every card PAN. */
final class Luhn {
    /** Weighted sum from the right: every second digit (starting with the one nearest the check) is doubled, digits of the product summed. */
    private static int weightedSum(String digits, boolean doubleFirst) {
        int sum = 0;
        boolean dbl = doubleFirst;
        for (int i = digits.length() - 1; i >= 0; i--) {
            int d = digits.charAt(i) - '0';
            if (d < 0 || d > 9) throw new IllegalArgumentException("not a digit at " + i + ": " + digits.charAt(i));
            if (dbl) {
                d *= 2;
                if (d > 9) d -= 9;              // digit sum of a two-digit product: 16 -> 1 + 6 = 7 = 16 - 9
            }
            sum += d;
            dbl = !dbl;
        }
        return sum;
    }

    /** The digit c such that payload + c validates. The payload's rightmost digit is doubled because c will sit to its right. */
    static int checkDigit(String payload) {
        if (payload.isEmpty()) throw new IllegalArgumentException("empty payload");
        return (10 - weightedSum(payload, true) % 10) % 10;
    }

    static boolean validate(String pan) {
        if (pan.length() < 2) return false;
        for (int i = 0; i < pan.length(); i++) if (!Character.isDigit(pan.charAt(i))) return false;
        return weightedSum(pan, false) % 10 == 0;
    }
}

/** ISO 7064 MOD 97-10 as used by ISO 13616 IBAN: two check digits, remainder must be 1. */
final class Iso7064Mod97 {
    /** Move the first four characters to the end, then replace each letter with two digits (A=10 .. Z=35). */
    static String ibanToNumeric(String iban) {
        String s = iban.replace(" ", "").toUpperCase();
        if (s.length() < 5) throw new IllegalArgumentException("IBAN too short: " + iban);
        String rearranged = s.substring(4) + s.substring(0, 4);
        StringBuilder out = new StringBuilder();
        for (int i = 0; i < rearranged.length(); i++) {
            char c = rearranged.charAt(i);
            if (c >= '0' && c <= '9') out.append(c);
            else if (c >= 'A' && c <= 'Z') out.append(c - 'A' + 10);
            else throw new IllegalArgumentException("invalid IBAN character: " + c);
        }
        return out.toString();
    }

    static boolean validateIban(String iban) {
        try {
            return Modular.modOfDecimalString(ibanToNumeric(iban), 97) == 1;
        } catch (IllegalArgumentException e) {
            return false;
        }
    }

    /** The two check digits for country + BBAN: compute with "00" in the check position, then 98 - remainder. */
    static String ibanCheckDigits(String countryCode, String bban) {
        long r = Modular.modOfDecimalString(ibanToNumeric(countryCode + "00" + bban), 97);
        long check = 98 - r;
        return (check < 10 ? "0" : "") + check;
    }

    static String buildIban(String countryCode, String bban) {
        return countryCode + ibanCheckDigits(countryCode, bban) + bban;
    }
}

/** Damm (2004): a single check digit from a totally anti-symmetric quasigroup of order 10. */
final class Damm {
    static final int[][] TABLE = {
            {0, 3, 1, 7, 5, 9, 8, 6, 4, 2},
            {7, 0, 9, 2, 1, 5, 4, 8, 6, 3},
            {4, 2, 0, 6, 8, 7, 1, 3, 5, 9},
            {1, 7, 5, 0, 9, 8, 3, 4, 2, 6},
            {6, 1, 2, 3, 0, 4, 5, 9, 7, 8},
            {3, 6, 7, 4, 2, 0, 9, 5, 8, 1},
            {5, 8, 6, 9, 7, 2, 0, 1, 3, 4},
            {8, 9, 4, 5, 3, 6, 2, 0, 1, 7},
            {9, 4, 3, 8, 6, 1, 7, 2, 0, 5},
            {2, 5, 8, 1, 4, 3, 6, 7, 9, 0},
    };

    private static int interim(String digits) {
        int state = 0;
        for (int i = 0; i < digits.length(); i++) {
            int d = digits.charAt(i) - '0';
            if (d < 0 || d > 9) throw new IllegalArgumentException("not a digit at " + i + ": " + digits.charAt(i));
            state = TABLE[state][d];
        }
        return state;
    }

    /** The check digit is the final interim value; appending it drives the state to 0 because TABLE[s][s] = 0. */
    static int checkDigit(String payload) {
        if (payload.isEmpty()) throw new IllegalArgumentException("empty payload");
        return interim(payload);
    }

    static boolean validate(String identifier) {
        if (identifier.isEmpty()) return false;
        for (int i = 0; i < identifier.length(); i++) if (!Character.isDigit(identifier.charAt(i))) return false;
        return interim(identifier) == 0;
    }
}

/** Which kinds of keying error a check digit is supposed to catch. */
enum ErrorClass {
    SINGLE_DIGIT,            // one digit replaced by another: 123 -> 173
    ADJACENT_TRANSPOSITION,  // two neighbouring, different digits swapped: 123 -> 213
    TWIN,                    // a pair of equal digits replaced by another equal pair: 122 -> 155
    JUMP_TRANSPOSITION,      // digits two apart swapped: 123 -> 321
}

/** Exhaustive error injection on one identifier, counting how many corruptions the validator rejects. */
final class ErrorAnalysis {
    record Report(ErrorClass errorClass, long tried, long caught, List<String> missed) {
        long missedCount() { return tried - caught; }
        String rate() { return tried == 0 ? "n/a" : String.format("%d/%d", caught, tried); }
    }

    /** Every corruption of the given class on `valid` (digit positions only; letters in an IBAN are left alone). */
    static List<String> corruptions(String valid, ErrorClass cls) {
        List<String> out = new ArrayList<>();
        char[] c = valid.toCharArray();
        int n = c.length;
        switch (cls) {
            case SINGLE_DIGIT -> {
                for (int i = 0; i < n; i++) {
                    if (!Character.isDigit(c[i])) continue;
                    for (char d = '0'; d <= '9'; d++) {
                        if (d == c[i]) continue;
                        char[] m = c.clone();
                        m[i] = d;
                        out.add(new String(m));
                    }
                }
            }
            case ADJACENT_TRANSPOSITION -> {
                for (int i = 0; i + 1 < n; i++) {
                    if (!Character.isDigit(c[i]) || !Character.isDigit(c[i + 1]) || c[i] == c[i + 1]) continue;
                    char[] m = c.clone();
                    m[i] = c[i + 1];
                    m[i + 1] = c[i];
                    out.add(new String(m));
                }
            }
            case TWIN -> {
                for (int i = 0; i + 1 < n; i++) {
                    if (!Character.isDigit(c[i]) || c[i] != c[i + 1]) continue;
                    for (char d = '0'; d <= '9'; d++) {
                        if (d == c[i]) continue;
                        char[] m = c.clone();
                        m[i] = d;
                        m[i + 1] = d;
                        out.add(new String(m));
                    }
                }
            }
            case JUMP_TRANSPOSITION -> {
                for (int i = 0; i + 2 < n; i++) {
                    if (!Character.isDigit(c[i]) || !Character.isDigit(c[i + 2]) || c[i] == c[i + 2]) continue;
                    char[] m = c.clone();
                    m[i] = c[i + 2];
                    m[i + 2] = c[i];
                    out.add(new String(m));
                }
            }
        }
        return out;
    }

    static Report analyse(Validator v, String valid, ErrorClass cls) {
        if (!v.validate(valid)) throw new IllegalArgumentException("the sample must validate before it is corrupted: " + valid);
        List<String> missed = new ArrayList<>();
        long tried = 0, caught = 0;
        for (String bad : corruptions(valid, cls)) {
            tried++;
            if (v.validate(bad)) missed.add(bad); else caught++;
        }
        return new Report(cls, tried, caught, missed);
    }

    /** Aggregate over many seeded random payloads so a single lucky sample cannot hide a weakness. */
    static Map<ErrorClass, long[]> aggregate(Validator v, java.util.function.Function<String, String> complete, int samples, int payloadLength, long seed) {
        SeededRandom rng = new SeededRandom(seed);
        Map<ErrorClass, long[]> totals = new LinkedHashMap<>();
        for (ErrorClass cls : ErrorClass.values()) totals.put(cls, new long[2]);
        for (int s = 0; s < samples; s++) {
            StringBuilder payload = new StringBuilder();
            for (int i = 0; i < payloadLength; i++) payload.append((char) ('0' + rng.nextInt(10)));
            String valid = complete.apply(payload.toString());
            for (ErrorClass cls : ErrorClass.values()) {
                Report r = analyse(v, valid, cls);
                totals.get(cls)[0] += r.tried();
                totals.get(cls)[1] += r.caught();
            }
        }
        return totals;
    }
}

/** Tiny 64-bit LCG, identical in all three languages, so the random payloads are the same everywhere. */
final class SeededRandom {
    private long state;
    SeededRandom(long seed) { state = seed; }
    int nextInt(int bound) {
        state = state * 6364136223846793005L + 1442695040888963407L;
        return (int) Long.remainderUnsigned(state >>> 33, bound);
    }
}

public final class Main {
    static final String PAN_PAYLOAD = "453909012223456";          // 15 digits; contains "90", "09", and "22" on purpose
    static final String IBAN_SAMPLE = "DE89370400440532013000";   // the ISO 13616 worked example

    public static void main(String[] args) {
        System.out.println("== Euclid: gcd, Bezout, inverse ==");
        System.out.printf("gcd(1071, 462) = %d%n", Euclid.gcd(1071, 462));
        Euclid.Bezout bz = Euclid.extendedGcd(240, 46);
        System.out.printf("240 * (%d) + 46 * (%d) = %d%n", bz.x(), bz.y(), bz.g());
        System.out.printf("29/1000 reduced by gcd: %d/%d%n", 29 / Euclid.gcd(29, 1000), 1000 / Euclid.gcd(29, 1000));
        System.out.printf("denominations {6, 10} reach amount 15? gcd=%d divides 15? %b%n", Euclid.gcd(6, 10), 15 % Euclid.gcd(6, 10) == 0);
        System.out.printf("inverse of 100 mod 97 = %d  (100 * %d mod 97 = %d)%n", Euclid.modInverse(100, 97), Euclid.modInverse(100, 97), Modular.mod(100 * Euclid.modInverse(100, 97), 97));
        try {
            Euclid.modInverse(6, 10);
        } catch (ArithmeticException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        System.out.printf("3^200 mod 97 = %d (by repeated squaring); 10^30 mod 97 = %d%n", Modular.modPow(3, 200, 97), Modular.modPow(10, 30, 97));

        System.out.println("\n== Luhn (ISO/IEC 7812) ==");
        String pan = PAN_PAYLOAD + Luhn.checkDigit(PAN_PAYLOAD);
        System.out.printf("payload %s -> check digit %d -> PAN %s valid=%b%n", PAN_PAYLOAD, Luhn.checkDigit(PAN_PAYLOAD), pan, Luhn.validate(pan));
        System.out.printf("4539 1488 0343 6467 (a test PAN) valid=%b; with one digit changed valid=%b%n",
                Luhn.validate("4539148803436467"), Luhn.validate("4539148803436461"));

        System.out.println("\n== ISO 7064 MOD 97-10 (IBAN) ==");
        System.out.printf("%s -> numeric %s%n", IBAN_SAMPLE, Iso7064Mod97.ibanToNumeric(IBAN_SAMPLE));
        System.out.printf("mod 97 = %d  valid=%b%n", Modular.modOfDecimalString(Iso7064Mod97.ibanToNumeric(IBAN_SAMPLE), 97), Iso7064Mod97.validateIban(IBAN_SAMPLE));
        System.out.printf("check digits recomputed for DE + 370400440532013000 = %s%n", Iso7064Mod97.ibanCheckDigits("DE", "370400440532013000"));
        System.out.printf("GB82 WEST 1234 5698 7654 32 valid=%b; typo 5689 valid=%b%n",
                Iso7064Mod97.validateIban("GB82 WEST 1234 5698 7654 32"), Iso7064Mod97.validateIban("GB82 WEST 1234 5689 7654 32"));

        System.out.println("\n== Damm (quasigroup) ==");
        String dammId = PAN_PAYLOAD + Damm.checkDigit(PAN_PAYLOAD);
        System.out.printf("572 -> check digit %d; %s valid=%b%n", Damm.checkDigit("572"), dammId, Damm.validate(dammId));

        System.out.println("\n== Which errors does each scheme catch? Exhaustive on one sample ==");
        Validator luhn = Luhn::validate, iban = Iso7064Mod97::validateIban, damm = Damm::validate;
        System.out.printf("%-24s %-10s %-10s %-10s%n", "error class", "Luhn", "MOD 97-10", "Damm");
        for (ErrorClass cls : ErrorClass.values()) {
            System.out.printf("%-24s %-10s %-10s %-10s%n", cls,
                    ErrorAnalysis.analyse(luhn, pan, cls).rate(),
                    ErrorAnalysis.analyse(iban, IBAN_SAMPLE, cls).rate(),
                    ErrorAnalysis.analyse(damm, dammId, cls).rate());
        }
        System.out.println("Luhn misses (adjacent): " + ErrorAnalysis.analyse(luhn, pan, ErrorClass.ADJACENT_TRANSPOSITION).missed());
        System.out.println("Luhn misses (twin)    : " + ErrorAnalysis.analyse(luhn, pan, ErrorClass.TWIN).missed());

        System.out.println("\n== Aggregate over 200 seeded random 15-digit payloads (seed 7) ==");
        Map<ErrorClass, long[]> l = ErrorAnalysis.aggregate(luhn, p -> p + Luhn.checkDigit(p), 200, 15, 7);
        Map<ErrorClass, long[]> d = ErrorAnalysis.aggregate(damm, p -> p + Damm.checkDigit(p), 200, 15, 7);
        Map<ErrorClass, long[]> m = ErrorAnalysis.aggregate(iban, p -> Iso7064Mod97.buildIban("XX", p), 200, 15, 7);
        System.out.printf("%-24s %-22s %-22s %-22s%n", "error class", "Luhn", "MOD 97-10", "Damm");
        for (ErrorClass cls : ErrorClass.values()) {
            System.out.printf("%-24s %-22s %-22s %-22s%n", cls, pct(l.get(cls)), pct(m.get(cls)), pct(d.get(cls)));
        }
    }

    static String pct(long[] triedCaught) {
        return String.format("%d/%d (%.2f%%)", triedCaught[1], triedCaught[0], 100.0 * triedCaught[1] / triedCaught[0]);
    }
}
