package getklogs

import (
	"strings"
	"testing"
	"time"

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

func TestNewPodLogOptionsIncludesSinceTimeWhenPositive(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	options := newPodLogOptions("main", 3*time.Hour, now)

	if options.Container != "main" {
		t.Fatalf("expected container main, got %q", options.Container)
	}
	if !options.Timestamps {
		t.Fatal("expected timestamps to be enabled")
	}
	if options.SinceTime == nil {
		t.Fatal("expected SinceTime to be set")
	}
	if !options.SinceTime.Time.Equal(now.Add(-3 * time.Hour)) {
		t.Fatalf("expected SinceTime %s, got %s", now.Add(-3*time.Hour), options.SinceTime.Time)
	}
}

func TestNewPodLogOptionsOmitsSinceTimeWhenDisabled(t *testing.T) {
	options := newPodLogOptions("main", 0, time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC))

	if options.SinceTime != nil {
		t.Fatalf("expected SinceTime to be nil, got %s", options.SinceTime.Time)
	}
}

func TestReadLogEntriesSupportsLongLines(t *testing.T) {
	message := strings.Repeat("x", 128*1024)
	reader := strings.NewReader("2026-03-14T12:00:00Z " + message + "\n")

	entries, err := readLogEntries(reader, "pod-a", "main")
	if err != nil {
		t.Fatalf("readLogEntries returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Message != message {
		t.Fatalf("unexpected message length: got %d want %d", len(entries[0].Message), len(message))
	}
}
