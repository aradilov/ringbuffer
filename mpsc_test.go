package ringbuffer

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Basic sanity: sequential enqueue/dequeue with ints.
func TestMPSCSequential(t *testing.T) {
	const (
		capacity = 1024
		N        = 100_000
	)

	q := NewMPSC[int](capacity)

	// Enqueue N items
	for i := 0; i < N; i++ {
		if !q.Enqueue(i) {
			t.Fatalf("enqueue failed at %d (queue unexpectedly full)", i)
		}
	}

	// Dequeue N items
	for i := 0; i < N; i++ {
		v, ok := q.Dequeue()
		if !ok {
			t.Fatalf("dequeue failed at %d (queue unexpectedly empty)", i)
		}
		if v != i {
			t.Fatalf("expected %d, got %d (FIFO violated)", i, v)
		}
	}

	// Now queue must be empty
	if v, ok := q.Dequeue(); ok {
		t.Fatalf("expected empty queue at the end, got value=%v", v)
	}
}

// Test that capacity is enforced and overflow is reported.
func TestMPSCCapacityOverflow(t *testing.T) {
	const capacity = 8
	q := NewMPSC[int](capacity)

	// Fill exactly capacity elements
	for i := 0; i < capacity; i++ {
		if !q.Enqueue(i) {
			t.Fatalf("enqueue failed at %d (queue unexpectedly full)", i)
		}
	}

	// One more must fail (queue is full)
	if q.Enqueue(999) {
		t.Fatalf("expected overflow (enqueue should return false), but got true")
	}
}

// Concurrent test: many producers, single consumer.
// Checks that all values [0..N) are received exactly once.
func TestMPSCConcurrentProducers(t *testing.T) {
	const (
		capacity    = 1 << 12
		N           = 200_000
		producers   = 8
		perProducer = N / producers
	)

	q := NewMPSC[int](capacity)
	var wg sync.WaitGroup

	// seen[i] == how many times we saw value i
	seen := make([]int32, N)

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()

		received := 0
		for received < N {
			v, ok := q.Dequeue()
			if !ok {
				// queue empty at the moment, give producers a chance
				runtime.Gosched()
				continue
			}
			if v < 0 || v >= N {
				t.Errorf("consumer: out-of-range value %d", v)
				continue
			}
			atomic.AddInt32(&seen[v], 1)
			received++
		}
	}()

	// Producers
	var pg sync.WaitGroup
	pg.Add(producers)
	for p := 0; p < producers; p++ {
		start := p * perProducer
		end := start + perProducer

		go func(from, to int) {
			defer pg.Done()
			for i := from; i < to; i++ {
				// Keep retrying on overflow (bounded queue)
				for !q.Enqueue(i) {
					runtime.Gosched()
				}
			}
		}(start, end)
	}

	pg.Wait()
	wg.Wait()

	// Verify that each value is seen exactly once
	for i := 0; i < N; i++ {
		if seen[i] != 1 {
			t.Fatalf("value %d seen %d times (expected 1)", i, seen[i])
		}
	}
}

// Benchmark: single producer, single consumer.
func BenchmarkMPSC_1P1C(b *testing.B) {
	const capacity = 1 << 16
	q := NewMPSC[int](capacity)

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

// Benchmark: many producers, single consumer.
func BenchmarkMPSC_MP1C(b *testing.B) {
	const (
		capacity  = 1 << 16
		producers = 8
	)

	q := NewMPSC[int](capacity)
	perProducer := b.N / producers

	var wg sync.WaitGroup
	wg.Add(producers + 1) // producers + consumer

	// Consumer
	go func() {
		defer wg.Done()
		total := 0
		for total < b.N {
			v, ok := q.Dequeue()
			if !ok {
				runtime.Gosched()
				continue
			}
			_ = v
			total++
		}
	}()

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
