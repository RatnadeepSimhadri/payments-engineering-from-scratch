// Project: projects/idempotent-payment-gateway — learner workspace entry point (Go).
// Run:  go run .
// Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang go --stage 1 --path <workspace>
// money.go, clock.go and rng.go are given. idempotency.go holds the Stage 01 stubs; later stages add
// payments.go, resilience.go and webhooks.go (see each stage's starter/STAGE.md).
package main

import "fmt"

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	store, err := NewIdempotencyStore(clock, 24*60*60*1000)
	if err != nil {
		panic(err)
	}
	req, err := NewChargeRequest("m_1", "key-1", must(Parse("19.99", USD)), "tok_visa", "order-42")
	if err != nil {
		panic(err)
	}
	fmt.Printf("idempotent-payment-gateway workspace: store ttl %d ms, request %v\n", store.TTLMillis(), req.Amount)
	defer func() {
		// Every stub panics with "not implemented: TODO stage 01 ..." until you replace it; recover so the
		// workspace still exits 0 and tells you which function to write next.
		if todo := recover(); todo != nil {
			fmt.Println("not implemented yet:", todo)
		}
	}()
	fmt.Println("fingerprint =", req.Fingerprint())
	fmt.Println("first begin ->", store.Begin("m_1", "key-1", req.Fingerprint()).Decision)
}
