// Tests for phases/03-systems-data-structures/09-merkle-trees. Run: go test ./...
package main

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"testing"
)

const eightPostingRoot = "4d637a9bd4f8ce3eb34cee489fc275f8ff0893e83ae35d42535b2ba5509fae18"

func b(s string) []byte { return []byte(s) }

func leaves(items ...string) [][]byte {
	out := make([][]byte, len(items))
	for i, s := range items {
		out[i] = b(s)
	}
	return out
}

func tree(t *testing.T, items ...string) *MerkleTree {
	t.Helper()
	mt, err := NewMerkleTree(leaves(items...))
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

func proofOf(t *testing.T, mt *MerkleTree, index int) []ProofStep {
	t.Helper()
	p, err := mt.Proof(index)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHashingPrimitives(t *testing.T) {
	if len(HashLeaf(b("a"))) != 32 {
		t.Error("leaf hash is SHA-256 of 0x00||data (32 bytes)")
	}
	if bytes.Equal(HashLeaf(b("a")), HashNode(b(""), b("a"))) {
		t.Error("leaf hash differs from raw sha256 of data")
	}
	if bytes.Equal(HashLeaf(b("xy")), HashNode(b("x"), b("y"))) {
		t.Error("leaf and node prefixes separate domains")
	}
	if bytes.Equal(HashNode(b("a"), b("b")), HashNode(b("b"), b("a"))) {
		t.Error("node hash is order-sensitive")
	}
}

func TestDeterministicStructure(t *testing.T) {
	one := tree(t, "only")
	if !bytes.Equal(one.Root(), HashLeaf(b("only"))) {
		t.Error("single leaf: root equals leaf hash")
	}
	if one.Height() != 1 || len(proofOf(t, one, 0)) != 0 {
		t.Error("single leaf: height 1, empty proof")
	}
	if !Verify(b("only"), proofOf(t, one, 0), one.Root()) {
		t.Error("single leaf: empty proof verifies")
	}
	two := tree(t, "a", "b")
	if !bytes.Equal(two.Root(), HashNode(HashLeaf(b("a")), HashLeaf(b("b")))) {
		t.Error("two leaves: root is H(1||Ha||Hb)")
	}
	eight := tree(t, "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7")
	if eight.Height() != 4 {
		t.Error("eight leaves: height 4")
	}
	if !bytes.Equal(eight.Root(), tree(t, "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7").Root()) {
		t.Error("same leaves twice give same root")
	}
	if bytes.Equal(eight.Root(), tree(t, "p1", "p0", "p2", "p3", "p4", "p5", "p6", "p7").Root()) {
		t.Error("order matters: swapped leaves change the root")
	}
	root := eight.Root()
	root[0] ^= 0xFF
	if bytes.Equal(root, eight.Root()) {
		t.Error("root() returns a defensive copy")
	}
	if _, err := NewMerkleTree(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Error("empty tree is rejected")
	}
}

func TestInclusionProofs(t *testing.T) {
	eight := tree(t, "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7")
	two := tree(t, "a", "b")
	for i := 0; i < 8; i++ {
		t.Run("leaf "+strconv.Itoa(i), func(t *testing.T) {
			proof := proofOf(t, eight, i)
			if len(proof) != 3 {
				t.Errorf("eight leaves: proof(%d) has 3 siblings, got %d", i, len(proof))
			}
			if !Verify(b("p"+strconv.Itoa(i)), proof, eight.Root()) {
				t.Errorf("eight leaves: proof(%d) verifies", i)
			}
		})
	}
	if proofOf(t, eight, 0)[0].SiblingOnLeft {
		t.Error("proof(0) first sibling is on the right")
	}
	if !proofOf(t, eight, 1)[0].SiblingOnLeft {
		t.Error("proof(1) first sibling is on the left")
	}
	if Verify(b("p9"), proofOf(t, eight, 2), eight.Root()) {
		t.Error("proof fails for wrong leaf data")
	}
	if Verify(b("p2"), proofOf(t, eight, 2), two.Root()) {
		t.Error("proof fails against wrong root")
	}
	if Verify(b("p3"), proofOf(t, eight, 2), eight.Root()) {
		t.Error("proof for leaf 2 does not verify leaf 3")
	}
	tampered := slices.Clone(proofOf(t, eight, 2))
	tampered[1] = ProofStep{tampered[1].Sibling, !tampered[1].SiblingOnLeft}
	if Verify(b("p2"), tampered, eight.Root()) {
		t.Error("flipping a side bit breaks the proof")
	}
	if _, err := eight.Proof(8); !errors.Is(err, ErrIndexOutOfBounds) {
		t.Error("proof index out of range")
	}
}

func TestOddCountsCarryUp(t *testing.T) {
	five := tree(t, "p0", "p1", "p2", "p3", "p4")
	if five.Height() != 4 {
		t.Error("five leaves: height 4")
	}
	if len(proofOf(t, five, 4)) != 1 {
		t.Error("five leaves: proof(4) has one sibling (carried up twice)")
	}
	if len(proofOf(t, five, 0)) != 3 {
		t.Error("five leaves: proof(0) has three siblings")
	}
	for i := 0; i < 5; i++ {
		if !Verify(b("p"+strconv.Itoa(i)), proofOf(t, five, i), five.Root()) {
			t.Errorf("five leaves: proof(%d) verifies", i)
		}
	}
	sixDup := tree(t, "p0", "p1", "p2", "p3", "p4", "p4")
	if bytes.Equal(five.Root(), sixDup.Root()) {
		t.Error("[p0..p4] and [p0..p4,p4] have different roots (no CVE-2012-2459 ambiguity)")
	}
	if five.LeafCount() != 5 || sixDup.LeafCount() != 6 {
		t.Error("leafCount reflects the input")
	}
}

func TestDiff(t *testing.T) {
	eight := tree(t, "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7")
	five := tree(t, "p0", "p1", "p2", "p3", "p4")
	same, _ := eight.Diff(tree(t, "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7"))
	if len(same.DivergentLeaves) != 0 || same.Comparisons != 1 {
		t.Error("identical trees: no divergence, one comparison")
	}
	d5, _ := eight.Diff(tree(t, "p0", "p1", "p2", "p3", "p4", "X", "p6", "p7"))
	if !slices.Equal(d5.DivergentLeaves, []int{5}) {
		t.Error("one bad leaf found at index 5")
	}
	if d5.Comparisons != 7 {
		t.Errorf("one bad leaf costs 1 + 2*3 = 7 comparisons: %d", d5.Comparisons)
	}
	d2, _ := eight.Diff(tree(t, "p0", "X", "p2", "p3", "p4", "p5", "p6", "Y"))
	if !slices.Equal(d2.DivergentLeaves, []int{1, 7}) {
		t.Error("two bad leaves in different halves, ascending order")
	}
	if d2.Comparisons != 11 {
		t.Errorf("two bad leaves in different halves cost 1 + 2*5 = 11 comparisons: %d", d2.Comparisons)
	}
	d45, _ := five.Diff(tree(t, "p0", "p1", "p2", "p3", "Z"))
	if !slices.Equal(d45.DivergentLeaves, []int{4}) {
		t.Error("odd tree: divergent carried-up leaf is found")
	}
	if d45.Comparisons != 5 {
		t.Errorf("odd tree: root, two children, then one per carried level = 5 comparisons: %d", d45.Comparisons)
	}
	if _, err := eight.Diff(five); !errors.Is(err, ErrInvalidArgument) {
		t.Error("diff requires equal leaf counts")
	}
}

func TestPostingsAndCanonicalBytes(t *testing.T) {
	p := Posting{"txn-0001", "alice", "bob", OfMinor(1250, USD)}
	if string(p.CanonicalBytes()) != "txn-0001|alice|bob|1250|USD" {
		t.Error("canonical bytes use minor units, not a formatted amount")
	}
	offByOneCent := Posting{"txn-0001", "alice", "bob", OfMinor(1251, USD)}
	if bytes.Equal(HashLeaf(p.CanonicalBytes()), HashLeaf(offByOneCent.CanonicalBytes())) {
		t.Error("one cent changes the leaf hash")
	}
	if OfMinor(1205, USD).String() != "12.05 USD" {
		t.Error("Money toString pads minor units")
	}
	if OfMinor(-7, USD).String() != "-0.07 USD" {
		t.Error("Money toString negative")
	}
	primary, err := OfPostings(SamplePostings(8))
	if err != nil {
		t.Fatal(err)
	}
	if primary.RootHex() != eightPostingRoot {
		t.Errorf("eight-posting root is pinned across languages: %s", primary.RootHex())
	}
	replica := SamplePostings(8)
	replica[5] = offByOneCent
	replicaTree, _ := OfPostings(replica)
	d, _ := primary.Diff(replicaTree)
	if !slices.Equal(d.DivergentLeaves, []int{5}) {
		t.Error("postings diff locates the off-by-one-cent row")
	}
}
