package main

import "testing"

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func codel(t *testing.T, target, interval int64) *CoDelQueue {
	t.Helper()
	q, err := NewCoDelQueue(target, interval)
	if err != nil {
		t.Fatalf("NewCoDelQueue: %v", err)
	}
	return q
}

func degrading(t *testing.T, target, interval, window int64, cooldown int) *DegradingPolicy {
	t.Helper()
	d, err := NewDegradingPolicy(target, interval, window, cooldown, 0)
	if err != nil {
		t.Fatalf("NewDegradingPolicy: %v", err)
	}
	return d
}

func TestCoDelNoDropsUnderTarget(t *testing.T) {
	calm := codel(t, 5, 100)
	calmServed := 0
	for now := int64(0); now < 1000; now++ {
		calm.Enqueue(NewRequest(now, NORMAL, now), now)
		if calm.Dequeue(now+2) != nil { // every request waits 2 ms < 5 ms target
			calmServed++
		}
	}
	check(t, "calm queue serves every request", calmServed == 1000)
	check(t, "no drops when sojourn stays under target", calm.Dropped == 0)
	check(t, "calm queue never enters dropping state", !calm.IsDropping())
}

func TestCoDelDropsAfterInterval(t *testing.T) {
	q := codel(t, 5, 100)
	for i := int64(0); i < 400; i++ {
		q.Enqueue(NewRequest(i, NORMAL, 0), 0)
	}
	firstDropAt := int64(-1)
	served := 0
	for now := int64(0); now < 400 && q.Size() > 0; now++ {
		before := q.Dropped
		if q.Dequeue(now) != nil {
			served++
		}
		if q.Dropped > before && firstDropAt < 0 {
			firstDropAt = now
		}
	}
	check(t, "first drop happens one interval after sojourn first reached target (t=5 + 100)", firstDropAt == 105)
	check(t, "CoDel is gentle: a handful of drops, not a purge", q.Dropped >= 3 && q.Dropped <= 10)
	check(t, "served + dropped == enqueued", int64(served)+q.Dropped == 400)
	check(t, "dropping state is left when the queue empties", !q.IsDropping() || q.Size() == 0)
	check(t, "parity: the Java demo drains 395 and drops 5", served == 395 && q.Dropped == 5)
	_, err := NewCoDelQueue(100, 50)
	check(t, "target must be below interval", err != nil)
	_, err = NewCoDelQueue(0, 50)
	check(t, "target must be positive", err != nil)
}

func TestCoDelToleratesShortSpike(t *testing.T) {
	spike := codel(t, 5, 100)
	for i := int64(0); i < 50; i++ {
		spike.Enqueue(NewRequest(i, HIGH, 0), 0)
	}
	var now int64
	for spike.Size() > 0 { // drains in 50 ms, max sojourn 49 ms > target but < interval
		spike.Dequeue(now)
		now++
	}
	check(t, "a 50 ms spike within a 100 ms interval drops nothing", spike.Dropped == 0)
	check(t, "but the sojourn was observed above target", spike.MaxSojournSeen >= 40)
}

func TestBrownoutController(t *testing.T) {
	flags := NewFlags()
	bc := NewBrownoutController(5, 100, 3, flags, 0)
	check(t, "starts NORMAL with full fraud", bc.Level() == LevelNormal && flags.Get("fraud.mode", "") == "full")
	bc.Observe(50)
	bc.Tick(100)
	check(t, "one bad window escalates one level", bc.Level() == LevelShedLow)
	check(t, "escalation flips fraud to rules-only", flags.Get("fraud.mode", "") == "rules_only")
	for _, at := range []int64{200, 300, 400} {
		bc.Observe(50)
		bc.Tick(at)
	}
	check(t, "escalates at most one level per window, capped at CRITICAL_ONLY", bc.Level() == LevelCriticalOnly)
	check(t, "CRITICAL_ONLY sheds HIGH, NORMAL, LOW", bc.Level().Sheds(HIGH) && bc.Level().Sheds(LOW) && !bc.Level().Sheds(CRITICAL))
	bc.Observe(1)
	bc.Tick(500)
	bc.Observe(1)
	bc.Tick(600)
	check(t, "two calm windows are not enough to de-escalate with cooldown 3", bc.Level() == LevelCriticalOnly)
	bc.Observe(1)
	bc.Tick(700)
	check(t, "third calm window de-escalates one level", bc.Level() == LevelShedNormal)
	for _, at := range []int64{800, 900, 1000} {
		bc.Observe(4)
		bc.Tick(at)
	}
	check(t, "sojourn in the hysteresis band (target/2, target] holds the level", bc.Level() == LevelShedNormal)
	check(t, "transitions are recorded", len(bc.Transitions) == 4)
	check(t, "transitions render like Java", bc.Transitions[0] == "100ms NORMAL -> SHED_LOW")
	check(t, "time at level is accounted", bc.MillisAtLevel[LevelNormal] == 100)
	check(t, "SHED_LOW sheds only LOW", LevelShedLow.Sheds(LOW) && !LevelShedLow.Sheds(NORMAL))
	check(t, "NORMAL sheds nothing", !LevelNormal.Sheds(LOW))
}

func TestFraudScorerFollowsFlag(t *testing.T) {
	var fs FraudScorer
	f2 := NewFlags()
	check(t, "full model costs more than rules", fs.ServiceMillis(f2) == FullMillis)
	f2.Set("fraud.mode", "rules_only")
	check(t, "rules-only is cheap", fs.ServiceMillis(f2) == RulesOnlyMillis)
}

func TestPolicyAdmission(t *testing.T) {
	pol := degrading(t, 5, 100, 100, 3)
	o := NewOutcome()
	check(t, "NORMAL level admits LOW", pol.Admit(NewRequest(1, LOW, 0), 0, o))
	admitted := pol.Admit(NewRequest(2, CRITICAL, 0), 0, o)
	first := pol.Next(0, o)
	check(t, "priority order: CRITICAL served before LOW", admitted && first != nil && first.Priority == CRITICAL)
	second := pol.Next(0, o)
	check(t, "then LOW", second != nil && second.Priority == LOW)
	check(t, "empty policy returns nil", pol.Next(0, o) == nil)
}

func TestOverloadProtectsCritical(t *testing.T) {
	sim := StandardScenario(11)
	fifo := sim.Run(NewFifoPolicy())
	deg := degrading(t, Target, Interval, Window, Cooldown)
	shed := sim.Run(deg)
	check(t, "same offered load for both policies (seeded)", fifo.TotalOffered() == shed.TotalOffered())
	check(t, "FIFO CRITICAL p99 is in the seconds", fifo.Percentile(CRITICAL, 0.99) > 1000)
	check(t, "shedding keeps CRITICAL p99 under 100 ms", shed.Percentile(CRITICAL, 0.99) < 100)
	check(t, "shedding keeps CRITICAL p50 near service time", shed.Percentile(CRITICAL, 0.50) <= 10)
	check(t, "shedding completes nearly every CRITICAL request", shed.Completed(CRITICAL) >= shed.Offered[CRITICAL]-10)
	check(t, "CRITICAL is never shed at the door", shed.ShedAtDoor[CRITICAL] == 0)
	check(t, "LOW is shed the most", float64(shed.ShedAtDoor[LOW]) > float64(shed.ShedAtDoor[NORMAL])*0.9 && shed.ShedAtDoor[LOW] > 1000)
	check(t, "FIFO sheds nothing and leaves a backlog", fifo.TotalShed() == 0 && fifo.StillQueued > 1000)
	check(t, "shedding leaves no meaningful backlog", shed.StillQueued < 50)
	check(t, "conservation: offered == completed + shed + dropped + queued (shedding)",
		shed.TotalOffered() == shed.TotalCompleted()+shed.TotalShed()+shed.TotalDropped()+shed.StillQueued)
	check(t, "conservation: offered == completed + queued (fifo)", fifo.TotalOffered() == fifo.TotalCompleted()+fifo.StillQueued)
	check(t, "brownout escalated during overload and recovered after", len(deg.Brownout.Transitions) > 0 &&
		deg.Brownout.Level() == LevelNormal && deg.Flags.Get("fraud.mode", "") == "full")
	check(t, "never reached CRITICAL_ONLY in this scenario", deg.Brownout.MillisAtLevel[LevelCriticalOnly] == 0)
	// Demo parity: java.util.Random arrivals reproduce the Java tables digit for digit.
	check(t, "parity: 12038 offered, FIFO completes 10403 with 1635 still queued", fifo.TotalOffered() == 12038 && fifo.TotalCompleted() == 10403 && fifo.StillQueued == 1635)
	check(t, "parity: shedding completes 9212, sheds 2814, drops 3, CRITICAL p99 25 ms", shed.TotalCompleted() == 9212 && shed.TotalShed() == 2814 && shed.TotalDropped() == 3 && shed.Percentile(CRITICAL, 0.99) == 25)
	check(t, "parity: first brownout transition at 2100 ms", len(deg.Brownout.Transitions) == 8 && deg.Brownout.Transitions[0] == "2100ms NORMAL -> SHED_LOW")

	light := NewSimulation(8, 3, 600, 600, 0, 0, 3000)
	quiet := degrading(t, Target, Interval, Window, Cooldown)
	calmOut := light.Run(quiet)
	check(t, "no shedding or drops under normal load", calmOut.TotalShed() == 0 && calmOut.TotalDropped() == 0)
	check(t, "stays NORMAL under normal load", len(quiet.Brownout.Transitions) == 0)

	again := StandardScenario(11).Run(NewFifoPolicy())
	check(t, "simulation is deterministic for a seed", again.TotalCompleted() == fifo.TotalCompleted() && again.Percentile(LOW, 0.99) == fifo.Percentile(LOW, 0.99))
}
