// Lesson: phases/04-concurrency/12-testing-concurrent-code/docs/en.md
// Testing Concurrent Code: Stress, Model Checking, and Determinism
// Run:  go run .
// Test: go test ./...   (also: go test -race ./...)
// Sources: Godefroid, "Model Checking for Programming Languages using VeriSoft" (POPL 1997) for stateless
// exploration; Musuvathi et al., "Finding and Reproducing Heisenbugs in Concurrent Programs" (CHESS, OSDI 2008).
// Go 1.22+, standard library only. Same design as the Java version.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------------------------
// Part 1: a tiny machine whose scheduler is an input.
// ---------------------------------------------------------------------------------------------

// Instruction is one atomic step of a modelled thread. A real `balance += 25` is three of these (Load, Add, Store);
// a real locked block is Lock ... Unlock. The explorer may switch threads between any two.
type Instruction interface {
	fmt.Stringer
	isInstruction()
}

// Load: reg = memory[var]
type Load struct{ Var, Reg string }

// Store: memory[var] = reg
type Store struct{ Var, Reg string }

// Add: reg = reg + k
type Add struct {
	Reg string
	K   int64
}

// AtomicAdd: memory[var] += k in one indivisible step: atomic.AddInt64, Redis INCRBY, UPDATE ... SET x = x + k.
type AtomicAdd struct {
	Var string
	K   int64
}

// Lock blocks while another thread holds the lock.
type Lock struct{ Lock string }

type Unlock struct{ Lock string }

// BranchIfLess: if (reg < k) jump to instruction index `target` in this thread, else fall through.
type BranchIfLess struct {
	Reg    string
	K      int64
	Target int
}

func (Load) isInstruction()         {}
func (Store) isInstruction()        {}
func (Add) isInstruction()          {}
func (AtomicAdd) isInstruction()    {}
func (Lock) isInstruction()         {}
func (Unlock) isInstruction()       {}
func (BranchIfLess) isInstruction() {}

func (i Load) String() string         { return i.Reg + " = load " + i.Var }
func (i Store) String() string        { return "store " + i.Var + " = " + i.Reg }
func (i Add) String() string          { return fmt.Sprintf("%s += %d", i.Reg, i.K) }
func (i AtomicAdd) String() string    { return fmt.Sprintf("atomic %s += %d", i.Var, i.K) }
func (i Lock) String() string         { return "lock " + i.Lock }
func (i Unlock) String() string       { return "unlock " + i.Lock }
func (i BranchIfLess) String() string { return fmt.Sprintf("if %s < %d goto %d", i.Reg, i.K, i.Target) }

// ThreadSpec is a modelled thread: a name and a straight-line program with optional forward branches.
type ThreadSpec struct {
	Name string
	Code []Instruction
}

// Program is the whole program: initial shared memory and the threads that run against it.
// Vars keeps the memory's declaration order, which Go maps do not.
type Program struct {
	InitialMemory map[string]int64
	Vars          []string
	Threads       []ThreadSpec
}

// NewProgram copies the memory and remembers its variables in sorted order for stable printing.
func NewProgram(initialMemory map[string]int64, threads []ThreadSpec) Program {
	mem := make(map[string]int64, len(initialMemory))
	vars := make([]string, 0, len(initialMemory))
	for k, v := range initialMemory {
		mem[k] = v
		vars = append(vars, k)
	}
	sort.Strings(vars)
	return Program{mem, vars, append([]ThreadSpec(nil), threads...)}
}

var ErrOverflow = errors.New("arithmetic overflow")

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == -1<<63) || (b == -1 && a == -1<<63) {
		return 0, ErrOverflow
	}
	return product, nil
}

// InterleavingsUpperBound is the number of distinct interleavings if no instruction ever blocks: the multinomial coefficient.
func (p Program) InterleavingsUpperBound() (int64, error) {
	var total, result int64 = 0, 1
	for _, t := range p.Threads {
		for i := int64(1); i <= int64(len(t.Code)); i++ {
			total++
			product, err := mulChecked(result, total)
			if err != nil {
				return 0, err
			}
			result = product / i // exact: C(total, i) is an integer
		}
	}
	return result, nil
}

// TraceStep is one scheduling decision: which thread ran, and what it executed.
type TraceStep struct {
	Thread      string
	Instruction Instruction
}

func (s TraceStep) String() string { return s.Thread + ": " + s.Instruction.String() }

// Invariant is a named end-state predicate.
type Invariant struct {
	Name  string
	Holds func(memory map[string]int64) bool
}

var (
	ErrNotEnabled   = errors.New("thread is not enabled")
	ErrUnlockNoHold = errors.New("unlock of a lock not held")
)

// Machine: shared memory, one program counter and register file per thread, lock ownership.
// It never spawns a goroutine: Step(i) runs exactly one instruction of thread i on the caller's goroutine.
type Machine struct {
	program   Program
	Memory    map[string]int64
	pc        []int
	registers []map[string]int64
	lockOwner map[string]int
	trace     []TraceStep
}

func NewMachine(program Program) *Machine {
	m := &Machine{program: program, Memory: map[string]int64{}, pc: make([]int, len(program.Threads)), lockOwner: map[string]int{}}
	for k, v := range program.InitialMemory {
		m.Memory[k] = v
	}
	for range program.Threads {
		m.registers = append(m.registers, map[string]int64{})
	}
	return m
}

func (m *Machine) Finished(t int) bool { return m.pc[t] >= len(m.program.Threads[t].Code) }

func (m *Machine) AllFinished() bool {
	for t := range m.pc {
		if !m.Finished(t) {
			return false
		}
	}
	return true
}

// Enabled: a thread is enabled when it has an instruction left and that instruction can run now.
func (m *Machine) Enabled(t int) bool {
	if m.Finished(t) {
		return false
	}
	if l, ok := m.program.Threads[t].Code[m.pc[t]].(Lock); ok {
		owner, held := m.lockOwner[l.Lock]
		return !held || owner == t
	}
	return true
}

func (m *Machine) EnabledThreads() []int {
	out := []int{}
	for t := range m.pc {
		if m.Enabled(t) {
			out = append(out, t)
		}
	}
	return out
}

// Deadlocked: nobody finished everything, yet nobody can move.
func (m *Machine) Deadlocked() bool { return !m.AllFinished() && len(m.EnabledThreads()) == 0 }

func (m *Machine) Step(t int) error {
	if !m.Enabled(t) {
		return fmt.Errorf("%w: thread %d", ErrNotEnabled, t)
	}
	spec := m.program.Threads[t]
	ins := spec.Code[m.pc[t]]
	regs := m.registers[t]
	next := m.pc[t] + 1
	switch i := ins.(type) {
	case Load:
		regs[i.Reg] = m.Memory[i.Var]
	case Store:
		m.Memory[i.Var] = regs[i.Reg]
	case Add:
		sum, err := addChecked(regs[i.Reg], i.K)
		if err != nil {
			return err
		}
		regs[i.Reg] = sum
	case AtomicAdd:
		sum, err := addChecked(m.Memory[i.Var], i.K)
		if err != nil {
			return err
		}
		m.Memory[i.Var] = sum
	case Lock:
		m.lockOwner[i.Lock] = t
	case Unlock:
		if owner, held := m.lockOwner[i.Lock]; !held || owner != t {
			return fmt.Errorf("%w: %s unlocks %s it does not hold", ErrUnlockNoHold, spec.Name, i.Lock)
		}
		delete(m.lockOwner, i.Lock)
	case BranchIfLess:
		if regs[i.Reg] < i.K {
			next = i.Target
		}
	}
	m.pc[t] = next
	m.trace = append(m.trace, TraceStep{spec.Name, ins})
	return nil
}

func (m *Machine) Trace() []TraceStep { return append([]TraceStep(nil), m.trace...) }

func (m *Machine) MemorySnapshot() map[string]int64 {
	out := make(map[string]int64, len(m.Memory))
	for k, v := range m.Memory {
		out[k] = v
	}
	return out
}

// FormatMemory prints memory like Java's LinkedHashMap: {a=100, b=100}, keys in the program's variable order.
func FormatMemory(memory map[string]int64) string {
	keys := make([]string, 0, len(memory))
	for k := range memory {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, memory[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ExplorationResult is what an exploration found. Counterexample is the first schedule that broke the invariant or
// deadlocked; it and FinalMemory are nil when the exploration was clean (Java's Optional.empty()).
type ExplorationResult struct {
	SchedulesExplored int64
	Violations        int64
	Deadlocks         int64
	Counterexample    []TraceStep
	FinalMemory       map[string]int64
}

func (r ExplorationResult) Clean() bool { return r.Violations == 0 && r.Deadlocks == 0 }

// Explorer: Exhaustive enumerates every interleaving by depth-first search over scheduling choices and replays each
// from the initial state (stateless model checking). Random samples schedules with a seeded generator for programs
// too large to enumerate. Both are single-goroutine and deterministic: the same program and seed give the same result.
type Explorer struct {
	explored, violations, deadlocks int64
	counterexample                  []TraceStep
	finalMemory                     map[string]int64
}

func Exhaustive(program Program, invariant Invariant) (ExplorationResult, error) {
	e := &Explorer{}
	if err := e.dfs(program, invariant, nil); err != nil {
		return ExplorationResult{}, err
	}
	return e.result(), nil
}

func Random(program Program, invariant Invariant, seed int64, schedules int) (ExplorationResult, error) {
	e := &Explorer{}
	rng := NewJavaRandom(seed)
	for i := 0; i < schedules; i++ {
		m := NewMachine(program)
		for !m.AllFinished() && !m.Deadlocked() {
			choices := m.EnabledThreads()
			if err := m.Step(choices[rng.NextInt(int32(len(choices)))]); err != nil {
				return ExplorationResult{}, err
			}
		}
		e.record(m, invariant)
	}
	return e.result(), nil
}

func (e *Explorer) dfs(program Program, invariant Invariant, prefix []int) error {
	m, err := replay(program, prefix)
	if err != nil {
		return err
	}
	if m.AllFinished() || m.Deadlocked() {
		e.record(m, invariant)
		return nil
	}
	for _, t := range m.EnabledThreads() {
		if err := e.dfs(program, invariant, append(prefix, t)); err != nil {
			return err
		}
	}
	return nil
}

func replay(program Program, schedule []int) (*Machine, error) {
	m := NewMachine(program)
	for _, t := range schedule {
		if err := m.Step(t); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (e *Explorer) record(m *Machine, invariant Invariant) {
	e.explored++
	bad := false
	if m.Deadlocked() {
		e.deadlocks++
		bad = true
	} else if !invariant.Holds(m.Memory) {
		e.violations++
		bad = true
	}
	if bad && e.counterexample == nil {
		e.counterexample = m.Trace()
		e.finalMemory = m.MemorySnapshot()
	}
}

func (e *Explorer) result() ExplorationResult {
	return ExplorationResult{e.explored, e.violations, e.deadlocks, e.counterexample, e.finalMemory}
}

// --- The programs this lesson checks. Every one mirrors a real type from Lesson 02 or Lesson 07. ---

// RacyIncrement: two threads each do `balance += delta` as load/add/store. The Lesson 02 lost update.
func RacyIncrement(initial, delta int64) Program {
	body := []Instruction{Load{"balance", "r"}, Add{"r", delta}, Store{"balance", "r"}}
	return NewProgram(map[string]int64{"balance": initial}, []ThreadSpec{{"T1", body}, {"T2", body}})
}

// LockedIncrement: the same increment under one lock: Lesson 02's LockedCounter.
func LockedIncrement(initial, delta int64) Program {
	body := []Instruction{Lock{"L"}, Load{"balance", "r"}, Add{"r", delta}, Store{"balance", "r"}, Unlock{"L"}}
	return NewProgram(map[string]int64{"balance": initial}, []ThreadSpec{{"T1", body}, {"T2", body}})
}

// CheckThenActDebit: two debits that each check the balance, then subtract atomically. "A bare atomic cannot do a debit."
func CheckThenActDebit(initial, debit int64) Program {
	body := []Instruction{
		Load{"balance", "r"},         // 0: check...
		BranchIfLess{"r", debit, 3},  // 1: insufficient -> skip to end
		AtomicAdd{"balance", -debit}, // 2: ...then act, atomically but too late
	}
	return NewProgram(map[string]int64{"balance": initial}, []ThreadSpec{{"D1", body}, {"D2", body}})
}

// TransferUnordered: transfer A->B while B->A, each locking source then destination. Lesson 07's deadlock.
func TransferUnordered() Program {
	ab := []Instruction{Lock{"A"}, Lock{"B"}, Load{"a", "x"}, Add{"x", -10}, Store{"a", "x"},
		Load{"b", "y"}, Add{"y", 10}, Store{"b", "y"}, Unlock{"B"}, Unlock{"A"}}
	ba := []Instruction{Lock{"B"}, Lock{"A"}, Load{"b", "x"}, Add{"x", -10}, Store{"b", "x"},
		Load{"a", "y"}, Add{"y", 10}, Store{"a", "y"}, Unlock{"A"}, Unlock{"B"}}
	return NewProgram(map[string]int64{"a": 100, "b": 100}, []ThreadSpec{{"AtoB", ab}, {"BtoA", ba}})
}

// TransferOrdered: the same two transfers with locks always taken in alphabetical order. Lesson 07's fix.
func TransferOrdered() Program {
	ab := []Instruction{Lock{"A"}, Lock{"B"}, Load{"a", "x"}, Add{"x", -10}, Store{"a", "x"},
		Load{"b", "y"}, Add{"y", 10}, Store{"b", "y"}, Unlock{"B"}, Unlock{"A"}}
	ba := []Instruction{Lock{"A"}, Lock{"B"}, Load{"b", "x"}, Add{"x", -10}, Store{"b", "x"},
		Load{"a", "y"}, Add{"y", 10}, Store{"a", "y"}, Unlock{"B"}, Unlock{"A"}}
	return NewProgram(map[string]int64{"a": 100, "b": 100}, []ThreadSpec{{"AtoB", ab}, {"BtoA", ba}})
}

func BalanceEquals(expected int64) Invariant {
	return Invariant{fmt.Sprintf("balance == %d", expected), func(m map[string]int64) bool { return m["balance"] == expected }}
}

func BalanceNonNegative() Invariant {
	return Invariant{"balance >= 0", func(m map[string]int64) bool { return m["balance"] >= 0 }}
}

func Conserved(total int64) Invariant {
	return Invariant{fmt.Sprintf("a + b == %d", total), func(m map[string]int64) bool { return m["a"]+m["b"] == total }}
}

// ---------------------------------------------------------------------------------------------
// Part 2: a stress harness with real goroutines, for the code the model abstracts.
// ---------------------------------------------------------------------------------------------

type BalanceCounter interface {
	Add(delta int64)
	Get() int64
}

// RacyCounter is Lesson 02's bug, unchanged. `go test -race` reports the data race in Add.
type RacyCounter struct{ balance int64 }

func (c *RacyCounter) Add(delta int64) { loaded := c.balance; c.balance = loaded + delta }
func (c *RacyCounter) Get() int64      { return c.balance }

// LockedCounter is Lesson 02's first fix, unchanged.
type LockedCounter struct {
	mu      sync.Mutex
	balance int64
}

func (c *LockedCounter) Add(delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balance += delta
}

func (c *LockedCounter) Get() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balance
}

type StressResult struct {
	Rounds                int
	RoundsWithLostUpdates int
	TotalLost             int64
	ExpectedPerRound      int64
}

// StressRun runs `threads` goroutines that each add `delta` `opsPerThread` times, `rounds` times over, releasing
// every round through a closed channel (the starting gun, Go's CyclicBarrier) so the goroutines collide as hard as
// the hardware allows. A stress test can show a race; it can never show the absence of one.
func StressRun(factory func() BalanceCounter, threads, opsPerThread int, delta int64, rounds int) StressResult {
	expected := int64(threads) * int64(opsPerThread) * delta
	badRounds := 0
	var totalLost int64
	for r := 0; r < rounds; r++ {
		counter := factory()
		start := make(chan struct{})
		var ready, done sync.WaitGroup
		ready.Add(threads)
		done.Add(threads)
		for i := 0; i < threads; i++ {
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				for k := 0; k < opsPerThread; k++ {
					counter.Add(delta)
				}
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		lost := expected - counter.Get()
		if lost != 0 {
			badRounds++
			totalLost += lost
		}
	}
	return StressResult{rounds, badRounds, totalLost, expected}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func report(title string, p Program, r ExplorationResult) {
	fmt.Println("-- " + title + " --")
	fmt.Printf("interleavings (upper bound) = %d, explored = %d, violations = %d, deadlocks = %d\n",
		must(p.InterleavingsUpperBound()), r.SchedulesExplored, r.Violations, r.Deadlocks)
	if r.Counterexample != nil {
		fmt.Println("first counterexample, final memory " + FormatMemory(r.FinalMemory) + ":")
		for _, s := range r.Counterexample {
			fmt.Println("    " + s.String())
		}
	}
}

func main() {
	fmt.Println("== Exhaustive: balance += 25.00 USD twice, from 100.00 USD (minor units) ==")
	racy := RacyIncrement(10_000, 2_500)
	report("racy load/add/store", racy, must(Exhaustive(racy, BalanceEquals(15_000))))
	locked := LockedIncrement(10_000, 2_500)
	report("same increment under a lock", locked, must(Exhaustive(locked, BalanceEquals(15_000))))

	fmt.Println()
	fmt.Println("== Exhaustive: two 1.00 USD debits from 1.50 USD, each checks first ==")
	debit := CheckThenActDebit(150, 100)
	report("check-then-act debit", debit, must(Exhaustive(debit, BalanceNonNegative())))

	fmt.Println()
	fmt.Println("== Exhaustive: transfer A->B while B->A ==")
	unordered := TransferUnordered()
	report("locks taken source-then-destination", unordered, must(Exhaustive(unordered, Conserved(200))))
	ordered := TransferOrdered()
	report("locks taken in a global order", ordered, must(Exhaustive(ordered, Conserved(200))))

	fmt.Println()
	fmt.Println("== Random schedules (seed 42): same bug, sampled instead of enumerated ==")
	sampled := must(Random(racy, BalanceEquals(15_000), 42, 1_000))
	fmt.Printf("1000 random schedules of the racy program: violations = %d\n", sampled.Violations)
	again := must(Random(racy, BalanceEquals(15_000), 42, 1_000))
	fmt.Printf("same seed again: violations = %d (identical: %t)\n", again.Violations, sameResult(sampled, again))

	fmt.Println()
	fmt.Println("== Stress with real goroutines: 4 goroutines x 20000 adds, 20 rounds, barrier start ==")
	racyStress := StressRun(func() BalanceCounter { return &RacyCounter{} }, 4, 20_000, 1, 20)
	lockedStress := StressRun(func() BalanceCounter { return &LockedCounter{} }, 4, 20_000, 1, 20)
	fmt.Printf("racy  : rounds with lost updates = %d of %d (nondeterministic: this line may differ between runs)\n",
		racyStress.RoundsWithLostUpdates, racyStress.Rounds)
	fmt.Printf("locked: rounds with lost updates = %d of %d (the lock makes this exact)\n",
		lockedStress.RoundsWithLostUpdates, lockedStress.Rounds)
	fmt.Printf("The model checker proved the racy program wrong in %d schedules; the stress test only made it likely.\n",
		must(Exhaustive(racy, BalanceEquals(15_000))).SchedulesExplored)
}

// sameResult compares two results field by field, including the counterexample trace and final memory.
func sameResult(a, b ExplorationResult) bool {
	if a.SchedulesExplored != b.SchedulesExplored || a.Violations != b.Violations || a.Deadlocks != b.Deadlocks {
		return false
	}
	if len(a.Counterexample) != len(b.Counterexample) || len(a.FinalMemory) != len(b.FinalMemory) {
		return false
	}
	for i := range a.Counterexample {
		if a.Counterexample[i] != b.Counterexample[i] {
			return false
		}
	}
	for k, v := range a.FinalMemory {
		if b.FinalMemory[k] != v {
			return false
		}
	}
	return true
}
