package notify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

func writeFakeSendmail(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sendmail")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func sendmailTransition() model.Transition {
	return model.Transition{
		CheckID:       "nas-snapshot",
		CheckName:     "NAS nightly snapshot",
		Kind:          model.KindPassive,
		From:          model.StateLate,
		To:            model.StateFailed,
		At:            time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		PreviousSince: time.Date(2026, 8, 6, 6, 0, 0, 0, time.UTC),
		Result:        &model.Result{Status: model.StatusFailed, Detail: "no report received"},
		Announce:      true,
	}
}

func TestSendmailDeliversSharedMessage(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "captured")
	arguments := filepath.Join(t.TempDir(), "arguments")
	path := writeFakeSendmail(t, fmt.Sprintf(`cat > %q
printf '%%s\n' "$@" > %q`, captured, arguments))

	notifier, err := NewSendmail(path, "scry <scry@example.com>", []string{"one@example.com", "two@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sendmailTransition()); err != nil {
		t.Fatal(err)
	}

	argv, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	want := "-i\n-f\nscry@example.com\none@example.com\ntwo@example.com\n"
	if string(argv) != want {
		t.Fatalf("argv:\n%q\nwant:\n%q", argv, want)
	}

	message, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	text := string(message)
	for _, expected := range []string{
		"From: \"scry\" <scry@example.com>\n",
		"To: <one@example.com>, <two@example.com>\n",
		"Subject: =?utf-8?q?[scry]_nas-snapshot:_late_=E2=86=92_failed?=\n",
		"Content-Type: text/plain; charset=UTF-8\n",
		"\n\n[scry] nas-snapshot: late",
		"- detail: no report received\n",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("message missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "\r\n") {
		t.Fatal("sendmail message must use local line endings")
	}
}

func TestSendmailNonZeroExitCarriesDetail(t *testing.T) {
	path := writeFakeSendmail(t, `echo "deferred: local queue unavailable" >&2
exit 75`)
	notifier, err := NewSendmail(path, "scry@example.com", []string{"one@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), sendmailTransition())
	if err == nil || !strings.Contains(err.Error(), "local queue unavailable") {
		t.Fatalf("error: %v", err)
	}
}

func TestSendmailStalledBinaryHonorsContextDeadline(t *testing.T) {
	path := writeFakeSendmail(t, `sleep 30`)
	notifier, err := NewSendmail(path, "scry@example.com", []string{"one@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	err = notifier.Notify(ctx, sendmailTransition())
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stalled sendmail held the attempt for %s", elapsed)
	}
}

func TestSendmailCanceledContextDoesNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	path := writeFakeSendmail(t, fmt.Sprintf("touch %q", marker))
	notifier, err := NewSendmail(path, "scry@example.com", []string{"one@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := notifier.Notify(ctx, sendmailTransition()); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("binary ran despite canceled context")
	}
}

func TestNewSendmailRejectsInvalidConfiguration(t *testing.T) {
	valid := writeFakeSendmail(t, "exit 0")
	cases := []struct {
		name string
		path string
		from string
		to   []string
		want string
	}{
		{"missing binary", filepath.Join(t.TempDir(), "absent"), "scry@example.com", []string{"one@example.com"}, "stat sendmail binary"},
		{"unparseable from", valid, "not an address", []string{"one@example.com"}, "from address"},
		{"no recipients", valid, "scry@example.com", nil, "recipients are required"},
		{"unparseable recipient", valid, "scry@example.com", []string{"also not one"}, "recipient 0"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSendmail(test.path, test.from, test.to)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestNewSendmailRejectsNonExecutable(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"no execute bits", 0o644},
		{"execute bit for an inapplicable class only", 0o601},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sendmail")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := NewSendmail(path, "scry@example.com", []string{"one@example.com"})
			if err == nil || !strings.Contains(err.Error(), "not executable by scry") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestNewSendmailAppliesDefaultPath(t *testing.T) {
	_, err := NewSendmail("", "scry@example.com", []string{"one@example.com"})
	if err != nil && !strings.Contains(err.Error(), DefaultSendmailPath) {
		t.Fatalf("default path not applied: %v", err)
	}
}
