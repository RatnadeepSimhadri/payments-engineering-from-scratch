// Project: projects/idempotent-payment-gateway — reference solution (Go), worked example.
// Run:  go run .
// Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang go --all --solution
// Stages: 01 idempotency store, 02 payment state machine, 03 retries + circuit breaker + gateway, 04 webhooks.
// Go 1.22+, standard library only. Same design and the same trace as the Java and Python solutions.
package main

import (
	"fmt"
	"strings"
)

func usd(text string) Money { return must(Parse(text, USD)) }

func request(merchant, key, amount, source, description string) ChargeRequest {
	return must1(NewChargeRequest(merchant, key, usd(amount), source, description))
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	store := must1(NewIdempotencyStore(clock, 24*60*60*1000))

	fmt.Println("== Stage 01: idempotency store ==")
	req := request("m_1", "key-1", "19.99", "tok_visa", "order-42")
	fmt.Println("canonical  =", strings.ReplaceAll(req.Canonical(), "\n", `\n`))
	fmt.Println("fingerprint=", req.Fingerprint())
	fmt.Println("first begin ->", store.Begin("m_1", "key-1", req.Fingerprint()).Decision)
	fmt.Println("while open  ->", store.Begin("m_1", "key-1", req.Fingerprint()).Decision)
	_ = store.Complete("m_1", "key-1", ApiResponse{201, `{"payment_id":"pay_1"}`, false})
	replayed := store.Begin("m_1", "key-1", req.Fingerprint())
	fmt.Printf("after done  -> %v %+v\n", replayed.Decision, *replayed.Response)
	other := request("m_1", "key-1", "29.99", "tok_visa", "order-42")
	fmt.Println("other body  ->", store.Begin("m_1", "key-1", other.Fingerprint()).Decision)

	fmt.Println("\n== Stage 02: payment state machine ==")
	p := must1(NewPayment("pay_demo", usd("50.00"), clock))
	_ = p.StartProcessing()
	_ = p.Succeed("auth-77")
	remaining, _ := p.Refund(usd("20.00"), "customer request")
	fmt.Println("refund 20 -> remaining", remaining)
	remaining, _ = p.Refund(usd("30.00"), "customer request")
	fmt.Printf("refund 30 -> remaining %v state %v\n", remaining, p.State())
	if _, err := p.Refund(usd("1.00"), "again"); err != nil {
		fmt.Println("rejected:", err)
	}
	for _, t := range p.History() {
		fmt.Printf("  %v -> %v : %s\n", t.From, t.To, t.Reason)
	}

	fmt.Println("\n== Stage 03: retries with full jitter, budget, breaker ==")
	policy := must1(NewBackoffPolicy(100, 2_000, 4))
	rng := NewRng(42)
	for retry := 1; retry <= 4; retry++ {
		fmt.Printf("retry %d: ceiling %d ms, drawn %d ms\n", retry, policy.CeilingFor(retry), policy.DelayBefore(retry, NewRng(int64(42+retry))))
	}
	breaker := must1(NewCircuitBreaker(clock, 3, 30_000, 1))
	client := NewRetryingClient(must1(NewScriptedProcessor("UUA")), policy, must1(NewRetryBudget(10, 10)), breaker, rng, clock)
	gateway := NewGateway(store, client, clock)
	first := gateway.Charge(request("m_1", "key-2", "19.99", "tok_visa", "order-43"))
	fmt.Printf("charge -> %d %s\n", first.Status, first.Body)
	replay := gateway.Charge(request("m_1", "key-2", "19.99", "tok_visa", "order-43"))
	fmt.Printf("replay -> %d replayed=%t\n", replay.Status, replay.Replayed)

	fmt.Println("\n== Stage 04: signed webhooks, at-least-once, dedupe ==")
	signer := must1(NewWebhookSigner("whsec_demo"))
	consumer := NewWebhookConsumer(must1(NewWebhookVerifier("whsec_demo", 5*60_000, clock)))
	dispatcher := must1(NewWebhookDispatcher(signer, NewConsumerEndpoint(consumer), clock, []int64{60_000, 300_000}))
	gateway.AttachWebhooks(dispatcher)
	gateway.Charge(request("m_1", "key-3", "5.00", "tok_visa", "order-44"))
	fmt.Println("pending before run:", len(dispatcher.Pending()))
	dispatcher.RunDue()
	evt := consumer.Processed()[0]
	body := evt.ToBody()
	header := signer.Header(clock.NowMillis(), body)
	fmt.Printf("delivered %s %s header %s\n", evt.ID, evt.Type, header)
	fmt.Println("same event again ->", consumer.Receive(body, header).Status)
	fmt.Println("tampered body    ->", consumer.Receive(strings.ReplaceAll(body, "5.00", "500.00"), header).Status)
}
