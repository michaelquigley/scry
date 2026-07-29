// Package notify owns transition formatting, delivery, and retry.
package notify

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/scry/internal/model"
)

const defaultAttemptTimeout = 30 * time.Second

var defaultBackoffs = []time.Duration{
	15 * time.Second,
	time.Minute,
	3 * time.Minute,
	6 * time.Minute,
}

// Notifier delivers one transition and honors ctx cancellation.
type Notifier interface {
	Notify(context.Context, model.Transition) error
}

// Destination names one configured delivery transport.
type Destination struct {
	Name     string
	Notifier Notifier
}

type deliveryQueue struct {
	destination Destination
	mu          sync.Mutex
	items       []model.Transition
	wake        chan struct{}
}

// Dispatcher fans announced transitions into one unbounded FIFO per notifier.
type Dispatcher struct {
	queues         []*deliveryQueue
	attemptTimeout time.Duration
	backoffs       []time.Duration
	wait           func(context.Context, time.Duration) bool
	started        atomic.Bool
}

// NewDispatcher validates destinations and returns a runnable dispatcher.
func NewDispatcher(destinations []Destination) (*Dispatcher, error) {
	dispatcher := &Dispatcher{
		queues:         make([]*deliveryQueue, len(destinations)),
		attemptTimeout: defaultAttemptTimeout,
		backoffs:       append([]time.Duration(nil), defaultBackoffs...),
		wait:           waitFor,
	}
	names := make(map[string]struct{}, len(destinations))
	for i, destination := range destinations {
		if destination.Name == "" {
			return nil, fmt.Errorf("notifier destination %d: name is required", i)
		}
		if destination.Notifier == nil {
			return nil, fmt.Errorf("notifier destination %q: implementation is required", destination.Name)
		}
		if _, found := names[destination.Name]; found {
			return nil, fmt.Errorf("duplicate notifier destination %q", destination.Name)
		}
		names[destination.Name] = struct{}{}
		dispatcher.queues[i] = &deliveryQueue{
			destination: destination,
			wake:        make(chan struct{}, 1),
		}
	}
	return dispatcher, nil
}

// Enqueue appends one independent transition copy to every notifier FIFO.
func (dispatcher *Dispatcher) Enqueue(transition model.Transition) {
	for _, queue := range dispatcher.queues {
		queue.enqueue(cloneTransition(transition))
	}
}

// Run drains each notifier queue until ctx ends.
func (dispatcher *Dispatcher) Run(ctx context.Context) error {
	if !dispatcher.started.CompareAndSwap(false, true) {
		return fmt.Errorf("notifier dispatcher already started")
	}

	var workers sync.WaitGroup
	for _, queue := range dispatcher.queues {
		workers.Add(1)
		go func() {
			defer workers.Done()
			dispatcher.runQueue(ctx, queue)
		}()
	}
	<-ctx.Done()
	workers.Wait()
	return nil
}

func (dispatcher *Dispatcher) runQueue(ctx context.Context, queue *deliveryQueue) {
	for {
		transition, found := queue.pop()
		if !found {
			select {
			case <-ctx.Done():
				return
			case <-queue.wake:
				continue
			}
		}
		if !dispatcher.deliver(ctx, queue.destination, transition) {
			return
		}
	}
}

func (dispatcher *Dispatcher) deliver(ctx context.Context, destination Destination, transition model.Transition) bool {
	attempts := len(dispatcher.backoffs) + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, dispatcher.attemptTimeout)
		err := destination.Notifier.Notify(attemptCtx, transition)
		if err == nil && attemptCtx.Err() != nil {
			err = attemptCtx.Err()
		}
		cancel()
		if err == nil {
			return true
		}
		lastErr = err
		if ctx.Err() != nil {
			return false
		}
		if attempt == attempts {
			break
		}
		dl.Warnf(
			"notifier delivery failed; notifier='%s' check='%s' attempt='%d' error='%v'; retrying",
			destination.Name,
			transition.CheckID,
			attempt,
			err,
		)
		if !dispatcher.wait(ctx, dispatcher.backoffs[attempt-1]) {
			return false
		}
	}
	dl.Errorf(
		"notifier delivery dropped; notifier='%s' check='%s' attempts='%d' error='%v'",
		destination.Name,
		transition.CheckID,
		attempts,
		lastErr,
	)
	return true
}

func (queue *deliveryQueue) enqueue(transition model.Transition) {
	queue.mu.Lock()
	queue.items = append(queue.items, transition)
	queue.mu.Unlock()
	select {
	case queue.wake <- struct{}{}:
	default:
	}
}

func (queue *deliveryQueue) pop() (model.Transition, bool) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.items) == 0 {
		return model.Transition{}, false
	}
	transition := queue.items[0]
	queue.items[0] = model.Transition{}
	queue.items = queue.items[1:]
	return transition, true
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func cloneTransition(transition model.Transition) model.Transition {
	clone := transition
	if transition.Result != nil {
		result := *transition.Result
		clone.Result = &result
	}
	return clone
}
