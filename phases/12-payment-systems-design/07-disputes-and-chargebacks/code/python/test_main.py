"""Tests for phases/12-payment-systems-design/07-disputes-and-chargebacks. Run: python3 -m unittest -v"""

import unittest
from datetime import date

from main import (ApplyOutcome, DisputeEvent, DisputeEventKind, DisputeService, DisputeState, EvidenceBundle,
                  EvidenceItem, EvidenceType, FakeClock, IllegalDisputeTransitionError, ReasonCodes, service, usd)

_seq = 0


def eid() -> str:
    global _seq
    _seq += 1
    return f"e{_seq}"


NOT_RECEIVED = ReasonCodes.get("VISA", "13.1")
FRAUD = ReasonCodes.get("VISA", "10.4")
DELIVERY = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "r"), EvidenceItem(EvidenceType.PROOF_OF_DELIVERY, "pod"))
_WITH_EVIDENCE = (DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.RESPOND_PRE_ARB, DisputeEventKind.RESPOND_INQUIRY)


def ev(k: DisputeEventKind) -> DisputeEvent:
    if k in _WITH_EVIDENCE:
        return DisputeEvent.with_evidence(eid(), k, DELIVERY, "t")
    return DisputeEvent.of(eid(), k, "t")


_PATHS = {
    DisputeState.CHARGEBACK: (),
    DisputeState.REPRESENTED: (DisputeEventKind.SUBMIT_REPRESENTMENT,),
    DisputeState.PRE_ARBITRATION: (DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED),
    DisputeState.PRE_ARB_RESPONDED: (DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED, DisputeEventKind.RESPOND_PRE_ARB),
    DisputeState.ARBITRATION: (DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.PRE_ARB_RECEIVED,
                               DisputeEventKind.RESPOND_PRE_ARB, DisputeEventKind.ARBITRATION_FILED),
    DisputeState.WON: (DisputeEventKind.SUBMIT_REPRESENTMENT, DisputeEventKind.ISSUER_ACCEPTED),
    DisputeState.LOST: (DisputeEventKind.ACCEPT,),
}


def dispute_in(svc: DisputeService, target: DisputeState) -> str:
    """Drive a fresh 100.00 USD 13.1 dispute into the requested state along the canonical path."""
    global _seq
    _seq += 1
    id = f"d{_seq}"
    via_inquiry = target in (DisputeState.INQUIRY, DisputeState.INQUIRY_RESPONDED, DisputeState.CLOSED_NO_CHARGEBACK)
    if via_inquiry:
        svc.open_inquiry(id, "p", usd(10_000), NOT_RECEIVED)
        if target is not DisputeState.INQUIRY:
            svc.apply(id, ev(DisputeEventKind.RESPOND_INQUIRY))
        if target is DisputeState.CLOSED_NO_CHARGEBACK:
            svc.apply(id, ev(DisputeEventKind.INQUIRY_CLOSED))
        return id
    svc.open_chargeback(id, "p", usd(10_000), NOT_RECEIVED)
    for k in _PATHS[target]:
        svc.apply(id, ev(k))
    return id


def fresh() -> DisputeService:
    return service(FakeClock(date(2026, 2, 2)))


class TableShapeTest(unittest.TestCase):
    def test_table_shape(self) -> None:
        self.assertEqual(len(DisputeService.RULES), 18)
        self.assertTrue(all(not r.from_state.is_terminal() for r in DisputeService.RULES))
        self.assertFalse(any(r.kind is DisputeEventKind.DEADLINE_PASSED
                             and r.from_state in (DisputeState.ARBITRATION, DisputeState.INQUIRY) for r in DisputeService.RULES))

    def test_every_rule_fires_as_written(self) -> None:
        for r in DisputeService.RULES:
            with self.subTest(rule=f"{r.from_state.name} + {r.kind.name}"):
                rc = FakeClock(date(2026, 2, 2))
                svc = service(rc)
                id = dispute_in(svc, r.from_state)
                if r.kind is DisputeEventKind.DEADLINE_PASSED:
                    rc.advance_days(31)
                    svc.sweep_deadlines()
                else:
                    svc.apply(id, ev(r.kind))
                self.assertIs(svc.dispute(id).state, r.to_state)
                self.assertTrue(svc.ledger().balances())

    def test_every_pair_not_in_the_table_is_refused(self) -> None:
        illegal = 0
        for s in DisputeState:
            for k in DisputeEventKind:
                if any(r.from_state is s and r.kind is k for r in DisputeService.RULES):
                    continue
                with self.subTest(state=s.name, kind=k.name):
                    svc = fresh()
                    id = dispute_in(svc, s)
                    entries = len(svc.ledger().journal())
                    versions = len(svc.dispute(id).history)
                    with self.assertRaises(IllegalDisputeTransitionError):
                        svc.apply(id, ev(k))
                    self.assertIs(svc.dispute(id).state, s)
                    self.assertEqual(len(svc.ledger().journal()), entries)
                    self.assertEqual(len(svc.dispute(id).history), versions)
                illegal += 1
        self.assertEqual(illegal, 112, "10 states x 13 events - 18 rules")


class EvidenceTest(unittest.TestCase):
    def test_requirements(self) -> None:
        receipt_only = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "r"))
        self.assertEqual(receipt_only.missing_for(NOT_RECEIVED),
                         [frozenset({EvidenceType.PROOF_OF_DELIVERY, EvidenceType.SIGNED_DELIVERY})])
        self.assertEqual(EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "r"),
                                           EvidenceItem(EvidenceType.SIGNED_DELIVERY, "s")).missing_for(NOT_RECEIVED), [])
        self.assertEqual(len(EvidenceBundle.of().missing_for(NOT_RECEIVED)), 2)
        tds = EvidenceBundle.of(EvidenceItem(EvidenceType.THREE_DS_AUTHENTICATION, "eci05"))
        self.assertTrue(tds.liability_shift(FRAUD))
        self.assertFalse(tds.liability_shift(NOT_RECEIVED))
        self.assertEqual(len(EvidenceBundle.of(EvidenceItem(EvidenceType.AVS_CVV_MATCH, "Y/M"),
                                               EvidenceItem(EvidenceType.RECEIPT, "r")).missing_for(FRAUD)), 1)
        with self.assertRaises(ValueError):
            ReasonCodes.get("VISA", "99.9")

    def test_incomplete_evidence_refused(self) -> None:
        svc = fresh()
        svc.open_chargeback("dp", "pay", usd(24_900), NOT_RECEIVED)
        receipt_only = EvidenceBundle.of(EvidenceItem(EvidenceType.RECEIPT, "r"))
        with self.assertRaises(IllegalDisputeTransitionError):
            svc.apply("dp", DisputeEvent.with_evidence("x1", DisputeEventKind.SUBMIT_REPRESENTMENT, receipt_only, "thin"))
        with self.assertRaises(IllegalDisputeTransitionError):
            svc.apply("dp", DisputeEvent.of("x2", DisputeEventKind.SUBMIT_REPRESENTMENT, "none"))
        self.assertIs(svc.dispute("dp").state, DisputeState.CHARGEBACK)


class MoneyOnTheFullPathTest(unittest.TestCase):
    def test_full_course(self) -> None:
        clock = FakeClock(date(2026, 2, 2))
        svc = service(clock)
        svc.open_chargeback("dp", "pay", usd(24_900), NOT_RECEIVED)
        self.assertEqual(svc.merchant_impact("dp"), usd(-26_400), "chargeback debits amount + 15.00 fee")
        self.assertEqual(svc.ledger().balance(DisputeService.FEES), usd(1_500))
        self.assertEqual(svc.dispute("dp").deadline, date(2026, 2, 22))
        clock.advance_days(12)
        svc.apply("dp", DisputeEvent.with_evidence("x3", DisputeEventKind.SUBMIT_REPRESENTMENT, DELIVERY, "ok"))
        self.assertEqual(svc.ledger().balance(DisputeService.HOLD), usd(24_900), "re-credited into the hold")
        self.assertEqual(svc.merchant_impact("dp"), usd(-26_400), "not to the merchant")
        clock.advance_days(29)
        svc.apply("dp", DisputeEvent.of("x4", DisputeEventKind.PRE_ARB_RECEIVED, "pre-arb"))
        self.assertEqual(svc.ledger().balance(DisputeService.HOLD), usd(24_900), "pre-arbitration moves no money")
        clock.advance_days(15)
        svc.apply("dp", DisputeEvent.with_evidence("x5", DisputeEventKind.RESPOND_PRE_ARB, DELIVERY, "respond"))
        clock.advance_days(7)
        svc.apply("dp", DisputeEvent.of("x6", DisputeEventKind.ARBITRATION_FILED, "filed"))
        self.assertIsNone(svc.dispute("dp").deadline, "arbitration has no deadline")
        clock.advance_days(55)
        svc.sweep_deadlines()
        self.assertIs(svc.dispute("dp").state, DisputeState.ARBITRATION, "sweep does not decide arbitration")
        svc.apply("dp", DisputeEvent.of("x7", DisputeEventKind.RULING_FOR_MERCHANT, "won"))
        self.assertIs(svc.dispute("dp").state, DisputeState.WON)
        self.assertTrue(svc.ledger().balance(DisputeService.HOLD).is_zero())
        self.assertEqual(svc.merchant_impact("dp"), usd(-1_500), "merchant is out only the fee")
        self.assertEqual(DisputeService.days_between(date(2026, 2, 2), clock.today()), 118)
        self.assertEqual(len(svc.dispute("dp").history), 6)
        self.assertEqual(svc.dispute("dp").history[5].version, 5)
        self.assertIs(svc.apply("dp", DisputeEvent.of("x7", DisputeEventKind.RULING_FOR_MERCHANT, "again")), ApplyOutcome.DUPLICATE)
        with self.assertRaises(IllegalDisputeTransitionError):
            svc.apply("dp", DisputeEvent.of("x8", DisputeEventKind.RULING_FOR_ISSUER, "late"))

    def test_losing_at_each_stage(self) -> None:
        svc = fresh()
        accepted = dispute_in(svc, DisputeState.LOST)
        self.assertEqual(svc.merchant_impact(accepted), usd(-11_500), "accepting the chargeback: amount + fee")
        pre_arb = dispute_in(svc, DisputeState.PRE_ARBITRATION)
        svc.apply(pre_arb, ev(DisputeEventKind.ACCEPT))
        self.assertEqual(svc.merchant_impact(pre_arb), usd(-11_500))
        self.assertIs(svc.dispute(pre_arb).state, DisputeState.LOST)
        arb = dispute_in(svc, DisputeState.ARBITRATION)
        svc.apply(arb, ev(DisputeEventKind.RULING_FOR_ISSUER))
        self.assertEqual(svc.merchant_impact(arb), usd(-61_500), "losing arbitration adds the 500.00 filing fee")
        self.assertTrue(svc.ledger().balance(DisputeService.HOLD).is_zero())
        self.assertTrue(svc.ledger().balances())


class DeadlineTest(unittest.TestCase):
    def test_deadlines_and_validation(self) -> None:
        dc = FakeClock(date(2026, 2, 2))
        d = service(dc)
        d.open_chargeback("late", "p", usd(5_000), FRAUD)
        dc.advance_days(20)
        self.assertEqual(d.sweep_deadlines(), [], "on the deadline day nothing fires")
        self.assertIs(d.dispute("late").state, DisputeState.CHARGEBACK)
        dc.advance_days(1)
        fired = d.sweep_deadlines()
        self.assertEqual(len(fired), 1)
        self.assertIs(d.dispute("late").state, DisputeState.LOST)
        d.open_chargeback("early", "p", usd(5_000), FRAUD)
        with self.assertRaises(IllegalDisputeTransitionError):
            d.apply("early", DisputeEvent.of("y0", DisputeEventKind.DEADLINE_PASSED, "forged"))
        self.assertEqual(d.sweep_deadlines(), [], "second sweep is harmless")
        self.assertEqual(len(d.dispute("late").history), 2)
        d.open_chargeback("tooLate", "p", usd(5_000), NOT_RECEIVED)
        dc.advance_days(21)
        with self.assertRaises(IllegalDisputeTransitionError):
            d.apply("tooLate", DisputeEvent.with_evidence("y1", DisputeEventKind.SUBMIT_REPRESENTMENT, DELIVERY, "late"))
        d.open_inquiry("inq", "p", usd(5_000), NOT_RECEIVED)
        dc.advance_days(21)
        inq = d.sweep_deadlines()
        self.assertTrue(any(s.startswith("inq overdue") for s in inq), "an overdue inquiry is reported, not moved")
        self.assertIs(d.dispute("inq").state, DisputeState.INQUIRY)
        self.assertTrue(d.merchant_impact("inq").is_zero())
        d.apply("inq", DisputeEvent.of("y2", DisputeEventKind.CHARGEBACK_RECEIVED, "issuer escalated"))
        self.assertEqual(d.merchant_impact("inq"), usd(-6_500))
        with self.assertRaises(RuntimeError):
            d.open_chargeback("late", "p", usd(1), FRAUD)
        with self.assertRaises(ValueError):
            d.open_chargeback("z", "p", usd(0), FRAUD)


if __name__ == "__main__":
    unittest.main()
