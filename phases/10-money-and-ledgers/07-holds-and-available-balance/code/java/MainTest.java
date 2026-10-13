// Tests for phases/10-money-and-ledgers/07-holds-and-available-balance. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Money usd(String text) { return Money.parse(text, Currency.USD); }

    static final long HOUR = 3600, DAY = 24 * HOUR;

    /** An account with 500.00 USD settled and nothing held. */
    static Account funded(ManualClock clock) {
        Account a = new Account("acct", Currency.USD, clock);
        a.credit(usd("500.00"));
        return a;
    }

    public static void main(String[] args) {
        ManualClock clock = new ManualClock(1_000_000L);

        // happy path: ledger vs available
        Account a = funded(clock);
        check("credit raises ledger", a.ledgerBalance().equals(usd("500.00")));
        check("no holds: available == ledger", a.availableBalance().equals(a.ledgerBalance()));
        Hold hotel = a.authorize("hotel", usd("300.00"), 7 * DAY);
        check("authorize leaves ledger untouched", a.ledgerBalance().equals(usd("500.00")));
        check("authorize reduces available", a.availableBalance().equals(usd("200.00")));
        check("hold is active", hotel.isActive() && hotel.status() == HoldStatus.ACTIVE);
        check("invariant after authorize", a.checkInvariant());

        // hold stacking: several holds reduce available together
        a.authorize("fuel", usd("125.00"), 2 * HOUR);
        check("two holds stack", a.heldTotal().equals(usd("425.00")) && a.availableBalance().equals(usd("75.00")));
        check("activeHolds lists both", a.activeHolds().size() == 2);
        checkThrows("third hold beyond available is rejected", InsufficientAvailableException.class,
                () -> a.authorize("too-much", usd("75.01"), DAY));
        check("exactly the available amount is allowed", a.authorize("exact", usd("75.00"), DAY) != null);
        check("available is now zero, never negative", a.availableBalance().isZero() && a.checkInvariant());
        a.voidHold("exact");
        check("void returns the whole amount", a.availableBalance().equals(usd("75.00")));
        check("voided hold is not active", a.hold("exact").status() == HoldStatus.VOIDED);
        checkThrows("void twice is rejected", HoldNotActiveException.class, () -> a.voidHold("exact"));

        // available, not ledger, gates a settled debit
        checkThrows("debit beyond available rejected even though ledger covers it", InsufficientAvailableException.class,
                () -> a.debit(usd("80.00")));
        a.debit(usd("5.00"));
        check("debit within available moves ledger", a.ledgerBalance().equals(usd("495.00")) && a.availableBalance().equals(usd("70.00")));

        // partial capture releases the remainder
        Money released = a.capture("fuel", usd("42.17"));
        check("partial capture returns the remainder", released.equals(usd("82.83")));
        check("partial capture debits ledger by the captured amount", a.ledgerBalance().equals(usd("452.83")));
        check("partial capture releases the rest to available", a.availableBalance().equals(usd("152.83")));
        check("captured hold records the captured amount", a.hold("fuel").status() == HoldStatus.CAPTURED && a.hold("fuel").captured().equals(usd("42.17")));
        check("invariant after partial capture", a.checkInvariant());
        checkThrows("capture twice is rejected", HoldNotActiveException.class, () -> a.capture("fuel", usd("1.00")));

        // over-capture rejected
        checkThrows("over-capture rejected", OverCaptureException.class, () -> a.capture("hotel", usd("300.01")));
        check("rejected over-capture changed nothing", a.ledgerBalance().equals(usd("452.83")) && a.hold("hotel").isActive());

        // expiry via the injected clock
        clock.advance(7 * DAY - 1);
        check("one second before expiry the hold is still active", a.activeHolds().size() == 1 && a.availableBalance().equals(usd("152.83")));
        clock.advance(1);
        check("at expiry the hold is released", a.activeHolds().isEmpty() && a.availableBalance().equals(usd("452.83")));
        check("expired hold has status EXPIRED", a.hold("hotel").status() == HoldStatus.EXPIRED);
        checkThrows("capturing an expired hold is rejected", HoldNotActiveException.class, () -> a.capture("hotel", usd("300.00")));
        check("invariant after expiry", a.checkInvariant());

        // incremental authorization
        Account b = funded(new ManualClock(0));
        b.authorize("h", usd("310.00"), DAY);
        b.increment("h", usd("45.00"));
        check("increment grows the hold", b.hold("h").amount().equals(usd("355.00")) && b.availableBalance().equals(usd("145.00")));
        checkThrows("increment beyond available rejected", InsufficientAvailableException.class, () -> b.increment("h", usd("145.01")));
        check("full capture of an incremented hold", b.capture("h", usd("355.00")).isZero() && b.ledgerBalance().equals(usd("145.00")));
        check("invariant after full capture", b.checkInvariant());

        // failures the lesson warns about
        checkThrows("duplicate hold id rejected", DuplicateHoldException.class, () -> b.authorize("h", usd("1.00"), DAY));
        checkThrows("unknown hold", UnknownHoldException.class, () -> b.capture("nope", usd("1.00")));
        checkThrows("currency mismatch rejected", CurrencyMismatchException.class, () -> b.authorize("eur", Money.parse("1.00", Currency.EUR), DAY));
        checkThrows("zero-amount hold rejected", IllegalArgumentException.class, () -> b.authorize("zero", usd("0.00"), DAY));
        checkThrows("clock does not run backwards", IllegalArgumentException.class, () -> new ManualClock(5).advance(-1));

        // randomized invariant: seeded sequence of operations never breaks available >= 0
        java.util.Random rnd = new java.util.Random(7);
        ManualClock c = new ManualClock(0);
        Account r = new Account("r", Currency.USD, c);
        r.credit(usd("1000.00"));
        boolean held = true;
        int n = 0;
        for (int i = 0; i < 2000; i++) {
            Money amt = Money.ofMinor(1 + rnd.nextInt(30_000), Currency.USD);
            try {
                switch (rnd.nextInt(5)) {
                    case 0 -> r.authorize("x" + (n++), amt, 1 + rnd.nextInt(3600));
                    case 1 -> { if (n > 0) r.capture("x" + rnd.nextInt(n), amt); }
                    case 2 -> { if (n > 0) r.voidHold("x" + rnd.nextInt(n)); }
                    case 3 -> c.advance(rnd.nextInt(1800));
                    default -> r.debit(amt);
                }
            } catch (HoldException expected) {
                // rejected operations are part of the model; they must leave the invariant intact
            }
            if (!r.checkInvariant()) { held = false; break; }
        }
        check("2000 seeded random operations keep available == ledger - held and >= 0", held);

        // concurrency: N threads race for the same available balance; exactly the affordable number win
        Account shared = new Account("shared", Currency.USD, new ManualClock(0));
        shared.credit(usd("500.00"));
        int[] outcome = Main.raceAuthorizations(shared, 50, usd("20.00"));
        check("exactly 25 of 50 concurrent 20.00 holds are accepted against 500.00", outcome[0] == 25);
        check("the other 25 are rejected, none lost", outcome[1] == 25);
        check("available is exactly zero after the race", shared.availableBalance().isZero());
        check("invariant holds after the race", shared.checkInvariant() && shared.activeHolds().size() == 25);
        Account solo = new Account("solo", Currency.USD, new ManualClock(0));
        solo.credit(usd("100.00"));
        int[] single = Main.raceAuthorizations(solo, 8, usd("100.00"));
        check("eight threads racing for the whole balance: one wins", single[0] == 1 && single[1] == 7);

        System.out.println("passed " + passed + ", failed " + failures.size());
        for (String f : failures) System.out.println("  FAIL " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
