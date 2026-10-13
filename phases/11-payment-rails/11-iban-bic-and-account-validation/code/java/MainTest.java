// Tests for phases/11-payment-rails/11-iban-bic-and-account-validation. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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

    // One registry example per supported country; every one must pass structure, length, and mod-97.
    static final List<String> REGISTRY_EXAMPLES = List.of(
            "AE070331234567890123456", "AT611904300234573201", "BE68539007547034", "CH9300762011623852957",
            "DE89370400440532013000", "DK5000400440116243", "ES9121000418450200051332", "FI2112345600000785",
            "FR1420041010050500013M02606", "GB82WEST12345698765432", "IE29AIBK93115212345678",
            "IT60X0542811101000000123456", "LU280019400644750000", "NL91ABNA0417164300", "NO9386011117947",
            "PL61109010140000071219812874", "PT50000201231234567890154", "SA0380000000608010167519",
            "SE4550000000058398257466");

    public static void main(String[] args) {
        // mod-97
        check("mod97 digits only", Registry.mod97("1234567890") == 1234567890L % 97);
        check("mod97 letters expand to two digits (AZ = 1035)", Registry.mod97("AZ") == 1035 % 97);
        check("mod97 rearranged valid IBAN is 1", Registry.mod97("WEST12345698765432GB82") == 1);
        checkThrows("mod97 rejects other characters", IllegalArgumentException.class, () -> Registry.mod97("12-34"));

        // IBAN happy path
        for (String example : REGISTRY_EXAMPLES) {
            ValidationResult r = Iban.validate(example);
            check("registry example valid " + example, r.valid() && r.issues().isEmpty());
            check("generate reproduces check digits " + example, Iban.checkDigitsFor(example.substring(0, 2), example.substring(4)).equals(example.substring(2, 4)));
            check("generate round-trips " + example, Iban.generate(example.substring(0, 2), example.substring(4)).value().equals(example));
        }
        check("every table country has an example", Registry.COUNTRY_FORMATS.keySet().stream().allMatch(c -> REGISTRY_EXAMPLES.stream().anyMatch(e -> e.startsWith(c))));
        check("normalize paper form", Registry.normalize("gb82 west-1234 5698 7654 32").equals("GB82WEST12345698765432"));
        check("validate accepts paper form", Iban.validate("gb82 west 1234 5698 7654 32").valid());

        Iban gb = Iban.parse("GB82 WEST 1234 5698 7654 32");
        check("parse country", gb.country().equals("GB"));
        check("parse check digits", gb.checkDigits().equals("82"));
        check("parse bban", gb.bban().equals("WEST12345698765432"));
        check("parse bank identifier", gb.bankIdentifier().equals("WEST"));
        check("parse branch (sort code)", gb.branchIdentifier().equals("123456"));
        check("formatted paper form", gb.formatted().equals("GB82 WEST 1234 5698 7654 32"));
        Iban it = Iban.parse("IT60X0542811101000000123456");
        check("italian bank identifier skips CIN", it.bankIdentifier().equals("05428") && it.branchIdentifier().equals("11101"));
        check("country without branch gives null", Iban.parse("DE89370400440532013000").branchIdentifier() == null);

        // IBAN failures the lesson warns about
        for (String example : REGISTRY_EXAMPLES) {
            char last = example.charAt(example.length() - 1);
            String mutated = example.substring(0, example.length() - 1) + (last != '1' ? '1' : '2');
            ValidationResult r = Iban.validate(mutated);
            check("single digit error detected " + mutated, r.has(Reason.CHECKSUM_FAILED) || r.has(Reason.BBAN_STRUCTURE));
        }
        ValidationResult transposed = Iban.validate("GB82WEST12345698765423");
        check("adjacent transposition detected", !transposed.valid() && transposed.has(Reason.CHECKSUM_FAILED));
        ValidationResult shortIban = Iban.validate("DE8937040044053201300");
        check("wrong length reported alone", shortIban.issues().size() == 1 && shortIban.has(Reason.WRONG_LENGTH));
        ValidationResult unknown = Iban.validate("XX82WEST12345698765432");
        check("unknown country reported alone", unknown.issues().size() == 1 && unknown.has(Reason.UNKNOWN_COUNTRY));
        check("invalid characters", Iban.validate("GB82 WEST 1234 5698 7654 3*").has(Reason.INVALID_CHARACTERS));
        ValidationResult structure = Iban.validate("GB82WEST1234569876543X");
        check("bban structure letters where digits expected", structure.has(Reason.BBAN_STRUCTURE));
        check("bban structure message names positions", structure.issues().stream().anyMatch(i -> i.message().contains("positions 11-18")));
        for (String bad : List.of("00", "01", "99")) {
            check("structurally invalid check digits " + bad, Iban.validate("GB" + bad + "WEST12345698765432").has(Reason.INVALID_CHECK_DIGITS));
        }
        check("empty", Iban.validate("   ").has(Reason.EMPTY));
        checkThrows("generate rejects wrong bban length", IllegalArgumentException.class, () -> Iban.checkDigitsFor("DE", "12345"));
        checkThrows("parse throws with reasons", IllegalArgumentException.class, () -> Iban.parse("DE8937040044053201300"));
        try { Iban.parse("DE8937040044053201300"); } catch (IllegalArgumentException e) { check("parse message names reason", e.getMessage().contains("WRONG_LENGTH")); }

        // BIC
        check("bic8 valid", Bic.validate("DEUTDEFF").valid());
        check("bic11 valid", Bic.validate("DEUTDEFF500").valid());
        check("bic with digit in location valid", Bic.validate("NWBKGB2L").valid());
        Bic deut = Bic.parse("deutdeff");
        check("bic parse parts", deut.institution().equals("DEUT") && deut.country().equals("DE") && deut.location().equals("FF") && deut.branch().equals("XXX"));
        check("bic8 accessor", deut.bic8().equals("DEUTDEFF") && !deut.isTest());
        ValidationResult testBic = Bic.validate("DEUTDEF0");
        check("test BIC is a warning not an error", testBic.valid() && testBic.issues().get(0).severity() == Severity.WARNING);
        check("test BIC flag", Bic.parse("DEUTDEF0").isTest());
        for (String bad : List.of("DEUTDE", "DEUTDEFFX", "DEUTDEFF5000")) {
            check("bic wrong length " + bad, Bic.validate(bad).has(Reason.WRONG_LENGTH));
        }
        check("bic unknown country", Bic.validate("DEUTZZFF").has(Reason.UNKNOWN_COUNTRY));
        check("bic digit in country position", Bic.validate("DEUT1EFF").has(Reason.INVALID_CHARACTERS));
        check("bic invalid characters", Bic.validate("DEUT.EFF").has(Reason.INVALID_CHARACTERS));

        // ABA
        check("aba check digit 02199999 -> 6", RoutingNumber.checkDigit("02199999") == '6');
        check("aba check digit 01199999 -> 3", RoutingNumber.checkDigit("01199999") == '3');
        check("aba check digit 11100002 -> 5", RoutingNumber.checkDigit("11100002") == '5');
        for (String rtn : List.of("021999996", "011999993", "061999998", "121999993")) {
            check("aba valid " + rtn, RoutingNumber.validate(rtn).valid());
        }
        check("aba transposition detected", RoutingNumber.validate("021999969").has(Reason.CHECKSUM_FAILED));
        ValidationResult prefix = RoutingNumber.validate("991999990");
        check("aba prefix outside ranges reported alone", prefix.issues().size() == 1 && prefix.has(Reason.INVALID_PREFIX));
        check("aba wrong length", RoutingNumber.validate("02199999").has(Reason.WRONG_LENGTH));
        check("aba invalid characters", RoutingNumber.validate("02199999A").has(Reason.INVALID_CHARACTERS));
        check("aba empty", RoutingNumber.validate("").has(Reason.EMPTY));
        checkThrows("aba check digit requires eight digits", IllegalArgumentException.class, () -> RoutingNumber.checkDigit("0219999"));

        // IBAN + BIC pair
        ValidationResult pair = AccountValidator.validatePair("DE89 3704 0044 0532 0130 00", "DEUTDEFF");
        check("matching pair has no issues", pair.valid() && pair.issues().isEmpty());
        ValidationResult mismatch = AccountValidator.validatePair("DE89 3704 0044 0532 0130 00", "NWBKGB2L");
        check("country mismatch is a warning", mismatch.valid() && mismatch.has(Reason.COUNTRY_MISMATCH));
        ValidationResult badPair = AccountValidator.validatePair("DE8937040044053201300", "DEUTDEFF");
        check("invalid iban propagates without mismatch", !badPair.valid() && badPair.has(Reason.WRONG_LENGTH) && !badPair.has(Reason.COUNTRY_MISMATCH));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
