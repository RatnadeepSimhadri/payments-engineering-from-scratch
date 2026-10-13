// Lesson: phases/05-distributed-foundations/07-rpc-and-serialization/docs/en.md
// RPC, Framing, and Serialization Over Raw Sockets
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only.

public final class Main {
    static int placeholder(int x) {
        return x; // TODO: replace with the lesson's implementation
    }

    public static void main(String[] args) {
        System.out.println("RPC, Framing, and Serialization Over Raw Sockets: " + placeholder(42));
    }
}
