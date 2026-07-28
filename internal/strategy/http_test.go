package strategy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/scry/internal/model"
)

func TestHTTPDefaultAndExplicitStatusJudgment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthy" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	healthy := NewHTTP(server.URL+"/healthy", nil, false).Evaluate(context.Background())
	if healthy.Status != model.StatusOK {
		t.Fatalf("healthy: %+v", healthy)
	}

	failed := NewHTTP(server.URL+"/failed", nil, false).Evaluate(context.Background())
	if failed.Status != model.StatusFailed || failed.Detail != "http status 503" {
		t.Fatalf("failed: %+v", failed)
	}

	expected := NewHTTP(server.URL+"/failed", []int{http.StatusServiceUnavailable}, false).Evaluate(context.Background())
	if expected.Status != model.StatusOK {
		t.Fatalf("explicit expected status: %+v", expected)
	}
}

func TestHTTPNeverFollowsRedirects(t *testing.T) {
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, destination.URL, http.StatusFound)
	}))
	defer redirect.Close()

	failed := NewHTTP(redirect.URL, nil, false).Evaluate(context.Background())
	if failed.Status != model.StatusFailed || failed.Detail != "http status 302" {
		t.Fatalf("default redirect judgment: %+v", failed)
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("strategy followed the redirect")
	}

	expected := NewHTTP(redirect.URL, []int{http.StatusFound}, false).Evaluate(context.Background())
	if expected.Status != model.StatusOK {
		t.Fatalf("expected redirect: %+v", expected)
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("strategy followed an expected redirect")
	}
}

func TestHTTPHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan model.Result, 1)
	go func() {
		resultCh <- NewHTTP(server.URL, nil, false).Evaluate(ctx)
	}()
	<-started
	cancel()
	result := <-resultCh
	if result.Status != model.StatusFailed || !strings.Contains(result.Detail, "context canceled") {
		t.Fatalf("result: %+v", result)
	}
}

func TestHTTPInsecureIsExplicit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	verified := NewHTTP(server.URL, nil, false).Evaluate(context.Background())
	if verified.Status != model.StatusFailed {
		t.Fatalf("untrusted certificate passed verification: %+v", verified)
	}
	insecure := NewHTTP(server.URL, nil, true).Evaluate(context.Background())
	if insecure.Status != model.StatusOK {
		t.Fatalf("explicit insecure probe: %+v", insecure)
	}
}
