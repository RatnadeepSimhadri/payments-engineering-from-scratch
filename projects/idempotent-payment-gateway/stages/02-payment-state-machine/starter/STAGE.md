# Stage 02 starter

Continue editing the workspace created at stage 1 (`--init`). There is nothing to copy: add a new file
`Payments.java` (no package) next to the Stage 01 code. The Python and Go ports have the same surface in snake_case and exported CamelCase (signatures below).

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

## New signatures (Python)

Keep everything in the one `main.py` the stage-1 starter gave you; the names below are the Java ones in snake_case.

```python
class PaymentState(Enum):     # CREATED, PROCESSING, SUCCEEDED, FAILED, CANCELED, REFUNDED
    def is_terminal(self) -> bool: ...                 # FAILED, CANCELED, REFUNDED
    def wire(self) -> str: ...                         # self.name.lower(): "succeeded"

class IllegalTransitionError(RuntimeError):
    def __init__(self, payment_id: str, from_state: PaymentState, to_state: PaymentState) -> None: ...

@dataclass(frozen=True)
class Transition:             # from_state: PaymentState, to_state: PaymentState, reason: str, at_millis: int

class PaymentStateMachine:
    TABLE: dict[PaymentState, frozenset[PaymentState]]
    @staticmethod
    def can_transition(from_state: PaymentState, to_state: PaymentState) -> bool: ...
    @staticmethod
    def next_states(from_state: PaymentState) -> frozenset[PaymentState]: ...

class Payment:
    def __init__(self, id: str, amount: Money, clock: Clock) -> None: ...   # blank id / non-positive amount -> ValueError
    def id(self) -> str; amount() -> Money; state() -> PaymentState; captured() -> Money; refunded() -> Money
    def refundable(self) -> Money; processor_reference() -> str; version() -> int; history() -> tuple[Transition, ...]
    def start_processing(self) -> None; succeed(reference: str); fail(reason: str); cancel(reason: str)
    def refund(self, refund_amount: Money, reason: str) -> Money: ...      # returns what is still refundable
```

## New signatures (Go)

Add `payments.go` next to the Stage 01 files; same `package main`.

```go
type PaymentState int
const ( Created PaymentState = iota; Processing; Succeeded; Failed; Canceled; Refunded )
func (s PaymentState) IsTerminal() bool
func (s PaymentState) Wire() string                             // "succeeded"

var ErrIllegalTransition = fmt.Errorf("%w: illegal transition", ErrIllegalState)
var ErrInvalidRefund = errors.New("invalid refund")              // non-positive, or more than refundable
type Transition struct{ From, To PaymentState; Reason string; AtMillis int64 }
func CanTransition(from, to PaymentState) bool                  // the table is a package-level map
func NextStates(from PaymentState) []PaymentState               // a copy

func NewPayment(id string, amount Money, clock Clock) (*Payment, error)   // blank id / non-positive amount -> error
func (p *Payment) ID() string; Amount() Money; State() PaymentState; Captured() Money; Refunded() Money
func (p *Payment) Refundable() Money; ProcessorReference() string; Version() int64; History() []Transition   // a copy
func (p *Payment) StartProcessing() error; Succeed(reference string) error; Fail(reason string) error; Cancel(reason string) error
func (p *Payment) Refund(refundAmount Money, reason string) (Money, error)   // ErrIllegalTransition / ErrInvalidRefund / ErrCurrencyMismatch
```

`Money.Compare` panics on a currency mismatch (a programming error in the lesson's Money), so `Refund` checks `refundAmount.Currency != remaining.Currency` itself and returns `ErrCurrencyMismatch` before comparing.
