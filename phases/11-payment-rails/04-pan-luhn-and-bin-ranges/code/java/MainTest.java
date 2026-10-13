// Tests for phases/11-payment-rails/04-pan-luhn-and-bin-ranges. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Random;

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
        // Luhn: published test numbers are valid
        check("luhn visa 4111111111111111", Luhn.isValid("4111111111111111"));
        check("luhn visa 4242424242424242", Luhn.isValid("4242424242424242"));
        check("luhn mastercard 5555555555554444", Luhn.isValid("5555555555554444"));
        check("luhn mastercard 2-series 2223003122003222", Luhn.isValid("2223003122003222"));
        check("luhn amex 378282246310005", Luhn.isValid("378282246310005"));
        check("luhn discover 6011111111111117", Luhn.isValid("6011111111111117"));
        check("luhn ISO 7812 example 7992739871 + 3", Luhn.isValid("79927398713"));

        // Luhn: what it catches and what it misses
        check("luhn catches a single-digit error", !Luhn.isValid("4111111111111112"));
        check("luhn catches an adjacent transposition 42 -> 24", !Luhn.isValid("4422424242424242"));
        check("luhn misses the 09 <-> 90 transposition", Luhn.isValid("091") && Luhn.isValid("901"));
        check("luhn check digit for 7992739871 is 3", Luhn.checkDigit("7992739871") == 3);
        check("luhn check digit for 411111111111111 is 1", Luhn.checkDigit("411111111111111") == 1);
        check("luhn complete appends the digit", Luhn.complete("411111111111111").equals("4111111111111111"));
        check("luhn complete is always valid (seeded random partials)", randomCompletesAreValid(1000));
        check("luhn check digit when sum is a multiple of 10 is 0", Luhn.checkDigit("0") == 0);
        checkThrows("luhn rejects non-digits", IllegalArgumentException.class, () -> Luhn.isValid("4111-1111"));
        checkThrows("luhn rejects empty", IllegalArgumentException.class, () -> Luhn.isValid(""));

        // PAN parsing and masking
        Pan visa = Pan.parse("4111 1111 1111 1111");
        check("parse strips spaces", visa.digits().equals("4111111111111111"));
        check("parse strips hyphens", Pan.parse("5555-5555-5555-4444").length() == 16);
        check("mask default is 6 + 4", visa.masked().equals("411111******1111"));
        check("mask with 8-digit BIN", visa.masked(8).equals("41111111****1111"));
        check("mask with last four only", visa.masked(0).equals("************1111"));
        check("toString is masked", visa.toString().equals("411111******1111"));
        check("amex mask has 15 characters", Pan.parse("378282246310005").masked().length() == 15);
        check("bin(6) and bin(8)", visa.bin(6).equals("411111") && visa.bin(8).equals("41111111"));
        check("MII of an Amex is 3", Pan.parse("378282246310005").majorIndustryIdentifier() == 3);
        checkThrows("reject Luhn failure", IllegalArgumentException.class, () -> Pan.parse("4111111111111112"));
        checkThrows("reject too short", IllegalArgumentException.class, () -> Pan.parse("79927398713"));
        checkThrows("reject 20 digits", IllegalArgumentException.class, () -> Pan.parse(Luhn.complete("4000000000000000000")));
        checkThrows("reject letters", IllegalArgumentException.class, () -> Pan.parse("4111 1111 1111 111a"));
        checkThrows("mask may not reveal more than 8 leading digits", IllegalArgumentException.class, () -> visa.masked(9));
        checkThrows("mask may not reveal the whole 12-digit PAN", IllegalArgumentException.class,
                () -> new Pan(Luhn.complete("40000000000")).masked(8));

        // BIN range normalization
        check("6-digit BIN low bound", BinRange.normalizeLow("411111") == 41111100L);
        check("6-digit BIN high bound", BinRange.normalizeHigh("411111") == 41111199L);
        check("8-digit BIN is a single point", BinRange.normalizeLow("55555555") == BinRange.normalizeHigh("55555555"));
        check("network prefix range 2221-2720", BinRange.ofPrefixRange("2221", "2720", Network.MASTERCARD, "n", "p").low() == 22210000L
                && BinRange.ofPrefixRange("2221", "2720", Network.MASTERCARD, "n", "p").high() == 27209999L);
        checkThrows("prefix range bounds must be the same length", IllegalArgumentException.class,
                () -> BinRange.ofPrefixRange("51", "555", Network.MASTERCARD, "n", "p"));
        checkThrows("overlapping ranges are rejected", IllegalArgumentException.class, () -> new BinTable(List.of(
                BinRange.ofPrefix("4111", Network.VISA, "a", "p"),
                BinRange.ofPrefix("411111", Network.VISA, "b", "p"))));

        // Binary search lookup
        BinTable issuers = Main.sampleIssuerTable();
        check("lookup 6-digit BIN hit", issuers.lookup(visa).map(BinRange::issuer).orElse("").equals("Test Bank A"));
        check("lookup 8-digit BIN hit", issuers.lookup(Pan.parse("5555555555554444")).map(BinRange::product).orElse("").contains("8-digit"));
        check("lookup 8-digit BIN miss one digit off", issuers.lookup(55555556_00L).isEmpty() && issuers.lookup(55555554L * 100 + 99).isEmpty());
        check("lookup below the first range is empty", issuers.lookup(0L).isEmpty());
        check("lookup above the last range is empty", issuers.lookup(BinRange.MAX_KEY).isEmpty());
        check("lookup in a gap between ranges is empty", issuers.lookup(41111200L).isEmpty());
        check("lookup at exact low bound", issuers.lookup(41111100L).isPresent());
        check("lookup at exact high bound", issuers.lookup(41111199L).isPresent());
        check("binary search agrees with linear scan on 10k random keys", binarySearchMatchesLinear(issuers, 10_000));

        // Network detection
        check("visa by 4", BinTable.detectNetwork(visa) == Network.VISA);
        check("mastercard 5-series", BinTable.detectNetwork(Pan.parse("5555555555554444")) == Network.MASTERCARD);
        check("mastercard 2-series", BinTable.detectNetwork(Pan.parse("2223003122003222")) == Network.MASTERCARD);
        check("2720 is still mastercard", BinTable.networkRanges().lookup(27209999L).map(BinRange::network).orElse(Network.UNKNOWN) == Network.MASTERCARD);
        check("2721 is not mastercard", BinTable.networkRanges().lookup(27210000L).isEmpty());
        check("2220 is not mastercard", BinTable.networkRanges().lookup(22209999L).isEmpty());
        check("amex 37", BinTable.detectNetwork(Pan.parse("378282246310005")) == Network.AMEX);
        check("amex 34", BinTable.detectNetwork(Pan.parse("340000000000009")) == Network.AMEX);
        check("discover 6011", BinTable.detectNetwork(Pan.parse("6011111111111117")) == Network.DISCOVER);
        check("discover 65", BinTable.detectNetwork(Pan.parse("6500000000000002")) == Network.DISCOVER);
        check("unknown prefix", BinTable.detectNetwork(new Pan(Luhn.complete("630400000000000"))) == Network.UNKNOWN);

        // Network length rules
        check("visa 16 ok", BinTable.lengthAllowed(Network.VISA, 16));
        check("visa 13 ok, 15 not", BinTable.lengthAllowed(Network.VISA, 13) && !BinTable.lengthAllowed(Network.VISA, 15));
        check("amex is 15 only", BinTable.lengthAllowed(Network.AMEX, 15) && !BinTable.lengthAllowed(Network.AMEX, 16));
        check("mastercard is 16 only", !BinTable.lengthAllowed(Network.MASTERCARD, 19));

        // End-to-end routing
        CardRouter router = new CardRouter(issuers);
        RoutingDecision d = router.route("2223 0031 2200 3222");
        check("route: network, issuer, length", d.network() == Network.MASTERCARD && d.issuerRange().isPresent() && d.lengthOk());
        check("route: unknown issuer BIN still detects network", router.route("4000056655665556").issuerRange().isEmpty()
                && router.route("4000056655665556").network() == Network.VISA);
        check("route: describe is masked", !d.describe().contains("2223003122003222") && d.describe().startsWith("222300******3222"));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean randomCompletesAreValid(int n) {
        Random rnd = new Random(42);
        for (int i = 0; i < n; i++) {
            int len = 11 + rnd.nextInt(8);
            StringBuilder sb = new StringBuilder();
            for (int j = 0; j < len; j++) sb.append((char) ('0' + rnd.nextInt(10)));
            if (!Luhn.isValid(Luhn.complete(sb.toString()))) return false;
        }
        return true;
    }

    static boolean binarySearchMatchesLinear(BinTable table, int n) {
        Random rnd = new Random(7);
        List<BinRange> all = List.of(
                BinRange.ofPrefix("411111", Network.VISA, "Test Bank A", "x"),
                BinRange.ofPrefix("424242", Network.VISA, "Test Bank B", "x"),
                BinRange.ofPrefix("55555555", Network.MASTERCARD, "Test Bank C", "x"),
                BinRange.ofPrefix("222300", Network.MASTERCARD, "Test Bank D", "x"),
                BinRange.ofPrefix("378282", Network.AMEX, "Amex (test)", "x"),
                BinRange.ofPrefix("601111", Network.DISCOVER, "Discover (test)", "x"));
        for (int i = 0; i < n; i++) {
            long key = i % 2 == 0 ? rnd.nextLong(BinRange.MAX_KEY + 1) : 41111000L + rnd.nextInt(400);  // half the keys near a boundary
            Optional<String> linear = all.stream().filter(r -> r.contains(key)).map(BinRange::issuer).findFirst();
            Optional<String> binary = table.lookup(key).map(BinRange::issuer);
            if (!linear.equals(binary)) return false;
        }
        return true;
    }
}
