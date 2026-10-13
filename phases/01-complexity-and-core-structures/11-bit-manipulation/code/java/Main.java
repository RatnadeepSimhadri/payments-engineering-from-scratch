// Lesson: phases/01-complexity-and-core-structures/11-bit-manipulation/docs/en.md
// Bit Manipulation and Compact Encodings
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 8583-1:2003 (bitmap and data-element layout); Twitter Snowflake (github.com/twitter-archive/snowflake);
//          Warren, "Hacker's Delight", 2nd ed., ch. 2 and 5; JLS 15.19 (shift operators).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;
import java.util.function.LongSupplier;

/** Masks, fields, and counting. Every method is a few instructions; the point is to name them. */
final class Bits {
    private Bits() {}

    /** A mask of `width` low bits set: mask(3) = 0b111. mask(64) is all ones; mask(0) is 0. */
    static long mask(int width) {
        if (width < 0 || width > 64) throw new IllegalArgumentException("width must be 0..64: " + width);
        return width == 64 ? -1L : (1L << width) - 1;
    }

    static boolean isSet(long v, int bit) { return ((v >>> bit) & 1L) != 0; }
    static long set(long v, int bit) { return v | (1L << bit); }
    static long clear(long v, int bit) { return v & ~(1L << bit); }
    static long toggle(long v, int bit) { return v ^ (1L << bit); }

    /** Read `width` bits starting at `shift` as an unsigned value. */
    static long extract(long v, int shift, int width) {
        checkField(shift, width);
        return (v >>> shift) & mask(width);
    }

    /** Write `field` into the `width` bits at `shift`, rejecting a field that does not fit. */
    static long insert(long v, int shift, int width, long field) {
        checkField(shift, width);
        if ((field & ~mask(width)) != 0) throw new IllegalArgumentException(field + " does not fit in " + width + " bits");
        long hole = ~(mask(width) << shift);
        return (v & hole) | (field << shift);
    }

    private static void checkField(int shift, int width) {
        if (shift < 0 || width < 1 || shift + width > 64) throw new IllegalArgumentException("field " + shift + "+" + width + " outside 64 bits");
    }

    /** Number of one bits, by clearing the lowest set bit until nothing is left. O(popcount). */
    static int popcountKernighan(long v) {
        int n = 0;
        while (v != 0) { v &= v - 1; n++; }
        return n;
    }

    /** Number of one bits in constant time: add neighbours in 2-, 4-, 8-bit groups, then fold. */
    static int popcountParallel(long v) {
        v = v - ((v >>> 1) & 0x5555555555555555L);
        v = (v & 0x3333333333333333L) + ((v >>> 2) & 0x3333333333333333L);
        v = (v + (v >>> 4)) & 0x0F0F0F0F0F0F0F0FL;
        return (int) ((v * 0x0101010101010101L) >>> 56);
    }

    /** The lowest set bit on its own: 0b101100 -> 0b100. Zero for zero. */
    static long lowestSetBit(long v) { return v & -v; }

    static boolean isPowerOfTwo(long v) { return v > 0 && (v & (v - 1)) == 0; }

    /** Smallest power of two >= v, for v in 1..2^62. Used by hash-map and ring-buffer capacities. */
    static long roundUpToPowerOfTwo(long v) {
        if (v < 1 || v > (1L << 62)) throw new IllegalArgumentException("out of range: " + v);
        return v == 1 ? 1 : Long.highestOneBit(v - 1) << 1;
    }

    /** Fixed-width binary rendering, most significant bit first. */
    static String toBinary(long v, int width) {
        StringBuilder sb = new StringBuilder(width);
        for (int i = width - 1; i >= 0; i--) sb.append(isSet(v, i) ? '1' : '0');
        return sb.toString();
    }

    /** Fixed-width upper-case hex. */
    static String toHex(long v, int digits) {
        String s = Long.toHexString(v).toUpperCase();
        return "0".repeat(Math.max(0, digits - s.length())) + s;
    }
}

/** Account state as a set of flags in one int. Each flag owns one bit; combinations are OR, tests are AND. */
final class AccountFlags {
    static final int FROZEN = 1 << 0;
    static final int KYC_VERIFIED = 1 << 1;
    static final int PAYOUTS_ENABLED = 1 << 2;
    static final int HIGH_RISK = 1 << 3;
    static final int CHARGEBACK_HOLD = 1 << 4;
    private static final String[] NAMES = {"FROZEN", "KYC_VERIFIED", "PAYOUTS_ENABLED", "HIGH_RISK", "CHARGEBACK_HOLD"};

    private final int bits;

    AccountFlags(int bits) {
        if ((bits & ~Bits.mask(NAMES.length)) != 0) throw new IllegalArgumentException("unknown flag bits in " + Integer.toBinaryString(bits));
        this.bits = bits;
    }

    static AccountFlags none() { return new AccountFlags(0); }

    boolean has(int flag) { return (bits & flag) == flag; }
    boolean hasAny(int flags) { return (bits & flags) != 0; }
    AccountFlags with(int flag) { return new AccountFlags(bits | flag); }
    AccountFlags without(int flag) { return new AccountFlags(bits & ~flag); }
    int bits() { return bits; }

    /** The business rule in one expression: may this account receive a payout right now? */
    boolean canPayOut() {
        return has(KYC_VERIFIED | PAYOUTS_ENABLED) && !hasAny(FROZEN | CHARGEBACK_HOLD);
    }

    @Override public String toString() {
        List<String> on = new ArrayList<>();
        for (int i = 0; i < NAMES.length; i++) if (Bits.isSet(bits, i)) on.add(NAMES[i]);
        return on.isEmpty() ? "{}" : "{" + String.join(", ", on) + "}";
    }
}

/** Dense bit set over long[] words: one bit per index, 64 per word. The structure behind "which of N things happened today". */
final class Bitmap {
    private final long[] words;
    private final int capacity;

    Bitmap(int capacity) {
        if (capacity < 0) throw new IllegalArgumentException("capacity must be >= 0");
        this.capacity = capacity;
        this.words = new long[(capacity + 63) >>> 6];
    }

    int capacity() { return capacity; }
    long wordsAllocated() { return words.length; }

    private void checkIndex(int i) {
        if (i < 0 || i >= capacity) throw new IndexOutOfBoundsException("bit " + i + " not in 0.." + (capacity - 1));
    }

    void set(int i) { checkIndex(i); words[i >>> 6] |= 1L << (i & 63); }
    void clear(int i) { checkIndex(i); words[i >>> 6] &= ~(1L << (i & 63)); }
    boolean get(int i) { checkIndex(i); return (words[i >>> 6] & (1L << (i & 63))) != 0; }

    int cardinality() {
        int n = 0;
        for (long w : words) n += Bits.popcountParallel(w);
        return n;
    }

    /** Index of the first set bit at or after `from`, or -1. Skips whole zero words. */
    int nextSetBit(int from) {
        if (from < 0) throw new IndexOutOfBoundsException("from must be >= 0");
        if (from >= capacity) return -1;
        int w = from >>> 6;
        long word = words[w] & (-1L << (from & 63)); // drop bits below `from`
        while (true) {
            if (word != 0) {
                int idx = (w << 6) + Long.numberOfTrailingZeros(word);
                return idx < capacity ? idx : -1;
            }
            if (++w == words.length) return -1;
            word = words[w];
        }
    }

    Bitmap and(Bitmap other) { return combine(other, true); }
    Bitmap or(Bitmap other) { return combine(other, false); }

    private Bitmap combine(Bitmap other, boolean and) {
        if (other.capacity != capacity) throw new IllegalArgumentException("bitmaps must have equal capacity");
        Bitmap out = new Bitmap(capacity);
        for (int i = 0; i < words.length; i++) out.words[i] = and ? (words[i] & other.words[i]) : (words[i] | other.words[i]);
        return out;
    }
}

/**
 * ISO 8583 bitmap: 64 bits that announce which data elements follow. Field f (1..64) is bit (64 - f)
 * of the primary bitmap, so field 1 is the most significant bit. Field 1 itself means "a secondary
 * bitmap follows" and announces fields 65..128 the same way.
 */
final class Iso8583Bitmap {
    private Iso8583Bitmap() {}

    /** Hex-encode the bitmap(s) for a set of present fields: 16 hex characters, or 32 when any field > 64 is present. */
    static String encode(Set<Integer> fields) {
        long primary = 0, secondary = 0;
        for (int f : fields) {
            if (f < 2 || f > 128) throw new IllegalArgumentException("field must be 2..128 (field 1 is derived): " + f);
            if (f <= 64) primary |= 1L << (64 - f);
            else secondary |= 1L << (128 - f);
        }
        if (secondary != 0) {
            primary |= 1L << 63; // field 1: secondary bitmap present
            return Bits.toHex(primary, 16) + Bits.toHex(secondary, 16);
        }
        return Bits.toHex(primary, 16);
    }

    /** Decode 16 or 32 hex characters into the ascending list of present fields (field 1 excluded, it is structural). */
    static List<Integer> decode(String hex) {
        if (hex.length() != 16 && hex.length() != 32) throw new IllegalArgumentException("bitmap must be 16 or 32 hex chars, got " + hex.length());
        long primary = Long.parseUnsignedLong(hex.substring(0, 16), 16);
        boolean hasSecondary = Bits.isSet(primary, 63);
        if (hasSecondary != (hex.length() == 32)) {
            throw new IllegalArgumentException("field 1 says secondary bitmap " + (hasSecondary ? "present" : "absent") + " but length says otherwise");
        }
        List<Integer> fields = new ArrayList<>();
        for (int f = 2; f <= 64; f++) if (Bits.isSet(primary, 64 - f)) fields.add(f);
        if (hasSecondary) {
            long secondary = Long.parseUnsignedLong(hex.substring(16), 16);
            for (int f = 65; f <= 128; f++) if (Bits.isSet(secondary, 128 - f)) fields.add(f);
        }
        return fields;
    }
}

/**
 * Snowflake-style 64-bit id: 0 | 41-bit milliseconds since a custom epoch | 10-bit worker | 12-bit sequence.
 * Ids are unique per worker and increase with time, so they sort by creation order as plain longs.
 */
final class SnowflakeId {
    static final long EPOCH_MILLIS = 1_700_000_000_000L; // 2023-11-14T22:13:20Z; ids stay positive until 2093
    static final int SEQUENCE_BITS = 12;
    static final int WORKER_BITS = 10;
    static final int TIMESTAMP_BITS = 41;
    static final int WORKER_SHIFT = SEQUENCE_BITS;
    static final int TIMESTAMP_SHIFT = SEQUENCE_BITS + WORKER_BITS;
    static final long MAX_SEQUENCE = Bits.mask(SEQUENCE_BITS);
    static final long MAX_WORKER = Bits.mask(WORKER_BITS);
    static final long MAX_TIMESTAMP = Bits.mask(TIMESTAMP_BITS);

    record Parts(long timestampMillis, long worker, long sequence) {}

    static long compose(long timestampMillis, long worker, long sequence) {
        long delta = timestampMillis - EPOCH_MILLIS;
        if (delta < 0 || delta > MAX_TIMESTAMP) throw new IllegalArgumentException("timestamp outside the 41-bit window");
        long id = 0;
        id = Bits.insert(id, TIMESTAMP_SHIFT, TIMESTAMP_BITS, delta);
        id = Bits.insert(id, WORKER_SHIFT, WORKER_BITS, worker);
        id = Bits.insert(id, 0, SEQUENCE_BITS, sequence);
        return id;
    }

    static Parts decompose(long id) {
        if (id < 0) throw new IllegalArgumentException("negative id: sign bit must be zero");
        return new Parts(
                Bits.extract(id, TIMESTAMP_SHIFT, TIMESTAMP_BITS) + EPOCH_MILLIS,
                Bits.extract(id, WORKER_SHIFT, WORKER_BITS),
                Bits.extract(id, 0, SEQUENCE_BITS));
    }

    /** Per-worker generator. The clock is injected so tests and the demo are deterministic. */
    static final class Generator {
        private final long worker;
        private final LongSupplier clock;
        private long lastMillis = -1;
        private long sequence;
        private long waits; // instrumentation: how often the sequence overflowed within one millisecond

        Generator(long worker, LongSupplier clock) {
            if (worker < 0 || worker > MAX_WORKER) throw new IllegalArgumentException("worker must be 0.." + MAX_WORKER);
            this.worker = worker;
            this.clock = clock;
        }

        long next() {
            long now = clock.getAsLong();
            if (now < lastMillis) throw new IllegalStateException("clock moved backwards by " + (lastMillis - now) + " ms; refusing to issue ids");
            if (now == lastMillis) {
                sequence = (sequence + 1) & MAX_SEQUENCE;
                if (sequence == 0) { // 4096 ids in one millisecond: wait for the clock to advance
                    waits++;
                    while (now <= lastMillis) now = clock.getAsLong();
                }
            } else {
                sequence = 0;
            }
            lastMillis = now;
            return compose(now, worker, sequence);
        }

        long waits() { return waits; }
    }
}

public final class Main {

    static void demoMasksAndFields() {
        System.out.println("== Masks, fields, popcount ==");
        long v = 0b1011_0100L;
        System.out.println("v            = " + Bits.toBinary(v, 8));
        System.out.println("set bit 0    = " + Bits.toBinary(Bits.set(v, 0), 8));
        System.out.println("clear bit 7  = " + Bits.toBinary(Bits.clear(v, 7), 8));
        System.out.println("toggle bit 2 = " + Bits.toBinary(Bits.toggle(v, 2), 8));
        System.out.println("extract bits 4..6 = " + Bits.extract(v, 4, 3) + "  (0b011)");
        System.out.println("insert 0b110 at 4 = " + Bits.toBinary(Bits.insert(v, 4, 3, 0b110), 8));
        System.out.println("popcount(v) kernighan=" + Bits.popcountKernighan(v) + " parallel=" + Bits.popcountParallel(v) + " Long.bitCount=" + Long.bitCount(v));
        System.out.println("lowest set bit of 0b1011_0100 = " + Bits.toBinary(Bits.lowestSetBit(v), 8));
        System.out.println("roundUpToPowerOfTwo(1000) = " + Bits.roundUpToPowerOfTwo(1000) + ", isPowerOfTwo(1024) = " + Bits.isPowerOfTwo(1024));
        System.out.println();
    }

    static void demoFlags() {
        System.out.println("== Account flags: five booleans in one int ==");
        AccountFlags f = AccountFlags.none().with(AccountFlags.KYC_VERIFIED).with(AccountFlags.PAYOUTS_ENABLED);
        System.out.println(f + " bits=" + Bits.toBinary(f.bits(), 5) + " canPayOut=" + f.canPayOut());
        f = f.with(AccountFlags.CHARGEBACK_HOLD);
        System.out.println(f + " bits=" + Bits.toBinary(f.bits(), 5) + " canPayOut=" + f.canPayOut());
        f = f.without(AccountFlags.CHARGEBACK_HOLD).with(AccountFlags.HIGH_RISK);
        System.out.println(f + " bits=" + Bits.toBinary(f.bits(), 5) + " canPayOut=" + f.canPayOut() + " (high risk alone does not block)");
        System.out.println();
    }

    static void demoBitmap() {
        System.out.println("== Bitmap: which of 1,000,000 merchants settled today ==");
        int merchants = 1_000_000;
        Bitmap settledToday = new Bitmap(merchants), activeThisMonth = new Bitmap(merchants);
        for (int m = 0; m < merchants; m += 7) settledToday.set(m);
        for (int m = 0; m < merchants; m += 5) activeThisMonth.set(m);
        Bitmap both = activeThisMonth.and(settledToday);
        System.out.printf("memory: %d longs = %d KB for %d merchants (a boolean[] would be %d KB, a HashSet<Integer> about %d KB)%n",
                settledToday.wordsAllocated(), settledToday.wordsAllocated() * 8 / 1024, merchants, merchants / 1024, merchants / 7 * 48 / 1024);
        System.out.println("settled today: " + settledToday.cardinality() + ", active this month: " + activeThisMonth.cardinality()
                + ", both: " + both.cardinality());
        System.out.println("first five settled merchants: " + firstN(settledToday, 5) + "; first set bit at or after 100: " + settledToday.nextSetBit(100));
        System.out.println();
    }

    private static List<Integer> firstN(Bitmap b, int n) {
        List<Integer> out = new ArrayList<>();
        for (int i = b.nextSetBit(0); i >= 0 && out.size() < n; i = b.nextSetBit(i + 1)) out.add(i);
        return out;
    }

    static void demoIso8583() {
        System.out.println("== ISO 8583 bitmap ==");
        Set<Integer> auth = new TreeSet<>(List.of(2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49));
        String primary = Iso8583Bitmap.encode(auth);
        System.out.println("0100 authorisation request fields " + auth);
        System.out.println("primary bitmap hex   = " + primary);
        System.out.println("primary bitmap bits  = " + Bits.toBinary(Long.parseUnsignedLong(primary, 16), 64));
        System.out.println("decoded              = " + Iso8583Bitmap.decode(primary));
        Set<Integer> withSecondary = new TreeSet<>(auth);
        withSecondary.add(102);
        String both = Iso8583Bitmap.encode(withSecondary);
        System.out.println("adding field 102 (account id 1) sets field 1 and appends a secondary bitmap:");
        System.out.println("bitmaps hex          = " + both + "  (" + both.length() + " hex chars)");
        System.out.println("decoded              = " + Iso8583Bitmap.decode(both));
        System.out.println();
    }

    static void demoSnowflake() {
        System.out.println("== Snowflake-style ids: 41 + 10 + 12 bits ==");
        long[] fakeNow = {SnowflakeId.EPOCH_MILLIS + 86_400_000L}; // one day after the epoch
        int[] calls = {0};
        LongSupplier clock = () -> { if (++calls[0] % 5_000 == 0) fakeNow[0]++; return fakeNow[0]; }; // advances 1 ms every 5,000 reads
        SnowflakeId.Generator gen = new SnowflakeId.Generator(7, clock);
        long prev = -1;
        boolean monotonic = true;
        long first = 0;
        for (int i = 0; i < 10_000; i++) {
            long id = gen.next();
            if (i == 0) first = id;
            if (id <= prev) monotonic = false;
            prev = id;
        }
        SnowflakeId.Parts p = SnowflakeId.decompose(first);
        System.out.println("first id = " + first + " = " + Bits.toBinary(first, 64));
        System.out.println("  timestamp " + p.timestampMillis() + " ms, worker " + p.worker() + ", sequence " + p.sequence());
        System.out.println("10,000 ids from one worker: strictly increasing = " + monotonic + ", sequence overflows waited out = " + gen.waits());
        System.out.println("last id decomposed: " + SnowflakeId.decompose(prev));
        System.out.println();
    }

    static void demoTwosComplementTraps() {
        System.out.println("== Two's complement traps ==");
        System.out.println("Math.abs(Long.MIN_VALUE)      = " + Math.abs(Long.MIN_VALUE) + "  (still negative: there is no +2^63)");
        System.out.println("Long.MAX_VALUE + 1            = " + (Long.MAX_VALUE + 1) + "  (wraps silently; Math.addExact throws)");
        System.out.println("(byte) 0x80                   = " + (byte) 0x80 + "  and (byte)0x80 & 0xFF = " + ((byte) 0x80 & 0xFF) + "  (mask before you widen)");
        System.out.println("-8 >> 1  (arithmetic)         = " + (-8 >> 1) + "   -8 >>> 1 (logical) = " + (-8 >>> 1));
        System.out.println("1 << 31  (int)                = " + (1 << 31) + "  1L << 31 (long) = " + (1L << 31));
        System.out.println("1 << 32  (int, shift mod 32)  = " + (1 << 32) + "  (JLS 15.19: only the low 5 bits of the shift count are used)");
        int lo = 2_000_000_000, hi = 2_100_000_000;
        System.out.println("(lo + hi) / 2 with int        = " + ((lo + hi) / 2) + "  vs  lo + ((hi - lo) >>> 1) = " + (lo + ((hi - lo) >>> 1)) + "  (the binary-search midpoint bug)");
        System.out.println("-7 / 2 = " + (-7 / 2) + "  -7 >> 1 = " + (-7 >> 1) + "  (division truncates toward zero, shift floors)");
        System.out.println("-7 % 2 = " + (-7 % 2) + "   -7 & 1 = " + (-7 & 1) + "  (use & for \"is odd\" on negatives)");
    }

    public static void main(String[] args) {
        demoMasksAndFields();
        demoFlags();
        demoBitmap();
        demoIso8583();
        demoSnowflake();
        demoTwosComplementTraps();
    }
}
