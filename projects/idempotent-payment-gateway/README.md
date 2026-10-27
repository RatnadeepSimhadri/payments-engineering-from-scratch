# Idempotent Payment Gateway

**Level 2 · Builder · 4 stages · about 10 hours · Java, Python or Go**

A charge API that survives client retries, flaky processors, and duplicate webhooks without ever charging twice.

## Overview

Every payment company has the same core problem: the network is at-least-once, and money must move exactly once. A merchant's client times out and retries. Your call to the card processor times out and you retry. Your webhook to the merchant times out and you retry. Each of those retries is a potential double charge, double decline, or double "order paid" email unless something in the middle remembers what already happened.

This project builds that middle, in memory, on an injected clock and a seeded random source, so every schedule, every signature and every decision is reproducible and testable. By the end you have a `Gateway` where the same `ChargeRequest` can be sent any number of times and the processor is called for at most one attempt sequence, plus a webhook pipeline where the same event can be delivered any number of times and the merchant processes it once.

## What you will build

| Stage | You add | The mechanism | Difficulty | Hours |
|---|---|---|---|---|
| 01 | `IdempotencyStore`, `ChargeRequest.fingerprint()` | Keys scoped per merchant, SHA-256 over a canonical request, `NEW` / `REPLAY` / `CONFLICT` / `IN_PROGRESS`, TTL on an injected clock | starter | 2 |
| 02 | `Payment`, `PaymentStateMachine` | One transition table (`CREATED -> PROCESSING -> SUCCEEDED` or `FAILED`, `CREATED -> CANCELED`, `SUCCEEDED -> REFUNDED`), versioned history, partial refunds in integer minor units | core | 2 |
| 03 | `BackoffPolicy`, `RetryBudget`, `CircuitBreaker`, `RetryingClient`, `Gateway` | Retry only `UNAVAILABLE`, capped exponential backoff with full jitter from a seeded `Rng`, a token budget, a `CLOSED / OPEN / HALF_OPEN` breaker, and the HTTP-shaped gateway (201 / 402 / 409 / 422 / 503) | core | 3 |
| 04 | `WebhookSigner`, `WebhookVerifier`, `WebhookDispatcher`, `WebhookConsumer` | HMAC-SHA256 over `timestamp.body`, verification inside a tolerance window with a constant-time compare, at-least-once delivery on a retry schedule with dead-lettering, consumer dedupe by event id | stretch | 3 |

Each stage's `docs/en.md` has a worked example with real numbers you can predict before coding, the exact Java, Python and Go signatures, a table of what must fail and how, and check-yourself questions. Later stages add files to the same workspace; nothing from an earlier stage changes shape, and the grader re-runs every earlier stage so a regression shows up at once.

## Prerequisites

Read (or at least run) these lessons first; the project assumes their vocabulary:

- `phases/05-distributed-foundations/09-idempotency-keys` — Idempotency Keys: Exactly-Once Effects on At-Least-Once Networks
- `phases/05-distributed-foundations/10-retries-backoff-and-jitter` — Retries, Exponential Backoff, and Jitter
- `phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads` — Circuit Breakers and Bulkheads
- `phases/12-payment-systems-design/01-idempotent-payment-api` — Designing an Idempotent Payment API

The `Money` type (integer minor units plus an ISO 4217 currency) is copied into the starter from `phases/10-money-and-ledgers/01-representing-money`; you use it, you do not rewrite it.

Tooling: Java 21 (`javac`, `java`), Python 3.11+, or Go 1.22+, plus Python 3 for the grader script. No build tool, no dependencies beyond the standard library (`java.security.MessageDigest` and `javax.crypto.Mac` in Java; `hashlib` and `hmac` in Python; `crypto/sha256` and `crypto/hmac` in Go).

## Commands

All commands run from the repository root.

```bash
# create a workspace from the stage 1 starter
python3 scripts/project_test.py idempotent-payment-gateway --lang java --init my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --init my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --init my-gateway

# grade stages 1..N against your workspace (earlier stages are re-run every time)
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 1 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 1 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 1 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 2 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 2 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 2 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 3 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 3 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 3 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang java --stage 4 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 4 --path my-gateway
python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 4 --path my-gateway

# everything, with a completion report
python3 scripts/project_test.py idempotent-payment-gateway --lang java --all --path my-gateway --report gateway-report.json
python3 scripts/project_test.py idempotent-payment-gateway --lang python --all --path my-gateway --report gateway-report.json
python3 scripts/project_test.py idempotent-payment-gateway --lang go --all --path my-gateway --report gateway-report.json

# run your own workspace's demo
cd my-gateway && javac -d .out *.java && java -cp .out Main   # Java
cd my-gateway && python3 main.py                                # Python
cd my-gateway && go run .                                       # Go
```

Maintainers verify the reference solution with:

```bash
python3 scripts/project_test.py idempotent-payment-gateway --lang java --all --solution
python3 scripts/project_test.py idempotent-payment-gateway --lang python --all --solution
python3 scripts/project_test.py idempotent-payment-gateway --lang go --all --solution
```

The grader copies your sources (`*.java` except `*Test.java`, `*.py` except `test_*.py`, or `*.go` except `*_test.go` plus `go.mod`) and the stage's tests (`StageNNTest.java`, `test_stage_NN.py`, or `stageNN_test.go`) into a temporary directory and runs them (`javac` + `java`, `python3 -m unittest -v`, or `go test ./...`). A failing run prints every failed assertion by name and exits 1. Assertion counts: 43, 46, 59, 50 in Java and Python; 43, 49, 60, 52 in Go.

## What you keep

- **An idempotency store you can explain in an interview and port to a database in an afternoon.** The `(merchant, key)` scoping, the fingerprint-before-state decision order, lazy TTL, and `release` for non-replayable failures are the parts people get wrong.
- **A payment state machine with a table, not `if`s.** Terminal states, versioned history, partial refunds that stay `SUCCEEDED` until the last minor unit, and the guarantee that a rejected transition changes nothing.
- **A retry client whose schedule you can predict.** `ceilingFor`, full jitter from a seeded generator, a retry budget, and a breaker whose OPEN → HALF_OPEN move is a clock comparison rather than a timer thread.
- **A webhook producer and consumer pair** with the same header format as Stripe and GitHub, a tolerance window, constant-time comparison, a retry schedule with dead letters, and consumer-side dedupe.
- **Deterministic tests for all of it**: `FakeClock`, `Rng`, `ScriptedProcessor("UUA")` and `ScriptedEndpoint("FFS")` are reusable test doubles for any payments code you write afterwards.

## Run with your own inputs

`Main.java` (or `main()` in `main.py` / `main.go`) in your workspace is yours. The reference `Main` prints one trace per stage; you can drive your own scenarios the same way (Python: same names in snake_case, `[60_000, 300_000]` for the delay list, `gateway.payment("pay_1")` returns the payment or `None`):

```java
FakeClock clock = new FakeClock(1_700_000_000_000L);
IdempotencyStore store = new IdempotencyStore(clock, 24L * 60 * 60 * 1000);

// Stage 3: a processor that is down for three calls, then approves, behind a seeded schedule
RetryingClient client = new RetryingClient(
        new ScriptedProcessor("UUUA"),
        new BackoffPolicy(100, 2_000, 5),
        new RetryBudget(10, 10),
        new CircuitBreaker(clock, 3, 30_000, 1),
        new Rng(2024), clock);
Gateway gateway = new Gateway(store, client, clock);

ChargeRequest req = new ChargeRequest("m_42", "order-9001", Money.parse("1250.00", Currency.INR), "tok_upi", "invoice 9001");
System.out.println(gateway.charge(req));          // 201 or 503 depending on the breaker threshold you chose
System.out.println(gateway.charge(req));          // replay, or a fresh attempt if the key was released
gateway.payment("pay_1").ifPresent(p -> p.history().forEach(System.out::println));

// Stage 4: deliver to a merchant that is down for the first two attempts
WebhookConsumer merchant = new WebhookConsumer(new WebhookVerifier("whsec_mine", 5 * 60_000, clock));
WebhookDispatcher dispatcher = new WebhookDispatcher(new WebhookSigner("whsec_mine"), new ConsumerEndpoint(merchant), clock, new long[] {60_000, 300_000, 3_600_000});
gateway.attachWebhooks(dispatcher);
gateway.charge(new ChargeRequest("m_42", "order-9002", Money.parse("99.00", Currency.USD), "tok_visa", "invoice 9002"));
while (!dispatcher.pending().isEmpty()) { dispatcher.runDue(); clock.advance(60_000); }
System.out.println(merchant.processed());
```

Things worth trying once the grader is green:

- Change the `ScriptedProcessor` script and the breaker threshold and predict, before running, whether the charge returns 201 or 503 and how many processor calls are made.
- Change the `Rng` seed and watch only the `waitedMillis` column change; the decisions should not.
- Give the `WebhookVerifier` a different secret from the signer and confirm every delivery is dead-lettered after the full schedule.
- Try to charge the same key with a different currency and the same minor-unit count (`19.99 USD` vs `19.99 EUR`): it must be a `CONFLICT`, because the currency code is in the canonical form.

## Layout

```text
projects/idempotent-payment-gateway/
  project.json
  README.md
  stages/
    01-idempotency-store/        docs/en.md  starter/java/{Main,Money,Clock,Rng,Idempotency}.java  starter/python/main.py  starter/go/{go.mod,main,money,clock,rng,idempotency}.go  tests/{java,python,go}/
    02-payment-state-machine/    docs/en.md  starter/STAGE.md  tests/{java,python,go}/
    03-retries-and-circuit-breaker/  docs/en.md  starter/STAGE.md  tests/{java,python,go}/
    04-webhook-dispatch/         docs/en.md  starter/STAGE.md  tests/{java,python,go}/
  solution/java/                 Main, Money, Clock, Rng, Idempotency, Payments, Resilience, Webhooks
  solution/python/               main.py (the same design in one module)
  solution/go/                   the same eight files as Java, lower-case, in one package main
```

You never need `solution/`. The `build-project` skill reads it only to diagnose, and shows at most one function after two honest attempts.
