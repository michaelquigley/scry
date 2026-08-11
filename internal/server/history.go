package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/michaelquigley/scry/internal/api"
	"github.com/michaelquigley/scry/internal/engine"
	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/model"
)

// HistoryReader serves one consistent cut of the ledger and the records from
// inside the engine's serialized loop. it needs no published snapshot: the
// records arrive inside the cut.
type HistoryReader interface {
	HistoryView(ctx context.Context, from, to *time.Time) (engine.HistoryView, error)
}

// historyHandler renders the recorded history document. it holds no clock —
// the bounds and the watermark are resolved against the one reading the
// serialized command already took.
type historyHandler struct {
	reader HistoryReader
	estate string
}

func newHistoryHandler(reader HistoryReader, estate string) (*historyHandler, error) {
	if reader == nil {
		return nil, fmt.Errorf("history reader is required")
	}
	if estate == "" {
		return nil, fmt.Errorf("estate name is required")
	}
	return &historyHandler{reader: reader, estate: estate}, nil
}

// GetHistory passes the request's optional bounds straight into the serialized
// read, where defaulting and validation both happen against one clock reading,
// and renders the resulting cut.
func (handler *historyHandler) GetHistory(ctx context.Context, params api.GetHistoryParams) (api.GetHistoryRes, error) {
	var from, to *time.Time
	if value, ok := params.From.Get(); ok {
		from = &value
	}
	if value, ok := params.To.Get(); ok {
		to = &value
	}

	view, err := handler.reader.HistoryView(ctx, from, to)
	if err != nil {
		if errors.Is(err, engine.ErrInvalidHistoryWindow) {
			return &api.GetHistoryBadRequest{Message: err.Error()}, nil
		}
		// a ledger the daemon cannot read fails the request, not the daemon.
		return &api.GetHistoryInternalServerError{Message: err.Error()}, nil
	}
	return describeHistory(handler.estate, view), nil
}

// describeHistory renders one cut as the contract's history document. it
// speaks only for the configured registry: ledger events under names no longer
// configured never enter, and boot has already warned about them.
func describeHistory(estate string, view engine.HistoryView) *api.History {
	document := &api.History{
		Estate:         estate,
		Generated:      view.Generated.UTC(),
		From:           view.From.UTC(),
		To:             view.To.UTC(),
		WatchingAtFrom: view.Window.WatchingAtFrom,
		Checks:         make([]api.CheckHistory, len(view.Checks)),
		Daemon:         make([]api.LifecycleEvent, 0),
	}
	transitions := groupTransitions(view.Window.Events)
	for _, event := range view.Window.Events {
		if event.Type == history.EventTransition {
			continue
		}
		document.Daemon = append(document.Daemon, api.LifecycleEvent{
			Ts:    event.TS.UTC(),
			Event: api.LifecycleEventEvent(event.Type),
		})
	}
	for i, entry := range view.Checks {
		document.Checks[i] = describeCheckHistory(entry, view, transitions[entry.Check.ID])
	}
	return document
}

// describeCheckHistory resolves one check's two bounds. the ledger answers
// first; where events cannot speak, the live record does, but only as far back
// as the state it is currently in actually reaches.
func describeCheckHistory(entry model.CheckRecord, view engine.HistoryView, events []api.TransitionEvent) api.CheckHistory {
	described := api.CheckHistory{
		ID:          entry.Check.ID,
		Kind:        api.Kind(entry.Check.Kind),
		StateAtFrom: api.NilState{Null: true},
		StateAtTo:   api.NilState{Null: true},
		Since:       api.NilDateTime{Null: true},
		Events:      events,
	}
	if described.Events == nil {
		described.Events = []api.TransitionEvent{}
	}

	if state, found := view.Window.StateAtFrom[entry.Check.ID]; found {
		described.StateAtFrom = api.NewNilState(api.State(state))
	} else if !entry.Record.Since.After(view.From) {
		described.StateAtFrom = api.NewNilState(api.State(entry.Record.State))
	}

	// the tail is a pair, resolved together and null together.
	if tail, found := view.Window.TailAtTo[entry.Check.ID]; found {
		described.StateAtTo = api.NewNilState(api.State(tail.State))
		described.Since = api.NewNilDateTime(tail.Since.UTC())
	} else if !entry.Record.Since.After(view.To) {
		described.StateAtTo = api.NewNilState(api.State(entry.Record.State))
		described.Since = api.NewNilDateTime(entry.Record.Since.UTC())
	}
	return described
}

// groupTransitions renders the window's transition events by check id, keeping
// each check's events in the ledger's ascending order.
func groupTransitions(events []history.Event) map[string][]api.TransitionEvent {
	grouped := make(map[string][]api.TransitionEvent)
	for _, event := range events {
		if event.Type != history.EventTransition {
			continue
		}
		described := api.TransitionEvent{
			Ts:        event.TS.UTC(),
			Kind:      api.Kind(event.Kind),
			From:      api.State(event.From),
			To:        api.State(event.To),
			PrevSince: event.PrevSince.UTC(),
			Detail:    api.NilString{Null: true},
		}
		if event.Detail != nil {
			described.Detail = api.NewNilString(*event.Detail)
		}
		grouped[event.Check] = append(grouped[event.Check], described)
	}
	return grouped
}
