package getklogs

import (
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

func TestConfigureRESTClientDisablesClientSideRateLimiting(t *testing.T) {
	originalLimiter := flowcontrol.NewTokenBucketRateLimiter(5, 10)
	config := &rest.Config{
		QPS:         5,
		Burst:       10,
		RateLimiter: originalLimiter,
	}

	config = configureRESTClient(config)

	if config.QPS >= 0 {
		t.Fatalf("expected negative QPS to disable client-side rate limiting, got %v", config.QPS)
	}
	if config.RateLimiter != nil {
		t.Fatal("expected RateLimiter to be cleared")
	}
	if config.Burst != 10 {
		t.Fatalf("expected Burst to stay unchanged, got %d", config.Burst)
	}
}
