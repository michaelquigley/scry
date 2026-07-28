// Package engine serializes result application and owns scry's runtime records.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

// ErrStopped reports a command submitted after the engine loop has exited.
var ErrStopped = errors.New("engine is stopped")

// Clock is the engine's only source of decision time.
type Clock func() time.Time

// Repository is the whole-state persistence seam.
type Repository interface {
	Load() (state.Snapshot, error)
	Save(state.Snapshot) error
}

// Snapshot is the registry-ordered read model published by the engine.
type Snapshot struct {
	Checks []model.CheckRecord
}

// Clone returns a deep copy safe for a caller to retain and mutate.
func (snapshot Snapshot) Clone() Snapshot {
	clone := Snapshot{Checks: make([]model.CheckRecord, len(snapshot.Checks))}
	for i, entry := range snapshot.Checks {
		entry.Record = entry.Record.Clone()
		clone.Checks[i] = entry
	}
	return clone
}

// Engine owns all mutable records inside Run's serialized command loop.
type Engine struct {
	checks     []model.Check
	registry   map[string]model.Check
	records    map[string]model.Record
	repository Repository
	clock      Clock
	commands   chan command
	done       chan struct{}
	started    atomic.Bool

	snapshotMu sync.RWMutex
	snapshot   Snapshot

	dirty bool
}

// New loads, reconciles, and persists state before returning a runnable engine.
func New(checks []model.Check, repository Repository, clock Clock) (*Engine, error) {
	if repository == nil {
		return nil, fmt.Errorf("state repository is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}

	engine := &Engine{
		checks:     make([]model.Check, len(checks)),
		registry:   make(map[string]model.Check, len(checks)),
		records:    make(map[string]model.Record, len(checks)),
		repository: repository,
		clock:      clock,
		commands:   make(chan command),
		done:       make(chan struct{}),
	}
	copy(engine.checks, checks)
	for _, check := range engine.checks {
		if err := check.Validate(); err != nil {
			return nil, err
		}
		if _, found := engine.registry[check.ID]; found {
			return nil, fmt.Errorf("duplicate check id %q", check.ID)
		}
		engine.registry[check.ID] = check
	}

	loaded, err := repository.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	at := clock()
	for _, check := range engine.checks {
		entry, found := loaded[check.ID]
		if !found || entry.Kind != check.Kind {
			engine.records[check.ID] = model.NewRecord(check, at)
			continue
		}
		engine.records[check.ID] = entry.Record.Clone()
	}

	if err := engine.persist(); err != nil {
		return nil, fmt.Errorf("persist reconciled state: %w", err)
	}
	engine.publish()
	return engine, nil
}

// Run processes commands until cancellation or a fatal persistence failure.
func (engine *Engine) Run(ctx context.Context) error {
	if !engine.started.CompareAndSwap(false, true) {
		return fmt.Errorf("engine already started")
	}
	defer close(engine.done)

	for {
		select {
		case <-ctx.Done():
			if engine.dirty {
				if err := engine.persist(); err != nil {
					return fmt.Errorf("persist state at shutdown: %w", err)
				}
			}
			return nil
		case command := <-engine.commands:
			response, fatal := engine.handle(command)
			command.reply <- response
			if fatal != nil {
				return fatal
			}
		}
	}
}

// ApplyActive submits one active-probe result to the serialized loop.
func (engine *Engine) ApplyActive(ctx context.Context, id string, result model.Result) (*model.Transition, error) {
	response, err := engine.submit(ctx, command{kind: commandActive, id: id, result: result})
	if err != nil {
		return nil, err
	}
	return response.transition, response.err
}

// Report submits one passive report and waits until it is durably recorded.
func (engine *Engine) Report(ctx context.Context, id string, result model.Result) (*model.Transition, error) {
	response, err := engine.submit(ctx, command{kind: commandReport, id: id, result: result})
	if err != nil {
		return nil, err
	}
	return response.transition, response.err
}

// Sweep evaluates every passive window against one clock reading.
func (engine *Engine) Sweep(ctx context.Context) ([]model.Transition, error) {
	response, err := engine.submit(ctx, command{kind: commandSweep})
	if err != nil {
		return nil, err
	}
	return response.transitions, response.err
}

// Flush persists dirty non-transitioning active results.
func (engine *Engine) Flush(ctx context.Context) error {
	response, err := engine.submit(ctx, command{kind: commandFlush})
	if err != nil {
		return err
	}
	return response.err
}

// Snapshot returns the latest copy published by the serialized owner.
func (engine *Engine) Snapshot() Snapshot {
	engine.snapshotMu.RLock()
	defer engine.snapshotMu.RUnlock()
	return engine.snapshot.Clone()
}

type commandKind int

const (
	commandActive commandKind = iota
	commandReport
	commandSweep
	commandFlush
)

type command struct {
	kind   commandKind
	id     string
	result model.Result
	reply  chan response
}

type response struct {
	transition  *model.Transition
	transitions []model.Transition
	err         error
}

func (engine *Engine) submit(ctx context.Context, command command) (response, error) {
	command.reply = make(chan response, 1)
	select {
	case engine.commands <- command:
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-engine.done:
		return response{}, ErrStopped
	}

	select {
	case response := <-command.reply:
		return response, nil
	case <-ctx.Done():
		select {
		case response := <-command.reply:
			return response, nil
		default:
		}
		return response{}, ctx.Err()
	case <-engine.done:
		select {
		case response := <-command.reply:
			return response, nil
		default:
		}
		return response{}, ErrStopped
	}
}

func (engine *Engine) handle(command command) (response, error) {
	switch command.kind {
	case commandActive:
		return engine.handleActive(command.id, command.result)
	case commandReport:
		return engine.handleReport(command.id, command.result)
	case commandSweep:
		return engine.handleSweep()
	case commandFlush:
		return engine.handleFlush()
	default:
		err := fmt.Errorf("unknown engine command %d", command.kind)
		return response{err: err}, nil
	}
}

func (engine *Engine) handleActive(id string, result model.Result) (response, error) {
	check, found := engine.registry[id]
	if !found {
		return response{err: fmt.Errorf("unknown check %q", id)}, nil
	}
	current := engine.records[id]
	change, err := model.ApplyActive(check, current, result, engine.clock())
	if err != nil {
		return response{err: err}, nil
	}
	engine.records[id] = change.Record
	engine.dirty = engine.dirty || change.Dirty
	if change.Transition != nil {
		if err := engine.persist(); err != nil {
			fatal := fmt.Errorf("persist transition for check %q: %w", id, err)
			return response{err: fatal}, fatal
		}
	}
	engine.publish()
	return response{transition: cloneTransition(change.Transition)}, nil
}

func (engine *Engine) handleReport(id string, result model.Result) (response, error) {
	check, found := engine.registry[id]
	if !found {
		return response{err: fmt.Errorf("unknown check %q", id)}, nil
	}
	current := engine.records[id]
	change, err := model.ApplyPassiveReport(check, current, result, engine.clock())
	if err != nil {
		return response{err: err}, nil
	}
	engine.records[id] = change.Record
	engine.dirty = true
	if err := engine.persist(); err != nil {
		fatal := fmt.Errorf("persist report for check %q: %w", id, err)
		return response{err: fatal}, fatal
	}
	engine.publish()
	return response{transition: cloneTransition(change.Transition)}, nil
}

func (engine *Engine) handleSweep() (response, error) {
	at := engine.clock()
	transitions := make([]model.Transition, 0)
	for _, check := range engine.checks {
		if check.Kind != model.KindPassive {
			continue
		}
		change, err := model.ApplyPassiveWindow(check, engine.records[check.ID], at)
		if err != nil {
			return response{err: err}, nil
		}
		if change.Transition == nil {
			continue
		}
		engine.records[check.ID] = change.Record
		engine.dirty = true
		transitions = append(transitions, *cloneTransition(change.Transition))
	}
	if len(transitions) == 0 {
		return response{transitions: transitions}, nil
	}
	if err := engine.persist(); err != nil {
		fatal := fmt.Errorf("persist passive sweep: %w", err)
		return response{err: fatal}, fatal
	}
	engine.publish()
	return response{transitions: transitions}, nil
}

func (engine *Engine) handleFlush() (response, error) {
	if !engine.dirty {
		return response{}, nil
	}
	if err := engine.persist(); err != nil {
		fatal := fmt.Errorf("flush state: %w", err)
		return response{err: fatal}, fatal
	}
	return response{}, nil
}

func (engine *Engine) persist() error {
	if err := engine.repository.Save(engine.persistedSnapshot()); err != nil {
		return err
	}
	engine.dirty = false
	return nil
}

func (engine *Engine) persistedSnapshot() state.Snapshot {
	snapshot := make(state.Snapshot, len(engine.checks))
	for _, check := range engine.checks {
		snapshot[check.ID] = state.Entry{
			Kind:   check.Kind,
			Record: engine.records[check.ID].Clone(),
		}
	}
	return snapshot
}

func (engine *Engine) publish() {
	snapshot := Snapshot{Checks: make([]model.CheckRecord, len(engine.checks))}
	for i, check := range engine.checks {
		snapshot.Checks[i] = model.CheckRecord{
			Check:  check,
			Record: engine.records[check.ID].Clone(),
		}
	}
	engine.snapshotMu.Lock()
	engine.snapshot = snapshot
	engine.snapshotMu.Unlock()
}

func cloneTransition(transition *model.Transition) *model.Transition {
	if transition == nil {
		return nil
	}
	clone := *transition
	if transition.Result != nil {
		result := *transition.Result
		clone.Result = &result
	}
	return &clone
}
