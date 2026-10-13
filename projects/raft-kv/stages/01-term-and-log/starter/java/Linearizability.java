// Project: projects/raft-kv — Stage 4: a small linearizability checker for recorded client histories.
// Op, History, and CheckResult are PROVIDED (the harness records them); Checker is YOUR CODE (Stage 4).
// Sources: Herlihy & Wing, "Linearizability: A Correctness Condition for Concurrent Objects" (TOPLAS 1990);
//          Wing & Gong, "Testing and Verifying Concurrent Objects" (JPDC 1993); Lowe, "Testing for Linearizability" (CCPE 2017).
// Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

enum OpKind { PUT, GET }

/**
 * One completed client operation. For a PUT, value is what was written; for a GET, value is what came back ("" when the
 * key was absent). invokedAt is the tick the client submitted it, returnedAt the tick the client saw the result.
 */
record Op(int id, String client, OpKind kind, String key, String value, int invokedAt, int returnedAt) {
    Op {
        if (returnedAt < invokedAt) throw new IllegalArgumentException("op " + id + " returned before it was invoked");
    }

    /** Real-time order: this op finished before the other started. Otherwise the two overlap and either may go first. */
    boolean precedes(Op other) { return returnedAt < other.invokedAt; }

    @Override public String toString() {
        String body = kind == OpKind.PUT ? "put(" + key + "=" + value + ")" : "get(" + key + ")->" + (value.isEmpty() ? "<absent>" : value);
        return "#" + id + " " + client + " " + body + " [" + invokedAt + "," + returnedAt + "]";
    }
}

/** An ordered collection of completed ops with dense ids 0..n-1, so the checker can track "already linearized" in one long. */
final class History {
    static final int MAX_OPS = 62;
    private final List<Op> ops = new ArrayList<>();

    History add(String client, OpKind kind, String key, String value, int invokedAt, int returnedAt) {
        if (ops.size() >= MAX_OPS) throw new IllegalArgumentException("this checker handles at most " + MAX_OPS + " ops per history");
        ops.add(new Op(ops.size(), client, kind, key, value, invokedAt, returnedAt));
        return this;
    }

    History put(String client, String key, String value, int invokedAt, int returnedAt) { return add(client, OpKind.PUT, key, value, invokedAt, returnedAt); }

    History get(String client, String key, String value, int invokedAt, int returnedAt) { return add(client, OpKind.GET, key, value, invokedAt, returnedAt); }

    List<Op> ops() { return List.copyOf(ops); }

    int size() { return ops.size(); }

    Op op(int id) { return ops.get(id); }
}

/** The verdict: a witness order (op ids) when linearizable, the longest prefix the search ever linearized when not. */
record CheckResult(boolean ok, List<Integer> order, List<Integer> longestPrefix, int explored) {
    String describe(History h) {
        StringBuilder sb = new StringBuilder(ok ? "LINEARIZABLE, witness order:" : "NOT LINEARIZABLE, longest prefix (" + longestPrefix.size() + " of " + h.size() + "):");
        for (int id : ok ? order : longestPrefix) sb.append("\n  ").append(h.op(id));
        if (!ok) sb.append("\n  no remaining op can go next from here");
        return sb.append("\n  (").append(explored).append(" states explored)").toString();
    }
}

/**
 * Stage 4: a Wing-Gong backtracking search over a map of string registers, each initially absent (""). Finds a sequential
 * order of all ops that the register model accepts and that respects real time: an op may be placed only when every op
 * that returned before it was invoked has already been placed. See stages/04-commit-and-apply/docs/en.md.
 */
final class Checker {
    CheckResult check(History history) {
        throw new UnsupportedOperationException("stage 4: Checker.check");
    }

    /** The sequential specification: the next state, or null when a GET's observed value is impossible from this state. */
    static Map<String, String> step(Map<String, String> state, Op op) {
        throw new UnsupportedOperationException("stage 4: Checker.step");
    }
}
