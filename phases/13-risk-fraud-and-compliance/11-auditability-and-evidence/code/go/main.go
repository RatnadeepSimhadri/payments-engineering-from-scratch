// Lesson: phases/13-risk-fraud-and-compliance/11-auditability-and-evidence/docs/en.md
// Auditability: Immutable Logs and Evidence Bundles
// Run:  go run .        Verify a bundle: go run . verify bundle.txt [trusted-key]
// Test: go test ./...
// Sources: RFC 6962 / RFC 9162 (Certificate Transparency Merkle trees, audit paths); RFC 3161 (time-stamp protocol);
// RFC 8032 (Ed25519); D. J. Bernstein, netstrings. Go 1.22+, standard library only (crypto/sha256, crypto/ed25519).
// Same design as the Java version, Ed25519 seal included; the Python port substitutes HMAC because it lacks Ed25519.
package main

import (
	"crypto/ed25519"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 currency, copied from Phase 10, Lesson 01 (subset).
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

// Money is integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01 (formatting subset).
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
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// Clock is the injected time source. Production uses the system clock; demos and tests step it by hand.
type Clock interface{ Millis() int64 }

// ClockFunc adapts a function to Clock (Java's lambda `() -> logClock.millis() + 250`).
type ClockFunc func() int64

func (f ClockFunc) Millis() int64 { return f() }

// ManualClock is a clock the caller moves explicitly. Can be moved backwards, which is exactly what the log must reject.
type ManualClock struct{ now int64 }

func NewManualClock(start int64) *ManualClock { return &ManualClock{start} }
func (c *ManualClock) Millis() int64          { return c.now }
func (c *ManualClock) Advance(ms int64)       { c.now += ms }
func (c *ManualClock) Set(ms int64)           { c.now = ms }

// ---------------------------------------------------------------------------------------------- crypto primitives

// ZeroHash is the prevHash of the genesis entry.
const ZeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Sha256 hashes the concatenation of parts.
func Sha256(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func Sha256Hex(data []byte) string { return hex.EncodeToString(Sha256(data)) }

// Netstrings: "5:hello,". Length-prefixing makes the encoding injective: ("ab","c") and ("a","bc") differ.
func Netstrings(fields ...string) []byte {
	var sb strings.Builder
	for _, f := range fields {
		sb.WriteString(strconv.Itoa(len(f))) // len counts UTF-8 bytes, as getBytes(UTF_8).length does
		sb.WriteByte(':')
		sb.WriteString(f)
		sb.WriteByte(',')
	}
	return []byte(sb.String())
}

// ErrBadNetstring is returned for anything malformed; the parser never guesses.
var ErrBadNetstring = errors.New("bad netstring")

// ParseNetstrings is the inverse of Netstrings.
func ParseNetstrings(data []byte) ([]string, error) {
	out := []string{}
	i := 0
	for i < len(data) {
		colon := i
		for colon < len(data) && data[colon] != ':' {
			colon++
		}
		if colon == len(data) || colon == i {
			return nil, fmt.Errorf("%w length at %d", ErrBadNetstring, i)
		}
		length, err := strconv.Atoi(string(data[i:colon]))
		if err != nil {
			return nil, fmt.Errorf("%w length at %d: %v", ErrBadNetstring, i, err)
		}
		end := colon + 1 + length
		if length < 0 || end >= len(data) || data[end] != ',' {
			return nil, fmt.Errorf("%w body at %d", ErrBadNetstring, i)
		}
		out = append(out, string(data[colon+1:end]))
		i = end + 1
	}
	return out, nil
}

// ---------------------------------------------------------------------------------------------- entries

// EntryVersion tags the canonical encoding so a future format change cannot collide with this one.
const EntryVersion = "pefs-log-v1"

// ErrBadEntry is returned when canonical bytes do not decode to an entry.
var ErrBadEntry = errors.New("not a pefs-log-v1 entry")

// Entry is one immutable log record. The hash covers every other field, including the previous entry's hash,
// so changing any byte of any earlier entry changes every hash after it. Payload keys are hashed in sorted order.
type Entry struct {
	Seq             int64
	TimestampMillis int64
	PrevHash        string
	Kind            string
	Subject         string
	Payload         map[string]string
	Hash            string
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyPayload(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func CreateEntry(seq, ts int64, prevHash, kind, subject string, payload map[string]string) Entry {
	p := copyPayload(payload)
	return Entry{seq, ts, prevHash, kind, subject, p, Sha256Hex(canonical(seq, ts, prevHash, kind, subject, p))}
}

// canonical is the exact bytes that are hashed. Field order is fixed; payload keys are sorted; every field is length-prefixed.
func canonical(seq, ts int64, prevHash, kind, subject string, payload map[string]string) []byte {
	fields := []string{EntryVersion, strconv.FormatInt(seq, 10), strconv.FormatInt(ts, 10), prevHash, kind, subject, strconv.Itoa(len(payload))}
	for _, k := range sortedKeys(payload) {
		fields = append(fields, k, payload[k])
	}
	return Netstrings(fields...)
}

func (e Entry) Canonical() []byte {
	return canonical(e.Seq, e.TimestampMillis, e.PrevHash, e.Kind, e.Subject, e.Payload)
}

func (e Entry) RecomputeHash() string { return Sha256Hex(e.Canonical()) }

// EntryFromCanonical parses canonical bytes back into an entry whose hash is recomputed, never trusted.
func EntryFromCanonical(bytes []byte) (Entry, error) {
	f, err := ParseNetstrings(bytes)
	if err != nil {
		return Entry{}, err
	}
	if len(f) < 7 || f[0] != EntryVersion {
		return Entry{}, ErrBadEntry
	}
	n, err := strconv.Atoi(f[6])
	if err != nil {
		return Entry{}, fmt.Errorf("%w: payload count %q", ErrBadEntry, f[6])
	}
	if len(f) != 7+2*n {
		return Entry{}, fmt.Errorf("%w: payload count mismatch", ErrBadEntry)
	}
	seq, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: seq %q", ErrBadEntry, f[1])
	}
	ts, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: timestamp %q", ErrBadEntry, f[2])
	}
	payload := map[string]string{}
	for i := 0; i < n; i++ {
		payload[f[7+2*i]] = f[8+2*i]
	}
	return CreateEntry(seq, ts, f[3], f[4], f[5], payload), nil
}

// WithPayloadKeepingHash is for tamper demonstrations only: same hash, different content.
func (e Entry) WithPayloadKeepingHash(key, value string) Entry {
	p := copyPayload(e.Payload)
	p[key] = value
	return Entry{e.Seq, e.TimestampMillis, e.PrevHash, e.Kind, e.Subject, p, e.Hash}
}

// Equal is the Java record equality: every field, payload by content.
func (e Entry) Equal(o Entry) bool {
	if e.Seq != o.Seq || e.TimestampMillis != o.TimestampMillis || e.PrevHash != o.PrevHash || e.Kind != o.Kind ||
		e.Subject != o.Subject || e.Hash != o.Hash || len(e.Payload) != len(o.Payload) {
		return false
	}
	for k, v := range e.Payload {
		if ov, ok := o.Payload[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

// PayloadString prints the payload as Java's TreeMap.toString: "{k=v, k2=v2}" in key order.
func (e Entry) PayloadString() string {
	parts := make([]string, 0, len(e.Payload))
	for _, k := range sortedKeys(e.Payload) {
		parts = append(parts, k+"="+e.Payload[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ---------------------------------------------------------------------------------------------- Merkle tree (RFC 6962)

func LeafHash(data []byte) []byte { return Sha256([]byte{0x00}, data) }

func NodeHash(left, right []byte) []byte { return Sha256([]byte{0x01}, left, right) }

// Split is the largest power of two strictly less than n (n >= 2).
func Split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

// MerkleRootRange is MTH(D[lo:hi]). Empty tree is SHA-256 of the empty string.
func MerkleRootRange(leaves [][]byte, lo, hi int) []byte {
	n := hi - lo
	if n == 0 {
		return Sha256()
	}
	if n == 1 {
		return LeafHash(leaves[lo])
	}
	k := Split(n)
	return NodeHash(MerkleRootRange(leaves, lo, lo+k), MerkleRootRange(leaves, lo+k, hi))
}

func MerkleRoot(leaves [][]byte) []byte { return MerkleRootRange(leaves, 0, len(leaves)) }

// ErrLeafOutOfRange is returned by AuditPath for a leaf index the tree does not have.
var ErrLeafOutOfRange = errors.New("leaf index out of range")

func auditPathRange(leaves [][]byte, m, lo, hi int) [][]byte {
	n := hi - lo
	if n <= 1 {
		return [][]byte{}
	}
	k := Split(n)
	if m < k {
		return append(auditPathRange(leaves, m, lo, lo+k), MerkleRootRange(leaves, lo+k, hi))
	}
	return append(auditPathRange(leaves, m-k, lo+k, hi), MerkleRootRange(leaves, lo, lo+k))
}

// AuditPath is PATH(m, D[0:n]): the sibling hashes from leaf m up to the root, bottom first.
func AuditPath(leaves [][]byte, m int) ([][]byte, error) {
	if m < 0 || m >= len(leaves) {
		return nil, fmt.Errorf("%w: leaf %d of %d", ErrLeafOutOfRange, m, len(leaves))
	}
	return auditPathRange(leaves, m, 0, len(leaves)), nil
}

// VerifyInclusion is RFC 9162 section 2.1.3.2: recompute the root from one leaf and its path. O(log n) hashes.
func VerifyInclusion(leafData []byte, index, treeSize int64, path [][]byte, expectedRoot []byte) bool {
	if index < 0 || index >= treeSize {
		return false
	}
	fn, sn := index, treeSize-1
	r := LeafHash(leafData)
	for _, p := range path {
		if sn == 0 {
			return false
		}
		if fn&1 == 1 || fn == sn {
			r = NodeHash(p, r)
			if fn&1 == 0 {
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			r = NodeHash(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	return sn == 0 && constantTimeEqual(r, expectedRoot)
}

// constantTimeEqual is MessageDigest.isEqual: a comparison whose running time does not leak the first differing byte.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ---------------------------------------------------------------------------------------------- time-stamp authority (Ed25519)

// TimestampToken is a signed statement "this digest existed no later than time T", in the spirit of an RFC 3161 time-stamp token.
type TimestampToken struct {
	DigestHex       string
	TimeMillis      int64
	SignatureBase64 string
}

func (t TimestampToken) SignedBytes() []byte {
	return Netstrings("pefs-tsa-v1", t.DigestHex, strconv.FormatInt(t.TimeMillis, 10))
}

// sha1prng is the JDK's SHA1PRNG SecureRandom, seeded with setSeed(long). The Java authority draws its 32-byte Ed25519
// seed from it, so reproducing the generator makes the Go authority hold the same key and emit the same signatures.
type sha1prng struct {
	state     []byte
	remainder []byte
	remCount  int
}

func newSHA1PRNG(seed int64) *sha1prng {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed)) // SecureRandom.longToByteArray is little-endian
	s := sha1.Sum(b[:])
	return &sha1prng{state: s[:]}
}

func (p *sha1prng) nextBytes(result []byte) {
	const digestSize = sha1.Size
	index := 0
	output := p.remainder
	if r := p.remCount; r > 0 {
		todo := min(len(result)-index, digestSize-r)
		copy(result[index:index+todo], output[r:r+todo])
		p.remCount += todo
		index += todo
	}
	for index < len(result) {
		sum := sha1.Sum(p.state)
		output = sum[:]
		p.updateState(output)
		todo := min(len(result)-index, digestSize)
		copy(result[index:index+todo], output[:todo])
		p.remCount += todo
		index += todo
	}
	p.remainder = output
	p.remCount %= digestSize
}

func (p *sha1prng) updateState(output []byte) {
	last := 1
	zf := false
	for i := range p.state {
		v := int(int8(p.state[i])) + int(int8(output[i])) + last
		t := byte(v)
		zf = zf || p.state[i] != t
		p.state[i] = t
		last = v >> 8
	}
	if !zf {
		p.state[0]++
	}
}

// spkiPrefix is the DER SubjectPublicKeyInfo header for an Ed25519 key, so PublicKeyBase64 matches Java's getEncoded().
var spkiPrefix = []byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}

// TimestampAuthority is an independent party with its own clock and an Ed25519 key. The log operator cannot forge its signatures.
type TimestampAuthority struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	clock   Clock
}

func NewTimestampAuthority(seed int64, clock Clock) *TimestampAuthority {
	keySeed := make([]byte, ed25519.SeedSize)
	newSHA1PRNG(seed).nextBytes(keySeed)
	private := ed25519.NewKeyFromSeed(keySeed)
	return &TimestampAuthority{private, private.Public().(ed25519.PublicKey), clock}
}

func (a *TimestampAuthority) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), spkiPrefix...), a.public...))
}

func (a *TimestampAuthority) Stamp(digestHex string) TimestampToken {
	t := a.clock.Millis()
	unsigned := TimestampToken{digestHex, t, ""}
	return TimestampToken{digestHex, t, base64.StdEncoding.EncodeToString(ed25519.Sign(a.private, unsigned.SignedBytes()))}
}

// VerifyToken checks the token against a Base64 SubjectPublicKeyInfo key. Any decoding failure is a verification failure.
func VerifyToken(publicKeyBase64 string, token TimestampToken) bool {
	spki, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil || len(spki) != len(spkiPrefix)+ed25519.PublicKeySize || !constantTimeEqual(spki[:len(spkiPrefix)], spkiPrefix) {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(token.SignatureBase64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(spki[len(spkiPrefix):]), token.SignedBytes(), sig)
}

// Checkpoint is a sealed prefix of the log: its size, Merkle root, head hash, and a time-stamp over all three.
type Checkpoint struct {
	Size          int64
	MerkleRootHex string
	HeadHashHex   string
	Token         TimestampToken
}

func StatementDigest(size int64, rootHex, headHex string) string {
	return Sha256Hex(Netstrings("pefs-checkpoint-v1", strconv.FormatInt(size, 10), rootHex, headHex))
}

// ---------------------------------------------------------------------------------------------- the log

var (
	ErrClockWentBackwards = errors.New("clock went backwards")
	ErrNothingToSeal      = errors.New("nothing to seal")
)

// DecisionLog is the append-only decision log. There is no update and no delete; the only mutations are append and seal.
type DecisionLog struct {
	entries     []Entry
	checkpoints []Checkpoint
	clock       Clock
	tsa         *TimestampAuthority
}

func NewDecisionLog(clock Clock, tsa *TimestampAuthority) *DecisionLog {
	return &DecisionLog{clock: clock, tsa: tsa}
}

func (l *DecisionLog) Append(kind, subject string, payload map[string]string) (Entry, error) {
	ts := l.clock.Millis()
	prev := ZeroHash
	if n := len(l.entries); n > 0 {
		last := l.entries[n-1]
		if ts < last.TimestampMillis {
			return Entry{}, fmt.Errorf("%w: %d < %d", ErrClockWentBackwards, ts, last.TimestampMillis)
		}
		prev = last.Hash
	}
	e := CreateEntry(int64(len(l.entries)), ts, prev, kind, subject, payload)
	l.entries = append(l.entries, e)
	return e, nil
}

// Seal everything appended so far: compute the Merkle root and have the independent authority time-stamp it.
func (l *DecisionLog) Seal() (Checkpoint, error) {
	if len(l.entries) == 0 {
		return Checkpoint{}, ErrNothingToSeal
	}
	size := int64(len(l.entries))
	root := hex.EncodeToString(MerkleRoot(LeafData(l.entries, size)))
	head := l.entries[len(l.entries)-1].Hash
	cp := Checkpoint{size, root, head, l.tsa.Stamp(StatementDigest(size, root, head))}
	l.checkpoints = append(l.checkpoints, cp)
	return cp, nil
}

// LeafData is the first `size` entry hashes as raw bytes: the Merkle leaves.
func LeafData(entries []Entry, size int64) [][]byte {
	leaves := make([][]byte, 0, size)
	for i := int64(0); i < size; i++ {
		raw, err := hex.DecodeString(entries[i].Hash)
		if err != nil {
			panic(err) // an entry's hash is always the hex this package produced
		}
		leaves = append(leaves, raw)
	}
	return leaves
}

// Entries returns a copy: appending to it does not touch the log.
func (l *DecisionLog) Entries() []Entry { return append([]Entry(nil), l.entries...) }

func (l *DecisionLog) Checkpoints() []Checkpoint { return append([]Checkpoint(nil), l.checkpoints...) }

func (l *DecisionLog) TsaPublicKey() string { return l.tsa.PublicKeyBase64() }

// Report is what a verifier found. OK is true only if Problems is empty. FirstBadSeq is -1 when no entry is implicated.
type Report struct {
	OK          bool
	FirstBadSeq int64
	Problems    []string
}

func ReportOf(problems []string, firstBad int64) Report {
	if len(problems) == 0 {
		return Report{true, -1, []string{}}
	}
	return Report{false, firstBad, append([]string(nil), problems...)}
}

// ---------------------------------------------------------------------------------------------- evidence bundles

// BundleMagic is the first line of every bundle.
const BundleMagic = "pefs-evidence-bundle v1"

// ExportBundle writes everything about one subject, provable against a sealed checkpoint without disclosing any other entry.
func ExportBundle(log *DecisionLog, subject string, cp Checkpoint) (string, error) {
	all := log.Entries()
	leaves := LeafData(all, cp.Size)
	var sb strings.Builder
	sb.WriteString(BundleMagic + "\n")
	fmt.Fprintf(&sb, "subject %s\n", subject)
	fmt.Fprintf(&sb, "tree-size %d\n", cp.Size)
	fmt.Fprintf(&sb, "merkle-root %s\n", cp.MerkleRootHex)
	fmt.Fprintf(&sb, "head-hash %s\n", cp.HeadHashHex)
	fmt.Fprintf(&sb, "tsa-time %d\n", cp.Token.TimeMillis)
	fmt.Fprintf(&sb, "tsa-signature %s\n", cp.Token.SignatureBase64)
	fmt.Fprintf(&sb, "tsa-public-key %s\n", log.TsaPublicKey())
	for i := int64(0); i < cp.Size; i++ {
		e := all[i]
		if e.Subject != subject {
			continue
		}
		fmt.Fprintf(&sb, "entry %d %s %s\n", e.Seq, base64.StdEncoding.EncodeToString(e.Canonical()), e.Hash)
		path, err := AuditPath(leaves, int(i))
		if err != nil {
			return "", err
		}
		hexPath := make([]string, 0, len(path))
		for _, p := range path {
			hexPath = append(hexPath, hex.EncodeToString(p))
		}
		proof := "-"
		if len(hexPath) > 0 {
			proof = strings.Join(hexPath, ",")
		}
		fmt.Fprintf(&sb, "proof %d %s\n", e.Seq, proof)
	}
	return sb.String(), nil
}

// ---------------------------------------------------------------------------------------------- demo

// T0 is 2026-01-01T00:00:00Z, fixed so output is reproducible.
const T0 = int64(1_767_225_600_000)

func decision(outcome string, score int, amount Money) map[string]string {
	return map[string]string{
		"outcome":    outcome,
		"score":      strconv.Itoa(score),
		"amount":     amount.String(),
		"model":      "scorecard-2026-01-a",
		"thresholds": "review>=60,decline>=130",
	}
}

// DemoTsa: the authority runs its own clock; here it reads 250 ms after the log clock, as if the request crossed a network.
func DemoTsa(logClock *ManualClock) *TimestampAuthority {
	return NewTimestampAuthority(2026, ClockFunc(func() int64 { return logClock.Millis() + 250 }))
}

func mustAppend(log *DecisionLog, kind, subject string, payload map[string]string) {
	if _, err := log.Append(kind, subject, payload); err != nil {
		panic(err)
	}
}

// BuildDemoLog: a threshold change, six decisions, two review outcomes, sealed twice.
func BuildDemoLog(clock *ManualClock, tsa *TimestampAuthority) *DecisionLog {
	log := NewDecisionLog(clock, tsa)
	mustAppend(log, "config.threshold", "risk-engine", map[string]string{"review": "60", "decline": "130", "approved_by": "risk-lead-7", "ticket": "RISK-412"})
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0001", decision("APPROVE", 13, OfMinor(4_250, USD)))
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0002", decision("REVIEW", 88, OfMinor(31_900, USD)))
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0003", decision("DECLINE", 861, OfMinor(129_999, USD)))
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0004", decision("APPROVE", 41, OfMinor(1_999, EUR)))
	clock.Advance(60_000)
	if _, err := log.Seal(); err != nil { // checkpoint 1 over entries 0..4
		panic(err)
	}
	clock.Advance(240_000)
	mustAppend(log, "review.outcome", "pay_0002", map[string]string{"result": "APPROVE", "analyst": "analyst-31", "minutes": "4"})
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0005", decision("REVIEW", 97, OfMinor(55_000, USD)))
	clock.Advance(1_000)
	mustAppend(log, "risk.decision", "pay_0006", decision("APPROVE", 7, OfMinor(899, USD)))
	clock.Advance(120_000)
	mustAppend(log, "review.outcome", "pay_0005", map[string]string{"result": "DECLINE", "analyst": "analyst-12", "minutes": "6"})
	clock.Advance(60_000)
	if _, err := log.Seal(); err != nil { // checkpoint 2 over entries 0..8
		panic(err)
	}
	return log
}

func printReport(label string, r Report) {
	first := ""
	if len(r.Problems) > 0 {
		first = r.Problems[0]
	}
	fmt.Printf("%-38s ok=%-5v first bad=#%d  %s\n", label+":", r.OK, r.FirstBadSeq, first)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		os.Exit(verifierMain(os.Args[2:]))
	}
	logClock := NewManualClock(T0)
	log := BuildDemoLog(logClock, DemoTsa(logClock))

	fmt.Println("== The hash-chained log ==")
	for _, e := range log.Entries() {
		fmt.Printf("#%d t=+%-6d %-17s %-11s prev=%s.. hash=%s..\n", e.Seq, (e.TimestampMillis-T0)/1000, e.Kind, e.Subject, e.PrevHash[:8], e.Hash[:8])
	}
	fmt.Println()
	for _, cp := range log.Checkpoints() {
		fmt.Printf("checkpoint size=%d root=%s.. head=%s.. stamped t=+%ds\n", cp.Size, cp.MerkleRootHex[:12], cp.HeadHashHex[:12], (cp.Token.TimeMillis-T0)/1000)
	}

	key := log.TsaPublicKey()
	fmt.Println("\n== Full-log verification ==")
	clean := VerifyLog(log.Entries(), log.Checkpoints(), key)
	fmt.Printf("untouched log: ok=%v\n", clean.OK)

	edited := log.Entries()
	edited[3] = edited[3].WithPayloadKeepingHash("outcome", "APPROVE")
	printReport("edit #3 outcome DECLINE->APPROVE", VerifyLog(edited, log.Checkpoints(), key))

	rehashed := log.Entries()
	e3 := rehashed[3]
	p3 := copyPayload(e3.Payload)
	p3["outcome"] = "APPROVE"
	rehashed[3] = CreateEntry(3, e3.TimestampMillis, e3.PrevHash, e3.Kind, e3.Subject, p3)
	printReport("edit #3 and recompute its hash", VerifyLog(rehashed, log.Checkpoints(), key))

	rewritten := append([]Entry(nil), rehashed[:4]...)
	for i := 4; i < len(log.Entries()); i++ {
		o := log.Entries()[i]
		rewritten = append(rewritten, CreateEntry(o.Seq, o.TimestampMillis, rewritten[i-1].Hash, o.Kind, o.Subject, o.Payload))
	}
	printReport("edit #3 and rewrite the whole chain", VerifyLog(rewritten, log.Checkpoints(), key))

	deleted := log.Entries()
	deleted = append(deleted[:5], deleted[6:]...)
	printReport("delete #5 (the review of pay_0002)", VerifyLog(deleted, log.Checkpoints(), key))

	fmt.Println("\n== Clock anchoring ==")
	logClock.Set(T0 + 5_000)
	if _, err := log.Append("risk.decision", "pay_0099", decision("APPROVE", 1, OfMinor(100, USD))); err != nil {
		fmt.Printf("backdated append rejected: %v\n", err)
	}
	cp1 := log.Checkpoints()[0]
	fmt.Printf("entries 0..%d existed no later than t=+%ds (signed by the TSA, not by us)\n", cp1.Size-1, (cp1.Token.TimeMillis-T0)/1000)

	fmt.Println("\n== Evidence bundle for pay_0002 ==")
	cp2 := log.Checkpoints()[1]
	bundle, err := ExportBundle(log, "pay_0002", cp2)
	if err != nil {
		panic(err)
	}
	for _, line := range javaSplit(bundle, "\n") {
		if len(line) > 110 {
			line = line[:107] + "..."
		}
		fmt.Println(line)
	}
	b := VerifyBundle(bundle, key)
	fmt.Printf("\nregulator verifies bundle with the TSA's published key: ok=%v\n", b.OK)
	lines, err := Describe(bundle)
	if err != nil {
		panic(err)
	}
	for _, line := range lines {
		fmt.Println("  " + line)
	}

	e5 := log.Entries()[5]
	forged := strings.ReplaceAll(bundle, b64(e5.Canonical()),
		b64(CreateEntry(5, e5.TimestampMillis, e5.PrevHash, "review.outcome", "pay_0002",
			map[string]string{"result": "DECLINE", "analyst": "analyst-31", "minutes": "4"}).Canonical()))
	printReport("bundle with the review result changed", VerifyBundle(forged, key))
}
