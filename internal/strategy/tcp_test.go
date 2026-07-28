package strategy

import (
	"context"
	"net"
	"testing"

	"github.com/michaelquigley/scry/internal/model"
)

func TestTCPSuccessAndRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			err = connection.Close()
		}
		accepted <- err
	}()

	result := NewTCP(listener.Addr().String()).Evaluate(context.Background())
	if result.Status != model.StatusOK {
		t.Fatalf("open listener: %+v", result)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	result = NewTCP(listener.Addr().String()).Evaluate(context.Background())
	if result.Status != model.StatusFailed || result.Detail == "" {
		t.Fatalf("closed listener: %+v", result)
	}
}

func TestTCPCanceledContextIsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewTCP("203.0.113.1:81").Evaluate(ctx)
	if result.Status != model.StatusFailed || result.Detail == "" {
		t.Fatalf("result: %+v", result)
	}
}
