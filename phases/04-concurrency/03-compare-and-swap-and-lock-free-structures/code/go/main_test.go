// Tests for phases/04-concurrency/03-compare-and-swap-and-lock-free-structures. Run: go test -race ./...
package main

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func pop[T any](s *TreiberStack[T]) T { v, _ := s.Pop(); return v }

func TestTreiberBasics(t *testing.T) {
	stack := &TreiberStack[string]{}
	check(t, "new stack is empty", stack.IsEmpty() && stack.Size() == 0)
	_, ok := stack.Pop()
	check(t, "pop on empty is absent", !ok)
	_, ok = stack.Peek()
	check(t, "peek on empty is absent", !ok)
	stack.Push("a")
	stack.Push("b")
	stack.Push("c")
	check(t, "size after three pushes", stack.Size() == 3)
	check(t, "LIFO order", pop(stack) == "c" && pop(stack) == "b" && pop(stack) == "a")
	_, ok = stack.Pop()
	check(t, "empty after popping everything", stack.IsEmpty() && !ok)
	check(t, "no retries when single-threaded", stack.CasRetries() == 0)
	stack.Push("")
	v, ok := stack.Pop()
	check(t, "the zero value is a legal element, distinguished from empty by ok", ok && v == "")
}

func TestTreiberStress(t *testing.T) {
	stack := &TreiberStack[int64]{}
	workers, perWorker := 4, 20_000
	// Phase 1: everyone pushes, nobody pops -> exactly workers*perWorker distinct values must be present.
	start := make(chan struct{})
	var done sync.WaitGroup
	for w := 0; w < workers; w++ {
		base := int64(w) * int64(perWorker)
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			for i := 0; i < perWorker; i++ {
				stack.Push(base + int64(i))
			}
		}()
	}
	close(start)
	done.Wait()
	check(t, "concurrent pushes: size is exact", stack.Size() == workers*perWorker)
	seen := map[int64]bool{}
	for v, ok := stack.Pop(); ok; v, ok = stack.Pop() {
		seen[v] = true
	}
	check(t, "concurrent pushes: every value present exactly once", len(seen) == workers*perWorker)

	// Phase 2: mixed push/pop; the stack must end empty and conserve the pop count.
	elapsed := StressStack(stack, workers, perWorker)
	check(t, "mixed push/pop leaves the stack empty", stack.IsEmpty())
	check(t, "stress finished in bounded time", elapsed < 10*time.Second)
}

// TestAbaReproduced reproduces the ABA interleaving deterministically in one goroutine via the beforeCas seam.
func TestAbaReproduced(t *testing.T) {
	var stack *PooledStack
	armed := true
	var poppedByB []string
	stack = NewPooledStack(func() {
		if !armed {
			return
		}
		armed = false
		v1, _ := stack.Pop() // pay_003, N3 freed
		poppedByB = append(poppedByB, v1.PaymentID)
		v2, _ := stack.Pop() // pay_002, N2 freed
		poppedByB = append(poppedByB, v2.PaymentID)
		stack.Push(CaptureJob{"pay_004", 4_000}) // reuses N3: head is N3 again, with different contents
	})
	stack.Push(CaptureJob{"pay_001", 1_000})
	stack.Push(CaptureJob{"pay_002", 2_000})
	stack.Push(CaptureJob{"pay_003", 3_000})
	check(t, "head is N3 before the race", stack.HeadNode().ID == 3)

	gotByA, _ := stack.Pop()
	check(t, "B popped pay_003 then pay_002", reflect.DeepEqual(poppedByB, []string{"pay_003", "pay_002"}))
	check(t, "A's stale CAS succeeded because the pointer matched", gotByA.PaymentID == "pay_004")
	after := stack.Snapshot()
	check(t, "ABA: pay_002 reappears on the stack although it was already popped", after[0] == "N2=pay_002")
	duplicate, ok := stack.Pop()
	check(t, "ABA: pay_002 is popped a second time (double capture)", ok && duplicate.PaymentID == "pay_002")
}

func TestAbaFixedByStamp(t *testing.T) {
	var stack *StampedPooledStack
	armed := true
	stack = NewStampedPooledStack(func() {
		if !armed {
			return
		}
		armed = false
		stack.Pop()
		stack.Pop()
		stack.Push(CaptureJob{"pay_004", 4_000})
	})
	stack.Push(CaptureJob{"pay_001", 1_000})
	stack.Push(CaptureJob{"pay_002", 2_000})
	stack.Push(CaptureJob{"pay_003", 3_000})
	check(t, "stamp counts successful CASes", stack.Stamp() == 3)

	gotByA, _ := stack.Pop()
	check(t, "stale CAS was rejected exactly once", stack.StaleCasRejected() == 1)
	check(t, "A retried and popped the current top (pay_004)", gotByA.PaymentID == "pay_004")
	check(t, "stack holds only pay_001 afterwards", reflect.DeepEqual(stack.Snapshot(), []string{"N1=pay_001"}))
	last, _ := stack.Pop()
	_, ok := stack.Pop()
	check(t, "pay_002 is not popped twice", last.PaymentID == "pay_001" && !ok)
	check(t, "stamp advanced past every operation", stack.Stamp() == 8)
}

func deq[T any](q *MichaelScottQueue[T]) T { v, _ := q.Dequeue(); return v }

func TestQueueBasics(t *testing.T) {
	q := NewMichaelScottQueue[string]()
	_, ok := q.Dequeue()
	check(t, "new queue is empty", q.IsEmpty() && !ok)
	q.Enqueue("a")
	q.Enqueue("b")
	q.Enqueue("c")
	check(t, "FIFO order", deq(q) == "a" && deq(q) == "b" && deq(q) == "c")
	_, ok = q.Dequeue()
	check(t, "empty again", q.IsEmpty() && !ok)
	q.Enqueue("d")
	check(t, "enqueue after draining works (sentinel moved)", deq(q) == "d")
	check(t, "no helping needed single-threaded", q.HelpedTail() == 0 && q.CasRetries() == 0)
}

func TestQueueStress(t *testing.T) {
	q := NewMichaelScottQueue[int]()
	producers, consumers, perProducer := 4, 4, 20_000
	total := producers * perProducer
	var mu sync.Mutex
	counts := map[int]int{}
	var consumed atomic.Int64
	var producersDone, consumersDone sync.WaitGroup
	for p := 0; p < producers; p++ {
		base := p * perProducer
		producersDone.Add(1)
		go func() {
			defer producersDone.Done()
			for i := 0; i < perProducer; i++ {
				q.Enqueue(base + i)
			}
		}()
	}
	for c := 0; c < consumers; c++ {
		consumersDone.Add(1)
		go func() {
			defer consumersDone.Done()
			for consumed.Load() < int64(total) {
				if v, ok := q.Dequeue(); ok {
					mu.Lock()
					counts[v]++
					mu.Unlock()
					consumed.Add(1)
				} else {
					runtime.Gosched()
				}
			}
		}()
	}
	producersDone.Wait()
	consumersDone.Wait()
	check(t, "every enqueued value was dequeued", len(counts) == total)
	once := true
	for _, n := range counts {
		once = once && n == 1
	}
	check(t, "no value was dequeued twice", once)
	check(t, "queue is empty after the stress", q.IsEmpty())
}

func TestProgressTable(t *testing.T) {
	check(t, "four progress levels, weakest first", len(Progresses) == 4 && Progresses[0] == Blocking && Progresses[3] == WaitFree)
	check(t, "lock-free is strictly weaker than wait-free", LockFree < WaitFree)
	check(t, "names match the Java enum", Blocking.String() == "BLOCKING" && ObstructionFree.String() == "OBSTRUCTION_FREE")
}
