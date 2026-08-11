package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelquigley/scry/internal/config"
)

func TestRunDaemonStopsCleanlyWithItsContext(t *testing.T) {
	root := t.TempDir()
	cfg := config.NewConfig()
	cfg.StateFile = filepath.Join(root, "state.json")
	cfg.HistoryDir = filepath.Join(root, "history")
	cfg.IngestListen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runDaemon(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRunComponentsCancelsPeersAndNamesTheFailure(t *testing.T) {
	peerStarted := make(chan struct{})
	failure := errors.New("failed")
	err := runComponents(
		context.Background(),
		component{
			name: "failing component",
			run: func(context.Context) error {
				<-peerStarted
				return failure
			},
		},
		component{
			name: "peer",
			run: func(ctx context.Context) error {
				close(peerStarted)
				<-ctx.Done()
				return nil
			},
		},
	)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "failing component stopped") {
		t.Fatalf("error: %v", err)
	}
}
