package ringbuffer

import (
	"fmt"
	"sync"
	"testing"
)

// Benchmark: many producers, many consumers.
func TestArrayMPMCSequential(t *testing.T) {
	const (
		capacity = 1 << 16
		readers  = 16
		bn       = capacity
	)

	q := NewArrayMPMC[string](capacity)
	perReader := int(bn / readers)

	for i := 0; i < capacity*2; i++ {
		pos, ok := q.Enqueue(fmt.Sprintf("item %d", i))
		if i < capacity {
			if !ok {
				t.Fatalf("enqueue failed at %d (queue unexpectedly full)", i)
			}
			if pos != i {
				t.Fatalf("expected pos=%d, got %d (FIFO violated)", i, pos)
			}
		} else if ok {
			t.Fatalf("enqueue failed at %d (queue unexpectedly not full)", i)
		}
	}

	var wgReaders sync.WaitGroup
	wgReaders.Add(readers)
	for c := 0; c < readers; c++ {
		go func(r int) {
			defer wgReaders.Done()
			start := r * perReader
			end := start + perReader
			for i := start; i < end; i++ {
				v := q.Get(i)
				if v != fmt.Sprintf("item %d", i) {
					t.Fatalf("expected %s, got %s", fmt.Sprintf("item %d", i), v)
				}
				q.Release(i)
			}
		}(c)
	}
	wgReaders.Wait()

}

// Benchmark: many producers, many consumers.
func BenchmarkArrayMPMC(b *testing.B) {
	const (
		capacity = 1 << 16
		readers  = 16
		N        = capacity
	)

	q := NewArrayMPMC[int](capacity)
	perReader := int(N / readers)

	b.Logf("b.N = %d, capacity = %d, readers = %d, perReader = %d", b.N, capacity, readers, perReader)
	for j := 0; j < b.N; j++ {
		positions := make([]int, N)
		seen := make([]int, N)
		for i := 0; i < capacity*2; i++ {
			pos, ok := q.Enqueue(i)

			if i < capacity {
				if !ok {
					b.Fatalf("[%d] enqueue failed at %d (queue unexpectedly full)", j, i)
				}
				v := seen[pos]
				if v != 0 {
					b.Fatalf("[%d] pos=%d seen %d times (expected 0)", j, pos, v)
				}
				positions[i] = pos
				seen[pos] = v + 1

			} else if ok {
				b.Fatalf("[%d] enqueue failed at %d (queue unexpectedly not full)", j, i)
			}
		}

		for i := 0; i < N; i++ {
			if seen[i] != 1 {
				b.Fatalf("[%d] value %d seen %d times (expected 1)", j, i, seen[i])
			}
		}

		var wgReaders sync.WaitGroup
		wgReaders.Add(readers)
		for c := 0; c < readers; c++ {
			go func(r int) {
				defer wgReaders.Done()
				start := r * perReader
				end := start + perReader
				for i := start; i < end; i++ {
					pos := positions[i]
					v := q.Get(pos)
					if v != i {
						b.Fatalf("[%d] expected %d, got %d for i %d", j, pos, v, i)
					}
					q.Release(i)
				}
			}(c)
		}
		wgReaders.Wait()
	}
}
