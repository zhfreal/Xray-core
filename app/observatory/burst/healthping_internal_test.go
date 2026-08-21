package burst

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestMaxDelayComputation verifies the deadline safety buffer:
// maxDelay = duration - timeout - 500ms, clamped to 0.
func TestMaxDelayComputation(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		timeout  time.Duration
		expected time.Duration
	}{
		{
			name:     "normal 15s interval with 5s timeout",
			duration: 15 * time.Second,
			timeout:  5 * time.Second,
			expected: 9500 * time.Millisecond,
		},
		{
			name:     "tight: barely covers timeout + buffer",
			duration: 6 * time.Second,
			timeout:  5 * time.Second,
			expected: 500 * time.Millisecond,
		},
		{
			name:     "equal: clamps to 0",
			duration: 5*time.Second + 500*time.Millisecond,
			timeout:  5 * time.Second,
			expected: 0,
		},
		{
			name:     "too tight: clamps to 0",
			duration: 5 * time.Second,
			timeout:  5 * time.Second,
			expected: 0,
		},
		{
			name:     "duration 0 (initial check): no delay",
			duration: 0,
			timeout:  5 * time.Second,
			expected: 0,
		},
		{
			name:     "negative result clamps to 0",
			duration: 3 * time.Second,
			timeout:  5 * time.Second,
			expected: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the exact logic from doCheck
			maxDelay := tt.duration
			if tt.duration > 0 {
				maxDelay = tt.duration - tt.timeout - 500*time.Millisecond
				if maxDelay <= 0 {
					maxDelay = 0
				}
			}
			if maxDelay != tt.expected {
				t.Errorf("maxDelay = %v, want %v (duration=%v, timeout=%v)",
					maxDelay, tt.expected, tt.duration, tt.timeout)
			}
		})
	}
}

// TestMeasureDelayContextCancellation verifies that cancelling the context
// causes MeasureDelay to return promptly instead of blocking on a slow server.
func TestMeasureDelayContextCancellation(t *testing.T) {
	// Start a server that sleeps for 10s — we should never wait that long
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newDirectPingClient(server.URL, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := client.MeasureDelay(ctx, http.MethodHead)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	// Should return well before the 10s server delay
	if elapsed > 2*time.Second {
		t.Errorf("MeasureDelay took %v after context cancellation, expected < 2s", elapsed)
	}
	t.Logf("MeasureDelay returned in %v with err: %v", elapsed, err)
}

// TestSemaphoreCapacityAndConcurrencyLimit verifies:
// 1. The semaphore channel has capacity == defaultMaxConcurrency.
// 2. Acquiring the semaphore actually limits concurrency.
func TestSemaphoreCapacityAndConcurrencyLimit(t *testing.T) {
	hp := NewHealthPing(context.Background(), nil, &HealthPingConfig{
		Interval:      10,
		SamplingCount: 1,
		Timeout:       2,
	})

	// Verify sem channel capacity
	if cap(hp.sem) != defaultMaxConcurrency {
		t.Errorf("sem capacity = %d, want %d", cap(hp.sem), defaultMaxConcurrency)
	}

	// Verify concurrency is actually limited by the semaphore.
	// Spawn more goroutines than defaultMaxConcurrency, each holding the
	// semaphore for a fixed duration, and track peak concurrency.
	const workers = defaultMaxConcurrency + 8
	var maxConcurrent atomic.Int32
	var current atomic.Int32
	done := make(chan struct{}, workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			// Acquire semaphore (same pattern as doCheck)
			hp.sem <- struct{}{}
			defer func() { <-hp.sem }()

			c := current.Add(1)
			// Update max seen concurrency
			for {
				old := maxConcurrent.Load()
				if c <= old || maxConcurrent.CompareAndSwap(old, c) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			current.Add(-1)
		}()
	}

	// Wait for all workers
	for i := 0; i < workers; i++ {
		<-done
	}

	peak := maxConcurrent.Load()
	if peak > int32(defaultMaxConcurrency) {
		t.Errorf("peak concurrency = %d, exceeded limit of %d", peak, defaultMaxConcurrency)
	}
	t.Logf("peak concurrency = %d (limit = %d, workers = %d)", peak, defaultMaxConcurrency, workers)
}
