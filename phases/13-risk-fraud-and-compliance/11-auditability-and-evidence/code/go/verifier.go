// Verifier for phases/13-risk-fraud-and-compliance/11-auditability-and-evidence (see docs/en.md).
// The program a regulator or auditor runs. It trusts nothing in the log except the time-stamp authority's public key.
// Run: go run . verify bundle.txt [trusted-tsa-public-key-base64]
// Exit code 0 means every check passed; 1 means at least one problem was found.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// javaSplit is String.split(sep): trailing empty strings are dropped.
func javaSplit(s, sep string) []string {
	parts := strings.Split(s, sep)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// instantString prints like java.time.Instant.toString(): fractional seconds only when non-zero.
func instantString(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	if ms%1000 == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

// VerifyLog verifies a whole log against its checkpoints: hashes, links, sequence, time order, Merkle roots, signatures.
func VerifyLog(entries []Entry, checkpoints []Checkpoint, trustedKey string) Report {
	problems := []string{}
	firstBad := int64(math.MaxInt64)
	for i, e := range entries {
		idx := int64(i)
		if e.Seq != idx {
			problems = append(problems, fmt.Sprintf("position %d holds seq %d (entry missing or reordered)", i, e.Seq))
			firstBad = min(firstBad, idx)
		}
		if e.RecomputeHash() != e.Hash {
			problems = append(problems, fmt.Sprintf("entry #%d content does not match its hash", i))
			firstBad = min(firstBad, idx)
		}
		expectedPrev := ZeroHash
		if i > 0 {
			expectedPrev = entries[i-1].Hash
		}
		if e.PrevHash != expectedPrev {
			problems = append(problems, fmt.Sprintf("entry #%d does not link to the entry before it", i))
			blamed := idx
			if e.Seq == idx && i > 0 { // either side of a broken link may be the edit
				blamed = idx - 1
			}
			firstBad = min(firstBad, blamed)
		}
		if i > 0 && e.TimestampMillis < entries[i-1].TimestampMillis {
			problems = append(problems, fmt.Sprintf("entry #%d is timestamped before entry #%d", i, i-1))
			firstBad = min(firstBad, idx)
		}
	}
	lastGoodSize := int64(0)
	rootFailureSeen := false
	for _, cp := range checkpoints {
		digest := StatementDigest(cp.Size, cp.MerkleRootHex, cp.HeadHashHex)
		if digest != cp.Token.DigestHex || !VerifyToken(trustedKey, cp.Token) {
			problems = append(problems, fmt.Sprintf("checkpoint size %d: time-stamp signature does not verify", cp.Size))
			continue
		}
		if cp.Size > int64(len(entries)) {
			problems = append(problems, fmt.Sprintf("log truncated: checkpoint covers %d entries, log has %d", cp.Size, len(entries)))
			firstBad = min(firstBad, int64(len(entries)))
			continue
		}
		root := hex.EncodeToString(MerkleRoot(LeafData(entries, cp.Size)))
		good := true
		if root != cp.MerkleRootHex {
			problems = append(problems, fmt.Sprintf("checkpoint size %d: Merkle root differs from the sealed root (history rewritten)", cp.Size))
			good = false
		}
		last := entries[cp.Size-1]
		if last.Hash != cp.HeadHashHex {
			problems = append(problems, fmt.Sprintf("checkpoint size %d: head hash differs from the sealed head", cp.Size))
			good = false
		}
		if last.TimestampMillis > cp.Token.TimeMillis {
			problems = append(problems, fmt.Sprintf("checkpoint size %d: an entry claims a time after its own seal", cp.Size))
			good = false
		}
		if good && !rootFailureSeen {
			lastGoodSize = cp.Size
		}
		if !good && !rootFailureSeen {
			rootFailureSeen = true
			// Entry-level evidence is more precise. Without it, the sealed prefix that still verifies bounds the change.
			if firstBad == math.MaxInt64 {
				firstBad = lastGoodSize
			}
		}
	}
	return ReportOf(problems, firstBad)
}

// Parsed is the header and lines of a bundle.
type Parsed struct {
	Header  map[string]string
	Entries [][]string
	Proofs  map[int64]string
}

// ErrBadBundle is returned when the text is not an evidence bundle.
var ErrBadBundle = errors.New("not an evidence bundle")

func ParseBundle(text string) (Parsed, error) {
	lines := javaSplit(text, "\n")
	if len(lines) == 0 || lines[0] != BundleMagic {
		return Parsed{}, ErrBadBundle
	}
	p := Parsed{Header: map[string]string{}, Entries: [][]string{}, Proofs: map[int64]string{}}
	for i := 1; i < len(lines); i++ {
		parts := javaSplit(lines[i], " ")
		if len(parts) == 0 {
			return Parsed{}, fmt.Errorf("%w: bad header line %d", ErrBadBundle, i)
		}
		switch parts[0] {
		case "entry":
			if len(parts) != 4 {
				return Parsed{}, fmt.Errorf("%w: bad entry line %d", ErrBadBundle, i)
			}
			p.Entries = append(p.Entries, parts)
		case "proof":
			if len(parts) != 3 {
				return Parsed{}, fmt.Errorf("%w: bad proof line %d", ErrBadBundle, i)
			}
			seq, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return Parsed{}, fmt.Errorf("%w: bad proof line %d", ErrBadBundle, i)
			}
			p.Proofs[seq] = parts[2]
		default:
			if len(parts) != 2 {
				return Parsed{}, fmt.Errorf("%w: bad header line %d", ErrBadBundle, i)
			}
			p.Header[parts[0]] = parts[1]
		}
	}
	return p, nil
}

func headerInt(h map[string]string, key string) int64 {
	v, ok := h[key]
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// VerifyBundle verifies a bundle: the seal is signed by the trusted authority, and every disclosed entry is inside the
// sealed tree. An empty trustedKey means "use the key inside the bundle", which proves integrity but not origin.
func VerifyBundle(text, trustedKey string) Report {
	problems := []string{}
	firstBad := int64(math.MaxInt64)
	p, err := ParseBundle(text)
	if err != nil {
		return ReportOf([]string{"unreadable bundle: " + err.Error()}, 0)
	}
	h := p.Header
	subject, root, head := h["subject"], h["merkle-root"], h["head-hash"]
	size, tsaTime := headerInt(h, "tree-size"), headerInt(h, "tsa-time")
	key := trustedKey
	if key == "" {
		key = h["tsa-public-key"]
	}
	if trustedKey != "" && trustedKey != h["tsa-public-key"] {
		problems = append(problems, "bundle names a different time-stamp key than the one the verifier trusts")
	}
	token := TimestampToken{StatementDigest(size, root, head), tsaTime, h["tsa-signature"]}
	if !VerifyToken(key, token) {
		problems = append(problems, "seal signature does not verify (size, root, head, or time altered)")
	}
	if len(p.Entries) == 0 {
		problems = append(problems, "bundle discloses no entries")
	}
	var previous *Entry
	for _, line := range p.Entries {
		seq, _ := strconv.ParseInt(line[1], 10, 64)
		raw, err := base64.StdEncoding.DecodeString(line[2])
		var e Entry
		if err == nil {
			e, err = EntryFromCanonical(raw)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("entry #%d is malformed: %v", seq, err))
			firstBad = min(firstBad, seq)
			continue
		}
		if e.Hash != line[3] || e.Seq != seq {
			problems = append(problems, fmt.Sprintf("entry #%d content does not match its stated hash", seq))
			firstBad = min(firstBad, seq)
		}
		if e.Subject != subject {
			problems = append(problems, fmt.Sprintf("entry #%d is about %s, not %s", seq, e.Subject, subject))
		}
		if e.TimestampMillis > tsaTime {
			problems = append(problems, fmt.Sprintf("entry #%d claims a time after the seal", seq))
		}
		proof, hasProof := p.Proofs[seq]
		path := [][]byte{}
		if hasProof && proof != "-" {
			for _, hx := range strings.Split(proof, ",") {
				b, err := hex.DecodeString(hx)
				if err != nil {
					hasProof = false
					break
				}
				path = append(path, b)
			}
		}
		leaf, _ := hex.DecodeString(e.Hash)
		expected, _ := hex.DecodeString(root)
		if !hasProof || !VerifyInclusion(leaf, seq, size, path, expected) {
			problems = append(problems, fmt.Sprintf("entry #%d is not provably inside the sealed tree", seq))
			firstBad = min(firstBad, seq)
		}
		if seq == size-1 && e.Hash != head {
			problems = append(problems, "last entry does not match the sealed head")
		}
		if previous != nil && previous.Seq+1 == seq && e.PrevHash != previous.Hash {
			problems = append(problems, fmt.Sprintf("entries #%d and #%d are adjacent but not linked", previous.Seq, seq))
		}
		prev := e
		previous = &prev
	}
	if firstBad == math.MaxInt64 {
		firstBad = 0
	}
	return ReportOf(problems, firstBad)
}

// Describe gives human-readable lines for the disclosed entries, decoded from the canonical bytes (the only source of truth).
func Describe(text string) ([]string, error) {
	p, err := ParseBundle(text)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, line := range p.Entries {
		raw, err := base64.StdEncoding.DecodeString(line[2])
		if err != nil {
			return nil, err
		}
		e, err := EntryFromCanonical(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("#%d %s %s %s", e.Seq, instantString(e.TimestampMillis), e.Kind, e.PayloadString()))
	}
	out = append(out, fmt.Sprintf("sealed: %s entries, no later than %s", p.Header["tree-size"], instantString(headerInt(p.Header, "tsa-time"))))
	return out, nil
}

// verifierMain is the regulator's entry point: `go run . verify bundle.txt [trusted-key]`. Returns the exit code.
func verifierMain(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: go run . verify bundle.txt [trusted-tsa-public-key-base64]")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	trusted := ""
	if len(args) > 1 {
		trusted = args[1]
	} else {
		fmt.Println("warning: no trusted key given; using the key inside the bundle (proves integrity, not origin)")
	}
	r := VerifyBundle(string(data), trusted)
	if r.OK {
		lines, err := Describe(string(data))
		if err == nil {
			for _, l := range lines {
				fmt.Println(l)
			}
		}
	}
	for _, problem := range r.Problems {
		fmt.Println("PROBLEM: " + problem)
	}
	if r.OK {
		fmt.Println("VERIFIED")
		return 0
	}
	fmt.Println("FAILED")
	return 1
}
