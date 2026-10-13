"""Lesson: phases/02-algorithms/15-numerical-algorithms/docs/en.md
Numerical Algorithms: GCD, Modular Arithmetic, and Check Digits
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO/IEC 7812-1 (Luhn, Annex B); ISO 7064:2003 (MOD 97-10); ISO 13616-1 (IBAN); Damm, "Totally
anti-symmetric quasigroups" (2004); Knuth TAOCP vol. 2 sec. 4.5.2 (Euclid).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Callable, Sequence

# -----------------------------------------------------------------------------------------------------------
# Euclid and modular arithmetic
# -----------------------------------------------------------------------------------------------------------


def gcd(a: int, b: int) -> int:
    """gcd(a, b) with gcd(0, 0) = 0 and the result always non-negative. O(log min(a, b)) divisions."""
    a, b = abs(a), abs(b)
    while b != 0:
        a, b = b, a % b
    return a


def lcm(a: int, b: int) -> int:
    if a == 0 or b == 0:
        return 0
    return abs(a // gcd(a, b) * b)


@dataclass(frozen=True)
class Bezout:
    """g = gcd(a, b) together with x, y such that a*x + b*y = g."""

    g: int
    x: int
    y: int


def extended_gcd(a: int, b: int) -> Bezout:
    """Extended Euclid: carries the coefficient pairs alongside the remainders."""
    old_r, r = a, b
    old_x, x = 1, 0
    old_y, y = 0, 1
    while r != 0:
        q = old_r // r  # floor division; identical to Java/Go truncation for the non-negative inputs used here
        old_r, r = r, old_r - q * r
        old_x, x = x, old_x - q * x
        old_y, y = y, old_y - q * y
    if old_r < 0:
        return Bezout(-old_r, -old_x, -old_y)
    return Bezout(old_r, old_x, old_y)


def mod(a: int, m: int) -> int:
    """Result in 0..m-1 regardless of sign. Python's % already does this for positive m; kept for symmetry."""
    if m <= 0:
        raise ValueError("modulus must be positive")
    return a % m


def mod_inverse(a: int, m: int) -> int:
    """The x in 0..m-1 with a*x = 1 (mod m); exists iff gcd(a, m) = 1."""
    if m <= 1:
        raise ValueError("modulus must be > 1")
    b = extended_gcd(mod(a, m), m)
    if b.g != 1:
        raise ArithmeticError(f"{a} has no inverse modulo {m} (gcd = {b.g})")
    return mod(b.x, m)


def mod_pow(base: int, exp: int, m: int) -> int:
    """base^exp mod m by repeated squaring, O(log exp) multiplications; exp must be non-negative."""
    if exp < 0:
        raise ValueError("exponent must be non-negative")
    result = 1 % m
    b = mod(base, m)
    while exp > 0:
        if exp & 1:
            result = result * b % m
        b = b * b % m
        exp >>= 1
    return result


def mod_of_decimal_string(digits: str, m: int) -> int:
    """Remainder of an arbitrarily long decimal string by Horner's rule: r = (r*10 + digit) mod m."""
    if not digits:
        raise ValueError("empty digit string")
    r = 0
    for i, c in enumerate(digits):
        if not "0" <= c <= "9":
            raise ValueError(f"not a digit at {i}: {c}")
        r = (r * 10 + ord(c) - 48) % m
    return r


# -----------------------------------------------------------------------------------------------------------
# Check digits
# -----------------------------------------------------------------------------------------------------------

Validator = Callable[[str], bool]


def _luhn_weighted_sum(digits: str, double_first: bool) -> int:
    total = 0
    dbl = double_first
    for i in range(len(digits) - 1, -1, -1):
        c = digits[i]
        if not "0" <= c <= "9":
            raise ValueError(f"not a digit at {i}: {c}")
        d = ord(c) - 48
        if dbl:
            d *= 2
            if d > 9:
                d -= 9  # digit sum of a two-digit product: 16 -> 1 + 6 = 7 = 16 - 9
        total += d
        dbl = not dbl
    return total


def luhn_check_digit(payload: str) -> int:
    """The digit c such that payload + c validates (ISO/IEC 7812-1 Annex B)."""
    if not payload:
        raise ValueError("empty payload")
    return (10 - _luhn_weighted_sum(payload, True) % 10) % 10


def luhn_validate(pan: str) -> bool:
    if len(pan) < 2 or not pan.isdigit() or not pan.isascii():
        return False
    return _luhn_weighted_sum(pan, False) % 10 == 0


def iban_to_numeric(iban: str) -> str:
    """Move the first four characters to the end, then replace each letter with two digits (A=10 .. Z=35)."""
    s = iban.replace(" ", "").upper()
    if len(s) < 5:
        raise ValueError(f"IBAN too short: {iban}")
    out = []
    for c in s[4:] + s[:4]:
        if "0" <= c <= "9":
            out.append(c)
        elif "A" <= c <= "Z":
            out.append(str(ord(c) - ord("A") + 10))
        else:
            raise ValueError(f"invalid IBAN character: {c}")
    return "".join(out)


def validate_iban(iban: str) -> bool:
    try:
        return mod_of_decimal_string(iban_to_numeric(iban), 97) == 1
    except ValueError:
        return False


def iban_check_digits(country_code: str, bban: str) -> str:
    """Two check digits: compute with "00" in the check position, then 98 - remainder, zero-padded."""
    r = mod_of_decimal_string(iban_to_numeric(country_code + "00" + bban), 97)
    return f"{98 - r:02d}"


def build_iban(country_code: str, bban: str) -> str:
    return country_code + iban_check_digits(country_code, bban) + bban


DAMM_TABLE = (
    (0, 3, 1, 7, 5, 9, 8, 6, 4, 2),
    (7, 0, 9, 2, 1, 5, 4, 8, 6, 3),
    (4, 2, 0, 6, 8, 7, 1, 3, 5, 9),
    (1, 7, 5, 0, 9, 8, 3, 4, 2, 6),
    (6, 1, 2, 3, 0, 4, 5, 9, 7, 8),
    (3, 6, 7, 4, 2, 0, 9, 5, 8, 1),
    (5, 8, 6, 9, 7, 2, 0, 1, 3, 4),
    (8, 9, 4, 5, 3, 6, 2, 0, 1, 7),
    (9, 4, 3, 8, 6, 1, 7, 2, 0, 5),
    (2, 5, 8, 1, 4, 3, 6, 7, 9, 0),
)


def _damm_interim(digits: str) -> int:
    state = 0
    for i, c in enumerate(digits):
        if not "0" <= c <= "9":
            raise ValueError(f"not a digit at {i}: {c}")
        state = DAMM_TABLE[state][ord(c) - 48]
    return state


def damm_check_digit(payload: str) -> int:
    """The final interim value; appending it drives the state to 0 because TABLE[s][s] = 0."""
    if not payload:
        raise ValueError("empty payload")
    return _damm_interim(payload)


def damm_validate(identifier: str) -> bool:
    if not identifier or not identifier.isdigit() or not identifier.isascii():
        return False
    return _damm_interim(identifier) == 0


# -----------------------------------------------------------------------------------------------------------
# Error analysis
# -----------------------------------------------------------------------------------------------------------


class ErrorClass(Enum):
    SINGLE_DIGIT = "single_digit"                      # 123 -> 173
    ADJACENT_TRANSPOSITION = "adjacent_transposition"  # 123 -> 213
    TWIN = "twin"                                      # 122 -> 155
    JUMP_TRANSPOSITION = "jump_transposition"          # 123 -> 321


@dataclass(frozen=True)
class Report:
    error_class: ErrorClass
    tried: int
    caught: int
    missed: list[str]

    def missed_count(self) -> int:
        return self.tried - self.caught

    def rate(self) -> str:
        return "n/a" if self.tried == 0 else f"{self.caught}/{self.tried}"


def corruptions(valid: str, cls: ErrorClass) -> list[str]:
    """Every corruption of the given class on `valid` (digit positions only; letters in an IBAN are left alone)."""
    out: list[str] = []
    c = list(valid)
    n = len(c)
    digits = "0123456789"
    if cls is ErrorClass.SINGLE_DIGIT:
        for i in range(n):
            if not c[i].isdigit():
                continue
            for d in digits:
                if d != c[i]:
                    m = c[:]
                    m[i] = d
                    out.append("".join(m))
    elif cls is ErrorClass.ADJACENT_TRANSPOSITION:
        for i in range(n - 1):
            if c[i].isdigit() and c[i + 1].isdigit() and c[i] != c[i + 1]:
                m = c[:]
                m[i], m[i + 1] = c[i + 1], c[i]
                out.append("".join(m))
    elif cls is ErrorClass.TWIN:
        for i in range(n - 1):
            if c[i].isdigit() and c[i] == c[i + 1]:
                for d in digits:
                    if d != c[i]:
                        m = c[:]
                        m[i] = m[i + 1] = d
                        out.append("".join(m))
    else:
        for i in range(n - 2):
            if c[i].isdigit() and c[i + 2].isdigit() and c[i] != c[i + 2]:
                m = c[:]
                m[i], m[i + 2] = c[i + 2], c[i]
                out.append("".join(m))
    return out


def analyse(validator: Validator, valid: str, cls: ErrorClass) -> Report:
    if not validator(valid):
        raise ValueError(f"the sample must validate before it is corrupted: {valid}")
    missed: list[str] = []
    tried = caught = 0
    for bad in corruptions(valid, cls):
        tried += 1
        if validator(bad):
            missed.append(bad)
        else:
            caught += 1
    return Report(cls, tried, caught, missed)


class SeededRandom:
    """Tiny 64-bit LCG, identical in all three languages, so the random payloads are the same everywhere."""

    MASK = (1 << 64) - 1

    def __init__(self, seed: int) -> None:
        self.state = seed & self.MASK

    def next_int(self, bound: int) -> int:
        self.state = (self.state * 6364136223846793005 + 1442695040888963407) & self.MASK
        return (self.state >> 33) % bound


def aggregate(validator: Validator, complete: Callable[[str], str], samples: int, payload_length: int, seed: int) -> dict[ErrorClass, tuple[int, int]]:
    """Aggregate (tried, caught) over many seeded random payloads so a single lucky sample cannot hide a weakness."""
    rng = SeededRandom(seed)
    totals = {cls: [0, 0] for cls in ErrorClass}
    for _ in range(samples):
        payload = "".join(str(rng.next_int(10)) for _ in range(payload_length))
        valid = complete(payload)
        for cls in ErrorClass:
            r = analyse(validator, valid, cls)
            totals[cls][0] += r.tried
            totals[cls][1] += r.caught
    return {cls: (t[0], t[1]) for cls, t in totals.items()}


PAN_PAYLOAD = "453909012223456"         # 15 digits; contains "90", "09", and "22" on purpose
IBAN_SAMPLE = "DE89370400440532013000"  # the ISO 13616 worked example


def pct(tried_caught: tuple[int, int]) -> str:
    tried, caught = tried_caught
    return f"{caught}/{tried} ({100.0 * caught / tried:.2f}%)"


def main() -> None:
    print("== Euclid: gcd, Bezout, inverse ==")
    print(f"gcd(1071, 462) = {gcd(1071, 462)}")
    bz = extended_gcd(240, 46)
    print(f"240 * ({bz.x}) + 46 * ({bz.y}) = {bz.g}")
    g = gcd(29, 1000)
    print(f"29/1000 reduced by gcd: {29 // g}/{1000 // g}")
    print(f"denominations {{6, 10}} reach amount 15? gcd={gcd(6, 10)} divides 15? {15 % gcd(6, 10) == 0}")
    inv = mod_inverse(100, 97)
    print(f"inverse of 100 mod 97 = {inv}  (100 * {inv} mod 97 = {100 * inv % 97})")
    try:
        mod_inverse(6, 10)
    except ArithmeticError as exc:
        print(f"rejected: {exc}")
    print(f"3^200 mod 97 = {mod_pow(3, 200, 97)} (by repeated squaring); 10^30 mod 97 = {mod_pow(10, 30, 97)}")

    print("\n== Luhn (ISO/IEC 7812) ==")
    pan = PAN_PAYLOAD + str(luhn_check_digit(PAN_PAYLOAD))
    print(f"payload {PAN_PAYLOAD} -> check digit {luhn_check_digit(PAN_PAYLOAD)} -> PAN {pan} valid={luhn_validate(pan)}")
    print(f"4539 1488 0343 6467 (a test PAN) valid={luhn_validate('4539148803436467')}; "
          f"with one digit changed valid={luhn_validate('4539148803436461')}")

    print("\n== ISO 7064 MOD 97-10 (IBAN) ==")
    print(f"{IBAN_SAMPLE} -> numeric {iban_to_numeric(IBAN_SAMPLE)}")
    print(f"mod 97 = {mod_of_decimal_string(iban_to_numeric(IBAN_SAMPLE), 97)}  valid={validate_iban(IBAN_SAMPLE)}")
    print(f"check digits recomputed for DE + 370400440532013000 = {iban_check_digits('DE', '370400440532013000')}")
    print(f"GB82 WEST 1234 5698 7654 32 valid={validate_iban('GB82 WEST 1234 5698 7654 32')}; "
          f"typo 5689 valid={validate_iban('GB82 WEST 1234 5689 7654 32')}")

    print("\n== Damm (quasigroup) ==")
    damm_id = PAN_PAYLOAD + str(damm_check_digit(PAN_PAYLOAD))
    print(f"572 -> check digit {damm_check_digit('572')}; {damm_id} valid={damm_validate(damm_id)}")

    print("\n== Which errors does each scheme catch? Exhaustive on one sample ==")
    print(f"{'error class':<24} {'Luhn':<10} {'MOD 97-10':<10} {'Damm':<10}")
    for cls in ErrorClass:
        print(f"{cls.name:<24} {analyse(luhn_validate, pan, cls).rate():<10} "
              f"{analyse(validate_iban, IBAN_SAMPLE, cls).rate():<10} {analyse(damm_validate, damm_id, cls).rate():<10}")
    print(f"Luhn misses (adjacent): {analyse(luhn_validate, pan, ErrorClass.ADJACENT_TRANSPOSITION).missed}")
    print(f"Luhn misses (twin)    : {analyse(luhn_validate, pan, ErrorClass.TWIN).missed}")

    print("\n== Aggregate over 200 seeded random 15-digit payloads (seed 7) ==")
    l = aggregate(luhn_validate, lambda p: p + str(luhn_check_digit(p)), 200, 15, 7)
    d = aggregate(damm_validate, lambda p: p + str(damm_check_digit(p)), 200, 15, 7)
    m = aggregate(validate_iban, lambda p: build_iban("XX", p), 200, 15, 7)
    print(f"{'error class':<24} {'Luhn':<22} {'MOD 97-10':<22} {'Damm':<22}")
    for cls in ErrorClass:
        print(f"{cls.name:<24} {pct(l[cls]):<22} {pct(m[cls]):<22} {pct(d[cls]):<22}")


if __name__ == "__main__":
    main()
