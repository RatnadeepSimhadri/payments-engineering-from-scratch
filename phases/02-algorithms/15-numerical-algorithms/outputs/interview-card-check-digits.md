---
name: interview-card-check-digits
description: Interview card on check digits and modular arithmetic for payments engineers, with model answers and the follow-ups that separate implementation from understanding
phase: 2
lesson: 15
tags: [interview, check-digits, luhn, iban, modular-arithmetic]
---

# Interview Card: Check Digits and Modular Arithmetic

For interviewing payments engineers, or for interviewing yourself before designing an identifier. Each question has a model answer and a follow-up. A strong candidate reaches the follow-up unprompted.

## 1. What does a check digit protect against, and what does it not?

**Model answer.** Accidental keying and transcription errors: single-digit substitutions, adjacent transpositions, and (depending on scheme) twin errors and jump transpositions. It does not protect against an adversary, who can compute a valid check digit for any payload, and it does not establish that the identifier exists.

**Follow-up.** "How would you protect the identifier against an adversary instead?" Expect an HMAC or signature, and the observation that it is a different threat model with a different cost.

## 2. Walk me through Luhn on 7992739871 and tell me the check digit.

**Model answer.** From the right, double every second digit (starting with the rightmost payload digit), subtract 9 from any doubled product over 9, sum everything: 67. The check digit is `(10 - 67 mod 10) mod 10 = 3`, giving 79927398713.

**Follow-up.** "Why subtract 9 rather than summing the digits of the product?" Because for a two-digit product `10 + k`, the digit sum is `1 + k = (10 + k) - 9`; same thing, one operation. "Why does that make the doubled map a permutation?" Because 0..9 map to 0,2,4,6,8,1,3,5,7,9.

## 3. Which errors does Luhn miss, and why?

**Model answer.** The adjacent transposition 09 <-> 90, because `f(d) - d` (doubled minus undoubled contribution) is zero for both 0 and 9 and for nothing else. The twin errors 22<->55, 33<->66, 44<->77, because `f(d) + d mod 10` collides for those pairs. Every jump transposition (digits two apart), because the two positions carry the same weight.

**Follow-up.** "How would you demonstrate this to a sceptical colleague?" Enumerate every error of each class on a sample number and count; do not sample randomly, because a random single-digit test shows 100% and hides the transposition gaps.

## 4. How does the IBAN check work, and why does it need two digits?

**Model answer.** Move the first four characters to the end, convert letters to two digits each (A=10..Z=35), and require the resulting number to be 1 mod 97. Because 97 is prime and larger than any single-position change (`k * 10^i`, `9 * 10^i`, `11 * 10^i`, `99 * 10^i` for the four error classes), every one of those changes alters the remainder. Two digits are needed because the remainder has 97 possible values.

**Follow-up.** "The number is 24 digits. How do you compute it mod 97 in a 64-bit integer?" Horner's rule: reduce after each digit; the running value never exceeds 979. "Why are 00, 01, and 99 never valid IBAN check digits?" Because the check is `98 - r` with `r` in 0..96.

## 5. Explain the extended Euclidean algorithm and one place you would use the modular inverse it gives you.

**Model answer.** Carry coefficient pairs alongside the remainders so that each remainder is expressed as `a*x + b*y`; when the remainder reaches zero the previous one is the gcd and `a*x + b*y = gcd`. If the gcd is 1, `x mod m` is the inverse of `a` mod `m`. Uses: solving for a check character whose position weight is not 1 (ISO 7064 pure systems), RSA private-key derivation, and reducing a fee ratio to lowest terms via the gcd itself.

**Follow-up.** "What does `gcd` tell you about coin change?" An amount is reachable with unlimited repeats only if the gcd of the denominations divides it; the Bezout coefficients are a (possibly negative) witness, so the test is necessary, not sufficient.

## 6. You need a single check digit that catches all single-digit errors and all adjacent transpositions. What do you use and why not Luhn?

**Model answer.** Damm or Verhoeff. Both fold the digits through a table with the quasigroup (Latin square) property, which guarantees single-digit detection, and total anti-symmetry, which guarantees adjacent-transposition detection. Luhn cannot do this in one digit because its weighted sum has the 09/90 collision built into the arithmetic.

**Follow-up.** "You transcribed the Damm table from a web page. How do you know it is right?" Test the properties directly: every row and column is a permutation, the diagonal is zero, and the anti-symmetry inequality holds for all `c, x != y`. A single swapped entry silently removes the guarantees.

## Scoring

| Level | Signals |
|-------|---------|
| Developing | Can implement Luhn from memory; believes it catches "all typos"; has not heard of mod 97 or Damm |
| Solid | Knows the 09/90 and twin blind spots and why; can derive the IBAN check and Horner's rule; knows when to reach for Damm or Verhoeff |
| Exemplary | Reasons from the arithmetic to the error classes for an unfamiliar scheme; proposes exhaustive enumeration as the test; connects gcd and inverses to coin change and cryptography unprompted |
