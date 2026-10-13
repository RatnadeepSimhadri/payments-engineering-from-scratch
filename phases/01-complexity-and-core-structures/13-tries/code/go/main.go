// Lesson: phases/01-complexity-and-core-structures/13-tries/docs/en.md
// Tries and Prefix Trees
// Run:  go run .
// Test: go test ./...
// Sources: Fredkin, "Trie Memory" (CACM 1960); ISO/IEC 7812-1 (issuer identification numbers, PAN length 8-19 digits);
// Mastercard 2-series BIN range 2221-2720 (public announcement, 2017). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Match is the result of a longest-prefix lookup: which stored prefix matched and the value stored under it.
type Match[V any] struct {
	Prefix string
	Value  V
}

type node[V any] struct {
	children map[rune]*node[V]
	terminal bool
	value    V
}

func newNode[V any]() *node[V] { return &node[V]{children: make(map[rune]*node[V])} }

// Trie is keyed by strings, one node per character. Invariant: every stored key corresponds to exactly one
// root-to-node path whose final node is terminal; every node that is neither terminal nor an ancestor of a
// terminal node has been pruned. Children are iterated in sorted order so results are deterministic.
type Trie[V any] struct {
	root      *node[V]
	size      int // number of keys
	nodeCount int // including the root
}

func NewTrie[V any]() *Trie[V] { return &Trie[V]{root: newNode[V](), nodeCount: 1} }

// Insert stores or replaces. It returns the previous value for this exact key and whether there was one.
func (t *Trie[V]) Insert(key string, value V) (old V, replaced bool) {
	n := t.root
	for _, c := range key {
		next, ok := n.children[c]
		if !ok {
			next = newNode[V]()
			n.children[c] = next
			t.nodeCount++
		}
		n = next
	}
	if n.terminal {
		old, replaced = n.value, true
	} else {
		t.size++
	}
	n.terminal = true
	n.value = value
	return old, replaced
}

// Search is an exact-match lookup. "41" does not match a stored "411".
func (t *Trie[V]) Search(key string) (V, bool) {
	n := t.walk(key)
	if n == nil || !n.terminal {
		var zero V
		return zero, false
	}
	return n.value, true
}

func (t *Trie[V]) Contains(key string) bool {
	_, ok := t.Search(key)
	return ok
}

// StartsWith reports whether any stored key begins with this prefix (including the prefix itself).
func (t *Trie[V]) StartsWith(prefix string) bool { return t.walk(prefix) != nil }

// LongestPrefixMatch returns the longest stored key that is a prefix of the query, with its value. It walks the
// query once, remembering the deepest terminal node seen, so a stored "4" and a stored "4111" both match
// "41111..." and "4111" wins.
func (t *Trie[V]) LongestPrefixMatch(query string) (Match[V], bool) {
	n := t.root
	var best Match[V]
	found := false
	if n.terminal {
		best, found = Match[V]{"", n.value}, true
	}
	for i, c := range query {
		n = n.children[c]
		if n == nil {
			break
		}
		if n.terminal {
			best, found = Match[V]{query[:i+len(string(c))], n.value}, true
		}
	}
	return best, found
}

// Delete removes a key and reports whether it was stored. It prunes every node on the path that is left with no
// children and no terminal mark, so shared prefixes of other keys are untouched and nothing leaks.
func (t *Trie[V]) Delete(key string) bool {
	runes := []rune(key)
	path := make([]*node[V], 0, len(runes)+1)
	n := t.root
	path = append(path, n)
	for _, c := range runes {
		n = n.children[c]
		if n == nil {
			return false
		}
		path = append(path, n)
	}
	if !n.terminal {
		return false
	}
	var zero V
	n.terminal = false
	n.value = zero
	t.size--
	for i := len(runes); i > 0; i-- {
		child := path[i]
		if child.terminal || len(child.children) > 0 {
			break
		}
		delete(path[i-1].children, runes[i-1])
		t.nodeCount--
	}
	return true
}

// KeysWithPrefix returns every stored key that begins with the prefix, in sorted order. The autocomplete operation.
func (t *Trie[V]) KeysWithPrefix(prefix string) []string {
	out := []string{}
	if start := t.walk(prefix); start != nil {
		t.collect(start, prefix, &out)
	}
	return out
}

func (t *Trie[V]) collect(n *node[V], acc string, out *[]string) {
	if n.terminal {
		*out = append(*out, acc)
	}
	keys := make([]rune, 0, len(n.children))
	for c := range n.children {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for _, c := range keys {
		t.collect(n.children[c], acc+string(c), out)
	}
}

func (t *Trie[V]) walk(s string) *node[V] {
	n := t.root
	for _, c := range s {
		if n == nil {
			return nil
		}
		n = n.children[c]
	}
	return n
}

func (t *Trie[V]) Size() int      { return t.size }
func (t *Trie[V]) NodeCount() int { return t.nodeCount }

// Route is where a card number routes: the network that owns the BIN, the issuer if known, and the prefix that decided it.
type Route struct {
	Network       string
	Issuer        string
	MatchedPrefix string
}

// ErrNoRoute is returned when no stored network prefix matches the PAN.
var ErrNoRoute = errors.New("no route")

func requireDigits(s string) error {
	if s == "" {
		return errors.New("expected a non-empty digit string, got \"\"")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return fmt.Errorf("expected a non-empty digit string, got %q", s)
		}
	}
	return nil
}

// Mask shows the first six and last four digits: the only form of a PAN that may ever be logged (PCI DSS 3.4).
func Mask(pan string) string {
	if len(pan) <= 10 {
		return strings.Repeat("*", len(pan))
	}
	return pan[:6] + strings.Repeat("*", len(pan)-10) + pan[len(pan)-4:]
}

// RangeToPrefixes decomposes a closed numeric range [lo, hi] of equal-length digit strings into the fewest
// prefixes that cover exactly the numbers in it. Greedy: from lo, take the widest prefix (most trailing zeros
// replaced) whose block still ends at or before hi, emit it, and continue from the number after that block.
func RangeToPrefixes(lo, hi string) ([]string, error) {
	if err := requireDigits(lo); err != nil {
		return nil, err
	}
	if err := requireDigits(hi); err != nil {
		return nil, err
	}
	if len(lo) != len(hi) {
		return nil, fmt.Errorf("lo and hi must have equal length: %s, %s", lo, hi)
	}
	if lo > hi {
		return nil, fmt.Errorf("lo must not exceed hi: %s > %s", lo, hi)
	}
	length := len(lo)
	out := []string{}
	cur := lo
	for cur <= hi {
		k := 0 // number of trailing digits the prefix will leave free
		for k < length && cur[length-1-k] == '0' && cur[:length-1-k]+strings.Repeat("9", k+1) <= hi {
			k++
		}
		prefix := cur[:length-k]
		out = append(out, prefix)
		blockEnd := prefix + strings.Repeat("9", k)
		if blockEnd == strings.Repeat("9", length) { // the block ran to the end of the digit space
			break
		}
		cur = increment(blockEnd)
	}
	return out, nil
}

// increment adds one to a digit string of fixed width; the caller guarantees it is not all nines.
func increment(digits string) string {
	d := []byte(digits)
	i := len(d) - 1
	for d[i] == '9' {
		d[i] = '0'
		i--
	}
	d[i]++
	return string(d)
}

// BinRouter: the leading digits of a PAN (the Bank Identification Number, ISO/IEC 7812 "issuer identification
// number") decide which network and which issuer a transaction goes to. Networks publish both prefixes ("4" is
// Visa) and numeric ranges (Mastercard's 2221-2720); ranges are decomposed into the minimal set of prefixes so
// one longest-prefix-match answers every lookup.
type BinRouter struct {
	networks *Trie[string]
	issuers  *Trie[string]
}

func NewBinRouter() *BinRouter {
	return &BinRouter{networks: NewTrie[string](), issuers: NewTrie[string]()}
}

func (r *BinRouter) AddNetwork(prefix, network string) error {
	if err := requireDigits(prefix); err != nil {
		return err
	}
	r.networks.Insert(prefix, network)
	return nil
}

func (r *BinRouter) AddNetworkRange(lo, hi, network string) error {
	prefixes, err := RangeToPrefixes(lo, hi)
	if err != nil {
		return err
	}
	for _, p := range prefixes {
		r.networks.Insert(p, network)
	}
	return nil
}

func (r *BinRouter) AddIssuer(prefix, issuer string) error {
	if err := requireDigits(prefix); err != nil {
		return err
	}
	r.issuers.Insert(prefix, issuer)
	return nil
}

func (r *BinRouter) Route(pan string) (Route, error) {
	if err := requireDigits(pan); err != nil {
		return Route{}, err
	}
	if len(pan) < 12 || len(pan) > 19 {
		return Route{}, fmt.Errorf("PAN must be 12-19 digits, got %d", len(pan))
	}
	network, ok := r.networks.LongestPrefixMatch(pan)
	if !ok {
		return Route{}, fmt.Errorf("%w: no network owns a prefix of %s", ErrNoRoute, Mask(pan))
	}
	issuerName := "UNKNOWN"
	if issuer, ok := r.issuers.LongestPrefixMatch(pan); ok {
		issuerName = issuer.Value
	}
	return Route{network.Value, issuerName, network.Prefix}, nil
}

func (r *BinRouter) NetworkPrefixCount() int { return r.networks.Size() }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// SampleRouter holds test BINs and PANs: the publicly documented test numbers every gateway sandbox accepts.
func SampleRouter() *BinRouter {
	r := NewBinRouter()
	_ = r.AddNetwork("4", "VISA")
	_ = r.AddNetworkRange("51", "55", "MASTERCARD")
	_ = r.AddNetworkRange("2221", "2720", "MASTERCARD")
	_ = r.AddNetwork("34", "AMEX")
	_ = r.AddNetwork("37", "AMEX")
	_ = r.AddNetwork("6011", "DISCOVER")
	_ = r.AddNetwork("65", "DISCOVER")
	_ = r.AddNetworkRange("644", "649", "DISCOVER")
	_ = r.AddNetworkRange("3528", "3589", "JCB")
	_ = r.AddIssuer("411111", "Visa sandbox issuer")
	_ = r.AddIssuer("555555", "Mastercard sandbox issuer")
	_ = r.AddIssuer("378282", "Amex sandbox issuer")
	return r
}

func showValue(v string, ok bool) string {
	if !ok {
		return "(none)"
	}
	return v
}

func showMatch(m Match[string], ok bool) string {
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("%q -> %s", m.Prefix, m.Value)
}

func fmtList(xs []string) string { return "[" + strings.Join(xs, ", ") + "]" }

func main() {
	fmt.Println("== A trie of card prefixes ==")
	t := NewTrie[string]()
	t.Insert("4", "VISA")
	t.Insert("4111", "VISA (sandbox range)")
	t.Insert("41", "VISA (sub-range)")
	t.Insert("51", "MASTERCARD")
	fmt.Printf("keys=%d nodes=%d\n", t.Size(), t.NodeCount())
	fmt.Printf("search(\"41\")   = %s\n", showValue(t.Search("41")))
	fmt.Printf("search(\"411\")  = %s   (a path, not a key)\n", showValue(t.Search("411")))
	fmt.Printf("startsWith(\"411\") = %v\n", t.StartsWith("411"))
	fmt.Printf("longestPrefixMatch(\"4111111111111111\") = %s\n", showMatch(t.LongestPrefixMatch("4111111111111111")))
	fmt.Printf("longestPrefixMatch(\"4000000000000002\") = %s\n", showMatch(t.LongestPrefixMatch("4000000000000002")))
	fmt.Printf("longestPrefixMatch(\"6011000000000004\") = %s\n", showMatch(t.LongestPrefixMatch("6011000000000004")))
	fmt.Printf("keysWithPrefix(\"4\") = %s\n", fmtList(t.KeysWithPrefix("4")))
	t.Delete("4111")
	fmt.Printf("after delete(\"4111\"): keys=%d nodes=%d search(\"41\")=%s startsWith(\"41\")=%v\n",
		t.Size(), t.NodeCount(), showValue(t.Search("41")), t.StartsWith("41"))

	fmt.Println("\n== A numeric range becomes a handful of prefixes ==")
	ms := must(RangeToPrefixes("2221", "2720"))
	fmt.Printf("2221-2720 -> %d prefixes: %s\n", len(ms), fmtList(ms))
	fmt.Printf("51-55     -> %s\n", fmtList(must(RangeToPrefixes("51", "55"))))
	fmt.Printf("3528-3589 -> %s\n", fmtList(must(RangeToPrefixes("3528", "3589"))))

	fmt.Println("\n== BIN routing with longest-prefix match ==")
	router := SampleRouter()
	fmt.Printf("network prefixes stored: %d\n", router.NetworkPrefixCount())
	for _, pan := range []string{"4111111111111111", "5555555555554444", "2221000000000009", "2720999999999996",
		"378282246310005", "6011111111111117", "6451111111111110", "3530111333300000"} {
		route := must(router.Route(pan))
		fmt.Printf("%-20s -> %-10s issuer=%-27s via prefix %s\n", Mask(pan), route.Network, route.Issuer, route.MatchedPrefix)
	}
	for _, pan := range []string{"2721000000000008", "9999000000000000"} {
		if _, err := router.Route(pan); errors.Is(err, ErrNoRoute) {
			fmt.Println("rejected:", strings.TrimPrefix(err.Error(), ErrNoRoute.Error()+": "))
		}
	}
}
