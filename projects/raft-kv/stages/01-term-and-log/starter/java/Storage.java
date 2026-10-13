// Project: projects/raft-kv — Stage 1: persistent state and the log. YOUR CODE GOES HERE.
// Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)":
// currentTerm, votedFor, log[]. In this simulation a Storage object survives crash(); everything else on the node is lost.
// Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.List;

/**
 * The log is 1-indexed as in the paper. Slot 0 holds a sentinel entry with term 0 so that prevLogIndex = 0 always
 * matches and lastTerm() of an empty log is 0. votedFor == -1 means null. `writes` counts stable-storage writes so a
 * test can prove that term, vote, and log are persisted before any reply leaves the node.
 *
 * Every method below must be implemented for Stage 1. Read stages/01-term-and-log/docs/en.md for the exact rules.
 */
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    final List<LogEntry> log = new ArrayList<>(List.of(new LogEntry(0, "<sentinel>")));
    int writes = 0;

    /** Persist a new (term, vote) pair. Must reject a term lower than currentTerm with IllegalArgumentException. */
    void save(long term, int votedFor) {
        throw new UnsupportedOperationException("stage 1: Storage.save");
    }

    /** Append one entry. Must reject an entry whose term is lower than lastTerm() with IllegalArgumentException. */
    void append(LogEntry e) {
        throw new UnsupportedOperationException("stage 1: Storage.append");
    }

    /** Delete the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays. */
    void truncate(int fromIndex) {
        throw new UnsupportedOperationException("stage 1: Storage.truncate");
    }

    /** Index of the last entry; 0 for an empty log. */
    int lastIndex() {
        throw new UnsupportedOperationException("stage 1: Storage.lastIndex");
    }

    /** Term of the last entry; 0 for an empty log. */
    long lastTerm() {
        throw new UnsupportedOperationException("stage 1: Storage.lastTerm");
    }

    /** Term of the entry at index (0 for the sentinel). Out of range -> IndexOutOfBoundsException. */
    long termAt(int index) {
        throw new UnsupportedOperationException("stage 1: Storage.termAt");
    }

    /** A copy of log[fromIndex..last]; empty when fromIndex == lastIndex() + 1. This is what a leader ships in AppendEntries. */
    List<LogEntry> entriesFrom(int fromIndex) {
        throw new UnsupportedOperationException("stage 1: Storage.entriesFrom");
    }

    /** Section 5.4.1: is a candidate whose log ends at (candidateLastIndex, candidateLastTerm) at least as up-to-date as ours? */
    boolean upToDate(int candidateLastIndex, long candidateLastTerm) {
        throw new UnsupportedOperationException("stage 1: Storage.upToDate");
    }

    /** log[1..last] as a string, for traces: [t1:PUT a 1, t1:NOOP]. */
    String describe() {
        throw new UnsupportedOperationException("stage 1: Storage.describe");
    }
}
