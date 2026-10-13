# Placement Quiz Answer Key

Read only the round the learner has just completed. Keep later rounds out of
context until their answers arrive. Reveal explanations only after all five
rounds are complete.

## Round 1: Data structures and algorithms

- Q1: C. With load factor 2 and a good hash, a successful chained lookup
  examines about `1 + α/2 = 2` entries on average, independent of n.
- Q2: B. A binary heap gives O(log n) insert and remove-min; a sorted array has
  O(n) insert, a hash map has no ordering, a sorted linked list has O(n) insert.

## Round 2: Systems data structures and concurrency

- Q3: C. Bloom filters have false positives but never false negatives:
  "definitely absent" is exact, "possibly present" is probabilistic.
- Q4: B. The read-modify-write is not atomic. Both threads can read 0 and both
  write 100 (lost update), or they can serialize and reach 200. 0 is impossible
  because at least one write of 100 lands.

## Round 3: Distributed systems

- Q5: C. Wall clocks on different machines are not comparable without knowing
  their skew; a 10 ms difference is well inside typical NTP error. Logical
  clocks (Lamport, vector) or a message between the events are needed to order
  them.
- Q6: A. The outcome is unknown, so the client must retry, and it must reuse
  the same idempotency key so the server can return the original result
  instead of charging again.

## Round 4: Money and ledgers

- Q7: B. Most decimal fractions (0.10, 19.99) have no finite binary
  representation, so each stored value carries a small error and sums drift;
  display rounding does not repair the stored values and the errors are biased,
  not cancelling.
- Q8: C. Every party whose balance changes gets a posting, and the debits
  (customer −100) must equal the credits (merchant +97, platform +3). Two
  postings would hide the fee; four would invent a party that did not move money.

## Round 5: Payment systems

- Q9: B. A partial capture settles $50; the uncaptured $30 remains an
  authorization hold on the cardholder's available balance until the merchant
  releases it or it expires under the network's rules. Nothing was charged for
  it, so there is nothing to refund.
- Q10: C. Webhooks are at-least-once; the consumer deduplicates by event id and
  acknowledges duplicates (2xx) so the sender stops retrying, without
  re-applying the effect. Returning an error would cause more retries.
