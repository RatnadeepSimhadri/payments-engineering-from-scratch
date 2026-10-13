// Tests for phases/05-distributed-foundations/07-rpc-and-serialization. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    public static void main(String[] args) {
        // ---- codec: fixed-width big-endian integers and length-prefixed strings round-trip
        byte[] b = new ByteWriter().u8(0xAB).u16(0xBEEF).u32(0xFFFFFFFFL).i64(-2L).string("héllo").toBytes();
        check("codec encodes expected size", b.length == 1 + 2 + 4 + 8 + 2 + 6);
        ByteReader r = new ByteReader(b);
        check("u8 round trip", r.u8() == 0xAB);
        check("u16 round trip", r.u16() == 0xBEEF);
        check("u32 round trip (unsigned, no sign extension)", r.u32() == 0xFFFFFFFFL);
        check("i64 round trip (negative)", r.i64() == -2L);
        check("string round trip (UTF-8 multibyte)", r.string().equals("héllo"));
        check("reader consumed everything", r.remaining() == 0);
        check("big-endian byte order", Arrays.equals(new ByteWriter().u32(0x01020304L).toBytes(), new byte[]{1, 2, 3, 4}));
        checkThrows("truncated read is a CodecException, not ArrayIndexOutOfBounds", CodecException.class, () -> new ByteReader(new byte[3]).u32());
        checkThrows("u8 range checked", CodecException.class, () -> new ByteWriter().u8(256));
        checkThrows("string length beyond buffer rejected", CodecException.class, () -> new ByteReader(new byte[]{0, 9, 'a'}).string());

        // ---- RPC message envelope
        RpcMessage m = new RpcMessage(MessageKind.REQUEST, 77, "Credit", new byte[]{9, 8, 7});
        RpcMessage back = RpcMessage.decode(m.encode());
        check("message round trip kind", back.kind() == MessageKind.REQUEST);
        check("message round trip id", back.requestId() == 77);
        check("message round trip method", back.method().equals("Credit"));
        check("message round trip payload", Arrays.equals(back.payload(), new byte[]{9, 8, 7}));
        checkThrows("unknown kind tag rejected", CodecException.class, () -> RpcMessage.decode(new ByteWriter().u8(9).i64(1).string("x").u32(0).toBytes()));
        checkThrows("truncated message rejected", CodecException.class, () -> RpcMessage.decode(Arrays.copyOf(m.encode(), 10)));
        checkThrows("trailing garbage rejected", CodecException.class, () -> { byte[] e = m.encode(); RpcMessage.decode(Arrays.copyOf(e, e.length + 1)); });
        check("error message carries reason", RpcMessage.error(5, "Credit", "boom").errorReason().equals("boom"));

        // ---- framing: boundaries survive arbitrary segmentation
        Framer f = new Framer(1024);
        byte[] p1 = "first".getBytes(), p2 = "second-frame".getBytes(), p3 = new byte[0];
        byte[] all = concat(f.frame(p1), f.frame(p2), f.frame(p3));
        List<byte[]> got = new ArrayList<>();
        for (int i = 0; i < all.length; i++) got.addAll(f.feed(new byte[]{all[i]}));            // one byte at a time
        check("byte-at-a-time yields three frames", got.size() == 3);
        check("frame 1 intact", Arrays.equals(got.get(0), p1));
        check("frame 2 intact", Arrays.equals(got.get(1), p2));
        check("empty frame allowed", got.get(2).length == 0);
        check("nothing left buffered", f.buffered() == 0);
        Framer g = new Framer(1024);
        check("all at once yields three frames", g.feed(all).size() == 3);
        Framer h = new Framer(1024);
        check("partial header yields nothing", h.feed(new byte[]{0, 0}).isEmpty() && h.buffered() == 2);
        check("header then body completes", h.feed(concat(new byte[]{0, 2}, new byte[]{1, 2})).size() == 1);
        checkThrows("length prefix above max rejected before allocation", CodecException.class, () -> new Framer(1024).feed(new ByteWriter().u32(0x7FFFFFFFL).toBytes()));
        checkThrows("payload above max cannot be framed", CodecException.class, () -> new Framer(8).frame(new byte[9]));

        // ---- channel: segmentation and delay are deterministic, order is preserved, cut() is silent
        Channel c = new Channel(new Rng(3), 4, 5);
        c.write(new byte[]{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, 0);
        check("nothing readable at t=0", c.read(0).length == 0);
        check("everything readable eventually and in order", Arrays.equals(c.read(1000), new byte[]{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}));
        Channel c2 = new Channel(new Rng(3), 4, 5);
        c2.write(new byte[]{1, 2, 3}, 0);
        c2.cut();
        c2.write(new byte[]{4, 5}, 0);
        check("cut channel: write accepted without error, bytes lost", c2.bytesLost == 5 && c2.read(1000).length == 0);
        Channel d1 = new Channel(new Rng(11), 4, 5), d2 = new Channel(new Rng(11), 4, 5);
        d1.write(new byte[40], 0); d2.write(new byte[40], 0);
        check("same seed, same segmentation", d1.pendingSegments() == d2.pendingSegments());

        // ---- end to end: client, server, service
        SimClock clock = new SimClock(0);
        BalanceService service = new BalanceService();
        RpcServer server = new RpcServer(service, 1 << 16);
        RpcClient client = new RpcClient(clock, server, new Rng(5), 5, 8, 1 << 16);
        check("GetBalance returns the seeded balance", client.getBalance("alice", 500).equals(Money.ofMinor(10_000, Currency.USD)));
        check("Credit returns the new balance", client.credit("alice", Money.ofMinor(250, Currency.USD), 500).equals(Money.ofMinor(10_250, Currency.USD)));
        check("Credit was applied once", service.creditsApplied == 1);
        checkThrows("unknown method -> ERROR response, not a hang", RpcErrorException.class, () -> client.call("Transfer", new byte[0], 500));
        checkThrows("unknown account -> ERROR response", RpcErrorException.class, () -> client.getBalance("nobody", 500));
        checkThrows("currency mismatch surfaces as an RPC error", RpcErrorException.class, () -> client.credit("alice", Money.ofMinor(1, Currency.EUR), 500));
        check("connection still open after application errors", !server.isClosed(client.link()));
        check("request ids are monotonic", client.nextRequestId() == 6);

        // ---- timeout: the deadline fires, the late response is matched by id and discarded
        server.processingMillis = 250;
        long t0 = clock.now();
        checkThrows("slow server -> timeout", RpcTimeoutException.class, () -> client.getBalance("bob", 100));
        check("timeout fired at the deadline, not later", clock.now() - t0 == 100);
        server.processingMillis = 0;
        check("next call gets its own answer, not the stale one", client.getBalance("bob", 1000).equals(Money.ofMinor(2_500, Currency.USD)));
        check("the late response was counted and dropped", client.lateResponses == 1 && client.timeouts == 1);

        // ---- half-open: silence, not an error; the deadline is the only detector; retry duplicates side effects
        int creditsBefore = service.creditsApplied;
        Link link = client.link();
        long id = client.send("Credit", BalanceService.encodeCredit("bob", Money.ofMinor(100, Currency.USD)));
        for (int i = 0; i < 60; i++) { clock.advance(1); server.poll(clock.now()); }
        link.halfOpen();
        check("server applied the credit before the link died", service.creditsApplied == creditsBefore + 1);
        checkThrows("half-open connection is detected only by the deadline", RpcTimeoutException.class, () -> client.await(id, 200));
        check("writes into a half-open link do not fail", link.clientToServer.bytesLost >= 0 && link.serverToClient.isCut());
        client.reconnect();
        check("reconnect is a new connection", client.link().connectionId != link.connectionId && client.reconnects == 1);
        client.credit("bob", Money.ofMinor(100, Currency.USD), 500);
        check("naive retry doubles the credit (the hazard Lesson 09 fixes)", service.creditsApplied == creditsBefore + 2);
        check("balance reflects both applications", service.getBalance("bob").equals(Money.ofMinor(2_700, Currency.USD)));

        // ---- a corrupt stream closes the connection instead of poisoning later frames
        RpcServer strict = new RpcServer(new BalanceService(), 64);
        RpcClient victim = new RpcClient(clock, strict, new Rng(9), 2, 64, 64);
        victim.link().clientToServer.write(new ByteWriter().u32(5_000).toBytes(), clock.now());
        for (int i = 0; i < 10; i++) { clock.advance(1); strict.poll(clock.now()); }
        check("oversized frame closes the connection", strict.isClosed(victim.link()) && strict.malformedFrames == 1);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(x -> System.out.println("FAIL: " + x));
        if (!failures.isEmpty()) System.exit(1);
    }

    static byte[] concat(byte[]... parts) {
        ByteWriter w = new ByteWriter();
        for (byte[] p : parts) w.bytes(p);
        return w.toBytes();
    }
}
