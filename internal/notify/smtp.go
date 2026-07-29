package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

var canceledDeadline = time.Unix(1, 0)

// SMTP delivers transitions through an unauthenticated house relay.
type SMTP struct {
	address      string
	host         string
	dialer       net.Dialer
	tlsConfig    *tls.Config
	fromHeader   string
	toHeader     string
	envelopeTo   []string
	envelopeFrom string
}

// NewSMTP validates addresses and returns a house-relay notifier.
func NewSMTP(host string, port int, from string, to []string) (*SMTP, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("smtp host is required")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("smtp port must be between 1 and 65535")
	}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("parse smtp from address: %w", err)
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("smtp recipients are required")
	}

	envelopeTo := make([]string, len(to))
	toHeaders := make([]string, len(to))
	for i, value := range to {
		recipient, err := mail.ParseAddress(value)
		if err != nil {
			return nil, fmt.Errorf("parse smtp recipient %d: %w", i, err)
		}
		envelopeTo[i] = recipient.Address
		toHeaders[i] = recipient.String()
	}

	return &SMTP{
		address:      net.JoinHostPort(host, strconv.Itoa(port)),
		host:         host,
		tlsConfig:    &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		fromHeader:   fromAddress.String(),
		toHeader:     strings.Join(toHeaders, ", "),
		envelopeFrom: fromAddress.Address,
		envelopeTo:   envelopeTo,
	}, nil
}

// Notify sends one transition, upgrading with STARTTLS when the relay offers it.
func (notifier *SMTP) Notify(ctx context.Context, transition model.Transition) error {
	connection, err := notifier.dialer.DialContext(ctx, "tcp", notifier.address)
	if err != nil {
		return fmt.Errorf("dial smtp relay: %w", err)
	}
	defer connection.Close()
	if deadline, found := ctx.Deadline(); found {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set smtp deadline: %w", err)
		}
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(canceledDeadline)
	})
	defer stopCancellation()

	client, err := smtp.NewClient(connection, notifier.host)
	if err != nil {
		return fmt.Errorf("start smtp client: %w", err)
	}
	defer client.Close()
	if offered, _ := client.Extension("STARTTLS"); offered {
		if err := client.StartTLS(notifier.tlsConfig.Clone()); err != nil {
			return fmt.Errorf("start smtp tls: %w", err)
		}
	}
	if err := client.Mail(notifier.envelopeFrom); err != nil {
		return fmt.Errorf("set smtp sender: %w", err)
	}
	for _, recipient := range notifier.envelopeTo {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("set smtp recipient %q: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start smtp data: %w", err)
	}
	if _, err := writer.Write(notifier.message(transition)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write smtp message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish smtp data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit smtp session: %w", err)
	}
	return nil
}

func (notifier *SMTP) message(transition model.Transition) []byte {
	body := strings.ReplaceAll(Message(transition), "\n", "\r\n")
	subject := mime.QEncoding.Encode("utf-8", Subject(transition))
	return []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n",
		notifier.fromHeader,
		notifier.toHeader,
		subject,
		transition.At.Format(time.RFC1123Z),
		body,
	))
}
