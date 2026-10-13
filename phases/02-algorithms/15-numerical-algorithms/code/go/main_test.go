// Tests for phases/02-algorithms/15-numerical-algorithms. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"
)

const (
	pan  = "4539090122234563"
	iban = "DE89370400440532013000"
)

func dammID() string { return fmt.Sprintf("%s%d", PanPayload, must(DammCheckDigit(PanPayload))) }

func TestGcdLcm(t *testing.T) {
	if Gcd(1071, 462) != 21 || Gcd(462, 1071) != 21 || Gcd(0, 5) != 5 || Gcd(0, 0) != 0 || Gcd(-12, 18) != 6 {
		t.Fatal("gcd")
	}
	if Lcm(4, 6) != 12 {
		t.Fatal("lcm")
	}
}

func TestBezout(t *testing.T) {
	b := ExtendedGcd(240, 46)
	if b.G != 2 || 240*b.X+46*b.Y != 2 || b.X != -9 || b.Y != 47 {
		t.Fatalf("bezout %+v", b)
	}
	b2 := ExtendedGcd(17, 5)
	if 17*b2.X+5*b2.Y != 1 {
		t.Fatalf("bezout coprime %+v", b2)
	}
}

func TestModInverse(t *testing.T) {
	if inv := must(ModInverse(100, 97)); inv != 65 || 100*inv%97 != 1 {
		t.Fatalf("inverse of 100 mod 97 = %d", inv)
	}
	if must(ModInverse(3, 11)) != 4 {
		t.Fatal("inverse of 3 mod 11")
	}
	if _, err := ModInverse(6, 10); !errors.Is(err, ErrNoInverse) {
		t.Fatalf("want ErrNoInverse, got %v", err)
	}
	if 15%Gcd(6, 10) == 0 || 16%Gcd(6, 10) != 0 {
		t.Fatal("coin-change reachability is a gcd question")
	}
}

func TestModular(t *testing.T) {
	if Mod(-7, 5) != 3 || Mod(7, 5) != 2 || Mod(-10, 5) != 0 {
		t.Fatal("mod sign")
	}
	if must(ModPow(3, 200, 97)) != 62 || must(ModPow(5, 0, 13)) != 1 {
		t.Fatal("modPow")
	}
	want := new(big.Int).Exp(big.NewInt(7), big.NewInt(123456), big.NewInt(1_000_003)).Int64()
	if must(ModPow(7, 123456, 1_000_003)) != want {
		t.Fatal("modPow vs math/big")
	}
	thirty := "370400440532013000131489123456"
	n, _ := new(big.Int).SetString(thirty, 10)
	if must(ModOfDecimalString(thirty, 97)) != new(big.Int).Mod(n, big.NewInt(97)).Int64() {
		t.Fatal("Horner mod vs math/big")
	}
	if _, err := ModOfDecimalString("12A4", 97); err == nil {
		t.Fatal("letters must be rejected")
	}
}

func TestLuhn(t *testing.T) {
	if must(LuhnCheckDigit("453909012223456")) != 3 || !LuhnValidate(pan) {
		t.Fatal("check digit / validate")
	}
	if !LuhnValidate("4539148803436467") || LuhnValidate("4539148803436461") {
		t.Fatal("known PAN")
	}
	if must(LuhnCheckDigit("7992739871")) != 3 || !LuhnValidate("79927398713") {
		t.Fatal("ISO example")
	}
	if LuhnValidate("4539-1488") || LuhnValidate("7") {
		t.Fatal("non-digits and short input")
	}
	if !LuhnValidate("4530990122234563") || !LuhnValidate("4539090155234563") {
		t.Fatal("known blind spots 09<->90 and 22<->55 should pass Luhn")
	}
}

func TestIban(t *testing.T) {
	if must(IbanToNumeric(iban)) != "370400440532013000131489" {
		t.Fatal("numeric rearrangement")
	}
	if !ValidateIban(iban) || !ValidateIban("gb82 west 1234 5698 7654 32") {
		t.Fatal("valid IBANs")
	}
	if ValidateIban("GB82 WEST 1234 5689 7654 32") || ValidateIban("DE89 3704 0044 0532 0130 0$") || ValidateIban("DE8") {
		t.Fatal("invalid IBANs")
	}
	if must(IbanCheckDigits("DE", "370400440532013000")) != "89" {
		t.Fatal("check digits")
	}
	built := must(BuildIban("GB", "WEST12345698765432"))
	if built != "GB82WEST12345698765432" || !ValidateIban(built) {
		t.Fatalf("build %s", built)
	}
	if len(must(IbanCheckDigits("XX", "000000000000001"))) != 2 {
		t.Fatal("zero padding")
	}
}

func TestDamm(t *testing.T) {
	for i := 0; i < 10; i++ {
		row, col := 0, 0
		for j := 0; j < 10; j++ {
			row |= 1 << DammTable[i][j]
			col |= 1 << DammTable[j][i]
		}
		if row != 0x3FF || col != 0x3FF || DammTable[i][i] != 0 {
			t.Fatalf("table row/col %d", i)
		}
	}
	if must(DammCheckDigit("572")) != 4 || !DammValidate("5724") {
		t.Fatal("572 -> 4")
	}
	if DammValidate("5725") || DammValidate("5734") || DammValidate("7524") || DammValidate("") || DammValidate("57a4") {
		t.Fatal("rejections")
	}
}

func TestLuhnErrorClasses(t *testing.T) {
	r := must(Analyse(LuhnValidate, pan, SingleDigit))
	if r.Tried != 144 || r.Caught != 144 {
		t.Fatalf("single digit %+v", r)
	}
	r = must(Analyse(LuhnValidate, pan, AdjacentTransposition))
	if r.Tried != 13 || r.MissedCount() != 3 {
		t.Fatalf("adjacent %+v", r)
	}
	r = must(Analyse(LuhnValidate, pan, Twin))
	if !reflect.DeepEqual(r.Missed, []string{"4539090155234563", "4539090125534563"}) {
		t.Fatalf("twin %+v", r)
	}
	r = must(Analyse(LuhnValidate, pan, JumpTransposition))
	if r.Tried != 11 || r.Caught != 0 {
		t.Fatalf("jump %+v", r)
	}
}

func TestMod97CatchesEverything(t *testing.T) {
	for _, cls := range AllErrorClasses {
		r := must(Analyse(ValidateIban, iban, cls))
		if r.Tried == 0 || r.MissedCount() != 0 {
			t.Fatalf("%v: %+v", cls, r)
		}
	}
}

func TestDammErrorClasses(t *testing.T) {
	id := dammID()
	if r := must(Analyse(DammValidate, id, SingleDigit)); r.Tried != 144 || r.Caught != 144 {
		t.Fatalf("single %+v", r)
	}
	if r := must(Analyse(DammValidate, id, AdjacentTransposition)); r.Tried != 13 || r.Caught != 13 {
		t.Fatalf("adjacent %+v", r)
	}
	if must(Analyse(DammValidate, id, Twin)).MissedCount() == 0 {
		t.Fatal("Damm does not promise twin errors")
	}
}

func TestCorruptionsAndGuard(t *testing.T) {
	if len(Corruptions(iban, SingleDigit)) != 20*9 {
		t.Fatal("corruptions must skip letters")
	}
	if _, err := Analyse(LuhnValidate, "4539090122234560", SingleDigit); err == nil {
		t.Fatal("analyse must refuse an invalid sample")
	}
}

func TestSeededAggregate(t *testing.T) {
	agg := must(Aggregate(LuhnValidate, func(p string) string { return fmt.Sprintf("%s%d", p, must(LuhnCheckDigit(p))) }, 50, 15, 7))
	if agg[SingleDigit] != [2]int{7200, 7200} {
		t.Fatalf("single %v", agg[SingleDigit])
	}
	if agg[AdjacentTransposition][1] >= agg[AdjacentTransposition][0] {
		t.Fatal("Luhn must miss some adjacent transpositions")
	}
	if agg[JumpTransposition][1] != 0 || agg[JumpTransposition][0] == 0 {
		t.Fatal("Luhn catches no jump transpositions")
	}
	rng := NewSeededRandom(7)
	if a, b, c := rng.NextInt(10), rng.NextInt(10), rng.NextInt(10); a != 8 || b != 1 || c != 3 {
		t.Fatalf("seeded values %d %d %d", a, b, c)
	}
}
