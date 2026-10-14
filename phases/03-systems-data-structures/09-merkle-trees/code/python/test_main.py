"""Tests for phases/03-systems-data-structures/09-merkle-trees. Run: python3 -m unittest -v"""
import unittest

from main import USD, Diff, MerkleTree, Money, Posting, ProofStep, sample_postings


def b(s: str) -> bytes:
    return s.encode("utf-8")


def leaves(*items: str) -> list[bytes]:
    return [b(s) for s in items]


EIGHT = ("p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7")


class HashingTest(unittest.TestCase):
    def test_primitives_and_domain_separation(self):
        self.assertEqual(len(MerkleTree.hash_leaf(b("a"))), 32)
        self.assertNotEqual(MerkleTree.hash_leaf(b("a")), MerkleTree.hash_node(b(""), b("a")))
        self.assertNotEqual(MerkleTree.hash_leaf(b("xy")), MerkleTree.hash_node(b("x"), b("y")))
        self.assertNotEqual(MerkleTree.hash_node(b("a"), b("b")), MerkleTree.hash_node(b("b"), b("a")))


class StructureTest(unittest.TestCase):
    def test_single_and_two_leaves(self):
        one = MerkleTree(leaves("only"))
        self.assertEqual(one.root(), MerkleTree.hash_leaf(b("only")))
        self.assertTrue(one.height() == 1 and one.proof(0) == [])
        self.assertTrue(MerkleTree.verify(b("only"), one.proof(0), one.root()))
        two = MerkleTree(leaves("a", "b"))
        self.assertEqual(two.root(), MerkleTree.hash_node(MerkleTree.hash_leaf(b("a")), MerkleTree.hash_leaf(b("b"))))

    def test_eight_leaves(self):
        eight = MerkleTree(leaves(*EIGHT))
        self.assertEqual(eight.height(), 4)
        self.assertEqual(eight.root(), MerkleTree(leaves(*EIGHT)).root())
        self.assertNotEqual(eight.root(), MerkleTree(leaves("p1", "p0", "p2", "p3", "p4", "p5", "p6", "p7")).root())
        # Java returns a defensive clone of its byte[]; Python bytes are immutable, so the caller cannot mutate it.
        root = eight.root()
        with self.assertRaises(TypeError):
            root[0] = 0  # type: ignore[index]
        with self.assertRaises(ValueError):
            MerkleTree([])


class ProofTest(unittest.TestCase):
    def setUp(self):
        self.eight = MerkleTree(leaves(*EIGHT))
        self.two = MerkleTree(leaves("a", "b"))

    def test_every_leaf_has_a_verifying_proof(self):
        for i in range(8):
            proof = self.eight.proof(i)
            self.assertEqual(len(proof), 3, f"proof({i})")
            self.assertTrue(MerkleTree.verify(b(f"p{i}"), proof, self.eight.root()), f"proof({i})")

    def test_sides_and_failures(self):
        eight = self.eight
        self.assertFalse(eight.proof(0)[0].sibling_on_left)
        self.assertTrue(eight.proof(1)[0].sibling_on_left)
        self.assertFalse(MerkleTree.verify(b("p9"), eight.proof(2), eight.root()))
        self.assertFalse(MerkleTree.verify(b("p2"), eight.proof(2), self.two.root()))
        self.assertFalse(MerkleTree.verify(b("p3"), eight.proof(2), eight.root()))
        tampered = list(eight.proof(2))
        tampered[1] = ProofStep(tampered[1].sibling, not tampered[1].sibling_on_left)
        self.assertFalse(MerkleTree.verify(b("p2"), tampered, eight.root()))
        with self.assertRaises(IndexError):
            eight.proof(8)
        with self.assertRaises(IndexError):
            eight.proof(-1)  # Python lists would silently accept -1; the tree must not


class OddCountTest(unittest.TestCase):
    def test_carry_up_never_duplicate(self):
        five = MerkleTree(leaves("p0", "p1", "p2", "p3", "p4"))
        self.assertEqual(five.height(), 4)
        self.assertEqual(len(five.proof(4)), 1)
        self.assertEqual(len(five.proof(0)), 3)
        for i in range(5):
            self.assertTrue(MerkleTree.verify(b(f"p{i}"), five.proof(i), five.root()), f"proof({i})")
        six_dup = MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "p4"))
        self.assertNotEqual(five.root(), six_dup.root())
        self.assertTrue(five.leaf_count() == 5 and six_dup.leaf_count() == 6)


class DiffTest(unittest.TestCase):
    def test_pruned_descent(self):
        eight = MerkleTree(leaves(*EIGHT))
        five = MerkleTree(leaves("p0", "p1", "p2", "p3", "p4"))
        self.assertEqual(eight.diff(MerkleTree(leaves(*EIGHT))), Diff([], 1))
        d5 = eight.diff(MerkleTree(leaves("p0", "p1", "p2", "p3", "p4", "X", "p6", "p7")))
        self.assertEqual(d5.divergent_leaves, [5])
        self.assertEqual(d5.comparisons, 7)
        d2 = eight.diff(MerkleTree(leaves("p0", "X", "p2", "p3", "p4", "p5", "p6", "Y")))
        self.assertEqual(d2.divergent_leaves, [1, 7])
        self.assertEqual(d2.comparisons, 11)
        d45 = five.diff(MerkleTree(leaves("p0", "p1", "p2", "p3", "Z")))
        self.assertEqual(d45.divergent_leaves, [4])
        self.assertEqual(d45.comparisons, 5)
        with self.assertRaises(ValueError):
            eight.diff(five)


class PostingTest(unittest.TestCase):
    def test_canonical_bytes_and_money(self):
        p = Posting("txn-0001", "alice", "bob", Money.of_minor(1250, USD))
        self.assertEqual(p.canonical_bytes().decode("utf-8"), "txn-0001|alice|bob|1250|USD")
        off_by_one_cent = Posting("txn-0001", "alice", "bob", Money.of_minor(1251, USD))
        self.assertNotEqual(MerkleTree.hash_leaf(p.canonical_bytes()), MerkleTree.hash_leaf(off_by_one_cent.canonical_bytes()))
        self.assertEqual(str(Money.of_minor(1205, USD)), "12.05 USD")
        self.assertEqual(str(Money.of_minor(-7, USD)), "-0.07 USD")
        primary = MerkleTree.of_postings(sample_postings(8))
        replica = sample_postings(8)
        replica[5] = off_by_one_cent
        self.assertEqual(primary.diff(MerkleTree.of_postings(replica)).divergent_leaves, [5])

    def test_root_is_identical_to_java(self):
        # Cross-language pin: the Java demo prints this root for the eight sample postings.
        self.assertEqual(MerkleTree.of_postings(sample_postings(8)).root_hex(),
                         "4d637a9bd4f8ce3eb34cee489fc275f8ff0893e83ae35d42535b2ba5509fae18")

    def test_money_rejects_float(self):
        with self.assertRaises(TypeError):
            Money(12.5, USD)  # type: ignore[arg-type]


if __name__ == "__main__":
    unittest.main()
