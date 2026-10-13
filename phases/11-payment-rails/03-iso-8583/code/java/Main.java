// Lesson: phases/11-payment-rails/03-iso-8583/docs/en.md
// ISO 8583: Parsing the Card Network Message
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 8583-1:1987 (MTI, bitmaps, data element dictionary); ISO 4217 numeric currency codes.
// Java 21, standard library only. ASCII encoding variant; all PANs are published test numbers.

import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency with its numeric code, the form DE49 carries (840 = USD). */
record Currency(String code, int numericCode, int exponent) {
    static final Currency USD = new Currency("USD", 840, 2);
    static final Currency EUR = new Currency("EUR", 978, 2);
    static final Currency GBP = new Currency("GBP", 826, 2);
    static final Currency INR = new Currency("INR", 356, 2);
    static final Currency JPY = new Currency("JPY", 392, 0);
    static final Currency KWD = new Currency("KWD", 414, 3);

    static Currency fromNumeric(String threeDigits) {
        for (Currency c : new Currency[] {USD, EUR, GBP, INR, JPY, KWD}) {
            if (String.format("%03d", c.numericCode).equals(threeDigits)) return c;
        }
        throw new Iso8583Exception("unknown numeric currency code " + threeDigits);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus currency, copied from Phase 10, Lesson 01. DE4 is always minor units. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

final class Iso8583Exception extends IllegalArgumentException {
    Iso8583Exception(String message) { super(message); }
}

/** How a data element's length is known: fixed by the spec, or announced by a 2- or 3-digit prefix. */
enum FieldType {
    FIXED(0), LLVAR(2), LLLVAR(3);

    final int prefixDigits;
    FieldType(int prefixDigits) { this.prefixDigits = prefixDigits; }
}

/** One row of the data element dictionary: number, length rule, length (exact or maximum), character class. */
record FieldSpec(int number, FieldType type, int length, boolean numeric, String name) {
    FieldSpec {
        if (number < 2 || number > 128) throw new IllegalArgumentException("data element number must be 2..128: " + number);
        if (length <= 0) throw new IllegalArgumentException("length must be positive");
        if (type == FieldType.LLVAR && length > 99) throw new IllegalArgumentException("LLVAR max length is 99");
        if (type == FieldType.LLLVAR && length > 999) throw new IllegalArgumentException("LLLVAR max length is 999");
    }

    /** Reject a value that does not fit this element. Called on set and again on encode. */
    void validate(String value) {
        if (value == null || value.isEmpty()) throw new Iso8583Exception("DE" + number + " (" + name + ") must not be empty");
        if (type == FieldType.FIXED && value.length() != length) {
            throw new Iso8583Exception("DE" + number + " (" + name + ") must be exactly " + length + " chars, got " + value.length());
        }
        if (type != FieldType.FIXED && value.length() > length) {
            throw new Iso8583Exception("DE" + number + " (" + name + ") exceeds max length " + length + ": " + value.length());
        }
        if (numeric && !value.chars().allMatch(Character::isDigit)) {
            throw new Iso8583Exception("DE" + number + " (" + name + ") must be numeric");
        }
        for (char ch : value.toCharArray()) {
            if (ch < 0x20 || ch > 0x7E) throw new Iso8583Exception("DE" + number + " contains a non-ASCII character");
        }
    }
}

/** The practical subset of the ISO 8583-1:1987 dictionary this lesson implements. */
final class Iso8583Spec {
    private final Map<Integer, FieldSpec> fields = new TreeMap<>();

    static Iso8583Spec standard1987() {
        Iso8583Spec s = new Iso8583Spec();
        s.add(new FieldSpec(2, FieldType.LLVAR, 19, true, "primary account number"));
        s.add(new FieldSpec(3, FieldType.FIXED, 6, true, "processing code"));
        s.add(new FieldSpec(4, FieldType.FIXED, 12, true, "amount, transaction"));
        s.add(new FieldSpec(7, FieldType.FIXED, 10, true, "transmission date and time MMDDhhmmss"));
        s.add(new FieldSpec(11, FieldType.FIXED, 6, true, "system trace audit number"));
        s.add(new FieldSpec(12, FieldType.FIXED, 6, true, "time, local transaction hhmmss"));
        s.add(new FieldSpec(13, FieldType.FIXED, 4, true, "date, local transaction MMDD"));
        s.add(new FieldSpec(22, FieldType.FIXED, 3, true, "point of service entry mode"));
        s.add(new FieldSpec(37, FieldType.FIXED, 12, false, "retrieval reference number"));
        s.add(new FieldSpec(39, FieldType.FIXED, 2, false, "response code"));
        s.add(new FieldSpec(41, FieldType.FIXED, 8, false, "card acceptor terminal id"));
        s.add(new FieldSpec(42, FieldType.FIXED, 15, false, "card acceptor id"));
        s.add(new FieldSpec(49, FieldType.FIXED, 3, true, "currency code, transaction"));
        s.add(new FieldSpec(102, FieldType.LLVAR, 28, false, "account identification 1"));
        s.add(new FieldSpec(123, FieldType.LLLVAR, 999, false, "reserved for private use"));
        return s;
    }

    Iso8583Spec add(FieldSpec spec) {
        fields.put(spec.number(), spec);
        return this;
    }

    FieldSpec require(int number) {
        FieldSpec spec = fields.get(number);
        if (spec == null) throw new Iso8583Exception("DE" + number + " is not defined in this specification");
        return spec;
    }

    boolean defines(int number) { return fields.containsKey(number); }
}

/** The 8-byte presence map. Bit 1 is the most significant bit of the first byte and announces a second bitmap. */
final class Bitmap {
    private final boolean[] bits = new boolean[65]; // index 1..64

    void set(int bit) { bits[bit] = true; }
    boolean isSet(int bit) { return bits[bit]; }

    /** 16 uppercase hex characters, the ASCII variant of the 8 binary bytes. */
    String toHex() {
        StringBuilder sb = new StringBuilder(16);
        for (int b = 0; b < 8; b++) {
            int value = 0;
            for (int i = 0; i < 8; i++) {
                if (bits[b * 8 + i + 1]) value |= 1 << (7 - i);
            }
            sb.append(String.format("%02X", value));
        }
        return sb.toString();
    }

    static Bitmap fromHex(String hex) {
        if (hex.length() != 16) throw new Iso8583Exception("bitmap must be 16 hex characters, got " + hex.length());
        Bitmap bm = new Bitmap();
        for (int b = 0; b < 8; b++) {
            int value;
            try {
                value = Integer.parseInt(hex.substring(b * 2, b * 2 + 2), 16);
            } catch (NumberFormatException e) {
                throw new Iso8583Exception("bitmap is not hexadecimal: " + hex);
            }
            for (int i = 0; i < 8; i++) {
                if ((value & (1 << (7 - i))) != 0) bm.bits[b * 8 + i + 1] = true;
            }
        }
        return bm;
    }
}

/**
 * One ISO 8583 message: a four-digit MTI and a sorted map of present data elements.
 * Values are stored as strings in their on-the-wire character form; interpretation (amount, currency)
 * happens in typed accessors so the wire format and the domain type never get confused.
 */
final class Iso8583Message {
    private final String mti;
    private final Iso8583Spec spec;
    private final TreeMap<Integer, String> fields = new TreeMap<>();

    Iso8583Message(String mti, Iso8583Spec spec) {
        if (mti == null || mti.length() != 4 || !mti.chars().allMatch(Character::isDigit)) {
            throw new Iso8583Exception("MTI must be four digits: " + mti);
        }
        this.mti = mti;
        this.spec = Objects.requireNonNull(spec);
    }

    String mti() { return mti; }

    /** 0 = 1987, 1 = 1993, 2 = 2003. This lesson only encodes version 0. */
    int mtiVersion() { return mti.charAt(0) - '0'; }
    /** 1 = authorization, 2 = financial, 4 = reversal, 8 = network management. */
    int mtiClass() { return mti.charAt(1) - '0'; }
    /** 0 = request, 1 = request response, 2 = advice, 3 = advice response, 4 = notification. */
    int mtiFunction() { return mti.charAt(2) - '0'; }
    /** 0 = acquirer, 1 = acquirer repeat, 2 = issuer, 3 = issuer repeat. */
    int mtiOrigin() { return mti.charAt(3) - '0'; }

    /** The MTI a responder uses: 0100 -> 0110, 0200 -> 0210, 0420 -> 0430. Only requests and advices have one. */
    String responseMti() {
        int function = mtiFunction();
        if (function != 0 && function != 2) throw new Iso8583Exception("MTI " + mti + " is not a request or advice");
        return mti.substring(0, 2) + (function + 1) + mti.substring(3);
    }

    Iso8583Message set(int number, String value) {
        spec.require(number).validate(value);
        fields.put(number, value);
        return this;
    }

    String get(int number) {
        String v = fields.get(number);
        if (v == null) throw new Iso8583Exception("DE" + number + " is not present in this message");
        return v;
    }

    boolean has(int number) { return fields.containsKey(number); }

    Map<Integer, String> fields() { return Map.copyOf(fields); }

    /** DE4 (12 digits, minor units) interpreted in the currency named by DE49. The only place money is typed. */
    Money amount() {
        Currency currency = Currency.fromNumeric(get(49));
        return Money.ofMinor(Long.parseLong(get(4)), currency);
    }

    Iso8583Message setAmount(Money money) {
        if (money.minorUnits() < 0) throw new Iso8583Exception("DE4 cannot carry a negative amount; use the MTI class instead");
        set(4, String.format("%012d", money.minorUnits()));
        set(49, String.format("%03d", money.currency().numericCode()));
        return this;
    }

    /** First six and last four digits of DE2; everything else replaced. Never log the raw PAN. */
    String maskedPan() {
        String pan = get(2);
        if (pan.length() <= 10) return "*".repeat(pan.length());
        return pan.substring(0, 6) + "*".repeat(pan.length() - 10) + pan.substring(pan.length() - 4);
    }

    /** The MTI, the bitmaps, then every present element in ascending order. ASCII variant. */
    byte[] encode() {
        Bitmap primary = new Bitmap();
        Bitmap secondary = new Bitmap();
        boolean needSecondary = false;
        for (int n : fields.keySet()) {
            if (n <= 64) {
                primary.set(n);
            } else {
                secondary.set(n - 64);
                needSecondary = true;
            }
        }
        if (needSecondary) primary.set(1);
        StringBuilder out = new StringBuilder();
        out.append(mti).append(primary.toHex());
        if (needSecondary) out.append(secondary.toHex());
        for (Map.Entry<Integer, String> e : fields.entrySet()) {
            FieldSpec fs = spec.require(e.getKey());
            String value = e.getValue();
            fs.validate(value);
            if (fs.type() != FieldType.FIXED) {
                out.append(String.format("%0" + fs.type().prefixDigits + "d", value.length()));
            }
            out.append(value);
        }
        return out.toString().getBytes(StandardCharsets.US_ASCII);
    }

    /** Inverse of encode. Every malformed input is a specific error; nothing is guessed. */
    static Iso8583Message decode(byte[] raw, Iso8583Spec spec) {
        String s = new String(raw, StandardCharsets.US_ASCII);
        int pos = 0;
        if (s.length() < 4 + 16) throw new Iso8583Exception("message too short for MTI and primary bitmap: " + s.length() + " bytes");
        Iso8583Message msg = new Iso8583Message(s.substring(0, 4), spec);
        pos = 4;
        Bitmap primary = Bitmap.fromHex(s.substring(pos, pos + 16));
        pos += 16;
        boolean[] present = new boolean[129];
        for (int bit = 2; bit <= 64; bit++) present[bit] = primary.isSet(bit);
        if (primary.isSet(1)) {
            if (s.length() < pos + 16) throw new Iso8583Exception("bit 1 set but no secondary bitmap follows");
            Bitmap secondary = Bitmap.fromHex(s.substring(pos, pos + 16));
            pos += 16;
            if (secondary.isSet(1)) throw new Iso8583Exception("tertiary bitmap (bit 65) is not supported");
            for (int bit = 2; bit <= 64; bit++) present[64 + bit] = secondary.isSet(bit);
        }
        for (int n = 2; n <= 128; n++) {
            if (!present[n]) continue;
            FieldSpec fs = spec.require(n);
            int length = fs.length();
            if (fs.type() != FieldType.FIXED) {
                int digits = fs.type().prefixDigits;
                if (pos + digits > s.length()) throw new Iso8583Exception("truncated length prefix for DE" + n);
                String prefix = s.substring(pos, pos + digits);
                if (!prefix.chars().allMatch(Character::isDigit)) throw new Iso8583Exception("length prefix for DE" + n + " is not numeric: " + prefix);
                length = Integer.parseInt(prefix);
                if (length > fs.length()) throw new Iso8583Exception("DE" + n + " announces " + length + " chars, max is " + fs.length());
                pos += digits;
            }
            if (pos + length > s.length()) {
                throw new Iso8583Exception("truncated: DE" + n + " needs " + length + " chars, " + (s.length() - pos) + " remain");
            }
            msg.set(n, s.substring(pos, pos + length));
            pos += length;
        }
        if (pos != s.length()) throw new Iso8583Exception((s.length() - pos) + " trailing bytes after the last data element");
        return msg;
    }

    /** Human-readable, PAN masked, one element per line. For logs and the demo. */
    String dump() {
        StringBuilder sb = new StringBuilder("MTI " + mti + "\n");
        for (Map.Entry<Integer, String> e : fields.entrySet()) {
            FieldSpec fs = spec.require(e.getKey());
            String shown = e.getKey() == 2 ? maskedPan() : e.getValue();
            sb.append(String.format("  DE%-3d %-40s %s%n", e.getKey(), fs.name(), shown));
        }
        return sb.toString();
    }
}

public final class Main {
    static final String TEST_PAN = "4111111111111111"; // published Visa test number, never a real card

    public static void main(String[] args) {
        Iso8583Spec spec = Iso8583Spec.standard1987();

        System.out.println("== Building a 0200 financial request ==");
        Iso8583Message request = new Iso8583Message("0200", spec)
                .set(2, TEST_PAN)
                .set(3, "000000")
                .setAmount(Money.ofMinor(1999, Currency.USD))
                .set(7, "1012143015")
                .set(11, "000123")
                .set(12, "143015")
                .set(13, "1012")
                .set(22, "051")
                .set(37, "123456789012")
                .set(41, "TERM0001")
                .set(42, "MERCHANT0000001");
        System.out.print(request.dump());
        byte[] wire = request.encode();
        String wireText = new String(wire, StandardCharsets.US_ASCII);
        System.out.println("encoded " + wire.length + " bytes:");
        System.out.println("  " + wireText);
        System.out.println("  MTI=" + wireText.substring(0, 4) + " bitmap=" + wireText.substring(4, 20) + " DE2 prefix=" + wireText.substring(20, 22));

        System.out.println("\n== Decoding it back ==");
        Iso8583Message decoded = Iso8583Message.decode(wire, spec);
        System.out.println("amount = " + decoded.amount() + ", STAN = " + decoded.get(11) + ", RRN = " + decoded.get(37));
        System.out.println("round trip equal: " + Arrays.equals(wire, decoded.encode()));

        System.out.println("\n== The issuer answers with a 0210 ==");
        Iso8583Message response = new Iso8583Message(request.responseMti(), spec);
        for (int n : new int[] {2, 3, 4, 7, 11, 12, 13, 37, 41, 42, 49}) response.set(n, request.get(n));
        response.set(39, "00");
        String responseText = new String(response.encode(), StandardCharsets.US_ASCII);
        System.out.println("MTI " + response.mti() + " bitmap=" + responseText.substring(4, 20) + " (bit 39 added, bit 22 dropped)");
        System.out.println("response code DE39 = " + response.get(39) + " -> approved");

        System.out.println("\n== A secondary bitmap appears when any DE above 64 is present ==");
        Iso8583Message withAccount = new Iso8583Message("0200", spec)
                .set(2, TEST_PAN).set(3, "000000").setAmount(Money.ofMinor(500000, Currency.INR))
                .set(11, "000124").set(102, "12345678901");
        String secondaryText = new String(withAccount.encode(), StandardCharsets.US_ASCII);
        System.out.println("primary   = " + secondaryText.substring(4, 20) + "  (first hex digit F: bit 1 set)");
        System.out.println("secondary = " + secondaryText.substring(20, 36) + "  (bit 38 = DE 102)");
        System.out.println("amount = " + Iso8583Message.decode(withAccount.encode(), spec).amount());

        System.out.println("\n== Malformed input is rejected, never guessed ==");
        String[] bad = {
                wireText.substring(0, 60),                                  // truncated mid-field
                wireText + "XYZ",                                           // trailing bytes
                "0200" + "ZZ38040008C08000" + wireText.substring(20),       // bitmap not hex
                "0200" + "7238040008C08000" + "99" + wireText.substring(22),  // LLVAR length over max
        };
        for (String b : bad) {
            try {
                Iso8583Message.decode(b.getBytes(StandardCharsets.US_ASCII), spec);
                System.out.println("accepted (unexpected)");
            } catch (Iso8583Exception e) {
                System.out.println("rejected: " + e.getMessage());
            }
        }
        try {
            new Iso8583Message("0200", spec).set(4, "19.99");
        } catch (Iso8583Exception e) {
            System.out.println("rejected: " + e.getMessage());
        }
    }
}
