package model

import (
	"testing"
	"time"
)

var modelEpoch = time.Date(2026, 7, 27, 12, 0, 0, 0, time.Local)

func activeCheck() Check {
	return Check{
		ID:        "web",
		Name:      "web",
		Kind:      KindHTTP,
		FailAfter: 3,
	}
}

func passiveCheck() Check {
	return Check{
		ID:          "job",
		Name:        "job",
		Kind:        KindPassive,
		Period:      24 * time.Hour,
		Grace:       2 * time.Hour,
		HardenAfter: 3,
	}
}

func requireTransition(t *testing.T, change Change, from, to State, announce bool) Transition {
	t.Helper()
	if change.Transition == nil {
		t.Fatal("expected a transition")
	}
	transition := *change.Transition
	if transition.From != from || transition.To != to {
		t.Fatalf("transition: %s -> %s, want %s -> %s", transition.From, transition.To, from, to)
	}
	if transition.Announce != announce {
		t.Fatalf("announce: %v, want %v", transition.Announce, announce)
	}
	return transition
}

func TestNewRecordCompleteness(t *testing.T) {
	active := NewRecord(activeCheck(), modelEpoch)
	if active.State != StateOK || !active.Since.Equal(modelEpoch) {
		t.Fatalf("active baseline: %+v", active)
	}
	if active.LastTransition != nil || active.LastSeen != nil || active.LastResult != nil || active.ConsecutiveFails != 0 {
		t.Fatalf("active baseline contains synthetic history: %+v", active)
	}

	passive := NewRecord(passiveCheck(), modelEpoch)
	if passive.State != StateOK || !passive.Since.Equal(modelEpoch) {
		t.Fatalf("passive baseline: %+v", passive)
	}
	if passive.LastSeen == nil || !passive.LastSeen.Equal(modelEpoch) {
		t.Fatalf("passive last seen: %v", passive.LastSeen)
	}
	if passive.LastTransition != nil || passive.LastResult != nil || passive.ConsecutiveFails != 0 {
		t.Fatalf("passive baseline contains synthetic history: %+v", passive)
	}
}

func TestPassiveWindowBoundaries(t *testing.T) {
	check := passiveCheck()
	record := NewRecord(check, modelEpoch)
	lateAt := modelEpoch.Add(26 * time.Hour)
	failedAt := modelEpoch.Add(32 * time.Hour)

	cases := []struct {
		name string
		at   time.Time
		want State
	}{
		{"before late", lateAt.Add(-time.Nanosecond), StateOK},
		{"at late", lateAt, StateOK},
		{"after late", lateAt.Add(time.Nanosecond), StateLate},
		{"at failed", failedAt, StateLate},
		{"after failed", failedAt.Add(time.Nanosecond), StateFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := PassiveWindowState(record, test.at, check.Period, check.Grace, check.HardenAfter)
			if got != test.want {
				t.Fatalf("state: %s, want %s", got, test.want)
			}
		})
	}
}

func TestActiveDampingAndPairedAnnouncements(t *testing.T) {
	check := activeCheck()
	record := NewRecord(check, modelEpoch)
	failed := Result{Status: StatusFailed, Detail: "connection refused"}

	first, err := ApplyActive(check, record, failed, modelEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, first, StateOK, StateLate, false)
	if first.Record.ConsecutiveFails != 1 {
		t.Fatalf("first failure count: %d", first.Record.ConsecutiveFails)
	}

	second, err := ApplyActive(check, first.Record, failed, modelEpoch.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.Transition != nil || second.Record.State != StateLate || second.Record.ConsecutiveFails != 2 {
		t.Fatalf("second failure: %+v", second)
	}

	third, err := ApplyActive(check, second.Record, failed, modelEpoch.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, third, StateLate, StateFailed, true)

	stickyCheck := check
	stickyCheck.FailAfter = 5
	sticky, err := ApplyActive(stickyCheck, third.Record, failed, modelEpoch.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sticky.Transition != nil || sticky.Record.State != StateFailed {
		t.Fatalf("raised threshold softened failure: %+v", sticky)
	}

	recovery, err := ApplyActive(stickyCheck, sticky.Record, Result{Status: StatusOK}, modelEpoch.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, recovery, StateFailed, StateOK, true)
	if recovery.Record.ConsecutiveFails != 0 {
		t.Fatalf("recovery count: %d", recovery.Record.ConsecutiveFails)
	}
}

func TestActiveThresholdUsesGreaterThanOrEqual(t *testing.T) {
	check := activeCheck()
	record := NewRecord(check, modelEpoch)
	record.State = StateLate
	record.ConsecutiveFails = 4

	change, err := ApplyActive(check, record, Result{Status: StatusFailed}, modelEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, change, StateLate, StateFailed, true)
}

func TestActiveLateRecoveryIsSilent(t *testing.T) {
	check := activeCheck()
	record := NewRecord(check, modelEpoch)
	record.State = StateLate
	record.ConsecutiveFails = 1

	change, err := ApplyActive(check, record, Result{Status: StatusOK}, modelEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, change, StateLate, StateOK, false)
}

func TestPassiveReportsAreDefinitive(t *testing.T) {
	check := passiveCheck()
	record := NewRecord(check, modelEpoch)
	failureAt := modelEpoch.Add(time.Hour)

	failure, err := ApplyPassiveReport(check, record, Result{Status: StatusFailed, Detail: "exit 2"}, failureAt)
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, failure, StateOK, StateFailed, true)
	if failure.Record.LastSeen == nil || !failure.Record.LastSeen.Equal(failureAt) {
		t.Fatalf("failed report did not check in: %v", failure.Record.LastSeen)
	}

	held, err := ApplyPassiveWindow(check, failure.Record, failureAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if held.Transition != nil || held.Record.State != StateFailed {
		t.Fatalf("fresh window softened explicit failure: %+v", held)
	}
	if held.Record.LastResult == nil || held.Record.LastResult.Detail != "exit 2" {
		t.Fatalf("sweep changed last result: %+v", held.Record.LastResult)
	}

	recoveryAt := modelEpoch.Add(2 * time.Hour)
	recovery, err := ApplyPassiveReport(check, held.Record, Result{Status: StatusOK, Detail: "complete"}, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, recovery, StateFailed, StateOK, true)
}

func TestPassiveSweepOnlyDegradesAndCanCatchUp(t *testing.T) {
	check := passiveCheck()
	record := NewRecord(check, modelEpoch)
	result := Result{Status: StatusOK, Detail: "last real report"}
	record.LastResult = &result

	catchup, err := ApplyPassiveWindow(check, record, modelEpoch.Add(33*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	requireTransition(t, catchup, StateOK, StateFailed, true)
	if catchup.Record.LastSeen == nil || !catchup.Record.LastSeen.Equal(modelEpoch) {
		t.Fatalf("sweep changed last seen: %v", catchup.Record.LastSeen)
	}
	if catchup.Record.LastResult == nil || *catchup.Record.LastResult != result {
		t.Fatalf("sweep changed last result: %+v", catchup.Record.LastResult)
	}

	again, err := ApplyPassiveWindow(check, catchup.Record, modelEpoch.Add(34*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Transition != nil || again.Record.State != StateFailed {
		t.Fatalf("repeat sweep re-fired: %+v", again)
	}
}

func TestNotificationPairingRule(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		from State
		to   State
		want bool
	}{
		{"passive late", KindPassive, StateOK, StateLate, true},
		{"active late", KindHTTP, StateOK, StateLate, false},
		{"passive failed", KindPassive, StateLate, StateFailed, true},
		{"active failed", KindTCP, StateLate, StateFailed, true},
		{"passive late recovery", KindPassive, StateLate, StateOK, true},
		{"active late recovery", KindHTTP, StateLate, StateOK, false},
		{"passive failed recovery", KindPassive, StateFailed, StateOK, true},
		{"active failed recovery", KindTCP, StateFailed, StateOK, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldAnnounce(test.kind, test.from, test.to); got != test.want {
				t.Fatalf("announce: %v, want %v", got, test.want)
			}
		})
	}
}
