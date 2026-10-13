// Tests for phases/07-storage-and-transactions/02-write-ahead-logs. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static byte[] bytes(String s) { return s.getBytes(StandardCharsets.UTF_8); }
    static Money usd(long cents) { return Money.ofMinor(cents, Currency.USD); }

    static List<String> replayStrings(WriteAheadLog log) {
        List<String> seen = new ArrayList<>();
        log.replay(r -> seen.add(new String(r.payload(), StandardCharsets.UTF_8)));
        return seen;
    }

    public static void main(String[] args) {
        // --- framing ---
        check("crc32 covers seq and payload: same payload, different seq, different crc",
                WriteAheadLog.crc32(1, bytes("hello")) != WriteAheadLog.crc32(2, bytes("hello")));
        check("crc32 known value for seq 1 + \"hello\"", WriteAheadLog.crc32(1, bytes("hello")) == 0x341ac4bcL);
        byte[] frame = WriteAheadLog.encode(1, bytes("hello"));
        check("frame is 16-byte header + payload", frame.length == WriteAheadLog.HEADER_BYTES + 5);
        check("length prefix is big-endian 5", ByteBuffer.wrap(frame).getInt(0) == 5);
        check("seq is stored at bytes 8..15", ByteBuffer.wrap(frame).getLong(8) == 1L);
        checkThrows("oversized payload rejected at encode", IllegalArgumentException.class,
                () -> WriteAheadLog.encode(1, new byte[WriteAheadLog.MAX_PAYLOAD_BYTES + 1]));

        // --- round trip ---
        MemoryStorage disk = new MemoryStorage();
        WriteAheadLog log = new WriteAheadLog(disk);
        long s1 = log.append(bytes("a"));
        long s2 = log.append(bytes("bb"));
        long s3 = log.append(bytes(""));
        check("sequence numbers start at 1 and are contiguous", s1 == 1 && s2 == 2 && s3 == 3);
        check("size after three appends", log.size() == 17 + 18 + 16);
        log.sync();
        WriteAheadLog reopened = new WriteAheadLog(disk);
        List<String> seen = new ArrayList<>();
        ReplayResult r = reopened.replay(rec -> seen.add(rec.seq() + ":" + new String(rec.payload(), StandardCharsets.UTF_8)));
        check("replay returns every record in order with its seq", seen.equals(List.of("1:a", "2:bb", "3:")));
        check("replay of clean log is not torn", !r.torn() && r.recordsReplayed() == 3 && r.validLength() == 51 && r.lastSeq() == 3);
        check("next seq continues after replay", reopened.nextSeq() == 4);
        check("empty log replays nothing", new WriteAheadLog(new MemoryStorage()).replay(x -> {}).recordsReplayed() == 0);

        // --- the fsync point ---
        MemoryStorage d2 = new MemoryStorage();
        WriteAheadLog l2 = new WriteAheadLog(d2);
        l2.append(bytes("synced"));
        l2.sync();
        l2.append(bytes("not synced"));
        d2.crash();
        check("unsynced append is lost on crash; synced one survives", replayStrings(new WriteAheadLog(d2)).equals(List.of("synced")));

        // --- torn tail ---
        MemoryStorage d3 = new MemoryStorage();
        WriteAheadLog l3 = new WriteAheadLog(d3);
        l3.append(bytes("one"));
        l3.append(bytes("two"));
        l3.sync();
        l3.append(bytes("three"));
        d3.crashKeepingUnsynced(7);                         // header cut off mid-way
        WriteAheadLog l3b = new WriteAheadLog(d3);
        List<String> got = replayStrings(l3b);
        check("torn header is dropped", got.equals(List.of("one", "two")));
        check("storage truncated back to valid prefix", d3.size() == 19 + 19);
        check("next append after torn recovery reuses the torn seq", l3b.append(bytes("x")) == 3);

        MemoryStorage d4 = new MemoryStorage();
        WriteAheadLog l4 = new WriteAheadLog(d4);
        l4.append(bytes("first"));
        l4.sync();
        l4.append(bytes("second"));
        d4.crashKeepingUnsynced(WriteAheadLog.HEADER_BYTES + 3); // payload cut off
        WriteAheadLog l4b = new WriteAheadLog(d4);
        List<String> got4 = new ArrayList<>();
        ReplayResult r4 = l4b.replay(rec -> got4.add(new String(rec.payload(), StandardCharsets.UTF_8)));
        check("torn payload is dropped and reported", got4.equals(List.of("first")) && r4.torn() && r4.truncatedBytes() == 19);

        // --- corruption ---
        MemoryStorage d5 = new MemoryStorage();
        WriteAheadLog l5 = new WriteAheadLog(d5);
        l5.append(bytes("one"));
        l5.append(bytes("two"));
        l5.append(bytes("three"));
        l5.sync();
        d5.flipBit(19 + WriteAheadLog.HEADER_BYTES);        // first payload byte of record 2
        List<String> got5 = new ArrayList<>();
        try {
            new WriteAheadLog(d5).replay(rec -> got5.add(new String(rec.payload(), StandardCharsets.UTF_8)));
            failures.add("corrupt middle record must throw");
        } catch (CorruptRecordException e) {
            check("corrupt middle record names its offset", e.offset == 19 && e.getMessage().contains("crc mismatch"));
            check("records before the corruption were delivered", got5.equals(List.of("one")));
        }
        check("corrupt log is not truncated", d5.size() == 19 + 19 + 21);

        MemoryStorage d6 = new MemoryStorage();
        WriteAheadLog l6 = new WriteAheadLog(d6);
        l6.append(bytes("first"));
        l6.append(bytes("last"));
        l6.sync();
        d6.flipBit(d6.size() - 1);                          // damage the final payload byte
        List<String> got6 = new ArrayList<>();
        ReplayResult r6 = new WriteAheadLog(d6).replay(rec -> got6.add(new String(rec.payload(), StandardCharsets.UTF_8)));
        check("checksum-failed final frame is treated as torn", got6.equals(List.of("first")) && r6.torn());

        MemoryStorage d7 = new MemoryStorage();
        d7.write(0, WriteAheadLog.encode(1, bytes("one")));
        d7.write(19, WriteAheadLog.encode(3, bytes("three")));   // seq 2 is missing
        d7.sync();
        checkThrows("sequence gap is corruption", CorruptRecordException.class, () -> new WriteAheadLog(d7).replay(x -> {}));

        MemoryStorage d8 = new MemoryStorage();
        d8.write(0, ByteBuffer.allocate(16).putInt(Integer.MAX_VALUE).putInt(0).putLong(1).array());
        d8.write(16, new byte[20]);
        d8.sync();
        checkThrows("implausible length is corruption", CorruptRecordException.class, () -> new WriteAheadLog(d8).replay(x -> {}));

        // --- replay is idempotent ---
        List<String> again = replayStrings(new WriteAheadLog(disk));
        List<String> andAgain = replayStrings(new WriteAheadLog(disk));
        check("replaying the same log twice yields the same records", again.equals(andAgain) && again.size() == 3);

        // --- ledger: write-ahead ordering and crash recovery ---
        MemoryStorage walDisk = new MemoryStorage();
        MemoryStorage snapDisk = new MemoryStorage();
        Ledger ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        int syncsBefore = walDisk.syncCount();
        ledger.credit("acme", usd(10_000));
        check("every commit syncs exactly once", walDisk.syncCount() == syncsBefore + 1);
        check("commit is durable before memory changes", walDisk.durableSize() == walDisk.size() && ledger.balance("acme").equals(usd(10_000)));
        ledger.credit("ann", usd(5_000));
        ledger.transfer("ann", "acme", usd(1_999));
        ledger.debit("acme", usd(1));
        checkThrows("debit beyond balance is rejected", IllegalStateException.class, () -> ledger.debit("ann", usd(999_999)));
        long walAfter = ledger.walSize();
        check("rejected debit appended nothing", walDisk.size() == walAfter);
        checkThrows("zero amount rejected", IllegalArgumentException.class, () -> ledger.credit("ann", usd(0)));
        walDisk.crash();
        Ledger recovered = Ledger.open(walDisk, snapDisk, Currency.USD);
        check("balances restored after crash", recovered.balance("acme").equals(usd(11_998)) && recovered.balance("ann").equals(usd(3_001)));
        check("replay count equals committed records", recovered.lastRecovery().recordsReplayed() == 4 && recovered.appliedSeq() == 4);

        // torn transfer: neither side moves
        WriteAheadLog raw = new WriteAheadLog(walDisk);
        raw.replay(x -> {});
        raw.append(Ledger.encodeOp(Ledger.OP_TRANSFER, "acme", "ann", usd(500)));
        walDisk.crashKeepingUnsynced(20);
        Ledger afterTorn = Ledger.open(walDisk, snapDisk, Currency.USD);
        check("torn transfer changes neither balance", afterTorn.balance("acme").equals(usd(11_998)) && afterTorn.balance("ann").equals(usd(3_001)));
        check("torn transfer truncated from the wal", afterTorn.lastRecovery().torn() && afterTorn.walSize() == walAfter);

        // --- checkpoint ---
        for (int i = 0; i < 20; i++) afterTorn.credit("acme", usd(1));
        long bulky = afterTorn.walSize();
        afterTorn.checkpoint();
        check("checkpoint empties the wal", afterTorn.walSize() == 0 && bulky > 0);
        check("checkpoint writes one snapshot record", afterTorn.snapshotCount() == 1 && snapDisk.size() > 0);
        afterTorn.credit("bob", usd(700));
        Ledger fromSnapshot = Ledger.open(walDisk, snapDisk, Currency.USD);
        check("snapshot + wal restore full state", fromSnapshot.balance("acme").equals(usd(12_018))
                && fromSnapshot.balance("ann").equals(usd(3_001)) && fromSnapshot.balance("bob").equals(usd(700)));
        check("only post-checkpoint records replayed from wal", fromSnapshot.lastRecovery().recordsReplayed() == 1);
        check("sequence numbers continue across the checkpoint", fromSnapshot.appliedSeq() == 25);

        // crash between snapshot sync and wal reset: the wal still holds covered records; seq makes them skippable
        MemoryStorage w9 = new MemoryStorage();
        MemoryStorage s9 = new MemoryStorage();
        Ledger l9 = Ledger.open(w9, s9, Currency.USD);
        l9.credit("a", usd(100));
        l9.credit("a", usd(100));
        long walBytes = l9.walSize();
        // hand-run the first half of checkpoint(): snapshot durable, wal untouched
        Ledger probe = Ledger.open(w9, s9, Currency.USD);
        probe.checkpoint();
        // undo the wal reset by replaying the old bytes back as if the truncate never reached the disk
        MemoryStorage w9b = new MemoryStorage();
        WriteAheadLog rebuild = new WriteAheadLog(w9b);
        rebuild.append(Ledger.encodeOp(Ledger.OP_CREDIT, "a", "", usd(100)));
        rebuild.append(Ledger.encodeOp(Ledger.OP_CREDIT, "a", "", usd(100)));
        rebuild.sync();
        check("rebuilt wal matches the original", w9b.size() == walBytes);
        Ledger l9b = Ledger.open(w9b, s9, Currency.USD);
        check("covered records are skipped, not double-applied", l9b.balance("a").equals(usd(200)));
        check("interrupted checkpoint is finished on open", l9b.walSize() == 0);
        l9b.credit("a", usd(1));
        check("numbering resumes after the covered seq", l9b.appliedSeq() == 3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
