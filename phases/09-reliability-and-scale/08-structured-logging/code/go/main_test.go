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

func sampler(t *testing.T, debug, info int) *Sampler {
	t.Helper()
	s, err := NewSampler(debug, info)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	return s
}

func fieldIs(r *LogRecord, key, want string) bool {
	v, ok := r.Field(key)
	return ok && v == want
}

func TestJsonSerialization(t *testing.T) {
	f := NewFields()
	f.Put("k", "v\\w")
	rec := NewLogRecord(1700000000000, INFO, "payments", "he said \"hi\"\n", f)
	json := rec.ToJson()
	check(t, "json starts with ts as a number", strings.HasPrefix(json, "{\"ts\":1700000000000,"))
	check(t, "json escapes quotes and newlines in the message", strings.Contains(json, "\"msg\":\"he said \\\"hi\\\"\\n\""))
	check(t, "json escapes backslashes in values", strings.Contains(json, "\"k\":\"v\\\\w\""))
	check(t, "json is one line", !strings.Contains(json, "\n"))
	check(t, "control characters become \\u escapes", JsonEscape("a\u0001b") == "a\\u0001b")
	var sb strings.Builder
	JsonNullPair(&sb, "x")
	check(t, "null value serializes as null", sb.String() == "\"x\":null")
}

func TestLevels(t *testing.T) {
	check(t, "ERROR is at least WARN", ERROR.AtLeast(WARN))
	check(t, "DEBUG is not at least INFO", !DEBUG.AtLeast(INFO))
	check(t, "syslog codes follow RFC 5424", ERROR.Syslog() == 3 && DEBUG.Syslog() == 7)
	check(t, "levels render by name", WARN.String() == "WARN")
}

func TestRedaction(t *testing.T) {
	red := &Redactor{}
	check(t, "Luhn accepts a valid test PAN", LuhnValid("4111111111111111"))
	check(t, "Luhn rejects a near miss", !LuhnValid("4111111111111112"))
	check(t, "mask keeps first six and last four", Mask("4111111111111111") == "411111******1111")
	cases := []struct{ name, in, want string }{
		{"PAN with spaces is found and masked", "card 4111 1111 1111 1111 ok", "card 411111******1111 ok"},
		{"PAN with dashes is found and masked", "5555-5555-5555-4444", "555555******4444"},
		{"13-digit PAN is masked", "4222222222222", "422222***2222"},
		{"non-Luhn digit run is left alone", "order 1234567890123 shipped", "order 1234567890123 shipped"},
		{"12-digit number is too short to be a PAN", "ref 411111111111", "ref 411111111111"},
		{"digits embedded in a longer run are not a PAN", "id 94111111111111111", "id 94111111111111111"},
		{"two PANs in one message are both masked", "4111111111111111 and 5555555555554444", "411111******1111 and 555555******4444"},
		{"a 20-digit run never matches, like the Java regex", "ref 41111111111111111111 x", "ref 41111111111111111111 x"},
		{"a chain longer than 19 digits backs off to 16 at a separator", "4111 1111 1111 1111 1111", "411111******1111 1111"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := red.MaskPans(c.in); got != c.want {
				t.Errorf("MaskPans(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	check(t, "secret-named field is replaced wholesale", red.RedactValue("api_key", "sk_live_x") == "[REDACTED]")
	check(t, "secret key match is case-insensitive", red.RedactValue("Authorization", "Bearer abc") == "[REDACTED]")
	check(t, "ordinary field keeps its value", red.RedactValue("merchant_id", "acme") == "acme")
	check(t, "ordinary field still gets PAN masking", red.RedactValue("note", "pan 4111111111111111") == "pan 411111******1111")
	check(t, "redaction count tracks events", red.Redactions == 9)
}

func TestSampler(t *testing.T) {
	s := sampler(t, 4, 4)
	check(t, "ERROR is always kept", s.Keep(ERROR, "0af7651916cd43dd8448eb211c80319c", "x"))
	check(t, "WARN is always kept", s.Keep(WARN, "0af7651916cd43dd8448eb211c80319c", "x"))
	check(t, "1-in-4 keeps the hash-selected trace", s.Keep(INFO, "1af7651916cd43dd8448eb211c80319c", "x"))
	check(t, "1-in-4 drops a non-selected trace", !s.Keep(INFO, "0af7651916cd43dd8448eb211c80319c", "x"))
	check(t, "decision is per trace, not per record", s.Keep(DEBUG, "1af7651916cd43dd8448eb211c80319c", "other") && !s.Keep(DEBUG, "0af7651916cd43dd8448eb211c80319c", "other"))
	check(t, "rate 1 keeps everything", sampler(t, 1, 1).Keep(DEBUG, "0af7651916cd43dd8448eb211c80319c", "x"))
	check(t, "FNV-1a is stable", Fnv1a("payments") == Fnv1a("payments") && Fnv1a("a") != Fnv1a("b"))
	check(t, "FNV-1a wraps like Java int: fnv1a(\"a\") is the known 32-bit value", Fnv1a("a") == int32(0xe40c292c-1<<32))
	check(t, "floorMod takes the divisor's sign", floorMod(-7, 4) == 1 && floorMod(7, 4) == 3)
	_, err := NewSampler(0, 1)
	check(t, "rate below 1 rejected", err != nil)
}

func TestLoggerBoundContext(t *testing.T) {
	clock := NewFakeClock(5)
	store := NewLogStore("payment_id", "trace_id")
	base := NewLogger("ledger", clock, INFO, &Redactor{}, sampler(t, 1, 1), store)
	scoped := base.WithTrace("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7").With("payment_id", "pay_1")
	scoped.Debug("not written: below minimum")
	scoped.Info("hold posted", "amount_minor", "1999", "currency", "USD")
	base.Info("no context here")
	check(t, "minimum level filters DEBUG", store.Size() == 2)
	posted := store.Query("payment_id", "pay_1")[0]
	check(t, "bound fields appear on the record", fieldIs(posted, "trace_id", "4bf92f3577b34da6a3ce929d0e0e4736") && fieldIs(posted, "span_id", "00f067aa0ba902b7"))
	check(t, "call-site fields appear after bound fields", strings.Index(posted.ToJson(), "\"payment_id\"") < strings.Index(posted.ToJson(), "\"amount_minor\""))
	_, hasPayment := store.All()[1].Field("payment_id")
	check(t, "child logger does not mutate the parent", len(store.Query("payment_id", "pay_1")) == 1 && !hasPayment)
	check(t, "query by trace id finds the same record", store.Query("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")[0] == posted)
	check(t, "query on an unindexed value is empty", len(store.Query("payment_id", "nope")) == 0)
	check(t, "timestamp comes from the injected clock", posted.TimestampMillis == 5)
	check(t, "odd key/value list rejected", errors.Is(scoped.Info("bad", "only_key"), ErrOddFields))
}

func TestEndToEnd(t *testing.T) {
	flow := NewPaymentFlow(sampler(t, 1, 1), DEBUG)
	flow.Charge("4bf92f3577b34da6a3ce929d0e0e4736", "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", false)
	byPayment := flow.Store.Query("payment_id", "pay_8f3k2")
	check(t, "every record carries the payment id", len(byPayment) == flow.Store.Size() && len(byPayment) == 9)
	sorted, allTrace, panRedacted := true, true, true
	services := map[string]bool{}
	var lines []string
	for i, r := range byPayment {
		if i > 0 && r.TimestampMillis < byPayment[i-1].TimestampMillis {
			sorted = false
		}
		services[r.Service] = true
		allTrace = allTrace && fieldIs(r, "trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")
		if pan, ok := r.Field("pan"); ok && pan != "[REDACTED]" {
			panRedacted = false
		}
		lines = append(lines, r.ToJson())
	}
	everything := strings.Join(lines, "\n")
	check(t, "records are in time order", sorted)
	check(t, "six services appear", len(services) == 6)
	check(t, "every record carries the trace id", allTrace)
	check(t, "raw PAN never reaches the store", !strings.Contains(everything, "4111111111111111") && !strings.Contains(everything, "4111 1111 1111 1111"))
	check(t, "PAN inside a JSON body field is masked", strings.Contains(everything, "411111******1111"))
	check(t, "pan-named field is redacted wholesale", panRedacted)
	check(t, "webhook secret is redacted", strings.Contains(everything, "\"secret\":\"[REDACTED]\"") && !strings.Contains(everything, "whsec_live"))
	check(t, "amounts are logged as minor-unit strings, never floats", strings.Contains(everything, "\"amount_minor\":\"1999\"") && !strings.Contains(everything, "19.99"))

	sampled := NewPaymentFlow(sampler(t, 4, 4), DEBUG)
	sampled.Charge("0af7651916cd43dd8448eb211c80319c", "pay_s0", "acme", 500, "EUR", "5555555555554444", false)
	sampled.Charge("2af7651916cd43dd8448eb211c80319c", "pay_s2", "acme", 502, "EUR", "5555555555554444", true)
	check(t, "unsampled successful trace leaves no records", len(sampled.Store.Query("trace_id", "0af7651916cd43dd8448eb211c80319c")) == 0)
	failed := sampled.Store.Query("payment_id", "pay_s2")
	check(t, "failure's ERROR and WARN survive sampling", len(failed) == 2 && failed[0].Level == ERROR && failed[1].Level == WARN)
	check(t, "dropped count accounts for every missing record", sampled.Sampler.Dropped+sampled.Store.Size() == 19)
}
