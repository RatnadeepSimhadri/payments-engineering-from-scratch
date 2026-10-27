// Tests for phases/02-algorithms/12-string-matching. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func naive(t *testing.T, text, pattern string) []int {
	t.Helper()
	return must(NaiveSearch(text, pattern))
}

func kmp(t *testing.T, pattern string) *KmpMatcher {
	t.Helper()
	return must(NewKmpMatcher(pattern))
}

func rabinKarp(t *testing.T, pattern string) *RabinKarpMatcher {
	t.Helper()
	return must(NewRabinKarpMatcher(pattern))
}

func names(matches []Match) []string {
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.Pattern
	}
	return out
}

func randomString(rng *JavaRandom, length, alphabet int) string {
	var sb strings.Builder
	for i := 0; i < length; i++ {
		sb.WriteRune(rune('a' + rng.NextIntN(alphabet)))
	}
	return sb.String()
}

func TestNaive(t *testing.T) {
	check(t, "naive finds all", slices.Equal(naive(t, "abracadabra", "abra"), []int{0, 7}))
	check(t, "naive overlapping", slices.Equal(naive(t, "aaaa", "aa"), []int{0, 1, 2}))
	check(t, "naive no match", len(naive(t, "abc", "d")) == 0)
	check(t, "naive pattern longer than text", len(naive(t, "ab", "abc")) == 0)
	_, err := NaiveSearch("abc", "")
	check(t, "naive rejects empty pattern", errors.Is(err, ErrEmptyPattern))
}

func TestKmp(t *testing.T) {
	// failure function: the hand-computed tables from the lesson
	check(t, "failure(ABABAC)", slices.Equal(FailureFunction("ABABAC"), []int{0, 0, 1, 2, 3, 0}))
	check(t, "failure(aaaab)", slices.Equal(FailureFunction("aaaab"), []int{0, 1, 2, 3, 0}))
	check(t, "failure(abcabcabd)", slices.Equal(FailureFunction("abcabcabd"), []int{0, 0, 0, 1, 2, 3, 4, 5, 0}))
	check(t, "failure(a)", slices.Equal(FailureFunction("a"), []int{0}))
	check(t, "failure of distinct chars is all zero", slices.Equal(FailureFunction("abcd"), []int{0, 0, 0, 0}))

	// search
	check(t, "kmp matches naive on abracadabra", slices.Equal(kmp(t, "abra").Search("abracadabra"), []int{0, 7}))
	check(t, "kmp overlapping matches", slices.Equal(kmp(t, "aa").Search("aaaa"), []int{0, 1, 2}))
	check(t, "kmp pathological input", slices.Equal(kmp(t, "aaaab").Search(strings.Repeat("a", 30)+"b"), []int{26}))
	check(t, "kmp pattern at the very end", slices.Equal(kmp(t, "xyz").Search("abcxyz"), []int{3}))
	check(t, "kmp pattern equals text", slices.Equal(kmp(t, "abc").Search("abc"), []int{0}))
	check(t, "kmp empty text", len(kmp(t, "abc").Search("")) == 0)
	m := kmp(t, "ab")
	f := m.Failure()
	f[0] = 99
	check(t, "kmp Failure() returns a copy", m.Failure()[0] == 0)
	_, err := NewKmpMatcher("")
	check(t, "kmp rejects empty pattern", errors.Is(err, ErrEmptyPattern))
}

func rollMatchesRehash(t *testing.T, text string, m int) bool {
	t.Helper()
	runes := []rune(text)
	rk := rabinKarp(t, string(runes[:m]))
	h := Hash(string(runes[:m]))
	for i := 0; i+m < len(runes); i++ {
		h = rk.Roll(h, runes[i], runes[i+m])
		if h != Hash(string(runes[i+1:i+1+m])) {
			return false
		}
	}
	return true
}

func TestRabinKarp(t *testing.T) {
	rk := rabinKarp(t, "ACQ")
	check(t, "rabin-karp finds all three", slices.Equal(rk.Search("ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003"), []int{0, 18, 36}))
	check(t, "rabin-karp verifies every hash match", rk.HashMatches() == 3 && rk.FalsePositives() == 0)
	check(t, "rabin-karp roll equals rehash", rollMatchesRehash(t, "the quick brown fox", 5))
	check(t, "rabin-karp overlapping", slices.Equal(rabinKarp(t, "aa").Search("aaaa"), []int{0, 1, 2}))
	check(t, "rabin-karp pattern longer than text", len(rabinKarp(t, "abcd").Search("abc")) == 0)
	check(t, "rabin-karp unicode chars (rune indices, not byte offsets)", slices.Equal(rabinKarp(t, "ü").Search("müller müller"), []int{1, 8}))
	check(t, "hash of ACQ matches the Java value", Hash("ACQ") == (int64('A')*256+int64('C'))*256+int64('Q'))
}

func TestMatchersAgree(t *testing.T) {
	// the three single-pattern matchers agree on 200 seeded random inputs over a tiny alphabet (java.util.Random(7))
	rng := NewJavaRandom(7)
	agree := true
	for trial := 0; trial < 200; trial++ {
		text := randomString(rng, 1+rng.NextIntN(60), 2)
		pat := randomString(rng, 1+rng.NextIntN(4), 2)
		expect := naive(t, text, pat)
		agree = agree && slices.Equal(kmp(t, pat).Search(text), expect) && slices.Equal(rabinKarp(t, pat).Search(text), expect)
	}
	check(t, "kmp and rabin-karp agree with naive on 200 random inputs", agree)

	// Aho-Corasick agrees with running KMP per pattern, on the next 100 random texts
	pats := []string{"ab", "ba", "aab", "bb", "abab"}
	acRandom := must(NewAhoCorasick(pats))
	acAgree := true
	for trial := 0; trial < 100; trial++ {
		text := randomString(rng, 1+rng.NextIntN(80), 2)
		var expect []Match
		for _, p := range pats {
			for _, s := range kmp(t, p).Search(text) {
				expect = append(expect, Match{p, s})
			}
		}
		got := acRandom.Search(text)
		containsAll := true
		for _, e := range expect {
			if !slices.Contains(got, e) {
				containsAll = false
			}
		}
		acAgree = acAgree && len(got) == len(expect) && containsAll
	}
	check(t, "aho-corasick agrees with per-pattern KMP on 100 random texts", acAgree)
}

func TestAhoCorasick(t *testing.T) {
	ac := must(NewAhoCorasick([]string{"he", "she", "his", "hers"}))
	check(t, "aho-corasick trie size", ac.NodeCount() == 10)
	check(t, "aho-corasick ushers", slices.Equal(ac.Search("ushers"), []Match{{"she", 1}, {"he", 2}, {"hers", 2}}))
	check(t, "aho-corasick finds pattern that is a suffix of another", slices.Equal(ac.Search("she"), []Match{{"she", 0}, {"he", 1}}))
	check(t, "aho-corasick no match", len(ac.Search("xyz")) == 0)
	check(t, "aho-corasick duplicate pattern inserted once", len(must(NewAhoCorasick([]string{"ab", "ab"})).Patterns()) == 1)
	_, err := NewAhoCorasick([]string{"a", ""})
	check(t, "aho-corasick rejects empty pattern", errors.Is(err, ErrEmptyPattern))
	// map iteration order varies between runs; the automaton must not
	same := true
	for i := 0; i < 20; i++ {
		other := must(NewAhoCorasick([]string{"he", "she", "his", "hers"}))
		if !slices.Equal(other.Search("ushers she his"), ac.Search("ushers she his")) {
			same = false
		}
	}
	check(t, "aho-corasick is deterministic across 20 builds", same)
}

func TestSanctionsScreener(t *testing.T) {
	screener := must(NewSanctionsScreener(SampleList()))
	check(t, "normalize folds case and punctuation", Normalize("  Al-Rashid   Exchange, Ltd. ") == "AL RASHID EXCHANGE LTD")
	check(t, "exact listed name hits", slices.Equal(names(screener.Screen("IVANOV TRADING, LLC")), []string{"IVANOV", "IVANOV TRADING LLC"}))
	check(t, "listed name inside a longer name hits", slices.Equal(names(screener.Screen("Northern Star Shipping Co.")), []string{"NORTHERN STAR SHIPPING", "STAR SHIPPING"}))
	check(t, "partial token does not hit (Ivanova)", len(screener.Screen("Dmitri Ivanova")) == 0)
	check(t, "partial token does not hit (Rashida)", len(screener.Screen("Rashida Patel")) == 0)
	check(t, "clean name is clear", len(screener.Screen("Acme Widgets Inc")) == 0)
	check(t, "match start is relative to the normalized name", screener.Screen("al rashid exchange house")[0].Start == 3)
	check(t, "automaton size matches the Java demo", screener.AutomatonSize() == 78)
}
