package main

// javaPriorityQueue is a transcription of java.util.PriorityQueue's siftUp/siftDown. container/heap sifts differently
// (hole to a leaf, then back up), which can arrange equal or near-equal keys differently in the backing array, and the
// weighted sampler's output order is the array order, so the demo would not match the Java run.
type javaPriorityQueue[T any] struct {
	queue []T
	less  func(a, b T) int
}

func newJavaPriorityQueue[T any](cmp func(a, b T) int) *javaPriorityQueue[T] {
	return &javaPriorityQueue[T]{less: cmp}
}

func (q *javaPriorityQueue[T]) Len() int { return len(q.queue) }

// Peek is the least element; the queue must be non-empty.
func (q *javaPriorityQueue[T]) Peek() T { return q.queue[0] }

// Add is PriorityQueue.offer: append, then sift up.
func (q *javaPriorityQueue[T]) Add(x T) {
	q.queue = append(q.queue, x)
	k := len(q.queue) - 1
	for k > 0 {
		parent := (k - 1) >> 1
		e := q.queue[parent]
		if q.less(x, e) >= 0 {
			break
		}
		q.queue[k] = e
		k = parent
	}
	q.queue[k] = x
}

// Poll is PriorityQueue.poll: take the root, move the last element to the root, sift down.
func (q *javaPriorityQueue[T]) Poll() T {
	result := q.queue[0]
	n := len(q.queue) - 1
	x := q.queue[n]
	q.queue = q.queue[:n]
	if n > 0 {
		k := 0
		half := n >> 1
		for k < half {
			child := 2*k + 1
			c := q.queue[child]
			if right := child + 1; right < n && q.less(c, q.queue[right]) > 0 {
				child = right
				c = q.queue[child]
			}
			if q.less(x, c) <= 0 {
				break
			}
			q.queue[k] = c
			k = child
		}
		q.queue[k] = x
	}
	return result
}

// Items is the backing array in heap order, which is what iterating a PriorityQueue yields.
func (q *javaPriorityQueue[T]) Items() []T { return append([]T(nil), q.queue...) }
