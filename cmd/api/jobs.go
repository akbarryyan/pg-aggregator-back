package main

import (
	"sync"
	"time"
)

// waitForJobs waits for the background job goroutines to return, at most
// timeout, and reports whether they all did. The jobs stop on their own once
// their context is cancelled; waiting lets an in-flight delivery or status
// update finish writing its result before the process exits.
func waitForJobs(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
