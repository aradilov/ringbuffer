package ringbuffer

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Basic sanity: sequential enqueue/dequeue with ints.
func TestTaskQSequential(t *testing.T) {
	const (
		capacity = 1024
		N        = 100_000
	)

	q := NewTaskQ(capacity)

	// Enqueue N items
	for i := 0; i < N; i++ {
		ok := q.DoNoWait([]byte(fmt.Sprintf("item %d", i)))
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
		pos, seq, ok := q.Next()
		if i < capacity {
			if !ok {
				t.Fatalf("dequeue failed at %d (queue unexpectedly empty)", i)
			}
			v := q.Get(pos)
			expected := fmt.Sprintf("item %d", i)
			if string(v.task) != expected {
				t.Fatalf("expected %q, got %q (FIFO violated)", expected, v.task)
			}

		} else if ok {
			t.Fatalf("dequeue failed at %d (queue unexpectedly not empty)", i)
		}
		if ok {
			q.Release(pos, seq)
		}

	}

	// Now queue must be empty
	if pos, _, ok := q.Next(); ok {
		t.Fatalf("expected empty queue at the end, got value=%v", q.Get(pos))
	}
}

func TestTaskQLock(t *testing.T) {
	const (
		capacity = 1024
	)
	q := NewTaskQ(capacity)
	done := make(chan struct{})

	timeout := time.Millisecond * 10
	msg2 := []byte("msg2")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		msg := []byte("msg1")
		err := q.Do(msg, ctx, nil)
		cancel()
		if nil == err {
			t.Errorf("expected timeout, got nil")
		}
	}()

	go func() {
		time.Sleep(time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		err := q.Do(msg2, ctx, func(resp []byte) {
			if string(resp) != string(msg2) {
				t.Errorf("expected %q, got %q", msg2, resp)
			}
		})
		cancel()
		if nil != err {
			t.Errorf("expected nil, got %v", err)
		}

		done <- struct{}{}
	}()

	// consumer
	go func() {
		time.Sleep(time.Millisecond * 2)
		for i := 0; i < 2; i++ {
			pos, seq, ok := q.Next()
			if !ok {
				t.Errorf("expected task, got nothing")
			}

			if i == 0 {
				go func(pos uint64, seq uint64, ok bool) {
					time.Sleep(timeout / 2)
					if ok = q.Lock(pos, seq, func(t *T) bool { return true }); !ok {
						t.Errorf("expected lock to succeed, got failure")
					}
					time.Sleep(timeout)
					if ok = q.Lock(pos, seq, func(t *T) bool { return true }); ok {
						t.Errorf("expected lock to fail, got success")
					}
				}(pos, seq, ok)
			} else {

				if ok = q.Lock(pos, seq, func(task *T) bool {
					if string(task.task) != string(msg2) {
						t.Errorf("expected %q, got %q", msg2, task.task)
					} else {
						task.resp = msg2
						task.ch <- nil
					}

					return true
				}); !ok {
					t.Errorf("expected lock to succeed, got failure")
				}

			}
		}

	}()

	<-done
}

func TestTaskQDeadline(t *testing.T) {
	const (
		capacity  = 1024
		producers = 8
		N         = 100e3
	)

	perProducer := N / producers
	iterations := perProducer * producers

	q := NewTaskQ(capacity)
	wg := sync.WaitGroup{}
	wg.Add(producers + 1)

	var dequeueAttempts, written int64

	go func() {

		defer wg.Done()
		// Dequeue N items
		for i := 0; i < int(iterations); i++ {
			pos, seq, ok := q.Next()
			if !ok {
				atomic.AddInt64(&dequeueAttempts, 1)
				i--
				runtime.Gosched()
				continue
			}

			ok = q.Lock(pos, seq, func(task *T) bool {
				task.resp = task.task
				task.ch <- nil
				return true
			})
			if !ok {
				t.Errorf("expected lock to succeed at %d, got failure", i)
			}
		}
	}()

	for p := 0; p < producers; p++ {
		go func(p int) {
			defer wg.Done()
			start := p * int(perProducer)
			end := start + int(perProducer)
			for i := start; i < end; i++ {
				atomic.AddInt64(&written, 1)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)

				task := []byte(fmt.Sprintf("item %d", i))
				err := q.Do(task, ctx, func(resp []byte) {
					if string(resp) != string(task) {
						t.Fatalf("expected %q at %d, got %q", task, i, resp)
					}
				})
				cancel()
				if nil != err {
					t.Errorf("expected nil at %d, got %v", i, err)
				}
			}
		}(p)
	}

	wg.Wait()

	// Now queue must be empty
	if pos, _, ok := q.Next(); ok {
		t.Fatalf("expected empty queue at the end, got value=%v", q.Get(pos))
	}

	//t.Logf("dequeueAttempts=%d, written = %d", dequeueAttempts, written)

}
