package ingest

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestServerRunsOnItsBoundListenerAndStopsWithContext(t *testing.T) {
	requested := make(chan struct{})
	server, err := NewServer("127.0.0.1:0", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(requested)
		writer.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- server.Run(ctx)
	}()

	response, err := http.Get("http://" + server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	<-requested
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status: %d", response.StatusCode)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestNewServerReportsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if _, err := NewServer(listener.Addr().String(), http.NotFoundHandler()); err == nil {
		t.Fatal("duplicate bind succeeded")
	}
	if _, err := NewServer("127.0.0.1:0", nil); err == nil {
		t.Fatal("nil handler accepted")
	}
}

func TestNewServerRejectsWildcardBind(t *testing.T) {
	if _, err := NewServer("0.0.0.0:0", http.NotFoundHandler()); err == nil {
		t.Fatal("wildcard bind accepted")
	}
}
