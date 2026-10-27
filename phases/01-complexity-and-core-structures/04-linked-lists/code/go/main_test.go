// Tests for phases/01-complexity-and-core-structures/04-linked-lists. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func singlyGet(t *testing.T, s *SinglyLinkedList[int], i int) int {
	t.Helper()
	v, err := s.Get(i)
	if err != nil {
		t.Fatalf("Get(%d): %v", i, err)
	}
	return v
}

func TestSinglyLinkedList(t *testing.T) {
	s := &SinglyLinkedList[int]{}
	check(t, "singly starts empty", s.IsEmpty() && s.Size() == 0)
	s.AddLast(2)
	s.AddLast(3)
	s.AddFirst(1)
	check(t, "singly addFirst/addLast order", s.String() == "[1, 2, 3]" && s.Size() == 3)
	check(t, "singly get walks to index", singlyGet(t, s, 0) == 1 && singlyGet(t, s, 2) == 3)
	_, err := s.Get(3)
	check(t, "singly get out of range", errors.Is(err, ErrIndexOutOfBounds))
	first, err := s.RemoveFirst()
	check(t, "singly removeFirst", err == nil && first == 1 && s.Size() == 2)
	check(t, "singly remove by value in middle", s.RemoveFirstOccurrence(2) && s.String() == "[3]")
	check(t, "singly remove missing value is false", !s.RemoveFirstOccurrence(99))
	check(t, "singly remove tail updates tail", s.RemoveFirstOccurrence(3) && s.IsEmpty())
	s.AddLast(10)
	check(t, "singly addLast after emptying works (tail was reset)", singlyGet(t, s, 0) == 10 && s.Size() == 1)
	_, err = (&SinglyLinkedList[int]{}).RemoveFirst()
	check(t, "singly removeFirst on empty", errors.Is(err, ErrEmpty))
	r := &SinglyLinkedList[int]{}
	for i := 1; i <= 5; i++ {
		r.AddLast(i)
	}
	r.Reverse()
	check(t, "singly reverse", r.String() == "[5, 4, 3, 2, 1]")
	r.AddLast(0)
	check(t, "singly reverse keeps tail correct", r.String() == "[5, 4, 3, 2, 1, 0]")
}

func TestDoublyLinkedList(t *testing.T) {
	d := NewDoublyLinkedList[string]()
	check(t, "doubly empty invariant", d.InvariantHolds() && d.First() == nil && d.Last() == nil)
	a := d.AddLast("a")
	c := d.AddLast("c")
	b, err := d.InsertBefore(c, "b")
	check(t, "doubly insertBefore", err == nil && d.String() == "[a, b, c]" && d.Size() == 3)
	check(t, "doubly invariant after inserts", d.InvariantHolds())
	check(t, "doubly first/last", d.First() == a && d.Last() == c)
	v, err := d.Unlink(b)
	check(t, "doubly unlink middle is O(1) and returns value", err == nil && v == "b" && d.String() == "[a, c]")
	check(t, "doubly unlinked node is disowned", b.owner == nil && b.prev == nil && b.next == nil)
	_, err = d.Unlink(b)
	check(t, "doubly unlink twice rejected", errors.Is(err, ErrNotOwned))
	_, err = NewDoublyLinkedList[string]().Unlink(a)
	check(t, "doubly unlink foreign node rejected", errors.Is(err, ErrNotOwned))
	d.AddFirst("z")
	check(t, "doubly addFirst", d.String() == "[z, a, c]")
	_ = d.MoveToFront(c)
	check(t, "doubly moveToFront", d.String() == "[c, z, a]" && d.InvariantHolds())
	_ = d.MoveToFront(c)
	check(t, "doubly moveToFront on head is a no-op", d.String() == "[c, z, a]" && d.Size() == 3)
	f, _ := d.RemoveFirst()
	l, _ := d.RemoveLast()
	check(t, "doubly removeFirst/removeLast", f == "c" && l == "a" && d.String() == "[z]")
	_, _ = d.RemoveLast()
	check(t, "doubly empty again with invariant", d.IsEmpty() && d.InvariantHolds())
	_, err = d.RemoveLast()
	check(t, "doubly removeLast on empty", errors.Is(err, ErrEmpty))
}

func TestSplice(t *testing.T) {
	x := NewDoublyLinkedList[int]()
	y := NewDoublyLinkedList[int]()
	for i := 1; i <= 3; i++ {
		x.AddLast(i)
	}
	for i := 4; i <= 6; i++ {
		y.AddLast(i)
	}
	five := y.First().Next()
	check(t, "splice appends all in order", x.SpliceAll(y) == nil && x.String() == "[1, 2, 3, 4, 5, 6]" && x.Size() == 6)
	check(t, "splice empties the source", y.IsEmpty() && y.InvariantHolds())
	v, err := x.Unlink(five)
	check(t, "splice transfers ownership so nodes can be unlinked from the new list", five.Owner() == nil && err == nil && v == 5)
	check(t, "splice invariant", x.InvariantHolds())
	_ = x.SpliceAll(y)
	check(t, "splice of empty list is a no-op", x.Size() == 5)
	check(t, "splice into itself rejected", errors.Is(x.SpliceAll(x), ErrSpliceSelf))
}

func TestIntrusiveList(t *testing.T) {
	p := NewIntrusiveList[*PendingAuthorization]()
	p1 := NewPendingAuthorization("p1", 100, 1)
	p2 := NewPendingAuthorization("p2", 200, 2)
	p3 := NewPendingAuthorization("p3", 300, 3)
	_ = p.AddLast(p1)
	_ = p.AddLast(p2)
	_ = p.AddLast(p3)
	first, ok := p.First()
	check(t, "intrusive size and first", p.Size() == 3 && ok && first == p1)
	check(t, "intrusive contains is O(1) via owner", p.Contains(p2) && p1.IsLinked())
	check(t, "intrusive remove middle", p.Remove(p2) == nil && p.Size() == 2 && !p.Contains(p2) && !p2.IsLinked())
	ids := ""
	p.Each(func(e *PendingAuthorization) { ids += e.ID })
	check(t, "intrusive iteration order", ids == "p1p3")
	check(t, "intrusive remove twice rejected", errors.Is(p.Remove(p2), ErrNotOwned))
	check(t, "intrusive element cannot be in two lists", errors.Is(NewIntrusiveList[*PendingAuthorization]().AddLast(p1), ErrAlreadyLinked))
	q := NewIntrusiveList[*PendingAuthorization]()
	_ = q.AddLast(p2)
	check(t, "intrusive element can move to another list after removal", q.Contains(p2) && !p.Contains(p2))
	check(t, "intrusive remove from wrong list rejected", errors.Is(p.Remove(p2), ErrNotOwned))
	_, ok = NewIntrusiveList[*PendingAuthorization]().First()
	check(t, "intrusive first on empty is absent", !ok)
}

func TestBenchmarkHelpers(t *testing.T) {
	n := 10_000
	arr := make([]int64, n)
	cells := make([]*Cell, n)
	for i := 0; i < n; i++ {
		arr[i] = int64(i)
		cells[i] = &Cell{Value: int64(i)}
	}
	expected := int64(n) * int64(n-1) / 2
	check(t, "sumArray", SumArray(arr) == expected)
	seq := LinkInOrder(cells, Identity(n))
	check(t, "sumList sequential", SumList(seq) == expected)
	perm := Shuffled(n, 7)
	sorted := append([]int(nil), perm...)
	sort.Ints(sorted)
	check(t, "shuffled is a permutation", reflect.DeepEqual(sorted, Identity(n)) && !reflect.DeepEqual(perm, Identity(n)))
	check(t, "shuffled is deterministic for a seed", reflect.DeepEqual(perm, Shuffled(n, 7)))
	seq.Clear()
	check(t, "clear detaches every element", seq.IsEmpty() && !cells[0].IsLinked() && !cells[n-1].IsLinked())
	check(t, "sumList shuffled order gives the same sum", SumList(LinkInOrder(cells, perm)) == expected)
	// java.util.Random(7): Fisher-Yates over 10 elements, same as the Java demo's shuffle.
	check(t, "shuffled matches java.util.Random", reflect.DeepEqual(Shuffled(10, 7), []int{0, 1, 9, 3, 7, 4, 8, 5, 2, 6}))
}
