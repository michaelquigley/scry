package model

import (
	"fmt"
	"time"
)

// ApplyActive applies one active-probe result with damping and sticky failure.
func ApplyActive(check Check, current Record, result Result, at time.Time) (Change, error) {
	if !check.Kind.Active() {
		return Change{}, fmt.Errorf("check %q is not active", check.ID)
	}
	if !result.Status.Valid() {
		return Change{}, fmt.Errorf("check %q: invalid result status %q", check.ID, result.Status)
	}

	next := current.Clone()
	stored := result
	next.LastResult = &stored

	switch result.Status {
	case StatusOK:
		next.ConsecutiveFails = 0
		next.State = StateOK
	case StatusFailed:
		next.ConsecutiveFails++
		switch {
		case current.State == StateFailed:
			next.State = StateFailed
		case next.ConsecutiveFails >= check.FailAfter:
			next.State = StateFailed
		default:
			next.State = StateLate
		}
	}

	if next.State != current.State {
		return transition(check, current, next, at), nil
	}
	return Change{Record: next, Dirty: true}, nil
}

// ApplyPassiveReport applies one definitive passive check-in.
func ApplyPassiveReport(check Check, current Record, result Result, at time.Time) (Change, error) {
	if check.Kind != KindPassive {
		return Change{}, fmt.Errorf("check %q is not passive", check.ID)
	}
	if !result.Status.Valid() {
		return Change{}, fmt.Errorf("check %q: invalid result status %q", check.ID, result.Status)
	}

	next := current.Clone()
	seen := at
	stored := result
	next.LastSeen = &seen
	next.LastResult = &stored
	next.ConsecutiveFails = 0
	if result.Status == StatusFailed {
		next.State = StateFailed
	} else {
		next.State = StateOK
	}

	if next.State != current.State {
		return transition(check, current, next, at), nil
	}
	return Change{Record: next, Dirty: true}, nil
}

// PassiveWindowState derives only the current silence verdict.
func PassiveWindowState(record Record, at time.Time, period, grace time.Duration, hardenAfter int) State {
	if record.LastSeen == nil {
		return StateFailed
	}
	lateAt := record.LastSeen.Add(period + grace)
	failedAt := lateAt.Add(time.Duration(hardenAfter) * grace)
	switch {
	case at.After(failedAt):
		return StateFailed
	case at.After(lateAt):
		return StateLate
	default:
		return StateOK
	}
}

// ApplyPassiveWindow degrades a passive record without touching its last report.
func ApplyPassiveWindow(check Check, current Record, at time.Time) (Change, error) {
	if check.Kind != KindPassive {
		return Change{}, fmt.Errorf("check %q is not passive", check.ID)
	}
	window := PassiveWindowState(current, at, check.Period, check.Grace, check.HardenAfter)
	if window.severity() <= current.State.severity() {
		return Change{Record: current.Clone()}, nil
	}
	next := current.Clone()
	next.State = window
	return transition(check, current, next, at), nil
}

// ShouldAnnounce implements the paired notification rule.
func ShouldAnnounce(kind Kind, from, to State) bool {
	if from == to {
		return false
	}
	if to == StateFailed {
		return true
	}
	if to == StateLate {
		return kind == KindPassive
	}
	if to != StateOK {
		return false
	}
	if from == StateFailed {
		return true
	}
	return from == StateLate && kind == KindPassive
}
