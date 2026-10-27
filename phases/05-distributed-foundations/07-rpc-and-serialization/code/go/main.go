// Lesson: phases/05-distributed-foundations/07-rpc-and-serialization/docs/en.md
// RPC, Framing, and Serialization Over Raw Sockets
// Run:  go run .
// Test: go test ./...
// Sources: RFC 9293 (TCP: byte stream, no message boundaries, half-open connections, section 3.6.2);
//
//	RFC 8949 (CBOR) and Protocol Buffers encoding docs for the shape of a length-delimited binary codec.
//
// Go 1.22+, standard library only. Sockets are simulated by an in-memory byte-stream pair so every run is deterministic.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)

// Currency is an ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

var (
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("amount overflows int64")
	ErrNoSuchAccount    = errors.New("no such account")
)

// NewCurrency validates the code and exponent, as the Java compact constructor does.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, fmt.Errorf("%w: currency code must be three uppercase letters: %q", ErrInvalidCurrency, code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("%w: exponent must be 0..4: %d", ErrInvalidCurrency, exponent)
	}
	return Currency{code, exponent}, nil
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is an exact integer count of minor units in one currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// ---------------------------------------------------------------------------------------------- deterministic infrastructure

// Rng is SplitMix64: the same stream in every language, so a seed replays everywhere.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{uint64(seed)} }

func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

func (r *Rng) NextInt(bound int) int { return int((uint64(r.NextLong()) >> 33) % uint64(bound)) }

func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * 0x1.0p-53 }

// SimClock is a clock the simulation advances by hand. Milliseconds. Nothing in this lesson sleeps.
type SimClock struct{ now int64 }

func NewSimClock(start int64) *SimClock  { return &SimClock{start} }
func (c *SimClock) Now() int64           { return c.now }
func (c *SimClock) Advance(millis int64) { c.now += millis }

// ---------------------------------------------------------------------------------------------- codec

// ErrCodec marks bytes that do not decode: truncated input, bad tag, negative length. It never leaks as an index panic.
var ErrCodec = errors.New("codec error")

func codecErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCodec, fmt.Sprintf(format, args...))
}

// ByteWriter appends big-endian fixed-width integers and length-prefixed UTF-8 strings to a growable buffer.
// The first range error sticks and is reported by ToBytes, so calls can still be chained like the Java version.
type ByteWriter struct {
	buf []byte
	err error
}

func NewByteWriter() *ByteWriter { return &ByteWriter{buf: make([]byte, 0, 64)} }

func (w *ByteWriter) U8(v int) *ByteWriter {
	if w.err == nil && (v < 0 || v > 0xFF) {
		w.err = codecErr("u8 out of range: %d", v)
	}
	if w.err == nil {
		w.buf = append(w.buf, byte(v))
	}
	return w
}

func (w *ByteWriter) U16(v int) *ByteWriter {
	if w.err == nil && (v < 0 || v > 0xFFFF) {
		w.err = codecErr("u16 out of range: %d", v)
	}
	if w.err == nil {
		w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v))
	}
	return w
}

func (w *ByteWriter) U32(v int64) *ByteWriter {
	if w.err == nil && (v < 0 || v > 0xFFFFFFFF) {
		w.err = codecErr("u32 out of range: %d", v)
	}
	if w.err == nil {
		w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
	}
	return w
}

func (w *ByteWriter) I64(v int64) *ByteWriter {
	if w.err == nil {
		w.buf = binary.BigEndian.AppendUint64(w.buf, uint64(v))
	}
	return w
}

// String writes a u16 byte length followed by UTF-8 bytes. 65535 bytes is the cap; account ids and method names are far smaller.
func (w *ByteWriter) String(s string) *ByteWriter {
	w.U16(len(s))
	if w.err == nil {
		w.buf = append(w.buf, s...)
	}
	return w
}

func (w *ByteWriter) Bytes(b []byte) *ByteWriter {
	if w.err == nil {
		w.buf = append(w.buf, b...)
	}
	return w
}

func (w *ByteWriter) Size() int { return len(w.buf) }

// ToBytes returns a copy of what was written, or the first range error.
func (w *ByteWriter) ToBytes() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	return append([]byte(nil), w.buf...), nil
}

// ByteReader reads what ByteWriter wrote. Every read checks bounds first; the first failure sticks in Err() and later
// reads return zero values, so a decoder reads all its fields and checks once, exactly like a bufio.Scanner.
type ByteReader struct {
	buf []byte
	pos int
	err error
}

func NewByteReader(buf []byte) *ByteReader { return &ByteReader{buf: buf} }

func (r *ByteReader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.pos+n > len(r.buf) {
		r.err = codecErr("truncated: need %d bytes at offset %d, have %d", n, r.pos, len(r.buf)-r.pos)
		return false
	}
	return true
}

func (r *ByteReader) U8() int {
	if !r.need(1) {
		return 0
	}
	v := int(r.buf[r.pos])
	r.pos++
	return v
}

func (r *ByteReader) U16() int {
	if !r.need(2) {
		return 0
	}
	v := int(binary.BigEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return v
}

func (r *ByteReader) U32() int64 {
	if !r.need(4) {
		return 0
	}
	v := int64(binary.BigEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v
}

func (r *ByteReader) I64() int64 {
	if !r.need(8) {
		return 0
	}
	v := int64(binary.BigEndian.Uint64(r.buf[r.pos:]))
	r.pos += 8
	return v
}

func (r *ByteReader) String() string {
	n := r.U16()
	if !r.need(n) {
		return ""
	}
	s := string(r.buf[r.pos : r.pos+n])
	r.pos += n
	return s
}

func (r *ByteReader) Remaining() int { return len(r.buf) - r.pos }

// ExpectEnd records an error if bytes remain unread.
func (r *ByteReader) ExpectEnd() {
	if r.err == nil && r.pos != len(r.buf) {
		r.err = codecErr("trailing bytes: %d", len(r.buf)-r.pos)
	}
}

// Err is the first decoding failure, or nil.
func (r *ByteReader) Err() error { return r.err }

// ---------------------------------------------------------------------------------------------- framing

const Header = 4

// Framer: length-prefixed framing, a 4-byte big-endian payload length then the payload. The reader is incremental: feed
// it whatever bytes the stream produced (half a header, three frames glued together) and it yields complete frames only.
type Framer struct {
	MaxFrame  int
	pending   []byte
	FramesOut int
}

func NewFramer(maxFrame int) *Framer { return &Framer{MaxFrame: maxFrame} }

// FrameWithMax prefixes the payload with its length, refusing payloads above maxFrame.
func FrameWithMax(payload []byte, maxFrame int) ([]byte, error) {
	if len(payload) > maxFrame {
		return nil, codecErr("payload %d exceeds max frame %d", len(payload), maxFrame)
	}
	return NewByteWriter().U32(int64(len(payload))).Bytes(payload).ToBytes()
}

func (f *Framer) Frame(payload []byte) ([]byte, error) { return FrameWithMax(payload, f.MaxFrame) }

// Feed appends stream bytes and returns every frame that is now complete. A hostile length fails before allocating.
func (f *Framer) Feed(chunk []byte) ([][]byte, error) {
	joined := append(f.pending, chunk...)
	var out [][]byte
	pos := 0
	for len(joined)-pos >= Header {
		length := int64(binary.BigEndian.Uint32(joined[pos:]))
		if length > int64(f.MaxFrame) {
			return nil, codecErr("frame length %d exceeds max %d; closing connection", length, f.MaxFrame)
		}
		if int64(len(joined)-pos-Header) < length { // header complete, body not yet
			break
		}
		out = append(out, append([]byte(nil), joined[pos+Header:pos+Header+int(length)]...))
		pos += Header + int(length)
	}
	f.pending = append([]byte(nil), joined[pos:]...)
	f.FramesOut += len(out)
	return out, nil
}

func (f *Framer) Buffered() int { return len(f.pending) }

// ---------------------------------------------------------------------------------------------- RPC messages

type MessageKind int

const (
	Request  MessageKind = 1
	Response MessageKind = 2
	Error    MessageKind = 3
)

func (k MessageKind) String() string {
	switch k {
	case Request:
		return "REQUEST"
	case Response:
		return "RESPONSE"
	case Error:
		return "ERROR"
	}
	return fmt.Sprintf("MessageKind(%d)", int(k))
}

func KindFromTag(tag int) (MessageKind, error) {
	switch MessageKind(tag) {
	case Request, Response, Error:
		return MessageKind(tag), nil
	}
	return 0, codecErr("unknown message kind tag %d", tag)
}

// RpcMessage is one RPC message. Wire layout: u8 kind, i64 requestId, string method, u32 payloadLength, payload bytes.
// The payload is method-specific and encoded by the service, so the envelope never needs to change when a method does.
type RpcMessage struct {
	Kind      MessageKind
	RequestID int64
	Method    string
	Payload   []byte
}

func (m RpcMessage) Encode() ([]byte, error) {
	return NewByteWriter().U8(int(m.Kind)).I64(m.RequestID).String(m.Method).U32(int64(len(m.Payload))).Bytes(m.Payload).ToBytes()
}

func Decode(bytes []byte) (RpcMessage, error) {
	r := NewByteReader(bytes)
	tag := r.U8()
	id := r.I64()
	method := r.String()
	n := r.U32()
	if r.Err() != nil {
		return RpcMessage{}, r.Err()
	}
	kind, err := KindFromTag(tag)
	if err != nil {
		return RpcMessage{}, err
	}
	if n > int64(r.Remaining()) {
		return RpcMessage{}, codecErr("payload length %d exceeds remaining %d", n, r.Remaining())
	}
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(r.U8())
	}
	r.ExpectEnd()
	if r.Err() != nil {
		return RpcMessage{}, r.Err()
	}
	return RpcMessage{kind, id, method, payload}, nil
}

func ErrorMessage(requestID int64, method, reason string) RpcMessage {
	payload, _ := NewByteWriter().String(reason).ToBytes() // a reason is a short string; the u16 cap cannot be hit by our own errors
	return RpcMessage{Error, requestID, method, payload}
}

func (m RpcMessage) ErrorReason() string { return NewByteReader(m.Payload).String() }

// ---------------------------------------------------------------------------------------------- the simulated socket pair

type segment struct {
	arriveAt int64
	bytes    []byte
}

// Channel is one direction of a connection. Writes are segmented into random-sized chunks that arrive after a random
// delay, which is what TCP does to your messages. Cut makes the channel half-open: writes are accepted and vanish,
// reads see nothing.
type Channel struct {
	rng                                     *Rng
	maxDelay, maxSegment                    int
	inFlight                                []segment
	cut                                     bool
	BytesWritten, BytesDelivered, BytesLost int64
}

func NewChannel(rng *Rng, maxDelay, maxSegment int) *Channel {
	return &Channel{rng: rng, maxDelay: max(1, maxDelay), maxSegment: max(1, maxSegment)}
}

func (c *Channel) Write(bytes []byte, now int64) {
	c.BytesWritten += int64(len(bytes))
	if c.cut { // half-open: the peer is gone and nobody told us
		c.BytesLost += int64(len(bytes))
		return
	}
	pos := 0
	arrive := now
	for pos < len(bytes) {
		n := min(len(bytes)-pos, 1+c.rng.NextInt(c.maxSegment))
		arrive += int64(1 + c.rng.NextInt(c.maxDelay)) // segments stay ordered: each arrives after the previous
		c.inFlight = append(c.inFlight, segment{arrive, append([]byte(nil), bytes[pos:pos+n]...)})
		pos += n
	}
}

// Read returns everything that has arrived by now, concatenated. Empty when nothing has: a non-blocking read.
func (c *Channel) Read(now int64) []byte {
	if c.cut {
		return nil
	}
	var out []byte
	for len(c.inFlight) > 0 && c.inFlight[0].arriveAt <= now {
		out = append(out, c.inFlight[0].bytes...)
		c.inFlight = c.inFlight[1:]
	}
	c.BytesDelivered += int64(len(out))
	return out
}

func (c *Channel) Cut() {
	c.cut = true
	for _, s := range c.inFlight {
		c.BytesLost += int64(len(s.bytes))
	}
	c.inFlight = nil
}

func (c *Channel) IsCut() bool          { return c.cut }
func (c *Channel) PendingSegments() int { return len(c.inFlight) }

var nextLinkID = 1

// Link is a connection: two channels. Connection ids let the demo show that a reconnect is a new connection, not a repair.
type Link struct {
	ConnectionID   int
	ClientToServer *Channel
	ServerToClient *Channel
}

func NewLink(rng *Rng, maxDelay, maxSegment int) *Link {
	l := &Link{nextLinkID, NewChannel(rng, maxDelay, maxSegment), NewChannel(rng, maxDelay, maxSegment)}
	nextLinkID++
	return l
}

// HalfOpen simulates the peer vanishing without a FIN or RST: both directions go dark, neither side gets an error.
func (l *Link) HalfOpen() {
	l.ClientToServer.Cut()
	l.ServerToClient.Cut()
}

// ---------------------------------------------------------------------------------------------- the service

// BalanceService is the application: account balances. Deliberately not idempotent so the retry hazard is visible.
type BalanceService struct {
	Balances       map[string]Money
	CreditsApplied int
}

func NewBalanceService() *BalanceService {
	return &BalanceService{Balances: map[string]Money{"alice": OfMinor(10_000, USD), "bob": OfMinor(2_500, USD)}}
}

func (s *BalanceService) GetBalance(account string) (Money, error) {
	m, ok := s.Balances[account]
	if !ok {
		return Money{}, fmt.Errorf("%w: %s", ErrNoSuchAccount, account)
	}
	return m, nil
}

func (s *BalanceService) Credit(account string, amount Money) (Money, error) {
	current, err := s.GetBalance(account)
	if err != nil {
		return Money{}, err
	}
	updated, err := current.Plus(amount)
	if err != nil {
		return Money{}, err
	}
	s.Balances[account] = updated
	s.CreditsApplied++
	return updated, nil
}

// -- payload codecs: the service owns its own wire format; the RPC layer sees opaque bytes.

func EncodeMoney(m Money) []byte {
	b, _ := NewByteWriter().I64(m.MinorUnits).String(m.Currency.Code).U8(m.Currency.Exponent).ToBytes()
	return b
}

func DecodeMoney(r *ByteReader) (Money, error) {
	minor := r.I64()
	code := r.String()
	exponent := r.U8()
	if r.Err() != nil {
		return Money{}, r.Err()
	}
	currency, err := NewCurrency(code, exponent)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(minor, currency), nil
}

func EncodeGetBalance(account string) []byte {
	b, _ := NewByteWriter().String(account).ToBytes()
	return b
}

func EncodeCredit(account string, amount Money) []byte {
	b, _ := NewByteWriter().String(account).Bytes(EncodeMoney(amount)).ToBytes()
	return b
}

// Dispatch by method name. Unknown methods and bad payloads become ERROR responses, never a dropped connection.
func (s *BalanceService) Dispatch(request RpcMessage) RpcMessage {
	result, err := s.handle(request)
	if err != nil {
		return ErrorMessage(request.RequestID, request.Method, errText(err))
	}
	return RpcMessage{Response, request.RequestID, request.Method, EncodeMoney(result)}
}

func (s *BalanceService) handle(request RpcMessage) (Money, error) {
	r := NewByteReader(request.Payload)
	switch request.Method {
	case "GetBalance":
		account := r.String()
		r.ExpectEnd()
		if r.Err() != nil {
			return Money{}, r.Err()
		}
		return s.GetBalance(account)
	case "Credit":
		account := r.String()
		amount, err := DecodeMoney(r)
		if err != nil {
			return Money{}, err
		}
		r.ExpectEnd()
		if r.Err() != nil {
			return Money{}, r.Err()
		}
		return s.Credit(account, amount)
	}
	return Money{}, errors.New("unknown method " + request.Method)
}

// errText strips the sentinel prefix so the wire carries the human-readable part, as e.getMessage() does in Java.
func errText(err error) string {
	msg := err.Error()
	for _, sentinel := range []error{ErrCodec, ErrCurrencyMismatch, ErrInvalidCurrency, ErrNoSuchAccount} {
		if errors.Is(err, sentinel) {
			return strings.TrimPrefix(msg, sentinel.Error()+": ")
		}
	}
	return msg
}

// ---------------------------------------------------------------------------------------------- server and client

type delayedResponse struct {
	readyAt  int64
	link     *Link
	response RpcMessage
}

// RpcServer reads frames off the connection, dispatches, writes responses. One Framer per connection because the
// stream has state. Links are kept in accept order so a poll is deterministic.
type RpcServer struct {
	Service         *BalanceService
	maxFrame        int
	links           []*Link
	framers         map[*Link]*Framer
	RequestsServed  int
	MalformedFrames int
	// ProcessingMillis is extra processing time per request; the simulation uses it to make the server slow.
	ProcessingMillis int64
	delayed          []delayedResponse
}

func NewRpcServer(service *BalanceService, maxFrame int) *RpcServer {
	return &RpcServer{Service: service, maxFrame: maxFrame, framers: map[*Link]*Framer{}}
}

func (s *RpcServer) Accept(link *Link) {
	s.links = append(s.links, link)
	s.framers[link] = NewFramer(s.maxFrame)
}

func (s *RpcServer) IsClosed(link *Link) bool { _, open := s.framers[link]; return !open }

func (s *RpcServer) close(link *Link) {
	delete(s.framers, link)
	for i, l := range s.links {
		if l == link {
			s.links = append(s.links[:i], s.links[i+1:]...)
			break
		}
	}
}

// Poll is one scheduling turn: drain every connection's readable bytes, dispatch complete frames, flush due responses.
func (s *RpcServer) Poll(now int64) {
	for _, link := range append([]*Link(nil), s.links...) {
		chunk := link.ClientToServer.Read(now)
		if len(chunk) == 0 {
			continue
		}
		frames, err := s.framers[link].Feed(chunk)
		if err != nil {
			s.MalformedFrames++
			s.close(link) // a corrupt stream cannot be resynchronised: close it
			continue
		}
		for _, f := range frames {
			request, err := Decode(f)
			if err != nil {
				s.MalformedFrames++
				continue
			}
			response := s.Service.Dispatch(request)
			s.RequestsServed++
			s.delayed = append(s.delayed, delayedResponse{now + s.ProcessingMillis, link, response})
		}
	}
	for len(s.delayed) > 0 && s.delayed[0].readyAt <= now {
		d := s.delayed[0]
		s.delayed = s.delayed[1:]
		if _, open := s.framers[d.link]; open {
			encoded, err := d.response.Encode()
			if err != nil {
				continue
			}
			framed, err := FrameWithMax(encoded, s.maxFrame)
			if err != nil {
				continue
			}
			d.link.ServerToClient.Write(framed, now)
		}
	}
}

var (
	ErrTimeout   = errors.New("rpc timeout")
	ErrRemote    = errors.New("rpc error")
	ErrUnknownID = errors.New("response for unknown id")
)

// RpcTimeoutError says the deadline passed with the outcome unknown; it matches ErrTimeout under errors.Is.
type RpcTimeoutError struct {
	RequestID     int64
	TimeoutMillis int64
	ConnectionID  int
}

func (e *RpcTimeoutError) Error() string {
	return fmt.Sprintf("request %d timed out after %d ms on connection %d (outcome unknown)", e.RequestID, e.TimeoutMillis, e.ConnectionID)
}

func (e *RpcTimeoutError) Is(target error) bool { return target == ErrTimeout }

// RpcRemoteError is an ERROR response from the server; it matches ErrRemote under errors.Is.
type RpcRemoteError struct{ Method, Reason string }

func (e *RpcRemoteError) Error() string        { return e.Method + ": " + e.Reason }
func (e *RpcRemoteError) Is(target error) bool { return target == ErrRemote }

// RpcClient assigns request ids, frames and writes the request, then pumps the simulation until the matching response
// arrives or the deadline passes. Responses whose id is not pending are late answers to timed-out calls and are counted,
// not delivered. The id counter survives reconnects so a late answer from an old connection can never match a new call.
type RpcClient struct {
	clock                          *SimClock
	server                         *RpcServer
	rng                            *Rng
	maxDelay, maxSegment, maxFrame int
	link                           *Link
	framer                         *Framer
	nextRequestID                  int64
	received                       map[int64]RpcMessage
	Timeouts, LateResponses        int
	Reconnects                     int
}

func NewRpcClient(clock *SimClock, server *RpcServer, rng *Rng, maxDelay, maxSegment, maxFrame int) *RpcClient {
	c := &RpcClient{clock: clock, server: server, rng: rng, maxDelay: maxDelay, maxSegment: maxSegment, maxFrame: maxFrame,
		nextRequestID: 1, received: map[int64]RpcMessage{}}
	c.Connect()
	return c
}

func (c *RpcClient) Connect() {
	c.link = NewLink(c.rng, c.maxDelay, c.maxSegment)
	c.framer = NewFramer(c.maxFrame)
	c.server.Accept(c.link)
}

func (c *RpcClient) Reconnect()           { c.Reconnects++; c.Connect() }
func (c *RpcClient) Link() *Link          { return c.link }
func (c *RpcClient) NextRequestID() int64 { return c.nextRequestID }

// Send writes a request without waiting. Returns its id so the caller can await it later (the demo uses this to cut the link mid-call).
func (c *RpcClient) Send(method string, payload []byte) (int64, error) {
	id := c.nextRequestID
	c.nextRequestID++
	encoded, err := RpcMessage{Request, id, method, payload}.Encode()
	if err != nil {
		return 0, err
	}
	framed, err := c.framer.Frame(encoded)
	if err != nil {
		return 0, err
	}
	c.link.ClientToServer.Write(framed, c.clock.Now())
	return id, nil
}

// Await pumps the world until the response for id arrives or the deadline passes.
func (c *RpcClient) Await(id int64, timeoutMillis int64) (RpcMessage, error) {
	deadline := c.clock.Now() + timeoutMillis
	for {
		if err := c.Pump(); err != nil {
			return RpcMessage{}, err
		}
		if r, ok := c.received[id]; ok {
			delete(c.received, id)
			if r.Kind == Error {
				return RpcMessage{}, &RpcRemoteError{r.Method, r.ErrorReason()}
			}
			return r, nil
		}
		if c.clock.Now() >= deadline {
			c.Timeouts++
			return RpcMessage{}, &RpcTimeoutError{id, timeoutMillis, c.link.ConnectionID}
		}
		c.clock.Advance(1)
	}
}

func (c *RpcClient) Call(method string, payload []byte, timeoutMillis int64) (RpcMessage, error) {
	id, err := c.Send(method, payload)
	if err != nil {
		return RpcMessage{}, err
	}
	return c.Await(id, timeoutMillis)
}

// Pump advances the world one step: the server runs, then we read whatever the server-to-client channel has delivered.
func (c *RpcClient) Pump() error {
	c.server.Poll(c.clock.Now())
	chunk := c.link.ServerToClient.Read(c.clock.Now())
	if len(chunk) == 0 {
		return nil
	}
	frames, err := c.framer.Feed(chunk)
	if err != nil {
		return err
	}
	for _, f := range frames {
		m, err := Decode(f)
		if err != nil {
			return err
		}
		if _, dup := c.received[m.RequestID]; m.RequestID >= c.nextRequestID || dup {
			return fmt.Errorf("%w: %d", ErrUnknownID, m.RequestID)
		}
		c.received[m.RequestID] = m
	}
	// Anything buffered for an id that has already timed out is a late response: count and drop it.
	for rid := range c.received {
		if rid < c.currentCallID() {
			c.LateResponses++
			delete(c.received, rid)
		}
	}
	return nil
}

func (c *RpcClient) currentCallID() int64 { return c.nextRequestID - 1 }

// -- typed wrappers so callers never touch bytes

func (c *RpcClient) GetBalance(account string, timeout int64) (Money, error) {
	r, err := c.Call("GetBalance", EncodeGetBalance(account), timeout)
	if err != nil {
		return Money{}, err
	}
	return DecodeMoney(NewByteReader(r.Payload))
}

func (c *RpcClient) Credit(account string, amount Money, timeout int64) (Money, error) {
	r, err := c.Call("Credit", EncodeCredit(account, amount), timeout)
	if err != nil {
		return Money{}, err
	}
	return DecodeMoney(NewByteReader(r.Payload))
}

// ---------------------------------------------------------------------------------------------- demo

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== The wire format: one GetBalance request, framed ==")
	req := RpcMessage{Request, 1, "GetBalance", EncodeGetBalance("alice")}
	body := must(req.Encode())
	framed := must(FrameWithMax(body, 1<<20))
	fmt.Printf("payload  (%d bytes): %s\n", len(body), hex.EncodeToString(body))
	fmt.Printf("framed   (%d bytes): %s\n", len(framed), hex.EncodeToString(framed))
	fmt.Printf("  = u32 length %d | u8 kind=1 | i64 requestId=1 | u16 len=10 'GetBalance' | u32 payloadLen=7 | u16 len=5 'alice'\n", len(body))
	back := must(Decode(body))
	fmt.Printf("decodes back to %s id=%d method=%s\n", back.Kind, back.RequestID, back.Method)

	fmt.Println()
	fmt.Println("== TCP does not respect your message boundaries: 3 frames, segmented into random chunks ==")
	rng := NewRng(42)
	ch := NewChannel(rng, 3, 7)
	reader := NewFramer(1 << 20)
	for i := int64(1); i <= 3; i++ {
		ch.Write(must(FrameWithMax(must(RpcMessage{Request, i, "GetBalance", EncodeGetBalance("bob")}.Encode()), 1<<20)), 0)
	}
	chunks, frames := 0, 0
	for t := int64(1); t <= 200 && frames < 3; t++ {
		chunk := ch.Read(t)
		if len(chunk) == 0 {
			continue
		}
		chunks++
		got := must(reader.Feed(chunk))
		frames += len(got)
		fmt.Printf("t=%-3d read %2d bytes -> %d complete frame(s), %2d bytes buffered\n", t, len(chunk), len(got), reader.Buffered())
	}
	fmt.Printf("%d frames reassembled from %d reads; no frame was ever half-decoded\n", frames, chunks)

	fmt.Println()
	fmt.Println("== A hostile length prefix is rejected before any allocation ==")
	if _, err := NewFramer(1 << 20).Feed(must(NewByteWriter().U32(0xFFFFFFFF).ToBytes())); err != nil {
		fmt.Println("rejected: " + errText(err))
	}
	if _, err := Decode(body[:len(body)-3]); err != nil {
		fmt.Println("truncated message rejected: " + errText(err))
	}

	fmt.Println()
	fmt.Println("== Request/response over the simulated socket pair (seed 7, delay 1-5 ms per segment) ==")
	clock := NewSimClock(0)
	service := NewBalanceService()
	server := NewRpcServer(service, 1<<20)
	client := NewRpcClient(clock, server, NewRng(7), 5, 16, 1<<20)
	t0 := clock.Now()
	alice := must(client.GetBalance("alice", 500))
	fmt.Printf("GetBalance(alice) = %s in %d ms, connection %d\n", alice, clock.Now()-t0, client.Link().ConnectionID)
	t0 = clock.Now()
	after := must(client.Credit("alice", OfMinor(1_999, USD), 500))
	fmt.Printf("Credit(alice, 19.99 USD) = %s in %d ms\n", after, clock.Now()-t0)
	if _, err := client.Call("Transfer", nil, 500); errors.Is(err, ErrRemote) {
		fmt.Println("server-side error travels back with the same request id: " + err.Error())
	}

	fmt.Println()
	fmt.Println("== Timeout: the server is slow, the client gives up, the answer arrives later and is discarded ==")
	server.ProcessingMillis = 300
	t0 = clock.Now()
	if _, err := client.GetBalance("bob", 100); errors.Is(err, ErrTimeout) {
		fmt.Printf("t=%d: %s\n", clock.Now()-t0, err.Error())
	}
	server.ProcessingMillis = 0
	bob := must(client.GetBalance("bob", 1000))
	fmt.Printf("next call still gets the right answer: GetBalance(bob) = %s; late responses discarded so far = %d\n", bob, client.LateResponses)

	fmt.Println()
	fmt.Println("== Half-open: the server vanishes mid-Credit; the client's socket shows no error, only silence ==")
	creditsBefore := service.CreditsApplied
	doomed := client.Link()
	inFlight := must(client.Send("Credit", EncodeCredit("bob", OfMinor(5_000, USD))))
	for i := 0; i < 60; i++ { // request arrives and is applied...
		clock.Advance(1)
		server.Poll(clock.Now())
	}
	doomed.HalfOpen() // ...and the reply never makes it back
	fmt.Printf("server applied the credit (creditsApplied %d -> %d), bob = %s\n", creditsBefore, service.CreditsApplied, must(service.GetBalance("bob")))
	fmt.Printf("then the link went half-open: %d reply bytes lost, client sees nothing\n", doomed.ServerToClient.BytesLost)
	probe := must(client.Send("GetBalance", EncodeGetBalance("bob"))) // a write into the void: no error
	fmt.Printf("a further write of request %d 'succeeds': %d bytes accepted by the socket and lost; reads return 0 bytes forever\n", probe, doomed.ClientToServer.BytesLost)
	if _, err := client.Await(inFlight, 200); errors.Is(err, ErrTimeout) {
		fmt.Println("client: " + err.Error() + "; only the deadline detects a half-open connection")
	}
	client.Reconnect()
	fmt.Printf("reconnect -> new connection %d (ids continue from %d, so no old reply can match)\n", client.Link().ConnectionID, client.NextRequestID())
	retried := must(client.Credit("bob", OfMinor(5_000, USD), 500))
	fmt.Printf("naive retry of Credit(bob, 50.00 USD) = %s  <- applied %d times for one intended credit. A timeout means UNKNOWN, not FAILED. Lesson 09 (idempotency keys) is the fix.\n",
		retried, service.CreditsApplied-creditsBefore)
}
