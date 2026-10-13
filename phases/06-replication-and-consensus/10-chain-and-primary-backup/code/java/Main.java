// Lesson: phases/06-replication-and-consensus/10-chain-and-primary-backup/docs/en.md
// Chain Replication and Primary-Backup
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: van Renesse & Schneider, "Chain Replication for Supporting High Throughput and Availability" (OSDI 2004), sections 2-3;
//          Alsberg & Day, "A Principle for Resilient Sharing of Distributed Resources" (ICSE 1976) for primary-backup;
//          Budhiraja, Marzullo, Schneider, Toueg, "The Primary-Backup Approach" (1993).
// Java 21, standard library only. Deterministic simulated network; a simulated master plays the failure detector and reconfigurer.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;

// ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() { long s = 1; for (int i = 0; i < exponent; i++) s *= 10; return s; }
}

record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return sign + whole + "." + String.format("%0" + currency.exponent() + "d", frac) + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------- deterministic infrastructure

final class Rng {
    private long state;
    Rng(long seed) { this.state = seed; }

    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    int nextInt(int bound) { return (int) ((nextLong() >>> 33) % bound); }
}

/** One client request: a balance change (UPDATE) or a read (QUERY). (clientId, reqId) identifies it for retries and deduplication. */
record Request(int clientId, long reqId, boolean isQuery, String account, Money delta) {
    String key() { return clientId + ":" + reqId; }
    @Override public String toString() { return (isQuery ? "Q" : "U") + key() + "(" + account + (isQuery ? "" : "," + delta) + ")"; }
}

/** An update as it lives in a replica's history: the request plus the sequence number the head assigned. */
record Update(long seq, Request request) {}

enum MessageKind { UPDATE, ACK, QUERY, REPLY, NOT_TAIL, SYNC, STATE_TRANSFER, SET_LINKS, PB_UPDATE, PB_ACK, PB_SYNC }

/** One message on the wire. Fields are used per kind; unused ones are null or zero. */
record Message(MessageKind kind, int from, int to, Update update, List<Update> updates, Request request, Money balance,
               int predecessor, int successor, boolean isHead, boolean isTail, String note) {
    static Message update(int from, int to, Update u) { return new Message(MessageKind.UPDATE, from, to, u, null, null, null, 0, 0, false, false, null); }
    static Message ack(int from, int to, Update u) { return new Message(MessageKind.ACK, from, to, u, null, null, null, 0, 0, false, false, null); }
    static Message query(int from, int to, Request r) { return new Message(MessageKind.QUERY, from, to, null, null, r, null, 0, 0, false, false, null); }
    static Message reply(int from, int to, Request r, Money balance) { return new Message(MessageKind.REPLY, from, to, null, null, r, balance, 0, 0, false, false, null); }
    static Message notTail(int from, int to, Request r) { return new Message(MessageKind.NOT_TAIL, from, to, null, null, r, null, 0, 0, false, false, null); }
    static Message sync(int from, int to, List<Update> us) { return new Message(MessageKind.SYNC, from, to, null, List.copyOf(us), null, null, 0, 0, false, false, null); }
    static Message stateTransfer(int from, int to, List<Update> history) { return new Message(MessageKind.STATE_TRANSFER, from, to, null, List.copyOf(history), null, null, 0, 0, false, false, null); }
    static Message setLinks(int to, int pred, int succ, String note) { return new Message(MessageKind.SET_LINKS, -1, to, null, null, null, null, pred, succ, pred < 0, succ < 0, note); }
    static Message pbUpdate(int from, int to, Update u) { return new Message(MessageKind.PB_UPDATE, from, to, u, null, null, null, 0, 0, false, false, null); }
    static Message pbAck(int from, int to, Update u) { return new Message(MessageKind.PB_ACK, from, to, u, null, null, null, 0, 0, false, false, null); }
    static Message pbSync(int from, int to, List<Update> history) { return new Message(MessageKind.PB_SYNC, from, to, null, List.copyOf(history), null, null, 0, 0, false, false, null); }
}

interface Node {
    int id();
    List<Message> handle(Message m, int now);
}

/** Seeded delays, crashes, FIFO per (from, to) pair. Chain replication assumes reliable FIFO links, so there are no drops. */
final class Network {
    record Envelope(int deliverAt, long seq, Message message) {}

    private final Rng rng;
    private final int maxDelay;
    private final Map<Integer, Node> nodes = new TreeMap<>();
    private final PriorityQueue<Envelope> inFlight = new PriorityQueue<>(Comparator.comparingInt(Envelope::deliverAt).thenComparingLong(Envelope::seq));
    private final Map<Long, Integer> lastDeliveryPerLink = new HashMap<>();
    private final Set<Integer> down = new HashSet<>();
    private Runnable observer = () -> {};
    private int now = 0;
    private long seq = 0;
    int sent = 0, delivered = 0, dropped = 0;

    Network(long seed, int maxDelay) { this.rng = new Rng(seed); this.maxDelay = Math.max(1, maxDelay); }

    void add(Node n) { nodes.put(n.id(), n); }
    void setObserver(Runnable r) { observer = r; }
    int now() { return now; }
    boolean isDown(int id) { return down.contains(id); }
    void crash(int id) { down.add(id); }
    boolean idle() { return inFlight.isEmpty(); }

    /** FIFO per link: a message never arrives before an earlier one on the same (from, to) pair, which the chain protocol relies on. */
    void send(Message m) {
        sent++;
        if (down.contains(m.to()) || (m.from() >= 0 && down.contains(m.from()))) { dropped++; return; }
        long link = ((long) m.from() << 32) ^ (m.to() & 0xFFFFFFFFL);
        int earliest = Math.max(now + 1, lastDeliveryPerLink.getOrDefault(link, 0));
        int at = earliest + rng.nextInt(maxDelay);
        lastDeliveryPerLink.put(link, at);
        inFlight.add(new Envelope(at, seq++, m));
    }

    void sendAll(List<Message> ms) { for (Message m : ms) send(m); }

    void tick() {
        now++;
        while (!inFlight.isEmpty() && inFlight.peek().deliverAt() <= now) {
            Message m = inFlight.poll().message();
            if (down.contains(m.to())) { dropped++; continue; }
            delivered++;
            sendAll(nodes.get(m.to()).handle(m, now));
            observer.run();
        }
    }
}

// ---------------------------------------------------------------------------------------------- chain replication (van Renesse & Schneider)

/**
 * One replica in a chain. State: account balances, the history of applied updates (Hist_i), and the updates forwarded to the
 * successor but not yet acknowledged by the tail (Sent_i). The two invariants the paper proves and this lesson checks:
 *   Update Propagation: for i before j in the chain, Hist_j is a prefix of Hist_i.
 *   Inprocess Requests:  Hist_i minus Hist_(i+1) is contained in Sent_i.
 */
final class ChainNode implements Node {
    final int id;
    int predecessor = -1, successor = -1;
    final Map<String, Money> balances = new TreeMap<>();
    final List<Update> history = new ArrayList<>();
    final Set<Long> seqsSeen = new HashSet<>();
    final Set<String> requestsSeen = new HashSet<>();
    final LinkedHashMap<Long, Update> sent = new LinkedHashMap<>();
    /** The balance each update's reply carried, so a retried request that is already committed can be answered without re-applying. */
    final Map<String, Money> replyValues = new HashMap<>();
    /** True on an old tail while its history is still being copied to a new tail: it keeps answering as the tail until then. */
    boolean transferPending = false;
    /** False for a spare or a joining replica whose history has not arrived yet; such a node is not part of the chain's invariants. */
    boolean inChain = false;
    long nextSeq = 1;
    int updatesHandled = 0, queriesHandled = 0, acksHandled = 0, messagesHandled = 0;

    ChainNode(int id) { this.id = id; }

    @Override public int id() { return id; }
    boolean isHead() { return predecessor < 0; }
    boolean isTail() { return successor < 0; }
    /** Who answers clients: the real tail, or an old tail whose successor has not finished receiving the history yet. */
    boolean actsAsTail() { return isTail() || transferPending; }
    Money balance(String account) { return balances.getOrDefault(account, Money.ofMinor(0, Currency.USD)); }

    private void apply(Update u) {
        history.add(u);
        seqsSeen.add(u.seq());
        requestsSeen.add(u.request().key());
        balances.put(u.request().account(), balance(u.request().account()).plus(u.request().delta()));
        replyValues.put(u.request().key(), balance(u.request().account()));
        nextSeq = Math.max(nextSeq, u.seq() + 1);
    }

    @Override
    public List<Message> handle(Message m, int now) {
        messagesHandled++;
        List<Message> out = new ArrayList<>();
        switch (m.kind()) {
            case UPDATE -> {
                updatesHandled++;
                Update u = m.update();
                if (m.from() >= 100 && !isHead()) return out;                          // stale client view: only the head accepts updates; the client retries
                if (isHead() && m.from() >= 100) {                                     // from a client: assign the sequence number
                    if (requestsSeen.contains(u.request().key())) {                    // duplicate retry: never apply twice
                        boolean stillInFlight = false;
                        for (Update s : sent.values()) if (s.request().key().equals(u.request().key())) stillInFlight = true;
                        if (!stillInFlight) out.add(Message.reply(id, m.from(), u.request(), replyValues.get(u.request().key())));   // committed: answer from the cache
                        return out;                                                    // otherwise the tail's reply is on its way
                    }
                    u = new Update(nextSeq, u.request());
                }
                if (seqsSeen.contains(u.seq())) return out;                            // already have it (resent after a reconfiguration)
                apply(u);
                out.addAll(forwardOrCommit(u));
            }
            case ACK -> {
                acksHandled++;
                if (m.update().seq() < 0) { transferPending = false; return out; }    // the new tail has the history: hand over
                if (sent.remove(m.update().seq()) != null && !isHead()) out.add(Message.ack(id, predecessor, m.update()));
            }
            case QUERY -> {
                queriesHandled++;
                if (actsAsTail()) out.add(Message.reply(id, m.from(), m.request(), balance(m.request().account())));
                else out.add(Message.notTail(id, m.from(), m.request()));             // a stale client view; it must re-ask the master
            }
            case SYNC -> {                                                             // predecessor resends Sent after a reconfiguration
                for (Update u : m.updates()) {
                    if (seqsSeen.contains(u.seq())) { out.add(Message.ack(id, m.from(), u)); continue; }   // we have it; the ack may have been lost with the dead node
                    apply(u);
                    out.addAll(forwardOrCommit(u));
                }
            }
            case STATE_TRANSFER -> {                                                   // joining at the tail: adopt the old tail's history
                for (Update u : m.updates()) if (!seqsSeen.contains(u.seq())) apply(u);
                inChain = true;
                out.add(Message.ack(id, m.from(), new Update(-1, null)));              // seq -1: "history received", the old tail stops acting as tail
            }
            case SET_LINKS -> {
                boolean wasTail = isTail();
                predecessor = m.predecessor();
                successor = m.successor();
                if ("extend-new".equals(m.note())) inChain = false; else inChain = true;   // a joining tail is in the chain once its history arrives
                if (isTail() && !wasTail) {                                           // the tail failed: everything we had in flight is now committed
                    for (Update u : new ArrayList<>(sent.values())) {
                        out.add(Message.reply(id, u.request().clientId(), u.request(), balance(u.request().account())));
                        if (!isHead()) out.add(Message.ack(id, predecessor, u));
                    }
                    sent.clear();
                } else if (!isTail()) {                                               // new successor: resend what it may be missing
                    if (wasTail && m.note() != null && m.note().startsWith("extend")) {
                        transferPending = true;                                        // keep answering clients until the copy lands
                        out.add(Message.stateTransfer(id, successor, history));
                    } else if (!sent.isEmpty()) {
                        out.add(Message.sync(id, successor, new ArrayList<>(sent.values())));
                    }
                }
            }
            default -> throw new IllegalStateException("chain node got " + m.kind());
        }
        return out;
    }

    private List<Message> forwardOrCommit(Update u) {
        List<Message> out = new ArrayList<>();
        if (actsAsTail()) {
            out.add(Message.reply(id, u.request().clientId(), u.request(), balance(u.request().account())));
            if (isTail() && !isHead()) out.add(Message.ack(id, predecessor, u));
        }
        if (!isTail()) {
            sent.put(u.seq(), u);
            out.add(Message.update(id, successor, u));
        }
        return out;
    }
}

/**
 * The master: a reliable (in the paper, itself replicated) service that detects failures and tells nodes who their
 * neighbours are. Here it is a plain object the test drives: fail(id), extend(id). Clients ask it for the head and tail.
 */
final class Master {
    final List<Integer> chain = new ArrayList<>();
    final Network network;
    final List<String> events = new ArrayList<>();
    int reconfigurations = 0;

    Master(Network network, List<Integer> initialChain) {
        this.network = network;
        chain.addAll(initialChain);
    }

    int head() { return chain.get(0); }
    int tail() { return chain.get(chain.size() - 1); }
    int length() { return chain.size(); }

    private int pred(int i) { return i == 0 ? -1 : chain.get(i - 1); }
    private int succ(int i) { return i == chain.size() - 1 ? -1 : chain.get(i + 1); }

    /** Push every node's links. Deterministic order: tail first so a new tail starts replying before its predecessor forwards. */
    void publishLinks(String note) {
        reconfigurations++;
        for (int i = chain.size() - 1; i >= 0; i--) {
            String n = note.equals("extend") && i == chain.size() - 1 ? "extend-new" : note;
            network.send(Message.setLinks(chain.get(i), pred(i), succ(i), n));
        }
    }

    /** The failure detector fired for `id`. Section 3 of the paper: head, tail, and middle failures each need a different repair. */
    void fail(int id) {
        int i = chain.indexOf(id);
        if (i < 0) return;
        String kind = i == 0 ? "head" : i == chain.size() - 1 ? "tail" : "middle";
        chain.remove(i);
        if (chain.isEmpty()) throw new IllegalStateException("chain has no replicas left");
        events.add(String.format("t=%-4d master: n%d (%s) failed -> chain %s", network.now(), id, kind, chain));
        publishLinks("fail " + kind);
    }

    /** Add a fresh replica at the tail. The old tail transfers its history; until the transfer lands the old tail still answers queries. */
    void extend(int id) {
        chain.add(id);
        events.add(String.format("t=%-4d master: extend chain with n%d at the tail -> %s", network.now(), id, chain));
        publishLinks("extend");
    }
}

/** A client: sends updates to the head and queries to the tail, as the master currently reports them; retries on timeout. */
final class ChainClient implements Node {
    final int id;
    final Master master;
    final Map<String, Integer> pendingSentAt = new HashMap<>();
    final Map<String, Request> pending = new LinkedHashMap<>();
    final Map<String, Money> replies = new LinkedHashMap<>();
    final List<String> repliesInOrder = new ArrayList<>();
    int retries = 0, notTailRedirects = 0;
    long nextReq = 1;

    ChainClient(int id, Master master) { this.id = id; this.master = master; }

    @Override public int id() { return id; }

    Request update(String account, Money delta, int now, List<Message> out) {
        Request r = new Request(id, nextReq++, false, account, delta);
        pending.put(r.key(), r); pendingSentAt.put(r.key(), now);
        out.add(Message.update(id, master.head(), new Update(0, r)));
        return r;
    }

    Request query(String account, int now, List<Message> out) {
        Request r = new Request(id, nextReq++, true, account, null);
        pending.put(r.key(), r); pendingSentAt.put(r.key(), now);
        out.add(Message.query(id, master.tail(), r));
        return r;
    }

    /** Resend anything outstanding longer than `timeout`, to whoever the master now says is head/tail. */
    List<Message> retryStale(int now, int timeout) {
        List<Message> out = new ArrayList<>();
        for (Request r : new ArrayList<>(pending.values())) {
            if (now - pendingSentAt.get(r.key()) >= timeout) {
                retries++;
                pendingSentAt.put(r.key(), now);
                out.add(r.isQuery() ? Message.query(id, master.tail(), r) : Message.update(id, master.head(), new Update(0, r)));
            }
        }
        return out;
    }

    @Override
    public List<Message> handle(Message m, int now) {
        List<Message> out = new ArrayList<>();
        switch (m.kind()) {
            case REPLY -> {
                if (pending.remove(m.request().key()) != null) {                       // duplicates after a tail failover are ignored
                    replies.put(m.request().key(), m.balance());
                    repliesInOrder.add(m.request().key());
                    pendingSentAt.remove(m.request().key());
                }
            }
            case NOT_TAIL -> { notTailRedirects++; out.add(Message.query(id, master.tail(), m.request())); }
            default -> throw new IllegalStateException("client got " + m.kind());
        }
        return out;
    }

    boolean hasReply(Request r) { return replies.containsKey(r.key()); }
    Money replyFor(Request r) { return replies.get(r.key()); }
}

// ---------------------------------------------------------------------------------------------- primary-backup, for comparison

/**
 * Primary-backup with synchronous replication: the primary applies an update, sends it to every backup, waits for all acks,
 * then replies. Queries go to the primary. On promotion the new primary pushes its history to the backups, because the
 * old primary may have reached some backups and not others with its last updates.
 */
final class PrimaryBackupNode implements Node {
    final int id;
    boolean isPrimary = false;
    final List<Integer> backups = new ArrayList<>();
    final Map<String, Money> balances = new TreeMap<>();
    final List<Update> history = new ArrayList<>();
    final Set<Long> seqsSeen = new HashSet<>();
    final Set<String> requestsSeen = new HashSet<>();
    final Map<Long, Set<Integer>> awaitingAcks = new HashMap<>();
    final Map<Long, Update> awaitingUpdate = new HashMap<>();
    long nextSeq = 1;
    int updatesHandled = 0, queriesHandled = 0, acksHandled = 0, messagesHandled = 0;

    PrimaryBackupNode(int id) { this.id = id; }

    @Override public int id() { return id; }
    Money balance(String account) { return balances.getOrDefault(account, Money.ofMinor(0, Currency.USD)); }

    private void apply(Update u) {
        history.add(u); seqsSeen.add(u.seq()); requestsSeen.add(u.request().key());
        balances.put(u.request().account(), balance(u.request().account()).plus(u.request().delta()));
        nextSeq = Math.max(nextSeq, u.seq() + 1);
    }

    @Override
    public List<Message> handle(Message m, int now) {
        messagesHandled++;
        List<Message> out = new ArrayList<>();
        switch (m.kind()) {
            case UPDATE -> {                                                           // from a client
                updatesHandled++;
                if (!isPrimary) return out;                                            // stale client view: silence, the client retries
                if (requestsSeen.contains(m.update().request().key())) { out.add(Message.reply(id, m.from(), m.update().request(), balance(m.update().request().account()))); return out; }
                Update u = new Update(nextSeq, m.update().request());
                apply(u);
                if (backups.isEmpty()) { out.add(Message.reply(id, m.from(), u.request(), balance(u.request().account()))); return out; }
                awaitingAcks.put(u.seq(), new HashSet<>(backups));
                awaitingUpdate.put(u.seq(), u);
                for (int b : backups) out.add(Message.pbUpdate(id, b, u));
            }
            case PB_UPDATE -> {
                updatesHandled++;
                if (!seqsSeen.contains(m.update().seq())) apply(m.update());
                out.add(Message.pbAck(id, m.from(), m.update()));
            }
            case PB_ACK -> {
                acksHandled++;
                Set<Integer> waiting = awaitingAcks.get(m.update().seq());
                if (waiting == null) return out;
                waiting.remove(m.from());
                if (waiting.isEmpty()) {
                    awaitingAcks.remove(m.update().seq());
                    Update u = awaitingUpdate.remove(m.update().seq());
                    out.add(Message.reply(id, u.request().clientId(), u.request(), balance(u.request().account())));
                }
            }
            case QUERY -> {
                queriesHandled++;
                if (isPrimary) out.add(Message.reply(id, m.from(), m.request(), balance(m.request().account())));
                else out.add(Message.notTail(id, m.from(), m.request()));
            }
            case PB_SYNC -> { for (Update u : m.updates()) if (!seqsSeen.contains(u.seq())) apply(u); out.add(Message.pbAck(id, m.from(), new Update(-1, null))); }
            default -> throw new IllegalStateException("primary-backup node got " + m.kind());
        }
        return out;
    }

    /** Called by the master on promotion: become primary over `newBackups` and re-sync them from my history. */
    List<Message> promote(List<Integer> newBackups) {
        isPrimary = true;
        backups.clear(); backups.addAll(newBackups);
        List<Message> out = new ArrayList<>();
        for (int b : backups) out.add(Message.pbSync(id, b, history));
        return out;
    }
}

/** Master for the primary-backup group: holds the member list, promotes on failure. Clients read head() and tail() from it too. */
final class PrimaryBackupMaster {
    final List<Integer> members = new ArrayList<>();
    final Network network;
    final Map<Integer, PrimaryBackupNode> nodes;
    final List<String> events = new ArrayList<>();

    PrimaryBackupMaster(Network network, Map<Integer, PrimaryBackupNode> nodes, List<Integer> members) {
        this.network = network; this.nodes = nodes; this.members.addAll(members);
        network.sendAll(nodes.get(members.get(0)).promote(members.subList(1, members.size())));
    }

    int primary() { return members.get(0); }

    void fail(int id) {
        if (!members.remove(Integer.valueOf(id))) return;
        events.add(String.format("t=%-4d pb-master: n%d failed -> primary n%d, backups %s", network.now(), id, members.get(0), members.subList(1, members.size())));
        network.sendAll(nodes.get(members.get(0)).promote(members.subList(1, members.size())));
    }
}

// ---------------------------------------------------------------------------------------------- the simulation harness

/** A chain, its master, its clients, and the invariant checker, on one network. */
final class ChainCluster {
    final Network network;
    final Master master;
    final Map<Integer, ChainNode> nodes = new TreeMap<>();
    final Map<Integer, ChainClient> clients = new TreeMap<>();
    final List<String> violations = new ArrayList<>();
    int checks = 0;

    ChainCluster(long seed, int maxDelay, int chainLength, int spares, int clientCount) {
        network = new Network(seed, maxDelay);
        List<Integer> initial = new ArrayList<>();
        for (int i = 0; i < chainLength + spares; i++) {
            ChainNode n = new ChainNode(i);
            nodes.put(i, n);
            network.add(n);
            if (i < chainLength) initial.add(i);
        }
        master = new Master(network, initial);
        for (int c = 0; c < clientCount; c++) {
            ChainClient cl = new ChainClient(100 + c, master);
            clients.put(cl.id, cl);
            network.add(cl);
        }
        master.publishLinks("initial");
        network.setObserver(this::checkInvariants);
        run(maxDelay + 2);
    }

    ChainNode node(int id) { return nodes.get(id); }
    ChainClient client(int i) { return clients.get(100 + i); }
    int now() { return network.now(); }
    void tick() { network.tick(); }
    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    /** Run until no client has a pending request (retrying stale ones every `timeout` ticks) or maxTicks pass. */
    int runUntilQuiet(int maxTicks, int timeout) {
        for (int i = 0; i < maxTicks; i++) {
            tick();
            for (ChainClient c : clients.values()) network.sendAll(c.retryStale(now(), timeout));
            boolean quiet = network.idle();
            for (ChainClient c : clients.values()) if (!c.pending.isEmpty()) quiet = false;
            if (quiet) return now();
        }
        return -1;
    }

    Request update(int client, String account, long cents) { List<Message> out = new ArrayList<>(); Request r = client(client).update(account, Money.ofMinor(cents, Currency.USD), now(), out); network.sendAll(out); return r; }
    Request query(int client, String account) { List<Message> out = new ArrayList<>(); Request r = client(client).query(account, now(), out); network.sendAll(out); return r; }

    void crash(int id) { network.crash(id); }
    /** The failure detector reports after `detectionDelay` ticks; until then the chain is broken and clients wait. */
    void crashAndDetect(int id, int detectionDelay) { crash(id); run(detectionDelay); master.fail(id); }

    /** Update Propagation and Inprocess Requests invariants, over the live chain, after every delivery. */
    void checkInvariants() {
        checks++;
        List<Integer> chain = master.chain;
        for (int i = 0; i + 1 < chain.size(); i++) {
            ChainNode pred = nodes.get(chain.get(i)), succ = nodes.get(chain.get(i + 1));
            if (network.isDown(pred.id) || network.isDown(succ.id) || pred.transferPending || !succ.inChain) continue;   // a joining tail is not in the chain yet
            if (succ.history.size() > pred.history.size()) { violations.add("UPDATE PROPAGATION: n" + succ.id + " longer than n" + pred.id); return; }
            for (int k = 0; k < succ.history.size(); k++) {
                if (!succ.history.get(k).equals(pred.history.get(k))) { violations.add("UPDATE PROPAGATION: n" + succ.id + " differs from n" + pred.id + " at " + k); return; }
            }
            for (int k = succ.history.size(); k < pred.history.size(); k++) {
                Update u = pred.history.get(k);
                if (!pred.sent.containsKey(u.seq())) { violations.add("INPROCESS REQUESTS: n" + pred.id + " has seq " + u.seq() + " that n" + succ.id + " lacks, and it is not in Sent"); return; }
            }
        }
    }

    ChainNode tail() { return nodes.get(master.tail()); }
    ChainNode head() { return nodes.get(master.head()); }
}

public final class Main {
    static void printChain(ChainCluster c) {
        for (int id : c.master.chain) {
            ChainNode n = c.node(id);
            System.out.printf("  n%d %-6s hist=%d sent=%d balances=%s%n", id, n.isHead() ? "HEAD" : n.isTail() ? "TAIL" : "middle", n.history.size(), n.sent.size(), n.balances);
        }
    }

    public static void main(String[] args) {
        System.out.println("== A 3-replica chain: updates enter at the head, replies come from the tail (seed 1, delay 1-3) ==");
        ChainCluster c = new ChainCluster(1, 3, 3, 1, 2);
        Request u1 = c.update(0, "alice", 10_000);
        Request u2 = c.update(0, "alice", -2_550);
        Request q1 = c.query(1, "alice");                     // sent at the same instant as the updates
        c.runUntilQuiet(500, 60);
        System.out.println("client 0: Credit alice 100.00 -> reply " + c.client(0).replyFor(u1) + "; Debit 25.50 -> reply " + c.client(0).replyFor(u2));
        System.out.println("client 1: Query alice issued at t=0 -> " + c.client(1).replyFor(q1) + " (the tail had not yet seen the updates: reads show only committed state)");
        Request q2 = c.query(1, "alice");
        c.runUntilQuiet(500, 60);
        System.out.println("client 1: Query alice after the replies -> " + c.client(1).replyFor(q2));
        printChain(c);

        System.out.println();
        System.out.println("== A read at the tail never shows an update the head has applied but the tail has not ==");
        ChainCluster v = new ChainCluster(2, 8, 3, 0, 2);
        Request w = v.update(0, "bob", 5_000);
        while (v.head().history.isEmpty()) v.tick();          // the head has applied it; the tail has not
        System.out.println("t=" + v.now() + ": head history=" + v.head().history.size() + ", tail history=" + v.tail().history.size() + ", client reply yet? " + v.client(0).hasReply(w));
        int sentAt = v.now();
        Request r = v.query(1, "bob");
        v.runUntilQuiet(500, 100);
        System.out.println("query sent at t=" + sentAt + " answered with " + v.client(1).replyFor(r) + "; the update's own reply was " + v.client(0).replyFor(w)
                + " -> a client never observes a write before the writer could");

        System.out.println();
        System.out.println("== Failures: head, middle, tail, each repaired by the master; no committed update is lost ==");
        ChainCluster f = new ChainCluster(3, 3, 4, 2, 3);
        for (int i = 0; i < 6; i++) f.update(i % 3, "acct" + (i % 2), 1_000 + i);
        f.runUntilQuiet(500, 60);
        int committedBefore = f.client(0).replies.size() + f.client(1).replies.size() + f.client(2).replies.size();
        System.out.println("6 updates committed; chain " + f.master.chain + "; tail history " + f.tail().history.size());
        // middle failure while updates are in flight
        for (int i = 0; i < 4; i++) f.update(i % 3, "acct0", 100);
        f.run(2);
        f.crashAndDetect(f.master.chain.get(1), 20);
        f.runUntilQuiet(1000, 60);
        System.out.println(f.master.events.get(f.master.events.size() - 1));
        System.out.println("  after middle failure: replies total=" + (f.client(0).replies.size() + f.client(1).replies.size() + f.client(2).replies.size())
                + ", tail history=" + f.tail().history.size() + ", retries=" + (f.client(0).retries + f.client(1).retries + f.client(2).retries));
        // tail failure with acks outstanding
        for (int i = 0; i < 3; i++) f.update(i % 3, "acct1", 7);
        f.run(1);
        f.crashAndDetect(f.master.tail(), 20);
        f.runUntilQuiet(1000, 60);
        System.out.println(f.master.events.get(f.master.events.size() - 1));
        System.out.println("  after tail failure: the new tail replied for " + f.tail().history.size() + " entries; outstanding Sent at new tail=" + f.tail().sent.size());
        // head failure: requests the head took but had not forwarded are lost and retried by clients
        for (int i = 0; i < 3; i++) f.update(i % 3, "acct0", 3);
        f.run(1);
        f.crashAndDetect(f.master.head(), 20);
        f.runUntilQuiet(1000, 60);
        System.out.println(f.master.events.get(f.master.events.size() - 1));
        System.out.println("  after head failure: chain " + f.master.chain + ", every client request answered, retries total=" + (f.client(0).retries + f.client(1).retries + f.client(2).retries));
        // extend back to length 3
        f.master.extend(4);
        f.runUntilQuiet(500, 60);
        f.update(0, "acct0", 1);
        f.runUntilQuiet(500, 60);
        System.out.println(f.master.events.get(f.master.events.size() - 1));
        printChain(f);
        System.out.println("  invariant checks run: " + f.checks + ", violations: " + f.violations.size());
        f.violations.forEach(x -> System.out.println("  " + x));

        System.out.println();
        System.out.println("== Load: chain spreads updates (head) and reads (tail) over different replicas; primary-backup puts both on one ==");
        int updates = 200, queries = 1000;
        ChainCluster lc = new ChainCluster(5, 2, 3, 0, 4);
        for (int i = 0; i < updates; i++) lc.update(i % 4, "a" + (i % 10), 100);
        for (int i = 0; i < queries; i++) lc.query(i % 4, "a" + (i % 10));
        lc.runUntilQuiet(20_000, 200);
        System.out.printf("chain (3 replicas), %d updates + %d queries:%n", updates, queries);
        for (int id : lc.master.chain) {
            ChainNode n = lc.node(id);
            System.out.printf("  n%d %-6s handled updates=%-4d queries=%-4d acks=%-4d total=%d%n", id, n.isHead() ? "HEAD" : n.isTail() ? "TAIL" : "middle", n.updatesHandled, n.queriesHandled, n.acksHandled, n.messagesHandled);
        }
        Network pbNet = new Network(5, 2);
        Map<Integer, PrimaryBackupNode> pbNodes = new TreeMap<>();
        for (int i = 0; i < 3; i++) { PrimaryBackupNode n = new PrimaryBackupNode(i); pbNodes.put(i, n); pbNet.add(n); }
        PrimaryBackupMaster pbMaster = new PrimaryBackupMaster(pbNet, pbNodes, List.of(0, 1, 2));
        Master facade = new Master(pbNet, List.of(0));                           // clients ask "head" and "tail": both are the primary
        List<ChainClient> pbClients = new ArrayList<>();
        for (int i = 0; i < 4; i++) { ChainClient cl = new ChainClient(100 + i, facade); pbClients.add(cl); pbNet.add(cl); }
        for (int t = 0; t < 10; t++) pbNet.tick();
        List<Message> out = new ArrayList<>();
        for (int i = 0; i < updates; i++) pbClients.get(i % 4).update("a" + (i % 10), Money.ofMinor(100, Currency.USD), pbNet.now(), out);
        for (int i = 0; i < queries; i++) pbClients.get(i % 4).query("a" + (i % 10), pbNet.now(), out);
        pbNet.sendAll(out);
        while (!pbNet.idle()) pbNet.tick();
        System.out.printf("primary-backup (1 primary + 2 backups), same workload:%n");
        for (PrimaryBackupNode n : pbNodes.values()) {
            System.out.printf("  n%d %-7s handled updates=%-4d queries=%-4d acks=%-4d total=%d%n", n.id, n.isPrimary ? "PRIMARY" : "backup", n.updatesHandled, n.queriesHandled, n.acksHandled, n.messagesHandled);
        }
        System.out.println("update latency: chain = one hop per replica plus the reply (" + lc.master.length() + " hops serial); primary-backup = primary -> backups -> primary -> client (3 hops, backups in parallel)");
        System.out.println("read consistency: chain tail reads and primary reads are both linearizable; chain backups never serve reads, p/b backups never serve anything");
    }
}
