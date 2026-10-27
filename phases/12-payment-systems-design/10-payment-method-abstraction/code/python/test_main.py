"""Tests for phases/12-payment-systems-design/10-payment-method-abstraction. Run: python3 -m unittest -v"""

import unittest
from typing import Optional

from main import (EUR, JPY, USD, BankDebitAdapter, BankDebitMethod, Capabilities, Capability, CardAdapter, CardMethod,
                  ChargeRequest, ConformanceTarget, CurrencyMismatchError, DeclineReason, FakeBankDebitApi,
                  FakeCardNetworkApi, MethodType, Money, PaymentEvent, PaymentResult, PaymentService, PaymentStatus,
                  ProcessorConformance, RetryAdvice, Rig, UnsupportedCapabilityError, WalletAdapter, WalletMethod, targets)

USD45 = Money.parse("45.00", USD)


class LossyCardAdapter:
    """A card adapter that collapses every decline to DO_NOT_HONOR: the lossy mapping the suite must catch."""

    def __init__(self, api: FakeCardNetworkApi) -> None:
        self._real = CardAdapter(api)

    def name(self) -> str:
        return "lossy_card"

    def method_type(self) -> MethodType:
        return MethodType.CARD

    def capabilities(self) -> Capabilities:
        return self._real.capabilities()

    def charge(self, request: ChargeRequest) -> PaymentResult:
        r = self._real.charge(request)
        if r.status is not PaymentStatus.DECLINED:
            return r
        return PaymentResult(r.reference, r.processor_ref, r.status, r.amount, DeclineReason.DO_NOT_HONOR, r.raw_code)

    def capture(self, ref: str, amount: Money) -> PaymentResult:
        return self._real.capture(ref, amount)

    def refund(self, ref: str, amount: Money) -> PaymentResult:
        return self._real.refund(ref, amount)

    def translate_event(self, raw: str) -> Optional[PaymentEvent]:
        return self._real.translate_event(raw)


class NoDedupeBankAdapter:
    """A bank adapter that forgets the rail has no idempotency and submits every call."""

    def __init__(self, api: FakeBankDebitApi) -> None:
        self._api = api
        self._translator = BankDebitAdapter(api)
        self._debited: dict[str, Money] = {}

    def name(self) -> str:
        return "no_dedupe_bank"

    def method_type(self) -> MethodType:
        return MethodType.BANK_DEBIT

    def capabilities(self) -> Capabilities:
        return self._translator.capabilities()

    def charge(self, request: ChargeRequest) -> PaymentResult:
        self.capabilities().require_currency(self.name(), request.amount.currency)
        if not request.capture:
            self.capabilities().require(self.name(), Capability.SEPARATE_CAPTURE)
        trace = self._api.submit_debit(request.method.token, request.amount.to_plain_string())
        self._debited[trace] = request.amount
        return PaymentResult(request.reference, trace, PaymentStatus.PENDING, request.amount, DeclineReason.NONE, "")

    def capture(self, ref: str, amount: Money) -> PaymentResult:
        return self._translator.capture(ref, amount)

    def refund(self, ref: str, amount: Money) -> PaymentResult:
        over = amount.compare_to(self._debited[ref]) > 0
        return PaymentResult(None, ref, PaymentStatus.DECLINED if over else PaymentStatus.PENDING, amount,
                             DeclineReason.INVALID_AMOUNT if over else DeclineReason.NONE, "")

    def translate_event(self, raw: str) -> Optional[PaymentEvent]:
        return self._translator.translate_event(raw)


class ConformanceTest(unittest.TestCase):
    def test_every_real_adapter_passes(self) -> None:
        for target in targets():
            report = ProcessorConformance.run(target)
            with self.subTest(adapter=report.adapter):
                self.assertTrue(report.passed(), report.failures)
                self.assertGreaterEqual(report.checks, 19)

    def test_suite_has_teeth(self) -> None:
        api = FakeCardNetworkApi()
        lossy = ProcessorConformance.run(ConformanceTarget(LossyCardAdapter(api), USD, CardMethod("tok_visa_4242", "visa"),
                                                           CardMethod("tok_insufficient", "visa"), lambda: [],
                                                           api.vendor_calls, api.charges_created))
        self.assertFalse(lossy.passed())
        self.assertIn("lossy_card: insufficient funds maps to INSUFFICIENT_FUNDS", lossy.failures)
        bank = FakeBankDebitApi()
        no_dedupe = ProcessorConformance.run(ConformanceTarget(NoDedupeBankAdapter(bank), USD, BankDebitMethod("acct_ok", "m1"),
                                                               BankDebitMethod("acct_nsf", "m2"), bank.drain_events,
                                                               bank.vendor_calls, bank.charges_created))
        self.assertFalse(no_dedupe.passed())
        self.assertIn("no_dedupe_bank: same reference creates exactly one vendor charge", no_dedupe.failures)


class VocabularyTest(unittest.TestCase):
    def test_card_codes(self) -> None:
        self.assertIs(CardAdapter.map_code("51"), DeclineReason.INSUFFICIENT_FUNDS)
        self.assertIs(CardAdapter.map_code("05"), DeclineReason.DO_NOT_HONOR)
        self.assertIs(CardAdapter.map_code("54"), DeclineReason.EXPIRED_INSTRUMENT)
        self.assertIs(CardAdapter.map_code("59"), DeclineReason.SUSPECTED_FRAUD)
        self.assertIs(CardAdapter.map_code("91"), DeclineReason.ISSUER_UNAVAILABLE)
        self.assertIs(CardAdapter.map_code("1A"), DeclineReason.AUTHENTICATION_REQUIRED)
        self.assertIs(CardAdapter.map_code("N7"), DeclineReason.UNKNOWN, "unmapped -> UNKNOWN, never NONE")

    def test_bank_and_wallet_codes(self) -> None:
        self.assertIs(BankDebitAdapter.map_code("R01"), DeclineReason.INSUFFICIENT_FUNDS)
        self.assertIs(BankDebitAdapter.map_code("R02"), DeclineReason.INVALID_ACCOUNT)
        self.assertIs(BankDebitAdapter.map_code("R10"), DeclineReason.UNAUTHORIZED_DEBIT)
        self.assertIs(BankDebitAdapter.map_code("R99"), DeclineReason.UNKNOWN)
        self.assertIs(WalletAdapter.map_issue("INSTRUMENT_DECLINED"), DeclineReason.DO_NOT_HONOR)

    def test_retry_advice_is_attached_to_the_reason(self) -> None:
        self.assertIs(DeclineReason.SUSPECTED_FRAUD.advice(), RetryAdvice.DO_NOT_RETRY)
        self.assertIs(DeclineReason.ISSUER_UNAVAILABLE.advice(), RetryAdvice.RETRY_NOW)
        self.assertIs(DeclineReason.UNKNOWN.advice(), RetryAdvice.DO_NOT_RETRY)
        self.assertIs(DeclineReason.AUTHENTICATION_REQUIRED.advice(), RetryAdvice.RETRY_WITH_CUSTOMER)


class WireQuirksTest(unittest.TestCase):
    def test_quirks_are_translated_inside_adapters(self) -> None:
        rig = Rig.create()
        rig.service.pay(ChargeRequest("w1", Money.parse("4500", JPY), CardMethod("tok_visa_4242", "visa"), True))
        self.assertEqual(rig.card.last_wire(), "amount=4500 currency=392")
        rig.service.pay(ChargeRequest("w2", USD45, BankDebitMethod("acct_ok", "m"), True))
        self.assertEqual(rig.bank.last_wire(), "amount=45.00")
        rig.service.pay(ChargeRequest("w3", Money.parse("19.99", EUR), WalletMethod("wal_ok", "wallet"), False))
        self.assertEqual(rig.wallet.last_wire(), "amount=1999 currency=eur intent=AUTHORIZE")


class StatusTest(unittest.TestCase):
    def test_requires_action_is_one_vocabulary(self) -> None:
        rig = Rig.create()
        sca = rig.service.pay(ChargeRequest("s1", USD45, CardMethod("tok_sca_required", "visa"), True))
        wal = rig.service.pay(ChargeRequest("s2", USD45, WalletMethod("wal_action", "wallet"), True))
        self.assertIs(sca.status, PaymentStatus.REQUIRES_ACTION)
        self.assertIs(wal.status, PaymentStatus.REQUIRES_ACTION)
        rig.wallet.payer_abandons(wal.processor_ref)
        for raw in rig.wallet.drain_events():
            rig.service.on_raw_event(MethodType.WALLET, raw)
        looked = rig.service.lookup(wal.processor_ref)
        assert looked is not None
        self.assertIs(looked.status, PaymentStatus.FAILED)

    def test_async_confirmation_through_the_service(self) -> None:
        rig = Rig.create()
        debit = rig.service.pay(ChargeRequest("a1", USD45, BankDebitMethod("acct_closed", "m"), True))
        self.assertIs(debit.status, PaymentStatus.PENDING)
        raw = rig.bank.drain_events()
        self.assertEqual(raw, ["ach|returned|ach_000001|R02"])
        applied = rig.service.on_raw_event(MethodType.BANK_DEBIT, raw[0])
        self.assertIsNotNone(applied)
        assert applied is not None
        self.assertIs(applied.status, PaymentStatus.FAILED)
        self.assertIs(applied.reason, DeclineReason.INVALID_ACCOUNT)
        self.assertIsNone(rig.service.on_raw_event(MethodType.BANK_DEBIT, raw[0]), "redelivered event on a final payment is ignored")
        self.assertIsNone(rig.service.on_raw_event(MethodType.BANK_DEBIT, "ach|settled|ach_999999"), "unknown reference is ignored")
        self.assertIsNone(CardAdapter(FakeCardNetworkApi()).translate_event("ach|settled|x"))

    def test_adapter_supplies_the_dedupe_the_rail_lacks(self) -> None:
        api = FakeBankDebitApi()
        api.submit_debit("acct_ok", "45.00")
        api.submit_debit("acct_ok", "45.00")
        self.assertEqual(api.charges_created(), 2, "raw rail double-debits on resubmit")
        adapter = BankDebitAdapter(FakeBankDebitApi())
        r = ChargeRequest("d1", USD45, BankDebitMethod("acct_ok", "m"), True)
        self.assertEqual(adapter.charge(r).processor_ref, adapter.charge(r).processor_ref)


class CapabilityTest(unittest.TestCase):
    def test_refusals(self) -> None:
        rig = Rig.create()
        with self.assertRaises(UnsupportedCapabilityError):
            BankDebitAdapter(rig.bank).capture("ach_000001", USD45)
        with self.assertRaises(UnsupportedCapabilityError):
            rig.service.pay(ChargeRequest("c1", Money.parse("100", JPY), WalletMethod("wal_ok", "w"), True))
        self.assertEqual(rig.wallet.vendor_calls(), 0, "refused JPY wallet charge never reached the vendor")
        auth = rig.service.pay(ChargeRequest("c2", Money.parse("100.00", USD), CardMethod("tok_visa_4242", "visa"), False))
        cap = rig.service.processor_for(MethodType.CARD).capture(auth.processor_ref, Money.parse("120.00", USD))
        self.assertIs(cap.status, PaymentStatus.DECLINED)
        self.assertIs(cap.reason, DeclineReason.INVALID_AMOUNT)
        self.assertEqual(Capabilities(frozenset(), frozenset({USD})).flags, frozenset(), "capabilities is a defensive copy")

    def test_construction_rules(self) -> None:
        with self.assertRaises(ValueError):
            ChargeRequest("z", Money.of_minor(0, USD), CardMethod("t", "visa"), True)
        with self.assertRaises(ValueError):
            CardAdapter(FakeCardNetworkApi()).charge(ChargeRequest("x", USD45, WalletMethod("wal_ok", "w"), True))
        with self.assertRaises(RuntimeError):
            s = PaymentService()
            s.register(CardAdapter(FakeCardNetworkApi()))
            s.register(CardAdapter(FakeCardNetworkApi()))
        self.assertEqual(USD45.to_plain_string(), "45.00")
        self.assertEqual(Money.parse("4500", JPY).to_plain_string(), "4500")
        with self.assertRaises(CurrencyMismatchError):
            USD45.plus(Money.parse("1.00", EUR))


if __name__ == "__main__":
    unittest.main()
