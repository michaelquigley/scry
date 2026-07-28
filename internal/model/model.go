// Package model contains scry's transport-free domain types and state rules.
package model

import (
	"fmt"
	"time"
)

// Kind identifies how a check receives results.
type Kind string

const (
	KindPassive Kind = "passive"
	KindHTTP    Kind = "http"
	KindTCP     Kind = "tcp"
)

// Valid reports whether kind is part of the v1 model.
func (kind Kind) Valid() bool {
	switch kind {
	case KindPassive, KindHTTP, KindTCP:
		return true
	default:
		return false
	}
}

// Active reports whether kind evaluates through a CheckStrategy.
func (kind Kind) Active() bool {
	return kind == KindHTTP || kind == KindTCP
}

// Status is the binary result produced by a probe or report.
type Status string

const (
	StatusOK     Status = "ok"
	StatusFailed Status = "failed"
)

// Valid reports whether status is part of the v1 result contract.
func (status Status) Valid() bool {
	return status == StatusOK || status == StatusFailed
}

// State is the damped runtime verdict for a check.
type State string

const (
	StateOK     State = "ok"
	StateLate   State = "late"
	StateFailed State = "failed"
)

// Valid reports whether state is part of the v1 state machine.
func (state State) Valid() bool {
	switch state {
	case StateOK, StateLate, StateFailed:
		return true
	default:
		return false
	}
}

func (state State) severity() int {
	switch state {
	case StateOK:
		return 0
	case StateLate:
		return 1
	case StateFailed:
		return 2
	default:
		return -1
	}
}

// Result is the opaque payload carried from a strategy or report through state.
type Result struct {
	Status Status
	Detail string
}

// Check carries the state-machine fields for one configured entity.
type Check struct {
	ID          string
	Name        string
	Kind        Kind
	FailAfter   int
	Period      time.Duration
	Grace       time.Duration
	HardenAfter int
}

// Validate checks the model-level invariants required by transition logic.
func (check Check) Validate() error {
	if check.ID == "" {
		return fmt.Errorf("check id is required")
	}
	if check.Name == "" {
		return fmt.Errorf("check %q: name is required", check.ID)
	}
	if !check.Kind.Valid() {
		return fmt.Errorf("check %q: invalid kind %q", check.ID, check.Kind)
	}
	if check.Kind.Active() && check.FailAfter < 2 {
		return fmt.Errorf("check %q: fail_after must be at least 2", check.ID)
	}
	if check.Kind == KindPassive {
		if check.Period <= 0 {
			return fmt.Errorf("check %q: period must be positive", check.ID)
		}
		if check.Grace <= 0 {
			return fmt.Errorf("check %q: grace must be positive", check.ID)
		}
		if check.HardenAfter < 1 {
			return fmt.Errorf("check %q: harden_after must be at least 1", check.ID)
		}
	}
	return nil
}

// Record is the complete persisted runtime state for one check.
type Record struct {
	State            State
	Since            time.Time
	LastTransition   *time.Time
	LastSeen         *time.Time
	LastResult       *Result
	ConsecutiveFails int
}

// NewRecord returns a complete registration baseline with no synthetic transition.
func NewRecord(check Check, at time.Time) Record {
	record := Record{
		State: StateOK,
		Since: at,
	}
	if check.Kind == KindPassive {
		seen := at
		record.LastSeen = &seen
	}
	return record
}

// Clone returns a deep copy safe to hand to a reader.
func (record Record) Clone() Record {
	clone := record
	if record.LastTransition != nil {
		value := *record.LastTransition
		clone.LastTransition = &value
	}
	if record.LastSeen != nil {
		value := *record.LastSeen
		clone.LastSeen = &value
	}
	if record.LastResult != nil {
		value := *record.LastResult
		clone.LastResult = &value
	}
	return clone
}

// CheckRecord pairs immutable registry identity with its runtime record.
type CheckRecord struct {
	Check  Check
	Record Record
}

// Transition describes one real state change after registration.
type Transition struct {
	CheckID       string
	CheckName     string
	Kind          Kind
	From          State
	To            State
	At            time.Time
	PreviousSince time.Time
	Result        *Result
	Announce      bool
}

// PreviousDuration reports how long the check occupied the state being left.
func (transition Transition) PreviousDuration() time.Duration {
	return transition.At.Sub(transition.PreviousSince)
}

// Change is the pure result of applying one input to a record.
type Change struct {
	Record     Record
	Transition *Transition
	Dirty      bool
}

func transition(check Check, current Record, next Record, at time.Time) Change {
	entered := at
	next.Since = at
	next.LastTransition = &entered
	change := Transition{
		CheckID:       check.ID,
		CheckName:     check.Name,
		Kind:          check.Kind,
		From:          current.State,
		To:            next.State,
		At:            at,
		PreviousSince: current.Since,
		Announce:      ShouldAnnounce(check.Kind, current.State, next.State),
	}
	if next.LastResult != nil {
		result := *next.LastResult
		change.Result = &result
	}
	return Change{Record: next, Transition: &change, Dirty: true}
}
