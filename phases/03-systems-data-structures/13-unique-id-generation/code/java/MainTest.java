// Tests for phases/03-systems-data-structures/13-unique-id-generation. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Random;
import java.util.Set;

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
        long base = 1_760_000_000_000L;

        // Uuid text form and parsing
        Uuid known = new Uuid(0x0199c82cc00072c9L, 0xbd984a5ce5b3f441L);
        check("uuid toString is 8-4-4-4-12 lowercase hex", known.toString().equals("0199c82c-c000-72c9-bd98-4a5ce5b3f441"));
        check("uuid parse round-trips", Uuid.parse(known.toString()).equals(known));
        check("uuid parse accepts uppercase", Uuid.parse(known.toString().toUpperCase()).equals(known));
        check("uuid version and variant nibbles", known.version() == 7 && known.variant() == 2);
        check("uuid v7 timestamp is the top 48 bits", known.timestampMillis() == 0x0199c82cc000L);
        check("uuid nil renders as all zeros", new Uuid(0, 0).toString().equals("00000000-0000-0000-0000-000000000000"));
        check("uuid compare is unsigned", new Uuid(0x8000000000000000L, 0).compareTo(new Uuid(0x7fffffffffffffffL, 0)) > 0);
        checkThrows("uuid parse rejects bad shape", IllegalArgumentException.class, () -> Uuid.parse("0199c82cc00072c9bd984a5ce5b3f441"));

        // UUIDv4: version/variant bits set, everything else random, no order
        UuidV4Generator v4 = new UuidV4Generator(new Random(1));
        Set<Uuid> seen4 = new HashSet<>();
        boolean bitsOk = true;
        for (int i = 0; i < 10_000; i++) {
            Uuid u = v4.next();
            bitsOk &= u.version() == 4 && u.variant() == 2;
            seen4.add(u);
        }
        check("v4: version 4 and variant 2 on every id", bitsOk);
        check("v4: 10000 ids, no duplicates", seen4.size() == 10_000);
        check("v4: seeded generator is reproducible", new UuidV4Generator(new Random(1)).next().equals(new UuidV4Generator(new Random(1)).next()));

        // UUIDv7: time-ordered, monotonic in one ms, survives a backwards clock
        ManualClock c7 = new ManualClock(base);
        UuidV7Generator v7 = new UuidV7Generator(c7, new Random(2));
        Uuid first = v7.next();
        check("v7: timestamp field equals the clock", first.timestampMillis() == base && first.version() == 7 && first.variant() == 2);
        List<Uuid> burst = new ArrayList<>();
        for (int i = 0; i < 5_000; i++) burst.add(v7.next());
        check("v7: 5000 ids in a frozen millisecond are strictly increasing", Main.isStrictlyIncreasing(burst));
        check("v7: counter overflow borrows the next millisecond", burst.get(4_999).timestampMillis() >= base + 1);
        check("v7: same-ms ids share the timestamp and differ in the counter", burst.get(0).timestampMillis() == burst.get(1).timestampMillis()
                && (burst.get(1).msb() & 0xFFF) == (burst.get(0).msb() & 0xFFF) + 1);
        c7.advance(10);
        Uuid later = v7.next();
        check("v7: clock advance resets to the wall clock", later.timestampMillis() == base + 10 && later.compareTo(burst.get(4_999)) > 0);
        c7.set(base - 60_000);
        Uuid afterStepBack = v7.next();
        check("v7: clock stepping back keeps the last timestamp and stays increasing", afterStepBack.timestampMillis() == base + 10 && afterStepBack.compareTo(later) > 0);
        check("v7: string order equals bit order", later.toString().compareTo(burst.get(0).toString()) > 0);

        // ULID: encoding, spec vector, round trip, lenient decode, monotonic mode
        check("ulid: spec timestamp vector 01ARZ3NDEK decodes to 1469922850259", Ulid.parse("01ARZ3NDEKTSV4RRFFQ69G5FAV").timestampMillis() == 1_469_922_850_259L);
        check("ulid: max timestamp encodes as 7ZZZZZZZZZ", new Ulid((1L << 48) - 1, 0, 0).toString().startsWith("7ZZZZZZZZZ"));
        check("ulid: zero renders as 26 zeros", new Ulid(0, 0, 0).toString().equals("00000000000000000000000000"));
        Ulid vec = new Ulid(1_469_922_850_259L, 0xABCD, 0x0123456789ABCDEFL);
        check("ulid: parse(toString) round-trips the 80 random bits", Ulid.parse(vec.toString()).equals(vec));
        check("ulid: lowercase and I/L/O are accepted on decode", Ulid.parse(vec.toString().toLowerCase().replace('1', 'l').replace('0', 'o')).equals(vec));
        check("ulid: always 26 characters from the alphabet", vec.toString().length() == 26 && vec.toString().chars().allMatch(ch -> Ulid.ALPHABET.indexOf(ch) >= 0));
        checkThrows("ulid: rejects U", IllegalArgumentException.class, () -> Ulid.parse("01ARZ3NDEKTSV4RRFFQ69G5FAU"));
        checkThrows("ulid: rejects timestamp overflow 8ZZZ...", IllegalArgumentException.class, () -> Ulid.parse("8ZZZZZZZZZZZZZZZZZZZZZZZZZ"));
        checkThrows("ulid: rejects wrong length", IllegalArgumentException.class, () -> Ulid.parse("01ARZ3NDEK"));
        ManualClock cu = new ManualClock(base);
        UlidGenerator ug = new UlidGenerator(cu, new Random(3));
        List<Ulid> uburst = new ArrayList<>();
        List<String> ustr = new ArrayList<>();
        for (int i = 0; i < 3_000; i++) { Ulid u = ug.next(); uburst.add(u); ustr.add(u.toString()); }
        check("ulid: monotonic in a frozen ms (random part + 1)", Main.isStrictlyIncreasing(uburst) && uburst.get(1).randomLo() == uburst.get(0).randomLo() + 1);
        check("ulid: string order equals generation order", Main.isLexicallyIncreasing(ustr));
        UlidGenerator carry = new UlidGenerator(new ManualClock(base), new Random(4));
        carry.resume(new Ulid(base, 5, -1L));
        Ulid carried = carry.next();
        check("ulid: increment carries from the low word into the high word", carried.randomHi() == 6 && carried.randomLo() == 0);
        UlidGenerator full = new UlidGenerator(new ManualClock(base), new Random(5));
        full.resume(new Ulid(base, 0xFFFF, -1L));
        checkThrows("ulid: randomness overflow in one ms is an error", IllegalStateException.class, full::next);
        UlidGenerator resumed = new UlidGenerator(new ManualClock(base + 1), new Random(6));
        resumed.resume(new Ulid(base, 0xFFFF, -1L));
        check("ulid: a later clock after resume starts a fresh random part", resumed.next().timestampMillis() == base + 1);

        // Snowflake: layout, monotonicity, sequence borrow, drift handling
        ManualClock cs = new ManualClock(base);
        SnowflakeGenerator sf = new SnowflakeGenerator(cs, 17, 5);
        long id0 = sf.next();
        check("snowflake: fields decode", SnowflakeGenerator.timestampMillisOf(id0) == base && SnowflakeGenerator.workerIdOf(id0) == 17 && SnowflakeGenerator.sequenceOf(id0) == 0);
        check("snowflake: positive long", id0 > 0);
        List<Long> fburst = new ArrayList<>();
        for (int i = 0; i < 5_000; i++) fburst.add(sf.next());
        check("snowflake: 5000 ids in one ms strictly increasing", Main.isStrictlyIncreasing(fburst));
        check("snowflake: sequence wraps after 4096 and borrows one ms", sf.borrowedMillis == 1 && SnowflakeGenerator.timestampMillisOf(fburst.get(4_999)) == base + 1);
        cs.advance(1);
        long sameAsBorrowed = sf.next();
        check("snowflake: wall clock catching up to borrowed time continues the sequence", SnowflakeGenerator.timestampMillisOf(sameAsBorrowed) == base + 1 && sameAsBorrowed > fburst.get(4_999));
        cs.set(base - 2);
        long smallDrift = sf.next();
        check("snowflake: small backwards drift keeps issuing on the last timestamp", smallDrift > sameAsBorrowed && SnowflakeGenerator.timestampMillisOf(smallDrift) == base + 1);
        cs.set(base - 10_000);
        checkThrows("snowflake: large backwards drift refuses to issue", ClockMovedBackwardsException.class, sf::next);
        checkThrows("snowflake: worker id out of range", IllegalArgumentException.class, () -> new SnowflakeGenerator(cs, 1024, 5));
        SnowflakeGenerator w1 = new SnowflakeGenerator(new ManualClock(base), 1, 5), w2 = new SnowflakeGenerator(new ManualClock(base), 2, 5);
        check("snowflake: two workers in the same ms never collide", w1.next() != w2.next());
        check("snowflake: ids from a later ms sort after any id from an earlier ms regardless of worker",
                new SnowflakeGenerator(new ManualClock(base + 1), 0, 5).next() > new SnowflakeGenerator(new ManualClock(base), 1023, 5).next());

        // Collision math
        check("birthday: 32 bits, 77163 ids is about 50%", Math.abs(Main.collisionProbability(77_163, 32) - 0.5) < 0.01);
        check("birthday: 122 bits, 1e12 ids is negligible", Main.collisionProbability(1e12, 122) < 1e-12);
        check("birthday: ids for 50% at 32 bits is about 77163", Math.abs(Main.idsForCollisionProbability(0.5, 32) - 77_163) < 10);
        check("birthday: probability is monotone in n", Main.collisionProbability(1e6, 62) < Main.collisionProbability(1e7, 62));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

}
