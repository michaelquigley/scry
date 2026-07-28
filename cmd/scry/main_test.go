package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/michaelquigley/scry/internal/config"
)

func TestRunDaemonStopsCleanlyWithItsContext(t *testing.T) {
	cfg := config.NewConfig()
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runDaemon(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}
