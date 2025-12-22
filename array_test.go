package ringbuffer

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func BenchmarkArrayMPMC_RoundTrip(b *testing.B) {
	const capacity = 1 << 16
	b.Run("ArrayMPMC", func(b *testing.B) {
		q := NewArrayMPMC[int](capacity)
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			pos, ok := q.Acquire(i)
			if !ok {
				b.Fatal("unexpected: no free slots")
			}
			_ = q.Get(pos)
			q.Release(pos)
		}
	})

	b.Run("MutexPool", func(b *testing.B) {
		p := NewMutexPool[int](capacity)
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			pos, ok := p.TryAcquire(i)
			if !ok {
				b.Fatal("unexpected: no free slots")
			}
			_ = p.Get(pos)
			p.Release(pos)
		}
	})
}

func BenchmarkPool_ParallelRoundTrip(b *testing.B) {
	benchPoolParallel(b, 0)
}

func BenchmarkPool_ParallelRoundTrip_WorkHolding(b *testing.B) {
	benchPoolParallel(b, 1)
}

func BenchmarkPool_ParallelRoundTrip_WorkIOHolding(b *testing.B) {
	benchPoolParallel(b, 2)
}

//go:noinline
func cpuWork(seed int) int {
	x := seed
	for k := 0; k < 256; k++ {
		x = x*1664525 + 1013904223
	}
	return x
}

var benchSink64 int64

func benchPoolParallel(b *testing.B, workHolding int) {
	const capacity = 1 << 16

	for _, p := range []int{1, 2, 4, 8, 32, 128} {
		b.Run(fmt.Sprintf("p=%d", p), func(b *testing.B) {
			b.Run("ArrayMPMC", func(b *testing.B) {
				q := NewArrayMPMC[int](capacity)

				var fails atomic.Uint64
				b.ReportAllocs()
				b.SetParallelism(p)
				b.ResetTimer()

				b.RunParallel(func(pb *testing.PB) {
					var localSink int
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

							switch workHolding {
							case 0:
								_ = q.Get(pos)
								q.Release(pos)
							case 1:
								v := q.Get(pos)
								localSink = cpuWork(v)
								q.Release(pos)
							case 2:
								go func(pos int) {
									time.Sleep(time.Millisecond)
									_ = q.Get(pos)
									q.Release(pos)
								}(pos)
							}

							i++
							break
						}
					}
					if localFails != 0 {
						fails.Add(localFails)
					}
					atomic.AddInt64(&benchSink64, int64(localSink))
				})

				b.StopTimer()
				//b.Logf("benchSink64=%d", atomic.LoadInt64(&benchSink64))
				//b.Logf("fails=%d", fails.Load())
				b.StartTimer()
			})

			b.Run("MutexPool", func(b *testing.B) {
				pool := NewMutexPool[int](capacity)

				var fails atomic.Uint64
				b.ReportAllocs()
				b.SetParallelism(p)
				b.ResetTimer()

				b.RunParallel(func(pb *testing.PB) {
					var localSink int
					i := 0
					localFails := uint64(0)
					for pb.Next() {
						for {
							pos, ok := pool.TryAcquire(i)
							if !ok {
								localFails++
								runtime.Gosched()
								continue
							}

							switch workHolding {
							case 0:
								_ = pool.Get(pos)
								pool.Release(pos)

							case 1:
								v := pool.Get(pos)
								localSink = cpuWork(v)
								pool.Release(pos)
							case 2:
								go func(pos int) {
									time.Sleep(time.Millisecond)
									_ = pool.Get(pos)
									pool.Release(pos)
								}(pos)
							}

							i++
							break
						}
					}
					if localFails != 0 {
						fails.Add(localFails)
					}
					atomic.AddInt64(&benchSink64, int64(localSink))
				})

				b.StopTimer()
				//b.Logf("benchSink64=%d", atomic.LoadInt64(&benchSink64))
				//b.Logf("fails=%d", fails.Load())
				b.StartTimer()
			})
		})
	}
}

// for comparing only

type MutexPool[T any] struct {
	mu   sync.Mutex
	free []int
	data []T
}

func NewMutexPool[T any](capacity int) *MutexPool[T] {
	p := &MutexPool[T]{
		free: make([]int, capacity),
		data: make([]T, capacity),
	}
	for i := 0; i < capacity; i++ {
		p.free[i] = i
	}
	return p
}

// TryAcquire returns (pos, true) if a slot is available, otherwise (0, false).
func (p *MutexPool[T]) TryAcquire(v T) (int, bool) {
	p.mu.Lock()
	n := len(p.free)
	if n == 0 {
		p.mu.Unlock()
		return 0, false
	}
	pos := p.free[n-1]
	p.free = p.free[:n-1]
	p.mu.Unlock()

	p.data[pos] = v
	return pos, true
}

func (p *MutexPool[T]) Get(pos int) T { return p.data[pos] }

func (p *MutexPool[T]) Release(pos int) {
	var zero T
	p.data[pos] = zero

	p.mu.Lock()
	p.free = append(p.free, pos)
	p.mu.Unlock()
}
