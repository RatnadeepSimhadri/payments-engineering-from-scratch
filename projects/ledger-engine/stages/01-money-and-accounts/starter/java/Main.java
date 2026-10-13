// Project: projects/ledger-engine — workspace entry point. Grows with every stage; the grader never runs it.
// Run:  javac -d .out *.java && java -cp .out Main
// Grade: python3 scripts/project_test.py ledger-engine --lang java --stage 1 --path <workspace>
// Java 21, standard library only.

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Ledger Engine workspace ==");
        System.out.println("given: " + Money.ofMinor(1234, Currency.USD) + ", " + Money.ofMinor(1250, Currency.KWD));
        try {
            Money fee = Money.parse("100.00", Currency.USD).times(29, 1000, Rounding.HALF_EVEN);
            AccountRegistry chart = new AccountRegistry();
            Account bank = chart.open("bank", AccountType.ASSET, Currency.USD);
            System.out.println("fee " + fee + "; " + bank.id() + " grows on " + bank.normalSide());
        } catch (UnsupportedOperationException todo) {
            System.out.println("stage 1 not finished yet: " + todo.getMessage());
        }
    }
}
