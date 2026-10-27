// Lesson: phases/02-algorithms/12-string-matching/docs/en.md
// String Matching: KMP, Rabin-Karp, and Aho-Corasick
// Run:  go run .
// Test: go test ./...
// Sources: Knuth, Morris, Pratt, "Fast pattern matching in strings" (SIAM J. Comput. 1977); Karp & Rabin,
// "Efficient randomized pattern-matching algorithms" (IBM J. R&D 1987); Aho & Corasick, "Efficient string
// matching: an aid to bibliographic search" (CACM 1975). Go 1.22+, standard library only. Same design as Java/Python.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// ErrEmptyPattern is returned where the Java throws IllegalArgumentException("pattern must be non-empty").
var ErrEmptyPattern = errors.New("pattern must be non-empty")

// All matchers index text by code point ([]rune), the counterpart of Java's char index for the Basic Multilingual Plane.

// Match is a match of one pattern in a text: the pattern and the (rune) index of its first character.
type Match struct {
	Pattern string
	Start   int
}

// End is one past the last matched index.
func (m Match) End() int { return m.Start + len([]rune(m.Pattern)) }

func (m Match) String() string { return fmt.Sprintf("Match[pattern=%s, start=%d]", m.Pattern, m.Start) }

func requireNonEmpty(pattern string) error {
	if pattern == "" {
		return ErrEmptyPattern
	}
	return nil
}

// NaiveSearch is the baseline: compare the pattern at every alignment. O(n * m) worst case, and the oracle the
// others are tested against.
func NaiveSearch(text, pattern string) ([]int, error) {
	if err := requireNonEmpty(pattern); err != nil {
		return nil, err
	}
	t, p := []rune(text), []rune(pattern)
	hits := []int{}
	for i := 0; i+len(p) <= len(t); i++ {
		j := 0
		for j < len(p) && t[i+j] == p[j] {
			j++
		}
		if j == len(p) {
			hits = append(hits, i)
		}
	}
	return hits, nil
}

// KmpMatcher is Knuth-Morris-Pratt. The failure function fail[i] is the length of the longest proper prefix of
// pattern[0..i] that is also a suffix of it. On a mismatch after matching j characters, the pattern
// slides so that its first fail[j-1] characters line up with the text already matched; the text
// index never moves backwards. O(n + m) time, O(m) space, and no hashing or randomness.
type KmpMatcher struct {
	pattern []rune
	fail    []int
}

// NewKmpMatcher precomputes the failure function.
func NewKmpMatcher(pattern string) (*KmpMatcher, error) {
	if err := requireNonEmpty(pattern); err != nil {
		return nil, err
	}
	p := []rune(pattern)
	return &KmpMatcher{p, FailureFunction(pattern)}, nil
}

// FailureFunction computes fail[] for p.
func FailureFunction(pattern string) []int {
	p := []rune(pattern)
	fail := make([]int, len(p))
	k := 0 // length of the current longest prefix-suffix
	for i := 1; i < len(p); i++ {
		for k > 0 && p[i] != p[k] {
			k = fail[k-1]
		}
		if p[i] == p[k] {
			k++
		}
		fail[i] = k
	}
	return fail
}

// Failure returns a copy of the failure table.
func (m *KmpMatcher) Failure() []int { return slices.Clone(m.fail) }

// Search returns every (possibly overlapping) start index of the pattern in text.
func (m *KmpMatcher) Search(text string) []int {
	hits := []int{}
	j := 0 // characters of pattern matched so far
	i := 0
	for _, c := range text {
		for j > 0 && c != m.pattern[j] {
			j = m.fail[j-1]
		}
		if c == m.pattern[j] {
			j++
		}
		if j == len(m.pattern) {
			hits = append(hits, i-j+1)
			j = m.fail[j-1] // keep going: matches may overlap
		}
		i++
	}
	return hits
}

// Rabin-Karp constants: a polynomial rolling hash with a large prime modulus so collisions are ~ 1 / Mod per window.
const (
	Base = 256
	Mod  = 1_000_000_007
)

// RabinKarpMatcher hashes the pattern and every window of the text with a polynomial rolling hash; compares
// characters only when the hashes agree. Expected O(n + m), worst case O(n * m) when every window
// collides. The verification step makes it exact; the counters make the collision rate observable.
type RabinKarpMatcher struct {
	pattern        []rune
	patternHash    int64
	highPower      int64 // Base^(m-1) mod Mod, the weight of the outgoing char
	hashMatches    int
	falsePositives int
}

// NewRabinKarpMatcher hashes the pattern.
func NewRabinKarpMatcher(pattern string) (*RabinKarpMatcher, error) {
	if err := requireNonEmpty(pattern); err != nil {
		return nil, err
	}
	p := []rune(pattern)
	h := int64(1)
	for i := 1; i < len(p); i++ {
		h = (h * Base) % Mod
	}
	return &RabinKarpMatcher{pattern: p, patternHash: Hash(pattern), highPower: h}, nil
}

// Hash is the polynomial hash of a whole string.
func Hash(s string) int64 {
	var h int64
	for _, c := range s {
		h = (h*Base + int64(c)) % Mod
	}
	return h
}

// Roll slides the window one character: drop `out`, shift, add `in`. O(1).
func (m *RabinKarpMatcher) Roll(windowHash int64, out, in rune) int64 {
	h := (windowHash - (int64(out)*m.highPower)%Mod + Mod) % Mod
	return (h*Base + int64(in)) % Mod
}

// Search returns every start index of the pattern in text and counts hash matches and false positives.
func (m *RabinKarpMatcher) Search(text string) []int {
	hits := []int{}
	t := []rune(text)
	n, k := len(t), len(m.pattern)
	if k > n {
		return hits
	}
	window := Hash(string(t[:k]))
	for i := 0; ; i++ {
		if window == m.patternHash {
			m.hashMatches++
			if slices.Equal(t[i:i+k], m.pattern) {
				hits = append(hits, i)
			} else {
				m.falsePositives++
			}
		}
		if i+k >= n {
			break
		}
		window = m.Roll(window, t[i], t[i+k])
	}
	return hits
}

// HashMatches is the number of windows whose hash equalled the pattern's.
func (m *RabinKarpMatcher) HashMatches() int { return m.hashMatches }

// FalsePositives is the number of hash matches that verification rejected.
func (m *RabinKarpMatcher) FalsePositives() int { return m.falsePositives }

// acNode is a trie node of the automaton.
type acNode struct {
	next     map[rune]*acNode
	fail     *acNode
	output   *acNode  // nearest node up the fail chain that ends a pattern, or nil
	patterns []string // patterns ending exactly here
	depth    int
}

func newAcNode(depth int) *acNode { return &acNode{next: map[rune]*acNode{}, depth: depth} }

// AhoCorasick is a trie of all patterns plus failure links (longest proper suffix of the current node's string that is
// also a trie node) and output links (nearest node on the failure chain that ends a pattern). One pass over the text
// finds every occurrence of every pattern: O(n + m_total + z) where z is the number of matches.
type AhoCorasick struct {
	root      *acNode
	nodeCount int
	patterns  []string
}

// NewAhoCorasick builds the automaton; an empty pattern is ErrEmptyPattern.
func NewAhoCorasick(patterns []string) (*AhoCorasick, error) {
	ac := &AhoCorasick{root: newAcNode(0), nodeCount: 1, patterns: []string{}}
	for _, p := range patterns {
		if err := ac.insert(p); err != nil {
			return nil, err
		}
	}
	ac.buildLinks()
	return ac, nil
}

func (ac *AhoCorasick) insert(p string) error {
	if err := requireNonEmpty(p); err != nil {
		return err
	}
	cur := ac.root
	for _, c := range p {
		child, ok := cur.next[c]
		if !ok {
			child = newAcNode(cur.depth + 1)
			cur.next[c] = child
			ac.nodeCount++
		}
		cur = child
	}
	if !slices.Contains(cur.patterns, p) {
		cur.patterns = append(cur.patterns, p)
		ac.patterns = append(ac.patterns, p)
	}
	return nil
}

// buildLinks runs a BFS from the root: a node's fail link is found by following its parent's fail chain until a node
// has the same edge. Map iteration order varies between runs, but every node at depth d is linked before depth d+1,
// which is all the construction needs; the automaton is the same whatever order siblings are visited in.
func (ac *AhoCorasick) buildLinks() {
	root := ac.root
	root.fail = root
	var queue []*acNode
	for _, child := range root.next {
		child.fail = root
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		if node.fail != root && len(node.fail.patterns) > 0 {
			node.output = node.fail
		} else {
			node.output = node.fail.output
		}
		for c, child := range node.next {
			f := node.fail
			for f != root && f.next[c] == nil {
				f = f.fail
			}
			target := f.next[c]
			if target == nil || target == child {
				child.fail = root
			} else {
				child.fail = target
			}
			queue = append(queue, child)
		}
	}
	root.output = nil
}

// NodeCount is the number of trie nodes including the root.
func (ac *AhoCorasick) NodeCount() int { return ac.nodeCount }

// Patterns returns the distinct patterns in insertion order (a copy).
func (ac *AhoCorasick) Patterns() []string { return slices.Clone(ac.patterns) }

// Search returns every occurrence of every pattern, in order of the position where the match ends.
func (ac *AhoCorasick) Search(text string) []Match {
	hits := []Match{}
	cur := ac.root
	i := 0
	for _, c := range text {
		for cur != ac.root && cur.next[c] == nil {
			cur = cur.fail
		}
		if step := cur.next[c]; step != nil {
			cur = step
		} else {
			cur = ac.root
		}
		out := cur
		if len(cur.patterns) == 0 {
			out = cur.output
		}
		for ; out != nil; out = out.output {
			for _, p := range out.patterns {
				hits = append(hits, Match{p, i - len([]rune(p)) + 1})
			}
		}
		i++
	}
	return hits
}

// SanctionsScreener normalizes a counterparty name, runs the automaton over it, and reports which list entries appear.
// Normalization is where real screening systems spend their effort; this one does the minimum (case, punctuation,
// whitespace) so the matching algorithm is the thing on display.
type SanctionsScreener struct {
	automaton *AhoCorasick
}

// NewSanctionsScreener normalizes the list and builds the automaton.
func NewSanctionsScreener(listedNames []string) (*SanctionsScreener, error) {
	normalized := make([]string, 0, len(listedNames))
	for _, n := range listedNames {
		normalized = append(normalized, Normalize(n))
	}
	ac, err := NewAhoCorasick(normalized)
	if err != nil {
		return nil, err
	}
	return &SanctionsScreener{ac}, nil
}

// Normalize upper-cases, keeps letters and digits, and collapses everything else to single spaces.
func Normalize(name string) string {
	var sb strings.Builder
	lastSpace := true
	for _, c := range strings.ToUpper(name) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			sb.WriteRune(c)
			lastSpace = false
		} else if !lastSpace {
			sb.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(sb.String())
}

// Screen returns the listed names found as whole-token sequences inside the (normalized) counterparty name, in order of appearance.
func (s *SanctionsScreener) Screen(counterpartyName string) []Match {
	padded := []rune(" " + Normalize(counterpartyName) + " ")
	raw := s.automaton.Search(string(padded))
	whole := []Match{}
	for _, m := range raw {
		// whole-token: bounded by spaces on both sides (the padding guarantees indices are in range)
		if padded[m.Start-1] == ' ' && padded[m.End()] == ' ' {
			whole = append(whole, Match{m.Pattern, m.Start - 1})
		}
	}
	return whole
}

// AutomatonSize is the number of trie nodes.
func (s *SanctionsScreener) AutomatonSize() int { return s.automaton.NodeCount() }

// SampleList is the demo's six listed names.
func SampleList() []string {
	return []string{
		"Ivanov Trading LLC",
		"Ivanov",
		"Northern Star Shipping",
		"Star Shipping",
		"Al-Rashid Exchange",
		"Rashid"}
}

// jstr formats a slice as Java's List.toString does: [a, b, c].
func jstr[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Naive vs KMP on a pathological input ==")
	text := strings.Repeat("a", 30) + "b"
	pattern := "aaaab"
	fmt.Println("text = a*30 + b, pattern = " + pattern)
	fmt.Println("naive hits:  " + jstr(must(NaiveSearch(text, pattern))))
	kmp := must(NewKmpMatcher(pattern))
	fmt.Println("kmp hits:    " + jstr(kmp.Search(text)) + "  failure function " + jstr(kmp.Failure()))
	abab := must(NewKmpMatcher("ABABAC"))
	fmt.Println("failure(ABABAC) = " + jstr(abab.Failure()))

	fmt.Println()
	fmt.Println("== Rabin-Karp: hash the window, verify on hash match ==")
	rk := must(NewRabinKarpMatcher("ACQ"))
	ledger := "ACQ-1001 CAP-1001 ACQ-1002 REF-1001 ACQ-1003"
	fmt.Println("text = " + ledger)
	fmt.Printf("hits %s, hash matches %d, false positives %d\n", jstr(rk.Search(ledger)), rk.HashMatches(), rk.FalsePositives())

	fmt.Println()
	fmt.Println("== Aho-Corasick: every pattern in one pass ==")
	ac := must(NewAhoCorasick([]string{"he", "she", "his", "hers"}))
	fmt.Printf("patterns he, she, his, hers -> %d trie nodes\n", ac.NodeCount())
	fmt.Println("search(\"ushers\") = " + jstr(ac.Search("ushers")))

	fmt.Println()
	fmt.Printf("== Sanctions screening: %d listed names, one pass per payment ==\n", len(SampleList()))
	screener := must(NewSanctionsScreener(SampleList()))
	fmt.Println("automaton nodes:", screener.AutomatonSize())
	beneficiaries := []string{
		"IVANOV TRADING, LLC",
		"Northern Star Shipping Co.",
		"Dmitri Ivanova",
		"Star Shipping Partners",
		"al rashid exchange house",
		"Rashida Patel",
		"Acme Widgets Inc"}
	for _, b := range beneficiaries {
		hits := screener.Screen(b)
		names := make([]string, len(hits))
		for i, m := range hits {
			names[i] = m.Pattern
		}
		verdict := "clear"
		if len(names) > 0 {
			verdict = "HIT " + jstr(names)
		}
		fmt.Printf("%-30s -> %s\n", b, verdict)
	}
}
