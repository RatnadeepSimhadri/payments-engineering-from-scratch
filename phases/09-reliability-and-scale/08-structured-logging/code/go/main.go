// Lesson: phases/09-reliability-and-scale/08-structured-logging/docs/en.md
// Structured Logging and Log Correlation: JSON records, bound context, trace correlation, PAN/secret redaction, sampling, query
// Run:  go run .
// Test: go test ./...
// Sources: PCI DSS v4.0 Requirement 3.3/3.4 (PAN rendering and storage); RFC 8259 (JSON); RFC 5424 (syslog severity levels);
//
//	W3C Trace Context; OpenTelemetry Logs Data Model. Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Clock is the injected time source.
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

// Level is severity in increasing order. Syslog codes follow RFC 5424 so a reader can compare across systems.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

var levelNames = [...]string{"DEBUG", "INFO", "WARN", "ERROR"}
var levelSyslog = [...]int{7, 6, 4, 3}

func (l Level) String() string           { return levelNames[l] }
func (l Level) Syslog() int              { return levelSyslog[l] }
func (l Level) AtLeast(other Level) bool { return l >= other }

// Fields is an insertion-ordered string map, the Java LinkedHashMap, so serialized records are stable and diffable.
type Fields struct {
	keys   []string
	values map[string]string
}

func NewFields() *Fields { return &Fields{values: map[string]string{}} }

func (f *Fields) Put(key, value string) {
	if _, ok := f.values[key]; !ok {
		f.keys = append(f.keys, key)
	}
	f.values[key] = value
}

func (f *Fields) Get(key string) (string, bool) {
	v, ok := f.values[key]
	return v, ok
}

func (f *Fields) Keys() []string { return append([]string(nil), f.keys...) }

func (f *Fields) Copy() *Fields {
	c := NewFields()
	for _, k := range f.keys {
		c.Put(k, f.values[k])
	}
	return c
}

// LogRecord is one log event. Fields keep insertion order so the serialized form is stable and diffable.
type LogRecord struct {
	TimestampMillis int64
	Level           Level
	Service         string
	Message         string
	fields          *Fields
}

func NewLogRecord(timestampMillis int64, level Level, service, message string, fields *Fields) *LogRecord {
	return &LogRecord{timestampMillis, level, service, message, fields.Copy()}
}

// Field returns a field's value, or false when the record does not carry it.
func (r *LogRecord) Field(key string) (string, bool) { return r.fields.Get(key) }

// ToJson is hand-written JSON (RFC 8259): one line, fixed key order, every string escaped.
func (r *LogRecord) ToJson() string {
	var sb strings.Builder
	sb.WriteByte('{')
	JsonPair(&sb, "ts", strconv.FormatInt(r.TimestampMillis, 10), false)
	sb.WriteByte(',')
	JsonPair(&sb, "level", r.Level.String(), true)
	sb.WriteByte(',')
	JsonPair(&sb, "service", r.Service, true)
	sb.WriteByte(',')
	JsonPair(&sb, "msg", r.Message, true)
	for _, k := range r.fields.keys {
		sb.WriteByte(',')
		JsonPair(&sb, k, r.fields.values[k], true)
	}
	sb.WriteByte('}')
	return sb.String()
}

// JsonPair appends "key":value, quoting and escaping the value when quoteValue is set.
func JsonPair(sb *strings.Builder, key, value string, quoteValue bool) {
	sb.WriteByte('"')
	sb.WriteString(JsonEscape(key))
	sb.WriteString("\":")
	if quoteValue {
		sb.WriteByte('"')
		sb.WriteString(JsonEscape(value))
		sb.WriteByte('"')
	} else {
		sb.WriteString(value)
	}
}

// JsonNullPair appends "key":null, the Java pair's behaviour for a null value.
func JsonNullPair(sb *strings.Builder, key string) {
	sb.WriteByte('"')
	sb.WriteString(JsonEscape(key))
	sb.WriteString("\":null")
}

func JsonEscape(s string) string {
	var out strings.Builder
	out.Grow(len(s) + 8)
	for _, c := range s {
		switch c {
		case '"':
			out.WriteString("\\\"")
		case '\\':
			out.WriteString("\\\\")
		case '\n':
			out.WriteString("\\n")
		case '\r':
			out.WriteString("\\r")
		case '\t':
			out.WriteString("\\t")
		default:
			if c < 0x20 {
				fmt.Fprintf(&out, "\\u%04x", c)
			} else {
				out.WriteRune(c)
			}
		}
	}
	return out.String()
}

// Redactor removes card numbers and secrets before a record is written. Two rules:
//  1. Any field whose key names a secret is replaced wholesale.
//  2. Any run of 13-19 digits (allowing spaces or dashes) that passes the Luhn check is masked to first six + last four,
//     the maximum PCI DSS permits to be displayed.
type Redactor struct{ Redactions int }

var SecretKeys = map[string]bool{"password": true, "secret": true, "token": true, "cvv": true, "cvc": true, "api_key": true, "authorization": true, "pan": true}

func (r *Redactor) RedactValue(key, value string) string {
	if SecretKeys[strings.ToLower(key)] {
		r.Redactions++
		return "[REDACTED]"
	}
	return r.MaskPans(value)
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// candidateAt reports the end offset of the longest run of 13-19 digits (single spaces or dashes allowed between
// digits) that starts at i and is not followed by a digit. It is the Java regex
// (?<!\d)\d(?:[ -]?\d){12,18}(?!\d) written out, because Go's RE2 has no lookaround.
func candidateAt(text string, i int) (int, bool) {
	ends := []int{i + 1} // ends[k-1] is the offset just past the k-th digit of the chain
	for j := i + 1; ; {
		switch {
		case j < len(text) && isDigit(text[j]):
			ends = append(ends, j+1)
			j++
		case j+1 < len(text) && (text[j] == ' ' || text[j] == '-') && isDigit(text[j+1]):
			ends = append(ends, j+2)
			j += 2
		default:
			for k := min(len(ends), 19); k >= 13; k-- { // greedy, then back off until the next char is not a digit
				if e := ends[k-1]; e >= len(text) || !isDigit(text[e]) {
					return e, true
				}
			}
			return 0, false
		}
	}
}

func (r *Redactor) MaskPans(text string) string {
	var out strings.Builder
	last := 0
	for i := 0; i < len(text); {
		if !isDigit(text[i]) || (i > 0 && isDigit(text[i-1])) {
			i++
			continue
		}
		end, ok := candidateAt(text, i)
		if !ok {
			i++
			continue
		}
		digits := strings.NewReplacer(" ", "", "-", "").Replace(text[i:end])
		if LuhnValid(digits) {
			out.WriteString(text[last:i])
			out.WriteString(Mask(digits))
			last = end
			r.Redactions++
		}
		i = end
	}
	out.WriteString(text[last:])
	return out.String()
}

func Mask(digits string) string {
	return digits[:6] + strings.Repeat("*", len(digits)-10) + digits[len(digits)-4:]
}

// LuhnValid is Luhn mod-10 (ISO/IEC 7812-1). Rejects most random digit runs so order ids and timestamps are left alone.
func LuhnValid(digits string) bool {
	sum := 0
	dbl := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		dbl = !dbl
	}
	return sum%10 == 0
}

// Sampler keeps or drops a record. ERROR and WARN are always kept. DEBUG and INFO are kept for 1 in rate traces,
// decided by hashing the trace id, so a trace is logged completely or not at all. Records without a trace id use the message hash.
type Sampler struct {
	debugRate int
	infoRate  int
	Dropped   int
}

func NewSampler(debugRate, infoRate int) (*Sampler, error) {
	if debugRate < 1 || infoRate < 1 {
		return nil, errors.New("rates are 1-in-N, N >= 1")
	}
	return &Sampler{debugRate: debugRate, infoRate: infoRate}, nil
}

// Keep decides for one record; traceId is "" when the record has no trace bound.
func (s *Sampler) Keep(level Level, traceId, message string) bool {
	if level.AtLeast(WARN) {
		return true
	}
	rate := s.infoRate
	if level == DEBUG {
		rate = s.debugRate
	}
	if rate == 1 {
		return true
	}
	key := traceId
	if key == "" {
		key = message
	}
	keep := floorMod(Fnv1a(key), int32(rate)) == 0
	if !keep {
		s.Dropped++
	}
	return keep
}

// floorMod is Java's Math.floorMod: the result takes the sign of the divisor, unlike Go's %.
func floorMod(x, y int32) int32 {
	m := x % y
	if m != 0 && (m < 0) != (y < 0) {
		m += y
	}
	return m
}

// Fnv1a is FNV-1a over UTF-16 code units in wrapping 32-bit arithmetic, bit for bit the Java int version.
func Fnv1a(s string) int32 {
	h := uint32(0x811c9dc5)
	for _, cu := range utf16.Encode([]rune(s)) {
		h ^= uint32(cu)
		h *= 0x01000193
	}
	return int32(h)
}

// LogStore is an in-memory destination with an index by field value: the part of a log platform the lesson needs.
type LogStore struct {
	records []*LogRecord
	index   map[string]map[string][]*LogRecord // field -> value -> records
	indexed []string
}

func NewLogStore(indexedFields ...string) *LogStore {
	return &LogStore{index: map[string]map[string][]*LogRecord{}, indexed: indexedFields}
}

func (s *LogStore) Write(r *LogRecord) {
	s.records = append(s.records, r)
	for _, f := range s.indexed {
		if v, ok := r.Field(f); ok {
			byValue, present := s.index[f]
			if !present {
				byValue = map[string][]*LogRecord{}
				s.index[f] = byValue
			}
			byValue[v] = append(byValue[v], r)
		}
	}
}

func (s *LogStore) Query(field, value string) []*LogRecord { return s.index[field][value] }

func (s *LogStore) All() []*LogRecord { return append([]*LogRecord(nil), s.records...) }
func (s *LogStore) Size() int         { return len(s.records) }

// ErrOddFields is returned when a log call passes an odd number of key/value strings.
var ErrOddFields = errors.New("fields must be key/value pairs")

// Logger is the logger a service holds. With returns a child logger carrying bound fields (payment_id, trace_id,
// span_id) so every record emitted inside a request scope carries them without each call site repeating them.
type Logger struct {
	service  string
	clock    Clock
	minimum  Level
	redactor *Redactor
	sampler  *Sampler
	store    *LogStore
	bound    *Fields
}

func NewLogger(service string, clock Clock, minimum Level, redactor *Redactor, sampler *Sampler, store *LogStore) *Logger {
	return &Logger{service, clock, minimum, redactor, sampler, store, NewFields()}
}

func (l *Logger) With(key, value string) *Logger {
	next := l.bound.Copy()
	next.Put(key, value)
	return &Logger{l.service, l.clock, l.minimum, l.redactor, l.sampler, l.store, next}
}

func (l *Logger) WithTrace(traceId, spanId string) *Logger {
	return l.With("trace_id", traceId).With("span_id", spanId)
}

func (l *Logger) Debug(msg string, kv ...string) error { return l.Log(DEBUG, msg, kv...) }
func (l *Logger) Info(msg string, kv ...string) error  { return l.Log(INFO, msg, kv...) }
func (l *Logger) Warn(msg string, kv ...string) error  { return l.Log(WARN, msg, kv...) }
func (l *Logger) Error(msg string, kv ...string) error { return l.Log(ERROR, msg, kv...) }

func (l *Logger) Log(level Level, message string, kv ...string) error {
	if len(kv)%2 != 0 {
		return ErrOddFields
	}
	if !level.AtLeast(l.minimum) {
		return nil
	}
	traceId, _ := l.bound.Get("trace_id")
	if !l.sampler.Keep(level, traceId, message) {
		return nil
	}
	fields := NewFields()
	for _, k := range l.bound.keys {
		fields.Put(k, l.redactor.RedactValue(k, l.bound.values[k]))
	}
	for i := 0; i < len(kv); i += 2 {
		fields.Put(kv[i], l.redactor.RedactValue(kv[i], kv[i+1]))
	}
	l.store.Write(NewLogRecord(l.clock.NowMillis(), level, l.service, l.redactor.MaskPans(message), fields))
	return nil
}

// PaymentFlow is six services logging one payment, with the trace ids from Lesson 07 bound into every record.
type PaymentFlow struct {
	Clock    *FakeClock
	Redactor *Redactor
	Store    *LogStore
	Sampler  *Sampler
	Minimum  Level
}

func NewPaymentFlow(sampler *Sampler, minimum Level) *PaymentFlow {
	return &PaymentFlow{NewFakeClock(1_700_000_000_000), &Redactor{}, NewLogStore("payment_id", "trace_id", "merchant_id"), sampler, minimum}
}

func (f *PaymentFlow) Logger(service string) *Logger {
	return NewLogger(service, f.Clock, f.Minimum, f.Redactor, f.Sampler, f.Store)
}

func (f *PaymentFlow) Charge(traceId, paymentId, merchantId string, amountMinor int64, currency, rawPan string, processorFails bool) {
	amount := strconv.FormatInt(amountMinor, 10)
	gw := f.Logger("api-gateway").WithTrace(traceId, "a1a1a1a1a1a1a1a1").With("payment_id", paymentId).With("merchant_id", merchantId)
	gw.Info("request received", "method", "POST", "path", "/charges")
	f.Clock.Advance(2)

	pay := f.Logger("payments").WithTrace(traceId, "b2b2b2b2b2b2b2b2").With("payment_id", paymentId).With("merchant_id", merchantId)
	pay.Debug("idempotency key checked", "idempotency_key", "idem_"+paymentId)
	pay.Info("authorize started", "amount_minor", amount, "currency", currency)
	// A careless developer logs the raw request body. The redactor must catch the PAN.
	pay.Debug("raw request body", "body", "{\"card\":\""+rawPan+"\",\"amount\":"+amount+"}")
	f.Clock.Advance(3)

	risk := f.Logger("risk").WithTrace(traceId, "c3c3c3c3c3c3c3c3").With("payment_id", paymentId)
	risk.Info("score computed", "score", "12", "decision", "APPROVE")
	f.Clock.Advance(23)

	ledger := f.Logger("ledger").WithTrace(traceId, "d4d4d4d4d4d4d4d4").With("payment_id", paymentId)
	ledger.Info("hold posted", "amount_minor", amount, "currency", currency, "journal_id", "jrn_"+paymentId)
	f.Clock.Advance(6)

	proc := f.Logger("processor-adapter").WithTrace(traceId, "e5e5e5e5e5e5e5e5").With("payment_id", paymentId)
	proc.Debug("iso8583 message built", "mti", "0100", "pan", rawPan)
	f.Clock.Advance(45)
	if processorFails {
		proc.Error("processor timeout", "processor", "acq-01", "timeout_ms", "45", "retryable", "true")
		again := f.Logger("ledger").WithTrace(traceId, "d4d4d4d4d4d4d4d4").With("payment_id", paymentId)
		again.Warn("hold reversed", "reason", "processor_timeout", "journal_id", "jrn_"+paymentId)
	} else {
		proc.Info("authorization approved", "processor", "acq-01", "auth_code", "ZX91Q2", "pan", rawPan)
	}
	f.Clock.Advance(4)

	notif := f.Logger("notifications").WithTrace(traceId, "f6f6f6f6f6f6f6f6").With("payment_id", paymentId)
	event := "payment.authorized"
	if processorFails {
		event = "payment.failed"
	}
	notif.Info("webhook enqueued", "event", event, "secret", "whsec_live_0123456789")
	f.Clock.Advance(3)
}

func mustSampler(s *Sampler, err error) *Sampler {
	if err != nil {
		panic(err)
	}
	return s
}

func main() {
	fmt.Println("== Unstructured vs structured ==")
	fmt.Println("unstructured: 2026-10-13 14:02:11 INFO Charged 19.99 for pay_8f3k2 (merchant acme) ok")
	flow := NewPaymentFlow(mustSampler(NewSampler(1, 1)), DEBUG)
	flow.Charge("4bf92f3577b34da6a3ce929d0e0e4736", "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", false)
	fmt.Println("structured:   " + flow.Store.All()[1].ToJson())
	fmt.Println()

	fmt.Println("== Every record for payment pay_8f3k2, across six services, in order ==")
	for _, r := range flow.Store.Query("payment_id", "pay_8f3k2") {
		fmt.Println(r.ToJson())
	}
	fmt.Println()

	fmt.Println("== Redaction ==")
	fmt.Printf("records written: %d, redactions applied: %d\n", flow.Store.Size(), flow.Redactor.Redactions)
	r := &Redactor{}
	fmt.Println("PAN in free text:    " + r.MaskPans("declined card 4111111111111111 for order 1234567890123"))
	fmt.Println("order id left alone: " + r.MaskPans("order 1234567890123 shipped") + "  (fails Luhn)")
	fmt.Println("secret by key:       " + r.RedactValue("api_key", "sk_live_abcdef"))
	fmt.Println()

	fmt.Println("== A failed payment: ERROR and WARN survive sampling, DEBUG/INFO are sampled 1-in-4 traces ==")
	sampled := NewPaymentFlow(mustSampler(NewSampler(4, 4)), DEBUG)
	traces := []string{"0af7651916cd43dd8448eb211c80319c", "1af7651916cd43dd8448eb211c80319c", "2af7651916cd43dd8448eb211c80319c", "3af7651916cd43dd8448eb211c80319c"}
	for i, t := range traces {
		sampled.Charge(t, "pay_s"+strconv.Itoa(i), "acme", int64(500+i), "EUR", "5555555555554444", i == 2)
	}
	fmt.Printf("records kept: %d of %d emitted\n", sampled.Store.Size(), sampled.Store.Size()+sampled.Sampler.Dropped)
	for _, t := range traces {
		recs := sampled.Store.Query("trace_id", t)
		errorsKept := 0
		for _, x := range recs {
			if x.Level.AtLeast(WARN) {
				errorsKept++
			}
		}
		fmt.Printf("trace %s...: %2d records kept, %d WARN/ERROR\n", t[:8], len(recs), errorsKept)
	}
	fmt.Println()
	fmt.Println("trace 1af76519... was selected by the hash, so all nine of its records survive; the other traces kept only WARN/ERROR")
	fmt.Println()
	fmt.Println("== Query: what happened to pay_s2? (its INFO/DEBUG were sampled out; the failure was not) ==")
	for _, rec := range sampled.Store.Query("payment_id", "pay_s2") {
		fmt.Printf("%-18s %-5s %s\n", rec.Service, rec.Level, rec.Message)
	}
}
