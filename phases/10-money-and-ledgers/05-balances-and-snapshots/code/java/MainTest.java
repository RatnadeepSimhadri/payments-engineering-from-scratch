// Tests for phases/10-money-and-ledgers/05-balances-and-snapshots. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
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

    static final Currency USD = Currency.USD;
    static final long T0 = 1_767_225_600L;

    static Ledger threeAccounts(int interval) {
        Ledger l = new Ledger(interval);
        l.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        l.openAccount("2100", "Merchant Payable", AccountType.LIABILITY, USD);
        l.openAccount("4100", "Fee Revenue", AccountType.REVENUE, USD);
        return l;
    }

    static JournalEntry sale(String id, long t, long grossMinor, long feeMinor) {
        Money gross = Money.ofMinor(grossMinor, USD), fee = Money.ofMinor(feeMinor, USD);
        return new JournalEntry(id, t, List.of(Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee)));
    }

    public static void main(String[] args) {
        // running balance and normal direction
        Ledger l = threeAccounts(2);
        l.post(sale("a", T0, 10_000, 290));
        l.post(sale("b", T0 + 10, 2_500, 72));
        l.post(sale("c", T0 + 20, 4_000, 116));
        check("receivable is debit-normal positive", l.balance("1200").minorUnits() == 16_500);
        check("payable is credit-normal positive", l.balance("2100").minorUnits() == 16_500 - 478);
        check("revenue is credit-normal positive", l.balance("4100").minorUnits() == 478);
        check("net debits sum to zero", l.currentNetDebit("1200").plus(l.currentNetDebit("2100")).plus(l.currentNetDebit("4100")).isZero());

        // snapshot schedule
        check("snapshots at 0 and 2", l.snapshots().stream().map(Snapshot::seq).toList().equals(List.of(0L, 2L)));
        check("nearest snapshot to seq 1 is 0", l.nearestSnapshot(1).seq() == 0);
        check("nearest snapshot to seq 3 is 2", l.nearestSnapshot(3).seq() == 2);
        check("takeSnapshot at head is added", l.takeSnapshot().seq() == 3 && l.snapshots().size() == 3);
        check("takeSnapshot twice is a no-op", l.takeSnapshot().seq() == 3 && l.snapshots().size() == 3);
        check("snapshot replay at exact snapshot seq scans nothing", l.snapshotNetDebit("2100", 2).postingsScanned() == 0);

        // three read paths agree with the fold at every seq
        boolean agree = true;
        for (long seq = 0; seq <= l.head(); seq++) {
            for (String id : List.of("1200", "2100", "4100")) {
                Money fold = l.foldNetDebit(id, seq).balance();
                agree &= fold.equals(l.snapshotNetDebit(id, seq).balance()) && fold.equals(l.columnNetDebit(id, seq).balance());
            }
        }
        check("fold == snapshot+delta == column at every seq (small)", agree);
        check("before first entry balance is zero", l.columnNetDebit("2100", 0).balance().isZero());

        // time-based queries
        check("seqAsOf before start is 0", l.seqAsOf(T0 - 1) == 0);
        check("seqAsOf exact timestamp includes the entry", l.seqAsOf(T0 + 10) == 2);
        check("seqAsOf between entries", l.seqAsOf(T0 + 15) == 2);
        check("seqAsOf far future is head", l.seqAsOf(T0 + 1_000_000) == 3);
        check("balanceAsOf mid-stream", l.balanceAsOf("2100", T0 + 15).minorUnits() == 9_710 + 2_428);

        // equal timestamps are allowed; backdating is not
        l.post(sale("d", T0 + 20, 1_999, 58));
        check("same-timestamp entries both counted", l.seqAsOf(T0 + 20) == 4);
        checkThrows("backdated entry rejected", LedgerException.class, () -> l.post(sale("late", T0, 100, 3)));
        check("rejected entry left no trace", l.head() == 4 && l.balance("4100").minorUnits() == 478 + 58);
        checkThrows("currency mismatch rejected", LedgerException.class, () -> l.post(new JournalEntry("eur", T0 + 30, List.of(
                Posting.debit("1200", Money.ofMinor(100, Currency.EUR)), Posting.credit("2100", Money.ofMinor(100, Currency.EUR))))));
        checkThrows("unbalanced entry cannot be built", LedgerException.class, () -> new JournalEntry("u", T0, List.of(
                Posting.debit("1200", Money.ofMinor(100, USD)), Posting.credit("2100", Money.ofMinor(99, USD)))));
        checkThrows("seq beyond head rejected", LedgerException.class, () -> l.snapshotNetDebit("2100", 99));
        checkThrows("negative seq rejected", LedgerException.class, () -> l.columnNetDebit("2100", -1));

        // verification catches a bad snapshot
        for (Snapshot s : l.snapshots()) check("snapshot " + s.seq() + " verifies", l.verify(s));
        Snapshot forged = new Snapshot(2, T0 + 10, Map.of("1200", 12_500L, "2100", -12_138L, "4100", -361L));
        check("forged snapshot fails verification", !l.verify(forged));
        Snapshot unknownAccount = new Snapshot(0, 0, Map.of("9999", 0L));
        check("snapshot with unknown account fails verification", !l.verify(unknownAccount));

        // randomized property: snapshot + delta == full fold, many intervals, many seqs
        PaymentStream stream = new PaymentStream(11, 25, T0);
        Ledger big = new Ledger(97);
        big.openAccount("1200", "Processor Receivable", AccountType.ASSET, USD);
        for (int i = 0; i < 25; i++) big.openAccount(PaymentStream.merchantId(i), "m", AccountType.LIABILITY, USD);
        for (int i = 0; i < 5_000; i++) big.post(stream.next());
        Random r = new Random(3);
        boolean prop = true;
        long maxScan = 0;
        for (int k = 0; k < 300; k++) {
            long seq = r.nextInt((int) big.head() + 1);
            String id = r.nextInt(5) == 0 ? "1200" : PaymentStream.merchantId(r.nextInt(25));
            Reading fold = big.foldNetDebit(id, seq), snap = big.snapshotNetDebit(id, seq), col = big.columnNetDebit(id, seq);
            prop &= fold.balance().equals(snap.balance()) && fold.balance().equals(col.balance());
            maxScan = Math.max(maxScan, snap.postingsScanned());
        }
        check("property: three paths agree on 300 random (account, seq) pairs", prop);
        check("snapshot replay bounded by interval * legs", maxScan <= 96 * 2);
        check("every automatic snapshot verifies", big.snapshots().stream().allMatch(big::verify));
        check("snapshot count = head / interval + 1", big.snapshots().size() == 5_000 / 97 + 1);
        long merchantsTotal = 0;
        for (int i = 0; i < 25; i++) merchantsTotal += big.balance(PaymentStream.merchantId(i)).minorUnits();
        check("conservation: receivable equals sum of payables", big.balance("1200").minorUnits() == merchantsTotal);

        // determinism: same seed, same ledger
        PaymentStream s1 = new PaymentStream(5, 10, T0), s2 = new PaymentStream(5, 10, T0);
        boolean same = true;
        for (int i = 0; i < 100; i++) same &= s1.next().equals(s2.next());
        check("seeded stream is deterministic", same);
        checkThrows("interval must be positive", IllegalArgumentException.class, () -> new Ledger(0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
