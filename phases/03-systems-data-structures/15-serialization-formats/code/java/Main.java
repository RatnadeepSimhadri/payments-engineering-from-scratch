// Lesson: phases/03-systems-data-structures/15-serialization-formats/docs/en.md
// Serialization: JSON, Protobuf-style Binary, and Schema Evolution
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Protocol Buffers encoding specification (protobuf.dev/programming-guides/encoding): varints, ZigZag, tags,
// wire types, unknown-field handling; RFC 8259 (JSON); IEEE 754 for the 2^53 integer limit. Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HexFormat;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency JPY = new Currency("JPY", 0);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    static Currency of(String code) {
        return switch (code) {
            case "USD" -> USD;
            case "JPY" -> JPY;
            default -> new Currency(code, 2);
        };
    }
}

/** Integer minor units plus a currency. On the wire: sint64 minor units + a 3-byte currency string. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

/** Growable byte buffer for encoding. */
final class ByteSink {
    private byte[] buf = new byte[64];
    private int len;

    void write(int b) {
        if (len == buf.length) buf = Arrays.copyOf(buf, buf.length * 2);
        buf[len++] = (byte) b;
    }

    void write(byte[] bytes) { for (byte b : bytes) write(b); }

    int size() { return len; }

    byte[] toByteArray() { return Arrays.copyOf(buf, len); }
}

/** Cursor over an encoded buffer for decoding. Every read checks bounds: truncated input is an error, never a guess. */
final class ByteSource {
    private final byte[] buf;
    private int pos;
    private final int limit;

    ByteSource(byte[] buf) { this(buf, 0, buf.length); }

    ByteSource(byte[] buf, int start, int limit) {
        this.buf = buf;
        this.pos = start;
        this.limit = limit;
    }

    boolean hasMore() { return pos < limit; }

    int position() { return pos; }

    int readByte() {
        if (pos >= limit) throw new MalformedMessageException("truncated: wanted a byte at offset " + pos);
        return buf[pos++] & 0xFF;
    }

    byte[] readBytes(int n) {
        if (n < 0 || pos + n > limit) throw new MalformedMessageException("truncated: wanted " + n + " bytes at offset " + pos);
        byte[] out = Arrays.copyOfRange(buf, pos, pos + n);
        pos += n;
        return out;
    }

    /** A view over the next n bytes, for nested messages, without copying. */
    ByteSource slice(int n) {
        if (n < 0 || pos + n > limit) throw new MalformedMessageException("truncated: nested message of " + n + " bytes at offset " + pos);
        ByteSource s = new ByteSource(buf, pos, pos + n);
        pos += n;
        return s;
    }
}

final class MalformedMessageException extends RuntimeException {
    MalformedMessageException(String message) { super(message); }
}

/** Varint and ZigZag: the two primitives everything else is built from. */
final class Varint {
    static final int MAX_BYTES = 10;

    /** Little-endian base-128: 7 payload bits per byte, high bit set on every byte but the last. */
    static void writeUnsigned(ByteSink out, long value) {
        while ((value & ~0x7FL) != 0) {
            out.write((int) ((value & 0x7F) | 0x80));
            value >>>= 7;
        }
        out.write((int) value);
    }

    static long readUnsigned(ByteSource in) {
        long result = 0;
        for (int shift = 0; shift < 64; shift += 7) {
            int b = in.readByte();
            result |= (long) (b & 0x7F) << shift;
            if ((b & 0x80) == 0) {
                if (shift == 63 && (b & 0x7E) != 0) throw new MalformedMessageException("varint overflows 64 bits");
                return result;
            }
        }
        throw new MalformedMessageException("varint longer than 10 bytes");
    }

    /** ZigZag maps signed to unsigned so small negatives stay small: 0,-1,1,-2,2 -> 0,1,2,3,4. */
    static long zigzagEncode(long n) { return (n << 1) ^ (n >> 63); }

    static long zigzagDecode(long n) { return (n >>> 1) ^ -(n & 1); }

    static void writeSigned(ByteSink out, long value) { writeUnsigned(out, zigzagEncode(value)); }

    static long readSigned(ByteSource in) { return zigzagDecode(readUnsigned(in)); }

    static int encodedSize(long unsignedValue) {
        int n = 1;
        while ((unsignedValue & ~0x7FL) != 0) { unsignedValue >>>= 7; n++; }
        return n;
    }
}

/** Protobuf wire types: the decoder needs only this to skip a field it does not know. */
enum WireType {
    VARINT(0), FIXED64(1), LENGTH_DELIMITED(2), FIXED32(5);

    final int code;

    WireType(int code) { this.code = code; }

    static WireType of(int code) {
        for (WireType w : values()) if (w.code == code) return w;
        throw new MalformedMessageException("unknown wire type " + code + " (groups are not supported)");
    }
}

/** Writes tag-prefixed fields. A tag is varint((fieldNumber << 3) | wireType). */
final class Encoder {
    final ByteSink out = new ByteSink();

    private void tag(int field, WireType type) {
        if (field < 1 || field > 536_870_911) throw new IllegalArgumentException("field number out of range: " + field);
        Varint.writeUnsigned(out, ((long) field << 3) | type.code);
    }

    Encoder uint64(int field, long value) { tag(field, WireType.VARINT); Varint.writeUnsigned(out, value); return this; }

    Encoder sint64(int field, long value) { tag(field, WireType.VARINT); Varint.writeSigned(out, value); return this; }

    Encoder bool(int field, boolean value) { return uint64(field, value ? 1 : 0); }

    Encoder fixed64(int field, long value) {
        tag(field, WireType.FIXED64);
        for (int i = 0; i < 8; i++) out.write((int) (value >>> (8 * i)) & 0xFF);     // little-endian
        return this;
    }

    Encoder bytes(int field, byte[] value) {
        tag(field, WireType.LENGTH_DELIMITED);
        Varint.writeUnsigned(out, value.length);
        out.write(value);
        return this;
    }

    Encoder string(int field, String value) { return bytes(field, value.getBytes(StandardCharsets.UTF_8)); }

    /** A nested message is just bytes: encode it separately, then write it length-delimited. */
    Encoder message(int field, Encoder nested) { return bytes(field, nested.toByteArray()); }

    byte[] toByteArray() { return out.toByteArray(); }
}

/** Reads tag-prefixed fields in order. Unknown fields are skipped by wire type; that is what makes evolution work. */
final class Decoder {
    private final ByteSource in;
    private int field;
    private WireType type;
    int skipped;

    Decoder(byte[] bytes) { this.in = new ByteSource(bytes); }

    Decoder(ByteSource source) { this.in = source; }

    /** Advance to the next field. Returns false at end of message. */
    boolean next() {
        if (!in.hasMore()) return false;
        long tag = Varint.readUnsigned(in);
        field = (int) (tag >>> 3);
        if (field == 0) throw new MalformedMessageException("field number 0 is reserved");
        type = WireType.of((int) (tag & 0x7));
        return true;
    }

    int field() { return field; }

    WireType type() { return type; }

    private void expect(WireType expected) {
        if (type != expected) throw new MalformedMessageException("field " + field + ": expected " + expected + " but found " + type);
    }

    long uint64() { expect(WireType.VARINT); return Varint.readUnsigned(in); }

    long sint64() { expect(WireType.VARINT); return Varint.readSigned(in); }

    boolean bool() { return uint64() != 0; }

    long fixed64() {
        expect(WireType.FIXED64);
        long v = 0;
        for (int i = 0; i < 8; i++) v |= (long) in.readByte() << (8 * i);
        return v;
    }

    byte[] bytes() {
        expect(WireType.LENGTH_DELIMITED);
        int n = (int) Varint.readUnsigned(in);
        return in.readBytes(n);
    }

    String string() { return new String(bytes(), StandardCharsets.UTF_8); }

    Decoder message() {
        expect(WireType.LENGTH_DELIMITED);
        int n = (int) Varint.readUnsigned(in);
        return new Decoder(in.slice(n));
    }

    /** Skip the current field's value using only its wire type. */
    void skip() {
        skipped++;
        switch (type) {
            case VARINT -> Varint.readUnsigned(in);
            case FIXED64 -> in.readBytes(8);
            case FIXED32 -> in.readBytes(4);
            case LENGTH_DELIMITED -> in.readBytes((int) Varint.readUnsigned(in));
        }
    }
}

/**
 * Schema version 1 of a ledger posting:
 *   1: id (uint64)   2: amount_minor (sint64)   3: currency (string)   4: debit_account (string)   5: credit_account (string)
 */
record PostingV1(long id, Money amount, String debitAccount, String creditAccount) {
    static final int F_ID = 1, F_AMOUNT = 2, F_CURRENCY = 3, F_DEBIT = 4, F_CREDIT = 5;

    byte[] encode() {
        return new Encoder()
                .uint64(F_ID, id)
                .sint64(F_AMOUNT, amount.minorUnits())
                .string(F_CURRENCY, amount.currency().code())
                .string(F_DEBIT, debitAccount)
                .string(F_CREDIT, creditAccount)
                .toByteArray();
    }

    static PostingV1 decode(byte[] bytes) { return decode(new Decoder(bytes)); }

    static PostingV1 decode(Decoder d) {
        long id = 0, minor = 0;
        String currency = "", debit = "", credit = "";
        while (d.next()) {
            switch (d.field()) {
                case F_ID -> id = d.uint64();
                case F_AMOUNT -> minor = d.sint64();
                case F_CURRENCY -> currency = d.string();
                case F_DEBIT -> debit = d.string();
                case F_CREDIT -> credit = d.string();
                default -> d.skip();                       // a field from the future: preserve nothing, break nothing
            }
        }
        if (currency.isEmpty()) throw new MalformedMessageException("posting without a currency");
        return new PostingV1(id, Money.ofMinor(minor, Currency.of(currency)), debit, credit);
    }
}

/** Audit trail added in version 2 as a nested message: 1: actor (string)  2: recorded_at_millis (fixed64). */
record Audit(String actor, long recordedAtMillis) {
    byte[] encode() { return new Encoder().string(1, actor).fixed64(2, recordedAtMillis).toByteArray(); }

    static Audit decode(Decoder d) {
        String actor = "";
        long at = 0;
        while (d.next()) {
            switch (d.field()) {
                case 1 -> actor = d.string();
                case 2 -> at = d.fixed64();
                default -> d.skip();
            }
        }
        return new Audit(actor, at);
    }
}

/**
 * Schema version 2: fields 1-5 unchanged; 6: memo (string), 7: audit (nested message), 8: reversal (bool).
 * Field numbers 1-5 keep their numbers and types forever; new fields take new numbers; nothing is renumbered.
 */
record PostingV2(long id, Money amount, String debitAccount, String creditAccount, String memo, Audit audit, boolean reversal) {
    static final int F_MEMO = 6, F_AUDIT = 7, F_REVERSAL = 8;

    byte[] encode() {
        Encoder e = new Encoder()
                .uint64(PostingV1.F_ID, id)
                .sint64(PostingV1.F_AMOUNT, amount.minorUnits())
                .string(PostingV1.F_CURRENCY, amount.currency().code())
                .string(PostingV1.F_DEBIT, debitAccount)
                .string(PostingV1.F_CREDIT, creditAccount);
        if (!memo.isEmpty()) e.string(F_MEMO, memo);                    // default values are omitted, as protobuf does
        if (audit != null) e.message(F_AUDIT, new Encoder().string(1, audit.actor()).fixed64(2, audit.recordedAtMillis()));
        if (reversal) e.bool(F_REVERSAL, true);
        return e.toByteArray();
    }

    static PostingV2 decode(byte[] bytes) {
        Decoder d = new Decoder(bytes);
        long id = 0, minor = 0;
        String currency = "", debit = "", credit = "", memo = "";
        Audit audit = null;
        boolean reversal = false;
        while (d.next()) {
            switch (d.field()) {
                case PostingV1.F_ID -> id = d.uint64();
                case PostingV1.F_AMOUNT -> minor = d.sint64();
                case PostingV1.F_CURRENCY -> currency = d.string();
                case PostingV1.F_DEBIT -> debit = d.string();
                case PostingV1.F_CREDIT -> credit = d.string();
                case F_MEMO -> memo = d.string();
                case F_AUDIT -> audit = Audit.decode(d.message());
                case F_REVERSAL -> reversal = d.bool();
                default -> d.skip();
            }
        }
        if (currency.isEmpty()) throw new MalformedMessageException("posting without a currency");
        return new PostingV2(id, Money.ofMinor(minor, Currency.of(currency)), debit, credit, memo, audit, reversal);
    }

    PostingV1 asV1() { return new PostingV1(id, amount, debitAccount, creditAccount); }
}

/** A minimal JSON writer for the size comparison and the 2^53 demonstration. RFC 8259 string escaping; 64-bit numbers optionally quoted. */
final class Json {
    static String posting(PostingV2 p, boolean numbersAsStrings) {
        StringBuilder sb = new StringBuilder("{");
        sb.append("\"id\":");
        if (numbersAsStrings) sb.append('"').append(p.id()).append('"'); else sb.append(p.id());
        sb.append(",\"amountMinor\":");
        if (numbersAsStrings) sb.append('"').append(p.amount().minorUnits()).append('"'); else sb.append(p.amount().minorUnits());
        sb.append(",\"currency\":").append(quote(p.amount().currency().code()));
        sb.append(",\"debitAccount\":").append(quote(p.debitAccount()));
        sb.append(",\"creditAccount\":").append(quote(p.creditAccount()));
        if (!p.memo().isEmpty()) sb.append(",\"memo\":").append(quote(p.memo()));
        if (p.audit() != null) sb.append(",\"audit\":{\"actor\":").append(quote(p.audit().actor())).append(",\"recordedAtMillis\":").append(p.audit().recordedAtMillis()).append('}');
        if (p.reversal()) sb.append(",\"reversal\":true");
        return sb.append('}').toString();
    }

    static String quote(String s) {
        StringBuilder sb = new StringBuilder("\"");
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"' -> sb.append("\\\"");
                case '\\' -> sb.append("\\\\");
                case '\n' -> sb.append("\\n");
                case '\r' -> sb.append("\\r");
                case '\t' -> sb.append("\\t");
                default -> { if (c < 0x20) sb.append(String.format("\\u%04x", (int) c)); else sb.append(c); }
            }
        }
        return sb.append('"').toString();
    }
}

public final class Main {
    static String hex(byte[] b) { return HexFormat.ofDelimiter(" ").formatHex(b); }

    public static void main(String[] args) {
        System.out.println("== 1. Varints: 7 bits per byte, little-endian, continuation bit ==");
        for (long v : new long[]{0, 1, 127, 128, 150, 300, 16_383, 16_384, 1_000_000_000L, Long.MAX_VALUE}) {
            ByteSink s = new ByteSink();
            Varint.writeUnsigned(s, v);
            System.out.printf("  %20d -> %-30s (%d byte%s)%n", v, hex(s.toByteArray()), s.size(), s.size() == 1 ? "" : "s");
        }
        ByteSink neg = new ByteSink();
        Varint.writeUnsigned(neg, -1);
        System.out.println("  -1 as a plain varint = " + hex(neg.toByteArray()) + " (10 bytes: the sign bit is 63 bits up)");

        System.out.println();
        System.out.println("== 2. ZigZag: small negatives stay small ==");
        for (long v : new long[]{0, -1, 1, -2, 2, -64, 63, -65, 64, Long.MIN_VALUE}) {
            ByteSink s = new ByteSink();
            Varint.writeSigned(s, v);
            System.out.printf("  %20d -> zigzag %20d -> %-30s (%d byte%s)%n", v, Varint.zigzagEncode(v), hex(s.toByteArray()), s.size(), s.size() == 1 ? "" : "s");
        }
        ByteSink refund = new ByteSink();
        Varint.writeSigned(refund, -1999);
        System.out.println("  a refund of -19.99 USD as sint64 = " + hex(refund.toByteArray()) + " (2 bytes, versus 10 as uint64)");

        System.out.println();
        System.out.println("== 3. Tags and wire types: the protobuf.dev test vectors ==");
        byte[] f1 = new Encoder().uint64(1, 150).toByteArray();
        byte[] f2 = new Encoder().string(2, "testing").toByteArray();
        System.out.println("  field 1 = 150        -> " + hex(f1) + "   (tag 0x08 = field 1 << 3 | VARINT)");
        System.out.println("  field 2 = \"testing\"  -> " + hex(f2) + "   (tag 0x12 = field 2 << 3 | LENGTH_DELIMITED, length 7)");

        System.out.println();
        System.out.println("== 4. A posting on the wire ==");
        PostingV2 p = new PostingV2(1_048_577L, Money.ofMinor(-1999, Currency.USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
                new Audit("svc-refunds", 1_760_000_000_000L), true);
        byte[] bin = p.encode();
        System.out.println("  binary (" + bin.length + " bytes): " + hex(bin));
        String json = Json.posting(p, false);
        System.out.println("  json   (" + json.getBytes(StandardCharsets.UTF_8).length + " bytes): " + json);
        PostingV2 back = PostingV2.decode(bin);
        System.out.println("  decode(encode(p)) equals p: " + back.equals(p) + "  amount = " + back.amount());

        System.out.println();
        System.out.println("== 5. Schema evolution ==");
        Decoder v1View = new Decoder(bin);
        PostingV1 oldReader = PostingV1.decode(v1View);
        System.out.println("  v1 reader on v2 bytes: " + oldReader + "  (skipped " + v1View.skipped + " unknown fields by wire type)");
        PostingV1 v1 = new PostingV1(7, Money.ofMinor(1250, Currency.USD), "acct:a", "acct:b");
        PostingV2 newReader = PostingV2.decode(v1.encode());
        System.out.println("  v2 reader on v1 bytes: memo=\"" + newReader.memo() + "\" audit=" + newReader.audit() + " reversal=" + newReader.reversal() + "  (defaults, not errors)");
        System.out.println("  v2 -> v1 -> compare core fields: " + back.asV1().equals(oldReader));

        System.out.println();
        System.out.println("== 6. What JSON numbers cannot carry ==");
        long bigId = 9_007_199_254_740_993L;                                 // 2^53 + 1
        double asDouble = (double) bigId;
        System.out.println("  id " + bigId + " through a JSON parser that uses double -> " + (long) asDouble + "  (lost: " + (bigId - (long) asDouble) + ")");
        System.out.println("  Twitter, Stripe, and this lesson send 64-bit ids as strings for this reason: " + Json.posting(new PostingV2(bigId, p.amount(), "a", "b", "", null, false), true));
        System.out.println("  0.1 + 0.2 in JSON-as-double = " + (0.1 + 0.2) + "; minor units as integers avoid the question entirely");

        System.out.println();
        System.out.println("== 7. Size over 10,000 random postings ==");
        Random rng = new Random(11);
        long binTotal = 0, jsonTotal = 0;
        for (int i = 0; i < 10_000; i++) {
            PostingV2 q = new PostingV2(rng.nextLong() >>> 1, Money.ofMinor(rng.nextInt(2_000_001) - 1_000_000, Currency.USD),
                    "acct:" + rng.nextInt(100_000), "acct:" + rng.nextInt(100_000), rng.nextBoolean() ? "memo " + rng.nextInt(1000) : "",
                    rng.nextBoolean() ? new Audit("svc-" + rng.nextInt(10), 1_760_000_000_000L + rng.nextInt(1_000_000)) : null, rng.nextInt(20) == 0);
            byte[] b = q.encode();
            if (!PostingV2.decode(b).equals(q)) throw new AssertionError("round trip failed");
            binTotal += b.length;
            jsonTotal += Json.posting(q, true).getBytes(StandardCharsets.UTF_8).length;
        }
        System.out.printf("  binary %d bytes, json %d bytes, ratio %.2fx; all 10000 round-tripped%n", binTotal, jsonTotal, (double) jsonTotal / binTotal);

        System.out.println();
        System.out.println("== 8. Malformed input is an error, not a guess ==");
        byte[] truncated = Arrays.copyOf(bin, bin.length - 3);
        try { PostingV2.decode(truncated); } catch (MalformedMessageException e) { System.out.println("  last 3 bytes cut off: " + e.getMessage()); }
        byte[] wrongType = new Encoder().string(PostingV1.F_AMOUNT, "19.99").toByteArray();
        try { PostingV2.decode(wrongType); } catch (MalformedMessageException e) { System.out.println("  type changed on field 2: " + e.getMessage()); }
    }
}
