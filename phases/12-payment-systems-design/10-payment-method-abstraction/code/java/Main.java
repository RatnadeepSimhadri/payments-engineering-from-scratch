// Lesson: phases/12-payment-systems-design/10-payment-method-abstraction/docs/en.md
// Payment Method Abstraction and Processor Adapters
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 8583 field 39 response codes; NACHA Operating Rules (return reason codes R01-R85);
//          ISO 4217 numeric currency codes; EMVCo 3-D Secure 2 (soft decline). Java 21, standard library only.

import java.util.ArrayList;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.function.IntSupplier;
import java.util.function.Supplier;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent, String numeric) {
    static final Currency USD = new Currency("USD", 2, "840");
    static final Currency EUR = new Currency("EUR", 2, "978");
    static final Currency GBP = new Currency("GBP", 2, "826");
    static final Currency JPY = new Currency("JPY", 0, "392");
    static final Currency CHF = new Currency("CHF", 2, "756");

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        }
        String padded = frac + "0".repeat(currency.exponent() - frac.length());
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.isEmpty() ? 0 : Long.parseLong(padded));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    /** Plain decimal without the currency code, e.g. "45.00" or "1200". */
    String toPlainString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result;
    }

    @Override public String toString() { return toPlainString() + " " + currency.code(); }
}

// ---------------------------------------------------------------------------------------------
// The normalized vocabulary. Product code sees only these types, never a vendor's raw codes.
// ---------------------------------------------------------------------------------------------

enum MethodType { CARD, BANK_DEBIT, WALLET }

sealed interface PaymentMethod permits CardMethod, BankDebitMethod, WalletMethod {
    MethodType type();
    String token();
}

record CardMethod(String token, String network) implements PaymentMethod {
    public MethodType type() { return MethodType.CARD; }
}

record BankDebitMethod(String token, String mandateId) implements PaymentMethod {
    public MethodType type() { return MethodType.BANK_DEBIT; }
}

record WalletMethod(String token, String provider) implements PaymentMethod {
    public MethodType type() { return MethodType.WALLET; }
}

enum Capability { SEPARATE_CAPTURE, PARTIAL_CAPTURE, REFUND, PARTIAL_REFUND, ASYNC_CONFIRMATION, CUSTOMER_ACTION }

enum PaymentStatus {
    AUTHORIZED, SUCCEEDED, PENDING, REQUIRES_ACTION, DECLINED, FAILED;

    boolean isFinal() { return this == SUCCEEDED || this == DECLINED || this == FAILED; }
}

enum RetryAdvice { NOT_APPLICABLE, RETRY_NOW, RETRY_LATER, RETRY_WITH_CUSTOMER, DO_NOT_RETRY }

enum DeclineReason {
    NONE(RetryAdvice.NOT_APPLICABLE),
    INSUFFICIENT_FUNDS(RetryAdvice.RETRY_LATER),
    DO_NOT_HONOR(RetryAdvice.RETRY_LATER),
    ISSUER_UNAVAILABLE(RetryAdvice.RETRY_NOW),
    AUTHENTICATION_REQUIRED(RetryAdvice.RETRY_WITH_CUSTOMER),
    EXPIRED_INSTRUMENT(RetryAdvice.DO_NOT_RETRY),
    INVALID_ACCOUNT(RetryAdvice.DO_NOT_RETRY),
    SUSPECTED_FRAUD(RetryAdvice.DO_NOT_RETRY),
    UNAUTHORIZED_DEBIT(RetryAdvice.DO_NOT_RETRY),
    INVALID_AMOUNT(RetryAdvice.DO_NOT_RETRY),
    UNKNOWN(RetryAdvice.DO_NOT_RETRY);

    private final RetryAdvice advice;
    DeclineReason(RetryAdvice advice) { this.advice = advice; }
    RetryAdvice advice() { return advice; }
}

record ChargeRequest(String reference, Money amount, PaymentMethod method, boolean capture) {
    ChargeRequest {
        Objects.requireNonNull(reference, "reference");
        Objects.requireNonNull(method, "method");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
    }
}

/** What every adapter returns. rawCode is for logs and support, never for branching. */
record PaymentResult(String reference, String processorRef, PaymentStatus status, Money amount,
                     DeclineReason reason, String rawCode) {
    PaymentResult {
        Objects.requireNonNull(status, "status");
        Objects.requireNonNull(reason, "reason");
    }
    RetryAdvice advice() { return reason.advice(); }
}

/** An asynchronous outcome (bank return, wallet approval) translated into the same vocabulary. */
record PaymentEvent(String processorRef, PaymentStatus status, DeclineReason reason, String rawCode) {}

final class UnsupportedCapabilityException extends RuntimeException {
    UnsupportedCapabilityException(String message) { super(message); }
}

record Capabilities(Set<Capability> flags, Set<Currency> currencies) {
    Capabilities {
        flags = flags.isEmpty() ? EnumSet.noneOf(Capability.class) : EnumSet.copyOf(flags);
        currencies = Set.copyOf(currencies);
    }

    boolean supports(Capability c) { return flags.contains(c); }

    void require(String adapter, Capability c) {
        if (!supports(c)) throw new UnsupportedCapabilityException(adapter + " does not support " + c);
    }

    void requireCurrency(String adapter, Currency c) {
        if (!currencies.contains(c)) throw new UnsupportedCapabilityException(adapter + " does not support currency " + c.code());
    }
}

/** The port. One interface for every rail; adapters implement it, product code depends on it. */
interface PaymentProcessor {
    String name();
    MethodType methodType();
    Capabilities capabilities();
    PaymentResult charge(ChargeRequest request);
    PaymentResult capture(String processorRef, Money amount);
    PaymentResult refund(String processorRef, Money amount);
    Optional<PaymentEvent> translateEvent(String rawEvent);
}

// ---------------------------------------------------------------------------------------------
// Fake vendors. Each has the quirks of its rail; none of them leak past its adapter.
// ---------------------------------------------------------------------------------------------

/** Card acquirer: ISO 8583-style response codes, ISO 4217 numeric currency, native idempotency keys. */
final class FakeCardNetworkApi {
    record AuthResponse(String responseCode, String authId, long amountMinor, String currencyNumeric) {}

    private final Map<String, AuthResponse> byIdempotencyKey = new HashMap<>();
    private final Map<String, long[]> ledger = new HashMap<>();   // authId -> {authorized, captured, refunded}
    private int vendorCalls;
    private int chargesCreated;
    private String lastWire = "";

    int vendorCalls() { return vendorCalls; }
    int chargesCreated() { return chargesCreated; }
    /** What went over the wire on the last authorize, for tests that pin the quirk translation. */
    String lastWire() { return lastWire; }

    AuthResponse authorize(String idempotencyKey, String panToken, long amountMinor, String currencyNumeric, boolean capture) {
        vendorCalls++;
        lastWire = "amount=" + amountMinor + " currency=" + currencyNumeric;
        AuthResponse prior = byIdempotencyKey.get(idempotencyKey);
        if (prior != null) return prior;
        chargesCreated++;
        String code = switch (panToken) {
            case "tok_insufficient" -> "51";
            case "tok_do_not_honor" -> "05";
            case "tok_expired" -> "54";
            case "tok_fraud" -> "59";
            case "tok_bad_number" -> "14";
            case "tok_issuer_down" -> "91";
            case "tok_sca_required" -> "1A";
            case "tok_weird" -> "N7";
            default -> "00";
        };
        String authId = String.format("auth_%06d", chargesCreated);
        if (code.equals("00")) ledger.put(authId, new long[] {amountMinor, capture ? amountMinor : 0, 0});
        AuthResponse response = new AuthResponse(code, authId, amountMinor, currencyNumeric);
        byIdempotencyKey.put(idempotencyKey, response);
        return response;
    }

    String capture(String authId, long amountMinor) {
        vendorCalls++;
        long[] row = ledger.get(authId);
        if (row == null) return "25";                                   // unable to locate record
        if (row[1] + amountMinor > row[0]) return "13";                 // invalid amount
        row[1] += amountMinor;
        return "00";
    }

    String refund(String authId, long amountMinor) {
        vendorCalls++;
        long[] row = ledger.get(authId);
        if (row == null) return "25";
        if (row[2] + amountMinor > row[1]) return "13";
        row[2] += amountMinor;
        return "00";
    }
}

/** ACH-style bank debit: decimal-string amounts, USD only, no idempotency, outcome arrives days later. */
final class FakeBankDebitApi {
    private final List<String> outbox = new ArrayList<>();
    private int vendorCalls;
    private int chargesCreated;
    private int traceSeq;
    private String lastWire = "";

    int vendorCalls() { return vendorCalls; }
    int chargesCreated() { return chargesCreated; }
    String lastWire() { return lastWire; }

    /** Returns a trace number. Submitting the same debit twice creates two debits: the rail has no dedupe. */
    String submitDebit(String accountToken, String amountDecimal) {
        vendorCalls++;
        lastWire = "amount=" + amountDecimal;
        chargesCreated++;
        String trace = String.format("ach_%06d", ++traceSeq);
        String returnCode = switch (accountToken) {
            case "acct_nsf" -> "R01";
            case "acct_closed" -> "R02";
            case "acct_no_account" -> "R03";
            case "acct_unauthorized" -> "R10";
            case "acct_weird" -> "R99";
            default -> null;
        };
        outbox.add(returnCode == null ? "ach|settled|" + trace : "ach|returned|" + trace + "|" + returnCode);
        return trace;
    }

    String submitCredit(String originalTrace, String amountDecimal) {
        vendorCalls++;
        return String.format("ach_%06d", ++traceSeq);
    }

    /** What the bank's file or webhook delivers later. */
    List<String> drainEvents() {
        List<String> out = new ArrayList<>(outbox);
        outbox.clear();
        return out;
    }
}

/** Wallet: lowercase currency, status words instead of codes, payer approval out of band. */
final class FakeWalletApi {
    record WalletOrder(String id, String status, String issue) {}

    private final Map<String, WalletOrder> byRequestId = new HashMap<>();
    private final Map<String, long[]> ledger = new HashMap<>();   // orderId -> {authorized, captured, refunded}
    private final List<String> outbox = new ArrayList<>();
    private int vendorCalls;
    private int chargesCreated;
    private String lastWire = "";

    int vendorCalls() { return vendorCalls; }
    int chargesCreated() { return chargesCreated; }
    String lastWire() { return lastWire; }

    WalletOrder createOrder(String requestId, String walletToken, long amountMinor, String currencyLower, String intent) {
        vendorCalls++;
        lastWire = "amount=" + amountMinor + " currency=" + currencyLower + " intent=" + intent;
        WalletOrder prior = byRequestId.get(requestId);
        if (prior != null) return prior;
        chargesCreated++;
        String id = String.format("wal_%06d", chargesCreated);
        WalletOrder order = switch (walletToken) {
            case "wal_nsf" -> new WalletOrder(id, "DECLINED", "INSUFFICIENT_FUNDS");
            case "wal_declined" -> new WalletOrder(id, "DECLINED", "INSTRUMENT_DECLINED");
            case "wal_action" -> new WalletOrder(id, "PAYER_ACTION_REQUIRED", "");
            default -> new WalletOrder(id, intent.equals("CAPTURE") ? "COMPLETED" : "APPROVED", "");
        };
        if (!order.status().equals("DECLINED")) {
            ledger.put(id, new long[] {amountMinor, intent.equals("CAPTURE") && order.status().equals("COMPLETED") ? amountMinor : 0, 0});
        }
        byRequestId.put(requestId, order);
        return order;
    }

    /** The wallet captures the full authorized amount or nothing. */
    String captureOrder(String orderId, long amountMinor) {
        vendorCalls++;
        long[] row = ledger.get(orderId);
        if (row == null || row[1] != 0 || amountMinor != row[0]) return "UNPROCESSABLE_ENTITY";
        row[1] = amountMinor;
        return "COMPLETED";
    }

    String refundCapture(String orderId, long amountMinor) {
        vendorCalls++;
        long[] row = ledger.get(orderId);
        if (row == null || row[2] + amountMinor > row[1]) return "UNPROCESSABLE_ENTITY";
        row[2] += amountMinor;
        return "COMPLETED";
    }

    /** The payer finishes the out-of-band step in the wallet's own UI. */
    void payerApproves(String orderId) {
        long[] row = ledger.get(orderId);
        row[1] = row[0];
        outbox.add("wallet:PAYMENT.CAPTURE.COMPLETED:" + orderId);
    }

    void payerAbandons(String orderId) { outbox.add("wallet:PAYMENT.CAPTURE.DENIED:" + orderId); }

    List<String> drainEvents() {
        List<String> out = new ArrayList<>(outbox);
        outbox.clear();
        return out;
    }
}

// ---------------------------------------------------------------------------------------------
// Adapters: the only place a vendor's vocabulary is spoken.
// ---------------------------------------------------------------------------------------------

final class CardAdapter implements PaymentProcessor {
    static final Map<String, DeclineReason> RESPONSE_CODES = Map.of(
            "51", DeclineReason.INSUFFICIENT_FUNDS,
            "05", DeclineReason.DO_NOT_HONOR,
            "54", DeclineReason.EXPIRED_INSTRUMENT,
            "59", DeclineReason.SUSPECTED_FRAUD,
            "14", DeclineReason.INVALID_ACCOUNT,
            "91", DeclineReason.ISSUER_UNAVAILABLE,
            "1A", DeclineReason.AUTHENTICATION_REQUIRED,
            "13", DeclineReason.INVALID_AMOUNT);

    private static final Capabilities CAPS = new Capabilities(
            EnumSet.of(Capability.SEPARATE_CAPTURE, Capability.PARTIAL_CAPTURE, Capability.REFUND, Capability.PARTIAL_REFUND),
            Set.of(Currency.USD, Currency.EUR, Currency.GBP, Currency.JPY));

    private final FakeCardNetworkApi api;

    CardAdapter(FakeCardNetworkApi api) { this.api = api; }

    public String name() { return "card"; }
    public MethodType methodType() { return MethodType.CARD; }
    public Capabilities capabilities() { return CAPS; }

    static DeclineReason mapCode(String code) { return RESPONSE_CODES.getOrDefault(code, DeclineReason.UNKNOWN); }

    public PaymentResult charge(ChargeRequest request) {
        if (!(request.method() instanceof CardMethod card)) throw new IllegalArgumentException("card adapter given " + request.method().type());
        CAPS.requireCurrency(name(), request.amount().currency());
        if (!request.capture()) CAPS.require(name(), Capability.SEPARATE_CAPTURE);
        FakeCardNetworkApi.AuthResponse r = api.authorize(request.reference(), card.token(),
                request.amount().minorUnits(), request.amount().currency().numeric(), request.capture());
        if (r.responseCode().equals("00")) {
            PaymentStatus status = request.capture() ? PaymentStatus.SUCCEEDED : PaymentStatus.AUTHORIZED;
            return new PaymentResult(request.reference(), r.authId(), status, request.amount(), DeclineReason.NONE, "00");
        }
        DeclineReason reason = mapCode(r.responseCode());
        PaymentStatus status = reason == DeclineReason.AUTHENTICATION_REQUIRED ? PaymentStatus.REQUIRES_ACTION : PaymentStatus.DECLINED;
        return new PaymentResult(request.reference(), r.authId(), status, request.amount(), reason, r.responseCode());
    }

    public PaymentResult capture(String processorRef, Money amount) {
        String code = api.capture(processorRef, amount.minorUnits());
        return settle(processorRef, amount, code);
    }

    public PaymentResult refund(String processorRef, Money amount) {
        String code = api.refund(processorRef, amount.minorUnits());
        return settle(processorRef, amount, code);
    }

    private PaymentResult settle(String processorRef, Money amount, String code) {
        if (code.equals("00")) return new PaymentResult(null, processorRef, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, code);
        return new PaymentResult(null, processorRef, PaymentStatus.DECLINED, amount, mapCode(code), code);
    }

    /** Card authorizations are synchronous; this rail has no confirmation events to translate. */
    public Optional<PaymentEvent> translateEvent(String rawEvent) { return Optional.empty(); }
}

final class BankDebitAdapter implements PaymentProcessor {
    static final Map<String, DeclineReason> RETURN_CODES = Map.of(
            "R01", DeclineReason.INSUFFICIENT_FUNDS,
            "R09", DeclineReason.INSUFFICIENT_FUNDS,
            "R02", DeclineReason.INVALID_ACCOUNT,
            "R03", DeclineReason.INVALID_ACCOUNT,
            "R04", DeclineReason.INVALID_ACCOUNT,
            "R16", DeclineReason.DO_NOT_HONOR,
            "R10", DeclineReason.UNAUTHORIZED_DEBIT,
            "R29", DeclineReason.UNAUTHORIZED_DEBIT);

    private static final Capabilities CAPS = new Capabilities(
            EnumSet.of(Capability.REFUND, Capability.PARTIAL_REFUND, Capability.ASYNC_CONFIRMATION),
            Set.of(Currency.USD));

    private final FakeBankDebitApi api;
    private final Map<String, PaymentResult> byReference = new HashMap<>();   // the rail has no dedupe, so we do
    private final Map<String, Money> debited = new HashMap<>();
    private final Map<String, Money> refunded = new HashMap<>();

    BankDebitAdapter(FakeBankDebitApi api) { this.api = api; }

    public String name() { return "bank_debit"; }
    public MethodType methodType() { return MethodType.BANK_DEBIT; }
    public Capabilities capabilities() { return CAPS; }

    static DeclineReason mapCode(String code) { return RETURN_CODES.getOrDefault(code, DeclineReason.UNKNOWN); }

    public PaymentResult charge(ChargeRequest request) {
        if (!(request.method() instanceof BankDebitMethod account)) throw new IllegalArgumentException("bank adapter given " + request.method().type());
        CAPS.requireCurrency(name(), request.amount().currency());
        if (!request.capture()) CAPS.require(name(), Capability.SEPARATE_CAPTURE);
        PaymentResult prior = byReference.get(request.reference());
        if (prior != null) return prior;
        String trace = api.submitDebit(account.token(), request.amount().toPlainString());
        PaymentResult result = new PaymentResult(request.reference(), trace, PaymentStatus.PENDING, request.amount(), DeclineReason.NONE, "");
        byReference.put(request.reference(), result);
        debited.put(trace, request.amount());
        return result;
    }

    public PaymentResult capture(String processorRef, Money amount) {
        CAPS.require(name(), Capability.SEPARATE_CAPTURE);
        throw new AssertionError("unreachable");
    }

    public PaymentResult refund(String processorRef, Money amount) {
        CAPS.require(name(), Capability.REFUND);
        Money original = debited.get(processorRef);
        if (original == null) throw new IllegalArgumentException("unknown debit " + processorRef);
        Money already = refunded.getOrDefault(processorRef, Money.ofMinor(0, amount.currency()));
        if (already.plus(amount).compareTo(original) > 0) {
            return new PaymentResult(null, processorRef, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, "over_refund");
        }
        String creditTrace = api.submitCredit(processorRef, amount.toPlainString());
        refunded.put(processorRef, already.plus(amount));
        return new PaymentResult(null, creditTrace, PaymentStatus.PENDING, amount, DeclineReason.NONE, "");
    }

    public Optional<PaymentEvent> translateEvent(String rawEvent) {
        String[] parts = rawEvent.split("\\|");
        if (parts.length < 3 || !parts[0].equals("ach")) return Optional.empty();
        return switch (parts[1]) {
            case "settled" -> Optional.of(new PaymentEvent(parts[2], PaymentStatus.SUCCEEDED, DeclineReason.NONE, ""));
            case "returned" -> parts.length < 4 ? Optional.empty()
                    : Optional.of(new PaymentEvent(parts[2], PaymentStatus.FAILED, mapCode(parts[3]), parts[3]));
            default -> Optional.empty();
        };
    }
}

final class WalletAdapter implements PaymentProcessor {
    static final Map<String, DeclineReason> ISSUES = Map.of(
            "INSUFFICIENT_FUNDS", DeclineReason.INSUFFICIENT_FUNDS,
            "INSTRUMENT_DECLINED", DeclineReason.DO_NOT_HONOR,
            "PAYER_CANNOT_PAY", DeclineReason.DO_NOT_HONOR);

    private static final Capabilities CAPS = new Capabilities(
            EnumSet.of(Capability.SEPARATE_CAPTURE, Capability.REFUND, Capability.PARTIAL_REFUND, Capability.CUSTOMER_ACTION),
            Set.of(Currency.USD, Currency.EUR, Currency.GBP));

    private final FakeWalletApi api;
    private final Map<String, Money> authorized = new HashMap<>();

    WalletAdapter(FakeWalletApi api) { this.api = api; }

    public String name() { return "wallet"; }
    public MethodType methodType() { return MethodType.WALLET; }
    public Capabilities capabilities() { return CAPS; }

    static DeclineReason mapIssue(String issue) { return ISSUES.getOrDefault(issue, DeclineReason.UNKNOWN); }

    public PaymentResult charge(ChargeRequest request) {
        if (!(request.method() instanceof WalletMethod wallet)) throw new IllegalArgumentException("wallet adapter given " + request.method().type());
        CAPS.requireCurrency(name(), request.amount().currency());
        if (!request.capture()) CAPS.require(name(), Capability.SEPARATE_CAPTURE);
        FakeWalletApi.WalletOrder o = api.createOrder(request.reference(), wallet.token(), request.amount().minorUnits(),
                request.amount().currency().code().toLowerCase(), request.capture() ? "CAPTURE" : "AUTHORIZE");
        PaymentStatus status = switch (o.status()) {
            case "COMPLETED" -> PaymentStatus.SUCCEEDED;
            case "APPROVED" -> PaymentStatus.AUTHORIZED;
            case "PAYER_ACTION_REQUIRED" -> PaymentStatus.REQUIRES_ACTION;
            default -> PaymentStatus.DECLINED;
        };
        if (status == PaymentStatus.AUTHORIZED) authorized.put(o.id(), request.amount());
        DeclineReason reason = status == PaymentStatus.DECLINED ? mapIssue(o.issue()) : DeclineReason.NONE;
        return new PaymentResult(request.reference(), o.id(), status, request.amount(), reason, o.status() + (o.issue().isEmpty() ? "" : "/" + o.issue()));
    }

    public PaymentResult capture(String processorRef, Money amount) {
        CAPS.require(name(), Capability.SEPARATE_CAPTURE);
        Money auth = authorized.get(processorRef);
        if (auth != null && amount.compareTo(auth) != 0) CAPS.require(name(), Capability.PARTIAL_CAPTURE);
        String status = api.captureOrder(processorRef, amount.minorUnits());
        return status.equals("COMPLETED")
                ? new PaymentResult(null, processorRef, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, status)
                : new PaymentResult(null, processorRef, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, status);
    }

    public PaymentResult refund(String processorRef, Money amount) {
        CAPS.require(name(), Capability.REFUND);
        String status = api.refundCapture(processorRef, amount.minorUnits());
        return status.equals("COMPLETED")
                ? new PaymentResult(null, processorRef, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, status)
                : new PaymentResult(null, processorRef, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, status);
    }

    public Optional<PaymentEvent> translateEvent(String rawEvent) {
        String[] parts = rawEvent.split(":");
        if (parts.length != 3 || !parts[0].equals("wallet")) return Optional.empty();
        return switch (parts[1]) {
            case "PAYMENT.CAPTURE.COMPLETED" -> Optional.of(new PaymentEvent(parts[2], PaymentStatus.SUCCEEDED, DeclineReason.NONE, parts[1]));
            case "PAYMENT.CAPTURE.DENIED" -> Optional.of(new PaymentEvent(parts[2], PaymentStatus.FAILED, DeclineReason.DO_NOT_HONOR, parts[1]));
            default -> Optional.empty();
        };
    }
}

// ---------------------------------------------------------------------------------------------
// The facade product code calls: route by method type, keep the latest normalized state.
// ---------------------------------------------------------------------------------------------

final class PaymentService {
    private final Map<MethodType, PaymentProcessor> processors = new EnumMap<>(MethodType.class);
    private final Map<String, PaymentResult> byProcessorRef = new HashMap<>();

    void register(PaymentProcessor processor) {
        if (processors.putIfAbsent(processor.methodType(), processor) != null) {
            throw new IllegalStateException("already registered: " + processor.methodType());
        }
    }

    PaymentProcessor processorFor(MethodType type) {
        PaymentProcessor p = processors.get(type);
        if (p == null) throw new UnsupportedCapabilityException("no processor for " + type);
        return p;
    }

    PaymentResult pay(ChargeRequest request) {
        PaymentResult result = processorFor(request.method().type()).charge(request);
        byProcessorRef.put(result.processorRef(), result);
        return result;
    }

    /** Feed a raw notification to the adapter for its rail; apply it if the adapter recognizes it. */
    Optional<PaymentResult> onRawEvent(MethodType type, String rawEvent) {
        Optional<PaymentEvent> event = processorFor(type).translateEvent(rawEvent);
        if (event.isEmpty()) return Optional.empty();
        PaymentEvent e = event.get();
        PaymentResult current = byProcessorRef.get(e.processorRef());
        if (current == null || current.status().isFinal()) return Optional.empty();   // unknown, or already decided
        PaymentResult updated = new PaymentResult(current.reference(), current.processorRef(), e.status(), current.amount(), e.reason(), e.rawCode());
        byProcessorRef.put(e.processorRef(), updated);
        return Optional.of(updated);
    }

    Optional<PaymentResult> lookup(String processorRef) { return Optional.ofNullable(byProcessorRef.get(processorRef)); }
}

// ---------------------------------------------------------------------------------------------
// The conformance suite: one set of contract checks every adapter must pass.
// ---------------------------------------------------------------------------------------------

/** Everything the suite needs to drive one adapter: its vendor's test tokens and counters. */
record ConformanceTarget(PaymentProcessor processor, Currency currency, PaymentMethod approve, PaymentMethod insufficientFunds,
                         Supplier<List<String>> drainEvents, IntSupplier vendorCalls, IntSupplier chargesCreated) {}

record ConformanceReport(String adapter, int checks, List<String> failures) {
    boolean passed() { return failures.isEmpty(); }
}

final class ProcessorConformance {
    private final ConformanceTarget t;
    private final Map<String, PaymentEvent> events = new HashMap<>();
    private final List<String> failures = new ArrayList<>();
    private int checks;
    private int seq;

    private ProcessorConformance(ConformanceTarget target) { this.t = target; }

    /** Runs every contract check once against a fresh target. Empty failures means conformant. */
    static ConformanceReport run(ConformanceTarget target) {
        ProcessorConformance c = new ProcessorConformance(target);
        c.runAll();
        return new ConformanceReport(target.processor().name(), c.checks, List.copyOf(c.failures));
    }

    private void expect(String name, boolean ok) {
        checks++;
        if (!ok) failures.add(t.processor().name() + ": " + name);
    }

    private ChargeRequest request(PaymentMethod method, String amount, boolean capture) {
        return new ChargeRequest("conf-" + t.processor().name() + "-" + (++seq), Money.parse(amount, t.currency()), method, capture);
    }

    /** Synchronous result, or the translated event if the adapter said PENDING. */
    private PaymentStatus finalStatus(PaymentResult r) {
        if (r.status() != PaymentStatus.PENDING) return r.status();
        for (String raw : t.drainEvents().get()) t.processor().translateEvent(raw).ifPresent(e -> events.put(e.processorRef(), e));
        PaymentEvent e = events.get(r.processorRef());
        return e == null ? PaymentStatus.PENDING : e.status();
    }

    private DeclineReason finalReason(PaymentResult r) {
        if (r.status() != PaymentStatus.PENDING) return r.reason();
        finalStatus(r);
        PaymentEvent e = events.get(r.processorRef());
        return e == null ? DeclineReason.NONE : e.reason();
    }

    private boolean throwsUnsupported(Runnable action, int[] vendorCallsDelta) {
        int before = t.vendorCalls().getAsInt();
        try {
            action.run();
            return false;
        } catch (UnsupportedCapabilityException e) {
            vendorCallsDelta[0] = t.vendorCalls().getAsInt() - before;
            return true;
        }
    }

    private void runAll() {
        PaymentProcessor p = t.processor();
        Capabilities caps = p.capabilities();
        boolean async = caps.supports(Capability.ASYNC_CONFIRMATION);

        // 1. Approval: synchronous success, or PENDING only if the adapter declares async confirmation.
        ChargeRequest ok = request(t.approve(), "45.00", true);
        PaymentResult approved = p.charge(ok);
        expect("approve returns a processor reference", approved.processorRef() != null && !approved.processorRef().isEmpty());
        expect("approve echoes the exact amount", approved.amount().equals(ok.amount()));
        expect("approve status is SUCCEEDED, or PENDING iff ASYNC_CONFIRMATION",
                async ? approved.status() == PaymentStatus.PENDING : approved.status() == PaymentStatus.SUCCEEDED);
        expect("approve eventually SUCCEEDED", finalStatus(approved) == PaymentStatus.SUCCEEDED);

        // 2. Insufficient funds normalizes to one reason and one retry advice, whatever the rail calls it.
        PaymentResult nsf = p.charge(request(t.insufficientFunds(), "45.00", true));
        PaymentStatus nsfFinal = finalStatus(nsf);
        expect("insufficient funds ends DECLINED or FAILED", nsfFinal == PaymentStatus.DECLINED || nsfFinal == PaymentStatus.FAILED);
        expect("insufficient funds maps to INSUFFICIENT_FUNDS", finalReason(nsf) == DeclineReason.INSUFFICIENT_FUNDS);
        expect("insufficient funds advises RETRY_LATER", finalReason(nsf).advice() == RetryAdvice.RETRY_LATER);

        // 3. Idempotency: the same reference twice is one charge at the vendor, one processor reference.
        ChargeRequest dup = request(t.approve(), "12.34", true);
        int createdBefore = t.chargesCreated().getAsInt();
        PaymentResult first = p.charge(dup);
        PaymentResult second = p.charge(dup);
        expect("same reference returns the same processor reference", first.processorRef().equals(second.processorRef()));
        expect("same reference creates exactly one vendor charge", t.chargesCreated().getAsInt() - createdBefore == 1);

        // 4. Currency honesty: an undeclared currency is refused before the vendor is called.
        int[] delta = {-1};
        ChargeRequest chf = new ChargeRequest("conf-chf-" + p.name(), Money.parse("10.00", Currency.CHF), t.approve(), true);
        expect("undeclared currency throws UnsupportedCapabilityException", throwsUnsupported(() -> p.charge(chf), delta));
        expect("undeclared currency never reaches the vendor", delta[0] == 0);

        // 5. Separate capture: works if declared, refused before the vendor if not.
        if (caps.supports(Capability.SEPARATE_CAPTURE)) {
            PaymentResult auth = p.charge(request(t.approve(), "100.00", false));
            expect("authorize-only returns AUTHORIZED", auth.status() == PaymentStatus.AUTHORIZED);
            Money partial = Money.parse("60.00", t.currency());
            if (caps.supports(Capability.PARTIAL_CAPTURE)) {
                PaymentResult cap = p.capture(auth.processorRef(), partial);
                expect("partial capture SUCCEEDED for the partial amount", cap.status() == PaymentStatus.SUCCEEDED && cap.amount().equals(partial));
            } else {
                int[] d = {-1};
                expect("undeclared partial capture is refused", throwsUnsupported(() -> p.capture(auth.processorRef(), partial), d));
                expect("undeclared partial capture never reaches the vendor", d[0] == 0);
                PaymentResult cap = p.capture(auth.processorRef(), auth.amount());
                expect("full capture SUCCEEDED", cap.status() == PaymentStatus.SUCCEEDED);
            }
        } else {
            int[] d = {-1};
            expect("undeclared separate capture is refused", throwsUnsupported(() -> p.charge(request(t.approve(), "100.00", false)), d));
            expect("undeclared separate capture never reaches the vendor", d[0] == 0);
        }

        // 6. Refunds: a declared partial refund works for exactly the requested amount; over-refund is declined.
        if (caps.supports(Capability.PARTIAL_REFUND)) {
            Money half = Money.parse("22.50", t.currency());
            PaymentResult refund = p.refund(approved.processorRef(), half);
            expect("partial refund is accepted", refund.status() == PaymentStatus.SUCCEEDED || refund.status() == PaymentStatus.PENDING);
            expect("partial refund is for the requested amount", refund.amount().equals(half));
            PaymentResult over = p.refund(approved.processorRef(), Money.parse("30.00", t.currency()));
            expect("refund beyond the captured amount is DECLINED", over.status() == PaymentStatus.DECLINED);
        }

        // 7. Events: unknown or foreign notifications are ignored, never thrown on.
        expect("unknown event is ignored", p.translateEvent("totally|unrelated|payload").isEmpty());
        expect("empty event is ignored", p.translateEvent("").isEmpty());

        // 8. Declines always carry a reason and the raw code, so support can trace them without product code branching on it.
        expect("decline carries a non-NONE reason", nsf.status() == PaymentStatus.PENDING || nsf.reason() != DeclineReason.NONE);
    }
}

public final class Main {
    record Rig(PaymentService service, FakeCardNetworkApi card, FakeBankDebitApi bank, FakeWalletApi wallet) {
        static Rig create() {
            FakeCardNetworkApi card = new FakeCardNetworkApi();
            FakeBankDebitApi bank = new FakeBankDebitApi();
            FakeWalletApi wallet = new FakeWalletApi();
            PaymentService service = new PaymentService();
            service.register(new CardAdapter(card));
            service.register(new BankDebitAdapter(bank));
            service.register(new WalletAdapter(wallet));
            return new Rig(service, card, bank, wallet);
        }
    }

    static List<ConformanceTarget> targets() {
        FakeCardNetworkApi card = new FakeCardNetworkApi();
        FakeBankDebitApi bank = new FakeBankDebitApi();
        FakeWalletApi wallet = new FakeWalletApi();
        return List.of(
                new ConformanceTarget(new CardAdapter(card), Currency.USD,
                        new CardMethod("tok_visa_4242", "visa"), new CardMethod("tok_insufficient", "visa"),
                        List::of, card::vendorCalls, card::chargesCreated),
                new ConformanceTarget(new BankDebitAdapter(bank), Currency.USD,
                        new BankDebitMethod("acct_ok", "mandate_1"), new BankDebitMethod("acct_nsf", "mandate_2"),
                        bank::drainEvents, bank::vendorCalls, bank::chargesCreated),
                new ConformanceTarget(new WalletAdapter(wallet), Currency.USD,
                        new WalletMethod("wal_ok", "wallet"), new WalletMethod("wal_nsf", "wallet"),
                        wallet::drainEvents, wallet::vendorCalls, wallet::chargesCreated));
    }

    static void show(String label, PaymentResult r) {
        System.out.printf("%-34s -> %-15s reason=%-24s advice=%-19s raw=%s%n",
                label, r.status(), r.reason(), r.advice(), r.rawCode().isEmpty() ? "-" : r.rawCode());
    }

    public static void main(String[] args) {
        Rig rig = Rig.create();
        PaymentService svc = rig.service();

        System.out.println("== One interface, three rails: the same 45.00 USD charge ==");
        show("card  tok_visa_4242", svc.pay(new ChargeRequest("order-1", Money.parse("45.00", Currency.USD), new CardMethod("tok_visa_4242", "visa"), true)));
        PaymentResult ach = svc.pay(new ChargeRequest("order-2", Money.parse("45.00", Currency.USD), new BankDebitMethod("acct_ok", "mandate_7"), true));
        show("bank  acct_ok", ach);
        show("wallet wal_ok", svc.pay(new ChargeRequest("order-3", Money.parse("45.00", Currency.USD), new WalletMethod("wal_ok", "wallet"), true)));

        System.out.println("\n== Three vendors say 'no money' three ways; product code sees one reason ==");
        show("card  ISO 8583 code 51", svc.pay(new ChargeRequest("order-4", Money.parse("45.00", Currency.USD), new CardMethod("tok_insufficient", "visa"), true)));
        PaymentResult achNsf = svc.pay(new ChargeRequest("order-5", Money.parse("45.00", Currency.USD), new BankDebitMethod("acct_nsf", "mandate_8"), true));
        show("bank  submitted (outcome later)", achNsf);
        for (String raw : rig.bank().drainEvents()) {
            System.out.println("  raw bank event: " + raw);
            svc.onRawEvent(MethodType.BANK_DEBIT, raw);
        }
        show("bank  after return file", svc.lookup(achNsf.processorRef()).orElseThrow());
        show("bank  order-2 after settlement", svc.lookup(ach.processorRef()).orElseThrow());
        show("wallet INSUFFICIENT_FUNDS", svc.pay(new ChargeRequest("order-6", Money.parse("45.00", Currency.USD), new WalletMethod("wal_nsf", "wallet"), true)));

        System.out.println("\n== Customer action: SCA soft decline and wallet approval, one status ==");
        show("card  ISO 8583 code 1A", svc.pay(new ChargeRequest("order-7", Money.parse("45.00", Currency.USD), new CardMethod("tok_sca_required", "visa"), true)));
        PaymentResult action = svc.pay(new ChargeRequest("order-8", Money.parse("45.00", Currency.USD), new WalletMethod("wal_action", "wallet"), true));
        show("wallet PAYER_ACTION_REQUIRED", action);
        rig.wallet().payerApproves(action.processorRef());
        for (String raw : rig.wallet().drainEvents()) svc.onRawEvent(MethodType.WALLET, raw);
        show("wallet after payer approves", svc.lookup(action.processorRef()).orElseThrow());

        System.out.println("\n== Capability flags refuse before the vendor is called ==");
        int before = rig.bank().vendorCalls();
        try {
            svc.pay(new ChargeRequest("order-9", Money.parse("45.00", Currency.USD), new BankDebitMethod("acct_ok", "mandate_9"), false));
        } catch (UnsupportedCapabilityException e) {
            System.out.println("authorize-only on bank debit: " + e.getMessage() + " (vendor calls +" + (rig.bank().vendorCalls() - before) + ")");
        }
        try {
            svc.pay(new ChargeRequest("order-10", Money.parse("4500", Currency.JPY), new WalletMethod("wal_ok", "wallet"), true));
        } catch (UnsupportedCapabilityException e) {
            System.out.println("JPY on wallet: " + e.getMessage());
        }
        PaymentResult jpy = svc.pay(new ChargeRequest("order-11", Money.parse("4500", Currency.JPY), new CardMethod("tok_visa_4242", "visa"), true));
        System.out.println("JPY on card: " + jpy.status() + " " + jpy.amount() + " (sent as minor=4500, currency=392)");

        System.out.println("\n== Conformance suite, run against every adapter ==");
        for (ConformanceTarget target : targets()) {
            ConformanceReport report = ProcessorConformance.run(target);
            System.out.printf("%-10s %2d checks  %-62s %s%n", report.adapter(), report.checks(), target.processor().capabilities().flags(),
                    report.passed() ? "PASS" : "FAIL " + report.failures());
        }
    }
}
