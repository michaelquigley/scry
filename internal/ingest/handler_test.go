package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

type reportCall struct {
	id     string
	result model.Result
}

type recordingReporter struct {
	calls []reportCall
	err   error
}

func (reporter *recordingReporter) Report(_ context.Context, id string, result model.Result) (*model.Transition, error) {
	reporter.calls = append(reporter.calls, reportCall{id: id, result: result})
	return nil, reporter.err
}

func newTestHandler(t *testing.T, reporter Reporter) *Handler {
	t.Helper()
	handler, err := NewHandler([]Check{{ID: "job", Token: "secret"}}, reporter)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveReport(t *testing.T, handler http.Handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.Len() != 0 {
		t.Fatalf("response body: %q", response.Body.String())
	}
	return response
}

func TestBareReportsAreOKAndGETIgnoresItsBody(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)

	response := serveReport(t, handler, http.MethodGet, "/report/job", `{"status":"failed"}`, "bearer secret")
	if response.Code != http.StatusNoContent {
		t.Fatalf("get status: %d", response.Code)
	}
	response = serveReport(t, handler, http.MethodPost, "/report/job", "", "Bearer secret")
	if response.Code != http.StatusNoContent {
		t.Fatalf("post status: %d", response.Code)
	}
	if len(reporter.calls) != 2 {
		t.Fatalf("calls: %+v", reporter.calls)
	}
	for _, call := range reporter.calls {
		if call.id != "job" || call.result.Status != model.StatusOK || call.result.Detail != "" {
			t.Fatalf("bare report: %+v", call)
		}
	}
}

func TestReportBodyBindsStrictlyWhileIgnoringUnknownFields(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)
	body := `{"status":"failed","detail":"snapshot exited 2","future":{"attempt":3}}`

	response := serveReport(t, handler, http.MethodPost, "/report/job", body, "BEARER secret")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status: %d", response.Code)
	}
	if len(reporter.calls) != 1 {
		t.Fatalf("calls: %+v", reporter.calls)
	}
	want := model.Result{Status: model.StatusFailed, Detail: "snapshot exited 2"}
	if reporter.calls[0].result != want {
		t.Fatalf("result: %+v, want %+v", reporter.calls[0].result, want)
	}
}

func TestReportStatusDefaultsToOK(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)

	response := serveReport(t, handler, http.MethodPost, "/report/job", `{"detail":"complete"}`, "Bearer secret")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status: %d", response.Code)
	}
	want := model.Result{Status: model.StatusOK, Detail: "complete"}
	if len(reporter.calls) != 1 || reporter.calls[0].result != want {
		t.Fatalf("calls: %+v", reporter.calls)
	}
}

func TestReportDetailTruncatesAtUTF8RuneBoundary(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)
	detail := strings.Repeat("a", 511) + "é"
	body := `{"detail":"` + detail + `"}`

	response := serveReport(t, handler, http.MethodPost, "/report/job", body, "Bearer secret")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status: %d", response.Code)
	}
	got := reporter.calls[0].result.Detail
	if got != strings.Repeat("a", 511) || len(got) > maxDetailBytes {
		t.Fatalf("detail bytes=%d value=%q", len(got), got)
	}
}

func TestReportAuthentication(t *testing.T) {
	accepted := []string{"bearer secret", "Bearer secret", "BEARER secret"}
	for _, authorization := range accepted {
		t.Run("accept "+authorization, func(t *testing.T) {
			reporter := &recordingReporter{}
			handler := newTestHandler(t, reporter)
			response := serveReport(t, handler, http.MethodGet, "/report/job", "", authorization)
			if response.Code != http.StatusNoContent || len(reporter.calls) != 1 {
				t.Fatalf("status=%d calls=%d", response.Code, len(reporter.calls))
			}
		})
	}

	rejected := []string{"", "Basic secret", "Bearer", "Bearer wrong", "Bearer secret extra"}
	for _, authorization := range rejected {
		t.Run("reject "+authorization, func(t *testing.T) {
			reporter := &recordingReporter{}
			handler := newTestHandler(t, reporter)
			response := serveReport(t, handler, http.MethodGet, "/report/job", "", authorization)
			if response.Code != http.StatusUnauthorized || len(reporter.calls) != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, len(reporter.calls))
			}
		})
	}
}

func TestUnknownAndNonPassiveIDsAreIndistinguishableFromBadCredentials(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)
	for _, id := range []string{"unknown", "web"} {
		for _, authorization := range []string{"", "Bearer wrong", "Bearer secret", "Bearer scry-ingest-unknown-check"} {
			response := serveReport(t, handler, http.MethodGet, "/report/"+id, "", authorization)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("id=%q authorization=%q status=%d", id, authorization, response.Code)
			}
		}
	}
	if len(reporter.calls) != 0 {
		t.Fatalf("calls: %+v", reporter.calls)
	}
}

func TestReportRejectsMalformedAndOversizedBodies(t *testing.T) {
	prefix := `{"detail":"`
	suffix := `"}`
	exact := prefix + strings.Repeat("a", maxBodyBytes-len(prefix)-len(suffix)) + suffix
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "exact limit", body: exact, status: http.StatusNoContent},
		{name: "over limit", body: exact + " ", status: http.StatusRequestEntityTooLarge},
		{name: "malformed", body: `{"status":`, status: http.StatusBadRequest},
		{name: "trailing document", body: `{"status":"ok"} {}`, status: http.StatusBadRequest},
		{name: "null", body: `null`, status: http.StatusBadRequest},
		{name: "array", body: `[]`, status: http.StatusBadRequest},
		{name: "invalid status", body: `{"status":"late"}`, status: http.StatusBadRequest},
		{name: "non-string status", body: `{"status":1}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reporter := &recordingReporter{}
			handler := newTestHandler(t, reporter)
			response := serveReport(t, handler, http.MethodPost, "/report/job", test.body, "Bearer secret")
			if response.Code != test.status {
				t.Fatalf("status: %d, want %d", response.Code, test.status)
			}
			wantCalls := 0
			if test.status == http.StatusNoContent {
				wantCalls = 1
			}
			if len(reporter.calls) != wantCalls {
				t.Fatalf("calls: %+v", reporter.calls)
			}
		})
	}
}

func TestReportMethodAndSurfaceIsolation(t *testing.T) {
	reporter := &recordingReporter{}
	handler := newTestHandler(t, reporter)

	method := serveReport(t, handler, http.MethodPut, "/report/job", "", "")
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("method response: status=%d allow=%q", method.Code, method.Header().Get("Allow"))
	}
	for _, path := range []string{"/", "/index.html", "/api/status", "/report", "/report/", "/report/job/child"} {
		response := serveReport(t, handler, http.MethodGet, path, "", "Bearer secret")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d", path, response.Code)
		}
	}
	if len(reporter.calls) != 0 {
		t.Fatalf("calls: %+v", reporter.calls)
	}
}

func TestReportFailureReturnsInternalServerError(t *testing.T) {
	reporter := &recordingReporter{err: errors.New("disk full")}
	handler := newTestHandler(t, reporter)

	response := serveReport(t, handler, http.MethodGet, "/report/job", "", "Bearer secret")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", response.Code)
	}
}

func TestNewHandlerRejectsInvalidRegistry(t *testing.T) {
	reporter := &recordingReporter{}
	tests := []struct {
		name   string
		checks []Check
	}{
		{name: "missing id", checks: []Check{{Token: "one"}}},
		{name: "missing token", checks: []Check{{ID: "one"}}},
		{name: "duplicate id", checks: []Check{{ID: "one", Token: "one"}, {ID: "one", Token: "two"}}},
		{name: "duplicate token", checks: []Check{{ID: "one", Token: "same"}, {ID: "two", Token: "same"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHandler(test.checks, reporter); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := NewHandler(nil, nil); err == nil {
		t.Fatal("missing reporter accepted")
	}
}

type engineRepository struct {
	mu       sync.Mutex
	snapshot state.Snapshot
	saves    int
}

func (repository *engineRepository) Load() (state.Snapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot.Clone(), nil
}

func (repository *engineRepository) Save(snapshot state.Snapshot) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.snapshot = snapshot.Clone()
	repository.saves++
	return nil
}

func (repository *engineRepository) saved() (state.Snapshot, int) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot.Clone(), repository.saves
}

func TestNoContentMeansReportWasDurablyRecorded(t *testing.T) {
	at := time.Date(2026, 7, 27, 22, 0, 0, 0, time.Local)
	repository := &engineRepository{}
	check := model.Check{
		ID:          "job",
		Name:        "job",
		Kind:        model.KindPassive,
		Period:      24 * time.Hour,
		Grace:       2 * time.Hour,
		HardenAfter: 3,
	}
	stateEngine, err := engine.New([]model.Check{check}, repository, func() time.Time { return at }, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, bootSaves := repository.saved()
	ctx, cancel := context.WithCancel(context.Background())
	engineErr := make(chan error, 1)
	go func() {
		engineErr <- stateEngine.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-engineErr; err != nil {
			t.Errorf("engine run: %v", err)
		}
	})

	handler := newTestHandler(t, stateEngine)
	response := serveReport(
		t,
		handler,
		http.MethodPost,
		"/report/job",
		`{"status":"failed","detail":"exit 2"}`,
		"bearer secret",
	)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status: %d", response.Code)
	}
	persisted, saves := repository.saved()
	if saves != bootSaves+1 {
		t.Fatalf("saves: %d, want %d", saves, bootSaves+1)
	}
	record := persisted["job"].Record
	if record.State != model.StateFailed || record.LastResult == nil || record.LastResult.Detail != "exit 2" {
		t.Fatalf("persisted record: %+v", record)
	}
}
