// Project: projects/raft-kv — Raft-replicated key-value store on a deterministic simulated network (Go).
// Run:   go run .
// Grade: python3 scripts/project_test.py raft-kv --lang go --stage 1 --path <this workspace>
// This starter main only exercises the PROVIDED scaffolding (sim.go). Replace it with your own demo as the stages
// land: the solution's main runs an election, replicates client writes, and checks the recorded history.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import "fmt"

// Echo is a trivial participant: replies to every AppendEntries with a reply carrying the same term, sends nothing on tick.
type Echo struct {
	id   int
	seen []string
}

func (e *Echo) ID() int { return e.id }

func (e *Echo) Tick(now int) []Message { return nil }

func (e *Echo) Handle(m Message, now int) []Message {
	e.seen = append(e.seen, fmt.Sprintf("t=%d n%d got %v term %d from n%d", now, e.id, m.Kind, m.Term, m.From))
	if m.Kind == AppendEntries {
		return []Message{NewAppendReply(e.id, m.From, m.Term, true, 0)}
	}
	return nil
}

func main() {
	fmt.Println("== The seeded Rng: the same seed gives the same stream, forever ==")
	a, b := NewRng(42), NewRng(42)
	for i := 0; i < 3; i++ {
		fmt.Printf("draw %d: %d == %d\n", i, a.NextInt(1000), b.NextInt(1000))
	}

	fmt.Println()
	fmt.Println("== The simulated network: delivery order is (deliverAt, sequence); nothing depends on wall-clock time ==")
	clock := NewClock()
	net := NewNetwork(clock, 7, 0.0, 5)
	n0, n1 := &Echo{id: 0}, &Echo{id: 1}
	net.Add(n0)
	net.Add(n1)
	net.Send(NewAppendEntries(0, 1, 1, 0, 0, nil, 0))
	net.Send(NewAppendEntries(0, 1, 1, 0, 0, nil, 0))
	for i := 0; i < 12; i++ {
		net.Tick()
	}
	for _, s := range n1.seen {
		fmt.Println(s)
	}
	for _, s := range n0.seen {
		fmt.Println(s)
	}
	fmt.Printf("sent=%d delivered=%d dropped=%d in-flight=%d at tick %d\n", net.Sent, net.Delivered, net.Dropped, net.InFlight(), clock.Now())

	fmt.Println()
	fmt.Println("not implemented yet: stage 1 (Storage.Save, Append, Truncate, LastIndex, LastTerm, TermAt, EntriesFrom, UpToDate, Describe)")
	fmt.Println("Stage 1: implement storage.go, then run the grader:")
	fmt.Println("  python3 scripts/project_test.py raft-kv --lang go --stage 1 --path <workspace>")
	defer func() {
		// Every stub panics with "not implemented: TODO stage NN ..." until you replace it; recover so the
		// workspace still exits 0 and tells you which function to write next.
		if todo := recover(); todo != nil {
			fmt.Println("next up:", todo)
		}
	}()
	s := NewStorage()
	fmt.Printf("a fresh Storage: term %d, votedFor %d, log %s\n", s.CurrentTerm, s.VotedFor, s.Describe())
}
