package burst_test

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/observatory/burst"
)

func TestHealthPingPutResultAndCleanup(t *testing.T) {
	hp := burst.NewHealthPing(context.Background(), nil, &burst.HealthPingConfig{
		Interval:      10,
		SamplingCount: 3,
		Timeout:       2,
	})

	hp.PutResult("node1", 50*time.Millisecond)
	hp.PutResult("node2", 100*time.Millisecond)
	hp.PutResult("node3", 150*time.Millisecond)

	if len(hp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(hp.Results))
	}

	// Cleanup should keep only active tags
	hp.Cleanup([]string{"node1", "node3"})

	if len(hp.Results) != 2 {
		t.Fatalf("expected 2 results after cleanup, got %d", len(hp.Results))
	}
	if _, ok := hp.Results["node1"]; !ok {
		t.Errorf("expected node1 to be present")
	}
	if _, ok := hp.Results["node3"]; !ok {
		t.Errorf("expected node3 to be present")
	}
	if _, ok := hp.Results["node2"]; ok {
		t.Errorf("expected node2 to be deleted")
	}
}

func TestHealthPingStopScheduler(t *testing.T) {
	hp := burst.NewHealthPing(context.Background(), nil, &burst.HealthPingConfig{
		Interval:      10,
		SamplingCount: 2,
		Timeout:       2,
	})

	hp.StartScheduler(func() ([]string, error) {
		return []string{"tag1"}, nil
	})

	// Stopping scheduler should cleanly stop and cancel context
	hp.StopScheduler()

	// Repeated StopScheduler should not panic
	hp.StopScheduler()
}
