// Tests for phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

const T0 = int64(1_700_000_000_000)

func hlcNow(t *testing.T, h *HybridLogicalClock) HlcTimestamp {
	t.Helper()
	ts, err := h.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	return ts
}

func hlcRecv(t *testing.T, h *HybridLogicalClock, m HlcTimestamp) HlcTimestamp {
	t.Helper()
	ts, err := h.Receive(m)
	if err != nil {
		t.Fatalf("Receive(%s): %v", m, err)
	}
	return ts
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestDriftingClock(t *testing.T) {
	tt := NewTrueTime(T0)
	exact := NewDriftingClock(tt, 0, 0)
	fast := NewDriftingClock(tt, 50, 0)
	behind := NewDriftingClock(tt, 0, -80)
	if exact.Now() != T0 {
		t.Error("exact clock reads true time")
	}
	if behind.Error() != -80 {
		t.Error("offset clock is behind by 80 ms")
	}
	tt.Advance(86_400_000)
	if fast.Error() != 4320 {
		t.Errorf("50 ppm gains 4320 ms per day: %d", fast.Error())
	}
	if exact.Error() != 0 {
		t.Error("exact clock still exact after a day")
	}
	fast.Step(-fast.Error())
	if fast.Error() != 0 {
		t.Error("step corrects the offset")
	}
	tt.Advance(1_000_000)
	if fast.Error() != 50 {
		t.Error("drift continues after a step")
	}
	defer func() {
		if recover() == nil {
			t.Error("true time never goes backwards")
		}
	}()
	tt.Advance(-1)
}

func TestJavaRoundMatchesMathRound(t *testing.T) {
	// Math.round rounds half up: 2.5 -> 3, -2.5 -> -2. Go's math.Round would give -3 for the second.
	if javaRound(2.5) != 3 || javaRound(-2.5) != -2 || javaRound(0.49) != 0 {
		t.Error("javaRound is floor(x + 0.5)")
	}
}

func TestNtpArithmetic(t *testing.T) {
	cases := []struct {
		name          string
		s             NtpSample
		offset, delay int64
	}{
		{"client 100 behind, 10 ms each way", NtpSample{1000, 1110, 1110, 1020}, 100, 20},
		{"5 ms of server processing is excluded", NtpSample{1000, 1110, 1115, 1025}, 100, 20},
		{"asymmetric path biases offset by half the asymmetry", NtpSample{1000, 1130, 1130, 1040}, 110, 40},
		{"client ahead: offset truncates toward zero like Java", NtpSample{1000, 850, 850, 1021}, -160, 21},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.s.Offset(); got != c.offset {
				t.Errorf("offset = %d, want %d", got, c.offset)
			}
			if got := c.s.Delay(); got != c.delay {
				t.Errorf("delay = %d, want %d", got, c.delay)
			}
		})
	}
	asym := NtpSample{1000, 1130, 1130, 1040}
	if abs(asym.Offset()-100) > asym.Delay()/2 {
		t.Error("offset error is bounded by delay/2")
	}
}

func TestSimulatedExchange(t *testing.T) {
	t2 := NewTrueTime(T0)
	server := NewDriftingClock(t2, 0, 0)
	client := NewDriftingClock(t2, 0, -150)
	symmetric := NewNtpClient(t2, client, server, 1, 10, 0)
	sym := symmetric.Exchange()
	if sym.Offset() != 150 {
		t.Error("symmetric exchange recovers the exact offset")
	}
	if sym.Delay() != 20 {
		t.Error("symmetric exchange delay is the round trip")
	}
	jittery := NewNtpClient(t2, client, server, 99, 10, 60)
	allBounded := true
	var best NtpSample
	for i := 0; i < 20; i++ {
		x := jittery.Exchange()
		if abs(x.Offset()-150) > x.Delay()/2 {
			allBounded = false
		}
		if i == 0 || x.Delay() < best.Delay() {
			best = x
		}
	}
	if !allBounded {
		t.Error("every jittery sample's error is within delay/2")
	}
	if abs(best.Offset()-150) > best.Delay()/2 {
		t.Error("minimum-delay sample is at least as accurate as the bound promises")
	}
	correction := jittery.Synchronize(16)
	if abs(correction-150) > 30 {
		t.Errorf("synchronize applies a correction close to the true offset: %d", correction)
	}
	if abs(client.Error()) > 30 {
		t.Errorf("residual error after synchronize is small: %d", client.Error())
	}
}

func TestJavaRandomParity(t *testing.T) {
	// Seed 7, nextLong(41): the Java demo's jittery latencies are 33, 16, 8, 7 in that order.
	r := NewJavaRandom(7)
	want := []int64{33, 16, 8, 7}
	for i, w := range want {
		if got := r.NextLongBounded(41); got != w {
			t.Errorf("draw %d = %d, want %d", i, got, w)
		}
	}
}

func TestHlcTimestampPackingAndOrdering(t *testing.T) {
	a := HlcTimestamp{T0, 3}
	b := HlcTimestamp{T0, 4}
	c := HlcTimestamp{T0 + 1, 0}
	if !(a.Compare(b) < 0 && b.Compare(c) < 0 && c.Compare(c) == 0) {
		t.Error("HLC compares by l then c")
	}
	if !(a.Pack() < b.Pack() && b.Pack() < c.Pack()) {
		t.Error("pack preserves order")
	}
	if Unpack(c.Pack()) != c || Unpack(a.Pack()) != a {
		t.Error("unpack round-trips")
	}
	if _, err := NewHlcTimestamp(T0, 70_000); !errors.Is(err, ErrCounterOutOfRange) {
		t.Error("counter must fit in 16 bits")
	}
	if _, err := NewHlcTimestamp(logicalMax, 0); !errors.Is(err, ErrLogicalOutOfRange) {
		t.Error("logical time must fit in 48 bits")
	}
	if a.String() != "1700000000000.3" {
		t.Error("String is l.c")
	}
}

func TestHlcAlgorithm(t *testing.T) {
	t3 := NewTrueTime(T0)
	pc := NewDriftingClock(t3, 0, 0)
	hlc := NewHybridLogicalClock(pc, 1000)
	e1 := hlcNow(t, hlc)
	e2 := hlcNow(t, hlc)
	if !(e1.L == T0 && e2.L == T0 && e2.C == e1.C+1) {
		t.Error("same millisecond: l unchanged, c increments")
	}
	t3.Advance(5)
	e3 := hlcNow(t, hlc)
	if !(e3.L == T0+5 && e3.C == 0) {
		t.Error("physical time advanced: l follows pt and c resets")
	}
	if e3.L < pc.Now() {
		t.Error("l is never behind physical time")
	}
	fromFuture := HlcTimestamp{T0 + 300, 2}
	e4 := hlcRecv(t, hlc, fromFuture)
	if !(e4.L == T0+300 && e4.C == 3) {
		t.Error("receive adopts a larger remote l and bumps its counter")
	}
	if e4.Compare(fromFuture) <= 0 {
		t.Error("receive is after the message")
	}
	e5 := hlcNow(t, hlc)
	if !(e5.L == T0+300 && e5.C == 4) {
		t.Error("local event after receive keeps l ahead of pt and increments c")
	}
	e6 := hlcRecv(t, hlc, HlcTimestamp{T0 + 100, 9})
	if !(e6.L == T0+300 && e6.C == 5) {
		t.Error("receive from the past does not move l backwards")
	}
	e7 := hlcRecv(t, hlc, HlcTimestamp{T0 + 300, 40})
	if !(e7.L == T0+300 && e7.C == 41) {
		t.Error("equal l on both sides: c = max(c, c_m) + 1")
	}
	t3.Advance(1000)
	e8 := hlcNow(t, hlc)
	if !(e8.L == T0+1005 && e8.C == 0) {
		t.Error("once pt passes l, l rejoins pt and c resets")
	}
	_, err := hlc.Receive(HlcTimestamp{pc.Now() + 5000, 0})
	var drift *ClockDriftError
	if !errors.Is(err, ErrClockDrift) || !errors.As(err, &drift) || drift.MaxDrift != 1000 {
		t.Errorf("remote timestamp beyond the drift bound is rejected: %v", err)
	}
	if hlc.Peek().L != T0+1005 {
		t.Error("rejected receive did not corrupt the clock")
	}
}

func TestHlcCounterOverflow(t *testing.T) {
	pc := NewDriftingClock(NewTrueTime(T0), 0, 0)
	hlc := NewHybridLogicalClock(pc, 1000)
	if _, err := hlc.Receive(HlcTimestamp{T0, int(CounterMax)}); !errors.Is(err, ErrCounterOverflow) {
		t.Errorf("65,536th event in one millisecond overflows the counter: %v", err)
	}
	if hlc.Peek() != (HlcTimestamp{T0, 0}) {
		t.Error("an overflow leaves the clock untouched")
	}
}

func TestPaymentTrace(t *testing.T) {
	t4 := NewTrueTime(T0)
	gateway := NewNode("gateway", t4, 0, 0, 1000)
	ledger := NewNode("ledger", t4, 0, -80, 1000)
	notifier := NewNode("notifier", t4, 0, 35, 1000)
	trace, err := PaymentTrace(t4, gateway, ledger, notifier)
	if err != nil {
		t.Fatal(err)
	}
	r := trace.Report()
	if r.Pairs != 4 {
		t.Error("trace has four causal pairs")
	}
	if r.WallViolations <= 0 {
		t.Error("wall clocks violate causality on the skewed node")
	}
	if r.LamportViolations != 0 {
		t.Error("Lamport never violates causality")
	}
	if r.HlcViolations != 0 {
		t.Error("HLC never violates causality")
	}
	if r.MaxHlcAheadOfTrueMs > 35 {
		t.Error("HLC stays within the clock skew of true time")
	}
	var last *HlcTimestamp
	for _, e := range trace.Events {
		if e.Node != "ledger" {
			continue
		}
		if last != nil && e.Hlc.Compare(*last) <= 0 {
			t.Error("HLC is strictly monotonic on one node")
		}
		h := e.Hlc
		last = &h
	}
	if r.WallViolations != 2 || len(trace.Events) != 9 || trace.Events[8].Lamport != 9 {
		t.Errorf("trace matches the Java demo: %+v", r)
	}
}
