package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/michaelquigley/scry/internal/model"
	"github.com/michaelquigley/scry/internal/strategy"
)

const (
	passiveSweepInterval = 15 * time.Second
	// every tick persists, dirty or not: the periodic save is what keeps the
	// state file's saved stamp a live bound on the daemon's death, which is
	// what bounds the unwatched gap history opens after an unclean stop.
	// making it conditional again would widen every crash gap to the last
	// transition.
	livenessFlushInterval = 60 * time.Second
)

// Jitter chooses one check's initial offset within its interval.
type Jitter func(time.Duration) time.Duration

// ActiveCheck pairs scheduling policy with one active strategy.
type ActiveCheck struct {
	ID       string
	Interval time.Duration
	Timeout  time.Duration
	Strategy strategy.CheckStrategy
}

type scheduledCheck struct {
	ActiveCheck
	initialOffset time.Duration
}

type probeResult struct {
	checkID string
	result  model.Result
}

// Scheduler runs active checks and submits all scheduled work to one Engine.
type Scheduler struct {
	engine *Engine
	checks []scheduledCheck
}

// RandomJitter chooses a uniformly distributed initial offset.
func RandomJitter(interval time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(interval)))
}

// NewScheduler validates active checks and fixes their initial offsets.
func NewScheduler(engine *Engine, checks []ActiveCheck, jitter Jitter) (*Scheduler, error) {
	if engine == nil {
		return nil, fmt.Errorf("engine is required")
	}
	if jitter == nil {
		return nil, fmt.Errorf("jitter is required")
	}

	scheduler := &Scheduler{
		engine: engine,
		checks: make([]scheduledCheck, len(checks)),
	}
	ids := make(map[string]struct{}, len(checks))
	for i, check := range checks {
		if check.ID == "" {
			return nil, fmt.Errorf("active check %d: id is required", i)
		}
		if _, found := ids[check.ID]; found {
			return nil, fmt.Errorf("duplicate active check id %q", check.ID)
		}
		if check.Interval <= 0 {
			return nil, fmt.Errorf("active check %q: interval must be positive", check.ID)
		}
		if check.Timeout <= 0 {
			return nil, fmt.Errorf("active check %q: timeout must be positive", check.ID)
		}
		if check.Strategy == nil {
			return nil, fmt.Errorf("active check %q: strategy is required", check.ID)
		}

		offset := jitter(check.Interval)
		if offset < 0 || offset >= check.Interval {
			return nil, fmt.Errorf(
				"active check %q: initial offset %s is outside [0, %s)",
				check.ID,
				offset,
				check.Interval,
			)
		}
		ids[check.ID] = struct{}{}
		scheduler.checks[i] = scheduledCheck{
			ActiveCheck:   check,
			initialOffset: offset,
		}
	}
	return scheduler, nil
}

// Run schedules probes, passive sweeps, and unconditional state flushes until
// ctx ends.
func (scheduler *Scheduler) Run(ctx context.Context) error {
	workersCtx, cancelWorkers := context.WithCancel(ctx)
	results := make(chan probeResult)
	var workers sync.WaitGroup
	for _, check := range scheduler.checks {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runActiveCheck(workersCtx, check, results)
		}()
	}

	sweepTicker := time.NewTicker(passiveSweepInterval)
	flushTicker := time.NewTicker(livenessFlushInterval)
	defer sweepTicker.Stop()
	defer flushTicker.Stop()
	defer workers.Wait()
	defer cancelWorkers()

	for {
		select {
		case <-ctx.Done():
			return nil
		case probe := <-results:
			if _, err := scheduler.engine.ApplyActive(ctx, probe.checkID, probe.result); err != nil {
				if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ErrStopped)) {
					return nil
				}
				return fmt.Errorf("apply active result for check %q: %w", probe.checkID, err)
			}
		case <-sweepTicker.C:
			if _, err := scheduler.engine.Sweep(ctx); err != nil {
				if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ErrStopped)) {
					return nil
				}
				return fmt.Errorf("sweep passive checks: %w", err)
			}
		case <-flushTicker.C:
			if err := scheduler.engine.Flush(ctx); err != nil {
				if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ErrStopped)) {
					return nil
				}
				return fmt.Errorf("flush active state: %w", err)
			}
		}
	}
}

func runActiveCheck(ctx context.Context, check scheduledCheck, results chan<- probeResult) {
	initial := time.NewTimer(check.initialOffset)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}

	ticker := time.NewTicker(check.Interval)
	defer ticker.Stop()
	for {
		result, deliver := evaluateProbe(ctx, check.Timeout, check.Strategy)
		if deliver {
			select {
			case results <- probeResult{checkID: check.ID, result: result}:
			case <-ctx.Done():
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func evaluateProbe(parent context.Context, timeout time.Duration, check strategy.CheckStrategy) (model.Result, bool) {
	probeCtx, cancel := context.WithTimeout(parent, timeout)
	result := check.Evaluate(probeCtx)
	probeErr := probeCtx.Err()
	cancel()

	if parent.Err() != nil {
		return model.Result{}, false
	}
	if errors.Is(probeErr, context.DeadlineExceeded) {
		return model.Result{
			Status: model.StatusFailed,
			Detail: "probe deadline exceeded",
		}, true
	}
	return result, true
}
