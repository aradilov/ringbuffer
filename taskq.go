package ringbuffer

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

var (
	ErrQueueIsFull = fmt.Errorf("queue is full")
	ErrTimeout     = fmt.Errorf("timeout")
)

type T struct {
	task []byte
	resp []byte

	ch chan error
}

func (t *T) Reset() {
	t.task = t.task[:0]
	t.resp = t.resp[:0]
	t.ch = nil
}

type TaskQ struct {
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

	dequeueAttempts                uint64
	dequeueFailedQIsEmpty          uint64
	dequeueFailedIntermediateState uint64

	enqueueAttempts                uint64
	enqueueFailedQIsFull           uint64
	enqueueFailedIntermediateState uint64

	timeout uint64
	success uint64
}

type TaskQStats struct {
	DequeueAttempts                uint64
	DequeueFailedQIsEmpty          uint64
	DequeueFailedIntermediateState uint64

	EnqueueAttempts                uint64
	EnqueueFailedQIsFull           uint64
	EnqueueFailedIntermediateState uint64

	Timeout uint64
	Success uint64
}

// NewTaskQ creates a new bounded ring queue.
// Capacity must be a power of two (1<<k).
func NewTaskQ(capacity uint64) *TaskQ {
	if capacity == 0 || (capacity&(capacity-1)) != 0 {
		panic("capacity must be power of 2 and > 0")
	}

	slots := make([]slot[T], capacity)
	for i := uint64(0); i < capacity; i++ {
		// initial sequence value per slot
		slots[i].seq.Store(i)
	}

	return &TaskQ{
		mask:     capacity - 1,
		capacity: capacity,
		slots:    slots,
	}
}

// Stats retrieves the current statistics of the TaskQ
func (q *TaskQ) Stats() TaskQStats {
	return TaskQStats{
		DequeueAttempts:                atomic.LoadUint64(&q.dequeueAttempts),
		DequeueFailedQIsEmpty:          atomic.LoadUint64(&q.dequeueFailedQIsEmpty),
		DequeueFailedIntermediateState: atomic.LoadUint64(&q.dequeueFailedIntermediateState),
		EnqueueAttempts:                atomic.LoadUint64(&q.enqueueAttempts),
		EnqueueFailedIntermediateState: atomic.LoadUint64(&q.enqueueFailedIntermediateState),
		EnqueueFailedQIsFull:           atomic.LoadUint64(&q.enqueueFailedQIsFull),
		Timeout:                        atomic.LoadUint64(&q.timeout),
		Success:                        atomic.LoadUint64(&q.success),
	}
}

// Do pushes an element into the queue with the given content.
// May be called concurrently from many goroutines (producers).
// It is safe to call Release after Do because Do works with T copy.
func (q *TaskQ) Do(task []byte, ctx context.Context, reader func(resp []byte)) error {

	var ch chan error
	chv := errorChPool.Get()
	if chv == nil {
		chv = make(chan error, 1)
	}
	ch = chv.(chan error)

	atomic.AddUint64(&q.enqueueAttempts, 1)
	for {
		pos := q.enqueue.Load()
		s := &q.slots[pos&q.mask]

		seq := s.seq.Load()
		diff := int64(seq) - int64(pos)

		if diff == 0 {
			// slot is free for this position, try to reserve it
			if q.enqueue.CompareAndSwap(pos, pos+1) {
				// we won the slot
				v := &s.val
				v.task = append(v.task[:0], task...)
				v.ch = ch

				// publish the value: seq = pos+1
				offset := pos + 1
				s.lock.Store(offset)
				s.seq.Store(offset)

				select {
				case err := <-ch:
					atomic.AddUint64(&q.success, 1)
					reader(v.resp)
					q.Release(pos, offset)
					errorChPool.Put(chv)
					return err
				case <-ctx.Done():
					atomic.AddUint64(&q.timeout, 1)
					q.Release(pos, offset)
					errorChPool.Put(chv)
					return ErrTimeout
				}
			}
			// contention, retry
		} else if diff < 0 {
			atomic.AddUint64(&q.enqueueFailedQIsFull, 1)
			// slot has not been freed by the consumer yet
			// => queue is full
			return ErrQueueIsFull
		} else {
			atomic.AddUint64(&q.enqueueFailedIntermediateState, 1)
			// diff > 0 => this slot still belongs to a previous cycle.
			// Just retry with a new pos.
			runtime.Gosched()
		}
	}
}

// DoNoWait pushes an element into the queue.
// Returns false if the queue is full (overflow).
// May be called concurrently from many goroutines (producers).
func (q *TaskQ) DoNoWait(task []byte) bool {
	atomic.AddUint64(&q.enqueueAttempts, 1)
	for {
		pos := q.enqueue.Load()
		s := &q.slots[pos&q.mask]

		seq := s.seq.Load()
		diff := int64(seq) - int64(pos)

		if diff == 0 {
			// slot is free for this position, try to reserve it
			if q.enqueue.CompareAndSwap(pos, pos+1) {
				// we won the slot
				v := &s.val
				v.task = append(v.task[:0], task...)

				// publish the value: seq = pos+1
				offset := pos + 1
				s.lock.Store(offset)
				s.seq.Store(offset)

				return true
			}
			// contention, retry
		} else if diff < 0 {
			atomic.AddUint64(&q.enqueueFailedQIsFull, 1)
			// slot has not been freed by the consumer yet
			// => queue is full
			return false
		} else {
			atomic.AddUint64(&q.enqueueFailedIntermediateState, 1)
			// diff > 0 => this slot still belongs to a previous cycle.
			// Just retry with a new pos.
			runtime.Gosched()
		}
	}
}

// Get returns T by slot position (for debug propose only, use Lock instead of)
func (q *TaskQ) Get(slotPosition uint64) *T {
	s := &q.slots[slotPosition&q.mask]
	return &s.val
}

// Lock attempts to acquire a lock on the specified queue slot if the expected sequence matches.
// If the lock is successfully acquired, the callback function `cb` is executed on the slot's value.
// For successful execution and if `cb` returns true, the slot lock is reset to the expected sequence.
// Returns true if the lock was acquired and the callback was executed, otherwise false.
func (q *TaskQ) Lock(slotPosition uint64, expectedSeq uint64, cb func(t *T) bool) bool {
	s := &q.slots[slotPosition&q.mask]
	newSeq := slotPosition + q.capacity

	if s.lock.CompareAndSwap(expectedSeq, newSeq) {
		if cb(&s.val) {
			s.lock.Store(expectedSeq)
		}

		return true
	}

	return false
}

// Release marks slot as free for the producers.
func (q *TaskQ) Release(slotPosition uint64, expectedSeq uint64) bool {
	s := &q.slots[slotPosition&q.mask]
	newSeq := slotPosition + q.capacity

	for {
		ok := q.Lock(slotPosition, expectedSeq, func(t *T) bool {
			t.ch = nil
			s.lock.Store(newSeq)
			s.seq.Store(newSeq)
			return false
		})
		if ok {
			break
		}
		runtime.Gosched()
	}

	return true

}

// Next pops an element from the queue but doesn't mark slot as free for the producers. Use Release method for it.
// Returns (zero, 0, 0, false) if the queue is empty, otherwise (T, slot position, slot sequence, true).
// IMPORTANT: must be called from a single consumer goroutine.
func (q *TaskQ) Next() (uint64, uint64, bool) {
	pos := q.dequeue
	s := &q.slots[pos&q.mask]
	atomic.AddUint64(&q.dequeueAttempts, 1)

	seq := s.seq.Load()
	diff := int64(seq) - int64(pos+1)

	if diff == 0 {
		q.dequeue = pos + 1
		return pos, seq, true
	}

	if diff < 0 {
		atomic.AddUint64(&q.dequeueFailedQIsEmpty, 1)
		// queue is logically empty (consumer is ahead of producers)
		return 0, 0, false
	}

	atomic.AddUint64(&q.dequeueFailedIntermediateState, 1)
	// diff > 0 => producer is not done yet or in intermediate state
	return 0, 0, false
}

// Capacity returns the fixed queue capacity.
func (q *TaskQ) Capacity() uint64 {
	return q.capacity
}

var errorChPool sync.Pool
