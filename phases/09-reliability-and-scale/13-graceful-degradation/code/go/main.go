// Lesson: phases/09-reliability-and-scale/13-graceful-degradation/docs/en.md
// Graceful Degradation and Load Shedding: priority queues with CoDel, brownout modes, feature flags degrading fraud scoring
// Run:  go run .
// Test: go test ./...
// Sources: RFC 8289 (CoDel, incl. the reference pseudocode); Nichols & Jacobson, "Controlling Queue Delay" (ACM Queue 2012);
//
//	Google SRE Book ch. 21-22 (overload, cascading failures); Netflix "Performance Under Load" (2018). Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Priority is the request class in order of importance. Lower value = more important = shed last.
type Priority int

const (
	CRITICAL Priority = iota // authorize, capture: money moving now
	HIGH                     // refunds, webhooks: money moving soon
	NORMAL                   // balance reads, status polls
	LOW                      // reporting, exports, analytics
)

// Priorities lists every class in declaration order, the Java Priority.values().
var Priorities = [...]Priority{CRITICAL, HIGH, NORMAL, LOW}

var priorityNames = [...]string{"CRITICAL", "HIGH", "NORMAL", "LOW"}

func (p Priority) String() string { return priorityNames[p] }

// Request is one unit of work. EnqueuedMillis is stamped when it enters a queue; sojourn = dequeue time - EnqueuedMillis.
type Request struct {
	Id             int64
	Priority       Priority
	ArrivalMillis  int64
	EnqueuedMillis int64
}

func NewRequest(id int64, priority Priority, arrivalMillis int64) *Request {
	return &Request{id, priority, arrivalMillis, arrivalMillis}
}

// CoDelQueue is a Controlled Delay queue (RFC 8289). Drops from the head when the *minimum* sojourn time over an
// interval has stayed above the target, then drops at an increasing rate (interval / sqrt(count)) until sojourn falls
// back under target. Integer milliseconds throughout; sqrt is the one place floating point appears, as in the RFC.
type CoDelQueue struct {
	targetMillis   int64
	intervalMillis int64
	queue          []*Request
	firstAboveTime int64 // when sojourn first exceeded target, plus interval; 0 = not above
	dropNext       int64 // next scheduled drop time while in dropping state
	count          int   // drops in the current dropping episode (drives the control law)
	lastCount      int
	dropping       bool
	Dropped        int64
	MaxSojournSeen int64
}

func NewCoDelQueue(targetMillis, intervalMillis int64) (*CoDelQueue, error) {
	if targetMillis <= 0 || intervalMillis <= targetMillis {
		return nil, errors.New("need 0 < target < interval")
	}
	return &CoDelQueue{targetMillis: targetMillis, intervalMillis: intervalMillis}, nil
}

func (q *CoDelQueue) Enqueue(r *Request, nowMillis int64) {
	r.EnqueuedMillis = nowMillis
	q.queue = append(q.queue, r)
}

func (q *CoDelQueue) Size() int        { return len(q.queue) }
func (q *CoDelQueue) IsDropping() bool { return q.dropping }

// doDequeue is RFC 8289 Section 5.3: pop one, and decide whether the sojourn signal says the queue is in a
// persistent-delay state. The Java DoDequeue record is the pair of return values.
func (q *CoDelQueue) doDequeue(now int64) (*Request, bool) {
	if len(q.queue) == 0 {
		q.firstAboveTime = 0
		return nil, false
	}
	r := q.queue[0]
	q.queue = q.queue[1:]
	sojourn := now - r.EnqueuedMillis
	q.MaxSojournSeen = max(q.MaxSojournSeen, sojourn)
	if sojourn < q.targetMillis {
		q.firstAboveTime = 0 // delay is fine; reset the "above target since" clock
		return r, false
	}
	if q.firstAboveTime == 0 {
		q.firstAboveTime = now + q.intervalMillis // start the interval timer; do not drop yet
		return r, false
	}
	return r, now >= q.firstAboveTime // above target for a whole interval: ok to drop
}

func (q *CoDelQueue) controlLaw(t int64, c int) int64 {
	return t + int64(float64(q.intervalMillis)/math.Sqrt(float64(c)))
}

// Dequeue is RFC 8289 Section 5.4. Returns the request to serve, or nil if the queue is empty.
func (q *CoDelQueue) Dequeue(now int64) *Request {
	r, ok := q.doDequeue(now)
	if q.dropping {
		if !ok {
			q.dropping = false // sojourn fell under target: leave dropping state
		} else {
			for now >= q.dropNext && q.dropping {
				q.Dropped++ // drop r
				q.count++
				r, ok = q.doDequeue(now)
				if !ok {
					q.dropping = false
				} else {
					q.dropNext = q.controlLaw(q.dropNext, q.count)
				}
			}
		}
	} else if ok {
		q.Dropped++ // first drop of a new episode
		r, _ = q.doDequeue(now)
		q.dropping = true
		// If we were dropping recently, resume at a higher rate instead of restarting from count = 1.
		delta := q.count - q.lastCount
		if delta > 1 && now-q.dropNext < 16*q.intervalMillis {
			q.count = delta
		} else {
			q.count = 1
		}
		q.dropNext = q.controlLaw(now, q.count)
		q.lastCount = q.count
	}
	return r
}

// Flags are runtime switches. In production a flag service; here a map the brownout controller flips.
type Flags struct{ values map[string]string }

func NewFlags() *Flags { return &Flags{map[string]string{}} }

func (f *Flags) Get(key, dflt string) string {
	if v, ok := f.values[key]; ok {
		return v
	}
	return dflt
}

func (f *Flags) Set(key, value string) { f.values[key] = value }

// FraudScorer is fraud scoring whose cost depends on a flag: the full model, or a cheap rule set under brownout.
type FraudScorer struct{}

const (
	FullMillis      int64 = 8
	RulesOnlyMillis int64 = 1
)

func (FraudScorer) ServiceMillis(flags *Flags) int64 {
	if flags.Get("fraud.mode", "full") == "rules_only" {
		return RulesOnlyMillis
	}
	return FullMillis
}

// Level is a brownout level. Each level sheds one more priority class at the door and degrades one more feature.
type Level int

const (
	LevelNormal Level = iota
	LevelShedLow
	LevelShedNormal
	LevelCriticalOnly
)

// Levels lists every level in escalation order, the Java Level.values().
var Levels = [...]Level{LevelNormal, LevelShedLow, LevelShedNormal, LevelCriticalOnly}

var levelNames = [...]string{"NORMAL", "SHED_LOW", "SHED_NORMAL", "CRITICAL_ONLY"}

// levelShedAtOrBelow: reject requests with priority >= this one; -1 means the level sheds nothing (the Java null).
var levelShedAtOrBelow = [...]int{-1, int(LOW), int(NORMAL), int(HIGH)}
var levelFraudMode = [...]string{"full", "rules_only", "rules_only", "rules_only"}

func (l Level) String() string    { return levelNames[l] }
func (l Level) FraudMode() string { return levelFraudMode[l] }

func (l Level) Sheds(p Priority) bool {
	return levelShedAtOrBelow[l] >= 0 && int(p) >= levelShedAtOrBelow[l]
}

// BrownoutController escalates when CRITICAL/HIGH sojourn exceeds target for a window; de-escalates after
// cooldownWindows calm windows.
type BrownoutController struct {
	targetMillis     int64
	windowMillis     int64
	cooldownWindows  int
	flags            *Flags
	level            Level
	windowStart      int64
	windowMaxSojourn int64
	calmWindows      int
	Transitions      []string
	MillisAtLevel    [len(Levels)]int64
	lastTick         int64
}

func NewBrownoutController(targetMillis, windowMillis int64, cooldownWindows int, flags *Flags, nowMillis int64) *BrownoutController {
	b := &BrownoutController{targetMillis: targetMillis, windowMillis: windowMillis, cooldownWindows: cooldownWindows,
		flags: flags, level: LevelNormal, windowStart: nowMillis, lastTick: nowMillis}
	flags.Set("fraud.mode", b.level.FraudMode())
	return b
}

func (b *BrownoutController) Level() Level { return b.level }

// Observe is called with the sojourn of every CRITICAL or HIGH request as it is dequeued.
func (b *BrownoutController) Observe(sojournMillis int64) {
	b.windowMaxSojourn = max(b.windowMaxSojourn, sojournMillis)
}

// Tick is called every simulated millisecond. Closes a window when it elapses and moves at most one level.
func (b *BrownoutController) Tick(nowMillis int64) {
	b.MillisAtLevel[b.level] += nowMillis - b.lastTick
	b.lastTick = nowMillis
	if nowMillis-b.windowStart < b.windowMillis {
		return
	}
	before := b.level
	switch {
	case b.windowMaxSojourn > b.targetMillis:
		b.calmWindows = 0
		if int(b.level) < len(Levels)-1 {
			b.level++
		}
	case b.windowMaxSojourn <= b.targetMillis/2:
		b.calmWindows++
		if b.calmWindows >= b.cooldownWindows && b.level != LevelNormal {
			b.level--
			b.calmWindows = 0
		}
	default:
		b.calmWindows = 0 // in the hysteresis band: hold
	}
	if b.level != before {
		b.Transitions = append(b.Transitions, fmt.Sprintf("%dms %s -> %s", nowMillis, before, b.level))
		b.flags.Set("fraud.mode", b.level.FraudMode())
	}
	b.windowMaxSojourn = 0
	b.windowStart = nowMillis
}

// Outcome is what a policy did with the offered load, per priority. Arrays indexed by Priority replace the EnumMaps.
type Outcome struct {
	Offered        [len(Priorities)]int64
	ShedAtDoor     [len(Priorities)]int64
	DroppedInQueue [len(Priorities)]int64
	Latencies      [len(Priorities)][]int64
	StillQueued    int64
}

func NewOutcome() *Outcome { return &Outcome{} }

func (o *Outcome) Completed(p Priority) int64 { return int64(len(o.Latencies[p])) }

// Percentile is nearest-rank over the completed latencies of one class; -1 when nothing completed.
func (o *Outcome) Percentile(p Priority, q float64) int64 {
	l := append([]int64(nil), o.Latencies[p]...)
	if len(l) == 0 {
		return -1
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	idx := int(math.Ceil(q*float64(len(l)))) - 1
	return l[max(0, min(len(l)-1, idx))]
}

func sum(a [len(Priorities)]int64) int64 {
	var s int64
	for _, v := range a {
		s += v
	}
	return s
}

func (o *Outcome) TotalOffered() int64 { return sum(o.Offered) }
func (o *Outcome) TotalCompleted() int64 {
	var s int64
	for _, p := range Priorities {
		s += o.Completed(p)
	}
	return s
}
func (o *Outcome) TotalShed() int64    { return sum(o.ShedAtDoor) }
func (o *Outcome) TotalDropped() int64 { return sum(o.DroppedInQueue) }

// Policy is a serving policy: how arriving requests are admitted and which one a free worker serves next.
type Policy interface {
	Name() string
	// Admit returns false if the request is rejected at the door.
	Admit(r *Request, now int64, out *Outcome) bool
	// Next is the request to serve, or nil; may drop stale requests as a side effect, recorded in out.
	Next(now int64, out *Outcome) *Request
	ServiceMillis(r *Request) int64
	Tick(now int64)
	Queued() int
}

// FifoPolicy is the baseline everyone ships first: one FIFO queue, no limit, full fraud scoring for everyone.
type FifoPolicy struct {
	queue []*Request
	flags *Flags
	fraud FraudScorer
}

func NewFifoPolicy() *FifoPolicy { return &FifoPolicy{flags: NewFlags()} }

func (f *FifoPolicy) Name() string { return "fifo, no shedding" }

func (f *FifoPolicy) Admit(r *Request, now int64, out *Outcome) bool {
	f.queue = append(f.queue, r)
	return true
}

func (f *FifoPolicy) Next(now int64, out *Outcome) *Request {
	if len(f.queue) == 0 {
		return nil
	}
	r := f.queue[0]
	f.queue = f.queue[1:]
	return r
}

func (f *FifoPolicy) ServiceMillis(r *Request) int64 {
	cost := BaseServiceMillis(r.Priority)
	if r.Priority == CRITICAL {
		cost += f.fraud.ServiceMillis(f.flags)
	}
	return cost
}

func (f *FifoPolicy) Tick(now int64) {}
func (f *FifoPolicy) Queued() int    { return len(f.queue) }

// DegradingPolicy: priority CoDel queues, brownout controller, and flag-driven fraud degradation.
type DegradingPolicy struct {
	Queues   [len(Priorities)]*CoDelQueue
	Flags    *Flags
	Brownout *BrownoutController
	fraud    FraudScorer
}

func NewDegradingPolicy(targetMillis, intervalMillis, windowMillis int64, cooldownWindows int, nowMillis int64) (*DegradingPolicy, error) {
	d := &DegradingPolicy{Flags: NewFlags()}
	for _, p := range Priorities {
		q, err := NewCoDelQueue(targetMillis, intervalMillis)
		if err != nil {
			return nil, err
		}
		d.Queues[p] = q
	}
	d.Brownout = NewBrownoutController(targetMillis, windowMillis, cooldownWindows, d.Flags, nowMillis)
	return d, nil
}

func (d *DegradingPolicy) Name() string { return "priority + CoDel + brownout" }

func (d *DegradingPolicy) Admit(r *Request, now int64, out *Outcome) bool {
	if d.Brownout.Level().Sheds(r.Priority) {
		out.ShedAtDoor[r.Priority]++
		return false
	}
	d.Queues[r.Priority].Enqueue(r, now)
	return true
}

func (d *DegradingPolicy) Next(now int64, out *Outcome) *Request {
	for _, p := range Priorities {
		q := d.Queues[p]
		before := q.Dropped
		r := q.Dequeue(now)
		if droppedNow := q.Dropped - before; droppedNow > 0 {
			out.DroppedInQueue[p] += droppedNow
		}
		if r != nil {
			if p == CRITICAL || p == HIGH {
				d.Brownout.Observe(now - r.EnqueuedMillis)
			}
			return r
		}
	}
	return nil
}

func (d *DegradingPolicy) ServiceMillis(r *Request) int64 {
	cost := BaseServiceMillis(r.Priority)
	if r.Priority == CRITICAL {
		cost += d.fraud.ServiceMillis(d.Flags)
	}
	return cost
}

func (d *DegradingPolicy) Tick(now int64) { d.Brownout.Tick(now) }

func (d *DegradingPolicy) Queued() int {
	n := 0
	for _, q := range d.Queues {
		n += q.Size()
	}
	return n
}

// Simulation is a discrete-time simulation: workers parallel servers, 1 ms ticks, Bernoulli arrivals per tick from a
// seeded RNG, with an overload phase in the middle. Deterministic for a given seed.
type Simulation struct {
	Workers                                              int
	Seed                                                 int64
	NormalRps, OverloadRps                               float64
	OverloadFromMillis, OverloadToMillis, DurationMillis int64
}

func NewSimulation(workers int, seed int64, normalRps, overloadRps float64, overloadFrom, overloadTo, duration int64) Simulation {
	return Simulation{workers, seed, normalRps, overloadRps, overloadFrom, overloadTo, duration}
}

func BaseServiceMillis(p Priority) int64 {
	switch p {
	case CRITICAL:
		return 4
	case HIGH:
		return 4
	case NORMAL:
		return 3
	default:
		return 6
	}
}

// Pick is the traffic mix: 30% critical, 10% high, 40% normal, 20% low.
func Pick(u float64) Priority {
	switch {
	case u < 0.30:
		return CRITICAL
	case u < 0.40:
		return HIGH
	case u < 0.80:
		return NORMAL
	}
	return LOW
}

func (s Simulation) Run(policy Policy) *Outcome {
	random := NewJavaRandom(s.Seed)
	out := NewOutcome()
	busyUntil := make([]int64, s.Workers)
	inService := make([]*Request, s.Workers)
	nextId := int64(1)
	for now := int64(0); now < s.DurationMillis; now++ {
		rps := s.NormalRps
		if now >= s.OverloadFromMillis && now < s.OverloadToMillis {
			rps = s.OverloadRps
		}
		// arrivals this millisecond: expected rps/1000, drawn as up to three Bernoulli trials for a Poisson-ish shape
		p := rps / 3000.0
		for k := 0; k < 3; k++ {
			if random.NextDouble() < p {
				r := NewRequest(nextId, Pick(random.NextDouble()), now)
				nextId++
				out.Offered[r.Priority]++
				policy.Admit(r, now, out)
			}
		}
		// workers: finish what is due, then pull the next request
		for w := 0; w < s.Workers; w++ {
			if inService[w] != nil && busyUntil[w] <= now {
				out.Latencies[inService[w].Priority] = append(out.Latencies[inService[w].Priority], now-inService[w].ArrivalMillis)
				inService[w] = nil
			}
			if inService[w] == nil {
				if r := policy.Next(now, out); r != nil {
					inService[w] = r
					busyUntil[w] = now + policy.ServiceMillis(r)
				}
			}
		}
		policy.Tick(now)
	}
	out.StillQueued = int64(policy.Queued())
	for _, r := range inService {
		if r != nil {
			out.StillQueued++
		}
	}
	return out
}

const (
	Workers        = 8
	Target   int64 = 5
	Interval int64 = 100
	Window   int64 = 100
	Cooldown       = 5
)

// StandardScenario: 8 workers, mean service ~6.4 ms at full fraud -> capacity ~1,250 rps. Normal 800 rps; overload 2,400 rps for 3 s.
func StandardScenario(seed int64) Simulation {
	return NewSimulation(Workers, seed, 800, 2400, 2000, 5000, 9000)
}

func printOutcome(o *Outcome) {
	fmt.Printf("%-9s %8s %8s %8s %9s %8s %8s\n", "priority", "offered", "shed", "dropped", "completed", "p50 ms", "p99 ms")
	for _, p := range Priorities {
		fmt.Printf("%-9s %8d %8d %8d %9d %8d %8d\n", p, o.Offered[p], o.ShedAtDoor[p], o.DroppedInQueue[p],
			o.Completed(p), o.Percentile(p, 0.50), o.Percentile(p, 0.99))
	}
	fmt.Printf("total: offered %d, completed %d, shed at door %d, dropped in queue %d, still queued at end %d\n\n",
		o.TotalOffered(), o.TotalCompleted(), o.TotalShed(), o.TotalDropped(), o.StillQueued)
}

func mustQueue(q *CoDelQueue, err error) *CoDelQueue {
	if err != nil {
		panic(err)
	}
	return q
}

func mustPolicy(d *DegradingPolicy, err error) *DegradingPolicy {
	if err != nil {
		panic(err)
	}
	return d
}

func main() {
	fmt.Println("== CoDel on one queue: 5 ms target, 100 ms interval ==")
	q := mustQueue(NewCoDelQueue(Target, Interval))
	var now int64
	for i := int64(0); i < 400; i++ {
		q.Enqueue(NewRequest(i, NORMAL, now), now)
	}
	served := 0
	for now = 0; now < 400 && q.Size() > 0; now++ { // drain one per ms: a 400-deep queue sojourns up to 400 ms
		if q.Dequeue(now) != nil {
			served++
		}
	}
	fmt.Printf("400 queued, drained at 1/ms: served %d, dropped %d, max sojourn seen %d ms\n", served, q.Dropped, q.MaxSojournSeen)
	fmt.Println("(sojourn reaches the 5 ms target at t=5; the first drop waits a full interval, at t=105; later drops are spaced interval/sqrt(count))")
	fmt.Println()

	sim := StandardScenario(11)
	fmt.Println("== 8 workers, 800 rps normal, 2,400 rps overload from t=2s to t=5s, 9 s total (seed 11) ==")
	fmt.Println("-- " + NewFifoPolicy().Name() + " --")
	fifo := sim.Run(NewFifoPolicy())
	printOutcome(fifo)

	degrading := mustPolicy(NewDegradingPolicy(Target, Interval, Window, Cooldown, 0))
	fmt.Println("-- " + degrading.Name() + " --")
	shed := sim.Run(degrading)
	printOutcome(shed)

	fmt.Println("brownout transitions:")
	for _, t := range degrading.Brownout.Transitions {
		fmt.Println("  " + t)
	}
	fmt.Print("time at each level (ms):")
	for _, l := range Levels {
		fmt.Printf("  %s=%d", l, degrading.Brownout.MillisAtLevel[l])
	}
	fmt.Println()
	fmt.Println("fraud.mode at end: " + degrading.Flags.Get("fraud.mode", "full"))
	fmt.Println()
	fmt.Println("Reading the tables: FIFO completes more LOW requests and makes every CRITICAL authorization wait behind them,")
	fmt.Println("so CRITICAL p99 explodes and the backlog is still draining when the run ends. The degrading policy sheds LOW")
	fmt.Println("and NORMAL at the door, flips fraud to rules-only, lets CoDel trim stale queue entries, and keeps CRITICAL p99")
	fmt.Println("within tens of milliseconds through the whole overload. It completes fewer requests and the right ones.")
}
