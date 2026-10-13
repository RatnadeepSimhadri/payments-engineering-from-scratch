"""Lesson: phases/11-payment-rails/11-iban-bic-and-account-validation/docs/en.md
IBAN, BIC, and Account Number Validation
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 13616-1 (IBAN) and ISO/IEC 7064 MOD 97-10; ISO 9362 (BIC); SWIFT IBAN Registry for the
country table; ABA routing transit number 3-7-1 check digit. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


# ---------------------------------------------------------------------------
# Structured failure reasons
# ---------------------------------------------------------------------------


class Severity(Enum):
    ERROR = "ERROR"
    WARNING = "WARNING"


class Reason(Enum):
    """Why a validation failed. Codes are stable identifiers a caller can branch on; messages are for humans."""

    EMPTY = "EMPTY"
    INVALID_CHARACTERS = "INVALID_CHARACTERS"
    UNKNOWN_COUNTRY = "UNKNOWN_COUNTRY"
    WRONG_LENGTH = "WRONG_LENGTH"
    INVALID_CHECK_DIGITS = "INVALID_CHECK_DIGITS"
    BBAN_STRUCTURE = "BBAN_STRUCTURE"
    CHECKSUM_FAILED = "CHECKSUM_FAILED"
    INVALID_PREFIX = "INVALID_PREFIX"
    TEST_BIC = "TEST_BIC"
    COUNTRY_MISMATCH = "COUNTRY_MISMATCH"


@dataclass(frozen=True)
class Issue:
    reason: Reason
    severity: Severity
    message: str

    def __str__(self) -> str:
        return f"{self.severity.value} {self.reason.value}: {self.message}"


@dataclass(frozen=True)
class ValidationResult:
    """The outcome of validating one identifier. `valid` is true when no ERROR-severity issue exists."""

    kind: str
    normalized: str
    issues: tuple[Issue, ...] = ()

    @property
    def valid(self) -> bool:
        return all(i.severity is not Severity.ERROR for i in self.issues)

    def has(self, reason: Reason) -> bool:
        return any(i.reason is reason for i in self.issues)


def normalize(text: str) -> str:
    """Strip whitespace and hyphens, upper-case. The paper form 'GB82 WEST 1234 ...' becomes machine form."""
    return "".join(ch for ch in text if ch not in " -\t\r\n").upper()


# ---------------------------------------------------------------------------
# ISO 7064 MOD 97-10
# ---------------------------------------------------------------------------


def mod97(text: str) -> int:
    """Interpret letters as two-digit numbers (A=10 .. Z=35) and reduce the whole digit string modulo 97.

    Processed one digit at a time so the intermediate value never exceeds 97*10+9; no big integers needed,
    which is why the Java and Go versions can use a plain long/int64."""
    remainder = 0
    for ch in text:
        if ch.isdigit():
            remainder = (remainder * 10 + (ord(ch) - ord("0"))) % 97
        elif "A" <= ch <= "Z":
            value = ord(ch) - ord("A") + 10
            remainder = (remainder * 10 + value // 10) % 97
            remainder = (remainder * 10 + value % 10) % 97
        else:
            raise ValueError(f"mod97 input must be A-Z or 0-9, got {ch!r}")
    return remainder


# ---------------------------------------------------------------------------
# Country formats (subset of the SWIFT IBAN Registry)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Group:
    """One run of characters in a BBAN: count and character class n (digits), a (upper letters), c (either)."""

    count: int
    char_class: str

    def matches(self, segment: str) -> bool:
        if len(segment) != self.count:
            return False
        if self.char_class == "n":
            return segment.isdigit()
        if self.char_class == "a":
            return all("A" <= ch <= "Z" for ch in segment)
        return all(ch.isdigit() or "A" <= ch <= "Z" for ch in segment)


@dataclass(frozen=True)
class CountryFormat:
    country: str
    length: int
    groups: tuple[Group, ...]
    bank_id: tuple[int, int]  # (start, length) of the bank identifier inside the BBAN
    branch_id: tuple[int, int] | None = None

    def bban_length(self) -> int:
        return sum(g.count for g in self.groups)


def _fmt(country: str, length: int, pattern: str, bank_id: tuple[int, int], branch_id: tuple[int, int] | None = None) -> CountryFormat:
    groups: list[Group] = []
    for token in pattern.split():
        groups.append(Group(int(token[:-1]), token[-1]))
    f = CountryFormat(country, length, tuple(groups), bank_id, branch_id)
    assert f.bban_length() + 4 == length, country
    return f


COUNTRY_FORMATS: dict[str, CountryFormat] = {
    f.country: f
    for f in [
        _fmt("AE", 23, "3n 16n", (0, 3)),
        _fmt("AT", 20, "5n 11n", (0, 5)),
        _fmt("BE", 16, "3n 7n 2n", (0, 3)),
        _fmt("CH", 21, "5n 12c", (0, 5)),
        _fmt("DE", 22, "8n 10n", (0, 8)),
        _fmt("DK", 18, "4n 9n 1n", (0, 4)),
        _fmt("ES", 24, "4n 4n 1n 1n 10n", (0, 4), (4, 4)),
        _fmt("FI", 18, "3n 11n", (0, 3)),
        _fmt("FR", 27, "5n 5n 11c 2n", (0, 5), (5, 5)),
        _fmt("GB", 22, "4a 6n 8n", (0, 4), (4, 6)),
        _fmt("IE", 22, "4a 6n 8n", (0, 4), (4, 6)),
        _fmt("IT", 27, "1a 5n 5n 12c", (1, 5), (6, 5)),
        _fmt("LU", 20, "3n 13c", (0, 3)),
        _fmt("NL", 18, "4a 10n", (0, 4)),
        _fmt("NO", 15, "4n 6n 1n", (0, 4)),
        _fmt("PL", 28, "8n 16n", (0, 8)),
        _fmt("PT", 25, "4n 4n 11n 2n", (0, 4), (4, 4)),
        _fmt("SA", 24, "2n 18c", (0, 2)),
        _fmt("SE", 24, "3n 16n 1n", (0, 3)),
    ]
}

# ISO 3166-1 alpha-2 codes a BIC may carry. The IBAN countries above plus common non-IBAN jurisdictions.
KNOWN_COUNTRIES: frozenset[str] = frozenset(COUNTRY_FORMATS) | frozenset(
    ["US", "CA", "JP", "AU", "SG", "HK", "IN", "CN", "BR", "MX", "ZA", "NZ", "KR"]
)


# ---------------------------------------------------------------------------
# IBAN
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Iban:
    """A validated IBAN decomposed into its parts. Construct through Iban.parse, never directly."""

    country: str
    check_digits: str
    bban: str
    bank_identifier: str
    branch_identifier: str | None

    @property
    def value(self) -> str:
        return self.country + self.check_digits + self.bban

    def formatted(self) -> str:
        """Paper form: groups of four separated by spaces (ISO 13616-1, section 6)."""
        v = self.value
        return " ".join(v[i : i + 4] for i in range(0, len(v), 4))

    @staticmethod
    def validate(text: str) -> ValidationResult:
        issues: list[Issue] = []
        s = normalize(text)
        if not s:
            return ValidationResult("IBAN", s, (Issue(Reason.EMPTY, Severity.ERROR, "empty IBAN"),))
        if not all(ch.isdigit() or "A" <= ch <= "Z" for ch in s):
            issues.append(Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "IBAN may contain only A-Z and 0-9"))
            return ValidationResult("IBAN", s, tuple(issues))
        if len(s) < 4 or not s[:2].isalpha():
            issues.append(Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, f"no country code in {s!r}"))
            return ValidationResult("IBAN", s, tuple(issues))
        country = s[:2]
        fmt = COUNTRY_FORMATS.get(country)
        if fmt is None:
            issues.append(Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, f"{country} is not in the IBAN registry subset"))
            return ValidationResult("IBAN", s, tuple(issues))
        if len(s) != fmt.length:
            issues.append(
                Issue(Reason.WRONG_LENGTH, Severity.ERROR, f"{country} IBAN must be {fmt.length} characters, got {len(s)}")
            )
            return ValidationResult("IBAN", s, tuple(issues))
        check = s[2:4]
        if not check.isdigit() or check in ("00", "01", "99"):
            issues.append(Issue(Reason.INVALID_CHECK_DIGITS, Severity.ERROR, f"check digits {check!r} are structurally invalid"))
        bban = s[4:]
        pos = 0
        for g in fmt.groups:
            segment = bban[pos : pos + g.count]
            if not g.matches(segment):
                issues.append(
                    Issue(
                        Reason.BBAN_STRUCTURE,
                        Severity.ERROR,
                        f"BBAN positions {pos + 1}-{pos + g.count} must be {g.count}{g.char_class}, got {segment!r}",
                    )
                )
                break
            pos += g.count
        if check.isdigit() and mod97(bban + country + check) != 1:
            issues.append(Issue(Reason.CHECKSUM_FAILED, Severity.ERROR, "mod-97 remainder is not 1; a digit is wrong or transposed"))
        return ValidationResult("IBAN", s, tuple(issues))

    @staticmethod
    def parse(text: str) -> Iban:
        result = Iban.validate(text)
        if not result.valid:
            raise ValueError("; ".join(str(i) for i in result.issues))
        s = result.normalized
        fmt = COUNTRY_FORMATS[s[:2]]
        bban = s[4:]
        bank = bban[fmt.bank_id[0] : fmt.bank_id[0] + fmt.bank_id[1]]
        branch = None
        if fmt.branch_id is not None:
            branch = bban[fmt.branch_id[0] : fmt.branch_id[0] + fmt.branch_id[1]]
        return Iban(s[:2], s[2:4], bban, bank, branch)

    @staticmethod
    def check_digits_for(country: str, bban: str) -> str:
        """ISO 13616-1 generation: replace the check digits with 00, compute 98 - mod97, zero-pad to two digits."""
        country = country.upper()
        bban = normalize(bban)
        fmt = COUNTRY_FORMATS.get(country)
        if fmt is None:
            raise ValueError(f"unknown country {country}")
        if len(bban) != fmt.bban_length():
            raise ValueError(f"{country} BBAN must be {fmt.bban_length()} characters, got {len(bban)}")
        remainder = mod97(bban + country + "00")
        return f"{98 - remainder:02d}"

    @staticmethod
    def generate(country: str, bban: str) -> Iban:
        check = Iban.check_digits_for(country, bban)
        return Iban.parse(country.upper() + check + normalize(bban))


# ---------------------------------------------------------------------------
# BIC (ISO 9362)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Bic:
    institution: str  # 4 characters: party prefix
    country: str  # 2 letters: ISO 3166-1
    location: str  # 2 characters
    branch: str  # 3 characters, 'XXX' for the primary office

    @property
    def value(self) -> str:
        return self.institution + self.country + self.location + self.branch

    @property
    def is_test(self) -> bool:
        return self.location[1] == "0"

    @property
    def bic8(self) -> str:
        return self.institution + self.country + self.location

    @staticmethod
    def validate(text: str) -> ValidationResult:
        issues: list[Issue] = []
        s = normalize(text)
        if not s:
            return ValidationResult("BIC", s, (Issue(Reason.EMPTY, Severity.ERROR, "empty BIC"),))
        if len(s) not in (8, 11):
            issues.append(Issue(Reason.WRONG_LENGTH, Severity.ERROR, f"BIC must be 8 or 11 characters, got {len(s)}"))
            return ValidationResult("BIC", s, tuple(issues))
        if not all(ch.isdigit() or "A" <= ch <= "Z" for ch in s):
            issues.append(Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "BIC may contain only A-Z and 0-9"))
            return ValidationResult("BIC", s, tuple(issues))
        country = s[4:6]
        if not country.isalpha():
            issues.append(Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, f"country code {country!r} must be two letters"))
        elif country not in KNOWN_COUNTRIES:
            issues.append(Issue(Reason.UNKNOWN_COUNTRY, Severity.ERROR, f"{country} is not a known ISO 3166-1 country"))
        location = s[6:8]
        if location[1] == "0":
            issues.append(Issue(Reason.TEST_BIC, Severity.WARNING, "location code ending in 0 denotes a test BIC"))
        return ValidationResult("BIC", s, tuple(issues))

    @staticmethod
    def parse(text: str) -> Bic:
        result = Bic.validate(text)
        if not result.valid:
            raise ValueError("; ".join(str(i) for i in result.issues))
        s = result.normalized
        return Bic(s[:4], s[4:6], s[6:8], s[8:11] if len(s) == 11 else "XXX")


# ---------------------------------------------------------------------------
# ABA routing transit number
# ---------------------------------------------------------------------------

ABA_WEIGHTS = (3, 7, 1, 3, 7, 1, 3, 7, 1)


class RoutingNumber:
    @staticmethod
    def check_digit(first_eight: str) -> str:
        if len(first_eight) != 8 or not first_eight.isdigit():
            raise ValueError("need exactly eight digits")
        total = sum(w * int(d) for w, d in zip(ABA_WEIGHTS, first_eight))
        return str((10 - total % 10) % 10)

    @staticmethod
    def prefix_is_valid(prefix: int) -> bool:
        """Federal Reserve routing symbol ranges: 00-12 banks, 21-32 thrifts, 61-72 electronic, 80 traveler's cheques."""
        return 0 <= prefix <= 12 or 21 <= prefix <= 32 or 61 <= prefix <= 72 or prefix == 80

    @staticmethod
    def validate(text: str) -> ValidationResult:
        issues: list[Issue] = []
        s = normalize(text)
        if not s:
            return ValidationResult("ABA", s, (Issue(Reason.EMPTY, Severity.ERROR, "empty routing number"),))
        if not s.isdigit():
            issues.append(Issue(Reason.INVALID_CHARACTERS, Severity.ERROR, "routing number must be digits"))
            return ValidationResult("ABA", s, tuple(issues))
        if len(s) != 9:
            issues.append(Issue(Reason.WRONG_LENGTH, Severity.ERROR, f"routing number must be 9 digits, got {len(s)}"))
            return ValidationResult("ABA", s, tuple(issues))
        if not RoutingNumber.prefix_is_valid(int(s[:2])):
            issues.append(Issue(Reason.INVALID_PREFIX, Severity.ERROR, f"prefix {s[:2]} is not a Federal Reserve routing symbol"))
        total = sum(w * int(d) for w, d in zip(ABA_WEIGHTS, s))
        if total % 10 != 0:
            issues.append(Issue(Reason.CHECKSUM_FAILED, Severity.ERROR, f"3-7-1 weighted sum {total} is not a multiple of 10"))
        return ValidationResult("ABA", s, tuple(issues))


# ---------------------------------------------------------------------------
# Cross-field consistency
# ---------------------------------------------------------------------------


class AccountValidator:
    """Facade: validate an IBAN and BIC pair as a payment system receives them on a payout instruction."""

    @staticmethod
    def validate_pair(iban_text: str, bic_text: str) -> ValidationResult:
        iban = Iban.validate(iban_text)
        bic = Bic.validate(bic_text)
        issues = list(iban.issues) + list(bic.issues)
        if iban.valid and bic.valid and iban.normalized[:2] != bic.normalized[4:6]:
            issues.append(
                Issue(
                    Reason.COUNTRY_MISMATCH,
                    Severity.WARNING,
                    f"IBAN country {iban.normalized[:2]} differs from BIC country {bic.normalized[4:6]}",
                )
            )
        return ValidationResult("IBAN+BIC", iban.normalized + "/" + bic.normalized, tuple(issues))


# ---------------------------------------------------------------------------
# Worked example
# ---------------------------------------------------------------------------


def _show(result: ValidationResult) -> None:
    status = "valid" if result.valid else "INVALID"
    print(f"  {result.kind:8s} {result.normalized:32s} {status}")
    for issue in result.issues:
        print(f"           - {issue}")


def main() -> None:
    print("== Mod-97 by hand: GB82 WEST 1234 5698 7654 32 ==")
    iban = Iban.parse("GB82 WEST 1234 5698 7654 32")
    rearranged = iban.bban + iban.country + iban.check_digits
    print(f"  rearranged      = {rearranged}")
    print(f"  mod 97          = {mod97(rearranged)} (must be 1)")
    print(f"  bank identifier = {iban.bank_identifier}, branch (sort code) = {iban.branch_identifier}")
    print(f"  check digits regenerated from BBAN = {Iban.check_digits_for('GB', iban.bban)}")

    print()
    print("== What mod-97 catches ==")
    for label, mutated in [
        ("one digit changed  ", "GB82WEST12345698765431"),
        ("adjacent transposed", "GB82WEST12345698765423"),
        ("two groups swapped ", "GB82WEST56981234765432"),
    ]:
        print(f"  {label} {mutated}: {'valid' if Iban.validate(mutated).valid else 'rejected'}")

    print()
    print("== Structured failures ==")
    for sample in [
        "DE89 3704 0044 0532 0130 00",
        "FR14 2004 1010 0505 0001 3M02 606",
        "DE89 3704 0044 0532 0130 0",
        "GB82 WEST 1234 5698 7654 3X",
        "GB00 WEST 1234 5698 7654 32",
        "XX82 WEST 1234 5698 7654 32",
        "gb82-west-1234-5698-7654-32",
    ]:
        _show(Iban.validate(sample))

    print()
    print("== BIC (ISO 9362) ==")
    for sample in ["DEUTDEFF", "DEUTDEFF500", "NWBKGB2L", "DEUTDEF0", "DEUTDE", "DEUTZZFF"]:
        _show(Bic.validate(sample))

    print()
    print("== ABA routing numbers (fictitious prefixes, real check digits) ==")
    for sample in ["021999996", "011999993", "021999969", "991999990", "02199999"]:
        _show(RoutingNumber.validate(sample))

    print()
    print("== Payout instruction: IBAN + BIC together ==")
    _show(AccountValidator.validate_pair("DE89 3704 0044 0532 0130 00", "DEUTDEFF"))
    _show(AccountValidator.validate_pair("DE89 3704 0044 0532 0130 00", "NWBKGB2L"))

    print()
    print("== Generating an IBAN from a BBAN ==")
    generated = Iban.generate("NL", "ABNA0417164300")
    print(f"  NL + ABNA0417164300 -> {generated.formatted()}")


if __name__ == "__main__":
    main()
