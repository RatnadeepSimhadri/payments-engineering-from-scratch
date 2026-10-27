"""Tests for phases/09-reliability-and-scale/07-distributed-tracing. Run: python3 -m unittest -v"""
import unittest

from main import IdGenerator, PaymentSystem, Propagator, SpanContext, TraceTree

TRACE = "4bf92f3577b34da6a3ce929d0e0e4736"
SPAN = "00f067aa0ba902b7"


class IdTest(unittest.TestCase):
    def test_ids(self):
        ids = IdGenerator(1)
        tid, sid = ids.trace_id(), ids.span_id()
        self.assertTrue(SpanContext.is_hex(tid, 32))
        self.assertTrue(SpanContext.is_hex(sid, 16))
        self.assertEqual(IdGenerator(1).trace_id(), tid)
        self.assertNotEqual(IdGenerator(2).trace_id(), tid)

    def test_java_random_parity(self):
        # java.util.Random(42).nextBytes(16) as the Java demo prints it
        self.assertEqual(IdGenerator(42).trace_id(), "359d41baf78afe0de1bbe7ae28c0450c")


class TraceparentTest(unittest.TestCase):
    def test_format_and_round_trip(self):
        ctx = SpanContext(TRACE, SPAN, True)
        self.assertEqual(ctx.to_traceparent(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
        self.assertTrue(SpanContext(TRACE, SPAN, False).to_traceparent().endswith("-00"))
        parsed = SpanContext.parse_traceparent(ctx.to_traceparent())
        self.assertTrue(parsed is not None and parsed == ctx)
        self.assertIsNotNone(SpanContext.parse_traceparent("  " + ctx.to_traceparent() + " "))
        self.assertTrue(SpanContext.parse_traceparent(f"00-{TRACE}-{SPAN}-03").sampled)

    def test_rejections(self):
        ctx = SpanContext(TRACE, SPAN, True)
        self.assertIsNone(SpanContext.parse_traceparent(f"ff-{TRACE}-{SPAN}-01"))
        self.assertIsNone(SpanContext.parse_traceparent("00-" + "0" * 32 + f"-{SPAN}-01"))
        self.assertIsNone(SpanContext.parse_traceparent(f"00-{TRACE}-" + "0" * 16 + "-01"))
        self.assertIsNone(SpanContext.parse_traceparent(f"00-{TRACE.upper()}-{SPAN}-01"))
        self.assertIsNone(SpanContext.parse_traceparent(f"00-abc-{SPAN}-01"))
        self.assertIsNone(SpanContext.parse_traceparent(ctx.to_traceparent() + "-extra"))
        self.assertIsNotNone(SpanContext.parse_traceparent(f"01-{TRACE}-{SPAN}-01-future"))
        self.assertIsNone(SpanContext.parse_traceparent(None))
        with self.assertRaises(ValueError):
            SpanContext("zz", SPAN, True)


class PaymentTraceTest(unittest.TestCase):
    def setUp(self):
        self.sys = PaymentSystem(42)
        self.incoming = {SpanContext.STATE_HEADER: "merchant=acme"}
        self.trace_id = self.sys.charge(self.incoming, "pay_1", 1999, "USD")
        self.spans = self.sys.collector.spans_for(self.trace_id)
        self.tree = TraceTree(self.trace_id, self.spans)

    def test_spans_across_six_services(self):
        tree, spans, trace_id = self.tree, self.spans, self.trace_id
        self.assertEqual(len(spans), 8)
        self.assertTrue(all(s.trace_id == trace_id for s in spans))
        self.assertTrue(len(tree.roots) == 1 and tree.roots[0].service == "api-gateway")
        self.assertEqual(tree.orphans, [])
        services = {s.service for s in spans}
        self.assertEqual(len(services), 6)
        root = tree.roots[0]
        payments = tree.children_of(root)[0]
        self.assertTrue(payments.service == "payments" and payments.parent_span_id == root.span_id)
        self.assertEqual(len(tree.children_of(payments)), 4)
        self.assertTrue(all(tree.depth(s) == 3 for s in tree.spans if s.name == "model.infer"))
        for s in tree.spans:
            if not s.is_root():
                p = tree.by_id[s.parent_span_id]
                self.assertTrue(s.start_millis >= p.start_millis and s.end_millis <= p.end_millis)
        self.assertEqual(root.duration_millis(), 92)

    def test_exclusive_time(self):
        tree = self.tree
        root = tree.roots[0]
        payments = tree.children_of(root)[0]
        self.assertEqual(sum(tree.exclusive_millis(s) for s in tree.spans), root.duration_millis())
        self.assertEqual(tree.exclusive_millis(root), 3)
        self.assertTrue(tree.hottest().name == "network.roundtrip" and tree.exclusive_millis(tree.hottest()) == 45)
        self.assertTrue(payments.attributes["payment.id"] == "pay_1" and payments.attributes["risk.decision"] == "APPROVE")

    def test_timeline(self):
        timeline = self.tree.render_timeline(40)
        services = {s.service for s in self.spans}
        self.assertTrue(all(svc in timeline for svc in services))
        self.assertIn("total 92 ms", timeline)
        self.assertIn("      risk / model.infer", timeline)

    def test_determinism(self):
        again = PaymentSystem(42)
        t2 = again.charge(dict(self.incoming), "pay_1", 1999, "USD")
        self.assertEqual(t2, self.trace_id)

    def test_broken_propagation(self):
        broken = PaymentSystem(42)
        broken.notifications_drops_header = True
        t3 = broken.charge({}, "pay_1", 1999, "USD")
        self.assertEqual(len(broken.collector.spans_for(t3)), 7)
        self.assertEqual(len(broken.collector.trace_ids()), 2)

    def test_orphan_detection(self):
        partial = [s for s in self.spans if s.name != "score"]
        partial_tree = TraceTree(self.trace_id, partial)
        self.assertTrue(len(partial_tree.orphans) == 1 and partial_tree.orphans[0].name == "model.infer")
        self.assertIn("ORPHAN risk / model.infer", partial_tree.render_timeline(40))


class JoinAndLifecycleTest(unittest.TestCase):
    def test_joining_merchant_trace_and_tracestate(self):
        joined = PaymentSystem(7)
        from_merchant = Propagator.inject(SpanContext("a" * 32, "b" * 16, True), "vendor=xyz")
        t4 = joined.charge(from_merchant, "pay_2", 500, "EUR")
        self.assertEqual(t4, "a" * 32)
        gw = TraceTree(t4, joined.collector.spans_for(t4)).spans[0]
        self.assertEqual(gw.parent_span_id, "b" * 16)
        self.assertEqual(Propagator.inject(gw.context(True), from_merchant[SpanContext.STATE_HEADER])["tracestate"], "vendor=xyz")
        self.assertNotIn("tracestate", Propagator.inject(gw.context(True), None))

    def test_lifecycle_guards(self):
        joined = PaymentSystem(7)
        t = joined.tracer("ledger")
        open_span = t.start_root("open")
        with self.assertRaises(RuntimeError):
            open_span.duration_millis()
        with self.assertRaises(RuntimeError):
            joined.collector.export(open_span)


if __name__ == "__main__":
    unittest.main()
