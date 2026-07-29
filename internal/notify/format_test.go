package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

func testTransition(id string) model.Transition {
	at := time.Date(2026, 7, 28, 9, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	return model.Transition{
		CheckID:       id,
		CheckName:     "NAS nightly snapshot",
		Kind:          model.KindPassive,
		From:          model.StateLate,
		To:            model.StateFailed,
		At:            at,
		PreviousSince: at.Add(-6 * time.Hour),
		Result:        &model.Result{Status: model.StatusFailed, Detail: "snapshot exited 2"},
		Announce:      true,
	}
}

func TestSharedNotificationFormat(t *testing.T) {
	transition := testTransition("nas-snapshot")
	if got, want := Subject(transition), "[scry] nas-snapshot: late → failed"; got != want {
		t.Fatalf("subject: %q, want %q", got, want)
	}
	message := Message(transition)
	for _, want := range []string{
		Subject(transition),
		"name: NAS nightly snapshot",
		"id: nas-snapshot",
		"state: late → failed",
		"time in previous state: 6h0m0s",
		"detail: snapshot exited 2",
		"timestamp: 2026-07-28T09:30:00-04:00",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

func TestNotificationFormatHandlesMissingDetail(t *testing.T) {
	transition := testTransition("job")
	transition.Result = nil
	if !strings.Contains(Message(transition), "detail: (none)") {
		t.Fatalf("message: %s", Message(transition))
	}
}
