package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-faster/jx"
	"github.com/michaelquigley/scry/internal/api"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/model"
)

var generatedAt = time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)

type staticReader struct {
	snapshot engine.Snapshot
}

func (reader staticReader) Snapshot() engine.Snapshot {
	return reader.snapshot
}

func fixedClock(at time.Time) engine.Clock {
	return func() time.Time { return at }
}

func timePtr(at time.Time) *time.Time {
	return &at
}

// estateSnapshot carries one check of each kind, in registry order, covering a
// fresh registration, an announced passive window, and a damped active failure.
func estateSnapshot() engine.Snapshot {
	lateSince := generatedAt.Add(-2 * time.Hour)
	lastSeen := generatedAt.Add(-26 * time.Hour)
	failedSince := generatedAt.Add(-15 * time.Minute)
	return engine.Snapshot{Checks: []model.CheckRecord{
		{
			Check: model.Check{ID: "nas-snapshot", Name: "NAS nightly snapshot", Kind: model.KindPassive},
			Record: model.Record{
				State:          model.StateLate,
				Since:          lateSince,
				LastTransition: timePtr(lateSince),
				LastSeen:       timePtr(lastSeen),
				LastResult:     &model.Result{Status: model.StatusOK, Detail: "snapshot complete"},
			},
		},
		{
			Check: model.Check{ID: "gitea", Name: "gitea web", Kind: model.KindHTTP},
			Record: model.Record{
				State:            model.StateFailed,
				Since:            failedSince,
				LastTransition:   timePtr(failedSince),
				LastResult:       &model.Result{Status: model.StatusFailed, Detail: "unexpected status 503"},
				ConsecutiveFails: 3,
			},
		},
		{
			Check:  model.Check{ID: "pg-hq", Name: "postgres", Kind: model.KindTCP},
			Record: model.Record{State: model.StateOK, Since: generatedAt.Add(-time.Minute)},
		},
	}}
}

func serveStatus(t *testing.T, snapshot engine.Snapshot) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := newHandler(staticReader{snapshot: snapshot}, fixedClock(generatedAt), dashboardFS())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code: %d", response.Code)
	}
	return response
}

// decodeStatus reads the response with the contract's own generated decoder,
// so the assertions below see exactly what a generated client would see.
func decodeStatus(t *testing.T, response *httptest.ResponseRecorder) api.Status {
	t.Helper()
	var document api.Status
	if err := document.Decode(jx.DecodeBytes(response.Body.Bytes())); err != nil {
		t.Fatalf("decoding status document: %v (%s)", err, response.Body.String())
	}
	return document
}

func TestStatusWalkRendersEveryCheckInRegistryOrder(t *testing.T) {
	document := decodeStatus(t, serveStatus(t, estateSnapshot()))

	if !document.Generated.Equal(generatedAt) {
		t.Fatalf("generated: %s", document.Generated)
	}
	if document.Rollup.Ok != 1 || document.Rollup.Late != 1 || document.Rollup.Failed != 1 {
		t.Fatalf("rollup: %+v", document.Rollup)
	}
	ids := make([]string, len(document.Checks))
	for i, check := range document.Checks {
		ids[i] = check.ID
	}
	if strings.Join(ids, ",") != "nas-snapshot,gitea,pg-hq" {
		t.Fatalf("registry order: %v", ids)
	}

	passive := document.Checks[0]
	if passive.Kind != api.KindPassive || passive.State != api.StateLate || passive.Name != "NAS nightly snapshot" {
		t.Fatalf("passive check: %+v", passive)
	}
	if lastSeen, ok := passive.LastSeen.Get(); !ok || !lastSeen.Equal(generatedAt.Add(-26*time.Hour)) {
		t.Fatalf("passive last_seen: %+v", passive.LastSeen)
	}
	if detail, ok := passive.Detail.Get(); !ok || detail != "snapshot complete" {
		t.Fatalf("passive detail: %+v", passive.Detail)
	}
}

func TestStatusRollupCountsEveryCheckExactlyOnce(t *testing.T) {
	document := decodeStatus(t, serveStatus(t, estateSnapshot()))

	counted := document.Rollup.Ok + document.Rollup.Late + document.Rollup.Failed
	if counted != len(document.Checks) {
		t.Fatalf("rollup counted %d of %d checks", counted, len(document.Checks))
	}
}

// the absent history fields are null rather than missing: the contract declares
// them required and nullable, so a consumer reads one shape whatever happened.
func TestAbsentHistoryFieldsRenderAsExplicitNulls(t *testing.T) {
	response := serveStatus(t, estateSnapshot())
	document := decodeStatus(t, response)

	active := document.Checks[1]
	if !active.LastSeen.IsNull() {
		t.Fatalf("active last_seen should be null: %+v", active.LastSeen)
	}
	fresh := document.Checks[2]
	if !fresh.LastTransition.IsNull() || !fresh.LastSeen.IsNull() || !fresh.Detail.IsNull() {
		t.Fatalf("fresh registration should carry no history: %+v", fresh)
	}
	body := response.Body.String()
	for _, null := range []string{`"last_seen":null`, `"last_transition":null`, `"detail":null`} {
		if !strings.Contains(body, null) {
			t.Fatalf("body should carry %s: %s", null, body)
		}
	}
}

// an empty registry still renders a complete document, so the dashboard has one
// shape to render rather than a special case.
func TestEmptyRegistryRendersAZeroRollupAndNoChecks(t *testing.T) {
	document := decodeStatus(t, serveStatus(t, engine.Snapshot{}))

	if len(document.Checks) != 0 {
		t.Fatalf("checks: %+v", document.Checks)
	}
	if document.Rollup.Ok != 0 || document.Rollup.Late != 0 || document.Rollup.Failed != 0 {
		t.Fatalf("rollup: %+v", document.Rollup)
	}
}

// every timestamp leaves as UTC regardless of the daemon's local zone, so the
// contract's date-time values never depend on where scry runs.
func TestTimestampsRenderInUTC(t *testing.T) {
	zone := time.FixedZone("test", -5*3600)
	local := generatedAt.In(zone)
	snapshot := engine.Snapshot{Checks: []model.CheckRecord{{
		Check: model.Check{ID: "job", Name: "job", Kind: model.KindPassive},
		Record: model.Record{
			State:          model.StateOK,
			Since:          local,
			LastTransition: timePtr(local),
			LastSeen:       timePtr(local),
		},
	}}}

	body := serveStatus(t, snapshot).Body.String()
	if strings.Contains(body, "-05:00") {
		t.Fatalf("timestamps should render in utc: %s", body)
	}
}

func TestStatusHandlerRequiresItsCollaborators(t *testing.T) {
	if _, err := newStatusHandler(nil, fixedClock(generatedAt)); err == nil {
		t.Fatal("a nil reader should be rejected")
	}
	if _, err := newStatusHandler(staticReader{}, nil); err == nil {
		t.Fatal("a nil clock should be rejected")
	}
}

// describeCheck converts the model's kind and state vocabularies directly into
// the contract's; this proves the two enumerations stay equal.
func TestContractEnumerationsMatchTheModel(t *testing.T) {
	modelKinds := []model.Kind{model.KindPassive, model.KindHTTP, model.KindTCP}
	apiKinds := api.Kind("").AllValues()
	if len(modelKinds) != len(apiKinds) {
		t.Fatalf("kind counts differ: model %v, contract %v", modelKinds, apiKinds)
	}
	for i, kind := range modelKinds {
		if !kind.Valid() || string(apiKinds[i]) != string(kind) {
			t.Fatalf("kind %d differs: model %q, contract %q", i, kind, apiKinds[i])
		}
	}

	modelStates := []model.State{model.StateOK, model.StateLate, model.StateFailed}
	apiStates := api.State("").AllValues()
	if len(modelStates) != len(apiStates) {
		t.Fatalf("state counts differ: model %v, contract %v", modelStates, apiStates)
	}
	for i, state := range modelStates {
		if !state.Valid() || string(apiStates[i]) != string(state) {
			t.Fatalf("state %d differs: model %q, contract %q", i, state, apiStates[i])
		}
	}
}
