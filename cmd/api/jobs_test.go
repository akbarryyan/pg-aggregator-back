package main

import (
	"sync"
	"testing"
	"time"
)

func TestWaitForJobs(t *testing.T) {
	t.Run("returns true once jobs finish", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { time.Sleep(20 * time.Millisecond); wg.Done() }()
		if !waitForJobs(&wg, time.Second) {
			t.Fatal("expected jobs to finish within the timeout")
		}
	})
	t.Run("returns false on timeout", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		defer wg.Done()
		if waitForJobs(&wg, 30*time.Millisecond) {
			t.Fatal("expected a timeout")
		}
	})
}
