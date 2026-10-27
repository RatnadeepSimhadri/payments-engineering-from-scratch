// Tests for phases/05-distributed-foundations/07-rpc-and-serialization. Run: go test ./...
package main

import (
	"bytes"
	"errors"
	"testing"
)

func toBytes(t *testing.T, w *ByteWriter) []byte {
	t.Helper()
	b, err := w.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	return b
}

func encode(t *testing.T, m RpcMessage) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

func frame(t *testing.T, f *Framer, payload []byte) []byte {
	t.Helper()
	b, err := f.Frame(payload)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	return b
}

func feed(t *testing.T, f *Framer, chunk []byte) [][]byte {
	t.Helper()
	out, err := f.Feed(chunk)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	return out
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestCodecRoundTrip(t *testing.T) {
	b := toBytes(t, NewByteWriter().U8(0xAB).U16(0xBEEF).U32(0xFFFFFFFF).I64(-2).String("héllo"))
	if len(b) != 1+2+4+8+2+6 {
		t.Errorf("codec encodes expected size: %d", len(b))
	}
	r := NewByteReader(b)
	if r.U8() != 0xAB {
		t.Error("u8 round trip")
	}
	if r.U16() != 0xBEEF {
		t.Error("u16 round trip")
	}
	if r.U32() != 0xFFFFFFFF {
		t.Error("u32 round trip (unsigned, no sign extension)")
	}
	if r.I64() != -2 {
		t.Error("i64 round trip (negative)")
	}
	if r.String() != "héllo" {
		t.Error("string round trip (UTF-8 multibyte)")
	}
	if r.Remaining() != 0 || r.Err() != nil {
		t.Error("reader consumed everything without error")
	}
	if !bytes.Equal(toBytes(t, NewByteWriter().U32(0x01020304)), []byte{1, 2, 3, 4}) {
		t.Error("big-endian byte order")
	}
	short := NewByteReader(make([]byte, 3))
	if short.U32() != 0 || !errors.Is(short.Err(), ErrCodec) {
		t.Error("truncated read is a codec error, not an index panic")
	}
	if _, err := NewByteWriter().U8(256).ToBytes(); !errors.Is(err, ErrCodec) {
		t.Error("u8 range checked")
	}
	if _, err := NewByteWriter().U16(-1).U8(1).ToBytes(); !errors.Is(err, ErrCodec) {
		t.Error("the first range error sticks through later writes")
	}
	bad := NewByteReader([]byte{0, 9, 'a'})
	if bad.String() != "" || !errors.Is(bad.Err(), ErrCodec) {
		t.Error("string length beyond buffer rejected")
	}
}

func TestRpcMessageEnvelope(t *testing.T) {
	m := RpcMessage{Request, 77, "Credit", []byte{9, 8, 7}}
	back, err := Decode(encode(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != Request {
		t.Error("message round trip kind")
	}
	if back.RequestID != 77 {
		t.Error("message round trip id")
	}
	if back.Method != "Credit" {
		t.Error("message round trip method")
	}
	if !bytes.Equal(back.Payload, []byte{9, 8, 7}) {
		t.Error("message round trip payload")
	}
	if _, err := Decode(toBytes(t, NewByteWriter().U8(9).I64(1).String("x").U32(0))); !errors.Is(err, ErrCodec) {
		t.Error("unknown kind tag rejected")
	}
	if _, err := Decode(encode(t, m)[:10]); !errors.Is(err, ErrCodec) {
		t.Error("truncated message rejected")
	}
	if _, err := Decode(append(encode(t, m), 0)); !errors.Is(err, ErrCodec) {
		t.Error("trailing garbage rejected")
	}
	if ErrorMessage(5, "Credit", "boom").ErrorReason() != "boom" {
		t.Error("error message carries reason")
	}
	if Request.String() != "REQUEST" || Error.String() != "ERROR" {
		t.Error("kinds print by name")
	}
}

func TestFramingSurvivesSegmentation(t *testing.T) {
	f := NewFramer(1024)
	p1, p2, p3 := []byte("first"), []byte("second-frame"), []byte{}
	all := concat(frame(t, f, p1), frame(t, f, p2), frame(t, f, p3))
	var got [][]byte
	for i := range all { // one byte at a time
		got = append(got, feed(t, f, all[i:i+1])...)
	}
	if len(got) != 3 {
		t.Fatalf("byte-at-a-time yields three frames: %d", len(got))
	}
	if !bytes.Equal(got[0], p1) {
		t.Error("frame 1 intact")
	}
	if !bytes.Equal(got[1], p2) {
		t.Error("frame 2 intact")
	}
	if len(got[2]) != 0 {
		t.Error("empty frame allowed")
	}
	if f.Buffered() != 0 {
		t.Error("nothing left buffered")
	}
	if len(feed(t, NewFramer(1024), all)) != 3 {
		t.Error("all at once yields three frames")
	}
	h := NewFramer(1024)
	if len(feed(t, h, []byte{0, 0})) != 0 || h.Buffered() != 2 {
		t.Error("partial header yields nothing")
	}
	if len(feed(t, h, []byte{0, 2, 1, 2})) != 1 {
		t.Error("header then body completes")
	}
	if _, err := NewFramer(1024).Feed(toBytes(t, NewByteWriter().U32(0x7FFFFFFF))); !errors.Is(err, ErrCodec) {
		t.Error("length prefix above max rejected before allocation")
	}
	if _, err := NewFramer(8).Frame(make([]byte, 9)); !errors.Is(err, ErrCodec) {
		t.Error("payload above max cannot be framed")
	}
}

func TestChannelIsDeterministicOrderedAndSilentWhenCut(t *testing.T) {
	c := NewChannel(NewRng(3), 4, 5)
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	c.Write(data, 0)
	if len(c.Read(0)) != 0 {
		t.Error("nothing readable at t=0")
	}
	if !bytes.Equal(c.Read(1000), data) {
		t.Error("everything readable eventually and in order")
	}
	c2 := NewChannel(NewRng(3), 4, 5)
	c2.Write([]byte{1, 2, 3}, 0)
	c2.Cut()
	c2.Write([]byte{4, 5}, 0)
	if c2.BytesLost != 5 || len(c2.Read(1000)) != 0 {
		t.Error("cut channel: write accepted without error, bytes lost")
	}
	d1, d2 := NewChannel(NewRng(11), 4, 5), NewChannel(NewRng(11), 4, 5)
	d1.Write(make([]byte, 40), 0)
	d2.Write(make([]byte, 40), 0)
	if d1.PendingSegments() != d2.PendingSegments() {
		t.Error("same seed, same segmentation")
	}
	// SplitMix64 parity with the Java demo: seed 42, maxDelay 3, maxSegment 7 delivers the first read at t=3 with 6 bytes.
	ch := NewChannel(NewRng(42), 3, 7)
	ch.Write(make([]byte, 36), 0)
	if len(ch.Read(2)) != 0 || len(ch.Read(3)) != 6 {
		t.Error("seed 42 segments the stream exactly as Java does")
	}
}

func TestEndToEnd(t *testing.T) {
	clock := NewSimClock(0)
	service := NewBalanceService()
	server := NewRpcServer(service, 1<<16)
	client := NewRpcClient(clock, server, NewRng(5), 5, 8, 1<<16)
	if got, err := client.GetBalance("alice", 500); err != nil || got != OfMinor(10_000, USD) {
		t.Errorf("GetBalance returns the seeded balance: %v %v", got, err)
	}
	if got, err := client.Credit("alice", OfMinor(250, USD), 500); err != nil || got != OfMinor(10_250, USD) {
		t.Errorf("Credit returns the new balance: %v %v", got, err)
	}
	if service.CreditsApplied != 1 {
		t.Error("Credit was applied once")
	}
	if _, err := client.Call("Transfer", nil, 500); !errors.Is(err, ErrRemote) {
		t.Errorf("unknown method -> ERROR response, not a hang: %v", err)
	}
	if _, err := client.GetBalance("nobody", 500); !errors.Is(err, ErrRemote) {
		t.Error("unknown account -> ERROR response")
	}
	var remote *RpcRemoteError
	if _, err := client.Credit("alice", OfMinor(1, EUR), 500); !errors.As(err, &remote) || remote.Reason != "cannot combine USD with EUR" {
		t.Errorf("currency mismatch surfaces as an RPC error with the reason: %v", err)
	}
	if server.IsClosed(client.Link()) {
		t.Error("connection still open after application errors")
	}
	if client.NextRequestID() != 6 {
		t.Error("request ids are monotonic")
	}

	// timeout: the deadline fires, the late response is matched by id and discarded
	server.ProcessingMillis = 250
	t0 := clock.Now()
	var timeout *RpcTimeoutError
	if _, err := client.GetBalance("bob", 100); !errors.Is(err, ErrTimeout) || !errors.As(err, &timeout) || timeout.RequestID != 6 {
		t.Errorf("slow server -> timeout: %v", err)
	}
	if clock.Now()-t0 != 100 {
		t.Error("timeout fired at the deadline, not later")
	}
	server.ProcessingMillis = 0
	if got, err := client.GetBalance("bob", 1000); err != nil || got != OfMinor(2_500, USD) {
		t.Error("next call gets its own answer, not the stale one")
	}
	if client.LateResponses != 1 || client.Timeouts != 1 {
		t.Error("the late response was counted and dropped")
	}

	// half-open: silence, not an error; the deadline is the only detector; retry duplicates side effects
	creditsBefore := service.CreditsApplied
	link := client.Link()
	id, err := client.Send("Credit", EncodeCredit("bob", OfMinor(100, USD)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		clock.Advance(1)
		server.Poll(clock.Now())
	}
	link.HalfOpen()
	if service.CreditsApplied != creditsBefore+1 {
		t.Error("server applied the credit before the link died")
	}
	if _, err := client.Await(id, 200); !errors.Is(err, ErrTimeout) {
		t.Error("half-open connection is detected only by the deadline")
	}
	if link.ClientToServer.BytesLost < 0 || !link.ServerToClient.IsCut() {
		t.Error("writes into a half-open link do not fail")
	}
	client.Reconnect()
	if client.Link().ConnectionID == link.ConnectionID || client.Reconnects != 1 {
		t.Error("reconnect is a new connection")
	}
	if _, err := client.Credit("bob", OfMinor(100, USD), 500); err != nil {
		t.Fatal(err)
	}
	if service.CreditsApplied != creditsBefore+2 {
		t.Error("naive retry doubles the credit (the hazard Lesson 09 fixes)")
	}
	if got, _ := service.GetBalance("bob"); got != OfMinor(2_700, USD) {
		t.Error("balance reflects both applications")
	}
}

func TestCorruptStreamClosesConnection(t *testing.T) {
	clock := NewSimClock(0)
	strict := NewRpcServer(NewBalanceService(), 64)
	victim := NewRpcClient(clock, strict, NewRng(9), 2, 64, 64)
	victim.Link().ClientToServer.Write(toBytes(t, NewByteWriter().U32(5_000)), clock.Now())
	for i := 0; i < 10; i++ {
		clock.Advance(1)
		strict.Poll(clock.Now())
	}
	if !strict.IsClosed(victim.Link()) || strict.MalformedFrames != 1 {
		t.Error("oversized frame closes the connection")
	}
}

func TestMoney(t *testing.T) {
	if OfMinor(-1_999, USD).String() != "-19.99 USD" {
		t.Error("String pads minor units and keeps the sign")
	}
	if _, err := OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD)); !errors.Is(err, ErrOverflow) {
		t.Error("Plus reports overflow instead of wrapping")
	}
	if _, err := NewCurrency("usd", 2); !errors.Is(err, ErrInvalidCurrency) {
		t.Error("currency codes are three uppercase letters")
	}
	if _, err := DecodeMoney(NewByteReader(EncodeMoney(OfMinor(5, Currency{"XXX", 9})))); !errors.Is(err, ErrInvalidCurrency) {
		t.Error("a bad exponent on the wire is a decode error")
	}
}
