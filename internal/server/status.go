// Package server owns scry's status API and embedded dashboard HTTP surface.
package server

import (
	"context"
	"fmt"

	"github.com/michaelquigley/scry/internal/api"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/model"
)

// Reader publishes the engine's current registry-ordered snapshot.
type Reader interface {
	Snapshot() engine.Snapshot
}

// statusHandler renders the single walk of the status model.
type statusHandler struct {
	reader Reader
	clock  engine.Clock
}

func newStatusHandler(reader Reader, clock engine.Clock) (*statusHandler, error) {
	if reader == nil {
		return nil, fmt.Errorf("snapshot reader is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	return &statusHandler{reader: reader, clock: clock}, nil
}

// GetStatus walks the published snapshot into the contract's status document.
func (handler *statusHandler) GetStatus(_ context.Context) (*api.Status, error) {
	snapshot := handler.reader.Snapshot()
	document := &api.Status{
		Generated: handler.clock().UTC(),
		Checks:    make([]api.Check, len(snapshot.Checks)),
	}
	for i, entry := range snapshot.Checks {
		document.Checks[i] = describeCheck(entry)
		switch entry.Record.State {
		case model.StateOK:
			document.Rollup.Ok++
		case model.StateLate:
			document.Rollup.Late++
		case model.StateFailed:
			document.Rollup.Failed++
		}
	}
	return document, nil
}

// describeCheck renders one runtime record as its contract shape. the kind and
// state vocabularies convert directly because the contract enumerates exactly
// the model's values; TestContractEnumerationsMatchTheModel pins that equality.
func describeCheck(entry model.CheckRecord) api.Check {
	described := api.Check{
		ID:             entry.Check.ID,
		Name:           entry.Check.Name,
		Kind:           api.Kind(entry.Check.Kind),
		State:          api.State(entry.Record.State),
		Since:          entry.Record.Since.UTC(),
		LastTransition: api.NilDateTime{Null: true},
		LastSeen:       api.NilDateTime{Null: true},
		Detail:         api.NilString{Null: true},
	}
	if entry.Record.LastTransition != nil {
		described.LastTransition = api.NewNilDateTime(entry.Record.LastTransition.UTC())
	}
	if entry.Record.LastSeen != nil {
		described.LastSeen = api.NewNilDateTime(entry.Record.LastSeen.UTC())
	}
	if entry.Record.LastResult != nil {
		described.Detail = api.NewNilString(entry.Record.LastResult.Detail)
	}
	return described
}
