"""Tests for phases/13-risk-fraud-and-compliance/11-auditability-and-evidence. Run: python3 -m unittest -v"""
import contextlib
import io
import os
import tempfile
import unittest

from main import (T0, Checkpoint, Crypto, DecisionLog, Entry, EvidenceBundle, ManualClock, Merkle, TimestampAuthority,
                  TimestampToken, Verifier, b64, build_demo_log, demo_tsa, instant_str, key_id)


def leaves(n: int) -> list:
    return [f"leaf-{i}".encode() for i in range(n)]


class CanonicalEncodingTest(unittest.TestCase):
    def test_netstrings(self):
        self.assertNotEqual(Crypto.netstrings("ab", "c"), Crypto.netstrings("a", "bc"))
        self.assertEqual(Crypto.netstrings("hello", "").decode(), "5:hello,0:,")
        self.assertTrue(Crypto.netstrings("€").decode().startswith("3:"))
        self.assertEqual(Crypto.parse_netstrings(Crypto.netstrings("a", "b:c,", "")), ["a", "b:c,", ""])
        with self.assertRaises(ValueError):
            Crypto.parse_netstrings(b"5:abc,")
        self.assertEqual(Crypto.sha256().hex(), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

    def test_entry_hash(self):
        a = Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1", {"outcome": "APPROVE", "score": "12"})
        b = Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1", {"score": "12", "outcome": "APPROVE"})
        self.assertEqual(a.hash, b.hash)
        self.assertEqual(Entry.from_canonical(a.canonical()), a)
        self.assertNotEqual(a.hash, Entry.create(0, 1000, Crypto.ZERO_HASH, "risk.decision", "pay_1",
                                                 {"outcome": "DECLINE", "score": "12"}).hash)
        self.assertNotEqual(a.hash, Entry.create(0, 1001, Crypto.ZERO_HASH, "risk.decision", "pay_1",
                                                 {"outcome": "APPROVE", "score": "12"}).hash)

    def test_instant_format_matches_java(self):
        self.assertEqual(instant_str(T0 + 2_000), "2026-01-01T00:00:02Z")
        self.assertEqual(instant_str(T0 + 486_250), "2026-01-01T00:08:06.250Z")


class MerkleTest(unittest.TestCase):
    def test_tree_shape(self):
        self.assertEqual(Merkle.root([]), Crypto.sha256())
        l0 = b"x"
        self.assertEqual(Merkle.root([l0]), Merkle.leaf_hash(l0))
        self.assertNotEqual(Merkle.leaf_hash(bytes(64)), Merkle.node_hash(bytes(32), bytes(32)))
        self.assertTrue(Merkle.split(5) == 4 and Merkle.split(8) == 4 and Merkle.split(2) == 1)
        three = leaves(3)
        manual = Merkle.node_hash(Merkle.node_hash(Merkle.leaf_hash(three[0]), Merkle.leaf_hash(three[1])), Merkle.leaf_hash(three[2]))
        self.assertEqual(Merkle.root(three), manual)

    def test_inclusion_proofs(self):
        all_proofs = True
        path_len_at_7 = -1
        for n in range(1, 18):
            ls = leaves(n)
            root = Merkle.root(ls)
            for m in range(n):
                path = Merkle.audit_path(ls, m)
                all_proofs &= Merkle.verify_inclusion(ls[m], m, n, path, root)
                if n == 16 and m == 7:
                    path_len_at_7 = len(path)
        self.assertTrue(all_proofs)
        self.assertEqual(path_len_at_7, 4)
        nine = leaves(9)
        root9 = Merkle.root(nine)
        self.assertFalse(Merkle.verify_inclusion(nine[3], 4, 9, Merkle.audit_path(nine, 3), root9))
        self.assertFalse(Merkle.verify_inclusion(b"evil", 3, 9, Merkle.audit_path(nine, 3), root9))
        self.assertFalse(Merkle.verify_inclusion(nine[3], 3, 17, Merkle.audit_path(nine, 3), root9))
        self.assertFalse(Merkle.verify_inclusion(nine[3], 9, 9, [], root9))


class LogTest(unittest.TestCase):
    def setUp(self):
        self.clock = ManualClock(T0)
        self.log = build_demo_log(self.clock, demo_tsa(self.clock))
        self.entries = self.log.entries()
        self.key = self.log.tsa_verification_key()

    def test_log_structure(self):
        self.assertTrue(len(self.entries) == 9 and len(self.log.checkpoints()) == 2)
        self.assertEqual(self.entries[0].prev_hash, Crypto.ZERO_HASH)
        self.assertEqual(self.entries[6].prev_hash, self.entries[5].hash)
        again = build_demo_log(ManualClock(T0), demo_tsa(ManualClock(T0)))
        self.assertEqual(again.entries()[8].hash, self.entries[8].hash)
        with self.assertRaises(AttributeError):
            self.log.entries().append(None)
        self.assertTrue(Verifier.verify_log(self.entries, self.log.checkpoints(), self.key).ok)

    def test_entry_hashes_match_the_java_version(self):
        # The chain uses only SHA-256 and netstrings, so it is byte-identical to the Java log (docs, Step 9).
        self.assertEqual(self.entries[0].hash[:8], "5dbd8f85")
        self.assertEqual(self.entries[8].hash[:8], "c30f2620")
        self.assertEqual(self.log.checkpoints()[1].merkle_root_hex[:12], "4a7c7fbbc562")

    def test_append_only(self):
        self.clock.set(T0 + 5_000)
        with self.assertRaises(RuntimeError):
            self.log.append("risk.decision", "pay_x", {"outcome": "APPROVE"})
        self.assertEqual(len(self.log.entries()), 9)
        with self.assertRaises(RuntimeError):
            DecisionLog(ManualClock(0), demo_tsa(ManualClock(0))).seal()

    def test_tamper_detection(self):
        entries, cps, key = self.entries, self.log.checkpoints(), self.key
        edited = list(entries)
        edited[3] = edited[3].with_payload_keeping_hash("outcome", "APPROVE")
        r1 = Verifier.verify_log(edited, cps, key)
        self.assertTrue(not r1.ok and r1.first_bad_seq == 3)

        rehashed = list(entries)
        e3 = entries[3]
        p3 = dict(e3.payload)
        p3["outcome"] = "APPROVE"
        rehashed[3] = Entry.create(3, e3.timestamp_millis, e3.prev_hash, e3.kind, e3.subject, p3)
        r2 = Verifier.verify_log(rehashed, cps, key)
        self.assertTrue(not r2.ok and r2.first_bad_seq == 3 and any("#4 does not link" in s for s in r2.problems))

        rewritten = list(rehashed[:4])
        for i in range(4, len(entries)):
            o = entries[i]
            rewritten.append(Entry.create(o.seq, o.timestamp_millis, rewritten[i - 1].hash, o.kind, o.subject, o.payload))
        self.assertTrue(Verifier.verify_log(rewritten, [], key).ok)
        r3 = Verifier.verify_log(rewritten, cps, key)
        self.assertTrue(not r3.ok and any("Merkle root" in s for s in r3.problems))

        deleted = list(entries)
        del deleted[5]
        r4 = Verifier.verify_log(deleted, cps, key)
        self.assertTrue(not r4.ok and r4.first_bad_seq == 5)

        r5 = Verifier.verify_log(entries[:7], cps, key)
        self.assertTrue(not r5.ok and any("truncated" in s for s in r5.problems))

        swapped = list(entries)
        swapped[1], swapped[2] = entries[2], entries[1]
        self.assertFalse(Verifier.verify_log(swapped, cps, key).ok)

    def test_seal_forgery(self):
        cp = self.log.checkpoints()[0]
        forged_time = Checkpoint(cp.size, cp.merkle_root_hex, cp.head_hash_hex,
                                 TimestampToken(cp.token.digest_hex, cp.token.time_millis - 3_600_000, cp.token.signature_base64))
        self.assertFalse(Verifier.verify_log(self.entries, [forged_time], self.key).ok)
        impostor = TimestampAuthority(999, lambda: T0)
        self.assertFalse(Verifier.verify_log(self.entries, self.log.checkpoints(), impostor.verification_key_base64()).ok)

    def test_operator_without_the_key_cannot_mint_a_seal(self):
        # The HMAC substitution's security property: a seal made under any other key does not verify.
        cp = self.log.checkpoints()[0]
        operator = TimestampAuthority(None, lambda: cp.token.time_millis - 3_600_000)   # a random key the TSA never shared
        backdated = Checkpoint(cp.size, cp.merkle_root_hex, cp.head_hash_hex, operator.stamp(cp.token.digest_hex))
        self.assertFalse(Verifier.verify_log(self.entries, [backdated], self.key).ok)


class BundleTest(unittest.TestCase):
    def setUp(self):
        clock = ManualClock(T0)
        self.log = build_demo_log(clock, demo_tsa(clock))
        self.entries = self.log.entries()
        self.key = self.log.tsa_verification_key()
        self.cp2 = self.log.checkpoints()[1]
        self.bundle = EvidenceBundle.export(self.log, "pay_0002", self.cp2)

    def test_export(self):
        bundle = self.bundle
        self.assertEqual(sum(1 for line in bundle.splitlines() if line.startswith("entry ")), 2)
        self.assertNotIn(b64(self.entries[3].canonical()), bundle)
        self.assertNotIn(self.key, bundle)   # the bundle names the HMAC key by fingerprint; the key itself never leaks
        self.assertTrue(Verifier.verify_bundle(bundle, self.key).ok)
        self.assertEqual(bundle, EvidenceBundle.export(self.log, "pay_0002", self.cp2))
        self.assertTrue(Verifier.verify_bundle(EvidenceBundle.export(self.log, "pay_0005", self.cp2), self.key).ok)

    def test_bundle_tampering(self):
        bundle, key, entries = self.bundle, self.key, self.entries
        e5 = entries[5]
        changed_entry = bundle.replace(b64(e5.canonical()), b64(Entry.create(5, e5.timestamp_millis, e5.prev_hash, "review.outcome", "pay_0002",
                                                                             {"result": "DECLINE", "analyst": "analyst-31", "minutes": "4"}).canonical()))
        self.assertFalse(Verifier.verify_bundle(changed_entry, key).ok)
        other_root = bundle.replace(self.cp2.merkle_root_hex, self.log.checkpoints()[0].merkle_root_hex)
        self.assertFalse(Verifier.verify_bundle(other_root, key).ok)
        impostor = TimestampAuthority(999, lambda: T0)
        other_key = bundle.replace(key_id(key), key_id(impostor.verification_key_base64()))
        self.assertFalse(Verifier.verify_bundle(other_key, key).ok)
        dropped = "".join(line + "\n" for line in bundle.splitlines() if not line.startswith("proof 5"))
        self.assertFalse(Verifier.verify_bundle(dropped, key).ok)
        self.assertFalse(Verifier.verify_bundle("hello", key).ok)
        self.assertIn("result=APPROVE", Verifier.describe(bundle)[1])
        self.assertFalse(Verifier.verify_bundle(bundle, None).ok)   # no trusted key, no verdict

    def test_verifier_cli(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "bundle.txt")
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(self.bundle)
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                code = Verifier.cli([path, self.key])
            self.assertTrue(code == 0 and out.getvalue().rstrip().endswith("VERIFIED"))


if __name__ == "__main__":
    unittest.main()
