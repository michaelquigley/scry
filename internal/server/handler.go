package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"time"

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

// apiHandler is the one type the generated server dispatches to; each route's
// handler owns its own collaborators.
type apiHandler struct {
	*statusHandler
	*historyHandler
}

// NewHandler builds the status handler over a published snapshot reader and
// the engine's serialized history read. started is the daemon's boot instant.
func NewHandler(reader Reader, history HistoryReader, clock engine.Clock, estate string, started time.Time) (*Handler, error) {
	dist, err := fs.Sub(ui.FS, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard: %w", err)
	}
	return newHandler(reader, history, clock, estate, started, dist)
}

func newHandler(reader Reader, history HistoryReader, clock engine.Clock, estate string, started time.Time, dist fs.FS) (*Handler, error) {
	status, err := newStatusHandler(reader, clock, estate, started)
	if err != nil {
		return nil, err
	}
	recorded, err := newHistoryHandler(history, estate)
	if err != nil {
		return nil, err
	}
	served, err := api.NewServer(apiHandler{statusHandler: status, historyHandler: recorded}, api.WithPathPrefix(apiPrefix))
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
