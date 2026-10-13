// Tests for phases/01-complexity-and-core-structures/11-bit-manipulation. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.BitSet;
import java.util.List;
import java.util.Random;
import java.util.Set;
import java.util.TreeSet;

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
        // masks and single bits
        check("mask(0)", Bits.mask(0) == 0);
        check("mask(3)", Bits.mask(3) == 0b111);
        check("mask(63)", Bits.mask(63) == Long.MAX_VALUE);
        check("mask(64) is all ones", Bits.mask(64) == -1L);
        checkThrows("mask(65) rejected", IllegalArgumentException.class, () -> Bits.mask(65));
        check("set/clear/toggle", Bits.set(0, 5) == 32 && Bits.clear(32, 5) == 0 && Bits.toggle(Bits.toggle(7, 1), 1) == 7);
        check("isSet on sign bit uses unsigned shift", Bits.isSet(Long.MIN_VALUE, 63) && !Bits.isSet(Long.MIN_VALUE, 62));
        check("set bit 63 gives negative long", Bits.set(0, 63) == Long.MIN_VALUE);

        // extract and insert
        check("extract middle field", Bits.extract(0b1011_0100L, 4, 3) == 0b011);
        check("extract top field of negative value is unsigned", Bits.extract(-1L, 60, 4) == 15);
        check("insert replaces only the field", Bits.insert(0b1011_0100L, 4, 3, 0b110) == 0b1110_0100L);
        check("insert then extract round-trips", Bits.extract(Bits.insert(0, 22, 41, 123_456_789L), 22, 41) == 123_456_789L);
        checkThrows("insert rejects a field that does not fit", IllegalArgumentException.class, () -> Bits.insert(0, 0, 3, 8));
        checkThrows("field beyond bit 63 rejected", IllegalArgumentException.class, () -> Bits.extract(0, 60, 5));

        // popcount: two implementations agree with each other and with Long.bitCount on random inputs
        Random rnd = new Random(5);
        boolean popAgree = true;
        for (int i = 0; i < 10_000 && popAgree; i++) {
            long v = rnd.nextLong();
            int expected = Long.bitCount(v);
            popAgree = Bits.popcountKernighan(v) == expected && Bits.popcountParallel(v) == expected;
        }
        check("popcount implementations agree on 10,000 random longs", popAgree);
        check("popcount boundaries", Bits.popcountParallel(0) == 0 && Bits.popcountParallel(-1L) == 64 && Bits.popcountKernighan(Long.MIN_VALUE) == 1);

        // powers of two and lowest set bit
        check("lowestSetBit", Bits.lowestSetBit(0b1011_0100L) == 0b100 && Bits.lowestSetBit(0) == 0 && Bits.lowestSetBit(Long.MIN_VALUE) == Long.MIN_VALUE);
        check("isPowerOfTwo", Bits.isPowerOfTwo(1) && Bits.isPowerOfTwo(1024) && !Bits.isPowerOfTwo(0) && !Bits.isPowerOfTwo(1000) && !Bits.isPowerOfTwo(Long.MIN_VALUE));
        check("roundUpToPowerOfTwo", Bits.roundUpToPowerOfTwo(1) == 1 && Bits.roundUpToPowerOfTwo(1000) == 1024 && Bits.roundUpToPowerOfTwo(1024) == 1024 && Bits.roundUpToPowerOfTwo(1025) == 2048);
        check("toBinary/toHex fixed width", Bits.toBinary(5, 8).equals("00000101") && Bits.toHex(255, 4).equals("00FF") && Bits.toHex(-1L, 16).equals("FFFFFFFFFFFFFFFF"));

        // account flags
        AccountFlags f = AccountFlags.none().with(AccountFlags.KYC_VERIFIED).with(AccountFlags.PAYOUTS_ENABLED);
        check("flags set and tested", f.has(AccountFlags.KYC_VERIFIED) && f.has(AccountFlags.KYC_VERIFIED | AccountFlags.PAYOUTS_ENABLED) && !f.has(AccountFlags.FROZEN));
        check("payout allowed when verified and enabled", f.canPayOut());
        check("hold blocks payout", !f.with(AccountFlags.CHARGEBACK_HOLD).canPayOut());
        check("frozen blocks payout", !f.with(AccountFlags.FROZEN).canPayOut());
        check("without removes exactly one flag", f.with(AccountFlags.FROZEN).without(AccountFlags.FROZEN).bits() == f.bits());
        check("without an absent flag is a no-op", f.without(AccountFlags.HIGH_RISK).bits() == f.bits());
        check("flags toString", f.toString().equals("{KYC_VERIFIED, PAYOUTS_ENABLED}") && AccountFlags.none().toString().equals("{}"));
        checkThrows("unknown flag bit rejected", IllegalArgumentException.class, () -> new AccountFlags(1 << 7));

        // bitmap against java.util.BitSet
        Bitmap bm = new Bitmap(200);
        BitSet ref = new BitSet(200);
        for (int i = 0; i < 2_000; i++) {
            int idx = rnd.nextInt(200);
            if (rnd.nextBoolean()) { bm.set(idx); ref.set(idx); } else { bm.clear(idx); ref.clear(idx); }
        }
        boolean bitmapAgree = bm.cardinality() == ref.cardinality();
        for (int i = 0; i < 200 && bitmapAgree; i++) bitmapAgree = bm.get(i) == ref.get(i) && bm.nextSetBit(i) == ref.nextSetBit(i);
        check("bitmap agrees with BitSet after 2,000 random ops", bitmapAgree);
        check("bitmap word allocation rounds up", new Bitmap(0).wordsAllocated() == 0 && new Bitmap(1).wordsAllocated() == 1 && new Bitmap(64).wordsAllocated() == 1 && new Bitmap(65).wordsAllocated() == 2);
        Bitmap sparse = new Bitmap(1_000);
        sparse.set(999);
        check("nextSetBit skips zero words", sparse.nextSetBit(0) == 999 && sparse.nextSetBit(999) == 999 && sparse.nextSetBit(1_000) == -1);
        check("nextSetBit on empty is -1", new Bitmap(1_000).nextSetBit(0) == -1);
        Bitmap a = new Bitmap(130), b = new Bitmap(130);
        a.set(0); a.set(129); b.set(129); b.set(64);
        check("and/or across word boundaries", a.and(b).cardinality() == 1 && a.and(b).get(129) && a.or(b).cardinality() == 3);
        checkThrows("bitmap index out of range", IndexOutOfBoundsException.class, () -> bm.set(200));
        checkThrows("bitmap and with different capacity", IllegalArgumentException.class, () -> a.and(bm));

        // ISO 8583 bitmap
        Set<Integer> fields = new TreeSet<>(List.of(2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49));
        String hex = Iso8583Bitmap.encode(fields);
        check("primary bitmap is 16 hex chars", hex.length() == 16);
        check("field 2 is bit 62, so the first hex digit is 7 for fields 2,3,4", hex.startsWith("7"));
        check("known vector: fields 2,3,4,7,11,12,13,22,24,25,35,41,42,49", hex.equals("7238058020C08000"));
        check("decode inverts encode", new TreeSet<>(Iso8583Bitmap.decode(hex)).equals(fields));
        Set<Integer> withSecondary = new TreeSet<>(fields);
        withSecondary.add(102);
        String both = Iso8583Bitmap.encode(withSecondary);
        check("secondary bitmap doubles the length and sets field 1", both.length() == 32 && both.startsWith("F"));
        check("field 102 is bit 26 of the secondary bitmap", both.substring(16).equals("0000000004000000"));
        check("decode with secondary", Iso8583Bitmap.decode(both).contains(102) && Iso8583Bitmap.decode(both).size() == 15);
        check("field 64 is the lowest primary bit", Iso8583Bitmap.encode(Set.of(64)).equals("0000000000000001"));
        checkThrows("field 1 cannot be requested directly", IllegalArgumentException.class, () -> Iso8583Bitmap.encode(Set.of(1)));
        checkThrows("field 129 out of range", IllegalArgumentException.class, () -> Iso8583Bitmap.encode(Set.of(129)));
        checkThrows("decode rejects inconsistent length", IllegalArgumentException.class, () -> Iso8583Bitmap.decode("F000000000000000"));
        checkThrows("decode rejects odd length", IllegalArgumentException.class, () -> Iso8583Bitmap.decode("7234"));

        // snowflake ids
        long ts = SnowflakeId.EPOCH_MILLIS + 123_456_789L;
        long id = SnowflakeId.compose(ts, 7, 42);
        SnowflakeId.Parts parts = SnowflakeId.decompose(id);
        check("compose/decompose round-trip", parts.timestampMillis() == ts && parts.worker() == 7 && parts.sequence() == 42);
        check("id is positive", id > 0);
        check("later timestamp gives larger id regardless of sequence", SnowflakeId.compose(ts + 1, 0, 0) > SnowflakeId.compose(ts, 1023, 4095));
        check("max fields fit", SnowflakeId.compose(SnowflakeId.EPOCH_MILLIS + SnowflakeId.MAX_TIMESTAMP, 1023, 4095) == Long.MAX_VALUE);
        checkThrows("timestamp before epoch rejected", IllegalArgumentException.class, () -> SnowflakeId.compose(SnowflakeId.EPOCH_MILLIS - 1, 0, 0));
        checkThrows("worker 1024 does not fit", IllegalArgumentException.class, () -> SnowflakeId.compose(ts, 1024, 0));
        checkThrows("negative id rejected", IllegalArgumentException.class, () -> SnowflakeId.decompose(-1L));

        long[] now = {SnowflakeId.EPOCH_MILLIS + 1_000};
        SnowflakeId.Generator gen = new SnowflakeId.Generator(3, () -> now[0]);
        long first = gen.next(), second = gen.next();
        check("same millisecond increments sequence", SnowflakeId.decompose(first).sequence() == 0 && SnowflakeId.decompose(second).sequence() == 1 && second > first);
        now[0]++;
        check("new millisecond resets sequence", SnowflakeId.decompose(gen.next()).sequence() == 0);
        now[0] -= 5;
        checkThrows("clock moving backwards is refused", IllegalStateException.class, gen::next);

        int[] reads = {0};
        long[] slow = {SnowflakeId.EPOCH_MILLIS + 5_000};
        SnowflakeId.Generator busy = new SnowflakeId.Generator(1, () -> { if (++reads[0] % 10_000 == 0) slow[0]++; return slow[0]; });
        long prev = -1;
        boolean increasing = true;
        for (int i = 0; i < 9_000; i++) { long x = busy.next(); if (x <= prev) increasing = false; prev = x; }
        check("sequence overflow waits for the next millisecond and stays unique", increasing && busy.waits() >= 1);

        // two's complement traps the lesson warns about
        check("abs(MIN_VALUE) is still negative", Math.abs(Long.MIN_VALUE) < 0);
        check("byte 0x80 widens to -128 unless masked", (byte) 0x80 == -128 && ((byte) 0x80 & 0xFF) == 128);
        check("arithmetic vs logical shift", (-8 >> 1) == -4 && (-8 >>> 1) == 2_147_483_644);
        check("int shift count is taken mod 32", (1 << 32) == 1 && (1 << 31) < 0);
        check("overflow-safe midpoint", 2_000_000_000 + ((2_100_000_000 - 2_000_000_000) >>> 1) == 2_050_000_000 && (2_000_000_000 + 2_100_000_000) / 2 < 0);
        check("is-odd on negatives uses & not %", (-7 % 2) == -1 && (-7 & 1) == 1);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f2 -> System.out.println("FAIL: " + f2));
        if (!failures.isEmpty()) System.exit(1);
    }
}
