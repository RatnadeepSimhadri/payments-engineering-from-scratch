package main

import (
	"errors"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func ctxOf(t *testing.T, traceId, spanId string, sampled bool) SpanContext {
	t.Helper()
	c, err := NewSpanContext(traceId, spanId, sampled)
	if err != nil {
		t.Fatalf("NewSpanContext: %v", err)
	}
	return c
}

func parses(header string) bool {
	_, ok := ParseTraceparent(header)
	return ok
}

func TestIds(t *testing.T) {
	ids := NewIdGenerator(1)
	tid, sid := ids.TraceId(), ids.SpanId()
	check(t, "trace id is 32 lowercase hex chars", IsHex(tid, 32))
	check(t, "span id is 16 lowercase hex chars", IsHex(sid, 16))
	check(t, "same seed gives same ids", NewIdGenerator(1).TraceId() == tid)
	check(t, "different seeds give different ids", NewIdGenerator(2).TraceId() != tid)
	check(t, "parity: seed 42 yields the Java demo's trace id", NewIdGenerator(42).TraceId() == "359d41baf78afe0de1bbe7ae28c0450c")
}

func TestTraceparent(t *testing.T) {
	ctx := ctxOf(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", true)
	check(t, "traceparent format", ctx.ToTraceparent() == "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	check(t, "unsampled flag is 00", strings.HasSuffix(ctxOf(t, ctx.TraceId, ctx.SpanId, false).ToTraceparent(), "-00"))
	parsed, ok := ParseTraceparent(ctx.ToTraceparent())
	check(t, "round trip preserves ids and flag", ok && parsed == ctx)
	check(t, "parse trims whitespace", parses("  "+ctx.ToTraceparent()+" "))
	flagged, ok := ParseTraceparent("00-" + ctx.TraceId + "-" + ctx.SpanId + "-03")
	check(t, "parse reads sampled bit from flags with other bits set", ok && flagged.Sampled)
	check(t, "reject version ff", !parses("ff-"+ctx.TraceId+"-"+ctx.SpanId+"-01"))
	check(t, "reject all-zero trace id", !parses("00-"+strings.Repeat("0", 32)+"-"+ctx.SpanId+"-01"))
	check(t, "reject all-zero span id", !parses("00-"+ctx.TraceId+"-"+strings.Repeat("0", 16)+"-01"))
	check(t, "reject uppercase hex", !parses("00-"+strings.ToUpper(ctx.TraceId)+"-"+ctx.SpanId+"-01"))
	check(t, "reject short trace id", !parses("00-abc-"+ctx.SpanId+"-01"))
	check(t, "reject version 00 with extra field", !parses(ctx.ToTraceparent()+"-extra"))
	check(t, "accept future version with extra field", parses("01-"+ctx.TraceId+"-"+ctx.SpanId+"-01-future"))
	check(t, "absent header is rejected", !parses(""))
	_, err := NewSpanContext("zz", ctx.SpanId, true)
	check(t, "constructor rejects bad trace id", err != nil)
	check(t, "context renders like the Java record", ctx.String() == "SpanContext[traceId=4bf92f3577b34da6a3ce929d0e0e4736, spanId=00f067aa0ba902b7, sampled=true]")
}

func TestSpansAcrossServices(t *testing.T) {
	sys := NewPaymentSystem(42)
	incoming := map[string]string{StateHeader: "merchant=acme"}
	traceId := sys.Charge(incoming, "pay_1", 1999, "USD")
	spans := sys.Collector.SpansFor(traceId)
	tree := NewTraceTree(traceId, spans)
	check(t, "eight spans in one trace", len(spans) == 8)
	allShare := true
	services := map[string]bool{}
	for _, s := range spans {
		allShare = allShare && s.TraceId == traceId
		services[s.Service] = true
	}
	check(t, "all spans share the trace id", allShare)
	check(t, "exactly one root", len(tree.Roots) == 1 && tree.Roots[0].Service == "api-gateway")
	check(t, "no orphans when every hop propagates", len(tree.Orphans) == 0)
	check(t, "six distinct services", len(services) == 6)
	root := tree.Roots[0]
	payments := tree.ChildrenOf(root)[0]
	check(t, "payments is child of gateway", payments.Service == "payments" && payments.ParentSpanId == root.SpanId)
	check(t, "payments has four direct children", len(tree.ChildrenOf(payments)) == 4)
	depthOk, nested := true, true
	for _, s := range tree.Spans {
		if s.Name == "model.infer" && tree.Depth(s) != 3 {
			depthOk = false
		}
		if !s.IsRoot() {
			p := tree.ById[s.ParentSpanId]
			if !(s.StartMillis >= p.StartMillis && s.EndMillis <= p.EndMillis) {
				nested = false
			}
		}
	}
	check(t, "model.infer is at depth 3", depthOk)
	check(t, "child starts after parent and ends before it", nested)
	check(t, "root duration is 92 ms", mustDuration(root) == 92)

	var sumExclusive int64
	for _, s := range tree.Spans {
		sumExclusive += tree.ExclusiveMillis(s)
	}
	check(t, "exclusive times sum to root duration for sequential children", sumExclusive == mustDuration(root))
	check(t, "gateway exclusive is its own 3 ms", tree.ExclusiveMillis(root) == 3)
	check(t, "hottest span is the card network round trip", tree.Hottest().Name == "network.roundtrip" && tree.ExclusiveMillis(tree.Hottest()) == 45)
	check(t, "attributes carried", payments.Attributes["payment.id"] == "pay_1" && payments.Attributes["risk.decision"] == "APPROVE")
	check(t, "attribute order is insertion order", strings.Join(payments.AttributeKeys(), ",") == "payment.id,amount.minor,currency,risk.decision")

	timeline := tree.RenderTimeline(40)
	namesAll := true
	for svc := range services {
		namesAll = namesAll && strings.Contains(timeline, svc)
	}
	check(t, "timeline names every service", namesAll)
	check(t, "timeline shows total", strings.Contains(timeline, "total 92 ms"))
	check(t, "timeline indents by depth", strings.Contains(timeline, "      risk / model.infer"))

	again := NewPaymentSystem(42)
	t2 := again.Charge(map[string]string{StateHeader: "merchant=acme"}, "pay_1", 1999, "USD")
	check(t, "same seed reproduces the trace id", t2 == traceId)

	broken := NewPaymentSystem(42)
	broken.NotificationsDropsHeader = true
	t3 := broken.Charge(map[string]string{}, "pay_1", 1999, "USD")
	check(t, "dropped header: one span missing from the trace", len(broken.Collector.SpansFor(t3)) == 7)
	check(t, "dropped header: the lost span became a second trace", len(broken.Collector.TraceIds()) == 2)

	var partial []*Span
	for _, s := range spans {
		if s.Name != "score" {
			partial = append(partial, s)
		}
	}
	partialTree := NewTraceTree(traceId, partial)
	check(t, "missing parent yields exactly one orphan", len(partialTree.Orphans) == 1 && partialTree.Orphans[0].Name == "model.infer")
	check(t, "timeline reports the orphan", strings.Contains(partialTree.RenderTimeline(40), "ORPHAN risk / model.infer"))
}

func TestJoinMerchantTrace(t *testing.T) {
	joined := NewPaymentSystem(7)
	fromMerchant := Inject(ctxOf(t, strings.Repeat("a", 32), strings.Repeat("b", 16), true), "vendor=xyz")
	t4 := joined.Charge(fromMerchant, "pay_2", 500, "EUR")
	check(t, "gateway joins the incoming trace", t4 == strings.Repeat("a", 32))
	gw := NewTraceTree(t4, joined.Collector.SpansFor(t4)).Spans[0]
	check(t, "gateway's parent is the merchant's span id", gw.ParentSpanId == strings.Repeat("b", 16))
	check(t, "tracestate is forwarded unchanged", Inject(gw.Context(true), fromMerchant[StateHeader])["tracestate"] == "vendor=xyz")
	_, has := Inject(gw.Context(true), "")["tracestate"]
	check(t, "inject omits empty tracestate", !has)

	tr := joined.Tracer("ledger")
	open := tr.StartRoot("open")
	_, err := open.DurationMillis()
	check(t, "duration of an open span is an error", errors.Is(err, ErrSpanOpen))
	check(t, "exporting an open span is an error", errors.Is(joined.Collector.Export(open), ErrSpanOpen))
}
