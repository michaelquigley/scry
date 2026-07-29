package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

type notifierFunc func(context.Context, model.Transition) error

func (notify notifierFunc) Notify(ctx context.Context, transition model.Transition) error {
	return notify(ctx, transition)
}

func startDispatcher(t *testing.T, dispatcher *Dispatcher) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- dispatcher.Run(ctx)
	}()
	return cancel, errCh
}

func stopDispatcher(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("dispatcher run: %v", err)
	}
}

func TestNewDispatcherValidatesDestinations(t *testing.T) {
	valid := Destination{Name: "one", Notifier: notifierFunc(func(context.Context, model.Transition) error { return nil })}
	tests := []struct {
		name         string
		destinations []Destination
		want         string
	}{
		{name: "missing name", destinations: []Destination{{Notifier: valid.Notifier}}, want: "name is required"},
		{name: "missing notifier", destinations: []Destination{{Name: "one"}}, want: "implementation is required"},
		{name: "duplicate name", destinations: []Destination{valid, valid}, want: "duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDispatcher(test.destinations)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v, want %q", err, test.want)
			}
		})
	}
}

func TestDispatcherUsesOneIndependentFIFOPerNotifier(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDeliveries := make(chan string, 2)
	secondDeliveries := make(chan string, 2)
	first := notifierFunc(func(_ context.Context, transition model.Transition) error {
		if transition.CheckID == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		firstDeliveries <- transition.CheckID
		return nil
	})
	second := notifierFunc(func(_ context.Context, transition model.Transition) error {
		secondDeliveries <- transition.CheckID
		return nil
	})
	dispatcher, err := NewDispatcher([]Destination{
		{Name: "first", Notifier: first},
		{Name: "second", Notifier: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel, runErr := startDispatcher(t, dispatcher)
	defer stopDispatcher(t, cancel, runErr)

	dispatcher.Enqueue(testTransition("first"))
	dispatcher.Enqueue(testTransition("second"))
	<-firstStarted
	if got := <-secondDeliveries; got != "first" {
		t.Fatalf("second notifier first delivery: %q", got)
	}
	if got := <-secondDeliveries; got != "second" {
		t.Fatalf("second notifier second delivery: %q", got)
	}
	close(releaseFirst)
	if got := <-firstDeliveries; got != "first" {
		t.Fatalf("first notifier first delivery: %q", got)
	}
	if got := <-firstDeliveries; got != "second" {
		t.Fatalf("first notifier second delivery: %q", got)
	}
}

func TestDispatcherRetriesFiveTimesThenAdvances(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	delivered := make(chan struct{})
	notifier := notifierFunc(func(_ context.Context, transition model.Transition) error {
		if transition.CheckID == "stalled" {
			mu.Lock()
			attempts++
			mu.Unlock()
			return errors.New("temporary")
		}
		close(delivered)
		return nil
	})
	dispatcher, err := NewDispatcher([]Destination{{Name: "test", Notifier: notifier}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.backoffs = []time.Duration{0, 0, 0, 0}
	dispatcher.wait = func(context.Context, time.Duration) bool { return true }
	dispatcher.Enqueue(testTransition("stalled"))
	dispatcher.Enqueue(testTransition("next"))
	cancel, runErr := startDispatcher(t, dispatcher)

	<-delivered
	mu.Lock()
	gotAttempts := attempts
	mu.Unlock()
	if gotAttempts != 5 {
		t.Fatalf("attempts: %d, want 5", gotAttempts)
	}
	stopDispatcher(t, cancel, runErr)
}

func TestDispatcherRetriesThenSucceedsWithoutReordering(t *testing.T) {
	var attempts int
	deliveries := make(chan string, 2)
	notifier := notifierFunc(func(_ context.Context, transition model.Transition) error {
		if transition.CheckID == "first" && attempts < 2 {
			attempts++
			return errors.New("temporary")
		}
		deliveries <- transition.CheckID
		return nil
	})
	dispatcher, err := NewDispatcher([]Destination{{Name: "test", Notifier: notifier}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.backoffs = []time.Duration{0, 0, 0, 0}
	dispatcher.wait = func(context.Context, time.Duration) bool { return true }
	dispatcher.Enqueue(testTransition("first"))
	dispatcher.Enqueue(testTransition("second"))
	cancel, runErr := startDispatcher(t, dispatcher)

	if got := <-deliveries; got != "first" {
		t.Fatalf("first delivery: %q", got)
	}
	if got := <-deliveries; got != "second" {
		t.Fatalf("second delivery: %q", got)
	}
	if attempts != 2 {
		t.Fatalf("retries: %d", attempts)
	}
	stopDispatcher(t, cancel, runErr)
}

func TestDispatcherOwnsIndependentTransitionCopies(t *testing.T) {
	detail := "original"
	transition := testTransition("job")
	transition.Result = &model.Result{Status: model.StatusFailed, Detail: detail}
	got := make(chan string, 1)
	dispatcher, err := NewDispatcher([]Destination{{
		Name: "test",
		Notifier: notifierFunc(func(_ context.Context, transition model.Transition) error {
			got <- transition.Result.Detail
			return nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.Enqueue(transition)
	transition.Result.Detail = "mutated"
	cancel, runErr := startDispatcher(t, dispatcher)
	if delivered := <-got; delivered != detail {
		t.Fatalf("detail: %q", delivered)
	}
	stopDispatcher(t, cancel, runErr)
}
