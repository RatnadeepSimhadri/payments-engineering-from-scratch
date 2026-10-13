// Lesson: phases/13-risk-fraud-and-compliance/04-sanctions-screening/docs/en.md
// Sanctions Screening: Name Matching Under Uncertainty
// Run:  go run .
// Test: go test ./...
// Sources: Winkler, "String Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter Model of
//          Record Linkage" (1990); U.S. National Archives, "The Soundex Indexing System"; OFAC Sanctions List Search FAQ.
// Go 1.22+, standard library only. Same design as the Java and Python versions. Every watchlist name is fictional.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------------------------
// Step 1: normalization. Case, diacritics, punctuation, honorifics, whitespace.
// The diacritic table is explicit (not golang.org/x/text) so the three languages fold identically.
// ---------------------------------------------------------------------------------------------

var diacritics = buildDiacritics()

func buildDiacritics() map[rune]string {
	table := map[string]string{
		"a": "àáâãäåāăą", "c": "çćĉċč", "d": "ďđ", "e": "èéêëēĕėęě", "g": "ĝğġģ", "h": "ĥħ",
		"i": "ìíîïĩīĭįı", "j": "ĵ", "k": "ķ", "l": "ĺļľŀł", "n": "ñńņňŉ", "o": "òóôõöøōŏő",
		"r": "ŕŗř", "s": "śŝşš", "t": "ţťŧ", "u": "ùúûüũūŭůűų", "w": "ŵ", "y": "ýÿŷ", "z": "źżž",
	}
	out := map[rune]string{}
	for base, variants := range table {
		for _, v := range variants {
			out[v] = base
			out[unicode.ToUpper(v)] = base
		}
	}
	out['ß'] = "ss"
	out['æ'] = "ae"
	out['Æ'] = "ae"
	out['œ'] = "oe"
	out['Œ'] = "oe"
	return out
}

// Stopwords are titles: they carry no identity and are removed before matching.
var stopwords = map[string]bool{"mr": true, "mrs": true, "ms": true, "dr": true, "jr": true, "sr": true, "prof": true, "sir": true}

// Particles are matched when present on both sides; an unmatched particle is not evidence against a match.
var particles = map[string]bool{"al": true, "el": true, "bin": true, "ibn": true, "abu": true, "de": true, "del": true,
	"da": true, "di": true, "la": true, "le": true, "van": true, "von": true, "der": true, "den": true}

// Normalize returns lowercase ASCII letters and digits separated by single spaces, honorifics removed.
func Normalize(name string) string {
	var out strings.Builder
	for _, ch := range name {
		if folded, ok := diacritics[ch]; ok {
			out.WriteString(folded)
		} else if ch < 128 && (unicode.IsLetter(ch) || unicode.IsDigit(ch)) {
			out.WriteRune(unicode.ToLower(ch))
		} else {
			out.WriteByte(' ') // punctuation, hyphens, apostrophes, anything else
		}
	}
	var tokens []string
	for _, t := range strings.Fields(out.String()) {
		if !stopwords[t] {
			tokens = append(tokens, t)
		}
	}
	return strings.Join(tokens, " ")
}

// Tokenize splits a normalized name on single spaces.
func Tokenize(normalized string) []string {
	return strings.Fields(normalized)
}

// ---------------------------------------------------------------------------------------------
// Step 2: Jaro-Winkler, from scratch. Returns a similarity in [0, 1]; 1 means identical.
// Operates on bytes: inputs are already normalized to ASCII.
// ---------------------------------------------------------------------------------------------

// Jaro counts matches within a sliding window and penalises half the transpositions.
func Jaro(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}
	window := max(len(a), len(b))/2 - 1
	if window < 0 {
		window = 0
	}
	aMatched := make([]bool, len(a))
	bMatched := make([]bool, len(b))
	matches := 0
	for i := 0; i < len(a); i++ {
		lo := max(0, i-window)
		hi := min(len(b), i+window+1)
		for j := lo; j < hi; j++ {
			if !bMatched[j] && b[j] == a[i] {
				aMatched[i] = true
				bMatched[j] = true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0.0
	}
	transpositions := 0
	j := 0
	for i := 0; i < len(a); i++ {
		if !aMatched[i] {
			continue
		}
		for !bMatched[j] {
			j++
		}
		if a[i] != b[j] {
			transpositions++
		}
		j++
	}
	half := transpositions / 2
	m := float64(matches)
	return (m/float64(len(a)) + m/float64(len(b)) + (m-float64(half))/m) / 3.0
}

const (
	jwPrefixScale = 0.1
	jwMaxPrefix   = 4
)

// JaroWinkler adds Winkler's prefix boost: up to four shared leading characters pull the score toward 1.
func JaroWinkler(a, b string) float64 {
	base := Jaro(a, b)
	prefix := 0
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] || prefix == jwMaxPrefix {
			break
		}
		prefix++
	}
	return base + float64(prefix)*jwPrefixScale*(1.0-base)
}

// ---------------------------------------------------------------------------------------------
// Step 3: Soundex. A four-character phonetic key: first letter + three digits.
// ---------------------------------------------------------------------------------------------

func soundexCode(c byte) byte {
	switch c {
	case 'b', 'f', 'p', 'v':
		return '1'
	case 'c', 'g', 'j', 'k', 'q', 's', 'x', 'z':
		return '2'
	case 'd', 't':
		return '3'
	case 'l':
		return '4'
	case 'm', 'n':
		return '5'
	case 'r':
		return '6'
	}
	return 0
}

// Soundex implements American Soundex: h and w are transparent, vowels separate repeated codes.
func Soundex(token string) string {
	var letters []byte
	for _, c := range []byte(strings.ToLower(token)) {
		if c >= 'a' && c <= 'z' {
			letters = append(letters, c)
		}
	}
	if len(letters) == 0 {
		return ""
	}
	key := []byte{byte(unicode.ToUpper(rune(letters[0])))}
	last := soundexCode(letters[0])
	for i := 1; i < len(letters) && len(key) < 4; i++ {
		c := letters[i]
		if c == 'h' || c == 'w' {
			continue
		}
		code := soundexCode(c)
		if code == 0 {
			last = 0 // a vowel separates: the same code may be emitted again
			continue
		}
		if code != last {
			key = append(key, code)
			last = code
		}
	}
	for len(key) < 4 {
		key = append(key, '0')
	}
	return string(key)
}

// ---------------------------------------------------------------------------------------------
// Step 4: token alignment with explainable reasons.
// ---------------------------------------------------------------------------------------------

// ReasonKind names the kind of evidence one aligned token pair contributed.
type ReasonKind string

const (
	Exact     ReasonKind = "exact"
	Fuzzy     ReasonKind = "fuzzy"
	Phonetic  ReasonKind = "phonetic"
	Initial   ReasonKind = "initial"
	Particle  ReasonKind = "particle"
	Unmatched ReasonKind = "unmatched"
)

// MatchReason is one aligned token pair and its contribution.
type MatchReason struct {
	Kind       ReasonKind
	QueryToken string
	ListToken  string
	Score      float64
}

func (r MatchReason) String() string {
	either := r.QueryToken
	if either == "" {
		either = r.ListToken
	}
	switch r.Kind {
	case Unmatched:
		return fmt.Sprintf("unmatched '%s' (+0.00)", either)
	case Particle:
		return fmt.Sprintf("particle '%s' ignored", either)
	case Phonetic:
		return fmt.Sprintf("phonetic '%s'~'%s' %s (+%.2f)", r.QueryToken, r.ListToken, Soundex(r.QueryToken), r.Score)
	}
	return fmt.Sprintf("%s '%s'~'%s' (+%.2f)", r.Kind, r.QueryToken, r.ListToken, r.Score)
}

// MatchScore is the aggregate score plus every reason that produced it.
type MatchScore struct {
	Score     float64
	Reasons   []MatchReason
	Reordered bool
}

func (m MatchScore) Explain() string {
	parts := make([]string, 0, len(m.Reasons)+1)
	for _, r := range m.Reasons {
		parts = append(parts, r.String())
	}
	if m.Reordered {
		parts = append(parts, "token order ignored")
	}
	return fmt.Sprintf("score=%.3f [%s]", m.Score, strings.Join(parts, "; "))
}

const (
	PhoneticFloor = 0.85
	InitialScore  = 0.85
	FuzzyFloor    = 0.70 // a token pair below this contributes nothing: noise, not evidence
)

// PairScore classifies one token pair and returns the evidence it is worth.
func PairScore(q, c string) MatchReason {
	if q == c {
		return MatchReason{Exact, q, c, 1.0}
	}
	if (len(q) == 1 && strings.HasPrefix(c, q)) || (len(c) == 1 && strings.HasPrefix(q, c)) {
		return MatchReason{Initial, q, c, InitialScore}
	}
	jw := JaroWinkler(q, c)
	if Soundex(q) == Soundex(c) && jw < PhoneticFloor {
		return MatchReason{Phonetic, q, c, PhoneticFloor}
	}
	if jw >= FuzzyFloor {
		return MatchReason{Fuzzy, q, c, jw}
	}
	return MatchReason{Unmatched, q, c, 0.0}
}

// NameMatcher performs a greedy best-pair alignment of query tokens to list tokens. Order does not
// matter; every token on the longer side must find a partner or the score is diluted.
type NameMatcher struct{}

type pair struct {
	score float64
	i, j  int
	kind  ReasonKind
}

func (NameMatcher) Compare(query, candidate string) MatchScore {
	q := Tokenize(Normalize(query))
	c := Tokenize(Normalize(candidate))
	if len(q) == 0 || len(c) == 0 {
		return MatchScore{0.0, nil, false}
	}
	var pairs []pair
	for i, qt := range q {
		for j, ct := range c {
			r := PairScore(qt, ct)
			if r.Kind != Unmatched {
				pairs = append(pairs, pair{r.Score, i, j, r.Kind})
			}
		}
	}
	// best first; ties broken by position so the alignment is deterministic
	sort.SliceStable(pairs, func(x, y int) bool {
		if pairs[x].score != pairs[y].score {
			return pairs[x].score > pairs[y].score
		}
		if pairs[x].i != pairs[y].i {
			return pairs[x].i < pairs[y].i
		}
		return pairs[x].j < pairs[y].j
	})
	usedQ := make([]bool, len(q))
	usedC := make([]bool, len(c))
	var reasons []MatchReason
	total := 0.0
	reordered := false
	for _, p := range pairs {
		if usedQ[p.i] || usedC[p.j] {
			continue
		}
		usedQ[p.i], usedC[p.j] = true, true
		total += p.score
		if p.i != p.j {
			reordered = true
		}
		reasons = append(reasons, MatchReason{p.kind, q[p.i], c[p.j], p.score})
	}
	qCount, cCount := len(q), len(c)
	for i, qt := range q {
		if usedQ[i] {
			continue
		}
		if particles[qt] {
			qCount--
			reasons = append(reasons, MatchReason{Particle, qt, "", 0.0})
		} else {
			reasons = append(reasons, MatchReason{Unmatched, qt, "", 0.0})
		}
	}
	for j, ct := range c {
		if usedC[j] {
			continue
		}
		if particles[ct] {
			cCount--
			reasons = append(reasons, MatchReason{Particle, "", ct, 0.0})
		} else {
			reasons = append(reasons, MatchReason{Unmatched, "", ct, 0.0})
		}
	}
	denominator := max(qCount, cCount, 1)
	return MatchScore{total / float64(denominator), reasons, reordered}
}

// ---------------------------------------------------------------------------------------------
// Step 5: the watchlist and the screening result.
// ---------------------------------------------------------------------------------------------

type WatchlistEntry struct {
	EntryID string
	Name    string
	Program string
	Aliases []string
}

func (e WatchlistEntry) AllNames() []string {
	return append([]string{e.Name}, e.Aliases...)
}

type Hit struct {
	EntryID     string
	MatchedName string
	Program     string
	Match       MatchScore
}

func (h Hit) Explain() string {
	return fmt.Sprintf("%s '%s' [%s] %s", h.EntryID, h.MatchedName, h.Program, h.Match.Explain())
}

type ScreeningResult struct {
	Query      string
	Normalized string
	Threshold  float64
	Hits       []Hit // best first
}

func (r ScreeningResult) IsHit() bool { return len(r.Hits) > 0 }

func (r ScreeningResult) Explain() string {
	verdict := "CLEAR"
	if r.IsHit() {
		verdict = "HIT"
	}
	lines := []string{fmt.Sprintf("screen '%s' -> '%s' @ %.2f: %s", r.Query, r.Normalized, r.Threshold, verdict)}
	for _, h := range r.Hits {
		lines = append(lines, "  "+h.Explain())
	}
	return strings.Join(lines, "\n")
}

var (
	ErrDuplicateEntry = errors.New("duplicate watchlist entry id")
	ErrBadThreshold   = errors.New("threshold must be in (0, 1]")
)

type Watchlist struct {
	Entries []WatchlistEntry
	Matcher NameMatcher
}

func NewWatchlist(entries []WatchlistEntry) (*Watchlist, error) {
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.EntryID] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateEntry, e.EntryID)
		}
		seen[e.EntryID] = true
	}
	return &Watchlist{Entries: append([]WatchlistEntry(nil), entries...)}, nil
}

func (w *Watchlist) Screen(name string, threshold float64) (ScreeningResult, error) {
	if !(threshold > 0.0 && threshold <= 1.0) {
		return ScreeningResult{}, ErrBadThreshold
	}
	var hits []Hit
	for _, entry := range w.Entries {
		var best *Hit
		for _, candidate := range entry.AllNames() { // best alias per entry: one hit per listed person
			m := w.Matcher.Compare(name, candidate)
			if best == nil || m.Score > best.Match.Score {
				best = &Hit{entry.EntryID, candidate, entry.Program, m}
			}
		}
		if best != nil && best.Match.Score >= threshold {
			hits = append(hits, *best)
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Match.Score != hits[b].Match.Score {
			return hits[a].Match.Score > hits[b].Match.Score
		}
		return hits[a].EntryID < hits[b].EntryID
	})
	return ScreeningResult{name, Normalize(name), threshold, hits}, nil
}

// ---------------------------------------------------------------------------------------------
// Step 6: threshold tuning against a labelled synthetic set.
// ---------------------------------------------------------------------------------------------

type LabelledPair struct {
	Query    string
	ListName string
	IsMatch  bool
}

type Evaluation struct {
	Threshold      float64
	TP, FP, FN, TN int
}

func (e Evaluation) Precision() float64 {
	if e.TP+e.FP == 0 {
		return 1.0
	}
	return float64(e.TP) / float64(e.TP+e.FP)
}

func (e Evaluation) Recall() float64 {
	if e.TP+e.FN == 0 {
		return 1.0
	}
	return float64(e.TP) / float64(e.TP+e.FN)
}

func (e Evaluation) F1() float64 {
	p, r := e.Precision(), e.Recall()
	if p+r == 0 {
		return 0.0
	}
	return 2 * p * r / (p + r)
}

func Evaluate(matcher NameMatcher, pairs []LabelledPair, threshold float64) Evaluation {
	ev := Evaluation{Threshold: threshold}
	for _, p := range pairs {
		predicted := matcher.Compare(p.Query, p.ListName).Score >= threshold
		switch {
		case predicted && p.IsMatch:
			ev.TP++
		case predicted:
			ev.FP++
		case p.IsMatch:
			ev.FN++
		default:
			ev.TN++
		}
	}
	return ev
}

func ThresholdTable(matcher NameMatcher, pairs []LabelledPair, thresholds []float64) []Evaluation {
	out := make([]Evaluation, 0, len(thresholds))
	for _, t := range thresholds {
		out = append(out, Evaluate(matcher, pairs, t))
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Demo data. Every name here is invented for the lesson.
// ---------------------------------------------------------------------------------------------

func DemoWatchlist() *Watchlist {
	wl, err := NewWatchlist([]WatchlistEntry{
		{"FIC-0001", "Thaddeus Grimblewood", "FICTIONAL-SDN", []string{"Tadeusz Grimblewood", "T. Grimblewood"}},
		{"FIC-0002", "Yevgenia Kraskovitch", "FICTIONAL-SDN", []string{"Evgenia Kraskovich"}},
		{"FIC-0003", "Hamid al-Qasrani", "FICTIONAL-SDN", []string{"Hamid Al Qasrani", "Hameed Qasrani"}},
		{"FIC-0004", "Ximena Fortaleza-Rúa", "FICTIONAL-SSI", []string{"Ximena Fortaleza Rua"}},
		{"FIC-0005", "Octavian Blunderbuss", "FICTIONAL-SSI", nil},
		{"FIC-0006", "Perpetua Nightshade", "FICTIONAL-SDN", []string{"Perpetua Nightshade-Vale"}},
		{"FIC-0007", "Bartholomew Quillfeather", "FICTIONAL-SSI", []string{"Bart Quillfeather"}},
	})
	if err != nil {
		panic(err)
	}
	return wl
}

// LabelledPairs is synthetic ground truth: true variants of listed names and hard negatives that share tokens.
func LabelledPairs() []LabelledPair {
	return []LabelledPair{
		{"Thaddeus Grimblewood", "Thaddeus Grimblewood", true},
		{"GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood", true},
		{"Thadeus Grimblewod", "Thaddeus Grimblewood", true},
		{"T Grimblewood", "Thaddeus Grimblewood", true},
		{"Evgenia Kraskovich", "Yevgenia Kraskovitch", true},
		{"Yevgeniya Kraskovitch", "Yevgenia Kraskovitch", true},
		{"Hamid Al-Qasrani", "Hamid al-Qasrani", true},
		{"Hameed Qasrani", "Hamid al-Qasrani", true},
		{"Ximena Fortaleza Rua", "Ximena Fortaleza-Rúa", true},
		{"Octavian Blunderbus", "Octavian Blunderbuss", true},
		{"Perpetua Nitshade", "Perpetua Nightshade", true},
		{"Bart Quillfeather", "Bartholomew Quillfeather", true},
		{"Theodora Grimblewood", "Thaddeus Grimblewood", false},
		{"Thaddeus Grimble", "Thaddeus Grimblewood", false},
		{"Yevgenia Krasnova", "Yevgenia Kraskovitch", false},
		{"Hamid Qasim", "Hamid al-Qasrani", false},
		{"Ximena Fortes", "Ximena Fortaleza-Rúa", false},
		{"Octavia Blundell", "Octavian Blunderbuss", false},
		{"Perpetua Nightingale", "Perpetua Nightshade", false},
		{"Bartholomew Quince", "Bartholomew Quillfeather", false},
		{"Marisol Everly", "Thaddeus Grimblewood", false},
		{"Kwame Oduya-Brandt", "Hamid al-Qasrani", false},
		{"Ingrid Halvorsen", "Yevgenia Kraskovitch", false},
		{"Rafael Ocampo", "Octavian Blunderbuss", false},
	}
}

func main() {
	fmt.Println("== Normalization ==")
	for _, raw := range []string{"GRIMBLEWOOD, Thaddeus", "Dr. Ximena Fortaleza-Rúa", "Hamid al-Qasrani", "Müller-Lüdenscheidt, Jörg"} {
		fmt.Printf("  %-34s -> '%s'\n", "'"+raw+"'", Normalize(raw))
	}

	fmt.Println("\n== Jaro-Winkler on classic pairs ==")
	for _, p := range [][2]string{{"martha", "marhta"}, {"dwayne", "duane"}, {"dixon", "dicksonx"}, {"grimblewood", "grimblewod"}, {"abc", "xyz"}} {
		fmt.Printf("  %-12s %-12s jaro=%.4f  jaro_winkler=%.4f\n", p[0], p[1], Jaro(p[0], p[1]), JaroWinkler(p[0], p[1]))
	}

	fmt.Println("\n== Soundex ==")
	for _, t := range []string{"robert", "rupert", "tymczak", "pfister", "ashcraft", "nightshade", "nitshade", "kraskovitch", "kraskovich"} {
		fmt.Printf("  %-12s %s\n", t, Soundex(t))
	}

	wl := DemoWatchlist()
	fmt.Println("\n== Screening at threshold 0.85 ==")
	for _, q := range []string{"Thaddeus Grimblewood", "GRIMBLEWOOD, THADDEUS", "Hameed Al Qasrani", "Perpetua Nitshade",
		"Theodora Grimblewood", "Marisol Everly"} {
		r, _ := wl.Screen(q, 0.85)
		fmt.Println(r.Explain())
	}

	fmt.Println("\n== Threshold tuning on the labelled synthetic set ==")
	fmt.Printf("  %5s %3s %3s %3s %3s %9s %7s %6s\n", "thr", "tp", "fp", "fn", "tn", "precision", "recall", "f1")
	for _, ev := range ThresholdTable(wl.Matcher, LabelledPairs(), []float64{0.70, 0.75, 0.80, 0.85, 0.90, 0.95, 1.00}) {
		fmt.Printf("  %5.2f %3d %3d %3d %3d %9.3f %7.3f %6.3f\n", ev.Threshold, ev.TP, ev.FP, ev.FN, ev.TN, ev.Precision(), ev.Recall(), ev.F1())
	}
}
