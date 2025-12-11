# ringbuffer

High-performance **lock-free ring buffers** for Go, implemented using the sequence-number technique from **Dmitry Vyukov's bounded MPMC queue**.

This repository contains two production-ready queue implementations:

- **MPSC** — multi-producer, single-consumer, bounded, lock-free
- **MPMC** — multi-producer, multi-consumer, bounded, lock-free

Both queues provide:

- zero allocations after initialization
- fixed-size ring buffer
- no mutexes
- atomic correctness with acquire/release memory ordering
- extremely high throughput suitable for real-time pipelines (adtech, RPC batching, event queues)

---

## 🔗 Algorithm Reference

These implementations are based on:

**Dmitry Vyukov — Bounded MPMC Queue**  
https://www.1024cores.net/home/lock-free-algorithms/queues/bounded-mpmc-queue

The core idea is a **sequence-numbered ring buffer**:

- each slot has a `seq` (sequence number) and `val` (value)
- producers and consumers use `seq` to coordinate slot ownership
- atomic operations on indexes and `seq` guarantee correctness and ordering

---

## 🧩 ASCII Diagram – seq-based Ring Buffer

Below is a simplified view of how a single slot cycles through states
as producers and consumers operate on logical positions (`pos`):

```text

Initial state for slot i:
  slot[i].seq = i          // slot is free for pos = i

Enqueue at logical position pos = i:

  1) Reserve position (CAS on tail):
       enqueue: pos -> pos+1

  2) Write value:
       slot[i].val = V

  3) Publish value:
       slot[i].seq = pos + 1   // seq == i+1 means "value ready for pos=i"

Dequeue at logical position pos = i:

  1) Check readiness:
       if slot[i].seq == pos + 1:
           value is ready

  2) Consume value:
       V = slot[i].val

  3) Free slot for next cycle:
       slot[i].seq = pos + capacity

Next time this physical slot is used at logical position:

  pos' = i + capacity

The consumer will again wait until:

  slot[i].seq == pos' + 1  => slot[i].seq == i + capacity + 1
```

This protocol ensures:

- producers never overwrite a slot that is not yet freed
- consumers never read a partially written value
- slots are reused in a bounded ring with predictable memory usage

---

## 📦 Installation

```sh
go get github.com/aradilov/ringbuffer
```

---

## 📚 Usage (MPSC)

```go
import "github.com/aradilov/ringbuffer"

type MyType struct {
    Value int
}

func exampleMPSC() {
    q := mpscring.MPSC[*MyType](1024)

    // Producers
    go func() {
        for i := 0; i < 1000; i++ {
            for !q.Enqueue(&MyType{Value: i}) {
                // Retry on overflow
            }
        }
    }()

    // Single consumer
    go func() {
        for {
            v, ok := q.Dequeue()
            if !ok {
                // Queue is temporarily empty
                continue
            }
            if v != nil {
                println("Got:", v.Value)
            }
        }
    }()
}
```

---

## 📚 Usage (MPMC)

```go
import "github.com/aradilov/ringbuffer"

func exampleMPMC() {
    q := ringbuffer.NewMPMC[int](1024)

    // Many producers
    for p := 0; p < 4; p++ {
        go func(id int) {
            for i := 0; i < 1000; i++ {
                for !q.Enqueue(i) {
                    // Retry on overflow
                }
            }
        }(p)
    }

    // Many consumers
    for c := 0; c < 4; c++ {
        go func(id int) {
            for {
                v, ok := q.Dequeue()
                if !ok {
                    // Queue is temporarily empty
                    continue
                }
                _ = v // process value
            }
        }(c)
    }
}
```

---

## 🧪 Tests

Run tests and the race detector:

```sh
go test ./... -race
```

---

## ⚡ Benchmarks

Benchmarks below were measured on:

```text
goos: darwin
goarch: amd64
cpu: Intel(R) Core(TM) i5-1038NG7 CPU @ 2.00GHz

```

### MPSC Benchmark (multi-producer → single consumer)

```text
BenchmarkMPSC_MP1C-8   	10975267	       130.5 ns/op
BenchmarkMPMC_MPMC-8   	 2215446	       540.4 ns/op


```

This corresponds to approximately **10e6 million enqueue+dequeue operations per second**
on a single consumer goroutine for MP1C and 2e6 for MPMC.


---

## 🏗 Design Notes

### MPSC Queue

- multi-producer CAS on the `enqueue` (tail) index
- single consumer updates `dequeue` (head) without CAS
- perfect fit for pipelines like:
    - RPC/GRPC batching
    - log ingestion
    - telemetry/event fans

### MPMC Queue

- CAS for both producers (`enqueue`) and consumers (`dequeue`)
- fully concurrent on both ends
- deterministic bounded memory

Both queues:

- are fixed-size (bounded) ring buffers
- never allocate after `NewQueue`
- rely on `atomic` operations and sequence numbers for correctness

---

## 📈 When to Use

Use this library when you need:

- better throughput than buffered Go channels in contended scenarios
- deterministic bounded memory (no unexpected allocations)
- predictable performance under high load
- backpressure via bounded capacity

Typical use cases:

- ad-serving and RTB pipelines
- GRPC request multiplexing
- streaming telemetry and metrics ingestion
- job schedulers and worker pools
- logging subsystems

---

## 🔒 Safety

- algorithms are memory-safe under the Go memory model
- value visibility is guaranteed by seq-based publish protocol (Acquire/Release)
- no data races on user values when used as intended
- includes unit tests and concurrency tests

---

## 📜 License

MIT License — free to use in personal and commercial projects.
