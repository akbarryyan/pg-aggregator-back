package activity

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeWriter struct {
	mu      sync.Mutex
	entries []Entry
	block   chan struct{}
}

func (f *fakeWriter) Insert(ctx context.Context, e *Entry) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.entries = append(f.entries, *e)
	f.mu.Unlock()
	return nil
}

func (f *fakeWriter) all() []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Entry(nil), f.entries...)
}

func TestRecorderDropsWhenFullWithoutBlocking(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	rec := NewRecorder(w, 2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			rec.Record(Entry{Action: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked")
	}
	if rec.Dropped() < 10 {
		t.Fatalf("dropped = %d", rec.Dropped())
	}
	close(w.block)
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderCloseDrains(t *testing.T) {
	w := &fakeWriter{}
	rec := NewRecorder(w, 100)
	for i := 0; i < 50; i++ {
		rec.Record(Entry{Action: "x"})
	}
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(w.all()); n != 50 {
		t.Fatalf("written = %d", n)
	}
}

func TestRecordAfterCloseDoesNotPanic(t *testing.T) {
	rec := NewRecorder(&fakeWriter{}, 4)
	_ = rec.Close(context.Background())
	rec.Record(Entry{})
	_ = rec.Close(context.Background())
}

func TestCloseHonoursDeadline(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	defer close(w.block)
	rec := NewRecorder(w, 4)
	rec.Record(Entry{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := rec.Close(ctx); err == nil {
		t.Fatal("expected deadline error")
	}
}
