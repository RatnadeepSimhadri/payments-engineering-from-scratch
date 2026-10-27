# Randomized Algorithms and Reservoir Sampling

> Sampling transactions for audit without storing them all.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 2, Lesson 01 (Sorting Algorithms), Phase 1, Lesson 07 (Hash Functions and Collisions), Phase 1, Lesson 09 (Heaps and Priority Queues), Phase 10, Lesson 01 (Representing Money)
**Time:** ~60 minutes

## Learning Objectives

- Distinguish Las Vegas from Monte Carlo algorithms by what is random (running time or correctness) and name one of each that a payments system runs
- Build reservoir sampling (Algorithm R), prove its invariant by induction, and extend it to weighted sampling with the A-Res key trick
- Build Fisher-Yates and show, by counting outcomes, why the obvious "swap each position with a random one" shuffle is biased
- Build randomized quickselect for medians and percentiles in expected linear time and explain why a random pivot defeats adversarial input
- Build a seeded, portable generator, explain modulo bias and how rejection sampling avoids it, and state why none of this is suitable for secrets

## The Problem

An internal audit team asks the payments platform for "a random sample of 500 of today's transactions" every day, to re-verify fees, FX rates, and sanctions screening results by hand. Today has 41 million transactions, streaming through a Kafka topic that is retained for 24 hours and never fully materialized anywhere queryable before the end-of-day batch. The first implementation waits for the batch, loads the day's transaction ids into memory (41 million strings, about 2.5 GB), shuffles them with `Collections.shuffle`, and takes the first 500. It works, it finishes at 03:00, and it needs a machine with 8 GB of heap for a job whose output is 500 rows.

The second implementation tries to be clever: keep each transaction with probability `500 / 41,000,000` as it streams past. On a quiet day with 30 million transactions it produces about 365 samples; on a heavy day, 620. The auditors want exactly 500, and more importantly the sampling probability depends on a count that is not known until the day is over. Someone patches it to "keep the first 500", which is a sample of the 00:00 to 00:04 window, when the only traffic is batch jobs and two time zones. The audit finds no fee errors for a month, then a regulator finds several, all in the afternoon.

A third version is asked for by the audit team after that: "sample proportional to amount, because a 50,000 USD wire matters more than a 4 USD coffee". And a fourth question arrives from the risk team: "what is the median and the 99th percentile transaction amount today", which the batch job answers by sorting all 41 million amounts, again in 8 GB of heap, when the answer could be found in one pass over a copy with no sort at all.

All four problems are solved by algorithms that make random choices. Reservoir sampling produces exactly `k` items from a stream of unknown length, uniformly, in `O(k)` memory. A weighted reservoir does the same proportional to amount. Randomized quickselect finds any percentile in expected linear time. And underneath all of them is a random number generator that must be seeded for reproducibility (an audit sample you cannot regenerate is not evidence), uniform (a biased shuffle is a biased audit), and fast. This lesson builds each one and tests the uniformity claims statistically, because a randomized algorithm whose distribution you have not measured is one you are taking on faith.

## The Concept

### Two kinds of randomized algorithm

| | Las Vegas | Monte Carlo |
|---|---|---|
| What is random | the running time | the answer |
| What is guaranteed | the answer is always correct | the running time is bounded |
| Failure mode | occasionally slow (with vanishing probability) | occasionally wrong (with bounded, tunable probability) |
| Examples in this lesson | randomized quickselect, Fisher-Yates (always a valid permutation) | Miller-Rabin primality, Bloom filters (Phase 3, Lesson 01), HyperLogLog |
| In payments | randomized pivots in sorting and selection; randomized retry backoff (jitter) | probabilistic duplicate detection, cardinality estimates, sampling-based reconciliation |

Reservoir sampling is neither exactly: it always runs in one pass and always produces exactly `k` items, and the randomness is in *which* items, which is the point. Its correctness claim is about a distribution ("every item equally likely"), and testing it means measuring frequencies over many runs, not checking one output.

The reason to randomize at all is adversarial or structured input. Quicksort with a fixed middle pivot is `O(n log n)` on average and `O(n^2)` on inputs that someone can construct, and sorted input (which is what a database returns) is one of them. A random pivot makes every input look like the average case: there is no bad input, only bad luck, and bad luck has a probability you can compute. Las Vegas algorithms trade a deterministic worst case for a probabilistic one; Monte Carlo algorithms trade certainty for speed or memory.

### Reservoir sampling: Algorithm R

Keep a reservoir of `k` slots. The first `k` items fill it. For item number `n > k` (counting from 1), pick a uniform integer `j` in `[0, n)`; if `j < k`, replace slot `j` with the new item, otherwise discard the new item.

**Invariant:** after `n >= k` items, every item seen so far is in the reservoir with probability exactly `k / n`.

Proof by induction. After exactly `k` items, every item is in the reservoir with probability `1 = k/k`. Assume it holds after `n - 1` items. Item `n` enters with probability `k / n` by construction. An earlier item `x` was in the reservoir with probability `k / (n-1)`; it survives item `n` unless item `n` enters *and* chooses `x`'s slot, which happens with probability `(k/n) * (1/k) = 1/n`. So `x` is in the reservoir after item `n` with probability `k/(n-1) * (1 - 1/n) = k/(n-1) * (n-1)/n = k/n`. Every item, old and new, is at `k/n`.

Worked example, `k = 3`, stream `1..10`, one draw per item:

```text
item  n   draw j in [0,n)   action                reservoir
1     1   (fills)                                 [1]
2     2   (fills)                                 [1, 2]
3     3   (fills)                                 [1, 2, 3]
4     4   j = 1             1 < 3: slot 1 := 4    [1, 4, 3]
5     5   j = 4             4 >= 3: discard       [1, 4, 3]
6     6   j = 0             slot 0 := 6           [6, 4, 3]
7     7   j = 5             discard               [6, 4, 3]
8     8   j = 2             slot 2 := 8           [6, 4, 8]
9     9   j = 7             discard               [6, 4, 8]
10   10   j = 1             slot 1 := 10          [6, 10, 8]
```

One specific run proves nothing; the invariant says each of the ten items ends up in the reservoir with probability 3/10. The test runs 30,000 streams and checks that every item was selected about 9,000 times (within 5%), and specifically that item 10 is not favoured over item 1. That last check catches the most common bug, an off-by-one in `n` that makes the newest item too likely.

Cost: one random draw per item after the first `k`, `O(n)` time, `O(k)` memory, one pass, and no knowledge of `n` in advance. Vitter's later Algorithm Z skips ahead by a random gap instead of drawing per item, which matters when drawing is expensive relative to reading; Algorithm R is what you want when the stream is the bottleneck.

### Weighted reservoir: the A-Res key trick

To sample with probability proportional to weight `w` (here: the transaction amount in minor units), Efraimidis and Spirakis give a one-line change: for each item draw `u` uniform in `(0, 1)` and compute the key `u^(1/w)`; keep the `k` items with the largest keys. A min-heap of size `k` on the keys does this in `O(n log k)`.

Why it works for `k = 1`: `P(u^(1/w) <= t) = P(u <= t^w) = t^w`, so the key's distribution function is `t^w`. For two items with weights `a` and `b`, the probability that item `a`'s key is the larger is `a / (a + b)`, exactly proportional. The general `k` case follows by the same argument on order statistics. The test checks a 9:1 weight pair over 20,000 trials and expects about 90% selections of the heavy item.

Keys are `double`s, and that is correct here: the key is not money, it is a random real used for ordering, and `Math.pow` on doubles is the right tool. The weight passed in is the exact `long` minor-unit amount; nothing about the money itself is ever a float.

### Fisher-Yates and the shuffle that is not uniform

To shuffle `n` items so that every one of the `n!` permutations is equally likely: for `i` from `n - 1` down to `1`, draw `j` uniform in `[0, i]` and swap positions `i` and `j`. Position `n - 1` receives a uniform choice among all `n` items; position `n - 2` a uniform choice among the remaining `n - 1`; and so on. That is `n * (n-1) * ... * 1 = n!` equally likely outcomes, one per permutation. Knuth's Algorithm P, `O(n)`, in place.

The tempting alternative is to swap every position with a uniformly random position from the *whole* list: `for i in 0..n-1: swap(a[i], a[random(0, n)])`. Count the outcomes: `n` choices at each of `n` steps is `n^n` equally likely execution paths. For `n = 3` that is 27 paths mapping onto 6 permutations, and 27 is not divisible by 6, so some permutations must be hit by more paths than others. The exact counts for `[a, b, c]`:

```text
permutation   paths of 27   probability   expected of 60,000
abc           4             0.148         8,889
acb           5             0.185        11,111
bac           5             0.185        11,111
bca           5             0.185        11,111
cab           4             0.148         8,889
cba           4             0.148         8,889
```

The demo runs both shuffles 60,000 times and the counts land within a few hundred of those numbers; the tests assert both the uniformity of Fisher-Yates and the specific non-uniformity of the naive shuffle. A biased shuffle in an audit sampler means certain positions in the day are systematically over-sampled; in a card-number tokenization scheme or a lottery it is a reportable defect.

### Randomized quickselect

To find the `k`-th smallest of `n` values without sorting: pick a pivot, partition the array so that smaller values are left of it and larger ones right, and recurse into the one side that contains index `k`. Each step discards the other side entirely, which is what makes it faster than sorting.

With a pivot chosen uniformly at random, the expected size of the side you keep is at most `3n/4` with probability at least `1/2`, so the expected total work is a geometric series summing to `O(n)`: `n + 3n/4 + 9n/16 + ... = 4n` comparisons in expectation. The worst case is `O(n^2)` (every pivot is the minimum or maximum), but since the pivot is random the probability of a bad run on a given input is the same for every input, and it falls exponentially in the depth. This is Las Vegas: the answer is exactly the `k`-th smallest every time; only the time varies. The test runs the same selection under twenty different seeds and asserts the same answer.

Worked example, find the median (index 4) of `[9, 1, 8, 2, 7, 3, 6, 4, 5, 0]`:

```text
pivot 7 (random): partition -> [1, 2, 0, 3, 6, 4, 5 | 7 | 8, 9]      pivot lands at index 7; k=4 < 7, keep indices 0..6
pivot 3:          partition -> [1, 2, 0 | 3 | 6, 4, 5]                pivot at index 3; k=4 > 3, keep indices 4..6
pivot 5:          partition -> [4 | 5 | 6]                            pivot at index 5; k=4 < 5, keep index 4
one element left: answer 4
```

Three partitions over 10 + 7 + 3 = 20 elements, versus `O(n log n)` for a sort. On 41 million amounts that is one pass and a bit instead of a full sort, and it needs no memory beyond the array.

Percentiles follow from selection: the nearest-rank `p99` is the element at index `ceil(0.99 * n) - 1`. The demo computes the median and `p99` of 10,000 generated amounts and checks both against a sorted copy.

### The generator underneath

Everything above draws from a pseudorandom number generator, and three properties of the generator are load-bearing.

**Seeded and portable.** An audit sample must be reproducible: given the stream and the seed, the same 500 transactions come out, so an auditor can verify the sample was not hand-picked. `java.util.Random`, Python's `random`, and Go's `math/rand` are all seedable but use different algorithms, so the same seed gives different streams across languages. The lesson implements SplitMix64 (Steele, Lea, Flood, 2014) in a dozen lines so the sequence is identical in every port; the test asserts the published first output for seed 0, `0xE220A8397B1DCDAF`.

**Uniform integers without modulo bias.** `nextLong() % bound` is biased whenever `bound` does not divide `2^64`: the residues `0 .. (2^64 mod bound) - 1` each get one extra preimage. For `bound = 7` the bias is one part in `2^61` and no test will ever see it; for `bound` near `2^63` it is severe. The portable fix is rejection sampling: mask the random bits down to the smallest power of two covering `bound`, and redraw if the result is `>= bound`. Expected draws are less than 2. `Rng.nextInt` does this and the test checks 70,000 draws of `nextInt(7)` land within 5% of uniform.

**Not cryptographic.** SplitMix64's entire state is 64 bits and its output function is invertible, so one observed output reveals every future one. That is fine for sampling and shuffling and fatal for session tokens, idempotency keys that must be unguessable, or card PAN tokenization. Those use `java.security.SecureRandom`, `secrets` in Python, `crypto/rand` in Go. The rule: if an attacker benefits from predicting the next value, the generator must be cryptographic; if the only cost of predictability is that the sample is reproducible, you want it reproducible.

### Monte Carlo done honestly: Miller-Rabin

To round out the taxonomy, the lesson includes one Monte Carlo algorithm with a one-sided error. Miller-Rabin tests whether `n` is prime by picking a random base `a` and checking whether `a` witnesses `n`'s compositeness via Fermat's little theorem strengthened with the square-root-of-one test. If any base is a witness, `n` is certainly composite. If `r` random bases all fail to witness, `n` is prime with probability at least `1 - 4^-r`. The error is one-sided: a true prime is *never* declared composite, and the test asserts that for every prime below 5,000 with a single round.

The number 561 is in the demo because it is the smallest Carmichael number: `a^560 = 1 mod 561` for every `a` coprime to 561, so the plain Fermat test passes it as prime on every base. Miller-Rabin's extra step catches it. This is where primality testing lives in payments: RSA key generation (`BigInteger.probablePrime`) and the mod-97 and Luhn checks of Lesson 15 both rest on modular arithmetic, and knowing which checks are exact and which are probabilistic is the difference between "this IBAN is valid" and "this IBAN is probably valid".

### Testing a randomized algorithm

A deterministic algorithm is tested by asserting outputs. A randomized one is tested by asserting *distributions*, and that takes a little arithmetic to do without writing a flaky test.

Each check in this lesson's tests has the form "run `N` trials, count how often event `E` happens, assert the count is within `tolerance` of `N * p`". The count is binomial with mean `N p` and standard deviation `sqrt(N p (1 - p))`. For the reservoir check, `N = 30,000`, `p = 0.3`, so the standard deviation is `sqrt(30000 * 0.3 * 0.7) = 79`. The tolerance of 450 is about 5.7 standard deviations. If the implementation is correct, a deviation that large happens with probability around `10^-8` per item; if it is wrong in the usual way (the last item at probability `k/(n-1)` instead of `k/n`, which is `0.333` instead of `0.300`), the expected count moves by `30000 * 0.033 = 1000`, more than twice the tolerance, and the test fails every time.

| Check | `N` | `p` | std dev | tolerance | in std devs | smallest bug it catches |
|---|---|---|---|---|---|---|
| `nextInt(7)` bucket | 70,000 | 1/7 | 93 | 500 | 5.4 | modulo bias at bound 7 would be invisible (1 in 2^61); this catches a broken mask |
| reservoir item | 30,000 | 0.30 | 79 | 450 | 5.7 | off-by-one in `n` (shift of 1,000) |
| weighted 9:1 | 20,000 | 0.90 | 42 | 400 | 9.4 | forgetting the `1/w` exponent (would give 50%) |
| Fisher-Yates permutation | 60,000 | 1/6 | 91 | 400 | 4.4 | naive shuffle (shift of 1,111 on three permutations) |

Two rules follow. Pick `N` so that the bug you care about moves the mean by several standard deviations more than the tolerance, and pick the tolerance at five or more standard deviations so that a correct implementation essentially never fails. Then seed the generator, so that the test is not merely unlikely to flake but *cannot* flake: the same seed produces the same counts on every run, and the tolerance is there to make the assertion mean something, not to absorb variance. A statistical test with an unseeded generator is a test that will fail in CI at 3 a.m. on a Sunday once a year, and someone will "fix" it by widening the tolerance until it catches nothing.

### What randomization does not do

A random sample is representative on average, not in every draw: 500 of 41 million can miss a fraud pattern that touched 200 transactions with probability about `(1 - 500/41e6)^200 = 99.76%`. Sampling is for estimating rates and spot-checking process, not for finding needles; needles need the exhaustive screening of Lesson 12. A seeded generator makes a sample reproducible, not unpredictable; anyone who knows the seed and the stream order knows the sample, which is exactly what the auditor wants and exactly what a fraudster placing transactions to dodge the sample would want too, so the seed is a secret from the business and an open book to the auditor. Expected linear time is not a latency guarantee: quickselect's `O(n^2)` tail is astronomically unlikely but not impossible, and a hard real-time path uses the deterministic median-of-medians instead. And Monte Carlo error bounds are only as good as the generator: Miller-Rabin's `4^-r` assumes the bases are uniform, and a biased generator can make the bound a lie.

## Build It

The code is in `code/java/Main.java`: `Rng`, `ReservoirSampler`, `WeightedReservoirSampler`, `Shuffle`, `Quickselect`, `MillerRabin`, plus `Money` and `Transaction` for the demo.

### Step 1: SplitMix64

```java
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        long z = (state += 0x9E3779B97F4A7C15L);
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }
}
```

The state advances by the golden-ratio constant each call; the output is a mixing function of the new state. `>>>` is the unsigned shift, which matters because `long` is signed and an arithmetic shift would smear the sign bit. `nextDouble` takes the top 53 bits (a `double` mantissa) and scales by `2^-53`, giving a uniform value in `[0, 1)` that can never be exactly 1. Java, Python, and Go can all reproduce this stream bit for bit, which is the reason to write it rather than call `java.util.Random`.

**Predict:** `new Rng(0).nextLong()` is `0xE220A8397B1DCDAF`. What is the first output of `new Rng(0x9E3779B97F4A7C15L)`? (Hint: look at what `state` is after the first increment.)

### Step 2: Uniform integers by rejection

```java
int nextInt(int bound) {
    if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
    long mask = Long.highestOneBit(bound) * 2 - 1;
    while (true) {
        long candidate = nextLong() & mask;
        if (candidate < bound) return (int) candidate;
    }
}
```

`mask` is the smallest `2^k - 1` that is at least `bound - 1`, so `candidate` is uniform on `[0, 2^k)` and strictly less than `2 * bound`. Rejecting candidates `>= bound` leaves a uniform distribution on `[0, bound)` with at most half the draws rejected. For `bound = 7`, `mask = 7`, and one draw in eight is rejected. There is no `%` anywhere.

**Predict:** for `bound = 1`, what is `mask`, and how many iterations does the loop take?

### Step 3: Algorithm R

```java
void offer(T item) {
    seen++;
    if (reservoir.size() < k) {
        reservoir.add(item);
        return;
    }
    long slot = nextLong(seen);
    if (slot < k) reservoir.set((int) slot, item);
}
```

`seen` is `n` in the proof, incremented before use so the `n`-th item draws from `[0, n)`. One draw decides both whether the item enters (`slot < k`, probability `k/n`) and which slot it takes (uniform over `k`), which is what makes the eviction probability of each existing item exactly `1/n`. Drawing twice (one coin for entry, one for slot) would also be correct but costs two draws; drawing `[0, k)` for the slot and a separate `[0, n)` for entry is a common bug when the two are mixed up.

**Predict:** if `seen++` were moved after the draw, so item `n` drew from `[0, n-1)`, which item would be over-represented, and by how much on the stream `1..10` with `k = 3`?

### Step 4: Weighted keys

```java
void offer(T item, long weight) {
    if (weight <= 0) throw new IllegalArgumentException("weight must be positive: " + weight);
    double u = rng.nextDouble();
    if (u == 0.0) u = Double.MIN_NORMAL;
    double key = Math.pow(u, 1.0 / weight);
    if (heap.size() < k) {
        heap.add(new Entry<>(key, item));
    } else if (key > heap.peek().key) {
        heap.poll();
        heap.add(new Entry<>(key, item));
    }
}
```

A min-heap on keys holds the `k` largest seen so far; a new item enters only if its key beats the smallest kept. `u = 0` would give key 0 for every weight and is nudged to the smallest positive normal double. The weight is the transaction's minor-unit amount, an exact `long`; the key is a `double` because it is a random real, not an amount.

### Step 5: Fisher-Yates, and its evil twin for the test

```java
static <T> void fisherYates(List<T> a, Rng rng) {
    for (int i = a.size() - 1; i > 0; i--) {
        int j = rng.nextInt(i + 1);
        Collections.swap(a, i, j);
    }
}

static <T> void naiveShuffle(List<T> a, Rng rng) {
    for (int i = 0; i < a.size(); i++) {
        int j = rng.nextInt(a.size());
        Collections.swap(a, i, j);
    }
}
```

The only differences: Fisher-Yates runs from the end and draws from `[0, i]`, the shrinking unprocessed prefix; the naive version draws from the whole list every time. `permutationCounts` (in the same class) runs either one 60,000 times on `[a, b, c]` and tallies the six permutations, which is how the demo prints the two columns and how the test asserts that one is flat and the other is not.

**Predict:** for `n = 4`, how many execution paths does the naive shuffle have, how many permutations are there, and can the distribution be uniform?

### Step 6: Quickselect with a random pivot

```java
static long select(long[] a, int k, Rng rng) {
    int lo = 0, hi = a.length - 1;
    while (lo < hi) {
        int pivotIndex = lo + rng.nextInt(hi - lo + 1);
        int p = partition(a, lo, hi, pivotIndex);
        if (k == p) return a[k];
        if (k < p) hi = p - 1; else lo = p + 1;
    }
    return a[lo];
}

static int partition(long[] a, int lo, int hi, int pivotIndex) {
    long pivot = a[pivotIndex];
    swap(a, pivotIndex, hi);
    int store = lo;
    for (int i = lo; i < hi; i++) {
        if (a[i] < pivot) swap(a, i, store++);
    }
    swap(a, store, hi);
    return store;
}
```

Lomuto partition: move the pivot to the end, sweep everything smaller to the front, put the pivot after them. The pivot's final index `p` is its rank, so if `k == p` the answer is in hand; otherwise one side is discarded. The loop is iterative rather than recursive, so the stack depth is constant even in the unlucky `O(n^2)` case. Duplicates go to the right of the pivot (`<` not `<=`), which is fine for selection and is tested on `[5, 5, 5, 1, 5]`.

**Predict:** if the pivot were always `a[hi]` instead of random, what input of length 10,000 would make `select` do about 50 million comparisons?

### Step 7: Miller-Rabin

```java
static boolean isWitness(long a, long d, int s, long n) {
    long x = powMod(a, d, n);
    if (x == 1 || x == n - 1) return false;
    for (int i = 1; i < s; i++) {
        x = (x * x) % n;
        if (x == n - 1) return false;
    }
    return true;
}
```

With `n - 1 = 2^s * d`, compute `a^d`, then square it `s - 1` times. If the sequence ever hits `-1` (that is, `n - 1`), or starts at `1`, `a` is not a witness. Otherwise `a` has exhibited a square root of 1 other than `+-1`, which cannot exist modulo a prime, so `n` is composite. `isProbablePrime` draws `rounds` random bases from `[2, n-2]` and returns `false` on the first witness. The `n < 2^31` guard keeps `x * x` inside a `long`; `BigInteger.isProbablePrime` is the unbounded version.

### Step 8: Run it

```bash
cd phases/02-algorithms/14-randomized-algorithms/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Reservoir sampling: 5 of 10,000 transactions for audit, one pass, O(k) memory ==
seen 10000, sample: [txn-08296, txn-04614, txn-02380, txn-08666, txn-09728]

== Weighted reservoir: probability proportional to amount ==
  txn-09397  3663.56 USD
  txn-05887  2498.20 USD
  ...

== Fisher-Yates vs the naive shuffle: 60,000 shuffles of [a, b, c] ==
perm       fisher      naive
abc         10056       8786
acb          9904      11268
bac          9940      10959
bca          9966      11043
cab         10031       8864
cba         10103       9080
expected 10,000 each; naive gives acb, bac, bca 5/27 each (11,111) and the others 4/27 (8,889)

== Quickselect: median and p99 amount without sorting 10,000 values ==
median 110.90 USD (sorted says 110.90 USD)
p99    4472.57 USD (sorted says 4472.57 USD)

== Miller-Rabin: Monte Carlo primality, 561 is a Carmichael number that fools Fermat ==
         561  probable prime after 10 rounds: false  trial division: false
     1000003  probable prime after 10 rounds: true   trial division: true
```

The 39 tests are mostly statistical: the generator's uniformity over 70,000 draws, the reservoir invariant over 30,000 streams with a specific check that the last item is not favoured, the 9:1 weighted selection rate, both shuffles' permutation frequencies against their exact expected values, quickselect against a sort for every rank and under twenty seeds, and Miller-Rabin against trial division for every `n` below 5,000 including the one-sided-error property. Because the generator is seeded, every one of these is deterministic: the tolerances exist to make the test meaningful, not to absorb flakiness.

### In Python

```python
def _next_u64(self) -> int:
    self._state = (self._state + 0x9E3779B97F4A7C15) & MASK64
    z = self._state
    z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64  # >> on a masked value is Java's >>>
    z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
    return z ^ (z >> 31)
```

Every class keeps its name and algorithm, and every number in the demo matches the Java run. Four things differ, all in `code/python/main.py`:

- **SplitMix64 needs explicit 64-bit wraparound.** Python integers never wrap, so the state and both multiplies are masked with `MASK64`. The state is held unsigned, which makes `>>` a logical shift (Java's `>>>`); `next_long` converts to a signed value with `to_int64` so it returns exactly what Java's `nextLong()` returns (the reference output `0xE220A8397B1DCDAF` is negative as a Java `long`, and a test checks that). `random.Random` must not be substituted: it is a Mersenne Twister and the reference test would fail.
- **The heap's array order is part of the output.** `WeightedReservoirSampler.sample()` iterates the heap's backing array, as the Java iterates `PriorityQueue`. `heapq.heappop` sifts differently from `PriorityQueue.poll` (hole to a leaf, then back up), which can arrange equal keys differently, so the port includes `JavaPriorityQueue`, a short transcription of the JDK's `siftUp`/`siftDown`. The weighted sample prints in the same order as the Java run. `math.pow` and Java's `Math.pow` are each within one ulp of the exact result and agree on every key in the demo; a disagreement could only swap two keys that differ in the last bit.
- **Truncating division in `Money.__str__`.** This lesson's Java `toString` uses `/` and `%` directly; for a negative amount those truncate toward zero, where Python's `//` and `%` floor. `trunc_div` and `java_rem` reproduce the Java result (`-150` prints `-1.50 USD`), and a test pins it.
- **The `2^31` guard stays.** In Python `x * x` cannot overflow, so `MillerRabin` would be correct without the `n < 2^31` restriction; it is kept so both languages accept and reject the same inputs. `pow(base, exp, mod)` is the built-in equivalent of `pow_mod`.

```bash
cd phases/02-algorithms/14-randomized-algorithms/code/python
python3 main.py
python3 -m unittest -v
```

The demo output is identical to the Java run; the suite covers the Java's 39 checks plus 2 for the signed and truncating conversions.

### In Go

```go
func (r *Rng) NextLong() int64 {
    r.state += 0x9E3779B97F4A7C15
    z := r.state
    z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9 // uint64 wraps and >> is logical: Java's >>>
    z = (z ^ (z >> 27)) * 0x94D049BB133111EB
    return int64(z ^ (z >> 31))
}
```

Every type keeps its name and algorithm, and every number in the demo matches the Java run. Four things differ, all in `code/go/`:

- **SplitMix64 is a direct transcription.** The state is a `uint64`, so multiplication wraps and `>>` is the logical shift Java spells `>>>`; the only conversion is the final `int64(...)` so `NextLong` returns the same signed value as Java (the reference output `0xE220A8397B1DCDAF` is negative, and a test checks that). `NextDouble` shifts the unsigned value right by 11 and scales by `2^-53`. `math/rand` must not be substituted: it is a different generator and the reference test would fail.
- **The heap's array order is part of the output.** `WeightedReservoirSampler.Sample()` returns the heap's backing array, as the Java iterates `PriorityQueue`. `container/heap` sifts differently from the JDK (hole to a leaf, then back up), which can arrange keys differently, so `javapq.go` is a short transcription of `PriorityQueue.offer`/`poll` with the JDK's `siftUp`/`siftDown`; the weighted sample prints in the same order as the Java run and a test pins it. `math.Pow` and `Math.pow` agree on every key in the demo.
- **Generics and errors.** `ReservoirSampler[T any]` and `WeightedReservoirSampler[T any]` take the element type as a parameter, `FisherYates[T any]` shuffles any slice, and constructors and `Select`, `Percentile`, `IsProbablePrime`, and `Rng.NextInt` return `(T, error)` with the sentinels `ErrBadK`, `ErrBadWeight`, `ErrEmpty`, `ErrKRange`, `ErrBadQ`, `ErrBadBound`, and `ErrTooLarge`. Inside the algorithms the generator is called through `nextInt`, which panics on a non-positive bound because every caller has already validated it; a bad bound there is a programming error, not an input.
- **Integer semantics match without help.** Go's `/` and `%` truncate toward zero like Java's, so `Money.String` prints `-150` as `-1.50 USD` with no helper (a test pins it), `int64` multiplication wraps like `long`, and the `n < 2^31` guard in Miller-Rabin is doing the same job as in Java: keeping `x * x` inside 63 bits. `math/big`'s `ProbablyPrime` is the production version.

```bash
cd phases/02-algorithms/14-randomized-algorithms/code/go
go run .
go test ./...
```

The demo output is identical to the Java run; the suite covers the Java's 39 checks plus 7 that pin the sign of the reference value, the demo's samples and shuffle counts, the `q = 0` sentinel, and the truncating `String`.

## Use It

**`java.util.Random` and friends.** `Collections.shuffle(list, random)` is Fisher-Yates. `Random.nextInt(bound)` uses rejection sampling for non-power-of-two bounds (read its source; the loop is the one in Step 2 with a different mask). `ThreadLocalRandom` and `SplittableRandom` (which is SplitMix64, from the same paper) are the modern choices for non-cryptographic use; `SecureRandom` is for secrets. `java.util.random.RandomGenerator` (Java 17+) is the common interface. Python's `random.sample(population, k)` requires a sequence, not a stream; `random.shuffle` is Fisher-Yates; `random.choices(weights=...)` is weighted sampling *with* replacement. Go's `math/rand/v2` has `Shuffle` and `IntN` with rejection sampling.

**Streaming systems.** Apache Flink and Spark Structured Streaming expose reservoir sampling for approximate queries; Kafka Streams users write Algorithm R by hand as a stateful processor. PostgreSQL's `TABLESAMPLE SYSTEM` and `BERNOULLI` are per-block and per-row Bernoulli sampling, not fixed-`k`, which is the "365 on a quiet day" problem; `ORDER BY random() LIMIT k` is exact but sorts the table. Datadog, Honeycomb, and OpenTelemetry tail-based sampling keep a fixed number of traces per second with reservoirs, and OpenTelemetry's `ParentBased(TraceIdRatioBased)` is Bernoulli.

**Selection in databases.** `percentile_cont` and `percentile_disc` in PostgreSQL sort; `approx_percentile` in Spark, Presto, and BigQuery use t-digest or KLL sketches, which are the streaming, mergeable cousins of quickselect for when the data does not fit in memory at all. The JDK's `Arrays.sort` uses introsort-style fallbacks; there is no `Arrays.select`, and `Collections.max` is selection for `k = n - 1`.

**Primality.** `BigInteger.isProbablePrime(certainty)` runs Miller-Rabin (plus Lucas-Lehmer for large inputs) and is what every JVM TLS stack uses to generate RSA and DH parameters. `certainty = 100` means error below `2^-100`. OpenSSL's `BN_is_prime_ex` is the same algorithm.

**Audit and compliance.** External auditors sample under standards (ISA 530, PCAOB AS 2315) that require the method to give every item a known, non-zero chance of selection and the selection to be reproducible. Reservoir sampling with a recorded seed satisfies both; "the first 500 of the day" satisfies neither. Monetary-unit sampling, the audit profession's name for probability-proportional-to-size sampling, is the weighted reservoir with amount as weight.

## Ship It

`outputs/runbook-audit-sampling.md` is a runbook for producing a defensible transaction sample: choosing `k`, uniform versus amount-weighted, recording and escrowing the seed, reproducing a sample on request, the statistical checks to run on the sampler itself, and the questions an auditor will ask that the algorithm answers.

## Exercises

1. **Reinforce.** Implement Vitter's Algorithm L (or Z): instead of drawing once per item, draw a random gap and skip that many items before the next possible replacement. Verify it against Algorithm R by running both 30,000 times on `1..10` with `k = 3` and comparing the selection frequencies. Then count the number of random draws each makes on a stream of one million items with `k = 100`.
2. **Apply.** The audit team wants stratified sampling: 200 transactions from each of five amount bands, uniformly within each band, in one pass. Build it as five reservoirs keyed by band. Then show, by construction of a stream, why a single uniform reservoir of 1,000 can return zero transactions from the top band, and compute the probability of that for a day where the top band is 0.1% of volume.
3. **Extend.** Combine with Phase 3, Lesson 01 (Bloom filters): the audit sample must exclude transactions already sampled in the previous 30 days. Feed the previous samples into a Bloom filter sized for 15,000 items at 1% false-positive rate, skip any streaming transaction the filter reports, and analyze what the filter's false positives do to the uniformity invariant. State whether the resulting sample is still defensible under a standard that requires a known non-zero selection probability for every item, and what you would change if not.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Las Vegas algorithm | "randomized but always right" | Randomness affects only the running time; the output is always correct (quickselect, randomized quicksort) |
| Monte Carlo algorithm | "probably right" | Randomness affects the answer, with a bounded and usually tunable error probability (Miller-Rabin, Bloom filters) |
| Reservoir sampling | "sample from a stream" | Algorithm R: keep `k` items; item `n` replaces a uniformly chosen slot with probability `k/n`; every item ends up selected with probability exactly `k/n` |
| Probability proportional to size | "weight by amount" | Each item's selection probability is proportional to its weight; A-Res achieves it with keys `u^(1/w)` and a size-`k` min-heap |
| Fisher-Yates | "a proper shuffle" | Swap position `i` with a uniform position in `[0, i]` for `i` from `n-1` down to 1; exactly `n!` equally likely outcomes |
| Modulo bias | "random % n" | Reducing a uniform `b`-bit value mod `n` over-represents small residues unless `n` divides `2^b`; rejection sampling removes it |
| Expected linear time | "O(n) on average" | The mean over the algorithm's own random choices is `O(n)` for every input; there is no bad input, only unlucky runs |
| Seed | "the starting point" | The initial generator state; fixing it makes every downstream random choice reproducible, which is required for audit evidence and tests and forbidden for secrets |

## Further Reading

- [Vitter, "Random sampling with a reservoir", ACM Transactions on Mathematical Software 11(1), 1985](https://doi.org/10.1145/3147.3165) — Algorithm R with its proof, and Algorithms X, Y, Z that skip items to reduce the number of random draws.
- [Efraimidis and Spirakis, "Weighted random sampling with a reservoir", Information Processing Letters 97(5), 2006](https://doi.org/10.1016/j.ipl.2005.11.003) — the `u^(1/w)` key trick (A-Res) and its proof, two pages long.
- [Knuth, *The Art of Computer Programming*, Volume 2, Section 3.4.2 (Random Sampling and Shuffling)](https://www-cs-faculty.stanford.edu/~knuth/taocp.html) — Algorithm P (Fisher-Yates) and Algorithm S (selection sampling), with the history back to Fisher and Yates' 1938 tables.
- [Steele, Lea, Flood, "Fast splittable pseudorandom number generators", OOPSLA 2014](https://doi.org/10.1145/2660193.2660195) — SplitMix64, the generator in this lesson and in `java.util.SplittableRandom`, with the test-suite results that justify trusting it for non-cryptographic use.
- [Hoare, "Algorithm 65: Find", Communications of the ACM 4(7), 1961](https://doi.org/10.1145/366622.366647) — quickselect in its original form, published alongside quicksort.
- [Rabin, "Probabilistic algorithm for testing primality", Journal of Number Theory 12(1), 1980](https://doi.org/10.1016/0022-314X(80)90084-0) — the randomized version of Miller's test and the `4^-r` error bound.
- [ISA 530, Audit Sampling (IAASB)](https://www.iaasb.org/publications/international-standard-auditing-isa-530-audit-sampling) — what an external auditor requires of a sampling method: every item a known non-zero chance of selection, and a method that can be reproduced.
