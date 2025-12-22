package ringbuffer

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestArrayMPMC_Correctness(t *testing.T) {
	const (
		capacity = 1 << 16
		readers  = 16
		N        = capacity
	)

	q := NewArrayMPMC[int](capacity)
	perReader := N / readers

	positions := make([]int, N)
	seen := make([]int, N)

	// fill
	for i := 0; i < capacity*2; i++ {
		pos, ok := q.Acquire(i)
		if i < capacity {
			if !ok {
				t.Fatalf("enqueue failed at %d (unexpectedly full)", i)
			}
			if seen[pos] != 0 {
				t.Fatalf("pos=%d seen %d times (expected 0)", pos, seen[pos])
			}
			positions[i] = pos
			seen[pos] = 1
		} else {
			if ok {
				t.Fatalf("enqueue succeeded at %d (unexpectedly not full)", i)
			}
		}
	}
	for i := 0; i < N; i++ {
		if seen[i] != 1 {
			t.Fatalf("slot %d seen %d times (expected 1)", i, seen[i])
		}
	}

	// parallel read/release
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func(r int) {
			defer wg.Done()
			start := r * perReader
			end := start + perReader
			for i := start; i < end; i++ {
				pos := positions[i]
				v := q.Get(pos)
				if v != i {
					select {
					case errCh <- fmt.Errorf("expected %d, got %d for i=%d pos=%d", i, v, i, pos):
					default:
					}
					return
				}
				q.Release(pos) // FIX
			}
		}(r)
	}

	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func BenchmarkArrayMPMC_RoundTrip(b *testing.B) {
	const capacity = 1 << 16
	q := NewArrayMPMC[int](capacity)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pos, ok := q.Acquire(i)
		if !ok {
			b.Fatal("unexpected full")
		}
		_ = q.Get(pos)
		q.Release(pos)
	}
}

func TestArrayMPMC_Stress(t *testing.T) {
	const (
		capacity = 1 << 16
		workers  = 64
		iters    = 200_000
	)

	q := NewArrayMPMC[int](capacity)

	var failed atomic.Bool
	var errMsg atomic.Value

	fail := func(msg string) {
		if failed.CompareAndSwap(false, true) {
			errMsg.Store(msg)
		}
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters && !failed.Load(); i++ {
				pos, ok := q.Acquire(i)
				if !ok {
					// queue full => backoff
					runtime.Gosched()
					continue
				}
				if v := q.Get(pos); v != i {
					fail(fmt.Sprintf("mismatch: pos=%d exp=%d got=%d", pos, i, v))
					return
				}
				q.Release(pos)
			}
		}(w)
	}
	wg.Wait()

	if failed.Load() {
		t.Fatal(errMsg.Load().(string))
	}
}

func BenchmarkArrayMPMC_ParallelRoundTrip(b *testing.B) {
	parallelRoundTrip(b, false)
}

func BenchmarkArrayMPMC_ParallelRoundTrip_WorkHolding(b *testing.B) {
	parallelRoundTrip(b, true)
}

func parallelRoundTrip(b *testing.B, workHolding bool) {
	const capacity = 1 << 12

	for _, p := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("p=%d", p), func(b *testing.B) {
			q := NewArrayMPMC[int](capacity)

			var fails atomic.Uint64
			b.ReportAllocs()
			b.SetParallelism(p)
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				i := 0
				localFails := uint64(0)
				for pb.Next() {
					for {
						pos, ok := q.Acquire(i)
						if !ok {
							localFails++
							runtime.Gosched()
							continue
						}

						if workHolding {
							x := 0
							for k := 0; k < 256; k++ {
								x = x*1664525 + 1013904223
							}
							_ = x
						}

						_ = q.Get(pos)
						q.Release(pos)
						i++
						break
					}
				}
				if localFails != 0 {
					fails.Add(localFails)
				}
			})

			b.StopTimer()
			//totalFails := fails.Load()
			//b.Logf("fails=%d", totalFails)
			b.StartTimer()
		})
	}
}
