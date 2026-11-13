package activity

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Writer persists an entry.
type Writer interface {
	Insert(ctx context.Context, e *Entry) error
}

// Recorder writes entries asynchronously on a single goroutine. Record never
// blocks; when the buffer is full the entry is dropped and counted.
type Recorder struct {
	w       Writer
	ch      chan Entry
	done    chan struct{}
	mu      sync.RWMutex // guards closed + sends on ch
	closed  bool
	dropped atomic.Int64
	lastLog atomic.Int64 // unix nanos of last drop warning
}

func NewRecorder(w Writer, buffer int) *Recorder {
	if buffer < 1 {
		buffer = 1
	}
	r := &Recorder{w: w, ch: make(chan Entry, buffer), done: make(chan struct{})}
	go r.loop()
	return r
}

func (r *Recorder) loop() {
	defer close(r.done)
	for e := range r.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := r.w.Insert(ctx, &e); err != nil {
			log.Printf("activity: insert failed: %v", err)
		}
		cancel()
	}
}

// Record queues e without blocking. After Close it is a silent no-op.
func (r *Recorder) Record(e Entry) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- e:
	default:
		n := r.dropped.Add(1)
		now := time.Now().UnixNano()
		last := r.lastLog.Load()
		if now-last >= int64(time.Minute) && r.lastLog.CompareAndSwap(last, now) {
			log.Printf("activity: buffer full, %d entries dropped so far", n)
		}
	}
}

// Close stops intake, drains queued entries and waits for the writer or ctx.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dropped returns how many entries were discarded because the buffer was full.
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }
