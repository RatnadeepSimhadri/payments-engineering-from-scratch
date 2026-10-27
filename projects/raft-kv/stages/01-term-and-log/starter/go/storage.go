// Project: projects/raft-kv — Stage 1: persistent state and the log (Go). YOUR CODE GOES HERE.
// Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)":
// currentTerm, votedFor, log[]. In this simulation a Storage object survives Crash(); everything else on the node is lost.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

// Storage: the log is 1-indexed as in the paper. Slot 0 holds a sentinel entry with term 0 so that prevLogIndex = 0 always
// matches and LastTerm() of an empty log is 0. VotedFor == -1 means null. Writes counts stable-storage writes so a
// test can prove that term, vote, and log are persisted before any reply leaves the node.
//
// Every method below must be implemented for Stage 1. Read stages/01-term-and-log/docs/en.md for the exact rules.
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

// Save persists a new (term, vote) pair. Must panic on a term lower than CurrentTerm.
func (s *Storage) Save(term int64, votedFor int) {
	panic("not implemented: TODO stage 01 Storage.Save")
}

// Append one entry. Must panic on an entry whose term is lower than LastTerm().
func (s *Storage) Append(e LogEntry) {
	panic("not implemented: TODO stage 01 Storage.Append")
}

// Truncate deletes the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays.
func (s *Storage) Truncate(fromIndex int) {
	panic("not implemented: TODO stage 01 Storage.Truncate")
}

// LastIndex is the index of the last entry; 0 for an empty log.
func (s *Storage) LastIndex() int {
	panic("not implemented: TODO stage 01 Storage.LastIndex")
}

// LastTerm is the term of the last entry; 0 for an empty log.
func (s *Storage) LastTerm() int64 {
	panic("not implemented: TODO stage 01 Storage.LastTerm")
}

// TermAt is the term of the entry at index (0 for the sentinel). Out of range -> panic.
func (s *Storage) TermAt(index int) int64 {
	panic("not implemented: TODO stage 01 Storage.TermAt")
}

// EntriesFrom is a copy of log[fromIndex..last]; empty when fromIndex == LastIndex() + 1. This is what a leader ships in AppendEntries.
func (s *Storage) EntriesFrom(fromIndex int) []LogEntry {
	panic("not implemented: TODO stage 01 Storage.EntriesFrom")
}

// UpToDate, section 5.4.1: is a candidate whose log ends at (candidateLastIndex, candidateLastTerm) at least as up-to-date as ours?
func (s *Storage) UpToDate(candidateLastIndex int, candidateLastTerm int64) bool {
	panic("not implemented: TODO stage 01 Storage.UpToDate")
}

// Describe renders log[1..last] for traces: [t1:PUT a 1, t1:NOOP].
func (s *Storage) Describe() string {
	panic("not implemented: TODO stage 01 Storage.Describe")
}
