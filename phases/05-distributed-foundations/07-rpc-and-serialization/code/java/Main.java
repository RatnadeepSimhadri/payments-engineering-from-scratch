// Lesson: phases/05-distributed-foundations/07-rpc-and-serialization/docs/en.md
// RPC, Framing, and Serialization Over Raw Sockets
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 9293 (TCP: byte stream, no message boundaries, half-open connections, section 3.6.2);
//          RFC 8949 (CBOR) and Protocol Buffers encoding docs for the shape of a length-delimited binary codec.
// Java 21, standard library only. Sockets are simulated by an in-memory byte-stream pair so every run is deterministic.

import java.nio.charset.StandardCharsets;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Deque;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

// ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)

/** ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** An exact integer count of minor units in one currency. Never a float. */
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

/** SplitMix64: the same stream in every language, so a seed replays everywhere. */
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
    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }
}

/** A clock the simulation advances by hand. Milliseconds. Nothing in this lesson sleeps. */
final class SimClock {
    private long now;
    SimClock(long start) { this.now = start; }
    long now() { return now; }
    void advance(long millis) { now += millis; }
}

// ---------------------------------------------------------------------------------------------- codec

/** Thrown when the bytes do not decode: truncated input, bad tag, negative length. Never leaks as an IndexOutOfBounds. */
final class CodecException extends RuntimeException {
    CodecException(String message) { super(message); }
}

/** Appends big-endian fixed-width integers and length-prefixed UTF-8 strings to a growable buffer. */
final class ByteWriter {
    private byte[] buf = new byte[64];
    private int len = 0;

    private void ensure(int extra) {
        if (len + extra > buf.length) buf = Arrays.copyOf(buf, Math.max(buf.length * 2, len + extra));
    }

    ByteWriter u8(int v) {
        if (v < 0 || v > 0xFF) throw new CodecException("u8 out of range: " + v);
        ensure(1); buf[len++] = (byte) v; return this;
    }

    ByteWriter u16(int v) {
        if (v < 0 || v > 0xFFFF) throw new CodecException("u16 out of range: " + v);
        ensure(2); buf[len++] = (byte) (v >>> 8); buf[len++] = (byte) v; return this;
    }

    ByteWriter u32(long v) {
        if (v < 0 || v > 0xFFFFFFFFL) throw new CodecException("u32 out of range: " + v);
        ensure(4);
        for (int shift = 24; shift >= 0; shift -= 8) buf[len++] = (byte) (v >>> shift);
        return this;
    }

    ByteWriter i64(long v) {
        ensure(8);
        for (int shift = 56; shift >= 0; shift -= 8) buf[len++] = (byte) (v >>> shift);
        return this;
    }

    /** u16 byte length followed by UTF-8 bytes. 65535 bytes is the cap; account ids and method names are far smaller. */
    ByteWriter string(String s) {
        byte[] utf8 = s.getBytes(StandardCharsets.UTF_8);
        u16(utf8.length);
        ensure(utf8.length); System.arraycopy(utf8, 0, buf, len, utf8.length); len += utf8.length;
        return this;
    }

    ByteWriter bytes(byte[] b) { ensure(b.length); System.arraycopy(b, 0, buf, len, b.length); len += b.length; return this; }

    int size() { return len; }
    byte[] toBytes() { return Arrays.copyOf(buf, len); }
}

/** Reads what ByteWriter wrote. Every read checks bounds first and fails with CodecException, not an array exception. */
final class ByteReader {
    private final byte[] buf;
    private int pos;

    ByteReader(byte[] buf) { this.buf = buf; this.pos = 0; }

    private void need(int n) {
        if (pos + n > buf.length) throw new CodecException("truncated: need " + n + " bytes at offset " + pos + ", have " + (buf.length - pos));
    }

    int u8() { need(1); return buf[pos++] & 0xFF; }
    int u16() { need(2); int v = ((buf[pos] & 0xFF) << 8) | (buf[pos + 1] & 0xFF); pos += 2; return v; }

    long u32() {
        need(4);
        long v = 0;
        for (int i = 0; i < 4; i++) v = (v << 8) | (buf[pos++] & 0xFF);
        return v;
    }

    long i64() {
        need(8);
        long v = 0;
        for (int i = 0; i < 8; i++) v = (v << 8) | (buf[pos++] & 0xFF);
        return v;
    }

    String string() {
        int n = u16();
        need(n);
        String s = new String(buf, pos, n, StandardCharsets.UTF_8);
        pos += n;
        return s;
    }

    int remaining() { return buf.length - pos; }

    void expectEnd() { if (pos != buf.length) throw new CodecException("trailing bytes: " + (buf.length - pos)); }
}

// ---------------------------------------------------------------------------------------------- framing

/**
 * Length-prefixed framing: a 4-byte big-endian payload length, then the payload. The reader is incremental: feed it
 * whatever bytes the stream produced (half a header, three frames glued together) and it yields complete frames only.
 */
final class Framer {
    static final int HEADER = 4;
    final int maxFrame;
    private byte[] pending = new byte[0];
    int framesOut = 0;

    Framer(int maxFrame) { this.maxFrame = maxFrame; }

    static byte[] frame(byte[] payload, int maxFrame) {
        if (payload.length > maxFrame) throw new CodecException("payload " + payload.length + " exceeds max frame " + maxFrame);
        return new ByteWriter().u32(payload.length).bytes(payload).toBytes();
    }

    byte[] frame(byte[] payload) { return frame(payload, maxFrame); }

    /** Append stream bytes and return every frame that is now complete. A hostile length makes this throw before allocating. */
    List<byte[]> feed(byte[] chunk) {
        byte[] joined = Arrays.copyOf(pending, pending.length + chunk.length);
        System.arraycopy(chunk, 0, joined, pending.length, chunk.length);
        List<byte[]> out = new ArrayList<>();
        int pos = 0;
        while (joined.length - pos >= HEADER) {
            long length = new ByteReader(Arrays.copyOfRange(joined, pos, pos + HEADER)).u32();
            if (length > maxFrame) throw new CodecException("frame length " + length + " exceeds max " + maxFrame + "; closing connection");
            if (joined.length - pos - HEADER < length) break;            // header complete, body not yet
            out.add(Arrays.copyOfRange(joined, pos + HEADER, pos + HEADER + (int) length));
            pos += HEADER + (int) length;
        }
        pending = Arrays.copyOfRange(joined, pos, joined.length);
        framesOut += out.size();
        return out;
    }

    int buffered() { return pending.length; }
}

// ---------------------------------------------------------------------------------------------- RPC messages

enum MessageKind {
    REQUEST(1), RESPONSE(2), ERROR(3);
    final int tag;
    MessageKind(int tag) { this.tag = tag; }

    static MessageKind fromTag(int tag) {
        for (MessageKind k : values()) if (k.tag == tag) return k;
        throw new CodecException("unknown message kind tag " + tag);
    }
}

/**
 * One RPC message. Wire layout: u8 kind, i64 requestId, string method, u32 payloadLength, payload bytes.
 * The payload is method-specific and encoded by the service, so the envelope never needs to change when a method does.
 */
record RpcMessage(MessageKind kind, long requestId, String method, byte[] payload) {
    byte[] encode() {
        return new ByteWriter().u8(kind.tag).i64(requestId).string(method).u32(payload.length).bytes(payload).toBytes();
    }

    static RpcMessage decode(byte[] bytes) {
        ByteReader r = new ByteReader(bytes);
        MessageKind kind = MessageKind.fromTag(r.u8());
        long id = r.i64();
        String method = r.string();
        long n = r.u32();
        if (n > r.remaining()) throw new CodecException("payload length " + n + " exceeds remaining " + r.remaining());
        byte[] payload = new byte[(int) n];
        for (int i = 0; i < n; i++) payload[i] = (byte) r.u8();
        r.expectEnd();
        return new RpcMessage(kind, id, method, payload);
    }

    static RpcMessage error(long requestId, String method, String reason) {
        return new RpcMessage(MessageKind.ERROR, requestId, method, new ByteWriter().string(reason).toBytes());
    }

    String errorReason() { return new ByteReader(payload).string(); }
}

// ---------------------------------------------------------------------------------------------- the simulated socket pair

/**
 * One direction of a connection. Writes are segmented into random-sized chunks that arrive after a random delay, which
 * is what TCP does to your messages. cut() makes the channel half-open: writes are accepted and vanish, reads see nothing.
 */
final class Channel {
    record Segment(long arriveAt, byte[] bytes) {}

    private final Rng rng;
    private final int maxDelay;
    private final int maxSegment;
    private final Deque<Segment> inFlight = new ArrayDeque<>();
    private boolean cut = false;
    long bytesWritten = 0, bytesDelivered = 0, bytesLost = 0;

    Channel(Rng rng, int maxDelay, int maxSegment) { this.rng = rng; this.maxDelay = Math.max(1, maxDelay); this.maxSegment = Math.max(1, maxSegment); }

    void write(byte[] bytes, long now) {
        bytesWritten += bytes.length;
        if (cut) { bytesLost += bytes.length; return; }           // half-open: the peer is gone and nobody told us
        int pos = 0;
        long arrive = now;
        while (pos < bytes.length) {
            int n = Math.min(bytes.length - pos, 1 + rng.nextInt(maxSegment));
            arrive += 1 + rng.nextInt(maxDelay);                   // segments stay ordered: each arrives after the previous
            inFlight.addLast(new Segment(arrive, Arrays.copyOfRange(bytes, pos, pos + n)));
            pos += n;
        }
    }

    /** Everything that has arrived by `now`, concatenated. Empty when nothing has: a non-blocking read. */
    byte[] read(long now) {
        if (cut) return new byte[0];
        ByteWriter w = new ByteWriter();
        while (!inFlight.isEmpty() && inFlight.peekFirst().arriveAt() <= now) w.bytes(inFlight.pollFirst().bytes());
        bytesDelivered += w.size();
        return w.toBytes();
    }

    void cut() { cut = true; bytesLost += inFlight.stream().mapToLong(s -> s.bytes().length).sum(); inFlight.clear(); }
    boolean isCut() { return cut; }
    int pendingSegments() { return inFlight.size(); }
}

/** A connection: two channels. Connection ids let the demo show that a reconnect is a new connection, not a repair. */
final class Link {
    private static int nextId = 1;
    final int connectionId = nextId++;
    final Channel clientToServer;
    final Channel serverToClient;

    Link(Rng rng, int maxDelay, int maxSegment) {
        clientToServer = new Channel(rng, maxDelay, maxSegment);
        serverToClient = new Channel(rng, maxDelay, maxSegment);
    }

    /** Simulate the peer vanishing without a FIN or RST: both directions go dark, neither side gets an error. */
    void halfOpen() { clientToServer.cut(); serverToClient.cut(); }
}

// ---------------------------------------------------------------------------------------------- the service

/** The application: account balances. Deliberately not idempotent so the retry hazard is visible; Lesson 09 fixes that. */
final class BalanceService {
    final Map<String, Money> balances = new TreeMap<>();
    int creditsApplied = 0;

    BalanceService() { balances.put("alice", Money.ofMinor(10_000, Currency.USD)); balances.put("bob", Money.ofMinor(2_500, Currency.USD)); }

    Money getBalance(String account) {
        Money m = balances.get(account);
        if (m == null) throw new IllegalArgumentException("no such account: " + account);
        return m;
    }

    Money credit(String account, Money amount) {
        Money updated = getBalance(account).plus(amount);
        balances.put(account, updated);
        creditsApplied++;
        return updated;
    }

    // -- payload codecs: the service owns its own wire format; the RPC layer sees opaque bytes.

    static byte[] encodeMoney(Money m) { return new ByteWriter().i64(m.minorUnits()).string(m.currency().code()).u8(m.currency().exponent()).toBytes(); }

    static Money decodeMoney(ByteReader r) {
        long minor = r.i64();
        String code = r.string();
        int exponent = r.u8();
        return Money.ofMinor(minor, new Currency(code, exponent));
    }

    static byte[] encodeGetBalance(String account) { return new ByteWriter().string(account).toBytes(); }

    static byte[] encodeCredit(String account, Money amount) { return new ByteWriter().string(account).bytes(encodeMoney(amount)).toBytes(); }

    /** Dispatch by method name. Unknown methods and bad payloads become ERROR responses, never a dropped connection. */
    RpcMessage dispatch(RpcMessage request) {
        try {
            ByteReader r = new ByteReader(request.payload());
            switch (request.method()) {
                case "GetBalance" -> {
                    String account = r.string();
                    r.expectEnd();
                    return new RpcMessage(MessageKind.RESPONSE, request.requestId(), request.method(), encodeMoney(getBalance(account)));
                }
                case "Credit" -> {
                    String account = r.string();
                    Money amount = decodeMoney(r);
                    r.expectEnd();
                    return new RpcMessage(MessageKind.RESPONSE, request.requestId(), request.method(), encodeMoney(credit(account, amount)));
                }
                default -> { return RpcMessage.error(request.requestId(), request.method(), "unknown method " + request.method()); }
            }
        } catch (CodecException | IllegalArgumentException e) {
            return RpcMessage.error(request.requestId(), request.method(), e.getMessage());
        }
    }
}

// ---------------------------------------------------------------------------------------------- server and client

/** Reads frames off the connection, dispatches, writes responses. One Framer per connection because the stream has state. */
final class RpcServer {
    final BalanceService service;
    private final int maxFrame;
    private final Map<Link, Framer> framers = new HashMap<>();
    int requestsServed = 0, malformedFrames = 0;
    /** Extra processing time per request in ms; the simulation uses it to make the server slow. */
    long processingMillis = 0;
    private final Deque<Object[]> delayed = new ArrayDeque<>();   // {readyAt, Link, RpcMessage}

    RpcServer(BalanceService service, int maxFrame) { this.service = service; this.maxFrame = maxFrame; }

    void accept(Link link) { framers.put(link, new Framer(maxFrame)); }

    boolean isClosed(Link link) { return !framers.containsKey(link); }

    /** One scheduling turn: drain every connection's readable bytes, dispatch complete frames, flush due responses. */
    void poll(long now) {
        for (Map.Entry<Link, Framer> e : new ArrayList<>(framers.entrySet())) {
            Link link = e.getKey();
            byte[] chunk = link.clientToServer.read(now);
            if (chunk.length == 0) continue;
            List<byte[]> frames;
            try {
                frames = e.getValue().feed(chunk);
            } catch (CodecException ex) {
                malformedFrames++;
                framers.remove(link);                              // a corrupt stream cannot be resynchronised: close it
                continue;
            }
            for (byte[] f : frames) {
                RpcMessage request;
                try { request = RpcMessage.decode(f); }
                catch (CodecException ex) { malformedFrames++; continue; }
                RpcMessage response = service.dispatch(request);
                requestsServed++;
                delayed.addLast(new Object[]{now + processingMillis, link, response});
            }
        }
        while (!delayed.isEmpty() && (Long) delayed.peekFirst()[0] <= now) {
            Object[] d = delayed.pollFirst();
            Link link = (Link) d[1];
            if (framers.containsKey(link)) link.serverToClient.write(Framer.frame(((RpcMessage) d[2]).encode(), maxFrame), now);
        }
    }
}

final class RpcTimeoutException extends RuntimeException {
    RpcTimeoutException(String message) { super(message); }
}

final class RpcErrorException extends RuntimeException {
    RpcErrorException(String message) { super(message); }
}

/**
 * The client: assigns request ids, frames and writes the request, then pumps the simulation until the matching response
 * arrives or the deadline passes. Responses whose id is not pending are late answers to timed-out calls and are counted,
 * not delivered. The id counter survives reconnects so a late answer from an old connection can never match a new call.
 */
final class RpcClient {
    private final SimClock clock;
    private final RpcServer server;
    private final Rng rng;
    private final int maxDelay, maxSegment, maxFrame;
    private Link link;
    private Framer framer;
    private long nextRequestId = 1;
    private final Map<Long, RpcMessage> received = new LinkedHashMap<>();
    int timeouts = 0, lateResponses = 0, reconnects = 0;

    RpcClient(SimClock clock, RpcServer server, Rng rng, int maxDelay, int maxSegment, int maxFrame) {
        this.clock = clock; this.server = server; this.rng = rng;
        this.maxDelay = maxDelay; this.maxSegment = maxSegment; this.maxFrame = maxFrame;
        connect();
    }

    void connect() {
        link = new Link(rng, maxDelay, maxSegment);
        framer = new Framer(maxFrame);
        server.accept(link);
    }

    void reconnect() { reconnects++; connect(); }
    Link link() { return link; }
    long nextRequestId() { return nextRequestId; }

    /** Write a request without waiting. Returns its id so the caller can await it later (the demo uses this to cut the link mid-call). */
    long send(String method, byte[] payload) {
        long id = nextRequestId++;
        link.clientToServer.write(framer.frame(new RpcMessage(MessageKind.REQUEST, id, method, payload).encode()), clock.now());
        return id;
    }

    /** Pump the world until the response for `id` arrives or the deadline passes. */
    RpcMessage await(long id, long timeoutMillis) {
        long deadline = clock.now() + timeoutMillis;
        while (true) {
            pump();
            RpcMessage r = received.remove(id);
            if (r != null) {
                if (r.kind() == MessageKind.ERROR) throw new RpcErrorException(r.method() + ": " + r.errorReason());
                return r;
            }
            if (clock.now() >= deadline) {
                timeouts++;
                throw new RpcTimeoutException("request " + id + " timed out after " + timeoutMillis + " ms on connection " + link.connectionId
                        + " (outcome unknown)");
            }
            clock.advance(1);
        }
    }

    RpcMessage call(String method, byte[] payload, long timeoutMillis) { return await(send(method, payload), timeoutMillis); }

    /** Advance the world one step: the server runs, then we read whatever the server-to-client channel has delivered. */
    void pump() {
        server.poll(clock.now());
        byte[] chunk = link.serverToClient.read(clock.now());
        if (chunk.length == 0) return;
        for (byte[] f : framer.feed(chunk)) {
            RpcMessage m = RpcMessage.decode(f);
            if (m.requestId() >= nextRequestId || received.containsKey(m.requestId())) throw new IllegalStateException("response for unknown id " + m.requestId());
            received.put(m.requestId(), m);
        }
        // Anything buffered for an id that has already timed out is a late response: count and drop it.
        received.keySet().removeIf(rid -> { boolean stale = rid < currentCallId(); if (stale) lateResponses++; return stale; });
    }

    private long currentCallId() { return nextRequestId - 1; }

    // -- typed wrappers so callers never touch bytes

    Money getBalance(String account, long timeout) {
        return BalanceService.decodeMoney(new ByteReader(call("GetBalance", BalanceService.encodeGetBalance(account), timeout).payload()));
    }

    Money credit(String account, Money amount, long timeout) {
        return BalanceService.decodeMoney(new ByteReader(call("Credit", BalanceService.encodeCredit(account, amount), timeout).payload()));
    }
}

// ---------------------------------------------------------------------------------------------- demo

public final class Main {
    static String hex(byte[] b) {
        StringBuilder sb = new StringBuilder();
        for (byte x : b) sb.append(String.format("%02x", x));
        return sb.toString();
    }

    public static void main(String[] args) {
        System.out.println("== The wire format: one GetBalance request, framed ==");
        RpcMessage req = new RpcMessage(MessageKind.REQUEST, 1, "GetBalance", BalanceService.encodeGetBalance("alice"));
        byte[] body = req.encode();
        byte[] framed = Framer.frame(body, 1 << 20);
        System.out.println("payload  (" + body.length + " bytes): " + hex(body));
        System.out.println("framed   (" + framed.length + " bytes): " + hex(framed));
        System.out.println("  = u32 length " + body.length + " | u8 kind=1 | i64 requestId=1 | u16 len=10 'GetBalance' | u32 payloadLen=7 | u16 len=5 'alice'");
        RpcMessage back = RpcMessage.decode(body);
        System.out.println("decodes back to " + back.kind() + " id=" + back.requestId() + " method=" + back.method());

        System.out.println();
        System.out.println("== TCP does not respect your message boundaries: 3 frames, segmented into random chunks ==");
        Rng rng = new Rng(42);
        Channel ch = new Channel(rng, 3, 7);
        Framer reader = new Framer(1 << 20);
        for (int i = 1; i <= 3; i++) ch.write(Framer.frame(new RpcMessage(MessageKind.REQUEST, i, "GetBalance", BalanceService.encodeGetBalance("bob")).encode(), 1 << 20), 0);
        int chunks = 0, frames = 0;
        for (long t = 1; t <= 200 && frames < 3; t++) {
            byte[] chunk = ch.read(t);
            if (chunk.length == 0) continue;
            chunks++;
            List<byte[]> got = reader.feed(chunk);
            frames += got.size();
            System.out.printf("t=%-3d read %2d bytes -> %d complete frame(s), %2d bytes buffered%n", t, chunk.length, got.size(), reader.buffered());
        }
        System.out.println(frames + " frames reassembled from " + chunks + " reads; no frame was ever half-decoded");

        System.out.println();
        System.out.println("== A hostile length prefix is rejected before any allocation ==");
        try {
            new Framer(1 << 20).feed(new ByteWriter().u32(0xFFFFFFFFL).toBytes());
        } catch (CodecException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        try {
            RpcMessage.decode(Arrays.copyOf(body, body.length - 3));
        } catch (CodecException e) {
            System.out.println("truncated message rejected: " + e.getMessage());
        }

        System.out.println();
        System.out.println("== Request/response over the simulated socket pair (seed 7, delay 1-5 ms per segment) ==");
        SimClock clock = new SimClock(0);
        BalanceService service = new BalanceService();
        RpcServer server = new RpcServer(service, 1 << 20);
        RpcClient client = new RpcClient(clock, server, new Rng(7), 5, 16, 1 << 20);
        long t0 = clock.now();
        Money alice = client.getBalance("alice", 500);
        System.out.println("GetBalance(alice) = " + alice + " in " + (clock.now() - t0) + " ms, connection " + client.link().connectionId);
        t0 = clock.now();
        Money after = client.credit("alice", Money.ofMinor(1_999, Currency.USD), 500);
        System.out.println("Credit(alice, 19.99 USD) = " + after + " in " + (clock.now() - t0) + " ms");
        try {
            client.call("Transfer", new byte[0], 500);
        } catch (RpcErrorException e) {
            System.out.println("server-side error travels back with the same request id: " + e.getMessage());
        }

        System.out.println();
        System.out.println("== Timeout: the server is slow, the client gives up, the answer arrives later and is discarded ==");
        server.processingMillis = 300;
        t0 = clock.now();
        try {
            client.getBalance("bob", 100);
        } catch (RpcTimeoutException e) {
            System.out.println("t=" + (clock.now() - t0) + ": " + e.getMessage());
        }
        server.processingMillis = 0;
        Money bob = client.getBalance("bob", 1000);
        System.out.println("next call still gets the right answer: GetBalance(bob) = " + bob + "; late responses discarded so far = " + client.lateResponses);

        System.out.println();
        System.out.println("== Half-open: the server vanishes mid-Credit; the client's socket shows no error, only silence ==");
        long creditsBefore = service.creditsApplied;
        Link doomed = client.link();
        long inFlight = client.send("Credit", BalanceService.encodeCredit("bob", Money.ofMinor(5_000, Currency.USD)));
        for (int i = 0; i < 60; i++) { clock.advance(1); server.poll(clock.now()); }   // request arrives and is applied...
        doomed.halfOpen();                                                            // ...and the reply never makes it back
        System.out.println("server applied the credit (creditsApplied " + creditsBefore + " -> " + service.creditsApplied + "), bob = " + service.getBalance("bob"));
        System.out.println("then the link went half-open: " + doomed.serverToClient.bytesLost + " reply bytes lost, client sees nothing");
        long probe = client.send("GetBalance", BalanceService.encodeGetBalance("bob"));   // a write into the void: no error
        System.out.println("a further write of request " + probe + " 'succeeds': " + doomed.clientToServer.bytesLost + " bytes accepted by the socket and lost; reads return 0 bytes forever");
        try {
            client.await(inFlight, 200);
        } catch (RpcTimeoutException e) {
            System.out.println("client: " + e.getMessage() + "; only the deadline detects a half-open connection");
        }
        client.reconnect();
        System.out.println("reconnect -> new connection " + client.link().connectionId + " (ids continue from " + client.nextRequestId() + ", so no old reply can match)");
        Money retried = client.credit("bob", Money.ofMinor(5_000, Currency.USD), 500);
        System.out.println("naive retry of Credit(bob, 50.00 USD) = " + retried + "  <- applied " + (service.creditsApplied - creditsBefore)
                + " times for one intended credit. A timeout means UNKNOWN, not FAILED. Lesson 09 (idempotency keys) is the fix.");
    }
}
