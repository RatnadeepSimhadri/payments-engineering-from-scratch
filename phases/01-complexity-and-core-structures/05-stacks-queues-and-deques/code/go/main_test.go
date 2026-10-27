// Tests for phases/01-complexity-and-core-structures/05-stacks-queues-and-deques. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func drain[T any](r *RingBuffer[T]) string {
	var parts []string
	for {
		v, ok := r.PollFirst()
		if !ok {
			break
		}
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, " ")
}

func pollFirst[T any](r *RingBuffer[T]) T { v, _ := r.PollFirst(); return v }
func pollLast[T any](r *RingBuffer[T]) T  { v, _ := r.PollLast(); return v }
func peekFirst[T any](r *RingBuffer[T]) T { v, _ := r.PeekFirst(); return v }
func peekLast[T any](r *RingBuffer[T]) T  { v, _ := r.PeekLast(); return v }
func get[T any](r *RingBuffer[T], i int) T {
	v, _ := r.Get(i)
	return v
}

func TestStack(t *testing.T) {
	st := mustStack[int](2)
	check(t, "stack starts empty", st.IsEmpty() && st.Size() == 0)
	st.Push(1)
	st.Push(2)
	st.Push(3)
	check(t, "stack grows past initial capacity", st.Capacity() == 4 && st.Size() == 3)
	top, err := st.Peek()
	check(t, "stack peek is top", err == nil && top == 3)
	a, _ := st.Pop()
	b, _ := st.Pop()
	c, _ := st.Pop()
	check(t, "stack pops LIFO", a == 3 && b == 2 && c == 1)
	_, err = st.Pop()
	check(t, "stack pop on empty", errors.Is(err, ErrEmpty))
	_, err = st.Peek()
	check(t, "stack peek on empty", errors.Is(err, ErrEmpty))
	st.Push(7)
	st.Push(8)
	order := ""
	st.Each(func(v int) { order += fmt.Sprint(v) })
	check(t, "stack iterates top-down", order == "87")
	check(t, "brackets balanced", BracketsBalanced("{\"a\":[1,2,{\"b\":3}]}") && BracketsBalanced(""))
	check(t, "brackets unbalanced", !BracketsBalanced("{\"a\":[1,2}") && !BracketsBalanced(")(") && !BracketsBalanced("(("))
}

func TestRingBasicsAndWrap(t *testing.T) {
	r := mustRing[string](4, Reject)
	check(t, "ring starts empty, not full", r.IsEmpty() && !r.IsFull() && r.Size() == 0)
	_, ok1 := r.PollFirst()
	_, ok2 := r.PollLast()
	_, ok3 := r.PeekFirst()
	_, ok4 := r.PeekLast()
	check(t, "ring poll on empty reports absent", !ok1 && !ok2 && !ok3 && !ok4)
	check(t, "ring offers 4", r.OfferLast("a") && r.OfferLast("b") && r.OfferLast("c") && r.OfferLast("d"))
	check(t, "ring full at capacity: size == capacity, no wasted slot", r.IsFull() && r.Size() == 4 && !r.IsEmpty())
	check(t, "ring REJECT returns false when full", !r.OfferLast("e") && r.Size() == 4)
	check(t, "ring REJECT leaves content intact", peekFirst(r) == "a" && peekLast(r) == "d")
	check(t, "ring FIFO", pollFirst(r) == "a" && pollFirst(r) == "b")
	check(t, "ring sequences advance", r.HeadSequence() == 2 && r.TailSequence() == 4 && r.Size() == 2)

	// wraparound
	check(t, "ring wraps: offers land in slots 0 and 1", r.OfferLast("e") && r.OfferLast("f") && r.IndexOf(4) == 0 && r.IndexOf(5) == 1)
	check(t, "ring logical order survives the wrap", strings.HasPrefix(r.String(), "[c, d, e, f]"))
	check(t, "ring get(i) through the wrap", get(r, 0) == "c" && get(r, 2) == "e" && get(r, 3) == "f")
	_, err := r.Get(4)
	check(t, "ring get past size", errors.Is(err, ErrIndexOutOfBounds))
	seen := ""
	r.Each(func(s string) { seen += s })
	check(t, "ring iterator follows sequences across the wrap", seen == "cdef")
	check(t, "ring pollLast from the wrapped end", pollLast(r) == "f" && peekLast(r) == "e" && r.TailSequence() == 5)
	check(t, "ring drain order", drain(r) == "c d e" && r.IsEmpty())
	check(t, "ring empty after drain keeps sequences (head == tail == 5)", r.HeadSequence() == 5 && r.TailSequence() == 5)
	check(t, "ring slots view marks head and tail", mustRing[string](2, Reject).SlotsView() == "[.]T [.]")
}

// modelDeque is the slice-backed reference the ring is checked against (ArrayDeque in Java, deque in Python).
type modelDeque struct{ items []int }

func (m *modelDeque) addLast(v int)  { m.items = append(m.items, v) }
func (m *modelDeque) addFirst(v int) { m.items = append([]int{v}, m.items...) }
func (m *modelDeque) pollFirst() (int, bool) {
	if len(m.items) == 0 {
		return 0, false
	}
	v := m.items[0]
	m.items = m.items[1:]
	return v, true
}
func (m *modelDeque) pollLast() (int, bool) {
	if len(m.items) == 0 {
		return 0, false
	}
	v := m.items[len(m.items)-1]
	m.items = m.items[:len(m.items)-1]
	return v, true
}
func (m *modelDeque) peekFirst() (int, bool) {
	if len(m.items) == 0 {
		return 0, false
	}
	return m.items[0], true
}

// javaRandom is java.util.Random's 48-bit LCG, so the 2,000 operations replay exactly as in MainTest.java.
type javaRandom struct{ seed int64 }

func newJavaRandom(seed int64) *javaRandom {
	return &javaRandom{seed: (seed ^ 0x5DEECE66D) & (1<<48 - 1)}
}

func (r *javaRandom) next(bits uint) int32 {
	r.seed = (r.seed*0x5DEECE66D + 0xB) & (1<<48 - 1)
	return int32(r.seed >> (48 - bits))
}

func (r *javaRandom) nextInt(bound int32) int32 {
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 {
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

func TestRingMatchesModelOver2000Ops(t *testing.T) {
	cyc := mustRing[int](3, Reject)
	model := &modelDeque{}
	rnd := newJavaRandom(11)
	ok := true
	for i := 0; i < 2000 && ok; i++ {
		switch rnd.nextInt(4) {
		case 0:
			accepted := cyc.OfferLast(i)
			if len(model.items) < 3 {
				model.addLast(i)
				ok = ok && accepted
			} else {
				ok = ok && !accepted
			}
		case 1:
			accepted := cyc.OfferFirst(i)
			if len(model.items) < 3 {
				model.addFirst(i)
				ok = ok && accepted
			} else {
				ok = ok && !accepted
			}
		case 2:
			a, aok := cyc.PollFirst()
			b, bok := model.pollFirst()
			ok = ok && aok == bok && a == b
		default:
			a, aok := cyc.PollLast()
			b, bok := model.pollLast()
			ok = ok && aok == bok && a == b
		}
		pa, paok := cyc.PeekFirst()
		pb, pbok := model.peekFirst()
		ok = ok && cyc.Size() == len(model.items) && paok == pbok && pa == pb
	}
	check(t, "ring matches the model deque over 2000 random operations with wraparound", ok)
}

func TestDequeOperations(t *testing.T) {
	d := mustRing[int](4, Reject)
	d.OfferLast(2)
	d.OfferLast(3)
	check(t, "deque offerFirst when head is 0 rebases sequences and keeps order",
		d.OfferFirst(1) && d.OfferFirst(0) && get(d, 0) == 0 && get(d, 3) == 3 && d.Size() == 4)
	check(t, "deque full after 4", d.IsFull() && !d.OfferFirst(9))
	check(t, "deque pollLast/pollFirst", pollLast(d) == 3 && pollFirst(d) == 0 && d.Size() == 2 && peekFirst(d) == 1 && peekLast(d) == 2)
}

func TestDropOldest(t *testing.T) {
	drop := mustRing[int](4, DropOldest)
	for i := 0; i < 10; i++ {
		check(t, fmt.Sprintf("drop offer always succeeds %d", i), drop.OfferLast(i))
	}
	check(t, "drop keeps the newest capacity elements", strings.HasPrefix(drop.String(), "[6, 7, 8, 9]") && drop.Size() == 4)
	check(t, "drop counts evictions and advances head", drop.Dropped() == 6 && drop.HeadSequence() == 6 && drop.TailSequence() == 10)
	check(t, "drop offerFirst when full drops the newest", drop.OfferFirst(5) && strings.HasPrefix(drop.String(), "[5, 6, 7, 8]") && drop.Dropped() == 7)

	// construction and clear
	_, err := NewRingBuffer[int](0, Reject)
	check(t, "ring capacity must be positive", errors.Is(err, ErrCapacity))
	_, err = NewArrayStack[int](0)
	check(t, "stack capacity must be positive", errors.Is(err, ErrCapacity))
	drop.Clear()
	_, ok := drop.PollFirst()
	check(t, "clear empties and resets", drop.IsEmpty() && drop.HeadSequence() == 0 && !ok)
}
