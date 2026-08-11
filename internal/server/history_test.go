package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-faster/jx"
	"github.com/michaelquigley/scry/internal/api"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

// the fixture's ledger and records are laid out around generatedAt so that one
// explicit window exercises every bound rule at once.
var (
	windowFrom = generatedAt.Add(-4 * time.Hour)
	windowTo   = generatedAt.Add(-90 * time.Minute)
)

type memoryState struct {
	mu       sync.Mutex
	snapshot state.Snapshot
	saved    time.Time
}

func (repository *memoryState) Load() (state.Snapshot, time.Time, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot.Clone(), repository.saved, nil
}

func (repository *memoryState) Save(snapshot state.Snapshot, at time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.snapshot = snapshot.Clone()
	repository.saved = at
	return nil
}

func historyCheck(id string, kind model.Kind) model.Check {
	check := model.Check{ID: id, Name: id, Kind: kind}
	if kind == model.KindPassive {
		check.Period = 24 * time.Hour
		check.Grace = 2 * time.Hour
		check.HardenAfter = 3
	} else {
		check.FailAfter = 3
	}
	return check
}

func seededTransition(id string, kind model.Kind, from, to model.State, at, previousSince time.Time, detail string) model.Transition {
	transition := model.Transition{
		CheckID:       id,
		CheckName:     id,
		Kind:          kind,
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

func seededRecord(state model.State, since time.Time, passive bool) model.Record {
	record := model.Record{State: state, Since: since}
	if passive {
		seen := since
		record.LastSeen = &seen
	}
	return record
}

// historyEstate seeds a ledger and a state file, then boots a real engine over
// them, so the route is exercised end to end: request, serialized read, real
// window resolution, document.
func historyEstate(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "history")
	// the seed writes before any Boot, which is what would normally create the
	// directory.
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := history.NewStore(directory)

	// one watched span, a recorded outage, then the span the transitions below
	// were recorded in; it is left open, so this boot closes it at the saved
	// stamp the way a crash would be closed.
	requireSeeded(t, ledger.AppendStart(generatedAt.AddDate(0, 0, -100)))
	requireSeeded(t, ledger.AppendStop(generatedAt.AddDate(0, 0, -50)))
	requireSeeded(t, ledger.AppendStart(generatedAt.AddDate(0, 0, -49)))

	// web anchors rule one: its ok -> late falls before the window's start.
	requireSeeded(t, ledger.AppendTransition(seededTransition("web", model.KindHTTP, model.StateOK, model.StateLate, generatedAt.Add(-5*time.Hour), generatedAt.AddDate(0, 0, -40), "one timeout")))
	// rekinded resolves by look-ahead, and its event carries the kind it had
	// when it fired rather than the registry's current tcp.
	requireSeeded(t, ledger.AppendTransition(seededTransition("rekinded", model.KindHTTP, model.StateOK, model.StateFailed, generatedAt.Add(-3*time.Hour), generatedAt.AddDate(0, 0, -30), "connection refused")))
	requireSeeded(t, ledger.AppendTransition(seededTransition("web", model.KindHTTP, model.StateLate, model.StateFailed, generatedAt.Add(-3*time.Hour), generatedAt.Add(-5*time.Hour), "unexpected status 503")))
	// gone is not configured; nothing it recorded may reach the document.
	requireSeeded(t, ledger.AppendTransition(seededTransition("gone", model.KindHTTP, model.StateOK, model.StateFailed, generatedAt.Add(-150*time.Minute), generatedAt.AddDate(0, 0, -10), "retired")))
	requireSeeded(t, ledger.AppendTransition(seededTransition("web", model.KindHTTP, model.StateFailed, model.StateOK, generatedAt.Add(-150*time.Minute), generatedAt.Add(-3*time.Hour), "200 in 84ms")))
	// a silent transition is real for history even though the pager ignored it.
	requireSeeded(t, ledger.AppendTransition(seededTransition("web", model.KindHTTP, model.StateOK, model.StateLate, generatedAt.Add(-2*time.Hour), generatedAt.Add(-150*time.Minute), "one timeout")))
	// late-born's only event begins after the window opens, so its backward
	// claim is refused at from and admitted at to.
	requireSeeded(t, ledger.AppendTransition(seededTransition("late-born", model.KindHTTP, model.StateOK, model.StateFailed, generatedAt.Add(-time.Hour), generatedAt.Add(-2*time.Hour), "")))

	repository := &memoryState{
		saved: generatedAt.Add(-30 * time.Minute),
		snapshot: state.Snapshot{
			"web":       {Kind: model.KindHTTP, Record: seededRecord(model.StateLate, generatedAt.Add(-2*time.Hour), false)},
			"job":       {Kind: model.KindPassive, Record: seededRecord(model.StateOK, generatedAt.AddDate(0, 0, -120), true)},
			"fresh":     {Kind: model.KindHTTP, Record: seededRecord(model.StateOK, generatedAt.Add(-time.Hour), false)},
			"late-born": {Kind: model.KindHTTP, Record: seededRecord(model.StateFailed, generatedAt.Add(-time.Hour), false)},
			"rekinded":  {Kind: model.KindTCP, Record: seededRecord(model.StateFailed, generatedAt.Add(-3*time.Hour), false)},
		},
	}
	checks := []model.Check{
		historyCheck("web", model.KindHTTP),
		historyCheck("job", model.KindPassive),
		historyCheck("fresh", model.KindHTTP),
		historyCheck("late-born", model.KindHTTP),
		historyCheck("rekinded", model.KindTCP),
	}

	stateEngine, err := engine.New(checks, repository, ledger, fixedClock(generatedAt), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- stateEngine.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("engine run: %v", err)
		}
	})

	handler, err := newHandler(stateEngine, stateEngine, fixedClock(generatedAt), "test estate", stateEngine.Started(), dashboardFS())
	if err != nil {
		t.Fatal(err)
	}
	return handler, directory
}

func requireSeeded(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func serveHistory(t *testing.T, handler *Handler, query string, want int) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/history"
	if query != "" {
		target += "?" + query
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != want {
		t.Fatalf("status code: %d, want %d (%s)", response.Code, want, response.Body.String())
	}
	return response
}

func decodeHistory(t *testing.T, response *httptest.ResponseRecorder) api.History {
	t.Helper()
	var document api.History
	if err := document.Decode(jx.DecodeBytes(response.Body.Bytes())); err != nil {
		t.Fatalf("decoding history document: %v (%s)", err, response.Body.String())
	}
	return document
}

func bounds(from, to time.Time) string {
	values := url.Values{}
	values.Set("from", from.Format(time.RFC3339Nano))
	values.Set("to", to.Format(time.RFC3339Nano))
	return values.Encode()
}

func historyEntry(t *testing.T, document api.History, id string) api.CheckHistory {
	t.Helper()
	for _, entry := range document.Checks {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("missing check %q", id)
	return api.CheckHistory{}
}

func requireBoundState(t *testing.T, label string, got api.NilState, want model.State) {
	t.Helper()
	state, ok := got.Get()
	if !ok {
		t.Fatalf("%s: null, want %q", label, want)
	}
	if string(state) != string(want) {
		t.Fatalf("%s: %q, want %q", label, state, want)
	}
}

func TestHistoryResolvesEveryBoundRule(t *testing.T) {
	handler, _ := historyEstate(t)
	document := decodeHistory(t, serveHistory(t, handler, bounds(windowFrom, windowTo), http.StatusOK))

	if document.Estate != "test estate" {
		t.Fatalf("estate: %q", document.Estate)
	}
	if !document.From.Equal(windowFrom) || !document.To.Equal(windowTo) {
		t.Fatalf("bounds: %s..%s", document.From, document.To)
	}
	if !document.Generated.Equal(generatedAt) {
		t.Fatalf("generated: %s", document.Generated)
	}

	t.Run("rule one names the state at from", func(t *testing.T) {
		web := historyEntry(t, document, "web")
		requireBoundState(t, "web state_at_from", web.StateAtFrom, model.StateLate)
		requireBoundState(t, "web state_at_to", web.StateAtTo, model.StateLate)
		since, ok := web.Since.Get()
		if !ok || !since.Equal(generatedAt.Add(-2*time.Hour)) {
			t.Fatalf("web since: %+v", web.Since)
		}
	})

	t.Run("rule two carries the look-ahead's from state", func(t *testing.T) {
		rekinded := historyEntry(t, document, "rekinded")
		requireBoundState(t, "rekinded state_at_from", rekinded.StateAtFrom, model.StateOK)
		requireBoundState(t, "rekinded state_at_to", rekinded.StateAtTo, model.StateFailed)
	})

	t.Run("the live record answers when no event can", func(t *testing.T) {
		job := historyEntry(t, document, "job")
		requireBoundState(t, "job state_at_from", job.StateAtFrom, model.StateOK)
		requireBoundState(t, "job state_at_to", job.StateAtTo, model.StateOK)
		since, ok := job.Since.Get()
		if !ok || !since.Equal(generatedAt.AddDate(0, 0, -120)) {
			t.Fatalf("job since: %+v", job.Since)
		}
		if len(job.Events) != 0 {
			t.Fatalf("job events: %+v", job.Events)
		}
	})

	t.Run("nothing is claimed before a check existed", func(t *testing.T) {
		fresh := historyEntry(t, document, "fresh")
		if !fresh.StateAtFrom.IsNull() || !fresh.StateAtTo.IsNull() || !fresh.Since.IsNull() {
			t.Fatalf("fresh: %+v", fresh)
		}
	})

	t.Run("a backward claim is refused past prev_since", func(t *testing.T) {
		// late-born's first event began at -2h, after the window opened, so
		// the window's start gets no claim at all; the same event resolves the
		// tail, whose state began where its prev_since says.
		born := historyEntry(t, document, "late-born")
		if !born.StateAtFrom.IsNull() {
			t.Fatalf("late-born state_at_from: %+v", born.StateAtFrom)
		}
		requireBoundState(t, "late-born state_at_to", born.StateAtTo, model.StateOK)
		since, ok := born.Since.Get()
		if !ok || !since.Equal(generatedAt.Add(-2*time.Hour)) {
			t.Fatalf("late-born since: %+v", born.Since)
		}
		if len(born.Events) != 0 {
			t.Fatalf("late-born events: %+v", born.Events)
		}
	})
}

func TestHistoryEventsCarryEveryTransitionAsRecorded(t *testing.T) {
	handler, _ := historyEstate(t)
	document := decodeHistory(t, serveHistory(t, handler, bounds(windowFrom, windowTo), http.StatusOK))

	web := historyEntry(t, document, "web")
	if len(web.Events) != 3 {
		t.Fatalf("web events: %+v", web.Events)
	}
	if web.Events[0].From != api.StateLate || web.Events[0].To != api.StateFailed {
		t.Fatalf("first event: %+v", web.Events[0])
	}
	if !web.Events[0].PrevSince.Equal(generatedAt.Add(-5 * time.Hour)) {
		t.Fatalf("prev_since: %s", web.Events[0].PrevSince)
	}
	if detail, ok := web.Events[0].Detail.Get(); !ok || detail != "unexpected status 503" {
		t.Fatalf("detail: %+v", web.Events[0].Detail)
	}
	// the silent ok -> late is real for history even though it never paged.
	silent := web.Events[2]
	if silent.From != api.StateOk || silent.To != api.StateLate {
		t.Fatalf("silent transition missing: %+v", web.Events)
	}

	// per-event kind is the authoritative attribution; the entry's kind is the
	// registry's current convenience value.
	rekinded := historyEntry(t, document, "rekinded")
	if rekinded.Kind != api.KindTCP {
		t.Fatalf("registry kind: %s", rekinded.Kind)
	}
	if len(rekinded.Events) != 1 || rekinded.Events[0].Kind != api.KindHTTP {
		t.Fatalf("event kind: %+v", rekinded.Events)
	}

	// a check the registry no longer carries reaches nobody.
	for _, entry := range document.Checks {
		if entry.ID == "gone" {
			t.Fatal("an unconfigured check entered the document")
		}
		for _, event := range entry.Events {
			if detail, ok := event.Detail.Get(); ok && detail == "retired" {
				t.Fatalf("an unconfigured check's event entered %q", entry.ID)
			}
		}
	}
}

func TestHistoryCarriesDaemonLifecycleAndWatchingAtFrom(t *testing.T) {
	handler, _ := historyEstate(t)
	document := decodeHistory(t, serveHistory(t, handler, bounds(windowFrom, windowTo), http.StatusOK))

	// the transitions were recorded inside a watched span, so a window over
	// them opens watched and carries no lifecycle events of its own.
	if !document.WatchingAtFrom {
		t.Fatal("watching_at_from should be true inside a watched span")
	}
	if len(document.Daemon) != 0 {
		t.Fatalf("daemon events inside the window: %+v", document.Daemon)
	}

	// a window opening inside the recorded outage learns it from the flag
	// alone: the stop that tells the story fell before the window.
	gap := decodeHistory(t, serveHistory(t, handler, bounds(generatedAt.AddDate(0, 0, -50).Add(2*time.Hour), generatedAt.AddDate(0, 0, -49).Add(time.Hour)), http.StatusOK))
	if gap.WatchingAtFrom {
		t.Fatal("watching_at_from should be false inside the gap")
	}
	if len(gap.Daemon) != 1 || gap.Daemon[0].Event != "start" {
		t.Fatalf("gap daemon events: %+v", gap.Daemon)
	}

	wide := decodeHistory(t, serveHistory(t, handler, bounds(generatedAt.AddDate(0, 0, -99), generatedAt), http.StatusOK))
	if !wide.WatchingAtFrom {
		t.Fatal("watching_at_from should be true inside the first watched span")
	}
	events := make([]string, len(wide.Daemon))
	for i, event := range wide.Daemon {
		events[i] = string(event.Event)
	}
	// this boot closes the span the seed left open, then opens its own.
	if strings.Join(events, ",") != "stop,start,stop,start" {
		t.Fatalf("daemon events: %v", events)
	}
	if !wide.Daemon[2].Ts.Equal(generatedAt.Add(-30*time.Minute)) || !wide.Daemon[3].Ts.Equal(generatedAt) {
		t.Fatalf("closure and boot: %s, %s", wide.Daemon[2].Ts, wide.Daemon[3].Ts)
	}
}

func TestHistoryDefaultsAndOneSidedBounds(t *testing.T) {
	handler, _ := historyEstate(t)
	defaultWindow := 90 * 24 * time.Hour
	historical := generatedAt.Add(-2 * time.Hour)

	t.Run("both omitted", func(t *testing.T) {
		document := decodeHistory(t, serveHistory(t, handler, "", http.StatusOK))
		if !document.To.Equal(generatedAt) || !document.From.Equal(generatedAt.Add(-defaultWindow)) {
			t.Fatalf("bounds: %s..%s", document.From, document.To)
		}
		// the default window's right edge and the watermark are one reading.
		if !document.To.Equal(document.Generated) {
			t.Fatalf("to %s and generated %s diverged", document.To, document.Generated)
		}
	})

	t.Run("from only", func(t *testing.T) {
		values := url.Values{}
		values.Set("from", historical.Format(time.RFC3339Nano))
		document := decodeHistory(t, serveHistory(t, handler, values.Encode(), http.StatusOK))
		if !document.From.Equal(historical) || !document.To.Equal(generatedAt) {
			t.Fatalf("bounds: %s..%s", document.From, document.To)
		}
	})

	t.Run("to only anchors from to the resolved to", func(t *testing.T) {
		values := url.Values{}
		values.Set("to", historical.Format(time.RFC3339Nano))
		document := decodeHistory(t, serveHistory(t, handler, values.Encode(), http.StatusOK))
		if !document.To.Equal(historical) || !document.From.Equal(historical.Add(-defaultWindow)) {
			t.Fatalf("bounds: %s..%s", document.From, document.To)
		}
	})
}

func TestHistoryRejectsAnUnservableWindow(t *testing.T) {
	handler, _ := historyEstate(t)
	future := generatedAt.Add(time.Hour)

	cases := []struct {
		name  string
		query string
	}{
		{"inverted", bounds(generatedAt, generatedAt.Add(-time.Hour))},
		{"empty", bounds(generatedAt, generatedAt)},
		{"future from with an omitted to", "from=" + url.QueryEscape(future.Format(time.RFC3339Nano))},
		{"future to", bounds(generatedAt.Add(-time.Hour), future)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := serveHistory(t, handler, test.query, http.StatusBadRequest)
			var document api.Error
			if err := document.Decode(jx.DecodeBytes(response.Body.Bytes())); err != nil {
				t.Fatalf("decoding error document: %v (%s)", err, response.Body.String())
			}
			if !strings.Contains(document.Message, "history window") {
				t.Fatalf("message: %q", document.Message)
			}
		})
	}
}

// a ledger that stops parsing after boot fails the request, not the daemon.
func TestHistoryReadFailureIsARequestFailure(t *testing.T) {
	handler, directory := historyEstate(t)
	segment := filepath.Join(directory, generatedAt.Format("2006")+".jsonl")
	if err := os.WriteFile(segment, []byte("not a ledger line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	response := serveHistory(t, handler, "", http.StatusInternalServerError)
	var document api.Error
	if err := document.Decode(jx.DecodeBytes(response.Body.Bytes())); err != nil {
		t.Fatalf("decoding error document: %v (%s)", err, response.Body.String())
	}
	if !strings.Contains(document.Message, "line 1") {
		t.Fatalf("message: %q", document.Message)
	}

	// the daemon is still serving.
	serveStatusThrough(t, handler)
}

func serveStatusThrough(t *testing.T, handler *Handler) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status after a history read failure: %d", response.Code)
	}
}

func TestHistoryTimestampsRenderInUTC(t *testing.T) {
	zone := time.FixedZone("test", -5*3600)
	root := t.TempDir()
	directory := filepath.Join(root, "history")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := history.NewStore(directory)
	at := generatedAt.Add(-time.Hour).In(zone)
	requireSeeded(t, ledger.AppendTransition(seededTransition("web", model.KindHTTP, model.StateOK, model.StateFailed, at, at.Add(-time.Hour), "down")))

	repository := &memoryState{snapshot: state.Snapshot{
		"web": {Kind: model.KindHTTP, Record: seededRecord(model.StateFailed, at, false)},
	}}
	stateEngine, err := engine.New([]model.Check{historyCheck("web", model.KindHTTP)}, repository, ledger, fixedClock(generatedAt.In(zone)), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- stateEngine.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("engine run: %v", err)
		}
	})
	handler, err := newHandler(stateEngine, stateEngine, fixedClock(generatedAt), "test estate", stateEngine.Started(), dashboardFS())
	if err != nil {
		t.Fatal(err)
	}

	body := serveHistory(t, handler, "", http.StatusOK).Body.String()
	if strings.Contains(body, "-05:00") {
		t.Fatalf("timestamps should render in utc: %s", body)
	}
}

func TestHistoryHandlerRequiresItsCollaborators(t *testing.T) {
	if _, err := newHistoryHandler(nil, "test estate"); err == nil {
		t.Fatal("a nil reader should be rejected")
	}
	if _, err := newHistoryHandler(staticHistoryReader{}, ""); err == nil {
		t.Fatal("an empty estate name should be rejected")
	}
}

// the contract's lifecycle vocabulary converts directly from the ledger's, the
// same way kind and state convert from the model.
func TestContractLifecycleEventsMatchTheLedger(t *testing.T) {
	ledgerEvents := []history.EventType{history.EventStart, history.EventStop}
	apiEvents := api.LifecycleEventEvent("").AllValues()
	if len(ledgerEvents) != len(apiEvents) {
		t.Fatalf("lifecycle counts differ: ledger %v, contract %v", ledgerEvents, apiEvents)
	}
	for i, event := range ledgerEvents {
		if !event.Valid() || string(apiEvents[i]) != string(event) {
			t.Fatalf("lifecycle %d differs: ledger %q, contract %q", i, event, apiEvents[i])
		}
	}
}
