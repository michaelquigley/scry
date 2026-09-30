package server

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/api"
)

// the shared fixture is one history document whose uptime figures were worked
// by hand: a ten-day window with a six-hour daemon outage on the third day,
// and one check for each shape the walk must handle. the dashboard's suite
// reads the same file and must reach the same figures from the document's
// other fields — that is what pins the daemon's walk and the page's to each
// other across the contract.
const uptimeFixture = "../api/specs/fixtures/history-uptime.json"

func loadUptimeFixture(t *testing.T) *api.History {
	t.Helper()
	raw, err := os.ReadFile(uptimeFixture)
	if err != nil {
		t.Fatal(err)
	}
	var document api.History
	if err := document.UnmarshalJSON(raw); err != nil {
		t.Fatalf("fixture does not decode as a history document: %v", err)
	}
	return &document
}

func TestUptimeMatchesTheSharedFixture(t *testing.T) {
	document := loadUptimeFixture(t)
	if len(document.Checks) != 5 {
		t.Fatalf("fixture checks: %d", len(document.Checks))
	}
	for _, entry := range document.Checks {
		t.Run(entry.ID, func(t *testing.T) {
			counted := describeUptime(document, entry)
			if counted != entry.Uptime {
				t.Fatalf("counted %+v, fixture says %+v", counted, entry.Uptime)
			}
			if counted.Ok+counted.Late+counted.Failed != counted.Watched {
				t.Fatalf("states do not sum to watched: %+v", counted)
			}
		})
	}
}

// the walk's rules, each isolated on a small document. the fixture above holds
// their combinations; these name the rule a failure would be against.
func TestUptimeRules(t *testing.T) {
	at := func(offset time.Duration) time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(offset)
	}
	hour := int64(3600)
	document := func(watching bool, daemon ...api.LifecycleEvent) *api.History {
		if daemon == nil {
			daemon = []api.LifecycleEvent{}
		}
		return &api.History{From: at(0), To: at(10 * time.Hour), WatchingAtFrom: watching, Daemon: daemon}
	}
	entry := func(atFrom api.NilState, atTo api.NilState, since api.NilDateTime, events ...api.TransitionEvent) api.CheckHistory {
		if events == nil {
			events = []api.TransitionEvent{}
		}
		return api.CheckHistory{StateAtFrom: atFrom, StateAtTo: atTo, Since: since, Events: events}
	}
	state := func(value api.State) api.NilState { return api.NewNilState(value) }
	when := func(offset time.Duration) api.NilDateTime { return api.NewNilDateTime(at(offset)) }
	nullState := api.NilState{Null: true}
	nullTime := api.NilDateTime{Null: true}
	event := func(offset time.Duration, from, to api.State, prevSince time.Duration) api.TransitionEvent {
		return api.TransitionEvent{Ts: at(offset), Kind: api.KindTCP, From: from, To: to, PrevSince: at(prevSince)}
	}

	cases := []struct {
		name     string
		document *api.History
		entry    api.CheckHistory
		want     api.Uptime
	}{
		{
			name:     "a quiet watched window is all one state",
			document: document(true),
			entry:    entry(state(api.StateOk), state(api.StateOk), when(-100*time.Hour)),
			want:     api.Uptime{Watched: 10 * hour, Ok: 10 * hour},
		},
		{
			name:     "events split the window at their instants",
			document: document(true),
			entry: entry(state(api.StateOk), state(api.StateOk), when(7*time.Hour),
				event(2*time.Hour, api.StateOk, api.StateFailed, -100*time.Hour),
				event(7*time.Hour, api.StateFailed, api.StateOk, 2*time.Hour)),
			want: api.Uptime{Watched: 10 * hour, Ok: 5 * hour, Failed: 5 * hour},
		},
		{
			name:     "nothing is counted before a check existed",
			document: document(true),
			entry: entry(nullState, state(api.StateOk), when(6*time.Hour),
				event(6*time.Hour, api.StateLate, api.StateOk, 4*time.Hour)),
			want: api.Uptime{Watched: 6 * hour, Ok: 4 * hour, Late: 2 * hour},
		},
		{
			name:     "an eventless registration inside the window counts from its since",
			document: document(true),
			entry:    entry(nullState, state(api.StateOk), when(8*time.Hour)),
			want:     api.Uptime{Watched: 2 * hour, Ok: 2 * hour},
		},
		{
			name:     "a check that never existed counts nothing",
			document: document(true),
			entry:    entry(nullState, nullState, nullTime),
			want:     api.Uptime{},
		},
		{
			name: "an unwatched gap leaves the count, whatever state it falls in",
			document: document(true,
				api.LifecycleEvent{Ts: at(3 * time.Hour), Event: api.LifecycleEventEventStop},
				api.LifecycleEvent{Ts: at(5 * time.Hour), Event: api.LifecycleEventEventStart}),
			entry: entry(state(api.StateFailed), state(api.StateOk), when(4*time.Hour),
				event(4*time.Hour, api.StateFailed, api.StateOk, -100*time.Hour)),
			want: api.Uptime{Watched: 8 * hour, Failed: 3 * hour, Ok: 5 * hour},
		},
		{
			name: "a window that opens unwatched counts from the first start",
			document: document(false,
				api.LifecycleEvent{Ts: at(1 * time.Hour), Event: api.LifecycleEventEventStart}),
			entry: entry(state(api.StateOk), state(api.StateOk), when(-100*time.Hour)),
			want:  api.Uptime{Watched: 9 * hour, Ok: 9 * hour},
		},
		{
			name: "a stop with no start after it runs unwatched to the end",
			document: document(true,
				api.LifecycleEvent{Ts: at(9 * time.Hour), Event: api.LifecycleEventEventStop}),
			entry: entry(state(api.StateLate), state(api.StateLate), when(-100*time.Hour)),
			want:  api.Uptime{Watched: 9 * hour, Late: 9 * hour},
		},
		{
			name:     "a fractional second is truncated exactly as the wire truncates it",
			document: document(true),
			entry: entry(state(api.StateOk), state(api.StateFailed), when(3*time.Hour+900*time.Millisecond),
				event(3*time.Hour+900*time.Millisecond, api.StateOk, api.StateFailed, -100*time.Hour)),
			want: api.Uptime{Watched: 10 * hour, Ok: 3 * hour, Failed: 7 * hour},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counted := describeUptime(tc.document, tc.entry)
			if counted != tc.want {
				t.Fatalf("counted %+v, want %+v", counted, tc.want)
			}
		})
	}
}

// the route carries the count end to end: the seeded estate's web check spends
// the explicit window late, failed, ok, and late again, all of it watched.
func TestHistoryCarriesUptime(t *testing.T) {
	handler, _ := historyEstate(t)
	document := decodeHistory(t, serveHistory(t, handler, bounds(windowFrom, windowTo), http.StatusOK))
	web := historyEntry(t, document, "web")
	want := api.Uptime{Watched: 9000, Ok: 1800, Late: 5400, Failed: 1800}
	if web.Uptime != want {
		t.Fatalf("web uptime: %+v, want %+v", web.Uptime, want)
	}
	fresh := historyEntry(t, document, "fresh")
	if fresh.Uptime != (api.Uptime{}) {
		t.Fatalf("fresh uptime: %+v", fresh.Uptime)
	}
}
