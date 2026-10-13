// Tests for phases/12-payment-systems-design/10-payment-method-abstraction. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
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

    static final Money USD45 = Money.parse("45.00", Currency.USD);

    /** A card adapter that collapses every decline to DO_NOT_HONOR: the lossy mapping the suite must catch. */
    static final class LossyCardAdapter implements PaymentProcessor {
        private final CardAdapter real;
        LossyCardAdapter(FakeCardNetworkApi api) { this.real = new CardAdapter(api); }
        public String name() { return "lossy_card"; }
        public MethodType methodType() { return MethodType.CARD; }
        public Capabilities capabilities() { return real.capabilities(); }
        public PaymentResult charge(ChargeRequest request) {
            PaymentResult r = real.charge(request);
            if (r.status() != PaymentStatus.DECLINED) return r;
            return new PaymentResult(r.reference(), r.processorRef(), r.status(), r.amount(), DeclineReason.DO_NOT_HONOR, r.rawCode());
        }
        public PaymentResult capture(String ref, Money amount) { return real.capture(ref, amount); }
        public PaymentResult refund(String ref, Money amount) { return real.refund(ref, amount); }
        public Optional<PaymentEvent> translateEvent(String raw) { return real.translateEvent(raw); }
    }

    /** A bank adapter that forgets the rail has no idempotency and submits every call. */
    static final class NoDedupeBankAdapter implements PaymentProcessor {
        private final FakeBankDebitApi api;
        private final BankDebitAdapter translator;
        private final Map<String, Money> debited = new HashMap<>();
        NoDedupeBankAdapter(FakeBankDebitApi api) { this.api = api; this.translator = new BankDebitAdapter(api); }
        public String name() { return "no_dedupe_bank"; }
        public MethodType methodType() { return MethodType.BANK_DEBIT; }
        public Capabilities capabilities() { return translator.capabilities(); }
        public PaymentResult charge(ChargeRequest request) {
            capabilities().requireCurrency(name(), request.amount().currency());
            if (!request.capture()) capabilities().require(name(), Capability.SEPARATE_CAPTURE);
            String trace = api.submitDebit(request.method().token(), request.amount().toPlainString());
            debited.put(trace, request.amount());
            return new PaymentResult(request.reference(), trace, PaymentStatus.PENDING, request.amount(), DeclineReason.NONE, "");
        }
        public PaymentResult capture(String ref, Money amount) { return translator.capture(ref, amount); }
        public PaymentResult refund(String ref, Money amount) {
            boolean over = amount.compareTo(debited.get(ref)) > 0;
            return new PaymentResult(null, ref, over ? PaymentStatus.DECLINED : PaymentStatus.PENDING, amount,
                    over ? DeclineReason.INVALID_AMOUNT : DeclineReason.NONE, "");
        }
        public Optional<PaymentEvent> translateEvent(String raw) { return translator.translateEvent(raw); }
    }

    public static void main(String[] args) {
        // The conformance suite passes for every real adapter.
        for (ConformanceTarget target : Main.targets()) {
            ConformanceReport report = ProcessorConformance.run(target);
            check("conformance passes: " + report.adapter() + " " + report.failures(), report.passed());
            check("conformance runs 19+ checks: " + report.adapter(), report.checks() >= 19);
        }

        // ...and has teeth: it rejects a lossy mapping and a missing dedupe.
        {
            FakeCardNetworkApi api = new FakeCardNetworkApi();
            ConformanceReport lossy = ProcessorConformance.run(new ConformanceTarget(new LossyCardAdapter(api), Currency.USD,
                    new CardMethod("tok_visa_4242", "visa"), new CardMethod("tok_insufficient", "visa"),
                    List::of, api::vendorCalls, api::chargesCreated));
            check("lossy mapping fails conformance", !lossy.passed());
            check("lossy mapping fails on the reason check",
                    lossy.failures().contains("lossy_card: insufficient funds maps to INSUFFICIENT_FUNDS"));
        }
        {
            FakeBankDebitApi api = new FakeBankDebitApi();
            ConformanceReport noDedupe = ProcessorConformance.run(new ConformanceTarget(new NoDedupeBankAdapter(api), Currency.USD,
                    new BankDebitMethod("acct_ok", "m1"), new BankDebitMethod("acct_nsf", "m2"),
                    api::drainEvents, api::vendorCalls, api::chargesCreated));
            check("missing dedupe fails conformance", !noDedupe.passed());
            check("missing dedupe fails on the one-vendor-charge check",
                    noDedupe.failures().contains("no_dedupe_bank: same reference creates exactly one vendor charge"));
        }

        // Card: ISO 8583 response codes map to one vocabulary.
        check("card 51 -> INSUFFICIENT_FUNDS", CardAdapter.mapCode("51") == DeclineReason.INSUFFICIENT_FUNDS);
        check("card 05 -> DO_NOT_HONOR", CardAdapter.mapCode("05") == DeclineReason.DO_NOT_HONOR);
        check("card 54 -> EXPIRED_INSTRUMENT", CardAdapter.mapCode("54") == DeclineReason.EXPIRED_INSTRUMENT);
        check("card 59 -> SUSPECTED_FRAUD", CardAdapter.mapCode("59") == DeclineReason.SUSPECTED_FRAUD);
        check("card 91 -> ISSUER_UNAVAILABLE", CardAdapter.mapCode("91") == DeclineReason.ISSUER_UNAVAILABLE);
        check("card 1A -> AUTHENTICATION_REQUIRED", CardAdapter.mapCode("1A") == DeclineReason.AUTHENTICATION_REQUIRED);
        check("unmapped card code -> UNKNOWN, never NONE", CardAdapter.mapCode("N7") == DeclineReason.UNKNOWN);

        // Bank: NACHA return codes map to the same vocabulary.
        check("R01 -> INSUFFICIENT_FUNDS", BankDebitAdapter.mapCode("R01") == DeclineReason.INSUFFICIENT_FUNDS);
        check("R02 -> INVALID_ACCOUNT", BankDebitAdapter.mapCode("R02") == DeclineReason.INVALID_ACCOUNT);
        check("R10 -> UNAUTHORIZED_DEBIT", BankDebitAdapter.mapCode("R10") == DeclineReason.UNAUTHORIZED_DEBIT);
        check("unmapped return code -> UNKNOWN", BankDebitAdapter.mapCode("R99") == DeclineReason.UNKNOWN);
        check("wallet INSTRUMENT_DECLINED -> DO_NOT_HONOR", WalletAdapter.mapIssue("INSTRUMENT_DECLINED") == DeclineReason.DO_NOT_HONOR);

        // Retry advice is attached to the reason, not decided by callers.
        check("fraud is DO_NOT_RETRY", DeclineReason.SUSPECTED_FRAUD.advice() == RetryAdvice.DO_NOT_RETRY);
        check("issuer unavailable is RETRY_NOW", DeclineReason.ISSUER_UNAVAILABLE.advice() == RetryAdvice.RETRY_NOW);
        check("unknown is DO_NOT_RETRY (fail safe)", DeclineReason.UNKNOWN.advice() == RetryAdvice.DO_NOT_RETRY);
        check("SCA soft decline is RETRY_WITH_CUSTOMER", DeclineReason.AUTHENTICATION_REQUIRED.advice() == RetryAdvice.RETRY_WITH_CUSTOMER);

        // Wire-format quirks are translated inside the adapters.
        {
            Main.Rig rig = Main.Rig.create();
            rig.service().pay(new ChargeRequest("w1", Money.parse("4500", Currency.JPY), new CardMethod("tok_visa_4242", "visa"), true));
            check("card sends JPY as minor units with ISO numeric 392", rig.card().lastWire().equals("amount=4500 currency=392"));
            rig.service().pay(new ChargeRequest("w2", USD45, new BankDebitMethod("acct_ok", "m"), true));
            check("bank sends a decimal string", rig.bank().lastWire().equals("amount=45.00"));
            rig.service().pay(new ChargeRequest("w3", Money.parse("19.99", Currency.EUR), new WalletMethod("wal_ok", "wallet"), false));
            check("wallet sends lowercase currency and AUTHORIZE intent", rig.wallet().lastWire().equals("amount=1999 currency=eur intent=AUTHORIZE"));
        }

        // Statuses: one vocabulary for "needs the customer".
        {
            Main.Rig rig = Main.Rig.create();
            PaymentResult sca = rig.service().pay(new ChargeRequest("s1", USD45, new CardMethod("tok_sca_required", "visa"), true));
            PaymentResult wal = rig.service().pay(new ChargeRequest("s2", USD45, new WalletMethod("wal_action", "wallet"), true));
            check("card 1A is REQUIRES_ACTION", sca.status() == PaymentStatus.REQUIRES_ACTION);
            check("wallet payer action is REQUIRES_ACTION", wal.status() == PaymentStatus.REQUIRES_ACTION);
            rig.wallet().payerAbandons(wal.processorRef());
            for (String raw : rig.wallet().drainEvents()) rig.service().onRawEvent(MethodType.WALLET, raw);
            check("abandoned wallet approval ends FAILED", rig.service().lookup(wal.processorRef()).orElseThrow().status() == PaymentStatus.FAILED);
        }

        // Async confirmation through the service: PENDING, then the event decides; late duplicates are ignored.
        {
            Main.Rig rig = Main.Rig.create();
            PaymentResult debit = rig.service().pay(new ChargeRequest("a1", USD45, new BankDebitMethod("acct_closed", "m"), true));
            check("bank debit starts PENDING", debit.status() == PaymentStatus.PENDING);
            List<String> raw = rig.bank().drainEvents();
            check("bank emits one return event", raw.size() == 1 && raw.get(0).equals("ach|returned|ach_000001|R02"));
            Optional<PaymentResult> applied = rig.service().onRawEvent(MethodType.BANK_DEBIT, raw.get(0));
            check("return event applied -> FAILED INVALID_ACCOUNT",
                    applied.isPresent() && applied.get().status() == PaymentStatus.FAILED && applied.get().reason() == DeclineReason.INVALID_ACCOUNT);
            check("redelivered event on a final payment is ignored", rig.service().onRawEvent(MethodType.BANK_DEBIT, raw.get(0)).isEmpty());
            check("event for an unknown reference is ignored", rig.service().onRawEvent(MethodType.BANK_DEBIT, "ach|settled|ach_999999").isEmpty());
            check("card adapter has no events to translate", new CardAdapter(new FakeCardNetworkApi()).translateEvent("ach|settled|x").isEmpty());
        }

        // The bank rail itself has no dedupe; the adapter supplies it.
        {
            FakeBankDebitApi api = new FakeBankDebitApi();
            api.submitDebit("acct_ok", "45.00");
            api.submitDebit("acct_ok", "45.00");
            check("raw rail double-debits on resubmit", api.chargesCreated() == 2);
            BankDebitAdapter adapter = new BankDebitAdapter(new FakeBankDebitApi());
            ChargeRequest r = new ChargeRequest("d1", USD45, new BankDebitMethod("acct_ok", "m"), true);
            check("adapter returns the same trace on resubmit", adapter.charge(r).processorRef().equals(adapter.charge(r).processorRef()));
        }

        // Capability refusals.
        {
            Main.Rig rig = Main.Rig.create();
            checkThrows("bank capture is unsupported", UnsupportedCapabilityException.class,
                    () -> new BankDebitAdapter(rig.bank()).capture("ach_000001", USD45));
            checkThrows("JPY on wallet is unsupported", UnsupportedCapabilityException.class,
                    () -> rig.service().pay(new ChargeRequest("c1", Money.parse("100", Currency.JPY), new WalletMethod("wal_ok", "w"), true)));
            check("refused JPY wallet charge never reached the vendor", rig.wallet().vendorCalls() == 0);
            PaymentResult auth = rig.service().pay(new ChargeRequest("c2", Money.parse("100.00", Currency.USD), new CardMethod("tok_visa_4242", "visa"), false));
            PaymentResult cap = rig.service().processorFor(MethodType.CARD).capture(auth.processorRef(), Money.parse("120.00", Currency.USD));
            check("card over-capture is DECLINED INVALID_AMOUNT", cap.status() == PaymentStatus.DECLINED && cap.reason() == DeclineReason.INVALID_AMOUNT);
            check("capabilities record is a defensive copy",
                    new Capabilities(EnumSet.noneOf(Capability.class), Set.of(Currency.USD)).flags().isEmpty());
        }

        // Construction rules.
        checkThrows("zero amount rejected", IllegalArgumentException.class,
                () -> new ChargeRequest("z", Money.ofMinor(0, Currency.USD), new CardMethod("t", "visa"), true));
        checkThrows("wrong method type rejected by adapter", IllegalArgumentException.class,
                () -> new CardAdapter(new FakeCardNetworkApi()).charge(new ChargeRequest("x", USD45, new WalletMethod("wal_ok", "w"), true)));
        checkThrows("one processor per method type", IllegalStateException.class, () -> {
            PaymentService s = new PaymentService();
            s.register(new CardAdapter(new FakeCardNetworkApi()));
            s.register(new CardAdapter(new FakeCardNetworkApi()));
        });
        check("Money plain string USD", USD45.toPlainString().equals("45.00"));
        check("Money plain string JPY", Money.parse("4500", Currency.JPY).toPlainString().equals("4500"));
        checkThrows("Money rejects currency mix", CurrencyMismatchException.class, () -> USD45.plus(Money.parse("1.00", Currency.EUR)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
