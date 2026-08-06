package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	handler, err := newHandler(staticReader{snapshot: estateSnapshot()}, fixedClock(generatedAt), "test estate", dashboardFS())
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// the decision-7 blast-radius guarantee, from the status side: this listener
// serves the estate map and the dashboard, and it never answers a report path.
// the matching assertion from the ingest side lives in internal/ingest.
func TestStatusSurfaceNeverServesReportPaths(t *testing.T) {
	handler := newTestHandler(t)

	for _, path := range []string{"/report/nas-snapshot", "/report/", "/report", "/api/report/nas-snapshot"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodPut} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d", method, path, response.Code)
			}
		}
	}
}

func TestUnknownAPIPathsAreNotFound(t *testing.T) {
	handler := newTestHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/estate", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown api path: %d", response.Code)
	}
}

func TestHandlerRejectsAnUnusableReader(t *testing.T) {
	if _, err := NewHandler(nil, fixedClock(generatedAt), "test estate"); err == nil {
		t.Fatal("a nil reader should be rejected")
	}
}

// NewHandler is exercised over the real embedded tree, which is empty under the
// no_ui build tag; the API contract holds either way.
func TestEmbeddedHandlerServesTheStatusAPI(t *testing.T) {
	handler, err := NewHandler(staticReader{snapshot: estateSnapshot()}, fixedClock(generatedAt), "test estate")
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d", response.Code)
	}
}

func TestServerServesUntilItsContextEnds(t *testing.T) {
	server, err := NewServer("127.0.0.1:0", newTestHandler(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- server.Run(ctx) }()

	address := "http://" + server.Addr().String() + "/api/status"
	response, err := http.Get(address)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status: %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if _, err := net.Dial("tcp", server.Addr().String()); err == nil {
		t.Fatal("the listener should be closed after shutdown")
	}
}

func TestServerRequiresAHandler(t *testing.T) {
	if _, err := NewServer("127.0.0.1:0", nil); err == nil {
		t.Fatal("a nil handler should be rejected")
	}
}
