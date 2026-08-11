package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/state"
)

type strategyFunc func(context.Context) model.Result

func (evaluate strategyFunc) Evaluate(ctx context.Context) model.Result {
	return evaluate(ctx)
}

type schedulerRepository struct {
	memoryRepository
	saved chan struct{}
}

func (repository *schedulerRepository) Save(snapshot state.Snapshot, at time.Time) error {
	if err := repository.memoryRepository.Save(snapshot, at); err != nil {
		return err
	}
	repository.saved <- struct{}{}
	return nil
}

func TestNewSchedulerValidatesChecksAndJitter(t *testing.T) {
	engine, err := New(nil, &memoryRepository{}, &fakeLedger{}, (&fakeClock{now: engineEpoch}).Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := ActiveCheck{
		ID:       "web",
		Interval: time.Minute,
		Timeout:  time.Second,
		Strategy: strategyFunc(func(context.Context) model.Result {
			return model.Result{Status: model.StatusOK}
		}),
	}

	tests := []struct {
		name   string
		checks []ActiveCheck
		jitter Jitter
		want   string
	}{
		{name: "missing jitter", checks: []ActiveCheck{valid}, want: "jitter is required"},
		{name: "missing id", checks: []ActiveCheck{{Interval: time.Minute, Timeout: time.Second, Strategy: valid.Strategy}}, jitter: func(time.Duration) time.Duration { return 0 }, want: "id is required"},
		{name: "duplicate id", checks: []ActiveCheck{valid, valid}, jitter: func(time.Duration) time.Duration { return 0 }, want: "duplicate"},
		{name: "zero interval", checks: []ActiveCheck{{ID: "web", Timeout: time.Second, Strategy: valid.Strategy}}, jitter: func(time.Duration) time.Duration { return 0 }, want: "interval must be positive"},
		{name: "zero timeout", checks: []ActiveCheck{{ID: "web", Interval: time.Minute, Strategy: valid.Strategy}}, jitter: func(time.Duration) time.Duration { return 0 }, want: "timeout must be positive"},
		{name: "missing strategy", checks: []ActiveCheck{{ID: "web", Interval: time.Minute, Timeout: time.Second}}, jitter: func(time.Duration) time.Duration { return 0 }, want: "strategy is required"},
		{name: "negative jitter", checks: []ActiveCheck{valid}, jitter: func(time.Duration) time.Duration { return -1 }, want: "outside"},
		{name: "interval jitter", checks: []ActiveCheck{valid}, jitter: func(interval time.Duration) time.Duration { return interval }, want: "outside"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jitter := test.jitter
			if jitter == nil && test.want != "jitter is required" {
				jitter = func(time.Duration) time.Duration { return 0 }
			}
			_, err := NewScheduler(engine, test.checks, jitter)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvaluateProbeDiscardsParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	finished := make(chan struct{})
	var (
		result  model.Result
		deliver bool
	)
	go func() {
		result, deliver = evaluateProbe(parent, time.Minute, strategyFunc(func(ctx context.Context) model.Result {
			close(started)
			<-ctx.Done()
			return model.Result{Status: model.StatusFailed, Detail: ctx.Err().Error()}
		}))
		close(finished)
	}()

	<-started
	cancel()
	<-finished
	if deliver {
		t.Fatalf("canceled probe delivered %+v", result)
	}
}

func TestEvaluateProbeTurnsLiveDeadlineIntoFailure(t *testing.T) {
	result, deliver := evaluateProbe(context.Background(), time.Nanosecond, strategyFunc(func(ctx context.Context) model.Result {
		<-ctx.Done()
		return model.Result{Status: model.StatusOK}
	}))

	if !deliver {
		t.Fatal("live probe deadline was discarded")
	}
	if result.Status != model.StatusFailed || result.Detail != "probe deadline exceeded" {
		t.Fatalf("deadline result: %+v", result)
	}
}

func TestSchedulerDeliversInitialProbeThroughEngine(t *testing.T) {
	clock := &fakeClock{now: engineEpoch}
	repository := &schedulerRepository{saved: make(chan struct{}, 2)}
	engine, err := New([]model.Check{engineActiveCheck("web")}, repository, &fakeLedger{}, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-repository.saved
	probed := make(chan struct{})
	scheduler, err := NewScheduler(engine, []ActiveCheck{{
		ID:       "web",
		Interval: time.Hour,
		Timeout:  time.Minute,
		Strategy: strategyFunc(func(context.Context) model.Result {
			close(probed)
			return model.Result{Status: model.StatusFailed, Detail: "down"}
		}),
	}}, func(time.Duration) time.Duration { return 0 })
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	engineErr := make(chan error, 1)
	schedulerErr := make(chan error, 1)
	go func() {
		engineErr <- engine.Run(ctx)
	}()
	go func() {
		schedulerErr <- scheduler.Run(ctx)
	}()

	<-probed
	<-repository.saved

	cancel()
	if err := <-schedulerErr; err != nil {
		t.Fatalf("scheduler run: %v", err)
	}
	if err := <-engineErr; err != nil {
		t.Fatalf("engine run: %v", err)
	}

	record := snapshotRecord(t, engine.Snapshot(), "web")
	if record.State != model.StateLate || record.ConsecutiveFails != 1 {
		t.Fatalf("scheduled record: %+v", record)
	}
	if record.LastResult == nil || record.LastResult.Detail != "down" {
		t.Fatalf("scheduled result: %+v", record.LastResult)
	}
}
