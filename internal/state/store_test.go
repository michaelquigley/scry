package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

var stateEpoch = time.Date(2026, 7, 27, 15, 0, 0, 123456789, time.Local)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := NewStore(path)
	transitionAt := stateEpoch.Add(time.Hour)
	seen := stateEpoch.Add(30 * time.Minute)
	status := model.Result{Status: model.StatusOK, Detail: "complete"}
	snapshot := Snapshot{
		"job": {
			Kind: model.KindPassive,
			Record: model.Record{
				State:          model.StateLate,
				Since:          transitionAt,
				LastTransition: &transitionAt,
				LastSeen:       &seen,
				LastResult:     &status,
			},
		},
		"web": {
			Kind: model.KindHTTP,
			Record: model.Record{
				State:            model.StateOK,
				Since:            stateEpoch,
				ConsecutiveFails: 0,
			},
		},
	}

	savedAt := stateEpoch.Add(2 * time.Hour)
	if err := store.Save(snapshot, savedAt); err != nil {
		t.Fatal(err)
	}
	loaded, saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	requireSnapshotEqual(t, loaded, snapshot)
	if !saved.Equal(savedAt) {
		t.Fatalf("saved: %v, want %v", saved, savedAt)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %o, want 600", info.Mode().Perm())
	}
}

func TestLoadMissingIsFirstBoot(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "missing", "state.json"))
	snapshot, saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 0 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if !saved.IsZero() {
		t.Fatalf("saved: %v, want zero", saved)
	}
}

func TestLoadPreStampFileHasNoSavedBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	content := `{
  "v": 1,
  "checks": {
    "web": {
      "kind": "http",
      "state": "ok",
      "since": "2026-07-27T15:00:00-04:00",
      "consecutive_fails": 0
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, saved, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if !saved.IsZero() {
		t.Fatalf("saved: %v, want zero", saved)
	}
}

func TestSaveWithoutAStampLeavesTheBoundAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	snapshot := Snapshot{
		"web": {
			Kind:   model.KindHTTP,
			Record: model.Record{State: model.StateOK, Since: stateEpoch},
		},
	}
	if err := store.Save(snapshot, time.Time{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "saved") {
		t.Fatalf("zero stamp reached the file: %s", data)
	}
	if _, saved, err := store.Load(); err != nil || !saved.IsZero() {
		t.Fatalf("saved: %v, error %v", saved, err)
	}
}

func TestLoadRejectsWholeFileFailures(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"malformed", `{"v":`, "parse state file"},
		{"trailing", `{"v":1,"checks":{}} {}`, "parse state file"},
		{"duplicate", `{"v":1,"v":1,"checks":{}}`, "parse state file"},
		{"unknown field", `{"v":1,"checks":{},"extra":true}`, "parse state file"},
		{"unsupported version", `{"v":2,"checks":{}}`, "unsupported version"},
		{"missing checks", `{"v":1}`, "parse state file"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := NewStore(path).Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidRecord(t *testing.T) {
	cases := []struct {
		name   string
		record string
		want   string
	}{
		{
			name: "passive missing last seen",
			record: `{
      "kind": "passive",
      "state": "ok",
      "since": "2026-07-27T15:00:00-04:00",
      "consecutive_fails": 0
    }`,
			want: "passive last_seen is required",
		},
		{
			name: "unpaired last result",
			record: `{
      "kind": "http",
      "state": "ok",
      "since": "2026-07-27T15:00:00-04:00",
      "last_status": "ok",
      "consecutive_fails": 0
    }`,
			want: "must appear together",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			content := `{
  "v": 1,
  "checks": {
    "check": ` + test.record + `
  }
}`
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := NewStore(path).Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v, want %q", err, test.want)
			}
		})
	}
}

func TestSaveRejectsInvalidSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	err := NewStore(path).Save(Snapshot{
		"web": {
			Kind: model.KindHTTP,
			Record: model.Record{
				State: model.StateOK,
			},
		},
	}, stateEpoch)
	if err == nil || !strings.Contains(err.Error(), "since is required") {
		t.Fatalf("error: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid save created a file: %v", statErr)
	}
}

func requireSnapshotEqual(t *testing.T, got, want Snapshot) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("snapshot size: %d, want %d", len(got), len(want))
	}
	for id, wantEntry := range want {
		gotEntry, found := got[id]
		if !found {
			t.Fatalf("missing check %q", id)
		}
		if gotEntry.Kind != wantEntry.Kind {
			t.Fatalf("check %q kind: %s, want %s", id, gotEntry.Kind, wantEntry.Kind)
		}
		requireRecordEqual(t, gotEntry.Record, wantEntry.Record)
	}
}

func requireRecordEqual(t *testing.T, got, want model.Record) {
	t.Helper()
	if got.State != want.State || !got.Since.Equal(want.Since) || got.ConsecutiveFails != want.ConsecutiveFails {
		t.Fatalf("record: %+v, want %+v", got, want)
	}
	requireTimeEqual(t, got.LastTransition, want.LastTransition)
	requireTimeEqual(t, got.LastSeen, want.LastSeen)
	switch {
	case got.LastResult == nil && want.LastResult == nil:
	case got.LastResult == nil || want.LastResult == nil:
		t.Fatalf("last result: %+v, want %+v", got.LastResult, want.LastResult)
	case *got.LastResult != *want.LastResult:
		t.Fatalf("last result: %+v, want %+v", got.LastResult, want.LastResult)
	}
}

func requireTimeEqual(t *testing.T, got, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Fatalf("time: %v, want %v", got, want)
	case !got.Equal(*want):
		t.Fatalf("time: %v, want %v", got, want)
	}
}
