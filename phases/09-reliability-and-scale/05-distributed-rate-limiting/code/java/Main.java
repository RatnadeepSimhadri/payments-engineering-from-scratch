// Lesson: phases/09-reliability-and-scale/05-distributed-rate-limiting/docs/en.md
// Distributed Rate Limiting
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only.

public final class Main {
    static int placeholder(int x) {
        return x; // TODO: replace with the lesson's implementation
    }

    public static void main(String[] args) {
        System.out.println("Distributed Rate Limiting: " + placeholder(42));
    }
}
