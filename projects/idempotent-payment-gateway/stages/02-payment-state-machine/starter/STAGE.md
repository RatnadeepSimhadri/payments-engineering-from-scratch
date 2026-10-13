# Stage 02 starter

Continue editing the workspace created at stage 1 (`--init`). There is nothing to copy: add a new file
`Payments.java` (no package) next to the Stage 01 code. Python and Go ports follow later; this pass is Java only.

The grader runs the Stage 01 tests again before the Stage 02 tests, so nothing from Stage 01 may change shape.

## New signatures (Java)

| Concept | Signature |
|---|---|
| States | `enum PaymentState { CREATED, PROCESSING, SUCCEEDED, FAILED, CANCELED, REFUNDED }` with `boolean isTerminal()`, `String wire()` |
| Rejected move | `final class IllegalTransitionException extends IllegalStateException` with constructor `(String paymentId, PaymentState from, PaymentState to)` |
| Log entry | `record Transition(PaymentState from, PaymentState to, String reason, long atMillis)` |
| Table | `PaymentStateMachine.canTransition(PaymentState from, PaymentState to)`, `Set<PaymentState> nextStates(PaymentState from)` (static) |
| Payment | `new Payment(String id, Money amount, Clock clock)` |
| Reads | `id()`, `amount()`, `state()`, `captured()`, `refunded()`, `refundable()`, `processorReference()`, `version()`, `List<Transition> history()` |
| Moves | `startProcessing()`, `succeed(String reference)`, `fail(String reason)`, `cancel(String reason)` |
| Refund | `Money refund(Money amount, String reason)` returns what is still refundable |

Transition table: `CREATED -> {PROCESSING, CANCELED}`, `PROCESSING -> {SUCCEEDED, FAILED}`, `SUCCEEDED -> {REFUNDED}`,
and nothing leaves `FAILED`, `CANCELED`, `REFUNDED`. `version` starts at 1 and bumps once per recorded transition
(a partial refund records `SUCCEEDED -> SUCCEEDED`). Read `docs/en.md` for the worked example and the failure rules.
