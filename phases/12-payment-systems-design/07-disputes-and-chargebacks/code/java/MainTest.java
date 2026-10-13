// Tests for phases/12-payment-systems-design/07-disputes-and-chargebacks. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;
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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }
    static int seq = 0;
    static String eid() { return "e" + (++seq); }

    static final ReasonCode NOT_RECEIVED = ReasonCodes.get("VISA", "13.1");
    static final EvidenceBundle DELIVERY = EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "r"),
            new EvidenceItem(EvidenceType.PROOF_OF_DELIVERY, "pod"));

    static DisputeEvent ev(DisputeEventKind k) {
        return switch (k) {
            case SUBMIT_REPRESENTMENT, RESPOND_PRE_ARB, RESPOND_INQUIRY -> DisputeEvent.withEvidence(eid(), k, DELIVERY, "t");
            default -> DisputeEvent.of(eid(), k, "t");
        };
    }

    /** Drive a fresh 100.00 USD 13.1 dispute into the requested state along the canonical path. */
    static String disputeIn(DisputeService svc, DisputeState target) {
        String id = "d" + (++seq);
        boolean viaInquiry = target == DisputeState.INQUIRY || target == DisputeState.INQUIRY_RESPONDED || target == DisputeState.CLOSED_NO_CHARGEBACK;
        if (viaInquiry) {
            svc.openInquiry(id, "p", usd(10_000), NOT_RECEIVED);
            if (target != DisputeState.INQUIRY) svc.apply(id, ev(DisputeEventKind.RESPOND_INQUIRY));
            if (target == DisputeState.CLOSED_NO_CHARGEBACK) svc.apply(id, ev(DisputeEventKind.INQUIRY_CLOSED));
            return id;
        }
        svc.openChargeback(id, "p", usd(10_000), NOT_RECEIVED);
        List<DisputeEventKind> path = switch (target) {
            case CHARGEBACK -> List.of();
            case REPRESENTED -> List.of(DisputeEventKind.SUBMIT_REPRESENTMENT);
            case PRE_ARBITRATION -> List.of(DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED);
            case PRE_ARB_RESPONDED -> List.of(DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED, DisputeEventKind.RESPOND_PRE_ARB);
            case ARBITRATION -> List.of(DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED, DisputeEventKind.RESPOND_PRE_ARB, DisputeEventKind.ARBITRATION_FILED);
            case WON -> List.of(DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.ISSUER_ACCEPTED);
            case LOST -> List.of(DisputeEventKind.ACCEPT);
            default -> throw new IllegalArgumentException(target.toString());
        };
        for (DisputeEventKind k : path) svc.apply(id, ev(k));
        return id;
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(LocalDate.of(2026, 2, 2));

        // --- table shape ---------------------------------------------------------------------------
        check("18 rules", DisputeService.RULES.size() == 18);
        check("terminal states have no outgoing rules", DisputeService.RULES.stream().noneMatch(r -> r.from().isTerminal()));
        check("ARBITRATION and INQUIRY have no deadline rule", DisputeService.RULES.stream().noneMatch(r ->
                r.kind() == DisputeEventKind.DEADLINE_PASSED && (r.from() == DisputeState.ARBITRATION || r.from() == DisputeState.INQUIRY)));

        // --- every rule fires as written -----------------------------------------------------------------
        for (DisputeRule r : DisputeService.RULES) {
            FakeClock rc = new FakeClock(LocalDate.of(2026, 2, 2));
            DisputeService svc = Main.service(rc);
            String id = disputeIn(svc, r.from());
            if (r.kind() == DisputeEventKind.DEADLINE_PASSED) {
                rc.advanceDays(31);
                svc.sweepDeadlines();
            } else {
                svc.apply(id, ev(r.kind()));
            }
            check("rule " + r.from() + " + " + r.kind() + " -> " + r.to(), svc.dispute(id).state() == r.to());
            check("ledger balances after " + r.from() + " + " + r.kind(), svc.ledger().balances());
        }

        // --- every pair not in the table is refused and changes nothing ---------------------------------------
        int illegal = 0;
        for (DisputeState s : DisputeState.values()) {
            for (DisputeEventKind k : DisputeEventKind.values()) {
                if (DisputeService.RULES.stream().anyMatch(r -> r.from() == s && r.kind() == k)) continue;
                DisputeService svc = Main.service(new FakeClock(LocalDate.of(2026, 2, 2)));
                String id = disputeIn(svc, s);
                int entries = svc.ledger().journal().size();
                int versions = svc.dispute(id).history().size();
                try {
                    svc.apply(id, ev(k));
                    failures.add("illegal " + s + " + " + k + " was applied");
                } catch (IllegalDisputeTransitionException e) {
                    passed++;
                }
                check("untouched " + s + " + " + k, svc.dispute(id).state() == s && svc.ledger().journal().size() == entries
                        && svc.dispute(id).history().size() == versions);
                illegal++;
            }
        }
        check("112 illegal pairs (10 states x 13 events - 18 rules)", illegal == 112);

        // --- evidence --------------------------------------------------------------------------------------
        EvidenceBundle receiptOnly = EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "r"));
        check("13.1 with receipt only misses the delivery group", receiptOnly.missingFor(NOT_RECEIVED).size() == 1
                && receiptOnly.missingFor(NOT_RECEIVED).get(0).equals(Set.of(EvidenceType.PROOF_OF_DELIVERY, EvidenceType.SIGNED_DELIVERY)));
        check("signed delivery satisfies the 'one of' group", EvidenceBundle.of(new EvidenceItem(EvidenceType.RECEIPT, "r"),
                new EvidenceItem(EvidenceType.SIGNED_DELIVERY, "s")).missingFor(NOT_RECEIVED).isEmpty());
        check("empty bundle misses both groups", EvidenceBundle.of().missingFor(NOT_RECEIVED).size() == 2);
        ReasonCode fraud = ReasonCodes.get("VISA", "10.4");
        EvidenceBundle tds = EvidenceBundle.of(new EvidenceItem(EvidenceType.THREE_DS_AUTHENTICATION, "eci05"));
        check("3DS shifts liability on a fraud reason", tds.liabilityShift(fraud));
        check("3DS does not shift liability on a consumer dispute", !tds.liabilityShift(NOT_RECEIVED));
        check("AVS/CVV match alone does not answer 10.4", EvidenceBundle.of(new EvidenceItem(EvidenceType.AVS_CVV_MATCH, "Y/M"),
                new EvidenceItem(EvidenceType.RECEIPT, "r")).missingFor(fraud).size() == 1);
        checkThrows("unknown reason code refused", IllegalArgumentException.class, () -> ReasonCodes.get("VISA", "99.9"));

        DisputeService svc = Main.service(clock);
        svc.openChargeback("dp", "pay", usd(24_900), NOT_RECEIVED);
        checkThrows("representment with incomplete evidence refused", IllegalDisputeTransitionException.class,
                () -> svc.apply("dp", DisputeEvent.withEvidence("x1", DisputeEventKind.SUBMIT_REPRESENTMENT, receiptOnly, "thin")));
        checkThrows("representment without a bundle refused", IllegalDisputeTransitionException.class,
                () -> svc.apply("dp", DisputeEvent.of("x2", DisputeEventKind.SUBMIT_REPRESENTMENT, "none")));
        check("refusals left CHARGEBACK", svc.dispute("dp").state() == DisputeState.CHARGEBACK);

        // --- money on the full 120-day path ---------------------------------------------------------------------
        check("chargeback debits amount + 15.00 fee", svc.merchantImpact("dp").equals(usd(-26_400))
                && svc.ledger().balance(DisputeService.FEES).equals(usd(1_500)));
        check("deadline is 20 days after the chargeback", svc.dispute("dp").deadline().equals(LocalDate.of(2026, 2, 22)));
        clock.advanceDays(12);
        svc.apply("dp", DisputeEvent.withEvidence("x3", DisputeEventKind.SUBMIT_REPRESENTMENT, DELIVERY, "ok"));
        check("representment re-credits into the hold, not to the merchant", svc.ledger().balance(DisputeService.HOLD).equals(usd(24_900))
                && svc.merchantImpact("dp").equals(usd(-26_400)));
        clock.advanceDays(29);
        svc.apply("dp", DisputeEvent.of("x4", DisputeEventKind.PRE_ARB_RECEIVED, "pre-arb"));
        check("pre-arbitration moves no money", svc.ledger().balance(DisputeService.HOLD).equals(usd(24_900)));
        clock.advanceDays(15);
        svc.apply("dp", DisputeEvent.withEvidence("x5", DisputeEventKind.RESPOND_PRE_ARB, DELIVERY, "respond"));
        clock.advanceDays(7);
        svc.apply("dp", DisputeEvent.of("x6", DisputeEventKind.ARBITRATION_FILED, "filed"));
        check("arbitration has no deadline", svc.dispute("dp").deadline() == null);
        clock.advanceDays(55);
        svc.sweepDeadlines();
        check("sweep does not decide arbitration", svc.dispute("dp").state() == DisputeState.ARBITRATION);
        svc.apply("dp", DisputeEvent.of("x7", DisputeEventKind.RULING_FOR_MERCHANT, "won"));
        check("win releases the hold; merchant is out only the fee", svc.dispute("dp").state() == DisputeState.WON
                && svc.ledger().balance(DisputeService.HOLD).isZero() && svc.merchantImpact("dp").equals(usd(-1_500)));
        check("the whole course took 118 days", DisputeService.daysBetween(LocalDate.of(2026, 2, 2), clock.today()) == 118);
        check("history has open + 5 transitions, versions contiguous", svc.dispute("dp").history().size() == 6
                && svc.dispute("dp").history().get(5).version() == 5);
        check("redelivered ruling is DUPLICATE", svc.apply("dp", DisputeEvent.of("x7", DisputeEventKind.RULING_FOR_MERCHANT, "again")) == ApplyOutcome.DUPLICATE);
        checkThrows("terminal WON refuses a new ruling", IllegalDisputeTransitionException.class,
                () -> svc.apply("dp", DisputeEvent.of("x8", DisputeEventKind.RULING_FOR_ISSUER, "late")));

        // --- losing at each stage costs different amounts --------------------------------------------------------
        DisputeService l = Main.service(new FakeClock(LocalDate.of(2026, 2, 2)));
        String accepted = disputeIn(l, DisputeState.LOST);
        check("accepting the chargeback: amount + fee", l.merchantImpact(accepted).equals(usd(-11_500)));
        String preArb = disputeIn(l, DisputeState.PRE_ARBITRATION);
        l.apply(preArb, ev(DisputeEventKind.ACCEPT));
        check("accepting pre-arb returns the hold to the scheme: amount + fee", l.merchantImpact(preArb).equals(usd(-11_500))
                && l.dispute(preArb).state() == DisputeState.LOST);
        String arb = disputeIn(l, DisputeState.ARBITRATION);
        l.apply(arb, ev(DisputeEventKind.RULING_FOR_ISSUER));
        check("losing arbitration adds the 500.00 filing fee", l.merchantImpact(arb).equals(usd(-61_500)));
        check("hold is empty when nothing is in flight", l.ledger().balance(DisputeService.HOLD).isZero() && l.ledger().balances());

        // --- deadlines -----------------------------------------------------------------------------------------------
        FakeClock dc = new FakeClock(LocalDate.of(2026, 2, 2));
        DisputeService d = Main.service(dc);
        d.openChargeback("late", "p", usd(5_000), fraud);
        dc.advanceDays(20);
        check("on the deadline day nothing fires", d.sweepDeadlines().isEmpty() && d.dispute("late").state() == DisputeState.CHARGEBACK);
        dc.advanceDays(1);
        List<String> fired = d.sweepDeadlines();
        check("the day after, CHARGEBACK -> LOST", fired.size() == 1 && d.dispute("late").state() == DisputeState.LOST);
        checkThrows("a hand-made DEADLINE_PASSED before the deadline is refused", IllegalDisputeTransitionException.class, () -> {
            d.openChargeback("early", "p", usd(5_000), fraud);
            d.apply("early", DisputeEvent.of("y0", DisputeEventKind.DEADLINE_PASSED, "forged"));
        });
        check("second sweep is harmless", d.sweepDeadlines().isEmpty() && d.dispute("late").history().size() == 2);
        d.openChargeback("tooLate", "p", usd(5_000), NOT_RECEIVED);
        dc.advanceDays(21);
        checkThrows("representment after the deadline refused even with full evidence", IllegalDisputeTransitionException.class,
                () -> d.apply("tooLate", DisputeEvent.withEvidence("y1", DisputeEventKind.SUBMIT_REPRESENTMENT, DELIVERY, "late")));
        d.openInquiry("inq", "p", usd(5_000), NOT_RECEIVED);
        dc.advanceDays(21);
        List<String> inq = d.sweepDeadlines();
        check("an overdue inquiry is reported, not moved", inq.stream().anyMatch(s -> s.startsWith("inq overdue"))
                && d.dispute("inq").state() == DisputeState.INQUIRY);
        check("inquiry moved no money", d.merchantImpact("inq").isZero());
        d.apply("inq", DisputeEvent.of("y2", DisputeEventKind.CHARGEBACK_RECEIVED, "issuer escalated"));
        check("inquiry escalated to chargeback debits the merchant", d.merchantImpact("inq").equals(usd(-6_500)));

        // --- validation ------------------------------------------------------------------------------------------------
        checkThrows("duplicate dispute id refused", IllegalStateException.class, () -> d.openChargeback("late", "p", usd(1), fraud));
        checkThrows("non-positive amount refused", IllegalArgumentException.class, () -> d.openChargeback("z", "p", usd(0), fraud));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }

}
