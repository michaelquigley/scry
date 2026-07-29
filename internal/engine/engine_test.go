package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

type memoryRepository struct {
	mu       sync.Mutex
	snapshot state.Snapshot
	loadErr  error
	saveErr  error
	saves    int
}

func (repository *memoryRepository) Load() (state.Snapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.loadErr != nil {
		return nil, repository.loadErr
	}
	return repository.snapshot.Clone(), nil
}

func (repository *memoryRepository) Save(snapshot state.Snapshot) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.saveErr != nil {
		return repository.saveErr
	}
	repository.snapshot = snapshot.Clone()
	repository.saves++
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

	engine, err := New(checks, repository, clock.Now, nil)
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
	restarted, err := New(checks, repository, clock.Now, nil)
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

	first, err := New([]model.Check{check}, repository, clock.Now, nil)
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

	restarted, err := New([]model.Check{check}, repository, clock.Now, nil)
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
	first, err := New([]model.Check{check}, repository, clock.Now, nil)
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

	restarted, err := New([]model.Check{check}, repository, clock.Now, nil)
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
			engine, err := New([]model.Check{check}, repository, clock.Now, nil)
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
	engine, err := New([]model.Check{enginePassiveCheck("job")}, repository, clock.Now, nil)
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
	engine, err := New([]model.Check{enginePassiveCheck("job")}, repository, clock.Now, nil)
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
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, clock.Now, nil)
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
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, clock.Now, nil)
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
		_, err := New(nil, &memoryRepository{loadErr: errors.New("corrupt")}, clock.Now, nil)
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("error: %v", err)
		}
	})
	t.Run("save", func(t *testing.T) {
		_, err := New(nil, &memoryRepository{saveErr: errors.New("read only")}, clock.Now, nil)
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
