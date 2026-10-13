// Tests for phases/02-algorithms/15-numerical-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.math.BigInteger;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) {
        // Euclid
        check("gcd(1071, 462) = 21", Euclid.gcd(1071, 462) == 21);
        check("gcd is symmetric and handles zero", Euclid.gcd(462, 1071) == 21 && Euclid.gcd(0, 5) == 5 && Euclid.gcd(0, 0) == 0);
        check("gcd of negatives is non-negative", Euclid.gcd(-12, 18) == 6);
        check("lcm(4, 6) = 12", Euclid.lcm(4, 6) == 12);
        Euclid.Bezout b = Euclid.extendedGcd(240, 46);
        check("extended gcd returns g = 2", b.g() == 2);
        check("Bezout identity holds", 240 * b.x() + 46 * b.y() == b.g());
        Euclid.Bezout b2 = Euclid.extendedGcd(17, 5);
        check("Bezout identity holds for coprime pair", 17 * b2.x() + 5 * b2.y() == 1);
        check("modInverse(100, 97) = 65", Euclid.modInverse(100, 97) == 65 && (100 * 65) % 97 == 1);
        check("modInverse(3, 11) = 4", Euclid.modInverse(3, 11) == 4);
        checkThrows("no inverse when not coprime", ArithmeticException.class, () -> Euclid.modInverse(6, 10));
        check("coin-change reachability is a gcd question", 15 % Euclid.gcd(6, 10) != 0 && 16 % Euclid.gcd(6, 10) == 0);

        // Modular
        check("mod keeps results non-negative", Modular.mod(-7, 5) == 3 && Modular.mod(7, 5) == 2 && Modular.mod(-10, 5) == 0);
        check("modPow 3^200 mod 97 = 62", Modular.modPow(3, 200, 97) == 62);
        check("modPow matches BigInteger", Modular.modPow(7, 123456, 1_000_003) == BigInteger.valueOf(7).modPow(BigInteger.valueOf(123456), BigInteger.valueOf(1_000_003)).longValue());
        check("modPow exponent 0 is 1", Modular.modPow(5, 0, 13) == 1);
        String thirty = "370400440532013000131489123456";
        check("Horner mod matches BigInteger on a 30-digit string", Modular.modOfDecimalString(thirty, 97) == new BigInteger(thirty).mod(BigInteger.valueOf(97)).longValue());
        checkThrows("modOfDecimalString rejects letters", IllegalArgumentException.class, () -> Modular.modOfDecimalString("12A4", 97));

        // Luhn
        check("Luhn check digit for 453909012223456 is 3", Luhn.checkDigit("453909012223456") == 3);
        check("Luhn validates the completed PAN", Luhn.validate("4539090122234563"));
        check("Luhn validates a known test PAN", Luhn.validate("4539148803436467"));
        check("Luhn rejects a single-digit error", !Luhn.validate("4539148803436461"));
        check("Luhn check digit for 7992739871 is 3 (ISO example)", Luhn.checkDigit("7992739871") == 3 && Luhn.validate("79927398713"));
        check("Luhn rejects non-digits and short input", !Luhn.validate("4539-1488") && !Luhn.validate("7"));
        check("Luhn misses the 09 <-> 90 transposition", Luhn.validate("4539090122234563") && Luhn.validate("4530990122234563"));
        check("Luhn misses the 22 <-> 55 twin error", Luhn.validate("4539090155234563"));

        // ISO 7064 MOD 97-10 / IBAN
        check("IBAN numeric rearrangement", Iso7064Mod97.ibanToNumeric("DE89370400440532013000").equals("370400440532013000131489"));
        check("DE worked example validates", Iso7064Mod97.validateIban("DE89370400440532013000"));
        check("IBAN with spaces and lowercase validates", Iso7064Mod97.validateIban("gb82 west 1234 5698 7654 32"));
        check("IBAN typo rejected", !Iso7064Mod97.validateIban("GB82 WEST 1234 5689 7654 32"));
        check("IBAN check digits recomputed = 89", Iso7064Mod97.ibanCheckDigits("DE", "370400440532013000").equals("89"));
        check("buildIban round-trips", Iso7064Mod97.validateIban(Iso7064Mod97.buildIban("GB", "WEST12345698765432")) && Iso7064Mod97.buildIban("GB", "WEST12345698765432").equals("GB82WEST12345698765432"));
        check("IBAN check digits are zero-padded", Iso7064Mod97.ibanCheckDigits("XX", "000000000000001").length() == 2);
        check("invalid characters make an IBAN invalid, not an exception", !Iso7064Mod97.validateIban("DE89 3704 0044 0532 0130 0$"));
        check("too-short IBAN is invalid", !Iso7064Mod97.validateIban("DE8"));

        // Damm
        check("Damm check digit for 572 is 4", Damm.checkDigit("572") == 4 && Damm.validate("5724"));
        check("Damm table is a Latin square", isLatinSquare(Damm.TABLE));
        check("Damm table has zero diagonal", zeroDiagonal(Damm.TABLE));
        check("Damm rejects a changed digit", !Damm.validate("5725") && !Damm.validate("5734"));
        check("Damm catches adjacent transposition", !Damm.validate("7524"));
        check("Damm rejects empty and non-digit input", !Damm.validate("") && !Damm.validate("57a4"));

        // Error analysis on the sample identifiers
        String pan = "4539090122234563";
        String iban = "DE89370400440532013000";
        String dammId = "453909012223456" + Damm.checkDigit("453909012223456");
        ErrorAnalysis.Report r;
        r = ErrorAnalysis.analyse(Luhn::validate, pan, ErrorClass.SINGLE_DIGIT);
        check("Luhn catches every single-digit error (144/144)", r.tried() == 144 && r.caught() == 144);
        r = ErrorAnalysis.analyse(Luhn::validate, pan, ErrorClass.ADJACENT_TRANSPOSITION);
        check("Luhn misses exactly the three 09/90 transpositions", r.tried() == 13 && r.missedCount() == 3);
        r = ErrorAnalysis.analyse(Luhn::validate, pan, ErrorClass.TWIN);
        check("Luhn misses the two 22->55 twin errors", r.tried() == 18 && r.missedCount() == 2 && r.missed().equals(List.of("4539090155234563", "4539090125534563")));
        r = ErrorAnalysis.analyse(Luhn::validate, pan, ErrorClass.JUMP_TRANSPOSITION);
        check("Luhn catches no jump transpositions (same weight two apart)", r.tried() == 11 && r.caught() == 0);
        for (ErrorClass cls : ErrorClass.values()) {
            r = ErrorAnalysis.analyse(Iso7064Mod97::validateIban, iban, cls);
            check("MOD 97-10 catches every " + cls, r.tried() > 0 && r.missedCount() == 0);
        }
        r = ErrorAnalysis.analyse(Damm::validate, dammId, ErrorClass.SINGLE_DIGIT);
        check("Damm catches every single-digit error", r.tried() == 144 && r.caught() == 144);
        r = ErrorAnalysis.analyse(Damm::validate, dammId, ErrorClass.ADJACENT_TRANSPOSITION);
        check("Damm catches every adjacent transposition", r.tried() == 13 && r.caught() == 13);
        r = ErrorAnalysis.analyse(Damm::validate, dammId, ErrorClass.TWIN);
        check("Damm does not promise twin errors", r.missedCount() > 0);
        check("corruptions skip letters in an IBAN", ErrorAnalysis.corruptions(iban, ErrorClass.SINGLE_DIGIT).size() == 20 * 9);
        checkThrows("analyse refuses an invalid sample", IllegalArgumentException.class, () -> ErrorAnalysis.analyse(Luhn::validate, "4539090122234560", ErrorClass.SINGLE_DIGIT));

        // Seeded aggregate: deterministic across runs and languages
        Map<ErrorClass, long[]> agg = ErrorAnalysis.aggregate(Luhn::validate, p -> p + Luhn.checkDigit(p), 50, 15, 7);
        check("aggregate: Luhn single-digit 7200/7200", agg.get(ErrorClass.SINGLE_DIGIT)[0] == 7200 && agg.get(ErrorClass.SINGLE_DIGIT)[1] == 7200);
        check("aggregate: Luhn adjacent transposition misses some", agg.get(ErrorClass.ADJACENT_TRANSPOSITION)[1] < agg.get(ErrorClass.ADJACENT_TRANSPOSITION)[0]);
        check("aggregate: Luhn jump transposition catches none", agg.get(ErrorClass.JUMP_TRANSPOSITION)[1] == 0 && agg.get(ErrorClass.JUMP_TRANSPOSITION)[0] > 0);
        SeededRandom rng = new SeededRandom(7);
        check("seeded random first values are 8, 1, 3", rng.nextInt(10) == 8 && rng.nextInt(10) == 1 && rng.nextInt(10) == 3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean isLatinSquare(int[][] t) {
        for (int i = 0; i < 10; i++) {
            int row = 0, col = 0;
            for (int j = 0; j < 10; j++) {
                row |= 1 << t[i][j];
                col |= 1 << t[j][i];
            }
            if (row != 0x3FF || col != 0x3FF) return false;
        }
        return true;
    }

    static boolean zeroDiagonal(int[][] t) {
        for (int i = 0; i < 10; i++) if (t[i][i] != 0) return false;
        return true;
    }
}
