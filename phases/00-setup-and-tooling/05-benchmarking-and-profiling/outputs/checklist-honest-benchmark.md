---
name: checklist-honest-benchmark
description: Review checklist for any pull request, design doc, or load-test report that cites a performance number
phase: 0
lesson: 5
tags: [benchmarking, profiling, latency, review, jmh, jfr]
---

# Honest Benchmark Review Checklist

Use when someone writes "X is faster than Y" or "p99 is N ms". One unchecked box means the number is an argument, not a measurement.

## The micro-benchmark

- [ ] Warm-up iterations were run and discarded, and the warm-up samples are shown (or the harness is JMH with `@Warmup` of at least 5 iterations).
- [ ] At least three forks (JMH) or three repeated runs, with each run's median reported separately, not averaged into one number.
- [ ] The spread across forks is stated. Above 10% coefficient of variation, the row is not a result.
- [ ] Every result reaches a blackhole or is otherwise provably not eliminated (`Blackhole.consume`, a volatile sink, or `-prof perfasm` output attached).
- [ ] Inputs come from fields or arrays populated at runtime, not literals the compiler can fold.
- [ ] Percentiles (p50, p90, p99) and max are reported. A mean alone is rejected.
- [ ] Percentile definition is stated (nearest-rank or interpolated) when sample counts are small.
- [ ] The timer is monotonic (`System.nanoTime`, `perf_counter_ns`, `time.Now` in Go), never wall-clock.
- [ ] Operations per iteration are high enough that the timer's own cost (20-30 ns per read) is below 1% of a sample.

## The comparison

- [ ] Both sides were run in the same JVM configuration, in both orders, or in separate forks; the first benchmark in a process is not the baseline.
- [ ] The input size and distribution match production (number of keys, key distribution, hit rate, working set versus cache size).
- [ ] The cases where the proposed winner loses are shown (for a collection: every access pattern the service uses, not the one that flatters it).
- [ ] The complexity analysis is stated and the measurement agrees with it, or the disagreement is explained (cache effects, branch prediction, allocation).
- [ ] The number is not used to claim anything about memory, GC pressure, or behaviour under concurrent load unless those were measured.

## The load test

- [ ] The generator is open-loop (sends on schedule regardless of responses), or the closed-loop results are corrected for coordinated omission (`recordValueWithExpectedInterval` or equivalent) and the expected interval is stated.
- [ ] Latency is measured from the scheduled send time, not from the time the request actually left the generator.
- [ ] p99 and p99.9 are reported with the sample count; a p99 from fewer than 1,000 samples is noted as low confidence.
- [ ] The test ran long enough to include at least several GC cycles and whatever periodic work the service does.
- [ ] Resource saturation (CPU, connection pool, thread pool) on the generator itself was ruled out.

## The profile

- [ ] The profiler is sampling (JFR, async-profiler), not instrumenting, unless call counts are the point.
- [ ] Self time and inclusive time are both shown; the recommendation names which one it is acting on.
- [ ] Safepoint bias is ruled out for hot loops (async-profiler, or JFR on JDK 17+).
- [ ] Inlined frames are accounted for (JFR reconstructs them; `jstack` does not).
- [ ] The profile was taken under representative load, not on an idle service or during warm-up.

## Three questions to ask of any performance claim

1. What did the first iteration cost, and when did it stop falling?
2. If I ran it again, how different would the number be, and how do you know?
3. What would this number look like if the thing being measured had stalled for one second in the middle?
