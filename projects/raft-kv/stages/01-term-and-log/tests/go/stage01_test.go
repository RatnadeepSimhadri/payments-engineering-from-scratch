// Stage 01 tests for projects/raft-kv (Go): persistent state (Storage) and the provided simulation scaffolding (sim.go).
// Run by the grader: go test ./...
// Port of Stage01Test.java: same checks, same names, same order. Helpers are prefixed s01 because every
// stage's test file compiles into the same package in the grader's sandbox.
package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func s01check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// s01checkPanics expects action to panic: the Go Storage panics where the Java one throws an unchecked exception.
func s01checkPanics(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("FAIL: %s (no panic)", name)
		}
	}()
	action()
}

// s01scenario runs one scenario; an unexpected panic fails the scenario instead of killing the whole run.
func s01scenario(t *testing.T, name string, body func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s blew up: %v", name, r)
			}
		}()
		body(t)
	})
}

func s01logged(terms ...int64) *Storage {
	s := NewStorage()
	for i, term := range terms {
		s.Append(LogEntry{term, fmt.Sprintf("cmd%d", i+1)})
	}
	return s
}

// s01Probe is a minimal network participant for the scaffolding tests. Records what it receives; echoes AppendEntries.
type s01Probe struct {
	id       int
	received []string
	ticks    int
}

func (p *s01Probe) ID() int { return p.id }

func (p *s01Probe) Tick(now int) []Message {
	p.ticks++
	return nil
}

func (p *s01Probe) Handle(m Message, now int) []Message {
	p.received = append(p.received, fmt.Sprintf("%d:%v:%d:%d", now, m.Kind, m.From, m.Term))
	if m.Kind == AppendEntries {
		return []Message{NewAppendReply(p.id, m.From, m.Term, true, 0)}
	}
	return nil
}

func TestStage01(t *testing.T) {
	s01scenario(t, "fresh storage", func(t *testing.T) {
		s := NewStorage()
		s01check(t, "fresh currentTerm is 0", s.CurrentTerm == 0)
		s01check(t, "fresh votedFor is null (-1)", s.VotedFor == -1)
		s01check(t, "fresh lastIndex is 0", s.LastIndex() == 0)
		s01check(t, "fresh lastTerm is 0", s.LastTerm() == 0)
		s01check(t, "sentinel at index 0 has term 0", s.TermAt(0) == 0)
		s01check(t, "fresh describe is []", s.Describe() == "[]")
		s01check(t, "fresh entriesFrom(1) is empty", len(s.EntriesFrom(1)) == 0)
		s01check(t, "no writes yet", s.Writes == 0)
	})

	s01scenario(t, "save persists term and vote", func(t *testing.T) {
		s := NewStorage()
		s.Save(3, 2)
		s01check(t, "save sets currentTerm", s.CurrentTerm == 3)
		s01check(t, "save sets votedFor", s.VotedFor == 2)
		s01check(t, "save counts one stable write", s.Writes == 1)
		s.Save(3, 2)
		s01check(t, "re-saving the same pair still writes (it is a persist, not a cache)", s.Writes == 2)
		s.Save(4, -1)
		s01check(t, "a new term clears votedFor when asked", s.CurrentTerm == 4 && s.VotedFor == -1)
		s01checkPanics(t, "term must never go backwards", func() { s.Save(2, 0) })
		s01check(t, "a rejected save changes nothing", s.CurrentTerm == 4 && s.VotedFor == -1 && s.Writes == 3)
	})

	s01scenario(t, "append and the 1-indexed log", func(t *testing.T) {
		s := NewStorage()
		s.Append(LogEntry{1, "PUT a 1"})
		s.Append(LogEntry{1, "PUT b 2"})
		s.Append(LogEntry{3, "NOOP"})
		s01check(t, "lastIndex counts entries, not the sentinel", s.LastIndex() == 3)
		s01check(t, "lastTerm is the last entry's term", s.LastTerm() == 3)
		s01check(t, "termAt(1) is 1", s.TermAt(1) == 1)
		s01check(t, "termAt(3) is 3", s.TermAt(3) == 3)
		s01check(t, "each append is a stable write", s.Writes == 3)
		s01check(t, "describe lists entries from index 1", s.Describe() == "[t1:PUT a 1, t1:PUT b 2, t3:NOOP]")
		s01check(t, "log list itself holds sentinel + 3", len(s.Log) == 4 && s.Log[0].Term == 0)
		s01checkPanics(t, "terms in a log never decrease", func() { s.Append(LogEntry{2, "late"}) })
		s01check(t, "a rejected append changes nothing", s.LastIndex() == 3 && s.Writes == 3)
		s.Append(LogEntry{3, "PUT c 3"})
		s01check(t, "same term may repeat", s.LastIndex() == 4 && s.LastTerm() == 3)
	})

	s01scenario(t, "termAt bounds", func(t *testing.T) {
		s := s01logged(1, 1)
		s01checkPanics(t, "termAt beyond last index panics (IndexOutOfBounds)", func() { s.TermAt(3) })
		s01checkPanics(t, "termAt negative panics (IndexOutOfBounds)", func() { s.TermAt(-1) })
	})

	s01scenario(t, "truncate", func(t *testing.T) {
		s := s01logged(1, 1, 2, 2, 2)
		before := s.Writes
		s.Truncate(3)
		s01check(t, "truncate(3) removes index 3 and after", s.LastIndex() == 2 && s.LastTerm() == 1)
		s01check(t, "truncate is one stable write", s.Writes == before+1)
		s01check(t, "truncate keeps earlier entries intact", s.Describe() == "[t1:cmd1, t1:cmd2]")
		s.Truncate(7)
		s01check(t, "truncating past the end is a no-op", s.LastIndex() == 2 && s.Writes == before+1)
		s.Truncate(1)
		s01check(t, "truncate(1) empties the log but keeps the sentinel", s.LastIndex() == 0 && s.LastTerm() == 0 && len(s.Log) == 1)
		s01checkPanics(t, "the sentinel cannot be truncated", func() { s.Truncate(0) })
		s.Append(LogEntry{5, "after"})
		s01check(t, "log is usable after a full truncate", s.LastIndex() == 1 && s.TermAt(1) == 5)
	})

	s01scenario(t, "entriesFrom", func(t *testing.T) {
		s := s01logged(1, 2, 2)
		tail := s.EntriesFrom(2)
		s01check(t, "entriesFrom(2) returns indexes 2..3", len(tail) == 2 && tail[0].Command == "cmd2" && tail[1].Command == "cmd3")
		s01check(t, "entriesFrom(1) returns everything", len(s.EntriesFrom(1)) == 3)
		s01check(t, "entriesFrom(lastIndex+1) is empty (a heartbeat)", len(s.EntriesFrom(4)) == 0)
		tail[0] = LogEntry{9, "clobbered"}
		tail = tail[:0]
		s01check(t, "entriesFrom returns a copy, not a view", s.LastIndex() == 3 && s.Log[2].Command == "cmd2" && len(tail) == 0)
		s01checkPanics(t, "entriesFrom(0) would ship the sentinel", func() { s.EntriesFrom(0) })
		s01checkPanics(t, "entriesFrom beyond lastIndex+1 panics", func() { s.EntriesFrom(5) })
	})

	s01scenario(t, "up-to-date rule (section 5.4.1)", func(t *testing.T) {
		s := s01logged(1, 2, 2) // ends at (3, t2)
		s01check(t, "higher last term wins even with a shorter log", s.UpToDate(1, 3))
		s01check(t, "same last term, longer log wins", s.UpToDate(4, 2))
		s01check(t, "same last term, same length is up-to-date", s.UpToDate(3, 2))
		s01check(t, "same last term, shorter log loses", !s.UpToDate(2, 2))
		s01check(t, "lower last term loses even with a longer log", !s.UpToDate(10, 1))
		s01check(t, "an empty log is up-to-date with another empty log", NewStorage().UpToDate(0, 0))
		s01check(t, "an empty candidate is not up-to-date with a non-empty voter", !s.UpToDate(0, 0))
	})

	s01scenario(t, "Rng is SplitMix64 and deterministic", func(t *testing.T) {
		a, b := NewRng(42), NewRng(42)
		s01check(t, "same seed, same stream", a.NextLong() == b.NextLong() && a.NextInt(1000) == b.NextInt(1000) && a.NextDouble() == b.NextDouble())
		s01check(t, "SplitMix64(1) first output", uint64(NewRng(1).NextLong()) == 0x910A2DEC89025CC1)
		c := NewRng(42)
		s01check(t, "Rng(42).nextInt(1000) stream is 451, 953, 371", c.NextInt(1000) == 451 && c.NextInt(1000) == 953 && c.NextInt(1000) == 371)
		d := NewRng(9)
		inRange := true
		for i := 0; i < 1000; i++ {
			if v := d.NextInt(7); v < 0 || v >= 7 {
				inRange = false
			}
			if x := d.NextDouble(); x < 0 || x >= 1 {
				inRange = false
			}
		}
		s01check(t, "nextInt stays in [0, bound) and nextDouble in [0, 1)", inRange)
	})

	s01scenario(t, "Clock and Network scaffolding", func(t *testing.T) {
		clock := NewClock()
		s01check(t, "clock starts at 0", clock.Now() == 0)
		net := NewNetwork(clock, 7, 0.0, 5)
		n0, n1 := &s01Probe{id: 0}, &s01Probe{id: 1}
		net.Add(n0)
		net.Add(n1)
		net.Send(NewAppendEntries(0, 1, 1, 0, 0, nil, 0))
		net.Send(NewAppendEntries(0, 1, 1, 0, 0, nil, 0))
		s01check(t, "messages are in flight, not delivered synchronously", net.InFlight() == 2 && len(n1.received) == 0)
		for i := 0; i < 12; i++ {
			net.Tick()
		}
		s01check(t, "clock advanced one tick per tick()", clock.Now() == 12)
		s01check(t, "both messages reached n1 at seed-7's schedule (tick 3, in send order)", slices.Equal(n1.received, []string{"3:APPEND_ENTRIES:0:1", "3:APPEND_ENTRIES:0:1"}))
		s01check(t, "replies came back to n0 at ticks 4 and 6", slices.Equal(n0.received, []string{"4:APPEND_ENTRIES_REPLY:1:1", "6:APPEND_ENTRIES_REPLY:1:1"}))
		s01check(t, "every live node ticks once per tick", n0.ticks == 12 && n1.ticks == 12)
		s01check(t, "counters: 4 sent, 4 delivered, 0 dropped", net.Sent == 4 && net.Delivered == 4 && net.Dropped == 0)
		noneAtZero := true
		for _, r := range n1.received {
			if strings.HasPrefix(r, "0:") {
				noneAtZero = false
			}
		}
		s01check(t, "delay is at least one tick: nothing is delivered in the tick it is sent", noneAtZero)

		c2 := NewClock()
		lossy := NewNetwork(c2, 7, 1.0, 5)
		p0, p1 := &s01Probe{id: 0}, &s01Probe{id: 1}
		lossy.Add(p0)
		lossy.Add(p1)
		lossy.Send(NewRequestVote(0, 1, 1, 0, 0))
		for i := 0; i < 10; i++ {
			lossy.Tick()
		}
		s01check(t, "dropRate 1.0 drops everything", lossy.Dropped == 1 && len(p1.received) == 0)

		c3 := NewClock()
		parted := NewNetwork(c3, 7, 0.0, 5)
		q0, q1, q2 := &s01Probe{id: 0}, &s01Probe{id: 1}, &s01Probe{id: 2}
		parted.Add(q0)
		parted.Add(q1)
		parted.Add(q2)
		parted.Partition([]int{0})
		parted.Send(NewRequestVote(0, 1, 1, 0, 0))
		parted.Send(NewRequestVote(1, 2, 1, 0, 0))
		for i := 0; i < 10; i++ {
			parted.Tick()
		}
		s01check(t, "a partition drops messages across the cut and delivers the rest", len(q1.received) == 0 && len(q2.received) == 1)
		parted.Heal()
		parted.Send(NewRequestVote(0, 1, 1, 0, 0))
		for i := 0; i < 10; i++ {
			parted.Tick()
		}
		s01check(t, "after heal the cut is gone", len(q1.received) == 1)
		parted.Crash(2)
		parted.Send(NewRequestVote(1, 2, 2, 0, 0))
		for i := 0; i < 10; i++ {
			parted.Tick()
		}
		s01check(t, "a crashed node receives nothing and does not tick", len(q2.received) == 1 && parted.IsDown(2) && q2.ticks == 20)
		q2b := &s01Probe{id: 2}
		parted.Restart(q2b)
		parted.Send(NewRequestVote(1, 2, 3, 0, 0))
		for i := 0; i < 10; i++ {
			parted.Tick()
		}
		s01check(t, "a restarted node is live again", !parted.IsDown(2) && len(q2b.received) == 1 && q2b.ticks == 10)
	})

	s01scenario(t, "Message helpers", func(t *testing.T) {
		rv := NewRequestVote(1, 2, 5, 7, 4)
		s01check(t, "requestVote carries the candidate's last (index, term) in prevLogIndex/prevLogTerm", rv.Kind == RequestVote && rv.PrevLogIndex == 7 && rv.PrevLogTerm == 4 && rv.Term == 5)
		ae := NewAppendEntries(0, 1, 2, 3, 1, []LogEntry{{2, "PUT k v"}}, 3)
		s01check(t, "appendEntries carries prev (index, term), entries, and leaderCommit", ae.PrevLogIndex == 3 && ae.PrevLogTerm == 1 && len(ae.Entries) == 1 && ae.LeaderCommit == 3)
		_, err := NewConfig(100, 200, 100)
		s01check(t, "Config rejects a heartbeat interval that is not well below the election timeout", err != nil)
		s01check(t, "default config is 150-300 / 50", DefaultConfig.ElectionTimeoutMin == 150 && DefaultConfig.ElectionTimeoutMax == 300 && DefaultConfig.HeartbeatInterval == 50)
	})
}
