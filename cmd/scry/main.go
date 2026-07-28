package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/state"
	"github.com/spf13/cobra"
)

var (
	verbose    bool
	configPath string
)

var rootCmd = &cobra.Command{
	Use:   strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0])),
	Short: "scry - status monitoring for a hand-curated estate",
	Run:   run,
}

func init() {
	dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/michaelquigley/"))
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "config file path")
	rootCmd.PersistentPreRun = func(_ *cobra.Command, _ []string) {
		if verbose {
			dl.Init(dl.DefaultOptions().SetTrimPrefix("github.com/michaelquigley/").SetLevel(slog.LevelDebug))
		}
	}
}

func run(_ *cobra.Command, _ []string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		dl.Fatalf("loading config: %v", err)
	}

	dl.Debugf(
		"config: status_listen='%s' ingest_listen='%s' state_file='%s' checks='%d'",
		cfg.StatusListen,
		cfg.IngestListen,
		cfg.StateFile,
		len(cfg.Checks),
	)

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if err := runDaemon(signalCtx, cfg); err != nil {
		dl.Fatalf("running daemon: %v", err)
	}
}

func runDaemon(ctx context.Context, cfg *config.Config) error {
	stateStore := state.NewStore(cfg.StateFile)
	stateEngine, err := engine.New(configuredChecks(cfg), stateStore, time.Now)
	if err != nil {
		return fmt.Errorf("initialize engine: %w", err)
	}
	active := configuredActiveChecks(cfg)
	scheduler, err := engine.NewScheduler(stateEngine, active, engine.RandomJitter)
	if err != nil {
		return fmt.Errorf("initialize scheduler: %w", err)
	}

	dl.Infof("daemon started; checks='%d' active='%d'", len(cfg.Checks), len(active))
	if err := runComponents(ctx, stateEngine, scheduler); err != nil {
		return err
	}
	dl.Infof("daemon stopped")
	return nil
}

type componentResult struct {
	name string
	err  error
}

func runComponents(ctx context.Context, stateEngine *engine.Engine, scheduler *engine.Scheduler) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan componentResult, 2)
	go func() {
		results <- componentResult{name: "engine", err: stateEngine.Run(runCtx)}
	}()
	go func() {
		results <- componentResult{name: "scheduler", err: scheduler.Run(runCtx)}
	}()

	completed := make([]componentResult, 0, 2)
	for len(completed) < 2 {
		result := <-results
		completed = append(completed, result)
		cancel()
	}
	for _, name := range []string{"engine", "scheduler"} {
		for _, result := range completed {
			if result.name == name && result.err != nil {
				return fmt.Errorf("%s stopped: %w", name, result.err)
			}
		}
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
