package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

// Subject returns the shared notification subject and first line.
func Subject(transition model.Transition) string {
	return fmt.Sprintf(
		"[scry] %s: %s → %s",
		transition.CheckID,
		transition.From,
		transition.To,
	)
}

// Message returns the shared human-readable notification body.
func Message(transition model.Transition) string {
	detail := "(none)"
	if transition.Result != nil && strings.TrimSpace(transition.Result.Detail) != "" {
		detail = transition.Result.Detail
	}
	return fmt.Sprintf(
		"%s\n\n- name: %s\n- id: %s\n- state: %s → %s\n- time in previous state: %s\n- detail: %s\n- timestamp: %s",
		Subject(transition),
		transition.CheckName,
		transition.CheckID,
		transition.From,
		transition.To,
		transition.PreviousDuration().Round(time.Second),
		detail,
		transition.At.Format(time.RFC3339),
	)
}
