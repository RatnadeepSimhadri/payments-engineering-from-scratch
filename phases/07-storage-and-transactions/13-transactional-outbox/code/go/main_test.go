// Tests for phases/07-storage-and-transactions/13-transactional-outbox. Run: go test ./...
package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// world is a fresh world: one store, one broker, an outbox service, a relay with the given batch size, a ledger consumer.
type world struct {
	clock  *SimClock
	store  *Store
	broker *Broker
	svc    *OutboxPaymentService
	relay  *OutboxRelay
	ledger *LedgerConsumer
}

func newWorld(batchSize int) *world {
	w := &world{clock: NewSimClock(0), store: NewStore(), broker: NewBroker()}
	w.svc = NewOutboxPaymentService(w.store, w.clock)
	w.relay = NewOutboxRelay(w.store, w.broker, batchSize)
	w.ledger = NewLedgerConsumer(w.broker)
	return w
}

func (w *world) pay(id, merchant, amount string) Payment {
	return payment(id, merchant, amount, w.clock)
}

func accept(t *testing.T, svc *OutboxPaymentService, p Payment) {
	t.Helper()
	if err := svc.Accept(p); err != nil {
		t.Fatalf("accept %s: %v", p.PaymentID, err)
	}
}

func drain(t *testing.T, r *OutboxRelay, max int) int {
	t.Helper()
	n, err := r.Drain(max)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	return n
}

func poll(t *testing.T, r *OutboxRelay) int {
	t.Helper()
	n, err := r.Poll()
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return n
}

func consume(t *testing.T, c *LedgerConsumer) int {
	t.Helper()
	n, err := c.Consume()
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	return n
}

func TestStoreTransactionIsAtomic(t *testing.T) {
	store := NewStore()
	tx := store.Begin().InsertPayment(Payment{"p1", "m", usd("1.00"), 0}).InsertOutbox("evt-p1", "m", "PaymentAccepted", "x", 0)
	assert(t, "nothing visible before commit", store.PaymentCount() == 0 && len(store.OutboxRows()) == 0)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assert(t, "both rows visible after commit", store.PaymentCount() == 1 && len(store.OutboxRows()) == 1)
	assert(t, "outbox seq assigned at commit", store.OutboxRows()[0].Seq == 1 && !store.OutboxRows()[0].Published)
	assert(t, "closed transaction cannot be reused", errors.Is(tx.Commit(), ErrTxClosed))
	assert(t, "closed transaction cannot roll back either", errors.Is(tx.Rollback(), ErrTxClosed))
	tx2 := store.Begin().InsertPayment(Payment{"p2", "m", usd("2.00"), 0}).InsertOutbox("evt-p2", "m", "PaymentAccepted", "y", 0)
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
	assert(t, "rollback drops payment and outbox row together", store.PaymentCount() == 1 && len(store.OutboxRows()) == 1 && store.Rollbacks == 1)
	dup := store.Begin().InsertPayment(Payment{"p1", "m", usd("1.00"), 0})
	assert(t, "duplicate payment id is rejected at insert", errors.Is(dup.Err(), ErrDuplicatePayment))
	assert(t, "a transaction with a rejected insert cannot commit", errors.Is(dup.Commit(), ErrDuplicatePayment) && store.Commits == 1)
	assert(t, "outbox row prints with its state", store.OutboxRows()[0].String() == "#1 evt-p1 PaymentAccepted x [pending]")
}

func TestDualWriteCommitThenPublish(t *testing.T) {
	clock := NewSimClock(0)
	store := NewStore()
	broker := NewBroker()
	svc := NewDualWritePaymentService(store, broker)
	broker.Down = true
	assert(t, "publish fails when the broker is down", errors.Is(svc.Accept(payment("d1", "acme", "20.00", clock)), ErrBrokerUnavailable))
	assert(t, "row committed without an event", store.PaymentCount() == 1 && broker.Size() == 0)
	broker.Down = false
	svc.CrashPoint = AfterCommitBeforePublish
	err := svc.Accept(payment("d2", "acme", "5.00", clock))
	assert(t, "crash after commit", errors.Is(err, ErrCrash) && err.Error() == "crash after commit, before publish")
	assert(t, "second orphan row, still no events", store.PaymentCount() == 2 && broker.Size() == 0)
	ledger := NewLedgerConsumer(broker)
	consume(t, ledger)
	assert(t, "ledger disagrees with store forever", ledger.Balance("acme") == usd("0.00"))
}

func TestDualWritePublishThenCommit(t *testing.T) {
	clock := NewSimClock(0)
	store := NewStore()
	broker := NewBroker()
	svc := NewDualWritePaymentService(store, broker)
	svc.PublishFirst = true
	svc.CrashPoint = AfterPublishBeforeCommit
	assert(t, "crash after publish", errors.Is(svc.Accept(payment("g1", "acme", "12.50", clock)), ErrCrash))
	ledger := NewLedgerConsumer(broker)
	consume(t, ledger)
	assert(t, "event without a row", store.PaymentCount() == 0 && broker.Size() == 1)
	assert(t, "ledger credits a payment that does not exist", ledger.Balance("acme") == usd("12.50"))
}

func TestOutboxSurvivesBrokerOutage(t *testing.T) {
	w := newWorld(10)
	w.broker.Down = true
	accept(t, w.svc, w.pay("o1", "acme", "20.00"))
	accept(t, w.svc, w.pay("o2", "acme", "35.00"))
	assert(t, "writes succeed while broker is down", w.store.PaymentCount() == 2 && w.store.UnpublishedCount() == 2)
	assert(t, "service never touched the broker", w.broker.PublishAttempts == 0)
	assert(t, "relay delivers nothing while broker is down", drain(t, w.relay, 5) == 0 && w.relay.FailedAttempts >= 1)
	assert(t, "rows stay pending after a failed poll", w.store.UnpublishedCount() == 2)
	w.broker.Down = false
	assert(t, "relay delivers both once broker is back", drain(t, w.relay, 5) == 2 && w.store.UnpublishedCount() == 0)
	consume(t, w.ledger)
	assert(t, "ledger matches store", w.ledger.Balance("acme") == usd("55.00"))
	assert(t, "relay with nothing pending is a no-op", poll(t, w.relay) == 0)
}

func TestRelayCrashBetweenPublishAndMark(t *testing.T) {
	w := newWorld(10)
	accept(t, w.svc, w.pay("r1", "globex", "100.00"))
	accept(t, w.svc, w.pay("r2", "globex", "1.00"))
	w.relay.CrashPoint = AfterPublishBeforeMark
	_, err := w.relay.Poll()
	assert(t, "relay crashes after first publish", errors.Is(err, ErrCrash))
	var crash *CrashError
	assert(t, "crash names the event", errors.As(err, &crash) && crash.Where == "after publish of evt-r1, before mark")
	assert(t, "published but not marked", w.broker.Size() == 1 && w.store.UnpublishedCount() == 2)
	drain(t, w.relay, 5)
	assert(t, "at-least-once: three messages for two events", w.broker.Size() == 3 && w.store.UnpublishedCount() == 0)
	assert(t, "the duplicate carries the same eventId", w.broker.Messages()[0].EventID == w.broker.Messages()[1].EventID)
	applied := consume(t, w.ledger)
	assert(t, "consumer applies each eventId once", applied == 2 && w.ledger.DuplicatesIgnored == 1)
	assert(t, "balance is not double-counted", w.ledger.Balance("globex") == usd("101.00"))
	assert(t, "consumer offset advanced past every message", w.ledger.Offset() == 3)
	assert(t, "re-consuming applies nothing new", consume(t, w.ledger) == 0 && w.ledger.Balance("globex") == usd("101.00"))
}

func TestOrdering(t *testing.T) {
	w := newWorld(2)
	for i := 1; i <= 5; i++ {
		accept(t, w.svc, w.pay("s"+strconv.Itoa(i), "acme", strconv.Itoa(i)+".00"))
	}
	polls := 0
	for w.store.UnpublishedCount() > 0 {
		poll(t, w.relay)
		polls++
	}
	assert(t, "batch size 2 needs three polls for five rows", polls == 3)
	var order strings.Builder
	for _, m := range w.broker.Messages() {
		order.WriteString(m.EventID + ",")
	}
	assert(t, "broker order equals commit order", order.String() == "evt-s1,evt-s2,evt-s3,evt-s4,evt-s5,")
	assert(t, "unpublished query respects the limit", len(newWorld(10).store.Unpublished(3)) == 0)
	w2 := newWorld(10)
	for i := 1; i <= 5; i++ {
		accept(t, w2.svc, w2.pay("t"+strconv.Itoa(i), "acme", "1.00"))
	}
	assert(t, "unpublished returns at most limit rows in seq order", len(w2.store.Unpublished(3)) == 3 && w2.store.Unpublished(3)[2].Seq == 3)
}

func TestBrokerFailureMidBatchKeepsOrder(t *testing.T) {
	w := newWorld(10)
	accept(t, w.svc, w.pay("b1", "acme", "1.00"))
	accept(t, w.svc, w.pay("b2", "acme", "2.00"))
	w.broker.Down = true
	assert(t, "poll publishes nothing when the first publish fails", poll(t, w.relay) == 0 && w.store.UnpublishedCount() == 2)
	w.broker.Down = false
	poll(t, w.relay)
	assert(t, "after recovery b1 precedes b2", w.broker.Messages()[0].EventID == "evt-b1" && w.broker.Messages()[1].EventID == "evt-b2")
}

func TestRollbackPath(t *testing.T) {
	w := newWorld(10)
	if err := w.svc.AcceptThenFail(w.pay("x1", "acme", "999.00")); err != nil {
		t.Fatal(err)
	}
	assert(t, "failed request leaves no payment and no outbox row", w.store.PaymentCount() == 0 && len(w.store.OutboxRows()) == 0)
	assert(t, "relay has nothing to publish for a rolled-back request", poll(t, w.relay) == 0 && w.broker.Size() == 0)
}

func TestMoneyAndPayload(t *testing.T) {
	assert(t, "payload carries the exact amount", strings.HasSuffix(PayloadFor(Payment{"p", "m", usd("0.05"), 0}), "0.05 USD"))
	m, err := ParseMoney("1234.56 USD")
	assert(t, "parseMoney round-trips minor units", err == nil && m.MinorUnits == 123456)
	_, err = ParseMoney("12x USD")
	assert(t, "parseMoney rejects garbage", err != nil)
	_, err = usd("1.00").Plus(OfMinor(100, Currency{"EUR", 2}))
	assert(t, "currency mismatch is rejected", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "overflow is an error, not a wrap", errors.Is(err, ErrOverflow))
}
