package history

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/scry/internal/model"
)

var historyEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)

func TestAppendAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	start := historyEpoch
	failedAt := historyEpoch.AddDate(0, 2, 0)
	recoveredAt := failedAt.Add(30 * time.Minute)
	stop := recoveredAt.Add(time.Hour)

	requireNoError(t, store.AppendStart(start))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, failedAt, historyEpoch, "connection refused")))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateFailed, model.StateOK, recoveredAt, failedAt, "")))
	requireNoError(t, store.AppendStop(stop))

	window := requireWindow(t, store, start.Add(-time.Hour), stop.Add(time.Hour))
	if len(window.Events) != 4 {
		t.Fatalf("events: %d, want 4", len(window.Events))
	}
	requireEvent(t, window.Events[0], Event{Version: lineVersion, TS: start, Type: EventStart})
	requireEvent(t, window.Events[3], Event{Version: lineVersion, TS: stop, Type: EventStop})

	failed := window.Events[1]
	if failed.Type != EventTransition || failed.Check != "web" || failed.Kind != model.KindHTTP {
		t.Fatalf("transition: %+v", failed)
	}
	if failed.From != model.StateOK || failed.To != model.StateFailed {
		t.Fatalf("transition endpoints: %+v", failed)
	}
	if failed.PrevSince == nil || !failed.PrevSince.Equal(historyEpoch) {
		t.Fatalf("prev_since: %v", failed.PrevSince)
	}
	if failed.Detail == nil || *failed.Detail != "connection refused" {
		t.Fatalf("detail: %v", failed.Detail)
	}
	if window.Events[2].Detail != nil {
		t.Fatalf("empty detail was recorded: %v", *window.Events[2].Detail)
	}

	lines := readLines(t, filepath.Join(dir, "2026.jsonl"))
	if len(lines) != 4 {
		t.Fatalf("lines: %d, want 4", len(lines))
	}
	if strings.Contains(lines[0], "check") || strings.Contains(lines[0], "from") {
		t.Fatalf("lifecycle line carries transition fields: %s", lines[0])
	}
	if strings.Contains(lines[2], "detail") {
		t.Fatalf("empty detail reached the line: %s", lines[2])
	}

	info, err := os.Stat(filepath.Join(dir, "2026.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %o, want 600", info.Mode().Perm())
	}
}

func TestAppendRejectsInvalidEvents(t *testing.T) {
	store := NewStore(t.TempDir())
	cases := []struct {
		name       string
		transition model.Transition
		want       string
	}{
		{
			name:       "missing prev since",
			transition: model.Transition{CheckID: "web", Kind: model.KindHTTP, From: model.StateOK, To: model.StateFailed, At: historyEpoch},
			want:       "prev_since is required",
		},
		{
			name:       "identical endpoints",
			transition: model.Transition{CheckID: "web", Kind: model.KindHTTP, From: model.StateOK, To: model.StateOK, At: historyEpoch, PreviousSince: historyEpoch},
			want:       "from and to are both",
		},
		{
			name:       "missing check",
			transition: model.Transition{Kind: model.KindHTTP, From: model.StateOK, To: model.StateFailed, At: historyEpoch, PreviousSince: historyEpoch},
			want:       "check is required",
		},
		{
			name:       "zero timestamp",
			transition: model.Transition{CheckID: "web", Kind: model.KindHTTP, From: model.StateOK, To: model.StateFailed, PreviousSince: historyEpoch},
			want:       "ts is required",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := store.AppendTransition(test.transition)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v, want %q", err, test.want)
			}
		})
	}
}

func TestBootOnEmptyDirectoryAppendsStartOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	store := NewStore(dir)
	requireNoError(t, store.Boot(historyEpoch, time.Time{}, nil))

	window := requireWindow(t, store, historyEpoch.Add(-time.Hour), historyEpoch.Add(time.Hour))
	if len(window.Events) != 1 {
		t.Fatalf("events: %+v", window.Events)
	}
	requireEvent(t, window.Events[0], Event{Version: lineVersion, TS: historyEpoch, Type: EventStart})
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("history directory: %v", err)
	}
}

func TestBootClosesUncleanLedger(t *testing.T) {
	newest := historyEpoch.AddDate(0, 1, 0)
	bootAt := newest.AddDate(0, 0, 1)
	cases := []struct {
		name      string
		seed      func(*testing.T, *Store)
		lastSaved time.Time
		want      []Event
	}{
		{
			name: "saved after the newest event",
			seed: func(t *testing.T, store *Store) {
				requireNoError(t, store.AppendStart(historyEpoch))
				requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, newest, historyEpoch, "")))
			},
			lastSaved: newest.Add(5 * time.Minute),
			want: []Event{
				{TS: historyEpoch, Type: EventStart},
				{TS: newest.Add(5 * time.Minute), Type: EventStop},
				{TS: bootAt, Type: EventStart},
			},
		},
		{
			name: "newest event after saved",
			seed: func(t *testing.T, store *Store) {
				requireNoError(t, store.AppendStart(historyEpoch))
				requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, newest, historyEpoch, "")))
			},
			lastSaved: newest.Add(-5 * time.Minute),
			want: []Event{
				{TS: historyEpoch, Type: EventStart},
				{TS: newest, Type: EventStop},
				{TS: bootAt, Type: EventStart},
			},
		},
		{
			name: "no saved stamp falls back to the newest event",
			seed: func(t *testing.T, store *Store) {
				requireNoError(t, store.AppendStart(historyEpoch))
				requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, newest, historyEpoch, "")))
			},
			want: []Event{
				{TS: historyEpoch, Type: EventStart},
				{TS: newest, Type: EventStop},
				{TS: bootAt, Type: EventStart},
			},
		},
		{
			// the ledger's own start is the only liveness evidence there is,
			// and it still has to be closed: two starts never stand without a
			// stop between them.
			name: "crash between the first start and the first stamped save",
			seed: func(t *testing.T, store *Store) {
				requireNoError(t, store.AppendStart(historyEpoch))
			},
			want: []Event{
				{TS: historyEpoch, Type: EventStart},
				{TS: historyEpoch, Type: EventStop},
				{TS: bootAt, Type: EventStart},
			},
		},
		{
			name: "clean stop is not reclosed",
			seed: func(t *testing.T, store *Store) {
				requireNoError(t, store.AppendStart(historyEpoch))
				requireNoError(t, store.AppendStop(newest))
			},
			lastSaved: newest,
			want: []Event{
				{TS: historyEpoch, Type: EventStart},
				{TS: newest, Type: EventStop},
				{TS: bootAt, Type: EventStart},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			test.seed(t, store)
			requireNoError(t, store.Boot(bootAt, test.lastSaved, map[string]struct{}{"web": {}}))

			window := requireWindow(t, store, historyEpoch.Add(-time.Hour), bootAt.Add(time.Hour))
			lifecycle := make([]Event, 0, len(window.Events))
			for _, event := range window.Events {
				if event.Type != EventTransition {
					lifecycle = append(lifecycle, event)
				}
			}
			if len(lifecycle) != len(test.want) {
				t.Fatalf("lifecycle events: %+v, want %+v", lifecycle, test.want)
			}
			for i, want := range test.want {
				if lifecycle[i].Type != want.Type || !lifecycle[i].TS.Equal(want.TS) {
					t.Fatalf("lifecycle[%d]: %s at %s, want %s at %s", i, lifecycle[i].Type, lifecycle[i].TS, want.Type, want.TS)
				}
			}
		})
	}
}

func TestBootFailsOnMalformedSegment(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	requireNoError(t, store.AppendStart(historyEpoch))
	writeSegment(t, dir, "2025.jsonl", `{"v":1,"ts":"2025-06-01T00:00:00Z","event":`)

	err := store.Boot(historyEpoch.AddDate(0, 0, 1), time.Time{}, nil)
	if err == nil || !strings.Contains(err.Error(), "2025.jsonl") || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error: %v", err)
	}
}

func TestBootWarnsOncePerUnconfiguredCheck(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	older := historyEpoch.AddDate(-1, 0, 0)
	requireNoError(t, store.AppendTransition(newTransition("gone", model.StateOK, model.StateFailed, older, older.Add(-time.Hour), "")))
	requireNoError(t, store.AppendTransition(newTransition("gone", model.StateFailed, model.StateOK, older.Add(time.Hour), older, "")))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, historyEpoch, older, "")))
	requireNoError(t, store.AppendTransition(newTransition("other", model.StateOK, model.StateFailed, historyEpoch.Add(time.Hour), older, "")))

	logged := captureLog(t, func() {
		requireNoError(t, store.Boot(historyEpoch.AddDate(0, 1, 0), time.Time{}, map[string]struct{}{"web": {}}))
	})
	for _, id := range []string{"gone", "other"} {
		want := "ignoring history for unconfigured check '" + id + "'"
		if count := strings.Count(logged, want); count != 1 {
			t.Fatalf("warnings for %q: %d, want 1\n%s", id, count, logged)
		}
	}
	if strings.Contains(logged, "unconfigured check 'web'") {
		t.Fatalf("configured check warned:\n%s", logged)
	}
}

func TestYearRolloverLandsEventsInPerYearSegments(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	before := time.Date(2026, 12, 31, 23, 59, 0, 0, time.Local)
	after := time.Date(2027, 1, 1, 0, 1, 0, 0, time.Local)
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, before, before.Add(-time.Hour), "")))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateFailed, model.StateOK, after, before, "")))

	for _, name := range []string{"2026.jsonl", "2027.jsonl"} {
		if lines := readLines(t, filepath.Join(dir, name)); len(lines) != 1 {
			t.Fatalf("%s lines: %d, want 1", name, len(lines))
		}
	}
	window := requireWindow(t, store, before.Add(-time.Hour), after.Add(time.Hour))
	if len(window.Events) != 2 || !window.Events[0].TS.Equal(before) {
		t.Fatalf("events: %+v", window.Events)
	}
}

func TestWindowResolvesBothBounds(t *testing.T) {
	// web transitions twice early in 2026, job once in the middle of it, and
	// vpn only in 2027 — its single transition is the sole evidence that vpn
	// existed at all during the 2026 windows below.
	webFailed := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	webRecovered := time.Date(2026, 3, 2, 0, 0, 0, 0, time.Local)
	webBorn := time.Date(2026, 2, 1, 0, 0, 0, 0, time.Local)
	jobLate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	jobBorn := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	vpnFailed := time.Date(2027, 2, 1, 0, 0, 0, 0, time.Local)
	vpnBorn := time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local)

	store := NewStore(t.TempDir())
	requireNoError(t, store.AppendStart(historyEpoch))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, webFailed, webBorn, "connection refused")))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateFailed, model.StateOK, webRecovered, webFailed, "200 in 84ms")))
	requireNoError(t, store.AppendTransition(newTransition("job", model.StateOK, model.StateLate, jobLate, jobBorn, "")))
	requireNoError(t, store.AppendTransition(newTransition("vpn", model.StateOK, model.StateFailed, vpnFailed, vpnBorn, "")))

	t.Run("rule one at both bounds", func(t *testing.T) {
		window := requireWindow(t, store, time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local))
		requireStateAtFrom(t, window, "web", model.StateOK)
		requireTailAtTo(t, window, "web", model.StateOK, webRecovered)
		if len(window.Events) != 0 {
			t.Fatalf("events: %+v", window.Events)
		}
	})

	t.Run("rule two refused and admitted at opposite bounds", func(t *testing.T) {
		// job's only event has prev_since 2026-05-01: after the from bound,
		// so nothing is claimed there, and at the to bound exactly, so the
		// eventless window still resolves a tail from the look-ahead.
		window := requireWindow(t, store, time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local))
		if state, found := window.StateAtFrom["job"]; found {
			t.Fatalf("job state at from: %q, want absent", state)
		}
		requireTailAtTo(t, window, "job", model.StateOK, jobBorn)
	})

	t.Run("rule two at from", func(t *testing.T) {
		window := requireWindow(t, store, time.Date(2026, 2, 15, 0, 0, 0, 0, time.Local), time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local))
		requireStateAtFrom(t, window, "web", model.StateOK)
		requireTailAtTo(t, window, "web", model.StateFailed, webFailed)
		if len(window.Events) != 1 || !window.Events[0].TS.Equal(webFailed) {
			t.Fatalf("events: %+v", window.Events)
		}
	})

	t.Run("cross-year look-ahead", func(t *testing.T) {
		window := requireWindow(t, store, time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local))
		requireStateAtFrom(t, window, "vpn", model.StateOK)
		requireTailAtTo(t, window, "vpn", model.StateOK, vpnBorn)
	})

	t.Run("absent before existence", func(t *testing.T) {
		window := requireWindow(t, store, historyEpoch, time.Date(2026, 1, 10, 0, 0, 0, 0, time.Local))
		for _, id := range []string{"web", "job", "vpn"} {
			if state, found := window.StateAtFrom[id]; found {
				t.Fatalf("%s state at from: %q, want absent", id, state)
			}
			if tail, found := window.TailAtTo[id]; found {
				t.Fatalf("%s tail at to: %+v, want absent", id, tail)
			}
		}
	})
}

func TestWindowWatchingAtFrom(t *testing.T) {
	first := historyEpoch
	stopped := historyEpoch.AddDate(0, 1, 0)
	restarted := stopped.Add(4 * time.Hour)

	store := NewStore(t.TempDir())
	requireNoError(t, store.AppendStart(first))
	requireNoError(t, store.AppendStop(stopped))
	requireNoError(t, store.AppendStart(restarted))

	cases := []struct {
		name string
		from time.Time
		want bool
	}{
		{"before the first lifecycle event", first.Add(-time.Hour), false},
		{"at the first start", first, true},
		{"inside the first watched span", first.Add(time.Hour), true},
		{"at the stop", stopped, false},
		{"inside the gap", stopped.Add(time.Hour), false},
		{"after the restart", restarted.Add(time.Hour), true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			window := requireWindow(t, store, test.from, restarted.AddDate(0, 1, 0))
			if window.WatchingAtFrom != test.want {
				t.Fatalf("watching at from: %t, want %t", window.WatchingAtFrom, test.want)
			}
		})
	}
}

func TestWindowKeepsStopBeforeStartAtOneInstant(t *testing.T) {
	store := NewStore(t.TempDir())
	requireNoError(t, store.AppendStop(historyEpoch))
	requireNoError(t, store.AppendStart(historyEpoch))

	window := requireWindow(t, store, historyEpoch.Add(-time.Hour), historyEpoch.Add(time.Hour))
	if len(window.Events) != 2 || window.Events[0].Type != EventStop || window.Events[1].Type != EventStart {
		t.Fatalf("events: %+v", window.Events)
	}
	// with both events at the bound itself, append order decides which one
	// the bound resolution sees last.
	atInstant := requireWindow(t, store, historyEpoch, historyEpoch.Add(time.Hour))
	if !atInstant.WatchingAtFrom {
		t.Fatalf("the start at the bound did not win the tiebreak")
	}
}

func TestWindowFailsOnMalformedLine(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	requireNoError(t, store.AppendStart(historyEpoch))
	appendRaw(t, dir, "2026.jsonl", `{"v":1,"ts":"2026-02-01T00:00:00Z","event":"transition","check":"web"}`)

	_, err := store.Window(historyEpoch, historyEpoch.AddDate(1, 0, 0))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error: %v", err)
	}
}

func TestPruneRetiresOneCheckID(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	older := time.Date(2025, 6, 1, 0, 0, 0, 0, time.Local)
	requireNoError(t, store.AppendStart(older))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, older.Add(time.Hour), older, "")))
	requireNoError(t, store.AppendTransition(newTransition("job", model.StateOK, model.StateLate, older.Add(2*time.Hour), older, "")))
	requireNoError(t, store.AppendStop(older.Add(3*time.Hour)))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateFailed, model.StateOK, historyEpoch, older.Add(time.Hour), "")))

	removed, err := store.Prune("web")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed: %d, want 2", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("emptied segment survived: %v", err)
	}
	if lines := readLines(t, filepath.Join(dir, "2025.jsonl")); len(lines) != 3 {
		t.Fatalf("2025 lines: %d, want 3", len(lines))
	}

	window := requireWindow(t, store, older.Add(-time.Hour), historyEpoch.Add(time.Hour))
	if len(window.Events) != 3 {
		t.Fatalf("events: %+v", window.Events)
	}
	for _, event := range window.Events {
		if event.Check == "web" {
			t.Fatalf("pruned event survived: %+v", event)
		}
	}
	if _, found := window.TailAtTo["job"]; !found {
		t.Fatalf("prune touched another check's history")
	}
}

func TestPruneLeavesUnmatchedLedgerAlone(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	requireNoError(t, store.AppendStart(historyEpoch))
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, historyEpoch.Add(time.Hour), historyEpoch, "")))
	before := readLines(t, filepath.Join(dir, "2026.jsonl"))

	removed, err := store.Prune("absent")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed: %d, want 0", removed)
	}
	if after := readLines(t, filepath.Join(dir, "2026.jsonl")); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("segment rewritten: %v, want %v", after, before)
	}
}

func TestPruneRefusesMalformedLedger(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	requireNoError(t, store.AppendTransition(newTransition("web", model.StateOK, model.StateFailed, historyEpoch, historyEpoch.Add(-time.Hour), "")))
	writeSegment(t, dir, "2025.jsonl", "not json\n")

	if _, err := store.Prune("web"); err == nil || !strings.Contains(err.Error(), "2025.jsonl") {
		t.Fatalf("error: %v", err)
	}
	if lines := readLines(t, filepath.Join(dir, "2026.jsonl")); len(lines) != 1 {
		t.Fatalf("refused prune still rewrote a segment: %v", lines)
	}
}

func TestPruneRequiresACheckID(t *testing.T) {
	if _, err := NewStore(t.TempDir()).Prune(""); err == nil {
		t.Fatal("empty check id accepted")
	}
}

func newTransition(id string, from, to model.State, at, previousSince time.Time, detail string) model.Transition {
	transition := model.Transition{
		CheckID:       id,
		CheckName:     id,
		Kind:          model.KindHTTP,
		From:          from,
		To:            to,
		At:            at,
		PreviousSince: previousSince,
	}
	if detail != "" {
		transition.Result = &model.Result{Status: model.StatusFailed, Detail: detail}
	}
	return transition
}

func requireWindow(t *testing.T, store *Store, from, to time.Time) Window {
	t.Helper()
	window, err := store.Window(from, to)
	if err != nil {
		t.Fatal(err)
	}
	return window
}

func requireStateAtFrom(t *testing.T, window Window, id string, want model.State) {
	t.Helper()
	state, found := window.StateAtFrom[id]
	if !found {
		t.Fatalf("%s state at from: absent, want %q", id, want)
	}
	if state != want {
		t.Fatalf("%s state at from: %q, want %q", id, state, want)
	}
}

func requireTailAtTo(t *testing.T, window Window, id string, want model.State, since time.Time) {
	t.Helper()
	tail, found := window.TailAtTo[id]
	if !found {
		t.Fatalf("%s tail at to: absent, want %q since %s", id, want, since)
	}
	if tail.State != want || !tail.Since.Equal(since) {
		t.Fatalf("%s tail at to: %q since %s, want %q since %s", id, tail.State, tail.Since, want, since)
	}
}

func requireEvent(t *testing.T, got, want Event) {
	t.Helper()
	if got.Version != want.Version || got.Type != want.Type || !got.TS.Equal(want.TS) {
		t.Fatalf("event: %+v, want %+v", got, want)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func writeSegment(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, dir, name, line string) {
	t.Helper()
	segment, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer segment.Close()
	if _, err := segment.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// captureLog redirects dl's global default channel for the duration of one
// call. the package's tests never run in parallel, so the redirect is safe.
func captureLog(t *testing.T, run func()) string {
	t.Helper()
	var buffer bytes.Buffer
	dl.Init(dl.DefaultOptions().SetOutput(&buffer))
	defer dl.Init(dl.DefaultOptions())
	run()
	return buffer.String()
}
