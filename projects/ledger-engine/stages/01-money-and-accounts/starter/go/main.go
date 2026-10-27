// Project: projects/ledger-engine — workspace entry point (Go). Grows with every stage; the grader never runs it.
// Run:  go run .
// Grade: python3 scripts/project_test.py ledger-engine --lang go --stage 1 --path <workspace>
// Go 1.22+, standard library only.
package main

import "fmt"

func main() {
	fmt.Println("== Ledger Engine workspace ==")
	fmt.Printf("given: %v, %v\n", OfMinor(1234, USD), OfMinor(1250, KWD))
	defer func() {
		// Every stub panics with "not implemented: TODO stage 01 ..." until you replace it; recover so the
		// workspace still exits 0 and tells you which function to write next.
		if todo := recover(); todo != nil {
			fmt.Println("stage 1 not finished yet:", todo)
		}
	}()
	fmt.Println("not implemented yet: stage 1 (Money.Parse, Times, AccountRegistry.Open ...)")
	hundred, err := Parse("100.00", USD)
	if err != nil {
		panic(err)
	}
	fee, err := hundred.Times(29, 1000, HalfEven)
	if err != nil {
		panic(err)
	}
	chart := NewAccountRegistry()
	bank, err := chart.Open("bank", Asset, USD)
	if err != nil {
		panic(err)
	}
	fmt.Printf("fee %v; %s grows on %v\n", fee, bank.ID, bank.NormalSide())
}
