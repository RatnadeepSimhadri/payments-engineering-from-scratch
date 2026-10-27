// Project: projects/raft-kv — Stage 4: a small linearizability checker for recorded client histories (Go).
// Op, History, and CheckResult are PROVIDED (the harness records them); Checker is YOUR CODE (Stage 4).
// Sources: Herlihy & Wing, "Linearizability: A Correctness Condition for Concurrent Objects" (TOPLAS 1990);
//
//	Wing & Gong, "Testing and Verifying Concurrent Objects" (JPDC 1993); Lowe, "Testing for Linearizability" (CCPE 2017).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"fmt"
	"slices"
	"strings"
)

type OpKind int

const (
	PUT OpKind = iota
	GET
)

func (k OpKind) String() string {
	if k == PUT {
		return "PUT"
	}
	return "GET"
}

// Op is one completed client operation. For a PUT, Value is what was written; for a GET, Value is what came back ("" when the
// key was absent). InvokedAt is the tick the client submitted it, ReturnedAt the tick the client saw the result.
type Op struct {
	ID         int
	Client     string
	Kind       OpKind
	Key        string
	Value      string
	InvokedAt  int
	ReturnedAt int
}

// Precedes is real-time order: this op finished before the other started. Otherwise the two overlap and either may go first.
func (o Op) Precedes(other Op) bool { return o.ReturnedAt < other.InvokedAt }

func (o Op) String() string {
	body := fmt.Sprintf("put(%s=%s)", o.Key, o.Value)
	if o.Kind == GET {
		shown := o.Value
		if shown == "" {
			shown = "<absent>"
		}
		body = fmt.Sprintf("get(%s)->%s", o.Key, shown)
	}
	return fmt.Sprintf("#%d %s %s [%d,%d]", o.ID, o.Client, body, o.InvokedAt, o.ReturnedAt)
}

// MaxOps bounds a history so the checker can track "already linearized" in one uint64.
const MaxOps = 62

// History is an ordered collection of completed ops with dense ids 0..n-1. Add/Put/Get chain like the Java builder and
// panic on the two programming errors the Java constructor throws for (too many ops, returned before invoked).
type History struct {
	ops []Op
}

func NewHistory() *History { return &History{} }

func (h *History) Add(client string, kind OpKind, key, value string, invokedAt, returnedAt int) *History {
	if len(h.ops) >= MaxOps {
		panic(fmt.Sprintf("this checker handles at most %d ops per history", MaxOps))
	}
	if returnedAt < invokedAt {
		panic(fmt.Sprintf("op %d returned before it was invoked", len(h.ops)))
	}
	h.ops = append(h.ops, Op{len(h.ops), client, kind, key, value, invokedAt, returnedAt})
	return h
}

func (h *History) Put(client, key, value string, invokedAt, returnedAt int) *History {
	return h.Add(client, PUT, key, value, invokedAt, returnedAt)
}

func (h *History) Get(client, key, value string, invokedAt, returnedAt int) *History {
	return h.Add(client, GET, key, value, invokedAt, returnedAt)
}

func (h *History) Ops() []Op    { return slices.Clone(h.ops) }
func (h *History) Size() int    { return len(h.ops) }
func (h *History) Op(id int) Op { return h.ops[id] }

// CheckResult is the verdict: a witness order (op ids) when linearizable, the longest prefix the search ever linearized when not.
type CheckResult struct {
	OK            bool
	Order         []int
	LongestPrefix []int
	Explored      int
}

func (r CheckResult) Describe(h *History) string {
	var sb strings.Builder
	ids := r.Order
	if r.OK {
		sb.WriteString("LINEARIZABLE, witness order:")
	} else {
		fmt.Fprintf(&sb, "NOT LINEARIZABLE, longest prefix (%d of %d):", len(r.LongestPrefix), h.Size())
		ids = r.LongestPrefix
	}
	for _, id := range ids {
		fmt.Fprintf(&sb, "\n  %v", h.Op(id))
	}
	if !r.OK {
		sb.WriteString("\n  no remaining op can go next from here")
	}
	fmt.Fprintf(&sb, "\n  (%d states explored)", r.Explored)
	return sb.String()
}

// Checker, Stage 4: a Wing-Gong backtracking search over a map of string registers, each initially absent (""). Finds a
// sequential order of all ops that the register model accepts and that respects real time: an op may be placed only when
// every op that returned before it was invoked has already been placed. See stages/04-commit-and-apply/docs/en.md.
type Checker struct{}

func NewChecker() *Checker { return &Checker{} }

func (c *Checker) Check(history *History) CheckResult {
	panic("not implemented: TODO stage 04 Checker.Check")
}

// Step is the sequential specification: the next state and true, or false when a GET's observed value is impossible from this state.
// A PUT returns a copy; the input map is never changed. An accepted GET returns the same map it was given.
func Step(state map[string]string, op Op) (map[string]string, bool) {
	panic("not implemented: TODO stage 04 Checker.Step")
}
