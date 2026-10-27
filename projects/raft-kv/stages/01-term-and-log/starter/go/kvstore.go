// Project: projects/raft-kv — Stage 4: the replicated state machine (Go). YOUR CODE GOES HERE (nothing to do before Stage 4).
// Commands are "PUT key value", "GET key", or "NOOP". Applying the same log in the same order on every node yields
// the same store (State Machine Safety, Figure 3). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
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
// "" for NOOP. A malformed or unknown command returns an error wrapping ErrBadCommand and is not recorded.
func (kv *KVStore) Apply(command string) (string, error) {
	panic("not implemented: TODO stage 04 KVStore.Apply")
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
