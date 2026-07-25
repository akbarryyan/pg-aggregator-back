package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPeriodic_CallsImmediatelyThenOnInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()

	var calls int32
	RunPeriodic(ctx, 10*time.Millisecond, "test-job", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	got := atomic.LoadInt32(&calls)
	if got < 3 {
		t.Fatalf("expected at least 3 calls (1 immediate + ticks) within the window, got %d", got)
	}
}

func TestRunPeriodic_StopsPromptlyOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		RunPeriodic(ctx, 5*time.Millisecond, "cancel-job", func(ctx context.Context) error {
			return nil
		})
		close(done)
	}()

	// Let it run the immediate call, then cancel.
	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// returned promptly, as expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("RunPeriodic did not return within 200ms of context cancellation")
	}
}

func TestRunPeriodic_SurvivesPanicAndKeepsTicking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	var calls int32
	RunPeriodic(ctx, 10*time.Millisecond, "panicking-job", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		panic("boom")
	})

	got := atomic.LoadInt32(&calls)
	if got < 2 {
		t.Fatalf("expected the loop to keep calling the task despite panics, got %d calls", got)
	}
}

func TestRunPeriodic_SurvivesTaskErrorAndKeepsTicking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	var calls int32
	RunPeriodic(ctx, 10*time.Millisecond, "failing-job", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("boom")
	})

	got := atomic.LoadInt32(&calls)
	if got < 2 {
		t.Fatalf("expected the loop to keep calling the task despite errors, got %d calls", got)
	}
}
