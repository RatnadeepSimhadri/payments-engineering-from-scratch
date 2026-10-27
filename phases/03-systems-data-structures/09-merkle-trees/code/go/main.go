// Lesson: phases/03-systems-data-structures/09-merkle-trees/docs/en.md
// Merkle Trees and Content-Addressed Verification
// Run:  go run .
// Test: go test ./...
// Sources: RFC 6962 (Certificate Transparency: leaf/node domain separation, audit paths); FIPS 180-4 (SHA-256);
// CVE-2012-2459 (Bitcoin duplicate-last-node ambiguity). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIndexOutOfBounds is returned where the Java throws IndexOutOfBoundsException.
	ErrIndexOutOfBounds = errors.New("index out of bounds")
)

// Currency is an ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("%w: currency code must be three letters", ErrInvalidArgument)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("%w: exponent must be 0..4", ErrInvalidArgument)
	}
	return Currency{code, exponent}, nil
}

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	sign := ""
	if m.MinorUnits < 0 {
		abs, sign = -abs, "-"
	}
	whole := strconv.FormatInt(abs/scale, 10)
	if m.Currency.Exponent == 0 {
		return sign + whole + " " + m.Currency.Code
	}
	frac := strconv.FormatInt(abs%scale, 10)
	for len(frac) < m.Currency.Exponent {
		frac = "0" + frac
	}
	return sign + whole + "." + frac + " " + m.Currency.Code
}

// Posting is one ledger posting. The leaf of the tree is the hash of CanonicalBytes, not of String():
// a hash is only comparable across systems if both systems hash exactly the same byte sequence.
type Posting struct {
	ID            string
	DebitAccount  string
	CreditAccount string
	Amount        Money
}

func (p Posting) CanonicalBytes() []byte {
	return []byte(p.ID + "|" + p.DebitAccount + "|" + p.CreditAccount + "|" +
		strconv.FormatInt(p.Amount.MinorUnits, 10) + "|" + p.Amount.Currency.Code)
}

// ProofStep is one step of an inclusion proof: the sibling hash and which side the sibling sits on.
type ProofStep struct {
	Sibling       []byte
	SiblingOnLeft bool
}

func (s ProofStep) Equal(o ProofStep) bool {
	return s.SiblingOnLeft == o.SiblingOnLeft && bytes.Equal(s.Sibling, o.Sibling)
}

func (s ProofStep) String() string {
	side := "R:"
	if s.SiblingOnLeft {
		side = "L:"
	}
	return side + Hex(s.Sibling)[:8]
}

// Diff is the result of comparing two trees: which leaf indexes differ, and how many hash comparisons it took.
type Diff struct {
	DivergentLeaves []int
	Comparisons     int
}

// MerkleTree is a binary hash tree with RFC 6962 domain separation: leaves are H(0x00 || data), internal nodes are
// H(0x01 || left || right). An odd node at any level is carried up unchanged, never duplicated.
type MerkleTree struct {
	levels [][][]byte // levels[0] is the leaf hashes; levels[len-1] has exactly one entry, the root
}

const (
	LeafPrefix byte = 0x00
	NodePrefix byte = 0x01
)

func NewMerkleTree(leafData [][]byte) (*MerkleTree, error) {
	if len(leafData) == 0 {
		return nil, fmt.Errorf("%w: a Merkle tree needs at least one leaf", ErrInvalidArgument)
	}
	leaves := make([][]byte, len(leafData))
	for i, d := range leafData {
		leaves[i] = HashLeaf(d)
	}
	t := &MerkleTree{levels: [][][]byte{leaves}}
	current := leaves
	for len(current) > 1 {
		next := make([][]byte, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			if i+1 < len(current) {
				next[i/2] = HashNode(current[i], current[i+1])
			} else {
				next[i/2] = current[i] // odd node: carried up, not duplicated
			}
		}
		t.levels = append(t.levels, next)
		current = next
	}
	return t, nil
}

func OfPostings(postings []Posting) (*MerkleTree, error) {
	data := make([][]byte, len(postings))
	for i, p := range postings {
		data[i] = p.CanonicalBytes()
	}
	return NewMerkleTree(data)
}

func HashLeaf(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{LeafPrefix})
	h.Write(data)
	return h.Sum(nil)
}

func HashNode(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{NodePrefix})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// Root returns a copy, so a caller cannot corrupt the tree by writing into the slice.
func (t *MerkleTree) Root() []byte { return bytes.Clone(t.levels[len(t.levels)-1][0]) }

func (t *MerkleTree) RootHex() string { return Hex(t.Root()) }

func (t *MerkleTree) LeafCount() int { return len(t.levels[0]) }

func (t *MerkleTree) Height() int { return len(t.levels) }

func (t *MerkleTree) LeafHash(index int) []byte { return bytes.Clone(t.levels[0][index]) }

// Proof is the audit path for leaf index: one sibling per level where the node has a sibling.
// Levels where the node was carried up contribute no step, so proofs vary in length.
func (t *MerkleTree) Proof(index int) ([]ProofStep, error) {
	if index < 0 || index >= t.LeafCount() {
		return nil, fmt.Errorf("%w: index %d out of bounds for length %d", ErrIndexOutOfBounds, index, t.LeafCount())
	}
	path := []ProofStep{}
	i := index
	for level := 0; level < len(t.levels)-1; level++ {
		nodes := t.levels[level]
		sibling := i - 1
		if i%2 == 0 {
			sibling = i + 1
		}
		if sibling < len(nodes) {
			path = append(path, ProofStep{bytes.Clone(nodes[sibling]), sibling < i})
		}
		i /= 2
	}
	return path, nil
}

// Verify recomputes the root from a leaf's data and its audit path; true iff it equals the expected root.
func Verify(leafData []byte, proof []ProofStep, expectedRoot []byte) bool {
	h := HashLeaf(leafData)
	for _, step := range proof {
		if step.SiblingOnLeft {
			h = HashNode(step.Sibling, h)
		} else {
			h = HashNode(h, step.Sibling)
		}
	}
	return subtle.ConstantTimeCompare(h, expectedRoot) == 1
}

// DiffTree finds every leaf index where this tree and other disagree, descending only into subtrees
// whose hashes differ. Both trees must have the same leaf count: the count is part of what the
// two sides must agree on before a hash comparison means anything.
func (t *MerkleTree) Diff(other *MerkleTree) (Diff, error) {
	if other.LeafCount() != t.LeafCount() {
		return Diff{}, fmt.Errorf("%w: cannot diff trees with %d and %d leaves", ErrInvalidArgument, t.LeafCount(), other.LeafCount())
	}
	d := Diff{DivergentLeaves: []int{}}
	t.diffNode(other, len(t.levels)-1, 0, &d)
	return d, nil
}

func (t *MerkleTree) diffNode(other *MerkleTree, level, index int, d *Diff) {
	d.Comparisons++
	if bytes.Equal(t.levels[level][index], other.levels[level][index]) {
		return
	}
	if level == 0 {
		d.DivergentLeaves = append(d.DivergentLeaves, index)
		return
	}
	below := len(t.levels[level-1])
	left, right := 2*index, 2*index+1
	if right >= below { // this node was carried up: one child only
		t.diffNode(other, level-1, left, d)
		return
	}
	t.diffNode(other, level-1, left, d)
	t.diffNode(other, level-1, right, d)
}

// ---------------------------------------------------------------- Demo ----

func Hex(b []byte) string { return hex.EncodeToString(b) }

func SamplePostings(n int) []Posting {
	accounts := []string{"alice", "bob", "carol", "dave"}
	postings := make([]Posting, 0, n)
	for i := 0; i < n; i++ {
		from := accounts[i%len(accounts)]
		to := accounts[(i+1)%len(accounts)]
		minor := 1250 + 37*int64(i) // deterministic, looks like real cents
		postings = append(postings, Posting{fmt.Sprintf("txn-%04d", i+1), from, to, OfMinor(minor, USD)})
	}
	return postings
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func joinAny[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	fmt.Println("== 1. Eight postings, one root ==")
	primary := SamplePostings(8)
	primaryTree := must(OfPostings(primary))
	for _, p := range primary {
		fmt.Printf("  %s %s -> %s %s\n", p.ID, p.DebitAccount, p.CreditAccount, p.Amount)
	}
	fmt.Println("  root = " + primaryTree.RootHex())
	fmt.Printf("  height = %d levels for %d leaves\n", primaryTree.Height(), primaryTree.LeafCount())

	fmt.Println()
	fmt.Println("== 2. The replica agrees: one 32-byte comparison instead of eight rows ==")
	replicaSame := must(OfPostings(SamplePostings(8)))
	fmt.Printf("  roots equal = %t\n", bytes.Equal(primaryTree.Root(), replicaSame.Root()))

	fmt.Println()
	fmt.Println("== 3. The replica is one cent off on txn-0006 ==")
	replica := append([]Posting{}, primary...)
	bad := replica[5]
	replica[5] = Posting{bad.ID, bad.DebitAccount, bad.CreditAccount, OfMinor(bad.Amount.MinorUnits+1, USD)}
	replicaTree := must(OfPostings(replica))
	fmt.Println("  primary root = " + primaryTree.RootHex()[:16] + "...")
	fmt.Println("  replica root = " + replicaTree.RootHex()[:16] + "...")
	d := must(primaryTree.Diff(replicaTree))
	fmt.Printf("  divergent leaves = %s (%s)\n", joinAny(d.DivergentLeaves), primary[5].ID)
	fmt.Printf("  hash comparisons = %d (a row-by-row compare would read all 8 rows on both sides)\n", d.Comparisons)
	fmt.Printf("  primary says %s, replica says %s\n", primary[5].Amount, replica[5].Amount)

	fmt.Println()
	fmt.Println("== 4. Inclusion proof: txn-0003 is in the signed daily root ==")
	proof := must(primaryTree.Proof(2))
	fmt.Printf("  proof length = %d siblings %s\n", len(proof), joinAny(proof))
	ok := Verify(primary[2].CanonicalBytes(), proof, primaryTree.Root())
	fmt.Printf("  verify(txn-0003, proof, root) = %t\n", ok)
	forged := Posting{"txn-0003", "alice", "bob", OfMinor(999_99, USD)}
	fmt.Printf("  verify(forged txn-0003 with 999.99 USD) = %t\n", Verify(forged.CanonicalBytes(), proof, primaryTree.Root()))

	fmt.Println()
	fmt.Println("== 5. Odd leaf counts: carry up, never duplicate ==")
	five := must(OfPostings(SamplePostings(5)))
	fmt.Printf("  5 leaves: height %d, proof lengths %d,%d,%d (leaf 4 is carried up twice)\n", five.Height(),
		len(must(five.Proof(0))), len(must(five.Proof(3))), len(must(five.Proof(4))))
	six := SamplePostings(5)
	six = append(six, six[4])
	sixDup := must(OfPostings(six))
	fmt.Printf("  [p0..p4] vs [p0..p4,p4] roots equal = %t  <- duplication would make this true (CVE-2012-2459)\n",
		bytes.Equal(five.Root(), sixDup.Root()))

	fmt.Println()
	fmt.Println("== 6. Scale: 1,000,000 postings disagree on one row ==")
	n := 1_000_000
	hashesPerSide, levelSize := 0, n
	for levelSize > 1 {
		hashesPerSide += levelSize / 2
		levelSize = (levelSize + 1) / 2
	}
	height, l := 1, n
	for l > 1 {
		l = (l + 1) / 2
		height++
	}
	fmt.Printf("  tree height = %d levels, internal hashes to build = %d\n", height, hashesPerSide)
	fmt.Printf("  hashes exchanged to locate one bad row ~ 2 per level = %d, versus %d rows shipped for a full compare\n", 2*(height-1), n)
}
