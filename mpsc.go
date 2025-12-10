package ringbuffer

import (
	"sync/atomic"
)

type MPSC[T any] struct {
	// Optional padding to avoid false sharing between frequently accessed fields
	_        [64]byte
	mask     uint64
	capacity uint64
	slots    []slot[T]
	_        [64]byte
	enqueue  atomic.Uint64 // logical "tail", updated by multiple producers
	_        [64]byte
	dequeue  uint64 // logical "head", updated by a single consumer
	_        [64]byte
}

// NewMPSC creates a new bounded ring queue.
// Capacity must be a power of two (1<<k).
func NewMPSC[T any](capacity uint64) *MPSC[T] {
	if capacity == 0 || (capacity&(capacity-1)) != 0 {
		panic("capacity must be power of 2 and > 0")
	}

	slots := make([]slot[T], capacity)
	for i := uint64(0); i < capacity; i++ {
		// initial sequence value per slot
		slots[i].seq.Store(i)
	}

	return &MPSC[T]{
		mask:     capacity - 1,
		capacity: capacity,
		slots:    slots,
	}
}

// Enqueue pushes an element into the queue.
// Returns false if the queue is full (overflow).
// May be called concurrently from many goroutines (producers).
func (q *MPSC[T]) Enqueue(v T) bool {
	for {
		pos := q.enqueue.Load()
		s := &q.slots[pos&q.mask]

		seq := s.seq.Load()
		diff := int64(seq) - int64(pos)

		if diff == 0 {
			// slot is free for this position, try to reserve it
			if q.enqueue.CompareAndSwap(pos, pos+1) {
				// we won the slot
				s.val = v
				// publish the value: seq = pos+1
				s.seq.Store(pos + 1)
				return true
			}
			// contention, retry
		} else if diff < 0 {
			// slot has not been freed by the consumer yet
			// => queue is full
			return false
		} else {
			// diff > 0 => this slot still belongs to a previous cycle
			// retry (pos is likely to change)
		}
	}
}

// Dequeue pops an element from the queue.
// Returns (zero, false) if the queue is empty.
// IMPORTANT: must be called from a single consumer goroutine.
func (q *MPSC[T]) Dequeue() (T, bool) {
	pos := q.dequeue
	s := &q.slots[pos&q.mask]

	seq := s.seq.Load()
	diff := int64(seq) - int64(pos+1)

	var zero T

	if diff == 0 {
		// element ready
		q.dequeue = pos + 1

		v := s.val
		// free the slot for the next cycle:
		// next time this physical slot will be used at pos+capacity
		s.seq.Store(pos + q.capacity)

		return v, true
	}

	if diff < 0 {
		// queue is logically empty (consumer is ahead of producers)
		return zero, false
	}

	// diff > 0 => producer is not done yet or in intermediate state
	return zero, false
}

// Capacity returns the fixed queue capacity.
func (q *MPSC[T]) Capacity() uint64 {
	return q.capacity
}
