// Lesson: phases/07-storage-and-transactions/02-write-ahead-logs/docs/en.md
// Write-Ahead Logs and Crash Recovery
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Mohan et al., "ARIES: A Transaction Recovery Method" (ACM TODS 17(1), 1992); PostgreSQL manual ch. 28
//          "Reliability and the Write-Ahead Log"; Pillai et al., "All File Systems Are Not Created Equal" (OSDI 2014);
//          CRC-32 per ISO 3309 / ITU-T V.42 (the polynomial behind java.util.zip.CRC32, zlib.crc32 and Go hash/crc32).
// Java 21, standard library only. Same design as the Python and Go versions.

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;
import java.util.zip.CRC32;

/** ISO 4217 currency code. Trimmed copy of the Phase 10, Lesson 01 type: the exponent is not needed here. */
record Currency(String code) {
    static final Currency USD = new Currency("USD");

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
    }
}

/** An exact amount: signed integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    boolean isNegative() { return minorUnits < 0; }

    @Override public String toString() {
        long abs = Math.abs(minorUnits);
        return String.format("%s%d.%02d %s", minorUnits < 0 ? "-" : "", abs / 100, abs % 100, currency.code());
    }
}

/**
 * The durability boundary. A real implementation wraps a FileChannel; the lesson injects an in-memory one so a
 * test can crash the "machine" at any byte and see exactly what survived. write() is buffered: bytes become
 * durable only when sync() returns, which is the contract of fsync(2).
 */
interface Storage {
    long size();
    byte[] read(long offset, int length);
    void write(long offset, byte[] data);
    void truncate(long size);
    void sync();
}

/** In-memory Storage with two copies of the bytes: what the process can see, and what has reached the disk. */
final class MemoryStorage implements Storage {
    private byte[] visible = new byte[0];
    private byte[] durable = new byte[0];
    private int syncCount = 0;

    @Override public long size() { return visible.length; }

    @Override public byte[] read(long offset, int length) {
        if (offset < 0 || offset + length > visible.length) throw new IndexOutOfBoundsException("read past end");
        return Arrays.copyOfRange(visible, (int) offset, (int) offset + length);
    }

    @Override public void write(long offset, byte[] data) {
        int end = (int) offset + data.length;
        if (end > visible.length) visible = Arrays.copyOf(visible, end);
        System.arraycopy(data, 0, visible, (int) offset, data.length);
    }

    @Override public void truncate(long size) {
        if (size < visible.length) visible = Arrays.copyOf(visible, (int) size);
    }

    @Override public void sync() { durable = visible.clone(); syncCount++; }

    int syncCount() { return syncCount; }
    long durableSize() { return durable.length; }

    /** kill -9 or power loss: everything that was not synced is gone. */
    void crash() { visible = durable.clone(); }

    /** Power loss during a write: the first keepBytes of the unsynced tail reached the disk, the rest did not. */
    void crashKeepingUnsynced(int keepBytes) {
        int keep = Math.min(keepBytes, Math.max(0, visible.length - durable.length));
        visible = Arrays.copyOf(visible, durable.length + keep);
        System.arraycopy(durable, 0, visible, 0, durable.length);
        durable = visible.clone();
    }

    /** Bit rot: flip one bit in a byte that is already durable. */
    void flipBit(long offset) {
        visible[(int) offset] ^= 0x01;
        durable = visible.clone();
    }
}

/** One record as replay hands it back: its sequence number, where it starts, and the caller's bytes. */
record Record(long seq, long offset, byte[] payload) {}

/** A fully written record was damaged, or the sequence is not contiguous. Recovery must stop, not guess. */
final class CorruptRecordException extends RuntimeException {
    final long offset;

    CorruptRecordException(long offset, String reason) {
        super("corrupt record at offset " + offset + ": " + reason);
        this.offset = offset;
    }
}

/** What replay saw: records delivered, where the valid prefix ends, torn bytes cut off, last sequence number. */
record ReplayResult(int recordsReplayed, long validLength, long truncatedBytes, long lastSeq) {
    boolean torn() { return truncatedBytes > 0; }
}

@FunctionalInterface
interface RecordConsumer {
    void accept(Record record);
}

/**
 * An append-only log of framed records on top of a Storage.
 *
 * Frame layout, all integers big-endian:
 *   u32 payloadLength | u32 crc32(seq || payload) | u64 seq | payload
 *
 * Rules: append never rewrites earlier bytes; sync() is the only durability point; sequence numbers are
 * contiguous within the log; replay accepts the longest valid prefix, truncates an incomplete or
 * checksum-failed FINAL frame (a torn write), and refuses to continue past a damaged frame that has data after it.
 */
final class WriteAheadLog {
    static final int HEADER_BYTES = 16;
    static final int MAX_PAYLOAD_BYTES = 1 << 20; // 1 MiB: anything larger is a corrupt length, not a record

    private final Storage storage;
    private long end;
    private long nextSeq;

    WriteAheadLog(Storage storage) {
        this.storage = storage;
        this.end = storage.size();
        this.nextSeq = 1;
    }

    long size() { return end; }
    long nextSeq() { return nextSeq; }

    static long crc32(long seq, byte[] payload) {
        CRC32 crc = new CRC32();
        crc.update(ByteBuffer.allocate(8).putLong(seq).array());
        crc.update(payload);
        return crc.getValue();
    }

    /** Frame a payload. Exposed so tests can inspect the exact bytes. */
    static byte[] encode(long seq, byte[] payload) {
        if (payload.length > MAX_PAYLOAD_BYTES) throw new IllegalArgumentException("payload too large: " + payload.length);
        ByteBuffer frame = ByteBuffer.allocate(HEADER_BYTES + payload.length);
        frame.putInt(payload.length);
        frame.putInt((int) crc32(seq, payload));
        frame.putLong(seq);
        frame.put(payload);
        return frame.array();
    }

    /** Append one frame at the end and return its sequence number. Not durable until sync(). */
    long append(byte[] payload) {
        long seq = nextSeq;
        byte[] frame = encode(seq, payload);
        storage.write(end, frame);
        end += frame.length;
        nextSeq++;
        return seq;
    }

    /** The durability point. Everything appended before this call survives a crash after it returns. */
    void sync() { storage.sync(); }

    /**
     * Read every frame from offset 0 and hand each to the consumer in order. Stops at the first frame that is
     * incomplete or checksum-failed AND is the last thing in the storage: that is a torn write, and the storage
     * is truncated back to the end of the previous good frame. A checksum failure, implausible length, or
     * sequence gap with more bytes after it is corruption of a frame that had been fully written; replay throws.
     */
    ReplayResult replay(RecordConsumer consumer) {
        long fileSize = storage.size();
        int replayed = 0;
        long offset = 0;
        long lastSeq = 0;
        boolean first = true;
        while (offset < fileSize) {
            long remaining = fileSize - offset;
            if (remaining < HEADER_BYTES) break;                                // torn: header cut off
            ByteBuffer header = ByteBuffer.wrap(storage.read(offset, HEADER_BYTES));
            int length = header.getInt();
            int storedCrc = header.getInt();
            long seq = header.getLong();
            if (length < 0 || length > MAX_PAYLOAD_BYTES) {
                throw new CorruptRecordException(offset, "implausible length " + length);
            }
            if (remaining - HEADER_BYTES < length) break;                        // torn: payload cut off
            byte[] payload = storage.read(offset + HEADER_BYTES, length);
            int actualCrc = (int) crc32(seq, payload);
            boolean isLast = offset + HEADER_BYTES + length == fileSize;
            if (actualCrc != storedCrc) {
                if (isLast) break;                                               // torn: tail record half-written
                throw new CorruptRecordException(offset, String.format(
                        "crc mismatch (stored %08x, computed %08x) with %d bytes after it",
                        storedCrc, actualCrc, fileSize - (offset + HEADER_BYTES + length)));
            }
            if (!first && seq != lastSeq + 1) {
                throw new CorruptRecordException(offset, "sequence gap: expected " + (lastSeq + 1) + ", found " + seq);
            }
            consumer.accept(new Record(seq, offset, payload));
            replayed++;
            lastSeq = seq;
            first = false;
            offset += HEADER_BYTES + length;
        }
        long truncated = fileSize - offset;
        if (truncated > 0) {
            storage.truncate(offset);
            storage.sync();
        }
        end = offset;
        if (replayed > 0) nextSeq = lastSeq + 1;
        return new ReplayResult(replayed, offset, truncated, lastSeq);
    }

    /** Throw away every frame after a checkpoint made them redundant; numbering continues from nextSeq. */
    void reset(long nextSeq) {
        if (end > 0) {
            storage.truncate(0);
            storage.sync();
            end = 0;
        }
        this.nextSeq = nextSeq;
    }
}

/**
 * A ledger of account balances whose only durable state is two logs: the WAL and a snapshot log.
 * Every mutation is framed, appended, and synced BEFORE the in-memory map changes. checkpoint() appends the
 * whole map as one snapshot record (tagged with the WAL sequence it covers) and then empties the WAL.
 */
final class Ledger {
    static final byte OP_CREDIT = 1;
    static final byte OP_DEBIT = 2;
    static final byte OP_TRANSFER = 3;

    private final WriteAheadLog wal;
    private final WriteAheadLog snapshots;
    private final TreeMap<String, Money> balances = new TreeMap<>();
    private final Currency currency;
    private long appliedSeq = 0;
    private ReplayResult lastRecovery;

    private Ledger(Storage walStorage, Storage snapshotStorage, Currency currency) {
        this.wal = new WriteAheadLog(walStorage);
        this.snapshots = new WriteAheadLog(snapshotStorage);
        this.currency = currency;
    }

    /** Recover: load the last complete snapshot, then replay WAL records newer than it. */
    static Ledger open(Storage walStorage, Storage snapshotStorage, Currency currency) {
        Ledger ledger = new Ledger(walStorage, snapshotStorage, currency);
        Record[] latest = new Record[1];
        ledger.snapshots.replay(r -> latest[0] = r);
        if (latest[0] != null) ledger.loadSnapshot(latest[0].payload());
        long covered = ledger.appliedSeq;
        ledger.lastRecovery = ledger.wal.replay(record -> {
            if (record.seq() <= covered) return;           // already inside the snapshot: skip, do not double-apply
            ledger.apply(record);
        });
        if (ledger.lastRecovery.lastSeq() <= covered) {
            ledger.wal.reset(covered + 1);                  // every WAL record (if any) is inside the snapshot: finish the interrupted checkpoint
        }
        return ledger;
    }

    ReplayResult lastRecovery() { return lastRecovery; }
    long walSize() { return wal.size(); }
    long snapshotCount() { return snapshots.nextSeq() - 1; }
    long appliedSeq() { return appliedSeq; }
    Map<String, Money> view() { return Collections.unmodifiableMap(balances); }

    Money balance(String account) { return balances.getOrDefault(account, Money.ofMinor(0, currency)); }

    void credit(String account, Money amount) {
        requirePositive(amount);
        commit(encodeOp(OP_CREDIT, account, "", amount));
    }

    void debit(String account, Money amount) {
        requirePositive(amount);
        if (balance(account).minus(amount).isNegative()) {
            throw new IllegalStateException("insufficient funds in " + account + ": " + balance(account) + " < " + amount);
        }
        commit(encodeOp(OP_DEBIT, account, "", amount));
    }

    /** One record, two balance changes: a crash can never leave the money in flight. */
    void transfer(String from, String to, Money amount) {
        requirePositive(amount);
        if (balance(from).minus(amount).isNegative()) {
            throw new IllegalStateException("insufficient funds in " + from + ": " + balance(from) + " < " + amount);
        }
        commit(encodeOp(OP_TRANSFER, from, to, amount));
    }

    private void requirePositive(Money amount) {
        if (!amount.currency().equals(currency)) throw new IllegalArgumentException("ledger is " + currency.code());
        if (amount.minorUnits() <= 0) throw new IllegalArgumentException("amount must be positive: " + amount);
    }

    /** Write-ahead: append, sync, and only then mutate memory. */
    private void commit(byte[] payload) {
        long seq = wal.append(payload);
        wal.sync();
        apply(new Record(seq, -1, payload));
    }

    /** Payload layout: u8 op | u16 fromLen | from | u16 toLen | to | 3 bytes currency | i64 minorUnits. */
    static byte[] encodeOp(byte op, String from, String to, Money amount) {
        byte[] f = from.getBytes(StandardCharsets.UTF_8);
        byte[] t = to.getBytes(StandardCharsets.UTF_8);
        ByteBuffer buf = ByteBuffer.allocate(1 + 2 + f.length + 2 + t.length + 3 + 8);
        buf.put(op).putShort((short) f.length).put(f).putShort((short) t.length).put(t);
        buf.put(amount.currency().code().getBytes(StandardCharsets.US_ASCII)).putLong(amount.minorUnits());
        return buf.array();
    }

    private void apply(Record record) {
        ByteBuffer buf = ByteBuffer.wrap(record.payload());
        if (buf.remaining() < 1 + 2 + 2 + 3 + 8) throw new CorruptRecordException(record.offset(), "payload shorter than op header");
        byte op = buf.get();
        String from = readString(buf, record.offset());
        String to = readString(buf, record.offset());
        byte[] ccy = new byte[3];
        buf.get(ccy);
        Money amount = Money.ofMinor(buf.getLong(), new Currency(new String(ccy, StandardCharsets.US_ASCII)));
        switch (op) {
            case OP_CREDIT -> balances.put(from, balance(from).plus(amount));
            case OP_DEBIT -> balances.put(from, balance(from).minus(amount));
            case OP_TRANSFER -> {
                balances.put(from, balance(from).minus(amount));
                balances.put(to, balance(to).plus(amount));
            }
            default -> throw new CorruptRecordException(record.offset(), "unknown op " + op);
        }
        appliedSeq = record.seq();
    }

    private static String readString(ByteBuffer buf, long offset) {
        int len = buf.getShort() & 0xffff;
        if (len > buf.remaining()) throw new CorruptRecordException(offset, "string length " + len + " exceeds payload");
        byte[] b = new byte[len];
        buf.get(b);
        return new String(b, StandardCharsets.UTF_8);
    }

    /**
     * Snapshot payload: u64 coveredSeq | u32 count | repeated (u16 len | account | 3 bytes currency | i64 minorUnits).
     * Append it to the snapshot log, sync, then empty the WAL. Crash before the sync: the snapshot frame is torn
     * and ignored, the WAL is intact. Crash after the sync but before the reset: the WAL still holds records with
     * seq <= coveredSeq, and open() skips them. Either way the recovered state is identical.
     */
    void checkpoint() {
        int size = 8 + 4;
        for (Map.Entry<String, Money> e : balances.entrySet()) size += 2 + e.getKey().getBytes(StandardCharsets.UTF_8).length + 3 + 8;
        ByteBuffer buf = ByteBuffer.allocate(size);
        buf.putLong(appliedSeq).putInt(balances.size());
        for (Map.Entry<String, Money> e : balances.entrySet()) {
            byte[] k = e.getKey().getBytes(StandardCharsets.UTF_8);
            buf.putShort((short) k.length).put(k);
            buf.put(e.getValue().currency().code().getBytes(StandardCharsets.US_ASCII)).putLong(e.getValue().minorUnits());
        }
        snapshots.append(buf.array());
        snapshots.sync();
        wal.reset(appliedSeq + 1);
    }

    private void loadSnapshot(byte[] payload) {
        ByteBuffer buf = ByteBuffer.wrap(payload);
        appliedSeq = buf.getLong();
        int count = buf.getInt();
        for (int i = 0; i < count; i++) {
            String account = readString(buf, -1);
            byte[] ccy = new byte[3];
            buf.get(ccy);
            balances.put(account, Money.ofMinor(buf.getLong(), new Currency(new String(ccy, StandardCharsets.US_ASCII))));
        }
    }
}

public final class Main {
    static String hex(byte[] bytes) {
        StringBuilder sb = new StringBuilder();
        for (byte b : bytes) sb.append(String.format("%02x", b));
        return sb.toString();
    }

    static Money usd(long cents) { return Money.ofMinor(cents, Currency.USD); }

    public static void main(String[] args) {
        System.out.println("== One frame on disk ==");
        byte[] payload = "hello".getBytes(StandardCharsets.UTF_8);
        System.out.printf("crc32(seq=1, \"hello\") = %08x%n", WriteAheadLog.crc32(1, payload));
        System.out.println("frame = " + hex(WriteAheadLog.encode(1, payload)) + "  (len | crc | seq | payload)");

        System.out.println("\n== Balances survive kill -9 ==");
        MemoryStorage walDisk = new MemoryStorage();
        MemoryStorage snapDisk = new MemoryStorage();
        Ledger ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        ledger.credit("merchant:acme", usd(10_000));
        ledger.credit("customer:ann", usd(5_000));
        ledger.transfer("customer:ann", "merchant:acme", usd(1_999));
        System.out.println("before crash: " + ledger.view() + ", wal bytes = " + ledger.walSize() + ", syncs = " + walDisk.syncCount());
        walDisk.crash();                                     // the process dies; only synced bytes remain
        ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        System.out.println("after restart: " + ledger.view() + ", replayed " + ledger.lastRecovery().recordsReplayed() + " records");

        System.out.println("\n== A torn final record is dropped, not guessed ==");
        long cleanSize = ledger.walSize();
        WriteAheadLog raw = new WriteAheadLog(walDisk);
        raw.replay(r -> {});
        raw.append(Ledger.encodeOp(Ledger.OP_CREDIT, "customer:bob", "", usd(700)));
        walDisk.crashKeepingUnsynced(9);                     // power fails 9 bytes into the write
        ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        ReplayResult r = ledger.lastRecovery();
        System.out.println("replayed " + r.recordsReplayed() + ", truncated " + r.truncatedBytes() + " torn bytes, wal back to "
                + r.validLength() + " (clean size was " + cleanSize + ")");
        System.out.println("customer:bob = " + ledger.balance("customer:bob"));

        System.out.println("\n== A corrupt middle record stops replay ==");
        walDisk.flipBit(WriteAheadLog.HEADER_BYTES + 5);     // flip one bit inside the first payload
        try {
            Ledger.open(walDisk, snapDisk, Currency.USD);
            System.out.println("unexpected: replay succeeded");
        } catch (CorruptRecordException e) {
            System.out.println("refused: " + e.getMessage());
        }
        walDisk.flipBit(WriteAheadLog.HEADER_BYTES + 5);     // repair it so the demo can continue

        System.out.println("\n== Checkpoint compacts the log; sequence numbers make it safe ==");
        ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        for (int i = 0; i < 50; i++) ledger.credit("merchant:acme", usd(1));
        System.out.println("50 credits: wal bytes = " + ledger.walSize() + ", applied seq = " + ledger.appliedSeq());
        ledger.checkpoint();
        System.out.println("after checkpoint: wal bytes = " + ledger.walSize() + ", snapshots = " + ledger.snapshotCount()
                + ", snapshot bytes = " + snapDisk.size());
        ledger.credit("customer:ann", usd(100));
        ledger = Ledger.open(walDisk, snapDisk, Currency.USD);
        System.out.println("after restart: " + ledger.view() + ", replayed " + ledger.lastRecovery().recordsReplayed()
                + " wal record(s) on top of the snapshot, next seq = " + (ledger.appliedSeq() + 1));
    }
}
