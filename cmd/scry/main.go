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
	"github.com/michaelquigley/scry/internal/ingest"
	"github.com/michaelquigley/scry/internal/notify"
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
	destinations, err := configuredNotifiers(cfg)
	if err != nil {
		return fmt.Errorf("initialize notifiers: %w", err)
	}
	dispatcher, err := notify.NewDispatcher(destinations)
	if err != nil {
		return fmt.Errorf("initialize notifier dispatcher: %w", err)
	}
	stateStore := state.NewStore(cfg.StateFile)
	stateEngine, err := engine.New(configuredChecks(cfg), stateStore, time.Now, dispatcher)
	if err != nil {
		return fmt.Errorf("initialize engine: %w", err)
	}
	active := configuredActiveChecks(cfg)
	scheduler, err := engine.NewScheduler(stateEngine, active, engine.RandomJitter)
	if err != nil {
		return fmt.Errorf("initialize scheduler: %w", err)
	}
	ingestHandler, err := ingest.NewHandler(configuredPassiveChecks(cfg), stateEngine)
	if err != nil {
		return fmt.Errorf("initialize ingest handler: %w", err)
	}
	ingestServer, err := ingest.NewServer(cfg.IngestListen, ingestHandler)
	if err != nil {
		return fmt.Errorf("initialize ingest listener: %w", err)
	}

	dl.Infof(
		"daemon started; checks='%d' active='%d' notifiers='%d' ingest='%s'",
		len(cfg.Checks),
		len(active),
		len(destinations),
		ingestServer.Addr().String(),
	)
	if err := runComponents(
		ctx,
		component{name: "engine", run: stateEngine.Run},
		component{name: "scheduler", run: scheduler.Run},
		component{name: "notifier dispatcher", run: dispatcher.Run},
		component{name: "ingest listener", run: ingestServer.Run},
	); err != nil {
		return err
	}
	dl.Infof("daemon stopped")
	return nil
}

type component struct {
	name string
	run  func(context.Context) error
}

type componentResult struct {
	index int
	err   error
}

func runComponents(ctx context.Context, components ...component) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan componentResult, len(components))
	for i, current := range components {
		go func() {
			results <- componentResult{index: i, err: current.run(runCtx)}
		}()
	}

	errorsByComponent := make([]error, len(components))
	for range components {
		result := <-results
		errorsByComponent[result.index] = result.err
		cancel()
	}
	for i, err := range errorsByComponent {
		if err != nil {
			return fmt.Errorf("%s stopped: %w", components[i].name, err)
		}
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
