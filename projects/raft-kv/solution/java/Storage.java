// Project: projects/raft-kv — Stage 1: persistent state and the log.
// Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)":
// currentTerm, votedFor, log[]. In this simulation a Storage object survives crash(); everything else on the node is lost.
// Java 21, standard library only. Python port in solution/python; Go port follows later.

import java.util.ArrayList;
import java.util.List;

/**
 * The log is 1-indexed as in the paper. Slot 0 holds a sentinel entry with term 0 so that prevLogIndex = 0 always
 * matches and lastTerm() of an empty log is 0. votedFor == -1 means null. `writes` counts stable-storage writes so a
 * test can prove that term, vote, and log are persisted before any reply leaves the node.
 */
final class Storage {
    long currentTerm = 0;
    int votedFor = -1;
    final List<LogEntry> log = new ArrayList<>(List.of(new LogEntry(0, "<sentinel>")));
    int writes = 0;

    /** Persist a new (term, vote) pair. A node calls this before it sends any message that depends on the pair. */
    void save(long term, int votedFor) {
        if (term < currentTerm) throw new IllegalArgumentException("term must not go backwards: " + term + " < " + currentTerm);
        this.currentTerm = term;
        this.votedFor = votedFor;
        writes++;
    }

    void append(LogEntry e) {
        if (e.term() < lastTerm()) throw new IllegalArgumentException("log terms must be non-decreasing: " + e + " after term " + lastTerm());
        log.add(e);
        writes++;
    }

    /** Delete the entry at fromIndex and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays. */
    void truncate(int fromIndex) {
        if (fromIndex < 1) throw new IllegalArgumentException("cannot truncate the sentinel at index 0");
        if (fromIndex > lastIndex()) return;
        while (log.size() > fromIndex) log.remove(log.size() - 1);
        writes++;
    }

    int lastIndex() { return log.size() - 1; }

    long lastTerm() { return log.get(log.size() - 1).term(); }

    long termAt(int index) {
        if (index < 0 || index > lastIndex()) throw new IndexOutOfBoundsException("no log entry at index " + index + " (last is " + lastIndex() + ")");
        return log.get(index).term();
    }

    /** A copy of log[fromIndex..last]; empty when fromIndex == lastIndex() + 1. This is what a leader ships in AppendEntries. */
    List<LogEntry> entriesFrom(int fromIndex) {
        if (fromIndex < 1 || fromIndex > lastIndex() + 1) throw new IndexOutOfBoundsException("entriesFrom(" + fromIndex + ") with last index " + lastIndex());
        return new ArrayList<>(log.subList(fromIndex, log.size()));
    }

    /**
     * Section 5.4.1: a candidate's log is at least as up-to-date as ours if its last term is higher, or the terms are equal
     * and its log is at least as long. A voter grants its vote only when this is true; that is how committed entries survive.
     */
    boolean upToDate(int candidateLastIndex, long candidateLastTerm) {
        return candidateLastTerm > lastTerm() || (candidateLastTerm == lastTerm() && candidateLastIndex >= lastIndex());
    }

    /** log[1..last] as a string, for traces: [t1:PUT a 1, t1:NOOP]. */
    String describe() { return log.subList(1, log.size()).toString(); }
}
