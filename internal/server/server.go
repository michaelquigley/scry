package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 5 * time.Second

// Server owns the status listener and no other HTTP surface. it binds the LAN
// address by design; the report surface is a separate loopback listener.
type Server struct {
	listener net.Listener
	http     *http.Server
}

// NewServer binds address and returns a ready status server.
func NewServer(address string, handler http.Handler) (*Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("status handler is required")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", address, err)
	}
	return &Server{
		listener: listener,
		http: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			IdleTimeout:       time.Minute,
		},
	}, nil
}

// Addr returns the bound listener address.
func (server *Server) Addr() net.Addr {
	return server.listener.Addr()
}

// Run serves until ctx ends or the listener fails.
func (server *Server) Run(ctx context.Context) error {
	served := make(chan error, 1)
	go func() {
		served <- server.http.Serve(server.listener)
	}()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.http.Shutdown(shutdownCtx); err != nil {
			_ = server.http.Close()
			<-served
			return fmt.Errorf("shut down status listener: %w", err)
		}
		err := <-served
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
