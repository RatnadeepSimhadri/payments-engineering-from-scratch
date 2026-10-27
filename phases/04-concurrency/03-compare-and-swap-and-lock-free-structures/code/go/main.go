// Lesson: phases/04-concurrency/03-compare-and-swap-and-lock-free-structures/docs/en.md
// Compare-and-Swap and Lock-Free Structures
// Run:  go run .
// Test: go test ./...
// Sources: Treiber, "Systems Programming: Coping with Parallelism" (IBM RJ 5118, 1986); Michael & Scott,
// "Simple, Fast, and Practical Non-Blocking and Blocking Concurrent Queue Algorithms" (PODC 1996);
// Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 3 and 11; the sync/atomic package documentation.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// treiberNode is one cell of a TreiberStack. next is written once, before the node is published by CAS.
type treiberNode[T any] struct {
	value T
	next  *treiberNode[T]
}

// TreiberStack is Treiber's lock-free stack. Every Push and Pop is one CAS on head; a failed CAS means
// another goroutine published first, so the loop re-reads and tries again. Nodes are freshly allocated
// and never reused, which is what makes this version immune to the ABA problem under a garbage collector.
type TreiberStack[T any] struct {
	head       atomic.Pointer[treiberNode[T]]
	casRetries atomic.Int64
}

// Push publishes a new node with one CAS.
func (s *TreiberStack[T]) Push(value T) {
	node := &treiberNode[T]{value: value}
	for {
		current := s.head.Load()
		node.next = current // link before publishing: next is set, then CAS publishes the node
		if s.head.CompareAndSwap(current, node) {
			return
		}
		s.casRetries.Add(1)
	}
}

// Pop removes the top; ok is false when the stack is empty.
func (s *TreiberStack[T]) Pop() (T, bool) {
	for {
		current := s.head.Load()
		if current == nil {
			var zero T
			return zero, false
		}
		next := current.next
		if s.head.CompareAndSwap(current, next) {
			return current.value, true
		}
		s.casRetries.Add(1)
	}
}

// Peek reads the top without removing it.
func (s *TreiberStack[T]) Peek() (T, bool) {
	current := s.head.Load()
	if current == nil {
		var zero T
		return zero, false
	}
	return current.value, true
}

// IsEmpty reports whether head is nil.
func (s *TreiberStack[T]) IsEmpty() bool { return s.head.Load() == nil }

// Size is an O(n) snapshot for tests and demos; not linearizable with respect to concurrent pushes.
func (s *TreiberStack[T]) Size() int {
	n := 0
	for cur := s.head.Load(); cur != nil; cur = cur.next {
		n++
	}
	return n
}

// CasRetries is how many CASes lost to another goroutine.
func (s *TreiberStack[T]) CasRetries() int64 { return s.casRetries.Load() }

// CaptureJob is a capture job popped from a work stack. The payments payload for the ABA demonstration.
type CaptureJob struct {
	PaymentID   string
	AmountMinor int64
}

// PooledNode is a recycled stack cell with a stable identity so the demo can print "N3" and show the same node reappearing.
type PooledNode struct {
	ID    int
	value CaptureJob
	next  *PooledNode
}

// PooledStack is a Treiber stack that recycles its nodes through a free list, the way a C implementation or an
// allocation-free hot path would. Recycling is exactly what re-enables the ABA problem: a node that
// was popped can be pushed again, so head can hold the same pointer with different contents.
//
// beforeCas is a test seam: it runs between the read of head and the CAS in Pop,
// so a test can perform a complete pop/pop/push sequence at precisely the vulnerable instant, in one goroutine.
type PooledStack struct {
	head      atomic.Pointer[PooledNode]
	mu        sync.Mutex
	freeList  []*PooledNode // FIFO: the oldest freed node is reused first
	beforeCas func()
	nextID    int
}

// NewPooledStack installs the seam; pass nil for no seam.
func NewPooledStack(beforeCas func()) *PooledStack {
	if beforeCas == nil {
		beforeCas = func() {}
	}
	return &PooledStack{beforeCas: beforeCas, nextID: 1}
}

func (s *PooledStack) allocate(value CaptureJob) *PooledNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	var node *PooledNode
	if len(s.freeList) == 0 {
		node = &PooledNode{ID: s.nextID}
		s.nextID++
	} else {
		node = s.freeList[0]
		s.freeList = s.freeList[1:]
	}
	node.value = value
	return node
}

func (s *PooledStack) free(node *PooledNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeList = append(s.freeList, node)
}

// Push takes a node from the free list and publishes it.
func (s *PooledStack) Push(value CaptureJob) {
	node := s.allocate(value)
	for {
		current := s.head.Load()
		node.next = current
		if s.head.CompareAndSwap(current, node) {
			return
		}
	}
}

// Pop unlinks the top and returns its node to the free list.
func (s *PooledStack) Pop() (CaptureJob, bool) {
	for {
		current := s.head.Load()
		if current == nil {
			return CaptureJob{}, false
		}
		next := current.next
		s.beforeCas() // the gap where ABA happens
		if s.head.CompareAndSwap(current, next) {
			value := current.value
			s.free(current)
			return value, true
		}
	}
}

// HeadNode exposes the top node for the ABA test.
func (s *PooledStack) HeadNode() *PooledNode { return s.head.Load() }

// Snapshot renders the stack top-down as "N<id>=<payment>".
func (s *PooledStack) Snapshot() []string {
	out := []string{}
	for cur := s.head.Load(); cur != nil; cur = cur.next {
		out = append(out, fmt.Sprintf("N%d=%s", cur.ID, cur.value.PaymentID))
	}
	return out
}

// stamped is a (pointer, stamp) pair. Go has no double-word CAS, so the pair lives behind one atomic.Pointer
// and every successful CAS installs a freshly allocated pair; this is how AtomicStampedReference works internally too.
type stamped struct {
	node  *PooledNode
	stamp int
}

// StampedPooledStack is the same pooled stack with the fix: head carries a stamp. Every successful
// CAS increments the stamp, so a CAS that expects (N3, stamp 3) fails if the stack has moved on to (N3, stamp 6),
// even though the node pointer is the same. This is the version-counter idea that databases call optimistic locking.
type StampedPooledStack struct {
	head             atomic.Pointer[stamped]
	mu               sync.Mutex
	freeList         []*PooledNode
	beforeCas        func()
	staleCasRejected atomic.Int64
	nextID           int
}

// NewStampedPooledStack installs the seam; pass nil for no seam.
func NewStampedPooledStack(beforeCas func()) *StampedPooledStack {
	if beforeCas == nil {
		beforeCas = func() {}
	}
	s := &StampedPooledStack{beforeCas: beforeCas, nextID: 1}
	s.head.Store(&stamped{nil, 0})
	return s
}

func (s *StampedPooledStack) allocate(value CaptureJob) *PooledNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	var node *PooledNode
	if len(s.freeList) == 0 {
		node = &PooledNode{ID: s.nextID}
		s.nextID++
	} else {
		node = s.freeList[0]
		s.freeList = s.freeList[1:]
	}
	node.value = value
	return node
}

func (s *StampedPooledStack) free(node *PooledNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeList = append(s.freeList, node)
}

// compareAndSet is AtomicStampedReference.compareAndSet: both the node and the stamp must match the pair read.
func (s *StampedPooledStack) compareAndSet(seen *stamped, expectedNode, newNode *PooledNode, expectedStamp, newStamp int) bool {
	if seen.node != expectedNode || seen.stamp != expectedStamp {
		return false
	}
	return s.head.CompareAndSwap(seen, &stamped{newNode, newStamp})
}

// Push publishes a node and bumps the stamp.
func (s *StampedPooledStack) Push(value CaptureJob) {
	node := s.allocate(value)
	for {
		seen := s.head.Load()
		node.next = seen.node
		if s.compareAndSet(seen, seen.node, node, seen.stamp, seen.stamp+1) {
			return
		}
	}
}

// Pop unlinks the top; a stale (node, stamp) pair is rejected and retried.
func (s *StampedPooledStack) Pop() (CaptureJob, bool) {
	for {
		seen := s.head.Load()
		current := seen.node
		if current == nil {
			return CaptureJob{}, false
		}
		next := current.next
		s.beforeCas()
		if s.compareAndSet(seen, current, next, seen.stamp, seen.stamp+1) {
			value := current.value
			s.free(current)
			return value, true
		}
		s.staleCasRejected.Add(1)
	}
}

// Stamp is the number of successful CASes so far.
func (s *StampedPooledStack) Stamp() int { return s.head.Load().stamp }

// StaleCasRejected counts CASes that failed on the stamp or the node.
func (s *StampedPooledStack) StaleCasRejected() int64 { return s.staleCasRejected.Load() }

// Snapshot renders the stack top-down.
func (s *StampedPooledStack) Snapshot() []string {
	out := []string{}
	for cur := s.head.Load().node; cur != nil; cur = cur.next {
		out = append(out, fmt.Sprintf("N%d=%s", cur.ID, cur.value.PaymentID))
	}
	return out
}

// msNode is one cell of a MichaelScottQueue; next is atomic because enqueue CASes it.
type msNode[T any] struct {
	value T
	next  atomic.Pointer[msNode[T]]
}

// MichaelScottQueue is the Michael-Scott lock-free FIFO queue. Two pointers, head (a sentinel whose next is the
// first real element) and tail (the last node, or one behind it). Enqueue links the new node to tail.next with a CAS
// and then swings tail; if another goroutine finds tail lagging it helps by swinging tail itself before retrying.
type MichaelScottQueue[T any] struct {
	head       atomic.Pointer[msNode[T]]
	tail       atomic.Pointer[msNode[T]]
	casRetries atomic.Int64
	helpedTail atomic.Int64
}

// NewMichaelScottQueue installs the sentinel.
func NewMichaelScottQueue[T any]() *MichaelScottQueue[T] {
	q := &MichaelScottQueue[T]{}
	sentinel := &msNode[T]{}
	q.head.Store(sentinel)
	q.tail.Store(sentinel)
	return q
}

// Enqueue appends with two CASes: link, then swing tail.
func (q *MichaelScottQueue[T]) Enqueue(value T) {
	node := &msNode[T]{value: value}
	for {
		last := q.tail.Load()
		next := last.next.Load()
		if last != q.tail.Load() { // tail moved under us; re-read
			q.casRetries.Add(1)
			continue
		}
		if next == nil {
			if last.next.CompareAndSwap(nil, node) { // step 1: link the node (linearization point)
				q.tail.CompareAndSwap(last, node) // step 2: swing tail; failure is fine, someone helped
				return
			}
			q.casRetries.Add(1)
		} else {
			q.helpedTail.Add(1)
			q.tail.CompareAndSwap(last, next) // tail is lagging: help, then retry
		}
	}
}

// Dequeue removes the oldest element; ok is false when the queue is empty.
func (q *MichaelScottQueue[T]) Dequeue() (T, bool) {
	for {
		first := q.head.Load()
		last := q.tail.Load()
		next := first.next.Load()
		if first != q.head.Load() {
			q.casRetries.Add(1)
			continue
		}
		if next == nil { // sentinel has no successor: empty
			var zero T
			return zero, false
		}
		if first == last { // tail lagging behind a real node: help
			q.helpedTail.Add(1)
			q.tail.CompareAndSwap(last, next)
			continue
		}
		value := next.value // read before the CAS: after it, next is the new sentinel
		if q.head.CompareAndSwap(first, next) {
			return value, true
		}
		q.casRetries.Add(1)
	}
}

// IsEmpty reports whether the sentinel has no successor.
func (q *MichaelScottQueue[T]) IsEmpty() bool { return q.head.Load().next.Load() == nil }

func (q *MichaelScottQueue[T]) CasRetries() int64 { return q.casRetries.Load() }
func (q *MichaelScottQueue[T]) HelpedTail() int64 { return q.helpedTail.Load() }

// Progress is one of the four standard progress guarantees, from weakest to strongest.
type Progress int

const (
	Blocking Progress = iota
	ObstructionFree
	LockFree
	WaitFree
)

// Progresses lists every guarantee in order, like Progress.values().
var Progresses = []Progress{Blocking, ObstructionFree, LockFree, WaitFree}

func (p Progress) String() string {
	return [...]string{"BLOCKING", "OBSTRUCTION_FREE", "LOCK_FREE", "WAIT_FREE"}[p]
}

// Meaning is the one-line definition.
func (p Progress) Meaning() string {
	return [...]string{
		"a thread holding a lock that is descheduled blocks every other thread",
		"a thread running alone for long enough completes; contention may livelock",
		"some thread completes in a bounded number of steps; an individual thread may starve",
		"every thread completes in a bounded number of its own steps",
	}[p]
}

// StressStack runs workers goroutines that each perform perWorker pushes and pops; returns the elapsed duration.
func StressStack(stack *TreiberStack[int64], workers, perWorker int) time.Duration {
	start := make(chan struct{})
	var done sync.WaitGroup
	var popped atomic.Int64
	for t := 0; t < workers; t++ {
		base := int64(t) * int64(perWorker)
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			for i := 0; i < perWorker; i++ {
				stack.Push(base + int64(i))
				if _, ok := stack.Pop(); ok {
					popped.Add(1)
				}
			}
		}()
	}
	t0 := time.Now()
	close(start)
	done.Wait()
	return time.Since(t0)
}

func orEmpty(v string, ok bool) string {
	if !ok {
		return "empty"
	}
	return v
}

func list(xs []string) string { return "[" + strings.Join(xs, ", ") + "]" }

func demoTreiber() {
	fmt.Println("== A lock-free stack: one CAS per push, one per pop ==")
	stack := &TreiberStack[string]{}
	stack.Push("pay_001")
	stack.Push("pay_002")
	stack.Push("pay_003")
	fmt.Printf("pushed pay_001, pay_002, pay_003; size = %d\n", stack.Size())
	a, okA := stack.Pop()
	b, okB := stack.Pop()
	fmt.Printf("pop -> %s, pop -> %s\n", orEmpty(a, okA), orEmpty(b, okB))
	p, okP := stack.Peek()
	fmt.Printf("peek -> %s, size = %d\n", orEmpty(p, okP), stack.Size())
	_, ok := (&TreiberStack[string]{}).Pop()
	fmt.Printf("pop on empty -> ok=%v\n", ok)
	fmt.Println()
}

func demoAba() {
	fmt.Println("== ABA: the same node, a different stack ==")
	var stack *PooledStack
	armed := true
	stack = NewPooledStack(func() {
		if !armed {
			return
		}
		armed = false // only the first pop is interrupted
		fmt.Println("  thread A paused between reading head=N3 and its CAS")
		v1, ok1 := stack.Pop()
		fmt.Printf("  thread B: pop -> %s  (N3 goes to the free list)\n", orEmpty(v1.PaymentID, ok1))
		v2, ok2 := stack.Pop()
		fmt.Printf("  thread B: pop -> %s  (N2 goes to the free list)\n", orEmpty(v2.PaymentID, ok2))
		stack.Push(CaptureJob{"pay_004", 4_000})
		fmt.Printf("  thread B: push pay_004 reuses N3; stack is now %s\n", list(stack.Snapshot()))
	})
	stack.Push(CaptureJob{"pay_001", 1_000})
	stack.Push(CaptureJob{"pay_002", 2_000})
	stack.Push(CaptureJob{"pay_003", 3_000})
	fmt.Printf("before: %s\n", list(stack.Snapshot()))
	got, _ := stack.Pop()
	fmt.Printf("thread A resumes: CAS(head, N3 -> N2) succeeds, returns %s\n", got.PaymentID)
	fmt.Printf("after:  %s   <- pay_002 is back on the stack and will be captured twice\n", list(stack.Snapshot()))
	fmt.Println()
}

func demoStamped() {
	fmt.Println("== The fix: a stamp that changes on every CAS ==")
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
		fmt.Printf("  thread B: pop, pop, push pay_004 -> stamp is now %d, stack %s\n", stack.Stamp(), list(stack.Snapshot()))
	})
	stack.Push(CaptureJob{"pay_001", 1_000})
	stack.Push(CaptureJob{"pay_002", 2_000})
	stack.Push(CaptureJob{"pay_003", 3_000})
	fmt.Printf("before: %s stamp = %d\n", list(stack.Snapshot()), stack.Stamp())
	got, _ := stack.Pop()
	fmt.Printf("thread A: CAS expecting (N3, stamp 3) rejected (%d stale CAS), retried, returned %s\n", stack.StaleCasRejected(), got.PaymentID)
	fmt.Printf("after:  %s   <- every job captured exactly once\n", list(stack.Snapshot()))
	fmt.Println()
}

func demoQueue() {
	fmt.Println("== Michael-Scott queue: two CAS pointers and a helping step ==")
	queue := NewMichaelScottQueue[int]()
	for i := 1; i <= 3; i++ {
		queue.Enqueue(i)
	}
	a, _ := queue.Dequeue()
	b, _ := queue.Dequeue()
	c, _ := queue.Dequeue()
	_, ok := queue.Dequeue()
	fmt.Printf("enqueue 1,2,3; dequeue -> %d, %d, %d, then ok=%v\n", a, b, c, ok)

	producers, perProducer := 4, 20_000
	var done sync.WaitGroup
	for p := 0; p < producers; p++ {
		base := p * perProducer
		done.Add(1)
		go func() {
			defer done.Done()
			for i := 0; i < perProducer; i++ {
				queue.Enqueue(base + i)
			}
		}()
	}
	done.Wait()
	seen := make([]bool, producers*perProducer)
	count := 0
	for v, ok := queue.Dequeue(); ok; v, ok = queue.Dequeue() {
		seen[v] = true
		count++
	}
	allSeen := true
	for _, b := range seen {
		allSeen = allSeen && b
	}
	fmt.Printf("%d producers x %d enqueues -> dequeued %d, every value exactly once: %v\n", producers, perProducer, count, allSeen)
	fmt.Println()
}

func demoProgress() {
	fmt.Println("== Progress guarantees ==")
	for _, p := range Progresses {
		fmt.Printf("%-17v %s\n", p, p.Meaning())
	}
	fmt.Println("Treiber stack and Michael-Scott queue: LOCK_FREE (a CAS fails only because another goroutine's succeeded).")
	fmt.Println()
}

func main() {
	demoTreiber()
	demoAba()
	demoStamped()
	demoQueue()

	fmt.Println("== Stress: 4 goroutines x 50,000 push/pop pairs on one Treiber stack ==")
	stack := &TreiberStack[int64]{}
	StressStack(stack, 4, 50_000)
	fmt.Printf("stack empty afterwards: %v; every push matched by a pop, nothing lost, nothing duplicated\n", stack.IsEmpty())
	fmt.Println("(CAS retries are counted by stack.CasRetries(); the number depends on the scheduler, which is the point)")
	fmt.Println()

	demoProgress()
}
