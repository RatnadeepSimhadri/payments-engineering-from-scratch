package main

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func close(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"GRIMBLEWOOD, Thaddeus":   "grimblewood thaddeus",
		"Ximena Fortaleza-Rúa":    "ximena fortaleza rua",
		"Dr. Perpetua Nightshade": "perpetua nightshade",
		"Straße":                  "strasse",
		"  Hamid   al-Qasrani ":   "hamid al qasrani",
		"...":                     "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if len(Tokenize("")) != 0 {
		t.Error("tokenize of empty string must be empty")
	}
}

func TestJaroWinklerPublishedExamples(t *testing.T) {
	if !close(Jaro("martha", "marhta"), 0.9444444444444445) {
		t.Errorf("jaro martha/marhta = %v", Jaro("martha", "marhta"))
	}
	if !close(JaroWinkler("martha", "marhta"), 0.9611111111111111) {
		t.Errorf("jw martha/marhta = %v", JaroWinkler("martha", "marhta"))
	}
	if !close(JaroWinkler("dwayne", "duane"), 0.84) {
		t.Errorf("jw dwayne/duane = %v", JaroWinkler("dwayne", "duane"))
	}
	if !close(JaroWinkler("dixon", "dicksonx"), 0.8133333333333332) {
		t.Errorf("jw dixon/dicksonx = %v", JaroWinkler("dixon", "dicksonx"))
	}
}

func TestJaroBoundaries(t *testing.T) {
	if Jaro("abc", "abc") != 1.0 || Jaro("abc", "xyz") != 0.0 || Jaro("", "abc") != 0.0 {
		t.Error("identical must be 1, disjoint and empty must be 0")
	}
	if JaroWinkler("kraskovitch", "kraskovich") != JaroWinkler("kraskovich", "kraskovitch") {
		t.Error("jaro-winkler must be symmetric")
	}
	jw := JaroWinkler("grimblewood", "grimblewod")
	if jw > 1.0 || jw <= 0.98 {
		t.Errorf("jw grimblewood/grimblewod = %v, want in (0.98, 1]", jw)
	}
	if JaroWinkler("abcdefgh", "abcdexyz") > JaroWinkler("abcd", "abcd") {
		t.Error("prefix bonus must be capped at four characters")
	}
}

func TestSoundex(t *testing.T) {
	cases := map[string]string{"Robert": "R163", "Rupert": "R163", "Tymczak": "T522", "Pfister": "P236", "Ashcraft": "A261", "Lee": "L000", "": ""}
	for in, want := range cases {
		if got := Soundex(in); got != want {
			t.Errorf("Soundex(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPairScoreKinds(t *testing.T) {
	if PairScore("hamid", "hamid").Kind != Exact {
		t.Error("identical tokens are exact")
	}
	if PairScore("t", "thaddeus").Kind != Initial {
		t.Error("single letter prefix is an initial")
	}
	if PairScore("nitshade", "nightshade").Kind != Fuzzy {
		t.Error("close spelling is fuzzy")
	}
	if PairScore("marisol", "thaddeus").Kind != Unmatched {
		t.Error("unrelated tokens are unmatched")
	}
}

func TestCompareAlignment(t *testing.T) {
	var m NameMatcher
	r := m.Compare("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood")
	if r.Score != 1.0 || !r.Reordered {
		t.Errorf("reordered exact tokens: score=%v reordered=%v", r.Score, r.Reordered)
	}
	a := m.Compare("Hameed Qasrani", "Hamid al-Qasrani").Score
	b := m.Compare("Hamid al-Qasrani", "Hameed Qasrani").Score
	if !close(a, b) {
		t.Errorf("compare must be symmetric: %v vs %v", a, b)
	}
	if !close(a, (1.0+JaroWinkler("hameed", "hamid"))/2) {
		t.Errorf("unmatched particle must not dilute: %v", a)
	}
	if s := m.Compare("Theodora Grimblewood", "Thaddeus Grimblewood").Score; s != 0.5 {
		t.Errorf("unmatched real token must dilute to 0.5, got %v", s)
	}
	if s := m.Compare("Rafael Ocampo", "Octavian Blunderbuss").Score; s >= 0.5 {
		t.Errorf("unrelated names must score low, got %v", s)
	}
	if m.Compare("", "Thaddeus Grimblewood").Score != 0.0 {
		t.Error("empty query scores zero")
	}
	if !strings.Contains(m.Compare("Perpetua Nitshade", "Perpetua Nightshade").Explain(), "fuzzy 'nitshade'~'nightshade'") {
		t.Error("explain must name the evidence")
	}
}

func TestScreening(t *testing.T) {
	wl := DemoWatchlist()
	exact, err := wl.Screen("Thaddeus Grimblewood", 0.85)
	if err != nil || !exact.IsHit() || exact.Hits[0].EntryID != "FIC-0001" {
		t.Fatalf("exact name must hit FIC-0001: %+v %v", exact, err)
	}
	count := 0
	for _, h := range exact.Hits {
		if h.EntryID == "FIC-0001" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("one hit per entry even with aliases, got %d", count)
	}
	alias, _ := wl.Screen("Hameed Al Qasrani", 0.85)
	if alias.Hits[0].MatchedName != "Hameed Qasrani" {
		t.Errorf("best alias must be reported, got %q", alias.Hits[0].MatchedName)
	}
	clear, _ := wl.Screen("Marisol Everly", 0.85)
	if clear.IsHit() {
		t.Error("unrelated name must be clear")
	}
	fp, _ := wl.Screen("Theodora Grimblewood", 0.85)
	if !fp.IsHit() {
		t.Error("initial alias produces a false positive at 0.85")
	}
	fp95, _ := wl.Screen("Theodora Grimblewood", 0.95)
	if fp95.IsHit() {
		t.Error("higher threshold removes the false positive")
	}
	wide, _ := wl.Screen("Perpetua Nightshade", 0.5)
	if wide.Hits[0].EntryID != "FIC-0006" {
		t.Error("hits must be sorted best first")
	}
}

func TestScreeningErrors(t *testing.T) {
	wl := DemoWatchlist()
	if _, err := wl.Screen("x", 0.0); !errors.Is(err, ErrBadThreshold) {
		t.Error("threshold zero must be rejected")
	}
	if _, err := wl.Screen("x", 1.5); !errors.Is(err, ErrBadThreshold) {
		t.Error("threshold above one must be rejected")
	}
	_, err := NewWatchlist([]WatchlistEntry{{"A", "X Y", "P", nil}, {"A", "Z W", "P", nil}})
	if !errors.Is(err, ErrDuplicateEntry) {
		t.Error("duplicate ids must be rejected")
	}
}

func TestThresholdTuning(t *testing.T) {
	var m NameMatcher
	pairs := LabelledPairs()
	at100 := Evaluate(m, pairs, 1.0)
	if at100.FP != 0 || at100.Precision() != 1.0 {
		t.Errorf("threshold 1.0 has no false positives: %+v", at100)
	}
	if at100.FN != 8 || !close(at100.Recall(), 1.0/3) {
		t.Errorf("threshold 1.0 misses variants: %+v", at100)
	}
	at85 := Evaluate(m, pairs, 0.85)
	if at85.Recall() != 1.0 || at85.FP != 6 {
		t.Errorf("threshold 0.85 full recall with 6 fp: %+v", at85)
	}
	if at85.TP+at85.FP+at85.FN+at85.TN != len(pairs) {
		t.Error("confusion matrix must sum to the number of pairs")
	}
	table := ThresholdTable(m, pairs, []float64{0.7, 0.9, 1.0})
	for i := 1; i < len(table); i++ {
		if table[i].Recall() > table[i-1].Recall() || table[i].FP > table[i-1].FP {
			t.Error("raising the threshold never raises recall or false positives")
		}
	}
	if (Evaluation{0.5, 0, 0, 5, 0}).F1() != 0.0 {
		t.Error("f1 with no predictions is zero")
	}
}
