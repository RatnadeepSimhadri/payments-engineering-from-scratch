// Tests for phases/13-risk-fraud-and-compliance/11-auditability-and-evidence (see docs/en.md). Run: go test ./...
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func leaves(n int) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, []byte(fmt.Sprintf("leaf-%d", i)))
	}
	return out
}

func anyContains(problems []string, needle string) bool {
	for _, p := range problems {
		if strings.Contains(p, needle) {
			return true
		}
	}
	return false
}

func TestCanonicalEncoding(t *testing.T) {
	check(t, "netstrings are injective: (ab,c) != (a,bc)", !bytes.Equal(Netstrings("ab", "c"), Netstrings("a", "bc")))
	check(t, "netstring format", string(Netstrings("hello", "")) == "5:hello,0:,")
	check(t, "netstrings count UTF-8 bytes, not chars", strings.HasPrefix(string(Netstrings("€")), "3:"))
	parsed, err := ParseNetstrings(Netstrings("a", "b:c,", ""))
	check(t, "netstrings round-trip", err == nil && len(parsed) == 3 && parsed[0] == "a" && parsed[1] == "b:c," && parsed[2] == "")
	_, err = ParseNetstrings([]byte("5:abc,"))
	check(t, "truncated netstring rejected", errors.Is(err, ErrBadNetstring))
	check(t, "SHA-256 known answer (empty string)", hex.EncodeToString(Sha256()) == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	a := CreateEntry(0, 1000, ZeroHash, "risk.decision", "pay_1", map[string]string{"outcome": "APPROVE", "score": "12"})
	b := CreateEntry(0, 1000, ZeroHash, "risk.decision", "pay_1", map[string]string{"score": "12", "outcome": "APPROVE"})
	check(t, "payload key order does not change the hash", a.Hash == b.Hash)
	rt, err := EntryFromCanonical(a.Canonical())
	check(t, "entry round-trips through canonical bytes", err == nil && rt.Equal(a))
	check(t, "hash covers the payload", a.Hash != CreateEntry(0, 1000, ZeroHash, "risk.decision", "pay_1", map[string]string{"outcome": "DECLINE", "score": "12"}).Hash)
	check(t, "hash covers the timestamp", a.Hash != CreateEntry(0, 1001, ZeroHash, "risk.decision", "pay_1", map[string]string{"outcome": "APPROVE", "score": "12"}).Hash)
	_, err = EntryFromCanonical(Netstrings("other-v9", "0"))
	check(t, "foreign canonical bytes are rejected", errors.Is(err, ErrBadEntry))
}

func TestMerkleTree(t *testing.T) {
	check(t, "empty tree root is SHA-256 of nothing", bytes.Equal(MerkleRoot([][]byte{}), Sha256()))
	l0 := []byte("x")
	check(t, "one-leaf root is the leaf hash", bytes.Equal(MerkleRoot([][]byte{l0}), LeafHash(l0)))
	check(t, "leaf and node hashes are domain-separated", !bytes.Equal(LeafHash(make([]byte, 64)), NodeHash(make([]byte, 32), make([]byte, 32))))
	check(t, "split(5) = 4, split(8) = 4, split(2) = 1", Split(5) == 4 && Split(8) == 4 && Split(2) == 1)
	three := leaves(3)
	manual := NodeHash(NodeHash(LeafHash(three[0]), LeafHash(three[1])), LeafHash(three[2]))
	check(t, "3-leaf root = H(1|H(1|L0|L1)|L2)", bytes.Equal(MerkleRoot(three), manual))
	allProofs := true
	pathLenAt7 := -1
	for n := 1; n <= 17; n++ {
		ls := leaves(n)
		root := MerkleRoot(ls)
		for m := 0; m < n; m++ {
			path, err := AuditPath(ls, m)
			allProofs = allProofs && err == nil && VerifyInclusion(ls[m], int64(m), int64(n), path, root)
			if n == 16 && m == 7 {
				pathLenAt7 = len(path)
			}
		}
	}
	check(t, "every leaf of every tree size 1..17 proves inclusion", allProofs)
	check(t, "audit path in a 16-leaf tree has log2(16) = 4 hashes", pathLenAt7 == 4)
	nine := leaves(9)
	root9 := MerkleRoot(nine)
	path3, _ := AuditPath(nine, 3)
	check(t, "proof for leaf 3 fails at index 4", !VerifyInclusion(nine[3], 4, 9, path3, root9))
	check(t, "proof fails for different leaf data", !VerifyInclusion([]byte("evil"), 3, 9, path3, root9))
	check(t, "proof fails against a tree size of a different shape", !VerifyInclusion(nine[3], 3, 17, path3, root9))
	check(t, "index outside the tree is rejected", !VerifyInclusion(nine[3], 9, 9, [][]byte{}, root9))
	_, err := AuditPath(nine, 9)
	check(t, "audit path for a missing leaf is an error", errors.Is(err, ErrLeafOutOfRange))
}

func TestTimestampAuthorityMatchesJava(t *testing.T) {
	tsa := NewTimestampAuthority(2026, ClockFunc(func() int64 { return T0 }))
	check(t, "SHA1PRNG-derived Ed25519 key equals the JDK's for seed 2026", tsa.PublicKeyBase64() == "MCowBQYDK2VwAyEAt46YrS6rBPHN0adsV8Fu/ul0V/hIXZsA2kXNG9zTc8c=")
	token := tsa.Stamp("ab")
	check(t, "a token verifies under its own key", VerifyToken(tsa.PublicKeyBase64(), token))
	check(t, "garbage keys verify nothing", !VerifyToken("not base64!", token) && !VerifyToken(base64.StdEncoding.EncodeToString([]byte("short")), token))
}

func TestLogAndTamperDetection(t *testing.T) {
	clock := NewManualClock(T0)
	log := BuildDemoLog(clock, DemoTsa(clock))
	entries := log.Entries()
	key := log.TsaPublicKey()
	check(t, "demo log has 9 entries and 2 checkpoints", len(entries) == 9 && len(log.Checkpoints()) == 2)
	check(t, "genesis links to the zero hash", entries[0].PrevHash == ZeroHash)
	check(t, "each entry links to its predecessor", entries[6].PrevHash == entries[5].Hash)
	other := NewManualClock(T0)
	check(t, "log is deterministic across runs", BuildDemoLog(other, DemoTsa(other)).Entries()[8].Hash == entries[8].Hash)
	copied := log.Entries()
	copied = append(copied, Entry{})
	check(t, "Entries() is a copy", len(log.Entries()) == 9 && len(copied) == 10)
	check(t, "untouched log verifies", VerifyLog(entries, log.Checkpoints(), key).OK)

	clock.Set(T0 + 5_000)
	_, err := log.Append("risk.decision", "pay_x", map[string]string{"outcome": "APPROVE"})
	check(t, "backdated append rejected", errors.Is(err, ErrClockWentBackwards))
	check(t, "rejected append left the log unchanged", len(log.Entries()) == 9)
	_, err = NewDecisionLog(NewManualClock(0), DemoTsa(NewManualClock(0))).Seal()
	check(t, "sealing an empty log rejected", errors.Is(err, ErrNothingToSeal))

	edited := log.Entries()
	edited[3] = edited[3].WithPayloadKeepingHash("outcome", "APPROVE")
	r1 := VerifyLog(edited, log.Checkpoints(), key)
	check(t, "edit without rehash detected at #3", !r1.OK && r1.FirstBadSeq == 3)

	rehashed := log.Entries()
	e3 := entries[3]
	p3 := copyPayload(e3.Payload)
	p3["outcome"] = "APPROVE"
	rehashed[3] = CreateEntry(3, e3.TimestampMillis, e3.PrevHash, e3.Kind, e3.Subject, p3)
	r2 := VerifyLog(rehashed, log.Checkpoints(), key)
	check(t, "edit with rehash breaks the link at #4", !r2.OK && r2.FirstBadSeq == 3 && anyContains(r2.Problems, "#4 does not link"))

	rewritten := append([]Entry(nil), rehashed[:4]...)
	for i := 4; i < len(entries); i++ {
		o := entries[i]
		rewritten = append(rewritten, CreateEntry(o.Seq, o.TimestampMillis, rewritten[i-1].Hash, o.Kind, o.Subject, o.Payload))
	}
	check(t, "a fully rewritten chain is internally consistent", VerifyLog(rewritten, nil, key).OK)
	r3 := VerifyLog(rewritten, log.Checkpoints(), key)
	check(t, "the external seal catches the rewrite", !r3.OK && anyContains(r3.Problems, "Merkle root"))

	deleted := append(append([]Entry(nil), entries[:5]...), entries[6:]...)
	r4 := VerifyLog(deleted, log.Checkpoints(), key)
	check(t, "deleting #5 is detected at position 5", !r4.OK && r4.FirstBadSeq == 5)

	r5 := VerifyLog(entries[:7], log.Checkpoints(), key)
	check(t, "truncating the tail is detected", !r5.OK && anyContains(r5.Problems, "truncated"))

	swapped := log.Entries()
	swapped[1], swapped[2] = entries[2], entries[1]
	check(t, "reordering is detected", !VerifyLog(swapped, log.Checkpoints(), key).OK)

	cp := log.Checkpoints()[0]
	forgedTime := Checkpoint{cp.Size, cp.MerkleRootHex, cp.HeadHashHex, TimestampToken{cp.Token.DigestHex, cp.Token.TimeMillis - 3_600_000, cp.Token.SignatureBase64}}
	check(t, "moving a seal's time breaks its signature", !VerifyLog(entries, []Checkpoint{forgedTime}, key).OK)
	impostor := NewTimestampAuthority(999, ClockFunc(func() int64 { return T0 }))
	check(t, "a seal from a different authority is rejected", !VerifyLog(entries, log.Checkpoints(), impostor.PublicKeyBase64()).OK)
}

func TestEvidenceBundles(t *testing.T) {
	clock := NewManualClock(T0)
	log := BuildDemoLog(clock, DemoTsa(clock))
	entries := log.Entries()
	key := log.TsaPublicKey()
	impostor := NewTimestampAuthority(999, ClockFunc(func() int64 { return T0 }))
	cp2 := log.Checkpoints()[1]
	bundle, err := ExportBundle(log, "pay_0002", cp2)
	if err != nil {
		t.Fatal(err)
	}
	entryLines := 0
	for _, l := range javaSplit(bundle, "\n") {
		if strings.HasPrefix(l, "entry ") {
			entryLines++
		}
	}
	check(t, "bundle discloses exactly the subject's 2 entries", entryLines == 2)
	check(t, "bundle does not leak other subjects", !strings.Contains(bundle, b64(entries[3].Canonical())))
	check(t, "bundle verifies with the trusted key", VerifyBundle(bundle, key).OK)
	again, _ := ExportBundle(log, "pay_0002", cp2)
	check(t, "bundle export is deterministic", bundle == again)
	last, _ := ExportBundle(log, "pay_0005", cp2)
	check(t, "bundle for the last entry checks the head", VerifyBundle(last, key).OK)

	changedEntry := strings.ReplaceAll(bundle, b64(entries[5].Canonical()),
		b64(CreateEntry(5, entries[5].TimestampMillis, entries[5].PrevHash, "review.outcome", "pay_0002",
			map[string]string{"result": "DECLINE", "analyst": "analyst-31", "minutes": "4"}).Canonical()))
	check(t, "edited entry in a bundle fails", !VerifyBundle(changedEntry, key).OK)
	otherRoot := strings.ReplaceAll(bundle, cp2.MerkleRootHex, log.Checkpoints()[0].MerkleRootHex)
	check(t, "swapped Merkle root fails the seal", !VerifyBundle(otherRoot, key).OK)
	otherKey := strings.ReplaceAll(bundle, key, impostor.PublicKeyBase64())
	check(t, "bundle naming an untrusted key fails", !VerifyBundle(otherKey, key).OK)
	var dropped strings.Builder
	for _, l := range javaSplit(bundle, "\n") {
		if !strings.HasPrefix(l, "proof 5") {
			dropped.WriteString(l + "\n")
		}
	}
	check(t, "entry without a proof fails", !VerifyBundle(dropped.String(), key).OK)
	check(t, "garbage is unreadable, not accepted", !VerifyBundle("hello", key).OK)
	lines, err := Describe(bundle)
	check(t, "describe decodes the review outcome", err == nil && strings.Contains(lines[1], "result=APPROVE"))
	check(t, "describe prints Java's Instant format", strings.Contains(lines[2], "2026-01-01T00:08:06.250Z") && strings.Contains(lines[0], "2026-01-01T00:00:02Z"))
	check(t, "no trusted key still proves integrity", VerifyBundle(bundle, "").OK)
}
