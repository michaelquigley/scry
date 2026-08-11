package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

var pruneEpoch = time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local)

func TestPruneStateRetiresOneCheckIDAndKeepsTheSavedStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := state.NewStore(path)
	snapshot := state.Snapshot{
		"web": {
			Kind:   model.KindHTTP,
			Record: model.Record{State: model.StateFailed, Since: pruneEpoch},
		},
		"job": {
			Kind:   model.KindPassive,
			Record: model.Record{State: model.StateOK, Since: pruneEpoch, LastSeen: &pruneEpoch},
		},
	}
	savedAt := pruneEpoch.Add(time.Hour)
	if err := store.Save(snapshot, savedAt); err != nil {
		t.Fatal(err)
	}

	entries, err := pruneState(path, "web")
	if err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("entries: %d, want 1", entries)
	}

	loaded, saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, found := loaded["web"]; found {
		t.Fatal("pruned check survived in the state file")
	}
	if _, found := loaded["job"]; !found {
		t.Fatal("prune removed another check")
	}
	// prune makes no liveness claim, so the bound history reads as the last
	// instant the daemon was alive must not move.
	if !saved.Equal(savedAt) {
		t.Fatalf("saved: %v, want %v", saved, savedAt)
	}
}

func TestPruneStateLeavesAnAbsentCheckAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := state.NewStore(path)
	if err := store.Save(state.Snapshot{
		"job": {
			Kind:   model.KindPassive,
			Record: model.Record{State: model.StateOK, Since: pruneEpoch, LastSeen: &pruneEpoch},
		},
	}, pruneEpoch); err != nil {
		t.Fatal(err)
	}

	entries, err := pruneState(path, "web")
	if err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Fatalf("entries: %d, want 0", entries)
	}
	loaded, saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || !saved.Equal(pruneEpoch) {
		t.Fatalf("state changed: %+v at %v", loaded, saved)
	}
}

func TestPrunedCheckResumesAsAFreshBaseline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	store := state.NewStore(path)
	old := pruneEpoch.Add(-48 * time.Hour)
	if err := store.Save(state.Snapshot{
		"web": {
			Kind:   model.KindHTTP,
			Record: model.Record{State: model.StateFailed, Since: old, LastTransition: &old},
		},
	}, pruneEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneState(path, "web"); err != nil {
		t.Fatal(err)
	}

	bootAt := pruneEpoch.Add(time.Hour)
	stateEngine, err := engine.New(
		[]model.Check{{ID: "web", Name: "web", Kind: model.KindHTTP, FailAfter: 3}},
		store,
		history.NewStore(filepath.Join(root, "history")),
		func() time.Time { return bootAt },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	record := stateEngine.Snapshot().Checks[0].Record
	if record.State != model.StateOK || !record.Since.Equal(bootAt) || record.LastTransition != nil {
		t.Fatalf("pruned check did not resume as a fresh baseline: %+v", record)
	}
}
