package config

import (
	"testing"
	"time"
)

func TestOfflineAfterAllowsForPollAndFlushing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Collector.PollIntervalSec = 10
	cfg.Storage.FlushIntervalSec = 30

	if got := cfg.OfflineAfter(); got != 60*time.Second {
		t.Errorf("expected 60s (3 polls of 10s plus a 30s flush), got %v", got)
	}
}

func TestPathIsEmptyWithoutAFile(t *testing.T) {
	if got := DefaultConfig().Path(); got != "" {
		t.Errorf("expected no path for a default config, got %q", got)
	}
}
