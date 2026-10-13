// Tests for phases/14-payments-at-scale/05-batch-vs-real-time-settlement. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.Instant;
import java.time.LocalDate;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static final Currency USD = Currency.USD;

    static Money usd(String s) { return Money.parse(s, USD); }

    static Platform demoPlatform() {
        Platform p = new Platform(USD, FeeSchedule.standard(USD));
        Main.demoMerchants().forEach(p::addMerchant);
        Main.demoDay().forEach(p::book);
        return p;
    }

    public static void main(String[] args) {
        FeeSchedule fees = FeeSchedule.standard(USD);
        Instant t = Instant.parse("2026-03-31T10:00:00Z");

        // fees: 2.9% + 0.30, HALF_EVEN on the percentage part
        check("fee on 45.50 is 1.62", fees.feeFor(new SettlementEvent("x", "m", EventType.CAPTURE, usd("45.50"), t)).equals(usd("1.62")));
        check("fee on 1000.00 is 29.30", fees.feeFor(new SettlementEvent("x", "m", EventType.CAPTURE, usd("1000.00"), t)).equals(usd("29.30")));
        check("refund returns no fee", fees.merchantEffect(new SettlementEvent("x", "m", EventType.REFUND, usd("20.00"), t)).equals(usd("-20.00")));
        check("chargeback costs amount plus 15.00", fees.merchantEffect(new SettlementEvent("x", "m", EventType.CHARGEBACK, usd("60.00"), t)).equals(usd("-75.00")));
        checkThrows("event amounts must be positive", IllegalArgumentException.class,
                () -> new SettlementEvent("x", "m", EventType.REFUND, usd("-1.00"), t));

        // the worked example: netting per merchant
        Platform p = demoPlatform();
        BatchSettler s = new BatchSettler(p, Money.zero(USD));
        SettlementBatch b = s.run(Main.DAY, Main.CUTOFF);
        check("two payout instructions", b.instructions().size() == 2);
        check("acme nets 173.60", b.instructions().get(0).merchantId().equals("m_acme") && b.instructions().get(0).amount().equals(usd("173.60")));
        check("cora nets 1213.15", b.instructions().get(1).amount().equals(usd("1213.15")));
        check("batch total 1386.75", b.total().equals(usd("1386.75")));
        check("bolt negative net is carried, not paid", b.carriedForward().get("m_bolt").equals(usd("-31.12")));
        check("acquirer funding 1415.50", b.processorFunding().equals(usd("1415.50")));
        check("event after cutoff is not settled", !s.isSettled("c5") && s.isSettled("c4"));
        check("seven events in the batch", b.eventIds().size() == 7);
        check("ties out after batch", s.tiesOut());
        check("trial balance zero", p.ledger.trialBalance() == 0);

        // idempotent rerun
        int journals = p.ledger.journalCount();
        SettlementBatch again = s.run(Main.DAY, Main.CUTOFF);
        check("rerun returns the same batch", again == b);
        check("rerun posts nothing", p.ledger.journalCount() == journals);
        check("rebooking an event is a no-op", !p.book(Main.demoDay().get(0)) && p.ledger.journalCount() == journals);

        // settlement file
        String file = SettlementFile.render(b);
        check("file trailer count, total, hash", file.contains("T|2|138675|0003200015"));
        check("file validates", SettlementFile.validate(file));
        check("tampered file fails validation", !SettlementFile.validate(file.replace("|17360", "|17361")));

        // funding then payout: cash never negative in batch mode
        p.receiveFunding(b.batchId(), b.processorFunding());
        s.confirmPayouts(b);
        check("cash after batch is fees minus bolt deficit", p.ledger.balance(Ledger.CASH).equals(usd("28.75")));
        check("batch needs no prefunding", p.ledger.peakPrefunding().equals(Money.zero(USD)));
        check("in transit cleared", p.ledger.balance(Ledger.PAYOUTS_IN_TRANSIT).minorUnits() == 0);

        // next day: c5 rolls in, bolt's deficit nets against new captures
        p.book(new SettlementEvent("c7", "m_bolt", EventType.CAPTURE, usd("100.00"), Instant.parse("2026-04-01T09:00:00Z")));
        SettlementBatch day2 = s.run(LocalDate.parse("2026-04-01"), Instant.parse("2026-04-01T23:00:00Z"));
        check("day 2 has two payouts", day2.instructions().size() == 2);
        check("bolt day 2 = 96.80 - 31.12", day2.instructions().get(0).amount().equals(usd("65.68")));
        check("cora day 2 = c5 net 9.41", day2.instructions().get(1).amount().equals(usd("9.41")));
        check("ties out after day 2", s.tiesOut() && s.carried("m_bolt").minorUnits() == 0);

        // holds and minimum payout
        Platform h = new Platform(USD, fees);
        h.addMerchant(new Merchant("m_hold", "021000021", "1", true));
        h.addMerchant(new Merchant("m_tiny", "021000021", "2", false));
        h.book(new SettlementEvent("h1", "m_hold", EventType.CAPTURE, usd("500.00"), t));
        h.book(new SettlementEvent("h2", "m_tiny", EventType.CAPTURE, usd("1.00"), t));
        BatchSettler hs = new BatchSettler(h, usd("5.00"));
        SettlementBatch hb = hs.run(Main.DAY, Main.CUTOFF);
        check("merchant on hold is not paid and its events stay open", hb.instructions().isEmpty() && !hs.isSettled("h1"));
        check("below-minimum net is carried positive", hs.carried("m_tiny").equals(usd("0.67")));
        check("holds and carries still tie out", hs.tiesOut());

        // ledger safety
        checkThrows("unbalanced journal rejected", UnbalancedJournalException.class,
                () -> h.ledger.post("bad", List.of(new Posting("a", 100), new Posting("b", -99))));
        checkThrows("foreign-currency event rejected", CurrencyMismatchException.class,
                () -> h.book(new SettlementEvent("e1", "m_tiny", EventType.CAPTURE, Money.parse("1.00", Currency.EUR), t)));
        checkThrows("unknown merchant rejected", IllegalArgumentException.class,
                () -> h.book(new SettlementEvent("u1", "m_nobody", EventType.CAPTURE, usd("1.00"), t)));

        // real time vs batch on the same events
        LiquidityComparison cmp = LiquidityComparison.run(Main.demoMerchants(), Main.demoDay(), Main.DAY, Main.CUTOFF, USD);
        check("real time sends one transfer per positive sweep", cmp.realTimeTransfers() == 5 && cmp.batchTransfers() == 2);
        check("real time prefunds everything it pays", cmp.realTimePeakPrefunding().equals(usd("1450.63")));
        check("real time exposure is refund plus chargeback", cmp.realTimeExposure().equals(usd("95.00")));
        check("real time cash after funding is fees minus exposure", cmp.realTimeCashAfter().equals(usd("-35.13")));
        check("batch cash after funding", cmp.batchCashAfter().equals(usd("28.75")) && cmp.batchPeakPrefunding().minorUnits() == 0);

        // a large seeded day still ties out to the cent
        List<Merchant> many = Main.generatedMerchants(50);
        Platform big = new Platform(USD, fees);
        many.forEach(big::addMerchant);
        Main.generatedDay(many, 5_000, 11).forEach(big::book);
        BatchSettler bs = new BatchSettler(big, usd("1.00"));
        SettlementBatch bb = bs.run(Main.DAY, Instant.parse("2026-04-01T00:00:00Z"));
        check("large batch ties out", bs.tiesOut());
        check("large batch file validates", SettlementFile.validate(SettlementFile.render(bb)));
        check("at most one payout per merchant", bb.instructions().size() <= many.size());

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
