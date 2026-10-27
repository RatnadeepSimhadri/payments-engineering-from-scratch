// Project: projects/raft-kv — Stage 4: the replicated state machine (Go).
// Commands are "PUT key value", "GET key", or "NOOP". Applying the same log in the same order on every node yields
// the same store (State Machine Safety, Figure 3). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrBadCommand is a malformed or unknown command; the leader should have rejected it before it reached the log.
var ErrBadCommand = errors.New("bad command")

type KVStore struct {
	Data map[string]string
	// Parallel slices: command i and its result, in apply order. Entry at log index k is AppliedCommands[k - 1].
	AppliedCommands []string
	Results         []string
}

func NewKVStore() *KVStore { return &KVStore{Data: map[string]string{}} }

// Apply one committed command and return its result: "OK" for PUT, the stored value ("" when absent) for GET,
// "" for NOOP. A malformed or unknown command is ErrBadCommand and is not recorded.
func (kv *KVStore) Apply(command string) (string, error) {
	parts := strings.SplitN(command, " ", 3)
	var result string
	switch parts[0] {
	case "PUT":
		if len(parts) != 3 || parts[1] == "" {
			return "", fmt.Errorf("%w: PUT needs key and value: %s", ErrBadCommand, command)
		}
		kv.Data[parts[1]] = parts[2]
		result = "OK"
	case "GET":
		if len(parts) != 2 {
			return "", fmt.Errorf("%w: GET needs exactly a key: %s", ErrBadCommand, command)
		}
		result = kv.Data[parts[1]]
	case "NOOP":
		if len(parts) != 1 {
			return "", fmt.Errorf("%w: NOOP takes no arguments: %s", ErrBadCommand, command)
		}
		result = ""
	default:
		return "", fmt.Errorf("%w: unknown command: %s", ErrBadCommand, command)
	}
	kv.AppliedCommands = append(kv.AppliedCommands, command)
	kv.Results = append(kv.Results, result)
	return result, nil
}

// Get is the current value and whether the key was ever PUT (Java returns null when absent).
func (kv *KVStore) Get(key string) (string, bool) {
	v, ok := kv.Data[key]
	return v, ok
}

func (kv *KVStore) Applied() int { return len(kv.AppliedCommands) }

// String renders the store like Java's TreeMap: {a=1, b=2}, keys sorted.
func (kv *KVStore) String() string {
	keys := make([]string, 0, len(kv.Data))
	for k := range kv.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+kv.Data[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
