---
name: runbook-gc-pause-triage
description: On-call runbook for a latency spike suspected to be garbage collection, on the JVM or the Go runtime, from confirmation to mitigation
phase: 4
lesson: 9
tags: [gc, latency, runbook, jvm, go, on-call]
---

# GC Pause Triage Runbook

Use when p99 or max latency spikes with no correlated request, dependency, or deploy. Work top to bottom; stop when the step tells you to.

## 1. Confirm it is the collector (5 minutes)

- [ ] Spikes are periodic or burst-shaped and hit **every** in-flight request at the same instant. Per-request causes (slow account, hot key) hit specific inputs instead.
- [ ] JVM: `-Xlog:gc*` is on (if not, enable it on one instance now; it is safe in production). Grep for the spike timestamp. A `Pause` line whose duration matches the spike confirms it.
- [ ] Go: `GODEBUG=gctrace=1` on one instance, or the `/runtime/gc/pauses:seconds` and `/gc/heap/live:bytes` metrics from `runtime/metrics`. Pauses above 1 ms, or GC CPU above 25 percent, are findings.
- [ ] Not confirmed? Check safepoints (`-Xlog:safepoint`: a long time-to-safepoint with a short collection is a stuck thread, not GC), then CPU throttling in the container, then stop using this runbook.

## 2. Classify the pause (5 minutes)

| Log evidence | Classification | Go to |
|--------------|----------------|-------|
| JVM `Pause Young`, 5 to 50 ms, frequent | allocation pressure | step 3 |
| JVM `Pause Young (Mixed)` 50 to 200 ms, periodic | promotion pressure | step 4 |
| JVM `Pause Full` (G1), any duration | headroom or fragmentation; incident-grade | step 5 |
| JVM ZGC `Allocation Stall` lines | collector cannot keep up | step 3 |
| JVM heap-after keeps rising across cycles | leak | step 6 |
| Go clock field third number above 1 ms, or STW phases in ms | stacks or globals scan; many goroutines | step 6 |
| Go CPU field dominated by assist | allocation outrunning marking | step 3 |
| Go live heap (third heap number) rising across cycles | leak or goroutine leak | step 6 |

## 3. Allocation pressure

- [ ] Compute allocation rate: heap growth between two consecutive collections divided by the interval. Record it.
- [ ] Immediate mitigation: scale out (halves per-instance rate), or increase young generation / `GOGC` if memory headroom exists.
- [ ] Root cause: allocation profile (JFR `jdk.ObjectAllocationSample`, async-profiler `-e alloc`, Go `pprof -alloc_space`). The top three frames almost always account for most of it: parse buffers, string concatenation in logging, per-request JSON serialisation.

## 4. Promotion pressure

- [ ] Compute promotion rate: old-generation growth per young collection. Compare with what should survive a request (cache entries, rate-limit windows).
- [ ] If promotion is higher than the design implies, something short-lived is living long enough to be tenured: a queue that drains slowly, a response held until a batch flushes. Fix the lifetime, not the collector.
- [ ] If promotion is as designed, G1 `-XX:MaxGCPauseMillis` lower with more frequent mixed cycles, or move to ZGC/Shenandoah.

## 5. Full GC on G1 (incident)

- [ ] Page the owning team; every request in flight during the pause breached the issuer timeout. Reconcile duplicates via the idempotency store for the pause window.
- [ ] Check for humongous allocations (`-Xlog:gc+humongous`): large arrays from file imports or oversized responses on the serving JVM.
- [ ] Short term: more heap headroom (`-Xmx`), larger regions (`-XX:G1HeapRegionSize`), `-XX:G1ReservePercent` up.
- [ ] Medium term: separate batch work from the serving path, or switch the serving path to a collector with no full-GC fallback and verify with a load test that it shows no allocation stalls.

## 6. Leak

- [ ] JVM: two heap histograms (`jcmd <pid> GC.class_histogram`) ten minutes apart; the class whose count grows is the leak. Heap dump only if the histogram is ambiguous.
- [ ] Go: `pprof -inuse_space` twice; and `runtime.NumGoroutine()` over time. A rising goroutine count is a goroutine leak; find the blocked `select` or channel receive with the goroutine profile.
- [ ] Mitigation while fixing: scheduled restarts are acceptable for hours, not weeks. Set `GOMEMLIMIT` or `-Xmx` with headroom so the orchestrator restarts the instance before it enters the death spiral.

## 7. Close out

- [ ] Record: collector and version, allocation rate, promotion rate, pause histogram by cause, before and after.
- [ ] Add alerts: any `Pause Full`; any allocation stall; Go GC CPU above 25 percent for five minutes; live heap trend over 24 hours.
- [ ] Re-derive the pause budget from the SLO (p99 target minus median, halved) and write it in the service's README next to the collector flags.
