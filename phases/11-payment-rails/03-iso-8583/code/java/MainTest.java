// Tests for phases/11-payment-rails/03-iso-8583. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static String ascii(byte[] b) { return new String(b, StandardCharsets.US_ASCII); }
    static byte[] bytes(String s) { return s.getBytes(StandardCharsets.US_ASCII); }

    static Iso8583Message purchase(Iso8583Spec spec) {
        return new Iso8583Message("0200", spec)
                .set(2, Main.TEST_PAN).set(3, "000000").setAmount(Money.ofMinor(1999, Currency.USD))
                .set(7, "1012143015").set(11, "000123").set(12, "143015").set(13, "1012").set(22, "051")
                .set(37, "123456789012").set(41, "TERM0001").set(42, "MERCHANT0000001");
    }

    static final String EXPECTED_WIRE = "0200" + "7238040008C08000"
            + "16" + "4111111111111111" + "000000" + "000000001999" + "1012143015" + "000123" + "143015" + "1012"
            + "051" + "123456789012" + "TERM0001" + "MERCHANT0000001" + "840";

    public static void main(String[] args) {
        Iso8583Spec spec = Iso8583Spec.standard1987();

        // bitmap arithmetic
        Bitmap bm = new Bitmap();
        for (int n : new int[] {2, 3, 4, 7, 11, 12, 13, 22, 37, 41, 42, 49}) bm.set(n);
        check("bitmap hex for the worked example", bm.toHex().equals("7238040008C08000"));
        check("bitmap round trip", Bitmap.fromHex("7238040008C08000").isSet(49) && !Bitmap.fromHex("7238040008C08000").isSet(39));
        Bitmap withResponse = Bitmap.fromHex("7238040008C08000");
        withResponse.set(39);
        check("adding DE39 flips byte 5 from 08 to 0A", withResponse.toHex().equals("723804000AC08000"));
        checkThrows("bitmap rejects non-hex", Iso8583Exception.class, () -> Bitmap.fromHex("ZZ38040008C08000"));
        checkThrows("bitmap rejects wrong length", Iso8583Exception.class, () -> Bitmap.fromHex("7238"));

        // MTI
        Iso8583Message req = purchase(spec);
        check("mti class is financial", req.mtiClass() == 2 && req.mtiFunction() == 0 && req.mtiVersion() == 0 && req.mtiOrigin() == 0);
        check("response MTI 0200 -> 0210", req.responseMti().equals("0210"));
        check("response MTI 0420 -> 0430", new Iso8583Message("0420", spec).responseMti().equals("0430"));
        checkThrows("response MTI of a response is an error", Iso8583Exception.class, () -> new Iso8583Message("0210", spec).responseMti());
        checkThrows("MTI must be four digits", Iso8583Exception.class, () -> new Iso8583Message("02A0", spec));
        checkThrows("MTI must be four characters", Iso8583Exception.class, () -> new Iso8583Message("200", spec));

        // encode: exact bytes of the worked example
        byte[] wire = req.encode();
        check("encoded length is 123 bytes", wire.length == 123);
        check("encoded bytes match the hand-built message", ascii(wire).equals(EXPECTED_WIRE));
        check("LLVAR prefix for 16-digit PAN is 16", ascii(wire).substring(20, 22).equals("16"));

        // decode and round trip
        Iso8583Message back = Iso8583Message.decode(wire, spec);
        check("decode MTI", back.mti().equals("0200"));
        check("decode PAN", back.get(2).equals(Main.TEST_PAN));
        check("decode amount as Money", back.amount().equals(Money.ofMinor(1999, Currency.USD)));
        check("decode STAN and RRN", back.get(11).equals("000123") && back.get(37).equals("123456789012"));
        check("decode preserves field set", back.fields().keySet().equals(req.fields().keySet()));
        check("round trip is byte-identical", Arrays.equals(wire, back.encode()));
        check("absent field reported absent", !back.has(39));
        checkThrows("get of absent field throws", Iso8583Exception.class, () -> back.get(39));

        // secondary bitmap
        Iso8583Message withAccount = new Iso8583Message("0200", spec)
                .set(2, Main.TEST_PAN).set(3, "000000").setAmount(Money.ofMinor(500000, Currency.INR))
                .set(11, "000124").set(102, "12345678901");
        String secondaryWire = ascii(withAccount.encode());
        check("bit 1 set when a DE above 64 is present", secondaryWire.substring(4, 20).equals("F020000000008000"));
        check("secondary bitmap carries bit 38 for DE102", secondaryWire.substring(20, 36).equals("0000000004000000"));
        Iso8583Message withAccountBack = Iso8583Message.decode(withAccount.encode(), spec);
        check("secondary round trip", withAccountBack.get(102).equals("12345678901") && withAccountBack.amount().equals(Money.ofMinor(500000, Currency.INR)));
        check("no secondary bitmap when all DEs are below 65", ascii(wire).charAt(4) == '7');

        // LLLVAR
        String privateData = "A".repeat(250);
        Iso8583Message priv = new Iso8583Message("0800", spec).set(11, "000001").set(123, privateData);
        String privWire = ascii(priv.encode());
        check("LLLVAR prefix is three digits", privWire.endsWith("250" + privateData));
        check("LLLVAR round trip", Iso8583Message.decode(priv.encode(), spec).get(123).equals(privateData));

        // field validation on set
        checkThrows("fixed field wrong length rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).set(3, "00000"));
        checkThrows("numeric field with a dot rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).set(4, "000000019.99"));
        checkThrows("LLVAR over max rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).set(2, "4".repeat(20)));
        checkThrows("undefined DE rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).set(64, "DEADBEEF"));
        checkThrows("empty value rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).set(41, ""));
        checkThrows("negative amount rejected", Iso8583Exception.class, () -> new Iso8583Message("0200", spec).setAmount(Money.ofMinor(-1, Currency.USD)));
        check("setAmount zero-pads to 12 and writes DE49", new Iso8583Message("0200", spec).setAmount(Money.ofMinor(5, Currency.JPY)).get(4).equals("000000000005")
                && new Iso8583Message("0200", spec).setAmount(Money.ofMinor(5, Currency.JPY)).get(49).equals("392"));
        check("JPY amount has no fraction", new Iso8583Message("0200", spec).setAmount(Money.ofMinor(1200, Currency.JPY)).amount().toString().equals("1200 JPY"));

        // decode: malformed input
        checkThrows("truncated message rejected", Iso8583Exception.class, () -> Iso8583Message.decode(bytes(EXPECTED_WIRE.substring(0, 60)), spec));
        checkThrows("trailing bytes rejected", Iso8583Exception.class, () -> Iso8583Message.decode(bytes(EXPECTED_WIRE + "XYZ"), spec));
        checkThrows("too short for header rejected", Iso8583Exception.class, () -> Iso8583Message.decode(bytes("0200723804"), spec));
        checkThrows("LLVAR length over max rejected on decode", Iso8583Exception.class,
                () -> Iso8583Message.decode(bytes("0200" + "7238040008C08000" + "99" + EXPECTED_WIRE.substring(22)), spec));
        checkThrows("non-numeric length prefix rejected", Iso8583Exception.class,
                () -> Iso8583Message.decode(bytes("0200" + "7238040008C08000" + "1A" + EXPECTED_WIRE.substring(22)), spec));
        checkThrows("bit set for undefined DE rejected", Iso8583Exception.class,
                () -> Iso8583Message.decode(bytes("0200" + "7238040008C08001" + EXPECTED_WIRE.substring(20) + "X"), spec)); // bit 64
        checkThrows("bit 1 without secondary bitmap rejected", Iso8583Exception.class,
                () -> Iso8583Message.decode(bytes("0200" + "F000000000000000"), spec));
        checkThrows("tertiary bitmap rejected", Iso8583Exception.class,
                () -> Iso8583Message.decode(bytes("0200" + "8000000000000000" + "8000000000000000"), spec));

        // masking
        check("masked PAN shows first six and last four", req.maskedPan().equals("411111******1111"));
        check("dump never contains the full PAN", !req.dump().contains(Main.TEST_PAN) && req.dump().contains("411111******1111"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
