// Package engine serializes result application and owns scry's runtime records.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

// defaultHistoryWindow is the span an omitted from bound resolves to, back
// from the resolved to.
const defaultHistoryWindow = 90 * 24 * time.Hour

// ErrStopped reports a command submitted after the engine loop has exited.
var ErrStopped = errors.New("engine is stopped")

// ErrInvalidHistoryWindow reports bounds the caller must fix. it is the one
// history reply a transport renders as a client error; every other reply is
// the daemon's own failure.
var ErrInvalidHistoryWindow = errors.New("invalid history window")

// Clock is the engine's only source of decision time.
type Clock func() time.Time

// Repository is the whole-state persistence seam. the saved stamp travels
// through the seam so the store never reads a clock of its own.
type Repository interface {
	Load() (state.Snapshot, time.Time, error)
	Save(state.Snapshot, time.Time) error
}

// Ledger is the append-only history seam. starting a ledger belongs to Boot
// alone; the engine never opens one mid-run.
type Ledger interface {
	Boot(at, lastSaved time.Time, configured map[string]struct{}) error
	AppendTransition(model.Transition) error
	AppendStop(at time.Time) error
	Window(from, to time.Time) (history.Window, error)
}

// TransitionQueue accepts announced transitions without delivery backpressure.
type TransitionQueue interface {
	Enqueue(model.Transition)
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

// HistoryView is one consistent cut across the ledger and the records: no
// document can miss an event older than its own Generated stamp.
type HistoryView struct {
	From      time.Time
	To        time.Time
	Generated time.Time
	Window    history.Window
	Checks    []model.CheckRecord
}

// Engine owns all mutable records inside Run's serialized command loop.
type Engine struct {
	checks      []model.Check
	registry    map[string]model.Check
	records     map[string]model.Record
	repository  Repository
	ledger      Ledger
	clock       Clock
	transitions TransitionQueue
	commands    chan command
	done        chan struct{}
	started     time.Time
	running     atomic.Bool

	snapshotMu sync.RWMutex
	snapshot   Snapshot

	dirty bool
}

// New loads, reconciles, boots history, and persists state before returning a
// runnable engine.
func New(checks []model.Check, repository Repository, ledger Ledger, clock Clock, transitions TransitionQueue) (*Engine, error) {
	if repository == nil {
		return nil, fmt.Errorf("state repository is required")
	}
	if ledger == nil {
		return nil, fmt.Errorf("history ledger is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}

	engine := &Engine{
		checks:      make([]model.Check, len(checks)),
		registry:    make(map[string]model.Check, len(checks)),
		records:     make(map[string]model.Record, len(checks)),
		repository:  repository,
		ledger:      ledger,
		clock:       clock,
		transitions: transitions,
		commands:    make(chan command),
		done:        make(chan struct{}),
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

	loaded, lastSaved, err := repository.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	at := clock()
	engine.started = at
	for _, check := range engine.checks {
		entry, found := loaded[check.ID]
		if !found || entry.Kind != check.Kind {
			engine.records[check.ID] = model.NewRecord(check, at)
			continue
		}
		engine.records[check.ID] = entry.Record.Clone()
	}

	// history boots before the reconciliation save, so a ledger the daemon
	// cannot read or write stops it before it has rewritten the state file.
	configured := make(map[string]struct{}, len(engine.registry))
	for id := range engine.registry {
		configured[id] = struct{}{}
	}
	if err := ledger.Boot(at, lastSaved, configured); err != nil {
		return nil, fmt.Errorf("boot history: %w", err)
	}

	if err := engine.persist(at); err != nil {
		return nil, fmt.Errorf("persist reconciled state: %w", err)
	}
	engine.publish()
	return engine, nil
}

// Run processes commands until cancellation or a fatal persistence failure.
func (engine *Engine) Run(ctx context.Context) error {
	if !engine.running.CompareAndSwap(false, true) {
		return fmt.Errorf("engine already started")
	}
	defer close(engine.done)

	for {
		select {
		case <-ctx.Done():
			at := engine.clock()
			if err := engine.persist(at); err != nil {
				return fmt.Errorf("persist state at shutdown: %w", err)
			}
			if err := engine.ledger.AppendStop(at); err != nil {
				return fmt.Errorf("record daemon stop: %w", err)
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

// Flush persists the whole snapshot whether or not anything changed, so a
// quiet estate still advances the saved stamp history reads as liveness.
func (engine *Engine) Flush(ctx context.Context) error {
	response, err := engine.submit(ctx, command{kind: commandFlush})
	if err != nil {
		return err
	}
	return response.err
}

// Started reports the daemon's boot instant, the same reading that stamps the
// ledger's start event. it is fixed for the life of the engine.
func (engine *Engine) Started() time.Time {
	return engine.started
}

// Snapshot returns the latest copy published by the serialized owner.
func (engine *Engine) Snapshot() Snapshot {
	engine.snapshotMu.RLock()
	defer engine.snapshotMu.RUnlock()
	return engine.snapshot.Clone()
}

// HistoryView reads the ledger window and the records as one cut inside the
// serialized loop. omitted bounds resolve against the same clock reading the
// view is stamped with, so the default window's edge and the watermark cannot
// diverge.
func (engine *Engine) HistoryView(ctx context.Context, from, to *time.Time) (HistoryView, error) {
	response, err := engine.submit(ctx, command{kind: commandHistory, from: from, to: to})
	if err != nil {
		return HistoryView{}, err
	}
	if response.err != nil {
		return HistoryView{}, response.err
	}
	return response.view, nil
}

type commandKind int

const (
	commandActive commandKind = iota
	commandReport
	commandSweep
	commandFlush
	commandHistory
)

type command struct {
	kind   commandKind
	id     string
	result model.Result
	from   *time.Time
	to     *time.Time
	reply  chan response
}

type response struct {
	transition  *model.Transition
	transitions []model.Transition
	view        HistoryView
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
	case commandHistory:
		return engine.handleHistory(command.from, command.to)
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
	at := engine.clock()
	current := engine.records[id]
	change, err := model.ApplyActive(check, current, result, at)
	if err != nil {
		return response{err: err}, nil
	}
	engine.records[id] = change.Record
	engine.dirty = engine.dirty || change.Dirty
	if change.Transition != nil {
		if err := engine.persist(at); err != nil {
			fatal := fmt.Errorf("persist transition for check %q: %w", id, err)
			return response{err: fatal}, fatal
		}
		if err := engine.record(*change.Transition); err != nil {
			return response{err: err}, err
		}
	}
	engine.enqueue(change.Transition)
	engine.publish()
	return response{transition: cloneTransition(change.Transition)}, nil
}

func (engine *Engine) handleReport(id string, result model.Result) (response, error) {
	check, found := engine.registry[id]
	if !found {
		return response{err: fmt.Errorf("unknown check %q", id)}, nil
	}
	at := engine.clock()
	current := engine.records[id]
	change, err := model.ApplyPassiveReport(check, current, result, at)
	if err != nil {
		return response{err: err}, nil
	}
	engine.records[id] = change.Record
	engine.dirty = true
	if err := engine.persist(at); err != nil {
		fatal := fmt.Errorf("persist report for check %q: %w", id, err)
		return response{err: fatal}, fatal
	}
	if change.Transition != nil {
		if err := engine.record(*change.Transition); err != nil {
			return response{err: err}, err
		}
	}
	engine.enqueue(change.Transition)
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
	if err := engine.persist(at); err != nil {
		fatal := fmt.Errorf("persist passive sweep: %w", err)
		return response{err: fatal}, fatal
	}
	// one persist covers the whole sweep, and every append lands before any
	// notification fans out, the same order the single-transition handlers
	// keep.
	for i := range transitions {
		if err := engine.record(transitions[i]); err != nil {
			return response{err: err}, err
		}
	}
	for i := range transitions {
		engine.enqueue(&transitions[i])
	}
	engine.publish()
	return response{transitions: transitions}, nil
}

// handleFlush persists unconditionally: the periodic flush is what keeps the
// saved stamp a live bound on the daemon's death, so a quiet estate has to
// advance it too.
func (engine *Engine) handleFlush() (response, error) {
	if err := engine.persist(engine.clock()); err != nil {
		fatal := fmt.Errorf("flush state: %w", err)
		return response{err: fatal}, fatal
	}
	return response{}, nil
}

func (engine *Engine) handleHistory(from, to *time.Time) (response, error) {
	generated := engine.clock()
	resolvedTo := generated
	if to != nil {
		resolvedTo = *to
	}
	resolvedFrom := resolvedTo.Add(-defaultHistoryWindow)
	if from != nil {
		resolvedFrom = *from
	}
	// validation runs once, on the fully resolved pair, so no one-sided
	// request can reach the ledger inverted.
	if !resolvedFrom.Before(resolvedTo) {
		return response{err: fmt.Errorf("%w: from must precede to", ErrInvalidHistoryWindow)}, nil
	}
	if resolvedTo.After(generated) {
		return response{err: fmt.Errorf("%w: to must not be in the future", ErrInvalidHistoryWindow)}, nil
	}

	window, err := engine.ledger.Window(resolvedFrom, resolvedTo)
	if err != nil {
		// a post-boot read fails the request, never the daemon.
		return response{err: fmt.Errorf("read history: %w", err)}, nil
	}
	return response{view: HistoryView{
		From:      resolvedFrom,
		To:        resolvedTo,
		Generated: generated,
		Window:    window,
		Checks:    engine.checkRecords(),
	}}, nil
}

func (engine *Engine) persist(at time.Time) error {
	if err := engine.repository.Save(engine.persistedSnapshot(), at); err != nil {
		return err
	}
	engine.dirty = false
	return nil
}

// record appends one transition to the ledger. the state save leads and the
// append follows, so a crash between them loses at most the final transition
// from history; a failed append is fatal exactly as a failed save is.
func (engine *Engine) record(transition model.Transition) error {
	if err := engine.ledger.AppendTransition(transition); err != nil {
		return fmt.Errorf("record transition for check %q: %w", transition.CheckID, err)
	}
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

// checkRecords returns a registry-ordered deep copy of the current records.
func (engine *Engine) checkRecords() []model.CheckRecord {
	records := make([]model.CheckRecord, len(engine.checks))
	for i, check := range engine.checks {
		records[i] = model.CheckRecord{
			Check:  check,
			Record: engine.records[check.ID].Clone(),
		}
	}
	return records
}

func (engine *Engine) publish() {
	snapshot := Snapshot{Checks: engine.checkRecords()}
	engine.snapshotMu.Lock()
	engine.snapshot = snapshot
	engine.snapshotMu.Unlock()
}

func (engine *Engine) enqueue(transition *model.Transition) {
	if transition == nil || !transition.Announce || engine.transitions == nil {
		return
	}
	engine.transitions.Enqueue(*cloneTransition(transition))
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
