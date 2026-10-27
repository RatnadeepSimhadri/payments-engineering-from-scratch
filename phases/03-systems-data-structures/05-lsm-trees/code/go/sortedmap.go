package main

import (
	"slices"
	"sort"
)

// SortedMap is the stand-in for java.util.TreeMap<String, Entry>: a sorted key slice plus a map.
// Lookup is O(log n) by binary search; insertion of a new key is O(n) for the shift, which is fine for
// a write buffer of a thousand entries and for the decoded blocks and merge outputs this lesson builds.
type SortedMap struct {
	keys []string
	m    map[string]Entry
}

func NewSortedMap() *SortedMap { return &SortedMap{m: map[string]Entry{}} }

// Put stores the entry and returns the previous entry for the key, if there was one (TreeMap.put's return).
func (s *SortedMap) Put(key string, e Entry) (Entry, bool) {
	old, had := s.m[key]
	if !had {
		i := sort.SearchStrings(s.keys, key)
		s.keys = slices.Insert(s.keys, i, key)
	}
	s.m[key] = e
	return old, had
}

func (s *SortedMap) Get(key string) (Entry, bool) {
	e, ok := s.m[key]
	return e, ok
}

func (s *SortedMap) Len() int { return len(s.keys) }

func (s *SortedMap) IsEmpty() bool { return len(s.keys) == 0 }

// Keys is the ascending key order; callers must not modify the returned slice.
func (s *SortedMap) Keys() []string { return s.keys }

func (s *SortedMap) FirstKey() string { return s.keys[0] }

func (s *SortedMap) LastKey() string { return s.keys[len(s.keys)-1] }

func (s *SortedMap) PutAll(o *SortedMap) {
	for _, k := range o.keys {
		s.Put(k, o.m[k])
	}
}

// SubMap returns the entries with lo <= key <= hi (TreeMap.subMap(lo, true, hi, true)).
func (s *SortedMap) SubMap(lo, hi string) *SortedMap {
	out := NewSortedMap()
	from := sort.SearchStrings(s.keys, lo)
	for i := from; i < len(s.keys) && s.keys[i] <= hi; i++ {
		out.keys = append(out.keys, s.keys[i])
		out.m[s.keys[i]] = s.m[s.keys[i]]
	}
	return out
}

// Equal is TreeMap.equals: same keys with equal entries.
func (s *SortedMap) Equal(o *SortedMap) bool {
	if !slices.Equal(s.keys, o.keys) {
		return false
	}
	for _, k := range s.keys {
		if s.m[k] != o.m[k] {
			return false
		}
	}
	return true
}
