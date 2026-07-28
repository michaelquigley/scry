package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

	stateStore := state.NewStore(cfg.StateFile)
	if _, err := engine.New(configuredChecks(cfg), stateStore, time.Now); err != nil {
		dl.Fatalf("initializing engine: %v", err)
	}
	dl.Infof("state reconciled; checks='%d'", len(cfg.Checks))
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
