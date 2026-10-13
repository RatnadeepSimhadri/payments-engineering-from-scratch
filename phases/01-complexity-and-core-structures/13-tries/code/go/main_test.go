// Tests for phases/01-complexity-and-core-structures/13-tries. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestEmptyTrie(t *testing.T) {
	tr := NewTrie[string]()
	if tr.Size() != 0 || tr.NodeCount() != 1 {
		t.Errorf("empty trie: size=%d nodes=%d", tr.Size(), tr.NodeCount())
	}
	if _, ok := tr.Search("4"); ok || tr.Contains("4") {
		t.Error("search on empty trie must miss")
	}
	if !tr.StartsWith("") || tr.StartsWith("4") {
		t.Error("StartsWith: root exists, nothing else does")
	}
}

func TestInsertSearchStartsWith(t *testing.T) {
	tr := NewTrie[string]()
	if _, replaced := tr.Insert("4111", "A"); replaced {
		t.Error("first insert reported a replacement")
	}
	if old, replaced := tr.Insert("4111", "B"); !replaced || old != "A" || tr.Size() != 1 {
		t.Errorf("replace returned (%q, %v), size %d", old, replaced, tr.Size())
	}
	tr.Insert("411", "C")
	tr.Insert("51", "D")
	if tr.Size() != 3 || tr.NodeCount() != 1+4+2 {
		t.Errorf("size=%d nodes=%d, want 3 and 7", tr.Size(), tr.NodeCount())
	}
	for k, want := range map[string]string{"4111": "B", "411": "C", "51": "D"} {
		if v, ok := tr.Search(k); !ok || v != want {
			t.Errorf("Search(%s) = (%q, %v), want %q", k, v, ok, want)
		}
	}
	for _, k := range []string{"41", "4", "41111"} {
		if _, ok := tr.Search(k); ok {
			t.Errorf("Search(%s) must miss: bare paths and extensions are not keys", k)
		}
	}
	if !tr.StartsWith("4") || !tr.StartsWith("41") || !tr.StartsWith("4111") {
		t.Error("StartsWith must match paths and keys")
	}
	if tr.StartsWith("6") || tr.StartsWith("4112") {
		t.Error("StartsWith must reject absent prefixes")
	}
}

func TestKeysWithPrefixSorted(t *testing.T) {
	tr := NewTrie[string]()
	for _, k := range []string{"4111", "411", "51", "4"} {
		tr.Insert(k, k)
	}
	if got := tr.KeysWithPrefix("4"); !slices.Equal(got, []string{"4", "411", "4111"}) {
		t.Errorf("KeysWithPrefix(4) = %v", got)
	}
	if got := tr.KeysWithPrefix("9"); len(got) != 0 {
		t.Errorf("KeysWithPrefix(9) = %v, want empty", got)
	}
}

func TestLongestPrefixMatch(t *testing.T) {
	tr := NewTrie[string]()
	tr.Insert("4", "VISA")
	tr.Insert("4111", "VISA-SANDBOX")
	tr.Insert("51", "MC")
	cases := []struct {
		query      string
		wantPrefix string
		wantValue  string
		wantOK     bool
	}{
		{"4111111111111111", "4111", "VISA-SANDBOX", true},
		{"4000000000000002", "4", "VISA", true},
		{"411", "4", "VISA", true},
		{"4", "4", "VISA", true},
		{"6011000000000004", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		m, ok := tr.LongestPrefixMatch(c.query)
		if ok != c.wantOK || (ok && (m.Prefix != c.wantPrefix || m.Value != c.wantValue)) {
			t.Errorf("LongestPrefixMatch(%q) = (%+v, %v), want (%q, %q, %v)", c.query, m, ok, c.wantPrefix, c.wantValue, c.wantOK)
		}
	}
	tr.Insert("", "DEFAULT")
	if m, ok := tr.LongestPrefixMatch("9999"); !ok || m.Value != "DEFAULT" {
		t.Error("an empty key must act as a default route")
	}
}

func newDeleteFixture() (*Trie[string], int) {
	d := NewTrie[string]()
	d.Insert("4111", "A")
	d.Insert("411", "B")
	d.Insert("4", "C")
	return d, d.NodeCount()
}

func TestDeleteOfPathOrAbsentKeyChangesNothing(t *testing.T) {
	d, before := newDeleteFixture()
	if d.Delete("41") || d.Delete("5") {
		t.Error("deleting a bare path or an absent key must return false")
	}
	if d.NodeCount() != before || d.Size() != 3 {
		t.Errorf("nodes=%d size=%d changed", d.NodeCount(), d.Size())
	}
}

func TestDeleteLeafKeepsSharedPrefix(t *testing.T) {
	d, before := newDeleteFixture()
	if !d.Delete("4111") || d.NodeCount() != before-1 || d.Size() != 2 {
		t.Fatalf("delete leaf: nodes=%d (before %d) size=%d", d.NodeCount(), before, d.Size())
	}
	if v, ok := d.Search("411"); !ok || v != "B" {
		t.Error("411 must survive")
	}
	if v, ok := d.Search("4"); !ok || v != "C" {
		t.Error("4 must survive")
	}
	if !d.StartsWith("41") || d.StartsWith("4111") {
		t.Error("StartsWith after delete is wrong")
	}
}

func TestDeleteInteriorKeyKeepsDescendants(t *testing.T) {
	d, before := newDeleteFixture()
	if !d.Delete("4") {
		t.Fatal("delete of 4 must succeed")
	}
	if _, ok := d.Search("4"); ok {
		t.Error("4 must be gone")
	}
	if v, ok := d.Search("411"); !ok || v != "B" {
		t.Error("descendant 411 must survive")
	}
	if d.NodeCount() != before {
		t.Errorf("nothing should be pruned: nodes=%d before=%d", d.NodeCount(), before)
	}
}

func TestDeleteEverythingPrunesToRoot(t *testing.T) {
	d, _ := newDeleteFixture()
	for _, k := range []string{"4111", "411", "4"} {
		if !d.Delete(k) {
			t.Fatalf("delete %s failed", k)
		}
	}
	if d.Size() != 0 || d.NodeCount() != 1 {
		t.Errorf("after deleting all: size=%d nodes=%d", d.Size(), d.NodeCount())
	}
	if _, replaced := d.Insert("51", "X"); replaced {
		t.Error("re-insert after full delete reported replacement")
	}
}

func TestRangeToPrefixes(t *testing.T) {
	if got := must(RangeToPrefixes("51", "55")); !slices.Equal(got, []string{"51", "52", "53", "54", "55"}) {
		t.Errorf("51-55 = %v", got)
	}
	if got := must(RangeToPrefixes("4", "4")); !slices.Equal(got, []string{"4"}) {
		t.Errorf("4-4 = %v", got)
	}
	if got := must(RangeToPrefixes("00", "99")); !slices.Equal(got, []string{""}) {
		t.Errorf("00-99 = %v, want the empty prefix", got)
	}
	want := []string{"3528", "3529", "353", "354", "355", "356", "357", "358", "359"}
	if got := must(RangeToPrefixes("3528", "3599")); !slices.Equal(got, want) {
		t.Errorf("3528-3599 = %v", got)
	}
	ms := must(RangeToPrefixes("2221", "2720"))
	if len(ms) != 23 {
		t.Errorf("2221-2720 has %d prefixes, want 23: %v", len(ms), ms)
	}
	for _, p := range []string{"23", "26", "270", "2720"} {
		if !slices.Contains(ms, p) {
			t.Errorf("missing prefix %s", p)
		}
	}
	for _, p := range []string{"222", "27", "2721"} {
		if slices.Contains(ms, p) {
			t.Errorf("unexpected prefix %s", p)
		}
	}
	for n := 2000; n < 3000; n++ {
		s := strconv.Itoa(n)
		covering := 0
		for _, p := range ms {
			if strings.HasPrefix(s, p) {
				covering++
			}
		}
		want := 0
		if n >= 2221 && n <= 2720 {
			want = 1
		}
		if covering != want {
			t.Fatalf("%s covered by %d prefixes, want %d", s, covering, want)
		}
	}
}

func TestRangeToPrefixesRejectsBadInput(t *testing.T) {
	for _, c := range [][2]string{{"51", "555"}, {"56", "55"}, {"5a", "55"}} {
		if _, err := RangeToPrefixes(c[0], c[1]); err == nil {
			t.Errorf("RangeToPrefixes(%s, %s) must fail", c[0], c[1])
		}
	}
}

func TestBinRouterSamplePans(t *testing.T) {
	r := SampleRouter()
	cases := []struct {
		pan  string
		want Route
	}{
		{"4111111111111111", Route{"VISA", "Visa sandbox issuer", "4"}},
		{"5555555555554444", Route{"MASTERCARD", "Mastercard sandbox issuer", "55"}},
		{"2221000000000009", Route{"MASTERCARD", "UNKNOWN", "2221"}},
		{"2720999999999996", Route{"MASTERCARD", "UNKNOWN", "2720"}},
		{"378282246310005", Route{"AMEX", "Amex sandbox issuer", "37"}},
		{"6011111111111117", Route{"DISCOVER", "UNKNOWN", "6011"}},
		{"6451111111111110", Route{"DISCOVER", "UNKNOWN", "645"}},
		{"3530111333300000", Route{"JCB", "UNKNOWN", "353"}},
	}
	for _, c := range cases {
		got, err := r.Route(c.pan)
		if err != nil || got != c.want {
			t.Errorf("Route(%s) = (%+v, %v), want %+v", Mask(c.pan), got, err, c.want)
		}
	}
}

func TestBinRouterRejects(t *testing.T) {
	r := SampleRouter()
	for _, pan := range []string{"2721000000000008", "9999000000000000"} {
		if _, err := r.Route(pan); !errors.Is(err, ErrNoRoute) {
			t.Errorf("Route(%s) err = %v, want ErrNoRoute", pan, err)
		}
	}
	for _, pan := range []string{"41111111111", "41111111111111111111", "4111-1111-1111-1111"} {
		_, err := r.Route(pan)
		if err == nil || errors.Is(err, ErrNoRoute) {
			t.Errorf("Route(%q) err = %v, want a validation error", pan, err)
		}
	}
}

func TestMask(t *testing.T) {
	if Mask("4111111111111111") != "411111******1111" {
		t.Errorf("Mask = %s", Mask("4111111111111111"))
	}
	if Mask("12345") != "*****" {
		t.Errorf("Mask short = %s", Mask("12345"))
	}
}
