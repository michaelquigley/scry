package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/history"
	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

var engineEpoch = time.Date(2026, 7, 27, 18, 0, 0, 0, time.Local)

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	return clock.now
}

// callLog records persistence and ledger calls in the order the serialized
// loop made them, so ordering claims are asserted rather than assumed.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (log *callLog) record(call string) {
	if log == nil {
		return
	}
	log.mu.Lock()
	log.calls = append(log.calls, call)
	log.mu.Unlock()
}

func (log *callLog) recorded() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.calls...)
}

type memoryRepository struct {
	mu       sync.Mutex
	log      *callLog
	snapshot state.Snapshot
	loaded   time.Time
	savedAt  time.Time
	loadErr  error
	saveErr  error
	saves    int
}

func (repository *memoryRepository) Load() (state.Snapshot, time.Time, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.loadErr != nil {
		return nil, time.Time{}, repository.loadErr
	}
	return repository.snapshot.Clone(), repository.loaded, nil
}

func (repository *memoryRepository) Save(snapshot state.Snapshot, at time.Time) error {
	repository.mu.Lock()
	if repository.saveErr != nil {
		err := repository.saveErr
		repository.mu.Unlock()
		return err
	}
	repository.snapshot = snapshot.Clone()
	repository.savedAt = at
	repository.saves++
	log := repository.log
	repository.mu.Unlock()
	log.record("save")
	return nil
}

func (repository *memoryRepository) setSaveError(err error) {
	repository.mu.Lock()
	repository.saveErr = err
	repository.mu.Unlock()
}

func (repository *memoryRepository) saved() (state.Snapshot, int) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot.Clone(), repository.saves
}

func (repository *memoryRepository) savedStamp() time.Time {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.savedAt
}

type fakeLedger struct {
	mu          sync.Mutex
	log         *callLog
	bootAt      time.Time
	bootSaved   time.Time
	bootIDs     map[string]struct{}
	bootErr     error
	transitions []model.Transition
	stops       []time.Time
	appendErr   error
	window      history.Window
	windowErr   error
}

func (ledger *fakeLedger) Boot(at, lastSaved time.Time, configured map[string]struct{}) error {
	ledger.mu.Lock()
	ledger.bootAt = at
	ledger.bootSaved = lastSaved
	ledger.bootIDs = configured
	err := ledger.bootErr
	log := ledger.log
	ledger.mu.Unlock()
	log.record("boot")
	return err
}

func (ledger *fakeLedger) AppendTransition(transition model.Transition) error {
	ledger.mu.Lock()
	if ledger.appendErr != nil {
		err := ledger.appendErr
		ledger.mu.Unlock()
		return err
	}
	ledger.transitions = append(ledger.transitions, transition)
	log := ledger.log
	ledger.mu.Unlock()
	log.record("append:" + transition.CheckID)
	return nil
}

func (ledger *fakeLedger) AppendStop(at time.Time) error {
	ledger.mu.Lock()
	ledger.stops = append(ledger.stops, at)
	log := ledger.log
	ledger.mu.Unlock()
	log.record("stop")
	return nil
}

func (ledger *fakeLedger) Window(from, to time.Time) (history.Window, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.window, ledger.windowErr
}

func (ledger *fakeLedger) appended() []model.Transition {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return append([]model.Transition(nil), ledger.transitions...)
}

func (ledger *fakeLedger) stopped() []time.Time {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return append([]time.Time(nil), ledger.stops...)
}

func (ledger *fakeLedger) booted() (time.Time, time.Time, map[string]struct{}) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.bootAt, ledger.bootSaved, ledger.bootIDs
}

type recordedEnqueue struct {
	transition model.Transition
	saves      int
}

type recordingTransitionQueue struct {
	mu         sync.Mutex
	repository *memoryRepository
	enqueues   []recordedEnqueue
}

func (queue *recordingTransitionQueue) Enqueue(transition model.Transition) {
	_, saves := queue.repository.saved()
	queue.mu.Lock()
	queue.enqueues = append(queue.enqueues, recordedEnqueue{
		transition: transition,
		saves:      saves,
	})
	queue.mu.Unlock()
}

func (queue *recordingTransitionQueue) recorded() []recordedEnqueue {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]recordedEnqueue(nil), queue.enqueues...)
}

func engineActiveCheck(id string) model.Check {
	return model.Check{
		ID:        id,
		Name:      id,
		Kind:      model.KindHTTP,
		FailAfter: 3,
	}
}

func enginePassiveCheck(id string) model.Check {
	return model.Check{
		ID:          id,
		Name:        id,
		Kind:        model.KindPassive,
		Period:      24 * time.Hour,
		Grace:       2 * time.Hour,
		HardenAfter: 3,
	}
}

func startEngine(t *testing.T, engine *Engine) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("engine run: %v", err)
		}
	})
	return ctx
}

func snapshotRecord(t *testing.T, snapshot Snapshot, id string) model.Record {
	t.Helper()
	for _, entry := range snapshot.Checks {
		if entry.Check.ID == id {
			return entry.Record
		}
	}
	t.Fatalf("missing check %q", id)
	return model.Record{}
}

func TestNewReconcilesAndPersistsBeforeReturning(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	oldSince := engineEpoch.Add(-24 * time.Hour)
	oldTransition := oldSince
	repository := &memoryRepository{
		snapshot: state.Snapshot{
			"keep": {
				Kind: model.KindHTTP,
				Record: model.Record{
					State:            model.StateFailed,
					Since:            oldSince,
					LastTransition:   &oldTransition,
					LastResult:       &model.Result{Status: model.StatusFailed, Detail: "down"},
					ConsecutiveFails: 3,
				},
			},
			"kind-change": {
				Kind: model.KindHTTP,
				Record: model.Record{
					State: model.StateOK,
					Since: oldSince,
				},
			},
			"removed": {
				Kind: model.KindTCP,
				Record: model.Record{
					State: model.StateOK,
					Since: oldSince,
				},
			},
		},
	}
	checks := []model.Check{
		engineActiveCheck("keep"),
		enginePassiveCheck("kind-change"),
		enginePassiveCheck("new"),
	}

	engine, err := New(checks, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, saves := repository.saved()
	if saves != 1 {
		t.Fatalf("boot saves: %d, want 1", saves)
	}
	snapshot := engine.Snapshot()
	if len(snapshot.Checks) != 3 {
		t.Fatalf("checks: %d", len(snapshot.Checks))
	}

	kept := snapshotRecord(t, snapshot, "keep")
	if kept.State != model.StateFailed || !kept.Since.Equal(oldSince) {
		t.Fatalf("kept state: %+v", kept)
	}
	for _, id := range []string{"kind-change", "new"} {
		record := snapshotRecord(t, snapshot, id)
		if record.State != model.StateOK || !record.Since.Equal(engineEpoch) {
			t.Fatalf("fresh %s: %+v", id, record)
		}
		if record.LastSeen == nil || !record.LastSeen.Equal(engineEpoch) {
			t.Fatalf("fresh %s last seen: %v", id, record.LastSeen)
		}
		if record.LastTransition != nil {
			t.Fatalf("registration became a transition: %v", record.LastTransition)
		}
	}
	persisted, _ := repository.saved()
	if _, found := persisted["removed"]; found {
		t.Fatal("configured-away check survived reconciliation")
	}

	clock.now = engineEpoch.Add(6 * time.Hour)
	restarted, err := New(checks, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kind-change", "new"} {
		record := snapshotRecord(t, restarted.Snapshot(), id)
		if !record.Since.Equal(engineEpoch) || record.LastSeen == nil || !record.LastSeen.Equal(engineEpoch) {
			t.Fatalf("restart shifted %s baseline: %+v", id, record)
		}
	}
}

func TestRestartResumesWithoutRefire(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	check := enginePassiveCheck("job")

	first, err := New([]model.Check{check}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, first)
	clock.now = engineEpoch.Add(time.Hour)
	transition, err := first.Report(ctx, "job", model.Result{Status: model.StatusFailed, Detail: "exit 2"})
	if err != nil {
		t.Fatal(err)
	}
	if transition == nil || transition.To != model.StateFailed {
		t.Fatalf("transition: %+v", transition)
	}

	restarted, err := New([]model.Check{check}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshotRecord(t, restarted.Snapshot(), "job")
	if record.State != model.StateFailed || record.LastResult == nil || record.LastResult.Detail != "exit 2" {
		t.Fatalf("restarted record: %+v", record)
	}
}

func TestActiveNonTransitionPersistsOnFlushAndRestart(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	check := engineActiveCheck("web")
	first, err := New([]model.Check{check}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, first)

	clock.now = engineEpoch.Add(time.Minute)
	if _, err := first.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "one"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(2 * time.Minute)
	transition, err := first.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if transition != nil {
		t.Fatalf("second failure transitioned: %+v", transition)
	}
	if err := first.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	restarted, err := New([]model.Check{check}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshotRecord(t, restarted.Snapshot(), "web")
	if record.State != model.StateLate || record.ConsecutiveFails != 2 {
		t.Fatalf("restarted counter: %+v", record)
	}
	if record.LastResult == nil || record.LastResult.Detail != "two" {
		t.Fatalf("restarted result: %+v", record.LastResult)
	}
}

func TestRestartedThresholdChangesHardenButNeverSoften(t *testing.T) {
	cases := []struct {
		name       string
		startState model.State
		startCount int
		failAfter  int
		wantState  model.State
		wantChange bool
	}{
		{
			name:       "lowered threshold hardens",
			startState: model.StateLate,
			startCount: 4,
			failAfter:  3,
			wantState:  model.StateFailed,
			wantChange: true,
		},
		{
			name:       "raised threshold keeps failed sticky",
			startState: model.StateFailed,
			startCount: 3,
			failAfter:  5,
			wantState:  model.StateFailed,
			wantChange: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			since := engineEpoch.Add(-time.Hour)
			repository := &memoryRepository{
				snapshot: state.Snapshot{
					"web": {
						Kind: model.KindHTTP,
						Record: model.Record{
							State:            test.startState,
							Since:            since,
							LastTransition:   &since,
							LastResult:       &model.Result{Status: model.StatusFailed, Detail: "down"},
							ConsecutiveFails: test.startCount,
						},
					},
				},
			}
			clock := &fakeClock{now: engineEpoch}
			check := engineActiveCheck("web")
			check.FailAfter = test.failAfter
			engine, err := New([]model.Check{check}, repository, &fakeLedger{}, clock.Now, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx := startEngine(t, engine)
			transition, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "still down"})
			if err != nil {
				t.Fatal(err)
			}
			if (transition != nil) != test.wantChange {
				t.Fatalf("transition: %+v, want change %v", transition, test.wantChange)
			}
			record := snapshotRecord(t, engine.Snapshot(), "web")
			if record.State != test.wantState {
				t.Fatalf("state: %s, want %s", record.State, test.wantState)
			}
		})
	}
}

func TestReportIsPersistedBeforeReply(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	engine, err := New([]model.Check{enginePassiveCheck("job")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)
	_, bootSaves := repository.saved()

	clock.now = engineEpoch.Add(time.Hour)
	transition, err := engine.Report(ctx, "job", model.Result{Status: model.StatusOK, Detail: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	if transition != nil {
		t.Fatalf("ok report transitioned: %+v", transition)
	}
	persisted, saves := repository.saved()
	if saves != bootSaves+1 {
		t.Fatalf("report saves: %d, want %d", saves, bootSaves+1)
	}
	record := persisted["job"].Record
	if record.LastSeen == nil || !record.LastSeen.Equal(clock.now) {
		t.Fatalf("persisted last seen: %v", record.LastSeen)
	}
	if record.LastResult == nil || record.LastResult.Detail != "complete" {
		t.Fatalf("persisted result: %+v", record.LastResult)
	}
}

func TestSweepCatchupAnnouncesOnce(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	engine, err := New([]model.Check{enginePassiveCheck("job")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	clock.now = engineEpoch.Add(33 * time.Hour)
	transitions, err := engine.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || transitions[0].From != model.StateOK || transitions[0].To != model.StateFailed || !transitions[0].Announce {
		t.Fatalf("catchup transitions: %+v", transitions)
	}
	again, err := engine.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("repeat sweep re-fired: %+v", again)
	}
}

func TestEnginePersistsBeforeEnqueueAndSkipsSilentTransitions(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	queue := &recordingTransitionQueue{repository: repository}
	engine, err := New(
		[]model.Check{
			engineActiveCheck("web"),
			enginePassiveCheck("reported-job"),
			enginePassiveCheck("silent-job"),
		},
		repository,
		&fakeLedger{},
		clock.Now,
		queue,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	clock.now = engineEpoch.Add(time.Minute)
	transition, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if transition == nil || transition.To != model.StateLate || transition.Announce {
		t.Fatalf("first active failure: %+v", transition)
	}
	if enqueues := queue.recorded(); len(enqueues) != 0 {
		t.Fatalf("silent active transition enqueued: %+v", enqueues)
	}

	clock.now = engineEpoch.Add(2 * time.Minute)
	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "two"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(3 * time.Minute)
	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "three"}); err != nil {
		t.Fatal(err)
	}

	clock.now = engineEpoch.Add(4 * time.Minute)
	if _, err := engine.Report(ctx, "reported-job", model.Result{Status: model.StatusFailed, Detail: "exit 2"}); err != nil {
		t.Fatal(err)
	}

	clock.now = engineEpoch.Add(33 * time.Hour)
	if _, err := engine.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	enqueues := queue.recorded()
	if len(enqueues) != 3 {
		t.Fatalf("enqueues: %+v", enqueues)
	}
	wantIDs := []string{"web", "reported-job", "silent-job"}
	wantSaves := []int{3, 4, 5}
	for i := range enqueues {
		if enqueues[i].transition.CheckID != wantIDs[i] || !enqueues[i].transition.Announce {
			t.Fatalf("enqueue %d transition: %+v", i, enqueues[i].transition)
		}
		if enqueues[i].saves != wantSaves[i] {
			t.Fatalf("enqueue %d observed saves %d, want %d", i, enqueues[i].saves, wantSaves[i])
		}
	}
}

func TestEngineDoesNotEnqueueWhenTransitionPersistenceFails(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	queue := &recordingTransitionQueue{repository: repository}
	engine, err := New(
		[]model.Check{enginePassiveCheck("job")},
		repository,
		&fakeLedger{},
		clock.Now,
		queue,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	repository.setSaveError(errors.New("disk full"))
	_, err = engine.Report(ctx, "job", model.Result{Status: model.StatusFailed})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("report error: %v", err)
	}
	if runErr := <-errCh; runErr == nil || !strings.Contains(runErr.Error(), "disk full") {
		t.Fatalf("run error: %v", runErr)
	}
	if enqueues := queue.recorded(); len(enqueues) != 0 {
		t.Fatalf("failed transition persistence enqueued: %+v", enqueues)
	}
}

func TestShutdownFlushesDirtyState(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusOK, Detail: "healthy"}); err != nil {
		t.Fatal(err)
	}
	_, before := repository.saved()
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	persisted, after := repository.saved()
	if after != before+1 {
		t.Fatalf("shutdown saves: %d, want %d", after, before+1)
	}
	if persisted["web"].Record.LastResult == nil {
		t.Fatal("shutdown did not flush the active result")
	}
}

func TestRuntimeSaveFailureStopsEngine(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	repository.setSaveError(errors.New("disk full"))
	_, err = engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("apply error: %v", err)
	}
	runErr := <-errCh
	if runErr == nil || !strings.Contains(runErr.Error(), "disk full") {
		t.Fatalf("run error: %v", runErr)
	}
	if _, err := engine.ApplyActive(context.Background(), "web", model.Result{Status: model.StatusOK}); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped apply: %v", err)
	}
}

func TestNewFailsOnLoadOrReconciliationSave(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	t.Run("load", func(t *testing.T) {
		_, err := New(nil, &memoryRepository{loadErr: errors.New("corrupt")}, &fakeLedger{}, clock.Now, nil)
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("error: %v", err)
		}
	})
	t.Run("save", func(t *testing.T) {
		_, err := New(nil, &memoryRepository{saveErr: errors.New("read only")}, &fakeLedger{}, clock.Now, nil)
		if err == nil || !strings.Contains(err.Error(), "read only") {
			t.Fatalf("error: %v", err)
		}
	})
}

func TestSnapshotIsADeepRegistryOrderedCopy(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	engine, err := New(
		[]model.Check{enginePassiveCheck("first"), engineActiveCheck("second")},
		&memoryRepository{},
		&fakeLedger{},
		clock.Now,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := engine.Snapshot()
	if snapshot.Checks[0].Check.ID != "first" || snapshot.Checks[1].Check.ID != "second" {
		t.Fatalf("registry order: %+v", snapshot.Checks)
	}
	snapshot.Checks[0].Record.State = model.StateFailed
	again := engine.Snapshot()
	if again.Checks[0].Record.State != model.StateOK {
		t.Fatal("caller mutation changed the published snapshot")
	}
}

func (ledger *fakeLedger) setAppendError(err error) {
	ledger.mu.Lock()
	ledger.appendErr = err
	ledger.mu.Unlock()
}

func requireCalls(t *testing.T, log *callLog, want ...string) {
	t.Helper()
	got := log.recorded()
	if len(got) != len(want) {
		t.Fatalf("calls: %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls: %v, want %v", got, want)
		}
	}
}

func newHistoryEngine(t *testing.T, clock *fakeClock, checks ...model.Check) *Engine {
	t.Helper()
	engine, err := New(checks, &memoryRepository{}, history.NewStore(t.TempDir()), clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func transitionEvents(window history.Window) []history.Event {
	events := make([]history.Event, 0, len(window.Events))
	for _, event := range window.Events {
		if event.Type == history.EventTransition {
			events = append(events, event)
		}
	}
	return events
}

func TestEngineAppendsEveryTransitionAfterPersistingIt(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	log := &callLog{}
	repository := &memoryRepository{log: log}
	ledger := &fakeLedger{log: log}
	engine, err := New(
		[]model.Check{
			engineActiveCheck("web"),
			enginePassiveCheck("reported-job"),
			enginePassiveCheck("silent-job"),
		},
		repository,
		ledger,
		clock.Now,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	// ok -> late is silent for the pager and real for history.
	clock.now = engineEpoch.Add(time.Minute)
	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "one"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(2 * time.Minute)
	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "two"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(3 * time.Minute)
	if _, err := engine.ApplyActive(ctx, "web", model.Result{Status: model.StatusFailed, Detail: "three"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(4 * time.Minute)
	if _, err := engine.Report(ctx, "reported-job", model.Result{Status: model.StatusFailed, Detail: "exit 2"}); err != nil {
		t.Fatal(err)
	}
	clock.now = engineEpoch.Add(33 * time.Hour)
	if _, err := engine.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	requireCalls(t, log,
		"boot",
		"save",
		"save", "append:web",
		"save", "append:web",
		"save", "append:reported-job",
		"save", "append:silent-job",
	)

	appended := ledger.appended()
	if len(appended) != 4 {
		t.Fatalf("appended: %+v", appended)
	}
	if appended[0].From != model.StateOK || appended[0].To != model.StateLate || appended[0].Announce {
		t.Fatalf("silent transition: %+v", appended[0])
	}
	if appended[1].From != model.StateLate || appended[1].To != model.StateFailed {
		t.Fatalf("hardening transition: %+v", appended[1])
	}
	if !appended[0].PreviousSince.Equal(engineEpoch) {
		t.Fatalf("previous since: %v", appended[0].PreviousSince)
	}
}

func TestSweepPersistsOnceThenAppendsEachTransition(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	log := &callLog{}
	repository := &memoryRepository{log: log}
	ledger := &fakeLedger{log: log}
	engine, err := New(
		[]model.Check{enginePassiveCheck("first"), enginePassiveCheck("second")},
		repository,
		ledger,
		clock.Now,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	clock.now = engineEpoch.Add(33 * time.Hour)
	transitions, err := engine.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 2 {
		t.Fatalf("transitions: %+v", transitions)
	}
	requireCalls(t, log, "boot", "save", "save", "append:first", "append:second")
}

func TestLedgerAppendFailureStopsEngine(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	ledger := &fakeLedger{}
	engine, err := New([]model.Check{enginePassiveCheck("job")}, repository, ledger, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	ledger.setAppendError(errors.New("read only"))
	if _, err := engine.Report(ctx, "job", model.Result{Status: model.StatusFailed}); err == nil || !strings.Contains(err.Error(), "read only") {
		t.Fatalf("report error: %v", err)
	}
	if runErr := <-errCh; runErr == nil || !strings.Contains(runErr.Error(), "read only") {
		t.Fatalf("run error: %v", runErr)
	}
}

func TestNewBootsHistoryWithTheLoadedStampBeforePersisting(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	log := &callLog{}
	lastSaved := engineEpoch.Add(-time.Minute)
	repository := &memoryRepository{log: log, loaded: lastSaved}
	ledger := &fakeLedger{log: log}

	if _, err := New([]model.Check{engineActiveCheck("web"), enginePassiveCheck("job")}, repository, ledger, clock.Now, nil); err != nil {
		t.Fatal(err)
	}
	requireCalls(t, log, "boot", "save")

	at, saved, configured := ledger.booted()
	if !at.Equal(engineEpoch) {
		t.Fatalf("boot at: %v, want %v", at, engineEpoch)
	}
	if !saved.Equal(lastSaved) {
		t.Fatalf("boot saved: %v, want %v", saved, lastSaved)
	}
	if len(configured) != 2 {
		t.Fatalf("configured ids: %v", configured)
	}
	for _, id := range []string{"web", "job"} {
		if _, found := configured[id]; !found {
			t.Fatalf("configured ids missing %q: %v", id, configured)
		}
	}
	if !repository.savedStamp().Equal(engineEpoch) {
		t.Fatalf("saved stamp: %v, want %v", repository.savedStamp(), engineEpoch)
	}
}

func TestNewFailsWhenHistoryCannotBoot(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	ledger := &fakeLedger{bootErr: errors.New("segment corrupt")}

	_, err := New([]model.Check{engineActiveCheck("web")}, repository, ledger, clock.Now, nil)
	if err == nil || !strings.Contains(err.Error(), "segment corrupt") {
		t.Fatalf("error: %v", err)
	}
	if _, saves := repository.saved(); saves != 0 {
		t.Fatalf("failed history boot rewrote the state file: %d saves", saves)
	}
}

func TestNewRequiresALedger(t *testing.T) {
	_, err := New(nil, &memoryRepository{}, nil, (&fakeClock{now: engineEpoch}).Now, nil)
	if err == nil || !strings.Contains(err.Error(), "history ledger is required") {
		t.Fatalf("error: %v", err)
	}
}

func TestShutdownAppendsStopAfterTheFinalPersist(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	log := &callLog{}
	repository := &memoryRepository{log: log}
	ledger := &fakeLedger{log: log}
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, ledger, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	stoppedAt := engineEpoch.Add(time.Hour)
	clock.now = stoppedAt
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	requireCalls(t, log, "boot", "save", "save", "stop")
	stops := ledger.stopped()
	if len(stops) != 1 || !stops[0].Equal(stoppedAt) {
		t.Fatalf("stops: %v, want one at %v", stops, stoppedAt)
	}
	if !repository.savedStamp().Equal(stoppedAt) {
		t.Fatalf("final saved stamp: %v, want %v", repository.savedStamp(), stoppedAt)
	}
}

func TestFlushWhenCleanAdvancesTheSavedStamp(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &memoryRepository{}
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	flushedAt := engineEpoch.Add(time.Minute)
	clock.now = flushedAt
	if err := engine.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, saves := repository.saved(); saves != 2 {
		t.Fatalf("saves: %d, want 2", saves)
	}
	if !repository.savedStamp().Equal(flushedAt) {
		t.Fatalf("saved stamp: %v, want %v", repository.savedStamp(), flushedAt)
	}
}

func TestHistoryViewResolvesBoundsInCommand(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	engine := newHistoryEngine(t, clock, engineActiveCheck("web"))
	ctx := startEngine(t, engine)
	clock.now = engineEpoch.Add(time.Hour)
	generated := clock.now
	requestedFrom := engineEpoch.Add(-48 * time.Hour)
	requestedTo := engineEpoch.Add(-time.Hour)

	cases := []struct {
		name     string
		from     *time.Time
		to       *time.Time
		wantFrom time.Time
		wantTo   time.Time
	}{
		{"both omitted", nil, nil, generated.Add(-defaultHistoryWindow), generated},
		{"from only", &requestedFrom, nil, requestedFrom, generated},
		{"to only", nil, &requestedTo, requestedTo.Add(-defaultHistoryWindow), requestedTo},
		{"both supplied", &requestedFrom, &requestedTo, requestedFrom, requestedTo},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			view, err := engine.HistoryView(ctx, test.from, test.to)
			if err != nil {
				t.Fatal(err)
			}
			if !view.From.Equal(test.wantFrom) || !view.To.Equal(test.wantTo) {
				t.Fatalf("bounds: %v..%v, want %v..%v", view.From, view.To, test.wantFrom, test.wantTo)
			}
			if !view.Generated.Equal(generated) {
				t.Fatalf("generated: %v, want %v", view.Generated, generated)
			}
			if len(view.Checks) != 1 || view.Checks[0].Check.ID != "web" {
				t.Fatalf("checks: %+v", view.Checks)
			}
		})
	}
}

func TestHistoryViewRejectsInvalidResolvedBounds(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	engine := newHistoryEngine(t, clock, engineActiveCheck("web"))
	ctx := startEngine(t, engine)
	future := engineEpoch.Add(time.Hour)
	past := engineEpoch.Add(-time.Hour)
	now := engineEpoch

	cases := []struct {
		name string
		from *time.Time
		to   *time.Time
	}{
		{"inverted", &now, &past},
		{"empty", &now, &now},
		{"future from with an omitted to", &future, nil},
		{"future to", &past, &future},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := engine.HistoryView(ctx, test.from, test.to); !errors.Is(err, ErrInvalidHistoryWindow) {
				t.Fatalf("error: %v", err)
			}
		})
	}
	if _, err := engine.HistoryView(ctx, nil, nil); err != nil {
		t.Fatalf("rejected bounds stopped the loop: %v", err)
	}
}

func TestHistoryViewIsOneConsistentCut(t *testing.T) {
	t.Run("a transition before the view is wholly present", func(t *testing.T) {
		clock := &fakeClock{now: engineEpoch}
		engine := newHistoryEngine(t, clock, enginePassiveCheck("job"))
		ctx := startEngine(t, engine)

		clock.now = engineEpoch.Add(time.Hour)
		if _, err := engine.Report(ctx, "job", model.Result{Status: model.StatusFailed, Detail: "exit 2"}); err != nil {
			t.Fatal(err)
		}
		view, err := engine.HistoryView(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		events := transitionEvents(view.Window)
		if len(events) != 1 || events[0].To != model.StateFailed {
			t.Fatalf("events: %+v", events)
		}
		if view.Checks[0].Record.State != model.StateFailed {
			t.Fatalf("records disagree with the ledger: %+v", view.Checks[0].Record)
		}
		if view.Generated.Before(events[0].TS) {
			t.Fatalf("generated %v precedes its own event %v", view.Generated, events[0].TS)
		}
	})

	t.Run("a transition after the view is wholly absent", func(t *testing.T) {
		clock := &fakeClock{now: engineEpoch}
		engine := newHistoryEngine(t, clock, enginePassiveCheck("job"))
		ctx := startEngine(t, engine)

		clock.now = engineEpoch.Add(time.Hour)
		view, err := engine.HistoryView(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Report(ctx, "job", model.Result{Status: model.StatusFailed, Detail: "exit 2"}); err != nil {
			t.Fatal(err)
		}
		if events := transitionEvents(view.Window); len(events) != 0 {
			t.Fatalf("events: %+v", events)
		}
		if view.Checks[0].Record.State != model.StateOK {
			t.Fatalf("records ran ahead of the ledger: %+v", view.Checks[0].Record)
		}
	})
}

func TestHistoryViewRepliesWhenTheLedgerReadFails(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	ledger := &fakeLedger{windowErr: errors.New("segment corrupt")}
	engine, err := New([]model.Check{enginePassiveCheck("job")}, &memoryRepository{}, ledger, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := startEngine(t, engine)

	_, err = engine.HistoryView(ctx, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "segment corrupt") {
		t.Fatalf("error: %v", err)
	}
	if errors.Is(err, ErrInvalidHistoryWindow) {
		t.Fatal("a ledger read failure was reported as a caller error")
	}
	if _, err := engine.Sweep(ctx); err != nil {
		t.Fatalf("the read failure stopped the loop: %v", err)
	}
}
