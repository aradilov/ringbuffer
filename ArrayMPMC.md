# ArrayMPMC

`ArrayMPMC` is a **bounded, allocation-free, contention-resistant pool** designed for very high-throughput Go workloads.
It provides `Acquire()/Release()` semantics (like a pool), but with **deterministic memory usage** and **stable latency** under parallel access.

It is especially useful when:

- you want to reuse objects/buffers with **zero allocations** and **no GC churn**;
- you need a **hard capacity limit** (unlike `sync.Pool`, which is opportunistic and may drop items);
- you expect **many goroutines** to acquire/release concurrently and want to avoid a single global lock becoming a bottleneck.

---

## What it is (in one sentence)

**A fixed array of `T` values + a lock-free bounded MPMC ring that stores indexes of free slots.**

- The **array** holds the actual objects (`[]T`), so their memory is stable.
- The **MPMC ring** holds **free indexes** (`uint32/uint64`), so `Acquire` is a dequeue and `Release` is an enqueue.

---

## How it works

### Data layout

- `data []T` — the backing storage for pooled objects.
- `free MPMC[uint32]` (or similar) — a bounded, lock-free queue of free indexes into `data`.

### Initialization

On creation, the pool:

1. allocates `data` with `capacity` elements;
2. pushes all indexes `0..capacity-1` into the `free` queue.

### Acquire (fast path)

1. dequeue an index from `free`;
2. return a pointer/reference to `data[idx]`.

If the pool is empty, `Acquire` returns `(nil/zero, false)` (or blocks, depending on your wrapper).

### Release (fast path)

1. optionally reset `data[idx]` (if your `T` needs it);
2. enqueue `idx` back into `free`.

---

## The underlying algorithm

The internal MPMC queue is based on the well-known **Dmitry Vyukov bounded MPMC ring queue** design:

- fixed-size ring buffer with **power-of-two capacity**;
- per-slot **sequence number** to avoid ABA and to distinguish empty/full states;
- atomic `head` (consumers) and `tail` (producers) indexes;
- producers/consumers use CAS loops; when contention is high, they yield (`runtime.Gosched`) / backoff.

Key properties:

- **Lock-free progress**: system makes progress even if some goroutines are stalled.
- **No allocations per op**: enqueue/dequeue are purely atomic + a few loads/stores.
- **False-sharing mitigation**: hot fields are padded to avoid cache-line ping-pong.

---

## Why not `sync.Pool`?

`sync.Pool` is great for reducing allocations, but it is intentionally *best-effort*:

- it can drop cached items at GC;
- it is not bounded (you can’t enforce “exactly N items exist”);
- it is optimized for typical workloads, not for deterministic “pool capacity” semantics.

`ArrayMPMC` is for cases where you want **bounded reuse with predictable behavior**.

---

## Benchmark summary (your results)

Environment:

- `goos: darwin`, `goarch: amd64`
- Intel(R) Core(TM) i5-1038NG7 CPU @ 2.00GHz
- `go test -run ^$ -bench BenchmarkPool_ParallelRoundTrip -benchmem -count=1`

We benchmarked two implementations:

- **ArrayMPMC** — the bounded array + lock-free MPMC free-index queue.
- **MutexPool** — a pool protected by a mutex (and whatever waiting policy it uses internally).

```go
aradilov@Andreys-MacBook-Pro ringbuffer % go test -run ^$ -bench BenchmarkPool_ParallelRoundTrip -benchmem -count=1 
goos: darwin
goarch: amd64
pkg: github.com/aradilov/ringbuffer
cpu: Intel(R) Core(TM) i5-1038NG7 CPU @ 2.00GHz
BenchmarkPool_ParallelRoundTrip/p=1/ArrayMPMC-8          7966358               145.6 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=1/MutexPool-8          9749154               130.0 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=2/ArrayMPMC-8          8015918               142.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=2/MutexPool-8          8039854               149.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=4/ArrayMPMC-8          8301133               144.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=4/MutexPool-8          7563541               161.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=8/ArrayMPMC-8          8710812               161.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=8/MutexPool-8          6033219               184.2 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=32/ArrayMPMC-8         8638357               154.1 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=32/MutexPool-8         5321392               192.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=128/ArrayMPMC-8        8520272               140.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=128/MutexPool-8        5637085               196.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=256/ArrayMPMC-8        8321271               142.1 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip/p=256/MutexPool-8        5973636               204.6 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=1/ArrayMPMC-8              9153340               131.2 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=1/MutexPool-8              3712148               325.4 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=2/ArrayMPMC-8              9052929               131.8 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=2/MutexPool-8              3392329               348.1 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=4/ArrayMPMC-8              9255921               130.7 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=4/MutexPool-8              3337450               361.6 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=8/ArrayMPMC-8              9095881               132.1 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=8/MutexPool-8              3323484               364.5 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=32/ArrayMPMC-8             8631716               131.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=32/MutexPool-8             3301174               364.0 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=128/ArrayMPMC-8            9440941               128.8 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=128/MutexPool-8            3128964               381.2 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=256/ArrayMPMC-8            9644053               140.0 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkHolding/p=256/MutexPool-8            3320242               381.4 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=1/ArrayMPMC-8            2842449               428.4 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=1/MutexPool-8            2306240               505.6 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=2/ArrayMPMC-8            2875647               442.2 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=2/MutexPool-8            1934212               565.4 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=4/ArrayMPMC-8            2579649               544.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=4/MutexPool-8            2092165               618.3 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=8/ArrayMPMC-8            2822034               519.0 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=8/MutexPool-8            2107314               575.8 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=32/ArrayMPMC-8           2572995               486.0 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=32/MutexPool-8           1861958               691.1 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=128/ArrayMPMC-8          2587197               489.2 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=128/MutexPool-8          1568818               790.4 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=256/ArrayMPMC-8          2668406               464.9 ns/op             0 B/op          0 allocs/op
BenchmarkPool_ParallelRoundTrip_WorkIOHolding/p=256/MutexPool-8          1240141              1062 ns/op               0 B/op          0 allocs/op
PASS
ok      github.com/aradilov/ringbuffer  67.108s

```

### 1) ParallelRoundTrip (no work holding)

Selected points:

| Parallelism (p) | ArrayMPMC (ns/op) | MutexPool (ns/op) | Winner |
|---:|---:|---:|:--|
| 1   | 145.6 | 130.0 | MutexPool |
| 8   | 161.3 | 184.2 | ArrayMPMC |
| 128 | 140.9 | 196.9 | ArrayMPMC |
| 256 | 142.1 | 204.6 | ArrayMPMC |

**Interpretation**

- With `p=1` the mutex version can be slightly faster: one goroutine, almost no contention, very short critical section.
- As `p` grows, **MutexPool degrades** because every Acquire/Release must pass through a **single shared lock**:
    - cache-line bouncing on the mutex state;
    - lock convoying under heavy contention;
    - more scheduler interaction if it ever blocks/parks.
- **ArrayMPMC stays flat** because contention is spread across the ring slots and progress is lock-free.

### 2) ParallelRoundTrip_WorkHolding (work holds the resource)

Selected points:

| Parallelism (p) | ArrayMPMC (ns/op) | MutexPool (ns/op) | Winner |
|---:|---:|---:|:--|
| 1   | 131.2 | 325.4 | ArrayMPMC |
| 2   | 131.8 | 348.1 | ArrayMPMC |
| 128 | 128.8 | 381.2 | ArrayMPMC |
| 256 | 140.0 | 381.4 | ArrayMPMC |

**Interpretation**

This pattern is the most important:

- When the resource is held longer, the pool spends more time in the **“scarcity”** regime (more contenders than free items).
- A mutex-based design tends to serialize the “wait for a free item” path:
    - more time inside the shared lock;
    - worse fairness and more contention overhead;
    - higher chance of goroutine parking/unparking churn.
- ArrayMPMC’s free list remains **cheap**: a failed dequeue is just a few atomics + yield/backoff.

### 3) ParallelRoundTrip_WorkIOHolding (I/O-like holding)

Selected points:

| Parallelism (p) | ArrayMPMC (ns/op) | MutexPool (ns/op) | Winner |
|---:|---:|---:|:--|
| 1   | 428.4 | 505.6 | ArrayMPMC |
| 4   | 544.3 | 618.3 | ArrayMPMC |
| 128 | 489.2 | 790.4 | ArrayMPMC |
| 256 | 464.9 | 1062  | ArrayMPMC |

**Interpretation**

- Here the “work” dominates the timeline, so the pool overhead is a smaller fraction of total time.
- Even so, MutexPool still gets worse as `p` increases because the lock becomes a coordination hotspot.
- ArrayMPMC remains significantly more stable.

---

## When MutexPool can still be a good choice

A mutex-based pool is not “bad” by default. It can be fine when:

- concurrency is low (`p` ~ 1..2);
- you prefer a simpler implementation and can tolerate contention;
- you need blocking semantics, strict fairness, or complex policies (timeouts, priorities, etc.).

But for *high-parallel acquire/release* patterns, your results show why a lock-free, bounded structure tends to win.

---

## Usage sketch

```go
// Create a pool of fixed capacity.
q := NewArrayMPMC[MyType](1 << 12) // capacity must be power of two (typical for Vyukov ring)

// Acquire an item.
pos, ok := q.Acquire(MyType{})
if !ok {
    // pool is empty: either wait, retry with backoff, or drop work
    return
}

// Get retrieves the element at the specified position in the dataset.
// Can be called simultaneously from many goroutines (read-only) for pos, which is not released
// NOTE:
// - Get does NOT synchronize with Acquire's write by itself.
// - Do not call Get(pos) after Release(pos).
_ = q.Get(pos)

// Release releases a slot back into the free-list.
// May be called concurrently from many goroutines
// Note: Release for one position should be called once
q.Release(pos)

```