// Project: projects/raft-kv — Stage 4: the replicated state machine. YOUR CODE GOES HERE (nothing to do before Stage 4).
// Commands are "PUT key value", "GET key", or "NOOP". Applying the same log in the same order on every node yields
// the same store (State Machine Safety, Figure 3). Java 21, standard library only. Python and Go ports follow later.

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

final class KVStore {
    final Map<String, String> data = new TreeMap<>();
    /** Parallel lists: command i and its result, in apply order. Entry at log index k is appliedCommands[k - 1]. */
    final List<String> appliedCommands = new ArrayList<>();
    final List<String> results = new ArrayList<>();

    /**
     * Apply one committed command and return its result: "OK" for PUT, the stored value ("" when absent) for GET,
     * "" for NOOP. A malformed or unknown command throws IllegalArgumentException and is not recorded.
     */
    String apply(String command) {
        throw new UnsupportedOperationException("stage 4: KVStore.apply");
    }

    /** Current value, or null when the key was never PUT. */
    String get(String key) { return data.get(key); }

    int applied() { return appliedCommands.size(); }

    @Override public String toString() { return data.toString(); }
}
