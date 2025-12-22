package ringbuffer

import (
	"runtime"
	"sync/atomic"
)

type MPMC[T any] struct {
	// Optional padding to avoid false sharing between hot fields.
	_        [64]byte
	mask     uint64
	capacity uint64
	slots    []slot[T]
	_        [64]byte
	enqueue  atomic.Uint64 // logical tail index (producers)
	_        [64]byte
	dequeue  atomic.Uint64 // logical head index (consumers)
	_        [64]byte
}

const goschedEvery = 64 // reduce runtime.Gosched() frequency in hot loops

// NewMPMC creates a bounded MPMC ring queue.
// 'capacity' must be a power of two (1<<k).
func NewMPMC[T any](capacity uint64) *MPMC[T] {
	if capacity == 0 || (capacity&(capacity-1)) != 0 {
		panic("capacity must be power of 2 and > 0")
	}

	slots := make([]slot[T], capacity)
	for i := uint64(0); i < capacity; i++ {
		// initial sequence for each slot matches its index
		slots[i].seq.Store(i)
	}

	return &MPMC[T]{
		mask:     capacity - 1,
		capacity: capacity,
		slots:    slots,
	}
}

// Enqueue pushes an element into the queue.
// Returns false if the queue is full (overflow).
// Safe to call concurrently from many producer goroutines.
func (q *MPMC[T]) Enqueue(v T) bool {
	var spins uint32
	for {
		pos := q.enqueue.Load()
		s := &q.slots[pos&q.mask]

		seq := s.seq.Load()
		diff := int64(seq) - int64(pos)

		if diff == 0 {
			// Slot is free for this position, try to reserve it.
			if q.enqueue.CompareAndSwap(pos, pos+1) {
				// We won this slot.
				s.val = v
				// Publish the value: seq = pos+1
				s.seq.Store(pos + 1)
				return true
			}
			spins++
			if spins%goschedEvery == 0 {
				runtime.Gosched()
			}
		} else if diff < 0 {
			// diff < 0 => consumer has not yet freed this slot.
			// MPMC is full for this producer.
			return false
		} else {
			// diff > 0 => this slot still belongs to a previous cycle.
			// Just retry with a new pos.
			spins++
			if spins%goschedEvery == 0 {
				runtime.Gosched()
			}
		}
	}
}

// Dequeue pops an element from the queue.
// Returns (zero, false) if the queue is empty.
// Safe to call concurrently from many consumer goroutines.
func (q *MPMC[T]) Dequeue() (T, bool) {
	var zero T
	var spins uint32
	for {
		pos := q.dequeue.Load()
		s := &q.slots[pos&q.mask]

		seq := s.seq.Load()
		diff := int64(seq) - int64(pos+1)

		if diff == 0 {
			// Element is ready for this position, try to claim it.
			if !q.dequeue.CompareAndSwap(pos, pos+1) {
				// Another consumer won this slot, retry.
				spins++
				if spins%goschedEvery == 0 {
					runtime.Gosched()
				}
				continue
			}

			// We successfully claimed this slot.
			v := s.val
			// Free the slot for the next cycle:
			// next time this physical slot will be used at pos+capacity.
			s.seq.Store(pos + q.capacity)

			return v, true
		}

		if diff < 0 {
			// MPMC is logically empty (head is ahead of producers).
			return zero, false
		}

		// diff > 0 => producer is not done yet or intermediate state.
		// Yield to let producers/other consumers make progress.
		spins++
		if spins%goschedEvery == 0 {
			runtime.Gosched()
		}
	}
}

// Capacity returns the fixed queue capacity.
func (q *MPMC[T]) Capacity() uint64 {
	return q.capacity
}
