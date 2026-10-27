// Lesson: phases/09-reliability-and-scale/07-distributed-tracing/docs/en.md
// Distributed Tracing and Context Propagation: trace/span ids, W3C traceparent, parent/child spans, exclusive time
// Run:  go run .
// Test: go test ./...
// Sources: W3C Trace Context Level 1 (https://www.w3.org/TR/trace-context/); OpenTelemetry Tracing API spec;
//
//	Sigelman et al., "Dapper, a Large-Scale Distributed Systems Tracing Infrastructure" (Google, 2010). Go 1.22+, stdlib only.
package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Clock is the injected time source. Spans record start and end from it; tests advance it by exact amounts.
type Clock interface {
	NowMillis() int64
}

// FakeClock is a clock moved by hand.
type FakeClock struct{ now int64 }

func NewFakeClock(start int64) *FakeClock { return &FakeClock{now: start} }

func (c *FakeClock) NowMillis() int64 { return c.now }

// Advance moves time forward; a negative step is a programming error and panics.
func (c *FakeClock) Advance(millis int64) {
	if millis < 0 {
		panic("time does not go backwards")
	}
	c.now += millis
}

// IdGenerator is a seeded id generator: 16-byte trace ids and 8-byte span ids as lowercase hex, never all zeros.
type IdGenerator struct{ random *JavaRandom }

func NewIdGenerator(seed int64) *IdGenerator { return &IdGenerator{NewJavaRandom(seed)} }

func (g *IdGenerator) TraceId() string { return g.hex(16) }
func (g *IdGenerator) SpanId() string  { return g.hex(8) }

func (g *IdGenerator) hex(n int) string {
	for {
		buf := make([]byte, n)
		g.random.NextBytes(buf)
		allZero := true
		for _, b := range buf {
			if b != 0 {
				allZero = false
			}
		}
		if !allZero {
			return hex.EncodeToString(buf)
		}
	}
}

// Header names for the W3C Trace Context carrier.
const (
	Header      = "traceparent"
	StateHeader = "tracestate"
)

var (
	zeroTraceId = strings.Repeat("0", 32)
	zeroSpanId  = strings.Repeat("0", 16)
)

// SpanContext is the part of a span that crosses process boundaries. Serialized as the W3C traceparent header:
// version-traceid-parentid-flags, e.g. 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
type SpanContext struct {
	TraceId string
	SpanId  string
	Sampled bool
}

// NewSpanContext validates both ids: 32 and 16 lowercase hex characters, never all zeros.
func NewSpanContext(traceId, spanId string, sampled bool) (SpanContext, error) {
	if !IsHex(traceId, 32) || traceId == zeroTraceId {
		return SpanContext{}, fmt.Errorf("bad trace id: %s", traceId)
	}
	if !IsHex(spanId, 16) || spanId == zeroSpanId {
		return SpanContext{}, fmt.Errorf("bad span id: %s", spanId)
	}
	return SpanContext{traceId, spanId, sampled}, nil
}

func IsHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for i := 0; i < length; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (c SpanContext) ToTraceparent() string {
	flags := "00"
	if c.Sampled {
		flags = "01"
	}
	return "00-" + c.TraceId + "-" + c.SpanId + "-" + flags
}

// String renders the context the way the Java record does.
func (c SpanContext) String() string {
	return fmt.Sprintf("SpanContext[traceId=%s, spanId=%s, sampled=%t]", c.TraceId, c.SpanId, c.Sampled)
}

// ParseTraceparent is a strict parse per W3C Trace Context Level 1; it returns false on any violation so the
// receiver starts a new trace. An absent header is the empty string.
func ParseTraceparent(header string) (SpanContext, bool) {
	h := strings.TrimSpace(header)
	parts := strings.Split(h, "-")
	if len(parts) < 4 {
		return SpanContext{}, false
	}
	version := parts[0]
	if !IsHex(version, 2) || version == "ff" {
		return SpanContext{}, false
	}
	if version == "00" && len(parts) != 4 { // version 00 has exactly four fields
		return SpanContext{}, false
	}
	if !IsHex(parts[1], 32) || parts[1] == zeroTraceId {
		return SpanContext{}, false
	}
	if !IsHex(parts[2], 16) || parts[2] == zeroSpanId {
		return SpanContext{}, false
	}
	if !IsHex(parts[3], 2) {
		return SpanContext{}, false
	}
	flags, err := strconv.ParseUint(parts[3], 16, 8)
	if err != nil {
		return SpanContext{}, false
	}
	return SpanContext{parts[1], parts[2], flags&0x01 == 1}, true
}

// ErrSpanOpen is returned when a span that has not ended is measured or exported.
var ErrSpanOpen = errors.New("span still open")

// Span is one unit of work. Mutable while open; immutable in effect once ended.
type Span struct {
	TraceId      string
	SpanId       string
	ParentSpanId string // "" for a root span
	Service      string
	Name         string
	StartMillis  int64
	EndMillis    int64
	attrKeys     []string
	Attributes   map[string]string
	Status       string
}

func NewSpan(traceId, spanId, parentSpanId, service, name string, startMillis int64) *Span {
	return &Span{TraceId: traceId, SpanId: spanId, ParentSpanId: parentSpanId, Service: service, Name: name,
		StartMillis: startMillis, EndMillis: -1, Attributes: map[string]string{}, Status: "OK"}
}

func (s *Span) IsRoot() bool { return s.ParentSpanId == "" }
func (s *Span) Ended() bool  { return s.EndMillis >= 0 }

// DurationMillis is end minus start; an open span has no duration and returns ErrSpanOpen.
func (s *Span) DurationMillis() (int64, error) {
	if !s.Ended() {
		return 0, fmt.Errorf("%w: %s", ErrSpanOpen, s.Name)
	}
	return s.EndMillis - s.StartMillis, nil
}

// Context is the propagated part of this span; the ids are known-good so no validation is needed.
func (s *Span) Context(sampled bool) SpanContext { return SpanContext{s.TraceId, s.SpanId, sampled} }

func (s *Span) SetAttribute(key, value string) {
	if _, ok := s.Attributes[key]; !ok {
		s.attrKeys = append(s.attrKeys, key)
	}
	s.Attributes[key] = value
}

// AttributeKeys is the insertion order of SetAttribute calls, as the Java LinkedHashMap keeps it.
func (s *Span) AttributeKeys() []string { return append([]string(nil), s.attrKeys...) }

func (s *Span) SetError(message string) {
	s.Status = "ERROR"
	s.SetAttribute("error.message", message)
}

// Collector receives finished spans. In production this is the exporter to Jaeger, Tempo, or a vendor; here it is a list.
type Collector struct{ spans []*Span }

func (c *Collector) Export(span *Span) error {
	if !span.Ended() {
		return fmt.Errorf("cannot export an open span: %w", ErrSpanOpen)
	}
	c.spans = append(c.spans, span)
	return nil
}

func (c *Collector) SpansFor(traceId string) []*Span {
	var out []*Span
	for _, s := range c.spans {
		if s.TraceId == traceId {
			out = append(out, s)
		}
	}
	return out
}

func (c *Collector) All() []*Span { return append([]*Span(nil), c.spans...) }

// TraceIds returns the distinct trace ids in first-seen order (the Java LinkedHashSet).
func (c *Collector) TraceIds() []string {
	seen := map[string]bool{}
	var ids []string
	for _, s := range c.spans {
		if !seen[s.TraceId] {
			seen[s.TraceId] = true
			ids = append(ids, s.TraceId)
		}
	}
	return ids
}

func (c *Collector) Size() int { return len(c.spans) }

// Tracer creates spans in one service. Every service in the demo has its own Tracer sharing one Collector.
type Tracer struct {
	service   string
	clock     Clock
	ids       *IdGenerator
	collector *Collector
}

func NewTracer(service string, clock Clock, ids *IdGenerator, collector *Collector) *Tracer {
	return &Tracer{service, clock, ids, collector}
}

// StartRoot starts a root span: a new trace id, no parent.
func (t *Tracer) StartRoot(name string) *Span {
	return NewSpan(t.ids.TraceId(), t.ids.SpanId(), "", t.service, name, t.clock.NowMillis())
}

// StartChild starts a child of an incoming context: same trace id, parent = the caller's span id.
func (t *Tracer) StartChild(name string, parent SpanContext) *Span {
	return NewSpan(parent.TraceId, t.ids.SpanId(), parent.SpanId, t.service, name, t.clock.NowMillis())
}

// StartFromCarrier starts a span from a carrier; if the header is missing or malformed, a new trace begins (and the break is visible).
func (t *Tracer) StartFromCarrier(name string, carrier map[string]string) *Span {
	if parent, ok := ParseTraceparent(carrier[Header]); ok {
		return t.StartChild(name, parent)
	}
	return t.StartRoot(name)
}

// End closes the span at the current time and exports it. The span was open a moment ago, so Export cannot fail.
func (t *Tracer) End(span *Span) {
	span.EndMillis = t.clock.NowMillis()
	if err := t.collector.Export(span); err != nil {
		panic(err)
	}
}

// Inject writes the W3C headers into a carrier (HTTP headers, Kafka record headers, a queue message's properties).
func Inject(ctx SpanContext, tracestate string) map[string]string {
	carrier := map[string]string{Header: ctx.ToTraceparent()}
	if tracestate != "" {
		carrier[StateHeader] = tracestate
	}
	return carrier
}

// TraceTree is a trace reassembled from its spans: tree structure, exclusive time, orphans, and a text timeline.
type TraceTree struct {
	TraceId  string
	Spans    []*Span
	ById     map[string]*Span
	children map[string][]*Span
	Roots    []*Span
	Orphans  []*Span
}

func NewTraceTree(traceId string, spans []*Span) *TraceTree {
	t := &TraceTree{TraceId: traceId, Spans: append([]*Span(nil), spans...), ById: map[string]*Span{}, children: map[string][]*Span{}}
	sort.SliceStable(t.Spans, func(i, j int) bool {
		a, b := t.Spans[i], t.Spans[j]
		if a.StartMillis != b.StartMillis {
			return a.StartMillis < b.StartMillis
		}
		return a.SpanId < b.SpanId
	})
	for _, s := range t.Spans {
		t.ById[s.SpanId] = s
	}
	for _, s := range t.Spans {
		switch {
		case s.IsRoot():
			t.Roots = append(t.Roots, s)
		case t.ById[s.ParentSpanId] != nil:
			t.children[s.ParentSpanId] = append(t.children[s.ParentSpanId], s)
		default:
			t.Orphans = append(t.Orphans, s) // parent never arrived: a service dropped the header or its span was lost
		}
	}
	return t
}

func (t *TraceTree) ChildrenOf(s *Span) []*Span { return t.children[s.SpanId] }

// mustDuration: every span in a tree came from the collector, which only accepts ended spans; an open one is a bug.
func mustDuration(s *Span) int64 {
	d, err := s.DurationMillis()
	if err != nil {
		panic(err)
	}
	return d
}

// ExclusiveMillis is the time spent in this span itself, not in any child: duration minus the union of child intervals.
func (t *TraceTree) ExclusiveMillis(s *Span) int64 {
	kids := append([]*Span(nil), t.ChildrenOf(s)...)
	sort.SliceStable(kids, func(i, j int) bool { return kids[i].StartMillis < kids[j].StartMillis })
	var covered int64
	cursor := s.StartMillis
	for _, k := range kids {
		from, to := max(cursor, k.StartMillis), min(s.EndMillis, k.EndMillis)
		if to > from {
			covered += to - from
			cursor = to
		}
	}
	return mustDuration(s) - covered
}

func (t *TraceTree) Depth(s *Span) int {
	d := 0
	cur := s
	for cur.ParentSpanId != "" && t.ById[cur.ParentSpanId] != nil {
		cur = t.ById[cur.ParentSpanId]
		d++
	}
	return d
}

// Hottest is the span with the largest exclusive time: where the trace actually spent its wall clock.
func (t *TraceTree) Hottest() *Span {
	var best *Span
	for _, s := range t.Spans {
		if best == nil || t.ExclusiveMillis(s) > t.ExclusiveMillis(best) {
			best = s
		}
	}
	return best
}

// RenderTimeline is a text timeline: one row per span, depth-indented, with a bar scaled to the root's duration.
func (t *TraceTree) RenderTimeline(width int) string {
	if len(t.Roots) == 0 {
		return "(no root span for trace " + t.TraceId + ")"
	}
	root := t.Roots[0]
	origin, total := root.StartMillis, max(1, mustDuration(root))
	var sb strings.Builder
	fmt.Fprintf(&sb, "trace %s  total %d ms\n", t.TraceId, total)
	fmt.Fprintf(&sb, "%-46s %7s %6s  %s\n", "service / span", "start", "dur", "timeline")
	w := int64(width)
	for _, s := range t.Spans {
		if !s.IsRoot() && t.ById[s.ParentSpanId] == nil {
			continue // orphans listed separately
		}
		label := strings.Repeat("  ", t.Depth(s)) + s.Service + " / " + s.Name
		from := (s.StartMillis - origin) * w / total
		length := max(1, mustDuration(s)*w/total)
		bar := strings.Repeat(" ", int(min(from, w))) + strings.Repeat("#", int(min(length, w-min(from, w))))
		suffix := ""
		if s.Status == "ERROR" {
			suffix = "  ERROR"
		}
		fmt.Fprintf(&sb, "%-46s %5d ms %4d ms  %s%s\n", label, s.StartMillis-origin, mustDuration(s), bar, suffix)
	}
	for _, o := range t.Orphans {
		fmt.Fprintf(&sb, "ORPHAN %s / %s (parent %s never arrived)\n", o.Service, o.Name, o.ParentSpanId)
	}
	return sb.String()
}

// PaymentSystem is the six simulated services of a card payment. Each one extracts the context from the incoming
// carrier, starts a child span, does some clock-advancing "work", and injects a fresh carrier for its downstream calls.
type PaymentSystem struct {
	Clock                    *FakeClock
	Collector                *Collector
	Ids                      *IdGenerator
	tracers                  map[string]*Tracer
	NotificationsDropsHeader bool // fault switch for the demo: the notifications consumer "forgets" to copy the header
}

func NewPaymentSystem(seed int64) *PaymentSystem {
	p := &PaymentSystem{Clock: NewFakeClock(1_700_000_000_000), Collector: &Collector{}, Ids: NewIdGenerator(seed), tracers: map[string]*Tracer{}}
	for _, svc := range []string{"api-gateway", "payments", "risk", "ledger", "processor-adapter", "notifications"} {
		p.tracers[svc] = NewTracer(svc, p.Clock, p.Ids, p.Collector)
	}
	return p
}

func (p *PaymentSystem) Tracer(svc string) *Tracer { return p.tracers[svc] }

// Charge is the entry point: an HTTP request with (possibly) a traceparent from the merchant's client. Returns the trace id.
func (p *PaymentSystem) Charge(incoming map[string]string, paymentId string, amountMinor int64, currency string) string {
	gw := p.Tracer("api-gateway")
	root := gw.StartFromCarrier("POST /charges", incoming)
	root.SetAttribute("payment.id", paymentId)
	root.SetAttribute("http.method", "POST")
	p.Clock.Advance(2) // auth, parsing
	p.payments(Inject(root.Context(true), incoming[StateHeader]), paymentId, amountMinor, currency)
	p.Clock.Advance(1) // serialize response
	gw.End(root)
	return root.TraceId
}

func (p *PaymentSystem) payments(carrier map[string]string, paymentId string, amountMinor int64, currency string) {
	t := p.Tracer("payments")
	s := t.StartFromCarrier("authorize", carrier)
	s.SetAttribute("payment.id", paymentId)
	s.SetAttribute("amount.minor", strconv.FormatInt(amountMinor, 10))
	s.SetAttribute("currency", currency)
	p.Clock.Advance(3) // load merchant config, idempotency check
	decision := p.risk(Inject(s.Context(true), carrier[StateHeader]), paymentId)
	s.SetAttribute("risk.decision", decision)
	p.Clock.Advance(1)
	p.ledger(Inject(s.Context(true), ""), paymentId, amountMinor)
	p.Clock.Advance(1)
	p.processor(Inject(s.Context(true), ""), paymentId)
	p.Clock.Advance(2)
	p.notifications(Inject(s.Context(true), ""), paymentId)
	p.Clock.Advance(1)
	t.End(s)
}

func (p *PaymentSystem) risk(carrier map[string]string, paymentId string) string {
	t := p.Tracer("risk")
	s := t.StartFromCarrier("score", carrier)
	p.Clock.Advance(4) // feature lookup
	model := t.StartChild("model.infer", s.Context(true))
	p.Clock.Advance(18) // the slow part
	t.End(model)
	p.Clock.Advance(1)
	t.End(s)
	return "APPROVE"
}

func (p *PaymentSystem) ledger(carrier map[string]string, paymentId string, amountMinor int64) {
	t := p.Tracer("ledger")
	s := t.StartFromCarrier("post-hold", carrier)
	s.SetAttribute("amount.minor", strconv.FormatInt(amountMinor, 10))
	p.Clock.Advance(6) // write journal entries
	t.End(s)
}

func (p *PaymentSystem) processor(carrier map[string]string, paymentId string) {
	t := p.Tracer("processor-adapter")
	s := t.StartFromCarrier("iso8583.auth", carrier)
	p.Clock.Advance(2)
	net := t.StartChild("network.roundtrip", s.Context(true))
	p.Clock.Advance(45) // the card network
	t.End(net)
	p.Clock.Advance(2)
	t.End(s)
}

func (p *PaymentSystem) notifications(carrier map[string]string, paymentId string) {
	t := p.Tracer("notifications")
	// Header-drop fault: when enabled, the carrier is replaced with an empty one, as if a queue consumer never copied it.
	if p.NotificationsDropsHeader {
		carrier = map[string]string{}
	}
	s := t.StartFromCarrier("enqueue-webhook", carrier)
	p.Clock.Advance(3)
	t.End(s)
}

func mustContext(c SpanContext, err error) SpanContext {
	if err != nil {
		panic(err)
	}
	return c
}

func main() {
	fmt.Println("== W3C traceparent round trip ==")
	ctx := mustContext(NewSpanContext("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", true))
	header := ctx.ToTraceparent()
	fmt.Println("header:  " + header)
	parsed, _ := ParseTraceparent(header)
	fmt.Println("parsed:  " + parsed.String())
	_, ok := ParseTraceparent("00-" + zeroTraceId + "-00f067aa0ba902b7-01")
	fmt.Printf("bad:     %t  (all-zero trace id rejected)\n", ok)
	fmt.Println()

	fmt.Println("== One payment through six services (seed 42) ==")
	sys := NewPaymentSystem(42)
	incoming := map[string]string{StateHeader: "merchant=acme"}
	traceId := sys.Charge(incoming, "pay_8f3k2", 1999, "USD")
	tree := NewTraceTree(traceId, sys.Collector.SpansFor(traceId))
	fmt.Print(tree.RenderTimeline(48))
	fmt.Println()

	root := tree.Roots[0]
	fmt.Printf("spans exported: %d, root duration: %d ms\n", len(tree.Spans), mustDuration(root))
	var sumExclusive int64
	for _, s := range tree.Spans {
		sumExclusive += tree.ExclusiveMillis(s)
	}
	fmt.Printf("sum of exclusive times: %d ms  (equals root duration when children are sequential)\n", sumExclusive)
	hot := tree.Hottest()
	fmt.Printf("hottest span: %s / %s with %d ms exclusive\n", hot.Service, hot.Name, tree.ExclusiveMillis(hot))
	fmt.Println()

	fmt.Println("== The same payment when notifications drops the header (seed 42) ==")
	broken := NewPaymentSystem(42)
	broken.NotificationsDropsHeader = true
	t2 := broken.Charge(map[string]string{}, "pay_8f3k2", 1999, "USD")
	brokenTree := NewTraceTree(t2, broken.Collector.SpansFor(t2))
	fmt.Printf("spans in trace %s...: %d (one fewer)\n", t2[:8], len(brokenTree.Spans))
	fmt.Printf("total traces in collector: %d (the notification span became its own root)\n", len(broken.Collector.TraceIds()))
	fmt.Println()

	fmt.Println("== Context extracted from a merchant-supplied traceparent ==")
	joined := NewPaymentSystem(7)
	fromMerchant := Inject(mustContext(NewSpanContext("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", true)), "vendor=xyz")
	t3 := joined.Charge(fromMerchant, "pay_join1", 500, "EUR")
	gateway := NewTraceTree(t3, joined.Collector.SpansFor(t3)).Spans[0]
	fmt.Printf("gateway span joined trace %s with parent %s\n", t3, gateway.ParentSpanId)
}
