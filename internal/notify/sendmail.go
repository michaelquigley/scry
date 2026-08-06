package notify

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/mail"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

// DefaultSendmailPath is used when the sendmail block omits an explicit path.
const DefaultSendmailPath = "/usr/bin/sendmail"

const maxSendmailDetailBytes = 512

// Sendmail delivers transitions through the host MTA's sendmail binary. the
// MTA owns queueing and retry beyond the dispatcher's own attempts, and no
// credential ever appears in scry's configuration.
type Sendmail struct {
	path         string
	fromHeader   string
	toHeader     string
	envelopeFrom string
	envelopeTo   []string
}

// NewSendmail validates addresses, applies the default path, and verifies the
// binary exists and is executable — a missing MTA is an operator error and
// dies at boot, not at first delivery.
func NewSendmail(path, from string, to []string) (*Sendmail, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultSendmailPath
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("stat sendmail binary %q: %w", path, err)
	}
	// LookPath on an absolute path performs a real execute-access check for
	// this process, not a mere mode-bit inspection — a binary the daemon
	// cannot actually run must fail at boot, not during delivery retries.
	if _, err := exec.LookPath(path); err != nil {
		return nil, fmt.Errorf("sendmail binary %q is not executable by scry: %w", path, err)
	}

	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("parse sendmail from address: %w", err)
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("sendmail recipients are required")
	}
	envelopeTo := make([]string, len(to))
	toHeaders := make([]string, len(to))
	for i, value := range to {
		recipient, err := mail.ParseAddress(value)
		if err != nil {
			return nil, fmt.Errorf("parse sendmail recipient %d: %w", i, err)
		}
		envelopeTo[i] = recipient.Address
		toHeaders[i] = recipient.String()
	}

	return &Sendmail{
		path:         path,
		fromHeader:   fromAddress.String(),
		toHeader:     strings.Join(toHeaders, ", "),
		envelopeFrom: fromAddress.Address,
		envelopeTo:   envelopeTo,
	}, nil
}

// Notify hands one message to the sendmail binary under the attempt context.
func (notifier *Sendmail) Notify(ctx context.Context, transition model.Transition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	arguments := append([]string{"-i", "-f", notifier.envelopeFrom}, notifier.envelopeTo...)
	command := exec.CommandContext(ctx, notifier.path, arguments...)
	command.Stdin = bytes.NewReader(notifier.message(transition))
	// a killed sendmail can leave forked children (postfix's postdrop) holding
	// the output pipes; without WaitDelay, Wait blocks on those orphans and one
	// stalled MTA holds the attempt past its deadline.
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > maxSendmailDetailBytes {
			detail = detail[:maxSendmailDetailBytes]
		}
		if detail != "" {
			return fmt.Errorf("run sendmail: %w: %s", err, detail)
		}
		return fmt.Errorf("run sendmail: %w", err)
	}
	return nil
}

// message renders the shared notification with local line endings; the MTA
// owns wire formatting.
func (notifier *Sendmail) message(transition model.Transition) []byte {
	subject := mime.QEncoding.Encode("utf-8", Subject(transition))
	return []byte(fmt.Sprintf(
		"From: %s\nTo: %s\nSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: 8bit\n\n%s\n",
		notifier.fromHeader,
		notifier.toHeader,
		subject,
		transition.At.Format(time.RFC1123Z),
		Message(transition),
	))
}
