// Tests for phases/04-concurrency/08-the-actor-model. Run: go test ./...  (and go test -race ./...)
package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func account(t *testing.T, s *ActorSystem, name string) *AccountActor {
	t.Helper()
	return ActorOf(s, name).(*AccountActor)
}

func probeOf(t *testing.T, s *ActorSystem, name string) *ProbeActor {
	t.Helper()
	return ActorOf(s, name).(*ProbeActor)
}

func spawn(t *testing.T, s *ActorSystem, name string, factory func() Actor, strategy SupervisorStrategy) *ActorRef {
	t.Helper()
	ref, err := s.Spawn(name, factory, strategy)
	if err != nil {
		t.Fatalf("Spawn(%s): %v", name, err)
	}
	return ref
}

func ref(t *testing.T, s *ActorSystem, name string) *ActorRef {
	t.Helper()
	r, err := s.Ref(name)
	if err != nil {
		t.Fatalf("Ref(%s): %v", name, err)
	}
	return r
}

func newProbe() Actor { return NewProbeActor(0) }

func expectPanic(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	action()
}

func TestMoney(t *testing.T) {
	if got := usd(12_345).String(); got != "123.45 USD" {
		t.Errorf("String = %q", got)
	}
	diff, err := usd(100).Minus(usd(150))
	if err != nil || !diff.IsNegative() {
		t.Errorf("minus can go negative but IsNegative reports it: %v %v", diff, err)
	}
	if _, err := usd(1).Plus(OfMinor(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("currency mismatch on plus: %v", err)
	}
	if _, err := usd(1 << 62).Plus(usd(1 << 62)); !errors.Is(err, ErrOverflow) {
		t.Errorf("overflow is reported, not wrapped: %v", err)
	}
}

func TestSingleAccount(t *testing.T) {
	sched := NewDeterministicScheduler(0)
	s := NewActorSystem(sched, true)
	probe := spawn(t, s, "probe", newProbe, Resume)
	acct := spawn(t, s, "acct-1", func() Actor { return NewAccountActor("acct-1", usd(10_000)) }, Resume)
	acct.Tell(Deposit{"d1", usd(2_500), probe})
	acct.Tell(Withdraw{"w1", usd(1_999), probe})
	acct.Tell(Withdraw{"w2", usd(50_000), probe})
	acct.Tell(GetBalance{probe})
	if account(t, s, "acct-1").Balance() != usd(10_000) || len(probeOf(t, s, "probe").Received()) != 0 {
		t.Error("nothing happens until the scheduler runs")
	}
	delivered := sched.RunUntilIdle()
	if delivered != 8 || sched.Steps() != 8 {
		t.Errorf("every message delivered exactly once: delivered=%d steps=%d", delivered, sched.Steps())
	}
	got := probeOf(t, s, "probe").Received()
	if d, ok := got[0].(Deposited); !ok || d.Balance != usd(12_500) {
		t.Errorf("deposit reply carries the new balance: %v", got[0])
	}
	if w, ok := got[1].(Withdrawn); !ok || w.Amount != usd(1_999) || w.Balance != usd(10_501) {
		t.Errorf("withdraw reply carries amount and balance: %v", got[1])
	}
	if f, ok := got[2].(InsufficientFunds); !ok || f.Requested != usd(50_000) || f.Balance != usd(10_501) {
		t.Errorf("overdraft is refused with the real balance: %v", got[2])
	}
	if b, ok := got[3].(BalanceIs); !ok || b.Balance != usd(10_501) {
		t.Errorf("balance query sees the serialised result: %v", got[3])
	}
	if account(t, s, "acct-1").Balance() != usd(10_501) {
		t.Error("refused withdrawal left the balance untouched")
	}
	var acctTrace []string
	for _, line := range s.Trace() {
		if strings.HasPrefix(line, "acct-1 <- ") {
			acctTrace = append(acctTrace, line)
		}
	}
	if !(strings.HasPrefix(acctTrace[0], "acct-1 <- Deposit") && strings.HasPrefix(acctTrace[1], "acct-1 <- Withdraw(w1") &&
		strings.HasPrefix(acctTrace[2], "acct-1 <- Withdraw(w2") && acctTrace[3] == "acct-1 <- GetBalance") {
		t.Errorf("messages are processed in mailbox order: %v", acctTrace)
	}
	expectPanic(t, "opening balance must not be negative", func() { NewAccountActor("x", usd(-1)) })
}

func TestTransfers(t *testing.T) {
	sched := NewDeterministicScheduler(0)
	s := BankWith(sched, true)
	probe := spawn(t, s, "probe", newProbe, Resume)
	ref(t, s, "transfers").Tell(Transfer{"ok", "acct-A", "acct-B", usd(7_500), probe})
	ref(t, s, "transfers").Tell(Transfer{"bad", "acct-C", "acct-A", usd(99_999), probe})
	sched.RunUntilIdle()
	results := probeOf(t, s, "probe").TransferResults()
	if len(results) != 2 {
		t.Fatalf("two results, got %d", len(results))
	}
	byID := map[string]TransferResult{}
	for _, r := range results {
		byID[r.TransferID] = r
	}
	if !byID["ok"].OK {
		t.Error("successful transfer reports ok")
	}
	if account(t, s, "acct-A").Balance() != usd(2_500) || account(t, s, "acct-B").Balance() != usd(17_500) {
		t.Error("source debited and destination credited by exactly the amount")
	}
	if bad := byID["bad"]; bad.OK || bad.Reason != "acct-C has 100.00 USD, needs 999.99 USD" {
		t.Errorf("failed transfer explains itself: %+v", bad)
	}
	if account(t, s, "acct-C").Balance() != usd(10_000) {
		t.Error("failed transfer moved nothing")
	}
	coordinator := ActorOf(s, "transfers").(*TransferCoordinator)
	if coordinator.Completed() != 1 || coordinator.Rejected() != 1 || coordinator.InFlight() != 0 {
		t.Error("coordinator bookkeeping")
	}
	trace := strings.Join(s.Trace(), "\n")
	if !strings.Contains(trace, "acct-A <- Withdraw(ok, 75.00 USD)") || !strings.Contains(trace, "acct-B <- Deposit(ok, 75.00 USD)") {
		t.Error("transfer is two hops: withdraw then deposit")
	}
}

func runScript(t *testing.T, seed int64) []string {
	t.Helper()
	sched := NewDeterministicScheduler(seed)
	s := BankWith(sched, true)
	probe := spawn(t, s, "probe", newProbe, Resume)
	for _, tr := range ScriptedTransfers(probe) {
		ref(t, s, "transfers").Tell(tr)
	}
	sched.RunUntilIdle()
	return s.Trace()
}

func TestDeterminism(t *testing.T) {
	if !reflect.DeepEqual(runScript(t, 42), runScript(t, 42)) {
		t.Error("same seed, same trace")
	}
	if !reflect.DeepEqual(runScript(t, 0), runScript(t, 0)) {
		t.Error("round-robin seed 0 is reproducible too")
	}
	someDifference := false
	base := runScript(t, 1)
	for seed := int64(2); seed <= 10 && !someDifference; seed++ {
		someDifference = !reflect.DeepEqual(runScript(t, seed), base)
	}
	if !someDifference {
		t.Error("different seeds explore different interleavings")
	}
	if len(runScript(t, 3)) != 32 || len(runScript(t, 9)) != 32 {
		t.Error("every run delivers the same number of messages")
	}
	// Parity with the Java demo: seed 42 starts with the same six trace lines.
	want := []string{
		"transfers <- Transfer(t1, acct-A -> acct-B, 75.00 USD)",
		"acct-A <- Withdraw(t1, 75.00 USD)",
		"transfers <- Transfer(t2, acct-B -> acct-C, 120.00 USD)",
		"acct-B <- Withdraw(t2, 120.00 USD)",
		"transfers <- Transfer(t3, acct-C -> acct-A, 50.00 USD)",
		"transfers <- Transfer(t4, acct-A -> acct-C, 90.00 USD)",
	}
	if got := runScript(t, 42)[:6]; !reflect.DeepEqual(got, want) {
		t.Errorf("seed 42 trace matches the Java port:\n%v", got)
	}
}

func TestConservationAcrossSeeds(t *testing.T) {
	for seed := int64(0); seed < 25; seed++ {
		t.Run("seed", func(t *testing.T) {
			sched := NewDeterministicScheduler(seed)
			s := BankWith(sched, false)
			probe := spawn(t, s, "probe", newProbe, Resume)
			for _, tr := range ScriptedTransfers(probe) {
				ref(t, s, "transfers").Tell(tr)
			}
			sched.RunUntilIdle()
			if Total(s, "acct-A", "acct-B", "acct-C") != usd(30_000) {
				t.Errorf("seed %d: money is conserved", seed)
			}
			for _, a := range []string{"acct-A", "acct-B", "acct-C"} {
				if account(t, s, a).Balance().IsNegative() {
					t.Errorf("seed %d: %s went negative", seed, a)
				}
			}
			if len(probeOf(t, s, "probe").TransferResults()) != 6 {
				t.Errorf("seed %d: every transfer gets exactly one result", seed)
			}
			if ActorOf(s, "transfers").(*TransferCoordinator).InFlight() != 0 {
				t.Errorf("seed %d: coordinator finishes with nothing in flight", seed)
			}
		})
	}
}

func TestSupervision(t *testing.T) {
	sched := NewDeterministicScheduler(0)
	s := NewActorSystem(sched, false)
	probe := spawn(t, s, "probe", newProbe, Resume)
	acct := spawn(t, s, "acct-X", func() Actor { return NewAccountActor("acct-X", usd(5_000)) }, Resume)
	resumed := spawn(t, s, "c-resume", func() Actor { return NewCounterActor() }, Resume)
	restarted := spawn(t, s, "c-restart", func() Actor { return NewCounterActor() }, Restart)
	stopped := spawn(t, s, "c-stop", func() Actor { return NewCounterActor() }, Stop)
	acct.Tell(Deposit{"eur", OfMinor(100, EUR), probe})
	acct.Tell(Deposit{"usd", usd(100), probe})
	for _, c := range []*ActorRef{resumed, restarted, stopped} {
		c.Tell(Increment{})
		c.Tell(Increment{})
		c.Tell(Crash{"boom"})
		c.Tell(Increment{})
		c.Tell(GetBalance{probe})
	}
	sched.RunUntilIdle()
	received := probeOf(t, s, "probe").Received()
	sawUSD, sawEUR := false, false
	var counts []int64
	for _, m := range received {
		switch v := m.(type) {
		case Deposited:
			if v.CorrelationID == "usd" && v.Balance == usd(5_100) {
				sawUSD = true
			}
			if v.CorrelationID == "eur" {
				sawEUR = true
			}
		case CountIs:
			counts = append(counts, v.Count)
		}
	}
	if !sawUSD {
		t.Error("a failing message is dropped and the next one is processed")
	}
	if account(t, s, "acct-X").Balance() != usd(5_100) || s.Cell("acct-X").Failures.Load() != 1 || s.Cell("acct-X").Restarts.Load() != 0 {
		t.Error("RESUME keeps the account's state")
	}
	if len(counts) != 2 || counts[0] != 3 {
		t.Errorf("RESUME counter keeps 2 and adds 1: %v", counts)
	}
	if len(counts) < 2 || counts[1] != 1 || s.Cell("c-restart").Restarts.Load() != 1 {
		t.Errorf("RESTART counter lost its state and counted only the post-restart increment: %v", counts)
	}
	if !s.Cell("c-stop").Stopped.Load() || s.Cell("c-stop").DeadLetters.Load() != 2 || s.DeadLetters() != 2 {
		t.Error("STOP discards the rest of the mailbox as dead letters")
	}
	stopped.Tell(Increment{})
	sched.RunUntilIdle()
	if s.Cell("c-stop").DeadLetters.Load() != 3 {
		t.Error("messages to a stopped actor are dead letters too")
	}
	failures := s.Failures()
	if len(failures) != 4 || !strings.Contains(failures[0], "acct-X failed on Deposit(eur") {
		t.Errorf("failures are recorded for operators: %v", failures)
	}
	if !strings.HasSuffix(failures[0], "cannot combine USD with EUR -> RESUME") {
		t.Errorf("failure line names the error and the strategy: %s", failures[0])
	}
	if sawEUR {
		t.Error("the wrong-currency deposit never reached the probe")
	}
}

func TestSystemRules(t *testing.T) {
	sched := NewDeterministicScheduler(0)
	s := NewActorSystem(sched, false)
	a := spawn(t, s, "a", func() Actor { return NewCounterActor() }, Resume)
	if _, err := s.Spawn("a", func() Actor { return NewCounterActor() }, Resume); !errors.Is(err, ErrActorExists) {
		t.Errorf("duplicate actor names are rejected: %v", err)
	}
	if _, err := s.Ref("nobody"); !errors.Is(err, ErrNoSuchActor) {
		t.Errorf("lookup of an unknown actor fails loudly: %v", err)
	}
	expectPanic(t, "nil messages are rejected at Tell", func() { a.Tell(nil) })
	if sched.Step() {
		t.Error("an idle scheduler reports no step")
	}
	a.Tell(Increment{})
	if !(sched.Step() && !sched.Step()) {
		t.Error("one message, one step")
	}
	a.Tell(Transfer{})
	sched.RunUntilIdle()
	if f := s.Failures(); len(f) != 1 || !strings.Contains(f[0], "counter does not understand Transfer(") {
		t.Errorf("an unknown message is a supervised failure, not a crash: %v", f)
	}
}

func TestThreadedHotAccount(t *testing.T) {
	pool := NewThreadedScheduler(4, 32)
	s := NewActorSystem(pool, false)
	acct := spawn(t, s, "hot", func() Actor { return NewAccountActor("hot", usd(0)) }, Resume)
	producers, perProducer := 4, 10_000
	var done sync.WaitGroup
	for p := 0; p < producers; p++ {
		done.Add(1)
		go func() {
			defer done.Done()
			for i := 0; i < perProducer; i++ {
				acct.Tell(Deposit{"x", usd(1), nil})
			}
		}()
	}
	done.Wait()
	probe := spawn(t, s, "probe", func() Actor { return NewProbeActor(1) }, Resume)
	acct.Tell(GetBalance{probe})
	if !probeOf(t, s, "probe").Latch.Await(10 * time.Second) {
		t.Fatal("probe answered in time")
	}
	pool.Close()
	if got := probeOf(t, s, "probe").Balances()["hot"]; got != usd(int64(producers*perProducer)) {
		t.Errorf("40,000 concurrent deposits produce an exact balance with no lock: %s", got)
	}
	if s.Cell("hot").MaxConcurrentReceives.Load() != 1 {
		t.Error("Receive never ran on two goroutines at once")
	}
	if s.Cell("hot").Processed.Load() != int64(producers*perProducer+1) {
		t.Error("every message processed exactly once")
	}
}

func TestThreadedTransfers(t *testing.T) {
	pool := NewThreadedScheduler(4, 16)
	s := BankWith(pool, false)
	transfers := 3_000
	probe := spawn(t, s, "probe", func() Actor { return NewProbeActor(transfers) }, Resume)
	names := []string{"acct-A", "acct-B", "acct-C"}
	var producers sync.WaitGroup
	for p := 0; p < 3; p++ {
		producers.Add(1)
		go func(id int) {
			defer producers.Done()
			for i := 0; i < transfers/3; i++ {
				from, to := names[(id+i)%3], names[(id+i+1)%3]
				ref(t, s, "transfers").Tell(Transfer{fmt.Sprintf("t%d-%d", id, i), from, to, usd(int64(1 + i%700)), probe})
			}
		}(p)
	}
	producers.Wait()
	if !probeOf(t, s, "probe").Latch.Await(10 * time.Second) {
		t.Fatal("all transfer results arrived")
	}
	probe2 := spawn(t, s, "probe2", func() Actor { return NewProbeActor(3) }, Resume)
	for _, n := range names {
		ref(t, s, n).Tell(GetBalance{probe2})
	}
	p2 := probeOf(t, s, "probe2")
	if !p2.Latch.Await(10 * time.Second) {
		t.Fatal("balances reported")
	}
	pool.Close()
	balances := p2.Balances()
	total := usd(0)
	for _, m := range balances {
		var err error
		if total, err = total.Plus(m); err != nil {
			t.Fatal(err)
		}
		if m.IsNegative() {
			t.Error("no account went negative")
		}
	}
	if total != usd(30_000) {
		t.Errorf("money conserved across 3,000 concurrent transfers: %s", total)
	}
	if s.Cell("acct-A").MaxConcurrentReceives.Load() != 1 || s.Cell("transfers").MaxConcurrentReceives.Load() != 1 {
		t.Error("each account processed on one goroutine at a time")
	}
}

func TestJavaRandomParity(t *testing.T) {
	// new java.util.Random(42).nextInt(5) x 5 -> 0, 3, 3, 4, 0 (JDK 21).
	r := NewJavaRandom(42)
	got := []int32{r.NextInt(5), r.NextInt(5), r.NextInt(5), r.NextInt(5), r.NextInt(5)}
	if want := []int32{0, 3, 3, 4, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("JavaRandom(42).NextInt(5) = %v, want %v", got, want)
	}
	if NewJavaRandom(7).NextInt(2) != NewJavaRandom(7).NextInt(2) {
		t.Error("same seed, same sequence")
	}
}
