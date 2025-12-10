package ringbuffer

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Basic sanity: sequential enqueue/dequeue with ints (single P, single C).
func TestMPMCSequential(t *testing.T) {
	const (
		capacity = 1024
		N        = 100_000
	)

	q := NewMPMC[int](capacity)

	// Enqueue N items
	for i := 0; i < N; i++ {
		ok := q.Enqueue(i)
		if i < capacity {
			if !ok {
				t.Fatalf("enqueue failed at %d (queue unexpectedly full)", i)
			}
		} else {
			if ok {
				t.Fatalf("enqueue failed at %d (queue unexpectedly not full)", i)
			}

		}
	}

	// Dequeue N items
	for i := 0; i < N; i++ {
		v, ok := q.Dequeue()
		if i < capacity {
			if !ok {
				t.Fatalf("dequeue failed at %d (queue unexpectedly empty)", i)
			}
			if v != i {
				t.Fatalf("expected %d, got %d (FIFO violated)", i, v)
			}
		} else if ok {
			t.Fatalf("dequeue failed at %d (queue unexpectedly not empty)", i)
		}

	}

	// Now queue must be empty
	if v, ok := q.Dequeue(); ok {
		t.Fatalf("expected empty queue at the end, got value=%v", v)
	}
}

// Capacity/overflow test for MPMC.
func TestMPMCCapacityOverflow(t *testing.T) {
	const capacity = 8
	q := NewMPMC[int](capacity)

	for i := 0; i < capacity; i++ {
		if !q.Enqueue(i) {
			t.Fatalf("enqueue failed at %d (queue unexpectedly full)", i)
		}
	}

	if q.Enqueue(999) {
		t.Fatalf("expected overflow (enqueue should return false), but got true")
	}
}

// Concurrent test: many producers, many consumers.
// Checks that all values [0..N) appear exactly once.
func TestMPMCConcurrent(t *testing.T) {
	const (
		capacity    = 1 << 12
		N           = 200_000
		producers   = 8
		consumers   = 4
		perProducer = N / producers
	)

	q := NewMPMC[int](capacity)
	seen := make([]int32, N)

	var wg sync.WaitGroup

	// Consumers
	wg.Add(consumers)
	for c := 0; c < consumers; c++ {
		go func() {
			defer wg.Done()
			for {
				v, ok := q.Dequeue()
				if !ok {
					// Heuristic stop condition: if producers are done and queue
					// looks empty for some time, we break.
					// To make this deterministic, you can add an explicit "done"
					// flag for producers. For this simple test, we rely on count.
					runtime.Gosched()
					// We will break once the sum of seen[] == N (checked later).
					continue
				}
				if v < 0 || v >= N {
					t.Errorf("consumer: out-of-range value %d", v)
					continue
				}
				atomic.AddInt32(&seen[v], 1)
			}
		}()
	}

	// Producers
	var pg sync.WaitGroup
	pg.Add(producers)
	for p := 0; p < producers; p++ {
		start := p * perProducer
		end := start + perProducer

		go func(from, to int) {
			defer pg.Done()
			for i := from; i < to; i++ {
				for !q.Enqueue(i) {
					runtime.Gosched()
				}
			}
		}(start, end)
	}

	pg.Wait()

	// Now wait until all values are consumed.
	// Simple polling: when sum(seen) == N, we know all are received.
	for {
		sum := 0
		for i := 0; i < N; i++ {
			sum += int(atomic.LoadInt32(&seen[i]))
		}
		if sum == N {
			break
		}
		runtime.Gosched()
	}

	// At this point all values should be seen; we stop consumers
	// by not providing more data. They may spin, but test ends.
	// In a real implementation you'd have a shutdown signal.

	// Verify that each value is seen exactly once.
	for i := 0; i < N; i++ {
		if seen[i] != 1 {
			t.Fatalf("value %d seen %d times (expected 1)", i, seen[i])
		}
	}
}

// Benchmark: single producer, single consumer.
func BenchmarkMPMC_1P1C(b *testing.B) {
	const capacity = 1 << 16
	q := NewMPMC[int](capacity)

	done := make(chan struct{})

	// Consumer
	go func() {
		for i := 0; i < b.N; i++ {
			for {
				if _, ok := q.Dequeue(); ok {
					break
				}
				runtime.Gosched()
			}
		}
		close(done)
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for !q.Enqueue(i) {
			runtime.Gosched()
		}
	}
	<-done
	b.StopTimer()
}

// Benchmark: many producers, many consumers.
func BenchmarkMPMC_MPMC(b *testing.B) {
	const (
		capacity  = 1 << 16
		producers = 1e3
		consumers = 8
	)

	q := NewMPMC[int](capacity)
	perProducer := b.N / producers

	var wg sync.WaitGroup
	wg.Add(producers + consumers)

	// Consumers
	for c := 0; c < consumers; c++ {
		go func() {
			defer wg.Done()
			for i := 0; i < b.N/consumers; i++ {
				for {
					if v, ok := q.Dequeue(); ok {
						_ = v
						break
					}
					runtime.Gosched()
				}
			}
		}()
	}

	// Producers
	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				for !q.Enqueue(i) {
					runtime.Gosched()
				}
			}
		}()
	}

	b.ResetTimer()
	wg.Wait()
	b.StopTimer()
}
