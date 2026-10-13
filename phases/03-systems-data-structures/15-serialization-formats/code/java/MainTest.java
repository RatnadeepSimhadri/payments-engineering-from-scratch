// Tests for phases/03-systems-data-structures/15-serialization-formats. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HexFormat;
import java.util.List;
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

    static byte[] hex(String s) { return HexFormat.of().parseHex(s.replace(" ", "")); }

    static byte[] varint(long v) { ByteSink s = new ByteSink(); Varint.writeUnsigned(s, v); return s.toByteArray(); }

    static long readVarint(byte[] b) { return Varint.readUnsigned(new ByteSource(b)); }

    public static void main(String[] args) {
        // Varint encoding against the protobuf specification's vectors
        check("varint 0 -> 00", Arrays.equals(varint(0), hex("00")));
        check("varint 1 -> 01", Arrays.equals(varint(1), hex("01")));
        check("varint 127 -> 7f (largest single byte)", Arrays.equals(varint(127), hex("7f")));
        check("varint 128 -> 80 01", Arrays.equals(varint(128), hex("80 01")));
        check("varint 150 -> 96 01 (protobuf.dev example)", Arrays.equals(varint(150), hex("96 01")));
        check("varint 300 -> ac 02 (protobuf.dev example)", Arrays.equals(varint(300), hex("ac 02")));
        check("varint 2^63-1 is 9 bytes", varint(Long.MAX_VALUE).length == 9);
        check("varint -1 (all ones) is 10 bytes ending in 01", varint(-1).length == 10 && Arrays.equals(varint(-1), hex("ff ff ff ff ff ff ff ff ff 01")));
        check("encodedSize matches writer", Varint.encodedSize(127) == 1 && Varint.encodedSize(128) == 2 && Varint.encodedSize(-1) == 10);
        boolean roundTrips = true;
        Random rng = new Random(3);
        for (int i = 0; i < 10_000; i++) {
            long v = rng.nextLong() >>> rng.nextInt(64);
            roundTrips &= readVarint(varint(v)) == v;
        }
        check("varint round-trips 10000 random values of all magnitudes", roundTrips);
        check("varint read of -1 round-trips", readVarint(varint(-1)) == -1);
        checkThrows("varint rejects truncated input", MalformedMessageException.class, () -> readVarint(hex("80")));
        checkThrows("varint rejects 11 continuation bytes", MalformedMessageException.class, () -> readVarint(hex("80 80 80 80 80 80 80 80 80 80 80 00")));
        checkThrows("varint rejects a 10th byte that overflows 64 bits", MalformedMessageException.class, () -> readVarint(hex("ff ff ff ff ff ff ff ff ff 7f")));

        // ZigZag against the specification's table
        check("zigzag 0 -> 0", Varint.zigzagEncode(0) == 0);
        check("zigzag -1 -> 1", Varint.zigzagEncode(-1) == 1);
        check("zigzag 1 -> 2", Varint.zigzagEncode(1) == 2);
        check("zigzag -2 -> 3", Varint.zigzagEncode(-2) == 3);
        check("zigzag 2147483647 -> 4294967294", Varint.zigzagEncode(2_147_483_647L) == 4_294_967_294L);
        check("zigzag -2147483648 -> 4294967295", Varint.zigzagEncode(-2_147_483_648L) == 4_294_967_295L);
        check("zigzag Long.MIN -> all ones", Varint.zigzagEncode(Long.MIN_VALUE) == -1L);
        check("zigzag Long.MAX -> all ones minus one", Varint.zigzagEncode(Long.MAX_VALUE) == -2L);
        boolean zz = true;
        for (int i = 0; i < 10_000; i++) { long v = rng.nextLong(); zz &= Varint.zigzagDecode(Varint.zigzagEncode(v)) == v; }
        check("zigzag round-trips 10000 random signed values", zz);
        ByteSink refund = new ByteSink();
        Varint.writeSigned(refund, -1999);
        check("sint64 -1999 is two bytes (9d 1f)", Arrays.equals(refund.toByteArray(), hex("9d 1f")));
        check("sint64 -64 is one byte, -65 is two", sizeSigned(-64) == 1 && sizeSigned(-65) == 2 && sizeSigned(63) == 1 && sizeSigned(64) == 2);

        // Tags, wire types, and the protobuf.dev message vectors
        check("field 1 uint64 150 -> 08 96 01", Arrays.equals(new Encoder().uint64(1, 150).toByteArray(), hex("08 96 01")));
        check("field 2 string testing -> 12 07 74 65 73 74 69 6e 67", Arrays.equals(new Encoder().string(2, "testing").toByteArray(), hex("12 07 74 65 73 74 69 6e 67")));
        check("field 3 nested message with field 1 = 150 -> 1a 03 08 96 01",
                Arrays.equals(new Encoder().message(3, new Encoder().uint64(1, 150)).toByteArray(), hex("1a 03 08 96 01")));
        check("fixed64 is little-endian", Arrays.equals(new Encoder().fixed64(4, 0x0102030405060708L).toByteArray(), hex("21 08 07 06 05 04 03 02 01")));
        check("field number 16 needs a two-byte tag", new Encoder().uint64(16, 1).toByteArray().length == 3 && new Encoder().uint64(15, 1).toByteArray().length == 2);
        checkThrows("field number 0 is rejected on encode", IllegalArgumentException.class, () -> new Encoder().uint64(0, 1));
        checkThrows("field number 0 is rejected on decode", MalformedMessageException.class, () -> new Decoder(hex("00 01")).next());
        checkThrows("wire type 3 (start group) is rejected", MalformedMessageException.class, () -> new Decoder(hex("0b")).next());
        Decoder d = new Decoder(hex("08 96 01 12 07 74 65 73 74 69 6e 67"));
        check("decoder walks fields in order", d.next() && d.field() == 1 && d.type() == WireType.VARINT && d.uint64() == 150
                && d.next() && d.field() == 2 && d.type() == WireType.LENGTH_DELIMITED && d.string().equals("testing") && !d.next());
        checkThrows("reading a string where a varint sits is a type error", MalformedMessageException.class, () -> { Decoder x = new Decoder(hex("08 96 01")); x.next(); x.string(); });

        // Skipping unknown fields by wire type, all four kinds
        byte[] mixed = new Encoder().uint64(9, 300).fixed64(10, 7).string(11, "skip me").message(12, new Encoder().uint64(1, 1)).uint64(1, 42).toByteArray();
        Decoder sk = new Decoder(mixed);
        long found = -1;
        while (sk.next()) { if (sk.field() == 1) found = sk.uint64(); else sk.skip(); }
        check("unknown fields of every wire type are skipped and the known field is still read", found == 42 && sk.skipped == 4);
        byte[] fixed32Unknown = hex("2d 01 02 03 04 08 05");         // field 5 FIXED32 (tag 0x2d), then field 1 = 5
        Decoder f32 = new Decoder(fixed32Unknown);
        long got = -1;
        while (f32.next()) { if (f32.field() == 1) got = f32.uint64(); else f32.skip(); }
        check("fixed32 unknown field is skipped as 4 bytes", got == 5);

        // Posting v1/v2 round trips and evolution
        PostingV2 p = new PostingV2(1_048_577L, Money.ofMinor(-1999, Currency.USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
                new Audit("svc-refunds", 1_760_000_000_000L), true);
        byte[] bin = p.encode();
        check("v2 round trip", PostingV2.decode(bin).equals(p));
        check("v2 encoding is 94 bytes", bin.length == 94);
        Decoder v1View = new Decoder(bin);
        PostingV1 asV1 = PostingV1.decode(v1View);
        check("v1 reader on v2 bytes keeps the core fields", asV1.equals(p.asV1()));
        check("v1 reader skipped exactly the three new fields", v1View.skipped == 3);
        PostingV1 old = new PostingV1(7, Money.ofMinor(1250, Currency.USD), "acct:a", "acct:b");
        PostingV2 upgraded = PostingV2.decode(old.encode());
        check("v2 reader on v1 bytes gets defaults for new fields", upgraded.memo().isEmpty() && upgraded.audit() == null && !upgraded.reversal()
                && upgraded.id() == 7 && upgraded.amount().equals(old.amount()));
        check("v2 with defaults encodes identically to v1", Arrays.equals(new PostingV2(7, old.amount(), "acct:a", "acct:b", "", null, false).encode(), old.encode()));
        check("negative amount survives as sint64", PostingV2.decode(bin).amount().minorUnits() == -1999);
        check("JPY zero-exponent currency survives", PostingV1.decode(new PostingV1(1, Money.ofMinor(1200, Currency.JPY), "a", "b").encode()).amount().toString().equals("1200 JPY"));
        check("UTF-8 account names round-trip", PostingV1.decode(new PostingV1(1, Money.ofMinor(1, Currency.USD), "acct:éè€", "b").encode()).debitAccount().equals("acct:éè€"));
        checkThrows("missing currency is rejected", MalformedMessageException.class, () -> PostingV1.decode(new Encoder().uint64(1, 1).toByteArray()));
        checkThrows("truncated message is rejected", MalformedMessageException.class, () -> PostingV2.decode(Arrays.copyOf(bin, bin.length - 3)));
        checkThrows("changed field type is rejected", MalformedMessageException.class, () -> PostingV2.decode(new Encoder().string(PostingV1.F_AMOUNT, "19.99").string(3, "USD").toByteArray()));
        boolean all = true;
        Random r2 = new Random(11);
        for (int i = 0; i < 2_000; i++) {
            PostingV2 q = new PostingV2(r2.nextLong() >>> 1, Money.ofMinor(r2.nextInt(2_000_001) - 1_000_000, Currency.USD),
                    "acct:" + r2.nextInt(100_000), "acct:" + r2.nextInt(100_000), r2.nextBoolean() ? "memo " + r2.nextInt(1000) : "",
                    r2.nextBoolean() ? new Audit("svc-" + r2.nextInt(10), 1_760_000_000_000L + r2.nextInt(1_000_000)) : null, r2.nextInt(20) == 0);
            all &= PostingV2.decode(q.encode()).equals(q);
        }
        check("2000 random postings round-trip", all);

        // JSON writer: escaping and the 2^53 problem
        check("json escapes quotes, backslashes, and control characters", Json.quote("a\"b\\c\nd\u0001").equals("\"a\\\"b\\\\c\\nd\\u0001\""));
        check("json numbers-as-strings quotes id and amount", Json.posting(new PostingV2(5, Money.ofMinor(-1, Currency.USD), "a", "b", "", null, false), true)
                .equals("{\"id\":\"5\",\"amountMinor\":\"-1\",\"currency\":\"USD\",\"debitAccount\":\"a\",\"creditAccount\":\"b\"}"));
        check("2^53 + 1 is not representable as a double", (long) (double) 9_007_199_254_740_993L == 9_007_199_254_740_992L);
        check("2^53 is representable as a double", (long) (double) 9_007_199_254_740_992L == 9_007_199_254_740_992L);
        check("binary is smaller than json for the sample posting", bin.length < Json.posting(p, false).getBytes(StandardCharsets.UTF_8).length);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static int sizeSigned(long v) { ByteSink s = new ByteSink(); Varint.writeSigned(s, v); return s.size(); }
}
