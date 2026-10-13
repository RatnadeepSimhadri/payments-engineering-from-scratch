"""Lesson: phases/13-risk-fraud-and-compliance/04-sanctions-screening/docs/en.md
Sanctions Screening: Name Matching Under Uncertainty
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Winkler, "String Comparator Metrics and Enhanced Decision Rules in the Fellegi-Sunter
         Model of Record Linkage" (1990); U.S. National Archives, "The Soundex Indexing System";
         OFAC, "Sanctions List Search" FAQ on fuzzy matching and the score slider.
Python 3.11+, standard library only. Same design as the Java and Go versions.
Every name on the watchlist in this file is fictional.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum

# ---------------------------------------------------------------------------------------------
# Step 1: normalization. Case, diacritics, punctuation, honorifics, whitespace.
# The diacritic table is explicit (not unicodedata) so the three languages fold identically.
# ---------------------------------------------------------------------------------------------

DIACRITICS: dict[str, str] = {}
for _base, _variants in {
    "a": "àáâãäåāăą", "c": "çćĉċč", "d": "ďđ", "e": "èéêëēĕėęě", "g": "ĝğġģ", "h": "ĥħ",
    "i": "ìíîïĩīĭįı", "j": "ĵ", "k": "ķ", "l": "ĺļľŀł", "n": "ñńņňŉ", "o": "òóôõöøōŏő",
    "r": "ŕŗř", "s": "śŝşš", "t": "ţťŧ", "u": "ùúûüũūŭůűų", "w": "ŵ", "y": "ýÿŷ", "z": "źżž",
}.items():
    for _v in _variants:
        DIACRITICS[_v] = _base
        DIACRITICS[_v.upper()] = _base
DIACRITICS["ß"] = "ss"
DIACRITICS["æ"] = "ae"
DIACRITICS["Æ"] = "ae"
DIACRITICS["œ"] = "oe"
DIACRITICS["Œ"] = "oe"

# Titles carry no identity and are removed before matching.
STOPWORDS = frozenset({"mr", "mrs", "ms", "dr", "jr", "sr", "prof", "sir"})
# Particles are part of the name and are matched when present on both sides, but an unmatched
# particle is not evidence against a match: "Hameed Qasrani" is still "Hamid al-Qasrani".
PARTICLES = frozenset({"al", "el", "bin", "ibn", "abu", "de", "del", "da", "di", "la", "le", "van", "von", "der", "den"})


def normalize(name: str) -> str:
    """Lowercase ASCII letters and digits separated by single spaces; honorifics removed."""
    out: list[str] = []
    for ch in name:
        if ch in DIACRITICS:
            out.append(DIACRITICS[ch])
        elif ch.isascii() and ch.isalnum():
            out.append(ch.lower())
        else:
            out.append(" ")  # punctuation, hyphens, apostrophes, anything else
    tokens = [t for t in "".join(out).split() if t not in STOPWORDS]
    return " ".join(tokens)


def tokenize(normalized: str) -> list[str]:
    return normalized.split()


# ---------------------------------------------------------------------------------------------
# Step 2: Jaro-Winkler, from scratch. Returns a similarity in [0, 1]; 1 means identical.
# ---------------------------------------------------------------------------------------------


def jaro(a: str, b: str) -> float:
    if a == b:
        return 1.0
    if not a or not b:
        return 0.0
    window = max(len(a), len(b)) // 2 - 1
    if window < 0:
        window = 0
    a_matched = [False] * len(a)
    b_matched = [False] * len(b)
    matches = 0
    for i, ca in enumerate(a):
        lo = max(0, i - window)
        hi = min(len(b), i + window + 1)
        for j in range(lo, hi):
            if not b_matched[j] and b[j] == ca:
                a_matched[i] = True
                b_matched[j] = True
                matches += 1
                break
    if matches == 0:
        return 0.0
    # transpositions: matched characters in order, count positions that disagree
    transpositions = 0
    j = 0
    for i, ca in enumerate(a):
        if not a_matched[i]:
            continue
        while not b_matched[j]:
            j += 1
        if ca != b[j]:
            transpositions += 1
        j += 1
    half = transpositions // 2
    return (matches / len(a) + matches / len(b) + (matches - half) / matches) / 3.0


JW_PREFIX_SCALE = 0.1
JW_MAX_PREFIX = 4


def jaro_winkler(a: str, b: str) -> float:
    base = jaro(a, b)
    prefix = 0
    for ca, cb in zip(a, b):
        if ca != cb or prefix == JW_MAX_PREFIX:
            break
        prefix += 1
    return base + prefix * JW_PREFIX_SCALE * (1.0 - base)


# ---------------------------------------------------------------------------------------------
# Step 3: Soundex. A four-character phonetic key: first letter + three digits.
# ---------------------------------------------------------------------------------------------

_SOUNDEX_CODES = {
    **dict.fromkeys("bfpv", "1"), **dict.fromkeys("cgjkqsxz", "2"), **dict.fromkeys("dt", "3"),
    "l": "4", **dict.fromkeys("mn", "5"), "r": "6",
}


def soundex(token: str) -> str:
    t = [c for c in token.lower() if "a" <= c <= "z"]
    if not t:
        return ""
    key = t[0].upper()
    last = _SOUNDEX_CODES.get(t[0], "")
    for c in t[1:]:
        if c in "hw":
            continue  # h and w are transparent: letters either side with the same code merge
        code = _SOUNDEX_CODES.get(c, "")
        if code == "":
            last = ""  # a vowel separates: the same code may be emitted again
            continue
        if code != last:
            key += code
            last = code
        if len(key) == 4:
            break
    return (key + "000")[:4]


# ---------------------------------------------------------------------------------------------
# Step 4: token alignment with explainable reasons.
# ---------------------------------------------------------------------------------------------


class ReasonKind(Enum):
    EXACT = "exact"
    FUZZY = "fuzzy"
    PHONETIC = "phonetic"
    INITIAL = "initial"
    PARTICLE = "particle"
    UNMATCHED = "unmatched"


@dataclass(frozen=True)
class MatchReason:
    kind: ReasonKind
    query_token: str
    list_token: str
    score: float

    def __str__(self) -> str:
        if self.kind is ReasonKind.UNMATCHED:
            return f"unmatched '{self.query_token or self.list_token}' (+0.00)"
        if self.kind is ReasonKind.PARTICLE:
            return f"particle '{self.query_token or self.list_token}' ignored"
        if self.kind is ReasonKind.PHONETIC:
            return f"phonetic '{self.query_token}'~'{self.list_token}' {soundex(self.query_token)} (+{self.score:.2f})"
        return f"{self.kind.value} '{self.query_token}'~'{self.list_token}' (+{self.score:.2f})"


@dataclass(frozen=True)
class MatchScore:
    score: float
    reasons: list[MatchReason] = field(default_factory=list)
    reordered: bool = False

    def explain(self) -> str:
        parts = [str(r) for r in self.reasons]
        if self.reordered:
            parts.append("token order ignored")
        return f"score={self.score:.3f} [" + "; ".join(parts) + "]"


PHONETIC_FLOOR = 0.85
INITIAL_SCORE = 0.85
FUZZY_FLOOR = 0.70  # a token pair below this contributes nothing: it is noise, not evidence


def _pair_score(q: str, c: str) -> tuple[ReasonKind, float]:
    if q == c:
        return ReasonKind.EXACT, 1.0
    if len(q) == 1 and c.startswith(q) or len(c) == 1 and q.startswith(c):
        return ReasonKind.INITIAL, INITIAL_SCORE
    jw = jaro_winkler(q, c)
    if soundex(q) == soundex(c) and jw < PHONETIC_FLOOR:
        return ReasonKind.PHONETIC, PHONETIC_FLOOR
    if jw >= FUZZY_FLOOR:
        return ReasonKind.FUZZY, jw
    return ReasonKind.UNMATCHED, 0.0


class NameMatcher:
    """Greedy best-pair alignment of query tokens to list tokens. Order does not matter;
    every token on the longer side must find a partner or the score is diluted."""

    def compare(self, query: str, candidate: str) -> MatchScore:
        q = tokenize(normalize(query))
        c = tokenize(normalize(candidate))
        if not q or not c:
            return MatchScore(0.0, [])
        pairs: list[tuple[float, int, int, ReasonKind]] = []
        for i, qt in enumerate(q):
            for j, ct in enumerate(c):
                kind, s = _pair_score(qt, ct)
                if kind is not ReasonKind.UNMATCHED:
                    pairs.append((s, i, j, kind))
        pairs.sort(key=lambda p: (-p[0], p[1], p[2]))  # best first; ties by position: deterministic
        used_q = [False] * len(q)
        used_c = [False] * len(c)
        reasons: list[MatchReason] = []
        total = 0.0
        reordered = False
        for s, i, j, kind in pairs:
            if used_q[i] or used_c[j]:
                continue
            used_q[i] = used_c[j] = True
            total += s
            if i != j:
                reordered = True
            reasons.append(MatchReason(kind, q[i], c[j], s))
        q_count, c_count = len(q), len(c)
        for i, qt in enumerate(q):
            if not used_q[i]:
                if qt in PARTICLES:
                    q_count -= 1
                    reasons.append(MatchReason(ReasonKind.PARTICLE, qt, "", 0.0))
                else:
                    reasons.append(MatchReason(ReasonKind.UNMATCHED, qt, "", 0.0))
        for j, ct in enumerate(c):
            if not used_c[j]:
                if ct in PARTICLES:
                    c_count -= 1
                    reasons.append(MatchReason(ReasonKind.PARTICLE, "", ct, 0.0))
                else:
                    reasons.append(MatchReason(ReasonKind.UNMATCHED, "", ct, 0.0))
        denominator = max(q_count, c_count, 1)
        return MatchScore(total / denominator, reasons, reordered)


# ---------------------------------------------------------------------------------------------
# Step 5: the watchlist and the screening result.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class WatchlistEntry:
    entry_id: str
    name: str
    program: str
    aliases: tuple[str, ...] = ()

    def all_names(self) -> list[str]:
        return [self.name, *self.aliases]


@dataclass(frozen=True)
class Hit:
    entry_id: str
    matched_name: str
    program: str
    match: MatchScore

    def explain(self) -> str:
        return f"{self.entry_id} '{self.matched_name}' [{self.program}] {self.match.explain()}"


@dataclass(frozen=True)
class ScreeningResult:
    query: str
    normalized: str
    threshold: float
    hits: list[Hit]  # best first

    def is_hit(self) -> bool:
        return bool(self.hits)

    def explain(self) -> str:
        head = f"screen '{self.query}' -> '{self.normalized}' @ {self.threshold:.2f}: " + ("HIT" if self.hits else "CLEAR")
        return "\n".join([head, *(f"  {h.explain()}" for h in self.hits)])


class Watchlist:
    def __init__(self, entries: list[WatchlistEntry], matcher: NameMatcher | None = None) -> None:
        ids = [e.entry_id for e in entries]
        if len(set(ids)) != len(ids):
            raise ValueError("duplicate watchlist entry id")
        self.entries = list(entries)
        self.matcher = matcher or NameMatcher()

    def screen(self, name: str, threshold: float) -> ScreeningResult:
        if not 0.0 < threshold <= 1.0:
            raise ValueError("threshold must be in (0, 1]")
        hits: list[Hit] = []
        for entry in self.entries:
            best: Hit | None = None
            for candidate in entry.all_names():  # best alias per entry: one hit per listed person
                m = self.matcher.compare(name, candidate)
                if best is None or m.score > best.match.score:
                    best = Hit(entry.entry_id, candidate, entry.program, m)
            if best is not None and best.match.score >= threshold:
                hits.append(best)
        hits.sort(key=lambda h: (-h.match.score, h.entry_id))
        return ScreeningResult(name, normalize(name), threshold, hits)


# ---------------------------------------------------------------------------------------------
# Step 6: threshold tuning against a labelled synthetic set.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class LabelledPair:
    query: str
    list_name: str
    is_match: bool


@dataclass(frozen=True)
class Evaluation:
    threshold: float
    tp: int
    fp: int
    fn: int
    tn: int

    def precision(self) -> float:
        return self.tp / (self.tp + self.fp) if self.tp + self.fp else 1.0

    def recall(self) -> float:
        return self.tp / (self.tp + self.fn) if self.tp + self.fn else 1.0

    def f1(self) -> float:
        p, r = self.precision(), self.recall()
        return 2 * p * r / (p + r) if p + r else 0.0


def evaluate(matcher: NameMatcher, pairs: list[LabelledPair], threshold: float) -> Evaluation:
    tp = fp = fn = tn = 0
    for pair in pairs:
        predicted = matcher.compare(pair.query, pair.list_name).score >= threshold
        if predicted and pair.is_match:
            tp += 1
        elif predicted:
            fp += 1
        elif pair.is_match:
            fn += 1
        else:
            tn += 1
    return Evaluation(threshold, tp, fp, fn, tn)


def threshold_table(matcher: NameMatcher, pairs: list[LabelledPair], thresholds: list[float]) -> list[Evaluation]:
    return [evaluate(matcher, pairs, t) for t in thresholds]


# ---------------------------------------------------------------------------------------------
# Demo data. Every name here is invented for the lesson.
# ---------------------------------------------------------------------------------------------


def demo_watchlist() -> Watchlist:
    return Watchlist([
        WatchlistEntry("FIC-0001", "Thaddeus Grimblewood", "FICTIONAL-SDN", ("Tadeusz Grimblewood", "T. Grimblewood")),
        WatchlistEntry("FIC-0002", "Yevgenia Kraskovitch", "FICTIONAL-SDN", ("Evgenia Kraskovich",)),
        WatchlistEntry("FIC-0003", "Hamid al-Qasrani", "FICTIONAL-SDN", ("Hamid Al Qasrani", "Hameed Qasrani")),
        WatchlistEntry("FIC-0004", "Ximena Fortaleza-Rúa", "FICTIONAL-SSI", ("Ximena Fortaleza Rua",)),
        WatchlistEntry("FIC-0005", "Octavian Blunderbuss", "FICTIONAL-SSI", ()),
        WatchlistEntry("FIC-0006", "Perpetua Nightshade", "FICTIONAL-SDN", ("Perpetua Nightshade-Vale",)),
        WatchlistEntry("FIC-0007", "Bartholomew Quillfeather", "FICTIONAL-SSI", ("Bart Quillfeather",)),
    ])


def labelled_pairs() -> list[LabelledPair]:
    """Synthetic ground truth: true variants of listed names and hard negatives that share tokens."""
    return [
        LabelledPair("Thaddeus Grimblewood", "Thaddeus Grimblewood", True),
        LabelledPair("GRIMBLEWOOD, THADDEUS", "Thaddeus Grimblewood", True),
        LabelledPair("Thadeus Grimblewod", "Thaddeus Grimblewood", True),
        LabelledPair("T Grimblewood", "Thaddeus Grimblewood", True),
        LabelledPair("Evgenia Kraskovich", "Yevgenia Kraskovitch", True),
        LabelledPair("Yevgeniya Kraskovitch", "Yevgenia Kraskovitch", True),
        LabelledPair("Hamid Al-Qasrani", "Hamid al-Qasrani", True),
        LabelledPair("Hameed Qasrani", "Hamid al-Qasrani", True),
        LabelledPair("Ximena Fortaleza Rua", "Ximena Fortaleza-Rúa", True),
        LabelledPair("Octavian Blunderbus", "Octavian Blunderbuss", True),
        LabelledPair("Perpetua Nitshade", "Perpetua Nightshade", True),
        LabelledPair("Bart Quillfeather", "Bartholomew Quillfeather", True),
        LabelledPair("Theodora Grimblewood", "Thaddeus Grimblewood", False),
        LabelledPair("Thaddeus Grimble", "Thaddeus Grimblewood", False),
        LabelledPair("Yevgenia Krasnova", "Yevgenia Kraskovitch", False),
        LabelledPair("Hamid Qasim", "Hamid al-Qasrani", False),
        LabelledPair("Ximena Fortes", "Ximena Fortaleza-Rúa", False),
        LabelledPair("Octavia Blundell", "Octavian Blunderbuss", False),
        LabelledPair("Perpetua Nightingale", "Perpetua Nightshade", False),
        LabelledPair("Bartholomew Quince", "Bartholomew Quillfeather", False),
        LabelledPair("Marisol Everly", "Thaddeus Grimblewood", False),
        LabelledPair("Kwame Oduya-Brandt", "Hamid al-Qasrani", False),
        LabelledPair("Ingrid Halvorsen", "Yevgenia Kraskovitch", False),
        LabelledPair("Rafael Ocampo", "Octavian Blunderbuss", False),
    ]


def main() -> None:
    print("== Normalization ==")
    for raw in ["GRIMBLEWOOD, Thaddeus", "Dr. Ximena Fortaleza-Rúa", "Hamid al-Qasrani", "Müller-Lüdenscheidt, Jörg"]:
        print(f"  {raw!r:34} -> {normalize(raw)!r}")

    print("\n== Jaro-Winkler on classic pairs ==")
    for a, b in [("martha", "marhta"), ("dwayne", "duane"), ("dixon", "dicksonx"), ("grimblewood", "grimblewod"), ("abc", "xyz")]:
        print(f"  {a:12} {b:12} jaro={jaro(a, b):.4f}  jaro_winkler={jaro_winkler(a, b):.4f}")

    print("\n== Soundex ==")
    for t in ["robert", "rupert", "tymczak", "pfister", "ashcraft", "nightshade", "nitshade", "kraskovitch", "kraskovich"]:
        print(f"  {t:12} {soundex(t)}")

    wl = demo_watchlist()
    print("\n== Screening at threshold 0.85 ==")
    for q in ["Thaddeus Grimblewood", "GRIMBLEWOOD, THADDEUS", "Hameed Al Qasrani", "Perpetua Nitshade",
              "Theodora Grimblewood", "Marisol Everly"]:
        print(wl.screen(q, 0.85).explain())

    print("\n== Threshold tuning on the labelled synthetic set ==")
    print(f"  {'thr':>5} {'tp':>3} {'fp':>3} {'fn':>3} {'tn':>3} {'precision':>9} {'recall':>7} {'f1':>6}")
    for ev in threshold_table(wl.matcher, labelled_pairs(), [0.70, 0.75, 0.80, 0.85, 0.90, 0.95, 1.00]):
        print(f"  {ev.threshold:5.2f} {ev.tp:3} {ev.fp:3} {ev.fn:3} {ev.tn:3} {ev.precision():9.3f} {ev.recall():7.3f} {ev.f1():6.3f}")


if __name__ == "__main__":
    main()
