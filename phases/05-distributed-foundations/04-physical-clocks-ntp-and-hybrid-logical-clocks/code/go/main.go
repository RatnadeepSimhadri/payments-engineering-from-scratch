// Lesson: phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks/docs/en.md
// Physical Clocks, NTP, and Hybrid Logical Clocks
// Run:  go run .
// Test: go test ./...
// Sources: RFC 5905 (NTPv4) section 8 for the offset/delay equations; Kulkarni, Demirbas, Madeppa, Avva, Leone,
//
//	"Logical Physical Clocks" (OPODIS 2014) for the HLC algorithm; Lamport 1978 for logical clocks. Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"math"
)

// TrueTime is the one true time of the simulation, in milliseconds. Nothing in the lesson reads time.Now();
// every clock derives its reading from this value, so every run is deterministic and replayable.
type TrueTime struct{ nowMs int64 }

func NewTrueTime(startMs int64) *TrueTime { return &TrueTime{startMs} }

func (t *TrueTime) Now() int64 { return t.nowMs }

// Advance moves true time forward. Moving it backwards is a programming error and panics.
func (t *TrueTime) Advance(ms int64) {
	if ms < 0 {
		panic("time does not go backwards")
	}
	t.nowMs += ms
}

// DriftingClock is a physical clock on one machine: true time distorted by a constant rate error (drift, parts per
// million) and an additive offset. A quartz oscillator drifts 10 to 100 ppm; a 50 ppm clock gains 4.3 s per day.
// Step models NTP correcting the offset; the drift stays, which is why correction is never finished.
type DriftingClock struct {
	trueTime    *TrueTime
	driftPpm    float64
	offsetMs    int64
	epochTrueMs int64
}

func NewDriftingClock(trueTime *TrueTime, driftPpm float64, initialOffsetMs int64) *DriftingClock {
	return &DriftingClock{trueTime, driftPpm, initialOffsetMs, trueTime.Now()}
}

// javaRound is Math.round: floor(x + 0.5), not Go's half-away-from-zero math.Round.
func javaRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// Now is what this machine believes the time is. Rounded to whole milliseconds like a real clock read.
func (c *DriftingClock) Now() int64 {
	elapsed := c.trueTime.Now() - c.epochTrueMs
	drifted := javaRound(float64(elapsed) * (c.driftPpm / 1_000_000.0))
	return c.epochTrueMs + elapsed + drifted + c.offsetMs
}

// Step applies a correction, as NTP does when the offset is large: the clock jumps. Negative deltas move it backwards.
func (c *DriftingClock) Step(deltaMs int64) { c.offsetMs += deltaMs }

func (c *DriftingClock) Error() int64 { return c.Now() - c.trueTime.Now() }

// NtpSample is one NTP exchange, RFC 5905 section 8: four timestamps, two derived quantities.
type NtpSample struct{ T1, T2, T3, T4 int64 }

// Offset is how far the client clock is behind the server (positive means client is behind). Go's / truncates like Java's.
func (s NtpSample) Offset() int64 { return ((s.T2 - s.T1) + (s.T3 - s.T4)) / 2 }

// Delay is the round-trip delay excluding the server's processing time. The offset error is at most delay/2.
func (s NtpSample) Delay() int64 { return (s.T4 - s.T1) - (s.T3 - s.T2) }

// NtpClient polls a server over a network with seeded, possibly asymmetric latency and keeps the sample with the
// smallest delay, which is NTP's clock-filter rule: the fastest round trip is the one least contaminated by queueing.
type NtpClient struct {
	trueTime     *TrueTime
	client       *DriftingClock
	server       *DriftingClock
	random       *JavaRandom
	minLatencyMs int64
	jitterMs     int64
}

func NewNtpClient(trueTime *TrueTime, client, server *DriftingClock, seed, minLatencyMs, jitterMs int64) *NtpClient {
	return &NtpClient{trueTime, client, server, NewJavaRandom(seed), minLatencyMs, jitterMs}
}

func (n *NtpClient) latency() int64 {
	if n.jitterMs == 0 {
		return n.minLatencyMs
	}
	return n.minLatencyMs + n.random.NextLongBounded(n.jitterMs+1)
}

// Exchange is one request/response with independently drawn outbound and return latencies.
func (n *NtpClient) Exchange() NtpSample {
	outbound := n.latency()
	inbound := n.latency()
	t1 := n.client.Now()
	n.trueTime.Advance(outbound)
	t2 := n.server.Now()
	t3 := n.server.Now() // zero processing time keeps the arithmetic legible
	n.trueTime.Advance(inbound)
	t4 := n.client.Now()
	return NtpSample{t1, t2, t3, t4}
}

// BestOf polls n times and returns the sample with the smallest delay (ties: earliest).
func (n *NtpClient) BestOf(polls int) NtpSample {
	var best NtpSample
	for i := 0; i < polls; i++ {
		s := n.Exchange()
		if i == 0 || s.Delay() < best.Delay() {
			best = s
		}
	}
	return best
}

// Synchronize measures and corrects the client clock by the best offset estimate. Returns the applied correction.
func (n *NtpClient) Synchronize(polls int) int64 {
	best := n.BestOf(polls)
	n.client.Step(best.Offset())
	return best.Offset()
}

// LamportClock: ordering with no relation to physical time. Here only for the comparison trace.
type LamportClock struct{ time int64 }

func (l *LamportClock) Tick() int64 { l.time++; return l.time }

func (l *LamportClock) Receive(remote int64) int64 {
	l.time = max(l.time, remote) + 1
	return l.time
}

func (l *LamportClock) Peek() int64 { return l.time }

// HlcTimestamp: L is the logical wall time (never behind the physical clock, never more than epsilon ahead),
// C is a counter that breaks ties among events with the same L. Compared lexicographically.
// Packs into 64 bits: 48 for L (enough for 8,900 years of milliseconds) and 16 for C (65,535 events per ms).
type HlcTimestamp struct {
	L int64
	C int
}

const (
	CounterBits = 16
	CounterMax  = int64(1)<<CounterBits - 1
	logicalMax  = int64(1) << 48
)

var (
	ErrCounterOutOfRange = errors.New("counter out of range")
	ErrLogicalOutOfRange = errors.New("logical time out of range")
	ErrCounterOverflow   = errors.New("HLC counter overflow: more than 65535 events in one millisecond")
	ErrClockDrift        = errors.New("clock drift bound exceeded")
)

// NewHlcTimestamp validates the 48-bit L and 16-bit C bounds that make Pack lossless.
func NewHlcTimestamp(l int64, c int) (HlcTimestamp, error) {
	if c < 0 || int64(c) > CounterMax {
		return HlcTimestamp{}, fmt.Errorf("%w: %d", ErrCounterOutOfRange, c)
	}
	if l < 0 || l >= logicalMax {
		return HlcTimestamp{}, fmt.Errorf("%w: %d", ErrLogicalOutOfRange, l)
	}
	return HlcTimestamp{l, c}, nil
}

func (t HlcTimestamp) Pack() int64 { return t.L<<CounterBits | int64(t.C) }

func Unpack(packed int64) HlcTimestamp {
	return HlcTimestamp{int64(uint64(packed) >> CounterBits), int(packed & CounterMax)}
}

func (t HlcTimestamp) Compare(o HlcTimestamp) int {
	switch {
	case t.L < o.L:
		return -1
	case t.L > o.L:
		return 1
	case t.C < o.C:
		return -1
	case t.C > o.C:
		return 1
	}
	return 0
}

func (t HlcTimestamp) String() string { return fmt.Sprintf("%d.%d", t.L, t.C) }

// ClockDriftError is returned when the physical clock has drifted so far that L would run more than maxDrift ahead of it.
type ClockDriftError struct{ L, Pt, MaxDrift int64 }

func (e *ClockDriftError) Error() string {
	return fmt.Sprintf("logical time %d is %d ms ahead of physical time %d; bound is %d", e.L, e.L-e.Pt, e.Pt, e.MaxDrift)
}

func (e *ClockDriftError) Is(target error) bool { return target == ErrClockDrift }

// HybridLogicalClock, Kulkarni et al. 2014, Figure 5. Each node holds (l, c). Two rules:
//   - send/local: l' = max(l, pt); c' = (l' == l) ? c + 1 : 0
//   - receive(m): l' = max(l, m.l, pt); c' depends on which of the three l's won the max (see Receive)
//
// Invariants: l >= pt always (never behind physical time); l - pt <= maxDrift (never far ahead);
// e happens-before f implies timestamp(e) < timestamp(f).
type HybridLogicalClock struct {
	physical   *DriftingClock
	maxDriftMs int64
	l          int64
	c          int
}

func NewHybridLogicalClock(physical *DriftingClock, maxDriftMs int64) *HybridLogicalClock {
	return &HybridLogicalClock{physical: physical, maxDriftMs: maxDriftMs, l: physical.Now()}
}

// Now timestamps a local event or an outgoing message.
func (h *HybridLogicalClock) Now() (HlcTimestamp, error) {
	pt := h.physical.Now()
	newL := max(h.l, pt)
	newC := 0
	if newL == h.l {
		var err error
		if newC, err = bumpCounter(h.c); err != nil {
			return HlcTimestamp{}, err
		}
	}
	return h.commit(newL, newC, pt)
}

// Receive merges an incoming message's timestamp, then timestamps the receive event.
func (h *HybridLogicalClock) Receive(m HlcTimestamp) (HlcTimestamp, error) {
	pt := h.physical.Now()
	newL := max(h.l, m.L, pt)
	var newC int
	var err error
	switch {
	case newL == h.l && newL == m.L:
		newC, err = bumpCounter(max(h.c, m.C))
	case newL == h.l:
		newC, err = bumpCounter(h.c)
	case newL == m.L:
		newC, err = bumpCounter(m.C)
	default:
		newC = 0
	}
	if err != nil {
		return HlcTimestamp{}, err
	}
	return h.commit(newL, newC, pt)
}

func (h *HybridLogicalClock) Peek() HlcTimestamp { return HlcTimestamp{h.l, h.c} }

func bumpCounter(c int) (int, error) {
	if int64(c) >= CounterMax {
		return 0, ErrCounterOverflow
	}
	return c + 1, nil
}

// commit validates before mutating, so a rejected timestamp leaves the clock exactly as it was.
func (h *HybridLogicalClock) commit(newL int64, newC int, pt int64) (HlcTimestamp, error) {
	if newL-pt > h.maxDriftMs {
		return HlcTimestamp{}, &ClockDriftError{newL, pt, h.maxDriftMs}
	}
	h.l, h.c = newL, newC
	return HlcTimestamp{h.l, h.c}, nil
}

// TraceEvent is one stamped event in the comparison trace.
type TraceEvent struct {
	Node    string
	What    string
	TrueMs  int64
	WallMs  int64
	Lamport int64
	Hlc     HlcTimestamp
}

// Node has all three clocks, so the same event can be stamped three ways and the stamps compared.
type Node struct {
	Name     string
	Wall     *DriftingClock
	Lamport  *LamportClock
	Hlc      *HybridLogicalClock
	trueTime *TrueTime
}

func NewNode(name string, trueTime *TrueTime, driftPpm float64, offsetMs, maxDriftMs int64) *Node {
	wall := NewDriftingClock(trueTime, driftPpm, offsetMs)
	return &Node{name, wall, &LamportClock{}, NewHybridLogicalClock(wall, maxDriftMs), trueTime}
}

func (n *Node) Local(what string) (TraceEvent, error) {
	hlc, err := n.Hlc.Now()
	if err != nil {
		return TraceEvent{}, err
	}
	return TraceEvent{n.Name, what, n.trueTime.Now(), n.Wall.Now(), n.Lamport.Tick(), hlc}, nil
}

func (n *Node) Send(what string) (TraceEvent, error) { return n.Local("send " + what) }

func (n *Node) Receive(what string, message TraceEvent) (TraceEvent, error) {
	hlc, err := n.Hlc.Receive(message.Hlc)
	if err != nil {
		return TraceEvent{}, err
	}
	return TraceEvent{n.Name, "recv " + what, n.trueTime.Now(), n.Wall.Now(), n.Lamport.Receive(message.Lamport), hlc}, nil
}

// TraceReport: causal pairs (send, receive) and the three orderings' verdicts on them.
type TraceReport struct {
	Pairs               int
	WallViolations      int
	LamportViolations   int
	HlcViolations       int
	MaxHlcAheadOfTrueMs int64
}

type Trace struct {
	Events      []TraceEvent
	CausalPairs [][2]TraceEvent
}

func (t *Trace) Record(e TraceEvent, err error) (TraceEvent, error) {
	if err != nil {
		return TraceEvent{}, err
	}
	t.Events = append(t.Events, e)
	return e, nil
}

func (t *Trace) Deliver(to *Node, what string, sent TraceEvent) (TraceEvent, error) {
	received, err := t.Record(to.Receive(what, sent))
	if err != nil {
		return TraceEvent{}, err
	}
	t.CausalPairs = append(t.CausalPairs, [2]TraceEvent{sent, received})
	return received, nil
}

func (t *Trace) Report() TraceReport {
	wall, lamport, hlc := 0, 0, 0
	for _, p := range t.CausalPairs {
		if !(p[0].WallMs < p[1].WallMs) {
			wall++
		}
		if !(p[0].Lamport < p[1].Lamport) {
			lamport++
		}
		if !(p[0].Hlc.Compare(p[1].Hlc) < 0) {
			hlc++
		}
	}
	var maxAhead int64
	for _, e := range t.Events {
		maxAhead = max(maxAhead, e.Hlc.L-e.TrueMs)
	}
	return TraceReport{len(t.CausalPairs), wall, lamport, hlc, maxAhead}
}

// PaymentTrace: three services, one payment: gateway authorises, ledger posts, notifier emails. Clocks disagree by up to 80 ms.
func PaymentTrace(t *TrueTime, gateway, ledger, notifier *Node) (*Trace, error) {
	trace := &Trace{}
	auth, err := trace.Record(gateway.Send("authorize pay_7f3"))
	if err != nil {
		return nil, err
	}
	t.Advance(4)
	if _, err = trace.Deliver(ledger, "authorize pay_7f3", auth); err != nil {
		return nil, err
	}
	t.Advance(2)
	postAck, err := trace.Record(ledger.Send("posted pay_7f3"))
	if err != nil {
		return nil, err
	}
	t.Advance(3)
	if _, err = trace.Deliver(gateway, "posted pay_7f3", postAck); err != nil {
		return nil, err
	}
	t.Advance(1)
	capture, err := trace.Record(gateway.Send("capture pay_7f3"))
	if err != nil {
		return nil, err
	}
	t.Advance(5)
	if _, err = trace.Deliver(ledger, "capture pay_7f3", capture); err != nil {
		return nil, err
	}
	t.Advance(2)
	notify, err := trace.Record(ledger.Send("notify pay_7f3"))
	if err != nil {
		return nil, err
	}
	t.Advance(6)
	if _, err = trace.Deliver(notifier, "notify pay_7f3", notify); err != nil {
		return nil, err
	}
	t.Advance(1)
	if _, err = trace.Record(notifier.Local("email sent")); err != nil {
		return nil, err
	}
	return trace, nil
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	const T0 = int64(1_700_000_000_000)
	fmt.Println("== A quartz clock drifts ==")
	t := NewTrueTime(T0)
	fast := NewDriftingClock(t, 50, 0)
	t.Advance(86_400_000)
	fmt.Printf("50 ppm clock after one day: %d ms fast\n", fast.Error())

	fmt.Println("\n== NTP: estimating offset from four timestamps ==")
	t2 := NewTrueTime(T0)
	server := NewDriftingClock(t2, 0, 0)
	client := NewDriftingClock(t2, 20, -150)
	ntp := NewNtpClient(t2, client, server, 42, 10, 0)
	symmetric := ntp.Exchange()
	fmt.Printf("symmetric 10 ms each way: offset=%d ms (true -150), delay=%d ms\n", symmetric.Offset(), symmetric.Delay())
	jittery := NewNtpClient(t2, client, server, 7, 10, 40)
	for i := 0; i < 3; i++ {
		s := jittery.Exchange()
		fmt.Printf("jittery sample %d: offset=%d ms, delay=%d ms, error=%d ms (bound delay/2=%d)\n",
			i+1, s.Offset(), s.Delay(), s.Offset()-(-client.Error()), s.Delay()/2)
	}
	applied := jittery.Synchronize(8)
	fmt.Printf("best-of-8 correction applied: %d ms; residual client error after step: %d ms\n", applied, client.Error())

	fmt.Println("\n== Three clocks on one payment trace ==")
	t3 := NewTrueTime(T0)
	gateway := NewNode("gateway", t3, 0, 0, 1000)
	ledger := NewNode("ledger", t3, 0, -80, 1000) // ledger's wall clock is 80 ms behind
	notifier := NewNode("notifier", t3, 0, 35, 1000)
	trace := must(PaymentTrace(t3, gateway, ledger, notifier))
	fmt.Printf("%-9s %-22s %-8s %-8s %-8s %s\n", "node", "event", "true", "wall", "lamport", "hlc (l shown relative to T0)")
	for _, e := range trace.Events {
		fmt.Printf("%-9s %-22s %-8d %-8d %-8d %d.%d\n", e.Node, e.What, e.TrueMs-T0, e.WallMs-T0, e.Lamport, e.Hlc.L-T0, e.Hlc.C)
	}
	r := trace.Report()
	fmt.Printf("causal pairs: %d; wall-clock order violated: %d; Lamport violated: %d; HLC violated: %d\n",
		r.Pairs, r.WallViolations, r.LamportViolations, r.HlcViolations)
	fmt.Printf("HLC stayed within %d ms of true time; Lamport's largest value was %d (unrelated to time)\n",
		r.MaxHlcAheadOfTrueMs, trace.Events[len(trace.Events)-1].Lamport)

	fmt.Println("\n== Bounded drift: HLC refuses to run away from physical time ==")
	t4 := NewTrueTime(T0)
	slow := NewDriftingClock(t4, 0, 0)
	hlc := NewHybridLogicalClock(slow, 500)
	if _, err := hlc.Receive(HlcTimestamp{slow.Now() + 2_000, 0}); errors.Is(err, ErrClockDrift) {
		fmt.Println("rejected: " + err.Error())
	}
	packed := must(NewHlcTimestamp(1_700_000_000_123, 7))
	fmt.Printf("pack(%s) = %d, unpack round-trips: %s\n", packed, packed.Pack(), Unpack(packed.Pack()))
}
