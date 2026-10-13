// Lesson: phases/09-reliability-and-scale/13-graceful-degradation/docs/en.md
// Graceful Degradation and Load Shedding: priority queues with CoDel, brownout modes, feature flags degrading fraud scoring
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 8289 (CoDel, incl. the reference pseudocode); Nichols & Jacobson, "Controlling Queue Delay" (ACM Queue 2012);
//          Google SRE Book ch. 21-22 (overload, cascading failures); Netflix "Performance Under Load" (2018). Java 21, stdlib only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumMap;
import java.util.List;
import java.util.Map;
import java.util.Random;

/** Request classes in order of importance. Lower ordinal = more important = shed last. */
enum Priority {
    CRITICAL,   // authorize, capture: money moving now
    HIGH,       // refunds, webhooks: money moving soon
    NORMAL,     // balance reads, status polls
    LOW         // reporting, exports, analytics
}

/** One unit of work. `enqueuedMillis` is stamped when it enters a queue; sojourn = dequeue time - enqueuedMillis. */
final class Request {
    final long id;
    final Priority priority;
    final long arrivalMillis;
    long enqueuedMillis;

    Request(long id, Priority priority, long arrivalMillis) {
        this.id = id;
        this.priority = priority;
        this.arrivalMillis = arrivalMillis;
        this.enqueuedMillis = arrivalMillis;
    }
}

/**
 * Controlled Delay queue (RFC 8289). Drops from the head when the *minimum* sojourn time over an interval has stayed
 * above the target, then drops at an increasing rate (interval / sqrt(count)) until sojourn falls back under target.
 * Integer milliseconds throughout; `sqrt` is the one place floating point appears, exactly as in the RFC pseudocode.
 */
final class CoDelQueue {
    private final long targetMillis;
    private final long intervalMillis;
    private final ArrayDeque<Request> queue = new ArrayDeque<>();
    private long firstAboveTime = 0;   // when sojourn first exceeded target, plus interval; 0 = not above
    private long dropNext = 0;         // next scheduled drop time while in dropping state
    private int count = 0;             // drops in the current dropping episode (drives the control law)
    private int lastCount = 0;
    private boolean dropping = false;
    long dropped = 0;
    long maxSojournSeen = 0;

    CoDelQueue(long targetMillis, long intervalMillis) {
        if (targetMillis <= 0 || intervalMillis <= targetMillis) throw new IllegalArgumentException("need 0 < target < interval");
        this.targetMillis = targetMillis;
        this.intervalMillis = intervalMillis;
    }

    void enqueue(Request r, long nowMillis) {
        r.enqueuedMillis = nowMillis;
        queue.addLast(r);
    }

    int size() { return queue.size(); }
    boolean isDropping() { return dropping; }

    private record DoDequeue(Request request, boolean okToDrop) {}

    /** RFC 8289 Section 5.3: pop one, and decide whether the sojourn signal says the queue is in a persistent-delay state. */
    private DoDequeue doDequeue(long now) {
        Request r = queue.pollFirst();
        if (r == null) {
            firstAboveTime = 0;
            return new DoDequeue(null, false);
        }
        long sojourn = now - r.enqueuedMillis;
        maxSojournSeen = Math.max(maxSojournSeen, sojourn);
        if (sojourn < targetMillis) {
            firstAboveTime = 0;                       // delay is fine; reset the "above target since" clock
            return new DoDequeue(r, false);
        }
        if (firstAboveTime == 0) {
            firstAboveTime = now + intervalMillis;    // start the interval timer; do not drop yet
            return new DoDequeue(r, false);
        }
        return new DoDequeue(r, now >= firstAboveTime);   // above target for a whole interval: ok to drop
    }

    private long controlLaw(long t, int c) {
        return t + (long) (intervalMillis / Math.sqrt(c));
    }

    /** RFC 8289 Section 5.4. Returns the request to serve, or null if the queue is empty. */
    Request dequeue(long now) {
        DoDequeue d = doDequeue(now);
        Request r = d.request();
        boolean ok = d.okToDrop();
        if (dropping) {
            if (!ok) {
                dropping = false;                     // sojourn fell under target: leave dropping state
            } else {
                while (now >= dropNext && dropping) {
                    dropped++;                        // drop r
                    count++;
                    d = doDequeue(now);
                    r = d.request();
                    ok = d.okToDrop();
                    if (!ok) dropping = false;
                    else dropNext = controlLaw(dropNext, count);
                }
            }
        } else if (ok) {
            dropped++;                                // first drop of a new episode
            d = doDequeue(now);
            r = d.request();
            dropping = true;
            // If we were dropping recently, resume at a higher rate instead of restarting from count = 1.
            int delta = count - lastCount;
            count = (delta > 1 && now - dropNext < 16 * intervalMillis) ? delta : 1;
            dropNext = controlLaw(now, count);
            lastCount = count;
        }
        return r;
    }
}

/** Runtime switches. In production a flag service; here a map the brownout controller flips. */
final class Flags {
    private final Map<String, String> values = new java.util.HashMap<>();
    String get(String key, String dflt) { return values.getOrDefault(key, dflt); }
    void set(String key, String value) { values.put(key, value); }
}

/** Fraud scoring whose cost depends on a flag: the full model, or a cheap rule set under brownout. */
final class FraudScorer {
    static final long FULL_MILLIS = 8;
    static final long RULES_ONLY_MILLIS = 1;
    long serviceMillis(Flags flags) {
        return flags.get("fraud.mode", "full").equals("rules_only") ? RULES_ONLY_MILLIS : FULL_MILLIS;
    }
}

/**
 * Brownout levels. Each level sheds one more priority class at the door and degrades one more feature.
 * Escalates when CRITICAL/HIGH sojourn exceeds target for a window; de-escalates after `cooldownWindows` calm windows.
 */
final class BrownoutController {
    enum Level {
        NORMAL(null, "full"),
        SHED_LOW(Priority.LOW, "rules_only"),
        SHED_NORMAL(Priority.NORMAL, "rules_only"),
        CRITICAL_ONLY(Priority.HIGH, "rules_only");

        final Priority shedAtOrBelow;   // reject requests with priority ordinal >= this one's ordinal
        final String fraudMode;
        Level(Priority shedAtOrBelow, String fraudMode) { this.shedAtOrBelow = shedAtOrBelow; this.fraudMode = fraudMode; }

        boolean sheds(Priority p) { return shedAtOrBelow != null && p.ordinal() >= shedAtOrBelow.ordinal(); }
    }

    private final long targetMillis;
    private final long windowMillis;
    private final int cooldownWindows;
    private final Flags flags;
    private Level level = Level.NORMAL;
    private long windowStart;
    private long windowMaxSojourn = 0;
    private int calmWindows = 0;
    final List<String> transitions = new ArrayList<>();
    final long[] millisAtLevel = new long[Level.values().length];
    private long lastTick;

    BrownoutController(long targetMillis, long windowMillis, int cooldownWindows, Flags flags, long nowMillis) {
        this.targetMillis = targetMillis;
        this.windowMillis = windowMillis;
        this.cooldownWindows = cooldownWindows;
        this.flags = flags;
        this.windowStart = nowMillis;
        this.lastTick = nowMillis;
        flags.set("fraud.mode", level.fraudMode);
    }

    Level level() { return level; }

    /** Called with the sojourn of every CRITICAL or HIGH request as it is dequeued. */
    void observe(long sojournMillis) { windowMaxSojourn = Math.max(windowMaxSojourn, sojournMillis); }

    /** Called every simulated millisecond. Closes a window when it elapses and moves at most one level. */
    void tick(long nowMillis) {
        millisAtLevel[level.ordinal()] += nowMillis - lastTick;
        lastTick = nowMillis;
        if (nowMillis - windowStart < windowMillis) return;
        Level before = level;
        if (windowMaxSojourn > targetMillis) {
            calmWindows = 0;
            if (level.ordinal() < Level.values().length - 1) level = Level.values()[level.ordinal() + 1];
        } else if (windowMaxSojourn <= targetMillis / 2) {
            calmWindows++;
            if (calmWindows >= cooldownWindows && level != Level.NORMAL) {
                level = Level.values()[level.ordinal() - 1];
                calmWindows = 0;
            }
        } else {
            calmWindows = 0;   // in the hysteresis band: hold
        }
        if (level != before) {
            transitions.add(nowMillis + "ms " + before + " -> " + level);
            flags.set("fraud.mode", level.fraudMode);
        }
        windowMaxSojourn = 0;
        windowStart = nowMillis;
    }
}

/** What a policy did with the offered load, per priority. */
final class Outcome {
    final Map<Priority, Long> offered = new EnumMap<>(Priority.class);
    final Map<Priority, Long> shedAtDoor = new EnumMap<>(Priority.class);
    final Map<Priority, Long> droppedInQueue = new EnumMap<>(Priority.class);
    final Map<Priority, List<Long>> latencies = new EnumMap<>(Priority.class);
    long stillQueued;

    Outcome() {
        for (Priority p : Priority.values()) {
            offered.put(p, 0L); shedAtDoor.put(p, 0L); droppedInQueue.put(p, 0L); latencies.put(p, new ArrayList<>());
        }
    }

    long completed(Priority p) { return latencies.get(p).size(); }

    long percentile(Priority p, double q) {
        List<Long> l = new ArrayList<>(latencies.get(p));
        if (l.isEmpty()) return -1;
        Collections.sort(l);
        int idx = (int) Math.ceil(q * l.size()) - 1;
        return l.get(Math.max(0, Math.min(l.size() - 1, idx)));
    }

    long totalOffered() { return offered.values().stream().mapToLong(Long::longValue).sum(); }
    long totalCompleted() { long s = 0; for (Priority p : Priority.values()) s += completed(p); return s; }
    long totalShed() { return shedAtDoor.values().stream().mapToLong(Long::longValue).sum(); }
    long totalDropped() { return droppedInQueue.values().stream().mapToLong(Long::longValue).sum(); }
}

/** A serving policy: how arriving requests are admitted and which one a free worker serves next. */
interface Policy {
    String name();
    /** Returns false if the request is rejected at the door. */
    boolean admit(Request r, long now, Outcome out);
    /** Next request to serve, or null; may drop stale requests as a side effect, recorded in `out`. */
    Request next(long now, Outcome out);
    long serviceMillis(Request r);
    void tick(long now);
    int queued();
}

/** The baseline everyone ships first: one FIFO queue, no limit, full fraud scoring for everyone. */
final class FifoPolicy implements Policy {
    private final ArrayDeque<Request> queue = new ArrayDeque<>();
    private final Flags flags = new Flags();
    private final FraudScorer fraud = new FraudScorer();

    @Override public String name() { return "fifo, no shedding"; }
    @Override public boolean admit(Request r, long now, Outcome out) { queue.addLast(r); return true; }
    @Override public Request next(long now, Outcome out) { return queue.pollFirst(); }
    @Override public long serviceMillis(Request r) { return Simulation.baseServiceMillis(r.priority) + (r.priority == Priority.CRITICAL ? fraud.serviceMillis(flags) : 0); }
    @Override public void tick(long now) {}
    @Override public int queued() { return queue.size(); }
}

/** Priority CoDel queues, brownout controller, and flag-driven fraud degradation. */
final class DegradingPolicy implements Policy {
    final Map<Priority, CoDelQueue> queues = new EnumMap<>(Priority.class);
    final Flags flags = new Flags();
    final BrownoutController brownout;
    private final FraudScorer fraud = new FraudScorer();

    DegradingPolicy(long targetMillis, long intervalMillis, long windowMillis, int cooldownWindows, long nowMillis) {
        for (Priority p : Priority.values()) queues.put(p, new CoDelQueue(targetMillis, intervalMillis));
        brownout = new BrownoutController(targetMillis, windowMillis, cooldownWindows, flags, nowMillis);
    }

    @Override public String name() { return "priority + CoDel + brownout"; }

    @Override public boolean admit(Request r, long now, Outcome out) {
        if (brownout.level().sheds(r.priority)) {
            out.shedAtDoor.merge(r.priority, 1L, Long::sum);
            return false;
        }
        queues.get(r.priority).enqueue(r, now);
        return true;
    }

    @Override public Request next(long now, Outcome out) {
        for (Priority p : Priority.values()) {
            CoDelQueue q = queues.get(p);
            long before = q.dropped;
            Request r = q.dequeue(now);
            long droppedNow = q.dropped - before;
            if (droppedNow > 0) out.droppedInQueue.merge(p, droppedNow, Long::sum);
            if (r != null) {
                if (p == Priority.CRITICAL || p == Priority.HIGH) brownout.observe(now - r.enqueuedMillis);
                return r;
            }
        }
        return null;
    }

    @Override public long serviceMillis(Request r) { return Simulation.baseServiceMillis(r.priority) + (r.priority == Priority.CRITICAL ? fraud.serviceMillis(flags) : 0); }
    @Override public void tick(long now) { brownout.tick(now); }
    @Override public int queued() { int n = 0; for (CoDelQueue q : queues.values()) n += q.size(); return n; }
}

/**
 * Discrete-time simulation: `workers` parallel servers, 1 ms ticks, Bernoulli arrivals per tick from a seeded RNG,
 * with an overload phase in the middle. Deterministic for a given seed.
 */
final class Simulation {
    final int workers;
    final long seed;
    final double normalRps, overloadRps;
    final long overloadFromMillis, overloadToMillis, durationMillis;

    Simulation(int workers, long seed, double normalRps, double overloadRps, long overloadFrom, long overloadTo, long duration) {
        this.workers = workers;
        this.seed = seed;
        this.normalRps = normalRps;
        this.overloadRps = overloadRps;
        this.overloadFromMillis = overloadFrom;
        this.overloadToMillis = overloadTo;
        this.durationMillis = duration;
    }

    static long baseServiceMillis(Priority p) {
        return switch (p) {
            case CRITICAL -> 4;
            case HIGH -> 4;
            case NORMAL -> 3;
            case LOW -> 6;
        };
    }

    /** Traffic mix: 30% critical, 10% high, 40% normal, 20% low. */
    static Priority pick(double u) {
        if (u < 0.30) return Priority.CRITICAL;
        if (u < 0.40) return Priority.HIGH;
        if (u < 0.80) return Priority.NORMAL;
        return Priority.LOW;
    }

    Outcome run(Policy policy) {
        Random random = new Random(seed);
        Outcome out = new Outcome();
        long[] busyUntil = new long[workers];
        Request[] inService = new Request[workers];
        long nextId = 1;
        for (long now = 0; now < durationMillis; now++) {
            double rps = (now >= overloadFromMillis && now < overloadToMillis) ? overloadRps : normalRps;
            // arrivals this millisecond: expected rps/1000, drawn as up to three Bernoulli trials for a Poisson-ish shape
            double p = rps / 3000.0;
            for (int k = 0; k < 3; k++) {
                if (random.nextDouble() < p) {
                    Request r = new Request(nextId++, pick(random.nextDouble()), now);
                    out.offered.merge(r.priority, 1L, Long::sum);
                    policy.admit(r, now, out);
                }
            }
            // workers: finish what is due, then pull the next request
            for (int w = 0; w < workers; w++) {
                if (inService[w] != null && busyUntil[w] <= now) {
                    out.latencies.get(inService[w].priority).add(now - inService[w].arrivalMillis);
                    inService[w] = null;
                }
                if (inService[w] == null) {
                    Request r = policy.next(now, out);
                    if (r != null) {
                        inService[w] = r;
                        busyUntil[w] = now + policy.serviceMillis(r);
                    }
                }
            }
            policy.tick(now);
        }
        out.stillQueued = policy.queued();
        for (Request r : inService) if (r != null) out.stillQueued++;
        return out;
    }
}

public final class Main {
    static final int WORKERS = 8;
    static final long TARGET = 5, INTERVAL = 100, WINDOW = 100;
    static final int COOLDOWN = 5;

    static Simulation standardScenario(long seed) {
        // 8 workers, mean service ~6.4 ms at full fraud -> capacity ~1,250 rps. Normal 800 rps; overload 2,400 rps for 3 s.
        return new Simulation(WORKERS, seed, 800, 2400, 2000, 5000, 9000);
    }

    static void printOutcome(Outcome o) {
        System.out.printf("%-9s %8s %8s %8s %9s %8s %8s%n", "priority", "offered", "shed", "dropped", "completed", "p50 ms", "p99 ms");
        for (Priority p : Priority.values()) {
            System.out.printf("%-9s %8d %8d %8d %9d %8d %8d%n", p, o.offered.get(p), o.shedAtDoor.get(p), o.droppedInQueue.get(p),
                    o.completed(p), o.percentile(p, 0.50), o.percentile(p, 0.99));
        }
        System.out.printf("total: offered %d, completed %d, shed at door %d, dropped in queue %d, still queued at end %d%n%n",
                o.totalOffered(), o.totalCompleted(), o.totalShed(), o.totalDropped(), o.stillQueued);
    }

    public static void main(String[] args) {
        System.out.println("== CoDel on one queue: 5 ms target, 100 ms interval ==");
        CoDelQueue q = new CoDelQueue(TARGET, INTERVAL);
        long now = 0;
        for (int i = 0; i < 400; i++) q.enqueue(new Request(i, Priority.NORMAL, now), now);
        int served = 0;
        for (now = 0; now < 400 && q.size() > 0; now += 1) {        // drain one per ms: a 400-deep queue sojourns up to 400 ms
            if (q.dequeue(now) != null) served++;
        }
        System.out.println("400 queued, drained at 1/ms: served " + served + ", dropped " + q.dropped + ", max sojourn seen " + q.maxSojournSeen + " ms");
        System.out.println("(sojourn reaches the 5 ms target at t=5; the first drop waits a full interval, at t=105; later drops are spaced interval/sqrt(count))");
        System.out.println();

        Simulation sim = standardScenario(11);
        System.out.println("== 8 workers, 800 rps normal, 2,400 rps overload from t=2s to t=5s, 9 s total (seed 11) ==");
        System.out.println("-- " + new FifoPolicy().name() + " --");
        Outcome fifo = sim.run(new FifoPolicy());
        printOutcome(fifo);

        DegradingPolicy degrading = new DegradingPolicy(TARGET, INTERVAL, WINDOW, COOLDOWN, 0);
        System.out.println("-- " + degrading.name() + " --");
        Outcome shed = sim.run(degrading);
        printOutcome(shed);

        System.out.println("brownout transitions:");
        for (String t : degrading.brownout.transitions) System.out.println("  " + t);
        System.out.print("time at each level (ms):");
        for (BrownoutController.Level l : BrownoutController.Level.values()) System.out.print("  " + l + "=" + degrading.brownout.millisAtLevel[l.ordinal()]);
        System.out.println();
        System.out.println("fraud.mode at end: " + degrading.flags.get("fraud.mode", "full"));
        System.out.println();
        System.out.println("Reading the tables: FIFO completes more LOW requests and makes every CRITICAL authorization wait behind them,");
        System.out.println("so CRITICAL p99 explodes and the backlog is still draining when the run ends. The degrading policy sheds LOW");
        System.out.println("and NORMAL at the door, flips fraud to rules-only, lets CoDel trim stale queue entries, and keeps CRITICAL p99");
        System.out.println("within tens of milliseconds through the whole overload. It completes fewer requests and the right ones.");
    }
}
