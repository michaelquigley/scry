package server

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/michaelquigley/scry/internal/api"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/ui"
)

const apiPrefix = "/api"

// Handler serves the status API and the embedded dashboard from its private
// mux. it registers no report route; those belong to the ingest listener's
// separate handler tree, and the two are never combined.
type Handler struct {
	mux *http.ServeMux
}

// NewHandler builds the status handler over a published snapshot reader.
func NewHandler(reader Reader, clock engine.Clock, estate string) (*Handler, error) {
	dist, err := fs.Sub(ui.FS, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard: %w", err)
	}
	return newHandler(reader, clock, estate, dist)
}

func newHandler(reader Reader, clock engine.Clock, estate string, dist fs.FS) (*Handler, error) {
	status, err := newStatusHandler(reader, clock, estate)
	if err != nil {
		return nil, err
	}
	served, err := api.NewServer(status, api.WithPathPrefix(apiPrefix))
	if err != nil {
		return nil, fmt.Errorf("create status api server: %w", err)
	}

	handler := &Handler{mux: http.NewServeMux()}
	handler.mux.Handle(apiPrefix+"/", served)
	handler.mux.Handle("/", newAssets(dist))
	return handler, nil
}

// ServeHTTP serves the status-only handler tree.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler.mux.ServeHTTP(writer, request)
}
