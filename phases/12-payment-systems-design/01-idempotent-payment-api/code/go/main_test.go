// Tests for phases/12-payment-systems-design/01-idempotent-payment-api. Run: go test ./...  (and go test -race ./...)
package main

import (
	"errors"
	"sync"
	"testing"
)

const M = "merchant_acme"

type fixture struct {
	clock     *FakeClock
	processor *FakeProcessor
	store     *IdempotencyStore
	api       *PaymentsHandler
}

func newFixture() fixture {
	clock := NewFakeClock(1_700_000_000_000)
	processor := NewFakeProcessor()
	store := NewIdempotencyStore(clock, KeyTTLMillis, LeaseMillis)
	return fixture{clock, processor, store, NewPaymentsHandler(store, processor)}
}

func order(t *testing.T) PaymentRequest {
	t.Helper()
	return mustRequest(must(Parse("19.99", USD)), "tok_visa_4242", "Order 1001")
}

func post(t *testing.T, f fixture, merchant, key string, r PaymentRequest) PaymentResponse {
	t.Helper()
	resp, err := f.api.Post(merchant, key, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

func TestCanonicalBodyAndFingerprintVector(t *testing.T) {
	o := order(t)
	if got := o.Canonical(); got != "amount=1999\ncurrency=USD\ndescription=Order 1001\nsource=tok_visa_4242\n" {
		t.Fatalf("canonical = %q", got)
	}
	if got := o.Fingerprint(); got != "a3e98e16f24b06155d4539aa040c1d1362dcaafddc3debe88ee16fb23f9a644f" {
		t.Fatalf("fingerprint = %s", got)
	}
	other := mustRequest(must(Parse("20.00", USD)), "tok_visa_4242", "Order 1001")
	if other.Fingerprint() == o.Fingerprint() {
		t.Fatal("fingerprint must change with amount")
	}
	eur := mustRequest(OfMinor(1999, EUR), "tok_visa_4242", "Order 1001")
	if eur.Fingerprint() == o.Fingerprint() {
		t.Fatal("fingerprint must change with currency")
	}
}

func TestScopedKeyAndReferenceID(t *testing.T) {
	if ScopedKey(M, "key-1001") != "merchant_acme\u001fkey-1001" {
		t.Fatal("scoped key format")
	}
	if got := ReferenceIDFor(ScopedKey(M, "key-1001")); got != "ref_349281adde6ae74d" {
		t.Fatalf("reference id = %s", got)
	}
	if _, err := NewPaymentRequest(OfMinor(0, USD), "tok", "x"); err == nil {
		t.Fatal("non-positive amount must be rejected")
	}
}

func TestFirstCallChargesAndDuplicateReplays(t *testing.T) {
	f := newFixture()
	first := post(t, f, M, "key-1", order(t))
	second := post(t, f, M, "key-1", order(t))
	if first.Status != 201 || first.Replayed() {
		t.Fatalf("first = %+v", first)
	}
	if second.Status != 201 || second.Body != first.Body || !second.Replayed() {
		t.Fatalf("second = %+v", second)
	}
	if f.processor.ChargeCalls() != 1 || f.store.Size() != 1 {
		t.Fatal("exactly one charge and one record expected")
	}
	if rec, _ := f.store.Get(ScopedKey(M, "key-1")); rec.State != Completed {
		t.Fatal("record must be COMPLETED")
	}
}

func TestMissingKeyOrMerchant(t *testing.T) {
	f := newFixture()
	if post(t, f, M, "", order(t)).Status != 400 || post(t, f, M, "   ", order(t)).Status != 400 {
		t.Fatal("missing key must be 400")
	}
	if post(t, f, M, "key\u001fx", order(t)).Status != 400 {
		t.Fatal("control character in key must be 400")
	}
	if post(t, f, "", "key-1", order(t)).Status != 401 {
		t.Fatal("missing merchant must be 401")
	}
	if f.processor.ChargeCalls() != 0 {
		t.Fatal("rejected requests must never reach the processor")
	}
}

func TestKeysAreScopedPerMerchant(t *testing.T) {
	f := newFixture()
	acme := post(t, f, "merchant_acme", "order-1001", order(t))
	globex := post(t, f, "merchant_globex", "order-1001", order(t))
	if f.processor.ChargeCalls() != 2 {
		t.Fatal("same key from two merchants must charge twice")
	}
	if acme.Status != 201 || globex.Status != 201 || globex.Replayed() || acme.Body == globex.Body {
		t.Fatal("second merchant must get its own payment")
	}
	if post(t, f, "merchant_globex", "order-1001", order(t)).Body != globex.Body {
		t.Fatal("each merchant replays its own response")
	}
	if f.store.Size() != 2 {
		t.Fatal("one record per merchant")
	}
}

func TestSameKeyDifferentFingerprintIs422(t *testing.T) {
	f := newFixture()
	post(t, f, M, "key-2", order(t))
	changed := mustRequest(must(Parse("29.99", USD)), "tok_visa_4242", "Order 1001")
	r := post(t, f, M, "key-2", changed)
	if r.Status != 422 || r.Replayed() || f.processor.ChargeCalls() != 1 {
		t.Fatalf("mismatch = %+v calls=%d", r, f.processor.ChargeCalls())
	}
	reordered := mustRequest(OfMinor(1999, USD), "tok_visa_4242", "Order 1001")
	if !post(t, f, M, "key-2", reordered).Replayed() {
		t.Fatal("same meaning must fingerprint the same and replay")
	}
}

func TestDeclinesAreStoredAndReplayed(t *testing.T) {
	f := newFixture()
	declined := mustRequest(must(Parse("10.00", USD)), "tok_decline_0002", "Order 7")
	d1 := post(t, f, M, "key-3", declined)
	d2 := post(t, f, M, "key-3", declined)
	if d1.Status != 402 || d2.Status != 402 || !d2.Replayed() || f.processor.ChargeCalls() != 1 {
		t.Fatalf("d1=%+v d2=%+v", d1, d2)
	}
}

func TestConcurrentDuplicateWhileInProgressIs409(t *testing.T) {
	f := newFixture()
	inFlight := make(chan struct{})
	release := make(chan struct{})
	f.processor.OnBeforeCommit(func() {
		close(inFlight)
		<-release
	})
	var owner PaymentResponse
	done := make(chan struct{})
	go func() {
		owner, _ = f.api.Post(M, "key-4", order(t))
		close(done)
	}()
	<-inFlight
	concurrent := post(t, f, M, "key-4", order(t))
	if concurrent.Status != 409 || concurrent.RetryAfterSeconds() != RetryAfterSeconds {
		t.Fatalf("concurrent = %+v", concurrent)
	}
	if f.processor.ChargeCalls() != 1 {
		t.Fatal("the duplicate must not reach the processor")
	}
	close(release)
	<-done
	f.processor.OnBeforeCommit(func() {})
	if owner.Status != 201 {
		t.Fatalf("owner = %+v", owner)
	}
	if !post(t, f, M, "key-4", order(t)).Replayed() {
		t.Fatal("after completion the duplicate is replayed")
	}
}

func TestManyGoroutinesOneKeyExactlyOneCharge(t *testing.T) {
	f := newFixture()
	const n = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := f.api.Post(M, "key-5", order(t))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			statuses[r.Status]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if f.processor.ChargeCalls() != 1 {
		t.Fatalf("expected one charge, got %d", f.processor.ChargeCalls())
	}
	total := 0
	for status, count := range statuses {
		if status != 201 && status != 409 {
			t.Fatalf("unexpected status %d", status)
		}
		total += count
	}
	if total != n {
		t.Fatalf("every goroutine must get a verdict, got %d", total)
	}
}

func TestCrashAfterCommitRecoversByLookup(t *testing.T) {
	f := newFixture()
	f.processor.CrashAfterNextCharge()
	if _, err := f.api.Post(M, "key-6", order(t)); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("expected simulated crash, got %v", err)
	}
	if rec, _ := f.store.Get(ScopedKey(M, "key-6")); rec.State != InProgress {
		t.Fatal("record must be left IN_PROGRESS after the crash")
	}
	if f.processor.ChargesStored() != 1 {
		t.Fatal("the processor did commit the charge")
	}
	if post(t, f, M, "key-6", order(t)).Status != 409 {
		t.Fatal("a retry inside the lease cannot tell crash from slow owner: 409")
	}
	f.clock.Advance(LeaseMillis)
	recovered := post(t, f, M, "key-6", order(t))
	if recovered.Status != 201 || f.processor.ChargeCalls() != 1 {
		t.Fatalf("recovery must look up, not re-charge: %+v calls=%d", recovered, f.processor.ChargeCalls())
	}
	if rec, _ := f.store.Get(ScopedKey(M, "key-6")); rec.State != Completed {
		t.Fatal("record must now be COMPLETED")
	}
	if !post(t, f, M, "key-6", order(t)).Replayed() {
		t.Fatal("subsequent call is a plain replay")
	}
}

func TestCrashBeforeCommitRecoversByCharging(t *testing.T) {
	f := newFixture()
	scoped := ScopedKey(M, "key-7")
	f.store.Reserve(scoped, order(t).Fingerprint(), ReferenceIDFor(scoped))
	f.clock.Advance(LeaseMillis)
	r := post(t, f, M, "key-7", order(t))
	if r.Status != 201 || f.processor.ChargeCalls() != 1 {
		t.Fatalf("recovery with no prior charge must charge once: %+v", r)
	}
}

func TestKeyTTL24h(t *testing.T) {
	f := newFixture()
	post(t, f, M, "key-8", order(t))
	f.clock.Advance(KeyTTLMillis - 1)
	if !post(t, f, M, "key-8", order(t)).Replayed() {
		t.Fatal("one ms before TTL still replays")
	}
	f.clock.Advance(1)
	fresh := post(t, f, M, "key-8", order(t))
	if fresh.Replayed() || f.processor.ChargeCalls() != 2 {
		t.Fatal("at TTL the key is forgotten and a new charge happens")
	}
	f.clock.Advance(KeyTTLMillis)
	other := mustRequest(must(Parse("1.00", USD)), "tok_visa_4242", "new order")
	if post(t, f, M, "key-8", other).Status != 201 {
		t.Fatal("after TTL the key may carry a different body")
	}
}

func TestReadsAreNotIdempotentOperations(t *testing.T) {
	f := newFixture()
	created := post(t, f, M, "key-r", order(t))
	id := ReferenceIDFor(ScopedKey(M, "key-r"))
	read := f.api.Get(id)
	if read.Status != 200 || read.Body != created.Body || read.Replayed() {
		t.Fatalf("read = %+v", read)
	}
	if f.api.Get("ref_missing").Status != 404 {
		t.Fatal("unknown id is 404")
	}
	if f.store.Size() != 1 || f.processor.ChargeCalls() != 1 {
		t.Fatal("reads create no records and never charge")
	}
}

func TestStoreInvariants(t *testing.T) {
	f := newFixture()
	if err := f.store.Complete("never-reserved", respond(201, "{}")); err == nil {
		t.Fatal("Complete without Reserve is a bug")
	}
	if f.store.Reserve("k", "fp", "ref").Decision != Reserved {
		t.Fatal("first reserve wins")
	}
	if f.store.Reserve("k", "fp", "ref").Decision != ReservationInProgress {
		t.Fatal("second reserve sees IN_PROGRESS")
	}
	if f.store.Reserve("k", "other", "ref").Decision != FingerprintMismatch {
		t.Fatal("different fingerprint beats state")
	}
}

func TestMoneyDiscipline(t *testing.T) {
	if must(Parse("19.99", USD)).String() != "19.99 USD" {
		t.Fatal("Money formatting")
	}
	if _, err := must(Parse("1.00", USD)).Plus(must(Parse("1.00", EUR))); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("currency mix must be rejected")
	}
}
