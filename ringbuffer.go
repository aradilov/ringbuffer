package ringbuffer

import "sync/atomic"

// Original algorithm by Dmitry Vyukov
// https://www.1024cores.net/home/lock-free-algorithms/queues/bounded-mpmc-queue

// T — specific type to store in the queue .
// MPSC: multi-producer, single-consumer (MPSC), bounded, lock-free.

type slot[T any] struct {
	seq  atomic.Uint64 // sequence number (controls visibility and slot ownership)
	lock atomic.Uint64 // sequence number for release loc (controls visibility and slot ownership)
	val  T             // actual value stored in this slot
}
