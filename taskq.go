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
					reader(v.resp)
					q.Release(pos, offset)
					errorChPool.Put(chv)
					return err
				case <-ctx.Done():
					q.Release(pos, offset)
					errorChPool.Put(chv)
					return ErrTimeout
				}
			}
			// contention, retry
		} else if diff < 0 {
			// slot has not been freed by the consumer yet
			// => queue is full
			return ErrQueueIsFull
		} else {
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
			// slot has not been freed by the consumer yet
			// => queue is full
			return false
		} else {
			// diff > 0 => this slot still belongs to a previous cycle.
			// Just retry with a new pos.
			runtime.Gosched()
		}
	}
}

// Get returns T by slot position
func (q *TaskQ) Get(slotPosition uint64) *T {
	s := &q.slots[slotPosition&q.mask]
	return &s.val
}

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
	}

	return true

}

// Next pops an element from the queue but doesn't mark slot as free for the producers. Use Release method for it.
// Returns (zero, 0, 0, false) if the queue is empty, otherwise (T, slot position, slot sequence, true).
// IMPORTANT: must be called from a single consumer goroutine.
func (q *TaskQ) Next() (T, uint64, uint64, bool) {
	pos := q.dequeue
	s := &q.slots[pos&q.mask]

	seq := s.seq.Load()
	diff := int64(seq) - int64(pos+1)

	var zero T

	if diff == 0 {
		q.dequeue = pos + 1
		v := s.val
		return v, pos, seq, true
	}

	if diff < 0 {
		// queue is logically empty (consumer is ahead of producers)
		return zero, 0, 0, false
	}

	// diff > 0 => producer is not done yet or in intermediate state
	return zero, 0, 0, false
}

// Capacity returns the fixed queue capacity.
func (q *TaskQ) Capacity() uint64 {
	return q.capacity
}

var errorChPool sync.Pool
