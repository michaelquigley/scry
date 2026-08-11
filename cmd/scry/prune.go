package main

import (
	"fmt"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/scry/internal/config"
	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/state"
	"github.com/spf13/cobra"
)

var pruneCmd = &cobra.Command{
	Use:   "prune <check-id>",
	Short: "retire one check's recorded history and state",
	Long: `retire one check's recorded history and state.

prune takes a check id, the key both the ledger and the state file are written
under, not the check's display name. it removes every transition the ledger
holds under that id and drops the check's entry from the state file. daemon
lifecycle events are estate-scoped and are left alone. a still-configured check
resumes as a fresh baseline at the next boot.

both files are owned by the running daemon; stop it before pruning.`,
	Args: cobra.ExactArgs(1),
	Run:  runPrune,
}

func init() {
	rootCmd.AddCommand(pruneCmd)
}

func runPrune(_ *cobra.Command, args []string) {
	id := args[0]
	cfg, err := config.Load(configPath)
	if err != nil {
		dl.Fatalf("loading config: %v", err)
	}

	events, err := history.NewStore(cfg.HistoryDir).Prune(id)
	if err != nil {
		dl.Fatalf("pruning history: %v", err)
	}
	entries, err := pruneState(cfg.StateFile, id)
	if err != nil {
		dl.Fatalf("pruning state: %v", err)
	}
	dl.Infof("pruned check '%s'; history events='%d' state entries='%d'", id, events, entries)
}

// pruneState drops one check's record, preserving the loaded saved stamp:
// prune makes no liveness claim, so it must not move the bound history reads
// as the last instant the daemon was alive.
func pruneState(path, id string) (int, error) {
	store := state.NewStore(path)
	snapshot, saved, err := store.Load()
	if err != nil {
		return 0, fmt.Errorf("load state: %w", err)
	}
	if _, found := snapshot[id]; !found {
		return 0, nil
	}
	delete(snapshot, id)
	if err := store.Save(snapshot, saved); err != nil {
		return 0, fmt.Errorf("save state: %w", err)
	}
	return 1, nil
}
