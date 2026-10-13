// Tests for phases/05-distributed-foundations/09-idempotency-keys. Run: go test -race ./...
// Same cases as MainTest.java and test_main.py.
package main

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
)

const expectedFingerprint = "795fe63bfb540da3cf414455d7494e879034c285ddc8d254e3b0ec2c396cb96f"

func request(key *string, cents int64) ChargeRequest {
	return mustRequest("acct_1", key, OfMinor(cents, USD), "tok_visa_4242", "Order 8812")
}

type fixture struct {
	clock     *FakeClock
	processor *FakeProcessor
	store     *IdempotencyStore
	api       *PaymentsApi
}

func newFixture(t *testing.T) fixture {
	clock := NewFakeClock(1_000)
	processor := &FakeProcessor{}
	store, err := NewIdempotencyStore(clock, DayMs, 30_000)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{clock, processor, store, &PaymentsApi{store, processor}}
}

func mustCharge(t *testing.T, api *PaymentsApi, r ChargeRequest) Response {
	resp, err := api.Charge(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

// blockingProcessor blocks inside Charge until released, so concurrent duplicates really overlap.
type blockingProcessor struct {
	release    chan struct{}
	mu         sync.Mutex
	executions int
}

func (p *blockingProcessor) Charge(ChargeRequest) (Response, error) {
	p.mu.Lock()
	p.executions++
	p.mu.Unlock()
	<-p.release
	return Response{200, `{"id":"ch_blocking"}`, false}, nil
}

func TestFingerprintMatchesJavaAndPython(t *testing.T) {
	if got := request(strPtr("k"), 1999).Fingerprint(); got != expectedFingerprint {
		t.Fatalf("fingerprint %s", got)
	}
}

func TestFingerprintIgnoresKeyAndAccount(t *testing.T) {
	other := mustRequest("acct_2", strPtr("k"), OfMinor(1999, USD), "tok_visa_4242", "Order 8812")
	if request(strPtr("other-key"), 1999).Fingerprint() != expectedFingerprint || other.Fingerprint() != expectedFingerprint {
		t.Fatal("fingerprint must depend only on the effectful fields")
	}
}

func TestFingerprintChangesWithAmountAndCurrency(t *testing.T) {
	eur := mustRequest("acct_1", strPtr("k"), OfMinor(1999, EUR), "tok_visa_4242", "Order 8812")
	if request(strPtr("k"), 2999).Fingerprint() == expectedFingerprint || eur.Fingerprint() == expectedFingerprint {
		t.Fatal("amount and currency must change the fingerprint")
	}
}

func TestRequestValidation(t *testing.T) {
	if _, err := NewChargeRequest("acct_1", strPtr(" "), OfMinor(1999, USD), "tok", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("blank key must be rejected")
	}
	if _, err := NewChargeRequest("acct_1", strPtr(strings.Repeat("x", 256)), OfMinor(1999, USD), "tok", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("over-long key must be rejected")
	}
	if _, err := NewChargeRequest("acct_1", strPtr("k"), OfMinor(0, USD), "tok", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("non-positive amount must be rejected")
	}
}

func TestFirstExecutesAndRetryReplays(t *testing.T) {
	f := newFixture(t)
	first := mustCharge(t, f.api, request(strPtr("order-1"), 1999))
	retry := mustCharge(t, f.api, request(strPtr("order-1"), 1999))
	if first.Status != 200 || first.Replayed {
		t.Fatalf("first=%+v", first)
	}
	if retry.Body != first.Body || !retry.Replayed {
		t.Fatalf("retry=%+v", retry)
	}
	if f.processor.Executions() != 1 || f.store.Size() != 1 {
		t.Fatalf("executions=%d size=%d", f.processor.Executions(), f.store.Size())
	}
}

func TestWithoutAKeyEverySendExecutes(t *testing.T) {
	f := newFixture(t)
	noKey := request(nil, 1999)
	mustCharge(t, f.api, noKey)
	mustCharge(t, f.api, noKey)
	if f.processor.Executions() != 2 {
		t.Fatalf("executions=%d", f.processor.Executions())
	}
}

func TestSameKeyDifferentBodyIs422AndDoesNotExecute(t *testing.T) {
	f := newFixture(t)
	first := mustCharge(t, f.api, request(strPtr("order-1"), 1999))
	conflict := mustCharge(t, f.api, request(strPtr("order-1"), 2999))
	if conflict.Status != 422 || conflict.Replayed || f.processor.Executions() != 1 {
		t.Fatalf("conflict=%+v executions=%d", conflict, f.processor.Executions())
	}
	if mustCharge(t, f.api, request(strPtr("order-1"), 1999)).Body != first.Body {
		t.Fatal("conflict must not disturb the stored response")
	}
}

func TestKeysAreScopedPerAccount(t *testing.T) {
	f := newFixture(t)
	mustCharge(t, f.api, request(strPtr("order-1"), 1999))
	other := mustCharge(t, f.api, mustRequest("acct_2", strPtr("order-1"), OfMinor(1999, USD), "tok_visa_4242", "Order 8812"))
	if other.Status != 200 || other.Replayed || f.processor.Executions() != 2 {
		t.Fatalf("other=%+v executions=%d", other, f.processor.Executions())
	}
}

func TestInProgressDuplicateGets409WithoutExecuting(t *testing.T) {
	f := newFixture(t)
	f.store.Begin("acct_1", "order-3", request(strPtr("order-3"), 1999).Fingerprint())
	busy := mustCharge(t, f.api, request(strPtr("order-3"), 1999))
	if busy.Status != 409 || f.processor.Executions() != 0 {
		t.Fatalf("busy=%+v executions=%d", busy, f.processor.Executions())
	}
}

func TestDeclinesAnd5xxReplay(t *testing.T) {
	f := newFixture(t)
	f.processor.DeclineNext()
	d1 := mustCharge(t, f.api, request(strPtr("order-4"), 5000))
	d2 := mustCharge(t, f.api, request(strPtr("order-4"), 5000))
	if d1.Status != 402 || d2.Status != 402 || !d2.Replayed {
		t.Fatalf("d1=%+v d2=%+v", d1, d2)
	}
	f.processor.ServerErrorNext()
	e1 := mustCharge(t, f.api, request(strPtr("order-5"), 5000))
	e2 := mustCharge(t, f.api, request(strPtr("order-5"), 5000))
	if e1.Status != 502 || e2.Status != 502 || !e2.Replayed || f.processor.Executions() != 2 {
		t.Fatalf("e1=%+v e2=%+v executions=%d", e1, e2, f.processor.Executions())
	}
}

func TestCrashReleasesReservationSoRetryExecutes(t *testing.T) {
	f := newFixture(t)
	f.processor.CrashNext()
	if _, err := f.api.Charge(request(strPtr("order-6"), 1200)); !errors.Is(err, ErrProcessorCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	after := mustCharge(t, f.api, request(strPtr("order-6"), 1200))
	if after.Status != 200 || after.Replayed || f.processor.Executions() != 2 {
		t.Fatalf("after=%+v executions=%d", after, f.processor.Executions())
	}
}

func TestConcurrentDuplicatesAtStoreLevel(t *testing.T) {
	f := newFixture(t)
	fp := request(strPtr("k"), 1999).Fingerprint()
	if f.store.Begin("acct_1", "order-2", fp).Outcome != New {
		t.Fatal("first begin reserves")
	}
	if f.store.Begin("acct_1", "order-2", fp).Outcome != InProgress {
		t.Fatal("second begin while in progress")
	}
	if f.store.Begin("acct_1", "order-2", "other-fp").Outcome != Conflict {
		t.Fatal("conflict is detected even while in progress")
	}
	if err := f.store.Complete("acct_1", "order-2", Response{200, `{"id":"ch_x"}`, false}); err != nil {
		t.Fatal(err)
	}
	c := f.store.Begin("acct_1", "order-2", fp)
	if c.Outcome != Replay || c.Response == nil || !c.Response.Replayed || c.Response.Body != `{"id":"ch_x"}` {
		t.Fatalf("c=%+v", c)
	}
}

func TestCompleteWithoutReservationOrTwiceIsAnError(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Complete("acct_1", "never-begun", Response{200, "{}", false}); !errors.Is(err, ErrNoReservation) {
		t.Fatalf("got %v", err)
	}
	f.store.Begin("acct_1", "order-2", "fp")
	_ = f.store.Complete("acct_1", "order-2", Response{200, "{}", false})
	if err := f.store.Complete("acct_1", "order-2", Response{200, "{}", false}); !errors.Is(err, ErrNoReservation) {
		t.Fatalf("got %v", err)
	}
}

func TestInProgressLeaseIsTakenOverAfterItExpires(t *testing.T) {
	f := newFixture(t)
	f.store.Begin("acct_1", "order-7", "fp")
	f.clock.Advance(29_999)
	if f.store.Begin("acct_1", "order-7", "fp").Outcome != InProgress {
		t.Fatal("still in progress just before the lease ends")
	}
	f.clock.Advance(1)
	if f.store.Begin("acct_1", "order-7", "fp").Outcome != New {
		t.Fatal("lease expired: the retry takes over as NEW")
	}
}

func TestTTLReplaysUntilExpiryThenForgets(t *testing.T) {
	clock := NewFakeClock(0)
	processor := &FakeProcessor{}
	store, _ := NewIdempotencyStore(clock, DayMs, 30_000)
	api := &PaymentsApi{store, processor}
	t1 := mustCharge(t, api, request(strPtr("order-8"), 1999))
	clock.Advance(DayMs - 1)
	if !mustCharge(t, api, request(strPtr("order-8"), 1999)).Replayed || processor.Executions() != 1 {
		t.Fatal("must replay one millisecond before the TTL")
	}
	clock.Advance(1)
	if store.PurgeExpired() != 1 || store.Size() != 0 {
		t.Fatal("purge must remove the expired entry")
	}
	t2 := mustCharge(t, api, request(strPtr("order-8"), 1999))
	if t2.Replayed || processor.Executions() != 2 || t2.Body == t1.Body {
		t.Fatalf("t2=%+v executions=%d", t2, processor.Executions())
	}
}

func TestStoreConfigValidation(t *testing.T) {
	if _, err := NewIdempotencyStore(NewFakeClock(0), 0, 1); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
	if _, err := NewIdempotencyStore(NewFakeClock(0), 1, 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}

func TestEightConcurrentDuplicatesExecuteOnce(t *testing.T) {
	blocking := &blockingProcessor{release: make(chan struct{})}
	store, _ := NewIdempotencyStore(NewFakeClock(0), DayMs, 30_000)
	api := &PaymentsApi{store, blocking}
	results := make(chan Response, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := api.Charge(request(strPtr("order-9"), 1999))
			if err != nil {
				t.Error(err)
				return
			}
			results <- r
		}()
	}
	close(start)
	var statuses []int
	for len(statuses) < 7 { // the seven losers finish with 409 while the winner is blocked
		statuses = append(statuses, (<-results).Status)
	}
	close(blocking.release)
	wg.Wait()
	statuses = append(statuses, (<-results).Status)
	sort.Ints(statuses)
	want := []int{200, 409, 409, 409, 409, 409, 409, 409}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("statuses=%v", statuses)
		}
	}
	blocking.mu.Lock()
	defer blocking.mu.Unlock()
	if blocking.executions != 1 {
		t.Fatalf("executions=%d", blocking.executions)
	}
}
