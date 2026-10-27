// Project: projects/raft-kv — Stage 1: persistent state and the log (Go).
// Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)":
// currentTerm, votedFor, log[]. In this simulation a Storage object survives Crash(); everything else on the node is lost.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"fmt"
	"slices"
	"strings"
)

// Storage: the log is 1-indexed as in the paper. Slot 0 holds a sentinel entry with term 0 so that prevLogIndex = 0 always
// matches and LastTerm() of an empty log is 0. VotedFor == -1 means null. Writes counts stable-storage writes so a
// test can prove that term, vote, and log are persisted before any reply leaves the node.
// The guards (term going backwards, log terms decreasing, touching the sentinel, indexes out of range) are assertions
// about the caller's correctness, so they panic the way the Java version throws unchecked exceptions.
type Storage struct {
	CurrentTerm int64
	VotedFor    int
	Log         []LogEntry
	Writes      int
}

func NewStorage() *Storage {
	return &Storage{VotedFor: -1, Log: []LogEntry{{0, "<sentinel>"}}}
}

// Save persists a new (term, vote) pair. A node calls this before it sends any message that depends on the pair.
func (s *Storage) Save(term int64, votedFor int) {
	if term < s.CurrentTerm {
		panic(fmt.Sprintf("term must not go backwards: %d < %d", term, s.CurrentTerm))
	}
	s.CurrentTerm = term
	s.VotedFor = votedFor
	s.Writes++
}

func (s *Storage) Append(e LogEntry) {
	if e.Term < s.LastTerm() {
		panic(fmt.Sprintf("log terms must be non-decreasing: %v after term %d", e, s.LastTerm()))
	}
	s.Log = append(s.Log, e)
	s.Writes++
}

// Truncate deletes the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays.
func (s *Storage) Truncate(fromIndex int) {
	if fromIndex < 1 {
		panic("cannot truncate the sentinel at index 0")
	}
	if fromIndex > s.LastIndex() {
		return
	}
	s.Log = s.Log[:fromIndex]
	s.Writes++
}

func (s *Storage) LastIndex() int { return len(s.Log) - 1 }

func (s *Storage) LastTerm() int64 { return s.Log[len(s.Log)-1].Term }

func (s *Storage) TermAt(index int) int64 {
	if index < 0 || index > s.LastIndex() {
		panic(fmt.Sprintf("no log entry at index %d (last is %d)", index, s.LastIndex()))
	}
	return s.Log[index].Term
}

// EntriesFrom is a copy of log[fromIndex..last]; empty when fromIndex == LastIndex() + 1. This is what a leader ships in AppendEntries.
func (s *Storage) EntriesFrom(fromIndex int) []LogEntry {
	if fromIndex < 1 || fromIndex > s.LastIndex()+1 {
		panic(fmt.Sprintf("EntriesFrom(%d) with last index %d", fromIndex, s.LastIndex()))
	}
	return slices.Clone(s.Log[fromIndex:])
}

// UpToDate, section 5.4.1: a candidate's log is at least as up-to-date as ours if its last term is higher, or the terms are
// equal and its log is at least as long. A voter grants its vote only when this is true; that is how committed entries survive.
func (s *Storage) UpToDate(candidateLastIndex int, candidateLastTerm int64) bool {
	return candidateLastTerm > s.LastTerm() || (candidateLastTerm == s.LastTerm() && candidateLastIndex >= s.LastIndex())
}

// Describe renders log[1..last] for traces: [t1:PUT a 1, t1:NOOP].
func (s *Storage) Describe() string {
	parts := make([]string, 0, len(s.Log)-1)
	for _, e := range s.Log[1:] {
		parts = append(parts, e.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
