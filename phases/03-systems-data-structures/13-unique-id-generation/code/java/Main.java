// Lesson: phases/03-systems-data-structures/13-unique-id-generation/docs/en.md
// Unique ID Generation: UUIDs, ULIDs, and Snowflake
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 9562 (UUIDs, versions 4 and 7, monotonic counters); ULID spec (ulid/spec on GitHub, Crockford base32);
// Twitter Snowflake announcement (2010) for the 41/10/12 layout. Java 21, standard library only.

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** Injected time source in milliseconds since the Unix epoch. */
interface Clock {
    long nowMillis();
}

final class ManualClock implements Clock {
    private long now;

    ManualClock(long startMillis) { this.now = startMillis; }

    @Override public long nowMillis() { return now; }

    void advance(long millis) { now += millis; }

    /** Clocks do go backwards in real systems (NTP step, VM migration). Tests need to simulate it. */
    void set(long millis) { now = millis; }
}

/** 128 bits as two longs, with the RFC 9562 text form. Built here rather than using java.util.UUID so the bits are visible. */
record Uuid(long msb, long lsb) implements Comparable<Uuid> {
    private static final char[] HEX = "0123456789abcdef".toCharArray();

    int version() { return (int) ((msb >>> 12) & 0xF); }

    /** 2 for the RFC 9562 variant (bit pattern 10xx in the top of the low half). */
    int variant() { return (int) ((lsb >>> 62) & 0x3); }

    /** For version 7: the 48-bit Unix millisecond timestamp in the top of the high half. */
    long timestampMillis() { return msb >>> 16; }

    @Override public int compareTo(Uuid o) {
        int c = Long.compareUnsigned(msb, o.msb);
        return c != 0 ? c : Long.compareUnsigned(lsb, o.lsb);
    }

    @Override public String toString() {
        char[] out = new char[36];
        int pos = 0;
        pos = hex(out, pos, msb >>> 32, 8); out[pos++] = '-';
        pos = hex(out, pos, msb >>> 16, 4); out[pos++] = '-';
        pos = hex(out, pos, msb, 4); out[pos++] = '-';
        pos = hex(out, pos, lsb >>> 48, 4); out[pos++] = '-';
        hex(out, pos, lsb, 12);
        return new String(out);
    }

    private static int hex(char[] out, int pos, long value, int digits) {
        for (int i = digits - 1; i >= 0; i--) {
            out[pos + i] = HEX[(int) (value & 0xF)];
            value >>>= 4;
        }
        return pos + digits;
    }

    static Uuid parse(String text) {
        if (text.length() != 36 || text.charAt(8) != '-' || text.charAt(13) != '-' || text.charAt(18) != '-' || text.charAt(23) != '-') {
            throw new IllegalArgumentException("not a UUID: " + text);
        }
        String hex = text.replace("-", "");
        return new Uuid(Long.parseUnsignedLong(hex.substring(0, 16), 16), Long.parseUnsignedLong(hex.substring(16), 16));
    }
}

/** RFC 9562 version 4: 122 random bits. No order, no time, no coordination. */
final class UuidV4Generator {
    private final Random rng;

    UuidV4Generator(Random rng) { this.rng = rng; }

    Uuid next() {
        long msb = rng.nextLong(), lsb = rng.nextLong();
        msb = (msb & ~0xF000L) | 0x4000L;                    // version nibble = 4
        lsb = (lsb & 0x3FFFFFFFFFFFFFFFL) | 0x8000000000000000L;   // variant bits = 10
        return new Uuid(msb, lsb);
    }
}

/**
 * RFC 9562 version 7: 48-bit Unix ms timestamp, 12-bit counter (rand_a, "method 1"), 62 random bits (rand_b).
 * Monotonic within one generator: same millisecond increments the counter; a backwards clock keeps using the
 * last timestamp; a counter overflow borrows the next millisecond.
 */
final class UuidV7Generator {
    private final Clock clock;
    private final Random rng;
    private long lastMillis = -1;
    private int counter;

    UuidV7Generator(Clock clock, Random rng) {
        this.clock = clock;
        this.rng = rng;
    }

    Uuid next() {
        long now = clock.nowMillis();
        if (now <= lastMillis) {                              // same ms, or clock went backwards
            counter++;
            if (counter > 0xFFF) { lastMillis++; counter = rng.nextInt(1 << 11); }   // borrow the next ms
        } else {
            lastMillis = now;
            counter = rng.nextInt(1 << 11);                    // leave the top counter bit clear: room to count
        }
        long msb = (lastMillis << 16) | 0x7000L | counter;
        long lsb = (rng.nextLong() & 0x3FFFFFFFFFFFFFFFL) | 0x8000000000000000L;
        return new Uuid(msb, lsb);
    }
}

/** ULID: 48-bit ms timestamp + 80 random bits, Crockford base32, 26 characters, lexically sortable. */
record Ulid(long timestampMillis, long randomHi, long randomLo) implements Comparable<Ulid> {
    static final String ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";   // no I, L, O, U
    private static final int[] DECODE = new int[128];

    static {
        java.util.Arrays.fill(DECODE, -1);
        for (int i = 0; i < ALPHABET.length(); i++) {
            char c = ALPHABET.charAt(i);
            DECODE[c] = i;
            DECODE[Character.toLowerCase(c)] = i;
        }
        // Crockford's alphabet excludes I, L, O, U to avoid misreads; decoding accepts the first three leniently.
        DECODE['I'] = DECODE['i'] = 1; DECODE['L'] = DECODE['l'] = 1; DECODE['O'] = DECODE['o'] = 0;
    }

    Ulid {
        if (timestampMillis < 0 || timestampMillis >= (1L << 48)) throw new IllegalArgumentException("timestamp must fit in 48 bits");
        if (randomHi < 0 || randomHi >= (1L << 16)) throw new IllegalArgumentException("randomHi must fit in 16 bits");
    }

    @Override public int compareTo(Ulid o) {
        int c = Long.compare(timestampMillis, o.timestampMillis);
        if (c != 0) return c;
        c = Long.compare(randomHi, o.randomHi);
        return c != 0 ? c : Long.compareUnsigned(randomLo, o.randomLo);
    }

    /** 10 chars of timestamp (50 bits, top two zero) + 16 chars of randomness (80 bits). */
    @Override public String toString() {
        char[] out = new char[26];
        long t = timestampMillis;
        for (int i = 9; i >= 0; i--) { out[i] = ALPHABET.charAt((int) (t & 0x1F)); t >>>= 5; }
        // 80 random bits = randomHi (16) followed by randomLo (64): emit from the low end, 5 bits at a time
        long lo = randomLo, hi = randomHi;
        for (int i = 25; i >= 10; i--) {
            out[i] = ALPHABET.charAt((int) (lo & 0x1F));
            lo = (lo >>> 5) | ((hi & 0x1F) << 59);
            hi >>>= 5;
        }
        return new String(out);
    }

    static Ulid parse(String text) {
        if (text.length() != 26) throw new IllegalArgumentException("ULID must be 26 characters: " + text);
        long t = 0;
        for (int i = 0; i < 10; i++) t = (t << 5) | digit(text.charAt(i));
        if (t >= (1L << 48)) throw new IllegalArgumentException("ULID timestamp overflows 48 bits: " + text);
        long hi = 0, lo = 0;
        for (int i = 10; i < 26; i++) {
            int d = digit(text.charAt(i));
            hi = (hi << 5) | (lo >>> 59);
            lo = (lo << 5) | d;
        }
        return new Ulid(t, hi & 0xFFFF, lo);
    }

    private static int digit(char c) {
        int d = c < 128 ? DECODE[c] : -1;
        if (d < 0) throw new IllegalArgumentException("invalid Crockford base32 character: " + c);
        return d;
    }
}

/** Monotonic ULID generator: same millisecond increments the 80-bit random part by one (ULID spec, "monotonicity"). */
final class UlidGenerator {
    private final Clock clock;
    private final Random rng;
    private Ulid last;

    UlidGenerator(Clock clock, Random rng) {
        this.clock = clock;
        this.rng = rng;
    }

    /** Continue after a restart from the last id this process issued, so monotonicity survives the restart. */
    void resume(Ulid lastIssued) { this.last = lastIssued; }

    Ulid next() {
        long now = clock.nowMillis();
        if (last != null && now <= last.timestampMillis()) {
            long lo = last.randomLo() + 1;
            long hi = last.randomHi() + (lo == 0 ? 1 : 0);
            if (hi > 0xFFFF) throw new IllegalStateException("ULID randomness overflowed within one millisecond");
            last = new Ulid(last.timestampMillis(), hi, lo);
        } else {
            last = new Ulid(now, rng.nextInt(1 << 16), rng.nextLong());
        }
        return last;
    }
}

/**
 * Snowflake: 63 usable bits as 41-bit ms since a custom epoch | 10-bit worker id | 12-bit sequence.
 * Sortable by time within a worker, 4,096 ids per ms per worker, no coordination beyond assigning worker ids.
 */
final class SnowflakeGenerator {
    static final long EPOCH_MILLIS = 1_704_067_200_000L;          // 2024-01-01T00:00:00Z
    static final int WORKER_BITS = 10, SEQUENCE_BITS = 12;
    static final long MAX_WORKER = (1L << WORKER_BITS) - 1, MAX_SEQUENCE = (1L << SEQUENCE_BITS) - 1;
    static final long MAX_TIMESTAMP = (1L << 41) - 1;

    private final Clock clock;
    private final long workerId;
    private final long maxBackwardsMillis;
    private long lastMillis = -1;
    private long sequence;
    long borrowedMillis;                                         // how many ms logical time ran ahead of the clock

    SnowflakeGenerator(Clock clock, long workerId, long maxBackwardsMillis) {
        if (workerId < 0 || workerId > MAX_WORKER) throw new IllegalArgumentException("worker id must be 0.." + MAX_WORKER);
        this.clock = clock;
        this.workerId = workerId;
        this.maxBackwardsMillis = maxBackwardsMillis;
    }

    long next() {
        long now = clock.nowMillis() - EPOCH_MILLIS;
        if (now < lastMillis) {
            if (lastMillis - now > maxBackwardsMillis) {
                throw new ClockMovedBackwardsException(lastMillis - now);
            }
            now = lastMillis;                                    // small drift: keep issuing on the last timestamp
        }
        if (now == lastMillis) {
            sequence = (sequence + 1) & MAX_SEQUENCE;
            if (sequence == 0) { lastMillis++; borrowedMillis++; now = lastMillis; }   // 4096 in one ms: borrow the next
        } else {
            lastMillis = now;
            sequence = 0;
        }
        if (lastMillis > MAX_TIMESTAMP) throw new IllegalStateException("snowflake epoch exhausted");
        return (lastMillis << (WORKER_BITS + SEQUENCE_BITS)) | (workerId << SEQUENCE_BITS) | sequence;
    }

    static long timestampMillisOf(long id) { return (id >>> (WORKER_BITS + SEQUENCE_BITS)) + EPOCH_MILLIS; }
    static long workerIdOf(long id) { return (id >>> SEQUENCE_BITS) & MAX_WORKER; }
    static long sequenceOf(long id) { return id & MAX_SEQUENCE; }
}

final class ClockMovedBackwardsException extends RuntimeException {
    final long byMillis;

    ClockMovedBackwardsException(long byMillis) {
        super("clock moved backwards by " + byMillis + " ms; refusing to issue ids that could collide");
        this.byMillis = byMillis;
    }
}

public final class Main {
    /** Birthday bound: probability of at least one collision among n ids drawn uniformly from 2^bits values. */
    static double collisionProbability(double n, int bits) {
        double pairs = n * (n - 1) / 2.0;
        return 1.0 - Math.exp(-pairs / Math.pow(2.0, bits));
    }

    /** How many ids until the collision probability reaches p: n ~ sqrt(2 * 2^bits * ln(1/(1-p))). */
    static double idsForCollisionProbability(double p, int bits) {
        return Math.sqrt(2.0 * Math.pow(2.0, bits) * Math.log(1.0 / (1.0 - p)));
    }

    static <T extends Comparable<T>> boolean isStrictlyIncreasing(List<T> items) {
        for (int i = 1; i < items.size(); i++) if (items.get(i - 1).compareTo(items.get(i)) >= 0) return false;
        return true;
    }

    static boolean isLexicallyIncreasing(List<String> items) {
        for (int i = 1; i < items.size(); i++) if (items.get(i - 1).compareTo(items.get(i)) >= 0) return false;
        return true;
    }

    public static void main(String[] args) {
        Random rng = new Random(7);
        ManualClock clock = new ManualClock(1_760_000_000_000L);   // 2025-10-09T08:53:20Z

        System.out.println("== 1. Four ids for the same posting, four shapes ==");
        UuidV4Generator v4 = new UuidV4Generator(rng);
        UuidV7Generator v7 = new UuidV7Generator(clock, rng);
        UlidGenerator ulid = new UlidGenerator(clock, rng);
        SnowflakeGenerator flake = new SnowflakeGenerator(clock, 17, 5);
        Uuid a4 = v4.next(); Uuid a7 = v7.next(); Ulid au = ulid.next(); long af = flake.next();
        System.out.println("  UUIDv4    " + a4 + "  version=" + a4.version() + " variant=" + a4.variant() + "  (122 random bits)");
        System.out.println("  UUIDv7    " + a7 + "  version=" + a7.version() + " ts=" + a7.timestampMillis() + " (48-bit ms, 12-bit counter, 62 random)");
        System.out.println("  ULID      " + au + "  ts=" + au.timestampMillis() + " (48-bit ms, 80 random, base32)");
        System.out.println("  Snowflake " + af + "  ts=" + SnowflakeGenerator.timestampMillisOf(af) + " worker=" + SnowflakeGenerator.workerIdOf(af) + " seq=" + SnowflakeGenerator.sequenceOf(af) + " (63 bits)");

        System.out.println();
        System.out.println("== 2. Sortability: 1000 ids over 1000 ms, then sort the strings ==");
        List<Uuid> v4s = new ArrayList<>(), v7s = new ArrayList<>();
        List<Ulid> ulids = new ArrayList<>();
        List<Long> flakes = new ArrayList<>();
        List<String> v4Str = new ArrayList<>(), v7Str = new ArrayList<>(), ulidStr = new ArrayList<>();
        for (int i = 0; i < 1000; i++) {
            clock.advance(1);
            Uuid x4 = v4.next(); Uuid x7 = v7.next(); Ulid xu = ulid.next(); long xf = flake.next();
            v4s.add(x4); v7s.add(x7); ulids.add(xu); flakes.add(xf);
            v4Str.add(x4.toString()); v7Str.add(x7.toString()); ulidStr.add(xu.toString());
        }
        System.out.println("  generation order == string sort order?  v4=" + isLexicallyIncreasing(v4Str) + "  v7=" + isLexicallyIncreasing(v7Str) + "  ulid=" + isLexicallyIncreasing(ulidStr) + "  snowflake=" + isStrictlyIncreasing(flakes));
        List<String> sorted4 = new ArrayList<>(v4Str);
        sorted4.sort(null);
        int displaced = 0;
        for (int i = 0; i < 1000; i++) if (!sorted4.get(i).equals(v4Str.get(i))) displaced++;
        System.out.println("  v4: " + displaced + " of 1000 ids change position when sorted -> random B-tree page writes");

        System.out.println();
        System.out.println("== 3. Monotonic within one millisecond ==");
        ManualClock frozen = new ManualClock(1_760_000_000_000L);
        UuidV7Generator fv7 = new UuidV7Generator(frozen, new Random(1));
        UlidGenerator fu = new UlidGenerator(frozen, new Random(1));
        SnowflakeGenerator ff = new SnowflakeGenerator(frozen, 3, 5);
        List<Uuid> burst7 = new ArrayList<>(); List<Ulid> burstU = new ArrayList<>(); List<Long> burstF = new ArrayList<>();
        for (int i = 0; i < 5000; i++) { burst7.add(fv7.next()); burstU.add(fu.next()); burstF.add(ff.next()); }
        System.out.println("  5000 ids, clock frozen: v7 increasing=" + isStrictlyIncreasing(burst7) + " ulid increasing=" + isStrictlyIncreasing(burstU) + " snowflake increasing=" + isStrictlyIncreasing(burstF));
        System.out.println("  v7 first two: " + burst7.get(0) + " -> " + burst7.get(1) + " (counter +1)");
        System.out.println("  ulid first two: " + burstU.get(0) + " -> " + burstU.get(1) + " (random part +1)");
        System.out.println("  snowflake borrowed " + ff.borrowedMillis + " ms of logical time to issue 5000 ids at 4096/ms");

        System.out.println();
        System.out.println("== 4. The clock moves backwards ==");
        ManualClock drift = new ManualClock(1_760_000_000_000L);
        SnowflakeGenerator sf = new SnowflakeGenerator(drift, 9, 5);
        long before = sf.next();
        drift.set(1_760_000_000_000L - 3);
        long after = sf.next();
        System.out.println("  3 ms backwards (within tolerance 5): issued " + before + " then " + after + ", still increasing=" + (after > before));
        drift.set(1_760_000_000_000L - 60_000);
        try {
            sf.next();
        } catch (ClockMovedBackwardsException e) {
            System.out.println("  60 s backwards: " + e.getMessage());
        }
        UuidV7Generator dv7 = new UuidV7Generator(drift, new Random(2));
        drift.set(1_760_000_000_000L);
        Uuid u1 = dv7.next();
        drift.set(1_760_000_000_000L - 60_000);
        Uuid u2 = dv7.next();
        System.out.println("  v7 after a 60 s step back: keeps the last timestamp, increasing=" + (u2.compareTo(u1) > 0));

        System.out.println();
        System.out.println("== 5. Collision math (birthday bound) ==");
        System.out.printf("  UUIDv4, 122 random bits: 10^12 ids -> p = %.1e; 50%% at %.2e ids%n", collisionProbability(1e12, 122), idsForCollisionProbability(0.5, 122));
        System.out.printf("  ULID, 80 random bits per ms: 10^6 ids in one ms -> p = %.1e; 10^9 in one ms -> p = %.1e%n", collisionProbability(1e6, 80), collisionProbability(1e9, 80));
        System.out.printf("  UUIDv7, 62 random bits per ms across uncoordinated generators: 10^6 ids in one ms -> p = %.1e%n", collisionProbability(1e6, 62));
        System.out.printf("  a 32-bit random id: 10^5 ids -> p = %.3f (50%% at about 77,000 ids)%n", collisionProbability(1e5, 32));
        System.out.println("  Snowflake: zero collision probability by construction, 4096 ids/ms/worker, 1024 workers, 69.7 years of 41-bit ms");

        System.out.println();
        System.out.println("== 6. Round trips ==");
        System.out.println("  Uuid.parse(toString) == original: " + Uuid.parse(a7.toString()).equals(a7));
        System.out.println("  Ulid.parse(toString) == original: " + Ulid.parse(au.toString()).equals(au));
        System.out.println("  Ulid lenient decode: parse accepts lowercase and maps I/L->1, O->0: " + Ulid.parse(au.toString().toLowerCase()).equals(au));
    }
}
