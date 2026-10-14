"""Tests for phases/05-distributed-foundations/07-rpc-and-serialization. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR, USD, BalanceService, ByteReader, ByteWriter, Channel, CodecException, Framer, MessageKind, Money, Rng,
    RpcClient, RpcErrorException, RpcMessage, RpcServer, RpcTimeoutException, SimClock,
)


def concat(*parts: bytes) -> bytes:
    w = ByteWriter()
    for p in parts:
        w.bytes(p)
    return w.to_bytes()


class CodecTest(unittest.TestCase):
    def test_round_trips(self):
        b = ByteWriter().u8(0xAB).u16(0xBEEF).u32(0xFFFFFFFF).i64(-2).string("héllo").to_bytes()
        self.assertEqual(len(b), 1 + 2 + 4 + 8 + 2 + 6, "codec encodes expected size")
        r = ByteReader(b)
        self.assertEqual(r.u8(), 0xAB)
        self.assertEqual(r.u16(), 0xBEEF)
        self.assertEqual(r.u32(), 0xFFFFFFFF, "u32 round trip (unsigned, no sign extension)")
        self.assertEqual(r.i64(), -2, "i64 round trip (negative)")
        self.assertEqual(r.string(), "héllo", "string round trip (UTF-8 multibyte)")
        self.assertEqual(r.remaining(), 0)
        self.assertEqual(ByteWriter().u32(0x01020304).to_bytes(), bytes([1, 2, 3, 4]), "big-endian byte order")

    def test_failures_are_codec_exceptions(self):
        with self.assertRaises(CodecException):
            ByteReader(bytes(3)).u32()
        with self.assertRaises(CodecException):
            ByteWriter().u8(256)
        with self.assertRaises(CodecException):
            ByteReader(bytes([0, 9, ord("a")])).string()
        with self.assertRaises(CodecException):
            ByteWriter().i64(1 << 63)


class EnvelopeTest(unittest.TestCase):
    def test_message(self):
        m = RpcMessage(MessageKind.REQUEST, 77, "Credit", bytes([9, 8, 7]))
        back = RpcMessage.decode(m.encode())
        self.assertIs(back.kind, MessageKind.REQUEST)
        self.assertEqual(back.request_id, 77)
        self.assertEqual(back.method, "Credit")
        self.assertEqual(back.payload, bytes([9, 8, 7]))
        with self.assertRaises(CodecException):
            RpcMessage.decode(ByteWriter().u8(9).i64(1).string("x").u32(0).to_bytes())
        with self.assertRaises(CodecException):
            RpcMessage.decode(m.encode()[:10])
        with self.assertRaises(CodecException):
            RpcMessage.decode(m.encode() + b"\x00")
        self.assertEqual(RpcMessage.error(5, "Credit", "boom").error_reason(), "boom")


class FramingTest(unittest.TestCase):
    def test_boundaries_survive_segmentation(self):
        f = Framer(1024)
        p1, p2, p3 = b"first", b"second-frame", b""
        all_ = concat(f.frame(p1), f.frame(p2), f.frame(p3))
        got = []
        for i in range(len(all_)):
            got.extend(f.feed(all_[i:i + 1]))                 # one byte at a time
        self.assertEqual(len(got), 3)
        self.assertEqual(got[0], p1)
        self.assertEqual(got[1], p2)
        self.assertEqual(len(got[2]), 0, "empty frame allowed")
        self.assertEqual(f.buffered(), 0)
        self.assertEqual(len(Framer(1024).feed(all_)), 3, "all at once yields three frames")
        h = Framer(1024)
        self.assertEqual(h.feed(bytes([0, 0])), [])
        self.assertEqual(h.buffered(), 2, "partial header yields nothing")
        self.assertEqual(len(h.feed(concat(bytes([0, 2]), bytes([1, 2])))), 1, "header then body completes")
        with self.assertRaises(CodecException):
            Framer(1024).feed(ByteWriter().u32(0x7FFFFFFF).to_bytes())
        with self.assertRaises(CodecException):
            Framer(8).frame(bytes(9))


class ChannelTest(unittest.TestCase):
    def test_segmentation_order_and_cut(self):
        c = Channel(Rng(3), 4, 5)
        c.write(bytes(range(1, 13)), 0)
        self.assertEqual(c.read(0), b"", "nothing readable at t=0")
        self.assertEqual(c.read(1000), bytes(range(1, 13)), "everything readable eventually and in order")
        c2 = Channel(Rng(3), 4, 5)
        c2.write(bytes([1, 2, 3]), 0)
        c2.cut()
        c2.write(bytes([4, 5]), 0)
        self.assertEqual(c2.bytes_lost, 5, "cut channel: write accepted without error, bytes lost")
        self.assertEqual(c2.read(1000), b"")
        d1, d2 = Channel(Rng(11), 4, 5), Channel(Rng(11), 4, 5)
        d1.write(bytes(40), 0)
        d2.write(bytes(40), 0)
        self.assertEqual(d1.pending_segments(), d2.pending_segments(), "same seed, same segmentation")

    def test_splitmix64_matches_reference(self):
        # SplitMix64 seeded with 0: first output 0xE220A8397B1DCDAF (reference implementation, as a signed long).
        self.assertEqual(Rng(0).next_long() & ((1 << 64) - 1), 0xE220A8397B1DCDAF)


class EndToEndTest(unittest.TestCase):
    def test_full_story(self):
        clock = SimClock(0)
        service = BalanceService()
        server = RpcServer(service, 1 << 16)
        client = RpcClient(clock, server, Rng(5), 5, 8, 1 << 16)
        self.assertEqual(client.get_balance("alice", 500), Money.of_minor(10_000, USD))
        self.assertEqual(client.credit("alice", Money.of_minor(250, USD), 500), Money.of_minor(10_250, USD))
        self.assertEqual(service.credits_applied, 1, "Credit was applied once")
        with self.assertRaises(RpcErrorException):
            client.call("Transfer", b"", 500)
        with self.assertRaises(RpcErrorException):
            client.get_balance("nobody", 500)
        with self.assertRaises(RpcErrorException):
            client.credit("alice", Money.of_minor(1, EUR), 500)
        self.assertFalse(server.is_closed(client.link()), "connection still open after application errors")
        self.assertEqual(client.next_request_id(), 6, "request ids are monotonic")

        # timeout: the deadline fires, the late response is matched by id and discarded
        server.processing_millis = 250
        t0 = clock.now()
        with self.assertRaises(RpcTimeoutException):
            client.get_balance("bob", 100)
        self.assertEqual(clock.now() - t0, 100, "timeout fired at the deadline, not later")
        server.processing_millis = 0
        self.assertEqual(client.get_balance("bob", 1000), Money.of_minor(2_500, USD))
        self.assertEqual(client.late_responses, 1, "the late response was counted and dropped")
        self.assertEqual(client.timeouts, 1)

        # half-open: silence, not an error; the deadline is the only detector; retry duplicates side effects
        credits_before = service.credits_applied
        link = client.link()
        rid = client.send("Credit", BalanceService.encode_credit("bob", Money.of_minor(100, USD)))
        for _ in range(60):
            clock.advance(1)
            server.poll(clock.now())
        link.half_open()
        self.assertEqual(service.credits_applied, credits_before + 1, "server applied the credit before the link died")
        with self.assertRaises(RpcTimeoutException):
            client.await_(rid, 200)
        self.assertGreaterEqual(link.client_to_server.bytes_lost, 0)
        self.assertTrue(link.server_to_client.is_cut(), "writes into a half-open link do not fail")
        client.reconnect()
        self.assertNotEqual(client.link().connection_id, link.connection_id, "reconnect is a new connection")
        self.assertEqual(client.reconnects, 1)
        client.credit("bob", Money.of_minor(100, USD), 500)
        self.assertEqual(service.credits_applied, credits_before + 2, "naive retry doubles the credit")
        self.assertEqual(service.get_balance("bob"), Money.of_minor(2_700, USD))

    def test_corrupt_stream_closes_connection(self):
        clock = SimClock(0)
        strict = RpcServer(BalanceService(), 64)
        victim = RpcClient(clock, strict, Rng(9), 2, 64, 64)
        victim.link().client_to_server.write(ByteWriter().u32(5_000).to_bytes(), clock.now())
        for _ in range(10):
            clock.advance(1)
            strict.poll(clock.now())
        self.assertTrue(strict.is_closed(victim.link()), "oversized frame closes the connection")
        self.assertEqual(strict.malformed_frames, 1)


if __name__ == "__main__":
    unittest.main()
