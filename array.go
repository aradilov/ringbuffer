package ringbuffer

type ArrayMPMC[T any] struct {
	slots *MPMC[int]
	data  []T
}

func NewArrayMPMC[T any](capacity uint64) *ArrayMPMC[T] {
	if capacity == 0 || (capacity&(capacity-1)) != 0 {
		panic("capacity must be power of 2 and > 0")
	}

	nonBlockingArray := &ArrayMPMC[T]{
		slots: NewMPMC[int](capacity),
		data:  make([]T, capacity),
	}

	for i := 0; i < int(capacity); i++ {
		if !nonBlockingArray.slots.Enqueue(i) {
			panic("unreached")
		}
	}

	return nonBlockingArray
}

// Enqueue pushes an element into the queue.
// May be called concurrently from many goroutines
func (q *ArrayMPMC[T]) Enqueue(v T) (int, bool) {
	pos, ok := q.slots.Dequeue()
	if !ok {
		return 0, false
	}
	q.data[pos] = v
	return pos, true
}

// Get retrieves the element at the specified position in the dataset.
// Can be called simultaneously from many goroutines (read-only) for pos, which is not released
func (q *ArrayMPMC[T]) Get(pos int) T {
	return q.data[pos]
}

// Release releases an element into the queue.
// May be called concurrently from many goroutines
// Note: Release for one position should be called once
func (q *ArrayMPMC[T]) Release(pos int) {
	if !q.slots.Enqueue(pos) {
		panic("unreached")
	}
}
