// Project: projects/raft-kv — Stage 4: the replicated state machine.
// Commands are "PUT key value", "GET key", or "NOOP". Applying the same log in the same order on every node yields
// the same store (State Machine Safety, Figure 3). Java 21, standard library only. Python port in solution/python; Go port in solution/go.

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
     * "" for NOOP. A malformed or unknown command throws IllegalArgumentException and is not recorded: the leader
     * should have rejected it before it reached the log.
     */
    String apply(String command) {
        String[] parts = command.split(" ", 3);
        String result;
        switch (parts[0]) {
            case "PUT" -> {
                if (parts.length != 3 || parts[1].isEmpty()) throw new IllegalArgumentException("PUT needs key and value: " + command);
                data.put(parts[1], parts[2]);
                result = "OK";
            }
            case "GET" -> {
                if (parts.length != 2) throw new IllegalArgumentException("GET needs exactly a key: " + command);
                result = data.getOrDefault(parts[1], "");
            }
            case "NOOP" -> {
                if (parts.length != 1) throw new IllegalArgumentException("NOOP takes no arguments: " + command);
                result = "";
            }
            default -> throw new IllegalArgumentException("unknown command: " + command);
        }
        appliedCommands.add(command);
        results.add(result);
        return result;
    }

    /** Current value, or null when the key was never PUT. */
    String get(String key) { return data.get(key); }

    int applied() { return appliedCommands.size(); }

    @Override public String toString() { return data.toString(); }
}
